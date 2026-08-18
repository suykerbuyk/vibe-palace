// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// TestIntegrationStatusSessionsTasks proves that status, sessions, and tasks
// commands all see the same vault data and produce consistent output.
func TestIntegrationStatusSessionsTasks(t *testing.T) {
	v := testVault(t)
	proj := "integration-test"

	// Seed the vault with realistic data.
	v.CreateTask(proj, storage.TaskSpec{Slug: "implement-auth", Title: "Implement authentication", Content: "OAuth2 flow", Priority: "high"})
	v.CreateTask(proj, storage.TaskSpec{Slug: "write-tests", Title: "Write unit tests", Content: "80% coverage target", Priority: "medium"})
	v.WriteSession(proj, storage.SessionMeta{
		Date: "2026-04-01", Title: "Initial auth work", Tag: "implementation",
		FrictionScore: 15,
	}, "Started OAuth2 integration")
	v.WriteSession(proj, storage.SessionMeta{
		Date: "2026-04-02", Title: "Test coverage push", Tag: "testing",
		FrictionScore: 30,
	}, "Added unit tests for auth module")

	// Status should see 2 tasks and 2 sessions.
	var statusBuf bytes.Buffer
	code := runStatus(v, proj, true, &statusBuf)
	if code != cli.ExitOK {
		t.Fatalf("status exit code = %d", code)
	}
	var status statusResult
	json.Unmarshal(statusBuf.Bytes(), &status)
	if status.Tasks != 2 {
		t.Errorf("status.Tasks = %d, want 2", status.Tasks)
	}
	if status.Sessions != 2 {
		t.Errorf("status.Sessions = %d, want 2", status.Sessions)
	}

	// Sessions should list both.
	var sessBuf bytes.Buffer
	code = runSessions(v, proj, sessionsQuery{limit: 10, asJSON: true}, &sessBuf)
	if code != cli.ExitOK {
		t.Fatalf("sessions exit code = %d", code)
	}
	var sessions []storage.SessionMeta
	json.Unmarshal(sessBuf.Bytes(), &sessions)
	if len(sessions) != 2 {
		t.Errorf("sessions count = %d, want 2", len(sessions))
	}

	// Tasks should list both.
	var tasksBuf bytes.Buffer
	code = runTasks(v, proj, taskListOpts{flat: true, asJSON: true}, &tasksBuf)
	if code != cli.ExitOK {
		t.Fatalf("tasks exit code = %d", code)
	}
	var tasks []storage.TaskMeta
	json.Unmarshal(tasksBuf.Bytes(), &tasks)
	if len(tasks) != 2 {
		t.Errorf("tasks count = %d, want 2", len(tasks))
	}

	// Retire one task and verify status reflects it.
	v.RetireTask(proj, "implement-auth")
	statusBuf.Reset()
	runStatus(v, proj, true, &statusBuf)
	json.Unmarshal(statusBuf.Bytes(), &status)
	if status.Tasks != 1 {
		t.Errorf("after retire: status.Tasks = %d, want 1", status.Tasks)
	}

	// Tasks without --done should hide retired.
	tasksBuf.Reset()
	runTasks(v, proj, taskListOpts{flat: true, asJSON: true}, &tasksBuf)
	json.Unmarshal(tasksBuf.Bytes(), &tasks)
	if len(tasks) != 1 {
		t.Errorf("tasks without --done = %d, want 1", len(tasks))
	}

	// Tasks with --done should show both.
	tasksBuf.Reset()
	runTasks(v, proj, taskListOpts{flat: true, includeDone: true, asJSON: true}, &tasksBuf)
	json.Unmarshal(tasksBuf.Bytes(), &tasks)
	if len(tasks) != 2 {
		t.Errorf("tasks with --done = %d, want 2", len(tasks))
	}
}

