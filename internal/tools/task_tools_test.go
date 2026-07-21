// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestListTasksEmpty(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ListTasksTool(vault)

	params, _ := json.Marshal(listTasksParams{Project: "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]any)
	tasks := m["tasks"].([]storage.TaskMeta)
	if len(tasks) != 0 {
		t.Errorf("tasks = %v", tasks)
	}
}

func TestListTasksPopulated(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "my-task", Title: "My Task", Content: "content", Priority: "high"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := ListTasksTool(vault)
	params, _ := json.Marshal(listTasksParams{Project: "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]any)
	tasks := m["tasks"].([]storage.TaskMeta)
	if len(tasks) != 1 {
		t.Fatalf("tasks count = %d", len(tasks))
	}
	if tasks[0].Slug != "my-task" {
		t.Errorf("slug = %q", tasks[0].Slug)
	}
}

func TestListTasksIncludeDone(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "active-task", Title: "Active", Content: "", Priority: "medium"}); err != nil {
		t.Fatalf("CreateTask active: %v", err)
	}
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "done-task", Title: "Done", Content: "", Priority: "low"}); err != nil {
		t.Fatalf("CreateTask done: %v", err)
	}
	if err := vault.RetireTask("test-proj", "done-task"); err != nil {
		t.Fatalf("RetireTask: %v", err)
	}

	tool := ListTasksTool(vault)

	// Without include_done.
	params, _ := json.Marshal(listTasksParams{Project: "test-proj"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	tasks := result.(map[string]any)["tasks"].([]storage.TaskMeta)
	if len(tasks) != 1 {
		t.Errorf("without done: got %d tasks", len(tasks))
	}

	// With include_done.
	params, _ = json.Marshal(listTasksParams{Project: "test-proj", IncludeDone: true})
	result, err = tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	tasks = result.(map[string]any)["tasks"].([]storage.TaskMeta)
	if len(tasks) != 2 {
		t.Errorf("with done: got %d tasks", len(tasks))
	}
}

func TestGetTaskFound(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "my-task", Title: "My Task", Content: "body", Priority: "high"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := GetTaskTool(vault)
	params, _ := json.Marshal(getTaskParams{Project: "test-proj", Task: "my-task"})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	tr := result.(getTaskResult)
	if tr.Meta.Slug != "my-task" {
		t.Errorf("slug = %q", tr.Meta.Slug)
	}
	if tr.Meta.Title != "My Task" {
		t.Errorf("title = %q", tr.Meta.Title)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetTaskTool(vault)

	params, _ := json.Marshal(getTaskParams{Project: "test-proj", Task: "nonexistent"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing task")
	}
}

// unitTaskBody returns a create body that clears the handler's minimum-content
// floor and carries no metadata header of its own (storage.CreateTask rejects a
// body with its own H1 or **Status:** line).
func unitTaskBody() string {
	return strings.Repeat("A real plan line describing what to do and why it matters.\n", 6)
}

func TestManageTaskCreate(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ManageTaskTool(vault)

	params, _ := json.Marshal(manageTaskParams{
		Project:  "test-proj",
		Action:   "create",
		Task:     "new-task",
		Title:    "New Task",
		Content:  unitTaskBody(),
		Priority: "high",
	})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "created" {
		t.Errorf("status = %q", m["status"])
	}

	// Verify it exists.
	meta, _, err := vault.GetTask("test-proj", "new-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if meta.Title != "New Task" {
		t.Errorf("title = %q", meta.Title)
	}
}

func TestManageTaskUpdateStatus(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "my-task", Title: "My Task", Content: "", Priority: "medium"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := ManageTaskTool(vault)
	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj",
		Action:  "update_status",
		Task:    "my-task",
		Status:  "in_progress",
	})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "in_progress" {
		t.Errorf("status = %q", m["status"])
	}
}

func TestManageTaskRetire(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "my-task", Title: "My Task", Content: "", Priority: "medium"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := ManageTaskTool(vault)
	params, _ := json.Marshal(manageTaskParams{
		Project:         "test-proj",
		Action:          "retire",
		Task:            "my-task",
		ApprovedByHuman: true,
	})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("handler: %v", err)
	}

	// Verify it's in done.
	meta, _, err := vault.GetTask("test-proj", "my-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !meta.Done {
		t.Error("expected Done=true")
	}
}

