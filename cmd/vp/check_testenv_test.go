// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcphost"
	"github.com/suykerbuyk/vibe-palace/internal/project"
)

// formatIntPtr formats a seam-call counter for a failure message: its count
// when set, or "nil" when the seam was never wired — never the pointer
// itself, which %v would print as an address rather than a count.
func formatIntPtr(p *int) string {
	if p == nil {
		return "nil"
	}
	return strconv.Itoa(*p)
}

// The full `vp check` suite (gatherCheckResults) reaches host state through
// two dependencies: the Embedder row constructs the ~90 MB ONNX model, and the
// MCP host rows run the real `grok mcp list`, which writes the developer's
// ~/.grok. A test that drives the full suite gets one of the two environments
// below. Both sandbox HOME and the config dir, run from a fresh temp cwd, and
// stub both seams, so nothing a full-suite test does depends on — or writes to
// — the machine it runs on.

// checkEnv is what a full-suite check environment hands its test.
type checkEnv struct {
	// Vault is the temp vault root; empty in unconfiguredCheckEnv.
	Vault string
	// Hosts is what the stubbed mcpHostRegistry returns. It is read at call
	// time, so a test may set it after the helper returns. nil by default:
	// the report then carries the single "MCP hosts" Skip row.
	Hosts []mcphost.Host
	// HostCalls counts mcpHostRegistry calls.
	HostCalls *int
	// EmbedderCalls counts newVaultEmbedder constructions (migrate's
	// stubVaultEmbedder counter). nil in unconfiguredCheckEnv, whose
	// embedder seam is forbidden rather than stubbed.
	EmbedderCalls *int
}

