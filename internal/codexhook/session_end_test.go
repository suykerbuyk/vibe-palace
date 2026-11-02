// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// markedCwd returns a fresh directory carrying a .vibe-palace.toml naming slug.
//
// 🔴 EVERY ProcessSessionEnd TEST NEEDS ONE. The capture gates begin with
// project.DetectSignal(event.CWD), and DetectSignal("") resolves the PROCESS's
// cwd and walks upward. Tests that left CWD empty would therefore answer from
// wherever `go test` happened to run — inside this repo they would find the
// repo's own marker and pass by accident, and from anywhere else they would
// skip and fail. The fixture makes the answer come from the test.
func markedCwd(t *testing.T, slug string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".vibe-palace.toml"),
		[]byte("[project]\nname = \""+slug+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// captureLog redirects slog to a buffer for the duration of one test and
// returns it, so a gate's operator-facing Warn can be asserted rather than
// assumed.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// recordingLauncher is a fake detachlaunch.LaunchFunc.
//
// 🔴 NEVER substitute the real detachlaunch.Launch here. Under `go test`,
// os.Executable() is the package's .test binary, and cmd/vp's TestMain re-runs
// the whole suite — an unfiltered re-exec is an unbounded recursive self-spawn
// that reboots the host. detachlaunch.ErrRefusedTestBinary exists for that
// reason; a fake means the guard is never even approached.
type recordingLauncher struct {
	calls   int
	binary  string
	args    []string
	logPath string
	// outAtCall is whatever the handler had written to its out writer at the
	// moment the launcher was invoked, so a test can prove ordering.
	outAtCall string
	out       *bytes.Buffer
}

func (r *recordingLauncher) launch(binary string, args []string, logPath string) (int, error) {
	r.calls++
	r.binary, r.args, r.logPath = binary, args, logPath
	if r.out != nil {
		r.outAtCall = r.out.String()
	}
	return 4242, nil
}

func writeTranscript(t *testing.T, content []byte) string {
	t.Helper()
	source := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return source
}

func TestSessionEndSmallTranscriptArchivesAndLinksReceiptWithinThreeSeconds(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	cwd := markedCwd(t, "demo")
	content := []byte("{\"type\":\"response_item\",\"payload\":{\"message\":\"kept verbatim\"}}\n")
	source := writeTranscript(t, content)
	var out bytes.Buffer
	launcher := &recordingLauncher{out: &out}
	started := time.Now()
	err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_small", TranscriptPath: source, CWD: cwd}, v, "demo", &out,
		Options{VPVersion: "0.0.0-codexhook-test", Launch: launcher.launch})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) >= 3*time.Second {
		t.Fatalf("small fixture took %s", time.Since(started))
	}
	if !strings.Contains(out.String(), "archived:") {
		t.Fatalf("success output missing archive path: %q", out.String())
	}
	entries, err := archive.ListEntries(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(entries))
	}
	sum := sha256.Sum256(content)
	if entries[0].Manifest.SourceSHA256 != hex.EncodeToString(sum[:]) || entries[0].Manifest.TurnCount != 0 {
		t.Fatalf("manifest = %+v", entries[0].Manifest)
	}
	// EQUALITY, not non-empty: a build that threads no cli.BuildInfo through
	// cmd/vp compiles and writes "", which is what every Codex manifest before
	// this change recorded, and a non-empty check would not see the difference
	// between "wired" and "wired to the wrong thing".
	if got := entries[0].Manifest.VPVersion; got != "0.0.0-codexhook-test" {
		t.Errorf("manifest vp_version = %q, want the injected value", got)
	}
	if entries[0].Manifest.VaultRelSessionNote != "" {
		t.Fatalf("stub receipt back-linked itself: %q", entries[0].Manifest.VaultRelSessionNote)
	}
	metaRows, _, err := v.ListSessions("demo", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var meta storage.SessionMeta
	for _, row := range metaRows {
		if row.ArchiveSessionID == "thr_small" {
			meta = row
			break
		}
	}
	if meta.Archive == "" {
		t.Fatalf("receipt has no forward archive link: %+v", meta)
	}
	if meta.Host != storage.HostCodex || meta.HostSource != storage.HostSourceCodexHookEvent || meta.ArchiveSessionIDSource != storage.ArchiveIDSourceCodexSessionEnd {
		t.Fatalf("receipt provenance = %+v", meta)
	}
}

