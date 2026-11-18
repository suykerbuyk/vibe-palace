// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// claimFixture is a marked project root with a subdirectory, a vault holding
// the initialised project, and a derivable host session id.
type claimFixture struct {
	root, sub, vaultRoot, sessionID string
	tool                            func(cwd string) error
	noteCount                       func() int
}

func newClaimFixture(t *testing.T) *claimFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	vault := testSessionVault(t)
	testutil.InitProject(t, vault.Root, "test-proj")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".vibe-palace.toml"), []byte("[project]\nname = \"test-proj\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &claimFixture{root: root, sub: sub, vaultRoot: vault.Root, sessionID: "claim-dir-session"}
	stubHostSessionID(t, f.sessionID)
	f.tool = func(cwd string) error {
		params, _ := json.Marshal(map[string]any{"project": "test-proj", "summary": "capture from " + cwd, "cwd": cwd})
		_, err := CaptureSessionTool(vault, nil).Handler(context.Background(), json.RawMessage(params))
		return err
	}
	f.noteCount = func() int {
		dir, err := vault.SessionDir("test-proj")
		if err != nil {
			t.Fatal(err)
		}
		notes, _ := filepath.Glob(filepath.Join(dir, "*.md"))
		return len(notes)
	}
	return f
}

func (f *claimFixture) runHook(t *testing.T, cwd string) *hook.Result {
	t.Helper()
	res, err := hook.Run(context.Background(), hook.Payload{
		SessionID: f.sessionID, CWD: cwd, HookEventName: "SessionEnd",
	}, hook.RunOptions{VaultRoot: f.vaultRoot, ProjectSlug: "test-proj", VPVersion: "test"})
	if err != nil {
		t.Fatalf("hook.Run: %v", err)
	}
	return res
}

func (f *claimFixture) assertOneNoteAndRootPlacement(t *testing.T, res *hook.Result) {
	t.Helper()
	if !res.ClaimedSkip {
		t.Error("the SessionEnd hook did not see the capture's claim and captured the session AGAIN")
	}
	if n := f.noteCount(); n != 1 {
		t.Errorf("%d session notes, want exactly 1 (a duplicate note)", n)
	}
	if !hook.IsClaimed(filepath.Join(f.root, ".vibe-palace"), f.sessionID) {
		t.Error("the claim is not at the project root")
	}
	if _, err := os.Stat(filepath.Join(f.sub, ".vibe-palace")); err == nil {
		t.Error("a stray .vibe-palace was created in the subdirectory")
	}
}

// Direction A: capture from <root>/sub, hook from <root>.
func TestCaptureSessionClaimAgreesWithHook_CaptureInSubdir(t *testing.T) {
	f := newClaimFixture(t)
	if err := f.tool(f.sub); err != nil {
		t.Fatalf("capture: %v", err)
	}
	f.assertOneNoteAndRootPlacement(t, f.runHook(t, f.root))
}

// Direction B: capture from <root>, hook from <root>/sub.
func TestCaptureSessionClaimAgreesWithHook_HookInSubdir(t *testing.T) {
	f := newClaimFixture(t)
	if err := f.tool(f.root); err != nil {
		t.Fatalf("capture: %v", err)
	}
	f.assertOneNoteAndRootPlacement(t, f.runHook(t, f.sub))
}

// A nested marked tree (worktree / scratch copy) is its own root: capture and
// hook inside it agree on ITS claim dir, and the parent's gets nothing.
func TestCaptureSessionClaimNestedMarkerIsItsOwnRoot(t *testing.T) {
	f := newClaimFixture(t)
	nested := filepath.Join(f.root, "wt")
	inner := filepath.Join(nested, "a")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".vibe-palace.toml"), []byte("[project]\nname = \"test-proj\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.tool(inner); err != nil {
		t.Fatalf("capture: %v", err)
	}
	res := f.runHook(t, nested)
	if !res.ClaimedSkip || f.noteCount() != 1 {
		t.Errorf("ClaimedSkip=%v notes=%d, want true / 1", res.ClaimedSkip, f.noteCount())
	}
	if !hook.IsClaimed(filepath.Join(nested, ".vibe-palace"), f.sessionID) {
		t.Error("claim is not at the nested root")
	}
	if hook.IsClaimed(filepath.Join(f.root, ".vibe-palace"), f.sessionID) {
		t.Error("the nested tree's claim leaked into the parent root")
	}
}
