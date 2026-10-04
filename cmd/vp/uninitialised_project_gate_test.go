// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// The CLI half of the uninitialised-project write refusal. `vp vault
// write|edit|move` and `vp archive create` reach the gated primitives
// (atomicfile, vaultfs.Move, archive.Create); this pins that each still does,
// so a refactor that gave the CLI its own path goes red. Each refusal is paired
// with the same command against an initialised project, which must succeed, so
// the refusal is the gate and not a broken fixture.

func cliGateTranscript(t *testing.T) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "session.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"hi"},"sessionId":"s1"}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"hello"},"sessionId":"s1"}` + "\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return src
}

func mustNotExist(t *testing.T, vaultDir, rel, who string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(vaultDir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
		t.Errorf("%s left %s behind (lstat err: %v)", who, rel, err)
	}
}

func TestVaultCLIRefusesWritesIntoAnUninitialisedProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		// refused runs against a project the vault has not initialised.
		refused []string
		// admitted is the same command against an initialised project.
		admitted []string
		run      func(args []string) int
		// absent must not exist after the refusal.
		absent []string
		// intact, when set, is seeded with "old\n" and must survive the
		// refusal byte-identical.
		intact string
	}{
		{name: "vault write",
			refused:  []string{"Projects/fresh/notes/x.md", "--content", "hi"},
			admitted: []string{"Projects/p/notes/x.md", "--content", "hi"},
			run:      func(a []string) int { return cmdVaultWrite().Run(a) },
			absent:   []string{"Projects/fresh", "palace/fresh"}},
		// Edit needs an existing file, so the uninitialised project is a
		// PHANTOM: Projects/ph holds memory/x.md and nothing that marks it.
		{name: "vault edit phantom",
			refused:  []string{"Projects/ph/memory/x.md", "--old", "old", "--new", "new"},
			admitted: []string{"Projects/p/memory/x.md", "--old", "old", "--new", "new"},
			run:      func(a []string) int { return cmdVaultEdit().Run(a) },
			absent:   []string{"palace/ph"},
			intact:   "Projects/ph/memory/x.md"},
		// A palace/<slug>/ file is judged by its project's Projects/<slug>.
		{name: "vault edit palace",
			refused:  []string{"palace/fresh/notes.md", "--old", "old", "--new", "new"},
			admitted: []string{"palace/p/notes.md", "--old", "old", "--new", "new"},
			run:      func(a []string) int { return cmdVaultEdit().Run(a) },
			absent:   []string{"Projects/fresh"},
			intact:   "palace/fresh/notes.md"},
		{name: "vault move",
			refused:  []string{"Projects/p/notes.md", "Projects/fresh/x.md"},
			admitted: []string{"Projects/p/notes.md", "Projects/q/x.md"},
			run:      func(a []string) int { return cmdVaultMove().Run(a) },
			absent:   []string{"Projects/fresh", "palace/fresh"},
			intact:   "Projects/p/notes.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vaultDir := setupTestVaultEnv(t)
			testutil.InitProject(t, vaultDir, "p", "q")
			seedVaultFile(t, vaultDir, "Projects/p/memory/x.md", "old\n")
			seedVaultFile(t, vaultDir, "palace/p/notes.md", "old\n")
			seedVaultFile(t, vaultDir, "Projects/p/notes.md", "old\n")
			if tc.intact != "" {
				seedVaultFile(t, vaultDir, tc.intact, "old\n")
			}

			var code int
			stderr := captureStderr(t, func() { code = tc.run(tc.refused) })
			if code == cli.ExitOK {
				t.Fatalf("vp %s %v: exit 0, want a refusal", tc.name, tc.refused)
			}
			if !strings.Contains(stderr, "project is not initialised") || !strings.Contains(stderr, "vp init") {
				t.Errorf("vp %s: stderr must say the project is not initialised and name `vp init`, got %q", tc.name, stderr)
			}
			for _, rel := range tc.absent {
				mustNotExist(t, vaultDir, rel, "vp "+tc.name)
			}
			if tc.intact != "" {
				if got, err := os.ReadFile(filepath.Join(vaultDir, filepath.FromSlash(tc.intact))); err != nil || string(got) != "old\n" {
					t.Errorf("vp %s changed %s: %q, %v", tc.name, tc.intact, got, err)
				}
			}

			stderr = captureStderr(t, func() { code = tc.run(tc.admitted) })
			if code != cli.ExitOK {
				t.Errorf("vp %s %v into an initialised project: exit %d, stderr %q", tc.name, tc.admitted, code, stderr)
			}
		})
	}
}

