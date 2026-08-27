// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// stubClientInfoHost pins the handshake-derived host for the duration of a
// test, mirroring stubHostSessionID. An empty name models "cannot be
// confirmed" (no session, no clientInfo, unnamed client).
func stubClientInfoHost(t *testing.T, name string) {
	t.Helper()
	orig := clientInfoHost
	clientInfoHost = func(context.Context) string { return name }
	t.Cleanup(func() { clientInfoHost = orig })
}

func TestDeclaredClientName(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"absent", "", ""},
		{"null", "null", ""},
		{"clientinfo object", `{"name":"grok-cli","version":"1.2"}`, "grok-cli"},
		{"object without name", `{"version":"1.2"}`, ""},
		{"object with whitespace name", `{"name":"  "}`, ""},
		{"bare string", `"grok-cli"`, "grok-cli"},
		{"empty string", `""`, ""},
		{"garbage", `{not json`, ""},
		{"number", `42`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.raw != "" {
				raw = json.RawMessage(tc.raw)
			}
			if got := declaredClientName(raw); got != tc.want {
				t.Errorf("declaredClientName(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestResolveCaptureHost(t *testing.T) {
	cases := []struct {
		name       string
		derived    string
		clientInfo string
		wantHost   string
		wantSource string
	}{
		{"derived only", "Zed", "", storage.HostZed, storage.HostSourceDerived},
		{"derived wins over declared", "Zed", `{"name":"grok-cli"}`, storage.HostZed, storage.HostSourceDerived},
		{"derived wins over matching declared", "Zed", `{"name":"Zed"}`, storage.HostZed, storage.HostSourceDerived},
		{"declared fallback", "", `{"name":"grok-cli"}`, storage.HostGrok, storage.HostSourceDeclared},
		{"neither is explicit unknown", "", "", storage.HostUnknown, storage.HostSourceUnknown},
		{"unparseable declared is unknown", "", `{broken`, storage.HostUnknown, storage.HostSourceUnknown},

		// The three specimens the live vault already holds, which are the whole
		// reason this normalization exists. Before it, each was written through
		// verbatim, so one agent on one machine was recorded as two hosts.
		{"specimen grok-shell-vibe-palace", "grok-shell-vibe-palace", "", storage.HostGrok, storage.HostSourceDerived},
		{"specimen capture-oneshot", "capture-oneshot", "", storage.HostUnknown, storage.HostSourceDerived},
		{"specimen Zed", "Zed", "", storage.HostZed, storage.HostSourceDerived},

		// An unrecognized DECLARED name closes the same way, and keeps the
		// provenance that says a caller — not the transport — supplied it.
		{"unrecognized declared normalizes", "", `{"name":"capture-oneshot"}`, storage.HostUnknown, storage.HostSourceDeclared},

		// host=unknown with source=derived is NOT the same record as the
		// source=unknown case above: the writer looked and the client named
		// itself something outside the vocabulary. Only the second means
		// nothing identified itself at all.
		{"unrecognized derived keeps derived provenance", "cursor", "", storage.HostUnknown, storage.HostSourceDerived},

		// Fail closed on an ambiguous name rather than picking a winner.
		{"two hosts in one name is unknown", "grok-zed-bridge", "", storage.HostUnknown, storage.HostSourceDerived},

		// Claude wins outright, so a compound name is never auto-inlined.
		{"claude compound normalizes to claude-code", "claude-grok-bridge", "", storage.HostClaudeCode, storage.HostSourceDerived},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubClientInfoHost(t, tc.derived)
			var raw json.RawMessage
			if tc.clientInfo != "" {
				raw = json.RawMessage(tc.clientInfo)
			}
			host, source := resolveCaptureHost(context.Background(), raw)
			if host != tc.wantHost || source != tc.wantSource {
				t.Errorf("resolveCaptureHost = (%q, %q), want (%q, %q)",
					host, source, tc.wantHost, tc.wantSource)
			}
		})
	}
}

// captureAndReadMeta drives the vp_capture_session handler with the given
// params and reads the written note's frontmatter back from the ephemeral
// vault.
func captureAndReadMeta(t *testing.T, params string) storage.SessionMeta {
	t.Helper()
	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)
	result, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	r := result.(captureSessionResult)
	meta, _, err := vault.ReadSession("test-proj", r.SessionID[:10], capture.ParseFingerprint(r.SessionID), r.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	return meta
}

