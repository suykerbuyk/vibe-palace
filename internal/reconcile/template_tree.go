// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
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
	// template and never prompts: an absent vault file is served from the
	// embedded floor, a copy the binary can prove is vp's — the current
	// embedded copy or an earlier shipped version, line endings aside
	// (templates.ClassifyVaultCopy) — is pruned, and anything else is an
	// operator's override, kept. No host-local state is read or written. The
	// name predates Design B and is kept because it is the mode's identifier,
	// not its description.
	TemplateModeMaterialize TemplateMode = "materialize"
	// TemplateModeScaffold creates commands/ and skills/ subdirectories
	// with README stubs but does not materialize any embedded content.
	TemplateModeScaffold TemplateMode = "scaffold"
)

// TemplateTreeSeed parameterises one reconciler instance.
type TemplateTreeSeed struct {
	// Mode picks materialize vs scaffold semantics.
	Mode TemplateMode
	// ExternalPrune hands every Delete to the caller: materialize Apply does
	// not remove the file. `vp config sync` sets it on a git vault, where a
	// prune is only safe once HEAD (and each remote tip) has been checked —
	// see storage.PruneMirrorsVerified.
	ExternalPrune bool
	// PendingRemovals are tracked files under the tree that were removed from
	// the worktree and whose removal is not committed, keyed by vault-relative
	// path ("Templates/commands/wrap.md"), each with HEAD's copy as git would
	// check it out (storage.UncommittedRemovals). A path whose committed copy
	// is vp-shipped plans a Delete, so the caller's verified prune commits the
	// removal; one whose committed copy is operator content plans Unchanged
	// with the ways to finish or undo it. `vp config sync` fills it only on a
	// vault that is its own repository.
	PendingRemovals map[string][]byte
}

// TemplateTreeReconciler implements the Reconciler interface against
// the embedded templates corpus.
//
// Layout:
//   - Mode=Materialize, relSubpath="Templates": each embedded resource is
//     reconciled against <vaultRoot>/Templates/<rel>, which holds only the
//     overrides an operator wrote. Nothing is copied in.
//   - Mode=Scaffold, relSubpath="Projects/<slug>": commands/ and
//     skills/ subdirectories with README stubs, write-if-absent.
type TemplateTreeReconciler struct {
	vaultRoot  string
	relSubpath string
	seed       TemplateTreeSeed
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

// vaultRelFromEmbedded computes the vault-relative target key for an
// embedded resource (e.g. "commands/wrap.md" → "Templates/commands/wrap.md").
func (r *TemplateTreeReconciler) vaultRelFromEmbedded(embeddedRel string) string {
	return r.relSubpath + "/" + embeddedRel
}

// pruneBeforeRemoveHook runs after a prune's bytes are re-read and accepted
// and immediately before the compare-and-set removal. Production leaves it a
// no-op; a test edits the file from it, which is otherwise a race no test can
// time.
var pruneBeforeRemoveHook = func(key string) {}

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
		info, err := os.Stat(readme)
		if os.IsNotExist(err) {
			status = check.Info
			summary = r.relSubpath + " scaffolding: Info (missing " + kind + "/README.md)"
			break
		}
		// A zero-length README is a crash-left or raced stub (see the
		// applyScaffold write-path comment): the create left the file
		// observable at length zero before the body landed. It is never a
		// legitimate steady state, so it is reported and repaired exactly
		// like a missing one, not silently treated as Pass.
		if err == nil && info.Size() == 0 {
			status = check.Info
			summary = r.relSubpath + " scaffolding: Info (empty " + kind + "/README.md)"
			break
		}
	}
	return check.Result{
		Name:    r.Name(),
		Status:  status,
		Summary: summary,
	}
}

// Plan emits the provenance-table actions for materialize mode; for
// scaffold mode it emits at most 4 Create actions (two directories +
// two READMEs — one per subdir).
func (r *TemplateTreeReconciler) Plan(_ context.Context) (Plan, error) {
	if r.seed.Mode == TemplateModeScaffold {
		return r.planScaffold()
	}
	return r.planMaterialize()
}

