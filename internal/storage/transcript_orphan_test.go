// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ageFile backdates a file's mtime by d.
func ageFile(t *testing.T, dir, rel string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if err := os.Chtimes(filepath.Join(dir, filepath.FromSlash(rel)), old, old); err != nil {
		t.Fatal(err)
	}
}

func isTracked(t *testing.T, dir, rel string) bool {
	t.Helper()
	return strings.TrimSpace(gitRun(t, dir, "ls-files", "--", rel)) != ""
}

func untrackedListed(t *testing.T, dir, rel string) bool {
	t.Helper()
	return strings.Contains(gitRun(t, dir, "status", "--porcelain", "-uall"), "?? "+rel)
}

const (
	orphanZst = "Projects/p/transcripts/orphan.jsonl.zst"
	staleAge  = time.Hour
)

// T1: a stale lone archive is Orphaned — not Deferred, not Swept, not Reported,
// not genuine dirt — stays untracked, and the user-facing notice names it.
func TestOrphan_StaleLoneArchiveIsOrphanedAndVisible(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, orphanZst, "zst\n")
	ageFile(t, dir, orphanZst, staleAge)
	if !untrackedListed(t, dir, orphanZst) { // non-vacuity: the fixture is dirty
		t.Fatal("fixture: the orphan is not an untracked entry")
	}

	res, err := TidyScan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPath(res.Orphaned, orphanZst) {
		t.Fatalf("stale lone archive must be Orphaned, got Orphaned=%v Deferred=%v", res.Orphaned, res.Deferred)
	}
	for name, set := range map[string][]string{"Deferred": res.Deferred, "Swept": res.Swept, "Reported": res.Reported, "GenuineDirt": res.GenuineDirt()} {
		if hasPath(set, orphanZst) {
			t.Errorf("orphan must not be in %s: %v", name, set)
		}
	}
	if note := OrphanedNotice(res.Orphaned); !strings.Contains(note, orphanZst) {
		t.Errorf("the user-facing orphan notice does not name the path:\n%s", note)
	}
	// A real tidy must neither commit nor stage it.
	if _, err := TidyVault(dir, false); err != nil {
		t.Fatal(err)
	}
	if isTracked(t, dir, orphanZst) || !untrackedListed(t, dir, orphanZst) {
		t.Error("tidy committed or lost the orphan; it must stay untracked")
	}
}

// T2: the in-flight race is still protected — a fresh lone archive is Deferred,
// never Orphaned, never committed.
func TestOrphan_FreshLoneArchiveStaysDeferredAndUncommitted(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, orphanZst, "zst\n") // mtime = now
	const donePair = "Projects/p/transcripts/done"
	writeFile(t, dir, donePair+".jsonl.zst", "zst\n")
	writeFile(t, dir, donePair+".manifest.json", "{}\n") // a commit must happen

	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Committed {
		t.Fatal("fixture: nothing was committed, so the deferral was not exercised against a real commit")
	}
	if !hasPath(res.Deferred, orphanZst) || hasPath(res.Orphaned, orphanZst) {
		t.Errorf("fresh archive must be Deferred only: Deferred=%v Orphaned=%v", res.Deferred, res.Orphaned)
	}
	if isTracked(t, dir, orphanZst) || !untrackedListed(t, dir, orphanZst) {
		t.Error("an in-flight half was committed")
	}
}

func seedUnpushed(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, dir, "Projects/p/resume.md", "resume\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "unpushed work")
}

func bareMainHead(t *testing.T, bare string) string {
	t.Helper()
	return strings.TrimSpace(gitRun(t, bare, "rev-parse", "main"))
}

func pushAndExpectDelivered(t *testing.T, dir, bare string) {
	t.Helper()
	local := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	if bareMainHead(t, bare) == local {
		t.Fatal("fixture: the remote already has HEAD, so a push proves nothing")
	}
	if _, err := PushPlain(dir, []string{"origin"}); err != nil {
		t.Fatalf("PushPlain refused: %v", err)
	}
	if got := bareMainHead(t, bare); got != local {
		t.Errorf("remote main = %s, want the unpushed commit %s", got, local)
	}
}

