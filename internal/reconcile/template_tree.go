// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// TemplateMode selects which flavour of tree a TemplateTreeReconciler
// operates on: a full materialize of the embedded corpus (vault
// Templates/) or a per-project scaffold (just directories + README
// stubs).
type TemplateMode string

const (
	// TemplateModeMaterialize copies every embedded resource into
	// <vault>/<relSubpath>/ and tracks its SHA in templates.lock.
	TemplateModeMaterialize TemplateMode = "materialize"
	// TemplateModeScaffold creates commands/ and skills/ subdirectories
	// with README stubs but does not materialize any embedded content.
	TemplateModeScaffold TemplateMode = "scaffold"
)

// TemplateTreeSeed parameterises one reconciler instance.
type TemplateTreeSeed struct {
	// Mode picks materialize vs scaffold semantics.
	Mode TemplateMode
	// AutoAccept short-circuits the Prompt branch: when true, any
	// Create actions emitted by Plan are applied without prompting.
	// vp init sets this on empty vaults so first-run materialize runs
	// unattended. It has no effect in scaffold mode (scaffold never
	// prompts).
	AutoAccept bool
}

// TemplateTreeReconciler implements the Reconciler interface against
// the embedded templates corpus and the templates.lock sidecar.
//
// Layout:
//   - Mode=Materialize, relSubpath="Templates": every embedded resource
//     is copied into <vaultRoot>/Templates/<rel>. Lock keys are the
//     full vault-relative path (e.g. "Templates/commands/wrap.md").
//   - Mode=Scaffold, relSubpath="Projects/<slug>": commands/ and
//     skills/ subdirectories with README stubs. No lock tracking; the
//     stubs are write-if-absent.
type TemplateTreeReconciler struct {
	vaultRoot  string
	relSubpath string
	seed       TemplateTreeSeed

	// planState carries the merged lock (including silent-adopted
	// entries) from Plan to Apply. Scoped to this reconciler instance
	// so parallel reconcilers can't race on a shared map. Nil until
	// Plan runs; Apply falls back to re-reading the lock if absent.
	planState *templateTreePlanState
}

// NewTemplateTree builds a reconciler for the template tree at
// <vaultRoot>/<relSubpath>. vaultRoot must be absolute; relSubpath is
// vault-relative with forward slashes.
func NewTemplateTree(vaultRoot, relSubpath string, seed TemplateTreeSeed) *TemplateTreeReconciler {
	return &TemplateTreeReconciler{
		vaultRoot:  vaultRoot,
		relSubpath: relSubpath,
		seed:       seed,
	}
}

// Name returns a stable identifier used by parity tests and the
// orchestrator ordering. Examples: "TemplateTree:Templates",
// "TemplateTree:Projects/proj2".
func (r *TemplateTreeReconciler) Name() string {
	return "TemplateTree:" + r.relSubpath
}

// Tier returns the tier this reconciler writes to. The Templates
// subtree is vault-tier; every Projects/<slug> subtree is project-tier.
func (r *TemplateTreeReconciler) Tier() Tier {
	if strings.HasPrefix(r.relSubpath, "Projects/") {
		return TierProject
	}
	return TierVault
}

// Requires declares that the Vault reconciler must run first so the
// vault directory exists before we materialize into it.
func (r *TemplateTreeReconciler) Requires() []string { return []string{"Vault"} }

// subpathAbs is <vaultRoot>/<relSubpath>.
func (r *TemplateTreeReconciler) subpathAbs() string {
	return filepath.Join(r.vaultRoot, filepath.FromSlash(r.relSubpath))
}

// vaultRelFromEmbedded computes the vault-relative lock/target key for
// an embedded resource (e.g. "commands/wrap.md" → "Templates/commands/wrap.md").
func (r *TemplateTreeReconciler) vaultRelFromEmbedded(embeddedRel string) string {
	return r.relSubpath + "/" + embeddedRel
}

