// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestCheckConfig(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		tmp := t.TempDir()
		configDir := filepath.Join(tmp, "vibe-palace")
		os.MkdirAll(configDir, 0755)

		vaultDir := filepath.Join(tmp, "vault")
		os.MkdirAll(vaultDir, 0755)

		os.WriteFile(filepath.Join(configDir, "config.toml"),
			[]byte("vault_path = "+`"`+vaultDir+`"`+"\n"), 0644)

		t.Setenv("XDG_CONFIG_HOME", tmp)

		configPath, vaultPath, r := CheckConfig()
		if r.Status != Pass {
			t.Fatalf("expected Pass, got %v: %s", r.Status, r.Summary)
		}
		if configPath == "" {
			t.Error("configPath should not be empty")
		}
		if vaultPath == "" {
			t.Error("vaultPath should not be empty")
		}
		if !strings.Contains(r.Summary, "config.toml") {
			t.Errorf("summary should contain config path, got %q", r.Summary)
		}
		if len(r.Details) == 0 {
			t.Error("expected details with vault_path")
		}
	})

	t.Run("missing config", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmp)

		_, _, r := CheckConfig()
		if r.Status != Fail {
			t.Fatalf("expected Fail, got %v: %s", r.Status, r.Summary)
		}
		if r.Err == nil {
			t.Error("expected non-nil error")
		}
	})

	t.Run("reports global source by default", func(t *testing.T) {
		tmp := t.TempDir()
		home := filepath.Join(tmp, "home")
		os.MkdirAll(home, 0o755)
		configDir := filepath.Join(home, ".config", "vibe-palace")
		os.MkdirAll(configDir, 0o755)
		vaultDir := filepath.Join(tmp, "vault")
		os.MkdirAll(vaultDir, 0o755)
		os.WriteFile(filepath.Join(configDir, "config.toml"),
			[]byte(`vault_path = "`+vaultDir+`"`+"\n"), 0o644)

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

		projectDir := filepath.Join(home, "code", "proj")
		os.MkdirAll(projectDir, 0o755)
		t.Chdir(projectDir)

		_, vaultPath, r := CheckConfig()
		if r.Status != Pass {
			t.Fatalf("expected Pass, got %v: %s", r.Status, r.Summary)
		}
		if vaultPath != vaultDir {
			t.Errorf("vaultPath = %q, want %q", vaultPath, vaultDir)
		}
		foundSource := false
		for _, d := range r.Details {
			if strings.HasPrefix(d, "vault_path source = global:") {
				foundSource = true
			}
		}
		if !foundSource {
			t.Errorf("details missing global source line: %v", r.Details)
		}
	})

	t.Run("reports cwd source when cwd override present", func(t *testing.T) {
		tmp := t.TempDir()
		home := filepath.Join(tmp, "home")
		os.MkdirAll(home, 0o755)
		configDir := filepath.Join(home, ".config", "vibe-palace")
		os.MkdirAll(configDir, 0o755)
		globalVault := filepath.Join(tmp, "global-vault")
		os.MkdirAll(globalVault, 0o755)
		os.WriteFile(filepath.Join(configDir, "config.toml"),
			[]byte(`vault_path = "`+globalVault+`"`+"\n"), 0o644)

		altVault := filepath.Join(tmp, "alt-vault")
		os.MkdirAll(altVault, 0o755)
		projectDir := filepath.Join(home, "code", "proj")
		os.MkdirAll(projectDir, 0o755)
		os.WriteFile(filepath.Join(projectDir, ".vibe-palace.toml"),
			[]byte(`vault_path = "`+altVault+`"`+"\n"), 0o644)

		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Chdir(projectDir)

		_, vaultPath, r := CheckConfig()
		if r.Status != Pass {
			t.Fatalf("expected Pass, got %v: %s", r.Status, r.Summary)
		}
		if vaultPath != altVault {
			t.Errorf("vaultPath = %q, want %q", vaultPath, altVault)
		}
		foundSource := false
		for _, d := range r.Details {
			if strings.HasPrefix(d, "vault_path source = cwd:") {
				foundSource = true
			}
		}
		if !foundSource {
			t.Errorf("details missing cwd source line: %v", r.Details)
		}
	})
}

