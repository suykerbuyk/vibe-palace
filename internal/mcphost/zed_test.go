// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tailscale/hujson"
)

// newTestZedHost returns a ZedHost pointed at a settings.json inside dir, with
// `zed` detection forced off so tests do not depend on the host environment.
func newTestZedHost(dir string) *ZedHost {
	return &ZedHost{
		settingsPath: filepath.Join(dir, "zed", "settings.json"),
		lookPath:     func() (string, error) { return "", os.ErrNotExist },
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// parseSettings reads a JWCC settings file (Format() emits trailing commas that
// encoding/json rejects), standardizes it to plain JSON, and unmarshals the
// context_servers map for shape assertions.
func parseSettings(t *testing.T, path string) map[string]map[string]map[string]any {
	t.Helper()
	std, err := hujson.Standardize([]byte(readFile(t, path)))
	if err != nil {
		t.Fatalf("standardize %s: %v", path, err)
	}
	var parsed map[string]map[string]map[string]any
	if err := json.Unmarshal(std, &parsed); err != nil {
		t.Fatalf("unmarshal %s: %v\n%s", path, err, std)
	}
	return parsed
}

func TestZedInstall_FreshFile(t *testing.T) {
	dir := t.TempDir()
	h := newTestZedHost(dir)

	if err := h.Install("v1", dir, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	installed, err := h.Installed()
	if err != nil || !installed {
		t.Fatalf("expected installed after fresh install, got installed=%v err=%v", installed, err)
	}

	// Entry shape: command vp, args [mcp], no env block.
	entry := parseSettings(t, h.settingsPath)["context_servers"]["vibe-palace"]
	if entry["command"] != "vp" {
		t.Errorf("command = %v, want vp", entry["command"])
	}
	if _, hasEnv := entry["env"]; hasEnv {
		t.Errorf("entry should omit env block, got %v", entry)
	}

	// AGENTS.md created + wired.
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Errorf("AGENTS.md not created: %v", err)
	}
}

func TestZedInstall_PreservesCommentsAndOtherKeys(t *testing.T) {
	dir := t.TempDir()
	h := newTestZedHost(dir)
	if err := os.MkdirAll(filepath.Dir(h.settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{
  // operator's font preference — must survive
  "buffer_font_size": 12,
  "theme": "Andromeda", // inline comment
}
`
	if err := os.WriteFile(h.settingsPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := h.Install("v1", dir, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	got := readFile(t, h.settingsPath)
	// Comments + keys survive. Avoid exact spacing: Format() aligns values.
	for _, want := range []string{
		"operator's font preference — must survive",
		"inline comment",
		"buffer_font_size",
		"Andromeda",
		"vibe-palace",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("result missing %q:\n%s", want, got)
		}
	}
	// Result is still valid JWCC (parses after standardization).
	if _, err := hujson.Standardize([]byte(got)); err != nil {
		t.Errorf("result is not valid JWCC: %v", err)
	}

	// .vp.bak captured the pre-edit content.
	bak := readFile(t, h.settingsPath+".vp.bak")
	if bak != original {
		t.Errorf("backup mismatch:\n--- got ---\n%s\n--- want ---\n%s", bak, original)
	}
}

func TestZedInstall_Idempotent(t *testing.T) {
	dir := t.TempDir()
	h := newTestZedHost(dir)

	if err := h.Install("v1", dir, nil); err != nil {
		t.Fatalf("first install: %v", err)
	}
	first := readFile(t, h.settingsPath)
	// Remove the backup so we can detect whether the second run rewrote the file.
	_ = os.Remove(h.settingsPath + ".vp.bak")

	if err := h.Install("v1", dir, nil); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if second := readFile(t, h.settingsPath); second != first {
		t.Errorf("idempotent install rewrote file:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	if _, err := os.Stat(h.settingsPath + ".vp.bak"); err == nil {
		t.Errorf("idempotent install made a backup — it should have skipped the write")
	}
}

func TestZedInstall_ExistingContextServersPreserved(t *testing.T) {
	dir := t.TempDir()
	h := newTestZedHost(dir)
	if err := os.MkdirAll(filepath.Dir(h.settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "context_servers": {
    "some-other-server": { "command": "other", "args": ["go"] }
  }
}
`
	if err := os.WriteFile(h.settingsPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := h.Install("v1", dir, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	got := readFile(t, h.settingsPath)
	if !strings.Contains(got, "some-other-server") {
		t.Errorf("existing context server dropped:\n%s", got)
	}
	if !strings.Contains(got, "vibe-palace") {
		t.Errorf("vibe-palace not added:\n%s", got)
	}
}

func TestZedInstall_UpdatesDifferingEntry(t *testing.T) {
	dir := t.TempDir()
	h := newTestZedHost(dir)
	if err := os.MkdirAll(filepath.Dir(h.settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// A stale entry pointing at the wrong command must be corrected.
	original := `{
  "context_servers": {
    "vibe-palace": { "command": "OLD-BINARY", "args": ["serve"] }
  }
}
`
	if err := os.WriteFile(h.settingsPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := h.Install("v1", dir, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	got := readFile(t, h.settingsPath)
	if strings.Contains(got, "OLD-BINARY") {
		t.Errorf("stale command not replaced:\n%s", got)
	}
	if cmd := parseSettings(t, h.settingsPath)["context_servers"]["vibe-palace"]["command"]; cmd != "vp" {
		t.Errorf("entry not updated to vp: command=%v\n%s", cmd, got)
	}
	// A differing entry IS a change, so a backup must exist.
	if _, err := os.Stat(h.settingsPath + ".vp.bak"); err != nil {
		t.Errorf("expected backup when updating a differing entry: %v", err)
	}
}

func TestZedDetected(t *testing.T) {
	dir := t.TempDir()
	// Config dir absent, zed not on PATH → not detected.
	h := &ZedHost{
		settingsPath: filepath.Join(dir, "zed", "settings.json"),
		lookPath:     func() (string, error) { return "", os.ErrNotExist },
	}
	if h.Detected() {
		t.Error("should not be detected with no config dir and no zed on PATH")
	}

	// Config dir present → detected.
	if err := os.MkdirAll(filepath.Dir(h.settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if !h.Detected() {
		t.Error("should be detected once config dir exists")
	}

	// zed on PATH (no config dir) → detected.
	h2 := &ZedHost{
		settingsPath: filepath.Join(t.TempDir(), "zed", "settings.json"),
		lookPath:     func() (string, error) { return "/usr/bin/zed", nil },
	}
	if !h2.Detected() {
		t.Error("should be detected when zed is on PATH")
	}
}

func TestZedUninstall(t *testing.T) {
	dir := t.TempDir()
	h := newTestZedHost(dir)
	if err := h.Install("v1", dir, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	if err := h.Uninstall(nil); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	installed, err := h.Installed()
	if err != nil {
		t.Fatalf("installed: %v", err)
	}
	if installed {
		t.Errorf("still installed after uninstall:\n%s", readFile(t, h.settingsPath))
	}
}

func TestZedUninstall_NotConfigured(t *testing.T) {
	dir := t.TempDir()
	h := newTestZedHost(dir)
	// No settings file at all — must be a graceful no-op.
	if err := h.Uninstall(nil); err != nil {
		t.Fatalf("uninstall on absent file: %v", err)
	}
}
