// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testSessionVault(t *testing.T) *storage.Vault {
	t.Helper()
	dir := t.TempDir()
	return storage.NewVault(dir)
}

// stubHostSessionID pins the handler's server-side session-id derivation for
// the duration of a test. An empty id models "cannot be confirmed" (non-Claude
// host, no sessions file): the note must stay honestly unlinked.
func stubHostSessionID(t *testing.T, id string) {
	t.Helper()
	orig := hostSessionID
	hostSessionID = func() string { return id }
	t.Cleanup(func() { hostSessionID = orig })
}

func TestCaptureSessionSchema(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)
	if tool.Name != "vp_capture_session" {
		t.Errorf("tool name = %q, want %q", tool.Name, "vp_capture_session")
	}
	if tool.Description == "" {
		t.Error("tool description is empty")
	}
	// Verify schema is valid JSON.
	var schema map[string]any
	if err := json.Unmarshal(tool.Schema, &schema); err != nil {
		t.Fatalf("invalid schema JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v, want object", schema["type"])
	}
}

func TestCaptureSessionBasic(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Implemented chunking engine for Phase 5."
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r, ok := result.(captureSessionResult)
	if !ok {
		t.Fatalf("unexpected result type: %T", result)
	}
	if r.Status != "ok" {
		t.Errorf("status = %q, want %q", r.Status, "ok")
	}
	if r.Project != "test-proj" {
		t.Errorf("project = %q, want %q", r.Project, "test-proj")
	}
	if r.SessionID == "" {
		t.Error("session_id is empty")
	}
	if r.Iteration < 1 {
		t.Errorf("iteration = %d, want >= 1", r.Iteration)
	}
}

func TestCaptureSessionWithAllFields(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Full session with all metadata.",
		"title": "Test Session",
		"tag": "implementation",
		"model": "claude-opus-4-6",
		"decisions": ["Use brute-force search", "Skip HNSW"],
		"files_changed": ["internal/capture/chunker.go"],
		"open_threads": ["Phase 6 next"]
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(captureSessionResult)
	if r.Status != "ok" {
		t.Errorf("status = %q, want %q", r.Status, "ok")
	}
}

func TestCaptureSessionWithTranscript(t *testing.T) {
	vault := testSessionVault(t)
	indexer := capture.NewIndexer(vault, nil, nil, storage.Config{}) // no engine = no embedding
	tool := CaptureSessionTool(vault, indexer)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Session with transcript.",
		"transcript": "We discussed the chunking algorithm and decided on sliding window with 800 char chunks."
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}

	r := result.(captureSessionResult)
	if r.Status != "ok" {
		t.Errorf("status = %q, want %q", r.Status, "ok")
	}
}

func TestCaptureSessionArchiveLink(t *testing.T) {
	vault := testSessionVault(t)

	// Seed a real archive under the project's transcripts dir.
	ctx := context.Background()
	srcPath := filepath.Join(t.TempDir(), "src.jsonl")
	if err := os.WriteFile(srcPath, []byte(sampleClaudeJSONL), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := archive.Create(archive.CreateOptions{
		Adapter:     archive.ClaudeCodeAdapterName,
		SessionID:   "link-session",
		SourcePath:  srcPath,
		VaultRoot:   vault.Root,
		ProjectSlug: "test-proj",
	})
	if err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	// The id reaches the note by SERVER-SIDE DERIVATION, never from the
	// caller — stub the resolver to the seeded session.
	stubHostSessionID(t, "link-session")

	tool := CaptureSessionTool(vault, nil)
	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Session linked to a pre-existing archive."
	}`)
	r, err := tool.Handler(ctx, params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)

	// Session frontmatter should carry archive: <vault-rel-manifest>.
	meta, _, err := vault.ReadSession("test-proj", got.SessionID[:10], capture.ParseFingerprint(got.SessionID), got.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	wantArchive := archive.VaultRelPath(vault.Root, res.ManifestPath)
	if meta.Archive != wantArchive {
		t.Errorf("session.archive = %q, want %q", meta.Archive, wantArchive)
	}
	if meta.ArchiveSessionID != "link-session" {
		t.Errorf("session.archive_session_id = %q, want %q", meta.ArchiveSessionID, "link-session")
	}
	if meta.ArchiveSessionIDSource != storage.ArchiveIDSourceDerived {
		t.Errorf("session.archive_session_id_source = %q, want %q",
			meta.ArchiveSessionIDSource, storage.ArchiveIDSourceDerived)
	}

	// Manifest should carry vault_rel_session_note.
	m, err := archive.ReadManifest(res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.VaultRelSessionNote == "" {
		t.Error("manifest vault_rel_session_note was not updated")
	}
	if !strings.HasSuffix(m.VaultRelSessionNote, ".md") {
		t.Errorf("vault_rel_session_note = %q, expected a .md path", m.VaultRelSessionNote)
	}
}

const sampleClaudeJSONL = `{"type":"permission-mode","permissionMode":"bypassPermissions","sessionId":"link-session"}
{"type":"user","message":{"role":"user","content":"hi"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"hello"}}
`

// A caller-supplied archive_session_id is IGNORED: the only caller of this
// tool is the agent, and agents are demonstrably fed wrong ids. A wrong id
// mis-links another session's transcript, so the note must carry the derived
// id or none — never the caller's.
func TestCaptureSessionCallerSuppliedIDIgnored(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "") // nothing derivable

	tool := CaptureSessionTool(vault, nil)
	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Caller pushes an id the server must not trust.",
		"archive_session_id": "wrong-bridge-id"
	}`)
	r, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)

	meta, _, err := vault.ReadSession("test-proj", got.SessionID[:10], capture.ParseFingerprint(got.SessionID), got.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	if meta.ArchiveSessionID != "" {
		t.Errorf("archive_session_id = %q, want empty — the caller's id must be ignored", meta.ArchiveSessionID)
	}
	if meta.ArchiveSessionIDSource != "" {
		t.Errorf("archive_session_id_source = %q, want empty", meta.ArchiveSessionIDSource)
	}
	if meta.Archive != "" {
		t.Errorf("archive = %q, want empty — nothing was derivable", meta.Archive)
	}
}

