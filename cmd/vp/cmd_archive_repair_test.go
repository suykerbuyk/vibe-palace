// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// repairCLIOrphan builds an orphaned archive in the CLI's configured vault: a real
// zst from Create, its manifest removed, its mtime an hour old.
func repairCLIOrphan(t *testing.T) (vault, name string) {
	t.Helper()
	vault = setupTestVaultEnv(t)
	testutil.InitProject(t, vault, "p")
	res, err := archive.Create(archive.CreateOptions{
		Adapter: archive.InlineAdapterName, SessionID: "sess-cli-repair",
		SourceContent: []byte(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}` + "\n"),
		VaultRoot:     vault, ProjectSlug: "p", Now: time.Now().Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(res.ManifestPath); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(res.ArchivePath, old, old); err != nil {
		t.Fatal(err)
	}
	return vault, strings.TrimSuffix(filepath.Base(res.ArchivePath), ".jsonl.zst")
}

func runRepair(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stdout = captureStdout(t, func() {
		stderr = captureStderr(t, func() { code = cmdArchiveRepairManifest(cli.BuildInfo{Version: "cli-test"}).Run(args) })
	})
	return code, stdout, stderr
}

// The listing is read-only and prints the hints and the exact command.
func TestArchiveRepairManifestListsOrphansReadOnly(t *testing.T) {
	vault, name := repairCLIOrphan(t)
	manifest := filepath.Join(vault, "Projects", "p", "transcripts", name+".manifest.json")

	code, stdout, stderr := runRepair(t, "--project", "p")
	if code != cli.ExitOK {
		t.Fatalf("exit %d; stderr %s", code, stderr)
	}
	for _, want := range []string{name, "file mtime", "filename day", "hints only", "vp archive repair-manifest " + name + " -p p --adapter", "--captured-at"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the listing does not contain %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(manifest); err == nil {
		t.Error("the read-only listing wrote a manifest")
	}
}

// Apply: --captured-at is REQUIRED (omitted is rejected), --adapter is required,
// an offset input is stored in UTC, and a second run is a no-op.
func TestArchiveRepairManifestApplyRequiresTheOperatorsClaims(t *testing.T) {
	vault, name := repairCLIOrphan(t)
	manifest := filepath.Join(vault, "Projects", "p", "transcripts", name+".manifest.json")

	code, _, stderr := runRepair(t, name, "-p", "p", "--adapter", "claude-code")
	if code == cli.ExitOK || !strings.Contains(stderr, "--captured-at is required") {
		t.Errorf("an omitted --captured-at: exit %d stderr %q", code, stderr)
	}
	code, _, stderr = runRepair(t, name, "-p", "p", "--captured-at", "2026-11-01T12:00:00Z")
	if code == cli.ExitOK || !strings.Contains(stderr, "--adapter is required") {
		t.Errorf("an omitted --adapter: exit %d stderr %q", code, stderr)
	}
	if _, err := os.Stat(manifest); err == nil {
		t.Fatal("a manifest was written without the operator's claims")
	}

	code, stdout, stderr := runRepair(t, name, "-p", "p", "--adapter", "claude-code", "--captured-at", "2026-11-01T05:31:54-07:00")
	if code != cli.ExitOK {
		t.Fatalf("exit %d; stderr %s", code, stderr)
	}
	if !strings.Contains(stdout, "manifest written") || !strings.Contains(stdout, "operator-supplied") {
		t.Errorf("stdout = %q", stdout)
	}
	m, err := archive.ReadManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if m.CapturedAt != "2026-11-01T12:31:54Z" {
		t.Errorf("captured_at = %q, want the offset input canonicalised to UTC", m.CapturedAt)
	}
	if m.Regenerated == nil || m.Regenerated.ByVPVersion != "cli-test" {
		t.Errorf("regenerated = %+v", m.Regenerated)
	}

	code, stdout, _ = runRepair(t, name, "-p", "p", "--adapter", "claude-code", "--captured-at", "2026-11-01T12:31:54Z")
	if code != cli.ExitOK || !strings.Contains(stdout, "nothing to do") {
		t.Errorf("a second run: exit %d stdout %q", code, stdout)
	}
}

// The help text states the rollout rule, so it is on the page an operator reads.
func TestArchiveRepairManifestHelpStatesTheRolloutRule(t *testing.T) {
	d := cmdArchiveRepairManifest(cli.BuildInfo{}).Description
	for _, want := range []string{"DO NOT RUN until every host, hooks included, is on v11 or later", "regenerated", "no bulk mode"} {
		if !strings.Contains(d, want) {
			t.Errorf("the help text does not say %q", want)
		}
	}
}
