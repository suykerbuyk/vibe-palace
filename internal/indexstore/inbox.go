// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// inboxFile holds the source_sha256 of archives that triggers named as First
// but could not serve themselves: the index run lock was held
// (pending-archive-ingester-and-per-archive-commit-step, plan revision R3).
// One sha per line, under index/<p>/; it is rewritten whole and atomically.
//
// The inbox also carries at most one reserved non-sha line, rebuildMarker: a
// generic "a corpus rebuild is wanted" coalesce record (ADR-016 decision 5),
// written by a cold/stale search that degraded (indexstore.NoteRebuild) and
// cleared only after the detached `vp index rebuild` commits and bumps the
// generation (ClearRebuild). It carries exactly one meaning — a corpus-rebuild
// want — and never an archive sha nor a convert request.
const inboxFile = "first.inbox"

// rebuildMarker is the reserved inbox line for a corpus-rebuild want. It is
// deliberately not a 64-hex string, so shaPattern can never match it and no sha
// can ever collide with it: the sha readers drop it, and the marker readers key
// on it exactly.
const rebuildMarker = "rebuild-corpus"

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// NoteFirst records that a trigger created archive sha of project and found
// the index run lock held. The holder reads the inbox (Tx.Firsts) before it
// creates the project's ledger, so the archive is never left in the baseline
// set as backlog, and treats every inbox entry as in scope for its automatic
// run. It takes the project's commit lock for timeout; a sha already in the
// inbox is not added twice. ErrProjectGone when the project is gone.
func NoteFirst(ctx context.Context, vault *storage.Vault, project, sha string, timeout time.Duration) error {
	if !shaPattern.MatchString(sha) {
		return fmt.Errorf("indexstore: %q is not a source_sha256", sha)
	}
	tx, err := Lock(ctx, vault, project, timeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	have, err := tx.Firsts()
	if err != nil {
		return err
	}
	// writeInbox sorts and de-duplicates, so a sha already there is kept once.
	if err := tx.writeInbox(append(have, sha)); err != nil {
		return err
	}
	return tx.Commit()
}

// inboxContents is the parsed inbox: the archive shas, sorted and de-duplicated,
// and whether the reserved rebuild-corpus marker line is present. The two are
// kept apart so every sha reader stays sha-only (plan, ensureLedger, tidyProject
// never see the marker as an archive) while the marker readers see only it.
type inboxContents struct {
	shas    []string
	rebuild bool
}

// readInboxContents parses the inbox: sha lines into shas, the one reserved
// rebuildMarker line into rebuild, every other line dropped. A missing file is
// the zero value, not an error — the file is only ever replaced whole.
func readInboxContents(path string) (inboxContents, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return inboxContents{}, nil
	}
	if err != nil {
		return inboxContents{}, fmt.Errorf("indexstore: read %s: %w", path, err)
	}
	var c inboxContents
	for _, l := range strings.Split(string(data), "\n") {
		switch l = strings.TrimSpace(l); {
		case l == rebuildMarker:
			c.rebuild = true
		case shaPattern.MatchString(l):
			c.shas = append(c.shas, l)
		}
	}
	slices.Sort(c.shas)
	c.shas = slices.Compact(c.shas)
	return c, nil
}

