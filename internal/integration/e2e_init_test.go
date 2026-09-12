// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

// Go port of the retired test/e2e/init/ bash tier (8 case scripts). Each test
// below is a 1:1 port of one NN-*.sh case: same scenario, same assertions,
// against the real built `vp` binary via testinfra.RunCLI instead of a
// sourced bash function. See test/e2e/init/lib.sh's fresh_home / run_vp
// contract in the (now-deleted) bash harness for the original wording this
// was ported from.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// TestIntegrationE2EInitPositionalProjectDir ports
// init/01-positional-project-dir.sh: `vp init` in a git-inited project
// directory (default vault path under HOME) creates .vibe-palace.toml in the
// cwd and creates the vault under HOME. init has no Templates pass, so a
// fresh install creates neither Templates/ nor its lock.
func TestIntegrationE2EInitPositionalProjectDir(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "init")
	projDir := filepath.Join(caseDir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, projDir)

	testinfra.RunCLI(t, env.Environ(), projDir, nil, "init", "--name", "positional-project-dir").Must(t)

	requireFileExists(t, filepath.Join(projDir, ".vibe-palace.toml"))
	requireDirExists(t, filepath.Join(env.Home, "vibe-palace-vault"))
	requireDirExists(t, filepath.Join(env.Home, "vibe-palace-vault", ".git"))
	cfg := filepath.Join(env.XDGConfigHome, "vibe-palace", "config.toml")
	requireFileExists(t, cfg)
	requireFileContains(t, cfg, "vault_path")

	requireAbsent(t, filepath.Join(env.Home, "vibe-palace-vault", "Templates"))
	requireAbsent(t, filepath.Join(env.Home, "vibe-palace-vault", ".vibe-palace", "templates.lock"))
}

// TestIntegrationE2EInitExplicitVaultPath ports init/02-explicit-vault-path.sh:
// --vault-path is honored — the vault lands at the requested path, NOT under
// HOME.
func TestIntegrationE2EInitExplicitVaultPath(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "init")
	projDir := filepath.Join(caseDir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, projDir)
	customVault := filepath.Join(caseDir, "custom-vault")

	testinfra.RunCLI(t, env.Environ(), projDir, nil,
		"init", "--vault-path", customVault, "--name", "explicit-vault").Must(t)

	requireDirExists(t, customVault)
	requireDirExists(t, filepath.Join(customVault, ".git"))
	requireAbsent(t, filepath.Join(env.Home, "vibe-palace-vault"))
	cfg := filepath.Join(env.XDGConfigHome, "vibe-palace", "config.toml")
	requireFileExists(t, cfg)
	requireFileContains(t, cfg, customVault)
}

// TestIntegrationE2EInitPositionalAndVault ports
// init/03-positional-and-vault.sh: both a positional (project dir) AND
// --vault-path (vault) must land independently.
func TestIntegrationE2EInitPositionalAndVault(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "init")
	myproj := filepath.Join(caseDir, "myproj")
	if err := os.MkdirAll(myproj, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, myproj)
	customVault := filepath.Join(caseDir, "custom-vault")

	testinfra.RunCLI(t, env.Environ(), caseDir, nil,
		"init", myproj, "--vault-path", customVault, "--name", "both-args").Must(t)

	requireFileExists(t, filepath.Join(myproj, ".vibe-palace.toml"))
	requireDirExists(t, filepath.Join(customVault, ".git"))
	requireAbsent(t, filepath.Join(env.Home, "vibe-palace-vault"))
}

// TestIntegrationE2EInitNonexistentPositional ports
// init/04-nonexistent-positional.sh: a regression lock for Fix 1 — `vp init
// <path-that-does-not-exist>` must abort with ExitUser BEFORE any filesystem
// writes, with a stderr hint pointing at --vault-path.
func TestIntegrationE2EInitNonexistentPositional(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "init")
	bogus := filepath.Join(caseDir, "does-not-exist", "vault-ish")

	r := testinfra.RunCLI(t, env.Environ(), caseDir, nil, "init", bogus)

	if r.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1 (cli.ExitUser)", r.ExitCode)
	}
	requireContains(t, r.Stderr, "does not exist")
	requireContains(t, r.Stderr, "vault-path")

	// Protective assertions: NO filesystem writes happened.
	requireAbsent(t, filepath.Join(env.XDGConfigHome, "vibe-palace", "config.toml"))
	requireAbsent(t, filepath.Join(env.Home, "vibe-palace-vault"))
}

