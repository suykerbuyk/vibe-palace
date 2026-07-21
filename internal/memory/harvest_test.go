// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package memory

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const testProject = "vibe-palace"

// newGitVault returns a fresh git-initialized temp dir usable as a vault root.
// Harvest always commits the routed files, so the vault must be a git repo.
func newGitVault(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-b", "main")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "user.name", "Test User")
	return dir
}

// countMD returns the number of *.md files in dir (0 if the dir is missing).
func countMD(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	return len(matches)
}

// hasRel reports whether rels contains want.
func hasRel(rels []string, want string) bool {
	return slices.Contains(rels, want)
}

// memoryRels returns the set of rels currently in the vault project's memory.
func memoryRels(t *testing.T, vault *storage.Vault) []string {
	t.Helper()
	metas, err := vault.ListMemories(testProject, 0)
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	var rels []string
	for _, m := range metas {
		rels = append(rels, m.Rel)
	}
	return rels
}

func TestNativeDirFromTranscript(t *testing.T) {
	got := NativeDirFromTranscript("/x/y/z/transcript.jsonl")
	want := filepath.FromSlash("/x/y/z/memory")
	if got != want {
		t.Fatalf("NativeDirFromTranscript = %q, want %q", got, want)
	}
}

func TestNativeDirFromCwd_KeepsLeadingDash(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_HOME", home)
	got, err := NativeDirFromCwd("/home/johns/code/vibe-palace")
	if err != nil {
		t.Fatalf("NativeDirFromCwd: %v", err)
	}
	want := filepath.Join(home, "projects", "-home-johns-code-vibe-palace", "memory")
	if got != want {
		t.Fatalf("NativeDirFromCwd = %q, want %q", got, want)
	}
	if !strings.Contains(got, "-home-johns-code-vibe-palace") {
		t.Fatalf("encoded segment should keep leading dash, got %q", got)
	}
}

func TestHarvest_MissingNativeDir(t *testing.T) {
	vaultRoot := t.TempDir()
	nativeDir := filepath.Join(t.TempDir(), "does-not-exist", "memory")

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if !res.NativeDirMissing {
		t.Fatal("expected NativeDirMissing=true")
	}
	if len(res.Routed) != 0 || len(res.DeletedHostLocal) != 0 {
		t.Fatalf("missing dir must not mutate: %+v", res)
	}
}

func TestHarvest_HappyPath(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir, Push: false})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}

	// 3 typed files routed.
	for _, rel := range []string{"pref-foo.md", "proj-bar.md", "pref-baz.md"} {
		if !hasRel(res.Routed, rel) {
			t.Errorf("expected %q routed, routed=%v", rel, res.Routed)
		}
	}
	// MEMORY.md skipped from routing AND deleted host-local.
	if !hasRel(res.IndexSkipped, "MEMORY.md") {
		t.Errorf("MEMORY.md should be IndexSkipped, got %v", res.IndexSkipped)
	}
	if !hasRel(res.DeletedHostLocal, "MEMORY.md") {
		t.Errorf("MEMORY.md should be deleted host-local, got %v", res.DeletedHostLocal)
	}
	// All host-local files drained.
	if n := countMD(t, nativeDir); n != 0 {
		t.Errorf("native dir should be empty after harvest, has %d *.md", n)
	}
	// Vault now holds the 3 memories.
	rels := memoryRels(t, vault)
	if len(rels) != 3 {
		t.Errorf("vault should have 3 memories, got %v", rels)
	}
	// Routed files are committed via the whole-dir commit path; the memory dir is
	// clean afterward.
	if !res.Committed {
		t.Errorf("expected Committed=true, res=%+v", res)
	}
	relMemDir := filepath.Join("Projects", testProject, "memory")
	if dirty := porcelain(t, vaultRoot, relMemDir); dirty != "" {
		t.Errorf("memory dir should be clean after harvest commit, got:\n%s", dirty)
	}
}

func TestHarvest_RouteByType(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	if _, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir}); err != nil {
		t.Fatalf("Harvest: %v", err)
	}

	// pref-foo.md is metadata.type: feedback in the fixture.
	meta, _, err := vault.ReadMemory(testProject, "pref-foo.md")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if meta.Type != "feedback" {
		t.Fatalf("pref-foo.md type = %q, want feedback", meta.Type)
	}
}

