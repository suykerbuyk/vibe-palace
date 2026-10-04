// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// archiveGateVault is a vault with an initialised project p, a phantom ph
// (memory/ only) and a departure record for q. fresh is absent.
func archiveGateVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testutil.InitProject(t, root, "p")
	for rel, body := range map[string]string{
		"Projects/ph/memory/x.md":  "m\n",
		"Audits/departures/q.json": `{"format":"1","slug":"q","kind":"deleted","date":"2026-10-01"}` + "\n",
	} {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func gateCreate(root, project string) (*CreateResult, error) {
	return Create(CreateOptions{
		Adapter:       InlineAdapterName,
		SessionID:     "session-gate",
		SourceContent: []byte(sampleInlineJSONL),
		VaultRoot:     root,
		ProjectSlug:   project,
		Now:           time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		VPVersion:     "0.0.0-test",
	})
}

func gateMissing(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a refused archive (err %v)", rel, err)
		}
	}
}

// An archive into an absent or phantom project is refused before its
// transcripts directory is made.
func TestCreateRefusesUninitialisedProject(t *testing.T) {
	for _, project := range []string{"fresh", "ph"} {
		t.Run(project, func(t *testing.T) {
			root := archiveGateVault(t)
			_, err := gateCreate(root, project)
			if !errors.Is(err, projectdir.ErrUninitialisedProject) {
				t.Fatalf("err = %v, want ErrUninitialisedProject", err)
			}
			gateMissing(t, root, "Projects/fresh", "palace/fresh", "Projects/ph/transcripts", "Projects/ph/.surface", "palace/ph")
		})
	}
}

// The counterpart: an archive into an initialised project lands.
func TestCreateAllowsInitialisedProject(t *testing.T) {
	root := archiveGateVault(t)
	res, err := gateCreate(root, "p")
	if err != nil {
		t.Fatalf("Create into initialised project: %v", err)
	}
	if _, err := os.Stat(res.ManifestPath); err != nil {
		t.Errorf("manifest missing: %v", err)
	}
}

// A departed project answers with the departure refusal, not the
// uninitialised one, though q is also absent.
func TestCreateDepartedRefusalWinsOverUninitialised(t *testing.T) {
	root := archiveGateVault(t)
	_, err := gateCreate(root, "q")
	if !errors.Is(err, departedpath.ErrDeparted) {
		t.Fatalf("err = %v, want ErrDeparted", err)
	}
	if errors.Is(err, projectdir.ErrUninitialisedProject) {
		t.Errorf("err = %v also reads as ErrUninitialisedProject", err)
	}
	gateMissing(t, root, "Projects/q", "palace/q")
}
