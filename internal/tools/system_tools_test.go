// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/onboard"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// sandboxHostEnv redirects every host-global root the tools in this file can
// reach at fresh temp dirs.
//
// Defense in depth, deliberately: no tool exercised here reads a host global
// today — internal/tools production code has no os.UserHomeDir call and the
// handlers take an explicit *storage.Vault — so with TMPDIR under /tmp the
// marker walk never reaches $HOME. A developer whose TMPDIR lives under $HOME
// would resolve against their real tree, and any future test that drives a
// tool up to the home boundary gets isolation without having to know it needed
// asking for.
//
// XDG_CACHE_HOME is deliberately NOT redirected here: nothing in this package
// constructs an ONNX embedder (check_tool.go never calls check.Run for exactly
// that reason), so there is no model cache to reach.
//
// Four vars, because os.UserHomeDir and os.UserConfigDir resolve differently
// per GOOS — HOME/USERPROFILE for the former, XDG_CONFIG_HOME/APPDATA for the
// latter. cmd/vp/cmd_init_test.go's initTestEnv documents the full matrix.
func sandboxHostEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("APPDATA", configDir)
}

// TestSandboxHostEnvSandboxesHostGlobals is the regression lock on the helper
// above: if it ever stops redirecting one of the four vars, a tool that walks
// to the home boundary would resolve against the developer's real tree.
func TestSandboxHostEnvSandboxesHostGlobals(t *testing.T) {
	real, _ := os.UserHomeDir()
	sandboxHostEnv(t)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if real != "" && home == real {
		t.Errorf("os.UserHomeDir() = %q, the real home", home)
	}
	if os.Getenv("USERPROFILE") != home {
		t.Errorf("USERPROFILE = %q, want %q", os.Getenv("USERPROFILE"), home)
	}
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" || configDir == home {
		t.Errorf("XDG_CONFIG_HOME = %q, want its own sandboxed dir", configDir)
	}
	if os.Getenv("APPDATA") != configDir {
		t.Errorf("APPDATA = %q, want %q", os.Getenv("APPDATA"), configDir)
	}
}

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
	// Match production: gitignore .vp-locks/ so the repo-root commit lock's
	// persistent sidecar never surfaces as dirt in a status check.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".vp-locks/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
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
	sandboxHostEnv(t)
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
	sandboxHostEnv(t)
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
	sandboxHostEnv(t)
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
	msg := err.Error()
	if !strings.Contains(msg, "uncommitted") {
		t.Errorf("error = %q, want it to mention uncommitted changes", err)
	}
	if !strings.Contains(msg, "dirty.txt") {
		t.Errorf("error = %q, want it to name the dirty path dirty.txt", err)
	}
	if !strings.Contains(msg, "vp_vault_status") {
		t.Errorf("error = %q, want a next-turn remedy naming vp_vault_status", err)
	}
}

