// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/enrichment"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// queuePath builds the enrichment-queue filename for the standard 2026-06-21
// test date, mirroring EnqueueEnrichment's naming (host-scoped when fp != "",
// legacy otherwise).
func queuePath(dir, fp string, iter int) string {
	return filepath.Join(dir, storage.SessionStem("2026-06-21", fp, iter)+".json")
}

// stripFrontmatter returns just the markdown body of a session file (the part
// after the closing frontmatter delimiter), so body-only comparisons ignore
// timestamp-bearing frontmatter.
func stripFrontmatter(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := string(data)
	const open = "---\n"
	if !strings.HasPrefix(s, open) {
		t.Fatalf("missing opening frontmatter in %s", path)
	}
	rest := s[len(open):]
	_, after, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		t.Fatalf("missing closing frontmatter in %s", path)
	}
	return after
}

func workingEnricher() *enrichment.Enricher {
	return enrichment.NewEnricher(mockCompleter{resp: cannedEnrichment}, "test-model", 5*time.Second, "")
}

func failingEnricher() *enrichment.Enricher {
	return enrichment.NewEnricher(mockCompleter{err: fmt.Errorf("boom")}, "test-model", 5*time.Second, "")
}

func TestEnqueueEnrichmentRoundTrip(t *testing.T) {
	cwd := t.TempDir()
	in := enrichment.PromptInput{
		UserText:         "user said things",
		AssistantText:    "assistant replied",
		FilesChanged:     []string{"a.go", "b.go"},
		NarrativeSummary: "plain summary",
		NarrativeTag:     "implementation",
	}
	const fp = "a1b2c3d4"
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 3, "/notes/x.md", in); err != nil {
		t.Fatalf("EnqueueEnrichment: %v", err)
	}

	// The queue filename is host-scoped by the fingerprint.
	path := filepath.Join(cwd, ".vibe-palace", "enrichment-queue", "2026-06-21-a1b2c3d4-03.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read queue file: %v", err)
	}
	var item enrichmentItem
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if item.Project != "proj" || item.Date != "2026-06-21" || item.Iteration != 3 {
		t.Errorf("coords = %+v, want proj/2026-06-21/3", item)
	}
	if item.Fingerprint != fp {
		t.Errorf("fingerprint = %q, want %q", item.Fingerprint, fp)
	}
	if item.NotePath != "/notes/x.md" {
		t.Errorf("note_path = %q", item.NotePath)
	}
	if item.Prompt.UserText != "user said things" || item.Prompt.NarrativeTag != "implementation" {
		t.Errorf("prompt round-trip mismatch: %+v", item.Prompt)
	}
	if len(item.Prompt.FilesChanged) != 2 {
		t.Errorf("files_changed = %v, want 2", item.Prompt.FilesChanged)
	}
}

func TestDrainEnrichmentQueueHappyPath(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	// Write a plain note directly, then enqueue an enrichment job for it.
	fp := surface.WriterFingerprint(vault.Root)
	if _, err := vault.WriteSession("proj", testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	in := enrichment.PromptInput{UserText: "u", AssistantText: "a"}
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 1, "", in); err != nil {
		t.Fatalf("EnqueueEnrichment: %v", err)
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("DrainEnrichmentQueue: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1", drained)
	}

	meta, body, err := vault.ReadSession("proj", "2026-06-21", fp, 1)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Summary != "LLM refined summary" {
		t.Errorf("summary = %q, want LLM refined", meta.Summary)
	}
	if len(meta.Decisions) != 1 || meta.Decisions[0] != "LLM decision one" {
		t.Errorf("decisions = %v", meta.Decisions)
	}
	if len(meta.OpenThreads) != 1 || meta.OpenThreads[0] != "LLM thread one" {
		t.Errorf("open_threads = %v", meta.OpenThreads)
	}
	if meta.Tag != "refactor" {
		t.Errorf("tag = %q, want refactor", meta.Tag)
	}
	if meta.EnrichedBy != "test-model" {
		t.Errorf("enriched_by = %q", meta.EnrichedBy)
	}
	if meta.EnrichedAt == "" {
		t.Error("enriched_at empty")
	}
	if !strings.Contains(body, "<!-- enriched -->") {
		t.Errorf("body missing fence:\n%s", body)
	}

	// Queue files gone.
	dir := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(leftovers) != 0 {
		t.Errorf("queue not empty after drain: %v", leftovers)
	}
}

