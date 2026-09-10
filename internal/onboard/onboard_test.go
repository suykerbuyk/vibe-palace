// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package onboard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// sandboxHost redirects every host-global root this package's steps can reach.
//
// hook-wiring calls hook.Install(), which REWRITES ~/.claude/settings.json, and
// command-shims probes the running host's user-global command surfaces. A test
// run without this rewrites the developer's real machine — which is exactly the
// hazard SideHostGlobal exists to name, so leaving it unsandboxed here would be
// the package failing its own thesis.
//
// Every variable is set unconditionally; the ones that do not apply to the
// running GOOS are inert rather than wrong. XDG_DATA_HOME is included because
// plugin.ClaudeUserCommandsHealthy reads the user-global plugin cache from
// there, and an operator whose real cache is healthy would otherwise flip the
// shim step onto its skip branch.
func sandboxHost(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("APPDATA", cfg)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg-data"))
	return home
}

// newRequest builds a Request over a fresh vault, optionally with a project
// directory that passes the rooted-signal gate.
func newRequest(t *testing.T, withProjectDir bool) (Request, string) {
	t.Helper()
	vaultDir := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(vaultDir, 0o755); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	req := Request{
		OpenVault: func() (*storage.Vault, error) { return storage.NewVault(vaultDir), nil },
		Slug:      "alpha",
	}
	if withProjectDir {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module alpha\n"), 0o644); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
		req.ProjectDir = dir
	}
	return req, vaultDir
}

// swapStepTable replaces the canonical table for the duration of one test.
// It is the only way to force a specific step to fail: every real step drives a
// writer in another package, and manufacturing a genuine failure in one of them
// would test that writer's error path rather than Run's sequencing.
func swapStepTable(t *testing.T, mutate func([]Step)) {
	t.Helper()
	restore := stepTable
	t.Cleanup(func() { stepTable = restore })
	next := slices.Clone(restore)
	mutate(next)
	stepTable = next
}

// renderStepTable formats the table for the golden comparison: one line per
// step, tab-separated, in table order.
func renderStepTable(steps []Step) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.Name)
		b.WriteByte('\t')
		b.WriteString(s.Side.String())
		b.WriteByte('\t')
		if s.ReadsHostGlobal {
			b.WriteString("reads-host-global")
		} else {
			b.WriteString("-")
		}
		b.WriteByte('\t')
		if len(s.Needs) == 0 {
			b.WriteString("-")
		} else {
			b.WriteString(strings.Join(s.Needs, ","))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// TestOnboardSteps_SideGolden pins the whole table: name, Side,
// ReadsHostGlobal and Needs.
//
// A "the tag is one of three values" test cannot substitute for this. Omitted
// is derived from a HAND-WRITTEN tag, so a step mis-tagged SideWorkingTree when
// it writes the host's globals is perfectly self-consistent — every invariant
// still holds, every row still renders, and the MCP surface quietly writes the
// server operator's ~/.claude. Only a golden makes retagging a reviewed diff.
func TestOnboardSteps_SideGolden(t *testing.T) {
	const goldenPath = "testdata/steps.golden"
	got := renderStepTable(Steps())

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("step table drifted from %s.\n--- got ---\n%s--- want ---\n%s\n"+
			"If this change is intended, update the golden IN THE SAME COMMIT and say in the "+
			"message what a remote surface may now do that it could not before.",
			goldenPath, got, want)
	}

	// The golden pins the tags; this pins that Needs cannot name a step that
	// does not exist, which a golden alone would happily record.
	known := map[string]bool{}
	for _, s := range Steps() {
		known[s.Name] = true
	}
	for _, s := range Steps() {
		for _, n := range s.Needs {
			if !known[n] {
				t.Errorf("step %q needs %q, which is not in the table", s.Name, n)
			}
		}
	}
}