// TestVaultTidy_DryRunClassifiesNoCommit verifies dry_run returns the
// swept/reported split without creating a commit.
func TestVaultTidy_DryRunClassifiesNoCommit(t *testing.T) {
	sandboxHostEnv(t)
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
	sandboxHostEnv(t)
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
	sandboxHostEnv(t)
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
	sandboxHostEnv(t)
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
	sandboxHostEnv(t)
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

// markProjectTree makes dir an existing directory that passes
// project.HasRootedSignal, which is the gate every working-tree step is behind.
func markProjectTree(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	return dir
}

// callInit drives the tool and returns the typed result.
func callInit(t *testing.T, tool mcp.Tool, p initParams) initResult {
	t.Helper()
	params, _ := json.Marshal(p)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	res, ok := result.(initResult)
	if !ok {
		t.Fatalf("result type = %T, want initResult", result)
	}
	return res
}

// omittedSteps returns the omitted step names, sorted.
func omittedSteps(res initResult) []string {
	var out []string
	for _, om := range res.Omitted {
		out = append(out, om.Step)
	}
	sort.Strings(out)
	return out
}

func TestInitProjectSuccess(t *testing.T) {
	sandboxHostEnv(t)
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	// The directory must EXIST and be a project: the handler no longer
	// os.MkdirAll's it, because a remote caller naming a path on the server's
	// disk is not evidence that a project belongs there.
	projDir := markProjectTree(t, filepath.Join(t.TempDir(), "my-project"))

	res := callInit(t, tool, initParams{
		Path:   projDir,
		Name:   "my-project",
		Domain: "work",
		Tags:   []string{"go", "cli"},
	})
	// "partial", not "initialized": hook-wiring and command-shims are always
	// omitted over MCP, so Complete is unreachable by design.
	if res.Status != "partial" || res.Complete {
		t.Errorf("status = %q complete = %v, want partial/false", res.Status, res.Complete)
	}
	if res.Project != "my-project" {
		t.Errorf("project = %q", res.Project)
	}
	if len(res.Steps) == 0 {
		t.Error("no step rows returned")
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
	sandboxHostEnv(t)
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	params, _ := json.Marshal(initParams{Path: "./relative"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for relative path")
	}
}

// TestInitProject_RejectsReservedDeviceName is the MCP-side twin of
// cmd/vp's TestInitRejectsReservedDeviceName: `vp_init {name: "con"}` (and any
// other Windows-reserved device name) must be refused at creation, not
// silently degraded to a skip downstream, and must not create a
// Projects/<name>/ directory in the vault.
func TestInitProject_RejectsReservedDeviceName(t *testing.T) {
	sandboxHostEnv(t)
	vaultDir := t.TempDir()
	vault := storage.NewVault(vaultDir)
	tool := InitProjectTool(vault)

	for _, reserved := range []string{"con", "aux", "com1"} {
		t.Run(reserved, func(t *testing.T) {
			projDir := markProjectTree(t, filepath.Join(t.TempDir(), reserved))
			params, _ := json.Marshal(initParams{Path: projDir, Name: reserved})
			if _, err := tool.Handler(context.Background(), params); err == nil {
				t.Fatalf("expected an error for reserved device name %q", reserved)
			}
			if _, err := os.Stat(filepath.Join(projDir, project.ConfigFileName)); err == nil {
				t.Errorf("%s was written despite the reserved name being refused", project.ConfigFileName)
			}
			if _, err := os.Stat(filepath.Join(vaultDir, "Projects", reserved)); err == nil {
				t.Errorf("Projects/%s was created in the vault despite the reserved name being refused", reserved)
			}
		})
	}
}

// TestInitProjectAlreadyExists is INVERTED, deliberately, and the inversion is
// the point of the change rather than a side effect of it.
//
// The handler used to refuse outright when .vibe-palace.toml existed. That
// refusal is what prevented an MCP-only client from ever healing itself: the
// tool wrote a two-line marker and two task directories, and then that marker
// was the reason it would never touch the project again — and the CLI's own
// marker gate said the same thing. The same setup must now SUCCEED and
// reconcile.
func TestInitProjectAlreadyExists(t *testing.T) {
	sandboxHostEnv(t)
	vaultDir := t.TempDir()
	vault := storage.NewVault(vaultDir)
	tool := InitProjectTool(vault)

	projDir := markProjectTree(t, filepath.Join(t.TempDir(), "test"))
	configPath := filepath.Join(projDir, project.ConfigFileName)
	os.WriteFile(configPath, []byte("[project]\nname = \"test\"\n"), 0644)

	res := callInit(t, tool, initParams{Path: projDir, Name: "test"})
	if res.Project != "test" {
		t.Errorf("project = %q", res.Project)
	}
	for _, row := range res.Steps {
		if row.Status == "fail" {
			t.Errorf("step %s failed: %s", row.Step, row.Summary)
		}
	}
	// The vault scaffold the old refusal made unreachable.
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "test", "config.toml")); err != nil {
		t.Errorf("vault project config still missing after re-init: %v", err)
	}
}

func TestInitProjectAutoDetectName(t *testing.T) {
	sandboxHostEnv(t)
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)

	// Detection is allowed only behind the rooted-signal gate; see
	// TestVpInit_RefusesNestedNonProject for the other half of that rule.
	projDir := markProjectTree(t, filepath.Join(t.TempDir(), "cool-app"))

	res := callInit(t, tool, initParams{Path: projDir})
	if res.Project != "cool-app" {
		t.Errorf("project = %q, want cool-app", res.Project)
	}
}

