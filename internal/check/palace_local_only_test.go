// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// plTree creates each rel path under root: a trailing "/" makes a directory,
// anything else a regular file.
func plTree(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// plDigest hashes every path, mode and file body under root, so a check that
// writes, renames or removes anything changes the answer.
func plDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		h.Write([]byte(rel + "|" + info.Mode().String() + "\n"))
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestCheckPalaceLocalOnly_PassOnCleanVault(t *testing.T) {
	root := t.TempDir()
	plTree(t, root,
		"palace/real/drawers/w/r/drawers.jsonl",
		"palace/.local/embed-cache/real/a.vec",
		"Projects/notes/sessions/n.md",
	)
	r := CheckPalaceLocalOnly(storage.NewVault(root))
	if r.Status != Pass || r.Summary != "none" {
		t.Fatalf("got %+v, want Pass/none", r)
	}
}

func TestCheckPalaceLocalOnly_NoPalaceTreeIsPass(t *testing.T) {
	r := CheckPalaceLocalOnly(storage.NewVault(t.TempDir()))
	if r.Status != Pass {
		t.Fatalf("a vault with no palace/ has nothing to report: %+v", r)
	}
}

// TestCheckPalaceLocalOnly_ReportsEveryNonStoreShape names each shape and what
// it holds, and says nothing about what to do with it.
func TestCheckPalaceLocalOnly_ReportsEveryNonStoreShape(t *testing.T) {
	root := t.TempDir()
	plTree(t, root,
		"palace/husk/.local/embed-cache/a.vec",
		"palace/husk/.local/embed-cache/b.vec",
		"palace/husk/.local/imported-sessions.jsonl",
		"palace/bare/",
		"palace/hollow/drawers/w/r/",
		"palace/both/.local/embed-cache/c.vec",
		"palace/both/kg/",
		"palace/emptylocal/.local/",
		"palace/platform/.local/embed-cache/d.vec",
		"palace/real/.surface",
		"Projects/husk/",
	)
	r := CheckPalaceLocalOnly(storage.NewVault(root))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info (never Fail): %+v", r.Status, r)
	}
	if r.Summary != "6 palace/ director(ies) hold no file outside machine-local .local/" {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Details) != 8 || r.Details[0] != palaceLocalOnlyHeader {
		t.Fatalf("details = %q, want the header, the tracking line, then one line per directory", r.Details)
	}
	// Not a repository, so nothing is tracked — and the row says so because it
	// checked, not because it assumed.
	if r.Details[1] != "git tracks no file in any of them, so none of this is synced to another host" {
		t.Errorf("tracking line = %q", r.Details[1])
	}
	want := []string{
		"palace/bare/ — (empty) — Projects/bare/: absent",
		"palace/both/ — .local/: embed-cache/ (1 file) — empty subtree outside .local/ — Projects/both/: absent",
		"palace/emptylocal/ — .local/: (empty) — Projects/emptylocal/: absent",
		"palace/hollow/ — (empty subtree only) — Projects/hollow/: absent",
		"palace/husk/ — .local/: embed-cache/ (2 files), imported-sessions.jsonl — Projects/husk/: present",
		"palace/platform/ — .local/: embed-cache/ (1 file) — Projects/platform/: absent",
	}
	for i, w := range want {
		if r.Details[i+2] != w {
			t.Errorf("details[%d] = %q\n want %q", i+2, r.Details[i+2], w)
		}
	}
	for _, d := range r.Details {
		if strings.Contains(d, "palace/real/") {
			t.Errorf("a real store must not be reported: %q", d)
		}
	}
}

// TestCheckPalaceLocalOnly_PrescribesNoDisposition pins the report-and-defer
// contract. The directories can hold imported-sessions.jsonl, the dedupe
// ledger `vp migrate` reads, so a removal instruction here is a data-loss
// instruction. Word boundaries, so a slug like "platform" is not a hit.
func TestCheckPalaceLocalOnly_PrescribesNoDisposition(t *testing.T) {
	root := t.TempDir()
	plTree(t, root,
		"palace/platform/.local/embed-cache/a.vec",
		"palace/husk/.local/imported-sessions.jsonl",
		"palace/bare/",
	)
	r := CheckPalaceLocalOnly(storage.NewVault(root))
	banned := regexp.MustCompile(`(?i)\brm\b|\brmdir\b|\bdelete\b|\bdeletion\b|\bremove\b|\bremoval\b|` +
		`\bdiscard\b|\bpurge\b|\bwipe\b|\bleftover\b|\bresidue\b|\bclean ?up\b|\bprune\b`)
	for _, line := range append([]string{r.Summary}, r.Details...) {
		if m := banned.FindString(line); m != "" {
			t.Errorf("row prescribes a disposition (%q) in %q", m, line)
		}
	}
}

