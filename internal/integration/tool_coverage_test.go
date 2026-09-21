// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// MCP tool-coverage matrix + completeness gate.
//
// This file is the fourth pin beside the three that already prove the
// registered tool surface is what it claims to be:
//   - internal/tools/register_test.go (wantNames + the two registered counts)
//   - cmd/vp/tool_surface_golden_test.go (TestToolSurfaceGolden)
//   - internal/tools/mutating_test.go (TestMutatingToolNamesMatchRegistry)
//
// None of those three ever DISPATCHES a tool through the real tools/call JSON-RPC
// path — they only construct a registry and inspect its ToolInfo list. This file
// is different in kind: it proves every registered tool has a fixture that
// actually calls its handler through the harness and asserts something REAL
// about the returned payload, never just "did not error".
//
// It lives here, in internal/integration, rather than in internal/tools or
// cmd/vp, because of a genuine import cycle: internal/testinfra imports
// internal/tools (to build a harness and call RegisterAll), so internal/tools
// itself can never import internal/testinfra back — not even from a _test.go
// file (confirmed with a live `go vet` run: "import cycle not allowed in
// test"). internal/integration is the established, best-fit home: it already
// imports internal/testinfra (transitively, via its own testHarness wrapper),
// it already carries 66+ other full-stack harness tests, and it is not the
// only package technically capable of hosting this (cmd/vp is not blocked by
// the cycle either) — it is simply the better fit, since cmd/vp/tool_surface_
// golden_test.go's actual job is a narrow schema-hash check, a poor
// conceptual home for a 74-tool dispatch matrix.
//
// NO HARDCODED TOOL COUNT. Every check below diffs against
// h.Server.Registry().List() at run time; nothing here assumes 74, or any
// other number, stays fixed.
//
// KNOWN LIMITATION (flagged for PR review, not mechanically enforced): the
// completeness gate below proves every registered tool has a fixture ENTRY —
// it cannot prove that entry's assert function is not hollow. A fixture
// authored as `assert: func(t *testing.T, h *testHarness, payload string) {}`
// would satisfy both this gate and the execution test while adding zero real
// coverage. Reviewers must read assert bodies, not just check this gate is
// green.
package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/memory"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// toolFixture is one registered tool's harness-driven coverage record.
//
// build establishes any preconditions the tool needs — seeding a project,
// writing a vault file, invoking an UPSTREAM tool for a DAG-dependent case
// (vp_vault_edit after vp_vault_write, vp_get_task after a vp_manage_task
// create, vp_get_session_detail/vp_get_effectiveness after vp_capture_session)
// — and returns the args to dispatch the tool under test with.
//
// assert runs against the tool's own returned payload (and, where it
// meaningfully strengthens the check, may drive a follow-up read-only tool
// call through h to confirm a side effect actually landed). It must check
// something real about the response, never merely that the call succeeded.
type toolFixture struct {
	build  func(t *testing.T, h *testHarness) any
	assert func(t *testing.T, h *testHarness, payload string)
}

// covUnmarshal decodes a tool's JSON text payload, failing the test with the
// raw payload on a parse error so a schema drift is diagnosable from the
// failure message alone.
func covUnmarshal(t *testing.T, payload string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(payload), v); err != nil {
		t.Fatalf("unmarshal payload: %v (raw: %.500s)", err, payload)
	}
}

// covGitVault git-inits the harness's OWN vault root in place, with a
// resolvable local identity, for the handful of tools (vp_vault_status,
// vp_vault_sync, vp_vault_tidy, vp_memory_harvest) whose handlers shell out to
// git against the bound vault. initGitRepo (hook_test.go) already does exactly
// this against an arbitrary directory with three commits, which is more than
// these fixtures need but harmless: the extra tracked file.txt revisions are
// inert vault dirt no assertion here depends on.
func covGitVault(t *testing.T, h *testHarness) {
	t.Helper()
	initGitRepo(t, h.Vault.Root)
}