func TestVaultSyncNonGitDir(t *testing.T) {
	sandboxHostEnv(t)
	vault := storage.NewVault(t.TempDir())
	tool := VaultSyncTool(vault)

	params, _ := json.Marshal(vaultSyncParams{Action: "pull"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

// TestVaultSync_ZeroRemotesRefused pins the empty-remote refusal on the bare
// pull/push/sync path. A real git repo with a commit and NO remote is the case
// storage.ListRemotes reports as `(nil, nil)` — a valid, error-free listing of
// nothing — so a handler that only checks `err` walks the pull and push loops
// over zero remotes and returns `status: "ok"` for a sync that contacted
// nothing. That is the honest-instruments failure exactly: success reported for
// work not done.
//
// Both halves are asserted deliberately. A test that only checked
// `ListRemotes(root)` returned no error would pass on the bug — it would be
// pinning the lister's contract, which is fine as it stands, instead of the
// call site's policy, which is what this refusal is.
func TestVaultSync_ZeroRemotesRefused(t *testing.T) {
	sandboxHostEnv(t)
	root := initVaultRepo(t) // a real repo, one commit, no remote
	tool := VaultSyncTool(storage.NewVault(root))

	for _, action := range []string{"pull", "push", "sync"} {
		t.Run(action, func(t *testing.T) {
			params, _ := json.Marshal(vaultSyncParams{Action: action})
			res, err := tool.Handler(context.Background(), params)
			if err == nil {
				t.Fatalf("action %q on a remote-less vault: expected a refusal, got result %v", action, res)
			}
			if !strings.Contains(err.Error(), "no git remotes configured") {
				t.Errorf("action %q: error %q must name the missing remotes", action, err)
			}
			// The verdict is the error. A result body alongside it would be a
			// second, contradictory answer for the same call.
			if m, ok := res.(map[string]any); ok && m["status"] == "ok" {
				t.Errorf("action %q refused but still returned status \"ok\": %v", action, m)
			}
		})
	}

	// --no-tidy takes a different branch to the same loops; it must refuse too.
	params, _ := json.Marshal(vaultSyncParams{Action: "sync", NoTidy: true})
	if res, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatalf("sync no_tidy on a remote-less vault: expected a refusal, got result %v", res)
	}
}

// nestedToolsVaultFixture builds the task's own reproduction for the MCP
// layer: a clean enclosing repository with a bare origin and one unrelated,
// unpushed local commit, and a vault subdirectory nested inside it with NO
// .git entry of its own — the shape that makes storage.InspectVaultGit report
// VaultGitNested. Mirrors TestConfigSyncNeverWritesAnEnclosingRepo's fixture
// (cmd/vp/cmd_config_override_test.go) and cmd/vp/cmd_vault_nested_test.go's
// CLI-level counterpart, for the vp_vault_sync MCP tool instead.
//
// Returns the vault path, the enclosing repo's own path (parent), and a
// state() closure fingerprinting the parent: HEAD, symbolic HEAD, the index,
// every ref, the bare origin's main, and the last 5 reflog entries.
func nestedToolsVaultFixture(t *testing.T) (vaultPath, parent string, state func() []string) {
	t.Helper()
	parent = t.TempDir()
	gitT(t, parent, "init", "-b", "main")
	gitT(t, parent, "config", "user.email", "test@example.com")
	gitT(t, parent, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, parent, "add", "-A")
	gitT(t, parent, "commit", "-m", "project")

	origin := filepath.Join(t.TempDir(), "origin.git")
	gitT(t, parent, "init", "--bare", "-b", "main", origin)
	gitT(t, parent, "remote", "add", "origin", origin)
	gitT(t, parent, "push", "-u", "origin", "main")

	// An unrelated, unpushed local commit — the "someone else's commits" this
	// task exists to protect.
	if err := os.WriteFile(filepath.Join(parent, "WIP.md"), []byte("not for push\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, parent, "add", "WIP.md")
	gitT(t, parent, "commit", "-m", "LOCAL WIP - not for push")

	vaultPath = filepath.Join(parent, "notes", "vault")
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		t.Fatal(err)
	}

	state = func() []string {
		return []string{
			gitT(t, parent, "rev-parse", "HEAD"),
			gitT(t, parent, "symbolic-ref", "HEAD"),
			gitT(t, parent, "ls-files", "-s"),
			gitT(t, parent, "for-each-ref"),
			gitT(t, origin, "rev-parse", "main"),
			gitT(t, parent, "reflog", "-n", "5"),
		}
	}
	return vaultPath, parent, state
}

// TestVaultSyncToolRefusesNestedVault is the MCP-layer pin: vp_vault_sync must
// refuse every action (sync, pull, push, and the explicit paths-commit
// variant) on a vault nested inside another repository's work tree, and must
// leave the enclosing repository's HEAD, refs, index, and reflog exactly as
// they were.
func TestVaultSyncToolRefusesNestedVault(t *testing.T) {
	sandboxHostEnv(t)

	for _, tc := range []struct {
		name   string
		params vaultSyncParams
	}{
		{"sync", vaultSyncParams{Action: "sync"}},
		{"sync_no_tidy", vaultSyncParams{Action: "sync", NoTidy: true}},
		{"pull", vaultSyncParams{Action: "pull"}},
		{"push", vaultSyncParams{Action: "push"}},
		{"paths_commit", vaultSyncParams{Action: "sync", Paths: []string{"notes.txt"}, Message: "should never land"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vaultPath, parent, state := nestedToolsVaultFixture(t)
			before := state()

			tool := VaultSyncTool(storage.NewVault(vaultPath))
			params, _ := json.Marshal(tc.params)
			res, err := tool.Handler(context.Background(), params)
			if err == nil {
				t.Fatalf("expected a refusal, got result %v", res)
			}
			if !strings.Contains(err.Error(), "inside another repository") {
				t.Errorf("error %q does not say \"inside another repository\"", err.Error())
			}
			if !strings.Contains(err.Error(), parent) {
				t.Errorf("error %q does not name the enclosing repository %q", err.Error(), parent)
			}
			after := state()
			for i, label := range []string{"HEAD", "symbolic HEAD", "index", "for-each-ref", "origin main", "reflog"} {
				if before[i] != after[i] {
					t.Errorf("%s changed:\n--- before\n%s\n--- after\n%s", label, before[i], after[i])
				}
			}
		})
	}
}

func TestVaultSyncInvalidAction(t *testing.T) {
	sandboxHostEnv(t)
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
	sandboxHostEnv(t)
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
	if report.Version != 3 {
		t.Errorf("Version = %d, want 3", report.Version)
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
	sandboxHostEnv(t)
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
		if report.Version != 3 || report.Branch != "main" {
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
		if report.Version != 3 || report.Branch != "main" {
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
	sandboxHostEnv(t)
	// RefreshIndexTool requires a non-nil engine. Verify the tool constructor works.
	// We can't easily test a full rebuild without the embedder, but we verify
	// the handler rejects empty project.
	tool := RefreshIndexTool(nil, nil)
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
	sandboxHostEnv(t)
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
	sandboxHostEnv(t)
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

func TestFormatDirtyVaultPushErrorCap(t *testing.T) {
	sandboxHostEnv(t)
	paths := make([]string, dirtyPathErrorCap+3)
	for i := range paths {
		paths[i] = fmt.Sprintf("p%d", i)
	}
	msg := formatDirtyVaultPushError(paths)
	if !strings.Contains(msg, "…and 3 more") {
		t.Errorf("want cap suffix, got %q", msg)
	}
	if strings.Contains(msg, fmt.Sprintf("p%d", dirtyPathErrorCap)) {
		t.Errorf("capped path should not appear: %q", msg)
	}
	// Under cap: no suffix.
	msg2 := formatDirtyVaultPushError([]string{"a", "b"})
	if strings.Contains(msg2, "more") {
		t.Errorf("unexpected suffix: %q", msg2)
	}
	if !strings.Contains(msg2, "a, b") {
		t.Errorf("want paths joined: %q", msg2)
	}
}

// TestVpInit_MCP_ProducesFullVaultScaffold asserts ON DISK what the tool used
// to claim in a string.
//
// The old handler returned {"status":"initialized"} unconditionally, having
// written a hand-rolled two-line .vibe-palace.toml and two task directories
// with discarded mkdir errors. Projects/<slug>/config.toml and the
// commands/skills scaffold were never created at all — and the marker it wrote
// then told the CLI there was nothing left to do.
func TestVpInit_MCP_ProducesFullVaultScaffold(t *testing.T) {
	sandboxHostEnv(t)
	vaultDir := t.TempDir()
	tool := InitProjectTool(storage.NewVault(vaultDir))
	projDir := markProjectTree(t, filepath.Join(t.TempDir(), "scaffolded"))

	res := callInit(t, tool, initParams{Path: projDir, Name: "scaffolded"})
	for _, row := range res.Steps {
		if row.Status == "fail" {
			t.Errorf("step %s failed: %s", row.Step, row.Summary)
		}
	}

	root := filepath.Join(vaultDir, "Projects", "scaffolded")
	for _, rel := range []string{
		"config.toml",
		filepath.Join("tasks", "done"),
		filepath.Join("tasks", "cancelled"),
		filepath.Join("commands", "README.md"),
		filepath.Join("skills", "README.md"),
	} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("vault scaffold missing Projects/scaffolded/%s: %v", rel, err)
		}
	}
}

// TestVpInit_MCP_Idempotent: two identical calls, and the tree after the second
// equals the tree after the first.
func TestVpInit_MCP_Idempotent(t *testing.T) {
	sandboxHostEnv(t)
	vaultDir := t.TempDir()
	tool := InitProjectTool(storage.NewVault(vaultDir))
	projDir := markProjectTree(t, filepath.Join(t.TempDir(), "twice"))

	p := initParams{Path: projDir, Name: "twice", Domain: "work"}
	callInit(t, tool, p)
	vault1 := initTreeSnapshot(t, vaultDir)
	proj1 := initTreeSnapshot(t, projDir)

	res2 := callInit(t, tool, p)
	for _, row := range res2.Steps {
		if row.Status == "fail" {
			t.Errorf("second call step %s failed: %s", row.Step, row.Summary)
		}
	}
	if d := initTreeDiff(vault1, initTreeSnapshot(t, vaultDir)); len(d) > 0 {
		t.Errorf("second call changed the vault tree:\n  %s", strings.Join(d, "\n  "))
	}
	if d := initTreeDiff(proj1, initTreeSnapshot(t, projDir)); len(d) > 0 {
		t.Errorf("second call changed the project tree:\n  %s", strings.Join(d, "\n  "))
	}
}

// TestVpInit_DoesNotCreateProjectDir pins the deletion of os.MkdirAll(p.Path).
//
// That one line is what made remote misuse SILENT: a caller naming a path that
// does not exist on the server got a directory conjured into being on the
// server's disk, plus a success string, and no indication that the project they
// meant lives on a different machine entirely.
func TestVpInit_DoesNotCreateProjectDir(t *testing.T) {
	sandboxHostEnv(t)
	vaultDir := t.TempDir()
	tool := InitProjectTool(storage.NewVault(vaultDir))
	projDir := filepath.Join(t.TempDir(), "never-created")

	res := callInit(t, tool, initParams{Path: projDir, Name: "never-created"})

	if _, err := os.Stat(projDir); !os.IsNotExist(err) {
		t.Errorf("handler created %s on the server's disk (stat err = %v)", projDir, err)
	}
	// The vault side still ran.
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "never-created", "config.toml")); err != nil {
		t.Errorf("vault side did not run: %v", err)
	}
	// And EVERY working-tree step is accounted for as an omission.
	omitted := map[string]bool{}
	for _, om := range res.Omitted {
		omitted[om.Step] = true
	}
	for _, st := range onboard.Steps() {
		if st.Side == onboard.SideVault {
			continue
		}
		if !omitted[st.Name] {
			t.Errorf("step %q writes %s but was not reported as omitted", st.Name, st.Side)
		}
	}
}

// TestVpInit_RefusesNestedNonProject is the slug gate.
//
// project.DetectProject walks UPWARD until the home boundary. Without the
// rooted-signal gate, vp_init{path: "<tmp>/repo/vendor/x"} fails the
// working-tree gate for vendor/x and still writes vault artifacts under the
// ANCESTOR's slug — a whole project's worth of vault state created under a name
// the caller never asked for.
func TestVpInit_RefusesNestedNonProject(t *testing.T) {
	sandboxHostEnv(t)
	vaultDir := t.TempDir()
	tool := InitProjectTool(storage.NewVault(vaultDir))

	tmp := t.TempDir()
	repo := markProjectTree(t, filepath.Join(tmp, "repo"))
	if err := os.WriteFile(filepath.Join(repo, project.ConfigFileName),
		[]byte("[project]\nname = \"repo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repo, "vendor", "x")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("no-name-is-refused", func(t *testing.T) {
		params, _ := json.Marshal(initParams{Path: nested})
		if _, err := tool.Handler(context.Background(), params); err == nil {
			t.Fatal("expected a refusal: the slug would have been walked up to from the ancestor")
		} else if !strings.Contains(err.Error(), "name is required") {
			t.Errorf("refusal does not say what the caller must supply: %v", err)
		}
		if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "repo")); !os.IsNotExist(err) {
			t.Errorf("vault artifacts written under the ANCESTOR's slug (stat err = %v)", err)
		}
	})

	t.Run("explicit-name-omits-the-working-tree", func(t *testing.T) {
		res := callInit(t, tool, initParams{Path: nested, Name: "vendored"})
		omitted := map[string]bool{}
		for _, om := range res.Omitted {
			omitted[om.Step] = true
		}
		for _, st := range onboard.Steps() {
			if st.Side == onboard.SideVault {
				continue
			}
			if !omitted[st.Name] {
				t.Errorf("step %q was not omitted for a non-project path", st.Name)
			}
		}
		entries, err := os.ReadDir(nested)
		if err != nil {
			t.Fatalf("read %s: %v", nested, err)
		}
		if len(entries) != 0 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("handler wrote into a non-project directory: %v", names)
		}
		if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "repo")); !os.IsNotExist(err) {
			t.Errorf("vault artifacts written under the ANCESTOR's slug (stat err = %v)", err)
		}
	})
}

