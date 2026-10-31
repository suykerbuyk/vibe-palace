// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package codexhook implements the local Codex hook protocol. Hooks emit an
// instruction only; MCP remains the source of project context and tools.
package codexhook

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/project"
)

const AdditionalContextLimit = 1200

type SessionStartEvent struct {
	CWD           string `json:"cwd"`
	HookEventName string `json:"hook_event_name"`
}

type Output struct {
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// SessionStartContext returns no instruction when the current directory has
// no Vibe-Palace marker. It refuses malformed or unnamed markers rather than
// inferring a slug from the checkout directory name.
func SessionStartContext(cwd string) (string, bool, error) {
	if strings.TrimSpace(cwd) == "" {
		return "", true, fmt.Errorf("Codex SessionStart event has no cwd")
	}
	if project.DetectSignal(cwd) != project.SignalVibeConfig {
		return "", false, nil
	}
	marker, err := project.LocateMarker(cwd)
	if err != nil {
		return "", true, fmt.Errorf("read Vibe-Palace project marker: %w", err)
	}
	if marker.Path == "" {
		return "", true, fmt.Errorf("Vibe-Palace marker disappeared while resolving %s", cwd)
	}
	if marker.Name == "" {
		return "", true, fmt.Errorf("Vibe-Palace marker %s has no [project].name", marker.Path)
	}
	if marker.NameErr != nil {
		return "", true, fmt.Errorf("Vibe-Palace marker %s has invalid project name %q: %w", marker.Path, marker.Name, marker.NameErr)
	}
	context := fmt.Sprintf("Before your first response, call vp_bootstrap_context with {\"project\":\"%s\"}; wait for its result before answering.", marker.Name)
	if len(context) > AdditionalContextLimit {
		return "", true, fmt.Errorf("bootstrap instruction is %d bytes, limit is %d", len(context), AdditionalContextLimit)
	}
	return context, true, nil
}

// HandleSessionStart reads Codex's event JSON and writes its hook response.
// Errors are returned for stderr reporting by the CLI, while callers should
// still emit an empty JSON object so hook failures do not block a session.
func HandleSessionStart(in io.Reader, out io.Writer) error {
	var event SessionStartEvent
	if err := json.NewDecoder(in).Decode(&event); err != nil {
		_ = json.NewEncoder(out).Encode(Output{})
		return fmt.Errorf("decode Codex SessionStart event: %w", err)
	}
	if event.HookEventName != "" && event.HookEventName != "SessionStart" {
		_ = json.NewEncoder(out).Encode(Output{})
		return fmt.Errorf("unexpected Codex hook event %q", event.HookEventName)
	}
	context, found, err := SessionStartContext(event.CWD)
	if err != nil || !found {
		if encodeErr := json.NewEncoder(out).Encode(Output{}); err == nil {
			err = encodeErr
		}
		return err
	}
	response := Output{HookSpecificOutput: &HookSpecificOutput{
		HookEventName:     "SessionStart",
		AdditionalContext: context,
	}}
	return json.NewEncoder(out).Encode(response)
}
