// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// TestIntegrationTemplateMaterializeAndReconcile is the full-stack,
// acceptance-criteria gate for the override-only vault Templates/ reconcile
// (ADR-008, Design B). It drives the real `vp` CLI binary (built once per test
// run) end to end: a fresh `vp init` leaves Templates/ alone entirely (no
// directory, no lock), an override of wrap.md survives a subsequent
// `vp config sync --yes` with no lock written, and a first install onto a
// vault that already holds an override neither fails nor touches it.
// TestIntegrationTemplateOverrideSurvivesSync covers the git paths that used
// to lose the override, and TestIntegrationTemplateProvenance the
// shipped-version manifest that decides what is vp's.
//
// We shell out to the built `vp` binary rather than importing unexported
// `cmd/vp` helpers: cmd/vp is package main and not importable from
// internal/integration, and building+exec-ing the binary is the most
// faithful end-to-end surface.
func TestIntegrationTemplateMaterializeAndReconcile(t *testing.T) {
	bin := buildVPBinary(t)

	// --- Part 1: fresh init leaves Templates/ override-only (no mirror) ---
	env := setupFreshEnv(t)
	runVP(t, bin, env, nil, "init", env.projectDir,
		"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")

	assertInitLeavesTemplatesUntouched(t, env)

	// --- Part 2: a genuine override survives `vp config sync --yes` ---
	// Under override-only there is nothing on disk to edit after init, so we
	// plant an override (bytes vp never shipped). The reconciler must keep it
	// — no clobber, no .bak, no prompt, and no templates.lock written.
	wrapPath := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")

	const userEdit = "# USER EDITED WRAP\n\nmy custom wrap brief\n"
	seedTrackedOverride(t, env.vaultPath, "commands/wrap.md", []byte(userEdit))

	out := runVP(t, bin, env, nil, "config", "sync", "--yes",
		"--project-root", env.projectDir)

	if got, _ := os.ReadFile(wrapPath); string(got) != userEdit {
		t.Errorf("step 2: override was clobbered by sync --yes\n got  %q\n want %q",
			got, userEdit)
	}
	if _, err := os.Stat(wrapPath + ".bak"); err == nil {
		t.Error("step 2: kept override must not produce .bak")
	}
	if !strings.Contains(out, "Templates/commands/wrap.md operator override of a built-in (kept)") {
		t.Errorf("step 2: no kept-override row:\n%s", out)
	}
	if strings.Contains(out, "[Prompt]") {
		t.Errorf("step 2: sync prompted about an override:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(env.vaultPath, retiredLockRel)); !os.IsNotExist(err) {
		t.Errorf("step 2: sync wrote %s (stat err=%v)", retiredLockRel, err)
	}

	// A first install onto a vault that ALREADY holds a Templates/ override —
	// a hand-written wrap.md — and a
	// .gitignore missing canonical lines. At 2581f4b init's Templates pass
	// returned "received ActionPrompt ... orchestrator must resolve Prompt
	// actions before Apply" and exited 2; runVP fails the test on any non-zero
	// exit, so that is the regression assertion. init must also top the
	// .gitignore up (the one job of the deleted pass that survives, now in the
	// Vault step) without the surface stamp's unrecognized-path warning.
	t.Run("first-install-ignores-vault-overrides", func(t *testing.T) {
		env := setupFreshEnv(t)
		wrap := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")
		if err := os.MkdirAll(filepath.Dir(wrap), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wrap, []byte(userEdit), 0o644); err != nil {
			t.Fatal(err)
		}
		gi := filepath.Join(env.vaultPath, ".gitignore")
		if err := os.WriteFile(gi, []byte("my-scratch/\n*.bak\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		out := runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", "tpl-first", "--vault-path", env.vaultPath, "--no-git")

		if got, err := os.ReadFile(wrap); err != nil || string(got) != userEdit {
			t.Errorf("first install touched the override: err=%v\n got  %q\n want %q", err, got, userEdit)
		}
		for _, side := range []string{".bak", ".new"} {
			if _, err := os.Stat(wrap + side); err == nil {
				t.Errorf("first install wrote %s", wrap+side)
			}
		}
		if _, err := os.Stat(filepath.Join(env.vaultPath, retiredLockRel)); !os.IsNotExist(err) {
			t.Errorf("first install created templates.lock (stat err=%v)", err)
		}
		if strings.Contains(out, "] Templates:") {
			t.Errorf("init rendered a Templates row:\n%s", out)
		}
		if !strings.Contains(out, "vault .gitignore: added") {
			t.Errorf("init did not report the .gitignore top-up:\n%s", out)
		}
		if strings.Contains(out, "unrecognized path") {
			t.Errorf("the .gitignore top-up tripped the surface stamp's unrecognized-path warning:\n%s", out)
		}
		giData, err := os.ReadFile(gi)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(giData), "my-scratch/\n*.bak\n") {
			t.Errorf(".gitignore seed not preserved at the top:\n%s", giData)
		}
		for _, pat := range []string{"*.new", ".vp-locks/", "palace/.local/"} {
			if !containsLine(string(giData), pat) {
				t.Errorf(".gitignore not topped up with %q:\n%s", pat, giData)
			}
		}

		// The override is still there for its real owner: `vp config sync`
		// sees it and keeps it — no prompt, no lock.
		plan := runVP(t, bin, env, nil, "config", "sync", "--dry-run",
			"--project-root", env.projectDir)
		if !strings.Contains(plan, "Templates/commands/wrap.md operator override of a built-in (kept)") {
			t.Errorf("config sync --dry-run does not see the override:\n%s", plan)
		}
	})
}

// retiredLockRel is where vp binaries before the shipped-version manifest kept
// the host-local templates.lock. No vp from this release reads or writes it.
const retiredLockRel = ".vibe-palace/templates.lock"

// seedTrackedOverride writes data to the vault Templates/ target for
// embeddedRel: an operator's override under the override-only model, where a
// fresh init leaves no mirror to edit. It plants the file only — provenance is
// decided by the binary alone, and no host-local lock exists to record it.
func seedTrackedOverride(t *testing.T, vaultPath, embeddedRel string, data []byte) {
	t.Helper()
	target := filepath.Join(vaultPath, "Templates", filepath.FromSlash(embeddedRel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
}

// assertInitLeavesTemplatesUntouched checks the post-`vp init` contract: init
// has no Templates pass, so there is no Templates/ directory and no
// templates.lock at all; NO embedded resource is mirrored (the embedded floor
// is served directly); the vault .gitignore still carries the *.bak / *.new
// patterns; and the current project's commands/ + skills/ README stubs exist
// (scaffold mode is unchanged).
func assertInitLeavesTemplatesUntouched(t *testing.T, env *testEnv) {
	t.Helper()

	if _, err := os.Stat(filepath.Join(env.vaultPath, "Templates")); !os.IsNotExist(err) {
		t.Errorf("init created <vault>/Templates (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(env.vaultPath, retiredLockRel)); !os.IsNotExist(err) {
		t.Errorf("init created %s (stat err=%v)", retiredLockRel, err)
	}

	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("no embedded resources discovered")
	}

	// No embedded resource is mirrored into the vault.
	for _, r := range resources {
		p := filepath.Join(env.vaultPath, "Templates", filepath.FromSlash(r.RelPath))
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("override-only init should not materialize %s (err=%v)", r.RelPath, err)
		}
	}

	// .gitignore has *.bak and *.new canonical patterns.
	gi, err := os.ReadFile(filepath.Join(env.vaultPath, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	for _, pat := range []string{"*.bak", "*.new"} {
		if !containsLine(string(gi), pat) {
			t.Errorf(".gitignore missing canonical pattern %q\n%s", pat, gi)
		}
	}

	// Current-project scaffold (unchanged by Design B):
	// Projects/<slug>/{commands,skills}/README.md.
	for _, kind := range []string{"commands", "skills"} {
		p := filepath.Join(env.vaultPath, "Projects", env.projectName, kind, "README.md")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected scaffold README %s: %v", p, err)
		}
	}
}

// containsLine reports whether any line of s equals line exactly after
// trimming trailing whitespace.
func containsLine(s, line string) bool {
	for l := range strings.SplitSeq(s, "\n") {
		if strings.TrimRight(l, " \t\r") == line {
			return true
		}
	}
	return false
}

// testEnv captures the isolated HOME / XDG / vault / project layout for
// one end-to-end invocation of the `vp` CLI. home/xdgConfig are populated
// from a *testinfra.Env (see setupFreshEnv) rather than the struct embedding
// it directly, so the many other internal/integration files that already
// address these fields as env.home / env.xdgConfig (skill_upgrade_test.go,
// template_override_survives_test.go, template_reset_test.go,
// skill_fallback_test.go) keep compiling unchanged.
type testEnv struct {
	home        string
	xdgConfig   string
	vaultPath   string
	projectDir  string
	projectName string

	// iso is the *testinfra.Env home/xdgConfig were populated from. runVP
	// uses it to build a subprocess environment via Environ() instead of
	// hand-constructing "HOME="+... / "XDG_CONFIG_HOME="+... itself.
	iso *testinfra.Env
}

// setupFreshEnv allocates a fresh HOME + XDG_CONFIG_HOME (via
// testinfra.IsolateEnv) + vault + project scratch area, marks the project
// dir as a Go project so `vp init` detects it, and returns the resolved
// layout. IsolateEnv calls t.Setenv for HOME and XDG_CONFIG_HOME so
// sub-processes inherit the isolation via os.Environ() (see runVP).
func setupFreshEnv(t *testing.T) *testEnv {
	t.Helper()
	env := testinfra.IsolateEnv(t)

	vaultPath := filepath.Join(env.Home, "vault")
	projectDir := filepath.Join(env.Home, "code", "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"),
		[]byte("module integration-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &testEnv{
		home:        env.Home,
		xdgConfig:   env.XDGConfigHome,
		vaultPath:   vaultPath,
		projectDir:  projectDir,
		projectName: filepath.Base(projectDir),
		iso:         env,
	}
}

// embeddedBytesFor returns the canonical embedded bytes for a
// templates-root-relative path, failing the test if no such resource
// exists.
func embeddedBytesFor(t *testing.T, relPath string) []byte {
	t.Helper()
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	for _, r := range resources {
		if r.RelPath == relPath {
			return r.Bytes
		}
	}
	t.Fatalf("no embedded resource %q", relPath)
	return nil
}

// runVP execs the built vp binary with stdin (optional) and the given
// args, returning combined stdout+stderr. Fails the test on non-zero
// exit. Passes through the test-local HOME and XDG_CONFIG_HOME so the
// subprocess sees the isolated config layout.
//
// This is a thin delegator over testinfra.RunCLI (promoted from this
// function's own former body) so the many other call sites in this package
// (agentfile_upgrade_test.go, journey_skill_materialization_test.go,
// skill_init_test.go, and the rest) keep compiling unchanged. `bin` is kept
// for signature compatibility but is otherwise unused — testinfra.RunCLI
// resolves the built binary itself via testinfra.BuildVPBinary, which shares
// the same sync.Once cache buildVPBinary below now delegates to (one binary
// build cache for the whole package, not two). The return value concatenates
// stdout+stderr rather than reproducing exec.Cmd.CombinedOutput's
// byte-level interleaving — every call site here only substring-matches the
// result, so the difference is not observable.
func runVP(t *testing.T, bin string, env *testEnv, stdin []byte, args ...string) string {
	t.Helper()
	r := testinfra.RunCLI(t, env.iso.Environ(), env.projectDir, stdin, args...)
	if r.ExitCode != 0 {
		t.Fatalf("vp %s failed: exit %d\n%s%s", strings.Join(args, " "), r.ExitCode, r.Stdout, r.Stderr)
	}
	return r.Stdout + r.Stderr
}

// buildVPBinary compiles cmd/vp once per test process and returns the
// resulting binary path. Delegates to testinfra.BuildVPBinary, promoted
// verbatim from this function's former body (including the Windows
// .exe-suffix requirement) — see testinfra/runcli.go for the implementation
// and its history.
func buildVPBinary(t *testing.T) string {
	t.Helper()
	return testinfra.BuildVPBinary(t)
}
