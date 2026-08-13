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

	mcplib "github.com/mark3labs/mcp-go/mcp"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// writeFile writes data to path, creating parent directories.
func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// resourceFixture builds a temp vault populated with one of every resource
// type for the "demo" project and returns the resolver, vault, and the raw
// bodies keyed by resource type.
func resourceFixture(t *testing.T) (*vpctx.Resolver, *storage.Vault, map[string]string) {
	t.Helper()
	root := t.TempDir()
	const project = "demo"
	projDir := filepath.Join(root, "Projects", project)

	bodies := map[string]string{
		"task":      "# Fix bug\n\n**Status:** pending\n**Priority:** high\n\nbody of task\n",
		"resume":    "# Resume\n\ncurrent state, no placeholders\n",
		"workflow":  "# Workflow\n\nrules, no placeholders\n",
		"doctrine":  "# Doctrine\n\ngeneric manual, no placeholders\n",
		"command":   "# Command\n\ncommand body, no placeholders\n",
		"skill":     "# Skill\n\nskill body without frontmatter\n",
		"session":   "# Session\n\nsession body\n",
		"knowledge": "# Knowledge\n\nproject knowledge\n",
		"learning":  "# Learning\n\nvault-wide learning\n",
		"iteration": "iteration body only",
	}

	writeFile(t, filepath.Join(projDir, "tasks", "fix-bug.md"), bodies["task"])
	writeFile(t, filepath.Join(projDir, "resume.md"), bodies["resume"])
	writeFile(t, filepath.Join(projDir, "workflow.md"), bodies["workflow"])
	writeFile(t, filepath.Join(projDir, "doctrine.md"), bodies["doctrine"])
	writeFile(t, filepath.Join(projDir, "commands", "deploy.md"), bodies["command"])
	writeFile(t, filepath.Join(projDir, "skills", "analyze", "SKILL.md"), bodies["skill"])
	// Sessions are stored with YAML frontmatter; ReadSession returns only the
	// body that follows it. The provider must hand back that body verbatim.
	writeFile(t, filepath.Join(projDir, "sessions", "2026-06-20-01.md"),
		"---\ntitle: Demo session\niteration: 1\n---\n"+bodies["session"])
	writeFile(t, filepath.Join(projDir, "knowledge.md"), bodies["knowledge"])
	// Learnings are vault-wide, not per-project: they live under
	// Knowledge/learnings/<slug>.md, not beneath Projects/<p>/. They carry YAML
	// frontmatter; the learning resource resolves through GetLearning and hands
	// back only the parsed body that follows it (byte-identical to
	// vp_get_learning's content), so bodies["learning"] is that body.
	writeFile(t, filepath.Join(root, "Knowledge", "learnings", "cache-invalidation.md"),
		"---\nname: Cache Invalidation\ndescription: vault-wide learning\ntype: reference\n---\n"+bodies["learning"])
	// iterations.md frame matches AppendIterationOwned so the slicer returns the body.
	writeFile(t, filepath.Join(projDir, "iterations.md"),
		"# demo — Iteration Narratives\n\n---\n## Iteration 7 — fixture\n\n"+bodies["iteration"])

	return vpctx.NewResolver(root), storage.NewVault(root), bodies
}

// TestResolveURIHappyPath exercises every resource type and asserts the body
// is returned byte-for-byte with the markdown MIME type.
func TestResolveURIHappyPath(t *testing.T) {
	resolver, vault, bodies := resourceFixture(t)

	cases := []struct {
		name string
		uri  string
		want string
	}{
		{"task", mcp.TaskURI("demo", "fix-bug"), bodies["task"]},
		{"resume", mcp.ResumeURI("demo"), bodies["resume"]},
		{"workflow", mcp.WorkflowURI("demo"), bodies["workflow"]},
		{"doctrine", mcp.DoctrineURI("demo"), bodies["doctrine"]},
		{"command", mcp.CommandURI("demo", "deploy"), bodies["command"]},
		{"skill", mcp.SkillURI("demo", "analyze"), bodies["skill"]},
		{"session", mcp.SessionURI("demo", "2026-06-20-01"), bodies["session"]},
		{"knowledge", mcp.KnowledgeURI("demo"), bodies["knowledge"]},
		{"learning", mcp.LearningURI("cache-invalidation"), bodies["learning"]},
		{"iteration", mcp.IterationURI("demo", 7), bodies["iteration"]},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, mime, err := ResolveURI(tc.uri, resolver, vault)
			if err != nil {
				t.Fatalf("ResolveURI(%q) error: %v", tc.uri, err)
			}
			if mime != "text/markdown" {
				t.Errorf("mime = %q, want text/markdown", mime)
			}
			if text != tc.want {
				t.Errorf("body mismatch for %s:\n got %q\nwant %q", tc.name, text, tc.want)
			}
		})
	}
}