// TestDrainByteIdenticalBody proves the inline-enriched body and the
// drain-enriched body converge to byte-identical markdown [M-B].
func TestDrainByteIdenticalBody(t *testing.T) {
	files := []string{"internal/foo.go"}

	// Session A: inline enrichment via a working Enricher.
	vaultA := testVault(t)
	resA, err := WriteSession(context.Background(), vaultA, nil, SessionParams{
		Project:      "proj",
		Summary:      "plain heuristic summary",
		Tag:          "implementation",
		FilesChanged: files,
		Transcript:   enrichTranscript,
		Enricher:     workingEnricher(),
	})
	if err != nil {
		t.Fatalf("inline WriteSession: %v", err)
	}
	pathA, _ := vaultA.SessionFile("proj", resA.SessionID[:10], ParseFingerprint(resA.SessionID), resA.Iteration)
	bodyA := stripFrontmatter(t, pathA)

	// Session B: failing Enricher + CWD → plain note + enqueue, then drain
	// with a working Enricher returning the same canned Result.
	vaultB := testVault(t)
	cwd := t.TempDir()
	resB, err := WriteSession(context.Background(), vaultB, nil, SessionParams{
		Project:      "proj",
		Summary:      "plain heuristic summary",
		Tag:          "implementation",
		FilesChanged: files,
		Transcript:   enrichTranscript,
		Enricher:     failingEnricher(),
		CWD:          cwd,
	})
	if err != nil {
		t.Fatalf("plain WriteSession: %v", err)
	}
	drained, err := DrainEnrichmentQueue(context.Background(), vaultB, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1", drained)
	}
	pathB, _ := vaultB.SessionFile("proj", resB.SessionID[:10], ParseFingerprint(resB.SessionID), resB.Iteration)
	bodyB := stripFrontmatter(t, pathB)

	if bodyA != bodyB {
		t.Errorf("bodies diverge:\ninline:\n%q\ndrained:\n%q", bodyA, bodyB)
	}
}

// TestDrainIdempotentFileBytes proves re-draining an already-enriched note
// produces byte-identical file bytes (EnrichedAt is preserved).
func TestDrainIdempotentFileBytes(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	fp := surface.WriterFingerprint(vault.Root)
	if _, err := vault.WriteSession("proj", testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	in := enrichment.PromptInput{UserText: "u", AssistantText: "a"}

	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 1, "", in); err != nil {
		t.Fatal(err)
	}
	if n, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0); err != nil || n != 1 {
		t.Fatalf("first drain: n=%d err=%v", n, err)
	}
	path, _ := vault.SessionFile("proj", "2026-06-21", fp, 1)
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Re-enqueue and drain the already-enriched note again.
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 1, "", in); err != nil {
		t.Fatal(err)
	}
	if n, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0); err != nil || n != 1 {
		t.Fatalf("second drain: n=%d err=%v", n, err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Errorf("re-drain not byte-identical:\nfirst:\n%q\nsecond:\n%q", first, second)
	}
}

func TestDrainTransientFailureRetries(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	fp := surface.WriterFingerprint(vault.Root)
	if _, err := vault.WriteSession("proj", testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	in := enrichment.PromptInput{UserText: "u"}
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 1, "", in); err != nil {
		t.Fatal(err)
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, failingEnricher(), 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 0 {
		t.Errorf("drained = %d, want 0 on transient failure", drained)
	}

	dir := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	jsonPath := queuePath(dir, fp, 1)
	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("job not retained for retry: %v", err)
	}
	if _, err := os.Stat(jsonPath + ".processing"); !os.IsNotExist(err) {
		t.Errorf(".processing not cleaned up after retry")
	}
}

// TestDrainClaimSkip simulates a concurrent claim: an item already renamed to
// .processing is invisible to glob("*.json"), so drain leaves it untouched.
func TestDrainClaimSkip(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	fp := surface.WriterFingerprint(vault.Root)
	if _, err := vault.WriteSession("proj", testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	in := enrichment.PromptInput{UserText: "u"}
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 1, "", in); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	jsonPath := queuePath(dir, fp, 1)
	procPath := jsonPath + ".processing"
	if err := os.Rename(jsonPath, procPath); err != nil {
		t.Fatalf("simulate claim: %v", err)
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 0 {
		t.Errorf("drained = %d, want 0 (claimed item must be skipped)", drained)
	}
	if _, err := os.Stat(procPath); err != nil {
		t.Errorf("claimed .processing file was touched: %v", err)
	}

	// The note must remain plain (not enriched).
	meta, _, err := vault.ReadSession("proj", "2026-06-21", fp, 1)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EnrichedBy != "" {
		t.Errorf("claimed item was processed: enriched_by=%q", meta.EnrichedBy)
	}
}

func TestDrainCorruptItemDiscarded(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-06-21-01.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 0 {
		t.Errorf("drained = %d, want 0 for corrupt item", drained)
	}
	// Corrupt .processing file is removed, not retried forever.
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(leftovers) != 0 {
		t.Errorf("corrupt item not cleaned up: %v", leftovers)
	}
}

func TestDrainNilResultRetains(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()
	fp := surface.WriterFingerprint(vault.Root)
	if _, err := vault.WriteSession("proj", testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 1, "", enrichment.PromptInput{}); err != nil {
		t.Fatal(err)
	}

	// An Enricher with a nil completer is non-nil but Enrich returns (nil,nil).
	nilResEnricher := enrichment.NewEnricher(nil, "test-model", time.Second, "")
	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, nilResEnricher, 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 0 {
		t.Errorf("drained = %d, want 0 for nil result", drained)
	}
	// Job retained for retry (renamed back).
	if _, err := os.Stat(queuePath(filepath.Join(cwd, ".vibe-palace", "enrichment-queue"), fp, 1)); err != nil {
		t.Errorf("job not retained on nil result: %v", err)
	}
}

func TestDrainMissingNoteRetains(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()
	// Enqueue a job whose target note does not exist.
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", "", 7, "", enrichment.PromptInput{}); err != nil {
		t.Fatal(err)
	}
	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 0 {
		t.Errorf("drained = %d, want 0 when note missing", drained)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".vibe-palace", "enrichment-queue", "2026-06-21-07.json")); err != nil {
		t.Errorf("job not retained when note missing: %v", err)
	}
}

