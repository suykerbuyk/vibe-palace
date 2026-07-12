// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"strings"
	"testing"
)

// This file pins the FRICTION on vp_manage_task. Read the scope honestly before
// trusting it:
//
//   - create must carry a real body (schema: `content` present; handler: at
//     least minTaskContentBytes of it).
//   - retire must carry approved_by_human=true.
//   - update_status can no longer reach a terminal state.
//
// approved_by_human is a boolean the AGENT PASSES TO ITSELF. Nothing in this
// codebase can ask a human anything — there is no elicitation, no prompt, no
// out-of-band channel. It is ATTESTATION, not AUTHORIZATION, and at least six
// other doors (vp_vault_write/edit/move/delete, vp_carried_promote_to_task, the
// `vp vault move` CLI, and plain `mv` on what is an ordinary git checkout) reach
// the identical on-disk state without passing through any of this. What these
// tests protect is narrow and worth protecting: the shortest, default,
// didn't-notice path to an agent closing out its own work.
//
// EVERY case here drives the REAL MCP SERVER (callToolRaw → HandleMessage), not
// the handler. That is the whole point: the schema conditionals are enforced by
// validateParams BEFORE the handler runs, so a handler-level test would pass
// even with a broken schema.

// TestManageTaskSchema_RetireWithoutApprovalIsRefused_CANARY is THE CANARY.
//
// The retire/create requirements are expressed as JSON Schema `if`/`then`
// conditionals, which exist only from draft-7 onward. compileSchema
// (internal/mcp/tools.go) never calls DefaultDraft, so conditionals work today
// partly by luck — the library falls back to the latest draft. manageTaskSchema
// pins "$schema" to 2020-12 to make that explicit and immune.
//
// If that pin is ever removed, or someone sets DefaultDraft(Draft6), if/then
// stops being a keyword: the conditionals are then silently ignored — no error,
// no warning, no log line — and this retire WITHOUT approved_by_human starts
// SUCCEEDING. This subtest is the only thing that will say so out loud. If it
// ever fails, the conditionals across EVERY tool in this project have gone
// quiet, not just this one.
func TestManageTaskSchema_RetireWithoutApprovalIsRefused_CANARY(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	const project, slug = "friction-proj", "canary-task"
	h.callTool(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "create",
		"task":    slug,
		"title":   "Canary Task",
		"content": taskBody("A task that must not be retirable without an explicit attestation."),
	})

	text, isErr := h.callToolRaw(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "retire",
		"task":    slug,
	})
	if !isErr {
		t.Fatalf("CANARY FAILED: retire without approved_by_human was ACCEPTED (%s).\n"+
			"The schema conditionals are no longer being enforced — check that "+
			"manageTaskSchema still pins $schema to draft 2020-12 and that "+
			"compileSchema has not had DefaultDraft set to a pre-draft-7 draft. "+
			"This affects EVERY if/then in EVERY tool, not just this one.", text)
	}
	if !strings.Contains(text, "approved_by_human") {
		t.Errorf("refusal should name the missing field, got: %s", text)
	}

	// And the task must still be active — refused means nothing moved.
	list := h.callTool(t, "vp_list_tasks", map[string]any{"project": project})
	if !strings.Contains(list, slug) {
		t.Errorf("refused retire must leave the task active, but it is gone: %s", list)
	}
}

// TestManageTaskFriction_CreateWithoutContentIsRefusedBySchema pins the schema
// conditional: `content` is required on create — and ONLY on create.
func TestManageTaskFriction_CreateWithoutContentIsRefusedBySchema(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	text, isErr := h.callToolRaw(t, "vp_manage_task", map[string]any{
		"project": "friction-proj",
		"action":  "create",
		"task":    "no-body",
		"title":   "No Body",
	})
	if !isErr {
		t.Fatalf("create without content was accepted: %s", text)
	}
	if !strings.Contains(text, "content") {
		t.Errorf("refusal should name the missing field, got: %s", text)
	}
}

// TestManageTaskFriction_CreateWithStubBodyIsRefusedByHandler pins the
// minimum-content floor, which lives in the HANDLER (a schema minLength cannot
// explain itself). This is friction, not proof: an agent can pad to clear it.
// What it catches is the real, observed failure — a body that is a pointer to a
// plan kept host-locally rather than the plan itself.
func TestManageTaskFriction_CreateWithStubBodyIsRefusedByHandler(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	for _, stub := range []string{
		"todo",
		"See PLAN.md in the working tree for the full plan.",
	} {
		text, isErr := h.callToolRaw(t, "vp_manage_task", map[string]any{
			"project": "friction-proj",
			"action":  "create",
			"task":    "stub-body",
			"title":   "Stub Body",
			"content": stub,
		})
		if !isErr {
			t.Fatalf("create with a %d-byte stub body was accepted: %s", len(stub), text)
		}
		if !strings.Contains(text, "minimum") {
			t.Errorf("refusal should explain the floor, got: %s", text)
		}
	}
}

// TestManageTaskFriction_CreateWithRealBodyIsAccepted is the positive control:
// the friction must not have broken the ordinary path.
func TestManageTaskFriction_CreateWithRealBodyIsAccepted(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	raw := h.callTool(t, "vp_manage_task", map[string]any{
		"project": "friction-proj",
		"action":  "create",
		"task":    "real-body",
		"title":   "Real Body",
		"content": taskBody("Implement the thing, with enough detail that a later reader can act on it."),
	})
	if !strings.Contains(raw, "created") {
		t.Fatalf("create with a real body: %s", raw)
	}
}

