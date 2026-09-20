// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// hostGitConfig points XDG_CONFIG_HOME at a fresh directory holding body as
// the host config, and returns the config path.
func hostGitConfig(t *testing.T, body string) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	p := filepath.Join(xdg, "vibe-palace", "config.toml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// noGitOnPATH replaces PATH with a directory holding only a `git` that records
// its arguments in the returned marker and fails, and restores the real PATH
// through the returned func. Any git spawn lands in the marker.
func noGitOnPATH(t *testing.T) (marker string, restore func()) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git stub is a shell script")
	}
	bin := t.TempDir()
	marker = filepath.Join(t.TempDir(), "git-was-spawned")
	script := "#!/bin/sh\necho \"$@\" >> '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	real := os.Getenv("PATH")
	t.Setenv("PATH", bin)
	return marker, func() { t.Setenv("PATH", real) }
}

// TestTaskWriteSkipsTheCommitWhenGitIsDisabled is acceptance item 4 for the
// task write: git_enabled = false means "do not run git", not "do not write a
// task". The write lands, HEAD does not move, the receipt says skipped, and
// no git process runs at all.
func TestTaskWriteSkipsTheCommitWhenGitIsDisabled(t *testing.T) {
	vault := newGitBackedTestVault(t)
	head := gitVaultRun(t, vault.Root, "rev-parse", "HEAD")
	hostGitConfig(t, "git_enabled = false\n")
	marker, restore := noGitOnPATH(t)

	m := manageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "create", Task: "filed-task",
		Title: "Filed", Content: unitTaskBody(), Priority: "high",
	})
	restore()

	if m["commit"] != taskCommitSkipped {
		t.Errorf("commit = %v, want %q (result %#v)", m["commit"], taskCommitSkipped, m)
	}
	if _, ok := m["commit_error"]; ok {
		t.Errorf("a skipped commit is not an error: %#v", m)
	}
	if note, _ := m["commit_note"].(string); !strings.Contains(note, "git_enabled = false") {
		t.Errorf("commit_note = %q, want it to name the setting", note)
	}
	if _, err := os.Stat(filepath.Join(vault.Root, "Projects", "test-proj", "tasks", "filed-task.md")); err != nil {
		t.Errorf("the task file was not written: %v", err)
	}
	if spawned, err := os.ReadFile(marker); err == nil {
		t.Errorf("a task write started git on a disabled host:\n%s", spawned)
	}
	if got := gitVaultRun(t, vault.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved from %s to %s under git_enabled = false", head, got)
	}
}

// TestTaskCommitStateFollowsTheSettingNotTheTree: on a clean tree the state is
// skipped too, never no_change — the gate runs before the dirty probe.
func TestTaskCommitStateFollowsTheSettingNotTheTree(t *testing.T) {
	vault := newGitBackedTestVault(t)
	assertVaultClean(t, vault.Root)
	hostGitConfig(t, "git_enabled = false\n")
	if got := commitTaskWrite(vault, "test-proj", "seed-task", "amend")["commit"]; got != taskCommitSkipped {
		t.Errorf("commit on a clean tree = %q, want %q", got, taskCommitSkipped)
	}
}

// TestTaskWriteReportsAnUnreadableGitSetting: the write lands, nothing is
// committed, and the receipt names the config path without blaming a git
// identity or pointing at vp_vault_sync, which refuses too.
func TestTaskWriteReportsAnUnreadableGitSetting(t *testing.T) {
	vault := newGitBackedTestVault(t)
	head := gitVaultRun(t, vault.Root, "rev-parse", "HEAD")
	cfgPath := hostGitConfig(t, "git_enabled = \"no\"\n")

	m := manageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "create", Task: "filed-task",
		Title: "Filed", Content: unitTaskBody(), Priority: "high",
	})
	if m["commit"] != taskCommitConfigUnreadable {
		t.Fatalf("commit = %v, want %q (result %#v)", m["commit"], taskCommitConfigUnreadable, m)
	}
	detail, _ := m["commit_error"].(string)
	if !strings.Contains(detail, cfgPath) || !strings.Contains(detail, "WRITTEN AND IS SAFE") {
		t.Errorf("commit_error %q does not name the config path and the safe write", detail)
	}
	for _, wrong := range []string{"identity", "vp_vault_sync"} {
		if strings.Contains(detail, wrong) {
			t.Errorf("commit_error %q points at %q, which is not the remedy", detail, wrong)
		}
	}
	if got := gitVaultRun(t, vault.Root, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved under an unreadable setting")
	}
}

// TestBootstrapDirtAlertOnAGitDisabledHost pins the disabled branch the live
// canary cannot reach on CI: the alert says the dirt is expected and that no
// vp tool commits here, and never tells the session vp_vault_sync REFUSES —
// a tool that refuses by design on this host. An unreadable setting says so,
// never "disabled".
func TestBootstrapDirtAlertOnAGitDisabledHost(t *testing.T) {
	cases := []struct {
		name, config string
		want         []string
	}{
		{"disabled", "git_enabled = false\n", []string{"expected", "git_enabled = false", "no vp tool will commit"}},
		{"unreadable", "git_enabled = \"no\"\n", []string{"could not be read", "refuses"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vault := newGitBackedTestVault(t)
			dirtyFile(t, vault.Root, "Projects/test-proj/tasks/hand-edited.md", "# Hand edited\n\nbody\n")
			hostGitConfig(t, tc.config)

			vd := computeVaultDirt(vault.Root)
			if vd == nil {
				t.Fatal("no alert on a dirty vault")
			}
			for _, want := range tc.want {
				if !strings.Contains(vd.Message, want) {
					t.Errorf("alert %q lacks %q", vd.Message, want)
				}
			}
			if strings.Contains(vd.Message, "vp_vault_sync REFUSES") || strings.Contains(vd.Message, "disabled (") {
				t.Errorf("alert %q points at a refusing tool or misreports the setting", vd.Message)
			}
		})
	}
}

// TestVaultDirtAlertVariantsStayWithinTheMeasuredCeiling: the bootstrap
// payload ceiling is measured on the enabled line, so neither variant may be
// longer, or the ceiling stops being the worst case.
func TestVaultDirtAlertVariantsStayWithinTheMeasuredCeiling(t *testing.T) {
	enabled := len(vaultDirtMessage(9999))
	for name, msg := range map[string]string{
		"disabled":   vaultDirtMessageFor(9999, false, nil),
		"unreadable": vaultDirtMessageFor(9999, false, errors.New("x")),
	} {
		if len(msg) > enabled {
			t.Errorf("%s alert is %d bytes, longer than the measured enabled line (%d)", name, len(msg), enabled)
		}
	}
}