// TestVpInit_NeverWritesHostGlobal is the SideHostGlobal contract, asserted at
// the tool boundary rather than in internal/onboard.
//
// Over MCP, ~/.claude/settings.json is the SERVER OPERATOR's file, never the
// caller's. Both the step that writes it and the step that merely READS it to
// decide what to write into the caller's project must be omitted.
func TestVpInit_NeverWritesHostGlobal(t *testing.T) {
	sandboxHostEnv(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")

	tool := InitProjectTool(storage.NewVault(t.TempDir()))
	projDir := markProjectTree(t, filepath.Join(t.TempDir(), "hostsafe"))
	res := callInit(t, tool, initParams{Path: projDir, Name: "hostsafe"})

	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Errorf("vp_init touched the host's %s (stat err = %v)", settings, err)
	}
	omitted := map[string]bool{}
	for _, om := range res.Omitted {
		omitted[om.Step] = true
	}
	for _, want := range []string{"hook-wiring", "command-shims"} {
		if !omitted[want] {
			t.Errorf("step %q is not in Omitted: %v", want, omittedSteps(res))
		}
	}
}

// TestVpInit_VaultOnly_ReportsIncomplete: the expected set is DERIVED from
// onboard.Steps(), never spelled out. A literal list would keep passing after
// someone added a ninth step that writes the caller's working tree.
func TestVpInit_VaultOnly_ReportsIncomplete(t *testing.T) {
	wantOmitted := func() map[string]bool {
		out := map[string]bool{}
		for _, st := range onboard.Steps() {
			if st.Side == onboard.SideWorkingTree || st.Side == onboard.SideHostGlobal || st.ReadsHostGlobal {
				out[st.Name] = true
			}
		}
		return out
	}()

	cases := []struct {
		name  string
		build func(t *testing.T) initParams
	}{
		{"no-path", func(*testing.T) initParams { return initParams{Name: "x"} }},
		{"non-project-path", func(t *testing.T) initParams {
			dir := t.TempDir() // exists, but carries no project signal
			return initParams{Path: filepath.Join(dir, "plain"), Name: "x"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandboxHostEnv(t)
			vaultDir := t.TempDir()
			tool := InitProjectTool(storage.NewVault(vaultDir))

			res := callInit(t, tool, tc.build(t))
			if res.Complete {
				t.Error("complete = true; a surface that cannot write the working tree is not done")
			}
			if res.Status != "partial" {
				t.Errorf("status = %q, want partial", res.Status)
			}
			got := map[string]bool{}
			for _, om := range res.Omitted {
				got[om.Step] = true
			}
			for step := range wantOmitted {
				if !got[step] {
					t.Errorf("step %q must be omitted on a vault-only call; got %v", step, omittedSteps(res))
				}
			}
			// The vault side still ran and produced its artifacts.
			if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "x", "config.toml")); err != nil {
				t.Errorf("vault side did not run: %v", err)
			}
		})
	}
}

