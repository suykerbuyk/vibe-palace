// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestNewSeedsProjectDrawersResume proves WithProject, WithDrawers, and
// WithResume each write what they claim — read back through the vault, not
// just asserted error-free — and that all three compose in one New(t, ...)
// call.
func TestNewSeedsProjectDrawersResume(t *testing.T) {
	h := New(t,
		WithProject("demo"),
		WithDrawers("demo", "general", "general",
			DrawerSpec{Content: "first drawer", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T10:00:00Z"},
			DrawerSpec{Content: "second drawer", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T10:00:00Z"},
		),
		WithResume("demo", "# Resume: demo\n\nseeded\n"),
	)

	if _, err := os.Stat(filepath.Join(h.Vault.Root, "Projects", "demo")); err != nil {
		t.Fatalf("WithProject did not create Projects/demo: %v", err)
	}

	drawers, err := h.Vault.ListDrawers("demo", "general", "general")
	if err != nil {
		t.Fatalf("ListDrawers: %v", err)
	}
	if len(drawers) != 2 {
		t.Fatalf("ListDrawers returned %d drawers, want 2: %+v", len(drawers), drawers)
	}
	contents := map[string]bool{}
	for _, d := range drawers {
		contents[d.Content] = true
	}
	if !contents["first drawer"] || !contents["second drawer"] {
		t.Errorf("ListDrawers missing an expected content: %+v", drawers)
	}

	resumeFile, err := h.Vault.ResumeFile("demo")
	if err != nil {
		t.Fatalf("ResumeFile: %v", err)
	}
	body, err := os.ReadFile(resumeFile)
	if err != nil {
		t.Fatalf("read resume.md: %v", err)
	}
	if !strings.Contains(string(body), "seeded") {
		t.Errorf("resume.md missing seeded content: %s", body)
	}
}

// TestWithDrawerOutMatchesReadBack proves WithDrawerOut's out parameter is
// populated with exactly what a ListDrawers read-back shows — not merely "no
// error" — for both a single WithDrawerOut and a WithDrawer/WithDrawerOut mix
// targeting the same room.
func TestWithDrawerOutMatchesReadBack(t *testing.T) {
	var out storage.Drawer
	h := New(t, WithDrawerOut("proj", "wing-a", "testing", "captured content", "facts", "2026-01-01T10:00:00Z", &out))

	drawers, err := h.Vault.ListDrawers("proj", "wing-a", "testing")
	if err != nil {
		t.Fatalf("ListDrawers: %v", err)
	}
	if len(drawers) != 1 {
		t.Fatalf("ListDrawers returned %d drawers, want 1: %+v", len(drawers), drawers)
	}
	want := drawers[0]
	if out.ID != want.ID || out.Content != want.Content || out.Hall != want.Hall || out.FiledAt != want.FiledAt {
		t.Errorf("WithDrawerOut out = %+v, want %+v (from ListDrawers)", out, want)
	}
	if out.ID != storage.DrawerID("wing-a", "captured content") {
		t.Errorf("WithDrawerOut out.ID = %q, want storage.DrawerID's own value %q", out.ID, storage.DrawerID("wing-a", "captured content"))
	}
}

// TestDrawerIDStableAcrossRoomsViaWithDrawerOut ports
// TestIntegrationDrawerIDStableAcrossRooms's assertion onto WithDrawerOut: the
// same content filed into two different rooms of the same wing gets the same
// drawer ID, and each batch (one room per WithDrawerOut call, since they
// target different rooms) still flushes correctly.
func TestDrawerIDStableAcrossRoomsViaWithDrawerOut(t *testing.T) {
	var d1, d2 storage.Drawer
	New(t,
		WithDrawerOut("proj", "wing-a", "testing", "shared content here", "facts", "2026-01-01T10:00:00Z", &d1),
		WithDrawerOut("proj", "wing-a", "debugging", "shared content here", "facts", "2026-01-01T10:00:00Z", &d2),
	)
	if d1.ID != d2.ID {
		t.Errorf("drawer IDs should be identical across rooms: %q vs %q", d1.ID, d2.ID)
	}
}

// AppendDrawersCallCount returns how many times AppendDrawers has been invoked
// through this harness's Seed/New calls so far. Count, not wall-clock time, is
// the observable — see doc/TESTING.md's TestIntegration_HandshakeDoesNotConstructEmbedder
// for the precedent this follows. It exists to catch a regression where seeding
// reverts from one batched AppendDrawers call per room group to one AppendDrawer
// (singular) call per drawer — the exact shape that makes seeding O(N²).
//
// Deliberately declared here rather than in harness.go: this package's own
// tests are its only caller today, and a _test.go declaration falls outside
// internal/sourceaudit's uninvoked-function gate entirely (that gate only
// inspects non-test declarations and non-test call sites), rather than
// needing a baseline.json exemption for a genuinely test-only reader.
func (h *TestHarness) AppendDrawersCallCount() int32 { return h.appendDrawersCalls.Load() }

// TestSeedBatchesManyDrawerOptionsIntoOneRoom is the regression test for the
// task's core requirement: a multi-room^Wmulti-option batch of
// WithDrawers/WithDrawer/WithDrawerOut calls targeting the SAME
// (project, wing, room) must all land — none dropped, none duplicated — from
// a single flushed AppendDrawers call, whether contributed via one WithDrawers
// call carrying many specs or many separate WithDrawer/WithDrawerOut calls.
// TestHarness.AppendDrawersCallCount now instruments the call directly, so the
// count below is a real call-counter assertion, not just an indirect proof
// via the batched result's correctness.
//
// WHAT BREAKS IT: reverting flushDrawers from one storage.Vault.AppendDrawers
// call per (project, wing, room) group to a loop calling storage.Vault.
// AppendDrawer (the singular n=1 wrapper) once per drawer — the exact shape
// that makes seeding O(N²), since AppendDrawer re-scans the whole room file
// for dedup on every call.
func TestSeedBatchesManyDrawerOptionsIntoOneRoom(t *testing.T) {
	specs := make([]DrawerSpec, 0, 30)
	for i := range 30 {
		specs = append(specs, DrawerSpec{
			Content:    "bulk drawer " + string(rune('a'+i)),
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-01-01T10:00:00Z",
		})
	}

	opts := []SeedOption{WithDrawers("bulk", "general", "general", specs...)}
	var captured storage.Drawer
	for i := range 19 {
		opts = append(opts, WithDrawer("bulk", "general", "general", "single drawer "+string(rune('a'+i)), "facts", "2026-01-01T10:00:00Z"))
	}
	opts = append(opts, WithDrawerOut("bulk", "general", "general", "captured single drawer", "facts", "2026-01-01T10:00:00Z", &captured))

	h := New(t, opts...)

	drawers, err := h.Vault.ListDrawers("bulk", "general", "general")
	if err != nil {
		t.Fatalf("ListDrawers: %v", err)
	}
	const want = 30 + 19 + 1
	if len(drawers) != want {
		t.Fatalf("ListDrawers returned %d drawers, want %d (30 batched + 19 singular + 1 captured)", len(drawers), want)
	}
	if captured.Content != "captured single drawer" {
		t.Errorf("captured drawer content = %q, want %q", captured.Content, "captured single drawer")
	}

	if got := h.AppendDrawersCallCount(); got != 1 {
		t.Fatalf("flushDrawers made %d AppendDrawers calls for one (project,wing,room) group, want 1 "+
			"— seeding has regressed from a single batched call to one call per drawer, which makes it O(N²)", got)
	}
}

// TestSeedAppendDrawersCallCountMatchesGroupCount is the NON-VACUITY companion
// to TestSeedBatchesManyDrawerOptionsIntoOneRoom: that test alone cannot rule
// out a counter that is trivially always 1 regardless of how many distinct
// (project, wing, room) groups are seeded. Seeding into THREE distinct groups
// in one New(t, ...) call and asserting the count is exactly 3 — not 1 (one
// call overall) and not the option count (one call per option) — proves the
// counter tracks flushed groups specifically.
func TestSeedAppendDrawersCallCountMatchesGroupCount(t *testing.T) {
	h := New(t,
		WithDrawers("proj-a", "general", "general",
			DrawerSpec{Content: "a1", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T10:00:00Z"},
			DrawerSpec{Content: "a2", Hall: "facts", SourceType: "manual", FiledAt: "2026-01-01T10:00:00Z"},
		),
		WithDrawer("proj-b", "general", "general", "b1", "facts", "2026-01-01T10:00:00Z"),
		WithDrawer("proj-b", "general", "general", "b2", "facts", "2026-01-01T10:00:00Z"),
		WithDrawer("proj-b", "general", "general", "b3", "facts", "2026-01-01T10:00:00Z"),
		WithDrawer("proj-c", "dev", "go", "c1", "facts", "2026-01-01T10:00:00Z"),
	)

	if got := h.AppendDrawersCallCount(); got != 3 {
		t.Fatalf("AppendDrawersCallCount() = %d, want 3 (one call per distinct (project,wing,room) group: "+
			"proj-a/general/general, proj-b/general/general, proj-c/dev/go) — either grouping broke (would "+
			"read >3) or batching broke (would read 6, one per option/drawer)", got)
	}

	for _, group := range []struct{ project, wing, room string }{
		{"proj-a", "general", "general"},
		{"proj-b", "general", "general"},
		{"proj-c", "dev", "go"},
	} {
		drawers, err := h.Vault.ListDrawers(group.project, group.wing, group.room)
		if err != nil {
			t.Fatalf("ListDrawers(%s/%s/%s): %v", group.project, group.wing, group.room, err)
		}
		if len(drawers) == 0 {
			t.Fatalf("ListDrawers(%s/%s/%s) returned no drawers; seeding did not actually land", group.project, group.wing, group.room)
		}
	}
}

// TestWithMemorySeedsProjectMemoryDirectly proves WithMemory writes directly
// into the vault's own project memory store — read back via
// Vault.ListMemories, not just "no error" — both for its no-args canonical
// fixture and for caller-supplied MemoryFileSpecs.
func TestWithMemorySeedsProjectMemoryDirectly(t *testing.T) {
	h := New(t, WithMemory("mem-fixture-proj"))

	mems, err := h.Vault.ListMemories("mem-fixture-proj", 0)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	if len(mems) != 3 {
		t.Fatalf("ListMemories returned %d memories, want 3 (the canonical fixture's typed files): %+v", len(mems), mems)
	}

	h2 := New(t, WithMemory("mem-custom-proj", MemoryFileSpec{
		Name: "custom.md",
		Content: `---
name: Custom memory
description: A parameterized memory file.
metadata:
  type: reference
---

Custom body content.
`,
	}))
	meta, body, err := h2.Vault.ReadMemory("mem-custom-proj", "custom.md")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if meta.Name != "Custom memory" || meta.Type != "reference" {
		t.Errorf("ReadMemory meta = %+v", meta)
	}
	if !strings.Contains(body, "Custom body content.") {
		t.Errorf("ReadMemory body = %q", body)
	}
}

// TestWithSessionWritesViaStorageDirectLane proves WithSession's storage-lane
// contract: a session written through it is on disk and indexed, read back
// via ListDrawers on the room the transcript's content should classify into,
// consistent with a real capture.WriteSession call.
func TestWithSessionWritesViaStorageDirectLane(t *testing.T) {
	h := New(t, WithSession(capture.SessionParams{
		Project: "session-proj",
		Summary: "Seeded directly via the storage lane.",
	}))

	sessions, err := h.Vault.ListSessions("session-proj", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions returned %d sessions, want 1: %+v", len(sessions), sessions)
	}
}

// TestWithCapturedSessionUsesMCPLaneAndReturnsResult proves WithCapturedSession
// is the MCP lane (not aliased with WithSession) and that its out parameter
// carries the same raw result text h.CallTool itself returns.
func TestWithCapturedSessionUsesMCPLaneAndReturnsResult(t *testing.T) {
	var raw string
	h := New(t, WithCapturedSession(map[string]any{
		"project": "captured-proj",
		"summary": "Seeded via the MCP lane.",
	}, &raw))

	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("unmarshal WithCapturedSession's out: %v (raw=%s)", err, raw)
	}
	if result.Status != "ok" {
		t.Fatalf("status = %q, want ok (raw=%s)", result.Status, raw)
	}

	sessions, err := h.Vault.ListSessions("captured-proj", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("ListSessions returned %d sessions, want 1: %+v", len(sessions), sessions)
	}
}

// TestNewReproducesResumeLostUpdateBug is the task's acceptance signal: the
// resume lost-update bug (iteration 179, commit 577c989) reproduced in under
// 10 lines of test code using New(t, opts...), collapsing what
// resume_cas_test.go's TestIntegration_UpdateResumeStaleWriteRefused needed a
// newHarness + registerAllTools + initMCP + WriteResume sequence for into one
// call.
func TestNewReproducesResumeLostUpdateBug(t *testing.T) {
	const project = "demo"
	h := New(t, WithResume(project, "# Resume: demo\n\n## Project History\n\nhistory\n"))

	var readA struct {
		Content string `json:"content"`
		Sha256  string `json:"sha256"`
	}
	json.Unmarshal([]byte(h.CallTool(t, "vp_vault_read", map[string]any{"path": "Projects/demo/resume.md"})), &readA)

	h.CallTool(t, "vp_vault_edit", map[string]any{
		"path": "Projects/demo/resume.md", "old_string": "## Project History",
		"new_string": "### landed\n\n## Project History", "expected_sha256": readA.Sha256,
	})

	_, isErr := h.CallToolRaw(t, "vp_update_resume", map[string]any{
		"project": project, "content": readA.Content + "\nstale\n", "expected_sha256": readA.Sha256,
	})
	if !isErr {
		t.Fatal("stale vp_update_resume succeeded; the lost-update bug reproduced")
	}
}
