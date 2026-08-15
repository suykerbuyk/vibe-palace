// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The hook path's host attribution, pinned from BOTH sides. Run() reads
// CLAUDE_CODE_ENTRYPOINT out of its own process environment (the hook is a
// child of the host that fired it), records storage.HostSourceDerived when it
// is there, and records the explicit unknown pair when it is not.
//
// 🔴 EVERY TEST HERE MUST SET CLAUDE_CODE_ENTRYPOINT EXPLICITLY, INCLUDING THE
// ABSENT CASE. `go test` inherits the developer's environment, and a Claude
// Code CLI session exports CLAUDE_CODE_ENTRYPOINT=cli to every descendant — so
// the unknown-branch test run on a maintainer's machine sees `cli` unless it
// clears the variable itself, and would fail there while passing in CI. The
// t.Setenv("", ...) call below is load-bearing, not decoration.
//
// KNOWN LIMITATION, pinned here so nobody reads these greens as more than they
// are: the env var is inherited TRANSITIVELY, so any agent launched from inside
// a Claude Code session (a grok pane, a zed started from a Bash call, a wrapper
// script) inherits it and will be attributed `cli` with a `derived` provenance
// that asserts the value was measured. These tests pin the CURRENT contract;
// narrowing it is tracked on the task hook-path-writes-no-host-attribution.

// notePath returns the absolute path of the single session note the fixture's
// run produced, failing the test if there is not exactly one.
func soleNote(t *testing.T, f linkFixture) (storage.SessionMeta, string) {
	t.Helper()
	vault := storage.NewVault(f.vaultRoot)
	notes, err := vault.ListSessions("test-project", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected exactly 1 note, got %d", len(notes))
	}
	return notes[0], filepath.Join(f.vaultRoot, notes[0].NotePath)
}

// A hook invocation that CAN see the host's entrypoint records it, and says the
// value was derived rather than declared.
func TestRun_HostDerivedFromEntrypointEnv(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")

	f := newLinkFixture(t)
	if res := f.run(t, "sess-host-derived", "Stop"); res.SessionNoteID == "" {
		t.Fatal("expected a session note to be written")
	}

	meta, path := soleNote(t, f)
	if meta.Host != "cli" {
		t.Errorf("host = %q, want %q", meta.Host, "cli")
	}
	if meta.HostSource != storage.HostSourceDerived {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceDerived)
	}

	// Assert the ON-DISK shape too, not just the parsed struct: the frontmatter
	// is what a human inspects and what every other reader parses.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "\nhost: cli\n") {
		t.Error("note frontmatter missing 'host: cli'")
	}
	if !strings.Contains(body, "\nhost_source: derived\n") {
		t.Error("note frontmatter missing 'host_source: derived'")
	}
}

// The negative branch — the one that silently rots into a guess. An unreadable
// environment records the explicit unknown PAIR, never a default host.
func TestRun_HostUnknownWhenEntrypointEnvAbsent(t *testing.T) {
	// Empty is how Run() sees "absent": it tests the value, not the presence of
	// the key. Clearing it here is what makes this test mean the same thing on a
	// maintainer's Claude Code machine as it does in CI (see the file comment).
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")

	f := newLinkFixture(t)
	if res := f.run(t, "sess-host-unknown", "Stop"); res.SessionNoteID == "" {
		t.Fatal("expected a session note to be written")
	}

	meta, path := soleNote(t, f)
	if meta.Host != storage.HostUnknown {
		t.Errorf("host = %q, want %q — absence must be recorded, not defaulted", meta.Host, storage.HostUnknown)
	}
	if meta.HostSource != storage.HostSourceUnknown {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceUnknown)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "\nhost: unknown\n") {
		t.Error("note frontmatter missing 'host: unknown'")
	}
	if !strings.Contains(body, "\nhost_source: unknown\n") {
		t.Error("note frontmatter missing 'host_source: unknown'")
	}
}

// The mutation proof. The two tests above are both satisfiable by a hard-coded
// constant paired with a hard-coded fallback; this one is not. It drives a value
// no source file contains, so any implementation that answers from a literal —
// "claude-code", "cli", anything — goes red here while the others stay green.
//
// That is the point: a host attribution that reports a compiled-in name is the
// failure this whole field exists to prevent (ADR-006 — a wrong DERIVE is a
// silent lie wearing the face of a measurement).
func TestRun_HostIsReadFromTheEnvironmentNotACompiledDefault(t *testing.T) {
	const sentinel = "entrypoint-sentinel-8f2c"
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", sentinel)

	f := newLinkFixture(t)
	if res := f.run(t, "sess-host-sentinel", "Stop"); res.SessionNoteID == "" {
		t.Fatal("expected a session note to be written")
	}

	meta, _ := soleNote(t, f)
	if meta.Host != sentinel {
		t.Errorf("host = %q, want the environment's value %q — a compiled-in host name is exactly what this field must never carry",
			meta.Host, sentinel)
	}
	if meta.Host == "claude-code" {
		t.Error("host defaulted to claude-code: the default that made the Zed adapter unreachable (ADR-006)")
	}
	if meta.HostSource != storage.HostSourceDerived {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceDerived)
	}
}
