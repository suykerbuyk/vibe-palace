// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hostConfig points XDG_CONFIG_HOME at a fresh directory for this test and, when
// body is non-nil, writes it as the host config. It returns the config path.
func hostConfig(t *testing.T, body *string) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	path := filepath.Join(xdg, "vibe-palace", "config.toml")
	if body != nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(*body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func str(s string) *string { return &s }

// TestHostGitEnabledReadsTheSetting covers every case that yields a value:
// missing file and absent key mean enabled; a nested git_enabled is not the
// top-level key; an unrelated wrong-typed key no longer blocks the read.
func TestHostGitEnabledReadsTheSetting(t *testing.T) {
	cases := []struct {
		name string
		body *string
		want bool
	}{
		{"missing file (Lstat ENOENT)", nil, true},
		{"absent key", str("log_level = \"info\"\n"), true},
		{"nested under a table", str("[palace]\ngit_enabled = false\n"), true},
		{"explicit false", str("git_enabled = false\n"), false},
		{"explicit true", str("git_enabled = true\n"), true},
		{"unrelated wrong-typed key beside a valid value", str("git_enabled = false\nhttp_port = \"abc\"\n"), false},
		{"current [meta]", str("git_enabled = false\n[meta]\nversion_major = 1\nversion_minor = 1\n"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hostConfig(t, tc.body)
			got, err := HostGitEnabled()
			if err != nil {
				t.Fatalf("HostGitEnabled: %v", err)
			}
			if got != tc.want {
				t.Errorf("HostGitEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// assertUnreadable pins the UNREADABLE verdict: an ErrGitConfigUnreadable that
// is never mistaken for, and never wraps, ErrGitDisabled.
func assertUnreadable(t *testing.T) {
	t.Helper()
	got, err := HostGitEnabled()
	if err == nil {
		t.Fatalf("HostGitEnabled = %v, nil; want ErrGitConfigUnreadable", got)
	}
	if !errors.Is(err, ErrGitConfigUnreadable) {
		t.Errorf("error %v does not wrap ErrGitConfigUnreadable", err)
	}
	if errors.Is(err, ErrGitDisabled) {
		t.Errorf("a read failure must never read as disabled: %v", err)
	}
	if !strings.Contains(err.Error(), "cannot read git_enabled") {
		t.Errorf("error %q lacks the pinned substring", err)
	}
}

// TestHostGitEnabledFailsClosedOnAnUnreadableConfig covers every case that
// must refuse vault git without claiming the operator disabled it.
func TestHostGitEnabledFailsClosedOnAnUnreadableConfig(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"wrong-typed git_enabled", "git_enabled = \"no\"\n"},
		{"lone case variant", "GIT_ENABLED = false\n"},
		{"TOML syntax error elsewhere", "git_enabled = true\nlog_level = \n"},
		{"config from a newer vp", "git_enabled = true\n[meta]\nversion_major = 2\nversion_minor = 0\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hostConfig(t, str(tc.body))
			assertUnreadable(t)
		})
	}
}

// TestHostGitEnabledIsDeterministicWithBothSpellings: a struct decode of
// git_enabled = true beside GIT_ENABLED = false splits on map iteration order.
// The verdict must be UNREADABLE on every one of 500 reads.
func TestHostGitEnabledIsDeterministicWithBothSpellings(t *testing.T) {
	hostConfig(t, str("git_enabled = true\nGIT_ENABLED = false\n"))
	for i := range 500 {
		got, err := HostGitEnabled()
		if !errors.Is(err, ErrGitConfigUnreadable) {
			t.Fatalf("read %d: HostGitEnabled = %v, %v; want ErrGitConfigUnreadable every time", i, got, err)
		}
	}
}

// TestHostGitEnabledTreatsADanglingSymlinkAsUnreadable: os.Stat calls a
// dangling symlink missing (and so enabled); os.Lstat finds the link.
func TestHostGitEnabledTreatsADanglingSymlinkAsUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on windows")
	}
	path := hostConfig(t, nil)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "unmounted", "config.toml"), path); err != nil {
		t.Fatal(err)
	}
	assertUnreadable(t)
}

// TestHostGitEnabledTreatsAnUnreadableConfigDirAsUnreadable: LoadConfig reads a
// mode-000 config directory as the embedded git_enabled = true, with a nil
// error. The reader must not.
func TestHostGitEnabledTreatsAnUnreadableConfigDirAsUnreadable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("mode 000 does not deny access here")
	}
	path := hostConfig(t, str("git_enabled = true\n"))
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	assertUnreadable(t)
}

// TestHostGitEnabledTreatsAnUnresolvablePathAsUnreadable: with neither HOME
// nor XDG_CONFIG_HOME there is no host config path at all. That host now
// refuses vault git, where LoadConfig fell back to enabled.
func TestHostGitEnabledTreatsAnUnresolvablePathAsUnreadable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("os.UserConfigDir consults other variables off linux")
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	assertUnreadable(t)
}
