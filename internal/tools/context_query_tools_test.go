// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestGetWorkflow(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	tool := GetWorkflowTool(resolver)
	if tool.Name != "vp_get_workflow" {
		t.Fatalf("name = %q", tool.Name)
	}

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	rr := result.(resolveResult)
	if rr.Project != "test-proj" {
		t.Errorf("project = %q", rr.Project)
	}
	if rr.Content == "" {
		t.Error("expected non-empty workflow from embedded defaults")
	}
}

func TestGetResume(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	tool := GetResumeTool(resolver)
	if tool.Name != "vp_get_resume" {
		t.Fatalf("name = %q", tool.Name)
	}

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	rr := result.(resolveResult)
	if rr.Content == "" {
		t.Error("expected non-empty resume from embedded defaults")
	}
}

func TestUpdateResumeThenGet(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	// Write a resume.
	updateTool := UpdateResumeTool(vault)
	params, _ := json.Marshal(updateResumeParams{
		Project: "test-proj",
		Content: "# My Resume\nUpdated content.",
	})
	result, err := updateTool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("update handler: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "updated" {
		t.Errorf("status = %q", m["status"])
	}

	// Read it back via the resolver.
	getTool := GetResumeTool(resolver)
	getParams, _ := json.Marshal(map[string]string{"project": "test-proj"})
	getResult, err := getTool.Handler(context.Background(), getParams)
	if err != nil {
		t.Fatalf("get handler: %v", err)
	}
	rr := getResult.(resolveResult)
	if rr.Content != "# My Resume\nUpdated content." {
		t.Errorf("content = %q", rr.Content)
	}
}

