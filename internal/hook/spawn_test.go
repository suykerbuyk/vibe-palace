// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// recordedLaunch is one call to the recording launcher below.
type recordedLaunch struct {
	binary  string
	args    []string
	logPath string
}

// newLaunchRecorder returns a detachlaunch.LaunchFunc that records every call
// and spawns nothing, plus a pointer to the recorded calls in order. A local
// recorder (not testinfra's) because internal/testinfra imports internal/tools,
// which imports this package — importing it here would be an import cycle.
func newLaunchRecorder() (detachlaunch.LaunchFunc, *[]recordedLaunch) {
	calls := &[]recordedLaunch{}
	fn := func(binary string, args []string, logPath string) (int, error) {
		*calls = append(*calls, recordedLaunch{binary, append([]string(nil), args...), logPath})
		return 4242, nil
	}
	return fn, calls
}

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// hookSpawnFixture builds a vault + cwd + transcript for a SessionEnd/PreCompact
// hook run, mirroring TestRun_HappyPath's setup.
func hookSpawnFixture(t *testing.T) (vaultRoot, cwd, transcriptPath, claimDir string) {
	t.Helper()
	vaultRoot = t.TempDir()
	testutil.InitProject(t, vaultRoot, "test-project")
	cwd = t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir = filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath = filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "initial commit")
	return
}

// manifestSHA reads the one manifest the hook wrote under test-project and
// returns its source_sha256.
func manifestSHA(t *testing.T, vaultRoot string) string {
	t.Helper()
	manifests, err := filepath.Glob(filepath.Join(vaultRoot, "Projects", "test-project", "transcripts", "*.manifest.json"))
	if err != nil || len(manifests) != 1 {
		t.Fatalf("want exactly one manifest, got %v (err %v)", manifests, err)
	}
	m, err := archive.ReadManifest(manifests[0])
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	return m.SourceSHA256
}

