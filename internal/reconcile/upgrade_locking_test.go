// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// seedVaultProject creates a vault root with Projects/<slug>/config.toml holding
// body, and returns (vaultRoot, cfgPath).
func seedVaultProject(t *testing.T, slug, body string) (string, string) {
	t.Helper()
	root := t.TempDir()
	cfgPath := filepath.Join(root, "Projects", slug, "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, cfgPath
}

// vaultProjectTarget is the upgradeTarget VaultProjectReconciler.Apply uses.
func vaultProjectTarget() upgradeTarget {
	return upgradeTarget{
		canonicalText: storage.VaultProjectTemplateContent(),
		templateText:  storage.VaultProjectTemplateContent(),
	}
}

// TestApplyUpgrade_VaultBranchIsLocked drives concurrent ActionUpdate applies at
// one Projects/<slug>/config.toml and proves the vault branch serializes them.
//
// WHAT FAILS WITHOUT THE LOCK, precisely: the pre-fix vault branch wrote a
// fixed-name sibling `config.toml.tmp` and renamed it over the target. Two
// concurrent upgraders share that ONE temp path, so they interleave
// truncate/write on it and then race the rename — the loser's os.Rename fails
// with ENOENT (surfaced as a reconcile error) and, when the rename wins the
// race against the other writer's partial truncate, the bytes landing on
// config.toml are a short prefix that no longer parses as TOML. This test
// asserts all three: no reconciler errors, the result parses, and the result
// carries every canonical key plus the operator's own pre-existing key.
//
// Concurrency is run over several rounds with a fresh vault each round, because
// a single two-goroutine round is a coin flip; the point of the test is that it
// reddens reliably on an unlocked tree, not that it reproduces one interleaving.
func TestApplyUpgrade_VaultBranchIsLocked(t *testing.T) {
	const slug = "demo"
	const rounds = 25
	const writers = 8

	// A key the operator owns. UpgradeConfig preserves the user's text and
	// appends the missing canonical blocks, so this must survive every round; a
	// truncated write is the way it disappears.
	const seed = "# project overrides\n[meta]\nkind = \"vault-project\"\n"

	for round := 0; round < rounds; round++ {
		root, cfgPath := seedVaultProject(t, slug, seed)
		r := NewVaultProject(storage.NewVault(root), slug)
		plan := Plan{Actions: []Action{{Kind: ActionUpdate, Target: cfgPath}}}

		var wg sync.WaitGroup
		errs := make(chan error, writers)
		start := make(chan struct{})
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				rep, err := r.Apply(context.Background(), plan)
				if err != nil {
					errs <- err
					return
				}
				for _, e := range rep.Errors {
					errs <- e
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("round %d: concurrent Apply reported an error: %v", round, err)
		}

		data, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("round %d: read upgraded config: %v", round, err)
		}
		var parsed map[string]any
		if err := toml.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("round %d: upgraded config is not valid TOML: %v\n---\n%s", round, err, data)
		}

		// The union: the operator's own key AND every canonical key the upgrade
		// was supposed to add.
		if !strings.Contains(string(data), `kind = "vault-project"`) {
			t.Fatalf("round %d: operator's pre-existing key was lost:\n%s", round, data)
		}
		canonical, err := storage.CanonicalKeysFrom(storage.VaultProjectTemplateContent())
		if err != nil {
			t.Fatal(err)
		}
		present := storage.PresentKeys(string(data))
		if missing := storage.MissingKeys(canonical, present); len(missing) != 0 {
			t.Fatalf("round %d: upgraded config still missing %v:\n%s", round, missing, data)
		}

		// atomicfile.Write owns the temp file and removes it; no fixed-name
		// sidecar may survive for a second writer to collide with.
		if _, err := os.Stat(cfgPath + ".tmp"); !os.IsNotExist(err) {
			t.Fatalf("round %d: config.toml.tmp survived the vault upgrade (err=%v)", round, err)
		}
	}
}

// TestApplyUpgrade_VaultBranchWritesNoBak pins the ruling that the vault branch
// emits no .bak.
//
// The rationale is not "backups are useless": it is that *.bak is in
// storage.CanonicalGitignorePatterns, so a .bak written inside the vault is
// never committed and never synced. It is host-local litter sitting next to a
// file whose real recoverable pre-image is the committed config.toml. If a
// later change helpfully restores the backup, this test is what says no.
func TestApplyUpgrade_VaultBranchWritesNoBak(t *testing.T) {
	root, cfgPath := seedVaultProject(t, "demo", "# project overrides\n")

	added, err := applyUpgrade(root, cfgPath, vaultProjectTarget())
	if err != nil {
		t.Fatal(err)
	}
	if added == 0 {
		t.Fatal("expected the vault upgrade to add missing keys, added 0")
	}
	if _, err := os.Stat(cfgPath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("vault branch wrote config.toml.bak (err=%v); *.bak is gitignored in the vault and is never a durable backup", err)
	}
	if _, err := os.Stat(cfgPath + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("vault branch left config.toml.tmp behind (err=%v)", err)
	}
}

