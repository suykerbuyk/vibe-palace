// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const sessionStartMatcher = "^(startup|resume|clear|compact|fork)$"

// InstallSessionStartHook adds one Vibe-Palace SessionStart hook while
// retaining all other hook events, entries, and unknown JSON fields.
func InstallSessionStartHook(path, vpPath string) error {
	if strings.TrimSpace(vpPath) == "" {
		return fmt.Errorf("resolved vp executable path is required")
	}
	doc, err := readHooks(path)
	if err != nil {
		return err
	}
	hooks := object(doc["hooks"])
	sessionRows := rows(hooks["SessionStart"])
	command := strconv.Quote(vpPath) + " codex hook session-start"
	filtered := make([]any, 0, len(sessionRows)+1)
	for _, raw := range sessionRows {
		entry := object(raw)
		commands := rows(entry["hooks"])
		kept := make([]any, 0, len(commands))
		for _, rawCommand := range commands {
			cmd := object(rawCommand)
			if strings.HasSuffix(stringField(cmd["command"]), " codex hook session-start") {
				continue
			}
			kept = append(kept, rawCommand)
		}
		if len(commands) == 0 || len(kept) > 0 {
			if len(commands) > 0 {
				entry["hooks"] = kept
			}
			filtered = append(filtered, entry)
		}
	}
	filtered = append(filtered, map[string]any{
		"matcher": sessionStartMatcher,
		"hooks": []any{map[string]any{
			"type": "command", "command": command, "additionalContextLimit": AdditionalContextLimit,
		}},
	})
	hooks["SessionStart"] = filtered
	doc["hooks"] = hooks
	return writeHooks(path, doc)
}

// InstallSessionEndHook adds the bounded SessionEnd archive handler while
// preserving all user and unknown hook configuration.
func InstallSessionEndHook(path, vpPath string) error {
	if strings.TrimSpace(vpPath) == "" {
		return fmt.Errorf("resolved vp executable path is required")
	}
	doc, err := readHooks(path)
	if err != nil {
		return err
	}
	hooks := object(doc["hooks"])
	rowsForEvent := rows(hooks["SessionEnd"])
	command := strconv.Quote(vpPath) + " codex hook session-end"
	filtered := make([]any, 0, len(rowsForEvent)+1)
	for _, raw := range rowsForEvent {
		entry := object(raw)
		commands := rows(entry["hooks"])
		kept := make([]any, 0, len(commands))
		for _, rawCommand := range commands {
			cmd := object(rawCommand)
			if strings.HasSuffix(stringField(cmd["command"]), " codex hook session-end") {
				continue
			}
			kept = append(kept, rawCommand)
		}
		if len(commands) == 0 || len(kept) > 0 {
			if len(commands) > 0 {
				entry["hooks"] = kept
			}
			filtered = append(filtered, entry)
		}
	}
	filtered = append(filtered, map[string]any{"matcher": "other", "hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 3}}})
	hooks["SessionEnd"] = filtered
	doc["hooks"] = hooks
	return writeHooks(path, doc)
}

// RemoveSessionStartHook removes only entries whose command names the
// Vibe-Palace SessionStart handler; other hooks and unknown fields survive.
func RemoveSessionStartHook(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat Codex hooks %s: %w", path, err)
	}
	doc, err := readHooks(path)
	if err != nil {
		return err
	}
	hooks := object(doc["hooks"])
	sessionRows := rows(hooks["SessionStart"])
	filtered := make([]any, 0, len(sessionRows))
	for _, raw := range sessionRows {
		entry := object(raw)
		commands := rows(entry["hooks"])
		kept := make([]any, 0, len(commands))
		for _, rawCommand := range commands {
			cmd := object(rawCommand)
			if strings.HasSuffix(stringField(cmd["command"]), " codex hook session-start") {
				continue
			}
			kept = append(kept, rawCommand)
		}
		if len(commands) == 0 || len(kept) > 0 {
			if len(commands) > 0 {
				entry["hooks"] = kept
			}
			filtered = append(filtered, entry)
		}
	}
	if len(filtered) == 0 {
		delete(hooks, "SessionStart")
	} else {
		hooks["SessionStart"] = filtered
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooks
	}
	return writeHooks(path, doc)
}

// RemoveSessionEndHook removes only Vibe-Palace's SessionEnd handler.
func RemoveSessionEndHook(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	doc, err := readHooks(path)
	if err != nil {
		return err
	}
	hooks := object(doc["hooks"])
	filtered := make([]any, 0)
	for _, raw := range rows(hooks["SessionEnd"]) {
		entry := object(raw)
		commands := rows(entry["hooks"])
		kept := make([]any, 0, len(commands))
		for _, rawCommand := range commands {
			cmd := object(rawCommand)
			if strings.HasSuffix(stringField(cmd["command"]), " codex hook session-end") {
				continue
			}
			kept = append(kept, rawCommand)
		}
		if len(commands) == 0 || len(kept) > 0 {
			if len(commands) > 0 {
				entry["hooks"] = kept
			}
			filtered = append(filtered, entry)
		}
	}
	if len(filtered) == 0 {
		delete(hooks, "SessionEnd")
	} else {
		hooks["SessionEnd"] = filtered
	}
	if len(hooks) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooks
	}
	return writeHooks(path, doc)
}

func readHooks(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Codex hooks %s: %w", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse Codex hooks %s: %w", path, err)
	}
	if doc == nil {
		return nil, fmt.Errorf("Codex hooks %s must contain a JSON object", path)
	}
	return doc, nil
}

func writeHooks(path string, doc map[string]any) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, raw) {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read Codex hooks before write %s: %w", path, err)
	}
	if err == nil {
		if err := os.WriteFile(path+".vp.bak", old, 0o600); err != nil {
			return fmt.Errorf("backup Codex hooks %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create Codex hooks directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vp-hooks-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

func object(value any) map[string]any {
	if result, ok := value.(map[string]any); ok && result != nil {
		return result
	}
	return map[string]any{}
}

func rows(value any) []any {
	if result, ok := value.([]any); ok {
		return result
	}
	return nil
}

func stringField(value any) string {
	result, _ := value.(string)
	return result
}
