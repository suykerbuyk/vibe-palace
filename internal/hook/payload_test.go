// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"encoding/json"
	"testing"
)

// TestPayloadUnmarshal_BothWireFormats proves the Payload decoder reconciles the
// two hook wire formats that reach vp: Claude Code (snake_case keys, PascalCase
// event values) and Grok (camelCase keys, snake_case event values). Before the
// dual-format decoder, a Grok payload landed with only cwd populated and failed
// at "session_id is required" — see hook-failure-cleanup. The Grok fixtures here
// mirror grok-build's HookEventEnvelope exactly (crates/codegen/xai-grok-hooks/
// src/event.rs: camelCase keys via rename_all, snake_case event via the enum's
// own rename_all="snake_case").
func TestPayloadUnmarshal_BothWireFormats(t *testing.T) {
	cases := []struct {
		name      string
		wire      string
		wantSess  string
		wantTrans string
		wantCWD   string
		wantEvent string
	}{
		{
			name:      "claude snake_case SessionEnd",
			wire:      `{"session_id":"claude-1","transcript_path":"/t/claude.jsonl","cwd":"/home/johns/code/vibe-palace","hook_event_name":"SessionEnd"}`,
			wantSess:  "claude-1",
			wantTrans: "/t/claude.jsonl",
			wantCWD:   "/home/johns/code/vibe-palace",
			wantEvent: "SessionEnd",
		},
		{
			name:      "grok camelCase session_end",
			wire:      `{"sessionId":"grok-1","transcriptPath":"/t/grok.jsonl","cwd":"/home/johns/code/grok-build","workspaceRoot":"/home/johns/code/grok-build","timestamp":"2026-07-21T11:20:00Z","hookEventName":"session_end"}`,
			wantSess:  "grok-1",
			wantTrans: "/t/grok.jsonl",
			wantCWD:   "/home/johns/code/grok-build",
			wantEvent: "SessionEnd",
		},
		{
			name:      "grok camelCase stop",
			wire:      `{"sessionId":"grok-2","cwd":"/w","hookEventName":"stop"}`,
			wantSess:  "grok-2",
			wantCWD:   "/w",
			wantEvent: "Stop",
		},
		{
			name:      "grok camelCase pre_compact",
			wire:      `{"sessionId":"grok-3","cwd":"/w","hookEventName":"pre_compact"}`,
			wantSess:  "grok-3",
			wantCWD:   "/w",
			wantEvent: "PreCompact",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var p Payload
			if err := json.Unmarshal([]byte(tc.wire), &p); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if p.SessionID != tc.wantSess {
				t.Errorf("SessionID = %q, want %q", p.SessionID, tc.wantSess)
			}
			if p.TranscriptPath != tc.wantTrans {
				t.Errorf("TranscriptPath = %q, want %q", p.TranscriptPath, tc.wantTrans)
			}
			if p.CWD != tc.wantCWD {
				t.Errorf("CWD = %q, want %q", p.CWD, tc.wantCWD)
			}
			if p.HookEventName != tc.wantEvent {
				t.Errorf("HookEventName = %q, want %q", p.HookEventName, tc.wantEvent)
			}
			// The canonicalized event must clear the same gate Run uses.
			if !ValidEvents[p.HookEventName] {
				t.Errorf("event %q not in ValidEvents after normalization", p.HookEventName)
			}
		})
	}
}

// TestPayloadUnmarshal_EmptyStillRejected guards the boundary the operator's
// standing decision draws: reconciling formats must NOT turn a genuinely empty
// payload into a valid one. An empty object yields an empty SessionID, which Run
// still rejects at the front door — the loud error for a truly blank invocation
// is preserved.
func TestPayloadUnmarshal_EmptyStillRejected(t *testing.T) {
	var p Payload
	if err := json.Unmarshal([]byte(`{}`), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p.SessionID != "" {
		t.Errorf("SessionID = %q, want empty", p.SessionID)
	}
	if ValidEvents[p.HookEventName] {
		t.Errorf("empty payload produced a valid event %q", p.HookEventName)
	}
}

// TestCanonicalHookEvent covers the event-name mapping in isolation, including
// the pass-through arm that leaves an unhandled event as-is so the ValidEvents
// gate can still reject it.
func TestCanonicalHookEvent(t *testing.T) {
	cases := map[string]string{
		"session_end":  "SessionEnd",   // grok
		"stop":         "Stop",         // grok
		"pre_compact":  "PreCompact",   // grok
		"SessionEnd":   "SessionEnd",   // claude, unchanged
		"Stop":         "Stop",         // claude, unchanged
		"PreCompact":   "PreCompact",   // claude, unchanged
		"pre_tool_use": "pre_tool_use", // grok event vp does not handle: unchanged, then rejected
		"":             "",
	}
	for in, want := range cases {
		if got := canonicalHookEvent(in); got != want {
			t.Errorf("canonicalHookEvent(%q) = %q, want %q", in, got, want)
		}
	}
}
