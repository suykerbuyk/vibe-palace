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

// DrawerID generates a deterministic ID: first 8 hex chars of md5(wing+content).
// Room is excluded so drawer identity is stable across reclassification.
func DrawerID(wing, content string) string {
	h := md5.Sum([]byte(wing + content))
	return hex.EncodeToString(h[:])[:8]
}

// AppendDrawer appends a drawer to the JSONL file for the given room.
// It generates a deterministic ID and rejects duplicates.
func (v *Vault) AppendDrawer(project, wing, room string, d Drawer) error {
	if err := validateSlugs(project, wing, room); err != nil {
		return err
	}

	d.ID = DrawerID(wing, d.Content)

	path, err := v.DrawerFile(project, wing, room)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure drawer dir: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock drawer file: %w", err)
	}
	defer release()

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read drawer file: %w", err)
	}

	// Check for duplicate ID.
	scanner := bufio.NewScanner(bytes.NewReader(existing))
	for scanner.Scan() {
		var cur Drawer
		if err := json.Unmarshal(scanner.Bytes(), &cur); err != nil {
			continue
		}
		if cur.ID == d.ID {
			return fmt.Errorf("drawer %q already exists in %s/%s", d.ID, wing, room)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan drawer file: %w", err)
	}

	line, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("marshal drawer: %w", err)
	}

	var buf bytes.Buffer
	buf.Write(existing)
	buf.Write(line)
	buf.WriteByte('\n')
	if err := atomicfile.Write(v.Root, path, buf.Bytes()); err != nil {
		return fmt.Errorf("write drawer: %w", err)
	}
	return nil
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

// ListDrawersByWing returns all drawers across all rooms in a wing.
func (v *Vault) ListDrawersByWing(project, wing string) ([]Drawer, error) {
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

	var all []Drawer
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		room := e.Name()
		drawers, err := v.readDrawerFile(project, wing, room)
		if err != nil {
			return nil, err
		}
		all = append(all, drawers...)
	}
	return all, nil
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
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var d Drawer
		if err := json.Unmarshal(scanner.Bytes(), &d); err != nil {
			return nil, fmt.Errorf("parse drawer line: %w", err)
		}
		drawers = append(drawers, d)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan drawer file: %w", err)
	}
	return drawers, nil
}