// T3: a stale orphan inside an already-tracked transcripts directory no longer
// blocks a push, and is not published.
func TestOrphan_StaleOrphanDoesNotBlockPush(t *testing.T) {
	dir, bare := syncSeedRemote(t)
	writeFile(t, dir, "Projects/p/transcripts/a.jsonl.zst", "z\n")
	writeFile(t, dir, "Projects/p/transcripts/a.manifest.json", "{}\n")
	seedUnpushed(t, dir)
	writeFile(t, dir, orphanZst, "zst\n")
	ageFile(t, dir, orphanZst, staleAge)

	pushAndExpectDelivered(t, dir, bare)
	if isTracked(t, dir, orphanZst) || !untrackedListed(t, dir, orphanZst) {
		t.Error("the orphan was committed or lost")
	}
}

// T3b (the review's blocker): NO tracked transcript child. Plain
// `git status --porcelain` collapses the new directory to one "?? dir/" line, so
// an exact-path filter over it would still refuse.
func TestOrphan_StaleOrphanInNewTranscriptsDirDoesNotBlockPush(t *testing.T) {
	dir, bare := syncSeedRemote(t)
	seedUnpushed(t, dir) // tracks Projects/p/resume.md only
	writeFile(t, dir, orphanZst, "zst\n")
	ageFile(t, dir, orphanZst, staleAge)
	if !strings.Contains(gitRun(t, dir, "status", "--porcelain")+"\n", "?? Projects/p/transcripts/\n") {
		t.Fatalf("fixture: plain porcelain does not collapse the directory:\n%s", gitRun(t, dir, "status", "--porcelain"))
	}

	pushAndExpectDelivered(t, dir, bare)
	if isTracked(t, dir, orphanZst) || !untrackedListed(t, dir, orphanZst) {
		t.Error("the orphan was committed or lost")
	}
}

// T4: a young in-flight half does not block a push either, and is not published.
func TestOrphan_InFlightHalfDoesNotBlockPush(t *testing.T) {
	dir, bare := syncSeedRemote(t)
	seedUnpushed(t, dir)
	writeFile(t, dir, orphanZst, "zst\n") // mtime = now

	pushAndExpectDelivered(t, dir, bare)
	if isTracked(t, dir, orphanZst) || !untrackedListed(t, dir, orphanZst) {
		t.Error("the in-flight half was committed or lost")
	}
}

// T5: real dirt still blocks, and the refusal does not list the exempt archive.
func TestOrphan_PushStillRefusesRealDirt(t *testing.T) {
	for name, dirt := range map[string]func(t *testing.T, dir string){
		"untracked non-capture file": func(t *testing.T, dir string) { writeFile(t, dir, "Projects/p/stray.txt", "x\n") },
		"modified tracked file":      func(t *testing.T, dir string) { writeFile(t, dir, "Projects/p/resume.md", "changed\n") },
		"untracked sibling dir file": func(t *testing.T, dir string) { writeFile(t, dir, "Projects/p/transcripts/other.txt", "x\n") },
	} {
		t.Run(name, func(t *testing.T) {
			dir, _ := syncSeedRemote(t)
			seedUnpushed(t, dir)
			writeFile(t, dir, orphanZst, "zst\n")
			ageFile(t, dir, orphanZst, staleAge)
			dirt(t, dir)

			_, err := PushPlain(dir, []string{"origin"})
			var dte *DirtyTreeError
			if !errors.As(err, &dte) {
				t.Fatalf("want *DirtyTreeError, got %v", err)
			}
			if strings.Contains(dte.Porcelain, "orphan.jsonl.zst") {
				t.Errorf("the refusal lists the exempt orphan:\n%s", dte.Porcelain)
			}
			if strings.TrimSpace(dte.Porcelain) == "" {
				t.Error("empty refusal porcelain")
			}
		})
	}
}

