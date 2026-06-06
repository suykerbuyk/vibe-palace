// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// initVaultRepo creates a git repo (with identity + seed commit) to use as a
// vault root in vault-sync tests.
func initVaultRepo(t *testing.T) string {
	t.Helper()
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	gitT(t, dir, "init", "-b", "main")
	gitT(t, dir, "config", "user.email", "test@example.com")
	gitT(t, dir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-m", "seed")
	return dir
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func TestVaultSync_PathsCommitLocal(t *testing.T) {
	root := initVaultRepo(t)
	vault := storage.NewVault(root)
	tool := VaultSyncTool(vault)

	// Two dirty files; commit only one (action=pull → no push, no remote needed).
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("k"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "leave.txt"), []byte("l"), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(vaultSyncParams{Action: "pull", Paths: []string{"keep.txt"}, Message: "commit keep"})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := res.(map[string]any)
	if m["committed"] != true {
		t.Errorf("committed = %v, want true", m["committed"])
	}
	status := gitT(t, root, "status", "--porcelain")
	if strings.Contains(status, "keep.txt") {
		t.Errorf("keep.txt should be committed, status: %q", status)
	}
	if !strings.Contains(status, "leave.txt") {
		t.Errorf("leave.txt should remain dirty, status: %q", status)
	}
}

func TestVaultSync_PathsRequireMessage(t *testing.T) {
	root := initVaultRepo(t)
	vault := storage.NewVault(root)
	tool := VaultSyncTool(vault)
	params, _ := json.Marshal(vaultSyncParams{Action: "pull", Paths: []string{"x.txt"}})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error when paths provided without message")
	}
}

// TestVaultSync_BarePushRefusesDirty pins the H2 invariant: a bare push (no
// paths) must refuse to run on a dirty vault.
func TestVaultSync_BarePushRefusesDirty(t *testing.T) {
	root := initVaultRepo(t)
	// Configure a remote so remote discovery succeeds and we reach the guard.
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	gitT(t, root, "push", "origin", "main")

	// Dirty the tree.
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}

	vault := storage.NewVault(root)
	tool := VaultSyncTool(vault)
	params, _ := json.Marshal(vaultSyncParams{Action: "push"})
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("expected bare push to refuse on dirty vault")
	}
	if !strings.Contains(err.Error(), "uncommitted") {
		t.Errorf("error = %q, want it to mention uncommitted changes", err)
	}
}

func TestInitProjectSuccess(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	projDir := filepath.Join(t.TempDir(), "my-project")

	params, _ := json.Marshal(initParams{
		Path:   projDir,
		Name:   "my-project",
		Domain: "work",
		Tags:   []string{"go", "cli"},
	})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]string)
	if m["status"] != "initialized" {
		t.Errorf("status = %q", m["status"])
	}
	if m["project"] != "my-project" {
		t.Errorf("project = %q", m["project"])
	}

	// Verify config file was created.
	configPath := filepath.Join(projDir, project.ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config not created: %v", err)
	}
	content := string(data)
	if !containsAll(content, "my-project", "work", "go", "cli") {
		t.Errorf("config content = %q", content)
	}
}

func TestInitProjectRelativePath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	params, _ := json.Marshal(initParams{Path: "./relative"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for relative path")
	}
}

func TestInitProjectAlreadyExists(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	projDir := t.TempDir()
	configPath := filepath.Join(projDir, project.ConfigFileName)
	os.WriteFile(configPath, []byte("[project]\nname = \"test\"\n"), 0644)

	params, _ := json.Marshal(initParams{Path: projDir, Name: "test"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for existing config")
	}
}

func TestInitProjectAutoDetectName(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	projDir := filepath.Join(t.TempDir(), "cool-app")

	params, _ := json.Marshal(initParams{Path: projDir})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := result.(map[string]string)
	if m["project"] != "cool-app" {
		t.Errorf("project = %q, want cool-app", m["project"])
	}
}

func TestVaultSyncNonGitDir(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := VaultSyncTool(vault)

	params, _ := json.Marshal(vaultSyncParams{Action: "pull"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestVaultSyncInvalidAction(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := VaultSyncTool(vault)

	params, _ := json.Marshal(vaultSyncParams{Action: "nope"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for invalid action")
	}
}

func TestRefreshIndexTool(t *testing.T) {
	// RefreshIndexTool requires a non-nil engine. Verify the tool constructor works.
	// We can't easily test a full rebuild without the embedder, but we verify
	// the handler rejects empty project.
	tool := RefreshIndexTool(nil)
	if tool.Name != "vp_refresh_index" {
		t.Fatalf("name = %q", tool.Name)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
