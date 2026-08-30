// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// Drawer represents a verbatim content chunk stored in the palace.
type Drawer struct {
	ID         string `json:"id"`
	Hall       string `json:"hall"`
	Content    string `json:"content"`
	SourceType string `json:"source_type"`
	SourceRef  string `json:"source_ref,omitempty"`
	ChunkIndex int    `json:"chunk_index,omitempty"`
	FiledAt    string `json:"filed_at"`
	AddedBy    string `json:"added_by,omitempty"`
}

// maxDrawerLine caps a single JSONL record for both the dedup scan and the
// reader. bufio.Scanner's 64 KB default is well above a chunker-sized drawer
// (max_chars 800), but a torn append can concatenate two records into one
// oversized line, and the default turns that into a scan ERROR that loses the
// whole room — the exact failure the tolerant reader exists to prevent. The
// ceiling stays finite so a corrupt file cannot drive an unbounded allocation.
const maxDrawerLine = 1 << 20

// DrawerID generates a deterministic ID: first 8 hex chars of md5(wing+content).
// Room is excluded so drawer identity is stable across reclassification.
func DrawerID(wing, content string) string {
	h := md5.Sum([]byte(wing + content))
	return hex.EncodeToString(h[:])[:8]
}

// AppendDrawer appends a drawer to the JSONL file for the given room.
// It generates a deterministic ID and rejects duplicates.
//
// It is the n=1 wrapper over AppendDrawers, and it exists for the callers whose
// contract is the ERROR rather than the count: MoveDrawer treats "already
// exists" as the signal that the copy half of its append-before-delete is
// already satisfied, and the MCP drawer tools report it to an operator who
// asked to file one specific drawer. AppendDrawers deliberately does not
// error on a duplicate, because its caller is a bulk ingest for which a
// duplicate is the normal case, not a failure.
func (v *Vault) AppendDrawer(project, wing, room string, d Drawer) error {
	n, err := v.AppendDrawers(project, wing, room, []Drawer{d})
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("drawer %q already exists in %s/%s", DrawerID(wing, d.Content), wing, room)
	}
	return nil
}

// AppendDrawers appends every drawer in ds that is not already filed in the
// room, and returns how many it actually appended. Duplicates — against what is
// on disk OR against an earlier entry in ds — are skipped silently.
//
// # Why this is the batch shape and not a loop over AppendDrawer
//
// The per-drawer entry point costs O(drawers already in the room): it reads the
// whole room JSONL and unmarshals every line to check for a duplicate ID, then
// used to rewrite the whole file to add one line. N appends therefore cost
// O(N²) in both directions, and MEASURED on this project's `general` room
// (19 MB, 20,056 drawers) that read-and-scan alone was 33 ms per drawer — so a
// backfill of the ~600k chunks sitting in unindexed archives could not finish
// in any timeout. This entry point pays the scan ONCE per (room, batch) and
// appends the new lines in ONE write, which removes the quadratic term without
// a sidecar index file, a persistent ID cache, or any new state to keep
// coherent. The `seen` set below lives for the duration of one call and is
// discarded with it; that is the whole mechanism.
//
// # The write is an append, not a whole-file replace
//
// It routes to appendUnderLock (family F4) rather than atomicfile.Write. The
// lock is acquired HERE and held across the read→dedup→append sequence, which
// is exactly the contract F4 documents: it does not acquire, and a second
// acquire on the same path would block forever. Do not move the acquire into
// the primitive, and do not call it from a caller that is not already holding.
func (v *Vault) AppendDrawers(project, wing, room string, ds []Drawer) (int, error) {
	if err := validateSlugs(project, wing, room); err != nil {
		return 0, err
	}
	if len(ds) == 0 {
		return 0, nil
	}

	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		return 0, err
	}
	// appendUnderLock opens with O_CREATE but does not create parent
	// directories the way atomicfile.Write does, so the room directory is
	// still this caller's job.
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return 0, fmt.Errorf("ensure drawer dir: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return 0, fmt.Errorf("lock drawer file: %w", err)
	}
	defer release()

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return 0, fmt.Errorf("read drawer file: %w", err)
	}

	// One scan of the room, not one per drawer. A line that does not parse is
	// skipped rather than failing the append: it contributes no ID to dedup
	// against, which is the same thing the per-line `continue` did before, and
	// readDrawerFile tolerates it on the way back out.
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(existing))
	scanner.Buffer(make([]byte, 0, 64*1024), maxDrawerLine)
	for scanner.Scan() {
		var cur Drawer
		if err := json.Unmarshal(scanner.Bytes(), &cur); err != nil {
			continue
		}
		seen[cur.ID] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan drawer file: %w", err)
	}

	var buf bytes.Buffer
	appended := 0
	for _, d := range ds {
		d.ID = DrawerID(wing, d.Content)
		if _, dup := seen[d.ID]; dup {
			continue
		}
		seen[d.ID] = struct{}{}
		line, err := json.Marshal(d)
		if err != nil {
			return 0, fmt.Errorf("marshal drawer: %w", err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
		appended++
	}

	// Every drawer in the batch was already filed. Write nothing at all: this
	// is what makes a re-run of an already-ingested archive cost one read
	// rather than one read plus a whole-file rewrite.
	if appended == 0 {
		return 0, nil
	}

	// 🔴 Heal a missing final newline before appending onto it. Every writer
	// here terminates its lines, so a file that does not end in '\n' ends in a
	// TORN record — the failure mode an append primitive has and a whole-file
	// replace does not. Appending straight onto it would concatenate the torn
	// bytes with the first new record and lose BOTH: the torn line is already
	// unreadable, and the good record following it becomes unreadable too.
	// Separating them costs one byte and confines the damage to the record that
	// was actually torn.
	out := buf.Bytes()
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		out = append([]byte{'\n'}, out...)
	}

	if err := v.appendUnderLock(path, out); err != nil {
		return 0, fmt.Errorf("write drawer: %w", err)
	}
	return appended, nil
}