func TestDrainMaxBound(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()
	// Two plain notes + two queued jobs.
	fp := surface.WriterFingerprint(vault.Root)
	for i := 1; i <= 2; i++ {
		if _, err := vault.WriteSession("proj", testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
			t.Fatal(err)
		}
		if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, i, "", enrichment.PromptInput{}); err != nil {
			t.Fatal(err)
		}
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 1)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 1 {
		t.Errorf("drained = %d, want 1 (max bound)", drained)
	}
	// One job remains.
	remaining, _ := filepath.Glob(filepath.Join(cwd, ".vibe-palace", "enrichment-queue", "*.json"))
	if len(remaining) != 1 {
		t.Errorf("remaining jobs = %d, want 1", len(remaining))
	}
}

func TestDrainNilEnricherAndMissingDir(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	// nil enricher → no-op.
	if n, err := DrainEnrichmentQueue(context.Background(), vault, cwd, nil, 0); err != nil || n != 0 {
		t.Errorf("nil enricher: n=%d err=%v, want 0/nil", n, err)
	}
	// Missing queue dir → no-op.
	if n, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0); err != nil || n != 0 {
		t.Errorf("missing dir: n=%d err=%v, want 0/nil", n, err)
	}
}

func TestWriteSessionEnqueueOnMiss(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	res, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "proj",
		Summary:    "plain heuristic summary",
		Transcript: enrichTranscript,
		Enricher:   failingEnricher(),
		CWD:        cwd,
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	// Note written plain (no fence).
	_, body, err := vault.ReadSession("proj", res.SessionID[:10], ParseFingerprint(res.SessionID), res.Iteration)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "<!-- enriched -->") {
		t.Errorf("note should be plain on enrichment miss:\n%s", body)
	}

	// A queue file exists for the captured iteration.
	dir := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	matches, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 queued item, found %d: %v", len(matches), matches)
	}
}

func TestWriteSessionNoEnqueueWithoutCWD(t *testing.T) {
	vault := testVault(t)

	// Failing enricher but no CWD: no queue, no panic.
	res, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "proj",
		Summary:    "plain heuristic summary",
		Transcript: enrichTranscript,
		Enricher:   failingEnricher(),
		// CWD intentionally empty.
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if res.Status != "ok" {
		t.Errorf("status = %q, want ok", res.Status)
	}
}

// TestDrainDeadLettersAfterMaxAttempts verifies that a deterministically
// failing job is dead-lettered once Attempts reaches maxEnrichAttempts: the
// <id>.json is renamed to <id>.json.failed instead of being retried forever,
// and drained stays 0 throughout.
func TestDrainDeadLettersAfterMaxAttempts(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	fp := surface.WriterFingerprint(vault.Root)
	if _, err := vault.WriteSession("proj", testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 1, "", enrichment.PromptInput{UserText: "u"}); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	jsonPath := queuePath(dir, fp, 1)
	failedPath := jsonPath + ".failed"

	// Drain repeatedly with a failing enricher. Each transient failure bumps
	// Attempts; the maxEnrichAttempts'th drain dead-letters the job.
	for i := range maxEnrichAttempts + 2 {
		drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, failingEnricher(), 0)
		if err != nil {
			t.Fatalf("drain %d: %v", i, err)
		}
		if drained != 0 {
			t.Fatalf("drain %d: drained = %d, want 0 (always failing)", i, drained)
		}
	}

	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Errorf("active job still present after dead-letter: stat err = %v", err)
	}
	if _, err := os.Stat(failedPath); err != nil {
		t.Errorf("dead-letter file missing: %v", err)
	}
	// No stray .processing left behind.
	if _, err := os.Stat(jsonPath + ".processing"); !os.IsNotExist(err) {
		t.Errorf(".processing not cleaned up after dead-letter")
	}
}

