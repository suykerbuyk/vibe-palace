// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"os"
	"os/exec"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// TestIntegration_ONNXCacheCrossProcess is SUPPLEMENTARY evidence only. The
// PRIMARY, deterministic evidence for the shared ONNX model cache race fix
// lives in onnx_slowwriter_harness_test.go: this test additionally spawns N
// real subprocesses, each calling the real embedder.NewONNX against a SHARED
// cold model cache dir, requiring network access to download the real ONNX
// model. It is good defense-in-depth and keeps this package consistent with
// internal/integration/vaultlock_crossprocess_test.go's house style, but per
// Review (2026-09-11) it must not be relied on as sole proof the fix works:
// it inherits the original bug's low, network-timing-dependent hit rate
// (1-in-4 on the reporting machine that first found the bug; 0-in-5 in the
// planning session's sandbox).
//
// Mechanism: re-exec this test binary N times with -test.run pinned to
// TestHelperProcess_NewONNXCrossProcess and an env var telling that helper
// which cache dir to share and which model to construct; assert every child
// exits 0 (no panic, no error) after constructing a real embedder against the
// shared cache concurrently with its siblings.
func TestIntegration_ONNXCacheCrossProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-process test spawns subprocesses and needs the real model/network; skipped under -short")
	}

	cacheDir := testutil.ProjectCacheDir(t)

	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)
	outs := make([]string, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0],
				"-test.run=^TestHelperProcess_NewONNXCrossProcess$",
				"-test.v",
			)
			cmd.Env = append(os.Environ(),
				"VP_ONNX_CROSSPROCESS_HELPER=1",
				"VP_ONNX_CROSSPROCESS_CACHEDIR="+cacheDir,
			)
			out, err := cmd.CombinedOutput()
			outs[i] = string(out)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i := range n {
		if errs[i] != nil {
			t.Errorf("subprocess %d failed: %v\noutput:\n%s", i, errs[i], outs[i])
		}
	}
}

// TestHelperProcess_NewONNXCrossProcess is not a real test: it is a
// subprocess entry point, inert unless VP_ONNX_CROSSPROCESS_HELPER=1, that
// constructs a real ONNXEmbedder against the shared cache dir named by
// VP_ONNX_CROSSPROCESS_CACHEDIR. TestIntegration_ONNXCacheCrossProcess is its
// only caller; running the package's tests normally (that env var unset)
// just skips it.
func TestHelperProcess_NewONNXCrossProcess(t *testing.T) {
	if os.Getenv("VP_ONNX_CROSSPROCESS_HELPER") != "1" {
		t.Skip("not running as the ONNX cross-process helper")
	}

	cacheDir := os.Getenv("VP_ONNX_CROSSPROCESS_CACHEDIR")
	if cacheDir == "" {
		t.Fatal("VP_ONNX_CROSSPROCESS_CACHEDIR not set")
	}

	emb, err := NewONNX("sentence-transformers/all-MiniLM-L6-v2", cacheDir, 256, 32)
	if err != nil {
		t.Fatalf("NewONNX: %v", err)
	}
	defer emb.Close()
}
