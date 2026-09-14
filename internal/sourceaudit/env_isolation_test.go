// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTestFileFixture drops a throwaway file named like a real Go test file
// (isTest is derived from the "_test.go" suffix, and envIsolationBypass is the
// one rule in this package that cares whether a file IS one) and returns its
// directory.
func writeTestFileFixture(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestFindsAPlantedTSetenvBypass is the mutation test for the t.Setenv shape —
// the one this rule actually exists for: tool_coverage_test.go carried exactly
// this call (Setenv("CLAUDE_HOME", ...)) until this task fixed it.
func TestFindsAPlantedTSetenvBypass(t *testing.T) {
	dir := writeTestFileFixture(t, `package integration

import "testing"

func TestPlantedBypass(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
}
`)
	findings, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEnvIsolationFinding(findings, "integration.fixture_test.go:HOME") {
		t.Fatalf("expected an env-isolation-bypass finding for HOME, got: %v", ids(findings))
	}
}

// TestFindsAPlantedOsSetenvBypass covers the os.Setenv form — a plausible
// hand-rolled recipe if a future test skips *testing.T's Setenv (and its
// automatic restore) entirely.
func TestFindsAPlantedOsSetenvBypass(t *testing.T) {
	dir := writeTestFileFixture(t, `package integration

import (
	"os"
	"testing"
)

func TestPlantedBypass(t *testing.T) {
	os.Setenv("XDG_CONFIG_HOME", t.TempDir())
}
`)
	findings, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEnvIsolationFinding(findings, "integration.fixture_test.go:XDG_CONFIG_HOME") {
		t.Fatalf("expected an env-isolation-bypass finding for XDG_CONFIG_HOME, got: %v", ids(findings))
	}
}

// TestIsolateEnvUsageIsNotFlagged pins the negative: a test that only calls
// testinfra.IsolateEnv (never Setenv directly) must not trip the rule, or the
// rule would be flagging the exact sanctioned replacement it exists to steer
// tests toward.
func TestIsolateEnvUsageIsNotFlagged(t *testing.T) {
	dir := writeTestFileFixture(t, `package integration

import "testing"

func TestUsesIsolateEnv(t *testing.T) {
	testinfra.IsolateEnv(t)
}
`)
	findings, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasRealEnvIsolationBypass(findings) {
		t.Fatalf("testinfra.IsolateEnv usage was flagged as a bypass: %v", ids(findings))
	}
}

// TestIsolateEnvsOwnSetenvIsNotFlagged pins the scope boundary: the rule is
// scoped to package integration, so IsolateEnv's own t.Setenv("HOME", ...) —
// living in package testinfra — is out of scope by construction, with no
// separate self-exemption needed (unlike vaultWriteFunnel's explicit
// atomicfile skip).
func TestIsolateEnvsOwnSetenvIsNotFlagged(t *testing.T) {
	dir := writeTestFileFixture(t, `package testinfra

import "testing"

func IsolateEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
}
`)
	findings, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasRealEnvIsolationBypass(findings) {
		t.Fatalf("package testinfra's own Setenv was flagged: %v", ids(findings))
	}
}

// TestUnrelatedSetenvIsNotFlagged pins that the rule is scoped to the exact
// vars testinfra.IsolateEnv manages, not every Setenv call in sight —
// internal/integration legitimately sets PATH and several GIT_* vars outside
// IsolateEnv today, and none of that is this rule's business.
func TestUnrelatedSetenvIsNotFlagged(t *testing.T) {
	dir := writeTestFileFixture(t, `package integration

import "testing"

func TestSetsPath(t *testing.T) {
	t.Setenv("PATH", "/some/bin")
}
`)
	findings, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if hasRealEnvIsolationBypass(findings) {
		t.Fatalf("an unrelated env var was flagged: %v", ids(findings))
	}
}

// TestFuncLitBypassIsFound pins the reason this rule walks the WHOLE file
// rather than per-FuncDecl: internal/integration/tool_coverage_test.go stores
// its per-tool setup as `build: func(t *testing.T, h *testHarness) any {...}`
// closures inside a package-level map literal — an *ast.FuncLit, never a
// FuncDecl. A rule that only inspected top-level FuncDecl bodies (the way
// vaultWriteFunnel does) would see nothing here, exactly as it saw nothing at
// tool_coverage_test.go:1578,2023 before this task fixed them.
func TestFuncLitBypassIsFound(t *testing.T) {
	dir := writeTestFileFixture(t, `package integration

import "testing"

type fixture struct {
	build func(t *testing.T) any
}

var fixtures = map[string]fixture{
	"some_tool": {
		build: func(t *testing.T) any {
			t.Setenv("CLAUDE_HOME", t.TempDir())
			return nil
		},
	},
}
`)
	findings, err := Run(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !hasEnvIsolationFinding(findings, "integration.fixture_test.go:CLAUDE_HOME") {
		t.Fatalf("expected an env-isolation-bypass finding for CLAUDE_HOME from a FuncLit, got: %v", ids(findings))
	}
}

func hasEnvIsolationFinding(findings []Finding, symbol string) bool {
	for _, f := range findings {
		if f.Kind == KindEnvIsolationBypass && f.Symbol == symbol {
			return true
		}
	}
	return false
}

// hasRealEnvIsolationBypass reports a genuine bypass finding, ignoring the
// VACUOUS sentinel every single-file fixture necessarily trips (its
// files-scanned floor is sized for the real internal/integration package, not
// a one-file fixture tree).
func hasRealEnvIsolationBypass(findings []Finding) bool {
	for _, f := range findings {
		if f.Kind == KindEnvIsolationBypass && f.Symbol != "sourceaudit.envIsolationBypass/VACUOUS" {
			return true
		}
	}
	return false
}
