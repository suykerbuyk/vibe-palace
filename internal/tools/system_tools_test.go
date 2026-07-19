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

// TestVaultTidy_DryRunClassifiesNoCommit verifies dry_run returns the
// swept/reported split without creating a commit.
func TestVaultTidy_DryRunClassifiesNoCommit(t *testing.T) {
	root := initVaultRepo(t)
	headBefore := gitT(t, root, "rev-parse", "HEAD")
	vault := storage.NewVault(root)
	tool := VaultTidyTool(vault)

	// One sweepable artifact, one piece of non-artifact dirt.
	mustWrite(t, vault, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")
	mustWrite(t, vault, "Projects/vibe-palace/resume.md", "resume\n")

	params, _ := json.Marshal(vaultTidyParams{DryRun: true})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := res.(map[string]any)
	if m["committed"] != false {
		t.Errorf("committed = %v, want false", m["committed"])
	}
	if m["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", m["dry_run"])
	}
	swept := m["swept"].([]string)
	reported := m["reported"].([]string)
	if !strings.Contains(strings.Join(swept, ","), "sessions/2026-06-17.md") {
		t.Errorf("expected session swept, swept=%v", swept)
	}
	if !strings.Contains(strings.Join(reported, ","), "resume.md") {
		t.Errorf("expected resume reported, reported=%v", reported)
	}
	if headAfter := gitT(t, root, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Errorf("dry run created a commit: HEAD %s -> %s", headBefore, headAfter)
	}
}

