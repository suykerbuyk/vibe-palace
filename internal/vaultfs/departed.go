// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

// The departed-project refusal at vaultfs's write entry points (Write, Create,
// Edit, both ends of Move): the layer the CLI (`vp vault write|edit|move`) and
// the MCP raw file tools (vp_vault_write|edit|move) both call, since a guard
// only an agent can trip is not a guard, and the MCP dispatch seam reads only
// `project`/`to_project`, never a path.
//
// The rule itself lives in internal/departedpath, the one definition every
// write funnel and departure.Find share: while Audits/departures/<p>.json
// exists, p is departed, whatever its directory holds — judged on the literal
// path and on the path it resolves to through any symlink in the vault.
//
// Not covered, by design: the lifecycle commands' own writes. The delete's
// `git rm` and a revert that removes the record in the same commit go through
// git, not this package; a copy writes into a vault with no record.
//
// Task: lc-u15-vaultfs-refuses-writes-into-a-departed-project.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
)

// ErrDepartedProject is the refusal of a write under a departed project's
// trees: departedpath.ErrDeparted, so a refusal from atomicfile or storage
// matches it too. vaultfs's own refusals also wrap ErrRefusedPath, so every
// caller that treats a refused path as a caller fault treats this one so.
var ErrDepartedProject = departedpath.ErrDeparted

// ErrDepartureRecordPath is the refusal of an ordinary write, edit, delete or
// move under Audits/departures/ (departedpath.ErrRecordPath).
var ErrDepartureRecordPath = departedpath.ErrRecordPath

// refuseDepartedWrite is the check every write entry point runs: no write
// under a departed project's trees, and no ordinary change to a departure
// record.
func refuseDepartedWrite(vaultRoot, relPath string) error {
	if err := departedpath.Refuse(vaultRoot, relPath); err != nil {
		return fmt.Errorf("%w: %w", ErrRefusedPath, err)
	}
	return refuseRecordChange(vaultRoot, relPath)
}

// refuseRecordChange refuses any ordinary change under Audits/departures/:
// Write, Create, Edit, Delete and both ends of Move.
func refuseRecordChange(vaultRoot, relPath string) error {
	if err := departedpath.RefuseRecord(vaultRoot, relPath); err != nil {
		return fmt.Errorf("%w: %w", ErrRefusedPath, err)
	}
	return nil
}

// WriteDepartureRecord is the ONE entry point that writes a departure record
// (Audits/departures/<slug>.json). held must be the live root-lock token of
// vaultRoot: only a lifecycle command holding the whole vault — `vp vault
// project delete`, the split purge, a rename — writes records. The write takes
// the record's own per-path lock and fsyncs. created reports that the file did
// not exist before, which a rollback needs.
func WriteDepartureRecord(held *vaultlock.Held, slug string, data []byte) (created bool, err error) {
	abs, err := departureRecordAbs(held, slug)
	if err != nil {
		return false, err
	}
	vaultRoot := held.Root()
	release, err := vaultlock.Acquire(vaultRoot, abs)
	if err != nil {
		return false, fmt.Errorf("lock %s: %w", departedpath.RecordRel(slug), err)
	}
	defer release()
	_, statErr := os.Lstat(abs)
	created = errors.Is(statErr, fs.ErrNotExist)
	if err := atomicfile.Write(vaultRoot, abs, data, atomicfile.WithFsync(), atomicfile.ForDepartureRecord(held)); err != nil {
		return false, fmt.Errorf("write %s: %w", departedpath.RecordRel(slug), err)
	}
	return created, nil
}

// RemoveDepartureRecord removes a departure record a lifecycle command wrote
// and is rolling back (never a committed one: that is restored from HEAD). It
// requires the same live root-lock token as WriteDepartureRecord. An absent
// record is not an error.
func RemoveDepartureRecord(held *vaultlock.Held, slug string) error {
	abs, err := departureRecordAbs(held, slug)
	if err != nil {
		return err
	}
	release, err := vaultlock.Acquire(held.Root(), abs)
	if err != nil {
		return fmt.Errorf("lock %s: %w", departedpath.RecordRel(slug), err)
	}
	defer release()
	if err := RemoveNoLock(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", departedpath.RecordRel(slug), err)
	}
	return nil
}

func departureRecordAbs(held *vaultlock.Held, slug string) (string, error) {
	if err := held.RequireRoot(); err != nil {
		return "", fmt.Errorf("a departure record is written only under the vault's root lock: %w", err)
	}
	if err := ValidatePortableSegment(slug); err != nil || strings.HasPrefix(slug, ".") {
		return "", fmt.Errorf("departure record slug %q: not a plain name", slug)
	}
	return filepath.Join(held.Root(), filepath.FromSlash(departedpath.RecordRel(slug))), nil
}

// RefuseDepartedWrite is the refusal for a caller outside this package that
// commits paths it did not write through vaultfs (the commit guard's
// backstop): nil unless relPath lies under a departed project's tree.
func RefuseDepartedWrite(vaultRoot, relPath string) error {
	return refuseDepartedWrite(vaultRoot, relPath)
}