// planMaterialize classifies every embedded resource's vault copy by
// provenance — the binary alone, no host-local state:
//
//	vault copy                         action     why
//	absent, not pending                Unchanged  served from the embedded floor
//	absent, pending, HEAD vp-shipped   Delete     the verified prune commits it
//	absent, pending, HEAD operator     Unchanged  names the reset verb / restore
//	not reached directly               Unchanged  kept, never followed
//	current (line endings aside)       Delete     prune
//	earlier shipped version            Delete     prune; recoverable from history
//	operator                           Unchanged  an override, kept
//	unreadable                         Plan error
//
// It never plans a Create, an Update or a prompt. A Delete's Details carry
// embedded_relpath=, provenance=, vault_sha= (the raw sha256 of the worktree
// bytes, empty when absent), key= (the ProvenanceKey a shipped.txt row
// matches) and, on a pending row, pending=true.
func (r *TemplateTreeReconciler) planMaterialize() (Plan, error) {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		return Plan{}, fmt.Errorf("walk embedded: %w", err)
	}

	var actions []Action
	for _, res := range resources {
		key := r.vaultRelFromEmbedded(res.RelPath)
		target := filepath.Join(r.vaultRoot, filepath.FromSlash(key))

		// A symlink anywhere in the path, a special file, or on Windows a
		// spelling the disk does not share: kept, never followed. Reading or
		// removing through it would act on a file this tree does not own.
		if err := vaultfs.CheckDirectPath(r.vaultRoot, key); err != nil {
			if !errors.Is(err, vaultfs.ErrIndirectPath) {
				return Plan{}, fmt.Errorf("inspect %s: %w", target, err)
			}
			actions = append(actions, Action{
				Kind:    ActionUnchanged,
				Target:  target,
				Summary: key + " is " + check.NotReachedDirectly,
				Details: []string{"reason=" + err.Error()},
			})
			continue
		}

		data, err := os.ReadFile(target)
		if errors.Is(err, os.ErrNotExist) {
			head, pending := r.seed.PendingRemovals[key]
			if !pending {
				actions = append(actions, Action{
					Kind:    ActionUnchanged,
					Target:  target,
					Summary: key + " served from embedded floor",
				})
				continue
			}
			// Removed from the worktree before this sync, and never
			// committed. The verified prune finishes it only when the
			// committed copy is vp-shipped bytes.
			prov := templates.ClassifyVaultCopy(res.RelPath, head)
			if prov == templates.ProvenanceOperator {
				actions = append(actions, Action{
					Kind:    ActionUnchanged,
					Target:  target,
					Summary: key + " removed from the worktree; the committed copy is operator content — " + finishOperatorRemoval(r.vaultRoot, key, res.RelPath),
				})
				continue
			}
			actions = append(actions, Action{
				Kind:   ActionDelete,
				Target: target,
				Summary: "prune " + key + " (removed from the worktree before this sync and not committed; the committed copy is " +
					committedBasis(prov, res.RelPath) + ")",
				Details: pruneDetails(res.RelPath, prov, nil, head, true),
			})
			continue
		}
		if err != nil {
			return Plan{}, fmt.Errorf("read %s: %w", target, err)
		}

		switch prov := templates.ClassifyVaultCopy(res.RelPath, data); prov {
		case templates.ProvenanceCurrent:
			actions = append(actions, Action{
				Kind:    ActionDelete,
				Target:  target,
				Summary: "prune " + key + " (byte-identical, line endings aside, to the current embedded copy)",
				Details: pruneDetails(res.RelPath, prov, data, data, false),
			})
		case templates.ProvenanceEarlier:
			actions = append(actions, Action{
				Kind:    ActionDelete,
				Target:  target,
				Summary: "prune " + key + " (matched an earlier shipped version of " + res.RelPath + "; recoverable from vibe-palace history)",
				Details: pruneDetails(res.RelPath, prov, data, data, false),
			})
		default:
			actions = append(actions, Action{
				Kind:    ActionUnchanged,
				Target:  target,
				Summary: key + " operator override of a built-in (kept)",
			})
		}
	}
	return Plan{Actions: actions}, nil
}

// pruneDetails is a Delete's Details. worktree is the file's bytes (nil when
// absent: vault_sha= is then empty); keyed is the copy the classification
// judged — the worktree bytes, or HEAD's copy for a pending removal.
func pruneDetails(embeddedRel string, prov templates.Provenance, worktree, keyed []byte, pending bool) []string {
	vaultSHA := ""
	if worktree != nil {
		sum := sha256.Sum256(worktree)
		vaultSHA = hex.EncodeToString(sum[:])
	}
	d := []string{
		"embedded_relpath=" + embeddedRel,
		"provenance=" + prov.String(),
		"vault_sha=" + vaultSHA,
		"key=" + templates.ProvenanceKey(keyed),
	}
	if pending {
		d = append(d, "pending=true")
	}
	return d
}