// onDiskSha reads a file and returns its lowercase-hex SHA-256, computed
// independently of the code under test.
func onDiskSha(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestGetResumeSha256MatchesDisk pins the CAS precondition: the sha vp_get_resume
// hands back must be the SHA-256 of the vault's resume.md as it sits on disk, or
// a Phase-1 compare-and-set write keyed on it can never match.
func TestGetResumeSha256MatchesDisk(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	const body = "# Resume\n\nProject state for test-proj.\n"
	if err := vault.WriteResume("test-proj", body, ""); err != nil {
		t.Fatal(err)
	}
	path, err := vault.ResumeFile("test-proj")
	if err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := GetResumeTool(resolver).Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rr := result.(resolveResult)

	if rr.Source != "project" {
		t.Fatalf("source = %q, want project", rr.Source)
	}
	want := onDiskSha(t, path)
	if rr.Sha256 != want {
		t.Errorf("sha256 = %q, want on-disk %q", rr.Sha256, want)
	}
}

// TestGetResumeSha256EmptyWithoutProjectFile pins the assert-absent signal: with
// no Projects/<slug>/resume.md the content falls through to the embedded default,
// which no CAS can be keyed on, so the sha must be empty rather than the hash of
// a template that isn't on disk.
func TestGetResumeSha256EmptyWithoutProjectFile(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := GetResumeTool(resolver).Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rr := result.(resolveResult)

	if rr.Source == "project" {
		t.Fatalf("source = project, expected a template fallback")
	}
	if rr.Sha256 != "" {
		t.Errorf("sha256 = %q, want empty for non-project source", rr.Sha256)
	}
}

// TestGetWorkflowSha256MatchesDisk pins that the digest is uniform across every
// tool sharing resolveResult, not special-cased for resume.
func TestGetWorkflowSha256MatchesDisk(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	dir, err := vault.ProjectDir("test-proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "workflow.md")
	if err := os.WriteFile(path, []byte("# Workflow\n\nRules.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := GetWorkflowTool(resolver).Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rr := result.(resolveResult)

	if want := onDiskSha(t, path); rr.Sha256 != want {
		t.Errorf("sha256 = %q, want on-disk %q", rr.Sha256, want)
	}
}

// TestGetResumeSha256IsOfRawBytes pins that the digest covers the bytes on disk,
// NOT the placeholder-expanded string the resolver returns. A resume carrying a
// live {{PROJECT}} placeholder is served expanded; hashing that expansion would
// yield a sha matching nothing on disk and would break every CAS write.
func TestGetResumeSha256IsOfRawBytes(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	const raw = "# Resume for {{PROJECT}}\n\nUnexpanded placeholder.\n"
	if err := vault.WriteResume("test-proj", raw, ""); err != nil {
		t.Fatal(err)
	}
	path, err := vault.ResumeFile("test-proj")
	if err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := GetResumeTool(resolver).Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	rr := result.(resolveResult)

	if strings.Contains(rr.Content, "{{PROJECT}}") {
		t.Fatalf("content not expanded, fixture no longer exercises the case: %q", rr.Content)
	}
	if want := onDiskSha(t, path); rr.Sha256 != want {
		t.Errorf("sha256 = %q, want on-disk %q (digest must hash raw bytes, not the expansion)", rr.Sha256, want)
	}
	expanded := sha256.Sum256([]byte(rr.Content))
	if rr.Sha256 == hex.EncodeToString(expanded[:]) {
		t.Error("sha256 is the hash of the EXPANDED content — no CAS keyed on it could ever match the file")
	}
}

func TestUpdateResumeValidation(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	tool := UpdateResumeTool(vault)

	tests := []struct {
		name   string
		params updateResumeParams
	}{
		{"empty project", updateResumeParams{Project: "", Content: "x"}},
		{"empty content", updateResumeParams{Project: "proj", Content: ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, _ := json.Marshal(tt.params)
			if _, err := tool.Handler(context.Background(), p); err == nil {
				t.Error("expected error")
			}
		})
	}
}

// TestUpdateResumeCASRoundTrip is the full read→write round trip an agent makes:
// vp_get_resume hands back a body and its sha, vp_update_resume submits a rewrite
// keyed on that sha, and the write lands.
func TestUpdateResumeCASRoundTrip(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	resolver := vpctx.NewResolver(vault.Root)

	if err := vault.WriteResume("test-proj", "# Resume\nv1\n", ""); err != nil {
		t.Fatal(err)
	}

	getParams, _ := json.Marshal(map[string]string{"project": "test-proj"})
	got, err := GetResumeTool(resolver).Handler(context.Background(), getParams)
	if err != nil {
		t.Fatalf("get handler: %v", err)
	}
	rr := got.(resolveResult)
	if rr.Sha256 == "" {
		t.Fatal("vp_get_resume returned an empty sha for an existing resume")
	}

	upParams, _ := json.Marshal(updateResumeParams{
		Project:        "test-proj",
		Content:        "# Resume\nv2\n",
		ExpectedSha256: rr.Sha256,
	})
	if _, err := UpdateResumeTool(vault).Handler(context.Background(), upParams); err != nil {
		t.Fatalf("update with the sha from vp_get_resume was refused: %v", err)
	}

	path, err := vault.ResumeFile("test-proj")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# Resume\nv2\n" {
		t.Errorf("content = %q, want the rewrite to have landed", data)
	}
}

// TestUpdateResumeStaleShaIsMachineParseableError pins the LOUD failure: a stale
// guard must come back as an ERROR (never a successful result with a flag), and
// its text must carry a JSON object with the ACTUAL current sha so the agent's
// retry is mechanical.
func TestUpdateResumeStaleShaIsMachineParseableError(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())

	if err := vault.WriteResume("test-proj", "# Resume\ncurrent\n", ""); err != nil {
		t.Fatal(err)
	}
	path, err := vault.ResumeFile("test-proj")
	if err != nil {
		t.Fatal(err)
	}
	currentSha := onDiskSha(t, path)
	staleSum := sha256.Sum256([]byte("# Resume\nlong gone\n"))
	staleSha := hex.EncodeToString(staleSum[:])

	params, _ := json.Marshal(updateResumeParams{
		Project:        "test-proj",
		Content:        "# CLOBBER\n",
		ExpectedSha256: staleSha,
	})
	_, err = UpdateResumeTool(vault).Handler(context.Background(), params)
	if err == nil {
		t.Fatal("a stale-sha vp_update_resume succeeded; the blind-overwrite path is back")
	}
	msg := err.Error()
	if !strings.Contains(msg, `"conflict":true`) {
		t.Errorf("error %q does not carry the machine-parseable conflict flag", msg)
	}
	if !strings.Contains(msg, currentSha) {
		t.Errorf("error %q does not carry the current sha %s", msg, currentSha)
	}

	// The embedded object must actually parse, and its current_sha256 must be
	// the digest a retry should key on.
	open := strings.Index(msg, "{")
	if open < 0 {
		t.Fatalf("error %q embeds no JSON object", msg)
	}
	var detail struct {
		Conflict       bool   `json:"conflict"`
		CurrentSha256  string `json:"current_sha256"`
		ExpectedSha256 string `json:"expected_sha256"`
		Remedy         string `json:"remedy"`
	}
	if uerr := json.Unmarshal([]byte(msg[open:]), &detail); uerr != nil {
		t.Fatalf("embedded object does not parse as JSON: %v (msg=%q)", uerr, msg)
	}
	if !detail.Conflict || detail.CurrentSha256 != currentSha || detail.ExpectedSha256 != staleSha || detail.Remedy == "" {
		t.Errorf("conflict detail = %+v, want conflict=true current=%s expected=%s and a remedy", detail, currentSha, staleSha)
	}

	// The refused write must not have touched the file.
	if data, rerr := os.ReadFile(path); rerr != nil || string(data) != "# Resume\ncurrent\n" {
		t.Errorf("refused write mutated the file: %q (%v)", data, rerr)
	}
}

// TestUpdateResumeSchemaRequiresExpectedSha asserts the guard cannot be OMITTED:
// the registry's schema validation rejects the call before the handler runs. An
// omitted guard must never silently become an assert-absent (or, worse, a blind
// overwrite).
func TestUpdateResumeSchemaRequiresExpectedSha(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	reg := mcp.NewServer(vault).Registry()
	reg.MustRegister(UpdateResumeTool(vault))

	// Omitted entirely: rejected by schema validation, before the handler runs.
	omitted, _ := json.Marshal(map[string]string{"project": "proj", "content": "# x\n"})
	_, err := reg.Dispatch(context.Background(), "vp_update_resume", omitted)
	if err == nil {
		t.Fatal("vp_update_resume without expected_sha256 was accepted; the guard is not required")
	}
	var verr *mcp.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("error = %v (%T), want a *mcp.ValidationError from the required-array check", err, err)
	}
	if !strings.Contains(verr.Error(), "expected_sha256") {
		t.Errorf("validation error %q does not name the missing property", verr)
	}

	// Present-but-empty: it CLEARS schema validation (the assert-absent case).
	// `required` mandates presence, not non-emptiness — exactly the intended
	// behavior, and why there is deliberately no minLength.
	present, _ := json.Marshal(map[string]string{"project": "proj", "content": "# x\n", "expected_sha256": ""})
	if _, err := reg.Dispatch(context.Background(), "vp_update_resume", present); err != nil {
		if errors.As(err, &verr) {
			t.Fatalf("an empty expected_sha256 failed schema validation: %v", err)
		}
		// Anything past validation (e.g. the mutating-write gate, which needs a
		// vault in context that only the real server injects) is out of scope here.
	}
}

func TestGetKnowledgeEmpty(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())
	tool := GetKnowledgeTool(vault)

	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	kr := result.(knowledgeResult)
	if kr.Stats.EntityCount != 0 {
		t.Errorf("entity count = %d", kr.Stats.EntityCount)
	}
	if len(kr.Triples) != 0 {
		t.Errorf("triples = %d", len(kr.Triples))
	}
}

