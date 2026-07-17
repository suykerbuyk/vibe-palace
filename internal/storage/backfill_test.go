// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"
)

// seedBackfillNote writes a session note with the given key/source/archive
// identity through the real writer, so the fixture is byte-identical to what
// capture produces (not a hand-rolled approximation of the frontmatter).
func seedBackfillNote(t *testing.T, v *Vault, project string, meta SessionMeta) SessionRef {
	t.Helper()
	if meta.Date == "" {
		meta.Date = "2026-07-16"
	}
	ref, err := v.WriteSessionRef(project, meta, "body\n")
	if err != nil {
		t.Fatalf("seed note: %v", err)
	}
	return ref
}

func TestBackfillArchiveLink_StampsCallerKeyedNote(t *testing.T) {
	v := testVault(t)
	const sid = "aaaa-1111"
	ref := seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceCaller, Title: "wrap", Tag: "implementation",
	})

	res, err := v.BackfillArchiveLink("proj", sid, "Projects/proj/transcripts/x.manifest.json")
	if err != nil {
		t.Fatalf("BackfillArchiveLink: %v", err)
	}
	if !res.Found {
		t.Fatal("Found = false, want true")
	}
	if len(res.Updated) != 1 || res.Updated[0].NotePath != ref.NotePath {
		t.Fatalf("Updated = %+v, want exactly %s", res.Updated, ref.NotePath)
	}
	if res.Canonical.NotePath != ref.NotePath {
		t.Fatalf("Canonical = %s, want %s", res.Canonical.NotePath, ref.NotePath)
	}

	meta, _, err := v.ReadSession("proj", ref.Date, ref.Fingerprint, ref.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ArchiveSessionID != sid {
		t.Errorf("ArchiveSessionID = %q, want %q", meta.ArchiveSessionID, sid)
	}
	if meta.ArchiveSessionIDSource != ArchiveIDSourceBackfilled {
		t.Errorf("ArchiveSessionIDSource = %q, want %q", meta.ArchiveSessionIDSource, ArchiveIDSourceBackfilled)
	}
	if meta.Archive != "Projects/proj/transcripts/x.manifest.json" {
		t.Errorf("Archive = %q, want the manifest rel", meta.Archive)
	}
}

func TestBackfillArchiveLink_IdempotentRerunWritesNothing(t *testing.T) {
	v := testVault(t)
	const sid = "aaaa-2222"
	seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceCaller, Title: "wrap",
	})

	if _, err := v.BackfillArchiveLink("proj", sid, "Projects/proj/transcripts/x.manifest.json"); err != nil {
		t.Fatal(err)
	}
	res, err := v.BackfillArchiveLink("proj", sid, "Projects/proj/transcripts/x.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updated) != 0 {
		t.Errorf("re-run Updated = %+v, want none (idempotent)", res.Updated)
	}
	if !res.Found {
		t.Error("re-run Found = false, want true (the note still matches)")
	}
}

func TestBackfillArchiveLink_SkipsMintedKeyCoincidence(t *testing.T) {
	// A minted key is a random UUID; one that collides with a manifest's session
	// id identifies nothing. The predicate says caller only.
	v := testVault(t)
	const sid = "aaaa-3333"
	seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceMinted, Title: "coincidence",
	})

	res, err := v.BackfillArchiveLink("proj", sid, "Projects/proj/transcripts/x.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if res.Found || len(res.Updated) != 0 {
		t.Fatalf("minted-key note matched: %+v", res)
	}
}

func TestBackfillArchiveLink_RefusesIdentityConflict(t *testing.T) {
	v := testVault(t)
	const sid = "aaaa-4444"
	seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceCaller,
		ArchiveSessionID: "some-OTHER-session", ArchiveSessionIDSource: ArchiveIDSourceDerived,
		Title: "conflicted",
	})

	_, err := v.BackfillArchiveLink("proj", sid, "Projects/proj/transcripts/x.manifest.json")
	if err == nil {
		t.Fatal("want identity-conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "identity conflict") {
		t.Errorf("error = %v, want it to name the identity conflict", err)
	}
}

func TestBackfillArchiveLink_NeverRepointsALinkedNote(t *testing.T) {
	v := testVault(t)
	const sid = "aaaa-5555"
	ref := seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceCaller,
		ArchiveSessionID: sid, // already stamped with THIS id (converge case)
		Archive:          "Projects/proj/transcripts/live-linked.manifest.json",
		Title:            "already linked by the live path",
	})

	res, err := v.BackfillArchiveLink("proj", sid, "Projects/proj/transcripts/other.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updated) != 0 {
		t.Fatalf("Updated = %+v, want none — an existing archive: link must never be re-pointed", res.Updated)
	}
	meta, _, err := v.ReadSession("proj", ref.Date, ref.Fingerprint, ref.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Archive != "Projects/proj/transcripts/live-linked.manifest.json" {
		t.Errorf("Archive = %q — the live link was overwritten", meta.Archive)
	}
}

func TestBackfillArchiveLink_CanonicalPrefersNonStub(t *testing.T) {
	v := testVault(t)
	const sid = "aaaa-6666"
	seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceCaller, Tag: TagAutoCapture, Title: "stub",
	})
	wrap := seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: sid, SessionKeySource: KeySourceCaller, Tag: "implementation", Title: "wrap",
	})

	res, err := v.BackfillArchiveLink("proj", sid, "Projects/proj/transcripts/x.manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updated) != 2 {
		t.Fatalf("Updated = %d notes, want 2 (every note of the session gets the forward link)", len(res.Updated))
	}
	if res.Canonical.NotePath != wrap.NotePath {
		t.Errorf("Canonical = %s, want the non-stub %s", res.Canonical.NotePath, wrap.NotePath)
	}
}

func TestListCallerKeyedUnstampedNotes(t *testing.T) {
	v := testVault(t)
	want := seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: "key-caller", SessionKeySource: KeySourceCaller, Title: "candidate",
	})
	seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: "key-minted", SessionKeySource: KeySourceMinted, Title: "minted — excluded",
	})
	seedBackfillNote(t, v, "proj", SessionMeta{
		SessionKey: "key-stamped", SessionKeySource: KeySourceCaller,
		ArchiveSessionID: "key-stamped", Title: "already stamped — excluded",
	})

	notes, err := v.ListCallerKeyedUnstampedNotes("proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1: %+v", len(notes), notes)
	}
	if notes[0].SessionKey != "key-caller" || notes[0].NoteRel != want.NotePath {
		t.Errorf("note = %+v, want {key-caller %s}", notes[0], want.NotePath)
	}
}
