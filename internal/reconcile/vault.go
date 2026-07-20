// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// VaultSeed carries what Vault.Apply needs when the vault directory has to
// be created from scratch. In sync mode the seed is zero-valued — Plan
// then derives the vault path from the (already-present) global config.
type VaultSeed struct {
	VaultPath  string
	GitEnabled bool
	seedSet    bool
}

func (s VaultSeed) WithCreate() VaultSeed { s.seedSet = true; return s }

// VaultReconciler manages the vault directory itself (mkdir, optional git
// init, .gitignore).
type VaultReconciler struct {
	root string
	seed VaultSeed
}

func NewVault(root string, seed VaultSeed) *VaultReconciler {
	return &VaultReconciler{root: root, seed: seed}
}

func (r *VaultReconciler) Name() string       { return "Vault" }
func (r *VaultReconciler) Tier() Tier         { return TierVault }
func (r *VaultReconciler) Requires() []string { return []string{"GlobalConfig"} }

func (r *VaultReconciler) resolvedVaultPath() (string, error) {
	if r.seed.seedSet && r.seed.VaultPath != "" {
		return r.seed.VaultPath, nil
	}
	path, _, err := storage.ResolveVaultPath(r.root)
	return path, err
}

func (r *VaultReconciler) gitEnabled() bool {
	if r.seed.seedSet {
		return r.seed.GitEnabled
	}
	// Sync mode: read git_enabled directly from the global config TOML
	// without opening a vault (the vault may not exist yet).
	cfgPath, err := storage.VaultConfigFilePath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return false
	}
	var raw struct {
		GitEnabled bool `toml:"git_enabled"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return false
	}
	return raw.GitEnabled
}

// Check returns CheckVault and (when the vault exists) CheckGit rows.
func (r *VaultReconciler) Check(_ context.Context) []check.Result {
	vaultPath, err := r.resolvedVaultPath()
	if err != nil || vaultPath == "" {
		return []check.Result{{
			Name: "Vault", Status: check.Fail,
			Summary: "cannot resolve vault path",
			Err:     err,
		}}
	}
	results := []check.Result{check.CheckVault(vaultPath)}
	if results[0].Status == check.Pass {
		results = append(results, check.CheckGit(vaultPath, r.gitEnabled()))
	}
	return results
}

// Plan reports one action per missing artifact: the vault dir, git init,
// and .gitignore. Existing items report Unchanged.
func (r *VaultReconciler) Plan(_ context.Context) (Plan, error) {
	vaultPath, err := r.resolvedVaultPath()
	if err != nil || vaultPath == "" {
		if !r.seed.seedSet {
			return Plan{Actions: []Action{{
				Kind: ActionSkip, Summary: "vault path unresolved — run `vp init`",
			}}}, nil
		}
		return Plan{}, fmt.Errorf("resolve vault path: %w", err)
	}
	var actions []Action

	// Vault directory itself.
	if _, statErr := os.Stat(vaultPath); errors.Is(statErr, os.ErrNotExist) {
		if r.seed.seedSet {
			actions = append(actions, Action{
				Kind: ActionCreate, Target: vaultPath,
				Summary: "create vault directory",
			})
		} else {
			actions = append(actions, Action{
				Kind: ActionSkip, Target: vaultPath,
				Summary: "vault missing — run `vp init`",
			})
			return Plan{Actions: actions}, nil
		}
	} else if statErr != nil {
		return Plan{}, fmt.Errorf("stat vault: %w", statErr)
	} else {
		actions = append(actions, Action{
			Kind: ActionUnchanged, Target: vaultPath,
			Summary: "vault directory present",
		})
	}

	// .gitignore
	gitignore := filepath.Join(vaultPath, ".gitignore")
	if _, statErr := os.Stat(gitignore); errors.Is(statErr, os.ErrNotExist) {
		actions = append(actions, Action{
			Kind: ActionCreate, Target: gitignore,
			Summary: "write vault .gitignore",
		})
	} else if statErr == nil {
		actions = append(actions, Action{
			Kind: ActionUnchanged, Target: gitignore,
			Summary: ".gitignore present",
		})
	}

	// git init (only when enabled and not yet a repo)
	if r.gitEnabled() {
		if _, statErr := os.Stat(filepath.Join(vaultPath, ".git")); errors.Is(statErr, os.ErrNotExist) {
			actions = append(actions, Action{
				Kind: ActionCreate, Target: filepath.Join(vaultPath, ".git"),
				Summary: "git init vault",
			})
		}
	}
	return Plan{Actions: actions}, nil
}

func (r *VaultReconciler) Apply(_ context.Context, p Plan) (Report, error) {
	var rep Report
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionCreate:
			switch filepath.Base(a.Target) {
			case ".gitignore":
				if err := storage.ReconcileVaultGitignore(filepath.Dir(a.Target)); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("write .gitignore: %w", err))
					continue
				}
			case ".git":
				if err := storage.GitInit(filepath.Dir(a.Target)); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("git init: %w", err))
					continue
				}
			default:
				// vault directory itself
				if err := os.MkdirAll(a.Target, 0o755); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("mkdir vault: %w", err))
					continue
				}
				// Born-current: a freshly CREATED vault is already in the current
				// on-disk data format, so stamp it at creation. This is the ONLY
				// place the format is stamped on scaffold — never on opening an
				// existing vault, which could falsely mark unmigrated data as
				// migrated. Non-fatal: a stamp hiccup must not abort vault init.
				if err := surface.WriteFormat(a.Target, surface.RequiredDataFormat); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("stamp vault data format: %w", err))
				}
			}
			rep.Created++
		case ActionUpdate:
			rep.Updated++
		case ActionUnchanged:
			rep.Unchanged++
		case ActionSkip:
			rep.Skipped++
		}
	}
	return rep, nil
}
