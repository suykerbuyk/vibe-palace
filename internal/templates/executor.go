// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
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
	// VaultRoot is the vault root that owns dst, used to stamp the
	// .surface version on a successful write. Leave empty for a
	// non-vault target (no stamp). Threading this through is why the
	// private atomicWrite copy was removed: stamping is now structural,
	// not out-of-band per-caller discipline.
	VaultRoot string
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
	// Route through the shared atomicfile primitive so the .surface stamp is
	// structural: on success it stamps opts.VaultRoot/dst (skipped when
	// VaultRoot is ""). This template write never fsynced under the old private
	// copy, so no WithFsync() here — behavior is preserved.
	return atomicfile.Write(opts.VaultRoot, dst, data, atomicfile.WithPerm(perm))
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
