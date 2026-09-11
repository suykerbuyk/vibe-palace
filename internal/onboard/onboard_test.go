// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package onboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/hook"
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
	// Complete is not a failure signal — a surface with a narrowed Scope pins
	// it false for reasons that have nothing to do with a failure — so the
	// failure has to be readable on its own field.
	if res.OK() {
		t.Error("OK() = true with a failed step")
	}
	if got := res.Failed; !slices.Equal(got, []string{"vault-project"}) {
		t.Errorf("Failed = %v, want [vault-project]", got)
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

// treeSnapshot maps every file under root to the sha256 of its content, and
// every directory to an empty marker so a directory created without content
// still shows up in a diff.
//
// .surface is EXCLUDED. It is the vault write stamp: it records a timestamp and
// the identity of the writing binary, so it differs between two runs by
// construction. Including it would make "the second run changed nothing" a
// claim no correct implementation could ever satisfy.
func treeSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[filepath.ToSlash(rel)+"/"] = "<dir>"
			return nil
		}
		if d.Name() == ".surface" {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

// diffSnapshots renders the difference between two snapshots, newest first.
func diffSnapshots(before, after map[string]string) []string {
	var out []string
	for k, v := range after {
		if old, ok := before[k]; !ok {
			out = append(out, "added: "+k)
		} else if old != v {
			out = append(out, "changed: "+k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			out = append(out, "removed: "+k)
		}
	}
	slices.Sort(out)
	return out
}

func createdSteps(res Result) []string {
	var out []string
	for _, oc := range res.Outcomes {
		if oc.Created {
			out = append(out, oc.Step)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func outcomesFor(res Result, step string) []Outcome {
	var out []Outcome
	for _, oc := range res.Outcomes {
		if oc.Step == step {
			out = append(out, oc)
		}
	}
	return out
}

// TestOnboardRun_MalformedMarkerFails is the assertion the marker gate used to
// make unnecessary and its deletion made load-bearing.
//
// storage.PresentKeys is a line scanner. Given a .vibe-palace.toml that is not
// valid TOML it finds NO keys, so every canonical key reads as missing,
// storage.UpgradeConfig appends the template blocks to the malformed text, and
// CwdProjectReconciler.Apply reports Updated over a file that is still invalid.
// A step that trusted Apply's return value would render [pass] over a project
// whose vault can never be resolved.
func TestOnboardRun_MalformedMarkerFails(t *testing.T) {
	sandboxHost(t)
	req, _ := newRequest(t, true)

	cfgPath := filepath.Join(req.ProjectDir, ".vibe-palace.toml")
	const malformed = "this is not valid toml = = =\n[unclosed\n"
	if err := os.WriteFile(cfgPath, []byte(malformed), 0o644); err != nil {
		t.Fatalf("seed malformed marker: %v", err)
	}

	res, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertEveryStepAccountedFor(t, res)

	rows := outcomesFor(res, "cwd-project")
	if len(rows) != 1 {
		t.Fatalf("cwd-project produced %d rows, want 1: %+v", len(rows), rows)
	}
	if rows[0].Status != Fail {
		t.Fatalf("cwd-project status = %v, want Fail — a clean Apply is not proof the file parses", rows[0].Status)
	}
	if rows[0].Created {
		t.Error("a failed cwd-project must not claim it created anything")
	}
	if !strings.Contains(rows[0].Summary, cfgPath) {
		t.Errorf("Fail row does not name the file the operator has to fix: %q", rows[0].Summary)
	}
	// The VERBATIM parse error, not a paraphrase: "invalid TOML" tells the
	// operator nothing about which line to open.
	var probe map[string]any
	_, parseErr := toml.Decode(malformed, &probe)
	if parseErr == nil {
		t.Fatal("fixture is valid TOML; the test proves nothing")
	}
	if !strings.Contains(rows[0].Summary, parseErr.Error()) {
		t.Errorf("Fail row does not carry the parse error %q: %q", parseErr, rows[0].Summary)
	}
	if res.Complete {
		t.Error("Complete = true with a failed cwd-project")
	}

	// The operator's bytes survive: either untouched in place, or as the .bak
	// the host-local branch of reconcile.applyUpgrade writes before appending.
	recovered := false
	for _, p := range []string{cfgPath, cfgPath + ".bak"} {
		if data, rerr := os.ReadFile(p); rerr == nil && string(data) == malformed {
			recovered = true
		}
	}
	if !recovered {
		t.Errorf("the operator's original bytes are not recoverable from %s or %s.bak", cfgPath, cfgPath)
	}
}

// TestOnboardRun_SameBinaryTwiceConverges is the property `vp init` has always
// CLAIMED ("Re-run `vp init` anytime — it is idempotent") and, before the
// marker gate came out, delivered by refusing to do the work at all.
//
// The three assertions are deliberately in this shape:
//
//   - run 1 must report at least one Created outcome. "Run 1 wrote nothing new"
//     would be the wrong assertion — onboarding a fresh project is supposed to
//     create things, and a test that forbade it would pass over a broken run.
//   - run 2 must report NO Created and no Fail.
//   - the tree after run 2 is byte-identical to the tree after run 1. This is
//     the real property; the Created flags only say who noticed.
func TestOnboardRun_SameBinaryTwiceConverges(t *testing.T) {
	sandboxHost(t)
	req, vaultDir := newRequest(t, true)

	res1, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	assertEveryStepAccountedFor(t, res1)
	if got := createdSteps(res1); len(got) == 0 {
		t.Fatalf("run 1 reported no Created outcome at all; onboarding a fresh project must create something")
	}
	for _, oc := range res1.Outcomes {
		if oc.Status == Fail {
			t.Fatalf("run 1 Fail row on %s: %s", oc.Step, oc.Summary)
		}
	}

	vault1 := treeSnapshot(t, vaultDir)
	proj1 := treeSnapshot(t, req.ProjectDir)

	res2, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	assertEveryStepAccountedFor(t, res2)
	if got := createdSteps(res2); len(got) > 0 {
		t.Errorf("run 2 reported Created for %v; a converged run creates nothing", got)
	}
	for _, oc := range res2.Outcomes {
		if oc.Status == Fail {
			t.Errorf("run 2 Fail row on %s: %s", oc.Step, oc.Summary)
		}
	}

	if d := diffSnapshots(vault1, treeSnapshot(t, vaultDir)); len(d) > 0 {
		t.Errorf("run 2 changed the vault tree:\n  %s", strings.Join(d, "\n  "))
	}
	if d := diffSnapshots(proj1, treeSnapshot(t, req.ProjectDir)); len(d) > 0 {
		t.Errorf("run 2 changed the project tree:\n  %s", strings.Join(d, "\n  "))
	}
}

// TestOnboardRun_ConvergesStaleProject is the regression the whole task exists
// for, stated on the shape the OLD MCP vp_init actually left behind: a two-line
// .vibe-palace.toml, Projects/<slug>/tasks/{done,cancelled}, and nothing else.
//
// Run 1 must MUTATE. A test asserting "a stale project is left alone" would be
// asserting the bug: the marker gate saw the two-line file, declared the
// project onboarded, and made `vp init` a permanent no-op over a vault subtree
// that had no config.toml and no commands/skills scaffold at all.
func TestOnboardRun_ConvergesStaleProject(t *testing.T) {
	sandboxHost(t)
	req, vaultDir := newRequest(t, true)

	// The MCP-thin shape, verbatim: the hand-rolled body the old handler wrote
	// and the two task dirs it mkdir'd with discarded errors.
	cfgPath := filepath.Join(req.ProjectDir, ".vibe-palace.toml")
	if err := os.WriteFile(cfgPath, []byte("[project]\nname = \"alpha\"\n"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	for _, sub := range []string{"done", "cancelled"} {
		if err := os.MkdirAll(filepath.Join(vaultDir, "Projects", "alpha", "tasks", sub), 0o755); err != nil {
			t.Fatalf("seed tasks/%s: %v", sub, err)
		}
	}
	cfgVault := filepath.Join(vaultDir, "Projects", "alpha", "config.toml")
	if _, err := os.Stat(cfgVault); !os.IsNotExist(err) {
		t.Fatalf("fixture already has %s; the premise is wrong", cfgVault)
	}

	res1, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	assertEveryStepAccountedFor(t, res1)
	for _, oc := range res1.Outcomes {
		if oc.Status == Fail {
			t.Fatalf("run 1 Fail row on %s: %s", oc.Step, oc.Summary)
		}
	}

	// The three artifacts the stale shape is missing.
	for _, want := range []string{
		filepath.Join(vaultDir, "Projects", "alpha", "config.toml"),
		filepath.Join(vaultDir, "Projects", "alpha", "commands", "README.md"),
		filepath.Join(vaultDir, "Projects", "alpha", "skills", "README.md"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("run 1 left the stale project unrepaired: %s missing (%v)", want, err)
		}
	}
	if got := createdSteps(res1); !slices.Contains(got, "vault-project") {
		t.Errorf("run 1 Created steps = %v, want vault-project among them", got)
	}

	vault1 := treeSnapshot(t, vaultDir)
	proj1 := treeSnapshot(t, req.ProjectDir)

	res2, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if got := createdSteps(res2); len(got) > 0 {
		t.Errorf("run 2 reported Created for %v; the repair must converge", got)
	}
	if d := diffSnapshots(vault1, treeSnapshot(t, vaultDir)); len(d) > 0 {
		t.Errorf("run 2 changed the vault tree:\n  %s", strings.Join(d, "\n  "))
	}
	if d := diffSnapshots(proj1, treeSnapshot(t, req.ProjectDir)); len(d) > 0 {
		t.Errorf("run 2 changed the project tree:\n  %s", strings.Join(d, "\n  "))
	}
}

// allOmissions builds every Omission the table can produce, over both builders
// and both project-dir shapes, so a remedy assertion covers the whole space
// rather than whichever ones one scope happens to emit.
func allOmissions(dir string) []Omission {
	req := Request{ProjectDir: dir, Slug: "alpha"}
	var out []Omission
	for _, step := range Steps() {
		out = append(out, omitForSide(step, req))
		if step.ReadsHostGlobal {
			out = append(out, omitForHostRead(step, req))
		}
	}
	return out
}

// TestOnboardOmissions_AllCarryARemedy asserts on the CONTENT of every Remedy,
// not on its presence. An Omission whose remedy is "" or "TODO" is worse than
// no omission at all: it tells the operator something is missing and then
// refuses to say what to do about it.
func TestOnboardOmissions_AllCarryARemedy(t *testing.T) {
	for _, dir := range []string{"/tmp/example-project", ""} {
		for _, om := range allOmissions(dir) {
			r := strings.TrimSpace(om.Remedy)
			switch {
			case r == "":
				t.Errorf("omission %q (dir=%q) has an empty remedy", om.Step, dir)
				continue
			case strings.EqualFold(r, "todo"), strings.EqualFold(r, "n/a"), strings.EqualFold(r, om.Step):
				t.Errorf("omission %q (dir=%q) remedy is a placeholder: %q", om.Step, dir, r)
				continue
			}
			if !strings.Contains(r, "vp ") {
				t.Errorf("omission %q (dir=%q) remedy names no `vp` command: %q", om.Step, dir, r)
			}
			if !strings.Contains(r, "on the machine") {
				t.Errorf("omission %q (dir=%q) remedy names no host: %q", om.Step, dir, r)
			}
			if !strings.Contains(r, " — it ") {
				t.Errorf("omission %q (dir=%q) remedy names no artifact: %q", om.Step, dir, r)
			}
		}
	}
}

// TestOnboardOmission_CommandShimsRemedyStatesTheOverdelivery pins the one
// remedy whose substitute is not equivalent.
//
// shims.Reconcile suppresses the Claude command shims and the Claude skill
// shims whenever the host's user-global Claude surface is healthy; `vp commands
// upgrade` plans them unconditionally. An operator who follows the remedy and
// then finds files they were not told to expect has been surprised by their own
// repair, which is the failure mode this package's whole vocabulary exists to
// prevent.
func TestOnboardOmission_CommandShimsRemedyStatesTheOverdelivery(t *testing.T) {
	found := 0
	for _, om := range allOmissions("/tmp/example-project") {
		if om.Step != "command-shims" {
			continue
		}
		found++
		for _, want := range []string{"vp commands upgrade", "SUPERSET", "user-global Claude"} {
			if !strings.Contains(om.Remedy, want) {
				t.Errorf("command-shims remedy does not mention %q: %q", want, om.Remedy)
			}
		}
	}
	if found == 0 {
		t.Fatal("no command-shims omission was produced; the test proves nothing")
	}
}

// TestOnboardRun_WarnsAboutUpgradeCommands pins the advisory's three halves to
// their owners: stale shims to `vp commands upgrade`, and each vault
// Templates/ override of a built-in to its own reset verb. No upgrade resets
// an override any more, so no Templates line may name an upgrade command.
func TestOnboardRun_WarnsAboutUpgradeCommands(t *testing.T) {
	sandboxHost(t)
	req, _ := newRequest(t, true)

	res, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Advisories) == 0 {
		t.Fatal("Run produced no advisory; a re-init that silently declines to upgrade is the old bug in a new place")
	}

	var text []string
	for _, ad := range res.Advisories {
		text = append(text, ad.Summary)
		text = append(text, ad.Details...)
	}

	// Each half must be stated on the SAME line as its own command, and that
	// line must not name the other one — otherwise "both commands appear
	// somewhere in the blob" would pass over a row that pairs them wrongly.
	cases := []struct{ half, cmd, notCmd string }{
		{"Templates/commands", "vp commands reset", "vp skills reset"},
		{"vpc-*.md", "vp commands upgrade", "vp skills"},
		{"Templates/skills", "vp skills reset", "vp commands reset"},
	}
	for _, tc := range cases {
		ok := false
		for _, line := range text {
			if strings.Contains(line, tc.half) && strings.Contains(line, tc.cmd) && !strings.Contains(line, tc.notCmd) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("no advisory line pairs %q with %q (and only that command):\n  %s",
				tc.half, tc.cmd, strings.Join(text, "\n  "))
		}
	}

	// Onboarding READS vault Templates/ — the shim steps resolve vault-wide
	// commands and skills through it — so the advisory must not claim otherwise.
	for _, line := range text {
		if strings.Contains(line, "never reads") {
			t.Errorf("advisory claims init never reads something; the shim steps read vault Templates/: %q", line)
		}
	}

	// Templates/ is override-only, so a file there is the operator's own: each
	// Templates line must name the explicit reset that removes it and say a
	// backup is kept, must not name an upgrade command (none resets one), and
	// must not send the operator to run anything against "drift".
	for _, half := range []string{"Templates/commands", "Templates/skills"} {
		for _, line := range text {
			if !strings.Contains(line, half) {
				continue
			}
			if !strings.Contains(line, "reset NAME") || !strings.Contains(line, "a backup is kept") {
				t.Errorf("advisory line for %s does not name the reset and its backup: %q", half, line)
			}
			if strings.Contains(line, "upgrade") {
				t.Errorf("advisory line for %s names an upgrade command: %q", half, line)
			}
			if strings.Contains(line, "drift: run") {
				t.Errorf("advisory line for %s still says \"drift: run\": %q", half, line)
			}
		}
	}

	// And the advisory must reach the rendered table, not just the struct.
	var rendered strings.Builder
	for _, r := range Rows(res) {
		rendered.WriteString(r.Name + " " + r.Summary + " " + strings.Join(r.Details, " ") + "\n")
	}
	for _, want := range []string{"vp commands upgrade", "vp commands reset", "vp skills reset"} {
		if !strings.Contains(rendered.String(), want) {
			t.Errorf("Rows() dropped %q from the rendered table", want)
		}
	}
}

// TestOnboardRun_HealthyRunReportsNoFailure is the other half of
// TestOnboardRun_SkipsStepWhosePrerequisiteFailed: the failure signal has to be
// SILENT when nothing failed, or it is as useless as the constant it replaced.
//
// It runs under ScopeForMCP deliberately. Under ScopeCLI, Complete already
// carries the answer; under a narrowed scope Complete is pinned false by the
// omissions and OK is the only field left that varies with the run.
func TestOnboardRun_HealthyRunReportsNoFailure(t *testing.T) {
	sandboxHost(t)
	req, _ := newRequest(t, true)

	res, err := Run(context.Background(), req, ScopeForMCP(req))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	assertEveryStepAccountedFor(t, res)

	if !res.OK() || len(res.Failed) != 0 {
		t.Errorf("healthy run: OK = %v Failed = %v, want true/[]", res.OK(), res.Failed)
	}
	if res.Complete {
		t.Error("Complete = true under ScopeForMCP, which always omits hook-wiring and command-shims")
	}
	if len(res.Omitted) < 2 {
		t.Errorf("ScopeForMCP omitted %d steps, want at least hook-wiring and command-shims", len(res.Omitted))
	}
}

// TestOnboardRun_FailedIsDedupedAndInTableOrder pins the two properties a
// caller can act on: a step is named ONCE however many rows it emits, and the
// order is the step table's rather than a map's.
func TestOnboardRun_FailedIsDedupedAndInTableOrder(t *testing.T) {
	sandboxHost(t)
	req, _ := newRequest(t, true)

	// agent-wiring is the multi-row step that has no prerequisite, so failing
	// it cannot cascade into the Skip rows that would confuse the ordering
	// assertion. cwd-project runs BEFORE it in the table.
	swapStepTable(t, func(steps []Step) {
		for i := range steps {
			switch steps[i].Name {
			case "agent-wiring":
				steps[i].Run = func(context.Context, Request) []Outcome {
					return []Outcome{
						{Status: Fail, Summary: "forced: AGENTS.md"},
						{Status: Fail, Summary: "forced: CLAUDE.md"},
					}
				}
			case "cwd-project":
				steps[i].Run = func(context.Context, Request) []Outcome {
					return []Outcome{{Status: Fail, Summary: "forced: .vibe-palace.toml"}}
				}
			}
		}
	})

	res, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := res.Failed; !slices.Equal(got, []string{"cwd-project", "agent-wiring"}) {
		t.Errorf("Failed = %v, want [cwd-project agent-wiring] — deduped, in step-table order", got)
	}
}

// TestOnboardRun_ConvergedScaffoldDoesNotClaimItScaffolded is F4.
//
// stepProjectScaffold printed "scaffolded Projects/<slug>/{commands,skills}/"
// unconditionally. While the marker gate existed a re-init never reached the
// step, so the claim was only ever seen when it was true; deleting the gate
// made an inaccurate claim print on EVERY run after the first.
func TestOnboardRun_ConvergedScaffoldDoesNotClaimItScaffolded(t *testing.T) {
	sandboxHost(t)
	req, _ := newRequest(t, true)

	res1, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	first := outcomesFor(res1, "project-scaffold")
	if len(first) != 1 {
		t.Fatalf("run 1 project-scaffold produced %d rows, want 1: %+v", len(first), first)
	}
	if first[0].Status != Pass || !first[0].Created {
		t.Fatalf("run 1 project-scaffold = %v created=%v, want Pass/true", first[0].Status, first[0].Created)
	}
	if !strings.Contains(first[0].Summary, "scaffolded") {
		t.Errorf("run 1 must say it scaffolded: %q", first[0].Summary)
	}

	res2, err := Run(context.Background(), req, ScopeCLI)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	second := outcomesFor(res2, "project-scaffold")
	if len(second) != 1 {
		t.Fatalf("run 2 project-scaffold produced %d rows, want 1: %+v", len(second), second)
	}
	if second[0].Created {
		t.Error("run 2 project-scaffold claims Created over a converged tree")
	}
	if strings.Contains(second[0].Summary, "scaffolded") {
		t.Errorf("run 2 claims work it did not do: %q", second[0].Summary)
	}
	if !strings.Contains(second[0].Summary, "already present") {
		t.Errorf("run 2 summary does not say the tree was already there: %q", second[0].Summary)
	}
	// Info, like stepCommandShims' rep.Empty() branch: a Pass over a step that
	// did nothing reads as work performed.
	if second[0].Status != Info {
		t.Errorf("run 2 project-scaffold status = %v, want Info", second[0].Status)
	}
}

// hookEventClause extracts the slash-separated event list a remedy names, or ""
// when the text names no hook entries at all.
var hookEventClause = regexp.MustCompile(`the vp ([A-Za-z/]+) entries in ~/\.claude/settings\.json`)

// TestRemedy_HookEventsMatchValidEvents is the guard for F3.
//
// render.go named "SessionStart/SessionEnd" for as long as the remedy existed
// and vibe-palace has never installed a SessionStart hook — a grep for it over
// the whole tree hit that one line and nothing else. Omission's doc comment
// makes naming the missing artifact MANDATORY, so a remedy pointing at an entry
// that will never appear in settings.json is a contract breach: the operator
// runs the remedy, greps for what they were told to expect, and cannot tell
// whether the repair worked.
//
// The expectation is DERIVED from hook.ValidEvents and checked in both
// directions, so adding or retiring a hook event without updating the remedy
// fails here rather than shipping a lie.
func TestRemedy_HookEventsMatchValidEvents(t *testing.T) {
	want := make([]string, 0, len(hook.ValidEvents))
	for ev := range hook.ValidEvents {
		want = append(want, ev)
	}
	slices.Sort(want)

	checked := 0
	check := func(where, text string) {
		m := hookEventClause.FindStringSubmatch(text)
		if m == nil {
			// Only strings that actually name settings.json entries are in
			// scope; a remedy that names none is not making the claim.
			if strings.Contains(text, "settings.json") && strings.Contains(text, "the vp ") {
				t.Errorf("%s names settings.json entries in a shape this guard cannot read: %q", where, text)
			}
			return
		}
		checked++
		got := strings.Split(m[1], "/")
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s names hook events %v, but hook.ValidEvents is %v", where, got, want)
		}
	}

	for _, dir := range []string{"/tmp/example-project", ""} {
		check("stepArtifact(hook-wiring)", stepArtifact("hook-wiring", dir))
		for _, om := range allOmissions(dir) {
			check("omission "+om.Step+".Reason", om.Reason)
			check("omission "+om.Step+".Remedy", om.Remedy)
		}
	}
	if checked == 0 {
		t.Fatal("no remedy named any settings.json hook entry; the guard proves nothing")
	}
}

// TestOnboardOmission_CommandShimsRemedyNamesBothShimKinds is the other half of
// F3: shims.Reconcile writes command shims AND skill shims, and the remedy
// named only the commands while remedyCaveat two sentences later talked about
// the skill shims the operator had never been told about.
func TestOnboardOmission_CommandShimsRemedyNamesBothShimKinds(t *testing.T) {
	const dir = "/tmp/example-project"
	found := 0
	for _, om := range allOmissions(dir) {
		if om.Step != "command-shims" {
			continue
		}
		found++
		for _, want := range []string{
			filepath.Join(dir, ".claude", "commands", "vpc-*.md"),
			filepath.Join(dir, ".claude", "skills", "vps-*", "SKILL.md"),
		} {
			if !strings.Contains(om.Remedy, want) {
				t.Errorf("command-shims remedy does not name %q: %q", want, om.Remedy)
			}
		}
	}
	if found == 0 {
		t.Fatal("no command-shims omission was produced; the test proves nothing")
	}
}

// TestOnboardOmission_AbsentDirectoryIsNotCalledUnmarked is F7.
//
// HasRootedSignal returns false both for a directory that carries no marker and
// for one that does not exist, and the reason string collapsed the two: an
// operator whose path is absent was told to look for a .vibe-palace.toml, .git
// or manifest inside a directory they cannot open.
func TestOnboardOmission_AbsentDirectoryIsNotCalledUnmarked(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "no-such-dir")

	unmarked := t.TempDir() // exists, carries no marker

	reasonFor := func(dir string) string {
		for _, om := range allOmissions(dir) {
			if om.Side == SideWorkingTree {
				return om.Reason
			}
		}
		t.Fatalf("no working-tree omission produced for %q", dir)
		return ""
	}

	gotAbsent := reasonFor(absent)
	if !strings.Contains(gotAbsent, "does not exist") {
		t.Errorf("absent directory reason does not say so: %q", gotAbsent)
	}
	if strings.Contains(gotAbsent, "not provably a project root") {
		t.Errorf("absent directory is described as an unmarked one: %q", gotAbsent)
	}

	gotUnmarked := reasonFor(unmarked)
	if !strings.Contains(gotUnmarked, "not provably a project root") {
		t.Errorf("unmarked directory reason changed: %q", gotUnmarked)
	}
	if strings.Contains(gotUnmarked, "does not exist") {
		t.Errorf("an existing directory is described as absent: %q", gotUnmarked)
	}
}
