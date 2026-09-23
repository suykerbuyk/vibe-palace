// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// T11. `vp archive create` writes Projects/<slug>/transcripts/, so for a
// project that departed the vault it must refuse with the redirect rather than
// re-create the old tree. A real transcript is supplied, so without the refusal
// the archive WOULD be written. The read subcommands stay admitted.
func TestArchiveCreateRefusesADepartedProject(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	if _, err := storage.NewVault(vaultDir).RecordDeparture("old", departure.Renamed, "new"); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "session.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"hi"},"sessionId":"s1"}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":"hello"},"sessionId":"s1"}` + "\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var code int
	stderr := captureStderr(t, func() {
		code = cmdArchiveCreate(cli.BuildInfo{}).Run([]string{"--session-id", "s1", "--project", "old", "--source", src})
	})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d (refusal); stderr: %s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, `set [project].name = "new"`) {
		t.Errorf("stderr must carry the redirect, got %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "old")); err == nil {
		t.Error("vp archive create re-created Projects/old/")
	}

	// A read of the departed slug is admitted, as the MCP seam admits reads.
	stderr = captureStderr(t, func() { code = cmdArchiveList().Run([]string{"--project", "old"}) })
	if strings.Contains(stderr, "renamed to") {
		t.Errorf("vp archive list is a read and must not be refused as departed: %q", stderr)
	}
}
