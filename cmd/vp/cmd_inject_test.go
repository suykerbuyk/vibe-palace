// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

func TestRunInjectEmpty(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	code := runInject(v, "test-proj", 8000, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var result tools.BootstrapResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result.Project != "test-proj" {
		t.Errorf("project = %q", result.Project)
	}
}

func TestRunInjectWithData(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", "my-task", "My Task", "content", "high")
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "Test Session", Tag: "impl",
	}, "body")

	var buf bytes.Buffer
	code := runInject(v, "test-proj", 8000, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var result tools.BootstrapResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(result.ActiveTasks) != 1 {
		t.Errorf("active_tasks = %d, want 1", len(result.ActiveTasks))
	}
	if len(result.RecentSessions) != 1 {
		t.Errorf("recent_sessions = %d, want 1", len(result.RecentSessions))
	}
}

func TestRunInjectTokenBudget(t *testing.T) {
	v := testVault(t)
	// Create enough sessions to exceed a small token budget.
	for i := 0; i < 10; i++ {
		v.WriteSession("test-proj", storage.SessionMeta{
			Date: "2026-04-01", Title: "Session with long title for token budget testing",
			Summary: "This is a longer summary to consume more tokens in the output",
			Tag:     "impl",
		}, "body content that takes up space")
	}

	var buf bytes.Buffer
	code := runInject(v, "test-proj", 500, &buf) // very small budget
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var result tools.BootstrapResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// With a tiny budget, sessions should be truncated.
	if len(result.RecentSessions) >= 5 {
		t.Errorf("expected truncated sessions, got %d", len(result.RecentSessions))
	}
}
