// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// VaultProjectReconciler manages {vault}/Projects/{slug}/config.toml plus
// the tasks/done and tasks/cancelled directories that vp init creates.
type VaultProjectReconciler struct {
	vault *storage.Vault
	slug  string

	// skipReason and skipTarget, when skipReason is non-empty, force Check
	// and Plan to unconditionally report a single Skip row instead of their
	// usual vault/slug-driven behavior. Set via WithSkipReason. Zero value on
	// both is today's behavior, unchanged.
	skipReason string
	skipTarget string
}

func NewVaultProject(v *storage.Vault, slug string) *VaultProjectReconciler {
	return &VaultProjectReconciler{vault: v, slug: slug}
}

// WithSkipReason returns a copy of the reconciler that unconditionally
// Checks/Plans a single Skip carrying reason and target, regardless of
// vault/slug state. Used by `vp config sync` to short-circuit onboarding
// when the resolved project directory is the vault itself or inside it —
// the reconciler is only ever given a vault and a slug, never a project
// directory, so it cannot detect this on its own. target becomes the
// Skip row's Action.Target (the specific path the caller wants named),
// matching CwdProjectReconciler's own skip-row convention of a generic
// Summary reason plus a distinct Target path.
func (r VaultProjectReconciler) WithSkipReason(reason, target string) *VaultProjectReconciler {
	r.skipReason = reason
	r.skipTarget = target
	return &r
}

func (r *VaultProjectReconciler) Name() string { return "VaultProject" }
func (r *VaultProjectReconciler) Tier() Tier   { return TierProject }

// Check reports the vault-project config's presence as an Info row — it is
// always Info/Skip and never causes a failure.
func (r *VaultProjectReconciler) Check(_ context.Context) []check.Result {
	if r.skipReason != "" {
		summary := r.skipReason
		if r.skipTarget != "" {
			summary = fmt.Sprintf("%s (%s)", r.skipReason, r.skipTarget)
		}
		return []check.Result{{Name: "Vault project", Status: check.Skip, Summary: summary}}
	}
	if r.vault == nil || r.slug == "" {
		return []check.Result{{
			Name: "Vault project", Status: check.Skip,
			Summary: "vault not open or project not identified",
		}}
	}
	cfgPath, err := r.vault.ProjectConfigFile(r.slug)
	if err != nil {
		return []check.Result{{
			Name: "Vault project", Status: check.Info,
			Summary: fmt.Sprintf("%s: %v", r.slug, err),
		}}
	}
	row := check.Result{Name: "Vault project"}
	if _, err := os.Stat(cfgPath); err == nil {
		row.Status = check.Pass
		row.Summary = cfgPath
	} else if errors.Is(err, os.ErrNotExist) {
		row.Status = check.Info
		row.Summary = fmt.Sprintf("%s missing", cfgPath)
	} else {
		row.Status = check.Fail
		row.Summary = err.Error()
		row.Err = err
	}
	return []check.Result{row}
}

func (r *VaultProjectReconciler) Plan(_ context.Context) (Plan, error) {
	if r.skipReason != "" {
		return Plan{Actions: []Action{{Kind: ActionSkip, Target: r.skipTarget, Summary: r.skipReason}}}, nil
	}
	if r.vault == nil || r.slug == "" {
		return Plan{Actions: []Action{{
			Kind: ActionSkip, Summary: "vault not open or project not identified",
		}}}, nil
	}
	cfgPath, err := r.vault.ProjectConfigFile(r.slug)
	if err != nil {
		return Plan{}, fmt.Errorf("project config path: %w", err)
	}
	tasksDir, err := r.vault.TasksDir(r.slug)
	if err != nil {
		return Plan{}, fmt.Errorf("tasks dir: %w", err)
	}

	var actions []Action

	if _, err := os.Stat(cfgPath); errors.Is(err, os.ErrNotExist) {
		actions = append(actions, Action{
			Kind: ActionCreate, Target: cfgPath,
			Summary: "write vault-project config",
		})
	} else if err != nil {
		return Plan{}, fmt.Errorf("stat %s: %w", cfgPath, err)
	} else {
		// File exists — also check for canonical-key drift so legacy
		// `vp config upgrade --project SLUG` callers can route through
		// here without losing the schema-fill behavior they relied on.
		missing, mErr := detectMissingKeys(cfgPath, upgradeTarget{
			canonicalText: storage.VaultProjectTemplateContent(),
			templateText:  storage.VaultProjectTemplateContent(),
		})
		if mErr != nil {
			return Plan{}, mErr
		}
		if len(missing) == 0 {
			actions = append(actions, Action{
				Kind: ActionUnchanged, Target: cfgPath, Summary: "present",
			})
		} else {
			actions = append(actions, Action{
				Kind:    ActionUpdate,
				Target:  cfgPath,
				Summary: fmt.Sprintf("%d missing key(s)", len(missing)),
				Details: missing,
			})
		}
	}

	for _, sub := range []string{"done", "cancelled"} {
		p := filepath.Join(tasksDir, sub)
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			actions = append(actions, Action{
				Kind: ActionCreate, Target: p,
				Summary: fmt.Sprintf("mkdir tasks/%s", sub),
			})
		} else if err == nil {
			actions = append(actions, Action{
				Kind: ActionUnchanged, Target: p,
				Summary: fmt.Sprintf("tasks/%s present", sub),
			})
		}
	}
	return Plan{Actions: actions}, nil
}

func (r *VaultProjectReconciler) Apply(_ context.Context, p Plan) (Report, error) {
	var rep Report
	if r.vault == nil || r.slug == "" {
		for _, a := range p.Actions {
			switch a.Kind {
			case ActionSkip:
				rep.Skipped++
			case ActionUnchanged:
				rep.Unchanged++
			}
		}
		return rep, nil
	}
	cfgPath, _ := r.vault.ProjectConfigFile(r.slug)
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionCreate:
			if a.Target == cfgPath {
				if _, _, err := r.vault.WriteVaultProjectConfig(r.slug); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("write vault-project config: %w", err))
					continue
				}
			} else {
				if err := os.MkdirAll(a.Target, 0o755); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("mkdir %s: %w", a.Target, err))
					continue
				}
			}
			rep.Created++
		case ActionUpdate:
			if _, err := applyUpgrade(r.vault.Root, a.Target, upgradeTarget{
				canonicalText: storage.VaultProjectTemplateContent(),
				templateText:  storage.VaultProjectTemplateContent(),
			}); err != nil {
				rep.Errors = append(rep.Errors, err)
				continue
			}
			rep.Updated++
		case ActionUnchanged:
			rep.Unchanged++
		case ActionSkip:
			rep.Skipped++
		}
	}
	return rep, nil
}
