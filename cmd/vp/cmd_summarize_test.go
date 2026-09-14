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
	"sync/atomic"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// summarizeTestFixture bundles everything runSummarizeIterations needs: a
// hermetic project directory (own .vibe-palace.toml vault_path + [project]
// name override, exactly like cmd_drain_test.go's setupDrainProject/
// bracketed-path test), a project config with [summarization] enabled and
// pointed at an httptest server, and an atomic request counter that server
// increments on every call — the "call-counting stub" this file's tests use
// in place of injecting a stub llm.Completer directly: itersummary.
// IterationSummarizer's fields are unexported, so from cmd/vp (a different
// package) the only way to get a real, controllable IterationSummarizer is
// through NewIterationSummarizerFromConfig's own config-resolution path,
// pointed at a local, canned-response HTTP server — the same technique
// internal/hook/hook_test.go's own enrichment tests already use.
type summarizeTestFixture struct {
	projectPath string
	vault       *storage.Vault
	slug        string
	srv         *httptest.Server
	calls       *int32 // atomic: number of HTTP requests the server has received
}

// canned is the JSON body the fixture's server responds with by default.
const summarizeTestCanned = `{"choices":[{"message":{"role":"assistant","content":"{\"summary\":\"Summarized.\",\"decisions\":[],\"unblocks\":\"\"}"}}]}`

func newSummarizeTestFixture(t *testing.T) *summarizeTestFixture {
	t.Helper()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(summarizeTestCanned))
	}))
	t.Cleanup(srv.Close)

	envName := fmt.Sprintf("VP_TEST_SUMMARIZE_KEY_%s", strings.ReplaceAll(t.Name(), "/", "_"))
	t.Setenv(envName, "sk-summarize-test")

	projectPath := t.TempDir()
	vaultRoot := t.TempDir()
	const slug = "test-project"

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
		"model = \"summarize-test-model\"\n" +
		"api_key_env = \"" + envName + "\"\n" +
		"base_url = \"" + srv.URL + "\"\n" +
		"max_tokens = 512\n" +
		"timeout_seconds = 10\n"
	if err := os.WriteFile(cfgPath, []byte(summCfg), 0o644); err != nil {
		t.Fatalf("write summarization config: %v", err)
	}

	return &summarizeTestFixture{
		projectPath: projectPath,
		vault:       vault,
		slug:        slug,
		srv:         srv,
		calls:       &calls,
	}
}

// writeIterations writes content as the fixture project's iterations.md.
func (f *summarizeTestFixture) writeIterations(t *testing.T, content string) {
	t.Helper()
	path, err := f.vault.IterationsFile(f.slug)
	if err != nil {
		t.Fatalf("IterationsFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir iterations dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write iterations.md: %v", err)
	}
}

func (f *summarizeTestFixture) callCount() int32 {
	return atomic.LoadInt32(f.calls)
}

const threeEntryFixture = "## Iteration 1 — First\n\nDid the first thing.\n\n---\n\n" +
	"## Iteration 2 — Second\n\nDid the second thing.\n\n---\n\n" +
	"## Iteration 3 — Third\n\nDid the third thing.\n"

