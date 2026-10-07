// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type trigLaunch struct {
	args    []string
	logPath string
}

func newTrigRecorder(t *testing.T) *[]trigLaunch {
	t.Helper()
	calls := &[]trigLaunch{}
	SetIncomingIngestLauncher(func(_ string, args []string, logPath string) (int, error) {
		*calls = append(*calls, trigLaunch{append([]string(nil), args...), logPath})
		return 1, nil
	})
	t.Cleanup(func() { SetIncomingIngestLauncher(nil) })
	return calls
}

// trigGit runs a git command in dir and fails the test on error.
func trigGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir,
		"-c", "user.email=test@example.com", "-c", "user.name=Test",
		"-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// trigCommitFile writes a repo-relative file and commits it, returning the new HEAD.
func trigCommitFile(t *testing.T, dir, rel, content, msg string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	trigGit(t, dir, "add", "-A")
	trigGit(t, dir, "commit", "-q", "-m", msg)
	return headOrEmpty(dir)
}

func trigRepo(t *testing.T) (dir, before string) {
	t.Helper()
	dir = t.TempDir()
	trigGit(t, dir, "init", "-q", "-b", "main")
	before = trigCommitFile(t, dir, "README.md", "seed\n", "seed")
	return
}

// TestSpawnIngestForIncoming_ManifestChangeSpawnsFirstProject: a commit that
// adds a transcript manifest triggers exactly one ingester, with --project the
// first changed project in slug order and no --first.
func TestSpawnIngestForIncoming_ManifestChangeSpawnsFirstProject(t *testing.T) {
	dir, before := trigRepo(t)
	calls := newTrigRecorder(t)

	// Two projects gain a manifest; slug order picks "alpha".
	trigCommitFile(t, dir, "Projects/zeta/transcripts/2026-10-06-z.manifest.json", "{}", "z manifest")
	after := trigCommitFile(t, dir, "Projects/alpha/transcripts/2026-10-06-a.manifest.json", "{}", "a manifest")

	spawnIngestForIncoming(dir, before, after)

	if len(*calls) != 1 {
		t.Fatalf("want exactly one ingester launch, got %d: %+v", len(*calls), *calls)
	}
	got := (*calls)[0].args
	if len(got) < 2 || got[0] != "drain" || got[1] != "archives" {
		t.Errorf("args = %v, want a `drain archives` command", got)
	}
	if v, ok := argVal(got, "--project"); !ok || v != "alpha" {
		t.Errorf("--project = %q, want alpha (first changed project in slug order)", v)
	}
	if v, ok := argVal(got, "--vault-root"); !ok || v != dir {
		t.Errorf("--vault-root = %q, want %q", v, dir)
	}
	if _, ok := argVal(got, "--first"); ok {
		t.Error("a pull trigger must pass no --first")
	}
}

// TestSpawnIngestForIncoming_NoManifestNoSpawn: a commit that changes only
// non-manifest files (a note) triggers nothing.
func TestSpawnIngestForIncoming_NoManifestNoSpawn(t *testing.T) {
	dir, before := trigRepo(t)
	calls := newTrigRecorder(t)
	after := trigCommitFile(t, dir, "Projects/alpha/sessions/2026-10-06-a-01.md", "note\n", "a note")
	spawnIngestForIncoming(dir, before, after)
	if len(*calls) != 0 {
		t.Errorf("want no launch when no manifest changed, got %+v", *calls)
	}
}

// TestSpawnIngestForIncoming_NoOpGuards: HEAD unchanged, empty heads, and a nil
// launcher each spawn nothing.
func TestSpawnIngestForIncoming_NoOpGuards(t *testing.T) {
	dir, before := trigRepo(t)

	// HEAD unchanged (before == after): no spawn.
	calls := newTrigRecorder(t)
	spawnIngestForIncoming(dir, before, before)
	if len(*calls) != 0 {
		t.Errorf("want no launch when HEAD did not move, got %+v", *calls)
	}

	// Empty before: no spawn.
	*calls = nil
	spawnIngestForIncoming(dir, "", before)
	if len(*calls) != 0 {
		t.Errorf("want no launch with an empty before-head, got %+v", *calls)
	}

	// Nil launcher: no spawn (and no panic), even with a real manifest change.
	SetIncomingIngestLauncher(nil)
	after := trigCommitFile(t, dir, "Projects/alpha/transcripts/x.manifest.json", "{}", "m")
	spawnIngestForIncoming(dir, before, after) // must be a no-op, not a panic
}

func argVal(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}
