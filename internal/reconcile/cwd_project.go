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

// CwdProjectSeed carries the fields needed to create a cwd-local project
// config on first-run. Zero-valued seed = sync mode.
type CwdProjectSeed struct {
	Name              string
	Domain            string
	Tags              []string
	VaultPathOverride string
	seedSet           bool
}

func (s CwdProjectSeed) WithCreate() CwdProjectSeed { s.seedSet = true; return s }

// CwdProjectReconciler manages <root>/.vibe-palace.toml.
type CwdProjectReconciler struct {
	root string
	seed CwdProjectSeed
}

func NewCwdProject(root string, seed CwdProjectSeed) *CwdProjectReconciler {
	return &CwdProjectReconciler{root: root, seed: seed}
}

func (r *CwdProjectReconciler) Name() string       { return "CwdProject" }
func (r *CwdProjectReconciler) Tier() Tier         { return TierProject }
func (r *CwdProjectReconciler) Requires() []string { return []string{"GlobalConfig"} }

func (r *CwdProjectReconciler) configPath() string {
	return filepath.Join(r.root, ".vibe-palace.toml")
}

func (r *CwdProjectReconciler) Check(_ context.Context) []check.Result {
	return []check.Result{check.CheckProjectAt(r.root)}
}

func (r *CwdProjectReconciler) Plan(_ context.Context) (Plan, error) {
	cfg := r.configPath()
	if _, err := os.Stat(cfg); errors.Is(err, os.ErrNotExist) {
		if r.seed.seedSet {
			return Plan{Actions: []Action{{
				Kind:    ActionCreate,
				Target:  cfg,
				Summary: fmt.Sprintf("create project config (name=%s, domain=%s)", r.seed.Name, r.seed.Domain),
			}}}, nil
		}
		return Plan{Actions: []Action{{
			Kind: ActionSkip, Target: cfg,
			Summary: "no project config here — not a project directory",
		}}}, nil
	} else if err != nil {
		return Plan{}, fmt.Errorf("stat %s: %w", cfg, err)
	}

	missing, err := detectMissingKeys(cfg, upgradeTarget{
		canonicalText: storage.CwdProjectTemplateContent(),
		templateText:  storage.CwdProjectTemplateContent(),
	})
	if err != nil {
		return Plan{}, err
	}
	if len(missing) == 0 {
		return Plan{Actions: []Action{{
			Kind: ActionUnchanged, Target: cfg, Summary: "in sync",
		}}}, nil
	}
	return Plan{Actions: []Action{{
		Kind:    ActionUpdate,
		Target:  cfg,
		Summary: fmt.Sprintf("%d missing key(s)", len(missing)),
		Details: missing,
	}}}, nil
}

func (r *CwdProjectReconciler) Apply(_ context.Context, p Plan) (Report, error) {
	var rep Report
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionCreate:
			if _, err := storage.WriteCwdProjectConfig(
				r.root, r.seed.Name, r.seed.Domain, r.seed.Tags, r.seed.VaultPathOverride,
			); err != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("write cwd project config: %w", err))
				continue
			}
			rep.Created++
		case ActionUpdate:
			if _, err := applyUpgrade(a.Target, upgradeTarget{
				canonicalText: storage.CwdProjectTemplateContent(),
				templateText:  storage.CwdProjectTemplateContent(),
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
