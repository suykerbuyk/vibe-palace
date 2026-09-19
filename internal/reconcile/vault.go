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

func (r *VaultReconciler) Name() string { return "Vault" }
func (r *VaultReconciler) Tier() Tier   { return TierVault }

func (r *VaultReconciler) resolvedVaultPath() (string, error) {
	if r.seed.seedSet && r.seed.VaultPath != "" {
		return r.seed.VaultPath, nil
	}
	path, _, err := storage.ResolveVaultPath(r.root)
	return path, err
}

// gitEnabled reports the git_enabled decision this reconciler plans against.
// `vp init` passes its own choice in the seed. In sync mode it is the host
// config's git_enabled, read through storage.HostGitEnabled: the same reader
// the vault git refusal uses, so `vp config sync`, `vp check` and every vault
// git entry point cannot disagree about the value. A read error is returned,
// never collapsed into "disabled".
func (r *VaultReconciler) gitEnabled() (bool, error) {
	if r.seed.seedSet {
		return r.seed.GitEnabled, nil
	}
	return storage.HostGitEnabled()
}

// Check returns CheckVault and (when the vault exists) CheckGit rows.
func (r *VaultReconciler) Check(_ context.Context) []check.Result {
	vaultPath, err := r.resolvedVaultPath()
	if err != nil || vaultPath == "" {
		// Details, not Err alone — see check.PrintRows.
		res := check.Result{
			Name: "Vault", Status: check.Fail,
			Summary: "cannot resolve vault path",
			Err:     err,
		}
		if err != nil {
			res.Details = []string{err.Error()}
		}
		return []check.Result{res}
	}
	results := []check.Result{check.CheckVault(vaultPath)}
	if results[0].Status == check.Pass {
		enabled, gerr := r.gitEnabled()
		results = append(results, check.CheckGit(vaultPath, enabled, gerr))
	}
	return results
}

