// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/memory"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// residueDepartureFixture is harvestCLIFixture with "harvp" departed the way a
// pull leaves it on a host that had ignored files under it: Projects/harvp/ is
// removed in tracked history by the same commit that adds its moved-to-vault
// departure record, and then ONE ignored file (a *.bak the canonical
// .gitignore covers) is planted under it — the residue a pull cannot delete.
// Projects/harvp/ is therefore PRESENT on disk, but git would carry nothing in
// it. It returns the vault and HEAD's commit count after the departure.
func residueDepartureFixture(t *testing.T) (vault string, commits string) {
	t.Helper()
	vault, _ = harvestCLIFixture(t, "git_enabled = true")
	if !strings.Contains(strings.Join(storage.CanonicalGitignorePatterns, "\n"), "*.bak") {
		t.Fatal("fixture precondition: the vault .gitignore must ignore *.bak")
	}
	gitInVault(t, vault, "rm", "-rq", "Projects/harvp")
	if _, err := os.Lstat(filepath.Join(vault, "Projects", "harvp")); err == nil {
		t.Fatal("fixture precondition: git rm must leave Projects/harvp/ absent before the record is written")
	}
	if _, err := storage.NewVault(vault).RecordDeparture("harvp", departure.MovedToVault, "work-vault"); err != nil {
		t.Fatal(err)
	}
	gitInVault(t, vault, "add", "-A")
	gitInVault(t, vault, "commit", "-qm", "depart: harvp moved to work-vault")
	putVaultFile(t, vault, "Projects/harvp/transcripts/a.manifest.json.0.bak", "residue\n")

	if got := gitInVault(t, vault, "ls-files", "Projects/harvp"); strings.TrimSpace(got) != "" {
		t.Fatalf("fixture precondition: Projects/harvp/ must hold no tracked file, got %q", got)
	}
	if got := gitInVault(t, vault, "status", "--porcelain", "--untracked-files=all", "--", "Projects/harvp"); strings.TrimSpace(got) != "" {
		t.Fatalf("fixture precondition: the planted residue must be ignored, got %q", got)
	}
	return vault, strings.TrimSpace(gitInVault(t, vault, "rev-list", "--count", "HEAD"))
}

// TestMemoryHarvestCLIRefusesADepartureWithOnlyResidue is the resurrection this
// fix closes: the departed project's directory survived holding only an
// ignored .bak, and "present" used to switch off every departure refusal, so
// `vp memory harvest --no-push` committed Projects/harvp/.surface and
// Projects/harvp/memory/<file> back into tracked history. It must refuse with
// the moved-to-vault redirect, make no commit, track nothing under the slug,
// and leave the native memory where it is.
func TestMemoryHarvestCLIRefusesADepartureWithOnlyResidue(t *testing.T) {
	vault, commits := residueDepartureFixture(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	nativeDir, err := memory.NativeDirFromCwd(cwd)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(nativeDir)
	if len(before) == 0 {
		t.Fatal("fixture precondition: the native memory dir must hold a pending memory")
	}

	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdMemoryHarvest(), "--no-push")
	if code != cli.ExitUser {
		t.Fatalf("exit %d, want %d (refusal)\nstdout: %s\nstderr: %s", code, cli.ExitUser, stdout, stderr)
	}
	for _, want := range []string{"moved to another vault", "still names it"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr must contain %q:\n%s", want, stderr)
		}
	}
	if got := strings.TrimSpace(gitInVault(t, vault, "rev-list", "--count", "HEAD")); got != commits {
		t.Errorf("a commit was made for a departed slug: %s commits before, %s after", commits, got)
	}
	if got := gitInVault(t, vault, "ls-files", "Projects/harvp"); strings.TrimSpace(got) != "" {
		t.Errorf("Projects/harvp/ was resurrected into tracked history:\n%s", got)
	}
	if after, _ := os.ReadDir(nativeDir); len(after) != len(before) {
		t.Errorf("native memory must be untouched: %d files before, %d after", len(before), len(after))
	}
}

// TestArchiveCreateRefusesADepartureWithOnlyResidue: the same residue-only
// departure, through `vp archive create -p harvp` (resolveProjectForWrite ->
// project.RefuseDeparted). A real transcript is supplied, so without the
// refusal the archive WOULD be written under Projects/harvp/transcripts/.
func TestArchiveCreateRefusesADepartureWithOnlyResidue(t *testing.T) {
	vault, commits := residueDepartureFixture(t)
	src := filepath.Join(t.TempDir(), "session.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"hi"},"sessionId":"s1"}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"hello"},"sessionId":"s1"}` + "\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	stderr := captureStderr(t, func() {
		code = cmdArchiveCreate(cli.BuildInfo{}).Run([]string{"--session-id", "s1", "-p", "harvp", "--source", src})
	})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (refusal); stderr: %s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, `moved to another vault, "work-vault"`) {
		t.Errorf("stderr must carry the moved-to-vault redirect, got %q", stderr)
	}
	if got := gitInVault(t, vault, "status", "--porcelain", "--untracked-files=all", "--", "Projects/harvp"); strings.TrimSpace(got) != "" {
		t.Errorf("vp archive create wrote carryable content under Projects/harvp/:\n%s", got)
	}
	if got := strings.TrimSpace(gitInVault(t, vault, "rev-list", "--count", "HEAD")); got != commits {
		t.Errorf("a commit was made for a departed slug: %s commits before, %s after", commits, got)
	}
}
