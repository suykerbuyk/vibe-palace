// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// TestRun_SkipsDepartedSlugWithOnlyResidue: a git vault in which
// Projects/test-project/ was removed by the commit that added its departure
// record, after which a pull's leftover — one ignored *.bak — sits under it.
// The directory is PRESENT, but holds nothing git would carry, and that used
// to switch the departure off: the SessionEnd archived, noted and harvested
// into Projects/test-project/ again. The run must be skipped before any write,
// nothing may become tracked under the slug, and the native memory survives.
func TestRun_SkipsDepartedSlugWithOnlyResidue(t *testing.T) {
	vaultRoot := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", vaultRoot}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	put := func(rel, body string) {
		t.Helper()
		p := filepath.Join(vaultRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "test@test.com")
	git("config", "user.name", "Test")
	put(".gitignore", "*.bak\n")
	put("Projects/test-project/resume.md", "old\n")
	git("add", "-A")
	git("commit", "-q", "-m", "seed")

	git("rm", "-rq", "Projects/test-project")
	rec, err := (departure.Record{Slug: "test-project", Kind: departure.Renamed, To: "renamed-project", Date: "2026-09-25"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	put(departure.RelPath("test-project"), string(rec))
	git("add", "-A")
	git("commit", "-q", "-m", "depart: test-project -> renamed-project")
	put("Projects/test-project/transcripts/a.manifest.json.0.bak", "residue\n")
	if got := git("status", "--porcelain", "--untracked-files=all", "--", "Projects/test-project"); strings.TrimSpace(got) != "" {
		t.Fatalf("fixture precondition: the planted residue must be ignored, got %q", got)
	}

	cwd := t.TempDir()
	writeVibeMarker(t, cwd) // names "test-project": the stale checkout
	initGitRepo(t, cwd, "initial commit")
	hostDir := t.TempDir()
	transcriptPath := filepath.Join(hostDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(hostDir, "memory", "note.md")
	if err := os.MkdirAll(filepath.Dir(native), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("---\nname: note\ndescription: d\nmetadata:\n  type: project\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(context.Background(), Payload{
		SessionID: "test-session", TranscriptPath: transcriptPath, CWD: cwd, HookEventName: "SessionEnd",
	}, RunOptions{VaultRoot: vaultRoot, ProjectSlug: "test-project", VPVersion: "test-0.1", ClaimDir: filepath.Join(cwd, ".vibe-palace")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.SkippedRemovedSlug {
		t.Error("SkippedRemovedSlug must be set for a departed slug whose directory holds only ignored residue")
	}
	if res.ArchivePath != "" || res.SessionNoteID != "" || res.MemoryHarvest != nil {
		t.Errorf("nothing may run for a departed slug: archive=%q note=%q harvest=%v", res.ArchivePath, res.SessionNoteID, res.MemoryHarvest)
	}
	if got := git("ls-files", "Projects/test-project"); strings.TrimSpace(got) != "" {
		t.Errorf("Projects/test-project/ was resurrected into tracked history:\n%s", got)
	}
	if got := git("status", "--porcelain", "--untracked-files=all", "--", "Projects/test-project"); strings.TrimSpace(got) != "" {
		t.Errorf("carryable content was written under Projects/test-project/:\n%s", got)
	}
	if _, err := os.Stat(native); err != nil {
		t.Errorf("the native memory must be neither harvested nor deleted: %v", err)
	}
}