// TestIntegrationE2EInitNoGitFlag ports init/05-no-git-flag.sh: --no-git
// disables vault git-init. The vault dir still gets created, but without a
// .git subdir.
func TestIntegrationE2EInitNoGitFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "init")
	projDir := filepath.Join(caseDir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, projDir)

	testinfra.RunCLI(t, env.Environ(), projDir, nil, "init", "--no-git", "--name", "no-git-case").Must(t)

	requireDirExists(t, filepath.Join(env.Home, "vibe-palace-vault"))
	requireAbsent(t, filepath.Join(env.Home, "vibe-palace-vault", ".git"))
}

// TestIntegrationE2EInitCleanupIsolation ports init/06-cleanup-isolation.sh,
// the harness's meta-safety check: HOME must resolve entirely under the
// harness sandbox, never the real user home.
//
// Unlike the bash harness — where fresh_home was a plain env-var
// reassignment the rest of the suite trusted blindly — testinfra.IsolateEnv
// keys HOME off t.TempDir() by construction, so "HOME is under the harness
// tmpdir" cannot silently regress here the way it could in bash (e.g. a
// missing `export`). This is a one-line sanity check, not a meaningful
// regression guard, kept for documentation value per the original case's own
// stated purpose.
func TestIntegrationE2EInitCleanupIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns no subprocess but matches the tier's -short convention")
	}
	env := testinfra.IsolateEnv(t)

	tmpRoot := os.TempDir()
	if real, err := filepath.EvalSymlinks(tmpRoot); err == nil {
		tmpRoot = real
	}
	resolvedHome := env.Home
	if real, err := filepath.EvalSymlinks(env.Home); err == nil {
		resolvedHome = real
	}
	if !strings.HasPrefix(resolvedHome, tmpRoot) {
		t.Fatalf("ISOLATION FAILURE: HOME=%q is not inside the harness tmpdir %q", env.Home, tmpRoot)
	}

	// The bash case also compared $HOME against `getent passwd
	// "$(id -un)"` — a lookup of the real OS user record that never reads
	// $HOME. There is no equivalent in Go's standard library: os.UserHomeDir
	// reads the very $HOME env var IsolateEnv's t.Setenv just overrode for
	// THIS process, so comparing against it would be tautological (it would
	// always equal env.Home by construction, not by coincidence) rather than
	// a real second, independent check. The HOME-under-the-harness-tmpdir
	// assertion above is the meaningful half of this case and is preserved
	// on its own.
}

