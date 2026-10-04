// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
// ARMED at 1 (kg-triple-filename-sanitization): the KG triple filename encoding
// changed to a FLAT, sanitized form, so a vault still at format 0 holds
// old-encoding triple files the current binary's globs cannot find. The format
// read gate (EnforceFormatFailStop) therefore FAIL-STOPS KG object-side reads on
// any vault below format 1 — reads must stop rather than silently miss data —
// until the migration renames the files and stamps format = 1. Freshly created
// vaults are born-current (stamped at creation), and the migration command
// advances an existing vault to 1 on completion.
//
// BUMPED TO 2 (board-reporting-surface-and-format-version-bump), after the migration that can
// reach it was proven. An earlier bump to 2 was reverted (revert-requireddataformat-to-1-until-
// migration-is-proven) because vp migrate task-board-fields then aborted partway on real vault
// data, and a constant requiring a format no vault can reach fail-stops every gated read with no
// exit. That command was split into a planner and an apply-only executor, the archived corpus
// was repaired, and on 2026-09-19 the whole chain ran clean on the live vault: 724 of 724 task
// files migrated, 0 refused, and a second run changed nothing.
//
// 🔴 THE VALUE IS NOT THE DESTINATION, AND THE ORDER OF OPERATIONS MATTERS. This gate fires
// when the vault is BEHIND the binary and never when it is ahead. So a binary carrying this
// value must not be installed where it will read a vault still at 1: run vp migrate
// task-board-fields with it first, which raises every task file's DataFormat marker and stamps
// the vault at 2, and install it afterwards. A binary still requiring 1 keeps working against a
// vault at 2.
//
// What format 2 means: vp board's chronological/status-bucket reporting needs CreateTime/ModTime
// and the widened Status vocabulary to be TRUSTWORTHY, not merely present. A vault at format 1
// may hold task files with pre-migration Status values and no CreateTime/ModTime at all. Format 2
// is the read-side signal that a migration has backfilled both across the vault.
//
// The bump does NOT extend the read gate's reach: checkFormatGate
// (internal/storage/format_gate.go) guards only the three KG-storage call sites
// (QueryEntity/KGStats/ListTriples), not vp_kg_invalidate, which resolves a triple by direct
// path and never calls checkFormatGate. So task/session/resume reads and every other vault
// operation are unaffected by either value.
//
// MCPSurfaceVersion is not bumped with this: the two axes are independent (see the header
// comment above). The release-tag convention v<MCPSurfaceVersion>.<RequiredDataFormat>.<build>
// therefore reads v6.2.y from this change on.
const RequiredDataFormat int = 2

// vaultManifestDir/vaultManifestFile locate the vault-root manifest carrying the
// data-format number: <root>/.vibe-palace/vault.toml.
const (
	vaultManifestDir  = ".vibe-palace"
	vaultManifestFile = "vault.toml"
)

// VaultManifest models the on-disk .vibe-palace/vault.toml: the vault-wide
// data-format number and the migration marker (ADR-014, "the migration marker").
// Every writer goes through ManifestBytes, so a write of one field keeps the
// others.
type VaultManifest struct {
	Format int `toml:"format"`
	// AuthoredOnly is the migration marker, `authored_only = "YYYY-MM-DD"`: the
	// date the vault became authored-only. Empty means unmigrated, and the key is
	// then not written at all, so a manifest without it encodes to exactly the
	// bytes FormatManifestBytes always wrote.
	AuthoredOnly MarkerDate `toml:"authored_only,omitempty"`
}

// MarkerDate is the migration marker's value. It is always written as a quoted
// TOML string, but a hand-edited unquoted TOML local date decodes too: the
// decoder hands that over as a time.Time, which a plain string field refuses.
type MarkerDate string

// markerDateLayout is the only form the marker takes.
const markerDateLayout = "2006-01-02"

// tomlLocalDate is the name of the time.Location BurntSushi/toml gives a TOML
// local date (internal.LocalDate, "date-local"). The package does not export
// the location itself, so the marker matches it by name.
const tomlLocalDate = "date-local"

