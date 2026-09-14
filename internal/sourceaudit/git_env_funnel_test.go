// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"strings"
	"testing"
)

// gitEnvFixture exercises every shape gitExecEnvFunnel must separate: no Env at
// all, an Env that never strips repo-local git vars, a call chained directly
// onto a method (so Env can never be attached), and each way of calling the
// guard correctly.
const gitEnvFixture = `package fixture

import (
	"context"
	"os"
	"os/exec"
)

// BYPASS: no .Env assignment at all.
func NoEnv(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "status")
	out, err := cmd.Output()
	return string(out), err
}

// BYPASS: sets .Env, but from a plain os.Environ() that never strips
// repo-local git vars.
func WrongEnv(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "status")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return string(out), err
}

// BYPASS: chained directly onto .Output(), never bound to a variable, so its
// Env can never be attached at all.
func Chained(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "status").Output()
	return string(out), err
}

// CLEAN: bare-call guard.
func Guarded(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "status")
	cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	return string(out), err
}

// CLEAN: package-qualified guard, the shape cmd/vp and internal/tools use.
func GuardedQualified(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "status")
	cmd.Env = storage.SafeGitEnv()
	out, err := cmd.Output()
	return string(out), err
}

// CLEAN: the guard nested inside append(), not a bare RHS.
func GuardedNested(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "status")
	cmd.Env = append(withoutRepoLocalGitEnv(nil), "X=1")
	out, err := cmd.Output()
	return string(out), err
}

// CLEAN: the CommandContext form, guarded.
func GuardedContext(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "status")
	cmd.Env = SafeGitEnv()
	out, err := cmd.Output()
	return string(out), err
}

// CLEAN: a different program entirely. Flagging this is the failure mode that
// gets a gate disabled.
func NotGit(dir string) (string, error) {
	cmd := exec.Command("ls", dir)
	out, err := cmd.Output()
	return string(out), err
}
`

func gitEnvFindingIDs(t *testing.T) []string {
	t.Helper()
	findings, err := Run(writeFixture(t, gitEnvFixture))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindGitExecUnsafeEnv {
			out = append(out, f.Symbol)
		}
	}
	return out
}

// TestGitEnvFunnelFlagsBypasses pins the recall claim: every unguarded git
// subprocess shape must be caught.
func TestGitEnvFunnelFlagsBypasses(t *testing.T) {
	got := gitEnvFindingIDs(t)
	for _, want := range []string{"fixture.NoEnv", "fixture.WrongEnv", "fixture.Chained"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s is an unguarded git subprocess and the rule did not flag it. "+
				"A ratchet that cannot see the defect it was written for is coverage in name only.\n  got: %v",
				want, got)
		}
	}
}

// TestGitEnvFunnelDoesNotFlagCleanCode pins the precision claim: a noisy gate
// is a disabled gate.
func TestGitEnvFunnelDoesNotFlagCleanCode(t *testing.T) {
	got := gitEnvFindingIDs(t)
	for _, unwanted := range []string{
		"fixture.Guarded", "fixture.GuardedQualified", "fixture.GuardedNested",
		"fixture.GuardedContext", "fixture.NotGit",
	} {
		if slices.Contains(got, unwanted) {
			t.Errorf("%s is correctly guarded (or is not git at all) and the rule flagged it.\n  got: %v",
				unwanted, got)
		}
	}
}

// TestGitEnvFunnelReportsVacuity proves the anti-vacuity floor is itself live:
// a tiny fixture cannot possibly meet the tree-wide floor and must say so
// rather than passing green.
func TestGitEnvFunnelReportsVacuity(t *testing.T) {
	findings, err := Run(writeFixture(t, "package fixture\n"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var vac string
	for _, f := range findings {
		if f.Kind == KindGitExecUnsafeEnv && strings.HasSuffix(f.Symbol, "/VACUOUS") {
			vac = f.Detail
		}
	}
	if vac == "" {
		t.Fatalf("a fixture with no git subprocesses at all did NOT report vacuity, so the floor is not "+
			"running.\n  findings: %v", ids(findings))
	}
	if !strings.Contains(vac, "Do NOT baseline this entry") {
		t.Errorf("the vacuity finding must tell the reader not to baseline it.\n  detail: %s", vac)
	}
}
