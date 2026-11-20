// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// A8: ProjectRoot is a caller option, so the backup directory can land inside a
// vault project tree. Under the tripwire that is refused before the directory (or
// the .bak file) exists. Control: a non-ambient ProjectRoot gets both.
func TestRewriteSourceFileBackupIsRefusedInsideTheAmbientVault(t *testing.T) {
	ps := func(root string) PlannedSource {
		return PlannedSource{Source: SourceForDisplayName("AGENTS.md"), DisplayPath: "AGENTS.md", AbsPath: filepath.Join(root, "AGENTS.md"), Original: []byte("original\n")}
	}
	run := func(root string) (string, error) {
		return rewriteSourceFile(ps(root), WriteOptions{ProjectRoot: root, Now: time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)})
	}

	ctl := t.TempDir()
	if err := os.WriteFile(filepath.Join(ctl, "AGENTS.md"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctl); err != nil {
		t.Fatalf("control: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ctl, ".vibe-palace")); err != nil {
		t.Fatalf("control did not create the backup directory: %v", err)
	}

	amb := t.TempDir()
	projectRoot := filepath.Join(amb, "Projects", "p") // a ProjectRoot pointing into the ambient vault
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.SetAmbientVaultRoot(amb))
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
			}
		}()
		_, _ = run(projectRoot)
	}()
	if !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("a backup into the ambient vault was not refused, got %q", msg)
	}
	if _, err := os.Lstat(filepath.Join(projectRoot, ".vibe-palace")); err == nil {
		t.Error(".vibe-palace was created inside the ambient vault before the refusal")
	}
}