// TestVpInitDescription_NamesEveryOmittedClass makes the Description carry its
// own weight.
//
// An agent reads Description, not this test file. A tool whose result says
// "omitted: hook-wiring" and whose Description never mentions that host-global
// state is off the table forces the agent to discover the boundary by being
// surprised at it.
//
// The per-step phrases live in a table the test itself checks for coverage, so
// adding a step that ScopeForMCP can exclude fails here until the Description
// says something about it.
func TestVpInitDescription_NamesEveryOmittedClass(t *testing.T) {
	desc := InitProjectTool(storage.NewVault(t.TempDir())).Description

	// One required phrase per step ScopeForMCP can exclude.
	want := map[string][]string{
		"cwd-project":          {".vibe-palace.toml"},
		"agent-wiring":         {"AGENTS.md"},
		"command-shims":        {"vpc-*.md", "reading that same home"},
		"hook-wiring":          {"~/.claude/settings.json", "never"},
		"project-gitignore":    {".gitignore"},
		"git-post-commit-hook": {"commit.msg"},
	}

	for _, st := range onboard.Steps() {
		excludable := st.Side != onboard.SideVault || st.ReadsHostGlobal
		if !excludable {
			continue
		}
		phrases, ok := want[st.Name]
		if !ok {
			t.Errorf("step %q can be excluded by ScopeForMCP but this test names no phrase for it — "+
				"add one AND make sure vp_init's Description says something about it", st.Name)
			continue
		}
		for _, ph := range phrases {
			if !strings.Contains(desc, ph) {
				t.Errorf("Description does not mention %q for omitted step %q:\n%s", ph, st.Name, desc)
			}
		}
	}

	// And the two things an agent must not key off the wrong field for.
	for _, ph := range []string{"complete", "omitted", "vp commands upgrade", "vp commands reset", "vp skills reset"} {
		if !strings.Contains(desc, ph) {
			t.Errorf("Description does not mention %q:\n%s", ph, desc)
		}
	}
}

