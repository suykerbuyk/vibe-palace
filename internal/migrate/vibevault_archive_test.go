// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Tests for the vibevault importer writing transcript archives (task
// importers-write-the-frozen-tracked-corpus, Scope 3, plan revisions R-2, R-8,
// R-9). The fixture vault is both source and destination: three sessions of
// test-project (setupTestVault).

const fixtureProject = "test-project"

// withZone runs f with the process-local zone set to name.
func withZone(t *testing.T, name string, f func()) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no tzdata for %s: %v", name, err)
	}
	old := time.Local
	time.Local = loc
	defer func() { time.Local = old }()
	f()
}

func importOnce(t *testing.T, vault *storage.Vault) ImportResult {
	t.Helper()
	res, err := ImportVibeVault(context.Background(), vault, vault, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("import errors: %v", res.Errors)
	}
	return res
}

func entries(t *testing.T, vault *storage.Vault, project string) []*archive.Entry {
	t.Helper()
	es, err := archive.ListEntries(vault.Root, project)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// ensureLedger gives project a ledger on this host, as an earlier ingest
// would have.
func ensureLedger(t *testing.T, vault *storage.Vault, project string) {
	t.Helper()
	tx, err := indexstore.Lock(context.Background(), vault, project, indexstore.NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Release()
	if _, err := tx.EnsureLedger(nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func ledger(t *testing.T, vault *storage.Vault, project string) *indexstore.Ledger {
	t.Helper()
	st, err := indexstore.ReadStore(vault, project)
	if err != nil {
		t.Fatal(err)
	}
	return st.Ledger()
}

func shasOf(es []*archive.Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Manifest.SourceSHA256)
	}
	sort.Strings(out)
	return out
}

// TestVibevaultWritesArchivesOnly: after an import of a pre-scaffolded project
// in a git vault, git status lists only the new archive and manifest pairs
// under Projects/<p>/transcripts/: no palace/ path (no drawers, no KG), and the
// marker sits in the git-ignored palace/.local/imports/<p>/.
func TestVibevaultWritesArchivesOnly(t *testing.T) {
	vault := setupTestVault(t)
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = vault.Root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(vault.Root, ".gitignore"), []byte("palace/.local/\n.vp-locks/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := scaffoldProject(context.Background(), vault, fixtureProject); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "fixture")

	res := importOnce(t, vault)
	if res.ArchivesWritten != 3 {
		t.Fatalf("archives written = %d, want 3", res.ArchivesWritten)
	}
	for _, line := range strings.Split(strings.TrimSpace(git("status", "--porcelain", "-uall")), "\n") {
		path := strings.TrimSpace(strings.TrimPrefix(line, "??"))
		if !strings.HasPrefix(path, "Projects/"+fixtureProject+"/transcripts/") ||
			!(strings.HasSuffix(path, ".jsonl.zst") || strings.HasSuffix(path, ".manifest.json")) {
			t.Errorf("the import wrote %q; only archive pairs are allowed", line)
		}
	}
	if _, err := os.Stat(filepath.Join(vault.Root, "palace", fixtureProject)); !os.IsNotExist(err) {
		t.Errorf("the import created palace/%s (stat err %v)", fixtureProject, err)
	}
	if _, err := os.Stat(filepath.Join(vault.Root, "palace", ".local", "imports", fixtureProject, "imported-sessions.jsonl")); err != nil {
		t.Errorf("no marker under palace/.local/imports/: %v", err)
	}
}

// TestVibevaultArchiveDate: a session dated 2026-04-01 is archived with
// captured_at 2026-04-01T12:00:00Z and a filename on that day, and
// SessionDay reads that day back from captured_at, under a zone west and a
// zone east of UTC.
func TestVibevaultArchiveDate(t *testing.T) {
	for _, zone := range []string{"America/Los_Angeles", "Asia/Tokyo"} {
		withZone(t, zone, func() {
			vault := setupTestVault(t)
			importOnce(t, vault)
			for _, e := range entries(t, vault, fixtureProject) {
				if e.Manifest.SessionID != "2026-04-01-01" {
					continue
				}
				if e.Manifest.CapturedAt != "2026-04-01T12:00:00Z" {
					t.Fatalf("%s: captured_at = %s", zone, e.Manifest.CapturedAt)
				}
				if !strings.HasPrefix(filepath.Base(e.ManifestPath), "2026-04-01-") {
					t.Fatalf("%s: archive file %s is not on the session's day", zone, e.ManifestPath)
				}
				day, err := e.Manifest.SessionDay([]byte("## Transcript\n\nmarkdown"))
				if err != nil || day.Day != "2026-04-01" || day.Source != archive.DayFromCapturedAt {
					t.Fatalf("%s: SessionDay = %+v, %v", zone, day, err)
				}
				return
			}
			t.Fatal("session 2026-04-01-01 was not archived")
		})
	}
}

// TestVibevaultBaseline: on a host with a ledger the archives an import
// brings in join the baseline set; a session already archived brings nothing
// in and is not added; with no ledger, the import creates none, and a ledger
// created later holds every archive in its baseline.
func TestVibevaultBaseline(t *testing.T) {
	t.Run("ledger", func(t *testing.T) {
		vault := setupTestVault(t)
		ensureLedger(t, vault, fixtureProject)
		importOnce(t, vault)
		es := entries(t, vault, fixtureProject)
		if len(es) != 3 {
			t.Fatalf("%d archives, want 3", len(es))
		}
		l := ledger(t, vault, fixtureProject)
		var refs []indexstore.ArchiveRef
		for _, e := range es {
			if !l.InBaseline(e.Manifest.SourceSHA256) {
				t.Errorf("archive of %s is not in the baseline set", e.Manifest.SessionID)
			}
			refs = append(refs, indexstore.ArchiveRef{SessionID: e.Manifest.SessionID, SHA: e.Manifest.SourceSHA256, Path: e.ArchivePath})
		}
		for _, p := range l.Pending(refs) {
			if !l.InBaseline(p.SHA) {
				t.Errorf("imported archive %s is pending outside the baseline", p.SessionID)
			}
		}
	})
	t.Run("already archived", func(t *testing.T) {
		vault := setupTestVault(t)
		// One session is archived before the import (a pull from another
		// host), with no marker on this host.
		first := filepath.Join(vault.Root, "Projects", fixtureProject, "sessions", "2026-04-01-01.md")
		meta, body, err := storage.ParseFrontmatter(mustRead(t, first))
		if err != nil {
			t.Fatal(err)
		}
		date, _ := sessionNoon(meta.Date)
		if _, err := writeArchive(vault, fixtureProject, pendingArchive{sessionID: meta.ID, text: strings.TrimSpace(body), date: date}); err != nil {
			t.Fatal(err)
		}
		ensureLedger(t, vault, fixtureProject)
		pre := shasOf(entries(t, vault, fixtureProject))
		importOnce(t, vault)
		added := baselineAdds(t, vault, fixtureProject)
		if len(added) != 2 || slices.Contains(added, pre[0]) {
			t.Fatalf("baseline additions %v; want the 2 archives this import brought in, not %s", added, pre[0])
		}
	})
	t.Run("no ledger", func(t *testing.T) {
		vault := setupTestVault(t)
		importOnce(t, vault)
		idir, _ := vault.IndexDir(fixtureProject)
		if _, err := os.Stat(filepath.Join(idir, "ledger.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("the import created a ledger (stat err %v)", err)
		}
		ensureLedger(t, vault, fixtureProject)
		l := ledger(t, vault, fixtureProject)
		for _, e := range entries(t, vault, fixtureProject) {
			if !l.InBaseline(e.Manifest.SourceSHA256) {
				t.Errorf("a ledger created later does not hold %s in its baseline", e.Manifest.SessionID)
			}
		}
	})
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// baselineAdds lists the hashes every baseline_add record of the project's
// ledger added, read from the raw file.
func baselineAdds(t *testing.T, vault *storage.Vault, project string) []string {
	t.Helper()
	idir, _ := vault.IndexDir(project)
	var out []string
	for _, line := range strings.Split(string(mustRead(t, filepath.Join(idir, "ledger.jsonl"))), "\n") {
		if !strings.Contains(line, `"kind":"baseline_add"`) {
			continue
		}
		i := strings.Index(line, `"archives":[`)
		if i < 0 {
			continue
		}
		rest := line[i+len(`"archives":[`):]
		rest = rest[:strings.Index(rest, "]")]
		for _, s := range strings.Split(rest, ",") {
			out = append(out, strings.Trim(s, `"`))
		}
	}
	return out
}

// TestVibevaultBaselineBeforeCreate: every hash is in a committed baseline
// addition before its archive is created; a failure after the addition and
// before the first archive leaves the hashes in the set and no archive, and a
// re-run creates the archives, all inside the set.
func TestVibevaultBaselineBeforeCreate(t *testing.T) {
	vault := setupTestVault(t)
	ensureLedger(t, vault, fixtureProject)
	old := archiveCreateFn
	t.Cleanup(func() { archiveCreateFn = old })
	archiveCreateFn = func(o archive.CreateOptions) (*archive.CreateResult, error) {
		return nil, errors.New("injected crash before the first archive")
	}
	if _, err := ImportVibeVault(context.Background(), vault, vault, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := len(entries(t, vault, fixtureProject)); n != 0 {
		t.Fatalf("%d archives after the injected crash, want 0", n)
	}
	if n := len(baselineAdds(t, vault, fixtureProject)); n != 3 {
		t.Fatalf("baseline holds %d hashes after the crash, want the 3 added first", n)
	}
	archiveCreateFn = old
	importOnce(t, vault)
	if n := len(entries(t, vault, fixtureProject)); n != 3 {
		t.Fatalf("the re-run created %d archives, want 3", n)
	}
	l := ledger(t, vault, fixtureProject)
	for _, e := range entries(t, vault, fixtureProject) {
		if !l.InBaseline(e.Manifest.SourceSHA256) {
			t.Errorf("the re-run's archive of %s is outside the baseline set", e.Manifest.SessionID)
		}
	}

	// The order, on a fresh vault: at each archive.Create, its hash is
	// already in a committed baseline addition.
	fresh := setupTestVault(t)
	ensureLedger(t, fresh, fixtureProject)
	archiveCreateFn = func(o archive.CreateOptions) (*archive.CreateResult, error) {
		if !ledger(t, fresh, fixtureProject).InBaseline(sourceSHA256(string(o.SourceContent))) {
			t.Errorf("%s was created before its hash joined the baseline", o.SessionID)
		}
		return old(o)
	}
	importOnce(t, fresh)
}

// TestVibevaultCrossHostIdempotence: host B holds the archives host A made (a
// pull: the tracked tree, not A's palace/.local/) and its own ledger. B
// imports the same source under another zone: every archive is skipped, B
// writes no tracked file, and B's baseline gains nothing.
func TestVibevaultCrossHostIdempotence(t *testing.T) {
	var aRoot string
	withZone(t, "America/Los_Angeles", func() {
		a := setupTestVault(t)
		importOnce(t, a)
		aRoot = a.Root
	})
	bRoot := t.TempDir()
	if err := filepath.WalkDir(aRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(aRoot, p)
		if rel == filepath.Join("palace", ".local") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(bRoot, rel), 0o755)
		}
		return os.WriteFile(filepath.Join(bRoot, rel), mustRead(t, p), 0o644)
	}); err != nil {
		t.Fatal(err)
	}
	b := storage.NewVault(bRoot)
	ensureLedger(t, b, fixtureProject)
	before := treeListing(t, bRoot)
	withZone(t, "Asia/Tokyo", func() {
		res := importOnce(t, b)
		if res.ArchivesWritten != 0 {
			t.Fatalf("host B wrote %d archives, want 0", res.ArchivesWritten)
		}
	})
	if after := treeListing(t, bRoot); !slices.Equal(before, after) {
		t.Fatalf("host B changed its tracked tree:\n%v\n%v", before, after)
	}
	if adds := baselineAdds(t, b, fixtureProject); len(adds) != 0 {
		t.Fatalf("host B's baseline gained %v", adds)
	}
}

// treeListing lists every file outside palace/.local/ with its size.
func treeListing(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() && rel == filepath.Join("palace", ".local") {
			return filepath.SkipDir
		}
		if d.Type().IsRegular() {
			fi, _ := d.Info()
			out = append(out, fmt.Sprintf("%s:%d", rel, fi.Size()))
		}
		return nil
	})
	return out
}

// TestVibevaultKnowledgeDate: knowledge.md is archived under knowledge-<slug>,
// dated by its own frontmatter date, else the fixed 2000-01-01 noon epoch.
func TestVibevaultKnowledgeDate(t *testing.T) {
	for _, tc := range []struct {
		body, want string
	}{
		{"---\ndate: \"2025-07-09\"\n---\nWhat we know.\n", "2025-07-09T12:00:00Z"},
		{"What we know, with no frontmatter.\n", "2000-01-01T12:00:00Z"},
	} {
		vault := setupTestVault(t)
		if err := os.WriteFile(filepath.Join(vault.Root, "Projects", fixtureProject, "knowledge.md"), []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		res := importOnce(t, vault)
		// knowledge.md is an archive, not a session.
		if res.ArchivesWritten != 4 || res.KnowledgeArchived != 1 || res.SessionsImported != 3 {
			t.Errorf("archives %d, knowledge %d, sessions %d; want 4, 1, 3", res.ArchivesWritten, res.KnowledgeArchived, res.SessionsImported)
		}
		found := false
		for _, e := range entries(t, vault, fixtureProject) {
			if e.Manifest.SessionID == "knowledge-"+fixtureProject {
				found = true
				if e.Manifest.CapturedAt != tc.want {
					t.Errorf("knowledge captured_at = %s, want %s", e.Manifest.CapturedAt, tc.want)
				}
			}
		}
		if !found {
			t.Error("knowledge.md was not archived")
		}
	}
}

// TestVibevaultEmptySession: a session with no body and no summary writes no
// archive, is recorded as done, and is counted as empty; a re-run skips it.
func TestVibevaultEmptySession(t *testing.T) {
	vault := setupTestVault(t)
	if err := os.WriteFile(filepath.Join(vault.Root, "Projects", fixtureProject, "sessions", "2026-04-03-01.md"), []byte(testSessionEmptyBody), 0o644); err != nil {
		t.Fatal(err)
	}
	res := importOnce(t, vault)
	if res.SessionsEmpty != 1 || res.ArchivesWritten != 3 {
		t.Fatalf("empty %d, archives %d; want 1 and 3", res.SessionsEmpty, res.ArchivesWritten)
	}
	if !sessionDone(t, vault, fixtureProject, "2026-04-03-01") {
		t.Fatal("the empty session is not recorded as done")
	}
	if res2 := importOnce(t, vault); res2.SessionsSkipped != 4 || res2.ArchivesWritten != 0 {
		t.Fatalf("re-run: skipped %d, archives %d; want 4 and 0", res2.SessionsSkipped, res2.ArchivesWritten)
	}
}

// TestVibevaultReimportWritesNothing: a second import on the same host
// writes no file and appends no marker line.
func TestVibevaultReimportWritesNothing(t *testing.T) {
	vault := setupTestVault(t)
	importOnce(t, vault)
	before := allFiles(t, vault.Root)
	res := importOnce(t, vault)
	if res.SessionsSkipped != 3 || res.ArchivesWritten != 0 {
		t.Fatalf("re-run: %+v", res)
	}
	if after := allFiles(t, vault.Root); !slices.Equal(before, after) {
		t.Fatalf("a re-import wrote:\n%v\n%v", before, after)
	}
}

func allFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if d.Type().IsRegular() {
			fi, _ := d.Info()
			rel, _ := filepath.Rel(root, p)
			out = append(out, fmt.Sprintf("%s:%d", rel, fi.Size()))
		}
		return nil
	})
	return out
}

