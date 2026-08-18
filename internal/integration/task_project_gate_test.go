// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManageTaskCreateRefusesUnknownProjectAndScaffoldsNothing is the defect
// test: a typo'd or hallucinated slug must refuse, and must leave no tree.
//
// The second assertion is the one that matters. CreateTask's EnsureDir runs
// BEFORE its own existence checks (internal/storage/tasks.go), so a create that
// is merely rejected further down still leaves Projects/<slug>/tasks/ behind.
// A gate that refuses but scaffolds anyway would satisfy a refusal-only test
// while leaving exactly the phantom tree it exists to prevent.
//
// Mutation: drop the RequireKnownProject call in the create case and this goes
// red twice — the call succeeds AND the directory appears.
func TestManageTaskCreateRefusesUnknownProjectAndScaffoldsNothing(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	body := strings.Repeat("A real plan line describing what to do and why it matters.\n", 6)
	text, isErr := h.callToolRaw(t, "vp_manage_task", map[string]any{
		"action":  "create",
		"project": "typo-slugg",
		"task":    "some-task",
		"content": body,
	})

	if !isErr {
		t.Fatalf("create into an unknown project SUCCEEDED: %s", text)
	}
	for _, want := range []string{"typo-slugg", "vp init", "vp_list_projects"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal missing %q:\n%s", want, text)
		}
	}

	// Nothing on disk. Not the project, not the tasks dir under it.
	projDir := filepath.Join(h.Vault.Root, "Projects", "typo-slugg")
	if _, err := os.Stat(projDir); !os.IsNotExist(err) {
		t.Errorf("refused create still scaffolded %s (stat err = %v)", projDir, err)
	}
}

// TestManageTaskCreateAcceptsKnownProjectThatIsNotCwd pins the feature the gate
// must not break. Cross-project create is documented behaviour: an agent
// routinely passes a slug that is NOT its own cwd-derived project. The gate
// authorizes on the project EXISTING, never on it being the caller's.
func TestManageTaskCreateAcceptsKnownProjectThatIsNotCwd(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)
	h.seedProject(t, "some-other-project")

	body := strings.Repeat("A real plan line describing what to do and why it matters.\n", 6)
	text, isErr := h.callToolRaw(t, "vp_manage_task", map[string]any{
		"action":  "create",
		"project": "some-other-project",
		"task":    "cross-project-task",
		"content": body,
	})
	if isErr {
		t.Fatalf("create into a KNOWN project was refused: %s", text)
	}
	if _, err := os.Stat(filepath.Join(h.Vault.Root, "Projects", "some-other-project",
		"tasks", "cross-project-task.md")); err != nil {
		t.Errorf("task file not written: %v", err)
	}
}

// TestManageTaskNonScaffoldingActionsRefuseWithoutScaffolding covers the other
// six actions. They are deliberately NOT gated, because none of them can create
// a project tree — each reads or stats the task file first, so the
// atomicfile.Write that would MkdirAll the parent is unreachable.
//
// That is a claim about behaviour, so it is asserted rather than asserted-in-a-
// comment: every action against an unknown project must fail AND leave no tree.
// If a future edit gives one of them a write path that runs before its read,
// this catches it.
func TestManageTaskNonScaffoldingActionsRefuseWithoutScaffolding(t *testing.T) {
	for _, tc := range []struct {
		action string
		extra  map[string]any
	}{
		{"amend", map[string]any{"section": "Decision", "content": "body"}},
		{"set_meta", map[string]any{"title": "New title"}},
		{"update_status", map[string]any{"status": "in_progress"}},
		{"set_relations", map[string]any{"parent": "some-epic"}},
		{"retire", map[string]any{"approved_by_human": true}},
		{"cancel", map[string]any{}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			h := newHarness(t, false)
			h.registerAllTools(t)

			args := map[string]any{
				"action":  tc.action,
				"project": "ghost-project",
				"task":    "ghost-task",
			}
			maps.Copy(args, tc.extra)
			_, isErr := h.callToolRaw(t, "vp_manage_task", args)
			if !isErr {
				t.Errorf("%s against an unknown project succeeded", tc.action)
			}
			projDir := filepath.Join(h.Vault.Root, "Projects", "ghost-project")
			if _, err := os.Stat(projDir); !os.IsNotExist(err) {
				t.Errorf("%s scaffolded %s (stat err = %v)", tc.action, projDir, err)
			}
		})
	}
}
