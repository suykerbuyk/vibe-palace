// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestNewGrokHostExecsGrokOnPATH covers NewGrokHost's real exec wiring — the
// runner and lookPath closures that every other GrokHost test replaces via
// fakeGrok — against a scripted `grok` in a temp dir. PATH is set to that dir
// ONLY and HOME to an empty temp dir, so the real Grok CLI and the developer's
// ~/.grok are unreachable. The scripts use shell builtins only, because PATH
// holds nothing else.
func TestNewGrokHostExecsGrokOnPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the scripted grok is an sh script")
	}
	cases := []struct {
		name          string
		script        string // "" installs no grok at all
		wantDetected  bool
		wantInstalled bool
		wantLog       string
	}{
		{
			name: "list names the server",
			script: `echo "$*" >> "$LOG"
case "$*" in "mcp list") echo "vibe-palace  vp mcp";; *) exit 1;; esac`,
			wantDetected:  true,
			wantInstalled: true,
			wantLog:       "mcp list\n",
		},
		{
			name: "list exits 1",
			script: `echo "$*" >> "$LOG"
exit 1`,
			wantDetected:  true,
			wantInstalled: false,
			wantLog:       "mcp list\n",
		},
		{
			name:          "no grok and no ~/.grok",
			wantDetected:  false,
			wantInstalled: false,
			wantLog:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "grok.log")
			if tc.script != "" {
				if err := os.WriteFile(filepath.Join(bin, grokBinary), []byte("#!/bin/sh\n"+tc.script+"\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin)
			t.Setenv("HOME", t.TempDir())
			t.Setenv("LOG", logPath)

			h := NewGrokHost()
			if got := h.Detected(); got != tc.wantDetected {
				t.Errorf("Detected() = %v, want %v", got, tc.wantDetected)
			}
			installed, err := h.Installed()
			if err != nil || installed != tc.wantInstalled {
				t.Errorf("Installed() = (%v, %v), want (%v, nil)", installed, err, tc.wantInstalled)
			}
			raw, err := os.ReadFile(logPath)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if string(raw) != tc.wantLog {
				t.Errorf("scripted grok saw argv log %q, want %q", raw, tc.wantLog)
			}
		})
	}
}