// writeInboxContents replaces the inbox with exactly c: the marker line first (if
// wanted), then the sorted shas. An inbox with no marker and no sha is removed,
// so an emptied inbox leaves no file.
func writeInboxContents(path, dir string, c inboxContents) error {
	slices.Sort(c.shas)
	c.shas = slices.Compact(c.shas)
	if !c.rebuild && len(c.shas) == 0 {
		return removeIfExists(path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("indexstore: create %s: %w", dir, err)
	}
	var b strings.Builder
	if c.rebuild {
		b.WriteString(rebuildMarker)
		b.WriteByte('\n')
	}
	for _, s := range c.shas {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return writeFile(path, []byte(b.String()))
}

// Firsts returns the project's inbox archive shas: archives triggers named and
// could not serve, sorted. The rebuild-corpus marker is not a sha and is never
// returned here; RebuildWanted reads it.
func (tx *Tx) Firsts() ([]string, error) { return readInbox(tx.inboxPath()) }

// RebuildWanted reports whether the project's inbox carries the generic
// rebuild-corpus marker (ADR-016 decision 5).
func (tx *Tx) RebuildWanted() (bool, error) {
	c, err := readInboxContents(tx.inboxPath())
	return c.rebuild, err
}

func readInbox(path string) ([]string, error) {
	c, err := readInboxContents(path)
	return c.shas, err
}

// setRebuild sets or clears the rebuild-corpus marker, preserving the inbox's
// shas. Under the project's commit lock, like every other inbox mutation.
func (tx *Tx) setRebuild(want bool) error {
	c, err := readInboxContents(tx.inboxPath())
	if err != nil {
		return err
	}
	c.rebuild = want
	return writeInboxContents(tx.inboxPath(), tx.files.dir, c)
}

// DropFirsts removes served entries from the inbox: archives now ledgered,
// superseded, or no longer on disk. An emptied inbox is removed.
func (tx *Tx) DropFirsts(shas []string) error {
	if len(shas) == 0 {
		return nil
	}
	have, err := tx.Firsts()
	if err != nil {
		return err
	}
	keep := slices.DeleteFunc(have, func(s string) bool { return slices.Contains(shas, s) })
	return tx.writeInbox(keep)
}

func (tx *Tx) inboxPath() string { return filepath.Join(tx.files.dir, inboxFile) }

// writeInbox replaces the inbox's shas, PRESERVING the rebuild-corpus marker:
// NoteFirst and DropFirsts both route through here, and neither may drop a
// marker a cold search recorded (that would lose the corpus-rebuild want). An
// inbox left with no shas and no marker is removed.
func (tx *Tx) writeInbox(shas []string) error {
	cur, err := readInboxContents(tx.inboxPath())
	if err != nil {
		return err
	}
	return writeInboxContents(tx.inboxPath(), tx.files.dir, inboxContents{shas: shas, rebuild: cur.rebuild})
}

// ReadFirsts reads project's inbox shas without a lock, for the run's
// post-release recheck: the file is only ever replaced whole.
func ReadFirsts(vault *storage.Vault, project string) ([]string, error) {
	pf, err := filesFor(vault, project)
	if err != nil {
		return nil, err
	}
	return readInbox(filepath.Join(pf.dir, inboxFile))
}

// ReadRebuildWanted reports whether project's inbox carries the rebuild-corpus
// marker, read without a lock (like ReadFirsts): the file is only ever replaced
// whole, so a reader never sees a torn value. A missing inbox reads as false.
func ReadRebuildWanted(vault *storage.Vault, project string) (bool, error) {
	pf, err := filesFor(vault, project)
	if err != nil {
		return false, err
	}
	c, err := readInboxContents(filepath.Join(pf.dir, inboxFile))
	return c.rebuild, err
}

// NoteRebuild records a generic corpus-rebuild want in project's inbox (ADR-016
// decision 5): a cold/stale search that degraded and could not build the corpus
// inline writes it, so the want is durable while the detached `vp index rebuild`
// catches up. It is idempotent — the marker is a single boolean line — and it
// NEVER try-locks the index run lock; it takes only the project's commit lock,
// like NoteFirst. ErrProjectGone when the project is gone.
func NoteRebuild(ctx context.Context, vault *storage.Vault, project string, timeout time.Duration) error {
	tx, err := Lock(ctx, vault, project, timeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	if err := tx.setRebuild(true); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearRebuild removes the rebuild-corpus marker from project's inbox, keeping
// every archive sha. It is the new clear site (ADR-016 decision 5): the detached
// `vp index rebuild` calls it only AFTER its engine rebuild commits and bumps the
// generation, so a want is never cleared before it is served. Idempotent.
func ClearRebuild(ctx context.Context, vault *storage.Vault, project string, timeout time.Duration) error {
	tx, err := Lock(ctx, vault, project, timeout)
	if err != nil {
		return err
	}
	defer tx.Release()
	if err := tx.setRebuild(false); err != nil {
		return err
	}
	return tx.Commit()
}
