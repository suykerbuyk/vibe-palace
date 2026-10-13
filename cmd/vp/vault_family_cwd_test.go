// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// These tests pin ADR-012 rule 6 (amended 2026-10-12): the vault GIT family
// (pull/push/sync/commit/tidy/status) resolves its target cwd-aware via
// ResolveVaultBinding when no --vault is given, while the file-CRUD family and
// the lifecycle commands stay global-only.
//
// Every test enters through the ACTUAL command (cmdVaultStatus().Run etc.), not
// through ResolveVaultPath/ResolveVaultBinding directly — a direct-resolver call
// returns the tier-1/flag answer today and so cannot distinguish old wiring from
// new. The fixtures honor the walk invariant: the project checkout sits UNDER
// the sandboxed $HOME (the marker walk stops at $HOME), and every target the
// family must accept is a real git repo with a surface stamp.

// makeGitVaultWithOrigin builds a real, stamped vault git repo at dir with a
// bare origin, mirroring setupVaultWithOrigin but parametrized on the path.
func makeGitVaultWithOrigin(t *testing.T, dir string) {
	t.Helper()
	if err := surface.WriteFormat(dir, surface.RequiredDataFormat); err != nil {
		t.Fatalf("stamp %s: %v", dir, err)
	}
	bare := t.TempDir()
	gitRun(t, bare, "init", "--bare", "-b", "main")
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@test.com")
	gitRun(t, dir, "config", "user.name", "Test")
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeTestFile(t, filepath.Join(dir, ".gitignore"), ".vp-locks/\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed")
	gitRun(t, dir, "push", "-u", "origin", "main")
}

// familyEnv sets the hermetic 4-var sandbox with a global vault (real git repo)
// and a global config pointing at it. Returns ($HOME, config path, global vault).
func familyEnv(t *testing.T) (home, cfgPath, global string) {
	t.Helper()
	global = t.TempDir()
	makeGitVaultWithOrigin(t, global)
	setupTestVaultEnvAt(t, global)
	home = os.Getenv("HOME")
	var err error
	cfgPath, err = storage.VaultConfigFilePath()
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	return home, cfgPath, global
}

// familyCheckout writes a project checkout UNDER $HOME. vaultPath empty means an
// identity-only marker (no vault_path line).
func familyCheckout(t *testing.T, home, name, vaultPath string) string {
	t.Helper()
	co := filepath.Join(home, "code", name)
	var body strings.Builder
	if vaultPath != "" {
		body.WriteString("vault_path = \"" + vaultPath + "\"\n\n")
	}
	body.WriteString("[project]\nname = \"" + name + "\"\n")
	writeTestFile(t, filepath.Join(co, ".vibe-palace.toml"), body.String())
	return co
}

// TestVaultFamily_ResolvesCwdBinding is the git-family's contract pin: from a
// checkout whose marker sets vault_path, the family resolves THAT vault.
// MUTATION: wire the six sites back to the global fallback and this goes red
// (it resolves the global vault instead).
func TestVaultFamily_ResolvesCwdBinding(t *testing.T) {
	home, _, global := familyEnv(t)
	x := t.TempDir()
	makeGitVaultWithOrigin(t, x)
	co := familyCheckout(t, home, "projx", x)
	t.Chdir(co)

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultStatus(), "--no-fetch")
	if code != cli.ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "vault_path = "+x) {
		t.Errorf("did not resolve the cwd-bound vault %q; stderr:\n%s", x, stderr)
	}
	if strings.Contains(stderr, "vault_path = "+global) {
		t.Errorf("resolved the GLOBAL vault %q, not the project's own", global)
	}
	if !strings.Contains(stderr, "vault_path source = cwd:") {
		t.Errorf("source is not cwd:; stderr:\n%s", stderr)
	}
}

