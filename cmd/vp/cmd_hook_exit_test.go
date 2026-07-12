// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// TestRunHookNeverExitsBlockingOnCaptureFailure is the exit-code half of the
// hook contract (DoD 2b), and it asserts the thing that sibling test
// TestHookLogsWarnToPayloadVault explicitly declines to.
//
// cli.ExitSystem is 2, and 2 is Claude Code's RESERVED BLOCKING-error code. The
// hook is installed on SessionEnd, Stop and PreCompact. On Stop — which fires
// once per assistant turn — exit 2 blocks the turn and feeds the hook's stderr
// back into the model, so a deterministically-failing capture (a bad config, an
// unwritable vault) would blocking-error on the first turn of every session and
// feed its own error message back to the model. A loop. At SessionEnd the very
// same exit code blocks nothing and is simply invisible. Obnoxious at one end,
// useless at the other: the exit code is not the alarm, and this test nails it
// down so nobody restores it.
//
// The alarm is the durable log, which is asserted here too — the capture failure
// that kills the note used to be printed with a bare fmt.Fprintf to stderr,
// never touching slog, so it reached no vp.log even after the logger was fixed.
func TestRunHookNeverExitsBlockingOnCaptureFailure(t *testing.T) {
	tmp := t.TempDir()

	projectVault := filepath.Join(tmp, "vault")
	projectDir := filepath.Join(tmp, "proj")
	for _, d := range []string{projectVault, projectDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// The marker both satisfies the hook's opt-in gate and pins the slug, so the
	// booby-trap below lands on the exact path capture will try to write.
	if err := os.WriteFile(
		filepath.Join(projectDir, ".vibe-palace.toml"),
		[]byte("vault_path = \""+projectVault+"\"\n\n[project]\nname = \"doomed-proj\"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	// Doom the capture: the sessions directory is a FILE, so the note cannot be
	// written. This is capture's one genuinely fatal error.
	projPath := filepath.Join(projectVault, "Projects", "doomed-proj")
	if err := os.MkdirAll(projPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projPath, "sessions"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stop, not SessionEnd: Stop is the event where exit 2 actually blocks, and
	// it skips archive/harvest/drain so the capture failure is the only fault.
	body, err := json.Marshal(map[string]string{
		"hook_event_name": "Stop",
		"session_id":      "doomed-session",
		"cwd":             projectDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	stdinFile := filepath.Join(tmp, "payload.json")
	if err := os.WriteFile(stdinFile, body, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	origStdin := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = origStdin })

	code := runHook(cli.BuildInfo{Version: "test"})
	vplog.Close()

	// THE ASSERTION.
	if code == cli.ExitSystem {
		t.Fatalf("vp hook exited %d (ExitSystem) on a capture failure — Claude Code reads 2 as a "+
			"BLOCKING error, and this hook fires on every Stop, so this loops the session", code)
	}
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d (ExitOK): a hook failure is reported through the log, not the exit", code, cli.ExitOK)
	}

	// The loudness that replaces the exit code: the failure must be durable.
	lines := logLines(t, projectVault)
	if len(lines) == 0 {
		t.Fatal("the note was lost entirely and NOTHING was logged — the failure is invisible")
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "session capture failed") {
		t.Errorf("expected the capture failure in vp.log; got:\n%s", joined)
	}
}
