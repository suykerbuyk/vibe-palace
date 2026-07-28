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
		{"derived only", "Zed", "", "Zed", storage.HostSourceDerived},
		{"derived wins over declared", "Zed", `{"name":"grok-cli"}`, "Zed", storage.HostSourceDerived},
		{"derived wins over matching declared", "Zed", `{"name":"Zed"}`, "Zed", storage.HostSourceDerived},
		{"declared fallback", "", `{"name":"grok-cli"}`, "grok-cli", storage.HostSourceDeclared},
		{"neither is explicit unknown", "", "", storage.HostUnknown, storage.HostSourceUnknown},
		{"unparseable declared is unknown", "", `{broken`, storage.HostUnknown, storage.HostSourceUnknown},
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

	meta := captureAndReadMeta(t, `{
		"project": "test-proj",
		"summary": "Captured over MCP from a handshake-identified host.",
		"client_info": {"name": "some-claimed-host", "version": "9.9"}
	}`)
	if meta.Host != "Zed" {
		t.Errorf("host = %q, want %q — the declared claim must lose to the derived value", meta.Host, "Zed")
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
	if meta.Host != "grok-cli" {
		t.Errorf("host = %q, want %q", meta.Host, "grok-cli")
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