// TestApplyUpgrade_HostLocalBranchUnchanged guards the deliberate asymmetry: the
// host-local branch (vaultRoot == "") keeps the exact raw
// backup + temp + rename it has always had. It is NOT routed through atomicfile,
// so it must not pick up atomicfile's permission handling, and it must keep the
// .bak the vault branch drops — a host-local config has no committed copy
// standing behind it, so the backup is its only pre-image.
func TestApplyUpgrade_HostLocalBranchUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	const before = "# project overrides\n"
	if err := os.WriteFile(cfgPath, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	target := vaultProjectTarget()

	// The bytes main's implementation produces, derived from the same storage
	// primitives the writer uses rather than from a golden blob, so the
	// assertion tracks the upgrade rules instead of freezing one rendering.
	canonical, err := storage.CanonicalKeysFrom(target.canonicalText)
	if err != nil {
		t.Fatal(err)
	}
	missing := storage.MissingKeys(canonical, storage.PresentKeys(before))
	want := storage.UpgradeConfig(before, missing, storage.ParseTemplateBlocks(target.templateText))

	added, err := applyUpgrade("", cfgPath, target)
	if err != nil {
		t.Fatal(err)
	}
	if added == 0 {
		t.Fatal("expected the host-local upgrade to add missing keys, added 0")
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("host-local upgrade content changed\n got: %q\nwant: %q", got, want)
	}
	st, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o644 {
		t.Errorf("host-local upgrade mode = %v, want 0644", st.Mode().Perm())
	}

	// The .bak must still be written, and must hold the PRE-upgrade bytes.
	bak, err := os.ReadFile(cfgPath + ".bak")
	if err != nil {
		t.Fatalf("host-local branch must still write a .bak: %v", err)
	}
	if string(bak) != before {
		t.Errorf("host-local .bak = %q, want the pre-upgrade bytes %q", bak, before)
	}
	bst, err := os.Stat(cfgPath + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if bst.Mode().Perm() != 0o644 {
		t.Errorf("host-local .bak mode = %v, want 0644", bst.Mode().Perm())
	}

	// No surface stamp: host-local configs live outside the vault.
	if _, err := os.Stat(filepath.Join(dir, ".surface")); !os.IsNotExist(err) {
		t.Errorf("host-local upgrade left a .surface stamp (err=%v)", err)
	}
	// And no leftover temp.
	if _, err := os.Stat(cfgPath + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("host-local upgrade left config.toml.tmp behind (err=%v)", err)
	}
}

// TestApplyUpgrade_VaultBranchIsNotStrandedByTheWriteRefusal is the second
// stranding pin for the vaultfs refuse-gate on Projects/<slug>/config.toml.
//
// applyUpgradeVault is the writer the original plan's safety argument missed:
// it rewrites that exact path on `vp config upgrade --project` and
// `vp config sync --tier project`. It survives the refusal because it writes
// through storage.LockedUpdate, which reaches atomicfile.Write and never enters
// vaultfs — the same route WriteVaultProjectConfig takes, for the same reason.
//
// Sabotage that reds it: route this branch's write through vaultfs instead of
// LockedUpdate. The guard-the-guard half below is what stops the test passing
// if the gate is deleted outright.
func TestApplyUpgrade_VaultBranchIsNotStrandedByTheWriteRefusal(t *testing.T) {
	const slug = "demo"
	root, cfgPath := seedVaultProject(t, slug, "# project overrides\n")

	added, err := applyUpgrade(root, cfgPath, vaultProjectTarget())
	if err != nil {
		t.Fatalf("vault config upgrade must still work after the refusal lands: %v", err)
	}
	if added == 0 {
		t.Fatal("expected the vault upgrade to add missing keys, added 0")
	}
	if _, err := toml.DecodeFile(cfgPath, &struct{}{}); err != nil {
		t.Fatalf("upgraded config does not parse: %v", err)
	}

	// Guard the guard: the path just rewritten is the one the generic tools
	// refuse.
	rel := filepath.Join("Projects", slug, "config.toml")
	if !vaultfs.IsVaultProjectConfigPath(rel) {
		t.Fatalf("vaultfs does not classify %q as the vault project config", rel)
	}
	if _, werr := vaultfs.Write(root, rel, "clobbered", ""); !errors.Is(werr, vaultfs.ErrRefusedPath) {
		t.Errorf("vaultfs.Write(%q) = %v, want ErrRefusedPath", rel, werr)
	}
}
