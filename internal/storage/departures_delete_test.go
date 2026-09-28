// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// RecordDepartureForDelete writes every lifecycle field, derives the
// generation from HEAD's record, and leaves the split purge's writer as it was.
func TestRecordDepartureForDeleteWritesTheLifecycleFields(t *testing.T) {
	root := initTestRepo(t)
	writeFile(t, root, "Projects/old/resume.md", "old\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "seed")
	v := NewVault(root)
	read := func() departure.Record {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(departure.RelPath("old"))))
		if err != nil {
			t.Fatal(err)
		}
		rec := departure.Parse("old", b)
		if rec.Malformed != "" {
			t.Fatalf("record does not parse: %s\n%s", rec.Malformed, b)
		}
		return rec
	}
	commitRecord := func() {
		t.Helper()
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-q", "-m", "record")
	}
	facts := DepartureFacts{CopyCommit: strings.Repeat("ab", 20), Footprint: "v1:" + strings.Repeat("ef", 32)}
	const label = "git@example.invalid:team/b.git"

	// First departure: generation 1, every field as given, written while the
	// tree is still here (the delete removes it afterwards).
	rel, created, _, err := v.RecordDepartureForDelete("old", departure.MovedToVault, label, facts)
	if err != nil || !created || rel != departure.RelPath("old") {
		t.Fatalf("RecordDepartureForDelete = (%q, %v, %v)", rel, created, err)
	}
	rec := read()
	if rec.Kind != departure.MovedToVault || rec.To != label || rec.Generation != 1 ||
		rec.CopyCommit != facts.CopyCommit || rec.Footprint != facts.Footprint || rec.BaseCommit == "" || rec.Date == "" {
		t.Fatalf("record = %+v", rec)
	}
	// A re-run over its own uncommitted record derives the same generation.
	if _, _, _, err := v.RecordDepartureForDelete("old", departure.MovedToVault, label, facts); err != nil {
		t.Fatal(err)
	}
	if g := read().Generation; g != 1 {
		t.Fatalf("re-run over an uncommitted record: generation %d, want 1", g)
	}

	// A later departure, over the committed one: generation 2, kind deleted.
	commitRecord()
	if _, _, _, err := v.RecordDepartureForDelete("old", departure.Deleted, "", DepartureFacts{Footprint: facts.Footprint}); err != nil {
		t.Fatal(err)
	}
	if rec := read(); rec.Kind != departure.Deleted || rec.Generation != 2 || rec.CopyCommit != "" || rec.To != "" {
		t.Fatalf("second departure = %+v", rec)
	}

	// A committed record from before the field existed counts as generation 1.
	if _, _, err := v.RecordDepartureForPurge("old", departure.MovedToVault, label); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(departure.RelPath("old"))))
	for _, key := range []string{"generation", "copy_commit", "footprint"} {
		if strings.Contains(string(b), key) {
			t.Fatalf("the split purge's writer must not write %s:\n%s", key, b)
		}
	}
	commitRecord()
	if _, _, _, err := v.RecordDepartureForDelete("old", departure.Deleted, "", DepartureFacts{}); err != nil {
		t.Fatal(err)
	}
	if g := read().Generation; g != 2 {
		t.Fatalf("over a pre-field record: generation %d, want 2", g)
	}

	// Refusals: a rename is not a delete, and deleted names no destination.
	if _, _, _, err := v.RecordDepartureForDelete("old", departure.Renamed, "new", DepartureFacts{}); err == nil {
		t.Error("a delete must refuse kind renamed")
	}
	if _, _, _, err := v.RecordDepartureForDelete("old", departure.Deleted, label, DepartureFacts{}); err == nil {
		t.Error("a deleted record must refuse a destination")
	}
	// A committed record that cannot be parsed leaves the generation unknown.
	writeFile(t, root, departure.RelPath("old"), "{not json\n")
	commitRecord()
	if _, _, _, err := v.RecordDepartureForDelete("old", departure.Deleted, "", DepartureFacts{}); err == nil ||
		!strings.Contains(err.Error(), "generation is unknown") {
		t.Errorf("over an unreadable committed record: err = %v", err)
	}
}

// Bind refuses a project whose departure record says deleted: it continues
// nowhere, so no vault can be proved to hold it. The config is untouched.
func TestBindRefusesADeletedProject(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git unavailable")
	}
	home := rebindEnv(t)
	global, _ := splitHost(t, home, "qa")
	b, err := (departure.Record{Slug: "qa", Kind: departure.Deleted, Date: "2026-09-27", Generation: 1}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, filepath.Join(global, filepath.FromSlash(departure.RelPath("qa"))), string(b))
	cfg, _ := VaultConfigFilePath()
	before := bindRead(t, cfg)

	_, err = BindProjectVault(movedReq("qa"))
	if err == nil || !strings.Contains(err.Error(), "it was deleted in") || !strings.Contains(err.Error(), "not moved to another vault") {
		t.Fatalf("bind of a deleted project: err = %v", err)
	}
	if after := bindRead(t, cfg); after != before {
		t.Fatalf("a refused bind changed the config:\n%s", after)
	}
}

