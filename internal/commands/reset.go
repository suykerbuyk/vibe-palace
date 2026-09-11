// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// resetBeforeRemoveHook runs after every backup is in place and immediately
// before each file is removed. Production leaves it a no-op; a test edits the
// file from it, which is otherwise a race no test can time.
var resetBeforeRemoveHook = func(rel string) {}

// ErrUnsafeResetPath means a file a reset was asked to remove is reached
// through a symlink, or is not a regular file. The reset refuses the whole
// invocation before it writes anything: removing through a link would delete
// whatever the link points at — a project-tier file elsewhere in the vault, say
// — and a backup written through it would land there too.
var ErrUnsafeResetPath = errors.New("unsafe path for a template reset")

// ResetOutcome is what Reset did to one vault Templates/ file.
type ResetOutcome struct {
	// Name is the resource name ("wrap", "chair/SKILL.md").
	Name string
	// Rel is the file's vault-relative path, with forward slashes.
	Rel string
	// Backup is the backup's vault-relative path; empty for a mirror.
	Backup string
	// BackupReused is true when a backup holding these bytes already existed.
	BackupReused bool
	// Mirror is true when the file held exactly the embedded bytes, so no
	// backup was needed.
	Mirror bool
	// Removed is true when this call removed the file.
	Removed bool
	// AlreadyGone is true when the file had disappeared by the time it was
	// to be removed (a concurrent reset, say). Nothing was removed.
	AlreadyGone bool
	// Kept, when non-empty, says why the file was left in place: it changed
	// after its bytes were read and backed up.
	Kept string
	// Err is a removal failure. The backup, if any, is still in place, so a
	// rerun reuses it.
	Err error
}

// Reset removes the vault Templates/ copies in changes, so the embedded floor
// serves each resource again, and keeps a backup of every one that was not a
// byte-identical mirror. changes come from Plan; only entries whose vault copy
// exists (ChangeOverride, ChangeUnchanged) are acted on, and the rest are
// ignored.
//
// It works in two phases, so a failure before the first removal changes
// nothing but backups:
//
//  1. Check and back up. Every file is checked before any backup is written:
//     one reached through a symlink in any path component — the file, a
//     Templates/skills/<skill> directory, Templates/commands — or that is not
//     a regular file refuses the whole call with ErrUnsafeResetPath. Then each
//     file's bytes are re-read (the plan's copy is not trusted); bytes equal to
//     the embedded template are a mirror and need no backup, and anything else
//     is kept by templates.PreserveBackup. Any failure returns an error, and
//     nothing is removed.
//  2. Remove. Each file goes through vaultfs.Delete — the locked removal
//     primitive — compare-and-set on the bytes just backed up, so an edit made
//     after the backup is kept (Kept) rather than lost. A removal failure is
//     recorded on that outcome and the rest continue; a rerun reuses the
//     backups, because the same bytes get the same backup name.
//
// Reset never writes a template, never reads or writes templates.lock, never
// touches the project, wing or room tiers, and never commits: committing the
// removal is the caller's (storage.CommitRemovals).
func Reset(changes []Change) ([]ResetOutcome, error) {
	type target struct {
		c   Change
		rel string
	}
	var targets []target
	for _, c := range changes {
		if c.Kind != ChangeOverride && c.Kind != ChangeUnchanged {
			continue
		}
		rel, err := filepath.Rel(c.VaultRoot, c.VaultPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.VaultPath, err)
		}
		targets = append(targets, target{c: c, rel: filepath.ToSlash(rel)})
	}

	// Phase 1a: every path is checked before anything is written.
	for _, t := range targets {
		if err := checkResetPath(t.c.VaultRoot, t.rel); err != nil {
			return nil, err
		}
	}

	// Phase 1b: read and back up.
	outcomes := make([]ResetOutcome, len(targets))
	data := make([][]byte, len(targets))
	for i, t := range targets {
		o := ResetOutcome{Name: t.c.Name, Rel: t.rel}
		b, err := os.ReadFile(t.c.VaultPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				o.AlreadyGone = true
				outcomes[i] = o
				continue
			}
			return nil, fmt.Errorf("read %s: %w", t.rel, err)
		}
		data[i] = b
		if string(b) == t.c.EmbeddedContent {
			o.Mirror = true
		} else {
			bk, err := templates.PreserveBackup(t.c.VaultRoot, t.rel, b)
			if err != nil {
				return nil, fmt.Errorf("back up %s: %w", t.rel, err)
			}
			o.Backup, o.BackupReused = bk.Rel, bk.Reused
		}
		outcomes[i] = o
	}

	// Phase 2: remove, compare-and-set on the bytes backed up.
	for i, t := range targets {
		o := &outcomes[i]
		if o.AlreadyGone {
			continue
		}
		resetBeforeRemoveHook(t.rel)
		sum := sha256.Sum256(data[i])
		_, err := vaultfs.Delete(t.c.VaultRoot, t.rel, hex.EncodeToString(sum[:]))
		switch {
		case err == nil:
			o.Removed = true
		case errors.Is(err, vaultfs.ErrFileNotFound):
			o.AlreadyGone = true
		case errors.Is(err, vaultfs.ErrShaConflict):
			if o.Backup != "" {
				o.Kept = "changed while resetting; kept — " + o.Backup + " holds the bytes read before the change"
			} else {
				o.Kept = "changed while resetting; kept"
			}
		default:
			o.Err = err
		}
	}
	return outcomes, nil
}

