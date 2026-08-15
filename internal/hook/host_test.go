// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The hook path's host attribution, pinned from both sides.
//
// Run() names the host from the WIRE DIALECT of the payload it was handed, and
// records CLAUDE_CODE_ENTRYPOINT separately as Entrypoint. The split is the
// contract under test: one field says which application wrote the note, the
// other says what an environment variable happened to hold, and no test here may
// let the second answer the first.
//
// 🔴 EVERY TEST THAT TOUCHES THE ENVIRONMENT MUST SET CLAUDE_CODE_ENTRYPOINT
// EXPLICITLY, INCLUDING THE ABSENT CASE. `go test` inherits the developer's
// environment, and a Claude Code CLI session exports CLAUDE_CODE_ENTRYPOINT=cli
// to every descendant — so an absent-case test written by omission sees `cli` on
// a maintainer's machine while passing in CI. The t.Setenv("") calls below are
// load-bearing, not decoration. (That same inheritance is why the variable no
// longer decides Host at all; see resolveHookHost.)

// soleNote returns the single session note the fixture's run produced, failing
// the test if there is not exactly one.
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

// runWire drives Run through the JSON decoder, which is the ONLY place a dialect
// is observed. A test that builds a Payload struct literal — as every other test
// in this package does — reports no dialect, so host attribution can only be
// exercised from the wire, exactly as in production.
func runWire(t *testing.T, f linkFixture, rawJSON string) *Result {
	t.Helper()
	var p Payload
	if err := json.Unmarshal([]byte(rawJSON), &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	res, err := Run(context.Background(), p, f.opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.SessionNoteID == "" {
		t.Fatal("expected a session note to be written")
	}
	return res
}

// frontmatterHas fails unless the note on disk carries the exact key/value line.
// The parsed struct is not enough: the frontmatter is what a human inspects and
// what every other reader parses.
func frontmatterHas(t *testing.T, path string, lines ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	body := string(data)
	for _, want := range lines {
		if !strings.Contains(body, "\n"+want+"\n") {
			t.Errorf("note frontmatter missing %q", want)
		}
	}
}

// Claude Code writes snake_case keys. That spelling is the evidence.
func TestRun_HostDerivedFromClaudeWireDialect(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")

	f := newLinkFixture(t)
	runWire(t, f, `{
		"session_id": "sess-claude-dialect",
		"transcript_path": "`+f.transcript+`",
		"cwd": "`+f.cwd+`",
		"hook_event_name": "Stop"
	}`)

	meta, path := soleNote(t, f)
	if meta.Host != storage.HostClaudeCode {
		t.Errorf("host = %q, want %q", meta.Host, storage.HostClaudeCode)
	}
	if meta.HostSource != storage.HostSourceDerived {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceDerived)
	}
	frontmatterHas(t, path, "host: claude-code", "host_source: derived", "entrypoint: cli")
}

// Grok writes camelCase keys and snake_case event values. Same hook, same
// settings.json entry, different client — and the note must say so.
func TestRun_HostDerivedFromGrokWireDialect(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")

	f := newLinkFixture(t)
	runWire(t, f, `{
		"sessionId": "sess-grok-dialect",
		"transcriptPath": "`+f.transcript+`",
		"cwd": "`+f.cwd+`",
		"hookEventName": "stop"
	}`)

	meta, path := soleNote(t, f)
	if meta.Host != storage.HostGrok {
		t.Errorf("host = %q, want %q", meta.Host, storage.HostGrok)
	}
	if meta.HostSource != storage.HostSourceDerived {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceDerived)
	}
	frontmatterHas(t, path, "host: grok", "host_source: derived", "entrypoint: unknown")
}

// 🔴 THE LOAD-BEARING TEST — and the one the 2026-08-05 design could not write.
//
// Two clients, two dialects, two DIFFERENT host values from the same hook. A
// test that only asserts "the field is non-empty" passes on a hard-coded default
// and proves nothing; this one fails on any implementation that answers from a
// constant, because one constant cannot be both values at once.
func TestRun_ClaudeAndGrokAreDistinguishable(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")

	claude := newLinkFixture(t)
	runWire(t, claude, `{
		"session_id": "sess-c",
		"transcript_path": "`+claude.transcript+`",
		"cwd": "`+claude.cwd+`",
		"hook_event_name": "Stop"
	}`)

	grok := newLinkFixture(t)
	runWire(t, grok, `{
		"sessionId": "sess-g",
		"transcriptPath": "`+grok.transcript+`",
		"cwd": "`+grok.cwd+`",
		"hookEventName": "stop"
	}`)

	cMeta, _ := soleNote(t, claude)
	gMeta, _ := soleNote(t, grok)

	if cMeta.Host == gMeta.Host {
		t.Fatalf("both clients recorded host = %q — the two capture paths are indistinguishable in the vault, which is the whole defect this task exists to fix", cMeta.Host)
	}
	if cMeta.Host != storage.HostClaudeCode || gMeta.Host != storage.HostGrok {
		t.Errorf("host pair = (%q, %q), want (%q, %q)", cMeta.Host, gMeta.Host, storage.HostClaudeCode, storage.HostGrok)
	}
}