// TestManageTaskFriction_RetireWithApprovalIsAccepted is the other half of the
// canary: with the attestation present, retire works exactly as before —
// storage.RetireTask is UNCHANGED.
func TestManageTaskFriction_RetireWithApprovalIsAccepted(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	const project, slug = "friction-proj", "retire-ok"
	h.callTool(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "create",
		"task":    slug,
		"title":   "Retire OK",
		"content": taskBody("A task the human has actually signed off on."),
	})

	raw := h.callTool(t, "vp_manage_task", map[string]any{
		"project":           project,
		"action":            "retire",
		"task":              slug,
		"approved_by_human": true,
	})
	if !strings.Contains(raw, "retired") {
		t.Fatalf("retire with approval: %s", raw)
	}
}

// TestManageTaskFriction_RetireWithApprovalFalseIsRefused pins that the schema
// requires the field to be PRESENT and the handler requires it to be TRUE —
// passing false explicitly is not a way through.
func TestManageTaskFriction_RetireWithApprovalFalseIsRefused(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	const project, slug = "friction-proj", "retire-false"
	h.callTool(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "create",
		"task":    slug,
		"title":   "Retire False",
		"content": taskBody("A task nobody has signed off on."),
	})

	text, isErr := h.callToolRaw(t, "vp_manage_task", map[string]any{
		"project":           project,
		"action":            "retire",
		"task":              slug,
		"approved_by_human": false,
	})
	if !isErr {
		t.Fatalf("retire with approved_by_human=false was accepted: %s", text)
	}
	if !strings.Contains(text, "human must say the task is done") {
		t.Errorf("refusal should say what is actually required, got: %s", text)
	}
}

// TestManageTaskFriction_UpdateStatusTerminalValuesRefused pins 3c: the terminal
// statuses are gone from the update_status enum. A task reaches a terminal state
// by MOVING (retire/cancel), never by being stamped in place — a "completed"
// file left sitting in tasks/ reads as finished and behaves as active.
//
// Honest scope: 0 of the 114 task files on disk use "completed". This closes a
// door almost nobody walks through. Do not read it as coverage.
func TestManageTaskFriction_UpdateStatusTerminalValuesRefused(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	const project, slug = "friction-proj", "status-task"
	h.callTool(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "create",
		"task":    slug,
		"title":   "Status Task",
		"content": taskBody("A task whose status is driven through update_status."),
	})

	for _, terminal := range []string{"completed", "retired", "cancelled"} {
		t.Run(terminal, func(t *testing.T) {
			text, isErr := h.callToolRaw(t, "vp_manage_task", map[string]any{
				"project": project,
				"action":  "update_status",
				"task":    slug,
				"status":  terminal,
			})
			if !isErr {
				t.Fatalf("update_status to terminal %q was accepted: %s", terminal, text)
			}
		})
	}
}

// TestManageTaskFriction_UpdateStatusNonTerminalAccepted is the positive control
// for 3c: the statuses a live task actually moves through still work.
func TestManageTaskFriction_UpdateStatusNonTerminalAccepted(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	const project, slug = "friction-proj", "status-ok"
	h.callTool(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "create",
		"task":    slug,
		"title":   "Status OK",
		"content": taskBody("A task that moves through the live statuses."),
	})

	for _, status := range []string{"in_progress", "blocked", "pending"} {
		raw := h.callTool(t, "vp_manage_task", map[string]any{
			"project": project,
			"action":  "update_status",
			"task":    slug,
			"status":  status,
		})
		if !strings.Contains(raw, status) {
			t.Fatalf("update_status %q: %s", status, raw)
		}
	}
}

// TestManageTaskFriction_SharedRequiredArrayNotBroken is the regression test for
// the trap this design exists to avoid.
//
// `content` and `approved_by_human` must NOT be in the schema's top-level
// `required` array: that array is enforced for ALL FOUR actions before the
// handler runs, so putting them there would reject every update_status, retire
// and cancel. They are attached to their action via `allOf`/`if`/`then` instead.
// These two calls — cancel and update_status, both carrying NEITHER field — are
// what proves it.
func TestManageTaskFriction_SharedRequiredArrayNotBroken(t *testing.T) {
	h := newHarness(t, false)
	h.registerAllTools(t)

	const project = "friction-proj"

	h.callTool(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "create",
		"task":    "shared-required",
		"title":   "Shared Required",
		"content": taskBody("Proves the shared required array still admits the other actions."),
	})

	// update_status: no content, no approved_by_human.
	raw := h.callTool(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "update_status",
		"task":    "shared-required",
		"status":  "in_progress",
	})
	if !strings.Contains(raw, "in_progress") {
		t.Fatalf("update_status without content/approved_by_human must work: %s", raw)
	}

	// cancel: no content, no approved_by_human. Cancelling is abandoning work,
	// not claiming it is done — it carries no attestation by design.
	raw = h.callTool(t, "vp_manage_task", map[string]any{
		"project": project,
		"action":  "cancel",
		"task":    "shared-required",
	})
	if !strings.Contains(raw, "cancelled") {
		t.Fatalf("cancel without content/approved_by_human must work: %s", raw)
	}
}
