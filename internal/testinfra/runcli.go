// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// Binary build is cached across the whole test process — `go build` is the
// expensive step, not the invocations. Promoted verbatim from
// internal/integration/template_reconcile_test.go's buildVPBinary.
var (
	vpBinaryOnce sync.Once
	vpBinaryPath string
	vpBinaryErr  error
)

// BuildVPBinary compiles cmd/vp once per test process (sync.Once-cached) and
// returns the resulting binary path. Promoted verbatim from
// internal/integration/template_reconcile_test.go's buildVPBinary, including
// the Windows .exe-suffix requirement.
//
// 🔴 THE .exe SUFFIX IS LOAD-BEARING ON WINDOWS, NOT COSMETIC. os/exec
// resolves an extension-less path against PATHEXT, so a binary built as plain
// "vp" cannot be launched at all — it fails with the misleading "executable
// file not found in %PATH%" even though the file is right there. That
// defeated the ENTIRE windows-lock job for 11+ consecutive pushes
// (2026-07-21 → 2026-07-26): all 16 children of
// TestIntegration_VaultLockCrossProcess failed to exec, and the resulting
// "lost update" / "an edit was clobbered" assertions read as a
// lock-correctness bug when nothing had ever run.
func BuildVPBinary(t *testing.T) string {
	t.Helper()
	vpBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "vp-integration-bin-")
		if err != nil {
			vpBinaryErr = err
			return
		}
		bin := filepath.Join(dir, "vp")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", bin,
			"github.com/suykerbuyk/vibe-palace/cmd/vp")
		cmd.Stderr = os.Stderr
		cmd.Stdout = os.Stderr
		if err := cmd.Run(); err != nil {
			vpBinaryErr = err
			return
		}
		vpBinaryPath = bin
	})
	if vpBinaryErr != nil {
		t.Fatalf("build vp binary: %v", vpBinaryErr)
	}
	return vpBinaryPath
}

// CLIResult captures one exec of the built vp binary. RunCLI never fails the
// test itself — callers assert ExitCode/Stdout/Stderr directly, or call Must
// for the old runVP-style fatal-on-nonzero convenience.
type CLIResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// RunCLI execs BuildVPBinary(t) with args in dir, using env verbatim as
// cmd.Env (build it via (*Env).Environ(extra...), or by hand for cases that
// need something Environ doesn't produce — e.g. a dead HTTPS_PROXY). stdin
// may be nil. RunCLI never falls back to os.Environ() implicitly: callers own
// isolation, and a nil/empty env is passed through as-is rather than silently
// widened.
//
// A non-zero exit is not a Go test failure by itself — cases that expect a
// non-zero exit (e.g. ExitUser on bad input) need CLIResult.ExitCode to stay
// inspectable rather than aborting the test. An exec failure that never
// produced an exit code (binary missing, permission denied, …) IS a hard
// t.Fatalf: no CLIResult is meaningful in that case.
func RunCLI(t *testing.T, env []string, dir string, stdin []byte, args ...string) CLIResult {
	t.Helper()
	bin := BuildVPBinary(t)

	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("RunCLI: exec %s %v: %v", bin, args, err)
		}
	}
	return CLIResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}
}

// Must fails the test if ExitCode != 0, echoing stdout+stderr, and returns r
// for chaining (e.g. testinfra.RunCLI(...).Must(t).Stdout).
func (r CLIResult) Must(t *testing.T) CLIResult {
	t.Helper()
	if r.ExitCode != 0 {
		t.Fatalf("vp exited %d\n--- stdout ---\n%s\n--- stderr ---\n%s", r.ExitCode, r.Stdout, r.Stderr)
	}
	return r
}

// RetainOnFailure returns a fresh directory under os.TempDir() named
// "vp-e2e-<tag>.<random>" (dot separator matches the retired bash harnesses'
// `mktemp -t vp-e2e-<tier>.XXXXXX` convention, so ci.yml's artifact-upload
// path globs need no edit) that is removed on cleanup UNLESS t.Failed(),
// matching the bash harnesses' retain-tmpdir-on-failure contract.
// t.TempDir() cannot express this: its cleanup is unconditional regardless of
// test outcome, so a failing case would lose its on-disk post-mortem state
// exactly like a passing one.
func RetainOnFailure(t *testing.T, tag string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vp-e2e-"+tag+".")
	if err != nil {
		t.Fatalf("RetainOnFailure: mkdtemp: %v", err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("retained tmpdir: %s", dir)
			return
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("RetainOnFailure: cleanup %s: %v", dir, err)
		}
	})
	return dir
}
