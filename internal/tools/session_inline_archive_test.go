// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// transcriptsDirEntries lists the project's transcripts directory, tolerating
// its absence (no inline archive → the dir was never created).
func transcriptsDirEntries(t *testing.T, vault *storage.Vault, project string) []string {
	t.Helper()
	dir := filepath.Join(vault.Root, "Projects", project, "transcripts")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read transcripts dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestCaptureSessionInlineArchiveHookless is the headline of the hook-less
// parity path: on a host where the server cannot derive a session id,
// archive_transcript:true creates an inline archive pair BEFORE the note is
// written, so the existing linking machinery fires at birth — the note carries
// archive:/archive_session_id and the manifest back-links the note, with no
// new linking code involved. The id is server-minted, its provenance says so
// (archive_session_id_source: inline, session_key_source: minted), and NO
// claim sentinel is written: a minted id is one no hook will ever query.
func TestCaptureSessionInlineArchiveHookless(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "") // hook-less: nothing derivable

	cwd := t.TempDir()
	tool := CaptureSessionTool(vault, nil)
	params, _ := json.Marshal(map[string]any{
		"project":            "test-proj",
		"summary":            "Hook-less capture that archives its own transcript.",
		"transcript":         sampleClaudeJSONL,
		"archive_transcript": true,
		"cwd":                cwd,
	})

	r, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)
	if got.SessionKey == "" {
		t.Fatal("result carries no session_key")
	}

	// The archive pair exists under Projects/<slug>/transcripts/ and the
	// manifest records the inline adapter and the minted id.
	entry, err := archive.ResolveEntry(vault.Root, "test-proj", got.SessionKey)
	if err != nil {
		t.Fatalf("no archive pair for the minted id: %v", err)
	}
	if entry.Manifest.Adapter != archive.InlineAdapterName {
		t.Errorf("manifest adapter = %q, want %q", entry.Manifest.Adapter, archive.InlineAdapterName)
	}
	if _, statErr := os.Stat(entry.ArchivePath); statErr != nil {
		t.Errorf("compressed transcript missing: %v", statErr)
	}

	// The note is linked at birth: archive:, archive_session_id (= the
	// returned session_key), and honest provenance on both the id and the key.
	meta, _, err := vault.ReadSession("test-proj", got.SessionID[:10], capture.ParseFingerprint(got.SessionID), got.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	wantArchive := archive.VaultRelPath(vault.Root, entry.ManifestPath)
	if meta.Archive != wantArchive {
		t.Errorf("session.archive = %q, want %q", meta.Archive, wantArchive)
	}
	if meta.ArchiveSessionID != got.SessionKey {
		t.Errorf("archive_session_id = %q, want the returned session_key %q", meta.ArchiveSessionID, got.SessionKey)
	}
	if meta.ArchiveSessionIDSource != storage.ArchiveIDSourceInline {
		t.Errorf("archive_session_id_source = %q, want %q", meta.ArchiveSessionIDSource, storage.ArchiveIDSourceInline)
	}
	if meta.SessionKeySource != storage.KeySourceMinted {
		t.Errorf("session_key_source = %q, want %q — the mint must be recorded honestly", meta.SessionKeySource, storage.KeySourceMinted)
	}

	// The reverse direction closed too: the manifest back-links the note.
	m, err := archive.ReadManifest(entry.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote == "" || !strings.HasSuffix(m.VaultRelSessionNote, ".md") {
		t.Errorf("manifest vault_rel_session_note = %q, want the note's .md path", m.VaultRelSessionNote)
	}

	// NO claim despite cwd being set: a minted inline id must never write a
	// claim no hook will ever query.
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if hook.IsClaimed(claimDir, got.SessionKey) {
		t.Error("a claim was written for a minted inline id — no hook will ever query it")
	}
	if _, statErr := os.Stat(claimDir); !os.IsNotExist(statErr) {
		t.Errorf("claim dir %s exists on the hook-less inline path", claimDir)
	}
}

// TestCaptureSessionInlineArchiveRetryConverges pins retry convergence: a
// retry carrying the same explicit session_key and the same transcript must
// land on the SAME manifest (Create is idempotent on the source hash — one
// pair, no .bak) and update the note in place rather than duplicating it.
func TestCaptureSessionInlineArchiveRetryConverges(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "")

	tool := CaptureSessionTool(vault, nil)
	params, _ := json.Marshal(map[string]any{
		"project":            "test-proj",
		"summary":            "First attempt with an explicit key.",
		"transcript":         sampleClaudeJSONL,
		"archive_transcript": true,
		"session_key":        "inline-retry-key",
	})

	if _, err := tool.Handler(context.Background(), json.RawMessage(params)); err != nil {
		t.Fatalf("first capture: %v", err)
	}

	retry, retryErr := tool.Handler(context.Background(), json.RawMessage(params))
	if retryErr != nil {
		t.Fatalf("retry: %v", retryErr)
	}
	res := retry.(captureSessionResult)
	if !res.Updated {
		t.Error("retry minted a new note instead of updating the existing one in place")
	}

	// Exactly one archive pair, no .bak preserved-manifest: the second Create
	// matched (session_id, adapter, source hash) and skipped.
	names := transcriptsDirEntries(t, vault, "test-proj")
	if len(names) != 2 {
		t.Errorf("transcripts dir holds %v, want exactly one .jsonl.zst + one .manifest.json", names)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".bak") {
			t.Errorf("retry rewrote the archive and left %s — Create was not idempotent", n)
		}
	}

	dir, err := vault.SessionDir("test-proj")
	if err != nil {
		t.Fatal(err)
	}
	notes, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	if len(notes) != 1 {
		t.Fatalf("%d notes on disk, want 1 — the retry duplicated the session record", len(notes))
	}
}

