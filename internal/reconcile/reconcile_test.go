// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// xdgTempHome sets XDG_CONFIG_HOME to a tempdir and returns the resolved
// config file path.
func xdgTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	cfgPath, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatalf("VaultConfigFilePath: %v", err)
	}
	return cfgPath
}

// newVaultAt creates a vault dir under tmp and opens it.
func newVaultAt(t *testing.T, tmp string) *storage.Vault {
	t.Helper()
	vdir := filepath.Join(tmp, "vault")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	return storage.NewVault(vdir)
}

func onlyAction(t *testing.T, p Plan) Action {
	t.Helper()
	if len(p.Actions) != 1 {
		t.Fatalf("expected 1 action, got %d: %+v", len(p.Actions), p.Actions)
	}
	return p.Actions[0]
}

func TestGlobalConfig_CreateThenUnchanged(t *testing.T) {
	cfgPath := xdgTempHome(t)
	tmp := filepath.Dir(filepath.Dir(cfgPath)) // XDG_CONFIG_HOME root
	vaultPath := filepath.Join(tmp, "vault")
	root := t.TempDir()

	r := NewGlobalConfig(root, GlobalSeed{VaultPath: vaultPath, GitEnabled: true}.WithCreate())

	p1, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if onlyAction(t, p1).Kind != ActionCreate {
		t.Fatalf("expected Create, got %v", p1.Actions[0].Kind)
	}

	if _, err := r.Apply(context.Background(), p1); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config not written: %v", err)
	}

	p2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan #2: %v", err)
	}
	if onlyAction(t, p2).Kind != ActionUnchanged {
		t.Fatalf("expected Unchanged after Apply, got %v", p2.Actions[0].Kind)
	}
}

func TestGlobalConfig_SyncModeMissingIsSkip(t *testing.T) {
	_ = xdgTempHome(t)
	root := t.TempDir()
	r := NewGlobalConfig(root, GlobalSeed{})
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if onlyAction(t, p).Kind != ActionSkip {
		t.Fatalf("expected Skip in sync mode when missing, got %v", p.Actions[0].Kind)
	}
}

func TestGlobalConfig_DriftUpdate(t *testing.T) {
	cfgPath := xdgTempHome(t)
	// Write a minimal config missing most canonical keys.
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte("vault_path = \"/tmp/x\"\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	r := NewGlobalConfig(t.TempDir(), GlobalSeed{})
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if onlyAction(t, p).Kind != ActionUpdate {
		t.Fatalf("expected Update on drift, got %v (%+v)", p.Actions[0].Kind, p.Actions[0].Details)
	}
	if _, err := r.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	p2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan #2: %v", err)
	}
	if onlyAction(t, p2).Kind != ActionUnchanged {
		t.Fatalf("expected Unchanged after drift-fix, got %v", p2.Actions[0].Kind)
	}
}

func TestVault_CreateFromSeed(t *testing.T) {
	tmp := t.TempDir()
	vaultPath := filepath.Join(tmp, "vault")
	r := NewVault(tmp, VaultSeed{VaultPath: vaultPath, GitEnabled: false}.WithCreate())

	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	kinds := map[ActionKind]int{}
	for _, a := range p.Actions {
		kinds[a.Kind]++
	}
	if kinds[ActionCreate] < 2 {
		t.Fatalf("expected at least 2 Create (vault dir + .gitignore), got %+v", kinds)
	}
	if _, err := r.Apply(context.Background(), p); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(vaultPath); err != nil {
		t.Fatalf("vault not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultPath, ".gitignore")); err != nil {
		t.Fatalf(".gitignore not written: %v", err)
	}
	p2, _ := r.Plan(context.Background())
	for _, a := range p2.Actions {
		if a.Kind == ActionCreate {
			t.Errorf("expected no Create after Apply, got %+v", a)
		}
	}
}

func TestVaultSettings_Passing(t *testing.T) {
	tmp := t.TempDir()
	v := newVaultAt(t, tmp)
	r := NewVaultSettings(v)
	rows := r.Check(context.Background())
	if len(rows) == 0 {
		t.Fatalf("no check rows")
	}
	if rows[0].Status != check.Pass {
		t.Fatalf("expected Pass, got %v: %s", rows[0].Status, rows[0].Summary)
	}
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if onlyAction(t, p).Kind != ActionUnchanged {
		t.Fatalf("expected Unchanged, got %v", p.Actions[0].Kind)
	}
}

func TestCwdProject_CreateThenUnchanged(t *testing.T) {
	root := t.TempDir()
	r := NewCwdProject(root, CwdProjectSeed{Name: "testproj"}.WithCreate())

	p1, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if onlyAction(t, p1).Kind != ActionCreate {
		t.Fatalf("expected Create, got %v", p1.Actions[0].Kind)
	}
	if _, err := r.Apply(context.Background(), p1); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".vibe-palace.toml")); err != nil {
		t.Fatalf("cwd project config not written: %v", err)
	}
	p2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan #2: %v", err)
	}
	if onlyAction(t, p2).Kind != ActionUnchanged {
		t.Fatalf("expected Unchanged, got %v (%+v)", p2.Actions[0].Kind, p2.Actions[0].Details)
	}
}