// TestRunSummarizeIterations_HappyPath proves the base case: three entries,
// no duplicates, no cache yet. A single run without --force must summarize
// all three (one real HTTP call each), write a cache file for each, and
// report summarized=3 skipped=0.
func TestRunSummarizeIterations_HappyPath(t *testing.T) {
	f := newSummarizeTestFixture(t)
	f.writeIterations(t, threeEntryFixture)

	var buf bytes.Buffer
	code := runSummarizeIterations(f.projectPath, false, &buf)
	if code != cli.ExitOK {
		t.Fatalf("code = %d, want ExitOK; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "summarized=3 skipped=0") {
		t.Errorf("expected summarized=3 skipped=0 in output, got: %s", buf.String())
	}
	if got := f.callCount(); got != 3 {
		t.Errorf("HTTP call count = %d, want 3", got)
	}

	for _, n := range []int{1, 2, 3} {
		got, ok, err := f.vault.ReadIterationSummary(f.slug, n)
		if err != nil {
			t.Fatalf("ReadIterationSummary(%d): %v", n, err)
		}
		if !ok {
			t.Errorf("n=%d: expected a cached summary, found none", n)
			continue
		}
		if got.Summary != "Summarized." {
			t.Errorf("n=%d: Summary = %q, want %q", n, got.Summary, "Summarized.")
		}
		if got.MatchIndex != 0 {
			t.Errorf("n=%d: MatchIndex = %d, want 0", n, got.MatchIndex)
		}
	}
}

// TestRunSummarizeIterations_SecondRunSkipsAlreadyCached is the additive-
// policy proof: run once (populates the cache for all three entries), reset
// the call counter, then run again WITHOUT --force. The summarizer must NOT
// be invoked again for any of the three already-cached entries — the HTTP
// call count must stay at zero for this second run — and the output must
// report skipped=3 summarized=0.
//
// This is not merely "the second run also happens to report success": if
// runSummarizeIterations's cache check were missing or broken (e.g. it
// always fell through to calling the summarizer, or compared against the
// wrong field), this second run would make three MORE real HTTP calls and
// this assertion on the call count would fail. The complementary
// TestRunSummarizeIterations_ForceRegeneratesEverything test below proves
// the opposite direction — that summarization would in fact happen again
// given --force against this exact same cached state — which is the direct
// evidence that skipping here is the cache check firing, not some unrelated
// reason (e.g. a bug that always no-ops on the second call regardless of
// --force).
func TestRunSummarizeIterations_SecondRunSkipsAlreadyCached(t *testing.T) {
	f := newSummarizeTestFixture(t)
	f.writeIterations(t, threeEntryFixture)

	var buf1 bytes.Buffer
	if code := runSummarizeIterations(f.projectPath, false, &buf1); code != cli.ExitOK {
		t.Fatalf("first run: code = %d, want ExitOK; output: %s", code, buf1.String())
	}
	if got := f.callCount(); got != 3 {
		t.Fatalf("first run: HTTP call count = %d, want 3", got)
	}

	var buf2 bytes.Buffer
	code := runSummarizeIterations(f.projectPath, false, &buf2)
	if code != cli.ExitOK {
		t.Fatalf("second run: code = %d, want ExitOK; output: %s", code, buf2.String())
	}
	if !strings.Contains(buf2.String(), "summarized=0 skipped=3") {
		t.Errorf("second run: expected summarized=0 skipped=3 in output, got: %s", buf2.String())
	}
	if got := f.callCount(); got != 3 {
		t.Errorf("second run: HTTP call count = %d, want still 3 (no new calls — the summarizer must not be invoked for cached entries)", got)
	}
}

// TestRunSummarizeIterations_ForceRegeneratesEverything proves --force
// bypasses the cache check entirely: starting from the SAME fully-cached
// state TestRunSummarizeIterations_SecondRunSkipsAlreadyCached exercises
// without --force (where the call count stays flat), a --force run must
// re-summarize every entry — the call count must increase by exactly 3 more
// — and report summarized=3 skipped=0, overwriting the existing cache files.
func TestRunSummarizeIterations_ForceRegeneratesEverything(t *testing.T) {
	f := newSummarizeTestFixture(t)
	f.writeIterations(t, threeEntryFixture)

	var buf1 bytes.Buffer
	if code := runSummarizeIterations(f.projectPath, false, &buf1); code != cli.ExitOK {
		t.Fatalf("first run: code = %d, want ExitOK; output: %s", code, buf1.String())
	}
	if got := f.callCount(); got != 3 {
		t.Fatalf("first run: HTTP call count = %d, want 3", got)
	}

	var buf2 bytes.Buffer
	code := runSummarizeIterations(f.projectPath, true, &buf2)
	if code != cli.ExitOK {
		t.Fatalf("force run: code = %d, want ExitOK; output: %s", code, buf2.String())
	}
	if !strings.Contains(buf2.String(), "summarized=3 skipped=0") {
		t.Errorf("force run: expected summarized=3 skipped=0 in output, got: %s", buf2.String())
	}
	if got := f.callCount(); got != 6 {
		t.Errorf("force run: HTTP call count = %d, want 6 (3 from the first run + 3 more from --force re-summarizing every entry)", got)
	}
}

// TestRunSummarizeIterations_StaleCacheResummarizedWithoutForce is a second,
// more targeted additive-policy proof: it manually plants a STALE cache (a
// storage.IterationSummary whose MatchIndex no longer equals the entry's
// current matchIndex — the "a newer same-N entry was appended since this
// summary was generated" scenario storage.IterationSummary's own doc comment
// describes) for one entry, alongside a genuinely fresh cache for another.
// A run WITHOUT --force must re-summarize only the stale one (one new HTTP
// call) and continue skipping the fresh one (call count does not double-
// count it). This demonstrates the skip check compares MatchIndex, not
// merely "a cache file exists at all" — a weaker check that would
// incorrectly skip the stale entry too.
func TestRunSummarizeIterations_StaleCacheResummarizedWithoutForce(t *testing.T) {
	f := newSummarizeTestFixture(t)
	f.writeIterations(t, "## Iteration 1 — First\n\nDid the first thing.\n\n---\n\n"+
		"## Iteration 2 — Second\n\nDid the second thing.\n")

	// Entry 1: a FRESH cache (MatchIndex 0, matching its current, only
	// match).
	if err := f.vault.WriteIterationSummary(f.slug, storage.IterationSummary{
		N: 1, MatchIndex: 0, Summary: "Pre-cached fresh.", Model: "prior-model", GeneratedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed fresh cache: %v", err)
	}
	// Entry 2: a STALE cache — MatchIndex 5 can never equal this entry's
	// real (and only) matchIndex of 0, simulating "this cache was generated
	// against a since-superseded match".
	if err := f.vault.WriteIterationSummary(f.slug, storage.IterationSummary{
		N: 2, MatchIndex: 5, Summary: "Pre-cached stale.", Model: "prior-model", GeneratedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed stale cache: %v", err)
	}

	var buf bytes.Buffer
	code := runSummarizeIterations(f.projectPath, false, &buf)
	if code != cli.ExitOK {
		t.Fatalf("code = %d, want ExitOK; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "summarized=1 skipped=1") {
		t.Errorf("expected summarized=1 skipped=1 in output, got: %s", buf.String())
	}
	if got := f.callCount(); got != 1 {
		t.Errorf("HTTP call count = %d, want 1 (only the stale entry should be re-summarized)", got)
	}

	// Entry 1's fresh cache must be untouched.
	got1, ok, err := f.vault.ReadIterationSummary(f.slug, 1)
	if err != nil || !ok {
		t.Fatalf("ReadIterationSummary(1): ok=%v err=%v", ok, err)
	}
	if got1.Summary != "Pre-cached fresh." {
		t.Errorf("n=1: Summary = %q, want unchanged %q", got1.Summary, "Pre-cached fresh.")
	}

	// Entry 2's stale cache must have been overwritten with a fresh
	// MatchIndex and the canned response's content.
	got2, ok, err := f.vault.ReadIterationSummary(f.slug, 2)
	if err != nil || !ok {
		t.Fatalf("ReadIterationSummary(2): ok=%v err=%v", ok, err)
	}
	if got2.Summary != "Summarized." {
		t.Errorf("n=2: Summary = %q, want %q (should have been regenerated)", got2.Summary, "Summarized.")
	}
	if got2.MatchIndex != 0 {
		t.Errorf("n=2: MatchIndex = %d, want 0 (refreshed)", got2.MatchIndex)
	}
}

// TestRunSummarizeIterations_MultiEntryPerN proves that when two entries
// share the same N, only the LAST file-order match is ever summarized — the
// non-last entry sharing that N is never passed to the summarizer at all
// (the HTTP call count must be exactly 1, not 2), matching
// internal/search/iterations.go's and internal/itersummary's own established
// "current last file-order match" convention.
func TestRunSummarizeIterations_MultiEntryPerN(t *testing.T) {
	f := newSummarizeTestFixture(t)
	f.writeIterations(t, "## Iteration 12 — First pass\n\nFirst pass body.\n\n---\n\n"+
		"## Iteration 12 — Second pass\n\nSecond pass body.\n")

	var buf bytes.Buffer
	code := runSummarizeIterations(f.projectPath, false, &buf)
	if code != cli.ExitOK {
		t.Fatalf("code = %d, want ExitOK; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "summarized=1 skipped=0") {
		t.Errorf("expected summarized=1 skipped=0 in output, got: %s", buf.String())
	}
	if got := f.callCount(); got != 1 {
		t.Errorf("HTTP call count = %d, want 1 (only the last of the two same-N entries should ever be summarized)", got)
	}

	got, ok, err := f.vault.ReadIterationSummary(f.slug, 12)
	if err != nil || !ok {
		t.Fatalf("ReadIterationSummary(12): ok=%v err=%v", ok, err)
	}
	if got.MatchIndex != 1 {
		t.Errorf("MatchIndex = %d, want 1 (the second/last of two matches)", got.MatchIndex)
	}
}

// TestRunSummarizeIterations_MissingIterationsFileIsOK proves that a project
// with a resolvable, enabled [summarization] config but NO iterations.md at
// all yet is a legitimate empty state (summarized=0 skipped=0, ExitOK), not
// an error — mirroring collectIterationCorpus's own "missing file is not an
// error" contract for the same file.
func TestRunSummarizeIterations_MissingIterationsFileIsOK(t *testing.T) {
	f := newSummarizeTestFixture(t)
	// Deliberately do NOT call f.writeIterations — no iterations.md exists.

	var buf bytes.Buffer
	code := runSummarizeIterations(f.projectPath, false, &buf)
	if code != cli.ExitOK {
		t.Fatalf("code = %d, want ExitOK; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "summarized=0 skipped=0") {
		t.Errorf("expected summarized=0 skipped=0 in output, got: %s", buf.String())
	}
	if got := f.callCount(); got != 0 {
		t.Errorf("HTTP call count = %d, want 0 (nothing to summarize)", got)
	}
}

// TestRunSummarizeIterations_DisabledSummarizationIsUserError proves that,
// unlike `vp drain summaries`, a project with [summarization] disabled (or
// simply absent) is a real, reported failure here — not a silent no-op —
// since this command's whole purpose is to summarize.
func TestRunSummarizeIterations_DisabledSummarizationIsUserError(t *testing.T) {
	projectPath := t.TempDir()
	vaultRoot := t.TempDir()
	cfg := fmt.Sprintf("vault_path = %q\n", vaultRoot)
	if err := os.WriteFile(filepath.Join(projectPath, ".vibe-palace.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}

	var buf bytes.Buffer
	code := runSummarizeIterations(projectPath, false, &buf)
	if code != cli.ExitUser {
		t.Fatalf("code = %d, want ExitUser; output: %s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "not enabled") {
		t.Errorf("expected a not-enabled message, got: %s", buf.String())
	}
}

func TestCmdSummarizeIterations_MissingProjectPathIsUserError(t *testing.T) {
	cmd := cmdSummarizeIterations()
	code := cmd.Run([]string{})
	if code != cli.ExitUser {
		t.Errorf("code = %d, want ExitUser", code)
	}
}

// TestCmdSummarizeIterations_RelativeProjectPathIsUserError exercises the
// real CLI entrypoint's own --project-path absoluteness check (mirroring
// `vp drain summaries`'s identical guard) through actual argv-shaped flags,
// not runSummarizeIterations directly.
func TestCmdSummarizeIterations_RelativeProjectPathIsUserError(t *testing.T) {
	cmd := cmdSummarizeIterations()
	code := cmd.Run([]string{"--project-path", "."})
	if code != cli.ExitUser {
		t.Errorf("code = %d, want ExitUser", code)
	}
}

// TestCmdSummarizeIterations_RunEndToEndThroughRealFlagParsing drives the
// actual CLI entrypoint (cmd.Run, real argv-shaped flags including --force)
// rather than calling runSummarizeIterations directly, proving the flag
// parsing, --project-path/--force wiring, and testable-body call all connect
// correctly end-to-end — the other tests in this file only ever call
// runSummarizeIterations directly and would not catch a bug in cmdSummarizeIterations's
// own Run closure (e.g. --force never actually being read from fv.Bool, or
// the wrong variable being passed through).
func TestCmdSummarizeIterations_RunEndToEndThroughRealFlagParsing(t *testing.T) {
	f := newSummarizeTestFixture(t)
	f.writeIterations(t, threeEntryFixture)

	cmd := cmdSummarizeIterations()
	code := cmd.Run([]string{"--project-path", f.projectPath})
	if code != cli.ExitOK {
		t.Fatalf("first run: code = %d, want ExitOK", code)
	}
	if got := f.callCount(); got != 3 {
		t.Fatalf("first run: HTTP call count = %d, want 3", got)
	}

	// A second real-flag-parsed run WITHOUT --force must not re-summarize.
	code = cmd.Run([]string{"--project-path", f.projectPath})
	if code != cli.ExitOK {
		t.Fatalf("second run (no --force): code = %d, want ExitOK", code)
	}
	if got := f.callCount(); got != 3 {
		t.Fatalf("second run (no --force): HTTP call count = %d, want still 3 (no new calls)", got)
	}

	// A third real-flag-parsed run WITH --force must re-summarize everything,
	// proving --force actually reaches runSummarizeIterations through the
	// real flag parser, not just when called directly in other tests.
	code = cmd.Run([]string{"--project-path", f.projectPath, "--force"})
	if code != cli.ExitOK {
		t.Fatalf("third run (--force): code = %d, want ExitOK", code)
	}
	if got := f.callCount(); got != 6 {
		t.Fatalf("third run (--force): HTTP call count = %d, want 6 (3 more)", got)
	}
}

func TestCmdSummarize_IsABareParent(t *testing.T) {
	cmd := cmdSummarize()
	if cmd.Name != "summarize" {
		t.Errorf("Name = %q, want %q", cmd.Name, "summarize")
	}
	if cmd.Run != nil {
		t.Errorf("expected a nil Run (pure parent, like cmdDrain())")
	}
}
