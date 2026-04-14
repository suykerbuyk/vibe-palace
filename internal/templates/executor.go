// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Executor is the central plan/apply surface for materializing
// embedded template bytes onto disk. Three callers delegate here:
//
//   - reconcile.TemplateTree.Apply (vp init materialize): uses
//     BackupPolicyAlways so a dirty user-edit is preserved as .bak
//     before an embedded bump lands.
//   - commands.Apply (vp commands upgrade): uses BackupPolicyNever.
//   - commands.ApplyWithBackup (vp skills upgrade): uses
//     BackupPolicyAlways.
//
// The asymmetry between commands and skills (commands=never,
// skills=always) predates this extraction; see
// doc/TEMPLATE_POLICY.md for the rationale and follow-up flag.
//
// The walk primitive (WalkEmbedded) is already in embedded.go; the
// Executor deliberately does not re-wrap it — callers that need a
// resource list ask WalkEmbedded directly, then funnel individual
// writes through Executor.Write so every template write on the
// project's golden path shares the same atomic-write, backup, and
// permissions story.
type Executor struct{}

// NewExecutor returns an Executor with defaults suitable for every
// caller in the tree today. The zero value is also valid.
func NewExecutor() *Executor { return &Executor{} }

// BackupPolicy controls whether Write creates a sibling .bak file
// before overwriting an existing target.
type BackupPolicy int

const (
	// BackupPolicyNever disables .bak emission regardless of whether
	// the target exists. commands upgrade uses this.
	BackupPolicyNever BackupPolicy = iota
	// BackupPolicyAlways writes a .bak of the pre-existing bytes
	// before overwriting. New files (no existing target) produce no
	// .bak. skills upgrade and init materialize use this.
	BackupPolicyAlways
	// BackupPolicyRename, like BackupPolicyAlways, preserves the
	// pre-existing bytes to a sibling .bak, but does so via rename
	// (rather than read+write). This matches the legacy
	// commands.ApplyWithBackup behavior byte-for-byte. Only distinct
	// from Always in the edge case where the write fails — rename
	// leaves the caller with a .bak and no primary; copy-then-write
	// leaves the primary intact.
	BackupPolicyRename
)

// WriteOptions configures a single Executor.Write call.
type WriteOptions struct {
	// Backup picks the .bak policy for this write.
	Backup BackupPolicy
	// Perm is the file mode applied to the written target. Defaults
	// to 0o644 when zero.
	Perm os.FileMode
}

// Write materializes data to dst atomically, honoring the backup
// policy. Parent directories are created with 0o755 as needed.
//
// Contract:
//   - BackupPolicyNever: a pre-existing dst is overwritten; no .bak
//     is left behind.
//   - BackupPolicyAlways: a pre-existing dst's bytes are copied to
//     dst+".bak" before the atomic rename. The primary remains
//     readable throughout; a concurrent reader sees either the old
//     bytes or the new bytes, never a partial write.
//   - BackupPolicyRename: a pre-existing dst is renamed to
//     dst+".bak"; the new bytes are then atomically written. This
//     has a (tiny) window where dst does not exist, which matches
//     the legacy commands.ApplyWithBackup behavior that callers'
//     tests pin down.
func (e *Executor) Write(dst string, data []byte, opts WriteOptions) error {
	perm := opts.Perm
	if perm == 0 {
		perm = 0o644
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	switch opts.Backup {
	case BackupPolicyAlways:
		// Copy-then-atomic-write: primary stays readable.
		if cur, err := os.ReadFile(dst); err == nil {
			if err := os.WriteFile(dst+".bak", cur, perm); err != nil {
				return fmt.Errorf("write bak %s: %w", dst+".bak", err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("read for bak %s: %w", dst, err)
		}
	case BackupPolicyRename:
		if _, err := os.Stat(dst); err == nil {
			if err := os.Rename(dst, dst+".bak"); err != nil {
				return fmt.Errorf("backup %s: %w", dst, err)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat for bak %s: %w", dst, err)
		}
	}
	return atomicWrite(dst, data, perm)
}

// atomicWrite writes data to dst via a sibling tmp + rename, leaving
// no partial file on failure. Shared by every template-write caller.
func atomicWrite(dst string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".vp-tmpl-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// HashFile returns the hex sha256 of a file on disk. A missing file
// returns ("", err) with an error that satisfies os.IsNotExist.
func HashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Classification is a purely-functional three-way compare of
// (embedded, vault, lock) SHAs. It is the decision subject of the
// materialize decision table in reconcile.TemplateTree — extracted
// here so Plan consumers in the commands package can share the same
// vocabulary in the future without needing the full Action/Plan
// machinery. Unused internally today; kept as the documented
// vocabulary the Executor plan surface is organized around.
type Classification int

const (
	// ClassMissing: vault copy does not exist.
	ClassMissing Classification = iota
	// ClassUnchanged: vault matches embedded.
	ClassUnchanged
	// ClassEmbeddedBumped: vault matches lock, embedded differs
	// (safe auto-upgrade).
	ClassEmbeddedBumped
	// ClassUserEdited: vault differs from lock, embedded matches
	// lock (keep user copy).
	ClassUserEdited
	// ClassDiverged: vault differs from lock AND embedded differs
	// from lock (requires user Prompt).
	ClassDiverged
	// ClassAdoptable: no lock entry, but vault bytes match embedded
	// bytes exactly — silent-adopt candidate.
	ClassAdoptable
)

// Classify returns the Classification for a (vaultSHA, embeddedSHA,
// lockSHA) triple. Empty strings mean "absent". The mapping matches
// the decision table documented in reconcile.TemplateTree.planMaterialize.
func Classify(vaultSHA, embeddedSHA, lockSHA string) Classification {
	switch {
	case vaultSHA == "":
		return ClassMissing
	case lockSHA == "" && vaultSHA == embeddedSHA:
		return ClassAdoptable
	case lockSHA == "":
		return ClassDiverged
	case vaultSHA == lockSHA && embeddedSHA == lockSHA:
		return ClassUnchanged
	case vaultSHA == lockSHA && embeddedSHA != lockSHA:
		return ClassEmbeddedBumped
	case vaultSHA != lockSHA && embeddedSHA == lockSHA:
		return ClassUserEdited
	default:
		return ClassDiverged
	}
}
