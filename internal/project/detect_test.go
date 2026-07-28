// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDetectProject_ConfigInCwd(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `[project]
name = "my-project"
domain = "work"
tags = ["go"]
`)
	got, err := DetectProject(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "my-project" {
		t.Errorf("got %q, want %q", got, "my-project")
	}
}

func TestDetectProject_ConfigInParent(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "src", "pkg")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, parent, `[project]
name = "parent-proj"
`)
	got, err := DetectProject(child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "parent-proj" {
		t.Errorf("got %q, want %q", got, "parent-proj")
	}
}

func TestDetectProject_ConfigEmptyName_FallsThrough(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `[project]
name = ""
`)
	// No git, so should fall back to directory basename.
	got, err := DetectProject(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty project name from basename fallback")
	}
}

func TestDetectProject_ConfigInvalidSlug(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `[project]
name = "INVALID SLUG!"
`)
	_, err := DetectProject(dir)
	if err == nil {
		t.Fatal("expected error for invalid slug in config")
	}
}

func TestDetectProject_GitRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	initGitRepo(t, dir, "https://github.com/user/cool-project.git")

	got, err := DetectProject(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "cool-project" {
		t.Errorf("got %q, want %q", got, "cool-project")
	}
}

func TestDetectProject_GitRemoteSSH(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	initGitRepo(t, dir, "git@github.com:user/ssh-repo.git")

	got, err := DetectProject(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ssh-repo" {
		t.Errorf("got %q, want %q", got, "ssh-repo")
	}
}

func TestDetectProject_GitRemoteFromSubdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	initGitRepo(t, dir, "https://github.com/user/from-subdir.git")

	sub := filepath.Join(dir, "deep", "nested")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}

	got, err := DetectProject(sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from-subdir" {
		t.Errorf("got %q, want %q", got, "from-subdir")
	}
}

func TestDetectProject_DirectoryBasename(t *testing.T) {
	// Use a temp dir with a known basename.
	parent := t.TempDir()
	dir := filepath.Join(parent, "my-fallback")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}

	got, err := DetectProject(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "my-fallback" {
		t.Errorf("got %q, want %q", got, "my-fallback")
	}
}

func TestDetectProject_BasenameSlugged(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "My Cool Project")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}

	got, err := DetectProject(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "my-cool-project" {
		t.Errorf("got %q, want %q", got, "my-cool-project")
	}
}

func TestDetectProject_ConfigTakesPriorityOverGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	initGitRepo(t, dir, "https://github.com/user/git-name.git")
	writeConfig(t, dir, `[project]
name = "config-name"
`)

	got, err := DetectProject(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "config-name" {
		t.Errorf("got %q, want %q (config should take priority over git)", got, "config-name")
	}
}

// --- DetectProjectHighConfidence tests ---

func TestDetectProjectHighConfidence_Config(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `[project]
name = "hc-config"
`)
	got, err := DetectProjectHighConfidence(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hc-config" {
		t.Errorf("got %q, want %q", got, "hc-config")
	}
}

func TestDetectProjectHighConfidence_GitRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initGitRepo(t, dir, "https://github.com/user/hc-git.git")
	got, err := DetectProjectHighConfidence(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hc-git" {
		t.Errorf("got %q, want %q", got, "hc-git")
	}
}

func TestDetectProjectHighConfidence_NoBasenameFallback(t *testing.T) {
	// Bare directory with a valid basename slug must NOT succeed — that is
	// exactly the silent wrong-default DetectProject permits and bootstrap
	// must refuse (worktree folders ≠ vault slugs).
	parent := t.TempDir()
	dir := filepath.Join(parent, "looks-like-a-project")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	got, err := DetectProjectHighConfidence(dir)
	if err == nil {
		t.Fatalf("expected error for basename-only dir, got slug %q", got)
	}
	if got != "" {
		t.Errorf("slug on error = %q, want empty", got)
	}
	// DetectProject still falls through to basename — contrast pin.
	if low, lerr := DetectProject(dir); lerr != nil || low != "looks-like-a-project" {
		t.Errorf("DetectProject contrast = %q, %v; want looks-like-a-project", low, lerr)
	}
}

func TestDetectProjectHighConfidence_ConfigBeatsGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initGitRepo(t, dir, "https://github.com/user/git-name.git")
	writeConfig(t, dir, `[project]
name = "config-name"
`)
	got, err := DetectProjectHighConfidence(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "config-name" {
		t.Errorf("got %q, want config-name", got)
	}
}

func TestDetectProjectHighConfidence_EmptyConfigNameFallsToGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	initGitRepo(t, dir, "https://github.com/user/from-git.git")
	writeConfig(t, dir, `[project]
