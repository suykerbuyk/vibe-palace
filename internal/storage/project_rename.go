// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// vp vault rename: the in-vault half of the project lifecycle commands (U9). It
// renames a project from one slug to another INSIDE one vault: it moves the
// tracked footprint (Projects/<old>/ -> Projects/<new>/ and palace/<old>/ ->
// palace/<new>/), rewrites every stored identifier (the exact-line rewrite
// classes W1..W8, W10/W10b), writes a `renamed` departure record for the old
// slug, commits all of it in ONE lifecycle commit, and publishes exactly that
// commit to every remote — or refuses and rolls back.
//
// Design: task rename-core-fresh-target-with-digest-bind (U9), governed by
// lifecycle-commands-design-plan § Rename, the Implementation plan / Plan
// revision / Plan re-review of 2026-10-05, and the Chair ruling of 2026-10-05
// (K0 stays until U12; staged delivery). THIS IS INCREMENT 1: the tracked
// commit, its CLI and MCP surface. The host-local index step (AdoptRenamedStore
// + imports carry + rename-pending record) is increment 2; the `bind renamed`
// mode is increment 3.
//
// It reuses the kept, K0-free rewrite helpers in project_rename_rewrite.go: the
// rewrite classes (slugScanClasses), the drawer-id re-hash (slugRehashRoom via
// slugScanClasses), the move primitive (slugMove) and the count assertion
// (slugAssertClasses). It never invokes K0 / make-room: a rename targets a
// FRESH slug, and a collision is refused up front. Those helpers were reshaped
// from the one-shot project-slug engine, which U12 retired along with its
// K0/make-room machinery (see project_rename_rewrite.go).

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// renameCommand is the lifecycle command name the pending marker records.
const renameCommand = "vault rename"

// RenameCommandName is renameCommand, exported for composer marker-routing
// (see CopyCommandName).
const RenameCommandName = renameCommand

// renameDigestFormat tags the rename digest. A change to its inputs gets a new tag.
const renameDigestFormat = "vp-vault-rename-digest/1"

// Rename trailer keys (§ Rename). The commit carries them so history records
// the rename and a redo can adopt its own commit (Vp-Run).
const (
	TrailerRenameFrom = "Vp-Rename-From"
	TrailerRenameTo   = "Vp-Rename-To"
)

// ErrRenameRefused wraps every refusal rename makes before it writes: the
// caller's input or the vault's state, never a fault.
var ErrRenameRefused = errors.New("vault rename refused")

func renameRefuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrRenameRefused, fmt.Sprintf(format, a...))
}

// RenameRequest is one `vp vault rename` invocation.
type RenameRequest struct {
	// Vault is V, the served vault: an absolute path.
	Vault string
	// From and To are the old and new slugs.
	From, To string
	// Expect, when set, must equal the plan's digest.
	Expect string
	// DryRun stops after the plan.
	DryRun bool
}

// RenamePlan is what the dry run shows and the real run binds.
type RenamePlan struct {
	Vault         string         `json:"vault"`
	VaultIdentity string         `json:"vault_identity"`
	From          string         `json:"from"`
	To            string         `json:"to"`
	Moves         []SlugMove     `json:"moves"`
	Files         int            `json:"files"`
	Rewrites      map[string]int `json:"rewrites"`
	PushTargets   []string       `json:"push_targets"`
	Digest        string         `json:"digest"`
	// Refusals is every reason the rename cannot run; empty for a runnable plan.
	Refusals []string `json:"refusals,omitempty"`
	// Command is the exact real-run command line, set only for a runnable plan.
	Command string `json:"command"`
}

// RenameResult is what a real run did.
type RenameResult struct {
	Plan   *RenamePlan `json:"plan,omitempty"`
	Commit string      `json:"commit,omitempty"`
	Redo   RedoOutcome `json:"redo"`
	Undo   []string    `json:"undo,omitempty"`
}

// RenameRefusalsError is every refusal a rename plan found, listed together.
type RenameRefusalsError struct {
	Refusals []string
}

