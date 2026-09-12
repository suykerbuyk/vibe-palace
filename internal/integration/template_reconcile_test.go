// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
func runVP(t *testing.T, bin string, env *testEnv, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env.iso.Environ()
	cmd.Dir = env.projectDir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vp %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// Binary build is cached across subtests — `go build` is the expensive
// step, not the invocations.
var (
	vpBinaryOnce sync.Once
	vpBinaryPath string
	vpBinaryErr  error
)

// buildVPBinary compiles cmd/vp once per test process and returns the
// resulting binary path. Uses t.TempDir via a module-global lock so
// every test in the package shares one build.
func buildVPBinary(t *testing.T) string {
	t.Helper()
	vpBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "vp-integration-bin-")
		if err != nil {
			vpBinaryErr = err
			return
		}
		// 🔴 THE .exe SUFFIX IS LOAD-BEARING ON WINDOWS, NOT COSMETIC. os/exec
		// resolves an extension-less path against PATHEXT, so a binary built as
		// plain "vp" cannot be launched at all — it fails with the misleading
		// `executable file not found in %PATH%` even though the file is right
		// there. That defeated the ENTIRE windows-lock job for 11+ consecutive
		// pushes (2026-07-21 → 2026-07-26): all 16 children of
		// TestIntegration_VaultLockCrossProcess failed to exec, and the
		// resulting "lost update" / "an edit was clobbered" assertions read as a
		// lock-correctness bug when nothing had ever run. Per ci.yml that job is
		// "the sole runtime proof of the LockFileEx/UnlockFileEx path", so the
		// Windows lock had no runtime coverage whatsoever for that entire span.
		bin := filepath.Join(dir, "vp")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin,
			"github.com/suykerbuyk/vibe-palace/cmd/vp")
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stderr
		if err := cmd.Run(); err != nil {
			vpBinaryErr = err
			return
		}
		vpBinaryPath = bin
	})
	if vpBinaryErr != nil {
		t.Fatalf("build vp binary: %v", vpBinaryErr)
	}
	return vpBinaryPath
}
