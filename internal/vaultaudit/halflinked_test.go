// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// seedForwardLinkedNote writes a session note that ALREADY names a manifest in
// its archive: field — the note side of the HALF-LINKED class.
//
// It is deliberately separate from seedKeyedNote rather than another parameter
// on it: these fixtures assert that a note with no session_key at all still
// pairs, because the live specimens are Codex receipts whose key is
// "codex-session-end:<id>" and therefore matches no manifest session id.
func seedForwardLinkedNote(t *testing.T, vault *storage.Vault, p, name, archiveSessionID, archiveRel, tag string) string {
	t.Helper()
	dir := filepath.Join(vault.Root, "Projects", p, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "session_key: codex-session-end:%s\n", archiveSessionID)
	b.WriteString("session_key_source: caller\n")
	fmt.Fprintf(&b, "archive_session_id: %s\n", archiveSessionID)
	fmt.Fprintf(&b, "archive: %s\n", archiveRel)
	if tag != "" {
		fmt.Fprintf(&b, "tag: %s\n", tag)
	}
	b.WriteString("title: t\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return "Projects/" + p + "/sessions/" + name
}

// 🔴 T-A1 — the dimension must tell the two classes APART, and the fixture holds
// BOTH so the mutation can discriminate.
//
// Dropping the forward-link lookup in auditArchiveRoundTrip makes every stranded
// manifest take the plain branch, so the half-linked assertion reddens while the
// genuinely-stranded one stays green. A one-manifest fixture could not show that:
// it would redden on any change to the wording at all, which is not what is being
// pinned.
//
// The half-linked manifest carries NO session_key match, by construction (see
// seedForwardLinkedNote). If this ever started passing because the note was
// caller-keyed, it would be measuring the pre-existing class-1 path instead.
func TestArchiveRoundTrip_SeparatesHalfLinkedFromStranded(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	half := seedManifestAt(t, vault, "p1", "sess-HALF", "", "2026-07-14-sess-HALF", "2026-07-14T01:00:00Z")
	seedForwardLinkedNote(t, vault, "p1", "2026-07-16-01.md", "sess-HALF", half, storage.TagAutoCapture)

	// Genuinely stranded: no note names it and no note carries its key.
	lost := seedManifestAt(t, vault, "p1", "sess-LOST", "", "2026-07-14-sess-LOST", "2026-07-14T03:00:00Z")

	findings, unknowns, err := auditArchiveRoundTrip(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknowns) != 0 {
		t.Fatalf("unknowns = %v, want none", unknowns)
	}

	byArtifact := map[string]string{}
	for _, f := range findings {
		byArtifact[f.Artifact] = f.Detail
	}
	// 🔴 BOTH are still findings. A half-linked manifest is an incomplete record
	// that `vp archive link` completes, so dropping it would switch off the
	// instrument that shows the repair working — and would make 17 accepted
	// baseline entries stale the moment this landed, which the operator ruling
	// assigns to the APPLICATION of (c), not to this re-wording.
	if len(byArtifact) != 2 {
		t.Fatalf("findings = %v, want BOTH stranded manifests (half-linked is still a finding)", byArtifact)
	}

	if d := byArtifact[half]; !strings.Contains(d, "HALF-LINKED") ||
		!strings.Contains(d, "Projects/p1/sessions/2026-07-16-01.md") {
		t.Errorf("half-linked detail = %q, want HALF-LINKED naming the note that points at it", d)
	}
	if d := byArtifact[half]; strings.Contains(d, "nothing can navigate from the note to it") {
		t.Errorf("half-linked detail still claims the stranded harm, which is FALSE of it: %q", d)
	}
	if d := byArtifact[lost]; !strings.Contains(d, "stranded") || strings.Contains(d, "HALF-LINKED") {
		t.Errorf("stranded detail = %q, want the stranded wording and NOT half-linked", d)
	}
}

// 🔴 T-A2 — the property (a)'s safety rests on: re-wording a Detail cannot churn
// the accepted baseline, because finding identity is (Dimension, Artifact).
//
// Making identity include Detail reddens this. Without it, "(a) lands with zero
// baseline movement" is an assertion in a plan rather than a fact about the code,
// and §6's prediction has nothing holding it up.
func TestBaselineDiff_DetailChangeDoesNotChurnAcceptedEntries(t *testing.T) {
	const artifact = "Projects/p1/transcripts/2026-07-14-sess-HALF.manifest.json"
	b := Baseline{Dimensions: map[string]DimensionBaseline{
		DimArchiveRoundTrip: {Reason: "accepted for the test", Accepted: []string{artifact}},
	}}

	stranded := []Finding{{Dimension: DimArchiveRoundTrip, Artifact: artifact,
		Detail: "transcript for session sess-HALF back-links to NO session note — the transcript is stranded"}}
	halfLinked := []Finding{{Dimension: DimArchiveRoundTrip, Artifact: artifact,
		Detail: "transcript for session sess-HALF is HALF-LINKED: note Projects/p1/sessions/2026-07-16-01.md already names it"}}

	for name, findings := range map[string][]Finding{"before (a)": stranded, "after (a)": halfLinked} {
		added, stale := b.Diff(findings)
		if len(added) != 0 {
			t.Errorf("%s: added = %v, want none — the artifact is accepted", name, added)
		}
		if len(stale) != 0 {
			t.Errorf("%s: stale = %v, want none — the artifact is still a finding", name, stale)
		}
	}
}

// 🔴 T-C1 — class 2 pairs on TWO exact equalities, and the fixture contains a
// counter-example for EACH so that dropping either clause reddens this.
//
// Mutate ONE arm at a time (a compound predicate mutated wholesale proves
// nothing about which half was load-bearing):
//
//   - drop N.archive == rel(M)            -> wrongManifest's note pairs, target moves
//   - drop N.archive_session_id == M.id   -> otherSession's note pairs, a 3rd candidate
//   - drop M.vault_rel_session_note == "" -> the already-linked manifest pairs
func TestBackfillCandidates_HalfLinkedClassPairsOnBothPointers(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// THE PAIR. Two manifests for one session; the note names the OLDER one, so
	// the target must be the newest manifest a note actually NAMES, not the
	// newest manifest.
	named := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A-early", "2026-07-14T01:00:00Z")
	wrongManifest := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A-late", "2026-07-14T09:00:00Z")
	note := seedForwardLinkedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", named, storage.TagAutoCapture)

	// NEGATIVE 1 — a note of a DIFFERENT session naming a manifest of its own.
	// Dropping the archive_session_id clause lets it pair with sess-A's.
	otherManifest := seedManifestAt(t, vault, "p1", "sess-B", "", "2026-07-14-sess-B", "2026-07-14T02:00:00Z")
	seedForwardLinkedNote(t, vault, "p1", "2026-07-17-01.md", "sess-OTHER", otherManifest, storage.TagAutoCapture)

	// NEGATIVE 2 — an ALREADY-LINKED manifest whose note names it. Dropping the
	// vault_rel_session_note clause makes it a candidate although nothing is
	// missing.
	doneNote := "Projects/p1/sessions/2026-07-18-01.md"
	doneManifest := seedManifestAt(t, vault, "p1", "sess-DONE", doneNote, "2026-07-14-sess-DONE", "2026-07-14T04:00:00Z")
	seedForwardLinkedNote(t, vault, "p1", "2026-07-18-01.md", "sess-DONE", doneManifest, storage.TagAutoCapture)

	cands, err := BackfillCandidates(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v, want exactly 1 (sess-A, half-linked)", cands)
	}
	c := cands[0]
	if c.SessionID != "sess-A" {
		t.Fatalf("candidate session = %q, want sess-A", c.SessionID)
	}
	if c.Class != ClassHalfLinked {
		t.Errorf("class = %q, want %q", c.Class, ClassHalfLinked)
	}
	if c.TargetManifest != named {
		t.Errorf("target = %q, want the manifest the note NAMES (%q), not the newest (%q)",
			c.TargetManifest, named, wrongManifest)
	}
	if c.Canonical != note {
		t.Errorf("canonical = %q, want %q", c.Canonical, note)
	}
}

// Class 1 still wins when a session holds notes of BOTH kinds. Class 2 must be
// purely additive: it may introduce candidates, never re-route one that already
// matched before the predicate was widened.
func TestBackfillCandidates_ClassOneWinsOverHalfLinkedForOneSession(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	m := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A", "2026-07-14T01:00:00Z")
	seedKeyedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", "caller", "", "")
	seedForwardLinkedNote(t, vault, "p1", "2026-07-17-01.md", "sess-A", m, storage.TagAutoCapture)

	cands, err := BackfillCandidates(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v, want 1", cands)
	}
	if cands[0].Class != ClassUnstampedCallerKeyed {
		t.Errorf("class = %q, want %q — the unstamped note is the one that still needs stamping",
			cands[0].Class, ClassUnstampedCallerKeyed)
	}
}

// Class 2 prefers a real note over the auto-capture stub, through the same
// ranking every other linker uses. Inverting storage.preferCanonical reddens it.
func TestBackfillCandidates_HalfLinkedPrefersRealNoteOverStub(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	m := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A", "2026-07-14T01:00:00Z")
	seedForwardLinkedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", m, storage.TagAutoCapture)
	real := seedForwardLinkedNote(t, vault, "p1", "2026-07-16-02.md", "sess-A", m, "implementation")

	cands, err := BackfillCandidates(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("candidates = %+v, want 1", cands)
	}
	if cands[0].Canonical != real {
		t.Errorf("canonical = %q, want the non-stub note %q", cands[0].Canonical, real)
	}
}

// 🔴 T-C2 — the applier writes the MANIFEST ONLY for class 2.
//
// Routing class 2 through storage.BackfillArchiveLink reddens this: that applier
// matches the note's session_key against the manifest's session_id, and a Codex
// receipt's key is "codex-session-end:<id>", so it reports Found=false and
// ApplyBackfill errors "not derivable" for a pair the report just listed. That is
// the defect this branch exists to avoid, and it is invisible to a test that only
// checks the manifest.
func TestApplyBackfill_HalfLinkedWritesManifestOnlyAndTouchesNoNoteByte(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	m := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A", "2026-07-14T01:00:00Z")
	noteRel := seedForwardLinkedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", m, storage.TagAutoCapture)
	notePath := filepath.Join(vault.Root, filepath.FromSlash(noteRel))

	before, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}

	res, err := ApplyBackfill(vault, "p1", "sess-A")
	if err != nil {
		t.Fatalf("ApplyBackfill: %v", err)
	}
	if res.NothingToDo != "" {
		t.Fatalf("NothingToDo = %q, want a real application", res.NothingToDo)
	}
	if res.Canonical != noteRel {
		t.Errorf("canonical = %q, want %q", res.Canonical, noteRel)
	}
	if len(res.NotesUpdated) != 0 {
		t.Errorf("NotesUpdated = %v, want none: a half-linked note is already complete", res.NotesUpdated)
	}

	after, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("the note was rewritten.\nbefore:\n%s\nafter:\n%s", before, after)
	}

	manifest, err := archive.ReadManifest(filepath.Join(vault.Root, filepath.FromSlash(m)))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.VaultRelSessionNote != noteRel {
		t.Errorf("manifest back-link = %q, want %q", manifest.VaultRelSessionNote, noteRel)
	}

	// Idempotent: a re-run changes nothing and says why.
	again, err := ApplyBackfill(vault, "p1", "sess-A")
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if again.NothingToDo == "" {
		t.Error("re-run reported work done; applying an applied pair must change nothing")
	}
}

