// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Archiving is rewrite-then-rename in storage.moveTask (adopted 2026-09-01).
// The ordering tests live beside that function; this one drives the whole
// surface an agent actually calls — vp_manage_task's retire and cancel arms —
// against a temp vault, so a reorder that satisfied the storage tests while
// breaking the handler could not pass unnoticed.
//
// The existing TestManageTaskRetire / TestManageTaskCancel assert Done only.
// This asserts the on-disk placement AND the stamped body, which is what the
// reorder actually moves around.
func TestManageTaskArchiveEndToEndPlacementAndStamp(t *testing.T) {
	for _, tc := range []struct {
		action     string
		wantStatus string
		dirFn      func(v *storage.Vault, project string) (string, error)
	}{
		{"retire", "retired", (*storage.Vault).TaskDoneDir},
		{"cancel", "cancelled", (*storage.Vault).TaskCancelledDir},
	} {
		t.Run(tc.action, func(t *testing.T) {
			vault := storage.NewVault(t.TempDir())
			// The body carries NO heading of its own: create emits the
			// conventional first H2 itself, and a body repeating it is refused
			// (validateTaskBody's conventional-heading arm). This fixture used
			// to pass "## Context\n\nBody.\n" and so built, as a side effect, the
			// exact two-sections-one-key file that refusal exists to prevent —
			// which is what the new arm caught. The placement and stamping this
			// test is about are unaffected by the body's shape.
			if err := vault.CreateTask("test-proj", storage.TaskSpec{
				Slug: "my-task", Title: "My Task", Content: "Body.\n", Priority: "medium",
			}); err != nil {
				t.Fatalf("CreateTask: %v", err)
			}

			params, _ := json.Marshal(manageTaskParams{
				Project:         "test-proj",
				Action:          tc.action,
				Task:            "my-task",
				ApprovedByHuman: true,
			})
			if _, err := ManageTaskTool(vault).Handler(context.Background(), params); err != nil {
				t.Fatalf("%s handler: %v", tc.action, err)
			}

			// Gone from the active directory — exactly one copy exists.
			activePath, _ := vault.TaskFile("test-proj", "my-task")
			if _, err := os.Stat(activePath); !os.IsNotExist(err) {
				t.Errorf("active copy should be gone after %s, stat err = %v", tc.action, err)
			}

			// Present in the archive directory, with the terminal status stamped
			// into the body rather than merely derived from the directory.
			dir, _ := tc.dirFn(vault, "test-proj")
			body, err := os.ReadFile(filepath.Join(dir, "my-task.md"))
			if err != nil {
				t.Fatalf("read archived body: %v", err)
			}

			meta, _, err := vault.GetTask("test-proj", "my-task")
			if err != nil {
				t.Fatalf("GetTask: %v", err)
			}
			if !meta.Done {
				t.Error("Done should be true after archiving")
			}
			if meta.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q — body: %q", meta.Status, tc.wantStatus, body)
			}
		})
	}
}