// TestVaultFamily_ResolvesProjectVaultsBinding: tier-2 [project_vaults].
func TestVaultFamily_ResolvesProjectVaultsBinding(t *testing.T) {
	home, cfg, _ := familyEnv(t)
	x := t.TempDir()
	makeGitVaultWithOrigin(t, x)
	appendToTestFile(t, cfg, "\n[project_vaults]\nprojy = \""+x+"\"\n")
	co := familyCheckout(t, home, "projy", "") // marker names projy, NO vault_path
	t.Chdir(co)

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultStatus(), "--no-fetch")
	if code != cli.ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "vault_path = "+x) {
		t.Errorf("did not resolve the [project_vaults] binding %q; stderr:\n%s", x, stderr)
	}
	if !strings.Contains(stderr, "vault_path source = binding:") {
		t.Errorf("source is not binding:; stderr:\n%s", stderr)
	}
}

// TestVaultFamily_FlagOverridesBinding: --vault still wins over a cwd marker.
func TestVaultFamily_FlagOverridesBinding(t *testing.T) {
	home, _, _ := familyEnv(t)
	x := t.TempDir()
	makeGitVaultWithOrigin(t, x)
	y := t.TempDir()
	makeGitVaultWithOrigin(t, y)
	co := familyCheckout(t, home, "projf", x)
	t.Chdir(co)

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultStatus(), "--vault", y, "--no-fetch")
	if code != cli.ExitOK {
		t.Fatalf("exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "vault_path = "+y) {
		t.Errorf("--vault did not override to %q; stderr:\n%s", y, stderr)
	}
	if !strings.Contains(stderr, "vault_path source = flag:--vault") {
		t.Errorf("source is not flag:--vault; stderr:\n%s", stderr)
	}
}

// TestVaultFamily_NoGlobalVaultPathButBindingSucceeds (operator case 3): global
// config has NO vault_path, but a [project_vaults] binding does. It succeeds
// because staleProjectBinding fail-opens when it cannot resolve the default
// vault — not because tier 2 validates the default.
func TestVaultFamily_NoGlobalVaultPathButBindingSucceeds(t *testing.T) {
	home, cfg, _ := familyEnv(t)
	x := t.TempDir()
	makeGitVaultWithOrigin(t, x)
	writeTestFile(t, cfg, "[project_vaults]\nprojn = \""+x+"\"\n") // no vault_path at all
	co := familyCheckout(t, home, "projn", "")
	t.Chdir(co)

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultStatus(), "--no-fetch")
	if code != cli.ExitOK {
		t.Fatalf("family must succeed with the binding when global vault_path is absent; exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "vault_path = "+x) {
		t.Errorf("did not resolve the binding %q; stderr:\n%s", x, stderr)
	}
}

