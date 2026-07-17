// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hostsession

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// writeSessionFile stages a sessions/<pid>.json under a temp claudeHome and
// returns that home.
func writeSessionFile(t *testing.T, pid int, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// fixedStart returns a ProcStartFunc that always reports start.
func fixedStart(start string) ProcStartFunc {
	return func(int) (string, error) { return start, nil }
}

func TestClaudeSessionID_ValidAndMatching(t *testing.T) {
	home := writeSessionFile(t, 4242,
		`{"pid":4242,"sessionId":"abc-123","cwd":"/x","procStart":"9912345","status":"idle"}`)
	got := ClaudeSessionID(4242, home, fixedStart("9912345"))
	if got != "abc-123" {
		t.Errorf("ClaudeSessionID = %q, want %q", got, "abc-123")
	}
}

func TestClaudeSessionID_MissingFile(t *testing.T) {
	home := t.TempDir() // no sessions/ dir at all
	if got := ClaudeSessionID(4242, home, fixedStart("1")); got != "" {
		t.Errorf("ClaudeSessionID = %q, want empty for missing file", got)
	}
}

func TestClaudeSessionID_MalformedJSON(t *testing.T) {
	home := writeSessionFile(t, 4242, `{"pid":4242,"sessionId":`)
	if got := ClaudeSessionID(4242, home, fixedStart("1")); got != "" {
		t.Errorf("ClaudeSessionID = %q, want empty for malformed JSON", got)
	}
}

// The live format carries procStart as a JSON string; a file that carries it
// as a NUMBER is not the format we validated against, and Unmarshal into the
// string field refuses it rather than coercing.
func TestClaudeSessionID_NumericProcStartRefused(t *testing.T) {
	home := writeSessionFile(t, 4242,
		`{"pid":4242,"sessionId":"abc-123","procStart":9912345}`)
	if got := ClaudeSessionID(4242, home, fixedStart("9912345")); got != "" {
		t.Errorf("ClaudeSessionID = %q, want empty for numeric procStart", got)
	}
}

func TestClaudeSessionID_PidReuseGuard(t *testing.T) {
	home := writeSessionFile(t, 4242,
		`{"pid":4242,"sessionId":"abc-123","procStart":"9912345"}`)
	// The live process started at a different tick: same pid, different process.
	if got := ClaudeSessionID(4242, home, fixedStart("8800000")); got != "" {
		t.Errorf("ClaudeSessionID = %q, want empty on procStart mismatch", got)
	}
}

func TestClaudeSessionID_ProcStartError(t *testing.T) {
	home := writeSessionFile(t, 4242,
		`{"pid":4242,"sessionId":"abc-123","procStart":"9912345"}`)
	failing := func(int) (string, error) { return "", errors.New("no /proc") }
	if got := ClaudeSessionID(4242, home, failing); got != "" {
		t.Errorf("ClaudeSessionID = %q, want empty when procStart errors", got)
	}
}

func TestClaudeSessionID_PidBodyMismatch(t *testing.T) {
	// File named 4242.json but claiming pid 9999 inside.
	home := writeSessionFile(t, 4242,
		`{"pid":9999,"sessionId":"abc-123","procStart":"9912345"}`)
	if got := ClaudeSessionID(4242, home, fixedStart("9912345")); got != "" {
		t.Errorf("ClaudeSessionID = %q, want empty on pid/body mismatch", got)
	}
}

func TestClaudeSessionID_DegenerateInputs(t *testing.T) {
	home := writeSessionFile(t, 4242,
		`{"pid":4242,"sessionId":"abc-123","procStart":"1"}`)
	if got := ClaudeSessionID(0, home, fixedStart("1")); got != "" {
		t.Errorf("pid 0: got %q, want empty", got)
	}
	if got := ClaudeSessionID(4242, "", fixedStart("1")); got != "" {
		t.Errorf("empty home: got %q, want empty", got)
	}
	if got := ClaudeSessionID(4242, home, nil); got != "" {
		t.Errorf("nil procStart: got %q, want empty", got)
	}
}

func TestParseProcStartTime_CommWithSpacesAndParens(t *testing.T) {
	// comm "(tmux: server) x)" — spaces AND a ')' inside; only the LAST ')'
	// terminates it. Fields after comm: state(3) ... starttime(22) = index 19.
	line := "1234 (tmux: server) x) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 START 23 24"
	got, err := parseProcStartTime([]byte(line))
	if err != nil {
		t.Fatalf("parseProcStartTime: %v", err)
	}
	if got != "START" {
		t.Errorf("starttime = %q, want %q", got, "START")
	}
}

func TestParseProcStartTime_TooShort(t *testing.T) {
	if _, err := parseProcStartTime([]byte("1234 (x) S 1 2 3")); err == nil {
		t.Error("expected error for truncated stat line")
	}
	if _, err := parseProcStartTime([]byte("no comm terminator here")); err == nil {
		t.Error("expected error for missing ')'")
	}
}

// TestReadProcStart_Self reads the test process's own stat line — the one
// process guaranteed to exist — and checks a plausible numeric field comes
// back. /proc is Linux-shaped; skip elsewhere.
func TestReadProcStart_Self(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc/<pid>/stat is Linux-only")
	}
	got, err := ReadProcStart(os.Getpid())
	if err != nil {
		t.Fatalf("ReadProcStart(self): %v", err)
	}
	if got == "" {
		t.Fatal("ReadProcStart(self) returned empty starttime")
	}
	if _, err := strconv.ParseUint(got, 10, 64); err != nil {
		t.Errorf("starttime %q is not numeric: %v", got, err)
	}
}
