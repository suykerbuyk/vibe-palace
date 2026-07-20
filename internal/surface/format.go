// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// The vault DATA-FORMAT axis is a second, orthogonal version mechanism to the
// .surface tool-surface axis (Stamp/CheckCompatible). The two answer different
// questions and MUST stay independent:
//
//   - .surface (surface = N): is the reading binary at least as new as the one
//     that last WROTE this vault? Absence-means-pass; fires when the vault is
//     AHEAD of the binary; a WRITE hazard.
//   - vault.toml (format = N): is the DATA on disk written in the old encoding
//     or the new one? Absence-means-format-0 (unmigrated, a POSITIVE signal);
//     fires when the vault is BEHIND the binary; a READ hazard.
//
// The format axis therefore gets a DEDICATED read/write path (ReadFormat /
// WriteFormat) that does NOT ride WriteStamp: WriteStamp no-ops on
// existing.Surface >= version and rebuilds a bare Stamp{Surface: N}, and
// StampForPath fires as a best-effort side effect on every schema write — all
// incompatible with a number that must advance ONLY when a migration completes.

// RequiredDataFormat is the on-disk data-format version this binary requires a
// vault to be at before its KG object-side reads are trustworthy. It is a
// hand-bumped monotonic integer, sibling to MCPSurfaceVersion but on an
// independent axis.
//
// Baseline 0: this task INTRODUCES the axis but does NOT arm it. With
// RequiredDataFormat = 0 and an absent manifest reading as format 0, the format
// read gate (EnforceFormatFailStop) is a NO-OP for every vault today. The
// consumer task kg-triple-filename-sanitization bumps this 0 -> 1 and ships the
// migration that writes format = 1; the two axes then compose.
const RequiredDataFormat int = 0

// vaultManifestDir/vaultManifestFile locate the vault-root manifest carrying the
// data-format number: <root>/.vibe-palace/vault.toml.
const (
	vaultManifestDir  = ".vibe-palace"
	vaultManifestFile = "vault.toml"
)

// VaultManifest models the on-disk .vibe-palace/vault.toml. It carries a single
// vault-wide data-format number today; more vault-scoped fields may join it.
type VaultManifest struct {
	Format int `toml:"format"`
}

// vaultManifestPath returns <root>/.vibe-palace/vault.toml.
func vaultManifestPath(root string) string {
	return filepath.Join(root, vaultManifestDir, vaultManifestFile)
}

// ReadFormat reads the vault-wide data-format number from
// <root>/.vibe-palace/vault.toml.
//
// Absence of the file OR absence of the field returns format 0 (unmigrated),
// nil — a POSITIVE signal that this vault has never recorded a migration, never
// an error and never "current". This polarity is the deliberate INVERSE of
// CheckCompatible's absence-means-pass on the surface axis. An empty root is
// likewise treated as absence (0, nil): there is no manifest to read.
//
// Only a present-but-unreadable or malformed manifest is an error.
func ReadFormat(root string) (int, error) {
	if root == "" {
		return 0, nil
	}
	data, err := os.ReadFile(vaultManifestPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read vault.toml: %w", err)
	}
	var m VaultManifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return 0, fmt.Errorf("parse vault.toml: %w", err)
	}
	return m.Format, nil
}

