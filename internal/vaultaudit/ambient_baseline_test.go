// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// A5: Baseline.Save aimed at the ambient vault is refused before Audits/ exists.
// A raw MkdirAll used to make that directory before atomicfile's tripwire ran;
// the assertion that discriminates is the directory's absence. Control: a
// non-ambient vault gets Audits/baseline.json.
func TestBaselineSaveDoesNotCreateAuditsInTheAmbientVault(t *testing.T) {
	ctl := t.TempDir()
	cp := filepath.Join(ctl, "Audits", "baseline.json")
	if err := (Baseline{Dimensions: map[string]DimensionBaseline{}}).Save(ctl, cp); err != nil {
		t.Fatalf("control save: %v", err)
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatalf("control did not write the baseline: %v", err)
	}

	amb := t.TempDir()
	t.Cleanup(surface.SetAmbientVaultRoot(amb))
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
			}
		}()
		_ = (Baseline{Dimensions: map[string]DimensionBaseline{}}).Save(amb, filepath.Join(amb, "Audits", "baseline.json"))
	}()
	if !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("a baseline save into the ambient vault was not refused, got %q", msg)
	}
	if _, err := os.Lstat(filepath.Join(amb, "Audits")); err == nil {
		t.Error("Audits/ was created in the ambient vault before the refusal")
	}
}