// DERIVED-WINS: a caller-declared client_info must lose to the host the
// initialize handshake confirmed, and the note must say the value was derived.
func TestCaptureSessionHostDerivedWins(t *testing.T) {
	stubHostSessionID(t, "")
	stubClientInfoHost(t, "Zed")

	// The transcript is load-bearing FIXTURE, not subject: "Zed" is a derived
	// hook-less host, so a capture with no transcript now fails loud (it can
	// never have an archive — see TestCaptureSessionFailsHardWhenNoArchiveIsPossible).
	// Supplying one keeps this test about attribution instead of archiving.
	meta := captureAndReadMeta(t, `{
		"project": "test-proj",
		"summary": "Captured over MCP from a handshake-identified host.",
		"transcript": "user: hi\nassistant: hello",
		"client_info": {"name": "some-claimed-host", "version": "9.9"}
	}`)
	// "Zed" is the RAW handshake name; storage.HostZed is what the note must
	// carry after normalization. The claim under test is still DERIVED-WINS —
	// "some-claimed-host" would normalize to unknown, so a note carrying zed
	// can only have come from the derived side.
	if meta.Host != storage.HostZed {
		t.Errorf("host = %q, want %q — the declared claim must lose to the derived value", meta.Host, storage.HostZed)
	}
	if meta.HostSource != storage.HostSourceDerived {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceDerived)
	}
}

// Declared fallback: with no handshake identity, a caller-declared client_info
// is honored but labeled "declared" so a reader can tell it from a measured one.
func TestCaptureSessionHostDeclaredFallback(t *testing.T) {
	stubHostSessionID(t, "")
	stubClientInfoHost(t, "")

	meta := captureAndReadMeta(t, `{
		"project": "test-proj",
		"summary": "Captured with only a caller-declared identity.",
		"client_info": {"name": "grok-cli", "version": "1.0"}
	}`)
	if meta.Host != storage.HostGrok {
		t.Errorf("host = %q, want %q", meta.Host, storage.HostGrok)
	}
	if meta.HostSource != storage.HostSourceDeclared {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceDeclared)
	}
}

// Absent everywhere: the note records an EXPLICIT unknown with provenance —
// never an empty field, and never a claude-code default. clientInfoHost is
// deliberately NOT stubbed here, so this also exercises the real derivation
// against a context with no MCP session (the absent-ClientInfo shape).
func TestCaptureSessionHostUnknownRecorded(t *testing.T) {
	stubHostSessionID(t, "")

	vault := testSessionVault(t)
	tool := CaptureSessionTool(vault, nil)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{
		"project": "test-proj",
		"summary": "Captured with no host identity at all."
	}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	r := result.(captureSessionResult)
	meta, _, err := vault.ReadSession("test-proj", r.SessionID[:10], capture.ParseFingerprint(r.SessionID), r.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	if meta.Host != storage.HostUnknown {
		t.Errorf("host = %q, want %q — absence must be recorded, not defaulted", meta.Host, storage.HostUnknown)
	}
	if meta.HostSource != storage.HostSourceUnknown {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceUnknown)
	}

	// The provenance must be on disk in the note's frontmatter, not just in
	// the struct round-trip.
	notePath, err := vault.SessionFile("test-proj", r.SessionID[:10], capture.ParseFingerprint(r.SessionID), r.Iteration)
	if err != nil {
		t.Fatalf("SessionFile: %v", err)
	}
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "host: unknown") {
		t.Error("note frontmatter missing 'host: unknown'")
	}
	if !strings.Contains(body, "host_source: unknown") {
		t.Error("note frontmatter missing 'host_source: unknown'")
	}
	if strings.Contains(body, "host: claude-code") {
		t.Error("note claims claude-code with no evidence — the fabricated default is back")
	}
}