// hashFile returns the hex sha256 of a file on disk, or ("", err) on
// read error. A missing file returns ("", os.ErrNotExist wrapped).
func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Check returns one check.Result per embedded resource for
// materialize mode; one aggregate row for scaffold mode.
func (r *TemplateTreeReconciler) Check(_ context.Context) []check.Result {
	if r.seed.Mode == TemplateModeScaffold {
		return []check.Result{r.checkScaffold()}
	}
	return r.checkMaterialize()
}

func (r *TemplateTreeReconciler) checkMaterialize() []check.Result {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		return []check.Result{{
			Name:   r.Name(),
			Status: check.Fail,
			Err:    fmt.Errorf("walk embedded: %w", err),
		}}
	}
	lock, err := templates.ReadLock(r.vaultRoot)
	if err != nil {
		return []check.Result{{
			Name:   r.Name(),
			Status: check.Fail,
			Err:    fmt.Errorf("read lock: %w", err),
		}}
	}
	var out []check.Result
	for _, res := range resources {
		key := r.vaultRelFromEmbedded(res.RelPath)
		target := filepath.Join(r.vaultRoot, filepath.FromSlash(key))
		vaultSHA, herr := hashFile(target)
		if herr != nil && !os.IsNotExist(herr) {
			out = append(out, check.Result{
				Name:   r.Name() + ":" + key,
				Status: check.Fail,
				Err:    herr,
			})
			continue
		}
		entry, haveLock := lock.Entries[key]
		status := check.Info
		summary := "drift"
		if haveLock && vaultSHA == entry.EmbeddedSHA && res.SHA256 == entry.EmbeddedSHA {
			status = check.Pass
			summary = "in sync"
		} else if !haveLock && herr != nil && os.IsNotExist(herr) {
			summary = "missing (not yet materialized)"
		}
		out = append(out, check.Result{
			Name:    r.Name() + ":" + key,
			Status:  status,
			Summary: summary,
		})
	}
	return out
}

func (r *TemplateTreeReconciler) checkScaffold() check.Result {
	sub := r.subpathAbs()
	status := check.Pass
	summary := r.relSubpath + " scaffolding: Pass"
	for _, kind := range []string{"commands", "skills"} {
		dir := filepath.Join(sub, kind)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			status = check.Info
			summary = r.relSubpath + " scaffolding: Info (missing " + kind + "/)"
			break
		}
		readme := filepath.Join(dir, "README.md")
		if _, err := os.Stat(readme); os.IsNotExist(err) {
			status = check.Info
			summary = r.relSubpath + " scaffolding: Info (missing " + kind + "/README.md)"
			break
		}
	}
	return check.Result{
		Name:    r.Name(),
		Status:  status,
		Summary: summary,
	}
}

// templateTreePlanState is what Plan collects for Apply: the action
// list plus the merged-lock snapshot that captures silent adoptions.
// We stash it on the reconciler (by key) so Apply can pick it up
// without needing to round-trip it through the Plan type.
type templateTreePlanState struct {
	lock templates.Lock
}

// Plan emits the decision-table actions for materialize mode; for
// scaffold mode it emits at most 3 Create actions (two directories +
// two READMEs — one per subdir).
func (r *TemplateTreeReconciler) Plan(_ context.Context) (Plan, error) {
	if r.seed.Mode == TemplateModeScaffold {
		return r.planScaffold()
	}
	return r.planMaterialize()
}

