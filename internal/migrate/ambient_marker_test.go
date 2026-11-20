// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// A4: the import marker writer creates palace/.local/imports/<p>/ through
// storage.EnsureDir. Aimed at the ambient vault it is refused before any palace/
// directory exists; the control writes the marker into a non-ambient vault.
func TestAppendMarkerIsRefusedBeforeCreatingDirectoriesInTheAmbientVault(t *testing.T) {
	ctl := t.TempDir()
	if err := appendMarker(storage.NewVault(ctl), "p", "s1", "src", "reason"); err != nil {
		t.Fatalf("control: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ctl, "palace", ".local", "imports", "p")); err != nil {
		t.Fatalf("control did not create the marker directory: %v", err)
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
		_ = appendMarker(storage.NewVault(amb), "p", "s1", "src", "reason")
	}()
	if !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("a marker write into the ambient vault was not refused, got %q", msg)
	}
	if _, err := os.Lstat(filepath.Join(amb, "palace")); err == nil {
		t.Error("palace/ was created in the ambient vault before the refusal")
	}
}