func TestManageTaskCancel(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "my-task", Title: "My Task", Content: "", Priority: "medium"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	tool := ManageTaskTool(vault)
	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj",
		Action:  "cancel",
		Task:    "my-task",
	})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("handler: %v", err)
	}

	meta, _, err := vault.GetTask("test-proj", "my-task")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if meta.Status != "cancelled" {
		t.Errorf("status = %q", meta.Status)
	}
}

func TestManageTaskInvalidAction(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ManageTaskTool(vault)

	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj",
		Action:  "nope",
		Task:    "my-task",
	})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestManageTaskUpdateStatusMissing(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ManageTaskTool(vault)

	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj",
		Action:  "update_status",
		Task:    "my-task",
	})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing status")
	}
}

// ---------------------------------------------------------------------------
// vp_get_task Phase-2 behaviour: include_content tri-state + ContentURI/Size.
// ---------------------------------------------------------------------------

// getTaskCall drives the vp_get_task handler with raw JSON params and returns
// the typed result, mirroring how context_tools_test.go drives bootstrap.
func getTaskCall(t *testing.T, vault *storage.Vault, params string) getTaskResult {
	t.Helper()
	tool := GetTaskTool(vault)
	res, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	gr, ok := res.(getTaskResult)
	if !ok {
		t.Fatalf("result type = %T, want getTaskResult", res)
	}
	return gr
}

// makeBigTask creates a task whose STORED body (header + content, what GetTask
// returns) comfortably exceeds taskExcerptCap so the excerpt path is exercised.
// It returns the canonical task URI and the full stored body.
func makeBigTask(t *testing.T, vault *storage.Vault, project, slug string) (uri, body string) {
	t.Helper()
	content := strings.Repeat("This is a line of task body content for the excerpt test.\n", 80)
	if err := vault.CreateTask(project, storage.TaskSpec{Slug: slug, Title: "Big Task", Content: content, Priority: "high"}); err != nil {
		t.Fatal(err)
	}
	_, stored, err := vault.GetTask(project, slug)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) <= taskExcerptCap {
		t.Fatalf("setup: stored body len %d <= taskExcerptCap %d, excerpt path unreachable",
			len(stored), taskExcerptCap)
	}
	return mcp.TaskURI(project, slug), stored
}