// TestVibevaultRefusesABatchIDSessionID: a session whose id has the form of a
// mempalace import batch id is refused, with a message, and gets no archive.
func TestVibevaultRefusesABatchIDSessionID(t *testing.T) {
	vault := setupTestVault(t)
	id := "mempalace:" + strings.Repeat("ab", 32) + ":0"
	body := "---\nsession_id: \"" + id + "\"\ndate: \"2026-04-05\"\n---\nA body.\n"
	if err := os.WriteFile(filepath.Join(vault.Root, "Projects", fixtureProject, "sessions", "x.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ImportVibeVault(context.Background(), vault, vault, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Err.Error(), "batch id") {
		t.Fatalf("errors = %v, want one refusal naming the batch id", res.Errors)
	}
	for _, e := range entries(t, vault, fixtureProject) {
		if e.Manifest.SessionID == id {
			t.Fatal("a batch-id session was archived")
		}
	}
}

// TestVibevaultLegacyMarkerIsReadOnceThenDeleted: a legacy marker under
// palace/<p>/.local/ still counts its sessions (confirmed by their archives),
// is moved into palace/.local/imports/<p>/ and deleted, and its emptied
// directory is removed; a second run behaves the same with no legacy file.
func TestVibevaultLegacyMarkerIsReadOnceThenDeleted(t *testing.T) {
	vault := setupTestVault(t)
	importOnce(t, vault)
	newMarker, _ := markerFile(vault, fixtureProject)
	data := mustRead(t, newMarker)
	if err := os.Remove(newMarker); err != nil {
		t.Fatal(err)
	}
	legacy, _ := legacyMarkerFile(vault, fixtureProject)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, data, 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if res := importOnce(t, vault); res.SessionsSkipped != 3 || res.ArchivesWritten != 0 {
			t.Fatalf("with the legacy marker: %+v", res)
		}
		if _, err := os.Stat(legacy); !os.IsNotExist(err) {
			t.Fatalf("the legacy marker is still there (stat err %v)", err)
		}
		if _, err := os.Stat(filepath.Dir(legacy)); !os.IsNotExist(err) {
			t.Fatalf("the emptied palace/<p>/.local/ is still there (stat err %v)", err)
		}
		if _, err := os.Stat(newMarker); err != nil {
			t.Fatalf("the marker was not moved: %v", err)
		}
	}
}

