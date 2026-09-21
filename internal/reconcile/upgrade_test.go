// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The vault branch of applyUpgrade — and its locking, no-.bak and
// not-stranded-by-the-refusal tests — went with the per-project vault config it
// upgraded (task move-per-project-config-out-of-the-shared-vault).

// TestApplyUpgrade_HostLocalBranchUnchanged guards applyUpgrade's one remaining
// path: the host-local configs it upgrades keep the exact raw
// backup + temp + rename they have always had. It is NOT routed through
// atomicfile, so it must not pick up atomicfile's permission handling, and it
// must keep its .bak — a host-local config has no committed copy standing
// behind it, so the backup is its only pre-image.
func TestApplyUpgrade_HostLocalBranchUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	const before = "# project overrides\n"
	if err := os.WriteFile(cfgPath, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}

	target := upgradeTarget{
		canonicalText: storage.CwdProjectTemplateContent(),
		templateText:  storage.CwdProjectTemplateContent(),
	}

	// The bytes main's implementation produces, derived from the same storage
	// primitives the writer uses rather than from a golden blob, so the
	// assertion tracks the upgrade rules instead of freezing one rendering.
	canonical, err := storage.CanonicalKeysFrom(target.canonicalText)
	if err != nil {
		t.Fatal(err)
	}
	missing := storage.MissingKeys(canonical, storage.PresentKeys(before))
	want := storage.UpgradeConfig(before, missing, storage.ParseTemplateBlocks(target.templateText))

	added, err := applyUpgrade(cfgPath, target)
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