func (r *TemplateTreeReconciler) planMaterialize() (Plan, error) {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		return Plan{}, fmt.Errorf("walk embedded: %w", err)
	}
	lock, err := templates.ReadLock(r.vaultRoot)
	if err != nil {
		return Plan{}, fmt.Errorf("read lock: %w", err)
	}

	// ---- Silent-adopt pre-pass ----
	// For any resource that has no lock entry but whose vault file's
	// bytes match the embedded bytes exactly, record the lock entry
	// in-memory immediately. Plan therefore emits no Prompt for that
	// row and Apply has a pre-populated lock snapshot to persist.
	now := time.Now().UTC()
	for _, res := range resources {
		key := r.vaultRelFromEmbedded(res.RelPath)
		if _, ok := lock.Entries[key]; ok {
			continue
		}
		target := filepath.Join(r.vaultRoot, filepath.FromSlash(key))
		vaultSHA, herr := hashFile(target)
		if herr != nil {
			// ENOENT is the normal "not yet materialized" case — the
			// main decision-table loop below will emit a Create. Any
			// other error (permission, IO) is unexpected and worth
			// logging: silent-adopt would skip it here and the main
			// loop would then classify the file as user-edited
			// (producing a confusing Prompt row) without any
			// diagnostic trail.
			if !os.IsNotExist(herr) {
				slog.Error("silent-adopt hash failed",
					"path", target, "err", herr, "reconciler", r.Name())
			}
			continue
		}
		// Use the test-injectable EmbeddedSHA so overrides flow through.
		embSHA, ok := templates.EmbeddedSHA(res.RelPath)
		if !ok {
			embSHA = res.SHA256
		}
		if vaultSHA == embSHA {
			lock.Entries[key] = templates.LockEntry{
				EmbeddedSHA: embSHA,
				WrittenAt:   now,
			}
		}
	}

	var actions []Action
	for _, res := range resources {
		key := r.vaultRelFromEmbedded(res.RelPath)
		target := filepath.Join(r.vaultRoot, filepath.FromSlash(key))
		embSHA, ok := templates.EmbeddedSHA(res.RelPath)
		if !ok {
			embSHA = res.SHA256
		}
		vaultSHA, herr := hashFile(target)
		vaultExists := herr == nil
		if herr != nil && !os.IsNotExist(herr) {
			return Plan{}, fmt.Errorf("hash %s: %w", target, herr)
		}
		entry, haveLock := lock.Entries[key]

		// --- Decision table ---
		switch {
		case !vaultExists:
			// Row 1: vault missing → Create.
			actions = append(actions, Action{
				Kind:    ActionCreate,
				Target:  target,
				Summary: "materialize " + key,
				Details: []string{
					"embedded_sha=" + embSHA,
					"vault_sha=",
					"lock_sha=" + entry.EmbeddedSHA,
				},
			})
		case haveLock && vaultSHA == entry.EmbeddedSHA && embSHA == entry.EmbeddedSHA:
			// Row 2: match / match → Unchanged.
			actions = append(actions, Action{
				Kind:    ActionUnchanged,
				Target:  target,
				Summary: key + " unchanged",
			})
		case haveLock && vaultSHA == entry.EmbeddedSHA && embSHA != entry.EmbeddedSHA:
			// Row 3: match / differs → auto-Update (safe).
			actions = append(actions, Action{
				Kind:    ActionUpdate,
				Target:  target,
				Summary: "upgrade " + key + " (embedded bumped)",
				Details: []string{
					"embedded_sha=" + embSHA,
					"vault_sha=" + vaultSHA,
					"lock_sha=" + entry.EmbeddedSHA,
				},
			})
		case haveLock && vaultSHA != entry.EmbeddedSHA && embSHA == entry.EmbeddedSHA:
			// Row 4: differs / match → user-edited, embedded stable → Unchanged.
			actions = append(actions, Action{
				Kind:    ActionUnchanged,
				Target:  target,
				Summary: key + " user-edited (embedded stable)",
			})
		case haveLock && vaultSHA != entry.EmbeddedSHA && embSHA != entry.EmbeddedSHA:
			// Row 5: differs / differs → Prompt.
			actions = append(actions, Action{
				Kind:    ActionPrompt,
				Target:  target,
				Summary: key + " diverged (user-edited AND embedded bumped)",
				Details: []string{
					"embedded_sha=" + embSHA,
					"vault_sha=" + vaultSHA,
					"lock_sha=" + entry.EmbeddedSHA,
					"embedded_relpath=" + res.RelPath,
				},
			})
		case !haveLock:
			// Lock absent AND file exists. Silent-adopt pre-pass would have
			// planted a lock entry when vaultSHA == embSHA, so reaching
			// this branch means vaultSHA ≠ embSHA → treat as user-edited,
			// Prompt.
			actions = append(actions, Action{
				Kind:    ActionPrompt,
				Target:  target,
				Summary: key + " diverged (no lock, bytes differ from embedded)",
				Details: []string{
					"embedded_sha=" + embSHA,
					"vault_sha=" + vaultSHA,
					"lock_sha=",
					"embedded_relpath=" + res.RelPath,
				},
			})
		}
	}

	r.planState = &templateTreePlanState{lock: lock}
	return Plan{Actions: actions}, nil
}

