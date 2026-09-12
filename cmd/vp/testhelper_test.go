// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// setupTestVaultEnv creates a temp vault and configures the global-config
// lookup so that storage.OpenVaultGlobal (and OpenVaultFromCwd, absent any
// cwd override) find a valid config pointing to the temp vault.
//
// It sandboxes the same four vars as initTestEnv, for the same per-GOOS
// reasons documented there: HOME/USERPROFILE for os.UserHomeDir (a command
// reaching hook.Install() would otherwise rewrite the developer's real
// ~/.claude/settings.json), and XDG_CONFIG_HOME/APPDATA for
// os.UserConfigDir, which honors XDG on Linux and %AppData% on Windows.
//
// XDG_CACHE_HOME goes under the sandboxed home too (<home>/.cache), and the
// embedder seam is forbidden (forbidVaultEmbedder): a test that needs an
// embedder substitutes one with stubVaultEmbedder. Together they mean no test
// built on this helper can reach the ~90 MB ONNX model download — whose
// go-huggingface downloader has a data race that `go test -race` reports as a
// failure in OUR tests — through any seam-routed site: setupEmbedder (both
// migrate subcommands), `vp search`, bootstrap(), and `vp check`'s Embedder
// row (gatherCheckResults hands check.CheckEmbedder a closure over
// newVaultEmbedder). A test that drives runCheck against this helper's valid
// temp vault therefore fails loudly instead of cold-downloading; the full-suite
// check tests use healthyCheckEnv, which stubs the seam. Do not route a new
// construction site around the seam, and do not re-pin XDG_CACHE_HOME to the
// host cache.
//
// Returns the vault root path; the env is restored by t.Setenv's cleanup.
func setupTestVaultEnv(t *testing.T) string {
	t.Helper()
	return setupTestVaultEnvAt(t, t.TempDir())
}

// setupTestVaultEnvAt is setupTestVaultEnv, parametrized on the vault
// directory instead of creating its own t.TempDir(). Use this when the test
// needs the vault to already sit somewhere specific — e.g. nested inside a
// pre-built enclosing repository — rather than at a fresh, standalone temp
// directory.
//
// Returns vaultDir unchanged, for symmetry with setupTestVaultEnv.
func setupTestVaultEnvAt(t *testing.T, vaultDir string) string {
	t.Helper()

	configDir := t.TempDir()
	homeDir := t.TempDir()

	// Create the config directory structure.
	vpConfigDir := filepath.Join(configDir, "vibe-palace")
	os.MkdirAll(vpConfigDir, 0o755)

	// Write config.toml pointing to our temp vault.
	configContent := `vault_path = "` + vaultDir + `"` + "\n"
	os.WriteFile(filepath.Join(vpConfigDir, "config.toml"), []byte(configContent), 0o644)

	t.Setenv("XDG_CONFIG_HOME", configDir)
	t.Setenv("APPDATA", configDir)
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(homeDir, ".cache"))
	forbidVaultEmbedder(t)

	return vaultDir
}

// embedderSeamEnv is set by both seam helpers. Its value records which one ran
// last ("forbid" or "stub"), and setting it at all is what makes the helpers
// parallel-safe: t.Setenv panics in a test that has called t.Parallel, so a
// parallel test cannot race another on the newVaultEmbedder package variable.
const embedderSeamEnv = "VP_TEST_EMBEDDER_SEAM"

// embedderSeamRecorder is the slice of testing.TB the seam helpers use. It is
// narrow so a test can hand in a recorder and observe the forbid path failing
// without failing itself.
type embedderSeamRecorder interface {
	Helper()
	Errorf(format string, args ...any)
	Cleanup(func())
	Setenv(key, value string)
}

// errEmbedderForbidden is what a forbidden newVaultEmbedder returns.
var errEmbedderForbidden = errors.New("embedder construction forbidden by forbidVaultEmbedder")

// forbidVaultEmbedder replaces newVaultEmbedder, for the rest of the test, with
// one that fails the test and returns errEmbedderForbidden. It uses Errorf,
// not Fatal, because the MCP stack's lazy embedder can construct off the test
// goroutine, where Fatal is not allowed.
func forbidVaultEmbedder(t embedderSeamRecorder) {
	t.Helper()
	// Setenv FIRST: under t.Parallel it panics before the variable is touched.
	t.Setenv(embedderSeamEnv, "forbid")
	prev := newVaultEmbedder
	t.Cleanup(func() { newVaultEmbedder = prev })
	newVaultEmbedder = func(*storage.Vault, storage.Config) (embedder.Embedder, error) {
		t.Errorf("forbidVaultEmbedder: this test constructed the ONNX embedder, which downloads ~90 MB on a cold cache; " +
			"setupTestVaultEnv forbids that — substitute one with stubVaultEmbedder, or stop the code path reaching it")
		return nil, errEmbedderForbidden
	}
}