// checkResetPath refuses rel unless the path to it, under the vault root with
// symlinks resolved, is the path itself — no component of it a symlink — and
// the file, if present, is a regular file.
//
// The comparison is made under the RESOLVED vault root, so a vault whose root
// directory is itself a symlink (a supported layout; see
// internal/vaultfs/raw.go) is not refused wholesale: only links inside the
// vault are.
func checkResetPath(vaultRoot, rel string) error {
	realRoot, err := filepath.EvalSymlinks(vaultRoot)
	if err != nil {
		return fmt.Errorf("resolve vault root: %w", err)
	}
	abs := filepath.Join(realRoot, filepath.FromSlash(rel))

	// Walk each component with Lstat: the first symlink is the one to name.
	cur := realRoot
	for part := range strings.SplitSeq(filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel))), "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return nil // nothing further down to follow or remove
		}
		if err != nil {
			return fmt.Errorf("inspect %s: %w", rel, err)
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			comp, _ := filepath.Rel(realRoot, cur)
			return fmt.Errorf("%w: %s is reached through a symlink (%s); vp does not follow or remove it",
				ErrUnsafeResetPath, rel, filepath.ToSlash(comp))
		}
		if cur == abs && !fi.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file (%s); vp does not remove it",
				ErrUnsafeResetPath, rel, fi.Mode().Type())
		}
	}

	// The resolved path must be the path itself. This catches what the walk
	// cannot name. On Windows EvalSymlinks also normalises letter case and
	// short (8.3) names, so a vault path spelled differently from the disk is
	// refused here too — fail-safe: nothing is removed or written.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", rel, err)
	}
	if resolved != filepath.Clean(abs) {
		return fmt.Errorf("%w: %s resolves to %s, not to itself — a symlink, or on Windows a letter-case "+
			"or short-name difference in the path; vp does not follow or remove it",
			ErrUnsafeResetPath, rel, resolved)
	}
	return nil
}

// ShimSourceRemoved reports whether any outcome removed a file that project
// shims are rendered from: a command template (its first paragraph is the
// shim's brief) or a skill's SKILL.md (its description is the skill shim's).
// Shims are per project and a reset is vault-wide, so after such a removal the
// shims in every project may still carry the removed override's text until
// `vp commands upgrade --overwrite` runs there.
func ShimSourceRemoved(outcomes []ResetOutcome) bool {
	for _, o := range outcomes {
		if !o.Removed {
			continue
		}
		if strings.HasPrefix(o.Rel, "Templates/commands/") {
			return true
		}
		// Templates/skills/<skill>/SKILL.md — not a reference that happens to
		// share the name.
		if path.Base(o.Rel) == "SKILL.md" && path.Dir(path.Dir(o.Rel)) == "Templates/skills" {
			return true
		}
	}
	return false
}
