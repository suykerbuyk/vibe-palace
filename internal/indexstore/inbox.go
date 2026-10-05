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
const inboxFile = "first.inbox"

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

// Firsts returns the project's inbox: archives triggers named and could not
// serve, sorted.
func (tx *Tx) Firsts() ([]string, error) { return readInbox(tx.inboxPath()) }

func readInbox(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("indexstore: read %s: %w", path, err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); shaPattern.MatchString(l) {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
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

// writeInbox replaces the inbox with shas, or removes it when there are none.
func (tx *Tx) writeInbox(shas []string) error {
	if len(shas) == 0 {
		return removeIfExists(tx.inboxPath())
	}
	slices.Sort(shas)
	shas = slices.Compact(shas)
	if err := os.MkdirAll(tx.files.dir, 0o755); err != nil {
		return fmt.Errorf("indexstore: create %s: %w", tx.files.dir, err)
	}
	return writeFile(tx.inboxPath(), []byte(strings.Join(shas, "\n")+"\n"))
}

// ReadFirsts reads project's inbox without a lock, for the run's post-release
// recheck: the file is only ever replaced whole.
func ReadFirsts(vault *storage.Vault, project string) ([]string, error) {
	pf, err := filesFor(vault, project)
	if err != nil {
		return nil, err
	}
	return readInbox(filepath.Join(pf.dir, inboxFile))
}