// When the id is derivable but no archive exists yet (the ordinary case — the
// hook only archives at SessionEnd), the note still records the derived id and
// its provenance, so the archiving hook run can find it and close the loop.
func TestCaptureSessionDerivedIDRecordedBeforeArchiveExists(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "live-session-uuid")

	tool := CaptureSessionTool(vault, nil)
	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Wrap note captured mid-session, before any archive exists."
	}`)
	r, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := r.(captureSessionResult)

	meta, _, err := vault.ReadSession("test-proj", got.SessionID[:10], capture.ParseFingerprint(got.SessionID), got.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	if meta.ArchiveSessionID != "live-session-uuid" {
		t.Errorf("archive_session_id = %q, want %q", meta.ArchiveSessionID, "live-session-uuid")
	}
	if meta.ArchiveSessionIDSource != storage.ArchiveIDSourceDerived {
		t.Errorf("archive_session_id_source = %q, want %q",
			meta.ArchiveSessionIDSource, storage.ArchiveIDSourceDerived)
	}
	if meta.Archive != "" {
		t.Errorf("archive = %q, want empty — no archive exists yet; the link is deferred", meta.Archive)
	}
}

// The claim sentinel is keyed on the DERIVED id, so it actually matches the
// payload.SessionID the SessionEnd hook checks. A claim keyed on a wrong
// caller-supplied id never matched anything, which meant the hook re-captured
// and duplicated the note.
func TestCaptureSessionClaimUsesDerivedID(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "live-session-uuid")

	cwd := t.TempDir()
	tool := CaptureSessionTool(vault, nil)
	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Capture that should claim the session for the hook.",
		"cwd": ` + string(mustJSON(t, cwd)) + `,
		"archive_session_id": "wrong-bridge-id"
	}`)
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("handler: %v", err)
	}

	claimDir := filepath.Join(cwd, ".vibe-palace")
	entries, err := os.ReadDir(claimDir)
	if err != nil {
		t.Fatalf("read claim dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "live-session-uuid") {
		t.Errorf("claim dir %v carries no claim for the derived id", names)
	}
	if strings.Contains(joined, "wrong-bridge-id") {
		t.Errorf("claim dir %v carries a claim for the caller's untrusted id", names)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCaptureSessionValidationMissingProject(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{"summary": "No project specified."}`)

	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing project")
	}
}

func TestCaptureSessionValidationMissingSummary(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{"project": "test-proj"}`)

	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing summary")
	}
}

