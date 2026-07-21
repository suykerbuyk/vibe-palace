// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// seedManifestAt is seedManifest with explicit stem and captured_at, so one
// session can own SEVERAL manifests (the PreCompact-then-SessionEnd shape) —
// the case the newest-stranded target rule exists for.
func seedManifestAt(t *testing.T, vault *storage.Vault, p, sessionID, noteRel, stem, capturedAt string) string {
	t.Helper()
	dir := filepath.Join(vault.Root, "Projects", p, "transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault.Root, "palace", p), 0o755); err != nil {
		t.Fatal(err)
	}
	m := archive.Manifest{
		SchemaVersion:       1,
		Adapter:             "claude-code",
		SessionID:           sessionID,
		ProjectSlug:         p,
		CapturedAt:          capturedAt,
		VaultRelSessionNote: noteRel,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(dir, stem+".manifest.json")
	if err := os.WriteFile(mp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stem+".jsonl.zst"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return "Projects/" + p + "/transcripts/" + stem + ".manifest.json"
}

// seedKeyedNote writes a session note with a real, parseable frontmatter
// identity block. name must be a valid session stem + ".md" (e.g.
// "2026-07-16-01.md") so the storage scanners can parse its coordinates.
func seedKeyedNote(t *testing.T, vault *storage.Vault, p, name, key, source, archiveSessionID, tag string) string {
	t.Helper()
	dir := filepath.Join(vault.Root, "Projects", p, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "session_key: %s\n", key)
	fmt.Fprintf(&b, "session_key_source: %s\n", source)
	if archiveSessionID != "" {
		fmt.Fprintf(&b, "archive_session_id: %s\n", archiveSessionID)
	}
	if tag != "" {
		fmt.Fprintf(&b, "tag: %s\n", tag)
	}
	b.WriteString("title: t\n---\nbody\n")
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return "Projects/" + p + "/sessions/" + name
}

func TestBackfillCandidates_PairsCallerKeyedNoteWithStranded(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// The candidate: stranded manifest + caller-keyed unstamped note.
	noteRel := seedKeyedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", "caller", "", "")
	target := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A", "2026-07-14T01:00:00Z")

	// Excluded: minted key that coincidentally matches a stranded manifest.
	seedKeyedNote(t, vault, "p1", "2026-07-16-02.md", "sess-B", "minted", "", "")
	seedManifestAt(t, vault, "p1", "sess-B", "", "2026-07-14-sess-B", "2026-07-14T02:00:00Z")

	// Excluded: manifest already linked.
	seedKeyedNote(t, vault, "p1", "2026-07-16-03.md", "sess-C", "caller", "", "")
	seedManifestAt(t, vault, "p1", "sess-C", "Projects/p1/sessions/2026-07-16-03.md",
		"2026-07-14-sess-C", "2026-07-14T03:00:00Z")

	cands, err := BackfillCandidates(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(cands), cands)
	}
	c := cands[0]
	if c.Project != "p1" || c.SessionID != "sess-A" || c.TargetManifest != target {
		t.Errorf("candidate = %+v, want p1/sess-A -> %s", c, target)
	}
	if len(c.NoteRels) != 1 || c.NoteRels[0] != noteRel {
		t.Errorf("NoteRels = %v, want [%s]", c.NoteRels, noteRel)
	}
}

func TestBackfillCandidates_TargetIsNewestStranded(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedKeyedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", "caller", "", "")
	// Same session, two stranded manifests (PreCompact then SessionEnd).
	seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A-early", "2026-07-14T01:00:00Z")
	newest := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A-late", "2026-07-14T09:00:00Z")

	cands, err := BackfillCandidates(vault)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("got %d candidates, want 1", len(cands))
	}
	if cands[0].TargetManifest != newest {
		t.Errorf("TargetManifest = %s, want the newest stranded %s", cands[0].TargetManifest, newest)
	}
	if len(cands[0].StrandedManifests) != 2 {
		t.Errorf("StrandedManifests = %v, want both", cands[0].StrandedManifests)
	}
}

func TestBackfillCandidates_UnreadableTranscriptsDirIsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission bits do not bind")
	}
	vault := storage.NewVault(t.TempDir())
	seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A", "2026-07-14T01:00:00Z")
	dir := filepath.Join(vault.Root, "Projects", "p1", "transcripts")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := BackfillCandidates(vault)
	if err == nil {
		t.Fatal("want an error for an unreadable transcripts dir — 'I could not look' is not 'no candidates'")
	}
}