// The load-bearing invariant of the ONE-TABLE rule: whatever
// resolveCaptureHost writes must still reach the same auto-inline verdict the
// RAW client name would have. Two copies of the vocabulary would break exactly
// here — a normalizer sending "Zed" to unknown while the predicate still calls
// the raw name hook-less silently drops the inline archive that is the native
// Zed pane's ONLY durable capture, and the capture would report success.
//
// This is asserted over the normalize→predicate composition, not over either
// half alone, because each half is independently correct in that failure.
func TestNormalizedHostKeepsAutoInlineVerdict(t *testing.T) {
	cases := []struct {
		raw          string
		wantHost     string
		wantHookless bool
	}{
		{"Zed", storage.HostZed, true},
		{"zed-editor", storage.HostZed, true},
		{"grok-shell-vibe-palace", storage.HostGrok, true},
		{"grok-cli", storage.HostGrok, true},
		{"xai-mcp", storage.HostXAI, true},
		{"capture-oneshot", storage.HostUnknown, false},
		{"claude-code", storage.HostClaudeCode, false},
		{"claude-grok-bridge", storage.HostClaudeCode, false},
		{"optimized", storage.HostUnknown, false},
	}
	const tx = "user: hi\nassistant: hello"
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			stubClientInfoHost(t, tc.raw)
			host, source := resolveCaptureHost(context.Background(), nil)
			if host != tc.wantHost {
				t.Fatalf("host = %q, want %q", host, tc.wantHost)
			}
			// The raw name and the normalized name must agree, or the table
			// has been forked.
			if raw, norm := isHooklessClient(tc.raw), isHooklessClient(host); raw != norm {
				t.Errorf("isHooklessClient disagrees across normalization: raw %q = %v, normalized %q = %v",
					tc.raw, raw, host, norm)
			}
			if got := wantInlineTranscriptArchive(false, "", tx, host, source); got != tc.wantHookless {
				t.Errorf("wantInlineTranscriptArchive(host=%q from raw %q) = %v, want %v",
					host, tc.raw, got, tc.wantHookless)
			}
		})
	}
}

func TestIsHooklessClient(t *testing.T) {
	cases := []struct {
		name string
		host string
		want bool
	}{
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"grok exact", "grok", true},
		{"Grok case", "Grok", true},
		{"grok-cli", "grok-cli", true},
		{"xai exact", "xai", true},
		{"XAI-Agent", "XAI-Agent", true},
		{"zed exact", "zed", true},
		{"Zed case", "Zed", true},
		{"zed-editor", "zed-editor", true},
		{"claude-code", "claude-code", false},
		{"Claude", "Claude", false},
		// MUT-1 pin: claude guard must fire even when an allow-list token
		// appears in the same compound name (substring allow-list would match "grok").
		{"claude-grok-bridge", "claude-grok-bridge", false},
		// Substring trap: English "zed" inside an unrelated client name.
		{"optimized", "optimized", false},
		{"authorized-client", "authorized-client", false},
		{"unknown", "unknown", false},
		{"cursor", "cursor", false},
		{"http-serve", "http-serve", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isHooklessClient(tc.host); got != tc.want {
				t.Errorf("isHooklessClient(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

func TestWantInlineTranscriptArchive(t *testing.T) {
	const tx = "user: hi\nassistant: hello"
	cases := []struct {
		name      string
		force     bool
		sessionID string
		tx        string
		host      string
		source    string
		want      bool
	}{
		{"auto grok derived", false, "", tx, "grok", storage.HostSourceDerived, true},
		{"auto zed derived", false, "", tx, "Zed", storage.HostSourceDerived, true},
		{"auto xai derived", false, "", tx, "xai-mcp", storage.HostSourceDerived, true},
		{"no auto unknown", false, "", tx, storage.HostUnknown, storage.HostSourceUnknown, false},
		{"no auto declared grok", false, "", tx, "grok", storage.HostSourceDeclared, false},
		{"no auto derived claude", false, "", tx, "claude-code", storage.HostSourceDerived, false},
		{"no auto empty tx", false, "", "", "grok", storage.HostSourceDerived, false},
		// The silent branch this predicate CANNOT cover: a derived Zed native
		// pane with no transcript. There is nothing to archive inline, so false
		// is correct here and stays correct — you cannot archive empty bytes.
		// The loss is real all the same, because no SessionEnd hook will write
		// one later either, so the CALLER fails loud on exactly this input. See
		// TestCaptureSessionFailsHardWhenNoArchiveIsPossible.
		{"no auto empty tx zed", false, "", "", "Zed", storage.HostSourceDerived, false},
		{"no auto with derived id", false, "live-id", tx, "grok", storage.HostSourceDerived, false},
		{"explicit true unknown", true, "", tx, storage.HostUnknown, storage.HostSourceUnknown, true},
		{"explicit true no-op with id", true, "live-id", tx, "claude-code", storage.HostSourceDerived, false},
		{"explicit false still auto grok", false, "", tx, "grok", storage.HostSourceDerived, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wantInlineTranscriptArchive(tc.force, tc.sessionID, tc.tx, tc.host, tc.source)
			if got != tc.want {
				t.Errorf("wantInlineTranscriptArchive(...) = %v, want %v", got, tc.want)
			}
		})
	}
}
