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

// TestHooksInstallPreservesOtherHooksAndUnknownFields also stands in for the
// coverage this test used to carry by passing "/opt/vp bin/vp": a path with a
// space, which existed to exercise strconv.Quote. The command is now a bare
// "vp" with no path in it, so there is nothing left to quote and that case is
// retired deliberately, not lost.
func TestHooksInstallPreservesOtherHooksAndUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	input := `{"custom_top":{"retained":true},"hooks":{"SessionStart":[{"matcher":"startup","hooks":[{"type":"command","command":"echo custom"}],"unknown":"keep"}],"SessionEnd":[{"hooks":[{"type":"command","command":"echo end"}]}]},"unknown_root":"keep"}`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHooks(path); err != nil {
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
	if _, err := InstallHooks(path); err != nil {
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
	if _, err := InstallHooks(path); err != nil {
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
	if _, err := InstallHooks(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHooks(path); err != nil {
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
	if _, err := InstallHooks(path); err != nil {
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

// TestInstallHooksReportsChangedOnlyOnChange pins the signal the install's
// operator warning is gated on. A repeat install must report false, or the
// warning cries wolf on every run and stops being read.
func TestInstallHooksReportsChangedOnlyOnChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	changed, err := InstallHooks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first install reported changed=false; it created the file")
	}
	changed, err = InstallHooks(path)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("repeat install reported changed=true; writeHooks must short-circuit on byte equality")
	}
}

// TestInstallHooksReplacesLegacyAbsoluteCommandWithoutDuplicating proves the
// absolute-to-bare migration needs no migration code.
//
// Row identity is the command suffix, so the legacy quoted-absolute row is
// replaced rather than left beside the new one. If this ever duplicates, the
// operator gets two hooks per event and Codex runs vp twice per session.
func TestInstallHooksReplacesLegacyAbsoluteCommandWithoutDuplicating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	legacy := `{"hooks":{"SessionStart":[{"matcher":"` + sessionStartMatcher + `","hooks":[{"type":"command","command":"\"/home/someone/.local/bin/vp\" codex hook session-start","additionalContextLimit":1200}]}],"SessionEnd":[{"matcher":"other","hooks":[{"type":"command","command":"\"/home/someone/.local/bin/vp\" codex hook session-end","timeout":3}]}]}}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallHooks(path); err != nil {
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
	for event, suffix := range map[string]string{"SessionStart": sessionStartSuffix, "SessionEnd": sessionEndSuffix} {
		eventRows := hooks[event].([]any)
		if len(eventRows) != 1 {
			t.Fatalf("%s has %d rows, want 1 — the legacy row was not replaced: %s", event, len(eventRows), raw)
		}
		commands := eventRows[0].(map[string]any)["hooks"].([]any)
		if len(commands) != 1 {
			t.Fatalf("%s row has %d commands, want 1: %s", event, len(commands), raw)
		}
		got := commands[0].(map[string]any)["command"].(string)
		if got != vpHookCommand+suffix {
			t.Fatalf("%s command = %q, want %q", event, got, vpHookCommand+suffix)
		}
	}
	if strings.Contains(string(raw), "/home/someone/.local/bin/vp") {
		t.Fatalf("legacy absolute path survived the rewrite: %s", raw)
	}
}

// TestInstallHooksFirstWriteDoesNotBackupAPartialDocument is the atomicity
// lock, and it is the ONLY assertion that distinguishes one write from two.
//
// writeHooks backs up only when the file already exists. So a two-write
// implementation creates hooks.json on its first call with no backup, and on
// its second finds the file it just wrote and copies that half-configured
// document into hooks.json.vp.bak — a "backup" of a state that never existed,
// while the real prior file (none, here) is unrecoverable. One write cannot
// produce that file at all.
//
// 🔴 Do not replace this with a pre-write error case. readHooks and writeHooks
// both fail identically in the one-write and two-write shapes, so any test
// built on them passes against the implementation this one exists to forbid.
func TestInstallHooksFirstWriteDoesNotBackupAPartialDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.json")
	if _, err := InstallHooks(path); err != nil {
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
	for _, event := range []string{"SessionStart", "SessionEnd"} {
		if _, ok := hooks[event]; !ok {
			t.Fatalf("%s missing after a single install: %s", event, raw)
		}
	}
	if _, err := os.Stat(path + ".vp.bak"); !os.IsNotExist(err) {
		backup, _ := os.ReadFile(path + ".vp.bak")
		t.Fatalf("a first install into an empty directory wrote %s.vp.bak (err=%v), so the two hook writes were not collapsed into one; it contains: %s", path, err, backup)
	}
}

// TestInstallHooksRefusesMalformedHooksFile is ordinary error-path coverage for
// readHooks, which the package otherwise has none for.
//
// 🔴 It is NOT the atomicity lock and must never be cited as one: readHooks
// runs before both event mutations, so this passes in the one-write and
// two-write shapes alike. TestInstallHooksFirstWriteDoesNotBackupAPartialDocument
// is the lock.
func TestInstallHooksRefusesMalformedHooksFile(t *testing.T) {
	for name, content := range map[string]string{
		"invalid json":       "{not json",
		"json null":          "null",
		"json not an object": "[]",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hooks.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			changed, err := InstallHooks(path)
			if err == nil {
				t.Fatal("InstallHooks accepted a malformed hooks file")
			}
			if changed {
				t.Fatal("InstallHooks reported changed=true on a refusal")
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != content {
				t.Fatalf("refused install still rewrote the file: %s", after)
			}
		})
	}
}

// TestInstallHooksReportsChangedOnAFormattingOnlyRewrite records that the
// changed bool tracks BYTES, not trust.
//
// writeHooks compares bytes, and Codex hashes the parsed hook, so these two
// notions diverge: a semantics-preserving reformat reports changed=true while
// Codex's trust is unaffected. That is the safe direction for a no-op guard —
// it never stays silent about a real rewrite — but it is why the caller's
// notice must say the hooks MAY need trusting rather than asserting they
// stopped. See mcphost.codexHookTrustAdvisory.
func TestInstallHooksReportsChangedOnAFormattingOnlyRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if _, err := InstallHooks(path); err != nil {
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
	compact, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if string(compact) == string(raw) {
		t.Fatal("compact re-serialisation matched the indented form; this test cannot distinguish anything")
	}
	if err := os.WriteFile(path, compact, 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := InstallHooks(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("a formatting-only difference reported changed=false; the no-op guard would then hide a real rewrite")
	}
}
