// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func testVault(t *testing.T) *storage.Vault {
	t.Helper()
	return storage.NewVault(t.TempDir())
}

func TestRunStatusEmpty(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	code := runStatus(v, "test-proj", false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Project: test-proj") {
		t.Errorf("missing project name in output: %s", out)
	}
	if !strings.Contains(out, "Tasks:   0 active") {
		t.Errorf("missing task count: %s", out)
	}
	if !strings.Contains(out, "Sessions: 0 total") {
		t.Errorf("missing session count: %s", out)
	}
}

func TestRunStatusWithData(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "task-one", Title: "Task One", Content: "content", Priority: "high"})
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "task-two", Title: "Task Two", Content: "content", Priority: "low"})
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "Session 1", Tag: "impl",
	}, "body")

	var buf bytes.Buffer
	code := runStatus(v, "test-proj", false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Tasks:   2 active") {
		t.Errorf("expected 2 tasks: %s", out)
	}
	if !strings.Contains(out, "Sessions: 1 total") {
		t.Errorf("expected 1 session: %s", out)
	}
}

func TestRunStatusJSON(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "task-one", Title: "Task One", Content: "content", Priority: "high"})

	var buf bytes.Buffer
	code := runStatus(v, "test-proj", true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var result statusResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result.Project != "test-proj" {
		t.Errorf("project = %q", result.Project)
	}
	if result.Tasks != 1 {
		t.Errorf("tasks = %d, want 1", result.Tasks)
	}
}