// WriteFormat atomically writes <root>/.vibe-palace/vault.toml with format = n.
//
// MONOTONE: it refuses to LOWER the recorded format (returns an error when n is
// below the current value) and no-ops when n equals the current value, so the
// tracked file stays byte-stable once written.
//
// It must be called ONLY explicitly — this is what a migration calls on
// COMPLETION. It is deliberately NOT wired as a side effect of any other write,
// and it does NOT go through WriteStamp / StampForPath: a half-migrated vault
// must still read as format 0.
//
// n == 0 against an absent manifest is a no-op: this axis never creates the
// manifest just to record the unmigrated baseline.
func WriteFormat(root string, n int) error {
	if root == "" {
		return ErrNoVault
	}
	if n < 0 {
		return fmt.Errorf("write vault.toml: format %d must not be negative", n)
	}
	current, err := ReadFormat(root)
	if err != nil {
		return err
	}
	if n < current {
		return fmt.Errorf("write vault.toml: refusing to lower data format from %d to %d (monotone)", current, n)
	}
	if n == current {
		return nil
	}

	dir := filepath.Join(root, vaultManifestDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create vault manifest dir: %w", err)
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(VaultManifest{Format: n}); err != nil {
		return fmt.Errorf("encode vault.toml: %w", err)
	}

	// Reuse the surface stamp's private temp-file + rename primitive (same
	// package) so the manifest is written atomically, mirroring how the stamp
	// is written.
	if err := writeStampFileAtomic(vaultManifestPath(root), buf.Bytes()); err != nil {
		return fmt.Errorf("write vault.toml: %w", err)
	}
	return nil
}

// FormatIncompatibleError is returned by the data-format read gate when a vault's
// recorded data format is BEHIND what this binary requires (the vault is on the
// old encoding). It is the format-axis analogue of IncompatibleError, but fires
// in the INVERTED direction: the surface gate fires when the vault is ahead of
// the binary; the format gate fires when the vault is behind it.
type FormatIncompatibleError struct {
	BinaryRequired int    // RequiredDataFormat this binary demands
	VaultFormat    int    // format recorded on disk (0 = unmigrated / absent)
	Root           string // vault root the check ran against
}

// Error renders the migrate-this-vault remediation message.
func (e *FormatIncompatibleError) Error() string {
	return fmt.Sprintf(
		"vp: this binary requires vault data format v%d; vault '%s' is at v%d (unmigrated)\n"+
			"    action:    run the vibe-palace data migration to upgrade this vault\n"+
			"    if you must proceed against unmigrated data (reads may be incomplete):\n"+
			"       VP_FORMAT_GATE=warn <original-command>   (proceed at risk)",
		e.BinaryRequired, e.Root, e.VaultFormat,
	)
}

// checkFormatCompatible is the parameterized core of the data-format read gate
// and the test seam: EnforceFormatFailStop passes RequiredDataFormat in
// production, while tests pass a higher required to exercise the fail-stop path
// that is inert at required == 0. This keeps required > 0 out of production
// entirely. It is a PURE READ — no write side effect (never a StampForPath-style
// stamp-on-read).
func checkFormatCompatible(root string, required int) error {
	format, err := ReadFormat(root)
	if err != nil {
		return err
	}
	if format < required {
		return &FormatIncompatibleError{
			BinaryRequired: required,
			VaultFormat:    format,
			Root:           root,
		}
	}
	return nil
}

// EnforceFormatFailStop is the fail-stop entry point for the data-format read
// gate. It returns a *FormatIncompatibleError when the vault is behind the
// binary, honoring the VP_FORMAT_GATE=warn escape hatch that downgrades the
// error to a single logged warning and returns nil — mirroring EXACTLY how
// EnforceFailStop reads VP_SURFACE_GATE (same precedence, same "warn" value).
//
// migratorExempt is the MIGRATOR SEAM: the migration tool runs on the current
// binary and MUST read format-0 (unmigrated) data to rewrite it, so it bypasses
// the gate entirely. Every normal caller passes false. Because
// RequiredDataFormat == 0, this gate is inert today regardless of the seam.
func EnforceFormatFailStop(root string, migratorExempt bool) error {
	return enforceFormatFailStop(root, RequiredDataFormat, migratorExempt)
}

// enforceFormatFailStop is the parameterized core (test seam) behind
// EnforceFormatFailStop: production passes RequiredDataFormat; tests pass a
// higher required to exercise the fail-stop / warn-downgrade / migrator-exempt
// paths that are inert at required == 0.
func enforceFormatFailStop(root string, required int, migratorExempt bool) error {
	if migratorExempt {
		return nil
	}
	err := checkFormatCompatible(root, required)
	if err == nil {
		return nil
	}
	var fe *FormatIncompatibleError
	if errors.As(err, &fe) && os.Getenv("VP_FORMAT_GATE") == "warn" {
		fmt.Fprintln(gateStderr, err.Error())
		return nil
	}
	return err
}
