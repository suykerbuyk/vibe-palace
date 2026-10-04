// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// gitIn runs git in the vault with no host or global config.
func gitIn(t *testing.T, v *storage.Vault, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", v.Root, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// The legacy ledger is deleted only when git tracks no drawer of the project:
// kept while a drawer is tracked (even one deleted from this host's disk), kept
// in a vault that is not a git repository, deleted once the drawers are
// untracked (whatever untracked drawer files this host still has on disk), and
// a missing ledger is not an error.
func TestDeleteLegacyLedgerIfUntracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	v := newVault(t)
	ledger, _ := v.IngestedArchivesFile("alpha")
	drawer := filepath.Join(v.Root, "palace", "alpha", "drawers", "w", "r.jsonl")
	if err := os.MkdirAll(filepath.Dir(drawer), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, drawer)
	if err := os.WriteFile(ledger, []byte(`{"source_sha256":"A"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check := func(what string, wantDeleted, wantGone bool) {
		t.Helper()
		deleted, err := DeleteLegacyLedgerIfUntracked(v, "alpha")
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		_, statErr := os.Stat(ledger)
		if deleted != wantDeleted || os.IsNotExist(statErr) != wantGone {
			t.Fatalf("%s: deleted=%v, ledger gone=%v; want %v and %v", what, deleted, os.IsNotExist(statErr), wantDeleted, wantGone)
		}
	}

	check("not a git repository", false, false)
	gitIn(t, v, "init", "-q")
	gitIn(t, v, "add", "palace/alpha/drawers")
	gitIn(t, v, "commit", "-qm", "drawers")
	check("a tracked drawer", false, false)
	if err := os.Remove(drawer); err != nil {
		t.Fatal(err)
	}
	check("a tracked drawer missing from this host's disk", false, false)
	gitIn(t, v, "rm", "-q", "--cached", "-r", "palace/alpha/drawers")
	gitIn(t, v, "commit", "-qm", "untrack")
	// An untracked drawer on this host's disk (an old binary wrote it) is not
	// a tracked one: the ledger still goes.
	touch(t, drawer)
	check("no tracked drawer, an untracked one on disk", true, true)
	check("no ledger", false, true)
}

// The migration marker is the second condition: with drawers still tracked, the
// marker deletes the legacy ledger and its absence keeps it. A malformed marker
// keeps it too (it reads as "not migrated", with a warning).
func TestDeleteLegacyLedgerOnTheMarker(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	v := newVault(t)
	ledger, _ := v.IngestedArchivesFile("alpha")
	drawer := filepath.Join(v.Root, "palace", "alpha", "drawers", "w", "r.jsonl")
	if err := os.MkdirAll(filepath.Dir(drawer), 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, drawer)
	gitIn(t, v, "init", "-q")
	gitIn(t, v, "add", "palace/alpha/drawers")
	gitIn(t, v, "commit", "-qm", "drawers")
	writeLedger := func() {
		t.Helper()
		if err := os.WriteFile(ledger, []byte(`{"source_sha256":"A"}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := filepath.Join(v.Root, ".vibe-palace", "vault.toml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what, toml  string
		wantDeleted bool
	}{
		{"tracked drawers, no marker", "format = 2\n", false},
		{"tracked drawers, a malformed marker", "format = 2\nauthored_only = 3\n", false},
		{"tracked drawers and the marker", "format = 2\nauthored_only = \"2026-10-03\"\n", true},
	} {
		writeLedger()
		if err := os.WriteFile(manifest, []byte(c.toml), 0o644); err != nil {
			t.Fatal(err)
		}
		deleted, err := DeleteLegacyLedgerIfUntracked(v, "alpha")
		if err != nil {
			t.Fatalf("%s: %v", c.what, err)
		}
		_, statErr := os.Stat(ledger)
		if deleted != c.wantDeleted || os.IsNotExist(statErr) != c.wantDeleted {
			t.Fatalf("%s: deleted=%v, gone=%v; want %v", c.what, deleted, os.IsNotExist(statErr), c.wantDeleted)
		}
	}
}

// A malformed marker keeps the legacy ledger even when no drawer is tracked:
// an unreadable vault.toml decides no deletion either way.
func TestMalformedMarkerKeepsTheLegacyLedger(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	v := newVault(t)
	ledger, _ := v.IngestedArchivesFile("alpha")
	if err := os.MkdirAll(filepath.Dir(ledger), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledger, []byte(`{"source_sha256":"A"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, v, "init", "-q")
	manifest := filepath.Join(v.Root, ".vibe-palace", "vault.toml")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("format = 2\nauthored_only = 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deleted, err := DeleteLegacyLedgerIfUntracked(v, "alpha")
	if err != nil || deleted {
		t.Fatalf("malformed marker, no tracked drawer: deleted=%v, %v; want kept", deleted, err)
	}
	if _, err := os.Stat(ledger); err != nil {
		t.Fatalf("the ledger is gone: %v", err)
	}
}
