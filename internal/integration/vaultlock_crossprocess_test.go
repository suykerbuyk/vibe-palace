// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestIntegration_VaultLockCrossProcess proves the per-path advisory flock in
// internal/vaultlock serializes whole-file read→modify→write edits across
// SEPARATE OS PROCESSES so concurrent edits to the same vault file never lose
// updates.
//
// Mechanism: build the real `vp` binary and launch N concurrent
// `vp vault edit <path> --old ANCHOR_iii --new DONE_iii` child processes, all
// contending on the SAME seeded file. Each child performs an independent
// read → string-replace → atomic whole-file rewrite. Without the flock, those
// rewrites would clobber one another and some DONE markers would be lost.
// With the flock, every anchor is converted exactly once.
//
// The anchors are fixed-width and zero-padded (ANCHOR_000..) so that no anchor
// is a substring of another and each edit matches a single occurrence (no
// --replace-all needed).
func TestIntegration_VaultLockCrossProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-process test spawns subprocesses; skipped under -short")
	}

	bin := buildVPBinary(t)

	// Isolated HOME + XDG_CONFIG_HOME so the child `vp` resolves our temp vault
	// via the global config, exactly as a real install would.
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	if err := os.MkdirAll(filepath.Join(xdg, "vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	vaultRoot := filepath.Join(home, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `vault_path = "` + vaultRoot + `"` + "\ngit_enabled = false\n"
	if err := os.WriteFile(filepath.Join(xdg, "vibe-palace", "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	const n = 16
	const relPath = "notes.md"

	// Seed the contended file with N distinct fixed-width anchors, one per line.
	var seed strings.Builder
	for i := range n {
		fmt.Fprintf(&seed, "ANCHOR_%03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, relPath), []byte(seed.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Launch N concurrent child processes, each rewriting one anchor.
	childEnv := append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+xdg,
	)
	errs := make([]error, n)
	outs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			cmd := exec.Command(bin, "vault", "edit", relPath,
				"--old", fmt.Sprintf("ANCHOR_%03d", i),
				"--new", fmt.Sprintf("DONE_%03d", i),
			)
			cmd.Env = childEnv
			out, err := cmd.CombinedOutput()
			outs[i] = string(out)
			errs[i] = err
		})
	}
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("child %d (`vp vault edit`) failed: %v\noutput: %s", i, errs[i], outs[i])
		}
	}

	// The whole point: every anchor converted exactly once, none lost.
	final, err := os.ReadFile(filepath.Join(vaultRoot, relPath))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	s := string(final)
	for i := range n {
		done := fmt.Sprintf("DONE_%03d", i)
		anchor := fmt.Sprintf("ANCHOR_%03d", i)
		if !strings.Contains(s, done) {
			t.Errorf("lost update: %s missing from final file:\n%s", done, s)
		}
		if strings.Contains(s, anchor) {
			t.Errorf("unconverted anchor %s remains (an edit was clobbered):\n%s", anchor, s)
		}
	}

	// The sidecar lock dir must have been created under the contended vault
	// root — proof the vaultlock path was exercised cross-process.
	if info, err := os.Stat(filepath.Join(vaultRoot, ".vp-locks")); err != nil || !info.IsDir() {
		t.Errorf(".vp-locks/ not present under vault root after run (err=%v)", err)
	}
}