// The pull guard (F3) treats an incoming deleted record like any departure:
// local work under the slug refuses the merge, and the remedy says the project
// was deleted rather than suggesting a rename or a destination.
func TestPullRefusesWorkUnderAnIncomingDelete(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.Deleted, "", nil)
	writeFile(t, b, "Projects/old/x.md", "work\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-m", "stale work")
	before := snapshotRepo(t, b)

	res, _ := Pull(b, []string{"origin"})
	msg := asDeparted(t, res.RemoteResults["origin"]).Error()
	if !strings.Contains(msg, `"old" (deleted from this vault)`) || !strings.Contains(msg, "continues nowhere") {
		t.Errorf("the deleted remedy is wrong:\n%s", msg)
	}
	if strings.Contains(msg, "rewrite the paths") || strings.Contains(msg, "belongs in the vault") {
		t.Errorf("a delete must not be described as a rename or a move:\n%s", msg)
	}
	if after := snapshotRepo(t, b); after.head != before.head || after.status != before.status {
		t.Errorf("HEAD or the working tree changed: %+v -> %+v", before, after)
	}
}

// The generation is monotonic across the record's whole history: delete,
// commit, `git revert` (the § Undo path, which removes the record), delete
// again writes generation 2, never 1 twice.
func TestRecordDepartureForDeleteAfterARevertIsTheNextGeneration(t *testing.T) {
	root := initTestRepo(t)
	writeFile(t, root, "Projects/old/resume.md", "old\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "seed")
	v := NewVault(root)
	gen := func() int {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(departure.RelPath("old"))))
		if err != nil {
			t.Fatal(err)
		}
		return departure.Parse("old", b).Generation
	}

	if _, _, _, err := v.RecordDepartureForDelete("old", departure.Deleted, "", DepartureFacts{}); err != nil {
		t.Fatal(err)
	}
	if g := gen(); g != 1 {
		t.Fatalf("first departure: generation %d, want 1", g)
	}
	gitRun(t, root, "rm", "-q", "-r", "Projects/old")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "vault project delete: old")
	gitRun(t, root, "revert", "--no-edit", "HEAD")
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(departure.RelPath("old")))); !os.IsNotExist(err) {
		t.Fatalf("the revert must remove the record: %v", err)
	}

	if _, _, _, err := v.RecordDepartureForDelete("old", departure.Deleted, "", DepartureFacts{}); err != nil {
		t.Fatal(err)
	}
	if g := gen(); g != 2 {
		t.Fatalf("re-delete after a revert: generation %d, want 2", g)
	}
}

// With --moved-to, the destination's own record sets a floor: the new record
// is above it even when the local history is lower. A floor on a deleted
// record, which has no destination, refuses.
func TestRecordDepartureForDeleteIsAboveTheDestinationGeneration(t *testing.T) {
	root := initTestRepo(t)
	writeFile(t, root, "Projects/old/resume.md", "old\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "seed")
	v := NewVault(root)
	const label = "git@example.invalid:team/b.git"

	// Local history: generation 1, committed.
	if _, _, _, err := v.RecordDepartureForDelete("old", departure.MovedToVault, label, DepartureFacts{}); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "record")

	if _, _, _, err := v.RecordDepartureForDelete("old", departure.MovedToVault, label, DepartureFacts{DestinationGeneration: 4}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(departure.RelPath("old"))))
	if err != nil {
		t.Fatal(err)
	}
	if g := departure.Parse("old", b).Generation; g != 5 {
		t.Fatalf("over a destination at generation 4 and local history at 1: generation %d, want 5", g)
	}

	if _, _, _, err := v.RecordDepartureForDelete("old", departure.Deleted, "", DepartureFacts{DestinationGeneration: 4}); err == nil {
		t.Error("a deleted record has no destination, so a destination generation must refuse")
	}
}

// An unreadable OLDER version of the record is skipped with a warning naming
// its commit, never a refusal: history is immutable. HEAD's version parses, so
// the generation is derived from the readable versions.
func TestDepartureGenerationSkipsAnUnreadableOlderVersion(t *testing.T) {
	root := initTestRepo(t)
	v := NewVault(root)
	commitRecord := func(body, msg string) string {
		t.Helper()
		writeFile(t, root, departure.RelPath("old"), body)
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-q", "-m", msg)
		return gitRun(t, root, "rev-parse", "HEAD")
	}
	enc := func(gen int) string {
		t.Helper()
		b, err := (departure.Record{Slug: "old", Kind: departure.Deleted, Date: "2026-09-27", Generation: gen}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	commitRecord(enc(2), "gen 2")
	broken := commitRecord("{not json\n", "a damaged record")
	commitRecord(enc(3), "repaired")

	gen, warnings, err := v.DepartureGeneration("old", departure.Deleted, 0)
	if err != nil {
		t.Fatalf("an unreadable older version must not refuse: %v", err)
	}
	if gen != 4 {
		t.Fatalf("generation %d, want 4", gen)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "skipped unreadable record at "+broken) {
		t.Fatalf("warnings = %q, want one naming %s", warnings, broken)
	}
	if _, _, w, err := v.RecordDepartureForDelete("old", departure.Deleted, "", DepartureFacts{}); err != nil || len(w) != 1 {
		t.Fatalf("the writer must return the same warning: %q, %v", w, err)
	}
}