// TestRun_SpawnsIngesterOnSuccessWithHash: a SessionEnd run that archives
// spawns exactly one `vp drain archives --vault-root <root> --project <p>
// --first <sha>`, and <sha> is the new manifest's source_sha256 — a hash, never
// the archive path. (ADR-014 decision 7; Scope 1, 4-S3.)
func TestRun_SpawnsIngesterOnSuccessWithHash(t *testing.T) {
	vaultRoot, cwd, transcriptPath, claimDir := hookSpawnFixture(t)
	launch, calls := newLaunchRecorder()

	res, err := Run(context.Background(), Payload{
		SessionID:      "test-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
		Launch:      launch,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ArchivePath == "" {
		t.Fatal("no archive created, so the spawn precondition never held")
	}
	if len(*calls) != 1 {
		t.Fatalf("want exactly one ingester launch, got %d: %+v", len(*calls), *calls)
	}
	got := (*calls)[0]
	if len(got.args) < 2 || got.args[0] != "drain" || got.args[1] != "archives" {
		t.Errorf("launch args = %v, want a `drain archives` command", got.args)
	}
	if v, ok := argValue(got.args, "--vault-root"); !ok || v != vaultRoot {
		t.Errorf("--vault-root = %q, want %q", v, vaultRoot)
	}
	if v, ok := argValue(got.args, "--project"); !ok || v != "test-project" {
		t.Errorf("--project = %q, want test-project", v)
	}
	first, ok := argValue(got.args, "--first")
	if !ok {
		t.Fatalf("no --first in %v", got.args)
	}
	if !sha256Hex.MatchString(first) {
		t.Errorf("--first = %q, want a 64-hex source_sha256 (not a path)", first)
	}
	if want := manifestSHA(t, vaultRoot); first != want {
		t.Errorf("--first = %q, want the manifest's source_sha256 %q", first, want)
	}
}

// TestRun_DedupedArchiveStillSpawnsWithHash: a second SessionEnd over the same
// bytes dedups (archive.Create returns Skipped) but still spawns, with --first
// equal to the existing manifest's source_sha256.
func TestRun_DedupedArchiveStillSpawnsWithHash(t *testing.T) {
	vaultRoot, cwd, transcriptPath, claimDir := hookSpawnFixture(t)

	run := func() []recordedLaunch {
		launch, calls := newLaunchRecorder()
		// A fresh claim dir each run so the claim gate never short-circuits the
		// archive step we are exercising.
		cd := t.TempDir()
		if _, err := Run(context.Background(), Payload{
			SessionID:      "test-session",
			TranscriptPath: transcriptPath,
			CWD:            cwd,
			HookEventName:  "SessionEnd",
		}, RunOptions{
			VaultRoot:   vaultRoot,
			ProjectSlug: "test-project",
			VPVersion:   "test-0.1",
			ClaimDir:    cd,
			Launch:      launch,
		}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return *calls
	}

	_ = claimDir
	first := run()
	second := run() // same bytes → archive.Create dedups (Skipped)

	want := manifestSHA(t, vaultRoot)
	for label, calls := range map[string][]recordedLaunch{"first": first, "second": second} {
		if len(calls) != 1 {
			t.Fatalf("%s run: want one launch, got %d: %+v", label, len(calls), calls)
		}
		if v, _ := argValue(calls[0].args, "--first"); v != want {
			t.Errorf("%s run: --first = %q, want %q (the dedup must still carry the hash)", label, v, want)
		}
	}
}

// TestRun_NoSpawnWhenArchiveFails: when the archive step fails (an unreadable
// transcript path), no spawn is registered — there is no archive to ingest.
func TestRun_NoSpawnWhenArchiveFails(t *testing.T) {
	vaultRoot, cwd, _, claimDir := hookSpawnFixture(t)
	launch, calls := newLaunchRecorder()

	res, err := Run(context.Background(), Payload{
		SessionID:      "test-session",
		TranscriptPath: filepath.Join(t.TempDir(), "does-not-exist.jsonl"),
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
		Launch:      launch,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ArchivePath != "" {
		t.Fatalf("expected the archive step to fail, but ArchivePath=%q", res.ArchivePath)
	}
	if len(*calls) != 0 {
		t.Errorf("want no launch when the archive failed, got %d: %+v", len(*calls), *calls)
	}
}

// TestRun_NoSpawnAtStop: a Stop run never archives (hook.go), so it never
// spawns.
func TestRun_NoSpawnAtStop(t *testing.T) {
	vaultRoot, cwd, transcriptPath, claimDir := hookSpawnFixture(t)
	launch, calls := newLaunchRecorder()

	if _, err := Run(context.Background(), Payload{
		SessionID:      "test-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "Stop",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
		Launch:      launch,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("want no launch at Stop, got %d: %+v", len(*calls), *calls)
	}
}

// TestRun_SpawnOnClaimedSessionEarlyReturn: the spawn is a defer registered in
// the archive step, so it still fires on the claimed-session early return — the
// path a straight-line call after WriteSession would miss (Scope 1, Design).
func TestRun_SpawnOnClaimedSessionEarlyReturn(t *testing.T) {
	vaultRoot, cwd, transcriptPath, claimDir := hookSpawnFixture(t)
	// Pre-claim the session so IsClaimed is true and Run takes the early return.
	if err := WriteClaim(claimDir, "test-session", "prior-note"); err != nil {
		t.Fatalf("WriteClaim: %v", err)
	}
	launch, calls := newLaunchRecorder()

	res, err := Run(context.Background(), Payload{
		SessionID:      "test-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
		Launch:      launch,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.ClaimedSkip {
		t.Fatalf("want ClaimedSkip=true (the early return path), got %+v", res)
	}
	if len(*calls) != 1 {
		t.Errorf("want one launch on the claimed-session early return, got %d: %+v", len(*calls), *calls)
	}
}

// TestRun_NilLauncherSpawnsNothing: a nil RunOptions.Launch (the library/test
// default) must be handled without spawning or panicking, so a hook library
// test never relaunches the test binary.
func TestRun_NilLauncherSpawnsNothing(t *testing.T) {
	vaultRoot, cwd, transcriptPath, claimDir := hookSpawnFixture(t)
	res, err := Run(context.Background(), Payload{
		SessionID:      "test-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
		// Launch nil
	})
	if err != nil {
		t.Fatalf("Run with nil Launch: %v", err)
	}
	if res.ArchivePath == "" {
		t.Error("expected the archive to still be created with a nil launcher")
	}
}
