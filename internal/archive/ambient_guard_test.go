// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

func ambientCreate(root string) error {
	_, err := Create(CreateOptions{
		Adapter:       InlineAdapterName,
		SessionID:     "session-ambient",
		SourceContent: []byte(sampleInlineJSONL),
		VaultRoot:     root,
		ProjectSlug:   "p",
		Now:           time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
		VPVersion:     "0.0.0-test",
	})
	return err
}

// A1: archive create aimed at the ambient vault is refused BEFORE anything is
// created in it: no transcripts/ directory, no .vp-locks. The control archives
// into a non-ambient initialised vault and DOES create transcripts/.
func TestCreateIsRefusedBeforeCreatingAnythingInTheAmbientVault(t *testing.T) {
	ctl := t.TempDir()
	testutil.InitProject(t, ctl, "p")
	if err := ambientCreate(ctl); err != nil {
		t.Fatalf("control archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ctl, "Projects", "p", "transcripts")); err != nil {
		t.Fatalf("control did not create transcripts/: %v", err)
	}

	amb := t.TempDir()
	testutil.InitProject(t, amb, "p")
	t.Cleanup(surface.SetAmbientVaultRoot(amb))
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg = fmt.Sprint(r)
			}
		}()
		_ = ambientCreate(amb)
	}()
	if !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("archive create at the ambient vault was not refused, got %q", msg)
	}
	for _, rel := range []string{"Projects/p/transcripts", ".vp-locks"} {
		if _, err := os.Lstat(filepath.Join(amb, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s was created in the ambient vault before the refusal", rel)
		}
	}
}

// Equivalence, checked rather than assumed: routing the transcripts directory
// through EnsureVaultDir must not change what a refused archive says. The
// refusal is returned unwrapped, byte for byte what projectdir words (the text
// the old direct call returned), and still carries its sentinel; only a genuine
// mkdir failure keeps the "create transcripts dir:" prefix.
func TestCreateRefusalTextIsExactlyProjectdirsAndMkdirFailureKeepsItsPrefix(t *testing.T) {
	root := archiveGateVault(t)
	for _, project := range []string{"fresh", "ph", "q"} {
		t.Run("refusal/"+project, func(t *testing.T) {
			dir := filepath.Join(root, "Projects", project, "transcripts")
			want := projectdir.RefuseUninitialisedDirAbs(root, dir)
			if want == nil {
				t.Fatalf("fixture: projectdir does not refuse %s, so the equivalence proves nothing", project)
			}
			_, got := gateCreate(root, project)
			if got == nil {
				t.Fatal("archive create was not refused")
			}
			if got.Error() != want.Error() {
				t.Errorf("refusal text changed:\n got: %q\nwant: %q", got.Error(), want.Error())
			}
			if strings.HasPrefix(got.Error(), "create transcripts dir:") {
				t.Errorf("a refusal must not carry the mkdir-failure prefix: %q", got.Error())
			}
			if errors.Is(want, projectdir.ErrUninitialisedProject) && !errors.Is(got, projectdir.ErrUninitialisedProject) {
				t.Errorf("the ErrUninitialisedProject sentinel was lost: %v", got)
			}
		})
	}

	t.Run("a genuine mkdir failure keeps the prefix", func(t *testing.T) {
		vault := t.TempDir()
		testutil.InitProject(t, vault, "p")
		// transcripts is a regular FILE, so MkdirAll fails for real (not a directory).
		if err := os.WriteFile(filepath.Join(vault, "Projects", "p", "transcripts"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := gateCreate(vault, "p")
		if err == nil || !strings.HasPrefix(err.Error(), "create transcripts dir:") {
			t.Fatalf("a real mkdir failure = %v, want the \"create transcripts dir:\" prefix", err)
		}
	})
}
