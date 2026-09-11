// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

func putFile(t *testing.T, vault, rel, body string) string {
	t.Helper()
	p := filepath.Join(vault, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func planFor(t *testing.T, vault, resourceType, only string) []Change {
	t.Helper()
	plan, err := Plan(vpctx.NewResolver(vault), PlanOptions{ResourceTypes: []string{resourceType}, Only: only})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return plan
}

func embedded(t *testing.T, resource string) string {
	t.Helper()
	s, err := vpctx.NewResolver(t.TempDir()).EmbeddedContent(resource)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func withHook(t *testing.T, fn func(rel string)) {
	t.Helper()
	old := resetBeforeRemoveHook
	resetBeforeRemoveHook = fn
	t.Cleanup(func() { resetBeforeRemoveHook = old })
}

func TestReset_RemovesOverrideAndKeepsBackup(t *testing.T) {
	vault := t.TempDir()
	const override = "# my wrap override\n"
	p := putFile(t, vault, "Templates/commands/wrap.md", override)

	out, err := Reset(planFor(t, vault, "command", "wrap"))
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("outcomes = %+v", out)
	}
	o := out[0]
	want := templates.BackupName("Templates/commands/wrap.md", []byte(override))
	if !o.Removed || o.Mirror || o.Backup != want || o.BackupReused || o.Rel != "Templates/commands/wrap.md" || o.Name != "wrap" {
		t.Errorf("outcome = %+v, want removed with backup %s", o, want)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("override still present (err=%v)", err)
	}
	got, err := os.ReadFile(filepath.Join(vault, filepath.FromSlash(want)))
	if err != nil || string(got) != override {
		t.Errorf("backup = %q (err=%v)", got, err)
	}
	if !ShimSourceRemoved(out) {
		t.Error("a removed command file is a shim source")
	}
}

// TestReset_MirrorNeedsNoBackup is I5: a named byte-identical mirror is
// removed with no backup.
func TestReset_MirrorNeedsNoBackup(t *testing.T) {
	vault := t.TempDir()
	putFile(t, vault, "Templates/commands/restart.md", embedded(t, "command:restart"))
	out, err := Reset(planFor(t, vault, "command", "restart"))
	if err != nil {
		t.Fatal(err)
	}
	if !out[0].Removed || !out[0].Mirror || out[0].Backup != "" {
		t.Errorf("outcome = %+v, want a removed mirror with no backup", out[0])
	}
	entries, _ := os.ReadDir(filepath.Join(vault, "Templates", "commands"))
	if len(entries) != 0 {
		t.Errorf("Templates/commands/ holds %d entries after a mirror reset", len(entries))
	}
}

// TestReset_BackupFailureRemovesNothing is the two-phase rule: every backup is
// written before the first removal, so a backup that cannot be written leaves
// every file where it was.
func TestReset_BackupFailureRemovesNothing(t *testing.T) {
	vault := t.TempDir()
	const a, b = "# restart override\n", "# wrap override\n"
	pa := putFile(t, vault, "Templates/commands/restart.md", a)
	pb := putFile(t, vault, "Templates/commands/wrap.md", b)
	// Take wrap's backup name with different bytes: a forced collision.
	putFile(t, vault, templates.BackupName("Templates/commands/wrap.md", []byte(b)), "edited\n")

	var plan []Change
	plan = append(plan, planFor(t, vault, "command", "restart")...)
	plan = append(plan, planFor(t, vault, "command", "wrap")...)
	_, err := Reset(plan)
	if !errors.Is(err, templates.ErrBackupCollision) {
		t.Fatalf("err = %v, want ErrBackupCollision", err)
	}
	for p, want := range map[string]string{pa: a, pb: b} {
		if got, _ := os.ReadFile(p); string(got) != want {
			t.Errorf("%s changed to %q", p, got)
		}
	}
}

// TestReset_EditDuringResetIsKept: the removal is compare-and-set on the bytes
// backed up, so an edit made after the backup survives and the backup holds
// the earlier bytes.
func TestReset_EditDuringResetIsKept(t *testing.T) {
	vault := t.TempDir()
	const before, edit = "# before\n", "# edited while resetting\n"
	p := putFile(t, vault, "Templates/commands/wrap.md", before)
	withHook(t, func(string) {
		if err := os.WriteFile(p, []byte(edit), 0o644); err != nil {
			t.Error(err)
		}
	})
	out, err := Reset(planFor(t, vault, "command", "wrap"))
	if err != nil {
		t.Fatal(err)
	}
	o := out[0]
	if o.Removed || !strings.Contains(o.Kept, "changed while resetting; kept") || !strings.Contains(o.Kept, o.Backup) {
		t.Errorf("outcome = %+v, want kept naming the backup", o)
	}
	if got, _ := os.ReadFile(p); string(got) != edit {
		t.Errorf("the edit was lost: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(vault, filepath.FromSlash(o.Backup))); string(got) != before {
		t.Errorf("backup = %q, want the bytes read before the edit", got)
	}
	if ShimSourceRemoved(out) {
		t.Error("nothing was removed, so no shim source was")
	}
}

// TestReset_MirrorEditedDuringResetIsKept covers the kept wording with no
// backup to name.
func TestReset_MirrorEditedDuringResetIsKept(t *testing.T) {
	vault := t.TempDir()
	p := putFile(t, vault, "Templates/commands/restart.md", embedded(t, "command:restart"))
	withHook(t, func(string) { _ = os.WriteFile(p, []byte("# now an override\n"), 0o644) })
	out, err := Reset(planFor(t, vault, "command", "restart"))
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Kept != "changed while resetting; kept" {
		t.Errorf("kept = %q", out[0].Kept)
	}
}

// TestReset_AlreadyGone: a file removed by someone else between the backup and
// the removal is reported, not an error.
func TestReset_AlreadyGone(t *testing.T) {
	vault := t.TempDir()
	p := putFile(t, vault, "Templates/commands/wrap.md", "# x\n")
	withHook(t, func(string) { _ = os.Remove(p) })
	out, err := Reset(planFor(t, vault, "command", "wrap"))
	if err != nil {
		t.Fatal(err)
	}
	if !out[0].AlreadyGone || out[0].Removed || out[0].Err != nil {
		t.Errorf("outcome = %+v, want already gone", out[0])
	}
}

// TestReset_GoneBeforeRead: a planned file that disappears before the reset
// reads it is already gone, with no backup.
func TestReset_GoneBeforeRead(t *testing.T) {
	vault := t.TempDir()
	p := putFile(t, vault, "Templates/commands/wrap.md", "# x\n")
	plan := planFor(t, vault, "command", "wrap")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	out, err := Reset(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !out[0].AlreadyGone || out[0].Backup != "" {
		t.Errorf("outcome = %+v", out[0])
	}
}

// TestReset_IgnoresEntriesWithNoVaultCopy: Unneeded entries are not acted on.
func TestReset_IgnoresEntriesWithNoVaultCopy(t *testing.T) {
	out, err := Reset(planFor(t, t.TempDir(), "command", ""))
	if err != nil || len(out) != 0 {
		t.Errorf("Reset over an empty vault = %+v, %v", out, err)
	}
}

// TestReset_SkillPerFileOutcomes: each override file of a skill gets its own
// backup, a mirror file none, and a vault-only extra file is not touched.
func TestReset_SkillPerFileOutcomes(t *testing.T) {
	vault := t.TempDir()
	putFile(t, vault, "Templates/skills/startup-analyst/SKILL.md", "---\nname: x\n---\nmine\n")
	putFile(t, vault, "Templates/skills/startup-analyst/references/capex-opex.md", "mine\n")
	putFile(t, vault, "Templates/skills/startup-analyst/references/funding-sources.md",
		embedded(t, "skill:startup-analyst/references/funding-sources.md"))
	extra := putFile(t, vault, "Templates/skills/startup-analyst/references/my-notes.md", "mine\n")

	out, err := Reset(planFor(t, vault, "skill", "startup-analyst"))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]ResetOutcome{}
	for _, o := range out {
		by[o.Name] = o
	}
	if len(out) != 3 {
		t.Fatalf("outcomes = %+v, want 3 (the files with a vault copy)", out)
	}
	for _, n := range []string{"startup-analyst/SKILL.md", "startup-analyst/references/capex-opex.md"} {
		if o := by[n]; !o.Removed || o.Backup == "" || o.Mirror {
			t.Errorf("%s: %+v, want removed with a backup", n, o)
		}
	}
	if o := by["startup-analyst/references/funding-sources.md"]; !o.Removed || !o.Mirror || o.Backup != "" {
		t.Errorf("mirror: %+v", o)
	}
	if _, err := os.Stat(extra); err != nil {
		t.Errorf("the vault-only extra file was touched: %v", err)
	}
	if !ShimSourceRemoved(out) {
		t.Error("a removed SKILL.md is a shim source")
	}
	if ShimSourceRemoved([]ResetOutcome{{Rel: "Templates/skills/startup-analyst/references/capex-opex.md", Removed: true}}) {
		t.Error("a reference is not a shim source")
	}
}

// TestReset_RefusesSymlinkedPath is M1 and N3: a symlink in any component of
// the path refuses the whole call before anything is written — including one
// found after another file of the same skill was already checked.
func TestReset_RefusesSymlinkedPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, vault string) (resourceType, only string)
	}{
		{"file", func(t *testing.T, vault string) (string, string) {
			target := putFile(t, vault, "Projects/p/commands/wrap.md", "# project wrap\n")
			if err := os.MkdirAll(filepath.Join(vault, "Templates", "commands"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(vault, "Templates", "commands", "wrap.md")); err != nil {
				t.Fatal(err)
			}
			return "command", "wrap"
		}},
		{"skill-directory", func(t *testing.T, vault string) (string, string) {
			putFile(t, vault, "Projects/p/skills/chair/SKILL.md", "---\nname: chair\n---\nproject chair\n")
			if err := os.MkdirAll(filepath.Join(vault, "Templates", "skills"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(vault, "Projects", "p", "skills", "chair"), filepath.Join(vault, "Templates", "skills", "chair")); err != nil {
				t.Fatal(err)
			}
			return "skill", "chair"
		}},
		{"commands-directory", func(t *testing.T, vault string) (string, string) {
			putFile(t, vault, "Elsewhere/wrap.md", "# elsewhere\n")
			if err := os.MkdirAll(filepath.Join(vault, "Templates"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(vault, "Elsewhere"), filepath.Join(vault, "Templates", "commands")); err != nil {
				t.Fatal(err)
			}
			return "command", "wrap"
		}},
		{"references-after-skill-md", func(t *testing.T, vault string) (string, string) {
			// SKILL.md is a plain override and sorts first; the references
			// directory is a link. N3: nothing — not even SKILL.md's backup —
			// may be written.
			putFile(t, vault, "Templates/skills/startup-analyst/SKILL.md", "---\nname: x\n---\nmine\n")
			putFile(t, vault, "Elsewhere/refs/capex-opex.md", "mine\n")
			if err := os.Symlink(filepath.Join(vault, "Elsewhere", "refs"),
				filepath.Join(vault, "Templates", "skills", "startup-analyst", "references")); err != nil {
				t.Fatal(err)
			}
			return "skill", "startup-analyst"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := t.TempDir()
			rt, only := tc.setup(t, vault)
			plan := planFor(t, vault, rt, only)
			before := digest(t, vault)
			_, err := Reset(plan)
			if !errors.Is(err, ErrUnsafeResetPath) {
				t.Fatalf("err = %v, want ErrUnsafeResetPath", err)
			}
			if !strings.Contains(err.Error(), "symlink") {
				t.Errorf("the refusal does not say why: %v", err)
			}
			if after := digest(t, vault); after != before {
				t.Errorf("the vault changed under a refused reset:\n--- before\n%s\n--- after\n%s", before, after)
			}
		})
	}
}

// TestReset_RefusesNonRegularFile: a directory where a template file belongs
// is refused, never removed.
func TestReset_RefusesNonRegularFile(t *testing.T) {
	vault := t.TempDir()
	dir := filepath.Join(vault, "Templates", "commands", "wrap.md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c := Change{Name: "wrap", ResourceType: "command", Kind: ChangeOverride, VaultPath: dir, VaultRoot: vault}
	if _, err := Reset([]Change{c}); !errors.Is(err, ErrUnsafeResetPath) || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want a not-a-regular-file refusal", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Errorf("the directory was touched: %v", err)
	}
}

// TestReset_SymlinkedVaultRootIsNotRefused: the check is made under the
// resolved root, so a vault reached through a symlinked root resets normally.
func TestReset_SymlinkedVaultRootIsNotRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need a privilege on Windows")
	}
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "vault-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	putFile(t, real, "Templates/commands/wrap.md", "# mine\n")
	out, err := Reset(planFor(t, link, "command", "wrap"))
	if err != nil {
		t.Fatalf("Reset through a symlinked root: %v", err)
	}
	if !out[0].Removed {
		t.Errorf("outcome = %+v", out[0])
	}
	if _, err := os.Stat(filepath.Join(real, filepath.FromSlash(out[0].Backup))); err != nil {
		t.Errorf("backup not under the real root: %v", err)
	}
}

// TestReset_RemovalFailureIsPerFile: a removal that fails (a read-only
// directory) is recorded on that file's outcome and the rest continue; the
// backup stays, so a rerun reuses it.
func TestReset_RemovalFailureIsPerFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX directory permissions and a non-root user")
	}
	vault := t.TempDir()
	putFile(t, vault, "Templates/skills/startup-analyst/SKILL.md", "---\nname: x\n---\nmine\n")
	putFile(t, vault, "Templates/skills/startup-analyst/references/capex-opex.md", "mine\n")
	refs := filepath.Join(vault, "Templates", "skills", "startup-analyst", "references")
	// The backup must be writable, the removal not: pre-create the backup,
	// then make the directory read-only.
	if _, err := templates.PreserveBackup(vault, "Templates/skills/startup-analyst/references/capex-opex.md", []byte("mine\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(refs, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(refs, 0o755) })

	out, err := Reset(planFor(t, vault, "skill", "startup-analyst"))
	if err != nil {
		t.Fatal(err)
	}
	var failed, removed int
	for _, o := range out {
		switch {
		case o.Err != nil:
			failed++
			if !o.BackupReused {
				t.Errorf("%s: backup was not reused: %+v", o.Rel, o)
			}
		case o.Removed:
			removed++
		}
	}
	if failed != 1 || removed != 1 {
		t.Errorf("failed=%d removed=%d, want 1 and 1: %+v", failed, removed, out)
	}
}

func digest(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if strings.HasPrefix(rel, ".vp-locks") {
			return nil
		}
		info, _ := os.Lstat(p)
		fmt.Fprintf(&b, "%s %s", rel, info.Mode())
		if info.Mode().IsRegular() {
			data, _ := os.ReadFile(p)
			fmt.Fprintf(&b, " %s", data)
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestCheckResetPaths: the check-only phase 1 a dry run calls refuses exactly
// what Reset refuses, and writes nothing.
func TestCheckResetPaths(t *testing.T) {
	vault := t.TempDir()
	putFile(t, vault, "Templates/commands/wrap.md", "# mine\n")
	if err := CheckResetPaths(planFor(t, vault, "command", "wrap")); err != nil {
		t.Errorf("a plain override: %v", err)
	}
	if err := CheckResetPaths(planFor(t, vault, "command", "restart")); err != nil {
		t.Errorf("no vault copy: %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	target := putFile(t, vault, "Projects/p/commands/restart.md", "# project\n")
	if err := os.Symlink(target, filepath.Join(vault, "Templates", "commands", "restart.md")); err != nil {
		t.Fatal(err)
	}
	before := digest(t, vault)
	if err := CheckResetPaths(planFor(t, vault, "command", "restart")); !errors.Is(err, ErrUnsafeResetPath) {
		t.Errorf("err = %v, want ErrUnsafeResetPath", err)
	}
	if digest(t, vault) != before {
		t.Error("CheckResetPaths wrote something")
	}
}