func (r *TemplateTreeReconciler) planScaffold() (Plan, error) {
	sub := r.subpathAbs()
	var actions []Action
	for _, kind := range []string{"commands", "skills"} {
		dir := filepath.Join(sub, kind)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			actions = append(actions, Action{
				Kind:    ActionCreate,
				Target:  dir,
				Summary: "scaffold " + r.relSubpath + "/" + kind + "/",
			})
		} else if err != nil {
			return Plan{}, fmt.Errorf("stat %s: %w", dir, err)
		}
		readme := filepath.Join(dir, "README.md")
		if _, err := os.Stat(readme); os.IsNotExist(err) {
			actions = append(actions, Action{
				Kind:    ActionCreate,
				Target:  readme,
				Summary: "scaffold " + r.relSubpath + "/" + kind + "/README.md",
			})
		} else if err != nil {
			return Plan{}, fmt.Errorf("stat %s: %w", readme, err)
		} else {
			actions = append(actions, Action{
				Kind:    ActionUnchanged,
				Target:  readme,
				Summary: kind + "/README.md already present",
			})
		}
	}
	return Plan{Actions: actions}, nil
}

// Apply executes a Plan. For materialize mode, the in-memory lock
// snapshot carried from Plan is updated per action and persisted at
// end. For scaffold mode, Apply is a straight mkdir/write loop.
func (r *TemplateTreeReconciler) Apply(_ context.Context, p Plan) (Report, error) {
	if r.seed.Mode == TemplateModeScaffold {
		return r.applyScaffold(p)
	}
	return r.applyMaterialize(p)
}