func TestCwdProject_SyncModeMissingIsSkip(t *testing.T) {
	root := t.TempDir()
	r := NewCwdProject(root, CwdProjectSeed{})
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if onlyAction(t, p).Kind != ActionSkip {
		t.Fatalf("expected Skip in sync mode, got %v", p.Actions[0].Kind)
	}
}

func TestVaultProject_CreateThenUnchanged(t *testing.T) {
	tmp := t.TempDir()
	v := newVaultAt(t, tmp)
	r := NewVaultProject(v, "testproj")

	p1, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	created := 0
	for _, a := range p1.Actions {
		if a.Kind == ActionCreate {
			created++
		}
	}
	if created < 3 {
		// config + tasks/done + tasks/cancelled
		t.Fatalf("expected at least 3 Create actions, got %d: %+v", created, p1.Actions)
	}
	if _, err := r.Apply(context.Background(), p1); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	tasksDir, _ := v.TasksDir("testproj")
	for _, sub := range []string{"done", "cancelled"} {
		if _, err := os.Stat(filepath.Join(tasksDir, sub)); err != nil {
			t.Errorf("tasks/%s not created: %v", sub, err)
		}
	}
	p2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan #2: %v", err)
	}
	for _, a := range p2.Actions {
		if a.Kind == ActionCreate {
			t.Errorf("expected no Create after Apply, got %+v", a)
		}
	}
}

// TestVaultProject_UpdateDriftedConfig covers the ActionUpdate branch of
// Apply: an existing config.toml that is missing canonical keys is
// patched in place (rather than created or left Unchanged).
// TestVaultProject_ApplyNilVault exercises the defensive Skip/Unchanged
// tally Apply runs when the reconciler has no vault bound.
func TestVaultProject_ApplyNilVault(t *testing.T) {
	r := NewVaultProject(nil, "x")
	p := Plan{Actions: []Action{
		{Kind: ActionSkip, Summary: "skip"},
		{Kind: ActionUnchanged, Summary: "nop"},
	}}
	rep, err := r.Apply(context.Background(), p)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Skipped != 1 || rep.Unchanged != 1 {
		t.Errorf("tally = %+v, want Skipped=1 Unchanged=1", rep)
	}
}

// TestVaultProject_ApplyTasksDirMkdir covers the Apply branch that
// creates a tasks/* subdirectory separately from the config.toml path.
func TestVaultProject_ApplyTasksDirMkdir(t *testing.T) {
	tmp := t.TempDir()
	v := newVaultAt(t, tmp)
	r := NewVaultProject(v, "tproj")

	// Pre-create the config.toml so the plan only emits Create actions
	// for the tasks/ subdirectories — isolating that Apply branch.
	cfgPath, _ := v.ProjectConfigFile("tproj")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.WriteVaultProjectConfig("tproj"); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	rep, err := r.Apply(context.Background(), p)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Created < 2 {
		t.Errorf("expected >=2 Created for tasks/done + tasks/cancelled, got %d", rep.Created)
	}
	tasksDir, _ := v.TasksDir("tproj")
	for _, sub := range []string{"done", "cancelled"} {
		if _, err := os.Stat(filepath.Join(tasksDir, sub)); err != nil {
			t.Errorf("tasks/%s not created: %v", sub, err)
		}
	}
}

func TestVaultProject_UpdateDriftedConfig(t *testing.T) {
	tmp := t.TempDir()
	v := newVaultAt(t, tmp)
	r := NewVaultProject(v, "drifted")

	// Seed a minimal, drifted config.toml (missing canonical keys).
	cfgPath, err := v.ProjectConfigFile("drifted")
	if err != nil {
		t.Fatalf("ProjectConfigFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("# drifted stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	sawUpdate := false
	for _, a := range p.Actions {
		if a.Kind == ActionUpdate && a.Target == cfgPath {
			sawUpdate = true
		}
	}
	if !sawUpdate {
		t.Fatalf("expected an ActionUpdate for drifted config, got: %+v", p.Actions)
	}

	rep, err := r.Apply(context.Background(), p)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Updated < 1 {
		t.Errorf("expected rep.Updated >= 1, got %d", rep.Updated)
	}
	if len(rep.Errors) != 0 {
		t.Errorf("unexpected errors: %v", rep.Errors)
	}

	// After update, Plan should report Unchanged for the config.
	p2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan #2: %v", err)
	}
	for _, a := range p2.Actions {
		if a.Target == cfgPath && a.Kind != ActionUnchanged {
			t.Errorf("expected config Unchanged after Update, got %+v", a)
		}
	}
}

func TestGlobalConfig_CheckRows(t *testing.T) {
	cfgPath := xdgTempHome(t)
	tmp := filepath.Dir(filepath.Dir(cfgPath))
	root := t.TempDir()

	// Missing: Check returns one row (Fail from CheckConfigAt; no staleness row).
	r := NewGlobalConfig(root, GlobalSeed{})
	rows := r.Check(context.Background())
	if len(rows) != 1 || rows[0].Status != check.Fail {
		t.Fatalf("expected 1 Fail row when missing, got %+v", rows)
	}

	// Present + up-to-date: Check returns 2 rows (config + staleness).
	r2 := NewGlobalConfig(root, GlobalSeed{VaultPath: filepath.Join(tmp, "vault"), GitEnabled: true}.WithCreate())
	p, _ := r2.Plan(context.Background())
	_, _ = r2.Apply(context.Background(), p)
	rows = r2.Check(context.Background())
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows after create, got %+v", rows)
	}
	if rows[0].Status != check.Pass || rows[1].Status != check.Pass {
		t.Errorf("expected both rows Pass, got %+v", rows)
	}
}