// stubVaultEmbedder replaces newVaultEmbedder, for the rest of the test, with
// one that returns emb, and returns a pointer to its construction count.
func stubVaultEmbedder(t embedderSeamRecorder, emb embedder.Embedder) *int {
	t.Helper()
	// Setenv FIRST: under t.Parallel it panics before the variable is touched.
	t.Setenv(embedderSeamEnv, "stub")
	var constructed int
	prev := newVaultEmbedder
	t.Cleanup(func() { newVaultEmbedder = prev })
	newVaultEmbedder = func(*storage.Vault, storage.Config) (embedder.Embedder, error) {
		constructed++
		return emb, nil
	}
	return &constructed
}

// seamRecorder is an embedderSeamRecorder that records instead of failing.
type seamRecorder struct {
	errors   []string
	cleanups []func()
	env      map[string]string
}

func (r *seamRecorder) Helper() {}
func (r *seamRecorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *seamRecorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }
func (r *seamRecorder) Setenv(k, v string) {
	if r.env == nil {
		r.env = map[string]string{}
	}
	r.env[k] = v
}

// runCleanups runs the recorded cleanups last-registered first, as testing does.
func (r *seamRecorder) runCleanups() {
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}

// seamFunc identifies the function newVaultEmbedder currently holds.
func seamFunc() uintptr { return reflect.ValueOf(newVaultEmbedder).Pointer() }

// TestForbidVaultEmbedderRecordsAndRestores drives forbidVaultEmbedder through
// a recorder, so the forbidden construction can be observed failing without
// failing this test.
func TestForbidVaultEmbedderRecordsAndRestores(t *testing.T) {
	before := seamFunc()
	rec := &seamRecorder{}
	forbidVaultEmbedder(rec)
	if rec.env[embedderSeamEnv] != "forbid" {
		t.Errorf("%s = %q, want \"forbid\"", embedderSeamEnv, rec.env[embedderSeamEnv])
	}

	emb, err := newVaultEmbedder(nil, storage.Config{})
	if !errors.Is(err, errEmbedderForbidden) || emb != nil {
		t.Errorf("forbidden seam returned (%v, %v), want (nil, errEmbedderForbidden)", emb, err)
	}
	if len(rec.errors) != 1 || !strings.Contains(rec.errors[0], "stubVaultEmbedder") {
		t.Errorf("recorded errors = %q, want one naming stubVaultEmbedder", rec.errors)
	}

	rec.runCleanups()
	if seamFunc() != before {
		t.Error("newVaultEmbedder was not restored by the helper's cleanup")
	}
}

// TestSetupTestVaultEnvInstallsEmbedderForbid locks the guard INTO the harness:
// only forbidVaultEmbedder sets the seam variable to "forbid", so dropping the
// call from setupTestVaultEnv turns this red.
func TestSetupTestVaultEnvInstallsEmbedderForbid(t *testing.T) {
	setupTestVaultEnv(t)
	if got := os.Getenv(embedderSeamEnv); got != "forbid" {
		t.Errorf("%s = %q after setupTestVaultEnv, want \"forbid\"", embedderSeamEnv, got)
	}
}

// TestEmbedderSeamHelpersRefuseParallel pins that both helpers panic in a
// parallel test (through t.Setenv) BEFORE touching the package variable, so
// two parallel tests can never race on it.
func TestEmbedderSeamHelpersRefuseParallel(t *testing.T) {
	helpers := map[string]func(*testing.T){
		"forbid": func(t *testing.T) { forbidVaultEmbedder(t) },
		"stub":   func(t *testing.T) { stubVaultEmbedder(t, embedder.NewMock(8)) },
	}
	t.Run("parallel", func(t *testing.T) {
		t.Parallel()
		for name, helper := range helpers {
			before := seamFunc()
			panicked := func() (msg string) {
				defer func() {
					if r := recover(); r != nil {
						msg = fmt.Sprint(r)
					}
				}()
				helper(t)
				return ""
			}()
			if !strings.Contains(panicked, "Setenv") {
				t.Errorf("%s: helper did not panic through t.Setenv under t.Parallel (recovered %q)", name, panicked)
			}
			if seamFunc() != before {
				t.Errorf("%s: helper swapped newVaultEmbedder before panicking", name)
			}
		}
	})
}