// Plan reports one action per missing artifact: the vault dir, git init,
// and .gitignore. Existing items report Unchanged, with one exception: a
// present .gitignore that lacks canonical lines plans an Update that tops it
// up (Details lists the missing lines), and one that cannot be read plans a
// Skip naming the read error rather than failing the whole Plan — a Plan error
// would abort every tier of `vp config sync` before any Apply.
func (r *VaultReconciler) Plan(_ context.Context) (Plan, error) {
	vaultPath, err := r.resolvedVaultPath()
	if err != nil || vaultPath == "" {
		if !r.seed.seedSet {
			// `vp init` is the remedy for an ABSENT config. A cwd
			// .vibe-palace.toml that parsed and was REJECTED has a config, and
			// sending that operator at the global one points them at the wrong
			// file — the same misdirection CheckConfigAt used to emit.
			summary := "vault path unresolved — run `vp init`"
			if errors.Is(err, storage.ErrSwallowedVaultPath) {
				summary = "vault path REJECTED, not missing: " + err.Error()
			}
			return Plan{Actions: []Action{{
				Kind: ActionSkip, Summary: summary,
			}}}, nil
		}
		return Plan{}, fmt.Errorf("resolve vault path: %w", err)
	}
	var actions []Action

	// Vault directory itself.
	if info, statErr := os.Stat(vaultPath); errors.Is(statErr, os.ErrNotExist) {
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
	} else if !info.IsDir() {
		// Name the real problem. Planning on past this point would report it
		// as an unreadable .gitignore (stat …/.gitignore: not a directory),
		// blaming a file that cannot exist rather than the vault path.
		actions = append(actions, Action{
			Kind: ActionSkip, Target: vaultPath,
			Summary: "vault path is not a directory: " + vaultPath,
		})
		return Plan{Actions: actions}, nil
	} else {
		actions = append(actions, Action{
			Kind: ActionUnchanged, Target: vaultPath,
			Summary: "vault directory present",
		})
	}

	// .gitignore
	actions = append(actions, planVaultGitignore(vaultPath))

	// git init (only when enabled and not yet a repo). An unreadable
	// git_enabled plans a Skip naming the read error, the idiom the .gitignore
	// branch uses: a Plan error would abort every tier of `vp config sync`.
	enabled, gerr := r.gitEnabled()
	if gerr != nil {
		actions = append(actions, Action{
			Kind: ActionSkip, Target: filepath.Join(vaultPath, ".git"),
			Summary: "git init skipped — " + gerr.Error(),
		})
	} else if enabled {
		gitPath := filepath.Join(vaultPath, ".git")
		if _, statErr := os.Stat(gitPath); errors.Is(statErr, os.ErrNotExist) {
			// No .git AT the vault. Before planning git init, ask whether git
			// sees a .git ABOVE the vault (VaultGitNested) — the same
			// predicate storage.PruneMirrorsVerifiedWithDowngrade's caller in
			// cmd_config.go uses for the Templates tier. Do not invent a
			// second nesting check.
			switch gitState, _ := storage.InspectVaultGit(vaultPath); gitState {
			case storage.VaultNotGit:
				actions = append(actions, Action{
					Kind: ActionCreate, Target: gitPath,
					Summary: "git init vault",
				})
			case storage.VaultGitNested:
				summary := "git init skipped — vault is nested inside another repository"
				if top, err := storage.GitTopLevel(vaultPath); err == nil && top != "" {
					summary = "git init skipped — vault is nested inside " + top
				}
				actions = append(actions, Action{
					Kind: ActionSkip, Target: gitPath,
					Summary: summary,
				})
			default:
				// VaultGitUnavailable or VaultGitBroken: a .git marker was
				// found at or above the vault, but git cannot be run or the
				// repository is unreadable, so the OK/Nested distinction is
				// unavailable. The marker cannot be an ordinary .git
				// directory AT the vault (the os.Stat above would have found
				// it), but it CAN be a dangling .git symlink AT the vault
				// itself: os.Stat follows symlinks and reports ErrNotExist
				// for a dangling target, while storage.InspectVaultGit's
				// os.Lstat finds the symlink entry without resolving it. So
				// do not claim the marker is "above the vault" unless an
				// os.Lstat at the vault itself confirms nothing is there —
				// otherwise a dangling symlink at the vault would be
				// misreported as an ancestor's repository.
				summary := "git init skipped — an existing .git entry could not be verified"
				if _, lstatErr := os.Lstat(gitPath); errors.Is(lstatErr, os.ErrNotExist) {
					summary = "git init skipped — an enclosing .git directory was found above the vault"
				}
				actions = append(actions, Action{
					Kind: ActionSkip, Target: gitPath,
					Summary: summary,
				})
			}
		} else if statErr != nil {
			return Plan{}, fmt.Errorf("stat vault git: %w", statErr)
		}
		// else: .git already present at the vault itself — unchanged,
		// exactly as before (VaultGitOK and any locally-present .git in
		// other states never reach storage.InspectVaultGit at all — no
		// behavior change there).
	}
	return Plan{Actions: actions}, nil
}

// planVaultGitignore plans the one .gitignore action: Create when absent,
// Update when present but missing canonical lines, Unchanged when complete,
// and Skip — carrying the error — when it cannot be stat'ed or read.
func planVaultGitignore(vaultPath string) Action {
	gitignore := filepath.Join(vaultPath, ".gitignore")
	if _, statErr := os.Stat(gitignore); errors.Is(statErr, os.ErrNotExist) {
		return Action{
			Kind: ActionCreate, Target: gitignore,
			Summary: "write vault .gitignore",
		}
	} else if statErr != nil {
		return Action{
			Kind: ActionSkip, Target: gitignore,
			Summary: "cannot read vault .gitignore: " + statErr.Error(),
		}
	}
	missing, err := storage.MissingVaultGitignorePatterns(vaultPath)
	switch {
	case err != nil:
		return Action{
			Kind: ActionSkip, Target: gitignore,
			Summary: "cannot read vault .gitignore: " + err.Error(),
		}
	case len(missing) > 0:
		return Action{
			Kind: ActionUpdate, Target: gitignore,
			Summary: fmt.Sprintf("top up vault .gitignore (+%d canonical line(s))", len(missing)),
			Details: missing,
		}
	default:
		return Action{
			Kind: ActionUnchanged, Target: gitignore,
			Summary: ".gitignore present",
		}
	}
}

// VaultArtifact names the part of the vault a VaultReconciler.Apply failure
// concerns. The zero value means unclassified.
type VaultArtifact string

const (
	VaultArtifactDir         VaultArtifact = "vault directory"
	VaultArtifactGitignore   VaultArtifact = "vault .gitignore"
	VaultArtifactGit         VaultArtifact = "vault git repository"
	VaultArtifactFormatStamp VaultArtifact = "vault data-format stamp"
)

