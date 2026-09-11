// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
// operates on: the override-only reconcile of vault Templates/ against the
// embedded corpus, or a per-project scaffold (just directories + README
// stubs).
type TemplateMode string

const (
	// TemplateModeMaterialize reconciles <vault>/<relSubpath>/ against the
	// embedded corpus OVERRIDE-ONLY (ADR-008, Design B). It never writes a
	// template on its own: an absent vault file is served from the embedded
	// floor, a byte-identical mirror is pruned, and a genuine override is kept
	// — or, when the embedded copy moved under it, prompted. templates.lock
	// records the baseline that makes a prune safe. The name predates Design B
	// and is kept because it is the mode's identifier, not its description.
	TemplateModeMaterialize TemplateMode = "materialize"
	// TemplateModeScaffold creates commands/ and skills/ subdirectories
	// with README stubs but does not materialize any embedded content.
	TemplateModeScaffold TemplateMode = "scaffold"
)

// TemplateTreeSeed parameterises one reconciler instance.
type TemplateTreeSeed struct {
	// Mode picks materialize vs scaffold semantics.
	Mode TemplateMode
	// ExternalPrune hands every Delete to the caller: materialize Apply
	// neither removes the file nor drops its lock entry. `vp config sync`
	// sets it on a git vault, where a prune is only safe once HEAD (and each
	// remote tip) has been checked, and its lock entry may only go once the
	// removal is committed — see storage.PruneMirrorsVerified and
	// ForgetPruned.
	ExternalPrune bool
}

// TemplateTreeReconciler implements the Reconciler interface against
// the embedded templates corpus and the templates.lock sidecar.
//
// Layout:
//   - Mode=Materialize, relSubpath="Templates": each embedded resource is
//     reconciled against <vaultRoot>/Templates/<rel>, which holds only the
//     overrides an operator wrote. Nothing is copied in. Lock keys are the
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