// TestSessionEndLaunchesTheArchiveIngester pins the argv exactly. `vp drain
// archives` accepts a non-hash --first, logs that it ignored it, and drains
// UNORDERED — a silent downgrade with no error anywhere — so passing
// ManifestPath instead of SourceSHA256 can only be caught by asserting the
// value, never by asserting that a launch happened.
func TestSessionEndLaunchesTheArchiveIngester(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	cwd := markedCwd(t, "demo")
	source := writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
	var out bytes.Buffer
	launcher := &recordingLauncher{out: &out}
	if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_launch", TranscriptPath: source, CWD: cwd}, v, "demo", &out,
		Options{Launch: launcher.launch}); err != nil {
		t.Fatal(err)
	}
	if launcher.calls != 1 {
		t.Fatalf("ingester launches = %d, want 1", launcher.calls)
	}
	entries, err := archive.ListEntries(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(entries))
	}
	want := []string{"drain", "archives", "--vault-root", root, "--project", "demo", "--first", entries[0].Manifest.SourceSHA256}
	if len(launcher.args) != len(want) {
		t.Fatalf("ingester args = %v, want %v", launcher.args, want)
	}
	for i := range want {
		if launcher.args[i] != want[i] {
			t.Fatalf("ingester args = %v, want %v", launcher.args, want)
		}
	}
	if launcher.binary != "" {
		t.Errorf("ingester binary = %q, want \"\" (self re-exec)", launcher.binary)
	}
	if wantLog := filepath.Join(v.VaultLocalDir(), "ingester.log"); launcher.logPath != wantLog {
		t.Errorf("ingester logPath = %q, want %q", launcher.logPath, wantLog)
	}
	// Ordering: the spawn is last, so the hook's observable success never
	// depends on an exec that may not start.
	if !strings.Contains(launcher.outAtCall, "archived:") {
		t.Errorf("ingester launched before the archived: line was written; out was %q", launcher.outAtCall)
	}
}

// TestSessionEndGitHeadComesFromTheEventCwdNotTheProcessCwd is the only
// assertion that separates "correct" from "coincidentally correct". archive's
// gitHead falls back to os.Getwd() when SourceCWD is empty, and the Codex hook
// normally runs IN the session's cwd — so a build that never passes SourceCWD
// still records the right git_head on a developer's machine. Putting the
// process somewhere non-git makes the fallback produce "".
func TestSessionEndGitHeadComesFromTheEventCwdNotTheProcessCwd(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)

	cwd := markedCwd(t, "demo")
	runGit(t, cwd, "init")
	runGit(t, cwd, "config", "user.email", "t@example.com")
	runGit(t, cwd, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(cwd, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, cwd, "add", ".")
	runGit(t, cwd, "commit", "-m", "initial")
	wantHead := strings.TrimSpace(gitOut(t, cwd, "rev-parse", "HEAD"))
	if wantHead == "" {
		t.Fatal("fixture produced no HEAD")
	}

	// The process sits in a directory that is NOT a git repository.
	t.Chdir(t.TempDir())

	source := writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
	var out bytes.Buffer
	if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_githead", TranscriptPath: source, CWD: cwd}, v, "demo", &out, Options{}); err != nil {
		t.Fatal(err)
	}
	entries, err := archive.ListEntries(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(entries))
	}
	if got := entries[0].Manifest.GitHead; got != wantHead {
		t.Errorf("manifest git_head = %q, want the EVENT cwd's HEAD %q (an empty value means SourceCWD was never passed)", got, wantHead)
	}
}

