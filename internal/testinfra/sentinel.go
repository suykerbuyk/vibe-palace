// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcphost"
)

// fixedAgentCLIs lists agent CLI binary names that no mcphost.Host reports via
// Executables() (mcphost.Registry()'s Host implementations only cover Claude,
// Grok, and Zed — Neovim, Cursor, and OpenCode remain documented-manual, per
// internal/mcphost's own package doc comment) but that a test could still
// accidentally exec or LookPath if one happens to be installed on the machine
// running the suite. Includes both "cursor" and "cursor-agent": the Cursor CLI
// has shipped under both names historically, and both appear in the parent
// epic's own evidence for this requirement.
var fixedAgentCLIs = []string{"codex", "cursor", "cursor-agent", "opencode", "nvim", "gemini"}

// SentinelPATH prepends a directory of recording sentinel scripts — one per
// binary name derived from every mcphost.Registry() host's Executables()
// (never Name()), plus fixedAgentCLIs above — onto PATH for the duration of
// the test. Any test that execs or LookPaths one of these binaries hits the
// sentinel instead of whatever happens to be installed on the machine running
// the suite, and the sentinel records the call to the returned log path
// instead of running anything real.
//
// Windows is unsupported (sentinels are #!/bin/sh scripts) — callers on
// runtime.GOOS == "windows" should skip, matching cmd/vp's own precedent
// (TestFullCheckExecsNoAgentCLI).
//
// Modeled on cmd/vp/cmd_check_test.go's TestFullCheckExecsNoAgentCLI, made a
// reusable, package-wide helper whose name set is derived from
// mcphost.Registry() rather than hand-maintained per test.
func SentinelPATH(t *testing.T) (logPath string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Fatal("SentinelPATH: sentinels are sh scripts; callers must skip on windows before calling this")
	}

	names := map[string]bool{}
	for _, h := range mcphost.Registry() {
		for _, x := range h.Executables() {
			names[x] = true
		}
	}
	for _, x := range fixedAgentCLIs {
		names[x] = true
	}
	if len(names) == 0 {
		t.Fatal("SentinelPATH: derived name set is empty — the sentinel would be vacuous")
	}

	dir := t.TempDir()
	logPath = filepath.Join(t.TempDir(), "sentinel.log")
	for name := range names {
		body := "#!/bin/sh\necho \"${0##*/}|$*\" >> \"$VP_SENTINEL_LOG\"\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatalf("SentinelPATH: write sentinel %q: %v", name, err)
		}
	}
	t.Setenv("VP_SENTINEL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for name := range names {
		if got, err := exec.LookPath(name); err != nil || got != filepath.Join(dir, name) {
			t.Fatalf("SentinelPATH: LookPath(%q) = %q, %v — the sentinel is not first on PATH", name, got, err)
		}
	}

	return logPath
}