func TestHarvest_UnclassifiedDefaultsToProject(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A memory file whose frontmatter declares no type.
	content := "---\nname: No type here\ndescription: missing type field.\n---\n\nbody text\n"
	if err := os.WriteFile(filepath.Join(nativeDir, "mystery.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if !hasRel(res.Unclassified, "mystery.md") {
		t.Fatalf("mystery.md should be Unclassified, got %v", res.Unclassified)
	}
	if !hasRel(res.Routed, "mystery.md") {
		t.Fatalf("mystery.md should still be routed, got %v", res.Routed)
	}
	meta, _, err := vault.ReadMemory(testProject, "mystery.md")
	if err != nil {
		t.Fatalf("ReadMemory: %v", err)
	}
	if meta.Type != "project" {
		t.Fatalf("unclassified default type = %q, want project", meta.Type)
	}
}

func TestHarvest_Dedup(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// Pre-write an identical proj-bar.md into the vault (same meta+body as the
	// fixture file).
	if err := vault.WriteMemory(testProject, "proj-bar.md", storage.MemoryMeta{
		Name:        "Project layout",
		Description: "The repository uses a standard internal/ layout.",
		Type:        "project",
	}, "The repository follows the standard Go internal/ package layout.\n"); err != nil {
		t.Fatalf("pre-write: %v", err)
	}
	before := memoryRels(t, vault)

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if !hasRel(res.DedupSkipped, "proj-bar.md") {
		t.Fatalf("proj-bar.md should be DedupSkipped, got %v", res.DedupSkipped)
	}
	if hasRel(res.Routed, "proj-bar.md") {
		t.Fatalf("deduped file must not be routed, routed=%v", res.Routed)
	}
	// Host-local original deleted.
	if _, err := os.Stat(filepath.Join(nativeDir, "proj-bar.md")); !os.IsNotExist(err) {
		t.Fatalf("deduped host-local original should be deleted")
	}
	// The deduped rel was already present; vault count for it is unchanged (the
	// other two fixture files are newly routed).
	if !hasRel(before, "proj-bar.md") {
		t.Fatalf("setup error: proj-bar.md should pre-exist")
	}
}

func TestHarvest_Conflict(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// Pre-write a DIFFERENT proj-bar.md into the vault.
	const origBody = "TOTALLY DIFFERENT BODY — must not be clobbered.\n"
	if err := vault.WriteMemory(testProject, "proj-bar.md", storage.MemoryMeta{
		Name:        "Project layout",
		Description: "different description",
		Type:        "project",
	}, origBody); err != nil {
		t.Fatalf("pre-write: %v", err)
	}

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if len(res.Conflicted) != 1 {
		t.Fatalf("expected 1 conflict, got %v", res.Conflicted)
	}
	conflictRel := res.Conflicted[0]
	if !strings.HasPrefix(conflictRel, "proj-bar.harvested-") || !strings.HasSuffix(conflictRel, ".md") {
		t.Fatalf("conflict rel %q should be proj-bar.harvested-<ts>.md", conflictRel)
	}
	// Original vault file untouched.
	_, body, err := vault.ReadMemory(testProject, "proj-bar.md")
	if err != nil {
		t.Fatalf("ReadMemory original: %v", err)
	}
	if body != origBody {
		t.Fatalf("original vault file was clobbered: %q", body)
	}
	// Conflict file exists with the native content.
	if _, _, err := vault.ReadMemory(testProject, conflictRel); err != nil {
		t.Fatalf("conflict file should exist: %v", err)
	}
}

func TestHarvest_DryRunNoMutation(t *testing.T) {
	vaultRoot := t.TempDir()
	vault := storage.NewVault(vaultRoot)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	beforeNative := countMD(t, nativeDir)

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir, DryRun: true})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	// Result still reports what WOULD happen.
	if len(res.Routed) != 3 {
		t.Errorf("dry-run should report 3 routed, got %v", res.Routed)
	}
	if len(res.DeletedHostLocal) == 0 {
		t.Errorf("dry-run should report would-delete set")
	}
	// ZERO mutation: native files all still present.
	if afterNative := countMD(t, nativeDir); afterNative != beforeNative {
		t.Errorf("dry-run mutated native dir: before=%d after=%d", beforeNative, afterNative)
	}
	// Vault unchanged.
	if rels := memoryRels(t, vault); len(rels) != 0 {
		t.Errorf("dry-run wrote to vault: %v", rels)
	}
	if res.Committed {
		t.Error("dry-run must not commit")
	}
}

// porcelain returns the trimmed `git status --porcelain` output limited to rel.
func porcelain(t *testing.T, dir, rel string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain", "--", rel)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status %s: %v\n%s", rel, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestHarvest_NativeMissingCommitsVaultWrites covers the architecture decision:
// a memory written directly to the vault (vp_memory_write) with NO native dir
// present must still be committed by harvest.
func TestHarvest_NativeMissingCommitsVaultWrites(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)

	// Simulate vp_memory_write: write straight into the vault, do NOT commit.
	if err := vault.WriteMemory(testProject, "direct-write.md", storage.MemoryMeta{
		Name:        "Direct write",
		Description: "written via vp_memory_write, not via the native dir",
		Type:        "project",
	}, "body from the MCP tool\n"); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}

	// Native dir does not exist.
	nativeDir := filepath.Join(t.TempDir(), "does-not-exist", "memory")

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if !res.NativeDirMissing {
		t.Fatalf("expected NativeDirMissing=true, res=%+v", res)
	}
	if !res.Committed {
		t.Fatalf("expected Committed=true (uncommitted vault memory present), res=%+v", res)
	}
	relMemDir := filepath.Join("Projects", testProject, "memory")
	if dirty := porcelain(t, vaultRoot, relMemDir); dirty != "" {
		t.Fatalf("memory dir should be clean after commit, got:\n%s", dirty)
	}
}

