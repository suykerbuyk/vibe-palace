// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const sessionStartMatcher = "^(startup|resume|clear|compact|fork)$"

// vpHookCommand is the executable name Codex runs for both Vibe-Palace hooks.
//
// It is written BARE rather than as a resolved absolute path, because Codex
// keys hook trust to the hook's command: a path that moves — a different
// PREFIX, a PATH reorder, another machine — rewrites that string and silently
// untrusts the hook, with no diagnostic from either side. A bare name is
// host- and install-independent, and matches what the Codex MCP server entry
// already uses. It is UNQUOTED because a bare name needs no quoting and the
// unquoted form is the one measured to execute under Codex.
//
// internal/mcphost.CodexHost pins, before writing anything, that this name
// resolves to the executable performing the install; nothing here re-checks it.
const vpHookCommand = "vp"

// sessionStartSuffix and sessionEndSuffix identify a Vibe-Palace hook row.
//
// Identity is the command's SUFFIX, deliberately: it holds for the legacy
// quoted-absolute command, for the bare one, and for any path a past vp wrote,
// so an install replaces whichever form it finds instead of appending a second
// row beside it. RemoveSessionStartHook and RemoveSessionEndHook spell the same
// two strings inline; they are out of scope for this change.
const (
	sessionStartSuffix = " codex hook session-start"
	sessionEndSuffix   = " codex hook session-end"
)

// InstallHooks writes both Vibe-Palace Codex hooks in ONE update, retaining
// every other hook event, entry, and unknown JSON field.
//
// Both events are applied to a single in-memory document and committed with a
// single writeHooks call, which renames a temp file into place. Installing them
// as two sequential writes — which this package used to do — makes two
// observable mistakes. A failure of the second leaves the first's rewritten
// command on disk, so that hook's trust is invalidated and no caller is in a
// position to say so. And the second write backs up the FIRST write's output,
// so a first install leaves a .vp.bak holding a half-configured document that
// never existed, while the real prior file is gone. One write has neither
// failure mode, and TestInstallHooksFirstWriteDoesNotBackupAPartialDocument
// fails if the single write is ever split again.
//
// changed reports whether the file was actually rewritten, so a caller can warn
// about hook trust only when the string Codex keys on really moved.
func InstallHooks(path string) (changed bool, err error) {
	doc, err := readHooks(path)
	if err != nil {
		return false, err
	}
	hooks := object(doc["hooks"])
	hooks["SessionStart"] = sessionStartRows(hooks["SessionStart"])
	hooks["SessionEnd"] = sessionEndRows(hooks["SessionEnd"])
	doc["hooks"] = hooks
	return writeHooks(path, doc)
}

// sessionStartRows returns the SessionStart rows with Vibe-Palace's handler
// replaced. Pure: it mutates nothing on disk and cannot fail.
func sessionStartRows(existing any) []any {
	return replaceOwnedRows(existing, sessionStartSuffix, map[string]any{
		"matcher": sessionStartMatcher,
		"hooks": []any{map[string]any{
			"type": "command", "command": vpHookCommand + sessionStartSuffix, "additionalContextLimit": AdditionalContextLimit,
		}},
	})
}

// sessionEndRows returns the SessionEnd rows with Vibe-Palace's handler
// replaced. Pure: it mutates nothing on disk and cannot fail.
func sessionEndRows(existing any) []any {
	return replaceOwnedRows(existing, sessionEndSuffix, map[string]any{
		"matcher": "other",
		"hooks":   []any{map[string]any{"type": "command", "command": vpHookCommand + sessionEndSuffix, "timeout": 3}},
	})
}

// replaceOwnedRows drops every command row whose command ends in suffix, keeps
// every foreign row and unknown field, and appends owned as Vibe-Palace's row.
//
// Row identity is the command SUFFIX, never a path. That is what lets the
// absolute-to-bare rewrite — and its rollback — replace the previous row
// instead of duplicating it, with no migration code anywhere.
func replaceOwnedRows(existing any, suffix string, owned map[string]any) []any {
	existingRows := rows(existing)
	filtered := make([]any, 0, len(existingRows)+1)
	for _, raw := range existingRows {
		entry := object(raw)
		commands := rows(entry["hooks"])
		kept := make([]any, 0, len(commands))
		for _, rawCommand := range commands {
			cmd := object(rawCommand)
			if strings.HasSuffix(stringField(cmd["command"]), suffix) {
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
	return append(filtered, owned)
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
	_, err = writeHooks(path, doc)
	return err
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
	_, err = writeHooks(path, doc)
	return err
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

// writeHooks commits doc atomically and reports whether the file changed.
//
// A byte-identical document is a no-op: no backup, no write, changed=false.
// That is what keeps a repeat install from churning the file, and what lets
// the caller stay silent instead of crying wolf about hook trust.
func writeHooks(path string, doc map[string]any) (bool, error) {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}
	raw = append(raw, '\n')
	old, err := os.ReadFile(path)
	if err == nil && bytes.Equal(old, raw) {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read Codex hooks before write %s: %w", path, err)
	}
	if err == nil {
		if err := os.WriteFile(path+".vp.bak", old, 0o600); err != nil {
			return false, fmt.Errorf("backup Codex hooks %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create Codex hooks directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".vp-hooks-*")
	if err != nil {
		return false, err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return false, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return false, err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return false, err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return false, err
	}
	return true, nil
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