// covWriteManifest drops a stranded (unlinked) transcript archive manifest
// under Projects/<project>/transcripts/, mirroring
// internal/vaultaudit/backfill_test.go's seedManifestAt (a different package,
// so not importable — replicated here against the exported archive.Manifest
// shape instead of a second hand-rolled struct).
func covWriteManifest(t *testing.T, vault *storage.Vault, project, sessionID, stem, capturedAt string) string {
	t.Helper()
	dir := filepath.Join(vault.Root, "Projects", project, "transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	m := archive.Manifest{
		SchemaVersion: 1,
		Adapter:       "claude-code",
		SessionID:     sessionID,
		ProjectSlug:   project,
		CapturedAt:    capturedAt,
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
	return "Projects/" + project + "/transcripts/" + stem + ".manifest.json"
}

// covWriteKeyedNote writes a session note carrying a caller-pushed
// session_key, the shape vp_archive_link's backfill matches a stranded
// manifest against. Mirrors internal/vaultaudit/backfill_test.go's
// seedKeyedNote.
func covWriteKeyedNote(t *testing.T, vault *storage.Vault, project, name, key string) string {
	t.Helper()
	dir := filepath.Join(vault.Root, "Projects", project, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("---\nsession_key: %s\nsession_key_source: caller\ntitle: t\n---\nbody\n", key)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return "Projects/" + project + "/sessions/" + name
}

// covWriteDecisionNote writes a well-formed session note carrying a YAML
// `decisions:` list, the shape vp_palace_backfill_decisions mines. Mirrors
// internal/tools/palace_backfill_tools_test.go's pbNote (package tools,
// not importable here).
func covWriteDecisionNote(t *testing.T, vault *storage.Vault, project, sessionID, date, decision string) {
	t.Helper()
	dir, err := vault.SessionDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "session_id: %s\n", sessionID)
	fmt.Fprintf(&b, "project: %s\n", project)
	fmt.Fprintf(&b, "date: %s\n", date)
	b.WriteString("iteration: 1\n")
	b.WriteString("decisions:\n")
	fmt.Fprintf(&b, "  - %q\n", decision)
	b.WriteString("---\n\n## Notes\n\nprose the backfill must never mine.\n")
	if err := os.WriteFile(filepath.Join(dir, sessionID+".md"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// covSeedTaskFile drops one file under Projects/<project>/tasks/ so a
// wrap-state task-delta computation has something to count.
func covSeedTaskFile(t *testing.T, vault *storage.Vault, project, slug string) {
	t.Helper()
	tasksDir, err := vault.TasksDir(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, slug+".md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// toolCoverageFixtures is the ONE fixture table this gate checks for
// completeness against h.Server.Registry().List(). Adding a tool to the
// registry without adding it here fails TestToolCoverageComplete; removing a
// fixture whose tool is still registered does the same — "deleting any one
// fixture turns the gate red" (this task's own Verification line).
var toolCoverageFixtures = map[string]toolFixture{
	// -----------------------------------------------------------------
	// Bootstrap / discovery / doctrine / commands / skills
	// -----------------------------------------------------------------

	"vp_bootstrap_context": {
		build: func(t *testing.T, h *testHarness) any {
			h.seedProject(t, "cov-bootstrap")
			return map[string]any{"project": "cov-bootstrap"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Project     string `json:"project"`
				WorkflowURI string `json:"workflow_uri"`
			}
			covUnmarshal(t, payload, &out)
			if out.Project != "cov-bootstrap" {
				t.Errorf("project = %q, want cov-bootstrap", out.Project)
			}
			if out.WorkflowURI == "" {
				t.Error("workflow_uri is empty — it is the only route to the project's rules")
			}
		},
	},

	"vp_get_command": {
		build: func(t *testing.T, h *testHarness) any {
			// content_uri is only minted for an unscoped PROJECT-level lookup
			// (getResourceHandler / resourceContentURI) — a bare name-only call
			// legitimately has no URI to mint, so a project is required here.
			return map[string]any{"name": "restart", "project": "cov-getcommand"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				ContentURI string `json:"content_uri"`
				Content    string `json:"content"`
				Complete   bool   `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if out.Content == "" {
				t.Error("content is empty for command 'restart'")
			}
			if out.ContentURI == "" {
				t.Error("content_uri is empty")
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_get_doctrine": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"project": "cov-doctrine"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				DoctrineURI string `json:"doctrine_uri"`
				Content     string `json:"content"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Content) < 100 {
				t.Errorf("doctrine content suspiciously short: %d bytes", len(out.Content))
			}
			if out.DoctrineURI == "" {
				t.Error("doctrine_uri is empty")
			}
		},
	},

	"vp_get_resume": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"project": "cov-resume"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				ResumeURI string `json:"resume_uri"`
				Content   string `json:"content"`
				Complete  bool   `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if out.ResumeURI == "" {
				t.Error("resume_uri is empty")
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_get_skill": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"name": "startup-analyst"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Content  string `json:"content"`
				Complete bool   `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if !strings.Contains(out.Content, "startup-analyst") {
				t.Errorf("skill content looks empty/wrong: %.100s", out.Content)
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_get_skill_section": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"name": "startup-analyst", "section": "capex-opex"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Content string `json:"content"`
				Source  string `json:"source"`
			}
			covUnmarshal(t, payload, &out)
			if out.Source != "embedded" {
				t.Errorf("source = %q, want embedded", out.Source)
			}
			if len(out.Content) == 0 {
				t.Error("section content is empty")
			}
		},
	},

	"vp_get_workflow": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"project": "cov-workflow"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Project string `json:"project"`
				Content string `json:"content"`
			}
			covUnmarshal(t, payload, &out)
			if out.Project != "cov-workflow" {
				t.Errorf("project = %q, want cov-workflow", out.Project)
			}
			if len(out.Content) == 0 {
				t.Error("workflow content is empty")
			}
		},
	},

	"vp_get_knowledge": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_kg_add", map[string]any{
				"project": "cov-knowledge", "subject": "vp", "predicate": "uses", "object": "sqlite",
			})
			return map[string]any{"project": "cov-knowledge"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Stats    storage.KGStats  `json:"stats"`
				Triples  []storage.Triple `json:"triples"`
				Complete bool             `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Triples) == 0 {
				t.Fatal("expected at least one triple")
			}
			if out.Triples[0].Subject != "vp" || out.Triples[0].Object != "sqlite" {
				t.Errorf("triple = %+v, want vp/uses/sqlite", out.Triples[0])
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_get_learning": {
		build: func(t *testing.T, h *testHarness) any {
			dir := h.Vault.LearningsDir()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			body := "---\nname: Cov Lesson\ndescription: a coverage-fixture learning\ntype: feedback\n---\nBody text.\n"
			if err := os.WriteFile(filepath.Join(dir, "cov-lesson.md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			return map[string]any{"slug": "cov-lesson"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Slug    string `json:"slug"`
				Name    string `json:"name"`
				Content string `json:"content"`
			}
			covUnmarshal(t, payload, &out)
			if out.Slug != "cov-lesson" || out.Name != "Cov Lesson" {
				t.Errorf("learning = %+v, want slug=cov-lesson name='Cov Lesson'", out)
			}
			if !strings.Contains(out.Content, "Body text.") {
				t.Errorf("content = %q, missing body", out.Content)
			}
		},
	},

	"vp_list_learnings": {
		build: func(t *testing.T, h *testHarness) any {
			dir := h.Vault.LearningsDir()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			body := "---\nname: Cov List Lesson\ndescription: d\ntype: feedback\n---\nbody\n"
			if err := os.WriteFile(filepath.Join(dir, "cov-list-lesson.md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			return map[string]any{}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Learnings []storage.LearningMetadata `json:"learnings"`
			}
			covUnmarshal(t, payload, &out)
			var found bool
			for _, l := range out.Learnings {
				if l.Slug == "cov-list-lesson" {
					found = true
				}
			}
			if !found {
				t.Errorf("cov-list-lesson missing from %+v", out.Learnings)
			}
		},
	},

	"vp_list_projects": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithDrawer("cov-listproj", "memory", "notes", "content", "long-term", "2026-01-01T10:00:00Z"))
			return map[string]any{}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			if !strings.Contains(payload, "cov-listproj") {
				t.Errorf("cov-listproj missing from list_projects: %s", payload)
			}
		},
	},

	"vp_list_commands": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Resources []struct {
					Name string `json:"name"`
				} `json:"resources"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Resources) == 0 {
				t.Fatal("expected at least one embedded command")
			}
		},
	},

	"vp_list_skills": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Resources []struct {
					Name string `json:"name"`
				} `json:"resources"`
			}
			covUnmarshal(t, payload, &out)
			var found bool
			for _, r := range out.Resources {
				if r.Name == "startup-analyst" {
					found = true
				}
			}
			if !found {
				t.Errorf("startup-analyst missing from %+v", out.Resources)
			}
		},
	},

	"vp_cmd": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"name": "restart"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			if !strings.Contains(payload, "=== EXECUTE COMMAND: restart ===") {
				t.Errorf("vp_cmd frame header missing: %.200s", payload)
			}
		},
	},

	"vp_skill": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"name": "startup-analyst"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			if !strings.Contains(payload, "=== ACTIVATE SKILL: startup-analyst ===") {
				t.Errorf("vp_skill frame header missing: %.200s", payload)
			}
			if !strings.Contains(payload, "References (fetch on demand via vp_get_skill_section):") {
				t.Error("vp_skill frame missing references block")
			}
		},
	},

	"vp_manual": {
		build: func(t *testing.T, h *testHarness) any {
			h.seedProject(t, "cov-manual")
			return map[string]any{"project": "cov-manual"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Tools              []mcp.ToolInfo `json:"tools"`
				ServerInstructions string         `json:"server_instructions"`
				Complete           bool           `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Tools) == 0 {
				t.Fatal("manual reported zero tools")
			}
			if out.ServerInstructions == "" {
				t.Error("server_instructions is empty")
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_read_resource": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"uri": mcp.DoctrineURI("cov-readresource")}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				TotalSize int    `json:"total_size"`
				EOF       bool   `json:"eof"`
				Complete  bool   `json:"complete"`
				Content   string `json:"content"`
			}
			covUnmarshal(t, payload, &out)
			if out.TotalSize == 0 || out.Content == "" {
				t.Error("doctrine resource read back empty")
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_health": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status     string `json:"status"`
				WarnCounts any    `json:"warn_counts"`
			}
			covUnmarshal(t, payload, &out)
			switch out.Status {
			case "healthy", "warnings", "errors", "unknown":
			default:
				t.Errorf("status = %q, not one of the documented values", out.Status)
			}
		},
	},

	// -----------------------------------------------------------------
	// Palace (wings/rooms/tunnels/traverse/status/query/backfill)
	// -----------------------------------------------------------------

	"vp_palace_status": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithDrawer("cov-palacestatus", "w1", "r1", "content", "facts", "2026-01-01T10:00:00Z"))
			return map[string]any{"project": "cov-palacestatus"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Stats palace.PalaceStats `json:"stats"`
			}
			covUnmarshal(t, payload, &out)
			if out.Stats.Drawers < 1 {
				t.Errorf("stats.drawers = %d, want >= 1", out.Stats.Drawers)
			}
		},
	},

	"vp_list_wings": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithDrawer("cov-listwings", "wing-a", "r", "content a", "facts", "2026-01-01T10:00:00Z"))
			h.Seed(t, testinfra.WithDrawer("cov-listwings", "wing-b", "r", "content b", "facts", "2026-01-01T10:00:00Z"))
			return map[string]any{"project": "cov-listwings"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Items []struct {
					Wing string `json:"wing"`
				} `json:"items"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Items) != 2 {
				t.Fatalf("got %d wings, want 2: %+v", len(out.Items), out.Items)
			}
			names := map[string]bool{out.Items[0].Wing: true, out.Items[1].Wing: true}
			if !names["wing-a"] || !names["wing-b"] {
				t.Errorf("wings = %+v, want wingA and wingB", out.Items)
			}
		},
	},

	"vp_list_rooms": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithDrawer("cov-listrooms", "w", "room-a", "content a", "facts", "2026-01-01T10:00:00Z"))
			h.Seed(t, testinfra.WithDrawer("cov-listrooms", "w", "room-b", "content b", "facts", "2026-01-01T10:00:00Z"))
			return map[string]any{"project": "cov-listrooms", "wing": "w"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Items []struct {
					Room    string `json:"room"`
					Drawers int    `json:"drawers"`
				} `json:"items"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Items) != 2 {
				t.Fatalf("got %d rooms, want 2: %+v", len(out.Items), out.Items)
			}
			for _, r := range out.Items {
				if r.Drawers != 1 {
					t.Errorf("room %s has %d drawers, want 1", r.Room, r.Drawers)
				}
			}
		},
	},

	"vp_find_tunnels": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithDrawer("cov-tunnels", "wing-a", "shared", "content a", "facts", "2026-01-01T10:00:00Z"))
			h.Seed(t, testinfra.WithDrawer("cov-tunnels", "wing-b", "shared", "content b", "facts", "2026-01-01T10:00:00Z"))
			return map[string]any{"project": "cov-tunnels"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Items []palace.Tunnel `json:"items"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Items) == 0 {
				t.Fatal("expected at least one tunnel spanning wingA/wingB")
			}
			if out.Items[0].Room != "shared" {
				t.Errorf("tunnel room = %q, want shared", out.Items[0].Room)
			}
			wings := map[string]bool{}
			for _, w := range out.Items[0].Wings {
				wings[w] = true
			}
			if !wings["wing-a"] || !wings["wing-b"] {
				t.Errorf("tunnel wings = %v, want wingA and wingB", out.Items[0].Wings)
			}
		},
	},

	"vp_traverse": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithDrawer("cov-traverse", "wing-a", "shared", "content a", "facts", "2026-01-01T10:00:00Z"))
			h.Seed(t, testinfra.WithDrawer("cov-traverse", "wing-b", "shared", "content b", "facts", "2026-01-01T10:00:00Z"))
			return map[string]any{"project": "cov-traverse", "start": "wing-a/shared", "max_hops": 3}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Items []struct {
					Wing    string `json:"wing"`
					Room    string `json:"room"`
					HopDist int    `json:"hop_distance"`
				} `json:"items"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Items) == 0 {
				t.Fatal("traverse returned no reachable nodes")
			}
			var crossedTunnel bool
			for _, r := range out.Items {
				if r.Wing == "wing-b" && r.Room == "shared" {
					crossedTunnel = true
				}
			}
			if !crossedTunnel {
				t.Errorf("traverse never reached wingB/shared across the tunnel: %+v", out.Items)
			}
		},
	},

	"vp_palace_query": {
		build: func(t *testing.T, h *testHarness) any {
			covWriteDecisionNote(t, h.Vault, "cov-palacequery", "2026-03-15-01", "2026-03-15",
				"chose the flat sessions layout")
			h.callTool(t, "vp_palace_backfill_decisions", map[string]any{
				"project": "cov-palacequery", "apply": true,
			})
			return map[string]any{"project": "cov-palacequery"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Drawers []struct {
					Content string `json:"content"`
				} `json:"drawers"`
				Complete bool `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Drawers) == 0 {
				t.Fatal("expected the backfilled decision drawer to be reachable via the default query")
			}
			if !strings.Contains(out.Drawers[0].Content, "chose the flat sessions layout") {
				t.Errorf("drawer content = %q", out.Drawers[0].Content)
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_palace_backfill_decisions": {
		build: func(t *testing.T, h *testHarness) any {
			h.seedProject(t, "cov-backfill")
			covWriteDecisionNote(t, h.Vault, "cov-backfill", "2026-03-15-01", "2026-03-15",
				"chose sqlite for the embedded store")
			return map[string]any{"project": "cov-backfill", "apply": true}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Apply          bool `json:"apply"`
				NotesScanned   int  `json:"notes_scanned"`
				DecisionsFound int  `json:"decisions_found"`
				Appended       int  `json:"appended"`
				Complete       bool `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if !out.Apply {
				t.Error("apply echoed false for an apply=true call")
			}
			if out.NotesScanned != 1 || out.DecisionsFound != 1 || out.Appended != 1 {
				t.Errorf("counts = %+v, want scanned=1 found=1 appended=1", out)
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_audit_vault": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"write": false}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Failed     bool  `json:"failed"`
				Dimensions []any `json:"dimensions"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Dimensions) == 0 {
				t.Fatal("audit reported zero dimensions")
			}
		},
	},

	"vp_archive_link": {
		build: func(t *testing.T, h *testHarness) any {
			const project, sessionID = "cov-archivelink", "sess-cov-a"
			covWriteKeyedNote(t, h.Vault, project, "2026-07-16-01.md", sessionID)
			covWriteManifest(t, h.Vault, project, sessionID, "2026-07-14-"+sessionID, "2026-07-14T01:00:00Z")
			return map[string]any{"project": project, "session_id": sessionID}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Project        string   `json:"project"`
				SessionID      string   `json:"session_id"`
				TargetManifest string   `json:"target_manifest"`
				NotesUpdated   []string `json:"notes_updated"`
				NothingToDo    string   `json:"nothing_to_do"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.NotesUpdated) != 1 {
				t.Fatalf("notes_updated = %v, want exactly 1", out.NotesUpdated)
			}
			if out.TargetManifest == "" {
				t.Error("target_manifest is empty")
			}
			if out.NothingToDo != "" {
				t.Errorf("nothing_to_do = %q, want empty (work should have happened)", out.NothingToDo)
			}
		},
	},

	// -----------------------------------------------------------------
	// Knowledge graph
	// -----------------------------------------------------------------

	"vp_kg_add": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{
				"project": "cov-kgadd", "subject": "vp", "predicate": "uses", "object": "go",
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status string `json:"status"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "added" {
				t.Errorf("status = %q, want added", out.Status)
			}
		},
	},

	"vp_kg_query": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_kg_add", map[string]any{
				"project": "cov-kgquery", "subject": "vp", "predicate": "uses", "object": "go",
			})
			return map[string]any{"project": "cov-kgquery", "entity": "vp"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Triples  []storage.Triple `json:"triples"`
				Complete bool             `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Triples) == 0 {
				t.Fatal("expected at least one triple for entity vp")
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_kg_invalidate": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_kg_add", map[string]any{
				"project": "cov-kginvalidate", "subject": "vp", "predicate": "uses", "object": "go",
			})
			return map[string]any{
				"project": "cov-kginvalidate", "subject": "vp", "predicate": "uses", "object": "go",
				"ended": "2026-06-01",
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status string `json:"status"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "invalidated" {
				t.Errorf("status = %q, want invalidated", out.Status)
			}
			// Verify the side effect actually landed, not just the status word.
			q := h.callTool(t, "vp_kg_query", map[string]any{"project": "cov-kginvalidate", "entity": "vp"})
			var qr struct {
				Triples []storage.Triple `json:"triples"`
			}
			covUnmarshal(t, q, &qr)
			var found bool
			for _, tr := range qr.Triples {
				if tr.ValidTo == "2026-06-01" {
					found = true
				}
			}
			if !found {
				t.Errorf("no triple carries valid_to=2026-06-01 after invalidate: %+v", qr.Triples)
			}
		},
	},

	"vp_kg_timeline": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_kg_add", map[string]any{
				"project": "cov-kgtimeline", "subject": "vp", "predicate": "uses", "object": "go",
			})
			return map[string]any{"project": "cov-kgtimeline", "entity": "vp"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Triples  []storage.Triple `json:"triples"`
				Complete bool             `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Triples) == 0 {
				t.Fatal("expected at least one triple in the timeline for vp")
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_kg_stats": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_kg_add", map[string]any{
				"project": "cov-kgstats", "subject": "vp", "predicate": "uses", "object": "go",
			})
			return map[string]any{"project": "cov-kgstats"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out storage.KGStats
			covUnmarshal(t, payload, &out)
			if out.TripleCount < 1 {
				t.Errorf("triple_count = %d, want >= 1", out.TripleCount)
			}
		},
	},

	// -----------------------------------------------------------------
	// Search
	// -----------------------------------------------------------------

	"vp_search": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithDrawer("cov-search", "w", "r", "alpha bravo charlie unique-marker", "facts", "2026-01-01T10:00:00Z"))
			return map[string]any{"project": "cov-search", "query": "alpha bravo charlie unique-marker"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Items []struct {
					Wing string `json:"wing"`
					Room string `json:"room"`
				} `json:"items"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Items) == 0 {
				t.Fatal("expected at least one search result")
			}
			if out.Items[0].Wing != "w" || out.Items[0].Room != "r" {
				t.Errorf("top result = %s/%s, want w/r", out.Items[0].Wing, out.Items[0].Room)
			}
		},
	},

	"vp_search_cross_project": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithDrawer("cov-crosssearch", "w", "r", "gearbox bearing drivetrain unique-marker", "facts", "2026-01-01T10:00:00Z"))
			return map[string]any{"query": "gearbox bearing drivetrain unique-marker"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Items []struct {
					Project string `json:"project"`
				} `json:"items"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Items) == 0 {
				t.Fatal("expected at least one cross-project search result")
			}
			if out.Items[0].Project != "cov-crosssearch" {
				t.Errorf("result project = %q, want cov-crosssearch", out.Items[0].Project)
			}
		},
	},

	"vp_search_sessions": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithCapturedSession(map[string]any{
				"project": "cov-searchsessions", "summary": "a distinctive coverage summary",
				"tag": "planning", "enrich": false,
			}, nil))
			return map[string]any{"project": "cov-searchsessions", "query": "distinctive coverage summary"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Items []struct {
					SessionID string `json:"session_id"`
				} `json:"items"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Items) == 0 {
				t.Fatal("expected at least one matching session")
			}
			if out.Items[0].SessionID == "" {
				t.Error("session_id is empty")
			}
		},
	},

	"vp_get_friction_trends": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithCapturedSession(map[string]any{
				"project": "cov-friction", "summary": "session for friction trend coverage", "enrich": false,
			}, nil))
			return map[string]any{"project": "cov-friction"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Items []capture.WeeklyMetric `json:"items"`
			}
			covUnmarshal(t, payload, &out)
			var total int
			for _, w := range out.Items {
				total += w.SessionCount
			}
			if total < 1 {
				t.Errorf("friction trends report 0 sessions across %d weeks, want >= 1", len(out.Items))
			}
		},
	},

	// -----------------------------------------------------------------
	// Sessions / capture / project context / effectiveness
	// -----------------------------------------------------------------

	"vp_capture_session": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{
				"project": "cov-capture", "summary": "a coverage capture session",
				"transcript": "## Human\nDo the thing.\n## Assistant\nDone.\n",
				"enrich":     false,
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status    string `json:"status"`
				SessionID string `json:"session_id"`
				Iteration int    `json:"iteration"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "ok" {
				t.Errorf("status = %q, want ok", out.Status)
			}
			if out.SessionID == "" {
				t.Error("session_id is empty")
			}
			if out.Iteration < 1 {
				t.Errorf("iteration = %d, want >= 1", out.Iteration)
			}
		},
	},

	"vp_get_session_detail": {
		build: func(t *testing.T, h *testHarness) any {
			var raw string
			h.Seed(t, testinfra.WithCapturedSession(map[string]any{
				"project": "cov-sessiondetail", "summary": "detail coverage session",
				"transcript": "## Human\nHello.\n## Assistant\nHi there — a distinctive marker.\n",
				"enrich":     false,
			}, &raw))
			var cap struct {
				SessionID string `json:"session_id"`
			}
			covUnmarshal(t, raw, &cap)
			return map[string]any{"project": "cov-sessiondetail", "session_id": cap.SessionID}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				SessionID  string `json:"session_id"`
				SessionURI string `json:"session_uri"`
				Body       string `json:"body"`
				Complete   bool   `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if out.SessionID == "" {
				t.Error("session_id is empty")
			}
			if out.SessionURI == "" {
				t.Error("session_uri is empty")
			}
			if !strings.Contains(out.Body, "detail coverage session") {
				t.Errorf("body missing the captured summary: %.200s", out.Body)
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_get_effectiveness": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithCapturedSession(map[string]any{
				"project": "cov-effectiveness", "summary": "effectiveness coverage session", "enrich": false,
			}, nil))
			return map[string]any{"project": "cov-effectiveness"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Overall capture.OverallEffectiveness `json:"overall"`
			}
			covUnmarshal(t, payload, &out)
			if out.Overall.TotalSessions < 1 {
				t.Errorf("overall.total_sessions = %d, want >= 1", out.Overall.TotalSessions)
			}
		},
	},

	"vp_get_project_context": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithCapturedSession(map[string]any{
				"project": "cov-projectcontext", "summary": "project context coverage session", "enrich": false,
			}, nil))
			return map[string]any{"project": "cov-projectcontext", "sections": []string{"sessions"}}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Sessions []struct {
					SessionID string `json:"session_id"`
				} `json:"sessions"`
				Complete bool `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Sessions) == 0 {
				t.Fatal("expected at least one session in project context")
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	// -----------------------------------------------------------------
	// Iterations / wrap state / commit-message archiving
	// -----------------------------------------------------------------

	"vp_append_iteration": {
		build: func(t *testing.T, h *testHarness) any {
			h.seedProject(t, "cov-appenditer")
			return map[string]any{
				"project": "cov-appenditer", "title": "coverage", "narrative": "narrative body",
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status string `json:"status"`
				IterN  int    `json:"iter_n"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "appended" {
				t.Errorf("status = %q, want appended", out.Status)
			}
			if out.IterN < 1 {
				t.Errorf("iter_n = %d, want >= 1", out.IterN)
			}
		},
	},

	"vp_get_iteration": {
		build: func(t *testing.T, h *testHarness) any {
			h.seedProject(t, "cov-getiter")
			h.callTool(t, "vp_append_iteration", map[string]any{
				"project": "cov-getiter", "title": "coverage iteration", "narrative": "narrative body",
			})
			return map[string]any{"project": "cov-getiter", "n": 1}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Entries []struct {
					N     int    `json:"n"`
					Title string `json:"title"`
				} `json:"entries"`
				Complete bool `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Entries) == 0 {
				t.Fatal("expected iteration 1 to be found")
			}
			if out.Entries[0].Title != "coverage iteration" {
				t.Errorf("title = %q, want 'coverage iteration'", out.Entries[0].Title)
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_collect_wrap_state": {
		build: func(t *testing.T, h *testHarness) any {
			const project = "cov-collectwrap"
			h.seedProject(t, project)
			projDir := t.TempDir()
			initGitRepo(t, projDir)
			iterPath, err := h.Vault.IterationsFile(project)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(iterPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(iterPath, []byte("## Iteration 3 — prior\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return map[string]any{"project": project, "project_path": projDir}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				IterN int `json:"iter_n"`
			}
			covUnmarshal(t, payload, &out)
			if out.IterN != 4 {
				t.Errorf("iter_n = %d, want 4 (next after seeded Iteration 3)", out.IterN)
			}
		},
	},

	"vp_stamp_iter": {
		build: func(t *testing.T, h *testHarness) any {
			const project = "cov-stampiter"
			covSeedTaskFile(t, h.Vault, project, "t1")
			projDir := t.TempDir()
			return map[string]any{"project": project, "project_path": projDir, "iter": 12}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Iter         int `json:"iter"`
				BytesWritten int `json:"bytes_written"`
			}
			covUnmarshal(t, payload, &out)
			if out.Iter != 12 {
				t.Errorf("iter = %d, want 12", out.Iter)
			}
			if out.BytesWritten == 0 {
				t.Error("bytes_written is 0")
			}
		},
	},

	"vp_enqueue_iteration_summary": {
		build: func(t *testing.T, h *testHarness) any {
			const project = "cov-enqueueitersummary"
			projDir := t.TempDir()
			return map[string]any{"project": project, "project_path": projDir, "iter": 3}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status    string `json:"status"`
				QueuePath string `json:"queue_path"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "enqueued" {
				t.Errorf("status = %q, want enqueued", out.Status)
			}
			if out.QueuePath == "" {
				t.Fatal("queue_path is empty")
			}
			// Verify the side effect actually landed: exactly one queued job
			// file under the reported queue directory.
			matches, err := filepath.Glob(filepath.Join(out.QueuePath, "*.json"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 1 {
				t.Fatalf("got %d queued files under %s, want 1", len(matches), out.QueuePath)
			}
		},
	},

	"vp_trigger_summarization_drain": func() toolFixture {
		// Shared between build and assert so assert can confirm the recorded
		// launch args against the EXACT project_path this fixture used,
		// rather than re-deriving it from the recorded call itself.
		var projDir string
		return toolFixture{
			build: func(t *testing.T, h *testHarness) any {
				const project = "cov-triggerdrain"
				projDir = t.TempDir()
				// Pre-populate a fake queue file so the drain has something
				// to see and the launch actually fires.
				queueDir := filepath.Join(projDir, ".vibe-palace", "summarization-queue")
				if err := os.MkdirAll(queueDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(queueDir, "00000000000000000001-aaaaaaaa.json"), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
				return map[string]any{"project": project, "project_path": projDir}
			},
			assert: func(t *testing.T, h *testHarness, payload string) {
				var out struct {
					Status string `json:"status"`
					Pid    int    `json:"pid"`
				}
				covUnmarshal(t, payload, &out)
				if out.Status != "launched" {
					t.Errorf("status = %q, want launched", out.Status)
				}
				if out.Pid == 0 {
					t.Error("pid is 0")
				}
				// The proof this whole seam works end-to-end: the harness's
				// recording-fake Launch (installed by default, see
				// testinfra.NewHarnessWithEmbedder) actually recorded the
				// call that reached it through the REAL dispatch path.
				launches := h.RecordedLaunches()
				if len(launches) != 1 {
					t.Fatalf("got %d recorded launches, want 1: %+v", len(launches), launches)
				}
				wantArgs := []string{"drain", "summaries", "--project-path", projDir}
				if !reflect.DeepEqual(launches[0].Args, wantArgs) {
					t.Errorf("recorded args = %v, want %v", launches[0].Args, wantArgs)
				}
			},
		}
	}(),

	"vp_check_summarization_queue": func() toolFixture {
		// Shared between build and assert so assert can confirm the
		// read-only contract (the planted queue file must still be there,
		// untouched) against the EXACT queue path this fixture used.
		var queueFile string
		return toolFixture{
			build: func(t *testing.T, h *testHarness) any {
				const project = "cov-checksummqueue"
				projDir := t.TempDir()
				// [summarization] pinned disabled: the fixture proves the
				// read-only diagnostic reports an EXPECTED-backlog verdict
				// (never Fail) for a pending, unconfigured queue,
				// distinguishing it from an actually-stuck one. The pin
				// lives in the host global config — the only tier that
				// carries [summarization] — under a per-test isolated
				// XDG_CONFIG_HOME (testinfra.IsolateEnv; package
				// integration may not t.Setenv it directly).
				env := testinfra.IsolateEnv(t)
				testutil.RequireResolvedUnder(t, env.XDGConfigHome, storage.VaultConfigFilePath)
				cfgPath, err := storage.VaultConfigFilePath()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cfgPath, []byte("[summarization]\nenabled = false\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				queueDir := filepath.Join(projDir, ".vibe-palace", "summarization-queue")
				if err := os.MkdirAll(queueDir, 0o755); err != nil {
					t.Fatal(err)
				}
				queueFile = filepath.Join(queueDir, "iteration-00001.json")
				if err := os.WriteFile(queueFile, []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
				return map[string]any{"project": project, "project_path": projDir}
			},
			assert: func(t *testing.T, h *testHarness, payload string) {
				var out struct {
					Status  string   `json:"status"`
					Summary string   `json:"summary"`
					Details []string `json:"details"`
				}
				covUnmarshal(t, payload, &out)
				if out.Status != "info" {
					t.Fatalf("status = %q, want info; summary=%q", out.Status, out.Summary)
				}
				if !strings.Contains(out.Summary, "not configured") {
					t.Errorf("summary = %q, want it to mention the expected-backlog (not configured) wording", out.Summary)
				}
				// Read-only contract: the planted queue file must still be
				// there, byte-identical — this tool never claims, requeues,
				// or drains anything.
				data, err := os.ReadFile(queueFile)
				if err != nil {
					t.Fatalf("queue file %s vanished or is unreadable after a read-only diagnostic call: %v", queueFile, err)
				}
				if string(data) != "{}" {
					t.Errorf("queue file %s content changed to %q, want unchanged \"{}\"", queueFile, data)
				}
			},
		}
	}(),

	"vp_preflight_wrap": {
		build: func(t *testing.T, h *testHarness) any {
			const project = "cov-preflight"
			h.seedProject(t, project)
			projDir := t.TempDir()
			initGitRepo(t, projDir)
			return map[string]any{"project": project, "project_path": projDir}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				OK     bool  `json:"ok"`
				Errors []any `json:"errors"`
			}
			covUnmarshal(t, payload, &out)
			if !out.OK {
				t.Errorf("preflight on a clean git repo should be ok: %s", payload)
			}
			if len(out.Errors) != 0 {
				t.Errorf("expected no errors, got %+v", out.Errors)
			}
		},
	},

	"vp_ingest_commit_msg": {
		build: func(t *testing.T, h *testHarness) any {
			const project = "cov-ingestcommitmsg"
			h.seedProject(t, project)
			projDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projDir, "commit.msg"), []byte("feat: coverage fixture\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return map[string]any{"project": project, "project_path": projDir}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Project      string `json:"project"`
				BytesWritten int    `json:"bytes_written"`
			}
			covUnmarshal(t, payload, &out)
			if out.Project != "cov-ingestcommitmsg" {
				t.Errorf("project = %q", out.Project)
			}
			if out.BytesWritten == 0 {
				t.Error("bytes_written is 0")
			}
		},
	},

	"vp_archive_commit_log": {
		build: func(t *testing.T, h *testHarness) any {
			const project = "cov-archivecommitlog"
			h.seedProject(t, project)
			projDir := t.TempDir()
			initGitRepo(t, projDir) // 3 commits
			return map[string]any{"project": project, "project_path": projDir}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				CommitsArchived int `json:"commits_archived"`
			}
			covUnmarshal(t, payload, &out)
			// initGitRepo makes 3 commits; the oldest root commit is the
			// exclusive lower bound on a first run, so 2 land in the archive.
			if out.CommitsArchived != 2 {
				t.Errorf("commits_archived = %d, want 2", out.CommitsArchived)
			}
		},
	},

	// -----------------------------------------------------------------
	// Tasks
	// -----------------------------------------------------------------

	"vp_manage_task": {
		build: func(t *testing.T, h *testHarness) any {
			h.seedProject(t, "cov-managetask")
			return map[string]any{
				"project": "cov-managetask", "action": "create", "task": "cov-task",
				"title": "Coverage Task", "content": testinfra.TaskBody("Coverage fixture task."),
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status string `json:"status"`
				Task   string `json:"task"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "created" {
				t.Errorf("status = %q, want created", out.Status)
			}
			if out.Task != "cov-task" {
				t.Errorf("task = %q, want cov-task", out.Task)
			}
			// Verify the side effect actually landed with the right slug/title/
			// content, not just that the create call echoed back a status word.
			get := h.callTool(t, "vp_get_task", map[string]any{
				"project": "cov-managetask", "task": "cov-task", "include_content": true,
			})
			var gt struct {
				Meta struct {
					Slug  string `json:"slug"`
					Title string `json:"title"`
				} `json:"meta"`
				Content string `json:"content"`
			}
			covUnmarshal(t, get, &gt)
			if gt.Meta.Slug != "cov-task" || gt.Meta.Title != "Coverage Task" {
				t.Errorf("created task meta = %+v, want slug=cov-task title='Coverage Task'", gt.Meta)
			}
			if !strings.Contains(gt.Content, "Coverage fixture task.") {
				t.Errorf("created task content = %q, missing seeded body", gt.Content)
			}
		},
	},

	"vp_get_task": {
		build: func(t *testing.T, h *testHarness) any {
			h.seedProject(t, "cov-gettask")
			h.callTool(t, "vp_manage_task", map[string]any{
				"project": "cov-gettask", "action": "create", "task": "cov-task",
				"title": "Coverage Task", "content": testinfra.TaskBody("Coverage fixture task."),
			})
			return map[string]any{"project": "cov-gettask", "task": "cov-task", "include_content": true}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Meta struct {
					Slug  string `json:"slug"`
					Title string `json:"title"`
				} `json:"meta"`
				Content string `json:"content"`
			}
			covUnmarshal(t, payload, &out)
			if out.Meta.Slug != "cov-task" || out.Meta.Title != "Coverage Task" {
				t.Errorf("meta = %+v", out.Meta)
			}
			if !strings.Contains(out.Content, "Coverage fixture task.") {
				t.Errorf("content = %q", out.Content)
			}
		},
	},

	"vp_list_tasks": {
		build: func(t *testing.T, h *testHarness) any {
			h.seedProject(t, "cov-listtasks")
			h.callTool(t, "vp_manage_task", map[string]any{
				"project": "cov-listtasks", "action": "create", "task": "cov-task",
				"title": "Coverage Task", "content": testinfra.TaskBody("Coverage fixture task."),
			})
			return map[string]any{"project": "cov-listtasks"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Tasks []struct {
					Slug string `json:"slug"`
				} `json:"tasks"`
			}
			covUnmarshal(t, payload, &out)
			var found bool
			for _, ts := range out.Tasks {
				if ts.Slug == "cov-task" {
					found = true
				}
			}
			if !found {
				t.Errorf("cov-task missing from list_tasks: %+v", out.Tasks)
			}
		},
	},

	// -----------------------------------------------------------------
	// Memory
	// -----------------------------------------------------------------

	"vp_memory_write": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{
				"project": "cov-memwrite", "rel": "pref-cov.md", "name": "Coverage Pref",
				"type": "feedback", "body": "The user prefers coverage fixtures.",
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status string `json:"status"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "written" {
				t.Errorf("status = %q, want written", out.Status)
			}
		},
	},

	"vp_memory_read": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_memory_write", map[string]any{
				"project": "cov-memread", "rel": "pref-cov.md", "name": "Coverage Pref",
				"type": "feedback", "body": "The user prefers coverage fixtures.",
			})
			return map[string]any{"project": "cov-memread", "rel": "pref-cov.md"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Meta struct {
					Name string `json:"name"`
				} `json:"meta"`
				Body string `json:"body"`
			}
			covUnmarshal(t, payload, &out)
			if out.Meta.Name != "Coverage Pref" {
				t.Errorf("meta.name = %q", out.Meta.Name)
			}
			if !strings.Contains(out.Body, "coverage fixtures") {
				t.Errorf("body = %q", out.Body)
			}
		},
	},

	"vp_memory_list": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_memory_write", map[string]any{
				"project": "cov-memlist", "rel": "pref-a.md", "name": "A",
				"type": "feedback", "body": "a",
			})
			h.callTool(t, "vp_memory_write", map[string]any{
				"project": "cov-memlist", "rel": "pref-b.md", "name": "B",
				"type": "feedback", "body": "b",
			})
			return map[string]any{"project": "cov-memlist"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Memories []storage.MemoryMeta `json:"memories"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Memories) != 2 {
				t.Fatalf("got %d memories, want 2: %+v", len(out.Memories), out.Memories)
			}
		},
	},

	"vp_memory_delete": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_memory_write", map[string]any{
				"project": "cov-memdelete", "rel": "pref-cov.md", "name": "Coverage Pref",
				"type": "feedback", "body": "body",
			})
			return map[string]any{"project": "cov-memdelete", "rel": "pref-cov.md"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status string `json:"status"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "deleted" {
				t.Errorf("status = %q, want deleted", out.Status)
			}
			// Verify the side effect: the index is now empty.
			list := h.callTool(t, "vp_memory_list", map[string]any{"project": "cov-memdelete"})
			var lr struct {
				Memories []storage.MemoryMeta `json:"memories"`
			}
			covUnmarshal(t, list, &lr)
			if len(lr.Memories) != 0 {
				t.Errorf("memory still listed after delete: %+v", lr.Memories)
			}
		},
	},

	"vp_memory_harvest": {
		build: func(t *testing.T, h *testHarness) any {
			covGitVault(t, h)
			const project = "cov-memharvest"
			h.seedProject(t, project)
			testinfra.IsolateEnv(t, testinfra.WithClaudeHome())
			cwd := t.TempDir()
			// A minimal native memory index + one typed file, in the shape
			// memory.Harvest expects (MEMORY.md front-matter table + a typed
			// markdown file it routes by metadata.type). Resolved through the
			// real package function rather than a guessed encoding, so this
			// fixture tracks the actual scheme.
			nativeDir, err := memory.NativeDirFromCwd(cwd)
			if err != nil {
				t.Fatalf("resolve native dir: %v", err)
			}
			if err := os.MkdirAll(nativeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			memBody := "---\nname: Cov Harvest\ndescription: d\ntype: feedback\n---\nharvested body\n"
			if err := os.WriteFile(filepath.Join(nativeDir, "pref-cov.md"), []byte(memBody), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(nativeDir, "MEMORY.md"), []byte("# Memory\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return map[string]any{"project": project, "cwd": cwd, "push": false}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Routed    []string `json:"routed"`
				Committed bool     `json:"committed"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Routed) == 0 {
				t.Fatal("expected at least one routed memory file")
			}
			if !out.Committed {
				t.Error("harvest should have committed the routed memory")
			}
		},
	},

	// -----------------------------------------------------------------
	// Vault-relative file accessors
	// -----------------------------------------------------------------

	"vp_vault_write": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{"path": "Projects/cov-vaultwrite/note.md", "content": "hello vault"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out vaultfs.WriteResult
			covUnmarshal(t, payload, &out)
			if out.Bytes != int64(len("hello vault")) {
				t.Errorf("bytes = %d, want %d", out.Bytes, len("hello vault"))
			}
			if out.Sha256 == "" {
				t.Error("sha256 is empty")
			}
		},
	},

	"vp_vault_edit": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_vault_write", map[string]any{
				"path": "Projects/cov-vaultedit/note.md", "content": "hello vault",
			})
			return map[string]any{
				"path": "Projects/cov-vaultedit/note.md", "old_string": "hello", "new_string": "goodbye",
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out vaultfs.EditResult
			covUnmarshal(t, payload, &out)
			if out.Replacements != 1 {
				t.Errorf("replacements = %d, want 1", out.Replacements)
			}
			read := h.callTool(t, "vp_vault_read", map[string]any{"path": "Projects/cov-vaultedit/note.md"})
			var rr vaultfs.Content
			covUnmarshal(t, read, &rr)
			if rr.Content != "goodbye vault" {
				t.Errorf("content after edit = %q, want 'goodbye vault'", rr.Content)
			}
		},
	},

	"vp_vault_read": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_vault_write", map[string]any{
				"path": "Projects/cov-vaultread/note.md", "content": "read me",
			})
			return map[string]any{"path": "Projects/cov-vaultread/note.md"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out vaultfs.Content
			covUnmarshal(t, payload, &out)
			if out.Content != "read me" {
				t.Errorf("content = %q, want 'read me'", out.Content)
			}
		},
	},

	"vp_vault_list": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_vault_write", map[string]any{
				"path": "Projects/cov-vaultlist/note.md", "content": "x",
			})
			return map[string]any{"path": "Projects/cov-vaultlist"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Entries  []vaultfs.Entry `json:"entries"`
				Complete bool            `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			var found bool
			for _, e := range out.Entries {
				if e.Name == "note.md" && e.Type == "file" {
					found = true
				}
			}
			if !found {
				t.Errorf("note.md missing from list: %+v", out.Entries)
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_vault_exists": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_vault_write", map[string]any{
				"path": "Projects/cov-vaultexists/note.md", "content": "x",
			})
			return map[string]any{"path": "Projects/cov-vaultexists/note.md"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out vaultfs.Existence
			covUnmarshal(t, payload, &out)
			if !out.Exists || out.Type != "file" {
				t.Errorf("existence = %+v, want exists=true type=file", out)
			}
		},
	},

	"vp_vault_sha256": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_vault_write", map[string]any{
				"path": "Projects/cov-vaultsha/note.md", "content": "shasum me",
			})
			return map[string]any{"path": "Projects/cov-vaultsha/note.md"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out vaultfs.Sha256Result
			covUnmarshal(t, payload, &out)
			if len(out.Sha256) != 64 {
				t.Errorf("sha256 = %q, want a 64-char hex digest", out.Sha256)
			}
			if out.Bytes != int64(len("shasum me")) {
				t.Errorf("bytes = %d, want %d", out.Bytes, len("shasum me"))
			}
		},
	},

	"vp_vault_move": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_vault_write", map[string]any{
				"path": "Projects/cov-vaultmove/from.md", "content": "x",
			})
			return map[string]any{
				"from_path": "Projects/cov-vaultmove/from.md", "to_path": "Projects/cov-vaultmove/to.md",
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out vaultfs.MoveResult
			covUnmarshal(t, payload, &out)
			if !out.Moved {
				t.Error("moved is not true")
			}
			exists := h.callTool(t, "vp_vault_exists", map[string]any{"path": "Projects/cov-vaultmove/to.md"})
			var er vaultfs.Existence
			covUnmarshal(t, exists, &er)
			if !er.Exists {
				t.Error("destination path does not exist after move")
			}
		},
	},

	"vp_vault_delete": {
		build: func(t *testing.T, h *testHarness) any {
			h.callTool(t, "vp_vault_write", map[string]any{
				"path": "Projects/cov-vaultdelete/note.md", "content": "x",
			})
			return map[string]any{"path": "Projects/cov-vaultdelete/note.md"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out vaultfs.DeleteResult
			covUnmarshal(t, payload, &out)
			if !out.Removed {
				t.Error("removed is not true")
			}
			exists := h.callTool(t, "vp_vault_exists", map[string]any{"path": "Projects/cov-vaultdelete/note.md"})
			var er vaultfs.Existence
			covUnmarshal(t, exists, &er)
			if er.Exists {
				t.Error("path still exists after delete")
			}
		},
	},

	"vp_update_resume": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{
				"project": "cov-updateresume", "content": "resume body", "expected_sha256": "",
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status string `json:"status"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "updated" {
				t.Errorf("status = %q, want updated", out.Status)
			}
		},
	},

	// -----------------------------------------------------------------
	// Vault split / merge
	// -----------------------------------------------------------------

	"vp_vault_split": {
		build: func(t *testing.T, h *testHarness) any {
			const project = "cov-vaultsplit"
			h.callTool(t, "vp_vault_write", map[string]any{
				"path": "Projects/" + project + "/note.md", "content": "splittable content",
			})
			return map[string]any{
				"action": "plan", "slugs": []string{project}, "destination": t.TempDir(),
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Files          int    `json:"files"`
				ManifestSHA256 string `json:"manifest_sha256"`
				Complete       bool   `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if out.Files < 1 {
				t.Errorf("files = %d, want >= 1", out.Files)
			}
			if out.ManifestSHA256 == "" {
				t.Error("manifest_sha256 is empty")
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	"vp_vault_merge": {
		build: func(t *testing.T, h *testHarness) any {
			const project = "cov-vaultmerge"
			// vaultMergePlan reports the DESTINATION's git remotes unconditionally
			// (mergeDestinationRemotes), which is a hard refusal against a
			// non-repository destination — so the bound vault itself must be a
			// git repo here, even though plan writes nothing.
			covGitVault(t, h)
			sourceRoot := t.TempDir()
			if err := surface.WriteFormat(sourceRoot, surface.RequiredDataFormat); err != nil {
				t.Fatalf("stamp source vault: %v", err)
			}
			projDir := filepath.Join(sourceRoot, "Projects", project)
			if err := os.MkdirAll(projDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(projDir, "note.md"), []byte("mergeable content"), 0o644); err != nil {
				t.Fatal(err)
			}
			return map[string]any{
				"action": "plan", "source": sourceRoot, "slugs": []string{project},
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Files          int    `json:"files"`
				SourceFormat   int    `json:"source_format"`
				ManifestSHA256 string `json:"manifest_sha256"`
				Complete       bool   `json:"complete"`
			}
			covUnmarshal(t, payload, &out)
			if out.Files < 1 {
				t.Errorf("files = %d, want >= 1", out.Files)
			}
			if out.SourceFormat != surface.RequiredDataFormat {
				t.Errorf("source_format = %d, want %d", out.SourceFormat, surface.RequiredDataFormat)
			}
			if out.ManifestSHA256 == "" {
				t.Error("manifest_sha256 is empty")
			}
			if !out.Complete {
				t.Error("complete is not true")
			}
		},
	},

	// -----------------------------------------------------------------
	// Vault-wide status / sync / tidy / init
	// -----------------------------------------------------------------

	"vp_vault_status": {
		build: func(t *testing.T, h *testHarness) any {
			covGitVault(t, h)
			return map[string]any{}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				VaultPath string `json:"vault_path"`
				Remotes   []any  `json:"remotes"`
			}
			covUnmarshal(t, payload, &out)
			if out.VaultPath != h.Vault.Root {
				t.Errorf("vault_path = %q, want %q", out.VaultPath, h.Vault.Root)
			}
			if len(out.Remotes) != 0 {
				t.Errorf("remotes = %+v, want none configured", out.Remotes)
			}
		},
	},

	"vp_vault_sync": {
		build: func(t *testing.T, h *testHarness) any {
			covGitVault(t, h)
			h.callTool(t, "vp_vault_write", map[string]any{
				"path": "Projects/cov-vaultsync/note.md", "content": "x",
			})
			return map[string]any{
				"action":  "pull", // doPush=false: a pure local commit, no remotes required
				"paths":   []string{"Projects/cov-vaultsync/note.md"},
				"message": "coverage fixture commit",
			}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Committed bool   `json:"committed"`
				CommitSHA string `json:"commit_sha"`
			}
			covUnmarshal(t, payload, &out)
			if !out.Committed {
				t.Error("committed is not true")
			}
			if out.CommitSHA == "" {
				t.Error("commit_sha is empty")
			}
		},
	},

	"vp_vault_tidy": {
		build: func(t *testing.T, h *testHarness) any {
			covGitVault(t, h)
			// An untracked capture artifact the tidy classifier should sweep.
			if err := os.MkdirAll(filepath.Join(h.Vault.Root, "Projects/cov-vaulttidy/sessions"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(h.Vault.Root, "Projects/cov-vaulttidy/sessions/2026-06-17.md"), []byte("session\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return map[string]any{"dry_run": true}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				DryRun    bool     `json:"dry_run"`
				Swept     []string `json:"swept"`
				Committed bool     `json:"committed"`
			}
			covUnmarshal(t, payload, &out)
			if !out.DryRun {
				t.Error("dry_run is not true")
			}
			var found bool
			for _, s := range out.Swept {
				if strings.Contains(s, "cov-vaulttidy/sessions/2026-06-17.md") {
					found = true
				}
			}
			if !found {
				t.Errorf("session artifact missing from swept: %+v", out.Swept)
			}
			if out.Committed {
				t.Error("dry_run must not commit")
			}
		},
	},

	"vp_refresh_index": {
		build: func(t *testing.T, h *testHarness) any {
			h.Seed(t, testinfra.WithDrawer("cov-refreshindex", "w", "r", "content to reindex", "facts", "2026-01-01T10:00:00Z"))
			return map[string]any{"project": "cov-refreshindex"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Status  string `json:"status"`
				Drawers int    `json:"drawers"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "rebuilt" {
				t.Errorf("status = %q, want rebuilt", out.Status)
			}
			if out.Drawers < 1 {
				t.Errorf("drawers = %d, want >= 1 (one drawer was seeded)", out.Drawers)
			}
		},
	},

	"vp_init": {
		build: func(t *testing.T, h *testHarness) any {
			projectDir := filepath.Join(t.TempDir(), "cov-init-proj")
			if err := os.MkdirAll(projectDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module cov\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return map[string]any{"path": projectDir, "name": "cov-init-proj"}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				OK      bool   `json:"ok"`
				Project string `json:"project"`
			}
			covUnmarshal(t, payload, &out)
			if !out.OK {
				t.Errorf("vp_init OK = false: %s", payload)
			}
			if out.Project != "cov-init-proj" {
				t.Errorf("project = %q, want cov-init-proj", out.Project)
			}
		},
	},

	// -----------------------------------------------------------------
	// Scan / diagnostics
	// -----------------------------------------------------------------

	"vp_scan_plans": {
		build: func(t *testing.T, h *testHarness) any {
			h.seedProject(t, "cov-scanplans")
			home := testinfra.IsolateEnv(t, testinfra.WithClaudeHome()).ClaudeHome
			candidateDir := filepath.Join(t.TempDir(), "cov-scanplans-candidate")
			if err := os.MkdirAll(candidateDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(candidateDir, ".vibe-palace.toml"),
				[]byte("[project]\nname = \"cov-scanplans\"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			plansDir := filepath.Join(home, "plans")
			if err := os.MkdirAll(plansDir, 0o755); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf("A stray plan referencing %s in its body.\n", candidateDir)
			if err := os.WriteFile(filepath.Join(plansDir, "orphan.md"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			return map[string]any{}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Strays []struct {
					Resolution struct {
						Kind    string `json:"kind"`
						Project string `json:"project"`
					} `json:"resolution"`
				} `json:"strays"`
			}
			covUnmarshal(t, payload, &out)
			if len(out.Strays) != 1 {
				t.Fatalf("got %d strays, want 1: %+v", len(out.Strays), out.Strays)
			}
			if out.Strays[0].Resolution.Kind != "managed" || out.Strays[0].Resolution.Project != "cov-scanplans" {
				t.Errorf("resolution = %+v, want managed/cov-scanplans", out.Strays[0].Resolution)
			}
		},
	},

	"vp_check": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out tools.CheckSuiteResult
			covUnmarshal(t, payload, &out)
			if len(out.Checks) != len(check.ProducerOrder) {
				t.Errorf("got %d check rows, want %d (check.ProducerOrder)", len(out.Checks), len(check.ProducerOrder))
			}
			if out.Status == "" {
				t.Error("status is empty")
			}
		},
	},

	"vp_surface_check": {
		build: func(t *testing.T, h *testHarness) any {
			return map[string]any{}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out tools.SurfaceCheckResult
			covUnmarshal(t, payload, &out)
			if out.Status != "pass" {
				t.Errorf("status = %q, want pass on a fresh compatible vault", out.Status)
			}
		},
	},

	"vp_repo_freshness": {
		build: func(t *testing.T, h *testHarness) any {
			// A project checkout that is deliberately NOT the vault, with no
			// remote configured — the tool must report "unverified", never a
			// fabricated "up_to_date", for a repo with nothing to check against.
			dir := t.TempDir()
			initGitRepo(t, dir)
			return map[string]any{"project_path": dir}
		},
		assert: func(t *testing.T, h *testHarness, payload string) {
			var out struct {
				Branch  string `json:"branch"`
				Status  string `json:"status"`
				Remotes []any  `json:"remotes"`
			}
			covUnmarshal(t, payload, &out)
			if out.Status != "unverified" {
				t.Errorf("status = %q, want unverified (no remote configured)", out.Status)
			}
			if len(out.Remotes) != 0 {
				t.Errorf("remotes = %+v, want none configured", out.Remotes)
			}
		},
	},
}

// TestToolCoverageComplete is the completeness gate: every tool
// h.Server.Registry().List() reports must have an entry in
// toolCoverageFixtures, and every fixture must name a tool that is still
// registered. A tool added to the registry without a fixture fails here with
// a readable "missing fixture" message; a fixture whose tool was removed
// fails as "has no registered tool" — the same two-way diff shape as
// TestMutatingToolNamesMatchRegistry and TestRegisterAll.
//
// vp_carried_add and vp_carried_promote_to_task are deliberately absent from
// toolCoverageFixtures: both were deleted (commit f656597) and carry zero
// registrations today. If either is ever re-added, this gate will demand a
// fixture for it like any other tool — nothing here special-cases them.
func TestToolCoverageComplete(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	registered := map[string]bool{}
	for _, ti := range h.Server.Registry().List() {
		registered[ti.Name] = true
	}

	for name := range registered {
		if _, ok := toolCoverageFixtures[name]; !ok {
			t.Errorf("missing fixture for %q: every registered tool needs a toolCoverageFixtures entry", name)
		}
	}
	for name := range toolCoverageFixtures {
		if !registered[name] {
			t.Errorf("fixture for %q has no registered tool: remove it, or check for a rename", name)
		}
	}
}

// TestToolCoverageExecution actually dispatches every fixtured tool through
// the real MCP tools/call path and runs its assertion against the returned
// payload — the piece TestToolCoverageComplete cannot do, since it only
// diffs name sets.
//
// Wrapped in testinfra.SentinelPATH(t) defensively: today no registered tool
// reaches internal/mcphost or execs an agent CLI (vp_check's stale-mcp
// producer is excluded from check.ProducerOrder; confirmed by grep against
// internal/tools), but this is the one test guaranteed to call every
// registered tool in one run, including ones that touch process tables or
// $HOME plugin trees. A future tool that starts reaching mcphost/exec without
// updating this file fails here, loudly, rather than flaking later on
// whichever machine happens to have an agent CLI on PATH.
//
// SentinelPATH calls t.Setenv, which panics once t.Parallel has been called on
// this test or an ancestor. It is called here, on the outer test, BEFORE any
// subtest — and none of the subtests below call t.Parallel. If a future pass
// parallelizes the per-tool subtests, SentinelPATH must stay on the parent,
// ahead of every t.Run(...).Parallel(), not moved inside them.
func TestToolCoverageExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SentinelPATH sentinels are #!/bin/sh scripts; Windows is out of scope")
	}
	testinfra.SentinelPATH(t)

	names := make([]string, 0, len(toolCoverageFixtures))
	for name := range toolCoverageFixtures {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		name, fx := name, toolCoverageFixtures[name]
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, false)
			h.registerAllTools(t)

			args := fx.build(t, h)
			payload, isErr := h.callToolRaw(t, name, args)
			if isErr {
				t.Fatalf("%s call failed: %s", name, payload)
			}
			fx.assert(t, h, payload)
		})
	}
}