// assertEveryStepAccountedFor is the invariant, asserted from the outside.
func assertEveryStepAccountedFor(t *testing.T, res Result) {
	t.Helper()
	seen := map[string]int{}
	for _, oc := range res.Outcomes {
		if seen[oc.Step] == 0 {
			seen[oc.Step] = 1
		}
	}
	for _, om := range res.Omitted {
		seen[om.Step] += 2
	}
	for _, s := range Steps() {
		switch seen[s.Name] {
		case 1, 2:
		case 0:
			t.Errorf("step %q produced neither an outcome nor an omission", s.Name)
		default:
			t.Errorf("step %q both ran and was omitted", s.Name)
		}
	}
}

// TestOnboardRun_AccountsForEveryStep is the accounting gate over all three
// scopes a surface can present.
//
// The failure it exists to catch is not a crash: it is a run that quietly did
// less than it was asked and reported success. A missing row looks like a short
// table, and a short table looks fine.
func TestOnboardRun_AccountsForEveryStep(t *testing.T) {
	cases := []struct {
		name       string
		projectDir bool
		scope      func(Request) Scope
		wantOmit   []string
	}{
		{
			name:       "cli",
			projectDir: true,
			scope:      func(Request) Scope { return ScopeCLI },
			wantOmit:   nil,
		},
		{
			name:       "mcp-rooted-project-dir",
			projectDir: true,
			scope:      ScopeForMCP,
			// The working tree is admitted (go.mod in the directory itself),
			// so only the host-global step and the step that READS the host's
			// globals are refused.
			wantOmit: []string{"command-shims", "hook-wiring"},
		},
		{
			name:       "mcp-no-project-dir",
			projectDir: false,
			scope:      ScopeForMCP,
			wantOmit: []string{
				"cwd-project", "agent-wiring", "command-shims",
				"hook-wiring", "project-gitignore", "git-post-commit-hook",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sandboxHost(t)
			req, _ := newRequest(t, tc.projectDir)

			res, err := Run(context.Background(), req, tc.scope(req))
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			assertEveryStepAccountedFor(t, res)

			var gotOmit []string
			for _, om := range res.Omitted {
				gotOmit = append(gotOmit, om.Step)
			}
			slices.Sort(gotOmit)
			want := slices.Clone(tc.wantOmit)
			slices.Sort(want)
			if !slices.Equal(gotOmit, want) {
				t.Errorf("Omitted = %v, want %v", gotOmit, want)
			}
			if want := len(res.Omitted) == 0; res.Complete != want {
				t.Errorf("Complete = %v with %d omissions", res.Complete, len(res.Omitted))
			}

			// Every Omission must be actionable: a verbatim command, the host
			// it runs on, and the artifact that is missing. "Run vp init" tells
			// an operator on another machine nothing at all.
			for _, om := range res.Omitted {
				if om.Reason == "" {
					t.Errorf("omission %q has no reason", om.Step)
				}
				if !strings.Contains(om.Remedy, "run `vp ") {
					t.Errorf("omission %q remedy names no verbatim command: %q", om.Step, om.Remedy)
				}
				if !strings.Contains(om.Remedy, "on the machine") {
					t.Errorf("omission %q remedy names no host: %q", om.Step, om.Remedy)
				}
				if !strings.Contains(om.Remedy, " — it ") {
					t.Errorf("omission %q remedy names no artifact: %q", om.Step, om.Remedy)
				}
			}
		})
	}

	// The invariant is only worth stating if Run REFUSES when it is broken.
	t.Run("violation-is-an-error", func(t *testing.T) {
		sandboxHost(t)
		swapStepTable(t, func([]Step) {})
		stepTable = append(stepTable, Step{
			Name: "ghost-step",
			Side: SideWorkingTree,
			Run:  func(context.Context, Request) []Outcome { return nil },
		})
		req, _ := newRequest(t, true)
		res, err := Run(context.Background(), req, ScopeCLI)
		if err == nil {
			t.Fatalf("Run accepted a table in which ghost-step produced nothing; got %d outcomes, %d omissions",
				len(res.Outcomes), len(res.Omitted))
		}
		if !strings.Contains(err.Error(), "ghost-step") {
			t.Errorf("accounting error does not name the offending step: %v", err)
		}
	})
}

