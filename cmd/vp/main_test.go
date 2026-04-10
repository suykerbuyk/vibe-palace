// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
)

func testRegistry() (*cli.Registry, *bytes.Buffer, *bytes.Buffer) {
	info := cli.BuildInfo{Version: "test", Commit: "abc1234", BuildDate: "2026-01-01"}
	reg := cli.NewRegistry(info)
	registerAll(reg, info)
	out := &bytes.Buffer{}
	errOut := &bytes.Buffer{}
	reg.SetOutput(out, errOut)
	return reg, out, errOut
}

func TestDispatchNoArgs(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch(nil)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d", code, cli.ExitOK)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Error("expected usage listing")
	}
}

func TestDispatchHelp(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"--help"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "Commands:") {
		t.Error("expected usage listing")
	}
}

func TestDispatchVersion(t *testing.T) {
	reg, _, _ := testRegistry()
	// version command writes to os.Stdout directly; just verify exit code.
	code := reg.Dispatch([]string{"version"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
}

func TestDispatchVersionFlag(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"--version"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp test") {
		t.Errorf("expected version, got %q", out.String())
	}
}

func TestDispatchUnknown(t *testing.T) {
	reg, _, errOut := testRegistry()
	code := reg.Dispatch([]string{"nope"})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want %d", code, cli.ExitUser)
	}
	if !strings.Contains(errOut.String(), "unknown command") {
		t.Error("expected error message")
	}
}

func TestDispatchHelpSearch(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"help", "search"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp search <query>") {
		t.Errorf("expected search help, got %q", out.String())
	}
}

func TestDispatchHelpMigrateVibevault(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"help", "migrate", "vibevault"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "--vault-path") {
		t.Errorf("expected migrate vibevault help, got %q", out.String())
	}
}

func TestDispatchHelpVaultSync(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"help", "vault", "sync"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vault sync") {
		t.Errorf("expected vault sync help, got %q", out.String())
	}
}

func TestDispatchPerCommandHelp(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"search", "--help"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vp search") {
		t.Errorf("expected search help, got %q", out.String())
	}
}

func TestDispatchTwoWordPerCommandHelp(t *testing.T) {
	reg, out, _ := testRegistry()
	code := reg.Dispatch([]string{"vault", "push", "--help"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "vault push") {
		t.Errorf("expected vault push help, got %q", out.String())
	}
}

func TestDispatchMigrateParent(t *testing.T) {
	reg, _, _ := testRegistry()
	// "migrate" alone should show help and return ExitOK.
	code := reg.Dispatch([]string{"migrate"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d", code, cli.ExitOK)
	}
}

func TestDispatchVaultParent(t *testing.T) {
	reg, _, _ := testRegistry()
	code := reg.Dispatch([]string{"vault"})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want %d", code, cli.ExitOK)
	}
}

func TestAllCommandsRegistered(t *testing.T) {
	reg, _, _ := testRegistry()
	expected := []string{
		"check", "help", "init", "inject", "mcp",
		"migrate", "migrate mempalace", "migrate vibevault",
		"search", "serve", "sessions", "status", "tasks",
		"vault", "vault pull", "vault push", "vault sync",
		"version",
	}
	for _, name := range expected {
		if _, ok := reg.Lookup(name); !ok {
			t.Errorf("command %q not registered", name)
		}
	}
}

// Command-specific tests are in cmd_*_test.go files.
