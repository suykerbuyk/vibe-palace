// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
)

// TestCaptureSessionRefusesUninitialisedProject: vp_capture_session into a
// project the vault has not initialised is refused BEFORE the inline archive
// is minted (task untracked-project-stamps-from-writers-that-never-commit).
// Without the up-front refusal the archive would be attempted first and a
// later writer's refusal would strand an archive with no note. The request
// carries a transcript and archive_transcript:true on a hook-less host — the
// shape TestCaptureSessionInlineArchiveHookless proves archives inline for an
// initialised project.
func TestCaptureSessionRefusesUninitialisedProject(t *testing.T) {
	vault := testSessionVault(t)
	stubHostSessionID(t, "") // hook-less: the inline archive would fire

	// The archive writer refuses an uninitialised project too, so the error
	// and the empty vault alone cannot tell the up-front refusal from a
	// refused archive attempt; the archive-failure Warn can.
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	tool := CaptureSessionTool(vault, nil)
	params, _ := json.Marshal(map[string]any{
		"project":            "fresh",
		"summary":            "Capture into a project the vault never initialised.",
		"transcript":         sampleClaudeJSONL,
		"archive_transcript": true,
		"cwd":                t.TempDir(),
	})

	_, err := tool.Handler(context.Background(), json.RawMessage(params))
	if !errors.Is(err, projectdir.ErrUninitialisedProject) {
		t.Fatalf("handler err = %v, want ErrUninitialisedProject", err)
	}
	if strings.Contains(logBuf.String(), "inline transcript archive failed") {
		t.Errorf("the inline archive was attempted before the refusal: %q", logBuf.String())
	}
	// No Projects/fresh means no transcripts/ archive pair and no session note.
	for _, dir := range []string{"Projects/fresh", "palace/fresh"} {
		if _, err := os.Stat(filepath.Join(vault.Root, filepath.FromSlash(dir))); !os.IsNotExist(err) {
			t.Errorf("%s must not exist after the refusal (stat err %v)", dir, err)
		}
	}
}
