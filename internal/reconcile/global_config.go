// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// GlobalSeed carries the fields needed to create a global config file on
// first-run. Zero-valued GlobalSeed means "sync mode" — Plan will not
// propose Create actions and will report missing as Skip.
type GlobalSeed struct {
	VaultPath  string
	GitEnabled bool
	seedSet    bool
}

// WithCreate marks the seed as populated so Plan is willing to create the
// config when it is missing. Call this from init paths that pass real
// --vault-path/--no-git flags; leave unset for sync-mode callers.
func (s GlobalSeed) WithCreate() GlobalSeed { s.seedSet = true; return s }

// GlobalConfigReconciler manages ~/.config/vibe-palace/config.toml.
type GlobalConfigReconciler struct {
	root string // project root for resolving any cwd-local vault_path override
	seed GlobalSeed
}

// NewGlobalConfig constructs a GlobalConfigReconciler. root is the project
// directory used to honor any .vibe-palace.toml vault_path override; pass
// os.Getwd() when in doubt.
func NewGlobalConfig(root string, seed GlobalSeed) *GlobalConfigReconciler {
	return &GlobalConfigReconciler{root: root, seed: seed}
}

func (r *GlobalConfigReconciler) Name() string       { return "GlobalConfig" }
func (r *GlobalConfigReconciler) Tier() Tier         { return TierGlobal }
func (r *GlobalConfigReconciler) Requires() []string { return nil }

// Check runs CheckConfigAt and, when the config file exists,
// CheckConfigStaleness — mirroring today's vp check rows.
func (r *GlobalConfigReconciler) Check(_ context.Context) []check.Result {
	configPath, _, row := check.CheckConfigAt(r.root)
	results := []check.Result{row}
	if row.Status == check.Pass && configPath != "" {
		results = append(results, check.CheckConfigStaleness(configPath))
	}
	return results
}

// Plan reports what Apply would do: Create when the file is missing and a
// seed is set; Update when drift is detected; Unchanged otherwise.
func (r *GlobalConfigReconciler) Plan(_ context.Context) (Plan, error) {
	configPath, err := storage.VaultConfigFilePath()
	if err != nil {
		return Plan{}, fmt.Errorf("resolve config path: %w", err)
	}
	if _, statErr := os.Stat(configPath); errors.Is(statErr, os.ErrNotExist) {
		if r.seed.seedSet {
			return Plan{Actions: []Action{{
				Kind:    ActionCreate,
				Target:  configPath,
				Summary: fmt.Sprintf("create global config (vault_path=%s, git_enabled=%v)", r.seed.VaultPath, r.seed.GitEnabled),
			}}}, nil
		}
		return Plan{Actions: []Action{{
			Kind:    ActionSkip,
			Target:  configPath,
			Summary: "missing — run `vp init` to create",
		}}}, nil
	} else if statErr != nil {
		return Plan{}, fmt.Errorf("stat %s: %w", configPath, statErr)
	}

	defaultsText, derr := storage.DefaultsTomlContent()
	if derr != nil {
		return Plan{}, fmt.Errorf("load defaults: %w", derr)
	}
	missing, err := detectMissingKeys(configPath, upgradeTarget{
		canonicalText: defaultsText,
		templateText:  storage.TemplateTomlContent(),
	})
	if err != nil {
		return Plan{}, err
	}
	if len(missing) == 0 {
		return Plan{Actions: []Action{{
			Kind: ActionUnchanged, Target: configPath,
			Summary: "in sync with canonical keys",
		}}}, nil
	}
	return Plan{Actions: []Action{{
		Kind:    ActionUpdate,
		Target:  configPath,
		Summary: fmt.Sprintf("%d missing key(s) — will add as commented defaults", len(missing)),
		Details: missing,
	}}}, nil
}

// Apply executes Create or Update actions from p. Unchanged and Skip are
// counted but do no work.
func (r *GlobalConfigReconciler) Apply(_ context.Context, p Plan) (Report, error) {
	var rep Report
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionCreate:
			if _, err := storage.WriteGlobalConfig(r.seed.VaultPath, r.seed.GitEnabled); err != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("write global config: %w", err))
				continue
			}
			rep.Created++
		case ActionUpdate:
			defaultsText, derr := storage.DefaultsTomlContent()
			if derr != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("load defaults: %w", derr))
				continue
			}
			// Global config lives under the user's config dir, not the
			// vault — no surface stamp.
			if _, err := applyUpgrade("", a.Target, upgradeTarget{
				canonicalText: defaultsText,
				templateText:  storage.TemplateTomlContent(),
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