// GetDrawer retrieves a single drawer by ID from the given room.
func (v *Vault) GetDrawer(project, wing, room, id string) (Drawer, error) {
	drawers, err := v.readDrawerFile(project, wing, room)
	if err != nil {
		return Drawer{}, err
	}
	for _, d := range drawers {
		if d.ID == id {
			return d, nil
		}
	}
	return Drawer{}, fmt.Errorf("drawer %q not found in %s/%s", id, wing, room)
}

// ListDrawers returns all drawers in a room.
func (v *Vault) ListDrawers(project, wing, room string) ([]Drawer, error) {
	return v.readDrawerFile(project, wing, room)
}

// DeleteDrawer removes a drawer by ID, rewriting the JSONL file without it.
// Uses atomic temp-file + rename to prevent data loss on crash.
func (v *Vault) DeleteDrawer(project, wing, room, id string) error {
	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		return err
	}

	// Hold the per-path lock across the full read→rewrite so this RMW
	// interlocks with concurrent AppendDrawer/DeleteDrawer on the same file.
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock drawer file: %w", err)
	}
	defer release()

	f, err := os.OpenFile(path, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open drawer file: %w", err)
	}
	defer f.Close()

	var kept [][]byte
	found := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		var d Drawer
		if err := json.Unmarshal(line, &d); err == nil && d.ID == id {
			found = true
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		kept = append(kept, cp)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan drawer file: %w", err)
	}
	if !found {
		return fmt.Errorf("drawer %q not found in %s/%s", id, wing, room)
	}

	// Atomic write via the shared primitive (temp + rename + surface stamp).
	var buf bytes.Buffer
	for _, line := range kept {
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := atomicfile.Write(v.Root, path, buf.Bytes()); err != nil {
		return fmt.Errorf("rewrite drawer file: %w", err)
	}
	return nil
}

// MoveDrawer atomically moves a drawer from one room to another within the
// same wing. Uses append-before-delete ordering to prevent data loss: if a
// crash occurs after append but before delete, the drawer exists in both rooms
// (recoverable by the next audit run) rather than being lost.
func (v *Vault) MoveDrawer(project, wing, fromRoom, toRoom, id string) error {
	if err := validateSlugs(project, wing, fromRoom); err != nil {
		return err
	}
	if err := validateSlugs(project, wing, toRoom); err != nil {
		return err
	}
	if fromRoom == toRoom {
		return nil
	}

	d, err := v.GetDrawer(project, wing, fromRoom, id)
	if err != nil {
		return fmt.Errorf("read source drawer: %w", err)
	}

	if err := v.AppendDrawer(project, wing, toRoom, d); err != nil {
		return fmt.Errorf("append to %s: %w", toRoom, err)
	}

	if err := v.DeleteDrawer(project, wing, fromRoom, id); err != nil {
		slog.Error("MoveDrawer: delete from source failed after successful append",
			"project", project, "wing", wing, "from", fromRoom, "to", toRoom,
			"drawer", id, "err", err)
		return fmt.Errorf("delete from %s (drawer already copied to %s): %w", fromRoom, toRoom, err)
	}

	return nil
}