// committedBasis names what a committed copy is, in the words a pending
// prune row and its commit use.
func committedBasis(prov templates.Provenance, embeddedRel string) string {
	if prov == templates.ProvenanceEarlier {
		return "an earlier shipped version of " + embeddedRel
	}
	return "the current embedded copy"
}

// finishOperatorRemoval tells the operator how to finish, or undo, a removal
// of operator content they left uncommitted. vp never commits it: the grant
// covers vp-shipped bytes only.
func finishOperatorRemoval(vaultRoot, key, embeddedRel string) string {
	restore := "or restore it with git -C " + vaultRoot + " checkout HEAD -- " + key
	var verb string
	switch {
	case strings.HasPrefix(embeddedRel, "commands/"):
		verb = "vp commands reset " + strings.TrimSuffix(strings.TrimPrefix(embeddedRel, "commands/"), ".md")
	case strings.HasPrefix(embeddedRel, "skills/"):
		verb = "vp skills reset " + strings.TrimPrefix(embeddedRel, "skills/")
	default:
		return "finish removing it with git -C " + vaultRoot + " commit -m \"chore(templates): remove an override\" -- " + key + ", " + restore
	}
	return "finish removing it with " + verb + " (commits the removal; a backup an earlier reset wrote stays beside it), " + restore
}

// emptySHA256 is sha256("") — the digest of a zero-length file. planScaffold
// carries it as a Detail on the ActionUpdate it emits for a zero-length
// README so applyScaffold's repair write can CAS against "still exactly
// empty" rather than blindly overwriting whatever landed there since Plan.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func (r *TemplateTreeReconciler) planScaffold() (Plan, error) {
	sub := r.subpathAbs()
	var actions []Action
	for _, kind := range []string{"commands", "skills"} {
		dir := filepath.Join(sub, kind)

		// The README write in applyScaffold goes through vaultfs.Create,
		// which enforces cross-platform path portability and refuses to
		// follow a symlinked directory. Check both here, before planning
		// anything: planning the directory Create and then having the
		// README write refused would leave an empty commands/skills/ dir
		// behind and fail the whole sync (the bug this guards against).
		// Reuse the exact relPath applyScaffold builds for its own
		// vaultfs calls so the two can never drift apart.
		readmeRelPath := r.relSubpath + "/" + kind + "/README.md"
		if err := vaultfs.ValidateRelPath(readmeRelPath); err != nil {
			actions = append(actions, Action{
				Kind:    ActionSkip,
				Target:  dir,
				Summary: "skip " + r.relSubpath + "/" + kind + "/: not portable (" + err.Error() + ")",
			})
			continue
		}
		if err := vaultfs.CheckDirectPath(r.vaultRoot, readmeRelPath); err != nil {
			if !errors.Is(err, vaultfs.ErrIndirectPath) {
				return Plan{}, fmt.Errorf("inspect %s: %w", dir, err)
			}
			actions = append(actions, Action{
				Kind:    ActionSkip,
				Target:  dir,
				Summary: "skip " + r.relSubpath + "/" + kind + "/: not scaffolded (" + check.NotReachedDirectly + "): " + err.Error(),
			})
			continue
		}

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
		info, err := os.Stat(readme)
		switch {
		case os.IsNotExist(err):
			actions = append(actions, Action{
				Kind:    ActionCreate,
				Target:  readme,
				Summary: "scaffold " + r.relSubpath + "/" + kind + "/README.md",
			})
		case err != nil:
			return Plan{}, fmt.Errorf("stat %s: %w", readme, err)
		case info.Size() == 0:
			// A crash between vaultfs.Create's rename and a reader's next
			// look — or a manual touch — leaves the README present but
			// empty. RenderReadmeStub never emits an empty body for a valid
			// kind, so zero length is unambiguous: repair it, CAS-guarded
			// against clobbering real content that landed in between.
			actions = append(actions, Action{
				Kind:    ActionUpdate,
				Target:  readme,
				Summary: "repair zero-length " + kind + "/README.md (crash-left or raced stub)",
				Details: []string{"expected_sha256=" + emptySHA256},
			})
		default:
			actions = append(actions, Action{
				Kind:    ActionUnchanged,
				Target:  readme,
				Summary: kind + "/README.md already present",
			})
		}
	}
	return Plan{Actions: actions}, nil
}