// TestHarvest_AllDedupCleanSkip: an all-dedup drain over content that is already
// committed (and no .surface re-stamp) leaves the vault clean → no commit, no
// error.
func TestHarvest_AllDedupCleanSkip(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)

	// Pre-write proj-bar.md identical to the fixture, then COMMIT it so the
	// vault memory dir starts clean.
	if err := vault.WriteMemory(testProject, "proj-bar.md", storage.MemoryMeta{
		Name:        "Project layout",
		Description: "The repository uses a standard internal/ layout.",
		Type:        "project",
	}, "The repository follows the standard Go internal/ package layout.\n"); err != nil {
		t.Fatalf("pre-write: %v", err)
	}
	gitRun(t, vaultRoot, "add", "-A")
	gitRun(t, vaultRoot, "commit", "-m", "seed memory")

	// Native dir holds ONLY an identical proj-bar.md → all-dedup drain.
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const dup = "---\nname: Project layout\ndescription: The repository uses a standard internal/ layout.\nmetadata:\n  type: project\n---\n\nThe repository follows the standard Go internal/ package layout.\n"
	if err := os.WriteFile(filepath.Join(nativeDir, "proj-bar.md"), []byte(dup), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if !hasRel(res.DedupSkipped, "proj-bar.md") {
		t.Fatalf("proj-bar.md should be DedupSkipped, got %v", res.DedupSkipped)
	}
	if res.Committed {
		t.Fatalf("clean all-dedup drain must not commit, res=%+v", res)
	}
	relMemDir := filepath.Join("Projects", testProject, "memory")
	if dirty := porcelain(t, vaultRoot, relMemDir); dirty != "" {
		t.Fatalf("memory dir should still be clean, got:\n%s", dirty)
	}
}

// TestHarvest_CleanVaultNativeMissing: no native dir AND a clean vault → no
// commit, no error (the common Stop/no-writes case).
func TestHarvest_CleanVaultNativeMissing(t *testing.T) {
	vaultRoot := newGitVault(t)
	nativeDir := filepath.Join(t.TempDir(), "does-not-exist", "memory")

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if !res.NativeDirMissing {
		t.Fatalf("expected NativeDirMissing=true, res=%+v", res)
	}
	if res.Committed {
		t.Fatalf("clean vault must not commit, res=%+v", res)
	}
}

// gitRun runs a git command in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestHarvest_CommitLocalDowngrade(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	vaultRoot := t.TempDir()
	gitRun(t, vaultRoot, "init", "-b", "main")
	gitRun(t, vaultRoot, "config", "user.email", "test@example.com")
	gitRun(t, vaultRoot, "config", "user.name", "Test User")

	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir, Push: true})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	if !res.Committed {
		t.Fatal("expected a commit")
	}
	if res.CommitSHA == "" {
		t.Error("expected a CommitSHA")
	}
	if !res.PushDowngraded {
		t.Error("expected PushDowngraded=true (no remotes configured)")
	}
}

// TestHarvest_NoMemoryDirDirtySurface guards the regression where a dirty
// .surface (stamped by ordinary non-memory vault writes) plus an ABSENT memory
// dir made the commit's `git add Projects/<p>/memory` abort with exit 128.
// Harvest must scope its commit to existing paths and no-op cleanly here.
func TestHarvest_NoMemoryDirDirtySurface(t *testing.T) {
	vaultRoot := newGitVault(t)
	// Stamp a dirty .surface at the project root, NO memory dir, leave uncommitted.
	projDir := filepath.Join(vaultRoot, "Projects", testProject)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, ".surface"), []byte("surface stamp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nativeDir := filepath.Join(t.TempDir(), "does-not-exist", "memory")

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("Harvest must not error when memory dir is absent: %v", err)
	}
	if res.Committed {
		t.Errorf("harvest must not commit when there is no memory dir, res=%+v", res)
	}
}