func TestApplyBackfill_EndToEnd(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	noteRel := seedKeyedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", "caller", "", "implementation")
	seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A-early", "2026-07-14T01:00:00Z")
	targetRel := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A-late", "2026-07-14T09:00:00Z")

	res, err := ApplyBackfill(vault, "p1", "sess-A")
	if err != nil {
		t.Fatalf("ApplyBackfill: %v", err)
	}
	if res.NothingToDo != "" {
		t.Fatalf("NothingToDo = %q, want work done", res.NothingToDo)
	}
	if res.TargetManifest != targetRel {
		t.Errorf("TargetManifest = %s, want newest stranded %s", res.TargetManifest, targetRel)
	}
	if len(res.NotesUpdated) != 1 || res.NotesUpdated[0] != noteRel {
		t.Errorf("NotesUpdated = %v, want [%s]", res.NotesUpdated, noteRel)
	}
	if res.Canonical != noteRel {
		t.Errorf("Canonical = %s, want %s", res.Canonical, noteRel)
	}

	// Ask the artifacts, not the result struct: the note is stamped with
	// backfilled provenance and forward-links the target...
	data, err := os.ReadFile(filepath.Join(vault.Root, filepath.FromSlash(noteRel)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"archive_session_id: sess-A",
		"archive_session_id_source: backfilled",
		"archive: " + targetRel,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("note is missing %q:\n%s", want, data)
		}
	}
	// ...and the target manifest back-links the note.
	m, err := archive.ReadManifest(filepath.Join(vault.Root, filepath.FromSlash(targetRel)))
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote != noteRel {
		t.Errorf("manifest back-link = %q, want %s", m.VaultRelSessionNote, noteRel)
	}

	// Idempotent re-run: the target is no longer stranded, and the ONLY other
	// stranded manifest (the early one) stays stranded BY DESIGN — but the
	// note, now stamped, is no longer a candidate, so its "apply" is honest
	// about doing nothing new. Re-running against this session applies to the
	// remaining stranded manifest? No: the note already links somewhere, and
	// BackfillArchiveLink never re-points. Assert the second run reports that.
	res2, err := ApplyBackfill(vault, "p1", "sess-A")
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	if len(res2.NotesUpdated) != 0 {
		t.Errorf("re-run rewrote notes: %v", res2.NotesUpdated)
	}
}

func TestApplyBackfill_NothingToDoWhenNotStranded(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	noteRel := seedKeyedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", "caller", "", "")
	seedManifestAt(t, vault, "p1", "sess-A", noteRel, "2026-07-14-sess-A", "2026-07-14T01:00:00Z")

	res, err := ApplyBackfill(vault, "p1", "sess-A")
	if err != nil {
		t.Fatal(err)
	}
	if res.NothingToDo == "" {
		t.Fatal("want an honest nothing-to-do for an already-linked session")
	}
}

func TestApplyBackfill_ErrorsHonestly(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// No manifest at all for the id.
	seedManifestAt(t, vault, "p1", "sess-OTHER", "", "2026-07-14-sess-OTHER", "2026-07-14T01:00:00Z")
	if _, err := ApplyBackfill(vault, "p1", "sess-A"); err == nil ||
		!strings.Contains(err.Error(), "no manifest for session id") {
		t.Errorf("no-manifest error = %v, want it to say so", err)
	}

	// Stranded manifest, but no note carries the key: not derivable.
	seedManifestAt(t, vault, "p1", "sess-B", "", "2026-07-14-sess-B", "2026-07-14T02:00:00Z")
	if _, err := ApplyBackfill(vault, "p1", "sess-B"); err == nil ||
		!strings.Contains(err.Error(), "not derivable") {
		t.Errorf("not-derivable error = %v, want it to say so", err)
	}
}

// The annotation changes the finding MESSAGE and only the message: identity is
// (Dimension, Artifact), and the accepted baseline is keyed on artifacts — an
// annotation that perturbed them would churn a 107-entry ledger.
func TestArchiveRoundTrip_AnnotatesRecoverable(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// Recoverable session with an older stranded sibling.
	seedKeyedNote(t, vault, "p1", "2026-07-16-01.md", "sess-A", "caller", "", "")
	early := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A-early", "2026-07-14T01:00:00Z")
	target := seedManifestAt(t, vault, "p1", "sess-A", "", "2026-07-14-sess-A-late", "2026-07-14T09:00:00Z")

	// Stranded and NOT recoverable: no note carries its key.
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
	// All three stranded manifests are findings, keyed by their unchanged rels.
	if len(byArtifact) != 3 {
		t.Fatalf("findings = %v, want the 3 stranded manifests", byArtifact)
	}

	if d := byArtifact[target]; !strings.Contains(d, "RECOVERABLE") ||
		!strings.Contains(d, "vp archive link sess-A -p p1") {
		t.Errorf("target detail = %q, want the RECOVERABLE annotation with the apply command", d)
	}
	if d := byArtifact[early]; !strings.Contains(d, "stays stranded by design") {
		t.Errorf("sibling detail = %q, want the stays-stranded annotation", d)
	}
	if d := byArtifact[lost]; strings.Contains(d, "RECOVERABLE") || strings.Contains(d, "recoverable") {
		t.Errorf("non-candidate detail = %q, want NO recoverable annotation", d)
	}
}
