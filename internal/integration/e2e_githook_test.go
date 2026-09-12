// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

// Go port of the retired test/e2e/githook/ bash tier (3 case scripts) —
// the git post-commit commit.msg reaper, driven through real `git commit`
// and the real built `vp` binary via testinfra.RunCLI.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// gitCmd runs a git subcommand in dir with GIT_CONFIG_GLOBAL/SYSTEM pinned to
// /dev/null (matching the retired scripts' isolation from the real
// developer's global gitconfig), failing the test on error.
func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// githookEnv is env.Environ() plus the GIT_CONFIG_GLOBAL/SYSTEM pin every
// githook case needs for its own `git commit` calls to stay isolated from
// the real developer's global gitconfig.
func githookEnv(env *testinfra.Env) []string {
	return env.Environ("GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
}

// TestIntegrationE2EGithookInitInstallsAndReapFires ports
// githook/01-init-installs-and-reap-fires.sh: the DoD, end to end through the
// real binary — `vp init` installs the post-commit hook, and a real `git
// commit -F commit.msg` typed WITHOUT the trailing `&& rm commit.msg` leaves
// NO commit.msg on disk. The message is multi-paragraph with trailing
// whitespace and a double blank line — the shape `git commit -F` rewrites
// under --cleanup=whitespace — so this also proves the positive path fires
// on this project's real message shape, not just byte-identical input.
func TestIntegrationE2EGithookInitInstallsAndReapFires(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary and real git commits; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "githook")
	projDir := filepath.Join(caseDir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, projDir, "init", "-q")
	gitCmd(t, projDir, "config", "user.name", "githook-e2e")
	gitCmd(t, projDir, "config", "user.email", "githook@test.invalid")

	if err := os.WriteFile(filepath.Join(projDir, "f.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, projDir, "add", "f.txt")
	gitCmd(t, projDir, "commit", "-q", "-m", "seed")

	testinfra.RunCLI(t, githookEnv(env), projDir, nil, "init", "--name", "githook-case").Must(t)

	hookPath := filepath.Join(projDir, ".git", "hooks", "post-commit")
	requireFileExists(t, hookPath)
	requireFileContains(t, hookPath, "vibe-palace:post-commit-reap")

	fi, err := os.Stat(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
		t.Fatalf("hook is not executable — git silently ignores a non-executable hook: %s", hookPath)
	}

	msg := "feat(e2e): a subject line\n\n" +
		"a body line with trailing spaces   \n" +
		"another body line\n\n\n" +
		"a paragraph after two blank lines\n"
	if err := os.WriteFile(filepath.Join(projDir, "commit.msg"), []byte(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "f.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, projDir, "add", "f.txt")

	// No `&& rm` — that omission is the whole hole this hook closes.
	gitCmd(t, projDir, "commit", "-q", "-F", "commit.msg")

	requireAbsent(t, filepath.Join(projDir, "commit.msg"))
}

// TestIntegrationE2EGithookCheckReportsMissingOnExistingClone ports
// githook/02-check-reports-missing-on-existing-clone.sh: an existing clone
// never re-runs `vp init`, so a missing hook has to be REPORTED by a path the
// operator already runs — `vp check`. Deleting the hook simulates the clone
// that predates the feature; `vp check` must name it, re-running `vp init`
// must repair it, and the repaired state must report clean.
//
// This is also the Q8/L5 handoff case: `vp check` must never construct the
// ONNX embedder or exec a real agent CLI (grok, claude, …) while doing so.
// The child env is hardened with BOTH named mechanisms at once — a
// sentinel-first PATH (testinfra.SentinelPATH) guarding against a real agent
// CLI exec, and a dead HTTPS_PROXY/HTTP_PROXY guarding against an ONNX cold
// download — following TestIntegrationMigrateLoadsNoModel's proven assertion
// shape.
func TestIntegrationE2EGithookCheckReportsMissingOnExistingClone(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary and real git commits; skipped under -short")
	}
	if runtime.GOOS == "windows" {
		t.Skip("SentinelPATH is sh-script based; unsupported on windows")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "githook")
	projDir := filepath.Join(caseDir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, projDir, "init", "-q")
	gitCmd(t, projDir, "config", "user.name", "githook-e2e")
	gitCmd(t, projDir, "config", "user.email", "githook@test.invalid")

	sentinelLog := testinfra.SentinelPATH(t)
	// SentinelPATH already t.Setenv'd PATH/VP_SENTINEL_LOG for THIS process;
	// githookEnv's base env.Environ() (built from os.Environ() at call time)
	// picks both up, and the dead proxy guards the ONNX embedder cold
	// download per the Q8 handoff.
	hardenedEnv := append(githookEnv(env),
		"HTTPS_PROXY=http://127.0.0.1:1", "HTTP_PROXY=http://127.0.0.1:1", "NO_PROXY=")

	testinfra.RunCLI(t, hardenedEnv, projDir, nil, "init", "--name", "githook-check-case").Must(t)
	hookPath := filepath.Join(projDir, ".git", "hooks", "post-commit")
	requireFileExists(t, hookPath)

	// The pre-hook clone.
	if err := os.Remove(hookPath); err != nil {
		t.Fatal(err)
	}

	checkMissing := testinfra.RunCLI(t, hardenedEnv, projDir, nil, "check")
	requireContains(t, checkMissing.Stdout, "Git commit.msg hook")
	requireContains(t, checkMissing.Stdout, "no post-commit hook")

	// Repair through the path the row names.
	testinfra.RunCLI(t, hardenedEnv, projDir, nil, "init", "--name", "githook-check-case").Must(t)
	requireFileExists(t, hookPath)

	checkClean := testinfra.RunCLI(t, hardenedEnv, projDir, nil, "check")
	requireContains(t, checkClean.Stdout, "commit.msg reaper installed")
	requireNotContains(t, checkClean.Stdout, "no post-commit hook")

	// Unlike TestIntegrationMigrateLoadsNoModel's cases (migrate/search),
	// where an input-validation gate refuses BEFORE the embedder is ever
	// constructed, `vp check`'s default run legitimately attempts embedder
	// construction whenever the vault is configured — that IS the row's job
	// (see internal/check/check.go's CheckEmbedder call in
	// gatherCheckResults). So this case cannot assert the HF cache directory
	// stays empty the way that one does: a genuine (and here, expected-to-
	// fail-closed) construction attempt legitimately creates it as a
	// bookkeeping side effect, with or without real network egress. The dead
	// HTTPS_PROXY/HTTP_PROXY above is what matters: it makes every attempt
	// fail closed at "proxyconnect ... connection refused" (confirmed while
	// writing this test), so no real bytes ever cross the network.
	//
	// `vp check`'s "MCP host: grok" row also DOES exec `grok mcp list` on a
	// grok-equipped machine — confirmed directly while writing this test,
	// which is exactly the Q8 handoff's "on a grok-equipped machine it can
	// spawn the real grok" risk. testinfra.SentinelPATH is what prevents
	// that: it prepends a directory of recording stub scripts onto PATH and
	// verifies (inside SentinelPATH itself, failing this test if not) that
	// they resolve FIRST — so this case does not need its own duplicate
	// assertion that no real binary ran; a non-empty sentinelLog is proof the
	// interception worked, not a failure. Logged for visibility only.
	if data, err := os.ReadFile(sentinelLog); err == nil && len(data) != 0 {
		t.Logf("sentinel intercepted (not exec'd for real): %s", data)
	}
}

// TestIntegrationE2EGithookInitRefusesForeignHookAndSharedHookspath ports
// githook/03-init-refuses-foreign-hook-and-shared-hookspath.sh: the two
// refusals, through the real binary.
//
//  1. A pre-existing post-commit hook this project did not write is never
//     clobbered and never appended to.
//  2. A repo with core.hooksPath set is never written to at all — installing
//     a vibe-palace hook into a directory shared by every repo the operator
//     owns is worse than a missing hook.
//
// Both refusals must leave `vp init` at exit 0: a hook that could not be
// installed is a row, never an abort.
func TestIntegrationE2EGithookInitRefusesForeignHookAndSharedHookspath(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary and real git commits; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "githook")

	// --- 1. foreign hook ---
	foreign := filepath.Join(caseDir, "foreign")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, foreign, "init", "-q")
	hooksDir := filepath.Join(foreign, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreignHook := filepath.Join(hooksDir, "post-commit")
	foreignBody := []byte("#!/bin/sh\n# somebody else wrote this\necho hi\n")
	if err := os.WriteFile(foreignHook, foreignBody, 0o755); err != nil {
		t.Fatal(err)
	}
	beforeSHA := sha256Hex(t, foreignHook)

	testinfra.RunCLI(t, githookEnv(env), foreign, nil, "init", "--name", "githook-foreign-case").Must(t)
	requireFileNotContains(t, foreignHook, "vibe-palace:post-commit-reap")

	afterSHA := sha256Hex(t, foreignHook)
	if beforeSHA != afterSHA {
		t.Fatalf("a foreign post-commit hook was modified: %s", foreignHook)
	}

	// --- 2. shared core.hooksPath ---
	sharedHooks := filepath.Join(caseDir, "shared-hooks")
	if err := os.MkdirAll(sharedHooks, 0o755); err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(caseDir, "shared")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, shared, "init", "-q")
	gitCmd(t, shared, "config", "core.hooksPath", sharedHooks)

	sharedResult := testinfra.RunCLI(t, githookEnv(env), shared, nil, "init", "--name", "githook-shared-case")
	sharedResult.Must(t)

	requireAbsent(t, filepath.Join(sharedHooks, "post-commit"))
	requireAbsent(t, filepath.Join(shared, ".git", "hooks", "post-commit"))
	requireContains(t, sharedResult.Stdout, "core.hooksPath")
}

func sha256Hex(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