// TestResolveURINotFound verifies a content-not-found error (distinct from an
// unknown-type error) when the backing file is absent. Resume and workflow are
// intentionally excluded: the precedence Resolver always falls back to the
// embedded tier-5 defaults, so they never report not-found.
func TestResolveURINotFound(t *testing.T) {
	resolver, vault, _ := resourceFixture(t)

	uris := []string{
		mcp.TaskURI("demo", "no-such-task"),
		mcp.IterationURI("demo", 999),
		mcp.CommandURI("empty", "ghost"),
		mcp.SkillURI("empty", "ghost"),
		mcp.SessionURI("demo", "2099-01-01-09"),
		// Learning resolves through GetLearning, so an unknown slug is a hard
		// not-found error (listing the available slugs), mirroring the task
		// resource — not the benign empty-body state that knowledge uses.
		mcp.LearningURI("no-such-learning"),
	}
	for _, uri := range uris {
		if _, _, err := ResolveURI(uri, resolver, vault); err == nil {
			t.Errorf("ResolveURI(%q): expected not-found error, got nil", uri)
		}
	}

	// Knowledge is asymmetric on purpose: a project that has never recorded
	// knowledge has no file yet, which is the benign "not written yet" state, so
	// it resolves to an EMPTY body rather than a raw os not-found error.
	body, _, err := ResolveURI(mcp.KnowledgeURI("empty"), resolver, vault)
	if err != nil {
		t.Errorf("ResolveURI(knowledge/empty): want empty body, got error %v", err)
	}
	if body != "" {
		t.Errorf("ResolveURI(knowledge/empty): want empty body, got %q", body)
	}
}

// TestResolveURIUnknownType verifies an unknown scheme/type yields an error.
func TestResolveURIUnknownType(t *testing.T) {
	resolver, vault, _ := resourceFixture(t)

	for _, uri := range []string{
		"vibe-palace://bogus/demo",
		"http://task/demo/fix-bug",
		"vibe-palace://",
	} {
		if _, _, err := ResolveURI(uri, resolver, vault); err == nil {
			t.Errorf("ResolveURI(%q): expected error, got nil", uri)
		}
	}
}

// TestResolveURITraversalRejected verifies that path-traversal in any URI
// variable is rejected by slug.Validate BEFORE any filesystem access. The
// fixture contains no matching files, so the only way these could "succeed"
// is via traversal — every case must error.
func TestResolveURITraversalRejected(t *testing.T) {
	resolver, vault, _ := resourceFixture(t)

	cases := []struct {
		name string
		uri  string
	}{
		{"task project ..", mcp.ResourceScheme + "task/../fix-bug"},
		{"task slug ..", mcp.ResourceScheme + "task/demo/.."},
		{"resume project ..", mcp.ResourceScheme + "resume/.."},
		{"command name ..", mcp.ResourceScheme + "command/demo/.."},
		{"skill name ..", mcp.ResourceScheme + "skill/demo/.."},
		{"session id ..", mcp.ResourceScheme + "session/demo/.."},
		{"knowledge project ..", mcp.ResourceScheme + "knowledge/.."},
		{"learning slug ..", mcp.ResourceScheme + "learning/.."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ResolveURI(tc.uri, resolver, vault)
			if err == nil {
				t.Fatalf("ResolveURI(%q): expected traversal rejection, got nil", tc.uri)
			}
			// A traversal must be rejected at validation, not surface as a
			// raw filesystem not-found.
			if !strings.Contains(err.Error(), "invalid") {
				t.Errorf("ResolveURI(%q): expected validation error, got %v", tc.uri, err)
			}
		})
	}
}

