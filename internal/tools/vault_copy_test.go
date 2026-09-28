// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func vcGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s", args, out)
	}
	return strings.TrimSpace(string(out))
}

func vcRepo(t *testing.T, files map[string]string) (dir, bare string) {
	t.Helper()
	dir, bare = t.TempDir(), t.TempDir()
	vcGit(t, bare, "init", "-q", "--bare", "-b", "main")
	vcGit(t, bare, "config", "uploadpack.allowFilter", "true")
	vcGit(t, dir, "init", "-q", "-b", "main")
	vcGit(t, dir, "config", "user.email", "t@example.com")
	vcGit(t, dir, "config", "user.name", "T")
	files[".vibe-palace/vault.toml"] = fmt.Sprintf("format = %d\n", surface.RequiredDataFormat)
	for rel, c := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vcGit(t, dir, "add", "-A")
	vcGit(t, dir, "commit", "-q", "-m", "seed")
	vcGit(t, dir, "remote", "add", "origin", "file://"+bare)
	vcGit(t, dir, "push", "-q", "origin", "main")
	return dir, bare
}

// vp_vault_copy: plan is admitted to a stale binary and writes nothing; apply
// is not; the tool is never served read-only; plan returns the digest and the
// real-run line; apply copies into the server's own vault.
func TestVaultCopyTool_PlanThenApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	_, srcBare := vcRepo(t, map[string]string{"Projects/p/resume.md": "p\n"})
	v, _ := vcRepo(t, map[string]string{"Projects/resident/resume.md": "r\n"})

	tool := VaultCopyTool(storage.NewVault(v))
	if !tool.ReadOnlyWhen(json.RawMessage(`{"action":"plan"}`)) || tool.ReadOnlyWhen(json.RawMessage(`{"action":"apply"}`)) {
		t.Fatal("only plan may be admitted to a stale binary")
	}
	if slices.Contains(ReadOnlyServeToolNames, tool.Name) {
		t.Fatal("vp_vault_copy writes; it must never be served read-only")
	}

	params := fmt.Sprintf(`{"action":"plan","slugs":["p"],"from":%q}`, "file://"+srcBare)
	out, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	plan := out.(*vaultCopyResult)
	if !plan.Complete || plan.Plan.Digest == "" || !strings.Contains(plan.Plan.Command, "--expect "+plan.Plan.Digest) {
		t.Fatalf("plan result %+v", plan)
	}
	if _, err := os.Lstat(filepath.Join(v, "Projects/p")); err == nil {
		t.Fatal("plan wrote to the vault")
	}

	params = fmt.Sprintf(`{"action":"apply","slugs":["p"],"from":%q,"at":%q,"expect":%q}`, "file://"+srcBare, plan.Plan.At, plan.Plan.Digest)
	out, err = tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	if res := out.(*vaultCopyResult); res.Commit != vcGit(t, v, "rev-parse", "HEAD") {
		t.Fatalf("apply result %+v", res)
	}

	// A refusal is a caller error; an unknown action too.
	if _, err := tool.Handler(context.Background(), json.RawMessage(params)); !apperr.IsCaller(err) {
		t.Fatalf("a second apply: err = %v, want a caller error", err)
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"action":"purge","slugs":["p"],"from":"x"}`)); !apperr.IsCaller(err) {
		t.Fatalf("unknown action: err = %v", err)
	}
}