// UnmarshalTOML accepts a "YYYY-MM-DD" string or a TOML local date and
// normalises either to "YYYY-MM-DD". Any other value is an error naming the key.
func (d *MarkerDate) UnmarshalTOML(v any) error {
	switch x := v.(type) {
	case string:
		t, err := time.Parse(markerDateLayout, x)
		if err != nil {
			return fmt.Errorf("authored_only = %q: want a date in YYYY-MM-DD form", x)
		}
		*d = MarkerDate(t.Format(markerDateLayout))
		return nil
	case time.Time:
		// Only a TOML local DATE: the decoder gives it the location
		// "date-local". A local time, a local datetime or an offset datetime
		// is not a date.
		if x.Location() == nil || x.Location().String() != tomlLocalDate {
			return fmt.Errorf("authored_only = %s: want a date in YYYY-MM-DD form, not a time or a datetime", x.Format(time.RFC3339))
		}
		*d = MarkerDate(x.Format(markerDateLayout))
		return nil
	default:
		return fmt.Errorf("authored_only: want a date in YYYY-MM-DD form, got a TOML %T", v)
	}
}

// formatOnly is the struct ReadFormat decodes: the format key alone, so a
// malformed migration marker never blocks a data-format-gated command. Only
// ReadVaultManifest, the marker's reader, reports it.
type formatOnly struct {
	Format int `toml:"format"`
}

// vaultManifestPath returns <root>/.vibe-palace/vault.toml.
func vaultManifestPath(root string) string {
	return filepath.Join(root, vaultManifestDir, vaultManifestFile)
}

// VaultManifestPath is vaultManifestPath for callers outside this package that
// must recognise a vault root by its manifest (the resolver's project-binding
// tier), so the location has one definition.
func VaultManifestPath(root string) string { return vaultManifestPath(root) }

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
	var m formatOnly
	if err := toml.Unmarshal(data, &m); err != nil {
		return 0, fmt.Errorf("parse vault.toml: %w", err)
	}
	return m.Format, nil
}

// ReadVaultManifest reads every field of <root>/.vibe-palace/vault.toml. An
// absent file (or an empty root) is the zero manifest: format 0, no marker. A
// malformed file, including a wrong-typed authored_only, is an error naming
// what is wrong.
func ReadVaultManifest(root string) (VaultManifest, error) {
	if root == "" {
		return VaultManifest{}, nil
	}
	data, err := os.ReadFile(vaultManifestPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return VaultManifest{}, nil
		}
		return VaultManifest{}, fmt.Errorf("read vault.toml: %w", err)
	}
	var m VaultManifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return VaultManifest{}, fmt.Errorf("parse vault.toml: %w", err)
	}
	return m, nil
}

// manifestWriteFile is the one write of vault.toml; a test seam counts it.
var manifestWriteFile = writeStampFileAtomic

// WriteVaultManifest writes every field of m to <root>/.vibe-palace/vault.toml
// in ONE atomic write (temp file and rename), so a crash leaves the old file or
// the new one, never a manifest carrying some fields and not others.
func WriteVaultManifest(root string, m VaultManifest) error {
	if root == "" {
		return ErrNoVault
	}
	data, err := ManifestBytes(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, vaultManifestDir), 0o755); err != nil {
		return fmt.Errorf("create vault manifest dir: %w", err)
	}
	if err := manifestWriteFile(vaultManifestPath(root), data); err != nil {
		return fmt.Errorf("write vault.toml: %w", err)
	}
	return nil
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
	// A read-modify-write over the whole manifest, so a format bump keeps the
	// migration marker. A malformed marker refuses the bump rather than drop it.
	m, err := ReadVaultManifest(root)
	if err != nil {
		return err
	}
	current := m.Format
	if n < current {
		return fmt.Errorf("write vault.toml: refusing to lower data format from %d to %d (monotone)", current, n)
	}
	if n == current {
		return nil
	}
	m.Format = n
	return WriteVaultManifest(root, m)
}

// ManifestBytes is the one encoder of .vibe-palace/vault.toml. With no marker it
// emits exactly `format = <n>` and a newline, the bytes vp has always written.
func ManifestBytes(m VaultManifest) ([]byte, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(m); err != nil {
		return nil, fmt.Errorf("encode vault.toml: %w", err)
	}
	return buf.Bytes(), nil
}

// FormatManifestBytes is the exact .vibe-palace/vault.toml WriteFormat writes
// for data format n on a vault with no marker. A caller that must recognise a
// stamp vp wrote (storage.CommitVaultInit) compares against these bytes.
func FormatManifestBytes(n int) ([]byte, error) {
	return ManifestBytes(VaultManifest{Format: n})
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
// the gate entirely. Every normal caller passes false. With RequiredDataFormat
// armed at 1, this gate fail-stops normal KG reads on any format-0 vault.
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
