// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// callResumeRefs runs the handler and asserts a clean ResumeRefsResult back.
func callResumeRefs(t *testing.T, vault *storage.Vault, params map[string]any) ResumeRefsResult {
	t.Helper()
	tool := ResumeRefsTool(vault)
	raw, _ := json.Marshal(params)
	res, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out, ok := res.(ResumeRefsResult)
	if !ok {
		t.Fatalf("result type = %T, want ResumeRefsResult", res)
	}
	return out
}

// writeResume creates <vault>/Projects/<slug>/resume.md with the given body.
func writeResume(t *testing.T, vaultRoot, slug, body string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write resume: %v", err)
	}
}

func TestResumeRefsTool_NotMutating(t *testing.T) {
	if got := ResumeRefsTool(storage.NewVault(t.TempDir())).Mutating; got {
		t.Fatalf("vp_check_resume_refs must be read-only (Mutating=false), got %v", got)
	}
}

func TestResumeRefsTool_Name(t *testing.T) {
	if got := ResumeRefsTool(storage.NewVault(t.TempDir())).Name; got != "vp_check_resume_refs" {
		t.Errorf("tool name = %q, want vp_check_resume_refs", got)
	}
}

// A vault whose resume files are clean passes.
func TestResumeRefsTool_PassClean(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	writeResume(t, vault.Root, "tidy", "# tidy\n\nSee tasks/done/task-1.md.\n")

	out := callResumeRefs(t, vault, map[string]any{})

	if out.Status != "pass" {
		t.Errorf("status = %q, want pass", out.Status)
	}
	if len(out.Details) != 0 {
		t.Errorf("details = %v, want empty on pass", out.Details)
	}
}

// A resume committing host-local plan references is reported as info, with each
// offending path enumerated; the fenced reference is ignored.
func TestResumeRefsTool_InfoWithBreaches(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	body := "## State\n\n" +
		"plan: ~/.claude/plans/active.md\n" +
		"old:  /home/dev/.claude/plans/older.md\n\n" +
		"```\n~/.claude/plans/ignored.md\n```\n"
	writeResume(t, vault.Root, "refproj", body)

	out := callResumeRefs(t, vault, map[string]any{"project": "refproj"})

	if out.Status != "info" {
		t.Fatalf("status = %q, want info\n%+v", out.Status, out)
	}
	joined := strings.Join(out.Details, "\n")
	for _, want := range []string{"~/.claude/plans/active.md", "/home/dev/.claude/plans/older.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q: %v", want, out.Details)
		}
	}
	if strings.Contains(joined, "ignored.md") {
		t.Errorf("fenced reference must be ignored: %v", out.Details)
	}
}

// No vault configured → skip (non-halting), mirroring the check's own contract.
func TestResumeRefsTool_SkipEmptyVault(t *testing.T) {
	out := callResumeRefs(t, storage.NewVault(""), map[string]any{})
	if out.Status != "skip" {
		t.Errorf("status = %q, want skip for empty vault path", out.Status)
	}
}
