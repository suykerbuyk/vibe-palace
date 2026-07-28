// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGrok builds a GrokHost with a captured runner. calls accumulates each
// argv the host would have executed.
func fakeGrok(present bool, out string, runErr error) (*GrokHost, *[][]string) {
	calls := &[][]string{}
	h := &GrokHost{
		runner: func(args ...string) ([]byte, error) {
			*calls = append(*calls, append([]string(nil), args...))
			return []byte(out), runErr
		},
		lookPath: func() (string, error) {
			if present {
				return "/usr/bin/grok", nil
			}
			return "", os.ErrNotExist
		},
		homeDir: func() (string, error) { return "/nonexistent-home", nil },
	}
	return h, calls
}

func TestGrokInstall_ArgvConstruction(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	dir := t.TempDir()
	h, calls := fakeGrok(true, "", nil)
	h.homeDir = func() (string, error) { return home, nil }

	if err := h.Install("v1", dir, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 grok call, got %d: %v", len(*calls), *calls)
	}
	got := strings.Join((*calls)[0], " ")
	for _, want := range []string{
		"mcp add vibe-palace",
		"--command vp",
		"--args mcp",
		"--env XAI_API_KEY=${XAI_API_KEY}",
		"--env ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("argv missing %q; got: %s", want, got)
		}
	}

	// Env values stay literal placeholders — no secret baked in.
	if strings.Contains(got, "=${") == false {
		t.Errorf("env values should be deferred ${VAR} placeholders; got: %s", got)
	}

	// AGENTS.md ensured.
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); err != nil {
		t.Errorf("AGENTS.md not created: %v", err)
	}
}

func TestGrokInstall_EmitsUserPluginSurfaces(t *testing.T) {
	// H4: host wiring, not just InstallGlobalSurfaces in isolation.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	dir := t.TempDir()
	h, _ := fakeGrok(true, "", nil)
	h.homeDir = func() (string, error) { return home, nil }

	var buf strings.Builder
	if err := h.Install("v1", dir, &buf); err != nil {
		t.Fatalf("install: %v", err)
	}
	cmdDir := filepath.Join(home, ".grok", "plugins", "vibe-palace", "commands")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		t.Fatalf("user plugin commands missing: %v\nout=%s", err, buf.String())
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vpc-") && strings.HasSuffix(e.Name(), ".md") {
			n++
		}
	}
	if n == 0 {
		t.Fatalf("expected vpc-*.md under %s; out=%s", cmdDir, buf.String())
	}
	hub := filepath.Join(home, ".grok", "plugins", "vibe-palace", "skills", "vpc", "SKILL.md")
	if _, err := os.Stat(hub); err != nil {
		t.Errorf("hub missing: %v", err)
	}
}

func TestGrokUninstall_RemovesUserPluginTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	pluginDir := filepath.Join(home, ".grok", "plugins", "vibe-palace", "commands")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "vpc-restart.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, _ := fakeGrok(true, "", nil)
	h.homeDir = func() (string, error) { return home, nil }
	if err := h.Uninstall(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".grok", "plugins", "vibe-palace")); !os.IsNotExist(err) {
		t.Errorf("user plugin tree should be removed; err=%v", err)
	}
}

func TestGrokInstall_NotOnPath(t *testing.T) {
	dir := t.TempDir()
	h, calls := fakeGrok(false, "", nil)

	err := h.Install("v1", dir, nil)
	if err == nil {
		t.Fatal("expected error when grok is absent")
	}
	if !strings.Contains(err.Error(), "grok") {
		t.Errorf("error should mention grok: %v", err)
	}
	if len(*calls) != 0 {
		t.Errorf("should not have run grok when absent: %v", *calls)
	}
}

func TestGrokInstall_RunnerError(t *testing.T) {
	dir := t.TempDir()
	h, _ := fakeGrok(true, "boom", errors.New("exit 1"))

	if err := h.Install("v1", dir, nil); err == nil {
		t.Fatal("expected install error to propagate runner failure")
	}
}

func TestGrokInstalled(t *testing.T) {
	h, _ := fakeGrok(true, "Configured MCP servers:\n  vibe-palace (stdio)\n", nil)
	ok, err := h.Installed()
	if err != nil || !ok {
		t.Fatalf("expected installed=true, got %v err=%v", ok, err)
	}

	h2, _ := fakeGrok(true, "No MCP servers configured\n", nil)
	ok2, _ := h2.Installed()
	if ok2 {
		t.Errorf("expected installed=false when list lacks vibe-palace")
	}

	h3, _ := fakeGrok(false, "", nil)
	ok3, err3 := h3.Installed()
	if ok3 || err3 != nil {
		t.Errorf("absent grok should report (false, nil), got (%v, %v)", ok3, err3)
	}
}

func TestGrokDetected(t *testing.T) {
	onPath, _ := fakeGrok(true, "", nil)
	if !onPath.Detected() {
		t.Error("grok on PATH should be detected")
	}

	// Not on PATH, but ~/.grok exists.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".grok"), 0o755); err != nil {
		t.Fatal(err)
	}
	viaDir := &GrokHost{
		runner:   func(...string) ([]byte, error) { return nil, nil },
		lookPath: func() (string, error) { return "", os.ErrNotExist },
		homeDir:  func() (string, error) { return home, nil },
	}
	if !viaDir.Detected() {
		t.Error("~/.grok present should be detected")
	}

	absent := &GrokHost{
		runner:   func(...string) ([]byte, error) { return nil, nil },
		lookPath: func() (string, error) { return "", os.ErrNotExist },
		homeDir:  func() (string, error) { return t.TempDir(), nil },
	}
	if absent.Detected() {
		t.Error("no grok anywhere should not be detected")
	}
}

func TestGrokUninstall(t *testing.T) {
	h, calls := fakeGrok(true, "", nil)
	if err := h.Uninstall(nil); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if len(*calls) != 1 || strings.Join((*calls)[0], " ") != "mcp remove vibe-palace" {
		t.Errorf("expected `mcp remove vibe-palace`, got %v", *calls)
	}

	// Absent grok: graceful no-op, no call.
	h2, calls2 := fakeGrok(false, "", nil)
	if err := h2.Uninstall(nil); err != nil {
		t.Fatalf("uninstall absent: %v", err)
	}
	if len(*calls2) != 0 {
		t.Errorf("should not run grok when absent: %v", *calls2)
	}
}