name = ""
`)
	got, err := DetectProjectHighConfidence(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from-git" {
		t.Errorf("got %q, want from-git", got)
	}
}

// --- ParseProjectConfig tests ---

func TestParseProjectConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := `[project]
name = "test-proj"
domain = "personal"
tags = ["go", "cli"]
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := ParseProjectConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "test-proj" {
		t.Errorf("Name = %q, want %q", cfg.Name, "test-proj")
	}
	if cfg.Domain != "personal" {
		t.Errorf("Domain = %q, want %q", cfg.Domain, "personal")
	}
	if len(cfg.Tags) != 2 || cfg.Tags[0] != "go" || cfg.Tags[1] != "cli" {
		t.Errorf("Tags = %v, want [go cli]", cfg.Tags)
	}
}

func TestParseProjectConfig_MissingFile(t *testing.T) {
	_, err := ParseProjectConfig("/nonexistent/.vibe-palace.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseProjectConfig_InvalidTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte("not valid [[[toml"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := ParseProjectConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
}

func TestParseProjectConfig_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := ParseProjectConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Name != "" {
		t.Errorf("expected empty name, got %q", cfg.Name)
	}
}

func TestParseProjectFile_VaultPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := `vault_path = "/tmp/alt-vault"

[project]
name = "test-proj"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pf, err := ParseProjectFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pf.VaultPath != "/tmp/alt-vault" {
		t.Errorf("VaultPath = %q, want %q", pf.VaultPath, "/tmp/alt-vault")
	}
	if pf.Project.Name != "test-proj" {
		t.Errorf("Project.Name = %q, want %q", pf.Project.Name, "test-proj")
	}
}

func TestParseProjectFile_UnknownKeysTolerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := `vault_path = "/tmp/v"
future_field = "ignored"

[project]
name = "p"

[future_section]
key = "value"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pf, err := ParseProjectFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pf.VaultPath != "/tmp/v" {
		t.Errorf("VaultPath = %q, want %q", pf.VaultPath, "/tmp/v")
	}
	if pf.Project.Name != "p" {
		t.Errorf("Project.Name = %q, want %q", pf.Project.Name, "p")
	}
}

func TestParseProjectFile_NoVaultPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	content := `[project]
name = "p"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pf, err := ParseProjectFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pf.VaultPath != "" {
		t.Errorf("VaultPath = %q, want empty", pf.VaultPath)
	}
}

// --- extractRepoName tests ---

func TestExtractRepoName(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/user/repo.git", "repo"},
		{"https://github.com/user/repo", "repo"},
		{"git@github.com:user/repo.git", "repo"},
		{"git@github.com:user/repo", "repo"},
		{"ssh://git@github.com/user/repo.git", "repo"},
		{"https://gitlab.com/group/subgroup/project.git", "project"},
		{"repo.git", "repo"},
		{"simple-name", "simple-name"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := extractRepoName(tt.url)
			if got != tt.want {
				t.Errorf("extractRepoName(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

// --- slugify tests ---

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"My Project", "my-project"},
		{"UPPER_CASE", "upper-case"},
		{"already-good", "already-good"},
		{"  spaces  ", "spaces"},
		{"special!@#chars", "special-chars"},
		{"multiple---hyphens", "multiple-hyphens"},
		{"CamelCase123", "camelcase123"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugify(tt.input)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- findFileUpward tests ---

func TestFindFileUpward_InCwd(t *testing.T) {
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := findFileUpward(dir, "sentinel.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sentinel {
		t.Errorf("got %q, want %q", got, sentinel)
	}
}

func TestFindFileUpward_InParent(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "a", "b", "c")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(parent, "marker.txt")
	if err := os.WriteFile(sentinel, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := findFileUpward(child, "marker.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sentinel {
		t.Errorf("got %q, want %q", got, sentinel)
	}
}

func TestFindFileUpward_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := findFileUpward(dir, "does-not-exist.xyz")
	if err == nil {
		t.Fatal("expected error for file not found")
	}
}

// --- DetectSignal tests ---

func TestDetectSignal_VibeConfig(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "[project]\nname = \"p\"\n")
	if got := DetectSignal(dir); got != SignalVibeConfig {
		t.Errorf("DetectSignal = %q, want %q", got, SignalVibeConfig)
	}
}

func TestDetectSignal_VibeConfigInParent(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "a", "b")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, parent, "[project]\nname = \"p\"\n")
	if got := DetectSignal(child); got != SignalVibeConfig {
		t.Errorf("DetectSignal = %q, want %q", got, SignalVibeConfig)
	}
}

func TestDetectSignal_Git(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DetectSignal(dir); got != SignalGit {
		t.Errorf("DetectSignal = %q, want %q", got, SignalGit)
	}
}

func TestDetectSignal_ManifestFiles(t *testing.T) {
	cases := []struct {
		filename string
		want     ProjectSignal
	}{
		{"go.mod", SignalGoMod},
		{"package.json", SignalPackageJSON},
		{"Cargo.toml", SignalCargoToml},
		{"pyproject.toml", SignalPyprojectToml},
		{"pom.xml", SignalPomXML},
	}
	for _, tc := range cases {
		t.Run(tc.filename, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, tc.filename), []byte(""), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := DetectSignal(dir); got != tc.want {
				t.Errorf("DetectSignal(%s) = %q, want %q", tc.filename, got, tc.want)
			}
		})
	}
}

func TestDetectSignal_NoSignal(t *testing.T) {
	dir := t.TempDir()
	if got := DetectSignal(dir); got != SignalNone {
		t.Errorf("DetectSignal on empty dir = %q, want %q", got, SignalNone)
	}
}

func TestDetectSignal_PrecedenceConfigOverGit(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "[project]\nname = \"p\"\n")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DetectSignal(dir); got != SignalVibeConfig {
		t.Errorf("DetectSignal = %q, want %q (config beats git)", got, SignalVibeConfig)
	}
}

func TestDetectSignal_PrecedenceGitOverManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := DetectSignal(dir); got != SignalGit {
		t.Errorf("DetectSignal = %q, want %q (git beats manifests)", got, SignalGit)
	}
}

func TestDetectSignal_ForceSkipHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	// Even if the real home contains signals, DetectSignal must return None.
	// We don't pollute the real home — we just probe it.
	got := DetectSignal(home)
	if got != SignalNone {
		t.Errorf("DetectSignal($HOME) = %q, want %q (must force-skip)", got, SignalNone)
	}
}

func TestDetectSignal_ForceSkipRoot(t *testing.T) {
	if got := DetectSignal("/"); got != SignalNone {
		t.Errorf("DetectSignal(/) = %q, want %q", got, SignalNone)
	}
}

// --- helpers ---

func writeConfig(t *testing.T, dir, content string) {
	t.Helper()
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func initGitRepo(t *testing.T, dir, remoteURL string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init"},
		{"git", "remote", "add", "origin", remoteURL},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %s\n%s", args, err, out)
		}
	}
}

// --- $HOME-boundary marker walk (home-marker-must-not-enroll-home-tree) ---

// mkdirAll is a test helper that creates a nested directory under root.
func mkSubdir(t *testing.T, root string, parts ...string) string {
	t.Helper()
	dir := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDetectSignal_HomeMarkerDoesNotEnrollHomeTree is the direct repro from the
// home-marker task: a single .vibe-palace.toml at $HOME must NOT make every
// descendant directory report SignalVibeConfig. Before the boundary walk, rows
// 2 and 3 leaked SignalVibeConfig.
func TestDetectSignal_HomeMarkerDoesNotEnrollHomeTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfig(t, home, `[project]
name = "dotfiles"
`)
	sub := mkSubdir(t, home, "random-subdir")
	nested := mkSubdir(t, home, "random-subdir", "nested")

	for _, dir := range []string{home, sub, nested} {
		if got := DetectSignal(dir); got != SignalNone {
			t.Errorf("DetectSignal(%q) = %q, want %q (a $HOME marker must not enroll the home tree)", dir, got, SignalNone)
		}
	}

	// DetectProject must also refuse to name descendants after the $HOME marker.
	// With no git remote it falls to the directory basename, never "dotfiles".
	if got, _ := DetectProject(sub); got == "dotfiles" {
		t.Errorf("DetectProject(%q) = %q; a $HOME marker must not name a descendant", sub, got)
	}
}

// TestDetectSignal_NearerMarkerWinsUnderHome verifies a real project under the
// home tree still detects — the boundary only ignores a marker AT $HOME, never
// a nearer one in an actual project directory.
func TestDetectSignal_NearerMarkerWinsUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeConfig(t, home, `[project]
name = "dotfiles"
`)
	proj := mkSubdir(t, home, "code", "realproj")
	writeConfig(t, proj, `[project]
name = "realproj"
`)
	sub := mkSubdir(t, home, "code", "realproj", "internal")

	if got := DetectSignal(sub); got != SignalVibeConfig {
		t.Errorf("DetectSignal(%q) = %q, want %q (nearer marker must win)", sub, got, SignalVibeConfig)
	}
	if got, err := DetectProject(sub); err != nil || got != "realproj" {
		t.Errorf("DetectProject(%q) = %q, %v; want realproj (nearer marker)", sub, got, err)
	}
}

// TestDetectSignal_ProjectOutsideHomeStillWalksToRoot verifies the boundary
// does not clip a project that lives OUTSIDE the home tree: the marker walk
// there must still climb to the filesystem root as before.
func TestDetectSignal_ProjectOutsideHomeStillWalksToRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A sibling temp root, not under $HOME.
	outside := t.TempDir()
	writeConfig(t, outside, `[project]
name = "srv-project"
`)
	sub := mkSubdir(t, outside, "a", "b", "c")

	if got := DetectSignal(sub); got != SignalVibeConfig {
		t.Errorf("DetectSignal(%q) = %q, want %q (a project outside $HOME must still walk to root)", sub, got, SignalVibeConfig)
	}
	if got, err := DetectProject(sub); err != nil || got != "srv-project" {
		t.Errorf("DetectProject(%q) = %q, %v; want srv-project", sub, got, err)
	}
}

// TestDetectSignal_SymlinkedHomeBoundary verifies the boundary comparison holds
// when $HOME itself is a symlink — the case that originally surfaced the bug
// (the reporter's ~/.vibe-palace.toml was a stow symlink). Both the walked path
// and the boundary are symlink-resolved, so the stop still fires.
func TestDetectSignal_SymlinkedHomeBoundary(t *testing.T) {
	realHome := t.TempDir()
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(realHome, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	t.Setenv("HOME", link)
	writeConfig(t, realHome, `[project]
name = "dotfiles"
`)
	// Reach a descendant through the symlinked home path.
	if err := os.MkdirAll(filepath.Join(realHome, "random"), 0o755); err != nil {
		t.Fatal(err)
	}
	via := filepath.Join(link, "random")

	if got := DetectSignal(via); got != SignalNone {
		t.Errorf("DetectSignal(%q) = %q, want %q (symlinked $HOME boundary must still stop the walk)", via, got, SignalNone)
	}
}

// --- RequireKnownProject (commit-write-tools write-authorization gate) ---

func TestRequireKnownProject_MarkerPresent(t *testing.T) {
	vault := t.TempDir()
	repo := t.TempDir()
	writeConfig(t, repo, `[project]
name = "proj"
`)
	if err := RequireKnownProject("proj", vault, repo); err != nil {
		t.Errorf("marker present must authorize: %v", err)
	}
}

func TestRequireKnownProject_ExistingVaultProject(t *testing.T) {
	vault := t.TempDir()
	repo := t.TempDir() // no marker
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RequireKnownProject("proj", vault, repo); err != nil {
		t.Errorf("existing Projects/<slug>/ must authorize a markerless project: %v", err)
	}
}

func TestRequireKnownProject_RefusesWhenBothAbsent(t *testing.T) {
	vault := t.TempDir()
	repo := t.TempDir() // no marker, no vault project
	if err := RequireKnownProject("proj", vault, repo); err == nil {
		t.Error("must refuse when neither a marker nor a vault project exists (the phantom-scaffold case)")
	}
}

func TestRequireKnownProject_RefusesHomeEvenIfVaultProjectExists(t *testing.T) {
	vault := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A stray Projects/<home-basename>/ residue must NOT re-authorize $HOME.
	if err := os.MkdirAll(filepath.Join(vault, "Projects", filepath.Base(home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := RequireKnownProject(filepath.Base(home), vault, home); err == nil {
		t.Error("must refuse a write rooted at $HOME even when a stray vault project dir exists")
	}
}

// TestFindMarkerUpward_ZeroBoundaryWalksToRoot documents that a zero boundary
// (home undeterminable) degrades to the unbounded walk rather than failing.
func TestFindMarkerUpward_ZeroBoundaryWalksToRoot(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `[project]
name = "x"
`)
	child := mkSubdir(t, root, "a", "b")
	got, err := findMarkerUpward(child, "")
	if err != nil {
		t.Fatalf("unexpected error with zero boundary: %v", err)
	}
	if got != filepath.Join(root, ConfigFileName) {
		t.Errorf("got %q, want marker at %q", got, root)
	}
}