// TestDrainReclaimsStaleProcessing verifies that an orphaned <id>.json.processing
// file older than staleProcessingAge is reclaimed (renamed back to <id>.json)
// at drain start and then processed normally.
func TestDrainReclaimsStaleProcessing(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	fp := surface.WriterFingerprint(vault.Root)
	if _, err := vault.WriteSession("proj", testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	// Build a valid queue item, then orphan it as a stale .processing claim.
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 1, "", enrichment.PromptInput{UserText: "u"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	jsonPath := queuePath(dir, fp, 1)
	procPath := jsonPath + ".processing"
	if err := os.Rename(jsonPath, procPath); err != nil {
		t.Fatalf("orphan claim: %v", err)
	}
	old := time.Now().Add(-20 * time.Minute)
	if err := os.Chtimes(procPath, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1 (stale claim reclaimed and processed)", drained)
	}

	// Note rewritten enriched.
	meta, body, err := vault.ReadSession("proj", "2026-06-21", fp, 1)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EnrichedBy != "test-model" {
		t.Errorf("enriched_by = %q, want test-model", meta.EnrichedBy)
	}
	if !strings.Contains(body, "<!-- enriched -->") {
		t.Errorf("body missing fence after reclaim+drain:\n%s", body)
	}
	// No queue residue (neither .json nor .processing).
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(leftovers) != 0 {
		t.Errorf("queue not empty after reclaim+drain: %v", leftovers)
	}
}

// TestDrainDoesNotReclaimFreshProcessing verifies a recent .processing claim
// (presumed live) is NOT reclaimed: a concurrent drainer still owns it, so the
// sweep leaves it untouched.
func TestDrainDoesNotReclaimFreshProcessing(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	fp := surface.WriterFingerprint(vault.Root)
	if _, err := vault.WriteSession("proj", testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
		t.Fatalf("WriteSession: %v", err)
	}
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", fp, 1, "", enrichment.PromptInput{UserText: "u"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cwd, ".vibe-palace", "enrichment-queue")
	jsonPath := queuePath(dir, fp, 1)
	procPath := jsonPath + ".processing"
	if err := os.Rename(jsonPath, procPath); err != nil {
		t.Fatalf("simulate live claim: %v", err)
	}
	// Fresh mtime (default from rename is now) — leave as-is.

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 0 {
		t.Errorf("drained = %d, want 0 (fresh claim must not be reclaimed)", drained)
	}
	if _, err := os.Stat(procPath); err != nil {
		t.Errorf("fresh .processing claim was disturbed: %v", err)
	}
	// Note must remain plain.
	meta, _, err := vault.ReadSession("proj", "2026-06-21", fp, 1)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EnrichedBy != "" {
		t.Errorf("fresh claim was processed: enriched_by=%q", meta.EnrichedBy)
	}
}

// testPlainMeta is a minimal plain session meta dated 2026-06-21 (iteration 1
// once written into a fresh vault).
func testPlainMeta() storage.SessionMeta {
	return storage.SessionMeta{
		Date:    "2026-06-21",
		Title:   "Plain",
		Summary: "plain",
	}
}

// TestDrainLegacyQueueItem proves back-compat: a legacy queue item with an
// empty Fingerprint, targeting a pre-fingerprint <date>-NN.md note, still
// drains because fp="" resolves the legacy filename end-to-end.
func TestDrainLegacyQueueItem(t *testing.T) {
	vault := testVault(t)
	cwd := t.TempDir()

	// Seed a legacy host-agnostic note (fp="") and a legacy queue item.
	if err := vault.RewriteSession("proj", "2026-06-21", "", 1, testPlainMeta(), "## Summary\n\nplain\n"); err != nil {
		t.Fatalf("RewriteSession(legacy): %v", err)
	}
	if err := EnqueueEnrichment(cwd, "proj", "2026-06-21", "", 1, "", enrichment.PromptInput{UserText: "u", AssistantText: "a"}); err != nil {
		t.Fatal(err)
	}

	drained, err := DrainEnrichmentQueue(context.Background(), vault, cwd, workingEnricher(), 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained = %d, want 1 (legacy item must still drain)", drained)
	}
	meta, _, err := vault.ReadSession("proj", "2026-06-21", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EnrichedBy != "test-model" {
		t.Errorf("legacy note not enriched: enriched_by=%q", meta.EnrichedBy)
	}
}
