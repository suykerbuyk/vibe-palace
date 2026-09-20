// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// TestVaultProjectConfigWriterIsNotStrandedByTheRefusal is the stranding pin
// for the vaultfs refuse-gate on Projects/<slug>/config.toml.
//
// The hazard the gate creates: anything that makes the vault file unwritable
// before every legitimate writer is gone strands `vp init`. It does not,
// because WriteVaultProjectConfig takes its own vaultlock and calls
// atomicfile.Write directly — it never enters vaultfs. That is a property of
// the code, not a coincidence, and this test is what makes it one that cannot
// be quietly removed.
//
// The sabotage this reds under is the one a later reader is most likely to
// make: ADR-003 pushes writers toward the vaultfs funnel, so a "comply with
// ADR-003" cleanup routing this writer through vaultfs.Create or vaultfs.Write
// looks like tidying. It would break `vp init` on every project that does not
// already have the file.
//
// The second half — asserting vaultfs DOES refuse the very path the writer
// just wrote — is what stops this test passing for the wrong reason. Without
// it, deleting the gate entirely would leave this green.
func TestVaultProjectConfigWriterIsNotStrandedByTheRefusal(t *testing.T) {
	v := NewVault(t.TempDir())

	path, wrote, err := v.WriteVaultProjectConfig("alpha")
	if err != nil {
		t.Fatalf("WriteVaultProjectConfig must still work after the refusal lands: %v", err)
	}
	if !wrote {
		t.Fatalf("WriteVaultProjectConfig reported no write on a fresh vault")
	}
	if _, serr := os.Stat(path); serr != nil {
		t.Fatalf("stat written config: %v", serr)
	}

	// Guard the guard: the path the writer just wrote is exactly the path the
	// generic tools refuse. If this stops being true, the test above has
	// stopped testing anything.
	rel := filepath.Join("Projects", "alpha", "config.toml")
	if !vaultfs.IsVaultProjectConfigPath(rel) {
		t.Fatalf("vaultfs does not classify %q as the vault project config — the gate and the writer disagree", rel)
	}
	if _, werr := vaultfs.Write(v.Root, rel, "clobbered", ""); !errors.Is(werr, vaultfs.ErrRefusedPath) {
		t.Errorf("vaultfs.Write(%q) = %v, want ErrRefusedPath", rel, werr)
	}

	// And the writer is still idempotent through the same route.
	if _, wrote2, err2 := v.WriteVaultProjectConfig("alpha"); err2 != nil || wrote2 {
		t.Errorf("second WriteVaultProjectConfig = (wrote=%v, err=%v), want (false, nil)", wrote2, err2)
	}
}