func TestArchiveCreateRefusesAnUninitialisedProject(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	testutil.InitProject(t, vaultDir, "p")
	src := cliGateTranscript(t)

	var code int
	stderr := captureStderr(t, func() {
		code = cmdArchiveCreate(cli.BuildInfo{}).Run([]string{"--session-id", "s1", "-p", "fresh", "--source", src})
	})
	if code == cli.ExitOK {
		t.Fatalf("vp archive create -p fresh: exit 0, want a refusal")
	}
	if !strings.Contains(stderr, "project is not initialised") || !strings.Contains(stderr, "vp init") {
		t.Errorf("stderr must say the project is not initialised and name `vp init`, got %q", stderr)
	}
	mustNotExist(t, vaultDir, "Projects/fresh", "vp archive create")
	mustNotExist(t, vaultDir, "palace/fresh", "vp archive create")

	stderr = captureStderr(t, func() {
		code = cmdArchiveCreate(cli.BuildInfo{}).Run([]string{"--session-id", "s1", "-p", "p", "--source", src, "--quiet"})
	})
	if code != cli.ExitOK {
		t.Fatalf("vp archive create -p p (initialised): exit %d, stderr %q", code, stderr)
	}
	matches, _ := filepath.Glob(filepath.Join(vaultDir, "Projects", "p", "transcripts", "*.manifest.json"))
	if len(matches) != 1 {
		t.Errorf("initialised archive create wrote %d manifests, want 1", len(matches))
	}
}

// `vp vault write` through an in-vault symlink is judged by where it lands:
// neither alias may create the uninitialised project it points into.
func TestVaultWriteRefusesThroughAnInVaultSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	for _, tc := range []struct {
		name, link, target, refused, admitted, absent string
	}{
		{name: "link inside a project to its parent", link: "Projects/alpha/lnk", target: "..",
			refused: "Projects/alpha/lnk/gamma/x.md", admitted: "Projects/alpha/lnk/alpha/y.md", absent: "Projects/gamma"},
		{name: "top-level link to Projects", link: "Notes", target: "Projects",
			refused: "Notes/beta/x.md", admitted: "Notes/alpha/y.md", absent: "Projects/beta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vaultDir := setupTestVaultEnv(t)
			testutil.InitProject(t, vaultDir, "alpha")
			if err := os.Symlink(tc.target, filepath.Join(vaultDir, filepath.FromSlash(tc.link))); err != nil {
				t.Fatal(err)
			}
			var code int
			stderr := captureStderr(t, func() { code = cmdVaultWrite().Run([]string{tc.refused, "--content", "hi"}) })
			if code == cli.ExitOK {
				t.Fatalf("vp vault write %s: exit 0, want a refusal", tc.refused)
			}
			if !strings.Contains(stderr, "project is not initialised") {
				t.Errorf("vp vault write %s: stderr %q does not name the refusal", tc.refused, stderr)
			}
			mustNotExist(t, vaultDir, tc.absent, "vp vault write "+tc.refused)
			stderr = captureStderr(t, func() { code = cmdVaultWrite().Run([]string{tc.admitted, "--content", "hi"}) })
			if code != cli.ExitOK {
				t.Errorf("vp vault write %s into the initialised alpha: exit %d, stderr %q", tc.admitted, code, stderr)
			}
		})
	}
}
