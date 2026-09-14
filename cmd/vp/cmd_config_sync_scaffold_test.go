// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
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
	if !strings.Contains(out, "skipped=") {
		t.Errorf("summary missing a skipped count:\n%s", out)
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