func (e *RenameRefusalsError) Error() string {
	if len(e.Refusals) == 1 {
		return ErrRenameRefused.Error() + ": " + e.Refusals[0]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d refusals:", ErrRenameRefused.Error(), len(e.Refusals))
	for _, r := range e.Refusals {
		b.WriteString("\n  - " + r)
	}
	return b.String()
}

func (e *RenameRefusalsError) Unwrap() error { return ErrRenameRefused }

// renameTestHook, when set, runs after the on-disk move (stage "moved"), after
// the rewrite (stage "rewritten"), before the commit (stage "commit"), between
// the commit and the marker's record of it (stage "committed"), and between the
// postcheck and the publish (stage "publish"). A test injects a failure or a
// simulated kill (errRenameSimulatedKill). Nil in production.
var renameTestHook func(stage string) error

// errRenameSimulatedKill makes ApplyRename return at once, as a killed process
// would: no rollback, marker left standing, lock released by the return.
var errRenameSimulatedKill = errors.New("simulated kill")

// PlanRename is the dry run: ApplyRename through the plan, writing nothing.
func PlanRename(req RenameRequest) (*RenamePlan, error) {
	req.DryRun = true
	res, err := ApplyRename(req)
	if res == nil {
		return nil, err
	}
	return res.Plan, err
}

// RenameCommandLine is the real-run command line for a plan.
func RenameCommandLine(p *RenamePlan) string {
	return strings.Join([]string{
		"vp", "vault", "rename", shellQuote(p.From), shellQuote(p.To),
		"--vault", shellQuote(p.Vault), "--expect", p.Digest,
	}, " ")
}

// renameRerunLine is the line a pending marker records. A redo is admitted only
// for an invocation that produces the same line.
func renameRerunLine(vault, from, to string) string {
	return strings.Join([]string{"vp", "vault", "rename", shellQuote(from), shellQuote(to), "--vault", shellQuote(vault)}, " ")
}

func validateRenameRequest(req *RenameRequest) (from, to string, err error) {
	if req.Vault == "" || !filepath.IsAbs(req.Vault) {
		return "", "", renameRefuse("the served vault must be an absolute path, got %q", req.Vault)
	}
	req.Vault = filepath.Clean(req.Vault)
	if err := slug.Validate(req.From); err != nil {
		return "", "", renameRefuse("--from %q: %v", req.From, err)
	}
	if err := slug.Validate(req.To); err != nil {
		return "", "", renameRefuse("--to %q: %v", req.To, err)
	}
	if req.From == req.To {
		return "", "", renameRefuse("the old and new slugs are both %q", req.From)
	}
	return req.From, req.To, nil
}

// ApplyRename runs § Rename. With req.DryRun it stops after the plan. The plan
// is always computed first, by the same code, so the real run cannot do what
// the dry run did not show; with req.Expect it refuses unless the digests match.
func ApplyRename(req RenameRequest) (*RenameResult, error) {
	from, to, err := validateRenameRequest(&req)
	if err != nil {
		return nil, err
	}
	if err := RefuseIfGitDisabled(req.Vault, "rename"); err != nil {
		return nil, err
	}

	var refusals []string

	// Data-format preflight: a rename reads and rewrites this vault's own
	// artifacts, so it runs only at the binary's data format.
	vFormat, err := surface.ReadFormat(req.Vault)
	if err != nil {
		return nil, fmt.Errorf("read %s's data format: %w", req.Vault, err)
	}
	if vFormat != surface.RequiredDataFormat {
		refusals = append(refusals, fmt.Sprintf("%s is at data format %d; this binary requires %d", req.Vault, vFormat, surface.RequiredDataFormat))
	}

	// The source must be present (tracked).
	src, err := slugTracked(req.Vault, "Projects/"+from, "palace/"+from)
	if err != nil {
		return nil, err
	}
	if len(src) == 0 {
		refusals = append(refusals, fmt.Sprintf("nothing is tracked under Projects/%s or palace/%s: there is no project %q to rename", from, from, from))
	}

	// Fresh-target refusals (SF2): the new slug must not already exist.
	refusals = append(refusals, renameFreshTargetRefusals(req.Vault, to)...)

	// Build the move plan and the rewrite counts (structural; a collision or a
	// dangling rewrite target is a refusal, not a fault).
	var p *SlugPlan
	var classes map[string]int
	if len(src) > 0 {
		pp, cl, perr := buildRenameMovePlan(req.Vault, from, to, src)
		if perr != nil {
			refusals = append(refusals, perr.Error())
		} else {
			p, classes = pp, cl
		}
	}

	// Lock V, then the live checks.
	held, err := vaultlock.AcquireHeld(req.Vault, req.Vault)
	if err != nil {
		return nil, fmt.Errorf("acquire vault commit lock: %w", err)
	}
	defer func() { _ = held.Release() }()

	branch := branchOrMain(req.Vault)
	remotes, err := lifecycleRemoteOrder(req.Vault)
	if err != nil {
		return nil, err
	}
	rerun := renameRerunLine(req.Vault, from, to)
	rollback := func() error { return renameRollback(req.Vault, from, to, p) }

	res := &RenameResult{Redo: RedoNone}
	if m, found, merr := readLifecycleMarker(req.Vault); found {
		if merr != nil || req.DryRun || m.Command != renameCommand || m.Rerun != rerun {
			return nil, &LifecyclePendingError{Vault: req.Vault, Marker: m, Malformed: errString(merr)}
		}
		outcome, rm, rerr := redoLifecycle(held, renameCommand, branch, remotes, rollback)
		res.Redo = outcome
		if rerr != nil {
			return res, rerr
		}
		if outcome == RedoPublished {
			res.Commit = rm.Commit
			res.Undo = renameUndoLines(req.Vault, rm.Commit, remotes, branch)
			return res, nil
		}
		// RedoRolledBack: the unfinished run's writes are gone; run afresh.
	} else if merr != nil {
		return nil, merr
	}

	if err := refuseOnPendingDepartures(req.Vault); err != nil {
		refusals = append(refusals, err.Error())
	}
	if dirty, err := footprintDirty(req.Vault, []string{from}); err != nil {
		return nil, err
	} else if dirty != "" {
		refusals = append(refusals, fmt.Sprintf("the footprint paths of %s are not clean:\n%s", from, dirty))
	}
	// A rename with remotes publishes exactly its commit and so refuses unless V
	// is at every remote's tip (the e8 no-`--no-push` guarantee). A rename with
	// NO remote commits locally and publishes nothing: there is no remote to
	// rebase over, so no e8 hazard, and refusing it would deny a purely local vp
	// user a supported operation (operator ruling 2026-10-05; the one-shot
	// allowed it too). exactPublish over an empty remote set commits locally and
	// clears the marker without pushing.
	var parent string
	if len(remotes) == 0 {
		parent, err = gitCmd(req.Vault, 10*time.Second, "rev-parse", "HEAD")
		if err != nil {
			return nil, fmt.Errorf("read HEAD: %w", err)
		}
	} else {
		parent, err = requireHeadAtEveryRemote(held, branch, remotes)
		if err != nil {
			refusals = append(refusals, err.Error())
			if !errors.Is(err, ErrRemoteNotAtHead) && !errors.Is(err, ErrRemoteUnreachable) {
				refusals[len(refusals)-1] = "cannot check V against its remotes: " + err.Error()
			}
		}
	}

	// Plan + digest.
	plan := &RenamePlan{
		Vault: req.Vault, VaultIdentity: vaultIdentity(req.Vault), From: from, To: to, PushTargets: remotes,
	}
	if p != nil {
		plan.Moves = p.k1Moves
		plan.Files = len(p.k1Moves)
		plan.Rewrites = classes
	}
	plan.Digest = renameDigest(plan)
	res.Plan = plan
	if req.Expect != "" && !strings.EqualFold(req.Expect, plan.Digest) {
		refusals = append(refusals, fmt.Sprintf("digest mismatch: --expect %s, but the plan now digests to %s — what the rename would do changed since the dry run; re-run the dry run", req.Expect, plan.Digest))
	}
	plan.Refusals = refusals
	if len(refusals) > 0 {
		return res, &RenameRefusalsError{Refusals: refusals}
	}
	plan.Command = RenameCommandLine(plan)
	if req.DryRun {
		return res, nil
	}

	// Real run.
	runID, err := newRunID()
	if err != nil {
		return res, err
	}
	m := lifecycleMarker{Command: renameCommand, RunID: runID, Parent: parent, Rerun: rerun}
	if err := writeLifecycleMarker(held, m); err != nil {
		return res, err
	}
	v := NewVault(req.Vault)
	fail := func(cause error) (*RenameResult, error) {
		if errors.Is(cause, errRenameSimulatedKill) {
			return res, cause
		}
		// An aborted run removes its rename-pending record: the host-local step
		// never runs, so nothing must keep index/<old>/ pinned afterwards.
		_ = v.RemoveRenamePending(from)
		if rerr := rollBackRun(held, m, rollback); rerr != nil {
			return res, fmt.Errorf("%w; rolling back also failed: %v", cause, rerr)
		}
		return res, cause
	}

	// Pin index/<old>/ against 1b's sweep for the window between this commit and
	// the host-local step, by writing the rename-pending record BEFORE the
	// commit. The host-local step (indexstore.AdoptRenamedProject, run from the
	// tool/CLI after this returns) removes it; fail() removes it on an abort.
	if err := v.WriteRenamePending(from, to); err != nil {
		return fail(fmt.Errorf("write the rename-pending record: %w", err))
	}

	// Move the footprint on disk, then rewrite the identifiers in place.
	if err := renameApplyMoves(req.Vault, p); err != nil {
		return fail(fmt.Errorf("move the footprint: %w", err))
	}
	if renameTestHook != nil {
		if err := renameTestHook("moved"); err != nil {
			return fail(err)
		}
	}
	applied, _, err := slugScanClasses(req.Vault, from, to, p, true)
	if err != nil {
		return fail(fmt.Errorf("rewrite identifiers: %w", err))
	}
	if err := slugAssertClasses(applied, classes); err != nil {
		return fail(fmt.Errorf("rewrite: %w", err))
	}
	if renameTestHook != nil {
		if err := renameTestHook("rewritten"); err != nil {
			return fail(err)
		}
	}

	// The renamed departure record, written before the commit and committed with it.
	recRel, _, _, err := v.RecordDepartureForRename(held, from, to)
	if err != nil {
		return fail(fmt.Errorf("record the rename: %w", err))
	}

	if renameTestHook != nil {
		if err := renameTestHook("commit"); err != nil {
			return fail(err)
		}
	}
	subject, trailers := renameCommitMessage(from, to, runID)
	cres, err := commitRenameLocked(held, RenameCommit{From: from, To: to, Record: recRel, ExpectHead: parent, Message: subject, Trailers: trailers})
	if err != nil {
		return fail(fmt.Errorf("commit: %w", err))
	}
	sha := cres.CommitSHA
	if renameTestHook != nil {
		if err := renameTestHook("committed"); err != nil {
			return fail(err)
		}
	}
	if err := setLifecycleMarkerCommit(held, runID, sha); err != nil {
		return fail(err)
	}
	m.Commit = sha

	// Postcheck, before anything is published.
	if err := renamePostcheck(req.Vault, sha, from, to, p); err != nil {
		return fail(err)
	}
	if renameTestHook != nil {
		if err := renameTestHook("publish"); err != nil {
			return fail(err)
		}
	}

	if err := exactPublish(held, branch, remotes, m, rollback, false); err != nil {
		// On a first-remote failure exactPublish has already rolled the commit
		// back (PublishRemoteMoved: reset to parent, <old> restored, marker
		// cleared). That abort must also drop the rename-pending record, or it
		// would pin index/<old>/ for a run that did not land (SF1). A partial
		// publish (the commit is on some remote) keeps the record deliberately:
		// a re-run finishes the publish and the host-local step.
		var pe *PublishError
		if errors.As(err, &pe) && pe.Kind == PublishRemoteMoved {
			_ = v.RemoveRenamePending(from)
		}
		return res, err
	}
	res.Commit = sha
	res.Undo = renameUndoLines(req.Vault, sha, remotes, branch)
	return res, nil
}

// renameFreshTargetRefusals is the SF2 fresh-target guard: the new slug must
// not already have either tree on disk, nor a departure record.
func renameFreshTargetRefusals(vaultPath, to string) []string {
	var out []string
	for _, tree := range ProjectTrees(to) {
		if slugExists(vaultPath, tree) {
			out = append(out, fmt.Sprintf("%s already holds %s: rename never merges into an existing project (destination %s already exists)", vaultPath, tree, tree))
		}
	}
	if _, found := departure.Read(vaultPath, to); found {
		out = append(out, fmt.Sprintf("%s holds a departure record for the new slug %s (%s): rename into a departed slug is refused", vaultPath, to, departure.RelPath(to)))
	}
	return out
}

// buildRenameMovePlan derives the fresh-target move set (no K0): every tracked
// file under the source trees mapped to its destination, every untracked
// (ignored .bak) file likewise, and the rewrite class counts. A destination
// that already exists is refused per path (SF2 :315-316 semantics).
func buildRenameMovePlan(root, from, to string, src []string) (*SlugPlan, map[string]int, error) {
	p := &SlugPlan{Root: root, From: from, To: to}
	tracked := map[string]bool{}
	for _, r := range src {
		tracked[r] = true
		dst := slugMapPath(r, from, to)
		if dst == "" {
			return nil, nil, fmt.Errorf("internal: %s is not under a source tree", r)
		}
		if slugExists(root, dst) {
			return nil, nil, fmt.Errorf("destination %s already exists", dst)
		}
		p.k1Moves = append(p.k1Moves, SlugMove{Src: r, Dst: dst})
	}
	// Untracked files under the source trees (the ignored .bak backups), carried
	// so the renamed project keeps them; .local is host-local and skipped.
	for _, dir := range []string{"Projects/" + from, "palace/" + from} {
		base := filepath.Join(root, filepath.FromSlash(dir))
		err := filepath.WalkDir(base, func(pth string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if d.Name() == ".local" {
					return fs.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, pth)
			rel = filepath.ToSlash(rel)
			if tracked[rel] {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("%s is not a regular file", rel)
			}
			dst := slugMapPath(rel, from, to)
			if slugExists(root, dst) {
				return fmt.Errorf("destination %s already exists", dst)
			}
			p.bakMoves = append(p.bakMoves, SlugMove{Src: rel, Dst: dst})
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	sort.Slice(p.bakMoves, func(i, j int) bool { return p.bakMoves[i].Src < p.bakMoves[j].Src })
	classes, _, err := slugScanClasses(root, from, to, p, false)
	if err != nil {
		return nil, nil, err
	}
	return p, classes, nil
}

// renameApplyMoves moves the footprint on disk: the ignored .bak files, then
// the tracked files, then removes the now-empty old trees.
func renameApplyMoves(root string, p *SlugPlan) error {
	for _, mv := range p.bakMoves {
		if err := slugMove(root, mv.Src, mv.Dst); err != nil {
			return err
		}
	}
	for _, mv := range p.k1Moves {
		if err := slugMove(root, mv.Src, mv.Dst); err != nil {
			return err
		}
	}
	for _, d := range ProjectTrees(p.From) {
		if err := slugRemoveEmptyTree(root, d); err != nil {
			return err
		}
	}
	return nil
}

// renameRollback undoes an unfinished rename, idempotently, so it is correct
// whether or not the commit landed. After a commit, rollBackRun has already run
// `git reset --keep` to the parent (restoring the tracked trees and removing
// the record and the rewrite); after that, or with no commit at all, this
// restores the old footprint from HEAD, vacates the new trees and brings the
// ignored backups back.
func renameRollback(root, from, to string, p *SlugPlan) error {
	if p == nil {
		return nil
	}
	// 1. Bring the ignored backups back to the old tree first (before the new
	// trees are removed under them), through the gated move primitive.
	for _, mv := range p.bakMoves {
		if !slugExists(root, mv.Dst) || slugExists(root, mv.Src) {
			continue
		}
		if err := slugMove(root, mv.Dst, mv.Src); err != nil {
			return fmt.Errorf("roll back %s: %w", mv.Dst, err)
		}
	}
	// 2. Vacate the new trees (untracked, moved+rewritten files; or already gone
	// after a reset).
	for _, tree := range ProjectTrees(to) {
		if !slugExists(root, tree) {
			continue
		}
		set, err := CollectPurgeTree(root, tree)
		if err != nil {
			return fmt.Errorf("roll back %s: %w", tree, err)
		}
		if _, _, err := RemovePurgeTree(root, set, nil); err != nil {
			return fmt.Errorf("roll back %s: %w", tree, err)
		}
	}
	// 3. Restore the old trees and the baseline from HEAD (no-op after a reset).
	for _, pathspec := range append(ProjectTrees(from), "Audits/baseline.json") {
		if out, err := gitCmd(root, 10*time.Second, "ls-tree", "HEAD", "--", pathspec); err != nil || out == "" {
			continue
		}
		if _, err := gitCmd(root, 60*time.Second, "checkout", "HEAD", "--", pathspec); err != nil {
			return fmt.Errorf("roll back: restore %s: %w", pathspec, err)
		}
	}
	// 4. Remove the renamed record if it is still on disk, through the gated
	// removal primitive.
	rec := filepath.Join(root, filepath.FromSlash(departure.RelPath(from)))
	if _, err := os.Lstat(rec); err == nil {
		if err := vaultfs.RemoveNoLock(rec); err != nil {
			return fmt.Errorf("roll back: remove %s: %w", departure.RelPath(from), err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("roll back: inspect %s: %w", departure.RelPath(from), err)
	}
	// 5. The footprint of both slugs is clean.
	if dirty, err := footprintDirty(root, []string{from, to}); err != nil {
		return err
	} else if dirty != "" {
		return fmt.Errorf("the footprint paths are still not clean after the rollback:\n%s", dirty)
	}
	return nil
}

// renameCommitMessage is the one commit's subject and trailers.
func renameCommitMessage(from, to, runID string) (subject, trailers string) {
	subject = fmt.Sprintf("vault rename: %s -> %s", from, to)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n%s: %s\n", TrailerRenameFrom, from, TrailerRenameTo, to)
	b.WriteString(lifecycleRunTrailerLine(runID))
	return subject, b.String()
}

// renameDigest binds what the rename would do (§ Command surface, the digest
// table, rename row): a format tag, the command, the slugs, V's identity, the
// move set (path -> path) and the rewrite class counts. Never a HEAD: an
// unrelated push does not change it. Content drift is caught by the clean
// preflight and requireHeadAtEveryRemote, not by the digest.
func renameDigest(p *RenamePlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\ncommand %s\nfrom %s\nto %s\nvault %s\n", renameDigestFormat, renameCommand, p.From, p.To, p.VaultIdentity)
	for _, mv := range p.Moves {
		fmt.Fprintf(&b, "move %s -> %s\n", mv.Src, mv.Dst)
	}
	keys := make([]string, 0, len(p.Rewrites))
	for k := range p.Rewrites {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "class %s %d\n", k, p.Rewrites[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// renamePostcheck is the after-commit check, before any publish: the commit
// touches only the rename's footprint, the footprint is clean, and no stored
// identifier still names the old slug (the zero-<old> scan; absorbs
// rename-verify-action).
//
// NOTE (plan vs. real code): the plan named the one-shot engine's R100-only
// post-commit check for the diff-scope check, but that check assumed K1 (pure
// rename) and K2 (rewrite) landed as SEPARATE commits. This engine makes ONE
// commit, so a moved-and-rewritten file is a rename-with-modification, which
// that check rejected. The diff-scope check here is instead the copy engine's
// shape (`git diff --name-only --no-renames`, every path inside the footprint);
// the rewrite is still asserted exactly by slugAssertClasses during apply.
func renamePostcheck(vaultPath, sha, from, to string, p *SlugPlan) error {
	out, err := gitCmd(vaultPath, 30*time.Second, "diff", "--name-only", "--no-renames", sha+"~1", sha)
	if err != nil {
		return fmt.Errorf("postcheck: diff the rename commit: %w", err)
	}
	allowedExact := map[string]bool{
		"Audits/baseline.json":  true,
		departure.RelPath(from): true,
		"Audits/.surface":       true,
	}
	prefixes := []string{"Projects/" + from + "/", "palace/" + from + "/", "Projects/" + to + "/", "palace/" + to + "/"}
	for rel := range strings.SplitSeq(out, "\n") {
		if rel == "" {
			continue
		}
		if allowedExact[rel] || filepath.Base(rel) == ".surface" {
			continue
		}
		inside := false
		for _, pre := range prefixes {
			if strings.HasPrefix(rel, pre) {
				inside = true
				break
			}
		}
		if !inside {
			return renameRefuse("postcheck: the rename commit touches %s, outside the rename's footprint", rel)
		}
	}
	if dirty, err := footprintDirty(vaultPath, []string{from, to}); err != nil {
		return err
	} else if dirty != "" {
		return renameRefuse("postcheck: the footprint paths are not clean after the commit:\n%s", dirty)
	}
	return renameZeroOld(vaultPath, from, to, p)
}

// renameZeroOld scans the moved identifier positions and refuses if any still
// names the old slug. It checks the exact lines the rewrite classes own —
// frontmatter project/note_path/archive/tracked_by, manifest
// project_slug/vault_rel_session_note, resume and workflow H1, and baseline
// accepted[] — not free-text prose or drawer content, which may legitimately
// mention the old slug.
func renameZeroOld(root, from, to string, p *SlugPlan) error {
	var bad []string
	note := func(rel, line string) { bad = append(bad, rel+": "+strings.TrimSpace(line)) }
	for _, mv := range p.k1Moves {
		rel := mv.Dst
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		under := func(kind string) bool { return strings.HasPrefix(rel, "Projects/"+to+"/"+kind+"/") }
		isSession := under("sessions") && strings.HasSuffix(rel, ".md")
		isNote := under("notes") && strings.HasSuffix(rel, ".md")
		isManifest := strings.HasPrefix(rel, "Projects/"+to+"/transcripts/") && strings.HasSuffix(rel, ".manifest.json")
		isResume := rel == "Projects/"+to+"/resume.md"
		isWorkflow := rel == "Projects/"+to+"/workflow.md"
		lines := strings.Split(string(data), "\n")
		switch {
		case isSession || isNote || isResume:
			// The checks mirror EXACTLY the (field × file-kind) matrix
			// slugScanClasses rewrites, so the scan can never flag a line the
			// rewriter deliberately leaves alone: `project:` for all three
			// (W1/W1b/W6); `note_path:`/`archive:` only on a session (W2/W3);
			// `tracked_by:` only on a note (W2b).
			end := slugFrontmatter(lines)
			for i := 1; i < end; i++ {
				l := lines[i]
				switch {
				case l == "project: "+from:
					note(rel, l)
				case isSession && strings.HasPrefix(l, "note_path: Projects/"+from+"/"):
					note(rel, l)
				case isSession && strings.HasPrefix(l, "archive: Projects/"+from+"/"):
					note(rel, l)
				case isNote && strings.HasPrefix(l, "tracked_by: Projects/"+from+"/"):
					note(rel, l)
				}
			}
			if isResume {
				for _, l := range lines {
					if l == "# "+from+" — Working Context" {
						note(rel, l)
					}
				}
			}
		case isWorkflow:
			for _, l := range lines {
				if l == "# "+from+" — Workflow" {
					note(rel, l)
				}
			}
		case isManifest:
			for _, l := range lines {
				if l == `  "project_slug": "`+from+`",` || strings.HasPrefix(l, `  "vault_rel_session_note": "Projects/`+from+`/`) {
					note(rel, l)
				}
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "Audits", "baseline.json")); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, `"`+from+`/`) || strings.HasPrefix(t, `"Projects/`+from+`/`) || strings.HasPrefix(t, `"palace/`+from+`/`) {
				note("Audits/baseline.json", l)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return renameRefuse("postcheck: %d identifier(s) still name the old slug %q after the rewrite:\n  %s", len(bad), from, strings.Join(bad, "\n  "))
	}
	return nil
}

// renameUndoLines are the plain-git reversal of a published rename.
func renameUndoLines(vaultPath, sha string, remotes []string, branch string) []string {
	lines := []string{fmt.Sprintf("git -C %s revert --no-edit %s", shellQuote(vaultPath), sha)}
	for _, r := range remotes {
		lines = append(lines, fmt.Sprintf("git -C %s push %s HEAD:refs/heads/%s", shellQuote(vaultPath), shellQuote(r), branch))
	}
	return lines
}
