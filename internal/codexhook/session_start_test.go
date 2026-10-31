// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionStartInjectsExplicitMarkerSlug(t *testing.T) {
	cwd := markerFixture(t, "codex-workspace")
	context, found, err := SessionStartContext(cwd)
	if err != nil || !found {
		t.Fatalf("context found=%v err=%v", found, err)
	}
	if !strings.Contains(context, `"project":"codex-workspace"`) || !strings.Contains(context, "vp_bootstrap_context") {
		t.Fatalf("bootstrap context does not carry explicit slug: %q", context)
	}
	var output bytes.Buffer
	if err := HandleSessionStart(strings.NewReader(`{"cwd":"`+cwd+`","hook_event_name":"SessionStart"}`), &output); err != nil {
		t.Fatal(err)
	}
	var response Output
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.HookSpecificOutput == nil || response.HookSpecificOutput.HookEventName != "SessionStart" || response.HookSpecificOutput.AdditionalContext != context {
		t.Fatalf("hook response = %#v", response)
	}
}

func TestSessionStartNoOpsWithoutVibeSignal(t *testing.T) {
	cwd := t.TempDir()
	context, found, err := SessionStartContext(cwd)
	if err != nil || found || context != "" {
		t.Fatalf("no-marker result context=%q found=%v err=%v", context, found, err)
	}
	var output bytes.Buffer
	if err := HandleSessionStart(strings.NewReader(`{"cwd":"`+cwd+`"}`), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "additionalContext") {
		t.Fatalf("no-op hook emitted bootstrap context: %s", output.String())
	}
}

func TestSessionStartReportsUnreadableOrInvalidMarker(t *testing.T) {
	t.Run("malformed marker", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"), []byte("[project]\nname = [\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, found, err := SessionStartContext(cwd); !found || err == nil {
			t.Fatalf("malformed marker found=%v err=%v", found, err)
		}
	})
	t.Run("invalid slug", func(t *testing.T) {
		cwd := markerFixture(t, "../escape")
		if _, found, err := SessionStartContext(cwd); !found || err == nil {
			t.Fatalf("invalid marker found=%v err=%v", found, err)
		}
	})
	t.Run("missing cwd", func(t *testing.T) {
		if _, found, err := SessionStartContext(""); !found || err == nil {
			t.Fatalf("missing cwd found=%v err=%v", found, err)
		}
	})
}

func TestSessionStartOutputStaysBelow1200Tokens(t *testing.T) {
	context, found, err := SessionStartContext(markerFixture(t, "codex-workspace"))
	if err != nil || !found {
		t.Fatalf("context found=%v err=%v", found, err)
	}
	// The rendered instruction is ASCII; its byte count is a conservative
	// upper bound on tokenizer pieces and must fit the configured limit.
	if len(context) > AdditionalContextLimit {
		t.Fatalf("SessionStart instruction is %d bytes, limit %d", len(context), AdditionalContextLimit)
	}
}

func markerFixture(t *testing.T, slug string) string {
	t.Helper()
	cwd := t.TempDir()
	contents := "[project]\nname = " + strconvQuote(slug) + "\n"
	if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