func (r *TemplateTreeReconciler) applyMaterialize(p Plan) (Report, error) {
	var rep Report

	// Ensure the canonical gitignore patterns are in place before any
	// sidecars appear. Done once per Apply, not per action.
	if err := storage.ReconcileVaultGitignore(r.vaultRoot); err != nil {
		rep.Errors = append(rep.Errors, fmt.Errorf("reconcile gitignore: %w", err))
	}

	// Coupling note: Apply resolves embedded bytes via byTarget[a.Target]
	// below. If an orchestrator rewrites an action's Target (e.g. to a
	// ".new" sidecar path), the lookup will miss and Create/Update will
	// error. Phase 3's resolveTemplatePrompts therefore writes ".new"
	// files directly and drops the action rather than rewriting Target.
	var state templateTreePlanState
	if r.planState != nil {
		state = *r.planState
	}
	if state.lock.Entries == nil {
		// Plan wasn't called (or was called on a different instance):
		// re-read the lock so Apply can still merge correctly.
		if l, err := templates.ReadLock(r.vaultRoot); err == nil {
			state.lock = l
		} else {
			state.lock.Entries = map[string]templates.LockEntry{}
		}
	}

	// Build an embedded-bytes lookup so we don't re-walk the FS per
	// action.
	resources, err := templates.WalkEmbedded()
	if err != nil {
		return rep, fmt.Errorf("walk embedded: %w", err)
	}
	byTarget := make(map[string]templates.Resource, len(resources))
	for _, res := range resources {
		key := r.vaultRelFromEmbedded(res.RelPath)
		target := filepath.Join(r.vaultRoot, filepath.FromSlash(key))
		byTarget[target] = res
	}

	now := time.Now().UTC()
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionPrompt:
			return rep, fmt.Errorf("template_tree Apply: received ActionPrompt for %s — orchestrator must resolve Prompt actions before Apply", a.Target)
		case ActionCreate:
			res, ok := byTarget[a.Target]
			if !ok {
				rep.Errors = append(rep.Errors, fmt.Errorf("create: no embedded resource for %s", a.Target))
				continue
			}
			if err := os.MkdirAll(filepath.Dir(a.Target), 0o755); err != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("mkdir %s: %w", filepath.Dir(a.Target), err))
				continue
			}
			if err := atomicWriteFile(a.Target, res.Bytes, 0o644); err != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("write %s: %w", a.Target, err))
				continue
			}
			embSHA, ok := templates.EmbeddedSHA(res.RelPath)
			if !ok {
				embSHA = res.SHA256
			}
			state.lock.Entries[r.vaultRelFromEmbedded(res.RelPath)] = templates.LockEntry{
				EmbeddedSHA: embSHA,
				WrittenAt:   now,
			}
			rep.Created++
		case ActionUpdate:
			res, ok := byTarget[a.Target]
			if !ok {
				rep.Errors = append(rep.Errors, fmt.Errorf("update: no embedded resource for %s", a.Target))
				continue
			}
			// Write .bak of current bytes (non-atomic, last-writer-wins).
			if cur, err := os.ReadFile(a.Target); err == nil {
				bakPath := a.Target + ".bak"
				if err := os.WriteFile(bakPath, cur, 0o644); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("write bak %s: %w", bakPath, err))
					continue
				}
			} else if !os.IsNotExist(err) {
				rep.Errors = append(rep.Errors, fmt.Errorf("read for bak %s: %w", a.Target, err))
				continue
			}
			if err := atomicWriteFile(a.Target, res.Bytes, 0o644); err != nil {
				rep.Errors = append(rep.Errors, fmt.Errorf("write %s: %w", a.Target, err))
				continue
			}
			embSHA, ok := templates.EmbeddedSHA(res.RelPath)
			if !ok {
				embSHA = res.SHA256
			}
			state.lock.Entries[r.vaultRelFromEmbedded(res.RelPath)] = templates.LockEntry{
				EmbeddedSHA: embSHA,
				WrittenAt:   now,
			}
			rep.Updated++
		case ActionUnchanged:
			rep.Unchanged++
		case ActionSkip:
			rep.Skipped++
		}
	}

	if err := templates.WriteLock(r.vaultRoot, state.lock); err != nil {
		rep.Errors = append(rep.Errors, fmt.Errorf("write lock: %w", err))
	}
	return rep, nil
}

func (r *TemplateTreeReconciler) applyScaffold(p Plan) (Report, error) {
	var rep Report
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionPrompt:
			return rep, fmt.Errorf("template_tree Apply (scaffold): unexpected ActionPrompt for %s", a.Target)
		case ActionCreate:
			base := filepath.Base(a.Target)
			if base == "README.md" {
				if err := os.MkdirAll(filepath.Dir(a.Target), 0o755); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("mkdir %s: %w", filepath.Dir(a.Target), err))
					continue
				}
				// Decide kind by parent directory name.
				kind := filepath.Base(filepath.Dir(a.Target))
				body := templates.RenderReadmeStub(kind)
				if body == "" {
					rep.Errors = append(rep.Errors, fmt.Errorf("unknown readme kind for %s", a.Target))
					continue
				}
				// Write-if-absent only; race-safety via O_EXCL would be
				// nicer, but scaffold mode runs serially per reconciler.
				if _, err := os.Stat(a.Target); err == nil {
					rep.Unchanged++
					continue
				}
				if err := os.WriteFile(a.Target, []byte(body), 0o644); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("write %s: %w", a.Target, err))
					continue
				}
				rep.Created++
			} else {
				if err := os.MkdirAll(a.Target, 0o755); err != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("mkdir %s: %w", a.Target, err))
					continue
				}
				rep.Created++
			}
		case ActionUnchanged:
			rep.Unchanged++
		case ActionSkip:
			rep.Skipped++
		case ActionUpdate:
			rep.Updated++
		}
	}
	return rep, nil
}

// atomicWriteFile writes data to path via a sibling tmp-file + rename.
// Leaves no partial file on failure.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmpl.*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