// TestSessionEndSkipsDepartedSlugFromRecord drives the stale-checkout gate with
// a real departure record and asserts the SKIP, not merely the absence of
// files: the returned error must be nil, because a skip is a correct outcome
// and cmd/vp turns nil into exit 0. A version that let archive.Create refuse
// would satisfy every filesystem assertion here and still exit 2.
func TestSessionEndSkipsDepartedSlugFromRecord(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	cwd := markedCwd(t, "demo")

	rec, err := (departure.Record{Slug: "demo", Kind: departure.Renamed, To: "renamed-demo", Date: "2026-09-23"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	recAbs := filepath.Join(root, filepath.FromSlash(departure.RelPath("demo")))
	if err := os.MkdirAll(filepath.Dir(recAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recAbs, rec, 0o644); err != nil {
		t.Fatal(err)
	}

	logBuf := captureLog(t)
	source := writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
	var out bytes.Buffer
	launcher := &recordingLauncher{out: &out}
	if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_departed", TranscriptPath: source, CWD: cwd}, v, "demo", &out,
		Options{Launch: launcher.launch}); err != nil {
		t.Fatalf("a gate skip must return nil, not an error: %v", err)
	}
	entries, err := archive.ListEntries(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("departed slug was archived anyway: %+v", entries)
	}
	if strings.Contains(out.String(), "archived:") {
		t.Errorf("departed slug emitted a success line: %q", out.String())
	}
	if launcher.calls != 0 {
		t.Errorf("departed slug launched the ingester %d times", launcher.calls)
	}
	// The category before the first colon is what vp_bootstrap_context's health
	// alert prints; assert it literally.
	if !strings.Contains(logBuf.String(), "hook stale checkout:") {
		t.Errorf("missing the stale-checkout category in the log: %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "renamed-demo") {
		t.Errorf("the Warn dropped the redirect attribute: %q", logBuf.String())
	}
}

// TestSessionEndSkipsDepartedSlugWithNoRecord covers project.Departed's
// git-history fallback: no departure record exists, and the only evidence the
// project left is the commit that removed Projects/demo/.
//
// Measured, by deleting the gate and re-running: the writer refuses this case
// too, but as "project is not initialised in this vault" — an error, exit 2,
// and the wrong diagnosis for a project that was deliberately removed. The
// gate's contribution is the OUTCOME, not the absence of a write: nil, exit 0,
// and the "hook stale checkout" category the health alert counts. Both halves
// are asserted below.
func TestSessionEndSkipsDepartedSlugWithNoRecord(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "t@example.com")
	runGit(t, root, "config", "user.name", "T")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "add demo")
	if err := os.RemoveAll(filepath.Join(root, "Projects", "demo")); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-m", "remove Projects/demo")

	v := storage.NewVault(root)
	cwd := markedCwd(t, "demo")
	logBuf := captureLog(t)
	source := writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
	var out bytes.Buffer
	if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_history", TranscriptPath: source, CWD: cwd}, v, "demo", &out, Options{}); err != nil {
		t.Fatalf("a gate skip must return nil, not an error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Projects", "demo")); err == nil {
		t.Error("Projects/demo was resurrected by a Codex SessionEnd")
	}
	if strings.Contains(out.String(), "archived:") {
		t.Errorf("history-departed slug emitted a success line: %q", out.String())
	}
	if !strings.Contains(logBuf.String(), "hook stale checkout:") {
		t.Errorf("missing the stale-checkout category in the log: %q", logBuf.String())
	}
	// The departed arm must WIN over the uninitialised arm: both are true of
	// this vault, and only the first names what actually happened.
	if strings.Contains(logBuf.String(), "hook uninitialised project:") {
		t.Errorf("the uninitialised gate answered a departed slug first: %q", logBuf.String())
	}
}

// TestSessionEndSkipsUninitialisedProject: the vault has not initialised the
// marker's project. archive.Create would refuse this too, which is exactly why
// the nil-return assertion carries the test — without it, a build with the gate
// deleted passes the three filesystem checks and differs only in returning an
// error (exit 2) where the operator should see a skip (exit 0).
func TestSessionEndSkipsUninitialisedProject(t *testing.T) {
	root := t.TempDir() // deliberately NOT testutil.InitProject
	v := storage.NewVault(root)
	cwd := markedCwd(t, "demo")
	logBuf := captureLog(t)
	source := writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
	var out bytes.Buffer
	launcher := &recordingLauncher{out: &out}
	if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_uninit", TranscriptPath: source, CWD: cwd}, v, "demo", &out,
		Options{Launch: launcher.launch}); err != nil {
		t.Fatalf("a gate skip must return nil, not an error: %v", err)
	}
	entries, _ := archive.ListEntries(root, "demo")
	if len(entries) != 0 {
		t.Errorf("uninitialised project was archived anyway: %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(root, "Projects", "demo")); err == nil {
		t.Error("Projects/demo was created for an uninitialised project")
	}
	if strings.Contains(out.String(), "archived:") {
		t.Errorf("uninitialised project emitted a success line: %q", out.String())
	}
	if launcher.calls != 0 {
		t.Errorf("uninitialised project launched the ingester %d times", launcher.calls)
	}
	if !strings.Contains(logBuf.String(), "hook uninitialised project:") {
		t.Errorf("missing the uninitialised category in the log: %q", logBuf.String())
	}
}

