// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// One test per site that writes to an operator-supplied destination. A suite
// covering only --export would report the class closed at 3 of 4: `vp archive
// extract` spells the flag --to, which is the exact trap this task exists to
// avoid. Every site below must refuse, and the --to case is not optional.

// vaultDocDest returns a destination inside v's vault that an export would
// clobber, plus the vault handle.
func vaultDocDest(t *testing.T) (*storage.Vault, string) {
	t.Helper()
	v := testVault(t)
	dir := filepath.Join(v.Root, "Projects", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dest := filepath.Join(dir, "resume.md")
	if err := os.WriteFile(dest, []byte("# live document\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return v, dest
}

// assertRefused checks the exit code, the curated remediation, and — the part
// that actually matters — that the target document was not overwritten.
func assertRefused(t *testing.T, code int, out, dest string) {
	t.Helper()
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (%d)", code, cli.ExitUser)
	}
	if !strings.Contains(out, "inside the vault") {
		t.Errorf("refusal must say the destination is inside the vault, got: %s", out)
	}
	if !strings.Contains(out, allowInsideVaultFlag) {
		t.Errorf("refusal must name %s as the override, got: %s", allowInsideVaultFlag, out)
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("re-read dest: %v", err)
	}
	if !strings.Contains(string(body), "live document") {
		t.Fatalf("destination was overwritten despite the refusal: %q", string(body))
	}
}

func TestAuditRoomsRefusesExportInsideVault(t *testing.T) {
	v, dest := vaultDocDest(t)
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", storage.Config{}, false, false, false, dest, false, &buf)
	assertRefused(t, code, buf.String(), dest)
}

func TestAuditRoomsAllowsExportInsideVaultWithOverride(t *testing.T) {
	v, dest := vaultDocDest(t)
	var buf bytes.Buffer
	code := runAuditRooms(v, "proj", storage.Config{}, false, false, false, dest, true, &buf)
	if code != cli.ExitOK {
		t.Fatalf("override must permit the write, got %d: %s", code, buf.String())
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if strings.Contains(string(body), "live document") {
		t.Fatal("override did not write; the guard is still refusing")
	}
}

func TestDiscoverRoomsRefusesExportInsideVault(t *testing.T) {
	v, dest := vaultDocDest(t)
	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", storage.Config{}, 0, false, false, dest, false, &buf)
	assertRefused(t, code, buf.String(), dest)
}

func TestTuneRoomsRefusesExportInsideVault(t *testing.T) {
	v, dest := vaultDocDest(t)
	var buf bytes.Buffer
	code := runTuneRooms(v, "proj", storage.Config{}, 0, false, false, false, dest, false, &buf)
	assertRefused(t, code, buf.String(), dest)
}

// TestArchiveExtractRefusesToInsideVault is the site whose flag is --to, not
// --export. It drives the real command so the flag name itself is under test.
func TestArchiveExtractRefusesToInsideVault(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	dir := filepath.Join(vaultDir, "Projects", "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dest := filepath.Join(dir, "resume.md")
	if err := os.WriteFile(dest, []byte("# live document\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var code int
	out := captureStderr(t, func() {
		code = cmdArchiveExtract().Run([]string{"--project", "proj", "--to", dest, "some-session-id"})
	})

	assertRefused(t, code, out, dest)
}

// TestGuardUnresolvableRootIgnoresTheOverride pins ruling 3 at the CLI helper.
// --allow-inside-vault is permission to write inside a KNOWN vault, not
// permission to skip verification. An unresolvable root must still refuse, with
// the flag set, and the remediation must NOT offer the flag as an escape —
// doing so would write the fail-open hole into the message.
func TestGuardUnresolvableRootIgnoresTheOverride(t *testing.T) {
	base := t.TempDir()
	missingVault := filepath.Join(base, "no-such-vault")
	dest := filepath.Join(base, "report.json")

	for _, allow := range []bool{false, true} {
		var buf bytes.Buffer
		code := guardExportDestination(missingVault, dest, allow, &buf)
		if code != cli.ExitSystem {
			t.Fatalf("allowInsideVault=%v: exit = %d, want ExitSystem (%d): %s",
				allow, code, cli.ExitSystem, buf.String())
		}
		if strings.Contains(buf.String(), allowInsideVaultFlag) {
			t.Fatalf("allowInsideVault=%v: unresolvable-root remediation must not offer %s: %s",
				allow, allowInsideVaultFlag, buf.String())
		}
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Fatalf("allowInsideVault=%v: destination must not have been written", allow)
		}
	}
}

// TestGuardEmptyVaultRootIgnoresTheOverride is the same rule for an empty vault
// root, which filepath.Abs would otherwise resolve to the process cwd.
func TestGuardEmptyVaultRootIgnoresTheOverride(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "report.json")
	for _, allow := range []bool{false, true} {
		var buf bytes.Buffer
		code := guardExportDestination("", dest, allow, &buf)
		if code != cli.ExitSystem {
			t.Fatalf("allowInsideVault=%v: empty vault root: exit = %d, want ExitSystem (%d): %s",
				allow, code, cli.ExitSystem, buf.String())
		}
		if strings.Contains(buf.String(), allowInsideVaultFlag) {
			t.Fatalf("allowInsideVault=%v: empty-root remediation must not offer %s: %s",
				allow, allowInsideVaultFlag, buf.String())
		}
	}
}

// TestGuardOverrideStillPermitsAResolvableInsideVaultDest is the other half:
// tightening the unresolvable-root path must not break the override for the
// finding it actually applies to.
func TestGuardOverrideStillPermitsAResolvableInsideVaultDest(t *testing.T) {
	v, dest := vaultDocDest(t)
	var buf bytes.Buffer
	if code := guardExportDestination(v.Root, dest, true, &buf); code != cli.ExitOK {
		t.Fatalf("override must permit a dest inside a resolvable vault, got %d: %s", code, buf.String())
	}
	var refuse bytes.Buffer
	if code := guardExportDestination(v.Root, dest, false, &refuse); code != cli.ExitUser {
		t.Fatalf("without the override the same dest must be refused, got %d: %s", code, refuse.String())
	}
}