// TestCheckPalaceLocalOnly_NeverWrites: vp_check is Mutating:false, and this
// row runs on every default invocation against the live vault.
func TestCheckPalaceLocalOnly_NeverWrites(t *testing.T) {
	root := t.TempDir()
	plTree(t, root,
		"palace/husk/.local/embed-cache/a.vec",
		"palace/bare/",
		"palace/real/drawers/w/r/drawers.jsonl",
	)
	before := plDigest(t, root)
	_ = CheckPalaceLocalOnly(storage.NewVault(root))
	if after := plDigest(t, root); after != before {
		t.Fatal("CheckPalaceLocalOnly changed the vault tree")
	}
}

func TestCheckPalaceLocalOnly_UnreadablePalaceIsInfo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "palace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	r := CheckPalaceLocalOnly(storage.NewVault(root))
	if r.Status != Info || !strings.HasPrefix(r.Summary, "scan palace/: ") {
		t.Fatalf("got %+v, want Info with a scan palace/ summary", r)
	}
}

func TestDescribeLocalEntries_Unreadable(t *testing.T) {
	got := describeLocalEntries([]storage.LocalEntry{{Name: "embed-cache", IsDir: true, Files: -1}})
	if got != "embed-cache/ (unreadable)" {
		t.Errorf("got %q", got)
	}
}

// TestPalaceLocalOnlyProducer covers the selector through the registry,
// including the no-vault verdict every vault-scoped producer shares.
func TestPalaceLocalOnlyProducer(t *testing.T) {
	rs, err := RunSelected("", "palace-local-only")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if len(rs) != 1 || rs[0].Name != "Palace local-only" || rs[0].Status != Skip ||
		rs[0].Summary != "no vault configured" {
		t.Fatalf("got %+v, want a skipped Palace local-only row", rs)
	}

	root := t.TempDir()
	plTree(t, root, "palace/husk/.local/embed-cache/a.vec")
	rs, err = RunSelected(root, "palace-local-only")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if rs[0].Status != Info {
		t.Fatalf("got %+v, want Info", rs[0])
	}
}

// plGit runs git hermetically in dir, skipping the test when git is absent.
func plGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

// TestCheckPalaceLocalOnly_TrackedFilesAreNamed: on a vault that committed a
// .local/ file (the canonical gitignore does not prevent it), the row must not
// claim the directory is untracked. It names the tracked count instead.
func TestCheckPalaceLocalOnly_TrackedFilesAreNamed(t *testing.T) {
	root := t.TempDir()
	plTree(t, root,
		"palace/committed/.local/imported-sessions.jsonl",
		"palace/husk/.local/embed-cache/a.vec",
	)
	plGit(t, root, "init", "-q")
	plGit(t, root, "add", "palace/committed")

	r := CheckPalaceLocalOnly(storage.NewVault(root))
	all := strings.Join(r.Details, "\n")
	if strings.Contains(all, "git tracks no file") {
		t.Errorf("the row claims nothing is tracked on a vault that tracks a .local/ file:\n%s", all)
	}
	if !strings.Contains(all, "palace/committed/ — .local/: imported-sessions.jsonl — Projects/committed/: absent — git tracks 1 file(s) under .local/") {
		t.Errorf("the tracked directory must carry its count:\n%s", all)
	}
	if strings.Contains(all, "palace/husk/ — .local/: embed-cache/ (1 file) — Projects/husk/: absent — git") {
		t.Errorf("an untracked directory must carry no tracked count:\n%s", all)
	}
}

// TestCheckPalaceLocalOnly_TrackingUnknownMakesNoClaim: when git cannot answer,
// the row says so and claims neither way.
func TestCheckPalaceLocalOnly_TrackingUnknownMakesNoClaim(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	plTree(t, root, "palace/husk/.local/embed-cache/a.vec")
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /nonexistent/vp-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := CheckPalaceLocalOnly(storage.NewVault(root))
	if !strings.HasPrefix(r.Details[1], "whether git tracks any file in them could not be checked") {
		t.Errorf("tracking line = %q, want the could-not-check wording", r.Details[1])
	}
}

// TestCheckPalaceLocalOnly_EmptyDirsNeedNoGit: with no .local/ anywhere there is
// no file for git to track, and the row says that without asking git.
func TestCheckPalaceLocalOnly_EmptyDirsNeedNoGit(t *testing.T) {
	root := t.TempDir()
	plTree(t, root, "palace/bare/", "palace/hollow/drawers/")
	r := CheckPalaceLocalOnly(storage.NewVault(root))
	if r.Details[1] != "none of them holds a file, and git cannot carry an empty directory" {
		t.Errorf("tracking line = %q", r.Details[1])
	}
}
