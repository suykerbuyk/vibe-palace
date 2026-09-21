// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/jobqueue"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

const checkSummQueueSlug = "proj"

// newCheckSummQueueVault builds a minimal *storage.Vault backed by a fresh
// t.TempDir(), mirroring the storage.NewVault(t.TempDir()) pattern already
// used throughout internal/check's own tests (e.g. TestCheckStrayScaffolds).
func newCheckSummQueueVault(t *testing.T) *storage.Vault {
	t.Helper()
	return storage.NewVault(t.TempDir())
}

// writeSummarizationHostConfig isolates XDG_CONFIG_HOME to a fresh temp dir
// for this test and writes the given [summarization] body into the HOST
// GLOBAL config there (storage.VaultConfigFilePath). The host global config is
// the only tier that can carry [summarization]: the vault
// Projects/<slug>/config.toml is no longer read, and the host-local
// per-project file carries only palace.scoring. The isolation is asserted
// with testutil.RequireResolvedUnder so the write can never reach the
// developer's real host config or the hermetic read-only fixture.
func writeSummarizationHostConfig(t *testing.T, body string) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	testutil.RequireResolvedUnder(t, xdg, storage.VaultConfigFilePath)
	cfgPath, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatalf("VaultConfigFilePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir host config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write host config: %v", err)
	}
}

