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

// TestHookRejectedBindingCapturesNothing is the ADR-012 half of iteration
// 210: a [project_vaults] binding the resolver REFUSES must capture nothing,
// never fall back to the global (live) vault.
//
// MUTATION CONTRACT: narrow runHook's refusal arm back to
// ErrSwallowedVaultPath and both subtests go RED on the live-vault assertion —
// the binding refusals wrap only ErrVaultBindingRejected, so the hook's
// "absent config" fallback reaches OpenVaultGlobal().
func TestHookRejectedBindingCapturesNothing(t *testing.T) {
	cases := map[string]func(tmp string) (marker string, binding string){
		// The bound vault does not exist on this host.
		"missing_target": func(tmp string) (string, string) {
			return "[project]\nname = \"qa\"\n", filepath.Join(tmp, "no-such-vault")
		},
		// The checkout's own vault_path disagrees with the host binding (R2).
		"tiers_disagree": func(tmp string) (string, string) {
			bound := filepath.Join(tmp, "quantum-vault")
			mustVault(t, bound)
			stale := filepath.Join(tmp, "stale-vault")
			mustVault(t, stale)
			return "vault_path = \"" + stale + "\"\n[project]\nname = \"qa\"\n", bound
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := t.TempDir()
			home := filepath.Join(tmp, "home")
			configDir := filepath.Join(home, ".config")
			liveVault := filepath.Join(tmp, "live-vault")
			projectDir := filepath.Join(tmp, "qa-checkout")
			for _, d := range []string{filepath.Join(configDir, "vibe-palace"), liveVault, projectDir} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", configDir)

			marker, bound := mk(tmp)
			writeTestFile(t, filepath.Join(configDir, "vibe-palace", "config.toml"),
				"vault_path = \""+liveVault+"\"\n\n[project_vaults]\nqa = \""+bound+"\"\n")
			writeTestFile(t, filepath.Join(projectDir, ".vibe-palace.toml"), marker)

			body, err := json.Marshal(map[string]string{
				"hook_event_name": "Stop",
				"session_id":      "binding-session",
				"cwd":             projectDir,
			})
			if err != nil {
				t.Fatal(err)
			}
			stdinFile := filepath.Join(tmp, "payload.json")
			writeTestFile(t, stdinFile, string(body))
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

			if entries, rerr := os.ReadDir(filepath.Join(liveVault, "Projects")); rerr == nil && len(entries) > 0 {
				t.Fatalf("the hook captured into the LIVE vault (%d project dir(s)) — a rejected binding must never reach the global fallback", len(entries))
			}
			if code != cli.ExitOK {
				t.Errorf("exit code = %d, want ExitOK (a hook failure is reported, never blocking)", code)
			}
			if joined := strings.Join(logLines(t, liveVault), "\n"); !strings.Contains(joined, "refusing to capture") {
				t.Errorf("the refusal never reached vp.log; got:\n%s", joined)
			}
		})
	}
}

// mustVault makes dir a vault: a directory holding .vibe-palace/vault.toml.
func mustVault(t *testing.T, dir string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, ".vibe-palace", "vault.toml"), "format = 2\n")
}

// TestSkillsShowDoesNotDegradeARejectedBinding: a refused binding is a usage
// error, never "no vault configured". MUTATION CONTRACT: make the missing-target
// refusal wrap fs.ErrNotExist and this goes RED.
func TestSkillsShowDoesNotDegradeARejectedBinding(t *testing.T) {
	env := newSkillsShowEnv(t)
	writeTestFile(t, filepath.Join(env.home, ".config", "vibe-palace", "config.toml"),
		"vault_path = \""+filepath.ToSlash(env.vault)+"\"\n\n[project_vaults]\nproj = \""+filepath.ToSlash(filepath.Join(env.home, "no-such-vault"))+"\"\n")
	writeTestFile(t, filepath.Join(env.proj, ".vibe-palace.toml"), "[project]\nname = \"proj\"\n")

	_, _, note, code := skillsShowScope(env.proj, "", false, "", "")
	if code != cli.ExitUser || strings.Contains(note, skillsShowNoVaultNote) || !strings.Contains(note, "no-such-vault") {
		t.Errorf("code %d note %q: a rejected binding must stay a usage error naming the target", code, note)
	}
}