// TestSessionEndSkipsCwdWithNoMarker covers the third gate arm. HandleSessionEnd
// hard-errors on a markerless cwd before ProcessSessionEnd is reached, so this
// arm only fires for a direct caller — it is defence in depth, and it is still
// asserted rather than assumed.
func TestSessionEndSkipsCwdWithNoMarker(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	logBuf := captureLog(t)
	source := writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
	var out bytes.Buffer
	// A directory with no .vibe-palace.toml anywhere above it.
	bare := t.TempDir()
	if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_nomarker", TranscriptPath: source, CWD: bare}, v, "demo", &out, Options{}); err != nil {
		t.Fatalf("a gate skip must return nil, not an error: %v", err)
	}
	entries, _ := archive.ListEntries(root, "demo")
	if len(entries) != 0 {
		t.Errorf("markerless cwd was archived anyway: %+v", entries)
	}
	if !strings.Contains(logBuf.String(), "no .vibe-palace.toml project signal") {
		t.Errorf("missing the no-project line in the log: %q", logBuf.String())
	}
}

func TestSessionEndOversizeTranscriptFailsWithoutReceipt(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	cwd := markedCwd(t, "demo")
	content := make([]byte, archive.DefaultMaxArchiveBytes+1)
	if _, err := rand.Read(content); err != nil {
		t.Fatal(err)
	}
	source := writeTranscript(t, content)
	var out bytes.Buffer
	launcher := &recordingLauncher{out: &out}
	started := time.Now()
	err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_large", TranscriptPath: source, CWD: cwd}, v, "demo", &out,
		Options{Launch: launcher.launch})
	if err == nil || time.Since(started) >= 3*time.Second {
		t.Fatalf("large source result err=%v elapsed=%s", err, time.Since(started))
	}
	if strings.Contains(out.String(), "archived:") {
		t.Fatalf("failure emitted success output: %q", out.String())
	}
	entries, listErr := archive.ListEntries(root, "demo")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("large failure committed manifest/receipt: %+v", entries)
	}
	if launcher.calls != 0 {
		t.Errorf("a failed archive launched the ingester %d times", launcher.calls)
	}
}

func TestSessionEndEmptyTranscriptReportsFailureWithoutReceipt(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	cwd := markedCwd(t, "demo")
	source := writeTranscript(t, nil)
	var out bytes.Buffer
	launcher := &recordingLauncher{out: &out}
	err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_empty", TranscriptPath: source, CWD: cwd}, storage.NewVault(root), "demo", &out,
		Options{Launch: launcher.launch})
	if err == nil || !strings.Contains(err.Error(), "usable non-empty regular file") {
		t.Fatalf("empty transcript error = %v", err)
	}
	if strings.Contains(out.String(), "archived:") {
		t.Fatalf("empty transcript emitted success output: %q", out.String())
	}
	entries, listErr := archive.ListEntries(root, "demo")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("empty source created archive: %+v", entries)
	}
	if launcher.calls != 0 {
		t.Errorf("an empty transcript launched the ingester %d times", launcher.calls)
	}
}

func TestSessionEndMissingTranscriptReportsFailureWithoutReceipt(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	cwd := markedCwd(t, "demo")
	var out bytes.Buffer
	launcher := &recordingLauncher{out: &out}
	err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_missing", TranscriptPath: filepath.Join(t.TempDir(), "missing"), CWD: cwd}, storage.NewVault(root), "demo", &out,
		Options{Launch: launcher.launch})
	if err == nil || !strings.Contains(err.Error(), "transcript unavailable") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), "archived:") {
		t.Fatalf("failure emitted success output: %q", out.String())
	}
	entries, listErr := archive.ListEntries(root, "demo")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 0 {
		t.Fatalf("missing source created archive: %+v", entries)
	}
	if launcher.calls != 0 {
		t.Errorf("a missing transcript launched the ingester %d times", launcher.calls)
	}
}

func TestSessionEndReportsArchiveSuccessReceiptFailure(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	cwd := markedCwd(t, "demo")
	dir, err := v.SessionDir("demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := v.EnsureVaultDir(dir); err != nil {
		t.Fatal(err)
	}
	release, err := vaultlock.Acquire(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	path := writeTranscript(t, []byte("x"))
	var out bytes.Buffer
	launcher := &recordingLauncher{out: &out}
	err = ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_fail", TranscriptPath: path, CWD: cwd}, v, "demo", &out,
		Options{Launch: launcher.launch})
	if err == nil || !strings.Contains(err.Error(), "archive created; note/link incomplete") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(out.String(), "archived:") {
		t.Fatalf("failure emitted archived line: %q", out.String())
	}
	entries, listErr := archive.ListEntries(root, "demo")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(entries) != 1 {
		t.Fatalf("archive should remain available for retry: %+v", entries)
	}
	// A receipt that failed means the note the drain would index is not there
	// yet; the ingester must not be told to prioritise this archive.
	if launcher.calls != 0 {
		t.Errorf("a failed receipt launched the ingester %d times", launcher.calls)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitenv.SafeGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitenv.SafeGitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
