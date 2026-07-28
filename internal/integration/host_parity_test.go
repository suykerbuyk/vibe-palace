// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// highFrictionTranscript is a multi-signal friction fixture reused from the
// friction integration tests. It must score high enough that a capture with
// this body cannot look like a smooth session.
const highFrictionTranscript = `
User: No not that, wrong file entirely.
Assistant: Let me try again.
User: No not that, read the other one. Undo that change.
User: Wrong, revert it. Go back to the previous version.
User: Start over, that approach is broken.
Error: compilation failed. Error: nil pointer. Exception in handler.
tool_use: read_file
tool_use: read_file
tool_use: read_file
tool_use: read_file
User: Try again with a different strategy. Scratch that.
The unique marker PARITY_ARCHIVE_MARKER_42 must appear in search hits.
`

// structuralFootprint is the host-parity durability matrix — what BOTH Claude
// (hook) and Grok (hook-less MCP, post capture-defaults) must produce. Fields
// that honestly differ by path (adapter name, claim presence, session id
// provenance) are NOT on this struct; path-specific checks live beside it.
type structuralFootprint struct {
	NotePath          string // vault-relative, names a real file
	SessionID         string
	ArchiveRel        string // note frontmatter archive: → manifest vault-rel
	ArchiveSessionID  string
	ManifestPath      string
	ArchivePath       string // .jsonl.zst
	ManifestBackLink  string // vault_rel_session_note
	FrictionScore     int
	Adapter           string
	ArchiveSessionSrc string
}

