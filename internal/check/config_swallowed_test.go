// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckConfigAt_SwallowedVaultPath pins the clause-3 channel for the full
// `vp check` report.
//
// Result.Err is NEVER rendered — PrintRows writes Name/Summary/Details only —
// so the why has to be placed in Details deliberately. The old branch reported
// every resolution failure as "not found at <global config> / Run 'vp init'",
// which points the operator at the wrong FILE entirely: the global config is
// fine, the cwd .vibe-palace.toml is what was rejected.
//
// MUTATION CONTRACT: restore the unconditional "not found / vp init" branch and
// this goes red on both halves.
func TestCheckConfigAt_SwallowedVaultPath(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	configDir := filepath.Join(home, ".config", "vibe-palace")
	projectDir := filepath.Join(tmp, "proj")
	for _, d := range []string{configDir, projectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"),
		[]byte("vault_path = \""+filepath.Join(tmp, "live-vault")+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ".vibe-palace.toml"),
		[]byte("[project]\nname = \"p\"\n\nvault_path = \"/tmp/tw\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, r := CheckConfigAt(projectDir)

	if r.Status != Fail {
		t.Fatalf("Status = %v, want Fail", r.Status)
	}
	details := strings.Join(r.Details, " ")
	for _, want := range []string{"[project]", "LIVE vault", "iteration 210"} {
		if !strings.Contains(details, want) {
			t.Errorf("Details missing %q — Result.Err is not rendered, so the why must be in Details.\ngot: %s", want, details)
		}
	}
	if strings.Contains(details, "vp init") {
		t.Errorf("Details recommend 'vp init', the wrong-file remedy: the global config is fine, "+
			"the cwd .vibe-palace.toml was rejected.\ngot: %s", details)
	}
}

// TestWrapDetail covers the helper that carries err.Error() into Details.
func TestWrapDetail(t *testing.T) {
	if got := WrapDetail(""); got != nil {
		t.Errorf("WrapDetail(\"\") = %v, want nil", got)
	}
	long := strings.Repeat("alpha beta ", 30)
	lines := WrapDetail(long)
	if len(lines) < 2 {
		t.Fatalf("expected the long message to wrap, got %d line(s)", len(lines))
	}
	for i, l := range lines {
		if len(l) > detailWidth {
			t.Errorf("line %d is %d chars, over detailWidth %d: %q", i, len(l), detailWidth, l)
		}
	}
	if joined := strings.Join(lines, " "); joined != strings.TrimSpace(long) {
		t.Errorf("wrapping lost or altered content:\n got: %q\nwant: %q", joined, strings.TrimSpace(long))
	}

	// A single word longer than the width gets its own line rather than being
	// broken, so paths stay copy-pasteable.
	path := strings.Repeat("x", detailWidth+20)
	if got := WrapDetail("see " + path); len(got) != 2 || got[1] != path {
		t.Errorf("oversized word was broken: %q", got)
	}
}