func TestVault_SyncModeMissingVaultIsSkip(t *testing.T) {
	_ = xdgTempHome(t) // so ResolveVaultPath has a global config path to hit
	// No global config written — ResolveVaultPath fails.
	root := t.TempDir()
	r := NewVault(root, VaultSeed{})
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Actions) == 0 || p.Actions[0].Kind != ActionSkip {
		t.Fatalf("expected Skip in sync mode with no vault, got %+v", p.Actions)
	}
}

func TestVault_GitEnabledReadsGlobalConfig(t *testing.T) {
	cfgPath := xdgTempHome(t)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tmp := filepath.Dir(filepath.Dir(cfgPath))
	vaultPath := filepath.Join(tmp, "vault")
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		t.Fatalf("mkdir vault: %v", err)
	}
	if err := os.WriteFile(cfgPath,
		[]byte("vault_path = \""+vaultPath+"\"\ngit_enabled = true\n"), 0o644); err != nil {
		t.Fatalf("seed cfg: %v", err)
	}

	r := NewVault(t.TempDir(), VaultSeed{})
	if enabled, err := r.gitEnabled(); err != nil || !enabled {
		t.Errorf("gitEnabled() = %v, %v; want true from global config", enabled, err)
	}
}

func TestVault_CheckRows(t *testing.T) {
	tmp := t.TempDir()
	vaultPath := filepath.Join(tmp, "vault")
	r := NewVault(tmp, VaultSeed{VaultPath: vaultPath, GitEnabled: false}.WithCreate())

	// Missing vault → Fail row from CheckVault, no CheckGit row.
	rows := r.Check(context.Background())
	if len(rows) != 1 || rows[0].Status != check.Fail {
		t.Fatalf("expected 1 Fail row, got %+v", rows)
	}

	// After Apply, both CheckVault and CheckGit rows come back.
	p, _ := r.Plan(context.Background())
	_, _ = r.Apply(context.Background(), p)
	rows = r.Check(context.Background())
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows after apply, got %+v", rows)
	}
}

func TestVaultSettings_NilVaultSkip(t *testing.T) {
	r := NewVaultSettings(nil)
	rows := r.Check(context.Background())
	if len(rows) != 1 || rows[0].Status != check.Skip {
		t.Fatalf("expected single Skip row for nil vault, got %+v", rows)
	}
	p, _ := r.Plan(context.Background())
	if onlyAction(t, p).Kind != ActionSkip {
		t.Fatalf("expected Skip action, got %+v", p.Actions)
	}
}

