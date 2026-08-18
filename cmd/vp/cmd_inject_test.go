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
	code := runInject(v, "test-proj", &buf)
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
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "my-task", Title: "My Task", Content: "content", Priority: "high"})
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "Test Session", Tag: "impl",
	}, "body")

	var buf bytes.Buffer
	code := runInject(v, "test-proj", &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var result tools.BootstrapResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result.ActiveTaskCount != 1 {
		t.Errorf("active_task_count = %d, want 1", result.ActiveTaskCount)
	}
	if len(result.HeadOfQueue) != 1 {
		t.Errorf("head_of_queue = %d, want 1", len(result.HeadOfQueue))
	}
	if len(result.RecentSessions) != 1 {
		t.Errorf("recent_sessions = %d, want 1", len(result.RecentSessions))
	}
}
