// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// TestIntegrationTemplateMaterializeAndReconcile is the full-stack,
// acceptance-criteria gate for the materialize-and-reconcile-vault-templates
// epic. It drives the real `vp` CLI binary (built once per test run) end to
// end: fresh `vp init` materializes Templates/, a user edit to wrap.md
// survives a subsequent `vp config sync --yes`, and a simulated embedded
// bump (achieved by corrupting the lock entry's SHA so vault≠lock and
// embedded≠lock simultaneously) exercises each of the three Prompt
// branches — skip / overwrite+.bak / write-as-.new — verifying the vault
// state predicates for each.
//
// We shell out to the built `vp` binary rather than importing unexported
// `cmd/vp` helpers: cmd/vp is package main and not importable from
// internal/integration, and building+exec-ing the binary is the most
// faithful end-to-end surface. Embedded-bump simulation via lock
// corruption is equivalent to the function-variable override described in
// the plan — it produces the same row-5 "differs/differs → Prompt"
// decision without requiring in-process code injection across a process
// boundary.
func TestIntegrationTemplateMaterializeAndReconcile(t *testing.T) {
	bin := buildVPBinary(t)

	// --- Part 1: fresh init materializes Templates/ ---
	env := setupFreshEnv(t)
	runVP(t, bin, env, nil, "init", env.projectDir,
		"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")

	assertInitialMaterialization(t, env)

	// --- Part 2: user edit + `vp config sync --yes` preserves edit ---
	wrapPath := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")
	embWrap := embeddedBytesFor(t, "commands/wrap.md")
	preEditLock, ok := readLockEntry(t, env.vaultPath, "Templates/commands/wrap.md")
	if !ok {
		t.Fatalf("init did not write lock entry for Templates/commands/wrap.md")
	}
	preEditSHA := preEditLock.EmbeddedSHA

	const userEdit = "# USER EDITED WRAP\n\nmy custom wrap brief\n"
	if err := os.WriteFile(wrapPath, []byte(userEdit), 0o644); err != nil {
		t.Fatalf("user-edit wrap.md: %v", err)
	}
	runVP(t, bin, env, nil, "config", "sync", "--yes",
		"--project-root", env.projectDir)

	if got, _ := os.ReadFile(wrapPath); string(got) != userEdit {
		t.Errorf("step 2: wrap.md was clobbered by sync --yes\n got  %q\n want %q",
			got, userEdit)
	}
	if _, err := os.Stat(wrapPath + ".bak"); err == nil {
		t.Error("step 2: user-edited-only file must not produce .bak")
	}
	afterSyncEntry, ok := readLockEntry(t, env.vaultPath, "Templates/commands/wrap.md")
	if !ok {
		t.Fatal("step 2: lock entry for wrap.md disappeared")
	}
	if afterSyncEntry.EmbeddedSHA != preEditSHA {
		t.Errorf("step 2: lock should still track embedded SHA, got %q want %q",
			afterSyncEntry.EmbeddedSHA, preEditSHA)
	}

	// --- Part 3: simulated binary bump → Prompt → three branches ---
	// Each subtest replays steps 1+2 in its own vault tempdir, then
	// corrupts the lock entry so vault_sha ≠ lock_sha and embedded_sha ≠
	// lock_sha (row 5 of the decision table → ActionPrompt).
	const bogusSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	t.Run("skip", func(t *testing.T) {
		env := setupFreshEnv(t)
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", "tpl-skip", "--vault-path", env.vaultPath, "--no-git")
		wrap := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")
		if err := os.WriteFile(wrap, []byte(userEdit), 0o644); err != nil {
			t.Fatal(err)
		}
		bumpLockSHA(t, env.vaultPath, "Templates/commands/wrap.md", bogusSHA)

		out := runVP(t, bin, env, []byte("s\n"),
			"config", "sync", "--project-root", env.projectDir)
		if !strings.Contains(out, "diverged") {
			t.Errorf("skip: expected prompt output to mention divergence\n%s", out)
		}

		if got, _ := os.ReadFile(wrap); string(got) != userEdit {
			t.Errorf("skip: wrap.md changed\n got  %q\n want %q", got, userEdit)
		}
		if _, err := os.Stat(wrap + ".bak"); err == nil {
			t.Error("skip: must not write .bak")
		}
		if _, err := os.Stat(wrap + ".new"); err == nil {
			t.Error("skip: must not write .new")
		}
		entry, _ := readLockEntry(t, env.vaultPath, "Templates/commands/wrap.md")
		if entry.EmbeddedSHA != bogusSHA {
			t.Errorf("skip: lock entry should remain pre-decision (bogus) SHA; got %q want %q",
				entry.EmbeddedSHA, bogusSHA)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		env := setupFreshEnv(t)
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", "tpl-ovw", "--vault-path", env.vaultPath, "--no-git")
		wrap := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")
		if err := os.WriteFile(wrap, []byte(userEdit), 0o644); err != nil {
			t.Fatal(err)
		}
		// Capture the real embedded SHA before we corrupt the lock.
		realEmbSHA, ok := templates.EmbeddedSHA("commands/wrap.md")
		if !ok {
			t.Fatal("no embedded SHA for commands/wrap.md")
		}
		bumpLockSHA(t, env.vaultPath, "Templates/commands/wrap.md", bogusSHA)

		runVP(t, bin, env, []byte("o\n"),
			"config", "sync", "--project-root", env.projectDir)

		// wrap.md should now hold the real embedded bytes (overwrite).
		got, err := os.ReadFile(wrap)
		if err != nil {
			t.Fatalf("read wrap: %v", err)
		}
		if !bytes.Equal(got, embWrap) {
			t.Errorf("overwrite: wrap.md bytes != embedded defaults\n got  len=%d\n want len=%d",
				len(got), len(embWrap))
		}
		// .bak should hold the user's pre-overwrite edit.
		bak, err := os.ReadFile(wrap + ".bak")
		if err != nil {
			t.Fatalf("overwrite: expected .bak, got err: %v", err)
		}
		if string(bak) != userEdit {
			t.Errorf("overwrite: .bak should contain user edit\n got  %q\n want %q",
				bak, userEdit)
		}
		entry, _ := readLockEntry(t, env.vaultPath, "Templates/commands/wrap.md")
		if entry.EmbeddedSHA != realEmbSHA {
			t.Errorf("overwrite: lock should track real embedded SHA; got %q want %q",
				entry.EmbeddedSHA, realEmbSHA)
		}
	})

	t.Run("new", func(t *testing.T) {
		env := setupFreshEnv(t)
		runVP(t, bin, env, nil, "init", env.projectDir,
			"--name", "tpl-new", "--vault-path", env.vaultPath, "--no-git")
		wrap := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")
		if err := os.WriteFile(wrap, []byte(userEdit), 0o644); err != nil {
			t.Fatal(err)
		}
		bumpLockSHA(t, env.vaultPath, "Templates/commands/wrap.md", bogusSHA)

		runVP(t, bin, env, []byte("n\n"),
			"config", "sync", "--project-root", env.projectDir)

		if got, _ := os.ReadFile(wrap); string(got) != userEdit {
			t.Errorf("new: wrap.md must remain user's edit\n got  %q\n want %q",
				got, userEdit)
		}
		sidecar, err := os.ReadFile(wrap + ".new")
		if err != nil {
			t.Fatalf("new: expected wrap.md.new, got err: %v", err)
		}
		if !bytes.Equal(sidecar, embWrap) {
			t.Errorf("new: .new sidecar bytes != embedded defaults (len=%d vs %d)",
				len(sidecar), len(embWrap))
		}
		if _, err := os.Stat(wrap + ".bak"); err == nil {
			t.Error("new: must not write .bak")
		}
	})
}

// assertInitialMaterialization checks every post-`vp init` predicate from
// step 1 of the acceptance test: wrap.md equals embedded bytes, workflow.md
// and resume.md exist, templates.lock contains an entry per materialized
// resource with a matching EmbeddedSHA, .gitignore contains *.bak and
// *.new, and the current project's commands/ + skills/ README stubs exist.
func assertInitialMaterialization(t *testing.T, env *testEnv) {
	t.Helper()

	// wrap.md matches embedded default byte-for-byte.
	wrap := filepath.Join(env.vaultPath, "Templates", "commands", "wrap.md")
	got, err := os.ReadFile(wrap)
	if err != nil {
		t.Fatalf("read %s: %v", wrap, err)
	}
	want := embeddedBytesFor(t, "commands/wrap.md")
	if !bytes.Equal(got, want) {
		t.Errorf("wrap.md != embedded default (len %d vs %d)", len(got), len(want))
	}

	// workflow.md + resume.md materialized.
	for _, rel := range []string{"workflow.md", "resume.md"} {
		p := filepath.Join(env.vaultPath, "Templates", rel)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected materialized %s: %v", p, err)
		}
	}

	// Lock file has a matching entry for every materialized resource.
	lock, err := templates.ReadLock(env.vaultPath)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("WalkEmbedded: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("no embedded resources discovered")
	}
	for _, r := range resources {
		key := "Templates/" + r.RelPath
		entry, ok := lock.Entries[key]
		if !ok {
			t.Errorf("lock missing entry for %s", key)
			continue
		}
		embSHA, _ := templates.EmbeddedSHA(r.RelPath)
		if entry.EmbeddedSHA != embSHA {
			t.Errorf("lock entry %s sha mismatch: have %q want %q",
				key, entry.EmbeddedSHA, embSHA)
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

	// Current-project scaffold: Projects/<slug>/{commands,skills}/README.md.
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
// one end-to-end invocation of the `vp` CLI.
type testEnv struct {
	home        string
	xdgConfig   string
	vaultPath   string
	projectDir  string
	projectName string
}

// setupFreshEnv allocates a fresh HOME + XDG_CONFIG_HOME + vault + project
// scratch area, marks the project dir as a Go project so `vp init` detects
// it, and returns the resolved layout. Calls t.Setenv for HOME and
// XDG_CONFIG_HOME so sub-processes inherit the isolation via os.Environ().
func setupFreshEnv(t *testing.T) *testEnv {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)

	vaultPath := filepath.Join(home, "vault")
	projectDir := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"),
		[]byte("module integration-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &testEnv{
		home:        home,
		xdgConfig:   xdg,
		vaultPath:   vaultPath,
		projectDir:  projectDir,
		projectName: filepath.Base(projectDir),
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

// readLockEntry loads the lock sidecar and returns the entry for the
// given vault-relative path. Absent entries return ok=false.
func readLockEntry(t *testing.T, vaultRoot, key string) (templates.LockEntry, bool) {
	t.Helper()
	l, err := templates.ReadLock(vaultRoot)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	e, ok := l.Entries[key]
	return e, ok
}

// bumpLockSHA rewrites the templates.lock entry for key so its
// EmbeddedSHA is sha, simulating a binary bump from the reconciler's
// perspective (row 5 of the three-SHA decision table: vault≠lock AND
// embedded≠lock → ActionPrompt). More robust across a process boundary
// than the in-process templates.EmbeddedSHA function-variable override,
// and produces the same Plan outcome.
func bumpLockSHA(t *testing.T, vaultRoot, key, sha string) {
	t.Helper()
	l, err := templates.ReadLock(vaultRoot)
	if err != nil {
		t.Fatalf("ReadLock: %v", err)
	}
	entry, ok := l.Entries[key]
	if !ok {
		t.Fatalf("bumpLockSHA: no entry for %s", key)
	}
	entry.EmbeddedSHA = sha
	l.Entries[key] = entry
	if err := templates.WriteLock(vaultRoot, l); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
}

// runVP execs the built vp binary with stdin (optional) and the given
// args, returning combined stdout+stderr. Fails the test on non-zero
// exit. Passes through the test-local HOME and XDG_CONFIG_HOME so the
// subprocess sees the isolated config layout.
func runVP(t *testing.T, bin string, env *testEnv, stdin []byte, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+env.home,
		"XDG_CONFIG_HOME="+env.xdgConfig,
	)
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
		bin := filepath.Join(dir, "vp")
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