// TestVibevaultTrackedLegacyMarkerIsCopiedNotDeleted: palace/<p>/.local/ is
// not ignored, so a vault may have committed the legacy marker. Its lines are
// copied into the new marker once, the committed file stays (git shows no
// deletion), and a second run copies nothing more.
func TestVibevaultTrackedLegacyMarkerIsCopiedNotDeleted(t *testing.T) {
	vault := setupTestVault(t)
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = vault.Root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	importOnce(t, vault)
	newMarker, _ := markerFile(vault, fixtureProject)
	data := mustRead(t, newMarker)
	if err := os.Remove(newMarker); err != nil {
		t.Fatal(err)
	}
	legacy, _ := legacyMarkerFile(vault, fixtureProject)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, data, 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(vault.Root, ".gitignore"), []byte("palace/.local/\n.vp-locks/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "a vault that committed its legacy marker")
	rel := filepath.ToSlash(strings.TrimPrefix(legacy, vault.Root+string(filepath.Separator)))
	if strings.TrimSpace(git("ls-files", "--", rel)) != rel {
		t.Fatalf("precondition: git must track %s", rel)
	}

	for run := range 2 {
		if res := importOnce(t, vault); res.SessionsSkipped != 3 || res.ArchivesWritten != 0 {
			t.Fatalf("run %d with the tracked legacy marker: %+v", run, res)
		}
		if st := git("status", "--porcelain", "--", rel); st != "" {
			t.Fatalf("run %d: the tracked legacy marker changed in git: %q", run, st)
		}
		if got := mustRead(t, newMarker); string(got) != string(data) {
			t.Fatalf("run %d: the new marker holds\n%s\nwant the legacy lines once\n%s", run, got, data)
		}
	}
}

// TestVibevaultADeletedAndRecreatedProjectIsImportedAgain: the marker is a
// hint. After a project's archives are gone (a project delete; a host where
// the marker survived, as before the delete purged palace/.local/imports/), a
// recreated project's import archives every session again instead of trusting
// the stale marker. (That the delete itself purges the marker is pinned by
// internal/storage's delete tests, through delLeftovers.)
func TestVibevaultADeletedAndRecreatedProjectIsImportedAgain(t *testing.T) {
	vault := setupTestVault(t)
	importOnce(t, vault)
	if err := os.RemoveAll(filepath.Join(vault.Root, "Projects", fixtureProject, "transcripts")); err != nil {
		t.Fatal(err)
	}
	if marker, _ := markerFile(vault, fixtureProject); len(mustRead(t, marker)) == 0 {
		t.Fatal("precondition: the stale marker must remain")
	}
	if res := importOnce(t, vault); res.ArchivesWritten != 3 {
		t.Fatalf("re-import after the archives were deleted wrote %d archives, want 3", res.ArchivesWritten)
	}
}
