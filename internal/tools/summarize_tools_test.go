// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---------------------------------------------------------------------------
// vp_enqueue_iteration_summary
// ---------------------------------------------------------------------------

func TestEnqueueIterationSummaryTool_MissingProjectPath(t *testing.T) {
	tool := EnqueueIterationSummaryTool()
	params, _ := json.Marshal(map[string]any{"project": "demo", "iter": 1})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing project_path")
	}
}

func TestEnqueueIterationSummaryTool_InvalidIter(t *testing.T) {
	tool := EnqueueIterationSummaryTool()
	projDir := t.TempDir()

	for _, iter := range []int{0, -1} {
		params, _ := json.Marshal(map[string]any{
			"project": "demo", "project_path": projDir, "iter": iter,
		})
		if _, err := tool.Handler(context.Background(), params); err == nil {
			t.Errorf("iter=%d: expected error", iter)
		}
	}
}

func TestEnqueueIterationSummaryTool_Success(t *testing.T) {
	tool := EnqueueIterationSummaryTool()
	projDir := t.TempDir()

	params, _ := json.Marshal(map[string]any{
		"project": "demo", "project_path": projDir, "iter": 7,
	})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	out, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", res)
	}
	if out["status"] != "enqueued" {
		t.Errorf("status = %v, want enqueued", out["status"])
	}
	queuePath, _ := out["queue_path"].(string)
	if queuePath == "" {
		t.Fatal("queue_path is empty")
	}

	// A file must have actually landed in the summarization queue directory.
	wantDir := filepath.Join(projDir, ".vibe-palace", "summarization-queue")
	if queuePath != wantDir {
		t.Errorf("queue_path = %q, want %q", queuePath, wantDir)
	}
	matches, err := filepath.Glob(filepath.Join(wantDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d queued files, want 1: %v", len(matches), matches)
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var item struct {
		Kind      string `json:"kind"`
		Project   string `json:"project"`
		Iteration int    `json:"iteration"`
	}
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	if item.Kind != "iteration" || item.Project != "demo" || item.Iteration != 7 {
		t.Errorf("queued item = %+v, want kind=iteration project=demo iteration=7", item)
	}
}

func TestEnqueueIterationSummaryTool_DetectsProjectFromPath(t *testing.T) {
	tool := EnqueueIterationSummaryTool()
	projDir := newGitProjectDir(t, "detected-project")

	// No "project" field: must be detected from project_path.
	params, _ := json.Marshal(map[string]any{"project_path": projDir, "iter": 1})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	wantDir := filepath.Join(projDir, ".vibe-palace", "summarization-queue")
	matches, err := filepath.Glob(filepath.Join(wantDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d queued files, want 1", len(matches))
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var item struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	if item.Project != "detected-project" {
		t.Errorf("detected project = %q, want detected-project", item.Project)
	}
	_ = res
}

// ---------------------------------------------------------------------------
// vp_trigger_summarization_drain
// ---------------------------------------------------------------------------

// fakeLaunch is a plain recording detachlaunch.LaunchFunc for a focused unit
// test — NOT testinfra's harness-level fake, which is exercised separately by
// the tool-coverage fixture in internal/integration.
type fakeLaunch struct {
	called  bool
	binary  string
	args    []string
	logPath string
	pid     int
	err     error
}

func (f *fakeLaunch) launch(binary string, args []string, logPath string) (int, error) {
	f.called = true
	f.binary = binary
	f.args = args
	f.logPath = logPath
	if f.err != nil {
		return 0, f.err
	}
	return f.pid, nil
}

func TestTriggerSummarizationDrainTool_MissingProjectPath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	fl := &fakeLaunch{}
	tool := TriggerSummarizationDrainTool(vault, fl.launch)

	params, _ := json.Marshal(map[string]any{})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for missing project_path")
	}
	if fl.called {
		t.Error("launch must not be called on a validation error")
	}
}

// TestTriggerSummarizationDrainTool_RelativeProjectPath pins a real fix: a
// relative project_path must be rejected here, at the tool's own boundary —
// not allowed through to glob/launch, where it would report
// {"status":"launched","pid":N} as if it succeeded while the launched
// child's own --project-path validation (cmd/vp/cmd_drain.go) rejects the
// same relative path and exits immediately, leaving the caller with a false
// success and the queued jobs stuck.
func TestTriggerSummarizationDrainTool_RelativeProjectPath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	fl := &fakeLaunch{}
	tool := TriggerSummarizationDrainTool(vault, fl.launch)

	params, _ := json.Marshal(map[string]any{"project_path": "relative/path"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for a relative project_path")
	}
	if fl.called {
		t.Error("launch must not be called when project_path validation fails")
	}
}

func TestTriggerSummarizationDrainTool_EmptyQueue(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	fl := &fakeLaunch{}
	tool := TriggerSummarizationDrainTool(vault, fl.launch)

	projDir := t.TempDir() // no summarization-queue dir at all

	params, _ := json.Marshal(map[string]any{"project_path": projDir})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := res.(map[string]any)
	if out["status"] != "empty" {
		t.Errorf("status = %v, want empty", out["status"])
	}
	if fl.called {
		t.Error("launch must not be called when the queue is empty")
	}
}

func TestTriggerSummarizationDrainTool_NonEmptyQueueLaunches(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	fl := &fakeLaunch{pid: 4242}
	tool := TriggerSummarizationDrainTool(vault, fl.launch)

	projDir := t.TempDir()
	queueDir := filepath.Join(projDir, ".vibe-palace", "summarization-queue")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "00000000000000000001-aaaaaaaa.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]any{"project_path": projDir})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := res.(map[string]any)
	if out["status"] != "launched" {
		t.Errorf("status = %v, want launched", out["status"])
	}
	if out["pid"] != 4242 {
		t.Errorf("pid = %v, want 4242", out["pid"])
	}

	if !fl.called {
		t.Fatal("launch was not called for a non-empty queue")
	}
	if fl.binary != "" {
		t.Errorf("binary = %q, want empty (self-relaunch)", fl.binary)
	}
	wantArgs := []string{"drain", "summaries", "--project-path", projDir}
	if !reflect.DeepEqual(fl.args, wantArgs) {
		t.Errorf("args = %v, want %v", fl.args, wantArgs)
	}
	wantLog := filepath.Join(projDir, ".vibe-palace", "summarization-drain.log")
	if fl.logPath != wantLog {
		t.Errorf("logPath = %q, want %q", fl.logPath, wantLog)
	}
}

// TestTriggerSummarizationDrainTool_OrphanedClaimOnlyStillLaunches pins the
// handler's own documented behavior: a queue whose ONLY entry is an orphaned
// "*.json.processing" claim (left by a drain that crashed mid-job, with no
// plain "*.json" file at all) must still be treated as non-empty and launch
// a drain — nothing else reclaims that orphan directly (only
// jobqueue.Claim's own reclaimStale sweep does, and only once a drain
// actually runs), so reporting "empty" here would leave it stuck forever
// unless some unrelated new job happened to be enqueued in the same project.
func TestTriggerSummarizationDrainTool_OrphanedClaimOnlyStillLaunches(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	fl := &fakeLaunch{pid: 777}
	tool := TriggerSummarizationDrainTool(vault, fl.launch)

	projDir := t.TempDir()
	queueDir := filepath.Join(projDir, ".vibe-palace", "summarization-queue")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only an orphaned claim — deliberately no plain *.json file at all.
	if err := os.WriteFile(filepath.Join(queueDir, "iteration-1.json.processing"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]any{"project_path": projDir})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := res.(map[string]any)
	if out["status"] != "launched" {
		t.Errorf("status = %v, want launched (an orphaned claim alone must still count as non-empty)", out["status"])
	}
	if !fl.called {
		t.Fatal("launch was not called for a queue holding only an orphaned claim")
	}
}

func TestTriggerSummarizationDrainTool_LaunchError(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	fl := &fakeLaunch{err: os.ErrPermission}
	tool := TriggerSummarizationDrainTool(vault, fl.launch)

	projDir := t.TempDir()
	queueDir := filepath.Join(projDir, ".vibe-palace", "summarization-queue")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "job.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	params, _ := json.Marshal(map[string]any{"project_path": projDir})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error when launch fails")
	}
}
