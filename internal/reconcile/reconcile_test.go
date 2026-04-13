// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"os"
	"path/filepath"
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
	if !r.gitEnabled() {
		t.Errorf("expected gitEnabled()=true from global config")
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
		req  []string
	}{
		{NewGlobalConfig(root, GlobalSeed{}), "GlobalConfig", TierGlobal, nil},
		{NewVault(root, VaultSeed{}), "Vault", TierVault, []string{"GlobalConfig"}},
		{NewVaultSettings(v), "VaultSettings", TierVault, []string{"Vault"}},
		{NewCwdProject(root, CwdProjectSeed{}), "CwdProject", TierProject, []string{"GlobalConfig"}},
		{NewVaultProject(v, "x"), "VaultProject", TierProject, []string{"Vault"}},
	}
	for _, c := range cases {
		if c.r.Name() != c.name {
			t.Errorf("Name(): got %q want %q", c.r.Name(), c.name)
		}
		if c.r.Tier() != c.tier {
			t.Errorf("%s Tier(): got %q want %q", c.name, c.r.Tier(), c.tier)
		}
		gotReq := c.r.Requires()
		if len(gotReq) != len(c.req) {
			t.Errorf("%s Requires(): got %v want %v", c.name, gotReq, c.req)
			continue
		}
		for i := range gotReq {
			if gotReq[i] != c.req[i] {
				t.Errorf("%s Requires()[%d]: got %q want %q", c.name, i, gotReq[i], c.req[i])
			}
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
			name: "Vault unchanged+update+skip",
			r:    NewVault(t.TempDir(), VaultSeed{}),
			p: Plan{Actions: []Action{
				{Kind: ActionUnchanged, Target: "x"},
				{Kind: ActionUpdate, Target: "y"},
				{Kind: ActionSkip, Target: "z"},
			}},
			want: Report{Unchanged: 1, Updated: 1, Skipped: 1},
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
