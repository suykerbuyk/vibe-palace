// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// setupDrainProject creates a hermetic project directory with its own
// .vibe-palace.toml vault_path override, so OpenProjectVaultAt(projectPath)
// resolves deterministically regardless of the host's real global vault
// config (or lack of one).
func setupDrainProject(t *testing.T) string {
	t.Helper()
	projectPath := t.TempDir()
	vaultRoot := t.TempDir()
	cfg := fmt.Sprintf("vault_path = %q\n", vaultRoot)
	if err := os.WriteFile(filepath.Join(projectPath, ".vibe-palace.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	return projectPath
}

func TestRunDrainSummaries_Success(t *testing.T) {
	projectPath := setupDrainProject(t)

	var buf bytes.Buffer
	code := runDrainSummaries(projectPath, 10, &buf)
	if code != cli.ExitOK {
		t.Fatalf("code = %d, want ExitOK; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "drained=0") {
		t.Errorf("expected drained=0 (nil Summarizer no-op) in output: %s", buf.String())
	}

	// The lock must have been released, not merely acquired: a second,
	// fully sequential call must succeed exactly the same way, not report
	// already_running. (vaultlock's sidecar .lock FILE is expected to
	// remain on disk — that is the package's own documented behavior, "a
	// lingering .lock marker file left behind... is harmless"; presence of
	// the file proves nothing either way, only successful re-acquisition
	// does.)
	var buf2 bytes.Buffer
	code2 := runDrainSummaries(projectPath, 10, &buf2)
	if code2 != cli.ExitOK {
		t.Fatalf("second call: code = %d, want ExitOK; output: %s", code2, buf2.String())
	}
	if strings.Contains(buf2.String(), "already_running") {
		t.Fatalf("second sequential call reported already_running — lock was not released: %s", buf2.String())
	}
}

func TestRunDrainSummaries_EmptyProjectHasNoQueueYet(t *testing.T) {
	// A project that has never enqueued anything must still succeed —
	// the queue directory is created defensively and
	// DrainSummarizationQueue reports 0 rather than erroring.
	projectPath := setupDrainProject(t)

	var buf bytes.Buffer
	code := runDrainSummaries(projectPath, 0, &buf)
	if code != cli.ExitOK {
		t.Fatalf("code = %d, want ExitOK; output: %s", code, buf.String())
	}
	queueDir := filepath.Join(projectPath, ".vibe-palace", "summarization-queue")
	if _, err := os.Stat(queueDir); err != nil {
		t.Errorf("queue dir not created: %v", err)
	}
}

// TestRunDrainSummaries_ConcurrentDrainReportsAlreadyRunning pins the
// single-flight guarantee itself: while another holder (simulated here by
// directly calling vaultlock.TryAcquire against the exact same root/target
// runDrainSummaries uses) holds the lock, a call must report
// already_running at ExitOK — not a user error, since a concurrent drain
// finishing the work is a success — and once the other holder releases, a
// subsequent call must succeed normally. Unlike the old pidfile scheme, there
// is no staleness window to fast-forward through here: the lock is either
// held or it is not.
func TestRunDrainSummaries_ConcurrentDrainReportsAlreadyRunning(t *testing.T) {
	projectPath := setupDrainProject(t)
	// Pre-create the queue dir (drainLockTarget's own value) exactly as
	// runDrainSummaries itself does before acquiring the lock, so both this
	// simulated holder and the later real call resolve vaultlock's
	// canonicalKey via the same EvalSymlinks-succeeds branch — not one via
	// that branch and the other via its nonexistent-path fallback, which
	// could diverge if any path component (e.g. a symlinked temp dir) needed
	// resolving.
	if err := os.MkdirAll(drainLockTarget(projectPath), 0o755); err != nil {
		t.Fatalf("pre-create queue dir: %v", err)
	}

	release, ok, err := vaultlock.TryAcquire(drainLockRoot(projectPath), drainLockTarget(projectPath))
	if err != nil || !ok {
		t.Fatalf("simulate concurrent holder: ok=%v err=%v", ok, err)
	}

	var buf bytes.Buffer
	code := runDrainSummaries(projectPath, 10, &buf)
	if code != cli.ExitOK {
		t.Fatalf("code = %d, want ExitOK (already_running is not a user error); output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "already_running") {
		t.Errorf("expected already_running in output: %s", buf.String())
	}

	if err := release(); err != nil {
		t.Fatalf("release simulated holder: %v", err)
	}

	var buf2 bytes.Buffer
	code2 := runDrainSummaries(projectPath, 10, &buf2)
	if code2 != cli.ExitOK {
		t.Fatalf("after release: code = %d, want ExitOK; output: %s", code2, buf2.String())
	}
	if strings.Contains(buf2.String(), "already_running") {
		t.Errorf("after release: expected the lock to be free, got already_running: %s", buf2.String())
	}
}

func TestCmdDrainSummaries_MissingProjectPathIsUserError(t *testing.T) {
	cmd := cmdDrainSummaries()
	code := cmd.Run([]string{"--max", "5"})
	if code != cli.ExitUser {
		t.Errorf("code = %d, want ExitUser", code)
	}
}

// TestCmdDrainSummaries_RelativeProjectPathIsUserError pins a real fix: a
// relative --project-path used to travel all the way to vaultlock.TryAcquire
// (which hard-requires an absolute vaultRoot) and surface as an opaque
// "acquire lock" system error instead of a clear, immediate validation
// error at the command's own argument-parsing boundary.
func TestCmdDrainSummaries_RelativeProjectPathIsUserError(t *testing.T) {
	cmd := cmdDrainSummaries()
	var buf bytes.Buffer
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := cmd.Run([]string{"--project-path", "."})
	w.Close()
	os.Stderr = oldStderr
	buf.ReadFrom(r)

	if code != cli.ExitUser {
		t.Errorf("code = %d, want ExitUser; stderr: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "must be absolute") {
		t.Errorf("expected a must-be-absolute error message: %s", buf.String())
	}
}

func TestCmdDrainSummaries_DefaultMaxAppliedInCode(t *testing.T) {
	// --max omitted (and --max 0) must fall back to
	// drainSummariesDefaultMax, applied in code — never read from
	// cli.FlagDef.Default, which ParseFlags never consults.
	projectPath := setupDrainProject(t)
	cmd := cmdDrainSummaries()
	code := cmd.Run([]string{"--project-path", projectPath})
	if code != cli.ExitOK {
		t.Fatalf("code = %d, want ExitOK", code)
	}
}

func TestCmdDrain_IsABareParent(t *testing.T) {
	cmd := cmdDrain()
	if cmd.Name != "drain" {
		t.Errorf("Name = %q, want %q", cmd.Name, "drain")
	}
	if cmd.Run != nil {
		t.Errorf("expected a nil Run (pure parent, like cmdDiscover())")
	}
}

func TestRunDrainSummaries_BadVaultConfigIsSystemError(t *testing.T) {
	projectPath := t.TempDir()
	// Malformed TOML: OpenProjectVaultAt must surface this as an error
	// rather than silently falling back.
	if err := os.WriteFile(filepath.Join(projectPath, ".vibe-palace.toml"), []byte("vault_path = [not valid toml"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	var buf bytes.Buffer
	code := runDrainSummaries(projectPath, 10, &buf)
	if code != cli.ExitSystem {
		t.Fatalf("code = %d, want ExitSystem; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "open vault") {
		t.Errorf("expected an open-vault error message: %s", buf.String())
	}
}

func TestRunDrainSummaries_UndetectableProjectIsSystemError(t *testing.T) {
	// A directory whose basename slugifies to empty (DetectProject's last
	// resort) fails project detection entirely.
	outer := t.TempDir()
	projectPath := filepath.Join(outer, "___")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vaultRoot := t.TempDir()
	cfg := fmt.Sprintf("vault_path = %q\n", vaultRoot)
	if err := os.WriteFile(filepath.Join(projectPath, ".vibe-palace.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	var buf bytes.Buffer
	code := runDrainSummaries(projectPath, 10, &buf)
	if code != cli.ExitSystem {
		t.Fatalf("code = %d, want ExitSystem; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "detect project") {
		t.Errorf("expected a detect-project error message: %s", buf.String())
	}
}

func TestRunDrainSummaries_QueueDirCreationFailureIsSystemError(t *testing.T) {
	projectPath := setupDrainProject(t)
	// Occupy the .vibe-palace path with a plain file, so MkdirAll of
	// .vibe-palace/summarization-queue underneath it cannot succeed.
	if err := os.WriteFile(filepath.Join(projectPath, ".vibe-palace"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	var buf bytes.Buffer
	code := runDrainSummaries(projectPath, 10, &buf)
	if code != cli.ExitSystem {
		t.Fatalf("code = %d, want ExitSystem; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "create queue dir") {
		t.Errorf("expected a create-queue-dir error message: %s", buf.String())
	}
}

// TestRunDrainSummaries_LockAcquireIOErrorIsSystemError exercises
// vaultlock.TryAcquire's own error path, distinct from the queue-dir-creation
// failure covered above: the queue dir itself is created just fine, but
// vaultlock's own .vp-locks sidecar directory (a sibling under the same
// .vibe-palace root) is blocked by a plain file occupying that exact path,
// so its MkdirAll cannot succeed.
func TestRunDrainSummaries_LockAcquireIOErrorIsSystemError(t *testing.T) {
	projectPath := setupDrainProject(t)
	vpDir := filepath.Join(projectPath, ".vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatalf("mkdir .vibe-palace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vpDir, ".vp-locks"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	var buf bytes.Buffer
	code := runDrainSummaries(projectPath, 10, &buf)
	if code != cli.ExitSystem {
		t.Fatalf("code = %d, want ExitSystem; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "acquire lock") {
		t.Errorf("expected an acquire-lock error message: %s", buf.String())
	}
}

// TestRunDrainSummaries_BracketedProjectPathProcessesQueuedJob is this
// phase's integration-level proof for the `vp drain summaries` entrypoint:
// it drives runDrainSummaries — the exact function `vp drain summaries`'s
// registered Run closure calls after parsing --project-path/--max — against
// a --project-path whose directory name contains a bracketed segment
// ("proj[1]"), with a real queued job file already sitting on disk in that
// project's summarization-queue directory (written via
// summarize.EnqueueIterationSummary, the package's own real enqueue path,
// not a hand-built fixture).
//
// This specifically pins Bug 3: internal/jobqueue.Claim used to locate
// claimable jobs via filepath.Glob(filepath.Join(dir, "*.json")). '[' and
// ']' are glob metacharacters, so when dir itself (here, the
// summarization-queue directory nested under "proj[1]") contained brackets,
// the pattern would silently fail to match the real job file sitting right
// there — the job would never be found, never drained, and never reported
// as an error. Claim now lists dir via os.ReadDir and matches by a plain
// ".json" suffix check, so bracket characters in the directory name are
// inert.
//
// runDrainSummaries no longer accepts an injectable Summarizer override — it
// always builds a real, config-driven summarize.DispatchSummarizer itself
// (see cmd_drain.go). So, to force the real Claim/Requeue/Done path to run
// instead of short-circuiting on a disabled summarizer, this test enables
// [summarization] in the project's own config and points base_url at an
// httptest server returning a canned OpenAI-compatible completion — the same
// pattern internal/hook/hook_test.go's
// TestRun_SessionEndDrainsQueuedEnrichmentFromBracketedProjectPath uses to
// preserve its own analogous enrichment-side proof.
func TestRunDrainSummaries_BracketedProjectPathProcessesQueuedJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"summary\":\"Drained via real wiring.\",\"decisions\":[],\"unblocks\":\"\"}"}}]}`))
	}))
	defer srv.Close()

	t.Setenv("VP_TEST_DRAIN_BRACKET_KEY", "sk-drain-bracket-test")

	outer := t.TempDir()
	projectPath := filepath.Join(outer, "proj[1]")
	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir bracketed project dir: %v", err)
	}
	vaultRoot := t.TempDir()
	const slug = "test-project"

	// .vibe-palace.toml pins BOTH the vault_path override AND the project
	// name (strategy 1 of project.DetectProjectHighConfidence) so slug is
	// deterministic regardless of the bracketed temp-dir basename, matching
	// what summarize.EnqueueIterationSummary below is keyed on.
	markerCfg := fmt.Sprintf("vault_path = %q\n\n[project]\nname = %q\n", vaultRoot, slug)
	if err := os.WriteFile(filepath.Join(projectPath, ".vibe-palace.toml"), []byte(markerCfg), 0o644); err != nil {
		t.Fatalf("write project marker config: %v", err)
	}

	vault := storage.NewVault(vaultRoot)
	cfgPath, err := vault.ProjectConfigFile(slug)
	if err != nil {
		t.Fatalf("ProjectConfigFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir project config dir: %v", err)
	}
	summCfg := "[summarization]\n" +
		"enabled = true\n" +
		"provider = \"openai\"\n" +
		"model = \"drain-test-model\"\n" +
		"api_key_env = \"VP_TEST_DRAIN_BRACKET_KEY\"\n" +
		"base_url = \"" + srv.URL + "\"\n" +
		"max_tokens = 512\n" +
		"timeout_seconds = 10\n"
	if err := os.WriteFile(cfgPath, []byte(summCfg), 0o644); err != nil {
		t.Fatalf("write summarization config: %v", err)
	}

	// Seed the iterations.md entry the queued job (N=7) resolves against.
	itersPath, err := vault.IterationsFile(slug)
	if err != nil {
		t.Fatalf("IterationsFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(itersPath), 0o755); err != nil {
		t.Fatalf("mkdir iterations dir: %v", err)
	}
	if err := os.WriteFile(itersPath, []byte("## Iteration 7 — Bracket path test\n\nDid the bracket thing.\n"), 0o644); err != nil {
		t.Fatalf("write iterations.md: %v", err)
	}

	// Place a real queued job on disk via internal/summarize's own enqueue
	// path (not a hand-built fixture), inside the bracketed project path.
	if err := summarize.EnqueueIterationSummary(projectPath, slug, 7); err != nil {
		t.Fatalf("EnqueueIterationSummary: %v", err)
	}
	queueFile := filepath.Join(summarize.QueueDir(projectPath), "iteration-00007.json")
	if _, err := os.Stat(queueFile); err != nil {
		t.Fatalf("queued job not on disk before drain: %v", err)
	}

	var buf bytes.Buffer
	code := runDrainSummaries(projectPath, 10, &buf)
	if code != cli.ExitOK {
		t.Fatalf("code = %d, want ExitOK; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "drained=1") {
		t.Errorf("expected drained=1 in output, got: %s", buf.String())
	}

	// The queued job file that lived in the bracketed directory must be
	// gone: actually found and processed, not silently left behind the way
	// the pre-fix filepath.Glob bug would have left it.
	if _, err := os.Stat(queueFile); !os.IsNotExist(err) {
		t.Errorf("queue file still present after drain (job was not processed): stat err = %v", err)
	}

	// The real IterationSummarizer must have run end-to-end: a
	// vault-committed cache file for N=7 now exists with the canned
	// response's content.
	got, ok, err := vault.ReadIterationSummary(slug, 7)
	if err != nil {
		t.Fatalf("ReadIterationSummary: %v", err)
	}
	if !ok {
		t.Fatal("expected an iteration summary to have been cached, found none")
	}
	if got.Summary != "Drained via real wiring." {
		t.Errorf("Summary = %q, want %q", got.Summary, "Drained via real wiring.")
	}
}