// TestVaultFamily_NoTierResolvesErrorNotPanic: nothing resolves → a clean error,
// not a panic, and the global failure is surfaced verbatim.
func TestVaultFamily_NoTierResolvesErrorNotPanic(t *testing.T) {
	home, cfg, _ := familyEnv(t)
	writeTestFile(t, cfg, "# a global config with no vault_path and no bindings\n")
	co := filepath.Join(home, "plain")
	if err := os.MkdirAll(co, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(co) // no marker here

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultStatus(), "--no-fetch")
	if code == cli.ExitOK {
		t.Fatalf("want a non-zero exit when no tier resolves; stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "vault_path not set in") {
		t.Errorf("error does not name the missing vault_path; stderr:\n%s", stderr)
	}
}

// TestVaultFamily_ParityNoteOnNonGlobalSource: a non-global resolution prints
// the CLI/MCP parity note to stderr; a global one does not. Driven through a
// MUTATOR (tidy) to prove the note is not status-only.
func TestVaultFamily_ParityNoteOnNonGlobalSource(t *testing.T) {
	home, _, _ := familyEnv(t)
	x := t.TempDir()
	makeGitVaultWithOrigin(t, x)
	co := familyCheckout(t, home, "projp", x)

	t.Chdir(co)
	_, stderr, _ := runVaultCmdCapturingBoth(t, cmdVaultTidy(), "--dry-run")
	if !strings.Contains(stderr, "MCP vp_vault_*") {
		t.Errorf("non-global source must print the parity note; stderr:\n%s", stderr)
	}

	t.Chdir(home) // no marker: resolves the global vault
	_, stderrG, _ := runVaultCmdCapturingBoth(t, cmdVaultTidy(), "--dry-run")
	if strings.Contains(stderrG, "MCP vp_vault_*") {
		t.Errorf("global source must NOT print the parity note; stderr:\n%s", stderrG)
	}
}

// TestVaultFamily_MutatorSyncRefusesNestedRepoRoot: refuseUnlessOwnGitRepo must
// catch a resolved root nested inside another repo even for sync (registered
// unwrapped). MUTATION: gate the root check on MutatesVault or vaultFlagSource.
func TestVaultFamily_MutatorSyncRefusesNestedRepoRoot(t *testing.T) {
	home, _, _ := familyEnv(t)
	enclosing := t.TempDir()
	gitRun(t, enclosing, "init", "-b", "main")
	nested := filepath.Join(enclosing, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	co := familyCheckout(t, home, "projnest", nested)
	t.Chdir(co)

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultSync(), "--dry-run")
	if code != cli.ExitUser {
		t.Fatalf("want ExitUser for a nested-repo root; got %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "inside another repository") {
		t.Errorf("refusal does not name the nested-repo cause; stderr:\n%s", stderr)
	}
}

// TestVaultFamily_GlobalSourceButNestedRootRefused: the root check runs even
// when the source is global. MUTATION: gate the check on source.
func TestVaultFamily_GlobalSourceButNestedRootRefused(t *testing.T) {
	enclosing := t.TempDir()
	gitRun(t, enclosing, "init", "-b", "main")
	nested := filepath.Join(enclosing, "vault")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := surface.WriteFormat(nested, surface.RequiredDataFormat); err != nil {
		t.Fatal(err)
	}
	setupTestVaultEnvAt(t, nested) // global vault_path = a nested dir
	plain := filepath.Join(os.Getenv("HOME"), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(plain) // no marker → tier-3 global = nested

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultTidy(), "--dry-run")
	if code != cli.ExitUser {
		t.Fatalf("want ExitUser for a nested global root; got %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "inside another repository") {
		t.Errorf("refusal does not name the nested-repo cause; stderr:\n%s", stderr)
	}
}

// TestVaultFamily_SameRootNotRefused: a legitimate co-located vault (marker
// vault_path == global) is NOT a false positive.
func TestVaultFamily_SameRootNotRefused(t *testing.T) {
	home, _, global := familyEnv(t)
	co := familyCheckout(t, home, "projsame", global)
	t.Chdir(co)

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultStatus(), "--no-fetch")
	if code != cli.ExitOK {
		t.Fatalf("a co-located vault must not be refused; exit = %d; stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "vault_path = "+global) {
		t.Errorf("did not resolve the (shared) vault root %q; stderr:\n%s", global, stderr)
	}
}

// TestVaultLifecycle_CwdMarkerDoesNotRetarget: the lifecycle commands
// (project-delete/copy/rename) stay global-only — a cwd marker must not change
// their target, and they must not print the git-family parity note.
func TestVaultLifecycle_CwdMarkerDoesNotRetarget(t *testing.T) {
	global := setupVaultWithOrigin(t)
	scratch := overrideTreeAt(t) // chdirs into a tree binding vault_path=scratch
	if global == scratch {
		t.Fatalf("degenerate fixture: global == scratch (%q)", global)
	}
	cases := []struct {
		name string
		cmd  *cli.Command
		args []string
	}{
		{"project-delete", cmdVaultProject(), []string{"delete", "someproj", "--discard", "--dry-run"}},
		{"copy", cmdVaultCopy(), []string{"someproj", "--from", "git@example.invalid:x/src.git", "--dry-run"}},
		{"rename", cmdVaultRename(), []string{"old-name", "new-name", "--dry-run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, _ := runVaultCmdCapturingBoth(t, tc.cmd, tc.args...)
			if !strings.Contains(stderr, "vault_path = "+global) {
				t.Errorf("did not resolve the GLOBAL vault; stderr:\n%s", stderr)
			}
			if strings.Contains(stderr, "vault_path = "+scratch) {
				t.Errorf("RETARGETED to the cwd vault %q; stderr:\n%s", scratch, stderr)
			}
			if strings.Contains(stderr, "MCP vp_vault_*") {
				t.Errorf("lifecycle must not print the git-family parity note; stderr:\n%s", stderr)
			}
		})
	}
}

// appendToTestFile appends body to an existing file (test helper).
func appendToTestFile(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
}