func TestCheckVault(t *testing.T) {
	t.Run("existing directory", func(t *testing.T) {
		tmp := t.TempDir()
		r := CheckVault(tmp)
		if r.Status != Pass {
			t.Fatalf("expected Pass, got %v: %s", r.Status, r.Summary)
		}
		if !strings.Contains(r.Summary, "exists") {
			t.Errorf("summary should mention exists, got %q", r.Summary)
		}
	})

	t.Run("missing directory", func(t *testing.T) {
		r := CheckVault("/nonexistent/path/that/does/not/exist")
		if r.Status != Fail {
			t.Fatalf("expected Fail, got %v: %s", r.Status, r.Summary)
		}
	})

	t.Run("file not directory", func(t *testing.T) {
		tmp := t.TempDir()
		f := filepath.Join(tmp, "not-a-dir")
		os.WriteFile(f, []byte("hi"), 0644)

		r := CheckVault(f)
		if r.Status != Fail {
			t.Fatalf("expected Fail, got %v: %s", r.Status, r.Summary)
		}
		if !strings.Contains(r.Summary, "not a directory") {
			t.Errorf("summary should mention not a directory, got %q", r.Summary)
		}
	})
}

func TestCheckSettings(t *testing.T) {
	tmp := t.TempDir()
	v := storage.NewVault(tmp)

	cfg, r := CheckSettings(v)
	if r.Status != Pass {
		t.Fatalf("expected Pass, got %v: %s", r.Status, r.Summary)
	}
	if cfg.EmbedderModel == "" {
		t.Error("expected non-empty embedder model from defaults")
	}
	if !strings.Contains(r.Summary, "model=") {
		t.Errorf("summary should contain model=, got %q", r.Summary)
	}
	if !strings.Contains(r.Summary, "search_limit=") {
		t.Errorf("summary should contain search_limit=, got %q", r.Summary)
	}
}

func TestCheckEmbedder(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedder test in short mode (requires ONNX model)")
	}

	tmp := t.TempDir()
	v := storage.NewVault(tmp)

	cfg, r := CheckSettings(v)
	if r.Status != Pass {
		t.Fatalf("settings failed: %s", r.Summary)
	}

	r = CheckEmbedder(cfg, v, "")
	if r.Status != Pass {
		t.Fatalf("expected Pass, got %v: %s", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "dimensions") {
		t.Errorf("summary should mention dimensions, got %q", r.Summary)
	}
}

func TestCheckConfigMissing_ActionableMessage(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	_, _, r := CheckConfig()
	if r.Status != Fail {
		t.Fatalf("expected Fail, got %v", r.Status)
	}
	found := false
	for _, d := range r.Details {
		if strings.Contains(d, "vp init") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Details should contain 'vp init', got %v", r.Details)
	}
}

func TestCheckVaultMissing_ActionableMessage(t *testing.T) {
	r := CheckVault("/nonexistent/path")
	if r.Status != Fail {
		t.Fatalf("expected Fail, got %v", r.Status)
	}
	found := false
	for _, d := range r.Details {
		if strings.Contains(d, "vp init") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Details should contain 'vp init', got %v", r.Details)
	}
}

func TestCheckGit_Disabled(t *testing.T) {
	r := CheckGit(t.TempDir(), false)
	if r.Status != Info {
		t.Errorf("expected Info, got %v", r.Status)
	}
	if !strings.Contains(r.Summary, "disabled") {
		t.Errorf("summary should mention disabled, got %q", r.Summary)
	}
}

func TestCheckGit_NotARepo(t *testing.T) {
	r := CheckGit(t.TempDir(), true)
	if r.Status != Info {
		t.Errorf("expected Info, got %v", r.Status)
	}
	if !strings.Contains(r.Summary, "not a git repository") {
		t.Errorf("summary should mention not a repo, got %q", r.Summary)
	}
}

func TestCheckGit_ValidRepo(t *testing.T) {
	dir := t.TempDir()
	if err := storage.GitInit(dir); err != nil {
		t.Fatalf("git init: %v", err)
	}

	r := CheckGit(dir, true)
	// No remotes → should be Info "no remotes configured"
	if r.Status != Info {
		t.Errorf("expected Info for repo with no remotes, got %v: %s", r.Status, r.Summary)
	}
}

