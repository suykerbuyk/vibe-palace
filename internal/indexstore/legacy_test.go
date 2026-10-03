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