// Apply executes a Plan. For materialize mode the only file operation it
// performs is a verified prune; for scaffold mode, Apply is a straight
// mkdir/write loop.
func (r *TemplateTreeReconciler) Apply(_ context.Context, p Plan) (Report, error) {
	if r.seed.Mode == TemplateModeScaffold {
		return r.applyScaffold(p)
	}
	return r.applyMaterialize(p)
}

// applyMaterialize never writes a template. `vp config sync` used to resolve
// a diverged override's `o` answer (and --yes) into an Update that overwrote
// the operator's bytes with the embedded copy; the next sync then classified
// the result as a reconciler-owned mirror, pruned it, and committed and pushed
// the deletion — the operator's override was gone on every host. No Plan emits
// Create or Update in this mode, so either kind reaching here is a defect in
// the caller and is reported as an error rather than executed. It writes no
// lock and no .gitignore: the Vault reconciler's locked top-up owns that file.
//
// The one file operation left is the prune, and it is verified three times:
// at Plan time (the provenance table), again immediately before the removal
// (PruneAccepts on freshly read bytes, reached directly), and by the removal
// itself — vaultfs.Delete, a compare-and-set on those bytes under the path's
// advisory lock, so an edit landing between the re-read and the remove is
// kept. With ExternalPrune (a git vault) Apply leaves every Delete to the
// caller, which must also check the committed copy first.
//
// No .bak is written. The bytes removed are provably vp-shipped: the current
// embedded copy, or a frozen shipped.txt row whose blob is in vibe-palace's
// history. A .bak already beside the file belongs to an earlier overwrite or
// a reset, and is never touched.
func (r *TemplateTreeReconciler) applyMaterialize(p Plan) (Report, error) {
	var rep Report
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionCreate, ActionUpdate:
			rep.Errors = append(rep.Errors, fmt.Errorf("template_tree Apply: unexpected %s for %s — the Templates reconcile never writes a template", a.Kind, a.Target))
		case ActionDelete:
			embeddedRel := a.Detail("embedded_relpath")
			if embeddedRel == "" {
				rep.Errors = append(rep.Errors, fmt.Errorf("prune %s: the plan recorded no embedded_relpath to verify the file against; kept", a.Target))
				continue
			}
			if r.seed.ExternalPrune {
				// The caller removes it, after checking HEAD and the remotes.
				continue
			}
			key := r.vaultRelFromEmbedded(embeddedRel)
			if err := vaultfs.CheckDirectPath(r.vaultRoot, key); err != nil {
				if errors.Is(err, vaultfs.ErrIndirectPath) {
					rep.Skipped++
					rep.Notes = append(rep.Notes, key+" is no longer reached directly; kept")
				} else {
					rep.Errors = append(rep.Errors, fmt.Errorf("prune inspect %s: %w", a.Target, err))
				}
				continue
			}
			// A file already gone (a concurrent sync, a manual rm) needs no
			// removal, and is not counted as pruned: this run removed nothing.
			data, err := os.ReadFile(a.Target)
			switch {
			case errors.Is(err, os.ErrNotExist):
				rep.Unchanged++
				continue
			case err != nil:
				rep.Errors = append(rep.Errors, fmt.Errorf("prune read %s: %w", a.Target, err))
				continue
			case !PruneAccepts(a, data):
				rep.Skipped++
				rep.Notes = append(rep.Notes, key+" changed since plan; kept")
				continue
			}
			pruneBeforeRemoveHook(key)
			sum := sha256.Sum256(data)
			if _, err := vaultfs.Delete(r.vaultRoot, key, hex.EncodeToString(sum[:])); err != nil {
				switch {
				case errors.Is(err, vaultfs.ErrShaConflict):
					rep.Skipped++
					rep.Notes = append(rep.Notes, key+" changed since plan; kept")
				case errors.Is(err, vaultfs.ErrFileNotFound):
					rep.Unchanged++
				default:
					rep.Errors = append(rep.Errors, fmt.Errorf("prune remove %s: %w", a.Target, err))
				}
				continue
			}
			rep.Pruned++
			if a.Detail("provenance") == templates.ProvenanceEarlier.String() {
				// No backup is kept, so the outcome is the audit record.
				rep.Notes = append(rep.Notes, "pruned "+key+" (earlier shipped version of "+embeddedRel+"; no backup)")
			}
		case ActionUnchanged:
			rep.Unchanged++
		case ActionSkip:
			rep.Skipped++
		}
	}
	return rep, nil
}