// healthyCheckEnv is a configured machine with a valid, empty vault: a global
// config naming a temp vault (setupTestVaultEnv, which also sandboxes HOME and
// XDG_CACHE_HOME and forbids the embedder), a Projects/ directory, and a temp
// cwd whose .vibe-palace.toml names project "checktest". The embedder seam is
// then stubbed with a 384-dimension mock and the host registry with e.Hosts.
//
// setupTestVaultEnv writes no git_enabled. The Git row reads the raw key
// through VaultReconciler.gitEnabled (internal/reconcile/vault.go), where an
// absent key is false — LoadConfig's defaults.toml layering does not apply to
// that decode — so the row reads "disabled", as the hand-written
// `git_enabled = false` these tests used to carry produced.
func healthyCheckEnv(t *testing.T) *checkEnv {
	t.Helper()
	e := &checkEnv{Vault: setupTestVaultEnv(t)}
	if err := os.MkdirAll(filepath.Join(e.Vault, "Projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.WriteFile(filepath.Join(cwd, project.ConfigFileName),
		[]byte("[project]\nname = \"checktest\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.EmbedderCalls = stubVaultEmbedder(t, embedder.NewMock(384))
	stubMCPHostRegistry(t, e)
	return e
}

// unconfiguredCheckEnv is a machine with no global config: initTestEnv's
// sandboxed HOME and config dir with nothing in them, and a temp cwd. The
// embedder seam is forbidden — the full suite skips the Embedder row with no
// config, so a construction here is a bug and fails the test loudly.
func unconfiguredCheckEnv(t *testing.T) *checkEnv {
	t.Helper()
	initTestEnv(t, false)
	forbidVaultEmbedder(t)
	t.Chdir(t.TempDir())
	e := &checkEnv{}
	stubMCPHostRegistry(t, e)
	return e
}

// mcpHostSeamEnv is set by stubMCPHostRegistry before it touches the package
// variable. t.Setenv panics in a test that has called t.Parallel, so a
// parallel test cannot race another on mcpHostRegistry — the same ordering as
// migrate's embedderSeamEnv.
const mcpHostSeamEnv = "VP_TEST_MCP_HOST_SEAM"

// stubMCPHostRegistry replaces mcpHostRegistry, for the rest of the test, with
// one that returns e.Hosts and counts into e.HostCalls.
func stubMCPHostRegistry(t *testing.T, e *checkEnv) {
	t.Helper()
	// Setenv FIRST: under t.Parallel it panics before the variable is touched.
	t.Setenv(mcpHostSeamEnv, "stub")
	calls := 0
	e.HostCalls = &calls
	prev := mcpHostRegistry
	t.Cleanup(func() { mcpHostRegistry = prev })
	mcpHostRegistry = func() []mcphost.Host {
		calls++
		return e.Hosts
	}
}

// fakeMCPHost is an mcphost.Host with fixed answers that runs nothing.
type fakeMCPHost struct {
	name      string
	detected  bool
	installed bool
}

func (f fakeMCPHost) Name() string                           { return f.name }
func (f fakeMCPHost) Flag() string                           { return "--" + f.name }
func (f fakeMCPHost) Detected() bool                         { return f.detected }
func (f fakeMCPHost) Installed() (bool, error)               { return f.installed, nil }
func (f fakeMCPHost) Install(_, _ string, _ io.Writer) error { return nil }
func (f fakeMCPHost) Uninstall(_ io.Writer) error            { return nil }
func (f fakeMCPHost) Executables() []string                  { return nil }

var _ mcphost.Host = fakeMCPHost{}

// runFullCheckHuman runs the full `vp check` suite with the human renderer and
// returns stdout and the exit code. The Embedder progress line on stderr is
// discarded.
func runFullCheckHuman(t *testing.T) (string, int) {
	t.Helper()
	fv, _ := cli.ParseFlags(checkFlags, nil)
	var code int
	out := captureStdout(t, func() {
		captureStderr(t, func() { code = runCheck(cli.BuildInfo{Version: "test"}, fv) })
	})
	return out, code
}

// runFullCheckJSON runs the full `vp check --json` suite and returns the parsed
// report and the exit code.
func runFullCheckJSON(t *testing.T) (check.JSONReport, int) {
	t.Helper()
	fv, _ := cli.ParseFlags(checkFlags, []string{"--json"})
	var code int
	out := captureStdout(t, func() {
		captureStderr(t, func() { code = runCheck(cli.BuildInfo{Version: "test"}, fv) })
	})
	var rep check.JSONReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	return rep, code
}

// checkRow returns the report's row named name, failing the test when absent.
func checkRow(t *testing.T, rep check.JSONReport, name string) check.JSONCheck {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q row in the report: %+v", name, rep.Checks)
	return check.JSONCheck{}
}

// TestCheckEnvHelpersStubHostRegistry locks the host seam INTO both helpers:
// each must leave mcpHostRegistry answering e.Hosts and counting, and the
// original registry must be back once the test ends. Dropping the stub from
// either helper turns this red.
func TestCheckEnvHelpersStubHostRegistry(t *testing.T) {
	registry := reflect.ValueOf(mcphost.Registry).Pointer()
	if reflect.ValueOf(mcpHostRegistry).Pointer() != registry {
		t.Fatal("mcpHostRegistry is not mcphost.Registry outside a stubbed test")
	}
	helpers := map[string]func(*testing.T) *checkEnv{
		"healthy":      healthyCheckEnv,
		"unconfigured": unconfiguredCheckEnv,
	}
	for name, helper := range helpers {
		t.Run(name, func(t *testing.T) {
			e := helper(t)
			if got := os.Getenv(mcpHostSeamEnv); got != "stub" {
				t.Errorf("%s = %q, want \"stub\"", mcpHostSeamEnv, got)
			}
			e.Hosts = []mcphost.Host{fakeMCPHost{name: "fake"}}
			hosts := mcpHostRegistry()
			if len(hosts) != 1 || hosts[0].Name() != "fake" {
				t.Errorf("mcpHostRegistry() = %v, want e.Hosts", hosts)
			}
			if e.HostCalls == nil || *e.HostCalls != 1 {
				t.Errorf("HostCalls = %s, want 1", formatIntPtr(e.HostCalls))
			}
		})
		if reflect.ValueOf(mcpHostRegistry).Pointer() != registry {
			t.Errorf("%s: mcpHostRegistry was not restored by the helper's cleanup", name)
		}
	}
	if got := os.Getenv(embedderSeamEnv); got != "" {
		t.Errorf("%s leaked past the subtests: %q", embedderSeamEnv, got)
	}
}

// TestStubMCPHostRegistryRefusesParallel pins that the host stub panics in a
// parallel test (through t.Setenv) BEFORE touching the package variable.
func TestStubMCPHostRegistryRefusesParallel(t *testing.T) {
	t.Run("parallel", func(t *testing.T) {
		t.Parallel()
		before := reflect.ValueOf(mcpHostRegistry).Pointer()
		panicked := func() (msg string) {
			defer func() {
				if r := recover(); r != nil {
					msg = fmt.Sprint(r)
				}
			}()
			stubMCPHostRegistry(t, &checkEnv{})
			return ""
		}()
		if !strings.Contains(panicked, "Setenv") {
			t.Errorf("stubMCPHostRegistry did not panic through t.Setenv under t.Parallel (recovered %q)", panicked)
		}
		if reflect.ValueOf(mcpHostRegistry).Pointer() != before {
			t.Error("stubMCPHostRegistry swapped mcpHostRegistry before panicking")
		}
	})
}
