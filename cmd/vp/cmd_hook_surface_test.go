// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// writePayloadStdin swaps os.Stdin for a file holding the given hook payload and
// restores it on cleanup. A terminal stdin returns ExitOK before the surface
// check (cmd_hook.go), so the swap is mandatory.
func writePayloadStdin(t *testing.T, payload map[string]string) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	stdinFile := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(stdinFile, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	origStdin := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = origStdin })
}

// bindPayloadVault creates a payload vault (optionally stamped above surface
// under Projects/foo) and a project cwd whose UNTRACKED .vibe-palace.toml binds
// to it. A tracked marker with vault_path is refused by the resolver
// (ADR-012), and in a no-git temp dir the marker is untracked, so it binds —
// this is the pattern logging_test.go uses. Returns the vault root, its stamp
// dir, and the project cwd.
func bindPayloadVault(t *testing.T, aboveSurface bool) (vaultRoot, stampDir, cwd string) {
	t.Helper()
	tmp := t.TempDir()
	vaultRoot = filepath.Join(tmp, "payload-vault")
	cwd = filepath.Join(tmp, "proj")
	for _, d := range []string{vaultRoot, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testutil.InitProject(t, vaultRoot, "proj")
	stampDir = filepath.Join(vaultRoot, "Projects", "foo")
	if aboveSurface {
		if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion+1, "newer"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"),
		[]byte("vault_path = \""+vaultRoot+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return vaultRoot, stampDir, cwd
}

// TestHookWarnsOnlyOnAbovePayloadVault is assertion (a): a payload vault stamped
// above surface produces exactly one warning that NAMES the payload stamp dir
// and NOT the configured vault, and the hook does not fail-stop. Two vaults are
// used on purpose: if OpenVaultFromCwd misses the binding it falls back to the
// configured global vault (cmd_hook.go), and a single-vault test would then warn
// on the configured vault and pass while the payload path is dead — the
// "configured root absent" assertion catches exactly that.
func TestHookWarnsOnlyOnAbovePayloadVault(t *testing.T) {
	t.Setenv("VP_SURFACE_QUIET", "")
	// Configured (global) vault: compatible, and the fallback target.
	configured := setupTestVaultEnvAt(t, t.TempDir())

	_, stampDir, cwd := bindPayloadVault(t, true)

	// Stop payload with no transcript: the warning fires before hook.Run, and a
	// Stop event does not depend on archive success.
	writePayloadStdin(t, map[string]string{
		"hook_event_name": "Stop",
		"session_id":      "surface-seam-a",
		"cwd":             cwd,
	})

	var buf bytes.Buffer
	t.Cleanup(surface.SetGateWriter(&buf))

	if code := runHook(cli.BuildInfo{Version: "test"}); code != cli.ExitOK {
		t.Fatalf("runHook exit = %d, want ExitOK (a hook never fail-stops)", code)
	}
	out := buf.String()

	if c := strings.Count(out, "this binary supports MCP surface"); c != 1 {
		t.Fatalf("want exactly one surface warning, got %d:\n%s", c, out)
	}
	if !strings.Contains(out, stampDir) {
		t.Errorf("warning does not name the payload stamp dir %q:\n%s", stampDir, out)
	}
	if strings.Contains(out, configured) {
		t.Errorf("warning names the CONFIGURED vault %q — runHook checked the wrong vault:\n%s", configured, out)
	}
	if !strings.Contains(out, "git pull && make install") {
		t.Errorf("warning missing remediation:\n%s", out)
	}
}

// TestSurfaceGateExemptsHook is assertion (b1): surfaceGate exempts the hook, so
// a configured vault above surface produces NO warning for the hook command,
// while the same vault DOES warn for a non-hook, non-mutating command. The
// contrast proves the exemption is load-bearing (not a silent CheckCompatible).
func TestSurfaceGateExemptsHook(t *testing.T) {
	t.Setenv("VP_SURFACE_QUIET", "")
	t.Setenv("VP_SURFACE_GATE", "")
	configured := setupTestVaultEnvAt(t, t.TempDir())
	stampDir := filepath.Join(configured, "Projects", "foo")
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion+1, "newer"); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	t.Cleanup(surface.SetGateWriter(&buf))

	if code := surfaceGate(&cli.Command{Name: "hook"}); code != cli.ExitOK {
		t.Fatalf("surfaceGate(hook) exit = %d, want ExitOK", code)
	}
	if buf.Len() != 0 {
		t.Fatalf("hook is exempt but surfaceGate warned on the configured vault:\n%s", buf.String())
	}

	// Contrast: a non-hook, non-mutating command on the SAME above-surface vault.
	buf.Reset()
	if code := surfaceGate(&cli.Command{Name: "status"}); code != cli.ExitOK {
		t.Fatalf("surfaceGate(status) exit = %d, want ExitOK (warn-only path)", code)
	}
	if buf.Len() == 0 {
		t.Fatal("contrast: a non-hook command did NOT warn on an above-surface vault — the stamp is not driving the test")
	}
	if !strings.Contains(buf.String(), stampDir) {
		t.Errorf("contrast warning does not name the configured stamp dir %q:\n%s", stampDir, buf.String())
	}
}

// TestHookSilentOnCompatiblePayloadVault is assertion (b2): a compatible payload
// vault produces no warning. The empty buffer is silent-for-every-reason, so a
// positive control (the hook's log landed in the payload vault) proves runHook
// actually opened that vault and ran the check — not that stdin was terminal or
// the binding was dead. A SessionEnd with a missing transcript is the
// deterministic log emission logging_test.go relies on.
func TestHookSilentOnCompatiblePayloadVault(t *testing.T) {
	t.Setenv("VP_SURFACE_QUIET", "")
	setupTestVaultEnvAt(t, t.TempDir()) // sandbox HOME/config; configured vault compatible

	payloadVault, _, cwd := bindPayloadVault(t, false)

	writePayloadStdin(t, map[string]string{
		"hook_event_name": "SessionEnd",
		"session_id":      "surface-seam-b2",
		"transcript_path": filepath.Join(t.TempDir(), "does-not-exist.jsonl"),
		"cwd":             cwd,
	})

	var buf bytes.Buffer
	t.Cleanup(surface.SetGateWriter(&buf))

	if code := runHook(cli.BuildInfo{Version: "test"}); code != cli.ExitOK {
		t.Fatalf("runHook exit = %d, want ExitOK", code)
	}
	vplog.Close()

	if buf.Len() != 0 {
		t.Fatalf("compatible payload vault should produce no surface warning, got:\n%s", buf.String())
	}
	// Positive control: the hook opened the payload vault and logged into it.
	if len(logLines(t, payloadVault)) == 0 {
		t.Fatal("no log in the payload vault — runHook did not open it, so the empty " +
			"buffer proves nothing; the binding or stdin swap is dead")
	}
}
