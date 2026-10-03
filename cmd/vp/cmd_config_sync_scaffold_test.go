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
)

// TestConfigSyncSkipsNonPortableProjectDir is the CLI-level reproduction of
// the filed bug: `vp config sync` used to exit ExitSystem (2) for ANY vault
// holding a Projects/ directory whose name is not portable across
// filesystems (e.g. contains ':', illegal on NTFS/exFAT), because
// planScaffold planned the directory Create unconditionally and
// applyScaffold's raw os.MkdirAll for it always "succeeded" while the very
// next README write (routed through vaultfs since commit 33fce64) was
// refused — leaving an empty commands/skills/ dir behind and, in default
// scope (no --tier/--project), failing every sync in the vault over one
// badly-named directory. This drives the real vp config sync command in
// default scope, exactly as the bug report's own reproduction did.
func TestConfigSyncSkipsNonPortableProjectDir(t *testing.T) {
	_, _ = initTestEnv(t, false)

	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultPath := filepath.Join(t.TempDir(), "vault")

	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "sync-portable", "--vault-path", vaultPath, "--no-git"}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}

	// Hand-create a Projects/ directory with a name that is not portable
	// across filesystems — mirrors the filed repro, which used a raw
	// `mkdir "<vault>/Projects/a:b"` outside of vp.
	badDir := filepath.Join(vaultPath, "Projects", "a:b")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", badDir, err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	// Default scope: no --tier/--project/--cwd, exactly the invocation the
	// bug report used ("In default scope ... one badly-named Projects/
	// directory anywhere makes every sync exit 2").
	out, code := runSyncWithStdin(t, "", []string{"--yes"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d (want ExitOK)\n%s", code, out)
	}
	if strings.Contains(out, "error:") {
		t.Errorf("sync output should carry no error line:\n%s", out)
	}
	// The operator-visible portability Skip row, and its count. The
	// Initialised() filter must not swallow a name that fails slug.Validate
	// before TemplateTree can report it: filtering first would drop a:b into
	// a log line and print skipped=0, and this assertion would go red.
	if !strings.Contains(out, "TemplateTree:Projects/a:b") || !strings.Contains(out, "not portable") {
		t.Errorf("output lacks the not-portable Skip row for Projects/a:b:\n%s", out)
	}
	if !strings.Contains(out, "skipped=2") {
		t.Errorf("summary does not report skipped=2 (commands/ and skills/ under a:b):\n%s", out)
	}

	// Nothing was created under the badly-named directory.
	for _, kind := range []string{"commands", "skills"} {
		if _, err := os.Stat(filepath.Join(badDir, kind)); !os.IsNotExist(err) {
			t.Errorf("%s/%s should not have been created (err=%v)", badDir, kind, err)
		}
	}

	// The real, portably-named project's own scaffold still ran.
	realReadme := filepath.Join(vaultPath, "Projects", "sync-portable", "commands", "README.md")
	if _, err := os.Stat(realReadme); err != nil {
		t.Errorf("real project's commands README not created: %v", err)
	}
}

// syncScaffoldVault runs `vp init` of one project into a fresh git vault and
// chdirs into that project, so a following runSyncWithStdin enumerates every
// initialised project in the vault. It returns the vault path.
func syncScaffoldVault(t *testing.T) string {
	t.Helper()
	_, _ = initTestEnv(t, false)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	projDir := t.TempDir()
	markProjectDir(t, projDir)
	vaultPath := filepath.Join(t.TempDir(), "vault")
	cmd := cmdInit(cli.BuildInfo{Version: "test"})
	if code := cmd.Run([]string{projDir, "--name", "sync-commit", "--vault-path", vaultPath}); code != cli.ExitOK {
		t.Fatalf("init exit code = %d", code)
	}
	// A fresh vault's own files (init's vault step) are not this unit's
	// business; commit them so only the projects under test are dirty.
	gitRun(t, vaultPath, "add", "-A")
	gitRun(t, vaultPath, "commit", "-q", "--allow-empty", "-m", "fixture: fresh vault")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	return vaultPath
}

func putFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestConfigSyncCommitsMarkersOfADeliberatelyInitialisedProject: a project
// whose .surface is already TRACKED was initialised on purpose. `vp config
// sync` scaffolds its missing markers and commits exactly those markers —
// never the .surface, which stays a tracked modification tidy sweeps.
func TestConfigSyncCommitsMarkersOfADeliberatelyInitialisedProject(t *testing.T) {
	vaultPath := syncScaffoldVault(t)
	putFile(t, vaultPath, "Projects/beta/resume.md", "# beta\n")
	// An OLDER surface than this binary's, so the scaffold's own write
	// re-stamps it: the stamp is then a dirty tracked file the commit must
	// still leave out.
	putFile(t, vaultPath, "Projects/beta/.surface", "surface = 7\n")
	gitRun(t, vaultPath, "add", "Projects/beta/resume.md", "Projects/beta/.surface")
	gitRun(t, vaultPath, "commit", "-q", "-m", "beta history")

	out, code := runSyncWithStdin(t, "", []string{"--yes"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(vaultPath, "Projects", "beta", "skills", "README.md")); err != nil {
		t.Fatalf("fixture: sync did not scaffold beta: %v\n%s", err, out)
	}
	files := strings.Split(gitRun(t, vaultPath, "show", "--name-only", "--format=", "HEAD"), "\n")
	want := []string{"Projects/beta/commands/README.md", "Projects/beta/skills/README.md"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Errorf("config sync commit touched %q, want exactly %q\n%s", files, want, out)
	}
	if st := gitRun(t, vaultPath, "status", "--porcelain", "-uall", "--", "Projects/beta"); strings.Contains(st, "README.md") || strings.Contains(st, "??") {
		t.Errorf("beta markers left uncommitted:\n%s", st)
	}
	if st := gitRun(t, vaultPath, "status", "--porcelain", "--", "Projects/beta/.surface"); !strings.Contains(st, "M Projects/beta/.surface") {
		t.Errorf("fixture: the stamp is not a dirty tracked file after sync (%q), so this test cannot see it committed", st)
	}
	if !strings.Contains(out, "Projects/beta scaffold committed") {
		t.Errorf("sync output does not report the commit:\n%s", out)
	}
}

// TestConfigSyncCommitsNothingForAStray is review C1: hook capture into a
// slug nobody initialised leaves sessions/ content and an UNTRACKED .surface.
// That is WithContent, so config sync enumerates and scaffolds it — but it must
// commit nothing for it. Committing its .surface would make the next sync
// sweep its sessions and push the whole stray unreviewed, which is exactly
// what tidy's untracked-.surface gate exists to stop.
func TestConfigSyncCommitsNothingForAStray(t *testing.T) {
	vaultPath := syncScaffoldVault(t)
	putFile(t, vaultPath, "Projects/stray/sessions/x.md", "a captured session\n")
	putFile(t, vaultPath, "Projects/stray/.surface", "surface = 8\n")
	head := gitRun(t, vaultPath, "rev-parse", "HEAD")

	out, code := runSyncWithStdin(t, "", []string{"--yes"})
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if got := gitRun(t, vaultPath, "rev-parse", "HEAD"); got != head {
		t.Errorf("config sync committed for a stray: HEAD %s -> %s\n%s\n%s", head, got,
			gitRun(t, vaultPath, "show", "--stat", "HEAD"), out)
	}
	_, refused, err := storage.SyncPreview(vaultPath)
	if !refused || err == nil || !strings.Contains(err.Error(), "Projects/stray/.surface") {
		t.Errorf("vault sync must still refuse on the stray: refused=%v err=%v", refused, err)
	}
}
