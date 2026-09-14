// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"errors"
	"testing"

	"github.com/knights-analytics/hugot"
)

// TestNewONNXSetsConcurrentConnectionsAboveDefault pins the
// upstream-model-download-bugs-and-the-hugot-upgrade mitigation: NewONNX
// must pass a DownloadOptions.ConcurrentConnections above hugot's
// NewDownloadOptions default of 5 (this model needs 6 files), so a cold
// download never contends go-huggingface's lost-wakeup semaphore bug. The
// downloadModel seam is swapped for a fake that only records the options it
// received and fails immediately -- it never touches a real socket or model
// file, so this runs unconditionally, including under -short.
func TestNewONNXSetsConcurrentConnectionsAboveDefault(t *testing.T) {
	dir := t.TempDir()
	modelName := "sentence-transformers/all-MiniLM-L6-v2"

	var got int
	old := downloadModel
	downloadModel = func(name, destination string, options hugot.DownloadOptions) (string, error) {
		got = options.ConcurrentConnections
		return "", errors.New("simulated download failure -- must never be a real network call in this test")
	}
	t.Cleanup(func() { downloadModel = old })

	if _, err := NewONNX(modelName, dir, 0, 1); err == nil {
		t.Fatal("expected an error from the simulated download failure, got nil")
	}

	const hugotDefault = 5 // hugot.NewDownloadOptions()'s own default, duplicated here (not imported) so a future upstream default change can't silently make this assertion vacuous
	if got <= hugotDefault {
		t.Errorf("ConcurrentConnections = %d, want > %d (hugot's default, which is already below this model's 6-file count and triggers the upstream semaphore bug)", got, hugotDefault)
	}
	if got != modelDownloadConcurrentConnections {
		t.Errorf("ConcurrentConnections = %d, want the named constant modelDownloadConcurrentConnections (%d)", got, modelDownloadConcurrentConnections)
	}
}