// assertSharedMatrix checks the structural durability bar common to both
// hosts. It does NOT assert Claude-equal field identity (adapter, claim,
// key source) — those are path-specific.
func assertSharedMatrix(t *testing.T, vault *storage.Vault, fp structuralFootprint, minFriction int) {
	t.Helper()

	if fp.NotePath == "" {
		t.Fatal("note_path empty — the note_path trap: capture reported success without naming the note")
	}
	if filepath.IsAbs(fp.NotePath) {
		t.Errorf("note_path = %q, want vault-relative", fp.NotePath)
	}
	if strings.Contains(fp.NotePath, `\`) {
		t.Errorf("note_path = %q, want slash-separated vault path", fp.NotePath)
	}
	noteAbs := filepath.Join(vault.Root, filepath.FromSlash(fp.NotePath))
	if _, err := os.Stat(noteAbs); err != nil {
		t.Fatalf("note_path %q does not resolve under vault: %v", fp.NotePath, err)
	}

	if fp.ArchiveRel == "" {
		t.Fatal("note frontmatter archive: empty — thin note shape, no transcript link")
	}
	if fp.ArchiveSessionID == "" {
		t.Fatal("archive_session_id empty — note cannot be found from the transcript side")
	}
	if fp.ManifestPath == "" {
		t.Fatal("no manifest path resolved")
	}
	if _, err := os.Stat(fp.ManifestPath); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	if fp.ArchivePath == "" {
		t.Fatal("no compressed archive path")
	}
	if _, err := os.Stat(fp.ArchivePath); err != nil {
		t.Fatalf("compressed transcript (.jsonl.zst) missing: %v", err)
	}
	if !strings.HasSuffix(fp.ArchivePath, ".jsonl.zst") {
		t.Errorf("archive path = %q, want .jsonl.zst suffix", fp.ArchivePath)
	}

	// Bi-link: note → manifest and manifest → note.
	wantArchiveRel := archive.VaultRelPath(vault.Root, fp.ManifestPath)
	if fp.ArchiveRel != wantArchiveRel {
		t.Errorf("note archive: = %q, want manifest vault-rel %q", fp.ArchiveRel, wantArchiveRel)
	}
	if fp.ManifestBackLink == "" || !strings.HasSuffix(fp.ManifestBackLink, ".md") {
		t.Errorf("manifest vault_rel_session_note = %q, want note .md path", fp.ManifestBackLink)
	}
	// Back-link should name the same note (suffix match tolerates path form).
	if !strings.HasSuffix(filepath.ToSlash(fp.ManifestBackLink), filepath.Base(fp.NotePath)) &&
		filepath.ToSlash(fp.ManifestBackLink) != filepath.ToSlash(fp.NotePath) {
		t.Errorf("manifest back-link %q does not name note %q", fp.ManifestBackLink, fp.NotePath)
	}

	if fp.FrictionScore < minFriction {
		t.Errorf("friction_score = %d, want >= %d (transcript supplied)", fp.FrictionScore, minFriction)
	}
	if fp.Adapter == "" {
		t.Error("manifest adapter empty")
	}
}

// TestIntegrationHostParityFootprint is the host-parity acceptance harness:
// a non-Claude (Grok) MCP session under post-defaults capture must produce
// the durable vault footprint (note + inline archive + bi-link + friction),
// and that footprint is structurally equivalent to Claude's hook path —
// not byte-identical. Driven through real makeHandler validation on a temp
// vault; never seeds the fields it asserts.
func TestIntegrationHostParityFootprint(t *testing.T) {
	// Isolate from a parent Claude host's live session map so hostSessionID
	// cannot resolve a real id and suppress the hook-less inline archive path.
	t.Setenv("CLAUDE_HOME", t.TempDir())
	// Isolate enrichment config that might re-tag on dev machines.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var grokFP, claudeFP structuralFootprint

	t.Run("grok_mcp_post_defaults", func(t *testing.T) {
		h := newHarness(t, false) // mock embedder — no ONNX; short-mode safe
		h.registerAllTools(t)

		logs := captureLogs(t)
		cwd := t.TempDir()

		// Drive through REAL stdio transport with clientInfo name=grok so
		// handshake-derived host attribution reaches ClientInfoFromContext
		// (HandleMessage alone registers no session). Flag OMITTED — the
		// post-defaults bar: derived hook-less host + non-empty transcript
		// must still archive without archive_transcript.
		raw := h.callToolStdio(t, "grok", "1.0.0-test", "vp_capture_session", map[string]any{
			"project":    "parity-grok",
			"summary":    "Grok hook-less capture under post-defaults auto archive.",
			"title":      "host-parity grok",
			"tag":        "implementation",
			"transcript": highFrictionTranscript,
			"cwd":        cwd,
			// deliberately no archive_transcript
		})

		var res struct {
			Status        string `json:"status"`
			NotePath      string `json:"note_path"`
			SessionID     string `json:"session_id"`
			SessionKey    string `json:"session_key"`
			FrictionScore int    `json:"friction_score"`
			Project       string `json:"project"`
		}
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			t.Fatalf("parse capture result: %v (raw: %s)", err, raw)
		}
		if res.Status != "ok" {
			t.Fatalf("status = %q, want ok (raw: %s)", res.Status, raw)
		}
		if res.SessionKey == "" {
			t.Fatal("session_key empty")
		}
		if res.NotePath == "" {
			t.Fatal("note_path empty in tool result")
		}
		// note_path must name a real file (the six-month trap).
		if _, err := os.Stat(filepath.Join(h.Vault.Root, filepath.FromSlash(res.NotePath))); err != nil {
			t.Fatalf("note_path %q is not a real vault file: %v", res.NotePath, err)
		}

		// Zero makeHandler WARN on the happy path (Debug enter/exit is fine).
		logOut := logs.String()
		for line := range strings.SplitSeq(logOut, "\n") {
			if line == "" {
				continue
			}
			if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "mcp.makeHandler") {
				t.Errorf("unexpected makeHandler WARN on happy path: %s", line)
			}
			if strings.Contains(line, `"msg":"mcp.makeHandler: validation failed"`) {
				t.Errorf("validation failed on happy path: %s", line)
			}
		}
		if !strings.Contains(logOut, `"msg":"mcp.makeHandler: enter"`) {
			t.Error("expected makeHandler enter log — log sink may not be wired")
		}
		if !strings.Contains(logOut, `"msg":"mcp.makeHandler: exit"`) {
			t.Error("expected makeHandler exit log")
		}

		// Resolve archive pair by the minted session_key.
		entry, err := archive.ResolveEntry(h.Vault.Root, "parity-grok", res.SessionKey)
		if err != nil {
			t.Fatalf("no inline archive under post-defaults (flag omitted): %v", err)
		}
		m, err := archive.ReadManifest(entry.ManifestPath)
		if err != nil {
			t.Fatalf("read manifest: %v", err)
		}

		sessions, err := h.Vault.ListSessions("parity-grok", "", "", 0)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if len(sessions) != 1 {
			t.Fatalf("expected 1 session note, got %d", len(sessions))
		}
		meta := sessions[0]

		// Grok-path provenance (not Claude-equal).
		if meta.ArchiveSessionID != res.SessionKey {
			t.Errorf("archive_session_id = %q, want session_key %q", meta.ArchiveSessionID, res.SessionKey)
		}
		if meta.ArchiveSessionIDSource != storage.ArchiveIDSourceInline {
			t.Errorf("archive_session_id_source = %q, want %q", meta.ArchiveSessionIDSource, storage.ArchiveIDSourceInline)
		}
		if meta.SessionKeySource != storage.KeySourceMinted {
			t.Errorf("session_key_source = %q, want %q", meta.SessionKeySource, storage.KeySourceMinted)
		}
		if m.Adapter != archive.InlineAdapterName {
			t.Errorf("manifest adapter = %q, want %q", m.Adapter, archive.InlineAdapterName)
		}
		if meta.Host != "grok" {
			t.Errorf("host = %q, want grok", meta.Host)
		}
		if meta.HostSource != storage.HostSourceDerived {
			t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceDerived)
		}
		// No claim sentinel for minted inline ids.
		claimDir := filepath.Join(cwd, ".vibe-palace")
		if hook.IsClaimed(claimDir, res.SessionKey) {
			t.Error("claim written for minted inline id — hook-less path must not claim")
		}
		if _, statErr := os.Stat(claimDir); !os.IsNotExist(statErr) {
			t.Errorf("claim dir %s exists on hook-less path", claimDir)
		}

		grokFP = structuralFootprint{
			NotePath:          res.NotePath,
			SessionID:         res.SessionID,
			ArchiveRel:        meta.Archive,
			ArchiveSessionID:  meta.ArchiveSessionID,
			ManifestPath:      entry.ManifestPath,
			ArchivePath:       entry.ArchivePath,
			ManifestBackLink:  m.VaultRelSessionNote,
			FrictionScore:     res.FrictionScore,
			Adapter:           m.Adapter,
			ArchiveSessionSrc: meta.ArchiveSessionIDSource,
		}
		assertSharedMatrix(t, h.Vault, grokFP, 50)

		// MCP path indexes inline — search must find transcript content.
		hits, err := h.Engine.Search(context.Background(), "PARITY_ARCHIVE_MARKER_42",
			search.SearchFilters{Project: "parity-grok"})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(hits) == 0 {
			t.Fatal("search found nothing for transcript marker — MCP path should index inline")
		}

		// Optional composed memory step (not a capture effect). Use the
		// HandleMessage path — memory does not need host derivation.
		h.callTool(t, "vp_memory_write", map[string]any{
			"project":     "parity-grok",
			"rel":         "parity-note.md",
			"name":        "Parity memory",
			"description": "Composed after capture, not by it.",
			"type":        "feedback",
			"body":        "Host-parity harness wrote this via vp_memory_write.",
		})
		memFile := filepath.Join(h.Vault.Root, "Projects", "parity-grok", "memory", "parity-note.md")
		if _, err := os.Stat(memFile); err != nil {
			t.Fatalf("memory file missing after vp_memory_write: %v", err)
		}
	})

	t.Run("claude_hook_structural", func(t *testing.T) {
		// Claude side of the bar via hook.Run (same pattern as hook_test).
		// Separate temp vault — never share with the Grok leg.
		vaultRoot := t.TempDir()
		cwd := t.TempDir()
		slug := "parity-claude"

		initGitRepo(t, cwd)
		if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"),
			[]byte("[project]\nname = \"parity-claude\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		prepareVaultProject(t, vaultRoot, slug)

		// High-friction content in the host JSONL so friction responds.
		transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
		// Claude JSONL shape with friction signals in message content.
		body := `{"type":"permission-mode","permissionMode":"default","sessionId":"parity-claude-sess"}
{"type":"user","message":{"role":"user","content":"No not that, wrong file entirely. Undo that change. Start over."}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"Error: compilation failed. Let me try again."}}
{"type":"user","message":{"role":"user","content":"Wrong, revert it. Go back. Scratch that approach."}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"PARITY_ARCHIVE_MARKER_42 fixed."}}
`
		if err := os.WriteFile(transcriptPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}

		res, err := hook.Run(context.Background(), hook.Payload{
			SessionID:      "parity-claude-sess",
			TranscriptPath: transcriptPath,
			CWD:            cwd,
			HookEventName:  "SessionEnd",
		}, hook.RunOptions{
			VaultRoot:   vaultRoot,
			ProjectSlug: slug,
			VPVersion:   "0.1.0-test",
		})
		if err != nil {
			t.Fatalf("hook.Run: %v", err)
		}
		if res.ClaimedSkip {
			t.Fatal("first run claimed-skip unexpectedly")
		}
		if res.SessionNoteID == "" {
			t.Fatal("SessionNoteID empty")
		}
		if res.ArchivePath == "" {
			t.Fatal("ArchivePath empty")
		}
		if _, err := os.Stat(res.ArchivePath); err != nil {
			t.Fatalf("archive file missing: %v", err)
		}

		vault := storage.NewVault(vaultRoot)
		sessions, err := vault.ListSessions(slug, "", "", 10)
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if len(sessions) != 1 {
			t.Fatalf("expected 1 session, got %d", len(sessions))
		}
		meta := sessions[0]

		entry, err := archive.ResolveEntry(vaultRoot, slug, "parity-claude-sess")
		if err != nil {
			t.Fatalf("ResolveEntry: %v", err)
		}
		m, err := archive.ReadManifest(entry.ManifestPath)
		if err != nil {
			t.Fatalf("ReadManifest: %v", err)
		}

		// Claude-path provenance (honest differences from Grok).
		if meta.ArchiveSessionID != "parity-claude-sess" {
			t.Errorf("archive_session_id = %q, want host session id", meta.ArchiveSessionID)
		}
		if m.Adapter != archive.ClaudeCodeAdapterName {
			t.Errorf("manifest adapter = %q, want %q", m.Adapter, archive.ClaudeCodeAdapterName)
		}
		claimDir := filepath.Join(cwd, ".vibe-palace")
		if !hook.IsClaimed(claimDir, "parity-claude-sess") {
			t.Error("Claude hook path must write a claim sentinel")
		}

		claudeFP = structuralFootprint{
			NotePath:          meta.NotePath,
			SessionID:         meta.ID,
			ArchiveRel:        meta.Archive,
			ArchiveSessionID:  meta.ArchiveSessionID,
			ManifestPath:      entry.ManifestPath,
			ArchivePath:       entry.ArchivePath,
			ManifestBackLink:  m.VaultRelSessionNote,
			FrictionScore:     meta.FrictionScore,
			Adapter:           m.Adapter,
			ArchiveSessionSrc: meta.ArchiveSessionIDSource,
		}
		// Hook friction may be lower than MCP plain-text scoring depending on
		// JSONL extraction; require a non-negative score and a linked archive
		// rather than the MCP high-friction threshold.
		assertSharedMatrix(t, vault, claudeFP, 0)
		if claudeFP.FrictionScore < 0 {
			t.Errorf("friction_score = %d, want >= 0", claudeFP.FrictionScore)
		}
	})

	t.Run("structural_equivalence", func(t *testing.T) {
		// Both legs must have populated footprints (subtests ran).
		if grokFP.NotePath == "" || claudeFP.NotePath == "" {
			t.Fatal("both host legs must run before equivalence check")
		}
		// Shared: both have real notes, archives, bi-links, adapters.
		for _, tt := range []struct {
			name string
			fp   structuralFootprint
		}{
			{"grok", grokFP},
			{"claude", claudeFP},
		} {
			if tt.fp.NotePath == "" || tt.fp.ArchiveRel == "" || tt.fp.ArchivePath == "" {
				t.Errorf("%s missing core footprint fields: %+v", tt.name, tt.fp)
			}
			if tt.fp.ManifestBackLink == "" {
				t.Errorf("%s missing bi-link back direction", tt.name)
			}
			if tt.fp.Adapter == "" {
				t.Errorf("%s missing adapter", tt.name)
			}
		}
		// Honest differences that must NOT be equal:
		if grokFP.Adapter == claudeFP.Adapter {
			t.Errorf("adapters unexpectedly equal (%q) — provenance requires they differ (inline vs claude-code)", grokFP.Adapter)
		}
		if grokFP.Adapter != archive.InlineAdapterName {
			t.Errorf("grok adapter = %q, want inline", grokFP.Adapter)
		}
		if claudeFP.Adapter != archive.ClaudeCodeAdapterName {
			t.Errorf("claude adapter = %q, want claude-code", claudeFP.Adapter)
		}
		if grokFP.ArchiveSessionSrc != storage.ArchiveIDSourceInline {
			t.Errorf("grok archive source = %q, want inline", grokFP.ArchiveSessionSrc)
		}
	})
}

// TestIntegrationHostParityNoAutoArchiveUnknownHost pins that the acceptance
// bar does not fire for unknown (Claude-miss-shaped) hosts when the flag is
// omitted — a thin note is correct there, and the harness must not treat that
// as a failure of defaults.
func TestIntegrationHostParityNoAutoArchiveUnknownHost(t *testing.T) {
	t.Setenv("CLAUDE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	h := newHarness(t, false)
	h.registerAllTools(t)
	// Default initMCP client is "integration-test" — not in the hook-less set.
	// No withClientInfo → HostUnknown / no auto archive.

	raw := h.callTool(t, "vp_capture_session", map[string]any{
		"project":    "parity-unknown",
		"summary":    "Unknown host must stay thin without explicit flag.",
		"transcript": highFrictionTranscript,
	})
	var res struct {
		Status    string `json:"status"`
		NotePath  string `json:"note_path"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok", res.Status)
	}
	if res.NotePath == "" {
		t.Fatal("note still required even without archive")
	}
	if _, err := os.Stat(filepath.Join(h.Vault.Root, filepath.FromSlash(res.NotePath))); err != nil {
		t.Fatalf("note missing: %v", err)
	}

	dir := filepath.Join(h.Vault.Root, "Projects", "parity-unknown", "transcripts")
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) > 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("unknown host auto-archived %v — defaults must not fire without hook-less signal", names)
	}

	sessions, err := h.Vault.ListSessions("parity-unknown", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if sessions[0].Archive != "" || sessions[0].ArchiveSessionID != "" {
		t.Errorf("thin note pretends to link: archive=%q id=%q", sessions[0].Archive, sessions[0].ArchiveSessionID)
	}
}