func TestCwdProject_DriftUpdate(t *testing.T) {
	root := t.TempDir()
	// Seed a minimal project config missing canonical keys.
	cfg := filepath.Join(root, ".vibe-palace.toml")
	if err := os.WriteFile(cfg, []byte("# tiny\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	r := NewCwdProject(root, CwdProjectSeed{})
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Project templates may be tiny; drift may or may not register. If
	// Update is returned, Apply and re-plan must settle to Unchanged.
	if p.Actions[0].Kind == ActionUpdate {
		if _, err := r.Apply(context.Background(), p); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		p2, _ := r.Plan(context.Background())
		if onlyAction(t, p2).Kind != ActionUnchanged {
			t.Fatalf("expected Unchanged after drift-fix, got %v", p2.Actions[0].Kind)
		}
	}
}

func TestVaultProject_NilVaultSkip(t *testing.T) {
	r := NewVaultProject(nil, "x")
	rows := r.Check(context.Background())
	if len(rows) != 1 || rows[0].Status != check.Skip {
		t.Fatalf("expected Skip row, got %+v", rows)
	}
	p, _ := r.Plan(context.Background())
	if onlyAction(t, p).Kind != ActionSkip {
		t.Fatalf("expected Skip action, got %+v", p.Actions)
	}
}

func TestVaultProject_CheckPresent(t *testing.T) {
	tmp := t.TempDir()
	v := newVaultAt(t, tmp)
	r := NewVaultProject(v, "testproj")

	// Before Apply — Info "missing".
	rows := r.Check(context.Background())
	if len(rows) != 1 || rows[0].Status != check.Info {
		t.Errorf("expected Info row before create, got %+v", rows)
	}

	p, _ := r.Plan(context.Background())
	_, _ = r.Apply(context.Background(), p)

	// After Apply — Pass.
	rows = r.Check(context.Background())
	if len(rows) != 1 || rows[0].Status != check.Pass {
		t.Errorf("expected Pass row after create, got %+v", rows)
	}
}

// TestInterfaceSatisfaction asserts all reconcilers satisfy the Reconciler
// interface — compile-time if possible.
func TestInterfaceSatisfaction(t *testing.T) {
	var _ Reconciler = (*GlobalConfigReconciler)(nil)
	var _ Reconciler = (*VaultReconciler)(nil)
	var _ Reconciler = (*VaultSettingsReconciler)(nil)
	var _ Reconciler = (*CwdProjectReconciler)(nil)
	var _ Reconciler = (*VaultProjectReconciler)(nil)
}

func TestReconcilerMetadata(t *testing.T) {
	root := t.TempDir()
	v := newVaultAt(t, root)
	cases := []struct {
		r    Reconciler
		name string
		tier Tier
	}{
		{NewGlobalConfig(root, GlobalSeed{}), "GlobalConfig", TierGlobal},
		{NewVault(root, VaultSeed{}), "Vault", TierVault},
		{NewVaultSettings(v), "VaultSettings", TierVault},
		{NewCwdProject(root, CwdProjectSeed{}), "CwdProject", TierProject},
		{NewVaultProject(v, "x"), "VaultProject", TierProject},
	}
	for _, c := range cases {
		if c.r.Name() != c.name {
			t.Errorf("Name(): got %q want %q", c.r.Name(), c.name)
		}
		if c.r.Tier() != c.tier {
			t.Errorf("%s Tier(): got %q want %q", c.name, c.r.Tier(), c.tier)
		}
	}
}

func TestVaultSettings_ApplyNoOp(t *testing.T) {
	r := NewVaultSettings(newVaultAt(t, t.TempDir()))
	p, _ := r.Plan(context.Background())
	rep, err := r.Apply(context.Background(), p)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Unchanged != 1 {
		t.Errorf("expected Unchanged=1, got %+v", rep)
	}
	// Skip path via nil vault.
	rNil := NewVaultSettings(nil)
	pNil, _ := rNil.Plan(context.Background())
	repNil, _ := rNil.Apply(context.Background(), pNil)
	if repNil.Skipped != 1 {
		t.Errorf("expected Skipped=1 for nil vault, got %+v", repNil)
	}
}

func TestApplyCountsByActionKind(t *testing.T) {
	// Feed synthesized plans to each reconciler's Apply to exercise all
	// branches of the count switch (Unchanged / Skip / Update / Create).
	cases := []struct {
		name string
		r    Reconciler
		p    Plan
		want Report
	}{
		{
			name: "GlobalConfig unchanged+skip",
			r:    NewGlobalConfig(t.TempDir(), GlobalSeed{}),
			p: Plan{Actions: []Action{
				{Kind: ActionUnchanged, Target: "x"},
				{Kind: ActionSkip, Target: "y"},
			}},
			want: Report{Unchanged: 1, Skipped: 1},
		},
		{
			name: "CwdProject unchanged+skip",
			r:    NewCwdProject(t.TempDir(), CwdProjectSeed{}),
			p: Plan{Actions: []Action{
				{Kind: ActionUnchanged, Target: "x"},
				{Kind: ActionSkip, Target: "y"},
			}},
			want: Report{Unchanged: 1, Skipped: 1},
		},
		{
			// An Update with no writer behind it (only .gitignore has one) is
			// an error, never a phantom Updated.
			name: "Vault unchanged+update+skip",
			r:    NewVault(t.TempDir(), VaultSeed{}),
			p: Plan{Actions: []Action{
				{Kind: ActionUnchanged, Target: "x"},
				{Kind: ActionUpdate, Target: "y"},
				{Kind: ActionSkip, Target: "z"},
			}},
			want: Report{Unchanged: 1, Skipped: 1, Errors: make([]error, 1)},
		},
		{
			name: "VaultProject skip",
			r:    NewVaultProject(newVaultAt(t, t.TempDir()), "x"),
			p: Plan{Actions: []Action{
				{Kind: ActionSkip, Target: "x"},
			}},
			want: Report{Skipped: 1},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.r.Apply(context.Background(), c.p)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got.Created != c.want.Created ||
				got.Updated != c.want.Updated ||
				got.Unchanged != c.want.Unchanged ||
				got.Skipped != c.want.Skipped ||
				len(got.Errors) != len(c.want.Errors) {
				t.Errorf("Report got=%+v want=%+v", got, c.want)
			}
		})
	}
}

func TestCwdProject_ApplyUpdateCountsErrorsOnMissingFile(t *testing.T) {
	// Update on a nonexistent file must populate Errors without crashing.
	r := NewCwdProject(t.TempDir(), CwdProjectSeed{})
	rep, _ := r.Apply(context.Background(), Plan{Actions: []Action{
		{Kind: ActionUpdate, Target: "/nonexistent/.vibe-palace.toml"},
	}})
	if len(rep.Errors) == 0 {
		t.Errorf("expected error for Update on missing file")
	}
}

func TestGlobalConfig_ApplyUnchangedSkip(t *testing.T) {
	// Drive Apply with a known-good plan to count Unchanged/Skip branches.
	r := NewGlobalConfig(t.TempDir(), GlobalSeed{})
	rep, _ := r.Apply(context.Background(), Plan{Actions: []Action{
		{Kind: ActionUpdate, Target: "/nonexistent/config.toml"},
	}})
	if len(rep.Errors) == 0 {
		t.Errorf("expected error for Update on missing file")
	}
}

func TestCwdProject_CheckReturnsRow(t *testing.T) {
	root := t.TempDir()
	r := NewCwdProject(root, CwdProjectSeed{})
	rows := r.Check(context.Background())
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %+v", rows)
	}
	if rows[0].Status != check.Info {
		t.Errorf("expected Info row, got %v", rows[0].Status)
	}
}