func TestCheckGit_WithRemotes(t *testing.T) {
	dir := t.TempDir()
	if err := storage.GitInit(dir); err != nil {
		t.Fatalf("git init: %v", err)
	}
	// Add a fake remote.
	cmd := exec.Command("git", "-C", dir, "remote", "add", "origin", "https://example.com/repo.git")
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	r := CheckGit(dir, true)
	if r.Status != Pass {
		t.Errorf("expected Pass with remote, got %v: %s", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "origin") {
		t.Errorf("summary should list remote name, got %q", r.Summary)
	}
}

func TestCheckConfigStaleness_UpToDate(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "vibe-palace")
	os.MkdirAll(configDir, 0o755)
	configPath := filepath.Join(configDir, "config.toml")

	// Write a full config with all canonical keys.
	content := `vault_path = "/tmp"
git_enabled = true
http_port = 7423
log_level = "info"

[meta]
version_major = 1
version_minor = 0
kind = "global"

[embedder]
model = "test"
max_sequence_length = 256
batch_size = 32

[search]
default_limit = 10
structural_boost_wing = 0.12
structural_boost_hall = 0.24
structural_boost_room = 0.34

[chunker]
max_chars = 800
overlap = 100
`
	os.WriteFile(configPath, []byte(content), 0o644)

	r := CheckConfigStaleness(configPath)
	if r.Status != Pass {
		t.Errorf("expected Pass for up-to-date config, got %v: %s", r.Status, r.Summary)
	}
}

func TestCheckConfigStaleness_Missing(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "vibe-palace")
	os.MkdirAll(configDir, 0o755)
	configPath := filepath.Join(configDir, "config.toml")

	// Write config missing git_enabled.
	content := `vault_path = "/tmp"
http_port = 7423
`
	os.WriteFile(configPath, []byte(content), 0o644)

	r := CheckConfigStaleness(configPath)
	if r.Status != Info {
		t.Errorf("expected Info for outdated config, got %v: %s", r.Status, r.Summary)
	}
	found := false
	for _, d := range r.Details {
		if strings.Contains(d, "vp config upgrade") {
			found = true
		}
	}
	if !found {
		t.Error("details should mention 'vp config upgrade'")
	}
}

func TestCheckConfigStaleness_FileNotFound(t *testing.T) {
	r := CheckConfigStaleness("/nonexistent/config.toml")
	if r.Status != Skip {
		t.Errorf("expected Skip for missing file, got %v: %s", r.Status, r.Summary)
	}
}

func TestCheckProject(t *testing.T) {
	t.Run("with project file", func(t *testing.T) {
		tmp := t.TempDir()
		os.WriteFile(filepath.Join(tmp, ".vibe-palace.toml"), []byte(`
[project]
name = "test-proj"
`), 0644)

		t.Chdir(tmp)
		r := CheckProject()
		if r.Status != Info {
			t.Fatalf("expected Info, got %v: %s", r.Status, r.Summary)
		}
		if !strings.Contains(r.Summary, "test-proj") {
			t.Errorf("summary should contain project name, got %q", r.Summary)
		}
	})

	t.Run("no project file fallback to basename", func(t *testing.T) {
		tmp := t.TempDir()
		t.Chdir(tmp)

		r := CheckProject()
		if r.Status != Info {
			t.Fatalf("expected Info, got %v: %s", r.Status, r.Summary)
		}
		// DetectProject falls back to directory basename when no .vibe-palace.toml exists.
		// It should still return Info status (never fail).
		if r.Summary == "" {
			t.Error("expected non-empty summary")
		}
	})
}