// TestIntegrationInjectBootstrap proves inject produces the same data
// that the MCP bootstrap tool would return.
func TestIntegrationInjectBootstrap(t *testing.T) {
	v := testVault(t)
	proj := "inject-test"

	v.CreateTask(proj, storage.TaskSpec{Slug: "my-task", Title: "Test Task", Content: "content", Priority: "high"})
	v.WriteSession(proj, storage.SessionMeta{
		Date: "2026-04-01", Title: "Work session", Tag: "impl",
		Summary: "Did some work",
	}, "session body")

	var buf bytes.Buffer
	code := runInject(v, proj, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	var result tools.BootstrapResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if result.Project != proj {
		t.Errorf("project = %q", result.Project)
	}
	if result.ActiveTaskCount != 1 {
		t.Errorf("active_task_count = %d", result.ActiveTaskCount)
	}
	if len(result.HeadOfQueue) != 1 {
		t.Fatalf("head_of_queue = %d", len(result.HeadOfQueue))
	}
	if result.HeadOfQueue[0].Slug != "my-task" {
		t.Errorf("head-of-queue slug = %q", result.HeadOfQueue[0].Slug)
	}
	if len(result.RecentSessions) != 1 {
		t.Errorf("recent_sessions = %d", len(result.RecentSessions))
	}
}

// TestIntegrationSearchPipeline proves that search indexes vault content,
// performs semantic search via mock embedder, and returns results with
// correct metadata from the storage layer.
func TestIntegrationSearchPipeline(t *testing.T) {
	v := testVault(t)
	proj := "search-test"
	cfg := storage.Config{
		SearchDefaultLimit: 10,
		BoostWing:          0.12,
		BoostHall:          0.24,
		BoostRoom:          0.34,
	}
	emb := embedder.NewMock(384)
	eng := search.NewEngine(emb, v, cfg)
	t.Cleanup(func() { eng.Close() })

	// Add drawers to vault — this data must flow through:
	// vault → engine.Rebuild → engine.Search → formatted output
	v.AppendDrawer(proj, "code", "auth", storage.Drawer{
		Content:    "JWT token validation middleware",
		Hall:       "facts",
		SourceType: "session",
		SourceRef:  "session-2026-04-01-01",
		FiledAt:    "2026-04-01T10:00:00Z",
	})
	v.AppendDrawer(proj, "docs", "api", storage.Drawer{
		Content:    "API documentation for authentication endpoints",
		Hall:       "facts",
		SourceType: "manual",
		SourceRef:  "doc/api.md",
		FiledAt:    "2026-04-01T12:00:00Z",
	})

	// Run search (text mode).
	var textBuf bytes.Buffer
	code := runSearch(eng, proj, "authentication", "", "", 10, false, &textBuf)
	if code != cli.ExitOK {
		t.Fatalf("text search exit code = %d", code)
	}
	textOut := textBuf.String()
	if strings.Contains(textOut, "No results") {
		t.Fatal("expected results from search")
	}

	// Run search (JSON mode) and verify metadata integrity.
	var jsonBuf bytes.Buffer
	code = runSearch(eng, proj, "authentication", "", "", 10, true, &jsonBuf)
	if code != cli.ExitOK {
		t.Fatalf("JSON search exit code = %d", code)
	}
	var results []search.SearchResult
	if err := json.Unmarshal(jsonBuf.Bytes(), &results); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}

	// Verify results have correct metadata from vault storage.
	for _, r := range results {
		if r.Project != proj {
			t.Errorf("result.Project = %q, want %q", r.Project, proj)
		}
		if r.Wing == "" {
			t.Error("result.Wing is empty")
		}
		if r.Room == "" {
			t.Error("result.Room is empty")
		}
		if r.Score <= 0 {
			t.Errorf("result.Score = %f, want > 0", r.Score)
		}
	}

	// Verify wing filter works.
	var filteredBuf bytes.Buffer
	runSearch(eng, proj, "authentication", "code", "", 10, true, &filteredBuf)
	var filtered []search.SearchResult
	json.Unmarshal(filteredBuf.Bytes(), &filtered)
	for _, r := range filtered {
		if r.Wing != "code" {
			t.Errorf("filtered result has wing=%q, want code", r.Wing)
		}
	}
}

// TestIntegrationSessionsTableFormat proves the text table output is
// well-formed with correct alignment and data from vault.
func TestIntegrationSessionsTableFormat(t *testing.T) {
	v := testVault(t)
	proj := "table-test"

	v.WriteSession(proj, storage.SessionMeta{
		Date: "2026-04-01", Title: "Auth implementation", Tag: "impl", FrictionScore: 20,
	}, "body")
	v.WriteSession(proj, storage.SessionMeta{
		Date: "2026-04-02", Title: "Bug fixing", Tag: "debug", FrictionScore: 45,
	}, "body")

	var buf bytes.Buffer
	code := runSessions(v, proj, sessionsQuery{limit: 10, asJSON: false}, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 { // header + 2 data lines
		t.Fatalf("expected 3 lines, got %d: %s", len(lines), out)
	}

	// Header should have column names.
	if !strings.Contains(lines[0], "DATE") || !strings.Contains(lines[0], "TAG") {
		t.Errorf("bad header: %s", lines[0])
	}

	// Data lines should have dates and tags.
	if !strings.Contains(lines[1], "2026-04-01") || !strings.Contains(lines[1], "impl") {
		t.Errorf("bad data line 1: %s", lines[1])
	}
	if !strings.Contains(lines[2], "2026-04-02") || !strings.Contains(lines[2], "debug") {
		t.Errorf("bad data line 2: %s", lines[2])
	}
}

// TestIntegrationTasksTableFormat proves the tasks table output shows
// correct priority, slug, status, and title columns.
func TestIntegrationTasksTableFormat(t *testing.T) {
	v := testVault(t)
	proj := "tasktable-test"

	v.CreateTask(proj, storage.TaskSpec{Slug: "high-pri", Title: "High Priority Task", Content: "urgent", Priority: "high"})
	v.CreateTask(proj, storage.TaskSpec{Slug: "low-pri", Title: "Low Priority Task", Content: "not urgent", Priority: "low"})
	v.UpdateTaskStatus(proj, "high-pri", "in_progress")

	var buf bytes.Buffer
	code := runTasks(v, proj, taskListOpts{flat: true}, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %s", len(lines), out)
	}

	if !strings.Contains(lines[0], "PRIORITY") || !strings.Contains(lines[0], "STATUS") {
		t.Errorf("bad header: %s", lines[0])
	}

	// Should show both tasks with correct statuses.
	found := map[string]bool{}
	for _, line := range lines[1:] {
		if strings.Contains(line, "high-pri") {
			found["high-pri"] = true
			if !strings.Contains(line, "in_progress") {
				t.Errorf("high-pri should be in_progress: %s", line)
			}
		}
		if strings.Contains(line, "low-pri") {
			found["low-pri"] = true
			if !strings.Contains(line, "pending") {
				t.Errorf("low-pri should be pending: %s", line)
			}
		}
	}
	if !found["high-pri"] || !found["low-pri"] {
		t.Errorf("missing tasks in output: %s", out)
	}
}