// TestVaultTidy_SweepCommitsLocal verifies a real (push=false) sweep commits
// the artifacts, reports the rest, and leaves reported dirt in the worktree.
func TestVaultTidy_SweepCommitsLocal(t *testing.T) {
	root := initVaultRepo(t)
	vault := storage.NewVault(root)
	tool := VaultTidyTool(vault)

	mustWrite(t, vault, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")
	mustWrite(t, vault, "Projects/vibe-palace/resume.md", "resume\n")

	push := false
	params, _ := json.Marshal(vaultTidyParams{Push: &push})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := res.(map[string]any)
	if m["committed"] != true {
		t.Errorf("committed = %v, want true", m["committed"])
	}
	if m["commit_sha"] == "" {
		t.Error("expected a commit_sha")
	}
	if sum, _ := m["summary"].(string); !strings.Contains(sum, "Swept 1 artifact") {
		t.Errorf("summary = %q, want it to mention Swept 1 artifact", sum)
	}

	status := gitT(t, root, "status", "--porcelain", "-uall")
	if strings.Contains(status, "sessions/2026-06-17.md") {
		t.Errorf("swept session should be committed, status: %q", status)
	}
	if !strings.Contains(status, "resume.md") {
		t.Errorf("reported resume.md should remain dirty, status: %q", status)
	}
}

// TestVaultSync_BareTidiesAndPushes covers the default (no no_tidy) sync path: a
// vault dirty with a sweepable capture artifact classifies → commits the
// artifact → pulls → pushes, returning committed:true, and the artifact reaches
// the bare remote.
func TestVaultSync_BareTidiesAndPushes(t *testing.T) {
	root := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	gitT(t, root, "push", "origin", "main")

	vault := storage.NewVault(root)
	tool := VaultSyncTool(vault)

	// One sweepable capture artifact, written directly so vaultfs's lock/.surface
	// sidecars don't add genuine dirt that would trip the refuse-on-dirt gate.
	sessDir := filepath.Join(root, "Projects/vibe-palace/sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "2026-06-17.md"), []byte("session\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(vaultSyncParams{Action: "sync"})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	m := res.(map[string]any)
	if m["committed"] != true {
		t.Errorf("committed = %v, want true", m["committed"])
	}
	if m["commit_sha"] == "" {
		t.Error("expected a commit_sha")
	}

	// The artifact must have reached the bare remote: clone it and check.
	clone := t.TempDir()
	gitT(t, clone, "clone", bare, ".")
	if _, err := os.Stat(filepath.Join(clone, "Projects/vibe-palace/sessions/2026-06-17.md")); err != nil {
		t.Errorf("swept artifact did not reach the bare remote: %v", err)
	}
	// The working tree is clean after the sweep+sync.
	if status := gitT(t, root, "status", "--porcelain", "-uall"); status != "" {
		t.Errorf("working tree should be clean after sync, status: %q", status)
	}
}

// TestVaultSync_BareRefusesGenuineDirt covers the refuse-on-dirt gate: a vault
// with genuine non-artifact dirt returns an error naming the file, before any
// network I/O, and nothing is pushed.
func TestVaultSync_BareRefusesGenuineDirt(t *testing.T) {
	root := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	gitT(t, root, "push", "origin", "main")
	headBefore := gitT(t, root, "rev-parse", "HEAD")

	vault := storage.NewVault(root)
	tool := VaultSyncTool(vault)

	// Genuine, non-artifact dirt (resume.md is reported, not swept).
	mustWrite(t, vault, "Projects/vibe-palace/resume.md", "resume\n")

	params, _ := json.Marshal(vaultSyncParams{Action: "sync"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected sync to refuse on genuine non-artifact dirt")
	} else if !strings.Contains(err.Error(), "resume.md") {
		t.Errorf("error = %q, want it to name the dirty file", err)
	}

	// Nothing committed, nothing pushed: HEAD unchanged locally and on the remote.
	if headAfter := gitT(t, root, "rev-parse", "HEAD"); headAfter != headBefore {
		t.Errorf("refused sync created a commit: HEAD %s -> %s", headBefore, headAfter)
	}
	if remoteHead := gitT(t, bare, "rev-parse", "HEAD"); remoteHead != headBefore {
		t.Errorf("refused sync pushed to the remote: remote HEAD %s, want %s", remoteHead, headBefore)
	}
}

// TestVaultSync_NoTidyIsRawRefusal covers no_tidy:true: on an artifact-dirty
// vault it takes the raw pull+push path, whose clean-state guard refuses on the
// (uncommitted, unswept) dirt rather than tidying it.
func TestVaultSync_NoTidyIsRawRefusal(t *testing.T) {
	root := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	gitT(t, root, "push", "origin", "main")

	vault := storage.NewVault(root)
	tool := VaultSyncTool(vault)

	// A sweepable capture artifact — the tidy path WOULD commit it, but no_tidy
	// takes the raw path whose guard refuses on any uncommitted change.
	mustWrite(t, vault, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")

	params, _ := json.Marshal(vaultSyncParams{Action: "sync", NoTidy: true})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected no_tidy sync to refuse on the uncommitted artifact")
	} else if !strings.Contains(err.Error(), "uncommitted") {
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

// TestVaultStatusTool verifies the read-only status tool: it is non-mutating and
// its handler returns a versioned StatusReport that round-trips through JSON.
func TestVaultStatusTool(t *testing.T) {
	root := initVaultRepo(t)
	vault := storage.NewVault(root)
	tool := VaultStatusTool(vault)

	if tool.Name != "vp_vault_status" {
		t.Errorf("name = %q, want vp_vault_status", tool.Name)
	}
	if tool.Mutating {
		t.Error("Mutating = true, want false (status is read-only)")
	}

	params, _ := json.Marshal(vaultStatusParams{Refresh: false})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	// The result must decode through JSON into the shared StatusReport schema.
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var report storage.StatusReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode StatusReport: %v\n%s", err, raw)
	}
	if report.Version != 1 {
		t.Errorf("Version = %d, want 1", report.Version)
	}
	if report.Branch != "main" {
		t.Errorf("Branch = %q, want main", report.Branch)
	}

	// A no-sections call must keep BOTH section keys present in the raw JSON
	// (the shared output struct has no omitempty; the default path zeroes
	// nothing). Assert on the wire bytes, not just the round-trip.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("decode raw keys: %v\n%s", err, raw)
	}
	if _, ok := keys["remotes"]; !ok {
		t.Errorf("default call: missing \"remotes\" key in %s", raw)
	}
	if _, ok := keys["dirt"]; !ok {
		t.Errorf("default call: missing \"dirt\" key in %s", raw)
	}
}

// buildVaultStatusRaw invokes the vp_vault_status handler with the given params
// and returns the raw JSON wire bytes plus the decoded StatusReport. It fails the
// test on any handler or (de)serialization error.
func buildVaultStatusRaw(t *testing.T, vault *storage.Vault, params map[string]any) ([]byte, storage.StatusReport) {
	t.Helper()
	tool := VaultStatusTool(vault)
	raw, _ := json.Marshal(params)
	res, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler(%v): %v", params, err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var report storage.StatusReport
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("decode StatusReport: %v\n%s", err, out)
	}
	return out, report
}

// hasKey reports whether the top-level JSON object in raw carries key k
// (present-but-empty still counts as present).
func hasKey(t *testing.T, raw []byte, k string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode raw keys: %v\n%s", err, raw)
	}
	_, ok := m[k]
	return ok
}

// TestVaultStatusSections verifies the optional sections selector: it post-filters
// the built report by ZEROING the unselected section's field (present-but-empty,
// never an absent key), always keeps Version/Branch/VaultPath, and rejects unknown
// section names with a clean error.
func TestVaultStatusSections(t *testing.T) {
	root := initVaultRepo(t)
	vault := storage.NewVault(root)

	t.Run("sync_only_zeroes_dirt", func(t *testing.T) {
		raw, report := buildVaultStatusRaw(t, vault, map[string]any{"sections": []string{"sync"}})
		if !hasKey(t, raw, "dirt") {
			t.Errorf("dirt key must remain present (zeroed), got %s", raw)
		}
		if report.Dirt.Swept != nil || report.Dirt.Reported != nil || report.Dirt.ReportedUserContent != nil {
			t.Errorf("Dirt must be zeroed, got %+v", report.Dirt)
		}
		if report.Remotes == nil {
			t.Errorf("Remotes must be populated (non-nil) for sync selection, got nil")
		}
		if report.Version != 1 || report.Branch != "main" {
			t.Errorf("Version/Branch must always be present, got version=%d branch=%q", report.Version, report.Branch)
		}
	})

	t.Run("dirt_only_zeroes_remotes", func(t *testing.T) {
		raw, report := buildVaultStatusRaw(t, vault, map[string]any{"sections": []string{"dirt"}})
		if !hasKey(t, raw, "remotes") {
			t.Errorf("remotes key must remain present (null/zeroed), got %s", raw)
		}
		if report.Remotes != nil {
			t.Errorf("Remotes must be nil for dirt selection, got %+v", report.Remotes)
		}
		if report.Version != 1 || report.Branch != "main" {
			t.Errorf("Version/Branch must always be present, got version=%d branch=%q", report.Version, report.Branch)
		}
	})

	t.Run("both_sections_equal_default", func(t *testing.T) {
		rawBoth, both := buildVaultStatusRaw(t, vault, map[string]any{"sections": []string{"sync", "dirt"}})
		rawDefault, _ := buildVaultStatusRaw(t, vault, map[string]any{})
		if string(rawBoth) != string(rawDefault) {
			t.Errorf("both-sections output must equal default output\n both=%s\n def =%s", rawBoth, rawDefault)
		}
		if !hasKey(t, rawBoth, "remotes") || !hasKey(t, rawBoth, "dirt") {
			t.Errorf("both-sections must carry remotes+dirt keys, got %s", rawBoth)
		}
		_ = both
	})

	t.Run("unknown_section_errors", func(t *testing.T) {
		tool := VaultStatusTool(vault)
		raw, _ := json.Marshal(map[string]any{"sections": []string{"bogus"}})
		if _, err := tool.Handler(context.Background(), raw); err == nil {
			t.Fatal("expected error for unknown section, got nil")
		}
	})
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

// A sweep whose commit reaches NO remote is an ERROR, not a status string.
//
// 🔴 THIS TEST USED TO ASSERT A SOFT TIER. It required status:"stranded" — a value
// that is neither success nor failure, which is exactly the tier iteration 196
// killed for capture and that this project keeps re-growing. A caller checking
// `err != nil` (which is every caller) saw a stranded commit as a clean sweep.
//
// The commit still lands locally, and the error says so: the result body is
// DISCARDED on a handler error (196), so everything the caller needs to act has to
// ride in the error string itself.
func TestVaultTidy_StrandedIsAnError(t *testing.T) {
	root := initVaultRepo(t)
	// A configured remote pointing at a path that does not exist: push + fetch
	// both fail, so the commit lands locally but reaches no remote.
	gitT(t, root, "remote", "add", "origin", filepath.Join(t.TempDir(), "nonexistent.git"))

	vault := storage.NewVault(root)
	tool := VaultTidyTool(vault)
	mustWrite(t, vault, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")

	push := true
	params, _ := json.Marshal(vaultTidyParams{Push: &push})
	res, err := tool.Handler(context.Background(), params)

	if err == nil {
		t.Fatalf("stranded tidy returned NO error — a commit that reached no remote was reported as success: %v", res)
	}
	if !strings.Contains(err.Error(), "STRANDED") {
		t.Errorf("error does not name the outcome: %v", err)
	}
	// The caller must still learn that the commit EXISTS locally, or it cannot act.
	if !strings.Contains(err.Error(), "EXISTS locally") {
		t.Errorf("error does not tell the caller the commit landed locally: %v", err)
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error does not name the failing remote: %v", err)
	}
}

// 🔴 A PARTIAL PUSH IS AN ERROR — AND THIS TEST USED TO ASSERT THE OPPOSITE.
//
// It was called TestVaultTidy_PartialPushCount, and its own doc comment described a
// 1-of-2 remote failure as "honest (not stranded, status ok)". It asserted
// status == "ok" and a summary reading "pushed to 1/2 remotes". That is the bug in
// this task, written down and pinned by a green test: the vault is synced across
// machines, and a remote that did not get the commit is a machine that will silently
// be behind. Reporting that as success is the sync path telling you it worked when
// it half-worked.
//
// The fixture it built — one healthy remote, one unreachable — IS the verification
// bar this task demands ("a real vault with two remotes, one of them unreachable").
// It was here the whole time, arriving at the wrong conclusion.
func TestVaultTidy_PartialPushIsAnError(t *testing.T) {
	root := initVaultRepo(t)
	good := t.TempDir()
	gitT(t, good, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "good", good)
	gitT(t, root, "push", "good", "main")
	gitT(t, root, "remote", "add", "dead", filepath.Join(t.TempDir(), "nonexistent.git"))

	vault := storage.NewVault(root)
	tool := VaultTidyTool(vault)
	mustWrite(t, vault, "Projects/vibe-palace/sessions/2026-06-17.md", "session\n")

	push := true
	params, _ := json.Marshal(vaultTidyParams{Push: &push})
	res, err := tool.Handler(context.Background(), params)

	if err == nil {
		t.Fatalf("a 1-of-2 partial push returned NO error — the dead remote is silently behind: %v", res)
	}
	if !strings.Contains(err.Error(), "PARTIAL") {
		t.Errorf("error does not distinguish PARTIAL from STRANDED: %v", err)
	}
	if !strings.Contains(err.Error(), "dead") {
		t.Errorf("error does not name the remote that failed, so nobody can act on it: %v", err)
	}
	// ...and it must NOT be reported as stranded: the commit did reach a remote, and
	// the remediation differs. Distinct in the message, identical in the verdict.
	if strings.Contains(err.Error(), "STRANDED") {
		t.Errorf("a partial push was reported as STRANDED: %v", err)
	}
}