// checkMaterialize delegates to check.TemplateDriftRows, which owns the
// classification. It used to live here, which is exactly what kept template
// drift out of check.Producers and therefore off the MCP surface: reconcile
// imports check, so the registry could never reach back into this package. The
// computation needs internal/templates, not this reconciler, so it moved to
// where both callers can share it — one definition of drift, not two.
func (r *TemplateTreeReconciler) checkMaterialize() []check.Result {
	return check.TemplateDriftRows(r.vaultRoot, r.relSubpath, r.Name())
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
// scaffold mode it emits at most 4 Create actions (two directories +
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
			// ENOENT is the normal case — no override exists, and the
			// main decision-table loop below plans case 1 (served from
			// the embedded floor). Any other error (permission, IO) is
			// unexpected and worth logging: silent-adopt would skip it
			// here and the main loop would then classify the file as
			// user-edited (producing a confusing Prompt row) without
			// any diagnostic trail.
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
		case !vaultExists && haveLock:
			// Case 1b: no vault file, but a lock entry. Either a prune whose
			// removal was never committed (a git failure after the remove —
			// the entry is only dropped once the outcome is known), or a file
			// someone else removed. Plan a Delete of the absent file: on a git
			// vault the verified prune commits the removal when HEAD's copy is
			// vp's and leaves it alone otherwise; either way the entry goes.
			actions = append(actions, Action{
				Kind:    ActionDelete,
				Target:  target,
				Summary: "prune " + key + " (already removed from the worktree)",
				Details: []string{
					"embedded_sha=" + embSHA,
					"vault_sha=",
					"lock_sha=" + entry.EmbeddedSHA,
				},
			})
		case !vaultExists:
			// Case 1: no vault mirror → the embedded floor serves it. Do
			// nothing to disk (replaces the old Create).
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
				Summary: key + " operator override of a built-in (kept)",
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
// end; the only file operation it performs is a verified prune. For
// scaffold mode, Apply is a straight mkdir/write loop.
func (r *TemplateTreeReconciler) Apply(_ context.Context, p Plan) (Report, error) {
	if r.seed.Mode == TemplateModeScaffold {
		return r.applyScaffold(p)
	}
	return r.applyMaterialize(p)
}

// applyMaterialize never writes a template. `vp config sync` used to
// resolve a diverged override's `o` answer (and --yes) into an Update that
// overwrote the operator's bytes with the embedded copy; the next sync then
// classified the result as a reconciler-owned mirror, pruned it, and committed
// and pushed the deletion — the operator's override was gone on every host.
// No Plan emits Create or Update in this mode and no orchestrator answer
// produces one any more, so either kind reaching here is a defect in the
// caller and is reported as an error rather than executed.
//
// The one file operation left is the prune, and it is verified twice: at Plan
// time (the decision table) and again immediately before os.Remove, against
// the SHAs the plan recorded (see PruneBasis). A file edited in between —
// the orchestrator can sit on a prompt for as long as the operator likes — is
// kept, not removed. With ExternalPrune (a git vault) Apply leaves every
// Delete to the caller, which must also check the committed copy first.
func (r *TemplateTreeReconciler) applyMaterialize(p Plan) (Report, error) {
	var rep Report

	// Ensure the canonical gitignore patterns are in place before any
	// sidecars appear. Done once per Apply, not per action.
	if err := storage.ReconcileVaultGitignore(r.vaultRoot); err != nil {
		rep.Errors = append(rep.Errors, fmt.Errorf("reconcile gitignore: %w", err))
	}

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

	// Map each target back to its lock key so a prune can drop its entry.
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

	for _, a := range p.Actions {
		switch a.Kind {
		case ActionPrompt:
			return rep, fmt.Errorf("template_tree Apply: received ActionPrompt for %s — orchestrator must resolve Prompt actions before Apply", a.Target)
		case ActionCreate, ActionUpdate:
			rep.Errors = append(rep.Errors, fmt.Errorf("template_tree Apply: unexpected %s for %s — the Templates reconcile never writes a template", a.Kind, a.Target))
		case ActionDelete:
			// Prune a reconciler-owned mirror so the embedded floor serves
			// the resource, then drop its lock entry so the persisted lock no
			// longer lists it.
			//
			// No .bak is written. The bytes removed are provably an embedded
			// copy (the re-hash below), so a backup would preserve nothing —
			// and a .bak already beside the file belongs to an earlier
			// overwrite or upgrade reset, which is exactly the copy of the
			// operator's bytes the old prune backup used to overwrite.
			res, ok := byTarget[a.Target]
			if !ok {
				rep.Errors = append(rep.Errors, fmt.Errorf("prune: no embedded resource for %s", a.Target))
				continue
			}
			accept := PruneBasis(a)
			if len(accept) == 0 {
				rep.Errors = append(rep.Errors, fmt.Errorf("prune %s: the plan recorded no SHA to verify the file against; kept", a.Target))
				continue
			}
			if r.seed.ExternalPrune {
				// The caller removes it, after checking HEAD and the remotes,
				// and drops the lock entry once the outcome is known.
				continue
			}
			// Re-hash immediately before the remove. This closes the
			// Plan→Apply window: the orchestrator may block on a prompt
			// between the two, and an edit made meanwhile must be kept, not
			// removed. A file already gone (a concurrent sync, a manual rm)
			// needs no removal: its entry goes, and it is not counted as
			// pruned, because this run removed nothing.
			cur, herr := hashFile(a.Target)
			switch {
			case herr != nil && !os.IsNotExist(herr):
				rep.Errors = append(rep.Errors, fmt.Errorf("prune read %s: %w", a.Target, herr))
				continue
			case herr != nil:
				delete(state.lock.Entries, r.vaultRelFromEmbedded(res.RelPath))
				rep.Unchanged++
				continue
			case accept[cur] == "":
				rep.Skipped++
				rep.Notes = append(rep.Notes, r.vaultRelFromEmbedded(res.RelPath)+" changed since plan; kept")
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

// PruneBasis is the one definition of the bytes a prune may remove: each SHA
// the Plan recorded in a Delete's Details, mapped to the words a commit
// message uses for it. The embedded SHA is vp's by definition; a lock
// baseline is vp's because templates.lock's only writer records the embedded
// SHA of what it wrote. An empty value (lock_sha= on a lock-less row)
// contributes nothing, and when both are one SHA the current embedded copy
// names it. The Apply re-hash and `vp config sync`'s HEAD and remote checks
// all read this, so they cannot drift apart.
func PruneBasis(a Action) map[string]string {
	basis := map[string]string{}
	if v := a.Detail("lock_sha"); v != "" {
		basis[v] = "lock-recorded embedded version"
	}
	if v := a.Detail("embedded_sha"); v != "" {
		basis[v] = "current embedded copy"
	}
	return basis
}

// ForgetPruned drops the lock entries for vault-relative keys whose prune
// outcome is known and final: removed and committed, never tracked, or gone
// by someone else's hand. It is the other half of ExternalPrune; entries of
// restored or kept files stay, so a restored override remains a silent keep
// rather than a Prompt on every sync.
func (r *TemplateTreeReconciler) ForgetPruned(keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	lock, err := templates.ReadLock(r.vaultRoot)
	if err != nil {
		return err
	}
	for _, k := range keys {
		delete(lock.Entries, k)
	}
	return templates.WriteLock(r.vaultRoot, lock)
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
				// Write-if-absent, and the absence check IS the write.
				//
				// This used to be a stat/WriteFile pair, with a comment saying
				// "race-safety via O_EXCL would be nicer, but scaffold mode
				// runs serially per reconciler". That premise was already
				// thin, and it is now false outright: `vp init` and the vp_init
				// MCP tool both drive internal/onboard, so two processes can
				// scaffold the same Projects/<slug> concurrently and interleave
				// between the stat and the write. O_EXCL is the option the old
				// comment named and declined; take it.
				//
				// EEXIST is the concurrent-loser path and counts as Unchanged
				// rather than an error — the other writer created the same
				// stub, which is the outcome this branch wanted anyway.
				f, err := os.OpenFile(a.Target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
				if err != nil {
					if errors.Is(err, fs.ErrExist) {
						rep.Unchanged++
						continue
					}
					rep.Errors = append(rep.Errors, fmt.Errorf("write %s: %w", a.Target, err))
					continue
				}
				_, werr := f.Write([]byte(body))
				if cerr := f.Close(); werr == nil {
					werr = cerr
				}
				if werr != nil {
					rep.Errors = append(rep.Errors, fmt.Errorf("write %s: %w", a.Target, werr))
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