func TestCaptureSessionResult(t *testing.T) {
	// Verify the result struct serializes correctly.
	r := captureSessionResult{
		Status:    "ok",
		Project:   "my-proj",
		NotePath:  "/path/to/session.md",
		Iteration: 3,
		SessionID: "2026-04-07-03",
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if decoded["status"] != "ok" {
		t.Errorf("status = %v, want ok", decoded["status"])
	}
	if decoded["session_id"] != "2026-04-07-03" {
		t.Errorf("session_id = %v, want 2026-04-07-03", decoded["session_id"])
	}
}

// writeEnrichmentProjectConfig writes a project-level config.toml enabling
// [enrichment] for the given project, pointing base_url at the test server.
func writeEnrichmentProjectConfig(t *testing.T, vault *storage.Vault, project, baseURL, keyEnv string) {
	t.Helper()
	path, err := vault.ProjectConfigFile(project)
	if err != nil {
		t.Fatalf("ProjectConfigFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[enrichment]\n" +
		"enabled = true\n" +
		"provider = \"openai\"\n" +
		"model = \"enrich-test-model\"\n" +
		"api_key_env = \"" + keyEnv + "\"\n" +
		"base_url = \"" + baseURL + "\"\n" +
		"max_tokens = 512\n" +
		"timeout_seconds = 10\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCaptureSessionEnrichDefaultPlain verifies that without enrich (default
// false) the handler writes a plain, unenriched note exactly as before.
func TestCaptureSessionEnrichDefaultPlain(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Plain agent-authored summary.",
		"transcript": "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hi\"}}"
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(captureSessionResult)
	if r.Status != "ok" {
		t.Errorf("status = %q, want ok", r.Status)
	}

	meta, _, err := vault.ReadSession("test-proj", r.SessionID[:10], capture.ParseFingerprint(r.SessionID), r.Iteration)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if meta.EnrichedBy != "" {
		t.Errorf("expected no enrichment, got enriched_by=%q", meta.EnrichedBy)
	}
	if meta.Summary != "Plain agent-authored summary." {
		t.Errorf("summary mutated unexpectedly: %q", meta.Summary)
	}
	notePath, err := vault.SessionFile("test-proj", r.SessionID[:10], capture.ParseFingerprint(r.SessionID), r.Iteration)
	if err != nil {
		t.Fatalf("SessionFile: %v", err)
	}
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if strings.Contains(string(data), "<!-- enriched -->") {
		t.Error("plain note must not carry an enrichment fence")
	}
}

// TestCaptureSessionEnrichDisabledConfig verifies that enrich:true with
// enrichment NOT enabled in config still succeeds with a plain note and
// surfaces no error to the caller.
func TestCaptureSessionEnrichDisabledConfig(t *testing.T) {
	vault := testSessionVault(t)

	// Write a project config that explicitly disables enrichment. This
	// overrides any global ~/.config/vibe-palace/config.toml that may have
	// [enrichment] enabled (as on developer machines), ensuring the test
	// verifies the "enrich requested but config disabled" path.
	path, err := vault.ProjectConfigFile("test-proj")
	if err != nil {
		t.Fatalf("ProjectConfigFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[enrichment]\nenabled = false\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Summary with enrich requested but config disabled.",
		"enrich": true,
		"transcript": "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hi\"}}"
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler should not error when enrichment is disabled: %v", err)
	}
	r := result.(captureSessionResult)
	if r.Status != "ok" {
		t.Errorf("status = %q, want ok", r.Status)
	}
	meta, _, err := vault.ReadSession("test-proj", r.SessionID[:10], capture.ParseFingerprint(r.SessionID), r.Iteration)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if meta.EnrichedBy != "" {
		t.Errorf("expected no enrichment when config disabled, got %q", meta.EnrichedBy)
	}
}

// TestCaptureSessionEnrichLive drives the full opt-in enrichment path against
// an httptest server returning a canned OpenAI-compatible completion, and
// asserts the written note carries the enriched summary, enriched_by, and the
// provenance fence.
func TestCaptureSessionEnrichLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// choices[0].message.content holds the enrichment JSON payload.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"summary\":\"LLM-refined summary.\",\"decisions\":[\"Chose option B\"],\"open_threads\":[\"Wire step 9\"],\"tag\":\"implementation\"}"}}]}`))
	}))
	defer srv.Close()

	t.Setenv("VP_TEST_ENRICH_KEY", "sk-test-live")

	vault := testSessionVault(t)
	writeEnrichmentProjectConfig(t, vault, "test-proj", srv.URL, "VP_TEST_ENRICH_KEY")
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Heuristic summary to be replaced.",
		"enrich": true,
		"transcript": "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"implement step 8\"}}\n{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":\"done\"}}"
	}`)

	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	r := result.(captureSessionResult)
	if r.Status != "ok" {
		t.Errorf("status = %q, want ok", r.Status)
	}

	meta, _, err := vault.ReadSession("test-proj", r.SessionID[:10], capture.ParseFingerprint(r.SessionID), r.Iteration)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if meta.Summary != "LLM-refined summary." {
		t.Errorf("summary = %q, want enriched summary", meta.Summary)
	}
	if meta.EnrichedBy != "enrich-test-model" {
		t.Errorf("enriched_by = %q, want %q", meta.EnrichedBy, "enrich-test-model")
	}
	if meta.Tag != "implementation" {
		t.Errorf("tag = %q, want implementation", meta.Tag)
	}
	notePath, err := vault.SessionFile("test-proj", r.SessionID[:10], capture.ParseFingerprint(r.SessionID), r.Iteration)
	if err != nil {
		t.Fatalf("SessionFile: %v", err)
	}
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "<!-- enriched -->") {
		t.Error("enriched note missing provenance fence")
	}
	if !strings.Contains(body, "enriched by enrich-test-model") {
		t.Error("enriched note missing enriched-by footer")
	}
	if !strings.Contains(body, "LLM-refined summary.") {
		t.Error("enriched note body missing refined summary")
	}
}

func TestCaptureSessionIterationAutoIncrements(t *testing.T) {
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)

	params := json.RawMessage(`{
		"project": "test-proj",
		"summary": "First session."
	}`)

	r1, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("first session: %v", err)
	}

	params2 := json.RawMessage(`{
		"project": "test-proj",
		"summary": "Second session."
	}`)

	r2, err := tool.Handler(context.Background(), params2)
	if err != nil {
		t.Fatalf("second session: %v", err)
	}

	iter1 := r1.(captureSessionResult).Iteration
	iter2 := r2.(captureSessionResult).Iteration
	if iter2 != iter1+1 {
		t.Errorf("iteration did not auto-increment: %d → %d", iter1, iter2)
	}
}