// T6: the grace boundary, and the stat-error branch reached through the seam.
func TestOrphan_GraceBoundaryAndStatError(t *testing.T) {
	dir := t.TempDir()
	const rel = "Projects/p/transcripts/x.jsonl.zst"
	writeFile(t, dir, rel, "z\n")
	mtime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, filepath.FromSlash(rel)), mtime, mtime); err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct {
		now        time.Time
		wantOrphan bool
	}{
		"just under grace": {mtime.Add(TranscriptPairGrace - time.Minute), false},
		"just over grace":  {mtime.Add(TranscriptPairGrace + time.Minute), true},
		"mtime in future":  {mtime.Add(-time.Hour), false},
	} {
		t.Run(name, func(t *testing.T) {
			young, orphaned := splitOrphanedDeferred(dir, []string{rel}, os.Stat, c.now)
			if got := hasPath(orphaned, rel); got != c.wantOrphan || hasPath(young, rel) == c.wantOrphan {
				t.Errorf("orphaned=%v young=%v, wantOrphan=%v", orphaned, young, c.wantOrphan)
			}
		})
	}

	t.Run("stat error stays deferred", func(t *testing.T) {
		calls := 0
		failing := func(string) (os.FileInfo, error) { calls++; return nil, errors.New("boom") }
		// A `now` far past any mtime: only the error branch can keep it young.
		young, orphaned := splitOrphanedDeferred(dir, []string{rel}, failing, time.Now().Add(100*365*24*time.Hour))
		if calls != 1 {
			t.Fatalf("the stub stat was called %d times; the error branch was not entered", calls)
		}
		if !hasPath(young, rel) || len(orphaned) != 0 {
			t.Errorf("a stat error must leave the path deferred: young=%v orphaned=%v", young, orphaned)
		}
	})
}

// The orphan must not make SyncVault refuse (it is not genuine dirt) — M6 guard.
func TestOrphan_DoesNotMakeSyncRefuse(t *testing.T) {
	dir, bare := syncSeedRemote(t)
	seedUnpushed(t, dir)
	writeFile(t, dir, orphanZst, "zst\n")
	ageFile(t, dir, orphanZst, staleAge)

	res, err := SyncVault(dir, []string{"origin"})
	if err != nil || res.Refused {
		t.Fatalf("SyncVault refused on an orphan: err=%v refused=%v", err, res.Refused)
	}
	if !hasPath(res.Orphaned, orphanZst) {
		t.Errorf("SyncResult.Orphaned = %v", res.Orphaned)
	}
	if bareMainHead(t, bare) != strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")) {
		t.Error("the sync did not deliver HEAD")
	}
}

// Delta nit: a path with a newline or " -> " must round-trip through the
// re-rendered porcelain as ONE literal path.
func TestRenderPorcelain_EscapesAwkwardPaths(t *testing.T) {
	for _, path := range []string{"plain.txt", "a -> b.txt", "two\nlines.txt", `quo"te.txt`, " lead.txt", "trail.txt ", "\ttab.txt"} {
		out := renderPorcelain([]PorcelainEntry{{Status: "??", Path: path}})
		if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
			t.Errorf("%q rendered as %d lines: %q", path, strings.Count(out, "\n"), out)
		}
	}
	if got := renderPorcelain([]PorcelainEntry{{Status: "??", Path: "plain.txt"}}); got != "?? plain.txt\n" {
		t.Errorf("a plain path must render unquoted, got %q", got)
	}
}

// F2: the orphan notice lists ONE path per line, so a comma in a filename cannot
// be mistaken for a separator, and a line break cannot forge a line.
func TestOrphanedNotice_OnePathPerLine(t *testing.T) {
	paths := []string{"Projects/p/transcripts/a, b.jsonl.zst", "Projects/p/transcripts/c.jsonl.zst", "Projects/p/transcripts/x\ny.jsonl.zst"}
	lines := strings.Split(OrphanedNotice(paths), "\n")
	if len(lines) != 1+len(paths) {
		t.Fatalf("want header + %d lines, got %d:\n%s", len(paths), len(lines), strings.Join(lines, "\n"))
	}
	if lines[1] != "  "+paths[0] || lines[2] != "  "+paths[1] {
		t.Errorf("paths are not one-per-line verbatim: %q", lines[1:3])
	}
	if lines[3] != "  "+`"Projects/p/transcripts/x\ny.jsonl.zst"` {
		t.Errorf("a path with a line break must be Go-quoted: %q", lines[3])
	}
	if OrphanedNotice(nil) != "" {
		t.Error("no orphans must give no notice")
	}
}

// F3: the notice names the repair verb (not the re-dating create route), and
// still lists one path per line.
func TestOrphanedNotice_NamesTheRepairVerb(t *testing.T) {
	note := OrphanedNotice([]string{"Projects/p/transcripts/a.jsonl.zst"})
	for _, want := range []string{"vp archive repair-manifest", "--adapter", "--captured-at", "  Projects/p/transcripts/a.jsonl.zst"} {
		if !strings.Contains(note, want) {
			t.Errorf("the notice does not contain %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "archive create") {
		t.Errorf("the notice still names the re-dating create route:\n%s", note)
	}
}