// TestCaptureSessionInlineArchiveNoopOnDerivableHost pins the derived-empty
// gate: when the server CAN derive a host session id, archive_transcript is a
// no-op — the SessionEnd hook archives the authoritative host transcript
// later, and an inline copy would be a lossy duplicate. The derived behavior
// is untouched, including the claim sentinel.
func TestCaptureSessionInlineArchiveNoopOnDerivableHost(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "live-session-uuid")

	cwd := t.TempDir()
	tool := CaptureSessionTool(vault, nil)
	params, _ := json.Marshal(map[string]any{
		"project":            "test-proj",
		"summary":            "Derivable host: the flag must defer to the hook.",
		"transcript":         sampleClaudeJSONL,
		"archive_transcript": true,
		"cwd":                cwd,
	})

	r, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)

	if names := transcriptsDirEntries(t, vault, "test-proj"); len(names) != 0 {
		t.Errorf("inline archive %v created on a derivable host — the hook owns archiving there", names)
	}

	meta, _, err := vault.ReadSession("test-proj", got.SessionID[:10], capture.ParseFingerprint(got.SessionID), got.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	if meta.ArchiveSessionID != "live-session-uuid" {
		t.Errorf("archive_session_id = %q, want the derived id", meta.ArchiveSessionID)
	}
	if meta.ArchiveSessionIDSource != storage.ArchiveIDSourceDerived {
		t.Errorf("archive_session_id_source = %q, want %q", meta.ArchiveSessionIDSource, storage.ArchiveIDSourceDerived)
	}

	// The claim IS written on the derived path — the tightened gate must not
	// have broken it.
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if !hook.IsClaimed(claimDir, "live-session-uuid") {
		t.Error("no claim for the derived id — the tightened claim gate broke the derived path")
	}
}