func TestPrint(t *testing.T) {
	t.Run("all pass", func(t *testing.T) {
		results := []Result{
			{Name: "Config", Status: Pass, Summary: "/path/to/config.toml"},
			{Name: "Vault", Status: Pass, Summary: "/path/to/vault (exists)"},
			{Name: "Settings", Status: Pass, Summary: "model=test  search_limit=10"},
			{Name: "Embedder", Status: Pass, Summary: "ONNX loaded, 384 dimensions"},
			{Name: "Project", Status: Info, Summary: "my-project (from .vibe-palace.toml)"},
		}

		var buf bytes.Buffer
		failures := Print(&buf, "0.1.0-dev", results)
		out := buf.String()

		if failures != 0 {
			t.Errorf("expected 0 failures, got %d", failures)
		}
		if !strings.Contains(out, "All checks passed.") {
			t.Errorf("expected 'All checks passed.' in output:\n%s", out)
		}
		if !strings.Contains(out, "[pass]") {
			t.Error("expected [pass] tags in output")
		}
		if !strings.Contains(out, "[info]") {
			t.Error("expected [info] tag in output")
		}
		if !strings.Contains(out, "0.1.0-dev") {
			t.Error("expected version in output")
		}
	})

	t.Run("with failure", func(t *testing.T) {
		results := []Result{
			{Name: "Config", Status: Pass, Summary: "/path/to/config.toml"},
			{Name: "Vault", Status: Fail, Summary: "/path/nonexistent does not exist"},
			{Name: "Settings", Status: Skip},
			{Name: "Embedder", Status: Skip},
		}

		var buf bytes.Buffer
		failures := Print(&buf, "0.1.0-dev", results)
		out := buf.String()

		if failures != 1 {
			t.Errorf("expected 1 failure, got %d", failures)
		}
		if !strings.Contains(out, "[FAIL]") {
			t.Error("expected [FAIL] in output")
		}
		if !strings.Contains(out, "[skip]") {
			t.Error("expected [skip] in output")
		}
		if !strings.Contains(out, "1 check failed.") {
			t.Errorf("expected '1 check failed.' in output:\n%s", out)
		}
	})

	t.Run("with details", func(t *testing.T) {
		results := []Result{
			{
				Name:    "Config",
				Status:  Pass,
				Summary: "/path/to/config.toml",
				Details: []string{"vault_path = /path/to/vault"},
			},
		}

		var buf bytes.Buffer
		Print(&buf, "0.1.0-dev", results)
		out := buf.String()

		if !strings.Contains(out, "vault_path = /path/to/vault") {
			t.Errorf("expected details in output:\n%s", out)
		}
	})

	t.Run("multiple failures", func(t *testing.T) {
		results := []Result{
			{Name: "A", Status: Fail, Summary: "fail 1"},
			{Name: "B", Status: Fail, Summary: "fail 2"},
		}

		var buf bytes.Buffer
		failures := Print(&buf, "0.1.0-dev", results)
		if failures != 2 {
			t.Errorf("expected 2 failures, got %d", failures)
		}
		if !strings.Contains(buf.String(), "2 checks failed.") {
			t.Errorf("expected '2 checks failed.' in output:\n%s", buf.String())
		}
	})
}

func TestProgressLine(t *testing.T) {
	var buf bytes.Buffer
	ProgressLine(&buf, "Embedder", "loading model...")
	out := buf.String()
	if !strings.Contains(out, "[ .. ]") {
		t.Errorf("expected [ .. ] tag, got %q", out)
	}
	if !strings.Contains(out, "Embedder") {
		t.Errorf("expected Embedder name, got %q", out)
	}
}

// TestCheckConfigAt_ParityWithCwd verifies that CheckConfigAt(cwd) produces
// the same result as CheckConfig() run from that cwd.
func TestCheckConfigAt_ParityWithCwd(t *testing.T) {
	tmp := t.TempDir()
	configDir := filepath.Join(tmp, "vibe-palace")
	os.MkdirAll(configDir, 0755)
	vaultDir := filepath.Join(tmp, "vault")
	os.MkdirAll(vaultDir, 0755)
	os.WriteFile(filepath.Join(configDir, "config.toml"),
		[]byte("vault_path = "+`"`+vaultDir+`"`+"\n"), 0644)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	projectDir := filepath.Join(tmp, "proj")
	os.MkdirAll(projectDir, 0755)
	t.Chdir(projectDir)

	cfgCwd, vaultCwd, rCwd := CheckConfig()
	cfgAt, vaultAt, rAt := CheckConfigAt(projectDir)

	if cfgCwd != cfgAt {
		t.Errorf("configPath mismatch: cwd=%q at=%q", cfgCwd, cfgAt)
	}
	if vaultCwd != vaultAt {
		t.Errorf("vaultPath mismatch: cwd=%q at=%q", vaultCwd, vaultAt)
	}
	if rCwd.Status != rAt.Status || rCwd.Summary != rAt.Summary {
		t.Errorf("result mismatch: cwd=%+v at=%+v", rCwd, rAt)
	}
}