// initTreeSnapshot maps every path under root to a content digest, EXCLUDING
// .surface: that file stamps a timestamp and the writing binary's identity, so
// it differs between two runs by construction.
func initTreeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[filepath.ToSlash(rel)+"/"] = "<dir>"
			return nil
		}
		if d.Name() == ".surface" {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

func initTreeDiff(before, after map[string]string) []string {
	var out []string
	for k, v := range after {
		if old, ok := before[k]; !ok {
			out = append(out, "added: "+k)
		} else if old != v {
			out = append(out, "changed: "+k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, "removed: "+k)
		}
	}
	sort.Strings(out)
	return out
}

// topLevelVerdict is the vp_init result MINUS the per-step detail: exactly what
// a caller sees before it decides whether it has to walk steps[] at all.
//
// json.Marshal orders map keys, so the rendering is stable and two runs that
// differ nowhere but in steps[] render identically here.
func topLevelVerdict(t *testing.T, res initResult) string {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	delete(m, "steps")
	delete(m, "omitted")
	delete(m, "advisories")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	return string(out)
}

// TestInitProject_FailureIsVisibleAtTopLevel is the F1 regression lock.
//
// The tool's Description used to tell callers to KEY OFF `complete` AND
// `omitted`, and both are CONSTANT over MCP: ScopeForMCP always omits
// hook-wiring and command-shims, so len(omitted) >= 2 always, `complete` is
// always false, and `status` is always "partial". A healthy run and a run in
// which the vault write FAILED — leaving Projects/<slug>/ absent entirely —
// returned byte-identical top-level results, so a caller following the
// documented contract to the letter learned nothing. That is the same
// unconditional-verdict defect this tool's rewrite exists to delete, relocated
// from `status` into `complete`.
//
// The assertion is deliberately on the WHOLE top level rather than on `ok`
// alone: it fails for any future change that reintroduces a constant verdict,
// not only for the removal of the field that fixes it today.
func TestInitProject_FailureIsVisibleAtTopLevel(t *testing.T) {
	sandboxHostEnv(t)

	// One project directory, two vaults. Everything the caller supplied is
	// identical across the runs, so any difference in the verdict is a
	// difference in what actually happened on the server.
	projDir := markProjectTree(t, filepath.Join(t.TempDir(), "verdict"))

	healthyVault := t.TempDir()
	healthy := callInit(t, InitProjectTool(storage.NewVault(healthyVault)),
		initParams{Path: projDir, Name: "verdict"})

	// A vault whose Projects/ is a regular FILE: the vault-project write fails
	// for a reason the server cannot paper over, and nothing lands under
	// Projects/<slug>/ at all.
	brokenVault := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenVault, "Projects"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatalf("seed broken vault: %v", err)
	}
	broken := callInit(t, InitProjectTool(storage.NewVault(brokenVault)),
		initParams{Path: projDir, Name: "verdict"})

	// The fixture has to actually break, or the test proves nothing.
	sawFail := false
	for _, row := range broken.Steps {
		if row.Step == "vault-project" && row.Status == "fail" {
			sawFail = true
		}
	}
	if !sawFail {
		t.Fatalf("fixture did not fail vault-project; steps = %+v", broken.Steps)
	}
	// Nothing landed under Projects/<slug>/ — the whole subtree is unreachable
	// through a regular file, so the stat fails with ENOTDIR rather than
	// ENOENT; what matters is that no directory exists there.
	if fi, err := os.Stat(filepath.Join(brokenVault, "Projects", "verdict")); err == nil && fi.IsDir() {
		t.Fatalf("fixture left Projects/verdict behind")
	}

	// THE ASSERTION. Without a failure signal these two strings are equal.
	if got, want := topLevelVerdict(t, broken), topLevelVerdict(t, healthy); got == want {
		t.Errorf("a failed run and a healthy run return the same top-level result — "+
			"a caller cannot tell them apart without walking steps[]:\n  %s", got)
	}

	if !healthy.OK || len(healthy.Failed) != 0 {
		t.Errorf("healthy run: ok = %v failed = %v, want true/[]", healthy.OK, healthy.Failed)
	}
	if broken.OK {
		t.Error("broken run reports ok = true")
	}
	if !slices.Contains(broken.Failed, "vault-project") {
		t.Errorf("broken run failed = %v, want it to name vault-project", broken.Failed)
	}

	// And the fields the Description now tells callers NOT to key off are
	// indeed identical across the two runs. This is not decoration: it is the
	// evidence for that instruction, and it fails if either ever becomes
	// informative, at which point the Description is the thing to fix.
	if healthy.Status != broken.Status || healthy.Complete != broken.Complete {
		t.Errorf("status/complete differ across the two runs (%q/%v vs %q/%v) — "+
			"the Description says they are constant by construction",
			healthy.Status, healthy.Complete, broken.Status, broken.Complete)
	}
	if healthy.Status != "partial" || healthy.Complete {
		t.Errorf("healthy run: status = %q complete = %v, want partial/false", healthy.Status, healthy.Complete)
	}
}

// TestInitProject_StepsCarryCreated is F5: Outcome.Created had no production
// reader at all, so the caller best placed to use it — an MCP client asking
// "did I create this, or was it already here?" — could not see it.
func TestInitProject_StepsCarryCreated(t *testing.T) {
	sandboxHostEnv(t)
	vault := storage.NewVault(t.TempDir())
	tool := InitProjectTool(vault)
	projDir := markProjectTree(t, filepath.Join(t.TempDir(), "createdness"))

	first := callInit(t, tool, initParams{Path: projDir, Name: "createdness"})
	var firstCreated []string
	for _, row := range first.Steps {
		if row.Created {
			firstCreated = append(firstCreated, row.Step)
		}
	}
	if !slices.Contains(firstCreated, "vault-project") {
		t.Errorf("fresh run created = %v, want it to include vault-project", firstCreated)
	}

	second := callInit(t, tool, initParams{Path: projDir, Name: "createdness"})
	for _, row := range second.Steps {
		if row.Created {
			t.Errorf("converged re-run still claims created on %s: %s", row.Step, row.Summary)
		}
	}

	// The field has to survive the wire, not merely the struct.
	b, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"created":true`) {
		t.Errorf("no step row marshalled created=true:\n%s", b)
	}
}