// ListWings returns all wing slugs for a project by reading the drawers directory.
// HasPalaceStore reports whether palace/<project>/drawers exists — i.e.
// whether this project has ever had a drawer store materialized.
//
// It exists because ListWings deliberately CANNOT answer this: it returns
// (nil, nil) both for "no store at all" and for "a store with no wings", which
// is right for a walk and wrong for a caller that must tell an operator whether
// there was anything to refresh. Callers that need the distinction ask here
// FIRST — a rebuild can create palace/<project>/ as a side effect of indexing
// the iterations or session-note corpus, so asking afterwards answers a
// different question.
func (v *Vault) HasPalaceStore(project string) (bool, error) {
	if err := slug.Validate(project); err != nil {
		return false, fmt.Errorf("project: %w", err)
	}
	info, err := os.Stat(filepath.Join(v.Root, "palace", project, "drawers"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat drawers dir: %w", err)
	}
	return info.IsDir(), nil
}

func (v *Vault) ListWings(project string) ([]string, error) {
	if err := slug.Validate(project); err != nil {
		return nil, fmt.Errorf("project: %w", err)
	}

	drawersDir := filepath.Join(v.Root, "palace", project, "drawers")
	entries, err := os.ReadDir(drawersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read drawers dir: %w", err)
	}

	var wings []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if slug.Validate(e.Name()) == nil {
			wings = append(wings, e.Name())
		}
	}
	return wings, nil
}

// ListRooms returns all room slugs for a wing by reading the wing directory.
func (v *Vault) ListRooms(project, wing string) ([]string, error) {
	if err := validateSlugs(project, wing); err != nil {
		return nil, err
	}

	wingDir := filepath.Join(v.Root, "palace", project, "drawers", wing)
	entries, err := os.ReadDir(wingDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read wing dir: %w", err)
	}

	var rooms []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if slug.Validate(e.Name()) == nil {
			rooms = append(rooms, e.Name())
		}
	}
	return rooms, nil
}

// ListProjects returns the project slugs that have a palace/ store — the
// drawer + knowledge-graph tree. It is what search wants, because search indexes
// drawers and drawers live there.
//
// It is NOT an enumeration of the vault. A project's sessions live under
// Projects/<slug>/, and many projects appear in one tree and not the other, so
// this returns a SUBSET and cannot tell you it did. Any caller asking "what is
// in this vault?" wants ListAllProjects (projects.go), which returns the union
// of both trees and records which one each project came from.
func (v *Vault) ListProjects() ([]string, error) {
	projects, err := listProjectDirs(filepath.Join(v.Root, "palace"))
	if err != nil {
		return nil, fmt.Errorf("read palace dir: %w", err)
	}
	return projects, nil
}

// readDrawerFile reads and parses all drawers from a room's JSONL file.
//
// 🔴 A LINE THAT DOES NOT PARSE IS SKIPPED AND WARNED, NOT RETURNED AS AN
// ERROR. This is not laxity, and it is a prerequisite of appending through F4
// rather than through the whole-file replace: atomicfile.Write renames a
// complete file into place, so a crash left the previous file intact, whereas
// an O_APPEND write can leave a torn final line. Hard-erroring on that line
// made ONE bad line lose the WHOLE room — ListDrawers and GetDrawer fail, and
// Engine.Rebuild skips every drawer in it — which is a far worse outcome than
// dropping the torn record. It also makes this reader agree with the two
// writers that already tolerate a bad line: AppendDrawers skips it when
// building its dedup set, and DeleteDrawer drops it when rewriting.
//
// The Warn is the whole reporting channel for corruption here, so it names the
// room and the line number: silently skipping would trade a loud wrong
// behaviour for a quiet one.
func (v *Vault) readDrawerFile(project, wing, room string) ([]Drawer, error) {
	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open drawer file: %w", err)
	}
	defer f.Close()

	var drawers []Drawer
	lineNo := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxDrawerLine)
	for scanner.Scan() {
		lineNo++
		var d Drawer
		if err := json.Unmarshal(scanner.Bytes(), &d); err != nil {
			slog.Warn("skipping malformed drawer line",
				"project", project, "wing", wing, "room", room,
				"line", lineNo, "err", err)
			continue
		}
		drawers = append(drawers, d)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan drawer file: %w", err)
	}
	return drawers, nil
}