// TestRegisterResourcesRoundTrip wires the real providers onto an mcp.Server via
// RegisterResources, then reads a task body back through the protocol layer
// (resources/read), asserting the closure-wired provider returns the body
// byte-for-byte with the markdown MIME type. This is the in-package companion to
// the streamable-HTTP transport guard in internal/mcp: it proves the provider
// closure (which closes over the captured vault/resolver) is reachable through
// the server's resource dispatch.
func TestRegisterResourcesRoundTrip(t *testing.T) {
	resolver, vault, bodies := resourceFixture(t)

	srv := mcp.NewServer(vault)
	RegisterResources(srv, resolver, vault)

	// Handshake so resource requests are accepted.
	srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": {
			"protocolVersion": "2025-03-26",
			"capabilities": {},
			"clientInfo": {"name": "test", "version": "0.1.0"}
		}
	}`))
	srv.HandleMessage(context.Background(), json.RawMessage(`{
		"jsonrpc": "2.0", "method": "notifications/initialized"
	}`))

	read := func(uri string) (string, string) {
		t.Helper()
		msg, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "resources/read",
			"params": map[string]any{"uri": uri},
		})
		resp := srv.HandleMessage(context.Background(), json.RawMessage(msg))
		rpcResp, ok := resp.(mcplib.JSONRPCResponse)
		if !ok {
			t.Fatalf("resources/read %q: expected JSONRPCResponse, got %T: %+v", uri, resp, resp)
		}
		raw, _ := json.Marshal(rpcResp.Result)
		var result struct {
			Contents []struct {
				MIMEType string `json:"mimeType"`
				Text     string `json:"text"`
			} `json:"contents"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("resources/read %q: unmarshal: %v", uri, err)
		}
		if len(result.Contents) != 1 {
			t.Fatalf("resources/read %q: %d contents, want 1", uri, len(result.Contents))
		}
		return result.Contents[0].Text, result.Contents[0].MIMEType
	}

	// Exercise every registered template so each provider closure is reached.
	cases := []struct {
		name string
		uri  string
		want string
	}{
		{"task", mcp.TaskURI("demo", "fix-bug"), bodies["task"]},
		{"resume", mcp.ResumeURI("demo"), bodies["resume"]},
		{"workflow", mcp.WorkflowURI("demo"), bodies["workflow"]},
		{"doctrine", mcp.DoctrineURI("demo"), bodies["doctrine"]},
		{"command", mcp.CommandURI("demo", "deploy"), bodies["command"]},
		{"skill", mcp.SkillURI("demo", "analyze"), bodies["skill"]},
		{"session", mcp.SessionURI("demo", "2026-06-20-01"), bodies["session"]},
		{"knowledge", mcp.KnowledgeURI("demo"), bodies["knowledge"]},
		{"learning", mcp.LearningURI("cache-invalidation"), bodies["learning"]},
		{"iteration", mcp.IterationURI("demo", 7), bodies["iteration"]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text, mime := read(tc.uri)
			if mime != "text/markdown" {
				t.Errorf("mime = %q, want text/markdown", mime)
			}
			if text != tc.want {
				t.Errorf("body mismatch:\n got %q\nwant %q", text, tc.want)
			}
		})
	}
}

// TestResolveURIByteIdentity confirms the returned task body is identical to
// the raw on-disk file (no reframing, no stamping).
func TestResolveURIByteIdentity(t *testing.T) {
	resolver, vault, _ := resourceFixture(t)

	raw, err := os.ReadFile(filepath.Join(vault.Root, "Projects", "demo", "tasks", "fix-bug.md"))
	if err != nil {
		t.Fatalf("read raw task: %v", err)
	}
	text, _, err := ResolveURI(mcp.TaskURI("demo", "fix-bug"), resolver, vault)
	if err != nil {
		t.Fatalf("ResolveURI: %v", err)
	}
	if text != string(raw) {
		t.Errorf("task body not byte-identical:\n got %q\nwant %q", text, string(raw))
	}
}