// 🔴 T-C5 — A LINKED SIBLING MUST NOT SUPPRESS AN INDEPENDENTLY HALF-LINKED TARGET.
//
// Found by the adversarial review, 2026-11-07. ApplyBackfill's already-linked
// short-circuit fires on ANY manifest of the session carrying a back-link, and it
// used to run BEFORE the class-2 candidate was resolved. So a session holding an
// older LINKED manifest plus a newer stranded manifest that a note names was
// listed by `vp archive backfill` as recoverable and then REFUSED by
// `vp archive link`, which reported "already back-links note …" about the sibling
// and left the target's reverse pointer empty.
//
// That is a report naming a pair the applier declines — the exact shape of the
// class-2 defect this whole unit was filed to close, reintroduced inside the fix
// for it.
//
// 🔴 IT SURVIVED BECAUSE THE OTHER APPLIER TEST HAS ONE MANIFEST and structurally
// cannot reach the branch. This fixture has TWO, which is the discriminating
// property: restoring the short-circuit ahead of the class-2 branch reddens this
// and leaves every single-manifest test green.
//
// It is NOT about upgrading the linked sibling. That narrowing stands: a manifest
// that already back-links a note is done, and TryLinkArchiveToSessions owns any
// upgrade. This is a DIFFERENT manifest, whose own reverse pointer is missing.
func TestApplyBackfill_LinkedSiblingDoesNotSuppressAHalfLinkedTarget(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// The older sibling, already linked to a note of its own. Untouchable.
	siblingNote := "Projects/p1/sessions/2026-07-15-01.md"
	sibling := seedManifestAt(t, vault, "p1", "sess-A", siblingNote, "2026-07-14-sess-A-early", "2026-07-14T01:00:00Z")

	// The newer manifest: stranded, and named by a note of the same session.
	target := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A-late", "2026-07-14T09:00:00Z")
	noteRel := seedForwardLinkedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", target, storage.TagAutoCapture)

	// PRECONDITION — the fixture CONTAINS the thing the mutation acts on: a
	// linked sibling. Without it the short-circuit is never reached and this
	// test cannot discriminate.
	m, err := archive.ReadManifest(filepath.Join(vault.Root, filepath.FromSlash(sibling)))
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote != siblingNote {
		t.Fatalf("precondition: sibling back-link = %q, want %q", m.VaultRelSessionNote, siblingNote)
	}

	// The report says it is recoverable...
	cands, err := BackfillCandidates(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Class != ClassHalfLinked || cands[0].TargetManifest != target {
		t.Fatalf("candidates = %+v, want one half-linked pair targeting %s", cands, target)
	}

	// ...so the applier must apply it, not refuse it.
	res, err := ApplyBackfill(vault, "p1", "sess-A")
	if err != nil {
		t.Fatalf("ApplyBackfill: %v", err)
	}
	if res.NothingToDo != "" {
		t.Fatalf("NothingToDo = %q — the report named this pair recoverable and the applier refused it", res.NothingToDo)
	}
	if res.TargetManifest != target {
		t.Errorf("target = %q, want %q", res.TargetManifest, target)
	}

	got, err := archive.ReadManifest(filepath.Join(vault.Root, filepath.FromSlash(target)))
	if err != nil {
		t.Fatal(err)
	}
	if got.VaultRelSessionNote != noteRel {
		t.Errorf("target back-link = %q, want %q", got.VaultRelSessionNote, noteRel)
	}

	// The sibling is untouched: this applier upgrades nothing.
	after, err := archive.ReadManifest(filepath.Join(vault.Root, filepath.FromSlash(sibling)))
	if err != nil {
		t.Fatal(err)
	}
	if after.VaultRelSessionNote != siblingNote {
		t.Errorf("sibling back-link changed to %q; it must stay %q", after.VaultRelSessionNote, siblingNote)
	}

	// Idempotent: with the target linked there is no candidate left, so the
	// short-circuit is reached on the re-run and reports honestly.
	again, err := ApplyBackfill(vault, "p1", "sess-A")
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if again.NothingToDo == "" {
		t.Error("re-run reported work done; applying an applied pair must change nothing")
	}
}

// 🔴 T-C3 — the REFUSAL direction. Widening the predicate must not make
// everything recoverable: a stranded manifest no note describes stays
// unrecoverable, and the applier still refuses it by name.
//
// Letting class 2 fall back to any note of the project reddens this.
func TestBackfillCandidates_HalfLinkedDoesNotRescueAnUndescribedManifest(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// A real, unrelated half-linked pair, so the class IS exercised in this run
	// — a fixture with no class-2 pair at all could not tell "correctly refused"
	// from "the class never ran".
	other := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A", "2026-07-14T01:00:00Z")
	seedForwardLinkedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", other, storage.TagAutoCapture)

	// The orphan: no note names it, no note carries its key.
	seedManifestAt(t, vault, "p1", "sess-LOST", "", "2026-07-14-sess-LOST", "2026-07-14T03:00:00Z")

	cands, err := BackfillCandidates(vault)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.SessionID == "sess-LOST" {
			t.Fatalf("sess-LOST became a candidate: %+v", c)
		}
	}

	_, err = ApplyBackfill(vault, "p1", "sess-LOST")
	if err == nil {
		t.Fatal("ApplyBackfill(sess-LOST) succeeded; an undescribed manifest is not derivable")
	}
	if !strings.Contains(err.Error(), "not derivable") {
		t.Errorf("error = %v, want the not-derivable refusal", err)
	}
}

// 🔴 T-C4 — the narrowing (b) introduces, made visible.
//
// After (b) a Codex manifest back-linked to its own receipt is no longer
// stranded, so this applier short-circuits and can never upgrade it. Upgrading is
// TryLinkArchiveToSessions's job. An operator told only "already back-links a
// note" cannot tell a real wrap note from a stub without going to look, so the
// message must name the note it declined to change.
func TestApplyBackfill_NothingToDoNamesTheNoteItIsAlreadyLinkedTo(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	stub := "Projects/p1/sessions/2026-07-16-01.md"
	seedManifestAt(t, vault, "p1", "sess-A", stub, "2026-07-14-sess-A", "2026-07-14T01:00:00Z")
	seedForwardLinkedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", "Projects/p1/transcripts/2026-07-14-sess-A.manifest.json", storage.TagAutoCapture)

	res, err := ApplyBackfill(vault, "p1", "sess-A")
	if err != nil {
		t.Fatal(err)
	}
	if res.NothingToDo == "" {
		t.Fatal("expected NothingToDo; the manifest already back-links a note")
	}
	if !strings.Contains(res.NothingToDo, stub) {
		t.Errorf("NothingToDo = %q, want it to NAME the note %q it declined to change", res.NothingToDo, stub)
	}
}