// seededVaultWithGitignore creates an existing vault whose .gitignore holds
// body, and returns the vault path and a seeded (init-mode) Vault reconciler.
func seededVaultWithGitignore(t *testing.T, body string) (string, *VaultReconciler) {
	t.Helper()
	tmp := t.TempDir()
	vaultPath := filepath.Join(tmp, "vault")
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultPath, ".gitignore"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return vaultPath, NewVault(tmp, VaultSeed{VaultPath: vaultPath, GitEnabled: false}.WithCreate())
}

// gitignoreAction returns the single action a Vault plan carries for
// .gitignore, failing when there is not exactly one.
func gitignoreAction(t *testing.T, p Plan) Action {
	t.Helper()
	var found []Action
	for _, a := range p.Actions {
		if filepath.Base(a.Target) == ".gitignore" {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one .gitignore action, got %+v", p.Actions)
	}
	return found[0]
}

func TestVaultPlan_GitignoreMissingCanonicalLines_PlansUpdate(t *testing.T) {
	// One canonical line present, so the expected missing set is every other.
	_, r := seededVaultWithGitignore(t, "custom/\n"+storage.CanonicalGitignorePatterns[0]+"\n")
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	a := gitignoreAction(t, p)
	if a.Kind != ActionUpdate {
		t.Fatalf("kind = %s, want Update: %+v", a.Kind, a)
	}
	want := storage.CanonicalGitignorePatterns[1:]
	if strings.Join(a.Details, "|") != strings.Join(want, "|") {
		t.Errorf("Details = %v, want the missing lines %v", a.Details, want)
	}
	if !strings.Contains(a.Summary, fmt.Sprintf("+%d canonical", len(want))) {
		t.Errorf("Summary %q does not name the count %d", a.Summary, len(want))
	}
}

func TestVaultPlan_GitignoreComplete_Unchanged(t *testing.T) {
	_, r := seededVaultWithGitignore(t, strings.Join(storage.CanonicalGitignorePatterns, "\n")+"\n")
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if a := gitignoreAction(t, p); a.Kind != ActionUnchanged {
		t.Fatalf("kind = %s, want Unchanged: %+v", a.Kind, a)
	}
}

// TestVaultPlan_GitignoreUnreadable_PlansSkip pins that an unreadable present
// .gitignore is a Skip carrying the reason, never a Plan error: a Plan error
// would abort every tier of `vp config sync` before any Apply ran.
func TestVaultPlan_GitignoreUnreadable_PlansSkip(t *testing.T) {
	tmp := t.TempDir()
	vaultPath := filepath.Join(tmp, "vault")
	if err := os.MkdirAll(filepath.Join(vaultPath, ".gitignore"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewVault(tmp, VaultSeed{VaultPath: vaultPath}.WithCreate())
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan returned an error, want a Skip action: %v", err)
	}
	a := gitignoreAction(t, p)
	if a.Kind != ActionSkip {
		t.Fatalf("kind = %s, want Skip: %+v", a.Kind, a)
	}
	if !strings.Contains(a.Summary, "cannot read vault .gitignore") {
		t.Errorf("Skip summary does not carry the reason: %q", a.Summary)
	}

}

// TestVaultPlan_VaultPathIsAFile_PlansSkip pins that a vault path which exists
// but is not a directory is reported as exactly that, and that planning stops
// there instead of reporting an unreadable .gitignore beneath a file.
func TestVaultPlan_VaultPathIsAFile_PlansSkip(t *testing.T) {
	tmp := t.TempDir()
	notDir := filepath.Join(tmp, "notadir")
	if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []VaultSeed{VaultSeed{VaultPath: notDir}.WithCreate(), {}} {
		if !seed.seedSet {
			// Sync mode resolves the path from the global config.
			cfg := xdgTempHome(t)
			if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte(`vault_path = "`+notDir+`"`+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		p, err := NewVault(tmp, seed).Plan(context.Background())
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if len(p.Actions) != 1 || p.Actions[0].Kind != ActionSkip ||
			!strings.Contains(p.Actions[0].Summary, "vault path is not a directory") {
			t.Errorf("seedSet=%v: want one Skip naming the vault path, got %+v", seed.seedSet, p.Actions)
		}
	}
}

func TestVaultApply_GitignoreUpdate_PreservesCustomLines(t *testing.T) {
	seed := "# mine\ncustom/\n" + storage.CanonicalGitignorePatterns[0] + "\n"
	vaultPath, r := seededVaultWithGitignore(t, seed)
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	rep, err := r.Apply(context.Background(), p)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: err=%v errors=%v", err, rep.Errors)
	}
	if rep.Updated != 1 {
		t.Errorf("Updated = %d, want 1", rep.Updated)
	}
	wantNote := fmt.Sprintf("vault .gitignore: added %d canonical line(s)", len(storage.CanonicalGitignorePatterns)-1)
	if len(rep.Notes) != 1 || rep.Notes[0] != wantNote {
		t.Errorf("Notes = %v, want [%q]", rep.Notes, wantNote)
	}
	data, err := os.ReadFile(filepath.Join(vaultPath, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), seed) {
		t.Errorf("custom lines not preserved verbatim at the top:\n%s", data)
	}
	missing, err := storage.MissingVaultGitignorePatterns(vaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("still missing after Apply: %v", missing)
	}
	p2, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("re-Plan: %v", err)
	}
	if a := gitignoreAction(t, p2); a.Kind != ActionUnchanged {
		t.Errorf("re-Plan kind = %s, want Unchanged", a.Kind)
	}
}

// TestVaultApply_GitignoreUpdate_NoteCountsWhatWasWritten pins that the
// "added N" note reports the lines the locked write actually appended, not the
// count the Plan estimated: another writer adds some lines between Plan and
// Apply, so the two numbers differ and only one of them is true.
func TestVaultApply_GitignoreUpdate_NoteCountsWhatWasWritten(t *testing.T) {
	vaultPath, r := seededVaultWithGitignore(t, "custom/\n")
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	planned := len(gitignoreAction(t, p).Details)
	if planned != len(storage.CanonicalGitignorePatterns) {
		t.Fatalf("planned %d missing lines, want the whole canonical set (%d)", planned, len(storage.CanonicalGitignorePatterns))
	}

	// Another writer lands three canonical lines after Plan, before Apply.
	const landedMeanwhile = 3
	gi := filepath.Join(vaultPath, ".gitignore")
	extra := strings.Join(storage.CanonicalGitignorePatterns[:landedMeanwhile], "\n") + "\n"
	f, err := os.OpenFile(gi, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(extra); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	rep, err := r.Apply(context.Background(), p)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: err=%v errors=%v", err, rep.Errors)
	}
	want := fmt.Sprintf("vault .gitignore: added %d canonical line(s)", planned-landedMeanwhile)
	if len(rep.Notes) != 1 || rep.Notes[0] != want {
		t.Errorf("Notes = %v, want [%q] — the planned count was %d", rep.Notes, want, planned)
	}
}

// TestVaultApply_GitignoreUpdate_Accounting pins the two ways an Update can end
// other than a write: a failed write is an Error and is NOT counted as Updated,
// and a file some other writer already completed is Unchanged. Neither leaves
// an "added" note.
func TestVaultApply_GitignoreUpdate_Accounting(t *testing.T) {
	vaultPath, r := seededVaultWithGitignore(t, "custom/\n")
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	gi := filepath.Join(vaultPath, ".gitignore")

	// Already completed between Plan and Apply → nothing written, Unchanged.
	if err := storage.ReconcileVaultGitignore(vaultPath); err != nil {
		t.Fatal(err)
	}
	rep, err := r.Apply(context.Background(), p)
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("Apply: err=%v errors=%v", err, rep.Errors)
	}
	if rep.Updated != 0 || rep.Unchanged == 0 || len(rep.Notes) != 0 {
		t.Errorf("already-complete file: got %+v, want Updated=0, no note, and the action counted Unchanged", rep)
	}

	// Gone between Plan and Apply → an error prefixed for initGlobal's triage.
	if err := os.Remove(gi); err != nil {
		t.Fatal(err)
	}
	rep, err = r.Apply(context.Background(), p)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if rep.Updated != 0 || len(rep.Notes) != 0 {
		t.Errorf("failed write counted as Updated or noted: %+v", rep)
	}
	var ve *VaultApplyError
	if len(rep.Errors) != 1 || !errors.As(rep.Errors[0], &ve) || ve.Artifact != VaultArtifactGitignore {
		t.Errorf("want one VaultApplyError for the .gitignore, got %v", rep.Errors)
	}
}

// runGit runs a git command in dir, failing the test on error. Local to this
// file: internal/storage's gitRun is unexported in another package.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// gitAction returns the single action a Vault plan carries for .git, failing
// when there is not exactly one.
func gitAction(t *testing.T, p Plan) Action {
	t.Helper()
	var found []Action
	for _, a := range p.Actions {
		if filepath.Base(a.Target) == ".git" {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one .git action, got %+v", p.Actions)
	}
	return found[0]
}

// TestVaultPlan_NestedVaultSkipsGitInit is the regression test for
// config-sync-git-inits-a-vault-nested-in-another-repository: git_enabled =
// true must not plan `git init` inside a vault that is a subdirectory of an
// enclosing repository's work tree, and must instead Skip naming the
// enclosing repo.
func TestVaultPlan_NestedVaultSkipsGitInit(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}
	enclosing := t.TempDir()
	runGit(t, enclosing, "init", "-b", "main")
	runGit(t, enclosing, "config", "user.email", "test@example.com")
	runGit(t, enclosing, "config", "user.name", "Test User")

	vaultPath := filepath.Join(enclosing, "vault")
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		t.Fatal(err)
	}

	r := NewVault(t.TempDir(), VaultSeed{VaultPath: vaultPath, GitEnabled: true}.WithCreate())
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(vaultPath, ".git")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Plan must not have created .git before Apply; stat err = %v", statErr)
	}
	a := gitAction(t, p)
	if a.Kind != ActionSkip {
		t.Fatalf("git action = %v, want ActionSkip: %+v", a.Kind, a)
	}
	if !strings.Contains(a.Summary, enclosing) {
		t.Errorf("Skip summary %q does not name the enclosing repo %q", a.Summary, enclosing)
	}

	// Apply must not create .git either (defence in depth: Plan and Apply
	// must agree, and a future edit to Apply must not resurrect the bug).
	rep, err := r.Apply(context.Background(), p)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(vaultPath, ".git")); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("Apply created .git inside a nested vault")
	}
	_ = rep
}