// TestCheckConfigAt_CwdOverride asserts the explicit-root variant honors a
// .vibe-palace.toml vault_path override found at root (not os.Getwd()).
func TestCheckConfigAt_CwdOverride(t *testing.T) {
	tmp := t.TempDir()
	// Global config points at one vault.
	configDir := filepath.Join(tmp, "vibe-palace")
	os.MkdirAll(configDir, 0755)
	globalVault := filepath.Join(tmp, "global-vault")
	os.MkdirAll(globalVault, 0755)
	os.WriteFile(filepath.Join(configDir, "config.toml"),
		[]byte("vault_path = "+`"`+globalVault+`"`+"\n"), 0644)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	// Project dir with a cwd-local override pointing at a different vault.
	projectDir := filepath.Join(tmp, "proj")
	os.MkdirAll(projectDir, 0755)
	projectVault := filepath.Join(tmp, "proj-vault")
	os.MkdirAll(projectVault, 0755)
	os.WriteFile(filepath.Join(projectDir, ".vibe-palace.toml"),
		[]byte("vault_path = "+`"`+projectVault+`"`+"\n"), 0644)

	// Run from an unrelated cwd — At(projectDir) should still pick up the override.
	t.Chdir(tmp)
	_, vaultPath, r := CheckConfigAt(projectDir)
	if r.Status != Pass {
		t.Fatalf("expected Pass, got %v: %s", r.Status, r.Summary)
	}
	if vaultPath != projectVault {
		t.Errorf("expected project-local vault override %q, got %q",
			projectVault, vaultPath)
	}
}

// TestCheckProjectAt_ParityWithCwd verifies that CheckProjectAt(cwd) produces
// the same result as CheckProject() run from that cwd.
func TestCheckProjectAt_ParityWithCwd(t *testing.T) {
	t.Run("with project file", func(t *testing.T) {
		tmp := t.TempDir()
		os.WriteFile(filepath.Join(tmp, ".vibe-palace.toml"),
			[]byte("[project]\nname = \"test-proj\"\n"), 0644)
		t.Chdir(tmp)
		rCwd := CheckProject()
		rAt := CheckProjectAt(tmp)
		if rCwd.Status != rAt.Status || rCwd.Summary != rAt.Summary {
			t.Errorf("mismatch: cwd=%+v at=%+v", rCwd, rAt)
		}
	})
	t.Run("basename fallback", func(t *testing.T) {
		tmp := t.TempDir()
		t.Chdir(tmp)
		rCwd := CheckProject()
		rAt := CheckProjectAt(tmp)
		if rCwd.Status != rAt.Status || rCwd.Summary != rAt.Summary {
			t.Errorf("mismatch: cwd=%+v at=%+v", rCwd, rAt)
		}
	})
}

// TestCheckProjectAt_ExplicitRootDecoupledFromCwd verifies that the explicit
// root is used for detection, not os.Getwd().
func TestCheckProjectAt_ExplicitRootDecoupledFromCwd(t *testing.T) {
	tmp := t.TempDir()
	projectDir := filepath.Join(tmp, "named-project")
	os.MkdirAll(projectDir, 0755)
	os.WriteFile(filepath.Join(projectDir, ".vibe-palace.toml"),
		[]byte("[project]\nname = \"named-project\"\n"), 0644)

	// Run from an unrelated cwd — At(projectDir) should detect the project.
	t.Chdir(tmp)
	r := CheckProjectAt(projectDir)
	if r.Status != Info {
		t.Fatalf("expected Info, got %v", r.Status)
	}
	if !strings.Contains(r.Summary, "named-project") {
		t.Errorf("expected summary to reference named-project, got %q", r.Summary)
	}
}

func TestCheckProjectGitignore_Missing(t *testing.T) {
	dir := t.TempDir()
	// No .gitignore at all → all canonical entries missing → advisory Info.
	r := CheckProjectGitignore(dir)
	if r.Status != Info {
		t.Errorf("missing file: want Info, got %v", r.Status)
	}
	if len(r.Details) == 0 {
		t.Error("expected details listing missing entries")
	}
}

func TestCheckProjectGitignore_Clean(t *testing.T) {
	dir := t.TempDir()
	if err := storage.ReconcileProjectGitignore(dir); err != nil {
		t.Fatal(err)
	}
	r := CheckProjectGitignore(dir)
	if r.Status != Pass {
		t.Errorf("reconciled file: want Pass, got %v (%s)", r.Status, r.Summary)
	}
}

func TestCheckProjectGitignore_Partial(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/.claude/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := CheckProjectGitignore(dir)
	if r.Status != Info {
		t.Errorf("partial file: want Info, got %v", r.Status)
	}
	if !strings.Contains(r.Summary, "missing") {
		t.Errorf("expected summary to mention missing, got %q", r.Summary)
	}
}
