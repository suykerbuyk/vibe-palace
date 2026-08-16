// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// TestRunHookRefusesSwallowedVaultPath is iteration 210 itself, as a test.
//
// The operator writes a vault_path meaning "capture this throwaway work
// somewhere else", a [table] above it swallows the key, and the hook — the
// SessionEnd/Stop capture path — writes the session into the LIVE vault.
//
// MUTATION CONTRACT: delete the ErrSwallowedVaultPath branch in runHook and this
// test goes RED on the live-vault assertion. It is not enough for
// readCwdVaultPath to refuse: runHook catches that error and falls back to
// OpenVaultGlobal(), which IS the live vault, so the guard alone leaves the
// original defect reachable through the back door.
func TestRunHookRefusesSwallowedVaultPath(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	configDir := filepath.Join(home, ".config")
	liveVault := filepath.Join(tmp, "live-vault")
	projectDir := filepath.Join(tmp, "throwaway-proj")
	for _, d := range []string{filepath.Join(configDir, "vibe-palace"), liveVault, projectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)

	// The global config — the LIVE vault the fallback would reach.
	if err := os.WriteFile(
		filepath.Join(configDir, "vibe-palace", "config.toml"),
		[]byte("vault_path = \""+liveVault+"\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	// The specimen: [project] above vault_path swallows it.
	if err := os.WriteFile(
		filepath.Join(projectDir, ".vibe-palace.toml"),
		[]byte("[project]\nname = \"throwaway\"\n\nvault_path = \""+filepath.Join(tmp, "throwaway-vault")+"\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	body, err := json.Marshal(map[string]string{
		"hook_event_name": "Stop",
		"session_id":      "throwaway-session",
		"cwd":             projectDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	stdinFile := filepath.Join(tmp, "payload.json")
	if err := os.WriteFile(stdinFile, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	origStdin := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = origStdin })

	code := runHook(cli.BuildInfo{Version: "test"})
	vplog.Close()

	// THE ASSERTION: nothing of this session reached the live vault.
	liveProjects := filepath.Join(liveVault, "Projects")
	if entries, rerr := os.ReadDir(liveProjects); rerr == nil && len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the hook captured into the LIVE vault: %s contains %v — "+
			"a swallowed vault_path must never reach the global fallback", liveProjects, names)
	}

	// The ruling: a hook failure is reported through the log and the result
	// body, NEVER the exit code (cli.ExitSystem is Claude Code's blocking 2).
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK)", code, cli.ExitOK)
	}

	// The alarm has to be durable, and reaching vp.log takes an explicit step
	// here: pre-run initLogging resolves through openProjectVault, which fails
	// on this very error.
	joined := strings.Join(logLines(t, liveVault), "\n")
	if !strings.Contains(joined, "refusing to capture") {
		t.Errorf("the refusal never reached vp.log — the alarm is missing exactly when it fires; got:\n%s", joined)
	}
	if !strings.Contains(joined, "iteration 210") {
		t.Errorf("clause 3: the logged refusal must carry the why; got:\n%s", joined)
	}
}