// VaultApplyError is the type of every error VaultReconciler.Apply puts in
// Report.Errors. It exists so a caller triages a failure by errors.As on
// Artifact, never by matching message text: the message is for humans and may
// be reworded, and a triage keyed on its prefix turns that reword into a
// silent downgrade from [FAIL] to a log line. Error() keeps the wording these
// failures have always had ("mkdir vault: …", "write .gitignore: …").
type VaultApplyError struct {
	Artifact VaultArtifact
	Op       string
	Err      error
}

func (e *VaultApplyError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *VaultApplyError) Unwrap() error { return e.Err }

func vaultApplyErr(artifact VaultArtifact, op string, err error) error {
	return &VaultApplyError{Artifact: artifact, Op: op, Err: err}
}

func (r *VaultReconciler) Apply(_ context.Context, p Plan) (Report, error) {
	var rep Report
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionCreate:
			switch filepath.Base(a.Target) {
			case ".gitignore":
				if err := storage.ReconcileVaultGitignore(filepath.Dir(a.Target)); err != nil {
					rep.Errors = append(rep.Errors, vaultApplyErr(VaultArtifactGitignore, "write .gitignore", err))
					continue
				}
			case ".git":
				if err := storage.GitInit(filepath.Dir(a.Target)); err != nil {
					rep.Errors = append(rep.Errors, vaultApplyErr(VaultArtifactGit, "git init", err))
					continue
				}
			default:
				// vault directory itself
				if err := os.MkdirAll(a.Target, 0o755); err != nil {
					rep.Errors = append(rep.Errors, vaultApplyErr(VaultArtifactDir, "mkdir vault", err))
					continue
				}
				// Born-current: a freshly CREATED vault is already in the current
				// on-disk data format, so stamp it at creation. This is the ONLY
				// place the format is stamped on scaffold — never on opening an
				// existing vault, which could falsely mark unmigrated data as
				// migrated. Non-fatal: a stamp hiccup must not abort vault init.
				if err := surface.WriteFormat(a.Target, surface.RequiredDataFormat); err != nil {
					rep.Errors = append(rep.Errors, vaultApplyErr(VaultArtifactFormatStamp, "stamp vault data format", err))
				}
			}
			rep.Created++
		case ActionUpdate:
			// The only Update this reconciler plans is the .gitignore top-up.
			// It is a read-modify-write of a shared, git-tracked vault file,
			// so it goes through the locked funnel (storage.LockedUpdate, via
			// TopUpVaultGitignore). The Create branch above is ALSO
			// lock-protected (ReconcileVaultGitignore's own absent-tolerant
			// single acquisition, since vault-gitignore-create-bypasses-the-vault-lock)
			// — the two branches use different acquisition shapes because
			// LockedUpdate has no absent-tolerant path (a missing file is an
			// error there, not a create), never because one of them is
			// unlocked. They never run nested: a single Apply action is
			// exactly one of Create or Update for a given target, never both.
			// The missing set is re-derived under the lock; if another writer
			// already added it, nothing is written and the action counts as
			// Unchanged.
			if filepath.Base(a.Target) != ".gitignore" {
				// Nothing else has an Update writer. Counting one as Updated
				// would report a success that wrote nothing.
				// The zero Artifact is "unclassified", which a caller must
				// treat as a failure.
				rep.Errors = append(rep.Errors, vaultApplyErr("", "update",
					fmt.Errorf("no Update writer for %s", a.Target)))
				continue
			}
			added, err := storage.TopUpVaultGitignore(filepath.Dir(a.Target))
			if err != nil {
				rep.Errors = append(rep.Errors, vaultApplyErr(VaultArtifactGitignore, "top up .gitignore", err))
				continue
			}
			if added == 0 {
				rep.Unchanged++
				continue
			}
			rep.Updated++
			// The count is the one the locked write actually added, not the
			// plan-time estimate: another writer may have added some between
			// Plan and Apply.
			rep.Notes = append(rep.Notes, fmt.Sprintf("vault .gitignore: added %d canonical line(s)", added))
		case ActionUnchanged:
			rep.Unchanged++
		case ActionSkip:
			rep.Skipped++
		}
	}
	return rep, nil
}
