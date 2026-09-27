// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// G16: `vp config sync`'s vault tier names a REJECTED binding as rejected, not
// as an absent config with the `vp init` remedy. MUTATION CONTRACT: key the
// arm on ErrSwallowedVaultPath again and this goes RED.
func TestVaultPlanNamesARejectedBinding(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	cfgDir := filepath.Join(home, ".config", "vibe-palace")
	proj := filepath.Join(tmp, "qa")
	for _, d := range []string{cfgDir, proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte("vault_path = \""+filepath.Join(tmp, "live")+
		"\"\n\n[project_vaults]\nqa = \""+filepath.Join(tmp, "no-such-vault")+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".vibe-palace.toml"), []byte("[project]\nname = \"qa\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := NewVault(proj, VaultSeed{}).Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || !strings.Contains(plan.Actions[0].Summary, "REJECTED") {
		t.Errorf("plan = %+v, want one Skip naming the rejected binding", plan.Actions)
	}
}