// TestGetTaskURIAndSizeAlwaysSet pins that ContentURI and ContentSize are set on
// every response shape — content included OR dropped — and that they address the
// canonical task URI and the full body length.
func TestGetTaskURIAndSizeAlwaysSet(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	uri, body := makeBigTask(t, vault, "test-proj", "big-task")

	cases := []struct {
		name   string
		params string
	}{
		{"default (no include_content)", `{"project":"test-proj","task":"big-task"}`},
		{"include_content=true", `{"project":"test-proj","task":"big-task","include_content":true}`},
		{"include_content=false", `{"project":"test-proj","task":"big-task","include_content":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gr := getTaskCall(t, vault, tc.params)
			if gr.ContentURI != uri {
				t.Errorf("ContentURI = %q, want %q", gr.ContentURI, uri)
			}
			if gr.ContentSize != len(body) {
				t.Errorf("ContentSize = %d, want %d (full body len)", gr.ContentSize, len(body))
			}
		})
	}
}

// TestGetTaskDefaultIncludesFullBody pins the back-compat contract: with
// include_content omitted (nil) or explicitly true, Content is the full body
// byte-for-byte and Excerpt is empty.
func TestGetTaskDefaultIncludesFullBody(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	_, body := makeBigTask(t, vault, "test-proj", "big-task")

	for _, params := range []string{
		`{"project":"test-proj","task":"big-task"}`,
		`{"project":"test-proj","task":"big-task","include_content":true}`,
	} {
		gr := getTaskCall(t, vault, params)
		if gr.Content != body {
			t.Errorf("params %s: Content not byte-identical to full body (len %d, want %d)",
				params, len(gr.Content), len(body))
		}
		if gr.Excerpt != "" {
			t.Errorf("params %s: Excerpt = %q, want empty when content included", params, gr.Excerpt)
		}
	}
}

// TestGetTaskExcludeContentReturnsExcerpt pins the slim path: include_content=false
// drops the inline Content for a bounded, rune-safe Excerpt that is a genuine
// leading slice of the body and never exceeds the excerpt cap.
func TestGetTaskExcludeContentReturnsExcerpt(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	_, body := makeBigTask(t, vault, "test-proj", "big-task")

	gr := getTaskCall(t, vault, `{"project":"test-proj","task":"big-task","include_content":false}`)

	if gr.Content != "" {
		t.Errorf("Content non-empty (len %d), want empty when include_content=false", len(gr.Content))
	}
	if gr.Excerpt == "" {
		t.Fatal("Excerpt should be non-empty when content is dropped")
	}
	if !utf8.ValidString(gr.Excerpt) {
		t.Error("Excerpt is not valid UTF-8")
	}
	// Bound read from the production constant, not a hardcoded guess.
	if len(gr.Excerpt) > taskExcerptCap {
		t.Errorf("Excerpt len %d exceeds taskExcerptCap %d", len(gr.Excerpt), taskExcerptCap)
	}
	// The excerpt is a true leading slice of the body (runeSafeExcerpt may cut
	// back to the last newline within the cap, which keeps it a prefix).
	if !strings.HasPrefix(body, gr.Excerpt) {
		t.Errorf("Excerpt is not a prefix of the body:\n excerpt head %q", gr.Excerpt[:min(60, len(gr.Excerpt))])
	}
	// The body was big enough that the excerpt is genuinely shorter.
	if len(gr.Excerpt) >= len(body) {
		t.Errorf("Excerpt len %d not shorter than body len %d — excerpt path not exercised", len(gr.Excerpt), len(body))
	}
}

// TestGetTaskExcludeContentSmallBodyStaysInline pins the fix for the wasted-fetch
// case: a body that already fits within taskExcerptCap cannot truncate, so even
// with include_content=false it is returned whole in Content (not as a partial
// Excerpt), sparing the agent a vp_read_resource round-trip.
func TestGetTaskExcludeContentSmallBodyStaysInline(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("test-proj", storage.TaskSpec{Slug: "small-task", Title: "Small", Content: "a tiny body", Priority: "low"}); err != nil {
		t.Fatal(err)
	}
	_, full, err := vault.GetTask("test-proj", "small-task")
	if err != nil {
		t.Fatal(err)
	}
	if len(full) > taskExcerptCap {
		t.Fatalf("fixture too large: %d > cap %d", len(full), taskExcerptCap)
	}

	gr := getTaskCall(t, vault, `{"project":"test-proj","task":"small-task","include_content":false}`)
	if gr.Content != full {
		t.Errorf("small body not returned inline: got Content len %d, want full len %d", len(gr.Content), len(full))
	}
	if gr.Excerpt != "" {
		t.Errorf("Excerpt should be empty for a complete small body, got %q", gr.Excerpt)
	}
	if gr.ContentURI == "" || gr.ContentSize != len(full) {
		t.Errorf("ContentURI/ContentSize must still be set: uri=%q size=%d want %d", gr.ContentURI, gr.ContentSize, len(full))
	}
}

// TestManageTaskAmend_RecordsADecisionIntoThePlan is the end-to-end reason amend
// exists: /vpc-review-plan produces findings and reversals, and before amend they
// had nowhere to land — the tool could change a task's STATUS and its EDGES but
// not its PLAN. A task whose body still asserts a premise you have disproved is a
// task that gets implemented wrong.
func TestManageTaskAmend_RecordsADecisionIntoThePlan(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ManageTaskTool(vault)

	body := "## Diagnosis\n\nThe premise: Rebuild serves a stale vector.\n\n## Verification\n\nDrive the real tool.\n" + unitTaskBody()
	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj", Action: "create", Task: "t",
		Title: "T", Content: body, Priority: "high",
	})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Amend twice with the SAME section: the second must replace, not duplicate.
	for _, decision := range []string{"Provisional.", "REVERSED: the key is already a content hash."} {
		p, _ := json.Marshal(manageTaskParams{
			Project: "test-proj", Action: "amend", Task: "t",
			Section: "Decision (205)", Content: decision,
		})
		result, err := tool.Handler(context.Background(), p)
		if err != nil {
			t.Fatalf("amend: %v", err)
		}
		m, ok := result.(map[string]string)
		if !ok || m["status"] != "amended" || m["section"] != "Decision (205)" {
			t.Errorf("amend result = %#v, want status=amended, section=Decision (205)", result)
		}
	}

	meta, got, err := vault.GetTask("test-proj", "t")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if n := strings.Count(got, "## Decision (205)"); n != 1 {
		t.Errorf("section count = %d, want 1 — a repeated amend duplicated instead of converging:\n%s", n, got)
	}
	if !strings.Contains(got, "REVERSED: the key is already a content hash.") {
		t.Errorf("the second amend did not land:\n%s", got)
	}
	if strings.Contains(got, "Provisional.") {
		t.Errorf("the first amend's body survived the replace:\n%s", got)
	}
	if meta.Status != "pending" || meta.Priority != "high" {
		t.Errorf("amend disturbed the header block: status=%q priority=%q", meta.Status, meta.Priority)
	}
	// The original plan sections must be intact.
	if !strings.Contains(got, "## Diagnosis") || !strings.Contains(got, "## Verification") {
		t.Errorf("amend destroyed the original plan:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// vp_list_tasks Phase-D behaviour: epic / standalone / epics_only derivations.
// ---------------------------------------------------------------------------

const epicFixtureProject = "epic-proj"

// epicFixtureVault builds a small hierarchy exercising every role:
//
//	root-epic (epic)
//	├── story-a (story)          — an epic nested under root-epic
//	│   └── deep-task (task)     — the deepest descendant
//	└── leaf-b (task)            — a leaf directly under the root
//	standalone (task)            — nobody's child, nobody's parent
//
// Parents are created before children only for readability; storage imposes no
// write-time ordering. Bodies are trivial — this fixture tests STRUCTURE.
func epicFixtureVault(t *testing.T) *storage.Vault {
	t.Helper()
	vault := storage.NewVault(t.TempDir())
	mk := func(slug, parent string) {
		spec := storage.TaskSpec{Slug: slug, Title: slug, Content: "body", Priority: "medium"}
		if parent != "" {
			spec.Parent = parent
		}
		if err := vault.CreateTask(epicFixtureProject, spec); err != nil {
			t.Fatalf("CreateTask %s: %v", slug, err)
		}
	}
	mk("root-epic", "")
	mk("story-a", "root-epic")
	mk("deep-task", "story-a")
	mk("leaf-b", "root-epic")
	mk("standalone", "")
	return vault
}

// listTasksCall drives the vp_list_tasks handler and returns the object result.
func listTasksCall(t *testing.T, vault *storage.Vault, p listTasksParams) map[string]any {
	t.Helper()
	tool := ListTasksTool(vault)
	params, _ := json.Marshal(p)
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", res)
	}
	return m
}

// listTasksErr drives the handler expecting an error, returning it.
func listTasksErr(t *testing.T, vault *storage.Vault, p listTasksParams) error {
	t.Helper()
	tool := ListTasksTool(vault)
	params, _ := json.Marshal(p)
	_, err := tool.Handler(context.Background(), params)
	return err
}

// derivedTasks extracts the []taskWithRole payload of a derived-tasks envelope.
func derivedTasks(t *testing.T, m map[string]any) []taskWithRole {
	t.Helper()
	got, ok := m["tasks"].([]taskWithRole)
	if !ok {
		t.Fatalf("tasks payload type = %T, want []taskWithRole", m["tasks"])
	}
	return got
}

// rolesBySlug indexes a derived-tasks slice by slug for order-independent asserts.
func rolesBySlug(items []taskWithRole) map[string]string {
	out := make(map[string]string, len(items))
	for _, it := range items {
		out[it.Slug] = it.Role
	}
	return out
}

// TestListTasksNoParamPathUnchanged pins the fast path: with none of the three
// derivation params set the handler returns the plain flat listing as
// []storage.TaskMeta (NOT the augmented taskWithRole), byte-shape unchanged.
func TestListTasksNoParamPathUnchanged(t *testing.T) {
	vault := epicFixtureVault(t)
	m := listTasksCall(t, vault, listTasksParams{Project: epicFixtureProject})
	tasks, ok := m["tasks"].([]storage.TaskMeta)
	if !ok {
		t.Fatalf("no-param path payload type = %T, want []storage.TaskMeta", m["tasks"])
	}
	// All five active tasks, no role field, no graph derivation.
	if len(tasks) != 5 {
		t.Errorf("flat listing count = %d, want 5", len(tasks))
	}
}

// TestListTasksEpicsOnly pins epics_only: root epics only, role=="epic", nested
// stories absent as top-level rows, and correct transitive open/total counts.
func TestListTasksEpicsOnly(t *testing.T) {
	vault := epicFixtureVault(t)
	m := listTasksCall(t, vault, listTasksParams{Project: epicFixtureProject, EpicsOnly: true})
	rows, ok := m["epics"].([]epicRow)
	if !ok {
		t.Fatalf("epics payload type = %T, want []epicRow", m["epics"])
	}
	if len(rows) != 1 {
		t.Fatalf("epics count = %d, want 1 (only root-epic; story-a is nested)", len(rows))
	}
	r := rows[0]
	if r.Slug != "root-epic" {
		t.Errorf("epic slug = %q, want root-epic", r.Slug)
	}
	if r.Role != "epic" {
		t.Errorf("role = %q, want epic", r.Role)
	}
	// Transitive descendants: story-a, deep-task, leaf-b = 3 open, 3 total.
	if r.Open != 3 || r.Total != 3 {
		t.Errorf("open/total = %d/%d, want 3/3", r.Open, r.Total)
	}
}

// TestListTasksEpicsOnlyCountsSplitOnArchive pins the FIXED counts formula:
// OPEN excludes the archive, TOTAL includes it, regardless of include_done
// (which only decides whether a fully-archived root epic is LISTED).
func TestListTasksEpicsOnlyCountsSplitOnArchive(t *testing.T) {
	vault := epicFixtureVault(t)
	if err := vault.RetireTask(epicFixtureProject, "deep-task"); err != nil {
		t.Fatalf("RetireTask deep-task: %v", err)
	}
	m := listTasksCall(t, vault, listTasksParams{Project: epicFixtureProject, EpicsOnly: true})
	rows := m["epics"].([]epicRow)
	if len(rows) != 1 {
		t.Fatalf("epics count = %d, want 1", len(rows))
	}
	// OPEN drops the retired deep-task (story-a, leaf-b = 2); TOTAL keeps it (3).
	if rows[0].Open != 2 || rows[0].Total != 3 {
		t.Errorf("open/total = %d/%d, want 2/3 (open excludes archive, total includes)", rows[0].Open, rows[0].Total)
	}
}

// TestListTasksEpicRootSubtree pins epic=<root>: the whole transitive subtree,
// each member carrying its derived role, including the deepest descendant.
func TestListTasksEpicRootSubtree(t *testing.T) {
	vault := epicFixtureVault(t)
	m := listTasksCall(t, vault, listTasksParams{Project: epicFixtureProject, Epic: "root-epic"})
	roles := rolesBySlug(derivedTasks(t, m))
	want := map[string]string{
		"root-epic": "epic",
		"story-a":   "story",
		"deep-task": "task",
		"leaf-b":    "task",
	}
	if len(roles) != len(want) {
		t.Fatalf("subtree members = %v, want keys %v", roles, want)
	}
	for slug, role := range want {
		if roles[slug] != role {
			t.Errorf("role[%s] = %q, want %q", slug, roles[slug], role)
		}
	}
}

// TestListTasksEpicNestedStorySubtree pins epic=<story>: a story is an accepted
// grouping node, and its subtree is itself plus its descendants.
func TestListTasksEpicNestedStorySubtree(t *testing.T) {
	vault := epicFixtureVault(t)
	m := listTasksCall(t, vault, listTasksParams{Project: epicFixtureProject, Epic: "story-a"})
	roles := rolesBySlug(derivedTasks(t, m))
	want := map[string]string{"story-a": "story", "deep-task": "task"}
	if len(roles) != len(want) {
		t.Fatalf("subtree members = %v, want keys %v", roles, want)
	}
	for slug, role := range want {
		if roles[slug] != role {
			t.Errorf("role[%s] = %q, want %q", slug, roles[slug], role)
		}
	}
}

// TestListTasksEpicLeafRejected pins that a leaf slug is a caller error.
func TestListTasksEpicLeafRejected(t *testing.T) {
	vault := epicFixtureVault(t)
	err := listTasksErr(t, vault, listTasksParams{Project: epicFixtureProject, Epic: "leaf-b"})
	if err == nil {
		t.Fatal("expected error for leaf slug")
	}
	if !strings.Contains(err.Error(), "leaf") {
		t.Errorf("error = %v, want it to mention leaf", err)
	}
}

// TestListTasksEpicUnknownRejected pins that an absent slug is a caller error.
func TestListTasksEpicUnknownRejected(t *testing.T) {
	vault := epicFixtureVault(t)
	err := listTasksErr(t, vault, listTasksParams{Project: epicFixtureProject, Epic: "ghost"})
	if err == nil {
		t.Fatal("expected error for unknown slug")
	}
	if !strings.Contains(err.Error(), "no such task") {
		t.Errorf("error = %v, want it to mention no such task", err)
	}
}

// TestListTasksStandalone pins standalone: only the no-parent-no-child task,
// role=="task".
func TestListTasksStandalone(t *testing.T) {
	vault := epicFixtureVault(t)
	m := listTasksCall(t, vault, listTasksParams{Project: epicFixtureProject, Standalone: true})
	items := derivedTasks(t, m)
	if len(items) != 1 {
		t.Fatalf("standalone count = %d, want 1 (only 'standalone')", len(items))
	}
	if items[0].Slug != "standalone" {
		t.Errorf("slug = %q, want standalone", items[0].Slug)
	}
	if items[0].Role != "task" {
		t.Errorf("role = %q, want task", items[0].Role)
	}
}

// TestListTasksIncludeDoneTogglesSubtreeArchive pins that include_done governs
// archive inclusion in a derived subtree: the retired deepest descendant is
// hidden by default and surfaced with include_done.
func TestListTasksIncludeDoneTogglesSubtreeArchive(t *testing.T) {
	vault := epicFixtureVault(t)
	if err := vault.RetireTask(epicFixtureProject, "deep-task"); err != nil {
		t.Fatalf("RetireTask deep-task: %v", err)
	}

	// Default: deep-task excluded.
	m := listTasksCall(t, vault, listTasksParams{Project: epicFixtureProject, Epic: "root-epic"})
	if roles := rolesBySlug(derivedTasks(t, m)); roles["deep-task"] != "" {
		t.Errorf("deep-task present without include_done: %v", roles)
	}

	// include_done: deep-task included.
	m = listTasksCall(t, vault, listTasksParams{Project: epicFixtureProject, Epic: "root-epic", IncludeDone: true})
	if roles := rolesBySlug(derivedTasks(t, m)); roles["deep-task"] != "task" {
		t.Errorf("deep-task missing/mis-roled with include_done: %v", roles)
	}
}

// TestListTasksMutuallyExclusiveModes pins that setting two+ derivation modes is
// rejected up front, for every pairing.
func TestListTasksMutuallyExclusiveModes(t *testing.T) {
	vault := epicFixtureVault(t)
	cases := []struct {
		name string
		p    listTasksParams
	}{
		{"epic+standalone", listTasksParams{Project: epicFixtureProject, Epic: "root-epic", Standalone: true}},
		{"epic+epics_only", listTasksParams{Project: epicFixtureProject, Epic: "root-epic", EpicsOnly: true}},
		{"standalone+epics_only", listTasksParams{Project: epicFixtureProject, Standalone: true, EpicsOnly: true}},
		{"all three", listTasksParams{Project: epicFixtureProject, Epic: "root-epic", Standalone: true, EpicsOnly: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := listTasksErr(t, vault, tc.p)
			if err == nil {
				t.Fatal("expected error for mutually-exclusive modes")
			}
			if !strings.Contains(err.Error(), "mutually exclusive") {
				t.Errorf("error = %v, want it to mention mutually exclusive", err)
			}
		})
	}
}

func TestManageTaskAmend_RequiresSectionAndContent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := ManageTaskTool(vault)

	params, _ := json.Marshal(manageTaskParams{
		Project: "test-proj", Action: "create", Task: "t",
		Title: "T", Content: unitTaskBody(), Priority: "high",
	})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("create: %v", err)
	}

	cases := []struct{ name, section, content, want string }{
		{"no section", "", "body", "section is required"},
		{"no content", "Decision", "", "content is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := json.Marshal(manageTaskParams{
				Project: "test-proj", Action: "amend", Task: "t",
				Section: tc.section, Content: tc.content,
			})
			if _, err := tool.Handler(context.Background(), p); err == nil {
				t.Fatal("amend succeeded, want rejection")
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
