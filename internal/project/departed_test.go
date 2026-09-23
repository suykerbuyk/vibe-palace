// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// writeDeparture puts a departure record into a vault the way
// storage.RecordDeparture would, without importing storage.
func writeDeparture(t *testing.T, vault string, r departure.Record) {
	t.Helper()
	b, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(vault, filepath.FromSlash(departure.RelPath(r.Slug)))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// T4. On a vault with NO git, the history fallback cannot answer, so only the
// record can refuse — and it says where the project went and what to set.
func TestRequireKnownProject_RecordRedirectsOnANonGitVault(t *testing.T) {
	vault := t.TempDir()
	writeDeparture(t, vault, departure.Record{Slug: "old", Kind: departure.Renamed, To: "new", Date: "2026-09-23"})

	err := RequireKnownProject("old", vault, markerRepo(t, "old"))
	if err == nil {
		t.Fatal("a marker naming a slug with a departure record must not authorize resurrecting it")
	}
	for _, want := range []string{`renamed to "new"`, `set [project].name = "new"`, "on 2026-09-23", "still names it", "vp init"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must contain %q, got %q", want, err)
		}
	}
	if d, ok := Departed(vault, "old"); !ok || d.Source != "record" {
		t.Errorf("Departed = %+v %v, want the record as the source", d, ok)
	}
	// A rename chain redirects to its live end.
	writeDeparture(t, vault, departure.Record{Slug: "new", Kind: departure.Renamed, To: "newest"})
	err = RequireKnownProject("old", vault, markerRepo(t, "old"))
	if err == nil || !strings.Contains(err.Error(), `set [project].name = "newest"`) || !strings.Contains(err.Error(), `by way of "new"`) {
		t.Errorf("a chain must redirect to its live end, got %v", err)
	}
}

// T5. A move to another vault says to repoint vault_path, with and without a
// recorded destination.
func TestRequireKnownProject_MovedToVaultSaysSetVaultPath(t *testing.T) {
	vault := t.TempDir()
	writeDeparture(t, vault, departure.Record{Slug: "labelled", Kind: departure.MovedToVault, To: "git@example.com:me/quantum.git"})
	writeDeparture(t, vault, departure.Record{Slug: "bare", Kind: departure.MovedToVault})

	err := RequireKnownProject("labelled", vault, markerRepo(t, "labelled"))
	if err == nil || !strings.Contains(err.Error(), `moved to another vault, "git@example.com:me/quantum.git"`) ||
		!strings.Contains(err.Error(), "point vault_path at that vault") {
		t.Errorf("labelled move refusal wrong: %v", err)
	}
	err = RefuseDeparted(vault, "bare")
	if err == nil || !strings.Contains(err.Error(), "a destination that was not recorded") ||
		!strings.Contains(err.Error(), "point vault_path at that vault") {
		t.Errorf("unlabelled move refusal wrong: %v", err)
	}
}