// 🔴 THE REGRESSION PIN for the defect that shipped between 2026-08-05 and
// 2026-08-15: host derived from an INHERITED environment variable.
//
// The environment here says Claude Code. It is lying by inheritance — this is a
// Grok payload, and in production it is a grok pane launched from inside a Claude
// Code session, which inherits CLAUDE_CODE_ENTRYPOINT=cli through the process
// tree. The old implementation wrote `host: cli, host_source: derived` for that
// case: a confidently wrong attribution wearing a provenance that claims it was
// measured. The wire dialect cannot be inherited, so it wins.
func TestRun_HostIgnoresTheInheritedEntrypointEnv(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")

	f := newLinkFixture(t)
	runWire(t, f, `{
		"sessionId": "sess-grok-inside-claude",
		"transcriptPath": "`+f.transcript+`",
		"cwd": "`+f.cwd+`",
		"hookEventName": "stop"
	}`)

	meta, _ := soleNote(t, f)
	if meta.Host != storage.HostGrok {
		t.Errorf("host = %q, want %q — a Grok payload was attributed from an inherited env var", meta.Host, storage.HostGrok)
	}
	if meta.Host == "cli" || meta.Host == storage.HostClaudeCode {
		t.Error("host came from CLAUDE_CODE_ENTRYPOINT: environment inheritance is transitive, so this attribution is a guess wearing a measurement's provenance (ADR-006)")
	}
	// The env var is still RECORDED — under the name that claims only what it
	// measured. Dropping it would lose the one signal that separates a CLI
	// session from an ACP pane.
	if meta.Entrypoint != "cli" {
		t.Errorf("entrypoint = %q, want %q — the measurement is kept, only its meaning is narrowed", meta.Entrypoint, "cli")
	}
}

// The negative branch — the one that silently rots into a guess. A payload whose
// dialect cannot be established records the explicit unknown PAIR, never a
// default host. Run's own callers construct Payload literals, so this is a live
// production shape, not a contrivance.
func TestRun_HostUnknownWhenDialectIndeterminate(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")

	f := newLinkFixture(t)
	if res := f.run(t, "sess-no-dialect", "Stop"); res.SessionNoteID == "" {
		t.Fatal("expected a session note to be written")
	}

	meta, path := soleNote(t, f)
	if meta.Host != storage.HostUnknown {
		t.Errorf("host = %q, want %q — an unobserved dialect must be recorded, not defaulted", meta.Host, storage.HostUnknown)
	}
	if meta.HostSource != storage.HostSourceUnknown {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceUnknown)
	}
	frontmatterHas(t, path, "host: unknown", "host_source: unknown")
}

// Entrypoint is READ FROM THE ENVIRONMENT, not compiled in. Driving a value no
// source file contains is what separates this from an assertion a literal could
// satisfy.
func TestRun_EntrypointReadFromEnvironmentNotACompiledDefault(t *testing.T) {
	const sentinel = "entrypoint-sentinel-8f2c"
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", sentinel)

	f := newLinkFixture(t)
	runWire(t, f, `{
		"session_id": "sess-entrypoint-sentinel",
		"transcript_path": "`+f.transcript+`",
		"cwd": "`+f.cwd+`",
		"hook_event_name": "Stop"
	}`)

	meta, _ := soleNote(t, f)
	if meta.Entrypoint != sentinel {
		t.Errorf("entrypoint = %q, want the environment's value %q", meta.Entrypoint, sentinel)
	}
	// ...and it must not have contaminated the host, which the dialect owns.
	if meta.Host != storage.HostClaudeCode {
		t.Errorf("host = %q, want %q", meta.Host, storage.HostClaudeCode)
	}
}

