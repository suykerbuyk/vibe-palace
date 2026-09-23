// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// rebindToml is the live shape: the cwd-project template with an operator's
// own comment and domain, so a rewrite that loses either is visible.
const rebindToml = `# Per-directory vibe-palace project config. Managed by 'vp init'.
# The operator's own note: keep this line.

# Override the palace vault for this source-dir and descendants.
# vault_path = "~/work-palace-vault"

[meta]
version_major = 1
version_minor = 0
# kind = "cwd-project"

[project]
name = "old-slug"
domain = "work"
# tags = []
`

// rebindEnv isolates HOME and the config dir, so HostProjectConfigPath and
// "~" resolve inside the test.
func rebindEnv(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func rebindWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rebindVault makes a format-stamped vault holding Projects/<slug> for each slug.
func rebindVault(t *testing.T, dir string, slugs ...string) string {
	t.Helper()
	if err := surface.WriteFormat(dir, surface.RequiredDataFormat); err != nil {
		t.Fatal(err)
	}
	for _, s := range slugs {
		rebindWrite(t, filepath.Join(dir, "Projects", s, "resume.md"), "# resume\n")
	}
	return dir
}

// rebindSnapshot hashes every file under root, keyed by relative path.
func rebindSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		s := sha256.Sum256(b)
		rel, _ := filepath.Rel(root, p)
		out[rel] = hex.EncodeToString(s[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func rebindGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = SafeGitEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func rebindRename(checkout, vault string) CheckoutRebind {
	return CheckoutRebind{Kind: RebindRename, Checkout: checkout, FromSlug: "old-slug", ToSlug: "new-slug", VaultRoot: vault}
}

// R1: the rename changes exactly the one name line; every other byte, the
// operator's comments included, survives, and the .bak is the pre-image.
func TestRebindCheckoutRenameChangesOneLine(t *testing.T) {
	rebindEnv(t)
	vault := rebindVault(t, t.TempDir(), "new-slug")
	co := t.TempDir()
	tp := filepath.Join(co, ".vibe-palace.toml")
	rebindWrite(t, tp, rebindToml)

	rep, err := RebindCheckout(rebindRename(co, vault))
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	got, _ := os.ReadFile(tp)
	want := strings.Replace(rebindToml, `name = "old-slug"`, `name = "new-slug"`, 1)
	if string(got) != want {
		t.Errorf("file after rename differs beyond the name line:\n got: %q\nwant: %q", got, want)
	}
	if bak, _ := os.ReadFile(tp + ".bak"); string(bak) != rebindToml {
		t.Errorf(".bak is not the pre-image: %q", bak)
	}
	if rep.BackupPath != tp+".bak" || !strings.Contains(rep.TomlChange, `+ name = "new-slug"`) {
		t.Errorf("report: backup %q change %q", rep.BackupPath, rep.TomlChange)
	}
}

// R2: a split sets the TOP-LEVEL vault_path, so the checkout resolves the new
// vault; it replaces an active one; and it refuses one swallowed by a table.
func TestRebindCheckoutSplitSetsTopLevelVaultPath(t *testing.T) {
	home := rebindEnv(t)
	newVault := rebindVault(t, filepath.Join(home, "quantum-vault"), "old-slug")
	split := func(co string) CheckoutRebind {
		return CheckoutRebind{Kind: RebindSplit, Checkout: co, FromSlug: "old-slug", VaultPath: "~/quantum-vault"}
	}

	t.Run("commented example", func(t *testing.T) {
		co := t.TempDir()
		tp := filepath.Join(co, ".vibe-palace.toml")
		rebindWrite(t, tp, rebindToml)
		if _, err := RebindCheckout(split(co)); err != nil {
			t.Fatalf("rebind: %v", err)
		}
		got, _ := os.ReadFile(tp)
		want := strings.Replace(rebindToml, "# vault_path = \"~/work-palace-vault\"\n",
			"# vault_path = \"~/work-palace-vault\"\nvault_path = \"~/quantum-vault\"\n", 1)
		if string(got) != want {
			t.Errorf("unexpected file:\n%s", got)
		}
		p, src, err := ResolveVaultPath(co)
		if err != nil || p != newVault || !strings.HasPrefix(src, "cwd:") {
			t.Errorf("ResolveVaultPath = %q, %q, %v; want %q from the checkout", p, src, err, newVault)
		}
	})
	t.Run("existing active vault_path", func(t *testing.T) {
		co := t.TempDir()
		tp := filepath.Join(co, ".vibe-palace.toml")
		before := strings.Replace(rebindToml, "# vault_path = \"~/work-palace-vault\"", "vault_path = \"~/old-vault\"", 1)
		rebindWrite(t, tp, before)
		if _, err := RebindCheckout(split(co)); err != nil {
			t.Fatalf("rebind: %v", err)
		}
		got, _ := os.ReadFile(tp)
		if want := strings.Replace(before, `vault_path = "~/old-vault"`, `vault_path = "~/quantum-vault"`, 1); string(got) != want {
			t.Errorf("unexpected file:\n%s", got)
		}
	})
	t.Run("swallowed vault_path refuses", func(t *testing.T) {
		co := t.TempDir()
		tp := filepath.Join(co, ".vibe-palace.toml")
		before := rebindToml + "vault_path = \"~/elsewhere\"\n"
		rebindWrite(t, tp, before)
		if _, err := RebindCheckout(split(co)); err == nil || !strings.Contains(err.Error(), "inside [project]") {
			t.Fatalf("want a refusal naming [project], got %v", err)
		}
		if got, _ := os.ReadFile(tp); string(got) != before {
			t.Error("a refused rebind must not change the file")
		}
	})
}

// R3: only the NEW checkout is written. The old one, used as the anchor
// source, is byte-identical afterwards; the anchors arrive in NEW, and the
// per-session claim sentinels do not.
func TestRebindCheckoutLeavesTheOldCheckoutUntouched(t *testing.T) {
	rebindEnv(t)
	vault := rebindVault(t, t.TempDir(), "new-slug")
	oldCo, newCo := t.TempDir(), t.TempDir()
	rebindWrite(t, filepath.Join(oldCo, ".vibe-palace.toml"), rebindToml)
	rebindWrite(t, filepath.Join(oldCo, ".vibe-palace", "last-iter"), "160\n")
	rebindWrite(t, filepath.Join(oldCo, ".vibe-palace", "last-tasks-snapshot.json"), `{"iter_n":160}`)
	rebindWrite(t, filepath.Join(oldCo, ".vibe-palace", "claimed-abc"), "session\n")
	rebindWrite(t, filepath.Join(newCo, ".vibe-palace.toml"), rebindToml)
	before := rebindSnapshot(t, oldCo)

	r := rebindRename(newCo, vault)
	r.AnchorSource = oldCo
	rep, err := RebindCheckout(r)
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	after := rebindSnapshot(t, oldCo)
	if len(after) != len(before) {
		t.Errorf("old checkout file set changed: %v -> %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Errorf("old checkout file %s changed", k)
		}
	}
	for name, want := range map[string]string{"last-iter": "160\n", "last-tasks-snapshot.json": `{"iter_n":160}`} {
		if got, err := os.ReadFile(filepath.Join(newCo, ".vibe-palace", name)); err != nil || string(got) != want {
			t.Errorf("anchor %s in NEW = %q (err %v), want %q", name, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(newCo, ".vibe-palace", "claimed-abc")); !os.IsNotExist(err) {
		t.Error("a claimed-* sentinel must never be copied")
	}
	if len(rep.Anchors) != 2 {
		t.Errorf("anchors report %v", rep.Anchors)
	}
}

// R4: a checkout naming neither slug, or a name line in a shape the helper
// cannot prove, refuses and is left alone; a second run is a no-op.
func TestRebindCheckoutRefusesAndIsIdempotent(t *testing.T) {
	rebindEnv(t)
	vault := rebindVault(t, t.TempDir(), "new-slug")

	co := t.TempDir()
	tp := filepath.Join(co, ".vibe-palace.toml")
	other := strings.Replace(rebindToml, `name = "old-slug"`, `name = "someone-else"`, 1)
	rebindWrite(t, tp, other)
	if _, err := RebindCheckout(rebindRename(co, vault)); err == nil || !strings.Contains(err.Error(), "someone-else") {
		t.Errorf("want a refusal naming what the checkout says, got %v", err)
	}

	odd := strings.Replace(rebindToml, `name = "old-slug"`, `name = "old-slug" # inline`, 1)
	rebindWrite(t, tp, odd)
	if _, err := RebindCheckout(rebindRename(co, vault)); err == nil || !strings.Contains(err.Error(), "edit it by hand") {
		t.Errorf("want a refusal for an inline-commented name line, got %v", err)
	}
	if got, _ := os.ReadFile(tp); string(got) != odd {
		t.Error("a refused rebind must not change the file")
	}

	rebindWrite(t, tp, rebindToml)
	_ = os.Remove(tp + ".bak")
	if _, err := RebindCheckout(rebindRename(co, vault)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	_ = os.Remove(tp + ".bak")
	rep, err := RebindCheckout(rebindRename(co, vault))
	if err != nil || rep.TomlChange != "already rebound" {
		t.Errorf("second run = %q, %v; want a no-op", rep.TomlChange, err)
	}
	if _, err := os.Stat(tp + ".bak"); !os.IsNotExist(err) {
		t.Error("a no-op run must not write a .bak")
	}
}

// R5: the host-local projects/<from>.toml moves to <to>.toml; a conflicting
// <to>.toml refuses BEFORE the checkout is touched.
func TestRebindCheckoutCarriesHostProjectConfig(t *testing.T) {
	rebindEnv(t)
	vault := rebindVault(t, t.TempDir(), "new-slug")
	src, _ := HostProjectConfigPath("old-slug")
	dst, _ := HostProjectConfigPath("new-slug")

	co := t.TempDir()
	tp := filepath.Join(co, ".vibe-palace.toml")
	rebindWrite(t, tp, rebindToml)
	rebindWrite(t, src, "[palace.scoring]\nmin_score = 0.5\n")
	rebindWrite(t, dst, "[palace.scoring]\nmin_score = 0.9\n")
	if _, err := RebindCheckout(rebindRename(co, vault)); err == nil || !strings.Contains(err.Error(), "different content") {
		t.Fatalf("want a refusal for a conflicting host config, got %v", err)
	}
	if got, _ := os.ReadFile(tp); string(got) != rebindToml {
		t.Error("the conflict must refuse before the checkout's toml is changed")
	}

	_ = os.Remove(dst)
	rep, err := RebindCheckout(rebindRename(co, vault))
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if got, err := os.ReadFile(dst); err != nil || !strings.Contains(string(got), "0.5") {
		t.Errorf("host config not carried: %q, %v", got, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("the old host config must have moved, not been copied")
	}
	if rep.HostConfig == nil || rep.HostConfig.Action != "moved" {
		t.Errorf("host config report %+v", rep.HostConfig)
	}

	co2 := t.TempDir()
	rebindWrite(t, filepath.Join(co2, ".vibe-palace.toml"), rebindToml)
	_ = os.Remove(dst)
	rep, err = RebindCheckout(rebindRename(co2, vault))
	if err != nil || rep.HostConfig == nil || rep.HostConfig.Action != "absent" {
		t.Errorf("absent host config: %+v, %v", rep.HostConfig, err)
	}
}

// R6: no file anywhere under HOME, the config dir or the vault records the
// checkout's path, after a rename or a split.
func TestRebindCheckoutPersistsNoCheckoutPath(t *testing.T) {
	home := rebindEnv(t)
	vault := rebindVault(t, filepath.Join(home, "vault"), "new-slug")
	rebindVault(t, filepath.Join(home, "quantum-vault"), "old-slug")
	src, _ := HostProjectConfigPath("old-slug")
	rebindWrite(t, src, "[palace.scoring]\n")

	outside := t.TempDir()
	coA := filepath.Join(outside, "checkout-a")
	coB := filepath.Join(outside, "checkout-b")
	rebindWrite(t, filepath.Join(coA, ".vibe-palace.toml"), rebindToml)
	rebindWrite(t, filepath.Join(coB, ".vibe-palace.toml"), rebindToml)
	if _, err := RebindCheckout(rebindRename(coA, vault)); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := RebindCheckout(CheckoutRebind{Kind: RebindSplit, Checkout: coB, FromSlug: "old-slug", VaultPath: "~/quantum-vault"}); err != nil {
		t.Fatalf("split: %v", err)
	}
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		for _, co := range []string{coA, coB, outside} {
			if strings.Contains(string(b), co) {
				t.Errorf("%s records a checkout path (%s)", p, co)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// R7: references to the old slug and the old directory are REPORTED; the
// files holding them are left exactly as they were.
func TestRebindCheckoutReportsButNeverFixesReferences(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git unavailable")
	}
	rebindEnv(t)
	vault := rebindVault(t, t.TempDir(), "new-slug")
	co := t.TempDir()
	rebindGit(t, co, "init", "-q")
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), rebindToml)
	script := "QKP=../../old-checkout/qkp\n"
	doc := "See the old-slug project history.\n"
	rebindWrite(t, filepath.Join(co, "mk", "cluster.sh"), script)
	rebindWrite(t, filepath.Join(co, "README.md"), doc)
	rebindGit(t, co, "add", "-A")
	rebindGit(t, co, "commit", "-qm", "fixture")

	r := rebindRename(co, vault)
	r.OldDirBase = "old-checkout"
	rep, err := RebindCheckout(r)
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	found := map[string]bool{}
	for _, h := range rep.GrepHits {
		found[h.Pattern+"|"+h.File] = true
	}
	for _, k := range []string{"old-slug|README.md", "old-checkout|mk/cluster.sh"} {
		if !found[k] {
			t.Errorf("missing grep hit %s in %+v", k, rep.GrepHits)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(co, "mk", "cluster.sh")); string(got) != script {
		t.Error("the rebind must report a reference, never rewrite it")
	}
	if got, _ := os.ReadFile(filepath.Join(co, "README.md")); string(got) != doc {
		t.Error("the rebind must report a reference, never rewrite it")
	}
}

// R8: the rebind never commits or stages in the project repository; it only
// prints path-scoped git commands.
func TestRebindCheckoutNeverCommits(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git unavailable")
	}
	rebindEnv(t)
	vault := rebindVault(t, t.TempDir(), "new-slug")
	co := t.TempDir()
	rebindGit(t, co, "init", "-q")
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), rebindToml)
	rebindWrite(t, filepath.Join(co, ".gitignore"), "*.bak\n")
	rebindGit(t, co, "add", "-A")
	rebindGit(t, co, "commit", "-qm", "fixture")
	head := rebindGit(t, co, "rev-parse", "HEAD")

	rep, err := RebindCheckout(rebindRename(co, vault))
	if err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if got := rebindGit(t, co, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved: %s -> %s", head, got)
	}
	if st := rebindGit(t, co, "status", "--porcelain"); st != "M .vibe-palace.toml" {
		t.Errorf("status %q, want only the unstaged toml change", st)
	}
	if staged := rebindGit(t, co, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("the rebind staged %q", staged)
	}
	if len(rep.GitCommands) == 0 || !strings.Contains(rep.GitCommands[0], "add -- .vibe-palace.toml") {
		t.Errorf("git commands %v", rep.GitCommands)
	}
}

// R9: a checkout is never pointed at a vault or slug that does not hold the
// project: rename before the vault rename landed, or a split target that is
// not a stamped vault holding the slug, refuses.
func TestRebindCheckoutRefusesATargetThatDoesNotHoldTheProject(t *testing.T) {
	home := rebindEnv(t)
	co := t.TempDir()
	tp := filepath.Join(co, ".vibe-palace.toml")
	rebindWrite(t, tp, rebindToml)

	noTarget := rebindVault(t, t.TempDir())
	if _, err := RebindCheckout(rebindRename(co, noTarget)); err == nil || !strings.Contains(err.Error(), "has no Projects/new-slug") {
		t.Errorf("want a refusal for a missing target, got %v", err)
	}
	both := rebindVault(t, t.TempDir(), "new-slug", "old-slug")
	if _, err := RebindCheckout(rebindRename(co, both)); err == nil || !strings.Contains(err.Error(), "still has Projects/old-slug") {
		t.Errorf("want a refusal while the source still exists, got %v", err)
	}
	unstamped := filepath.Join(home, "not-a-vault")
	rebindWrite(t, filepath.Join(unstamped, "Projects", "old-slug", "resume.md"), "x\n")
	if _, err := RebindCheckout(CheckoutRebind{Kind: RebindSplit, Checkout: co, FromSlug: "old-slug", VaultPath: "~/not-a-vault"}); err == nil {
		t.Error("want a refusal for an unstamped split target")
	}
	rebindVault(t, filepath.Join(home, "empty-vault"))
	if _, err := RebindCheckout(CheckoutRebind{Kind: RebindSplit, Checkout: co, FromSlug: "old-slug", VaultPath: "~/empty-vault"}); err == nil || !strings.Contains(err.Error(), "holds neither") {
		t.Errorf("want a refusal for a vault without the slug, got %v", err)
	}
	if got, _ := os.ReadFile(tp); string(got) != rebindToml {
		t.Error("every refusal must leave the file unchanged")
	}
}