// TestCaptureSessionInlineArchiveAutoOnDerivedGrok pins L4 auto-enable: a
// handshake-derived hook-less host (grok) with empty session id + non-empty
// transcript archives inline even when archive_transcript is omitted.
func TestCaptureSessionInlineArchiveAutoOnDerivedGrok(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "")
	stubClientInfoHost(t, "grok")

	tool := CaptureSessionTool(vault, nil)
	params, _ := json.Marshal(map[string]any{
		"project":    "test-proj",
		"summary":    "Derived grok host auto-archives without the flag.",
		"transcript": sampleClaudeJSONL,
	})

	r, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)
	if got.SessionKey == "" {
		t.Fatal("result carries no session_key")
	}

	entry, err := archive.ResolveEntry(vault.Root, "test-proj", got.SessionKey)
	if err != nil {
		t.Fatalf("no inline archive on derived grok auto path: %v", err)
	}
	if entry.Manifest.Adapter != archive.InlineAdapterName {
		t.Errorf("manifest adapter = %q, want %q", entry.Manifest.Adapter, archive.InlineAdapterName)
	}

	meta, _, err := vault.ReadSession("test-proj", got.SessionID[:10], capture.ParseFingerprint(got.SessionID), got.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	if meta.ArchiveSessionID != got.SessionKey {
		t.Errorf("archive_session_id = %q, want session_key %q", meta.ArchiveSessionID, got.SessionKey)
	}
	if meta.ArchiveSessionIDSource != storage.ArchiveIDSourceInline {
		t.Errorf("archive_session_id_source = %q, want %q", meta.ArchiveSessionIDSource, storage.ArchiveIDSourceInline)
	}
	if meta.Host != "grok" || meta.HostSource != storage.HostSourceDerived {
		t.Errorf("host=%q source=%q, want grok/%s", meta.Host, meta.HostSource, storage.HostSourceDerived)
	}
}

// TestCaptureSessionInlineArchiveAutoOffUnknownHost pins that empty session id
// + transcript alone is NOT enough: unknown host (Claude-miss-shaped) must not
// mint an inline archive when the flag is omitted. Replaces the old
// FlagOffUnchanged pin whose premise (flag off = never archive) is false under
// auto-on for derived hook-less hosts.
func TestCaptureSessionInlineArchiveAutoOffUnknownHost(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "")
	// clientInfoHost defaults to "" → HostUnknown / HostSourceUnknown.

	cwd := t.TempDir()
	tool := CaptureSessionTool(vault, nil)
	params, _ := json.Marshal(map[string]any{
		"project":    "test-proj",
		"summary":    "Unknown host must not auto-archive.",
		"transcript": sampleClaudeJSONL,
		"cwd":        cwd,
	})

	r, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)
	if got.Status != "ok" {
		t.Errorf("status = %q, want ok", got.Status)
	}

	if names := transcriptsDirEntries(t, vault, "test-proj"); len(names) != 0 {
		t.Errorf("archive %v created for unknown host without archive_transcript", names)
	}
	if _, statErr := os.Stat(filepath.Join(cwd, ".vibe-palace")); !os.IsNotExist(statErr) {
		t.Error("claim dir created without a derivable id")
	}

	meta, _, err := vault.ReadSession("test-proj", got.SessionID[:10], capture.ParseFingerprint(got.SessionID), got.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	if meta.ArchiveSessionID != "" || meta.ArchiveSessionIDSource != "" || meta.Archive != "" {
		t.Errorf("note pretends to link (id=%q source=%q archive=%q) — unknown host must not auto-archive",
			meta.ArchiveSessionID, meta.ArchiveSessionIDSource, meta.Archive)
	}
}

// TestCaptureSessionInlineArchiveAutoOffDerivedClaude pins that a derived
// Claude-family host never auto-archives on empty session id (derivation-miss
// shape) when the flag is omitted — SessionEnd remains the authority.
func TestCaptureSessionInlineArchiveAutoOffDerivedClaude(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "")
	stubClientInfoHost(t, "claude-code")

	tool := CaptureSessionTool(vault, nil)
	params, _ := json.Marshal(map[string]any{
		"project":    "test-proj",
		"summary":    "Derived Claude host must not auto-archive on derivation miss.",
		"transcript": sampleClaudeJSONL,
	})

	r, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)

	if names := transcriptsDirEntries(t, vault, "test-proj"); len(names) != 0 {
		t.Errorf("archive %v created for derived claude* without force flag", names)
	}
	meta, _, err := vault.ReadSession("test-proj", got.SessionID[:10], capture.ParseFingerprint(got.SessionID), got.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	if meta.ArchiveSessionID != "" || meta.Archive != "" {
		t.Errorf("note links (id=%q archive=%q) — Claude derivation-miss must not mint inline",
			meta.ArchiveSessionID, meta.Archive)
	}
}