// TestHarvest_CorruptVaultMemoryNotClobbered guards the regression where an
// existing-but-unparseable vault file at the same rel was blind-overwritten
// (and its host-local original deleted). The corrupt vault file must be
// preserved and the incoming content routed to a conflict name.
func TestHarvest_CorruptVaultMemoryNotClobbered(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)

	// Pre-write a CORRUPT vault memory at note.md (bypassing WriteMemory's
	// validation) — invalid frontmatter the parser cannot read.
	memDir, err := vault.MemoryDir(testProject)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const corrupt = "this is not valid frontmatter at all\n"
	corruptPath := filepath.Join(memDir, "note.md")
	if err := os.WriteFile(corruptPath, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}

	// Native dir has a VALID note.md with different content.
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const valid = "---\nname: Note\ndescription: a valid note\nmetadata:\n  type: project\n---\n\nincoming native body\n"
	if err := os.WriteFile(filepath.Join(nativeDir, "note.md"), []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	// The corrupt vault file must be untouched (not clobbered).
	got, err := os.ReadFile(corruptPath)
	if err != nil {
		t.Fatalf("corrupt file should still exist: %v", err)
	}
	if string(got) != corrupt {
		t.Errorf("corrupt vault file was clobbered: got %q", got)
	}
	// Incoming content routed to a conflict name, not to note.md.
	if len(res.Conflicted) != 1 || !strings.HasPrefix(res.Conflicted[0], "note.harvested-") {
		t.Errorf("expected incoming content routed to a note.harvested-* name, got Conflicted=%v", res.Conflicted)
	}
	if hasRel(res.Routed, "note.md") {
		t.Errorf("note.md must not be routed (would clobber), Routed=%v", res.Routed)
	}
}

// TestHarvest_DedupIgnoresTrailingNewline guards the regression where a native
// body lacking a trailing newline never deduped (WriteMemory appends one), so
// every re-harvest spawned a duplicate .harvested-<ts>.md for unchanged content.
func TestHarvest_DedupIgnoresTrailingNewline(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)

	// Native file body has NO trailing newline.
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const noNL = "---\nname: NL test\ndescription: trailing newline edge\nmetadata:\n  type: project\n---\n\nbody without trailing newline"
	nativeFile := filepath.Join(nativeDir, "nl.md")
	if err := os.WriteFile(nativeFile, []byte(noNL), 0o644); err != nil {
		t.Fatal(err)
	}

	// First harvest routes it.
	if _, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir}); err != nil {
		t.Fatalf("first Harvest: %v", err)
	}

	// Recreate the identical native file (no trailing newline) and re-harvest.
	if err := os.WriteFile(nativeFile, []byte(noNL), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir})
	if err != nil {
		t.Fatalf("second Harvest: %v", err)
	}
	if !hasRel(res.DedupSkipped, "nl.md") {
		t.Errorf("unchanged content must dedup, got DedupSkipped=%v Conflicted=%v", res.DedupSkipped, res.Conflicted)
	}
	if len(res.Conflicted) != 0 {
		t.Errorf("re-harvest of unchanged content must not create a conflict, got %v", res.Conflicted)
	}
	// Exactly one memory file in the vault (no duplicate .harvested-* spawned).
	if rels := memoryRels(t, vault); len(rels) != 1 {
		t.Errorf("expected 1 vault memory, got %v", rels)
	}
}

// TestHarvest_UnportableNameSkippedNotFailed: a single native memory file whose
// NAME is illegal on the Windows/darwin targets must be skipped and LEFT
// host-local — never routed into the synced vault, and never aborting the whole
// hook (which would strand every other memory in the batch).
func TestHarvest_UnportableNameSkippedNotFailed(t *testing.T) {
	vaultRoot := newGitVault(t)
	vault := storage.NewVault(vaultRoot)
	nativeDir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	typed := func(name string) string {
		return "---\nname: " + name + "\ndescription: d\nmetadata:\n  type: user\n---\nbody\n"
	}
	// "aux" is a Windows reserved device name — well-formed content, illegal name.
	badName := filepath.Join(nativeDir, "aux.md")
	if err := os.WriteFile(badName, []byte(typed("Bad")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeDir, "good-note.md"), []byte(typed("Good")), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Harvest(Options{VaultRoot: vaultRoot, Project: testProject, NativeDir: nativeDir, Push: false})
	if err != nil {
		t.Fatalf("Harvest must not fail on one unportable name: %v", err)
	}
	if !hasRel(res.NameRejected, "aux.md") {
		t.Errorf("aux.md should be NameRejected, got %v", res.NameRejected)
	}
	if !hasRel(res.Routed, "good-note.md") {
		t.Errorf("good-note.md should still route despite the bad sibling, got %v", res.Routed)
	}
	// The rejected native file is left host-local (for human eyes), not deleted.
	if _, err := os.Stat(badName); err != nil {
		t.Errorf("rejected native file must be left host-local, stat err: %v", err)
	}
	// And it must never reach the vault.
	if hasRel(memoryRels(t, vault), "aux.md") {
		t.Error("an unportable name must not reach the synced vault")
	}
}
