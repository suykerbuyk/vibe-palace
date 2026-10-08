// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"io/fs"
	"strings"
	"testing"
)

// TestRestartTemplatePointsAtIndexCoverage pins the restart command's embedder
// bullet after index-coverage-instrument rewrote it: the old "search rebuilds
// lazily on the first vp_search call" prose is gone, and the agent is pointed at
// index_coverage and the explicit `vp index rebuild` path instead (Scope 9).
func TestRestartTemplatePointsAtIndexCoverage(t *testing.T) {
	b, err := fs.ReadFile(FS(), "templates/commands/restart.md")
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if strings.Contains(body, "rebuilds lazily") {
		t.Error("restart.md still contains the old \"rebuilds lazily\" prose")
	}
	if !strings.Contains(body, "index_coverage") {
		t.Error("restart.md does not mention index_coverage")
	}
	if !strings.Contains(body, "vp index rebuild") {
		t.Error("restart.md does not name `vp index rebuild`")
	}
	// It still forbids running a rebuild during restart.
	if !strings.Contains(body, "Restart never waits on the embedder") {
		t.Error("restart.md no longer carries the no-rebuild-during-restart posture")
	}
}
