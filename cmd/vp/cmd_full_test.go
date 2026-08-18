// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// TestFullStatusCommand tests the cmdStatus closure end-to-end through OpenVault.
func TestFullStatusCommand(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	v := storage.NewVault(vaultDir)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "task-one", Title: "Task One", Content: "content", Priority: "high"})
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "Session", Tag: "impl",
	}, "body")

	// Capture stdout.
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := cmdStatus()
	code := cmd.Run([]string{"--project", "test-proj"})

	w.Close()
	os.Stdout = old
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out, "test-proj") {
		t.Errorf("expected project name in output: %s", out)
	}
}

// TestFullStatusJSON tests the full status --json path through OpenVault.
func TestFullStatusJSON(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	v := storage.NewVault(vaultDir)
	v.CreateTask("jsonproj", storage.TaskSpec{Slug: "t1", Title: "T1", Content: "c", Priority: "high"})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := cmdStatus()
	code := cmd.Run([]string{"--project", "jsonproj", "--json"})

	w.Close()
	os.Stdout = old
	var buf [8192]byte
	n, _ := r.Read(buf[:])

	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var result statusResult
	if err := json.Unmarshal(buf[:n], &result); err != nil {
		t.Fatalf("invalid JSON: %v\nraw: %s", err, buf[:n])
	}
	if result.Tasks != 1 {
		t.Errorf("tasks = %d", result.Tasks)
	}
}

// TestFullSessionsCommand tests cmdSessions through OpenVault.
func TestFullSessionsCommand(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	v := storage.NewVault(vaultDir)
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "Test", Tag: "impl", FrictionScore: 25,
	}, "body")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := cmdSessions()
	code := cmd.Run([]string{"--project", "test-proj"})

	w.Close()
	os.Stdout = old
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out, "2026-04-01") {
		t.Errorf("expected session date in output: %s", out)
	}
}

// TestFullTasksCommand tests cmdTasks through OpenVault.
func TestFullTasksCommand(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	v := storage.NewVault(vaultDir)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "my-task", Title: "My Task", Content: "content", Priority: "high"})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := cmdTasks()
	code := cmd.Run([]string{"--project", "test-proj"})

	w.Close()
	os.Stdout = old
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out, "my-task") {
		t.Errorf("expected task in output: %s", out)
	}
}

// TestFullInjectCommand tests cmdInject through OpenVault.
func TestFullInjectCommand(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	v := storage.NewVault(vaultDir)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "inject-task", Title: "Inject Task", Content: "content", Priority: "high"})

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := cmdInject()
	code := cmd.Run([]string{"--project", "test-proj"})

	w.Close()
	os.Stdout = old
	var buf [16384]byte
	n, _ := r.Read(buf[:])

	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var result tools.BootstrapResult
	if err := json.Unmarshal(buf[:n], &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if result.Project != "test-proj" {
		t.Errorf("project = %q", result.Project)
	}
	if result.ActiveTaskCount != 1 {
		t.Errorf("active_task_count = %d", result.ActiveTaskCount)
	}
	if len(result.HeadOfQueue) != 1 {
		t.Errorf("head_of_queue = %d", len(result.HeadOfQueue))
	}
}

// TestFullSessionsWithLimit tests --last flag through full closure.
func TestFullSessionsWithLimit(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	v := storage.NewVault(vaultDir)
	for range 5 {
		v.WriteSession("test-proj", storage.SessionMeta{
			Date: "2026-04-01", Title: "Session",
		}, "body")
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := cmdSessions()
	code := cmd.Run([]string{"--project", "test-proj", "--last", "2"})

	w.Close()
	os.Stdout = old
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	// Header + 2 data lines.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d", len(lines))
	}
}

// TestFullBadFlags tests that bad flags are rejected across commands.
func TestFullBadFlags(t *testing.T) {
	cmds := []struct {
		name string
		cmd  *cli.Command
	}{
		{"status", cmdStatus()},
		{"sessions", cmdSessions()},
		{"tasks", cmdTasks()},
		{"inject", cmdInject()},
	}

	for _, tc := range cmds {
		t.Run(tc.name, func(t *testing.T) {
			code := tc.cmd.Run([]string{"--unknown-flag"})
			if code != cli.ExitUser {
				t.Errorf("%s: exit code = %d, want ExitUser (bad flag)", tc.name, code)
			}
		})
	}
}

// TestFullSessionsDefaultLimit tests that default limit (10) works through
// the full closure path.
func TestFullSessionsDefaultLimit(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	v := storage.NewVault(vaultDir)
	for range 15 {
		v.WriteSession("test-proj", storage.SessionMeta{
			Date: "2026-04-01", Title: "Session",
		}, "body")
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := cmdSessions()
	code := cmd.Run([]string{"--project", "test-proj"})

	w.Close()
	os.Stdout = old
	var buf [8192]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	// Default limit is 10; header + 10 data lines = 11
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 11 {
		t.Errorf("expected 11 lines (default limit 10), got %d", len(lines))
	}
}

// TestFullTasksDone tests --done flag through the full closure.
func TestFullTasksDone(t *testing.T) {
	vaultDir := setupTestVaultEnv(t)
	v := storage.NewVault(vaultDir)
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "active-one", Title: "Active", Content: "c", Priority: "high"})
	v.CreateTask("test-proj", storage.TaskSpec{Slug: "done-one", Title: "Done", Content: "c", Priority: "low"})
	v.RetireTask("test-proj", "done-one")

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := cmdTasks()
	code := cmd.Run([]string{"--project", "test-proj", "--done"})

	w.Close()
	os.Stdout = old
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	out := string(buf[:n])

	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out, "done-one") {
		t.Error("expected done task to appear with --done flag")
	}
}

// TestFullVaultPullNoConfig tests vault pull when vault can't be opened.
func TestFullVaultPullNoConfig(t *testing.T) {
	// Point to nonexistent config.
	configDir := t.TempDir()
	os.MkdirAll(configDir+"/vibe-palace", 0o755)
	// No config.toml → OpenVault will fail.

	old := os.Getenv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", configDir)
	t.Cleanup(func() {
		if old == "" {
			os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			os.Setenv("XDG_CONFIG_HOME", old)
		}
	})

	cmd := cmdVaultPull()
	code := cmd.Run(nil)
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser (no config)", code)
	}
}