// TestCaptureSessionInlineArchiveExplicitTrueAnyHost pins that archive_transcript
// true still forces under empty id + transcript even without a hook-less host
// signal (template pin / intentional path).
func TestCaptureSessionInlineArchiveExplicitTrueAnyHost(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "")
	// Unknown host — no auto signal; explicit true must still force.
	stubClientInfoHost(t, "")

	tool := CaptureSessionTool(vault, nil)
	params, _ := json.Marshal(map[string]any{
		"project":            "test-proj",
		"summary":            "Explicit force on unknown host.",
		"transcript":         sampleClaudeJSONL,
		"archive_transcript": true,
	})

	r, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)
	if _, err := archive.ResolveEntry(vault.Root, "test-proj", got.SessionKey); err != nil {
		t.Fatalf("explicit archive_transcript:true did not create inline archive: %v", err)
	}
}

// TestCaptureSessionInlineArchiveFailureStillWritesNote pins the priority: a
// failed inline archive must never cost the note. The loss surfaces as the
// machine-parseable incomplete-capture error naming stage transcript_archive,
// the payload carries the session_key for a convergent retry, and the note's
// frontmatter does not pretend to link.
func TestCaptureSessionInlineArchiveFailureStillWritesNote(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "")

	// Force archive.Create to fail: plant a regular FILE where the transcripts
	// directory must go, so its MkdirAll errors.
	projDir := filepath.Join(vault.Root, "Projects", "test-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "transcripts"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := CaptureSessionTool(vault, nil)
	params, _ := json.Marshal(map[string]any{
		"project":            "test-proj",
		"summary":            "Capture whose inline archive cannot be written.",
		"transcript":         sampleClaudeJSONL,
		"archive_transcript": true,
	})

	_, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err == nil {
		t.Fatal("the inline archive was lost and the handler returned success")
	}

	msg := err.Error()
	brace := strings.Index(msg, "{")
	if brace < 0 {
		t.Fatalf("error carries no JSON payload: %s", msg)
	}
	var payload struct {
		Captured   bool     `json:"captured"`
		NotePath   string   `json:"note_path"`
		SessionID  string   `json:"session_id"`
		SessionKey string   `json:"session_key"`
		Lost       []string `json:"lost"`
	}
	if uerr := json.Unmarshal([]byte(msg[brace:]), &payload); uerr != nil {
		t.Fatalf("error payload is not JSON: %v (%s)", uerr, msg)
	}

	if !payload.Captured {
		t.Error("payload says captured=false, but the note WAS written")
	}
	if payload.NotePath == "" {
		t.Fatal("payload names no note_path")
	}
	if _, statErr := os.Stat(filepath.Join(vault.Root, filepath.FromSlash(payload.NotePath))); statErr != nil {
		t.Errorf("the note did not survive the archive failure: %v", statErr)
	}
	if payload.SessionKey == "" {
		t.Error("payload carries no session_key — the retry cannot converge on the same manifest")
	}
	if len(payload.Lost) != 1 || payload.Lost[0] != capture.StageTranscriptArchive {
		t.Errorf("lost = %v, want exactly [%s]", payload.Lost, capture.StageTranscriptArchive)
	}

	// Nothing pretends to link: the id was minted but the archive never
	// materialized, so the frontmatter must stay honestly unlinked.
	meta, _, rerr := vault.ReadSession("test-proj", payload.SessionID[:10], capture.ParseFingerprint(payload.SessionID), capture.ParseIteration(payload.SessionID))
	if rerr != nil {
		t.Fatalf("read session back: %v", rerr)
	}
	if meta.ArchiveSessionID != "" || meta.Archive != "" {
		t.Errorf("note links (id=%q archive=%q) to an archive that was never written",
			meta.ArchiveSessionID, meta.Archive)
	}
}
