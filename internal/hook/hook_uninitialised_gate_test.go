// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"bytes"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// Gate 2c (task untracked-project-stamps-from-writers-that-never-commit, F3):
// a marker naming a project the vault has not initialised skips the whole run
// before archive, harvest and capture, Warns with the recovery commands, and
// writes nothing — neither into the vault nor the claim sentinel.

// uninitialisedHookFixture is a .vibe-palace.toml checkout plus a host dir
// holding a transcript and one native memory file. It returns the checkout,
// the transcript path and the native memory path.
func uninitialisedHookFixture(t *testing.T) (cwd, transcriptPath, native string) {
	t.Helper()
	cwd = t.TempDir()
	writeVibeMarker(t, cwd)
	initGitRepo(t, cwd, "initial commit")
	hostDir := t.TempDir()
	transcriptPath = filepath.Join(hostDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	native = filepath.Join(hostDir, "memory", "note.md")
	if err := os.MkdirAll(filepath.Dir(native), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("---\nname: note\ndescription: d\nmetadata:\n  type: project\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd, transcriptPath, native
}

// captureWarn routes slog to a buffer at Warn for the rest of the test.
func captureWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// vaultFiles lists every file under root, vault-relative.
func vaultFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runFresh(t *testing.T, vaultRoot, cwd, transcriptPath string) *Result {
	t.Helper()
	res, err := Run(context.Background(), Payload{
		SessionID: "test-session", TranscriptPath: transcriptPath, CWD: cwd, HookEventName: "SessionEnd",
	}, RunOptions{VaultRoot: vaultRoot, ProjectSlug: "fresh", VPVersion: "test-0.1", ClaimDir: filepath.Join(cwd, ".vibe-palace")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// assertSkippedUninitialised checks the run was skipped by gate 2c and wrote
// nothing anywhere.
func assertSkippedUninitialised(t *testing.T, res *Result, log, cwd, native string) {
	t.Helper()
	if !res.SkippedUninitialised {
		t.Error("SkippedUninitialised must be set for a marker naming an uninitialised project")
	}
	if res.SkippedRemovedSlug {
		t.Error("SkippedRemovedSlug must not be set: the project never departed")
	}
	if res.ArchivePath != "" || res.SessionNoteID != "" || res.MemoryHarvest != nil {
		t.Errorf("nothing may run for an uninitialised project: archive=%q note=%q harvest=%v",
			res.ArchivePath, res.SessionNoteID, res.MemoryHarvest)
	}
	if _, err := os.Stat(ClaimPath(filepath.Join(cwd, ".vibe-palace"), "test-session")); !os.IsNotExist(err) {
		t.Errorf("the claim sentinel must not be written (stat err %v)", err)
	}
	if _, err := os.Stat(native); err != nil {
		t.Errorf("the native memory must be neither harvested nor deleted: %v", err)
	}
	for _, want := range []string{"hook uninitialised project:", "vp init", "vp archive create"} {
		if !strings.Contains(log, want) {
			t.Errorf("the Warn must contain %q, got %q", want, log)
		}
	}
}

func TestRun_SkipsUninitialisedProject(t *testing.T) {
	vaultRoot := t.TempDir()
	cwd, transcriptPath, native := uninitialisedHookFixture(t)
	log := captureWarn(t)

	res := runFresh(t, vaultRoot, cwd, transcriptPath)
	assertSkippedUninitialised(t, res, log.String(), cwd, native)
	for _, dir := range []string{"Projects/fresh", "palace/fresh"} {
		if _, err := os.Stat(filepath.Join(vaultRoot, filepath.FromSlash(dir))); !os.IsNotExist(err) {
			t.Errorf("%s must not exist after the skip (stat err %v)", dir, err)
		}
	}
	if files := vaultFiles(t, vaultRoot); len(files) != 0 {
		t.Errorf("the vault must be untouched, got %v", files)
	}
}

// A phantom project — a Projects/fresh/ holding only a memory file, no
// scaffold marker and no content — is not initialised either.
func TestRun_SkipsPhantomProject(t *testing.T) {
	vaultRoot := t.TempDir()
	mem := filepath.Join(vaultRoot, "Projects", "fresh", "memory", "x.md")
	if err := os.MkdirAll(filepath.Dir(mem), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mem, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, transcriptPath, native := uninitialisedHookFixture(t)
	log := captureWarn(t)

	res := runFresh(t, vaultRoot, cwd, transcriptPath)
	assertSkippedUninitialised(t, res, log.String(), cwd, native)
	if files := vaultFiles(t, vaultRoot); !slices.Equal(files, []string{"Projects/fresh/memory/x.md"}) {
		t.Errorf("the vault must hold only the phantom's memory file, got %v", files)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "palace", "fresh")); !os.IsNotExist(err) {
		t.Errorf("palace/fresh must not exist after the skip (stat err %v)", err)
	}
}

// A departed slug is ALSO not initialised; gate 2b answers it first, so the
// stale-checkout skip (with its redirect) wins and 2c never fires.
func TestRun_DepartedSlugSkippedByRemovedSlugGateNotUninitialised(t *testing.T) {
	vaultRoot := t.TempDir()
	rec, err := (departure.Record{Slug: "fresh", Kind: departure.Renamed, To: "renamed-project", Date: "2026-09-23"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	recAbs := filepath.Join(vaultRoot, filepath.FromSlash(departure.RelPath("fresh")))
	if err := os.MkdirAll(filepath.Dir(recAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recAbs, rec, 0o644); err != nil {
		t.Fatal(err)
	}
	cwd, transcriptPath, _ := uninitialisedHookFixture(t)
	log := captureWarn(t)

	res := runFresh(t, vaultRoot, cwd, transcriptPath)
	if !res.SkippedRemovedSlug {
		t.Error("SkippedRemovedSlug must be set for a departed slug")
	}
	if res.SkippedUninitialised {
		t.Error("SkippedUninitialised must not be set: gate 2b answers a departed slug first")
	}
	if l := log.String(); !strings.Contains(l, "hook stale checkout:") || strings.Contains(l, "hook uninitialised project:") {
		t.Errorf("only the stale-checkout Warn may fire, got %q", l)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "Projects", "fresh")); !os.IsNotExist(err) {
		t.Errorf("Projects/fresh must not exist (stat err %v)", err)
	}
}
