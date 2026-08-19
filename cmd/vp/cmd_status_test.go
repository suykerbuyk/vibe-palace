// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// testVaultSource is a stand-in resolution source in the same cwd:<file>
// vocabulary storage.ResolveVaultPath emits. The end-to-end wiring — that
// cmdStatus passes the REAL one — is pinned separately by
// TestStatusCommandWiresTheResolvedVaultPath.
const testVaultSource = "cwd:/fixture/.vibe-palace.toml"

func testVault(t *testing.T) *storage.Vault {
	t.Helper()
	return storage.NewVault(t.TempDir())
}

func TestRunStatusEmpty(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	code := runStatus(v, "test-proj", testVaultSource, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Project: test-proj") {
		t.Errorf("missing project name in output: %s", out)
	}
	if !strings.Contains(out, "Tasks:   0 active") {
		t.Errorf("missing task count: %s", out)
	}
	if !strings.Contains(out, "Sessions: 0 total") {
		t.Errorf("missing session count: %s", out)
	}
}

func TestRunStatusWithData(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "task-one", Title: "Task One", Content: "content", Priority: "high"})
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "task-two", Title: "Task Two", Content: "content", Priority: "low"})
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "Session 1", Tag: "impl",
	}, "body")

	var buf bytes.Buffer
	code := runStatus(v, "test-proj", testVaultSource, false, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "Tasks:   2 active") {
		t.Errorf("expected 2 tasks: %s", out)
	}
	if !strings.Contains(out, "Sessions: 1 total") {
		t.Errorf("expected 1 session: %s", out)
	}
}

func TestRunStatusJSON(t *testing.T) {
	v := testVault(t)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "task-one", Title: "Task One", Content: "content", Priority: "high"})

	var buf bytes.Buffer
	code := runStatus(v, "test-proj", testVaultSource, true, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var result statusResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result.Project != "test-proj" {
		t.Errorf("project = %q", result.Project)
	}
	if result.Tasks != 1 {
		t.Errorf("tasks = %d, want 1", result.Tasks)
	}
}

// TestRunStatusPrintsVaultPathAndSource is the ratchet for
// vp-status-does-not-print-the-vault-path-its-guidance-promises.
//
// `vault-abs-paths` tells every agent "RESOLVE, DON'T RECALL" and points at a
// command that prints the resolved vault_path. For a long time vp status — the
// command whose name most suggests it, and the one several documents named —
// printed five lines and no path, so an agent that obeyed the instruction got
// nothing and was pushed back toward recalling a path: the instrument's repair
// instruction routing the victim into the bug.
//
// The assertion is on BOTH fields and on the vp check spelling, because the
// remediation says to grep for vault_path and one grep has to work against
// either command. Delete either Fprintf in runStatus and this fails.
func TestRunStatusPrintsVaultPathAndSource(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	if code := runStatus(v, "test-proj", testVaultSource, false, &buf); code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	out := buf.String()

	// Byte-identical to internal/check.CheckConfigAt's detail lines.
	wantPath := "vault_path = " + v.Root
	if !strings.Contains(out, wantPath) {
		t.Errorf("human output must carry the resolved vault path as %q; got:\n%s", wantPath, out)
	}
	wantSource := "vault_path source = " + testVaultSource
	if !strings.Contains(out, wantSource) {
		t.Errorf("human output must name the resolution SOURCE as %q — a path without its source cannot be checked against the config that produced it; got:\n%s", wantSource, out)
	}
}

// TestRunStatusJSONCarriesVaultPathAndSource pins the same fact on the machine
// surface. A human-only fix would leave every scripted caller still unable to
// answer the question the guidance tells it to ask.
func TestRunStatusJSONCarriesVaultPathAndSource(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	if code := runStatus(v, "test-proj", testVaultSource, true, &buf); code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	var result statusResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result.VaultPath != v.Root {
		t.Errorf("vault_path = %q, want the vault this run actually read (%q)", result.VaultPath, v.Root)
	}
	if result.VaultPathSource != testVaultSource {
		t.Errorf("vault_path_source = %q, want %q", result.VaultPathSource, testVaultSource)
	}
}

// TestRunStatusUnknownSourceIsNotGuessed pins the absence case: a caller that
// supplies no source gets "unknown", never a fabricated one. ADR-006 — absence
// is not a value, and a wrong DERIVE is a silent lie wearing the face of a
// measurement, which is the whole disease this output treats.
func TestRunStatusUnknownSourceIsNotGuessed(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	if code := runStatus(v, "test-proj", "", false, &buf); code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "vault_path source = unknown") {
		t.Errorf("an unsupplied source must report unknown, not a guess; got:\n%s", buf.String())
	}
}

// TestStatusCommandWiresTheResolvedVaultPath drives cmdStatus().Run end to end.
//
// The previous two tests exercise runStatus with a source handed to it, which
// says nothing about whether the COMMAND resolves and passes the real one —
// exercising the helper is not exercising the path it is installed on. This
// chdirs into a directory whose .vibe-palace.toml points vault_path at a known
// temp vault, then asserts the JSON names that vault and cites that very marker
// file as the source.
func TestStatusCommandWiresTheResolvedVaultPath(t *testing.T) {
	vaultDir := t.TempDir()
	projDir := t.TempDir()
	marker := filepath.Join(projDir, ".vibe-palace.toml")
	body := "vault_path = \"" + vaultDir + "\"\n\n[project]\nname = \"wired-status\"\n"
	if err := os.WriteFile(marker, []byte(body), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	t.Chdir(projDir)

	// Control: the fixture must actually bind this vault, or the assertions
	// below would be measuring the ambient machine's config instead.
	gotPath, gotSource, err := storage.ResolveVaultPath(projDir)
	if err != nil {
		t.Fatalf("fixture does not resolve: %v", err)
	}
	if gotPath != vaultDir {
		t.Fatalf("fixture resolved to %q, want %q — the marker is not binding", gotPath, vaultDir)
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	code := cmdStatus().Run([]string{"--json"})
	w.Close()
	os.Stdout = old
	raw, _ := io.ReadAll(r)

	if code != cli.ExitOK {
		t.Fatalf("exit code = %d, output:\n%s", code, raw)
	}
	var result statusResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("invalid JSON: %v\nraw:\n%s", err, raw)
	}
	if result.VaultPath != vaultDir {
		t.Errorf("vault_path = %q, want the vault the marker binds (%q) — the command is not passing what it resolved", result.VaultPath, vaultDir)
	}
	if result.VaultPathSource != gotSource {
		t.Errorf("vault_path_source = %q, want %q", result.VaultPathSource, gotSource)
	}
	if !strings.Contains(result.VaultPathSource, marker) {
		t.Errorf("source %q must cite the marker file that bound the vault (%q)", result.VaultPathSource, marker)
	}
}