// An absent variable records the explicit unknown, so "we looked and found
// nothing" stays distinguishable from a note whose writer never looked — the
// same doctrine as HostUnknown, and the distinction that made the presence-based
// discrimination rule unimplementable.
func TestRun_EntrypointUnknownWhenEnvAbsent(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")

	f := newLinkFixture(t)
	runWire(t, f, `{
		"session_id": "sess-entrypoint-absent",
		"transcript_path": "`+f.transcript+`",
		"cwd": "`+f.cwd+`",
		"hook_event_name": "Stop"
	}`)

	meta, path := soleNote(t, f)
	if meta.Entrypoint != storage.EntrypointUnknown {
		t.Errorf("entrypoint = %q, want %q", meta.Entrypoint, storage.EntrypointUnknown)
	}
	frontmatterHas(t, path, "entrypoint: unknown")
}

// detectDialect in isolation, including the shapes Run can never reach through
// its own fixtures.
func TestDetectDialect(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		want  string
		event string
	}{
		{name: "claude snake_case", raw: `{"session_id":"a","transcript_path":"t","hook_event_name":"Stop"}`, want: DialectClaude, event: "Stop"},
		{name: "grok camelCase", raw: `{"sessionId":"a","transcriptPath":"t","hookEventName":"stop"}`, want: DialectGrok, event: "Stop"},
		{name: "claude with only session_id", raw: `{"session_id":"a"}`, want: DialectClaude},
		{name: "grok with only sessionId", raw: `{"sessionId":"a"}`, want: DialectGrok},

		// cwd is spelled identically by both clients, so it is evidence of
		// nothing and must not tip the answer either way.
		{name: "cwd alone is not evidence", raw: `{"cwd":"/tmp"}`, want: DialectUnknown},
		{name: "empty object", raw: `{}`, want: DialectUnknown},

		// No client sends both spellings. A payload that does is synthetic or
		// malformed, and naming a host from it would be a guess — which is the
		// one thing this signal exists to avoid.
		{name: "mixed spellings claim nothing", raw: `{"session_id":"a","sessionId":"a"}`, want: DialectUnknown},
		{name: "mixed across fields", raw: `{"session_id":"a","hookEventName":"stop"}`, want: DialectUnknown},

		// Present-but-empty is not a spelling observation.
		{name: "empty values are not evidence", raw: `{"session_id":"","sessionId":""}`, want: DialectUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Payload
			if err := json.Unmarshal([]byte(tc.raw), &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if p.Dialect != tc.want {
				t.Errorf("dialect = %q, want %q", p.Dialect, tc.want)
			}
			// Dialect detection must not disturb the normalization it rides
			// along with: a Grok event value still arrives canonicalized.
			if tc.event != "" && p.HookEventName != tc.event {
				t.Errorf("hook_event_name = %q, want %q", p.HookEventName, tc.event)
			}
		})
	}
}

// resolveHookHost's table, stated once and directly. The Run-level tests above
// prove the wiring; this proves the policy, including the pair that must never
// appear: a positive host with an unknown source, or the reverse.
func TestResolveHookHost(t *testing.T) {
	cases := []struct {
		dialect, env        string
		host, source, entry string
	}{
		{DialectClaude, "cli", storage.HostClaudeCode, storage.HostSourceDerived, "cli"},
		{DialectClaude, "", storage.HostClaudeCode, storage.HostSourceDerived, storage.EntrypointUnknown},
		{DialectGrok, "cli", storage.HostGrok, storage.HostSourceDerived, "cli"},
		{DialectGrok, "", storage.HostGrok, storage.HostSourceDerived, storage.EntrypointUnknown},
		{DialectUnknown, "cli", storage.HostUnknown, storage.HostSourceUnknown, "cli"},
		{DialectUnknown, "", storage.HostUnknown, storage.HostSourceUnknown, storage.EntrypointUnknown},
	}

	for _, tc := range cases {
		host, source, entry := resolveHookHost(tc.dialect, tc.env)
		if host != tc.host || source != tc.source || entry != tc.entry {
			t.Errorf("resolveHookHost(%q, %q) = (%q, %q, %q), want (%q, %q, %q)",
				tc.dialect, tc.env, host, source, entry, tc.host, tc.source, tc.entry)
		}
		// Never a positive host with an unknown provenance, and never the
		// reverse: the pair is the claim, and half a claim is unreadable.
		if (host == storage.HostUnknown) != (source == storage.HostSourceUnknown) {
			t.Errorf("resolveHookHost(%q, %q) split the pair: host=%q source=%q", tc.dialect, tc.env, host, source)
		}
	}
}