// writeQueueFile plants a raw queue-directory file directly on disk, letting
// tests control the exact suffix (plain *.json, *.json.processing,
// *.json.failed) independent of internal/summarize's own enqueue path.
func writeQueueFile(t *testing.T, projectPath, name string, item summarize.SummaryItem) string {
	t.Helper()
	dir := summarize.QueueDir(projectPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir queue dir: %v", err)
	}
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal item: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func iterItem(n int) summarize.SummaryItem {
	return summarize.SummaryItem{Kind: summarize.KindIteration, Project: checkSummQueueSlug, Iteration: n}
}

// ---------------------------------------------------------------------------
// 1. Empty queue (missing dir, and an existing-but-empty dir) -> Pass.
// ---------------------------------------------------------------------------

func TestCheckSummarizationQueue_MissingDirIsPass(t *testing.T) {
	v := newCheckSummQueueVault(t)
	projectPath := t.TempDir() // never touches summarize.QueueDir at all

	r := CheckSummarizationQueue(v, projectPath, checkSummQueueSlug)
	if r.Status != Pass {
		t.Fatalf("Status = %v, want Pass; summary=%q details=%v", r.Status, r.Summary, r.Details)
	}
	if r.Summary != "empty" {
		t.Errorf("Summary = %q, want %q", r.Summary, "empty")
	}
}

func TestCheckSummarizationQueue_ExistingEmptyDirIsPass(t *testing.T) {
	v := newCheckSummQueueVault(t)
	projectPath := t.TempDir()
	if err := os.MkdirAll(summarize.QueueDir(projectPath), 0o755); err != nil {
		t.Fatalf("mkdir queue dir: %v", err)
	}

	r := CheckSummarizationQueue(v, projectPath, checkSummQueueSlug)
	if r.Status != Pass {
		t.Fatalf("Status = %v, want Pass; summary=%q details=%v", r.Status, r.Summary, r.Details)
	}
	if r.Summary != "empty" {
		t.Errorf("Summary = %q, want %q", r.Summary, "empty")
	}
}

// ---------------------------------------------------------------------------
// 2. Disabled config with pending files -> Info, distinct "not configured"
//    wording.
// ---------------------------------------------------------------------------

func TestCheckSummarizationQueue_DisabledConfigIsInfoWithExpectedBacklogWording(t *testing.T) {
	v := newCheckSummQueueVault(t)
	projectPath := t.TempDir()
	writeSummarizationHostConfig(t, "[summarization]\nenabled = false\n")
	writeQueueFile(t, projectPath, "iteration-00001.json", iterItem(1))
	writeQueueFile(t, projectPath, "iteration-00002.json", iterItem(2))

	r := CheckSummarizationQueue(v, projectPath, checkSummQueueSlug)
	if r.Status != Info {
		t.Fatalf("Status = %v, want Info; summary=%q", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "not configured") {
		t.Errorf("Summary = %q, want it to contain %q", r.Summary, "not configured")
	}
	// It must send the operator to the one tier that can set [summarization]:
	// the host config, named by its resolved path — never "this project".
	hostPath, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Summary, hostPath) || strings.Contains(r.Summary, "for this project") {
		t.Errorf("Summary = %q, want it to name the host config %s and no per-project config", r.Summary, hostPath)
	}
	if strings.Contains(r.Summary, "enabled but unresolvable") || strings.Contains(r.Summary, "investigate why drain isn't keeping up") {
		t.Errorf("Summary = %q, unexpectedly contains case-3/case-4 wording", r.Summary)
	}
}

// ---------------------------------------------------------------------------
// 3. Enabled but unresolvable with pending files -> Info, names the real
//    underlying error text, distinct from case 2.
// ---------------------------------------------------------------------------

func TestCheckSummarizationQueue_EnabledUnresolvableNamesUnderlyingError(t *testing.T) {
	v := newCheckSummQueueVault(t)
	projectPath := t.TempDir()
	// api_key_env named but never set in the environment: both
	// NewIterationSummarizerFromConfig and NewSessionNoteSummarizerFromConfig
	// return this exact error text.
	const envName = "VP_TEST_CHECK_SUMMQUEUE_UNSET_KEY"
	t.Setenv(envName, "")
	writeSummarizationHostConfig(t, fmt.Sprintf(
		"[summarization]\nenabled = true\nprovider = \"anthropic\"\nmodel = \"claude-test\"\napi_key_env = %q\n", envName))
	writeQueueFile(t, projectPath, "iteration-00001.json", iterItem(1))

	r := CheckSummarizationQueue(v, projectPath, checkSummQueueSlug)
	if r.Status != Info {
		t.Fatalf("Status = %v, want Info; summary=%q", r.Status, r.Summary)
	}
	wantErrText := fmt.Sprintf("summarization enabled but api key env %q is unset", envName)
	if !strings.Contains(r.Summary, "enabled but unresolvable") {
		t.Errorf("Summary = %q, want it to contain %q", r.Summary, "enabled but unresolvable")
	}
	if !strings.Contains(r.Summary, wantErrText) {
		t.Errorf("Summary = %q, want it to contain the underlying error %q", r.Summary, wantErrText)
	}
	if strings.Contains(r.Summary, "not configured") || strings.Contains(r.Summary, "investigate why drain isn't keeping up") {
		t.Errorf("Summary = %q, unexpectedly contains case-2/case-4 wording", r.Summary)
	}
}

// ---------------------------------------------------------------------------
// 4. Enabled and resolving, pending over the escalation threshold -> Info
//    (never Fail), "investigate why drain isn't keeping up" wording, distinct
//    from cases 2 and 3.
// ---------------------------------------------------------------------------

func TestCheckSummarizationQueue_EnabledResolvingOverThresholdEscalates(t *testing.T) {
	v := newCheckSummQueueVault(t)
	projectPath := t.TempDir()
	const envName = "VP_TEST_CHECK_SUMMQUEUE_RESOLVING_KEY"
	t.Setenv(envName, "sk-test-resolves-fine")
	// provider "anthropic" needs no base_url to construct successfully (see
	// internal/llm/anthropic.go's newAnthropicClient — endpoint defaults at
	// call time, not construction time), matching
	// internal/itersummary's own TestNewIterationSummarizerFromConfig_Resolvable.
	writeSummarizationHostConfig(t, fmt.Sprintf(
		"[summarization]\nenabled = true\nprovider = \"anthropic\"\nmodel = \"claude-test\"\napi_key_env = %q\ntimeout_seconds = 5\n", envName))

	for i := 1; i <= summarizationQueuePendingEscalationThreshold+1; i++ {
		writeQueueFile(t, projectPath, fmt.Sprintf("iteration-%05d.json", i), iterItem(i))
	}

	r := CheckSummarizationQueue(v, projectPath, checkSummQueueSlug)
	if r.Status == Fail {
		t.Fatalf("Status = Fail, want never-Fail (Info); summary=%q", r.Summary)
	}
	if r.Status != Info {
		t.Fatalf("Status = %v, want Info; summary=%q", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "investigate why drain isn't keeping up") {
		t.Errorf("Summary = %q, want it to contain %q", r.Summary, "investigate why drain isn't keeping up")
	}
	if strings.Contains(r.Summary, "not configured") || strings.Contains(r.Summary, "enabled but unresolvable") {
		t.Errorf("Summary = %q, unexpectedly contains case-2/case-3 wording", r.Summary)
	}
}

// ---------------------------------------------------------------------------
// 5. .failed entries present: additive, surfaced regardless of the pending
//    clause.
// ---------------------------------------------------------------------------

func TestCheckSummarizationQueue_DeadLetterAloneSurfacesDistinctly(t *testing.T) {
	v := newCheckSummQueueVault(t)
	projectPath := t.TempDir()
	writeQueueFile(t, projectPath, "iteration-00001.json.failed", iterItem(1))

	r := CheckSummarizationQueue(v, projectPath, checkSummQueueSlug)
	if r.Status != Info {
		t.Fatalf("Status = %v, want Info (zero pending + one dead-lettered must not be Pass); summary=%q", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "1 dead-lettered") && !detailsContain(r.Details, "dead-lettered") {
		t.Errorf("dead-letter fact not surfaced distinctly; summary=%q details=%v", r.Summary, r.Details)
	}
	if !detailsContain(r.Details, "permanently dead-lettered") {
		t.Errorf("expected an additive dead-letter advisory Details line; details=%v", r.Details)
	}
}

func TestCheckSummarizationQueue_DeadLetterIsAdditiveAlongsidePendingClause(t *testing.T) {
	v := newCheckSummQueueVault(t)
	projectPath := t.TempDir()
	writeSummarizationHostConfig(t, "[summarization]\nenabled = false\n")
	writeQueueFile(t, projectPath, "iteration-00001.json", iterItem(1))
	writeQueueFile(t, projectPath, "iteration-00002.json.failed", iterItem(2))
	writeQueueFile(t, projectPath, "iteration-00003.json.failed", iterItem(3))

	r := CheckSummarizationQueue(v, projectPath, checkSummQueueSlug)
	if r.Status != Info {
		t.Fatalf("Status = %v, want Info; summary=%q", r.Status, r.Summary)
	}
	// The pending-count clause (case 2's "not configured" wording) must still
	// fire...
	if !strings.Contains(r.Summary, "not configured") {
		t.Errorf("Summary = %q, want it to still contain the disabled-config wording", r.Summary)
	}
	// ...PLUS the dead-letter fact must be mentioned distinctly, additively.
	if !detailsContain(r.Details, "permanently dead-lettered") {
		t.Errorf("expected an additive dead-letter advisory Details line alongside the pending clause; details=%v", r.Details)
	}
	if !detailsContain(r.Details, "dead-lettered (.failed): 2") {
		t.Errorf("expected the raw dead-lettered count of 2 in Details; details=%v", r.Details)
	}
}

func detailsContain(details []string, substr string) bool {
	for _, d := range details {
		if strings.Contains(d, substr) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 6. Oldest-pending age reflects the actual oldest file's mtime.
// ---------------------------------------------------------------------------

func TestCheckSummarizationQueue_OldestPendingAgeIsTheOldestNotNewestOrAverage(t *testing.T) {
	v := newCheckSummQueueVault(t)
	projectPath := t.TempDir()

	now := time.Now()
	newest := now.Add(-1 * time.Hour)
	middle := now.Add(-10 * time.Hour)
	oldest := now.Add(-100 * time.Hour)

	p1 := writeQueueFile(t, projectPath, "iteration-00001.json", iterItem(1))
	p2 := writeQueueFile(t, projectPath, "iteration-00002.json", iterItem(2))
	p3 := writeQueueFile(t, projectPath, "iteration-00003.json", iterItem(3))

	if err := os.Chtimes(p1, newest, newest); err != nil {
		t.Fatalf("Chtimes p1: %v", err)
	}
	if err := os.Chtimes(p2, oldest, oldest); err != nil {
		t.Fatalf("Chtimes p2: %v", err)
	}
	if err := os.Chtimes(p3, middle, middle); err != nil {
		t.Fatalf("Chtimes p3: %v", err)
	}

	r := CheckSummarizationQueue(v, projectPath, checkSummQueueSlug)
	if r.Status != Info {
		t.Fatalf("Status = %v, want Info; summary=%q", r.Status, r.Summary)
	}

	var ageLine string
	for _, d := range r.Details {
		if strings.HasPrefix(d, "oldest pending age:") {
			ageLine = d
		}
	}
	if ageLine == "" {
		t.Fatalf("no 'oldest pending age' Details line found; details=%v", r.Details)
	}

	// The reported age must be close to 100h (the oldest file), not ~1h (the
	// newest) and not ~37h (the average of 1h/10h/100h).
	wantAge := time.Since(oldest)
	gotDuration, err := parseAgeLine(ageLine)
	if err != nil {
		t.Fatalf("parse age line %q: %v", ageLine, err)
	}
	diff := gotDuration - wantAge
	if diff < 0 {
		diff = -diff
	}
	if diff > 5*time.Minute {
		t.Errorf("age line %q parsed to %s, want close to the OLDEST file's age %s (not newest ~1h or average ~37h)",
			ageLine, gotDuration, wantAge)
	}
}

// parseAgeLine extracts the time.Duration from a "oldest pending age: <dur>"
// Details line, as produced by time.Duration.Round(time.Second).String().
func parseAgeLine(line string) (time.Duration, error) {
	const prefix = "oldest pending age: "
	if !strings.HasPrefix(line, prefix) {
		return 0, fmt.Errorf("line %q missing prefix %q", line, prefix)
	}
	return time.ParseDuration(strings.TrimPrefix(line, prefix))
}

// ---------------------------------------------------------------------------
// Suffix-precision sanity check: a ".failed" or ".processing" entry must
// never also be counted as pending.
// ---------------------------------------------------------------------------

func TestCheckSummarizationQueue_SuffixBucketingIsPrecise(t *testing.T) {
	v := newCheckSummQueueVault(t)
	projectPath := t.TempDir()
	writeQueueFile(t, projectPath, "iteration-00001.json", iterItem(1))
	writeQueueFile(t, projectPath, "iteration-00002.json"+jobqueue.ProcessingSuffix, iterItem(2))
	writeQueueFile(t, projectPath, "iteration-00003.json"+jobqueue.FailedSuffix, iterItem(3))

	r := CheckSummarizationQueue(v, projectPath, checkSummQueueSlug)
	if !detailsContain(r.Details, "pending: 1") {
		t.Errorf("expected exactly 1 pending; details=%v", r.Details)
	}
	if !detailsContain(r.Details, "claimed (.processing): 1") {
		t.Errorf("expected exactly 1 claimed; details=%v", r.Details)
	}
	if !detailsContain(r.Details, "dead-lettered (.failed): 1") {
		t.Errorf("expected exactly 1 dead-lettered; details=%v", r.Details)
	}
}
