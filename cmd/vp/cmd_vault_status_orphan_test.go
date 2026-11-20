// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// `vp vault status` printed `Working tree: clean` for a tree whose only dirt was
// an orphaned (or deferred) transcript archive, because its clean test and the
// report both dropped what the tidy scan had classified. These tests drive the
// real CLI over a real vault; every one has a clean control run first, so a
// fixture that is secretly dirty cannot pass for the right reason.

const statusOrphanZst = "Projects/p/transcripts/orphan.jsonl.zst"

func statusRun(t *testing.T, args ...string) string {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = cmdVaultStatus().Run(args) })
	if code != cli.ExitOK {
		t.Fatalf("vp vault status %v: exit %d\n%s", args, code, out)
	}
	return out
}

func writeArchive(t *testing.T, vaultDir, rel string, age time.Duration) {
	t.Helper()
	p := filepath.Join(vaultDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("zst\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestVaultStatusOrphanOnlyTreeIsNotClean(t *testing.T) {
	vaultDir := setupVaultWithOrigin(t)
	if out := statusRun(t, "--no-fetch"); !strings.Contains(out, "Working tree: clean") {
		t.Fatalf("control: an untouched vault must print `Working tree: clean`:\n%s", out)
	}

	writeArchive(t, vaultDir, statusOrphanZst, time.Hour)
	out := statusRun(t, "--no-fetch")
	t.Logf("vp vault status --no-fetch (orphan-only fixture):\n%s", out)
	if strings.Contains(out, "Working tree: clean") {
		t.Errorf("an orphan-only tree printed `Working tree: clean`:\n%s", out)
	}
	if want := storage.OrphanedNotice([]string{statusOrphanZst}); !strings.Contains(out, want) {
		t.Errorf("the orphan must be named with OrphanedNotice's exact wording.\nwant:\n%s\ngot:\n%s", want, out)
	}

	var rep storage.StatusReport
	if err := json.Unmarshal([]byte(statusRun(t, "--no-fetch", "--json")), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Dirt.Orphaned) != 1 || rep.Dirt.Orphaned[0] != statusOrphanZst || rep.Dirt.Clean() {
		t.Errorf("--json dirt = %+v, want orphaned=[%s] and not clean", rep.Dirt, statusOrphanZst)
	}
}

func TestVaultStatusDeferredOnlyTreeIsNotClean(t *testing.T) {
	vaultDir := setupVaultWithOrigin(t)
	if out := statusRun(t, "--no-fetch"); !strings.Contains(out, "Working tree: clean") {
		t.Fatalf("control: an untouched vault must print `Working tree: clean`:\n%s", out)
	}

	writeArchive(t, vaultDir, statusOrphanZst, 0) // fresh: still inside the pair grace
	out := statusRun(t, "--no-fetch")
	if strings.Contains(out, "Working tree: clean") {
		t.Errorf("a deferred-only tree printed `Working tree: clean`:\n%s", out)
	}
	if !strings.Contains(out, "Deferred 1 incomplete transcript pair (manifest pending)") {
		t.Errorf("the deferred archive must be named:\n%s", out)
	}
	if strings.Contains(out, "ORPHANED") {
		t.Errorf("a fresh archive is deferred, not orphaned:\n%s", out)
	}
}

// An artifact under a departed project's tree is never swept or committed, and
// the status of a vault holding only that must not read clean. The path has to
// be rendered by the CLI itself: it is the CLI's own copy of LeftDeparted into
// the TidyResult it prints that this pins.
func TestVaultStatusLeftDepartedOnlyTreeIsNotClean(t *testing.T) {
	vaultDir := setupVaultWithOrigin(t)
	rec, err := (departure.Record{Slug: "old", Kind: departure.MovedToVault, To: "git@example.invalid:q/v.git", Date: "2026-09-23"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	recPath := filepath.Join(vaultDir, filepath.FromSlash(departure.RelPath("old")))
	if err := os.MkdirAll(filepath.Dir(recPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, rec, 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, vaultDir, "add", "-A")
	gitIn(t, vaultDir, "commit", "-q", "-m", "record the departure of old")
	if out := statusRun(t, "--no-fetch"); !strings.Contains(out, "Working tree: clean") {
		t.Fatalf("control: with the departure committed and nothing else, the tree must print clean:\n%s", out)
	}

	const stray = "Projects/old/sessions/2026-09-27-aaaa-01.md"
	p := filepath.Join(vaultDir, filepath.FromSlash(stray))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("a stale session\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := statusRun(t, "--no-fetch")
	if strings.Contains(out, "Working tree: clean") {
		t.Errorf("a left-departed-only tree printed `Working tree: clean`:\n%s", out)
	}
	if !strings.Contains(out, "Left untouched") || !strings.Contains(out, stray+": left untouched: departed project old") {
		t.Errorf("the departed artifact must be rendered in the Left-untouched section:\n%s", out)
	}
}