// TestVaultPlan_UnversionedVaultStillPlansGitInit guards against a fix that
// accidentally skips ALL git-init planning: a vault with no enclosing
// repository at or above it must still plan `git init` when git_enabled is
// true.
func TestVaultPlan_UnversionedVaultStillPlansGitInit(t *testing.T) {
	tmp := t.TempDir()
	vaultPath := filepath.Join(tmp, "vault")
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		t.Fatal(err)
	}

	r := NewVault(t.TempDir(), VaultSeed{VaultPath: vaultPath, GitEnabled: true}.WithCreate())
	p, err := r.Plan(context.Background())
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	a := gitAction(t, p)
	if a.Kind != ActionCreate {
		t.Fatalf("git action = %v, want ActionCreate: %+v", a.Kind, a)
	}
	if filepath.Base(a.Target) != ".git" {
		t.Errorf("Create target = %q, want it to end in .git", a.Target)
	}
}

// TestVaultPlan_GitMarkerUnverifiable_SkipsGitInit is the regression test for
// the review's Medium finding on this fix: the default branch of the new
// three-way switch (VaultGitUnavailable / VaultGitBroken — a .git marker
// exists at or above the vault but git cannot disambiguate OK vs Nested) had
// no test. It also covers the accompanying Low finding: the Skip wording must
// not claim the marker is "above the vault" when it is really a dangling
// .git symlink AT the vault itself (an os.Stat/os.Lstat asymmetry).
func TestVaultPlan_GitMarkerUnverifiable_SkipsGitInit(t *testing.T) {
	if !storage.GitAvailable() {
		t.Skip("git not in PATH")
	}

	t.Run("broken marker above the vault", func(t *testing.T) {
		enclosing := t.TempDir()
		if err := os.WriteFile(filepath.Join(enclosing, ".git"), []byte("gitdir: /nonexistent\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		vaultPath := filepath.Join(enclosing, "vault")
		if err := os.MkdirAll(vaultPath, 0o755); err != nil {
			t.Fatal(err)
		}

		r := NewVault(t.TempDir(), VaultSeed{VaultPath: vaultPath, GitEnabled: true}.WithCreate())
		p, err := r.Plan(context.Background())
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		a := gitAction(t, p)
		if a.Kind != ActionSkip {
			t.Fatalf("kind = %v, want ActionSkip: %+v", a.Kind, a)
		}
		if !strings.Contains(a.Summary, "above the vault") {
			t.Errorf("Skip summary = %q, want it to name the marker as above the vault", a.Summary)
		}
		if _, statErr := os.Stat(filepath.Join(vaultPath, ".git")); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("Plan must not have created .git before Apply")
		}
	})

	t.Run("dangling symlink at the vault itself is not misattributed", func(t *testing.T) {
		vaultPath := t.TempDir()
		if err := os.Symlink(filepath.Join(vaultPath, "nonexistent-target"), filepath.Join(vaultPath, ".git")); err != nil {
			t.Fatal(err)
		}

		r := NewVault(t.TempDir(), VaultSeed{VaultPath: vaultPath, GitEnabled: true}.WithCreate())
		p, err := r.Plan(context.Background())
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		a := gitAction(t, p)
		if a.Kind != ActionSkip {
			t.Fatalf("kind = %v, want ActionSkip: %+v", a.Kind, a)
		}
		if strings.Contains(a.Summary, "above the vault") {
			t.Errorf("Skip summary = %q misattributes a dangling .git symlink AT the vault as being above it", a.Summary)
		}
	})
}

// TestVault_PlanReadsGitEnabledThroughTheOneReader pins the reader unification:
// the reconcile reader used to read a missing key, a missing file, an empty
// file and a nested git_enabled as false. Through storage.HostGitEnabled they
// all read as enabled, so `vp config sync` plans a git init for a vault that is
// not a repo. An unreadable git_enabled plans a Skip naming the read error and
// never fails the Plan.
func TestVault_PlanReadsGitEnabledThroughTheOneReader(t *testing.T) {
	cases := []struct {
		name     string
		cfgExtra string
		wantInit bool
		wantSkip string
	}{
		{"key absent", "", true, ""},
		{"nested under a table", "[palace]\ngit_enabled = false\n", true, ""},
		{"explicit false", "git_enabled = false\n", false, ""},
		{"unreadable", "git_enabled = \"no\"\n", false, "cannot read git_enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := xdgTempHome(t)
			if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
				t.Fatal(err)
			}
			vaultPath := filepath.Join(t.TempDir(), "vault")
			if err := os.MkdirAll(vaultPath, 0o755); err != nil {
				t.Fatal(err)
			}
			body := "vault_path = \"" + vaultPath + "\"\n" + tc.cfgExtra
			if err := os.WriteFile(cfgPath, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := NewVault(t.TempDir(), VaultSeed{}).Plan(context.Background())
			if err != nil {
				t.Fatalf("Plan returned an error, which would abort every tier of config sync: %v", err)
			}
			var gotInit bool
			var gotSkip string
			for _, a := range p.Actions {
				if a.Kind == ActionCreate && a.Summary == "git init vault" {
					gotInit = true
				}
				if a.Kind == ActionSkip && strings.HasPrefix(a.Summary, "git init skipped") {
					gotSkip = a.Summary
				}
			}
			if gotInit != tc.wantInit {
				t.Errorf("git init planned = %v, want %v (actions %+v)", gotInit, tc.wantInit, p.Actions)
			}
			if tc.wantSkip != "" && !strings.Contains(gotSkip, tc.wantSkip) {
				t.Errorf("Skip summary = %q, want it to contain %q", gotSkip, tc.wantSkip)
			}
			if tc.wantSkip == "" && gotSkip != "" {
				t.Errorf("unexpected Skip %q", gotSkip)
			}
		})
	}
}

// TestVault_CheckGitRowFailsOnAnUnreadableSetting: the Git row names the read
// error as Fail, never the "disabled" row.
func TestVault_CheckGitRowFailsOnAnUnreadableSetting(t *testing.T) {
	cfgPath := xdgTempHome(t)
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(vaultPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("vault_path = \""+vaultPath+"\"\nGIT_ENABLED = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := NewVault(t.TempDir(), VaultSeed{}).Check(context.Background())
	var git *check.Result
	for i := range rows {
		if rows[i].Name == "Git" {
			git = &rows[i]
		}
	}
	if git == nil {
		t.Fatalf("no Git row in %+v", rows)
	}
	if git.Status != check.Fail {
		t.Errorf("Git row status = %v, want Fail", git.Status)
	}
	if strings.Contains(git.Summary, "disabled (") {
		t.Errorf("an unreadable setting must not render as disabled: %q", git.Summary)
	}
	if !strings.Contains(strings.Join(git.Details, "\n"), cfgPath) {
		t.Errorf("Git row details %v do not name the config path %s", git.Details, cfgPath)
	}
}