// PruneAccepts is the one accept rule for a Templates prune: content may be
// removed at a's path only when a names the built-in it prunes
// (embedded_relpath=) and content classifies as vp's there — the current
// embedded copy or an earlier shipped version, line endings aside. The Apply
// re-check and `vp config sync`'s worktree, HEAD and remote-tip checks all
// call it, so they cannot drift apart. A Delete without embedded_relpath —
// the retired-lock removal, say — is never a prune.
func PruneAccepts(a Action, content []byte) bool {
	rel := a.Detail("embedded_relpath")
	return rel != "" && templates.ClassifyVaultCopy(rel, content) != templates.ProvenanceOperator
}

func (r *TemplateTreeReconciler) applyScaffold(p Plan) (Report, error) {
	var rep Report
	for _, a := range p.Actions {
		switch a.Kind {
		case ActionCreate:
			base := filepath.Base(a.Target)
			if base == "README.md" {
				// Decide kind by parent directory name.
				kind := filepath.Base(filepath.Dir(a.Target))
				body := templates.RenderReadmeStub(kind)
				if body == "" {
					rep.Errors = append(rep.Errors, fmt.Errorf("unknown readme kind for %s", a.Target))
					continue
				}
				// Write-if-absent through the storage funnel (ADR-003)
				// rather than a raw os.OpenFile(O_EXCL). This used to be an
				// O_EXCL create with a comment explaining why raw O_EXCL was
				// taken over a stat/WriteFile pair: two processes (`vp
				// init` and the vp_init MCP tool both drive
				// internal/onboard) can scaffold the same Projects/<slug>
				// concurrently. O_EXCL closed the clobber race but still
				// left the target observable at length zero between the
				// open and the write — a crash or a concurrent reader in
				// that window saw (and could capture) an empty README that
				// was never detected or repaired. vaultfs.Create closes
				// that window structurally: it takes the path's advisory
				// lock, checks absence under the lock (so two writers still
				// cannot both succeed), and writes through atomicfile.Write
				// — a temp file renamed over the target only once the full
				// body is written and (WithFsync) synced, so the target
				// never exists at a partial length.
				//
				// ErrExists is the concurrent-loser path and counts as
				// Unchanged rather than an error — the other writer created
				// the same stub, which is the outcome this branch wanted
				// anyway. vaultfs.Create also stamps the MCP surface
				// version internally, so no separate stampVaultWrite call
				// is needed here (and mkdir is handled by atomicfile.Write).
				relPath := r.relSubpath + "/" + kind + "/README.md"
				if _, err := vaultfs.Create(r.vaultRoot, relPath, body); err != nil {
					if errors.Is(err, vaultfs.ErrExists) {
						rep.Unchanged++
						continue
					}
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
			// The only Update this reconciler ever plans: a zero-length
			// README (see planScaffold). Guard the assumption so a future
			// Update source doesn't silently fall through this repair path.
			if filepath.Base(a.Target) != "README.md" {
				rep.Errors = append(rep.Errors, fmt.Errorf("template_tree Apply: unexpected Update for %s — scaffold has no other Update writer", a.Target))
				continue
			}
			kind := filepath.Base(filepath.Dir(a.Target))
			body := templates.RenderReadmeStub(kind)
			if body == "" {
				rep.Errors = append(rep.Errors, fmt.Errorf("unknown readme kind for %s", a.Target))
				continue
			}
			// vaultfs.Write (not Create): the target already exists at size
			// 0, so this is an overwrite, CAS-guarded by expected_sha256
			// against the empty-file digest. An operator who wrote real
			// content into that path between Plan and Apply is refused
			// (ErrShaConflict) and kept, exactly like the prune's "changed
			// since plan; kept" pattern, rather than clobbered.
			relPath := r.relSubpath + "/" + kind + "/README.md"
			if _, err := vaultfs.Write(r.vaultRoot, relPath, body, a.Detail("expected_sha256")); err != nil {
				if errors.Is(err, vaultfs.ErrShaConflict) {
					rep.Skipped++
					rep.Notes = append(rep.Notes, kind+"/README.md changed since plan; kept")
					continue
				}
				rep.Errors = append(rep.Errors, fmt.Errorf("repair %s: %w", a.Target, err))
				continue
			}
			rep.Updated++
		}
	}
	return rep, nil
}

// atomicWriteFile was the reconciler's private atomic-write helper. It became
// templates.Executor.Write, which was deleted with the upgrade reset
// (upgrade-overwrite-resets-vault-template-overrides); the Templates reconcile
// writes no template bytes.
