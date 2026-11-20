// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// W2: a write whose vaultRoot is a temp vault but whose target lies under the
// ambient (operator's configured) vault is refused, and nothing is created
// there. Two temp dirs; no rename, no symlink.
func TestWriteAtomicRefusesATargetInsideTheAmbientVaultWithADifferentVaultRoot(t *testing.T) {
	vault, amb := t.TempDir(), t.TempDir()
	testutil.InitProject(t, vault, "p")
	testutil.InitProject(t, amb, "p")

	// Control: a write inside the temp vault succeeds and creates its directory.
	ctl := filepath.Join(vault, "Projects", "p", "sub", "ok.md")
	if err := Write(vault, ctl, []byte("x\n")); err != nil {
		t.Fatalf("control write inside a temp vault: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(ctl)); err != nil {
		t.Fatalf("control did not create its directory: %v", err)
	}

	t.Cleanup(surface.SetAmbientVaultRoot(amb))
	target := filepath.Join(amb, "Projects", "p", "newdir", "x.md")
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg, _ = r.(string)
			}
		}()
		_ = Write(vault, target, []byte("x\n"))
	}()
	if !strings.Contains(msg, "a write inside the CONFIGURED vault") {
		t.Fatalf("a cross-vault write into the ambient vault was not refused, got %q", msg)
	}
	if _, err := os.Lstat(filepath.Dir(target)); err == nil {
		t.Error("a directory was created in the ambient vault before the refusal")
	}
}