// TestOnboardRun_SkipsStepWhosePrerequisiteFailed proves the distinction the
// whole Omitted/Skip split rests on: Omitted means "this SURFACE may not run
// it", not "it did not get to run". A prerequisite failure is the second, so it
// is a Skip row and the step must NOT appear in Omitted — a caller that treated
// it as an omission would tell the operator to go run it on another machine.
func TestOnboardRun_SkipsStepWhosePrerequisiteFailed(t *testing.T) {
	sandboxHost(t)
	req, vaultDir := newRequest(t, true)

	swapStepTable(t, func(steps []Step) {
		for i := range steps {
			if steps[i].Name == "vault-project" {
				steps[i].Run = func(context.Context, Request) []Outcome {
					return []Outcome{{Status: Fail, Summary: "forced: vault-project config write failed"}}
				}
			}
		}
	})

	res, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var scaffold []Outcome
	for _, oc := range res.Outcomes {
		if oc.Step == "project-scaffold" {
			scaffold = append(scaffold, oc)
		}
	}
	if len(scaffold) != 1 {
		t.Fatalf("project-scaffold produced %d rows, want 1: %+v", len(scaffold), scaffold)
	}
	if scaffold[0].Status != Skip {
		t.Errorf("project-scaffold status = %v, want Skip", scaffold[0].Status)
	}
	if !strings.Contains(scaffold[0].Summary, "vault-project") {
		t.Errorf("skip row does not name the failed prerequisite: %q", scaffold[0].Summary)
	}
	for _, om := range res.Omitted {
		if om.Step == "project-scaffold" {
			t.Error("project-scaffold was recorded as Omitted; a failed prerequisite is a Skip, not an omission")
		}
	}
	if res.Complete {
		t.Error("Complete = true with a failed step")
	}

	// Nothing was scaffolded into a project whose config write just failed.
	if _, err := os.Stat(filepath.Join(vaultDir, "Projects", "alpha")); !os.IsNotExist(err) {
		t.Errorf("scaffold ran anyway: stat Projects/alpha = %v", err)
	}
	assertEveryStepAccountedFor(t, res)
}

// TestOnboardRun_OpenVaultFailureIsARow proves the memoization is STICKY.
//
// A vault that will not resolve is one fault, not one per step. Re-deriving it
// would report the same fault three times — and on a flapping filesystem could
// report it DIFFERENTLY each time, which is worse than reporting it once.
func TestOnboardRun_OpenVaultFailureIsARow(t *testing.T) {
	sandboxHost(t)
	req, _ := newRequest(t, true)

	const boom = "resolve vault: malformed .vibe-palace.toml"
	calls := 0
	req.OpenVault = func() (*storage.Vault, error) {
		calls++
		return nil, errors.New(boom)
	}

	res, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if calls != 1 {
		t.Errorf("OpenVault called %d times, want 1 — the resolver must be memoized, error and all", calls)
	}

	var fails []Outcome
	for _, oc := range res.Outcomes {
		if oc.Status == Fail {
			fails = append(fails, oc)
		}
	}
	if len(fails) != 1 {
		t.Fatalf("got %d Fail rows, want exactly 1: %+v", len(fails), fails)
	}
	if !strings.Contains(fails[0].Summary, boom) {
		t.Errorf("Fail row does not name the resolution error: %q", fails[0].Summary)
	}
	if fails[0].Step != "vault-project" {
		t.Errorf("Fail row attributed to %q, want the first SideVault step", fails[0].Step)
	}

	// Every OTHER vault-side step names the same cause in a Skip rather than
	// re-deriving it.
	for _, s := range Steps() {
		if s.Side != SideVault || s.Name == fails[0].Step {
			continue
		}
		var rows []Outcome
		for _, oc := range res.Outcomes {
			if oc.Step == s.Name {
				rows = append(rows, oc)
			}
		}
		if len(rows) != 1 || rows[0].Status != Skip {
			t.Fatalf("step %q produced %+v, want exactly one Skip", s.Name, rows)
		}
		if !strings.Contains(rows[0].Summary, boom) {
			t.Errorf("step %q skip does not name the vault error as the cause: %q", s.Name, rows[0].Summary)
		}
	}

	if res.Complete {
		t.Error("Complete = true despite a Fail row")
	}
	assertEveryStepAccountedFor(t, res)
}
