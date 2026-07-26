// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// stampVaultWrite best-effort records the MCP surface version for a successful
// vault write at path under vaultRoot. A stamp failure is logged and never
// propagated, so it can never fail the underlying write. Pass vaultRoot == ""
// (or a non-vault path) to skip stamping; surface resolves those to no stamp
// dir. Shared by the materialize/scaffold/upgrade apply paths in this package.
func stampVaultWrite(vaultRoot, path string) {
	if err := surface.StampForPath(vaultRoot, path); err != nil {
		slog.Warn("surface stamp failed", "path", path, "err", err)
	}
}

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

// hashFile delegates to templates.HashFile so the reconciler and the
// templates.Executor share one hash primitive for on-disk bytes.
func hashFile(path string) (string, error) { return templates.HashFile(path) }

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
		vaultExists := herr == nil
		embSHA, ok := templates.EmbeddedSHA(res.RelPath)
		if !ok {
			embSHA = res.SHA256
		}
		entry, haveLock := lock.Entries[key]

		// Override-only model: no vault mirror is the healthy state (the
		// embedded floor serves it). A byte-identical mirror is drift
		// pending a prune; a genuine override is in sync.
		status := check.Info
		summary := "drift"
		switch {
		case !vaultExists && !haveLock:
			status = check.Pass
			summary = "served from embedded floor"
		case !vaultExists && haveLock:
			// Dangling lock entry: sync will drop it. Drift, not fatal.
			summary = "drift (dangling lock entry; embedded floor serves it)"
		case haveLock && vaultSHA == entry.EmbeddedSHA:
			summary = "drift (reconciler-owned mirror pending prune)"
		case haveLock && vaultSHA == embSHA:
			summary = "drift (byte-identical to current embedded; pending prune)"
		case haveLock && embSHA == entry.EmbeddedSHA:
			status = check.Pass
			summary = "user override"
			// default: diverged override (haveLock) or no-lock file — drift
			// pending a prompt.
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

		// --- Decision table (Design B: override-only materialization) ---
		// The embedded floor is served directly over MCP, so the vault
		// Templates/ mirror is override-only. Byte-identical mirrors are
		// pruned (embedded serves them); genuine overrides are kept. The
		// KEY invariant: vault bytes still equal the lock's recorded
		// embedded baseline ⇒ reconciler-owned (user never edited it) ⇒
		// safe to prune; vault bytes differ from the baseline ⇒ user
		// override ⇒ keep.
		switch {
		case !vaultExists:
			// Case 1: no vault mirror → the embedded floor serves it. Do
			// nothing to disk (replaces the old Create). Drop any dangling
			// lock entry so the persisted lock lists only real overrides.
			if haveLock {
				delete(lock.Entries, key)
			}
			actions = append(actions, Action{
				Kind:    ActionUnchanged,
				Target:  target,
				Summary: key + " served from embedded floor",
			})
		case haveLock && vaultSHA == entry.EmbeddedSHA:
			// Case 2: vault bytes still equal the lock baseline →
			// reconciler-owned mirror (covers old Row 2 all-match and old
			// Row 3 embedded-bumped-but-vault-still-baseline). Prune.
			actions = append(actions, Action{
				Kind:    ActionDelete,
				Target:  target,
				Summary: "prune " + key + " (reconciler-owned mirror; embedded floor serves it)",
				Details: []string{
					"embedded_sha=" + embSHA,
					"vault_sha=" + vaultSHA,
					"lock_sha=" + entry.EmbeddedSHA,
				},
			})
		case haveLock && vaultSHA == embSHA:
			// Case 3: vault bytes are byte-identical to CURRENT embedded
			// while the lock baseline is stale (old Row 4b relock case).
			// The mirror is redundant → prune. Ordered ABOVE case 4/5,
			// whose predicates would otherwise swallow it.
			actions = append(actions, Action{
				Kind:    ActionDelete,
				Target:  target,
				Summary: "prune " + key + " (byte-identical to current embedded; lock stale)",
				Details: []string{
					"embedded_sha=" + embSHA,
					"vault_sha=" + vaultSHA,
					"lock_sha=" + entry.EmbeddedSHA,
				},
			})
		case haveLock && embSHA == entry.EmbeddedSHA:
			// Case 4: vault bytes differ from the baseline but embedded is
			// stable → genuine user override, embedded unchanged. Keep
			// (old Row 4).
			actions = append(actions, Action{
				Kind:    ActionUnchanged,
				Target:  target,
				Summary: key + " user override (kept)",
			})
		case haveLock:
			// Case 5: vault bytes differ from the baseline AND embedded
			// bumped (embSHA != baseline, since cases 2/3/4 fell through)
			// → diverged override (user-edited AND embedded bumped).
			// Prompt (old Row 5).
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
		default:
			// Case 6: lock absent AND file exists. The silent-adopt
			// pre-pass would have planted a lock entry when vaultSHA ==
			// embSHA, so reaching here means vaultSHA ≠ embSHA → can't
			// prove reconciler-owned; treat as a user override → Prompt
			// (old no-lock branch).
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
	templateExec := templates.NewExecutor()

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
			// Create: nothing to preserve, so Backup=Never. VaultRoot lets
			// Executor.Write stamp .surface structurally — no out-of-band
			// stampVaultWrite needed here any more.
			if err := templateExec.Write(a.Target, res.Bytes, templates.WriteOptions{Backup: templates.BackupPolicyNever, VaultRoot: r.vaultRoot}); err != nil {
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
			// Update: preserve pre-existing bytes as .bak (copy-then-
			// rename so the primary stays readable throughout). VaultRoot lets
			// Executor.Write stamp .surface structurally — no out-of-band
			// stampVaultWrite needed here any more.
			if err := templateExec.Write(a.Target, res.Bytes, templates.WriteOptions{Backup: templates.BackupPolicyAlways, VaultRoot: r.vaultRoot}); err != nil {
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
		case ActionDelete:
			// Prune a reconciler-owned mirror so the embedded floor serves
			// the resource. Back the file up to a sibling .bak first —
			// mirroring the BackupPolicyAlways discipline the Update path
			// uses — then remove the primary and drop its lock entry so the
			// persisted lock no longer lists it. os.Remove tolerates an
			// already-gone file (Check-mode drift may race a manual delete).
			res, ok := byTarget[a.Target]
			if !ok {
				rep.Errors = append(rep.Errors, fmt.Errorf("prune: no embedded resource for %s", a.Target))
				continue
			}
			if cur, rerr := os.ReadFile(a.Target); rerr == nil {
				if werr := os.WriteFile(a.Target+".bak", cur, 0o644); werr != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("prune backup %s: %w", a.Target+".bak", werr))
					continue
				}
			} else if !os.IsNotExist(rerr) {
				rep.Errors = append(rep.Errors, fmt.Errorf("prune read %s: %w", a.Target, rerr))
				continue
			}
			if err := os.Remove(a.Target); err != nil && !os.IsNotExist(err) {
				rep.Errors = append(rep.Errors, fmt.Errorf("prune remove %s: %w", a.Target, err))
				continue
			}
			delete(state.lock.Entries, r.vaultRelFromEmbedded(res.RelPath))
			rep.Pruned++
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
				stampVaultWrite(r.vaultRoot, a.Target)
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

// atomicWriteFile was the reconciler's private atomic-write helper.
// It has been promoted to templates.Executor.Write (internal atomic
// primitive); this file's three call sites now delegate.
