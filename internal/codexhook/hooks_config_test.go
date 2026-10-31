// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHooksInstallPreservesOtherHooksAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	input := `{"custom_top":{"retained":true},"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo custom"}],"unknown":"keep"}],"SessionEnd":[{"hooks":[{"type":"command","command":"echo end"}]}]},"unknown_root":"keep"}`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallSessionStartHook(path, "/opt/vp bin/vp"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["unknown_root"] != "keep" || doc["custom_top"].(map[string]any)["retained"] != true {
		t.Fatalf("unknown root fields were lost: %s", raw)
	}
	hooks := doc["hooks"].(map[string]any)
	rows := hooks["SessionStart"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["unknown"] != "keep" {
		t.Fatalf("custom SessionStart hook row lost: %s", raw)
	}
	if _, ok := hooks["SessionEnd"]; !ok {
		t.Fatalf("unrelated hook event was lost: %s", raw)
	}
	if !strings.Contains(string(raw), sessionStartMatcher) {
		t.Fatalf("Codex hook registration missing matcher: %s", raw)
	}
	commandHook := rows[1].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if commandHook["additionalContextLimit"] != float64(AdditionalContextLimit) {
		t.Fatalf("SessionStart additionalContextLimit=%v, want %d: %s", commandHook["additionalContextLimit"], AdditionalContextLimit, raw)
	}
	if commandHook["timeout"] == float64(3) {
		t.Fatalf("SessionStart must use Codex's default timeout, not the SessionEnd 3s limit: %s", raw)
	}
}

func TestHooksInstallSessionEndUsesOtherMatcherAndThreeSecondTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := InstallSessionEndHook(path, "/usr/local/bin/vp"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	hooks := doc["hooks"].(map[string]any)
	rows := hooks["SessionEnd"].([]any)
	if len(rows) != 1 {
		t.Fatalf("SessionEnd rows=%v", rows)
	}
	row := rows[0].(map[string]any)
	if row["matcher"] != "other" {
		t.Fatalf("matcher=%v", row["matcher"])
	}
	command := row["hooks"].([]any)[0].(map[string]any)
	if command["timeout"] != float64(3) || !strings.Contains(command["command"].(string), "codex hook session-end") {
		t.Fatalf("command hook=%v", command)
	}
	if err := InstallSessionEndHook(path, "/usr/local/bin/vp"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(second) != string(raw) {
		t.Fatal("SessionEnd hook install is not byte-idempotent")
	}
	if err := RemoveSessionEndHook(path); err != nil {
		t.Fatal(err)
	}
	final, _ := os.ReadFile(path)
	if strings.Contains(string(final), "codex hook session-end") {
		t.Fatalf("SessionEnd hook remained after removal: %s", final)
	}
}

func TestHooksInstallIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := InstallSessionStartHook(path, "/usr/local/bin/vp"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallSessionStartHook(path, "/usr/local/bin/vp"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("repeat install changed hooks bytes:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestHooksUninstallPreservesCustomRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := InstallSessionStartHook(path, "/usr/local/bin/vp"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	hooks := doc["hooks"].(map[string]any)
	rows := hooks["SessionStart"].([]any)
	rows = append(rows, map[string]any{"matcher": "startup", "hooks": []any{map[string]any{"type": "command", "command": "echo custom"}}})
	hooks["SessionStart"] = rows
	data, _ = json.Marshal(doc)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSessionStartHook(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "codex hook session-start") || !strings.Contains(string(after), "echo custom") {
		t.Fatalf("uninstall did not remove only VP hook: %s", after)
	}
}