// TestIntegrationE2EInitReinitIdempotent ports init/07-reinit-idempotent.sh:
// running `vp init` a second time over an already-initialized project must
// converge — exit 0, no [FAIL] row, and the project config file
// byte-identical to what the first run left. The vault-side artifacts a
// re-init must not lose (project config, commands/skills README stubs) are
// asserted after both runs.
func TestIntegrationE2EInitReinitIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "init")
	projDir := filepath.Join(caseDir, "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, projDir)

	vaultRoot := filepath.Join(env.Home, "vibe-palace-vault")
	assertVaultArtifacts := func() {
		t.Helper()
		requireFileExists(t, projectConfigPath(t, vaultRoot, "reinit-case"))
		requireFileExists(t, filepath.Join(vaultRoot, "Projects", "reinit-case", "commands", "README.md"))
		requireFileExists(t, filepath.Join(vaultRoot, "Projects", "reinit-case", "skills", "README.md"))
	}

	r1 := testinfra.RunCLI(t, env.Environ(), projDir, nil, "init", "--name", "reinit-case")
	r1.Must(t)
	requireFileExists(t, filepath.Join(projDir, ".vibe-palace.toml"))
	requireNotContains(t, r1.Stdout, "[FAIL]")
	assertVaultArtifacts()

	marker1, err := os.ReadFile(filepath.Join(projDir, ".vibe-palace.toml"))
	if err != nil {
		t.Fatal(err)
	}

	r2 := testinfra.RunCLI(t, env.Environ(), projDir, nil, "init", "--name", "reinit-case")
	r2.Must(t)
	requireNotContains(t, r2.Stdout, "[FAIL]")

	marker2, err := os.ReadFile(filepath.Join(projDir, ".vibe-palace.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(marker1) != string(marker2) {
		t.Fatalf("second vp init rewrote .vibe-palace.toml:\n--- run1 ---\n%s\n--- run2 ---\n%s", marker1, marker2)
	}

	assertVaultArtifacts()

	// The re-init says out loud what it deliberately does NOT do, and names
	// the owners: `vp commands upgrade` for stale shims, and the explicit
	// reset verbs for a vault Templates/ override.
	requireContains(t, r2.Stdout, "vp commands upgrade")
	requireContains(t, r2.Stdout, "vp commands reset")
	requireContains(t, r2.Stdout, "vp skills reset")
}

// TestIntegrationE2EInitSkillShimFallbackReachable ports
// init/08-skill-shim-fallback-reachable.sh: the persona shims' MCP-less
// fallback, run exactly as an agent's shell would. Every persona shim ends
// with a fallback for a session where the vp_skill tool cannot be loaded:
// run `vp skills show <name>` from the project directory. This proves the
// literal fallback command text works — serving the built-in first, then a
// project override once one exists — and that no shim names a vault path.
func TestIntegrationE2EInitSkillShimFallbackReachable(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary; skipped under -short")
	}
	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "init")
	projDir := filepath.Join(caseDir, "proj")
	for _, d := range []string{".cursor/rules", ".grok"} {
		if err := os.MkdirAll(filepath.Join(projDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gitInit(t, projDir)

	testinfra.RunCLI(t, env.Environ(), projDir, nil, "init", "--name", "fallback-case").Must(t)

	rule := filepath.Join(projDir, ".cursor", "rules", "vps-chair.mdc")
	requireFileExists(t, rule)

	// Extract the plain `vp skills show <name>` command from the rendered
	// .mdc, the same way the bash case's `sed -n 's/.../p'` did: per-line,
	// first backtick-delimited capture only, so the `vp skills show chair
	// --section <ref>` span on another line never matches.
	data, err := os.ReadFile(rule)
	if err != nil {
		t.Fatal(err)
	}
	const prefix, suffix = "`vp skills show ", "`"
	var cmds []string
	for _, line := range strings.Split(string(data), "\n") {
		start := strings.Index(line, prefix)
		if start < 0 {
			continue
		}
		rest := line[start+len(prefix):]
		end := strings.Index(rest, suffix)
		if end < 0 {
			continue
		}
		name := rest[:end]
		if strings.ContainsAny(name, " \t") {
			continue // not a bare "vp skills show <name>" span
		}
		cmds = append(cmds, "vp skills show "+name)
	}
	cmd := strings.Join(cmds, "\n")
	if cmd != "vp skills show chair" {
		t.Fatalf("fallback command in vps-chair.mdc is %q, want %q\n%s", cmd, "vp skills show chair", data)
	}

	fields := strings.Fields(cmd) // ["vp", "skills", "show", "chair"]
	args := fields[1:]

	res := testinfra.RunCLI(t, env.Environ(), projDir, nil, args...)
	res.Must(t)
	requireContains(t, res.Stdout, "# skill: chair | source: embedded")

	// With a project override in the vault, the same command run from the
	// project directory serves it — the tier vp_skill serves there.
	override := filepath.Join(env.Home, "vibe-palace-vault", "Projects", "fallback-case", "skills", "chair", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(override, []byte("# Chair override\n\nfallback-case chair body\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res2 := testinfra.RunCLI(t, env.Environ(), projDir, nil, args...)
	res2.Must(t)
	requireContains(t, res2.Stdout, "# skill: chair | source: project")
	requireContains(t, res2.Stdout, "fallback-case chair body")

	// No shim names a vault path.
	for _, d := range []string{".cursor", ".grok", ".claude"} {
		requireDirExists(t, filepath.Join(projDir, d))
	}
	vaultRoot := filepath.Join(env.Home, "vibe-palace-vault")
	err = filepath.WalkDir(projDir, func(path string, de os.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(projDir, path)
		top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
		if top != ".cursor" && top != ".grok" && top != ".claude" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(string(data), "Templates/") || strings.Contains(string(data), vaultRoot) {
			t.Errorf("%s names a vault path", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