func TestGetKnowledgePopulated(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())

	if err := vault.AddTriple("test-proj", storage.Triple{
		Subject: "Go", Predicate: "uses", Object: "modules",
	}); err != nil {
		t.Fatalf("AddTriple: %v", err)
	}

	tool := GetKnowledgeTool(vault)
	params, _ := json.Marshal(map[string]string{"project": "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	kr := result.(knowledgeResult)
	if kr.Stats.TripleCount != 1 {
		t.Errorf("triple count = %d", kr.Stats.TripleCount)
	}
	if len(kr.Triples) != 1 {
		t.Errorf("triples = %d", len(kr.Triples))
	}
}

func TestGetKnowledgeLimit(t *testing.T) {
	vault := bornCurrentTestVault(t, t.TempDir())

	for i := range 5 {
		if err := vault.AddTriple("test-proj", storage.Triple{
			Subject:   "A",
			Predicate: "rel",
			Object:    string(rune('a' + i)),
		}); err != nil {
			t.Fatalf("AddTriple %d: %v", i, err)
		}
	}

	tool := GetKnowledgeTool(vault)
	params, _ := json.Marshal(getKnowledgeParams{Project: "test-proj", Limit: 2})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	kr := result.(knowledgeResult)
	if len(kr.Triples) != 2 {
		t.Errorf("triples = %d, want 2", len(kr.Triples))
	}
	if kr.Stats.TripleCount != 5 {
		t.Errorf("stats.triple_count = %d, want 5", kr.Stats.TripleCount)
	}
}
