// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/knights-analytics/hugot"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// writeGarbageSnapshot seeds dir (a lockTarget-shaped model directory) with a
// tokenizer.json (content is irrelevant here -- hugot's model load never
// reaches tokenizer parsing when the .onnx parse fails first; see
// backends/model.go's LoadModel, which runs CreateModelBackend strictly
// before LoadTokenizer) and a nonzero-size garbage model.onnx. That passes
// both the old localSnapshotComplete check and the new size>0 floor, failing
// only at actual ONNX parse time -- exactly the class of corruption a
// truncated hugot download leaves behind (see
// truncated-model-onnx-panics-and-is-never-re-downloaded).
func writeGarbageSnapshot(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write tokenizer.json: %v", err)
	}
	garbage := []byte("this is not a real onnx model -- simulates a truncated/corrupt copy")
	if err := os.WriteFile(filepath.Join(dir, "model.onnx"), garbage, 0o644); err != nil {
		t.Fatalf("write model.onnx: %v", err)
	}
}

// TestNewONNXGarbageModelReturnsErrorNotPanic is the hermetic regression test
// for truncated-model-onnx-panics-and-is-never-re-downloaded's core bug:
// hugot@v0.7.0's backends/model_gomlx.go calls onnx.Model.WithBaseDir on a
// nil interface before its own error check for a failed parser.ParseFile
// ever runs, turning a corrupt model.onnx into a nil-pointer panic instead
// of an error. It also proves the self-heal is bounded to exactly one
// attempt: the downloadModel seam is swapped for a call-counting fake that
// itself fails, so the only way this test could "download" anything is
// through that fake -- it never touches a real socket.
//
// Runs unconditionally, including under -short: no real model file or
// network access is involved anywhere in this test.
func TestNewONNXGarbageModelReturnsErrorNotPanic(t *testing.T) {
	dir := t.TempDir()
	modelName := "sentence-transformers/all-MiniLM-L6-v2"
	lockTarget := modelCacheLockPath(dir, modelName)
	writeGarbageSnapshot(t, lockTarget)

	calls := 0
	old := downloadModel
	downloadModel = func(name, destination string, options hugot.DownloadOptions) (string, error) {
		calls++
		return "", errors.New("simulated download failure -- must never be a real network call in this test")
	}
	t.Cleanup(func() { downloadModel = old })

	_, err := NewONNX(modelName, dir, 0, 1)
	if err == nil {
		t.Fatal("expected an error for a garbage model.onnx, got nil (a panic would have crashed this test process instead)")
	}
	if !strings.Contains(err.Error(), lockTarget) {
		t.Errorf("error %q does not name the model directory %q", err.Error(), lockTarget)
	}
	if calls != 1 {
		t.Errorf("downloadModel called %d times, want exactly 1 (bounded self-heal, not a loop)", calls)
	}
}

// TestNewONNXTruncatedModelSelfHeals is the hermetic self-heal regression
// test: a "complete"-looking but corrupt local snapshot (standing in for a
// truncated hugot download, e.g. the task's reproduced 45MB-of-90MB
// model.onnx) must be discarded and re-downloaded exactly once, succeeding
// on the retry, instead of failing forever.
//
// It follows the same testing.Short() skip convention as this file's other
// real-model tests (see onnx_test.go's newTestONNX) rather than requiring
// network: the downloadModel seam is swapped for a fake that copies this
// machine's already-warm real model files from testutil.ProjectCacheDir(t)
// -- populated by a prior non-short test run or `make model-test`, never by
// this test -- so the retried hugot.NewPipeline call loads genuine bytes,
// not a mocked success. This keeps it out of `go test -short ./...` and out
// of narrow `-run` invocations against unrelated packages, matching CI's
// separate model job.
func TestNewONNXTruncatedModelSelfHeals(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping self-heal test in short mode (requires the project's warm real-model cache)")
	}

	realDir := modelCacheLockPath(testutil.ProjectCacheDir(t), "sentence-transformers/all-MiniLM-L6-v2")
	if _, err := os.Stat(filepath.Join(realDir, "model.onnx")); err != nil {
		t.Skipf("real model cache not warm at %s (%v); run `make model-test` first to populate it", realDir, err)
	}

	dir := t.TempDir()
	modelName := "sentence-transformers/all-MiniLM-L6-v2"
	lockTarget := modelCacheLockPath(dir, modelName)
	writeGarbageSnapshot(t, lockTarget)

	old := downloadModel
	downloadModel = func(name, destination string, options hugot.DownloadOptions) (string, error) {
		dest := modelCacheLockPath(destination, name)
		if err := copyDirFiles(realDir, dest); err != nil {
			return "", err
		}
		return dest, nil
	}
	t.Cleanup(func() { downloadModel = old })

	emb, err := NewONNX(modelName, dir, 0, 1)
	if err != nil {
		t.Fatalf("NewONNX did not self-heal from a truncated model.onnx: %v", err)
	}
	defer emb.Close()

	if _, err := emb.Embed(context.Background(), "hello"); err != nil {
		t.Fatalf("Embed after self-heal: %v", err)
	}
}

// copyDirFiles copies the flat file contents of src into dst (creating dst),
// standing in for hugot.DownloadModel's real copy step in the hermetic
// self-heal test above.
func copyDirFiles(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
