// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// `vp vault project delete`: remove projects from the vault this process
// serves, in one published commit that carries their departure records. Run
// on the vault the projects LEAVE. With --moved-to it first proves, from the
// destination's published remote, that the destination holds exactly the
// bytes being deleted; --discard deletes without a destination.
//
// Design: task lifecycle-commands-design-plan, § Delete, § Command surface
// (dry run and digest, locks, exact publish), Q4, and the Chair rulings.
// PlanDelete and ApplyDelete are the one core; the CLI and vp_vault_project_delete
// are thin over them, and every refusal lives here.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// deleteCommand names the command in its pending marker.
const deleteCommand = "vault project delete"

// deleteDigestFormat tags the dry-run digest; a change to its preimage changes
// the tag.
const deleteDigestFormat = "vp-vault-project-delete/1"

// The trailers a copy commit carries (§ Copy step 7) and a delete commit
// writes (§ Delete step 7). The commit also carries U3's Vp-Run trailer
// (lifecycleRunTrailerLine), so a redo adopts only its own commit.
const (
	trailerCopyProject   = "Vp-Copy-Project"
	trailerCopySource    = "Vp-Copy-Source"
	trailerCopyFootprint = "Vp-Copy-Footprint"
	trailerDeleteProject = "Vp-Delete-Project"
	trailerDeleteKind    = "Vp-Delete-Kind"
	trailerDeleteTo      = "Vp-Delete-To"
	trailerDeleteFP      = "Vp-Delete-Footprint"
)

// DeleteRequest is one `vp vault project delete` invocation.
type DeleteRequest struct {
	Projects []string
	// MovedTo is the destination vault's published remote URL. Exactly one of
	// MovedTo and Discard is set.
	MovedTo string
	Discard bool
	// Expect is the digest a dry run printed; apply refuses on any mismatch.
	Expect string
}

// DeleteMode is what a run does.
type DeleteMode string

const (
	// DeleteFull removes the footprint in one published commit, then the
	// ignored leftovers.
	DeleteFull DeleteMode = "delete"
	// DeleteLeftovers finishes a run that died after it published: the
	// projects are departed on every remote, and only the ignored leftovers
	// are removed.
	DeleteLeftovers DeleteMode = "leftovers"
)

// DeleteFile is one tracked file the delete removes.
type DeleteFile struct {
	Path string `json:"path"`
	OID  string `json:"oid"`
}

// DeleteLeftover is one file git does not track, removed only after the
// delete is published everywhere. None of them is restorable by git revert.
type DeleteLeftover struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Class  string `json:"class"` // "ignored" or "machine-local"
}

// DeleteProjectPlan is one project's part of the plan.
type DeleteProjectPlan struct {
	Project string `json:"project"`
	// Footprint is F(V HEAD, p) for a full delete, or the F the departure
	// record carries for a leftovers-only run.
	Footprint  string       `json:"footprint"`
	Tracked    []DeleteFile `json:"tracked,omitempty"`
	CopyCommit string       `json:"copy_commit,omitempty"`
	// DestinationGeneration is the destination's own record generation for the
	// project (0: none); Generation is the one this delete writes.
	DestinationGeneration int `json:"destination_generation,omitempty"`
	Generation            int `json:"generation,omitempty"`
}

// DeletePlan is what a dry run prints and what apply executes.
type DeletePlan struct {
	Vault    string              `json:"vault"`
	Mode     DeleteMode          `json:"mode"`
	Kind     departure.Kind      `json:"kind"`
	To       string              `json:"to,omitempty"`
	Projects []DeleteProjectPlan `json:"projects"`
	// Leftovers are "not restorable by git revert".
	Leftovers []DeleteLeftover `json:"leftovers"`
	Branch    string           `json:"branch"`
	Remotes   []string         `json:"push_targets"`
	Head      string           `json:"head"`
	Warnings  []string         `json:"warnings,omitempty"`
	Digest    string           `json:"digest"`
	// Command is the exact real-run command line, --expect filled in.
	Command string `json:"command"`

	sets []PurgeSet // the collected trees, leftovers only
}

// DeleteResult is what apply did.
type DeleteResult struct {
	Plan *DeletePlan `json:"plan"`
	// Commit is the published delete commit; empty for a leftovers-only run.
	Commit string `json:"commit,omitempty"`
	// Redo is what a pending marker made this run do first.
	Redo         RedoOutcome         `json:"redo,omitempty"`
	FilesRemoved int                 `json:"leftover_files_removed"`
	DirsRemoved  int                 `json:"dirs_removed"`
	Kept         []PurgeCleanupEntry `json:"kept,omitempty"`
	Undo         []string            `json:"undo,omitempty"`
}

// errNothingLeft is the leftovers-only plan's refusal when the projects are
// departed and nothing is left under their trees. After a redo that published
// the delete, it is success: there is simply nothing more to remove.
var errNothingLeft = errors.New("nothing is left under their trees")

// Test seams, no-ops in production. deleteAfterPlan runs between the plan and
// the first write; deleteAfterRecords between the records and the commit;
// deleteBeforePublish between the postcheck and the push, and
// deleteBeforeLeftovers between the confirmed publish and the leftovers. A
// seam that returns an error stops the run on the spot, as a killed process
// would: nothing is rolled back and the marker stays.
var (
	deleteAfterPlan       = func() {}
	deleteAfterRecords    = func() error { return nil }
	deleteBeforePublish   = func() error { return nil }
	deleteBeforeLeftovers = func() error { return nil }
)

func (r DeleteRequest) validate() error {
	if len(r.Projects) == 0 {
		return fmt.Errorf("name at least one project")
	}
	seen := map[string]bool{}
	for _, p := range r.Projects {
		if err := slug.Validate(p); err != nil {
			return fmt.Errorf("project %q: %w", p, err)
		}
		if seen[p] {
			return fmt.Errorf("project %q is named twice", p)
		}
		seen[p] = true
	}
	switch {
	case r.MovedTo != "" && r.Discard:
		return fmt.Errorf("pass --moved-to or --discard, not both")
	case r.MovedTo == "" && !r.Discard:
		return fmt.Errorf("pass --moved-to <url> (the destination's published remote) or --discard: " +
			"a delete with neither would destroy the only copy")
	case r.MovedTo != "":
		if err := departure.ValidateLabel(r.MovedTo); err != nil {
			return err
		}
		if _, ok := normaliseRemoteURL(r.MovedTo); !ok {
			return fmt.Errorf("--moved-to %q is not a git remote URL", r.MovedTo)
		}
	}
	return nil
}

func (r DeleteRequest) kind() departure.Kind {
	if r.Discard {
		return departure.Deleted
	}
	return departure.MovedToVault
}

// commandLine renders the command, for the dry run and the marker's re-run.
func (r DeleteRequest) commandLine(vaultRoot, expect string) string {
	parts := append([]string{"vp", "vault", "project", "delete"}, r.Projects...)
	if r.Discard {
		parts = append(parts, "--discard")
	} else {
		parts = append(parts, "--moved-to", r.MovedTo)
	}
	parts = append(parts, "--vault", vaultRoot)
	if expect != "" {
		parts = append(parts, "--expect", expect)
	}
	return strings.Join(parts, " ")
}

// destination is the fetched destination vault: a private blobless snapshot
// of its published branch.
type destination struct {
	URL  string
	snap *remoteSnapshot
}

// fetchDestination is § Delete step 1, done BEFORE the lock is taken.
func fetchDestination(url string) (*destination, error) {
	h, err := lsRemoteHead(url)
	if err != nil {
		return nil, fmt.Errorf("refusing: cannot read the destination %s: %w", url, err)
	}
	snap, err := newRemoteSnapshot(url, h.Branch)
	if err != nil {
		return nil, fmt.Errorf("refusing: cannot fetch the destination %s: %w", url, err)
	}
	return &destination{URL: url, snap: snap}, nil
}

func (d *destination) close() {
	if d != nil {
		_ = d.snap.Close()
	}
}

// PlanDelete is the dry run: every check and every refusal apply would make,
// and the digest, with nothing written.
func PlanDelete(vaultRoot string, req DeleteRequest) (*DeletePlan, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	var dest *destination
	if req.MovedTo != "" {
		d, err := fetchDestination(req.MovedTo)
		if err != nil {
			return nil, err
		}
		defer d.close()
		dest = d
	}
	held, err := vaultlock.AcquireHeld(vaultRoot, vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("acquire vault commit lock: %w", err)
	}
	defer held.Release()
	if err := refuseOnLifecyclePendingStrict(vaultRoot); err != nil {
		return nil, fmt.Errorf("%w (the dry run cannot plan over an unfinished run; re-run the real command without --dry-run to finish it)", err)
	}
	return planDeleteLocked(held, req, dest)
}

// ApplyDelete runs the delete: plan, record, one commit, exact publish, then
// the leftovers. A re-run over a pending marker is the redo of Exact publish
// rule 6; a re-run after a published delete removes only the leftovers.
func ApplyDelete(vaultRoot string, req DeleteRequest) (*DeleteResult, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	var dest *destination
	if req.MovedTo != "" {
		d, err := fetchDestination(req.MovedTo)
		if err != nil {
			return nil, err
		}
		defer d.close()
		dest = d
	}
	held, err := vaultlock.AcquireHeld(vaultRoot, vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("acquire vault commit lock: %w", err)
	}
	defer held.Release()
	root := held.Root()
	res := &DeleteResult{}

	// A marker that cannot be read comes back found (with an error) and is not
	// this command's, so the strict check below refuses on it.
	if m, found, _ := readLifecycleMarker(root); found {
		if m.Command != deleteCommand {
			return nil, refuseOnLifecyclePendingStrict(root)
		}
		remotes, err := lifecycleRemoteOrder(root)
		if err != nil {
			return nil, err
		}
		out, rm, err := redoLifecycle(held, deleteCommand, branchOrMain(root), remotes, deleteRollback(root, req.Projects))
		if err != nil {
			return nil, err
		}
		res.Redo = out
		if out == RedoPublished {
			res.Commit = rm.Commit
			res.Undo = deleteUndoLines(root, rm.Commit, remotes)
			// The earlier run's commit is now published everywhere, so the
			// projects are departed: what is left is step 10, which the plan
			// below finds as a leftovers-only run. The earlier run's digest
			// bound its own leftovers, not these, so --expect is not
			// re-checked.
			req.Expect = ""
		}
	}

	plan, err := planDeleteLocked(held, req, dest)
	if err != nil {
		if res.Redo == RedoPublished && errors.Is(err, errNothingLeft) {
			return res, nil // published, and no leftover to remove
		}
		return nil, err
	}
	res.Plan = plan
	if req.Expect != "" && req.Expect != plan.Digest {
		return nil, fmt.Errorf("refusing: the plan's digest is %s, not the --expect %s: something changed since the dry run; run the dry run again", plan.Digest, req.Expect)
	}
	deleteAfterPlan()

	if plan.Mode == DeleteFull {
		commit, err := applyFullDelete(held, req, plan)
		if err != nil {
			return nil, err
		}
		res.Commit = commit
		res.Undo = deleteUndoLines(root, commit, plan.Remotes)
		if err := deleteBeforeLeftovers(); err != nil {
			return nil, err
		}
	}
	res.FilesRemoved, res.DirsRemoved, res.Kept = removeDeleteLeftovers(root, plan)
	return res, nil
}

// planDeleteLocked is the plan, under V's root lock. dest is nil for --discard.
func planDeleteLocked(held *vaultlock.Held, req DeleteRequest, dest *destination) (*DeletePlan, error) {
	if err := held.RequireRoot(); err != nil {
		return nil, err
	}
	root := held.Root()
	if err := RefuseIfGitDisabled(root, "delete a project"); err != nil {
		return nil, err
	}
	if state, _ := InspectVaultGit(root); state != VaultGitOK {
		return nil, fmt.Errorf("refusing: %s is not the top level of its own git repository, so a delete could not be committed and published", root)
	}
	remotes, err := lifecycleRemoteOrder(root)
	if err != nil {
		return nil, err
	}
	if len(remotes) == 0 {
		return nil, fmt.Errorf("refusing: %s has no remote; a delete publishes its commit, so it needs one", root)
	}
	if dest != nil {
		if err := refuseSelfDestination(root, dest.URL); err != nil {
			return nil, err
		}
	}
	head, err := gitCmd(root, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("read HEAD: %w", err)
	}
	plan := &DeletePlan{Vault: root, Kind: req.kind(), To: req.MovedTo, Branch: branchOrMain(root), Remotes: remotes, Head: head}

	// The mode: every project present (tracked content at HEAD, not departed)
	// is a full delete; every project departed with leftovers on disk is a
	// leftovers-only run; anything else refuses and changes nothing.
	var present, departed []string
	for _, p := range req.Projects {
		_, n, err := footprintHash(root, head, p)
		if err != nil {
			return nil, err
		}
		_, isDeparted := departure.Find(root, p)
		switch {
		case !isDeparted && n > 0:
			present = append(present, p)
		case isDeparted:
			departed = append(departed, p)
		default:
			return nil, fmt.Errorf("refusing: project %q is not present in %s (it has no tracked content file at HEAD); nothing was changed", p, root)
		}
	}
	switch {
	case len(present) == len(req.Projects):
		plan.Mode = DeleteFull
		err = planFullDelete(held, req, dest, plan)
	case len(departed) == len(req.Projects):
		plan.Mode = DeleteLeftovers
		err = planLeftoversOnly(held, req, plan)
	default:
		return nil, fmt.Errorf("refusing: %s are present and %s already departed; delete them in separate runs",
			strings.Join(present, ", "), strings.Join(departed, ", "))
	}
	if err != nil {
		return nil, err
	}
	plan.Digest = deleteDigest(root, req, plan)
	plan.Command = req.commandLine(root, plan.Digest)
	return plan, nil
}

// refuseSelfDestination refuses a --moved-to that is one of V's own remotes.
func refuseSelfDestination(root, url string) error {
	want, _ := normaliseRemoteURL(url)
	urls, err := VaultRemoteURLs(root)
	if err != nil {
		return err
	}
	for _, u := range urls {
		if n, ok := normaliseRemoteURL(u); ok && n == want {
			return fmt.Errorf("refusing: --moved-to %s is a remote of this vault itself, not another vault", url)
		}
	}
	return nil
}

// collectDeleteTrees collects every file under p's two trees and its embed
// cache, before anything is changed.
func collectDeleteTrees(root string, projects []string) ([]PurgeSet, error) {
	var sets []PurgeSet
	for _, p := range projects {
		for _, tree := range append([]string{"palace/.local/embed-cache/" + p}, ProjectTrees(p)...) {
			set, err := CollectPurgeTree(root, tree)
			if err != nil {
				return nil, err
			}
			sets = append(sets, set)
		}
	}
	return sets, nil
}

// planLeftovers turns the collected sets into the leftover list (every
// collected file that is not tracked), each hashed and classified, and keeps
// in plan.sets only those files: step 10 removes nothing else. An untracked
// file git does not ignore refuses: it is project content the commit would not
// carry, and tidy would commit it back.
func planLeftovers(root string, sets []PurgeSet, tracked map[string]bool, plan *DeletePlan) error {
	var untracked []string
	for _, set := range sets {
		kept := PurgeSet{Dirs: set.Dirs}
		for _, rel := range set.Files {
			if tracked[rel] {
				continue
			}
			class := "ignored"
			if ClassifyProjectPath(rel) == ProjectMachineLocal || strings.HasPrefix(rel, "palace/.local/") {
				class = "machine-local"
			} else if ignored, err := GitPathIgnored(root, rel); err != nil {
				return fmt.Errorf("check whether %s is ignored: %w", rel, err)
			} else if !ignored {
				untracked = append(untracked, rel)
				continue
			}
			sum, size, err := HashFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				return fmt.Errorf("hash %s: %w", rel, err)
			}
			plan.Leftovers = append(plan.Leftovers, DeleteLeftover{Path: rel, SHA256: sum, Size: size, Class: class})
			kept.Files = append(kept.Files, rel)
			kept.Bytes += size
		}
		plan.sets = append(plan.sets, kept)
	}
	if len(untracked) > 0 {
		sort.Strings(untracked)
		return fmt.Errorf("refusing: %d untracked file(s) that git does not ignore sit under the deleted trees: %s. "+
			"They are project content this delete would not commit; commit or remove them first",
			len(untracked), strings.Join(firstN(untracked, 20), ", "))
	}
	sort.Slice(plan.Leftovers, func(i, j int) bool { return plan.Leftovers[i].Path < plan.Leftovers[j].Path })
	return nil
}

func planFullDelete(held *vaultlock.Held, req DeleteRequest, dest *destination, plan *DeletePlan) error {
	root := held.Root()
	// Step 2: V is in sync with every remote, live, and SplitPurgePreflight.
	if _, err := requireHeadAtEveryRemote(held, plan.Branch, plan.Remotes); err != nil {
		return fmt.Errorf("refusing: %w", err)
	}
	sets, err := collectDeleteTrees(root, req.Projects)
	if err != nil {
		return err
	}
	collected := map[string]bool{}
	for _, set := range sets {
		for _, rel := range set.Files {
			collected[rel] = true
		}
	}
	head, err := SplitPurgePreflight(root, req.Projects, collected)
	if err != nil {
		return err
	}
	if head != plan.Head {
		return fmt.Errorf("refusing: HEAD moved from %s to %s while the delete was planned; run the dry run again", shortSHA(plan.Head), shortSHA(head))
	}

	tracked := map[string]bool{}
	for _, p := range req.Projects {
		pp := DeleteProjectPlan{Project: p}
		f, n, err := footprintHash(root, head, p)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("refusing: project %q has no content file at HEAD", p)
		}
		pp.Footprint = f
		files, err := lsTreeFiles(root, head, ProjectTrees(p))
		if err != nil {
			return err
		}
		for _, df := range files {
			tracked[df.Path] = true
		}
		pp.Tracked = files
		if dest != nil {
			// Step 4, the copy check.
			c, err := findVerifiedCopy(root, dest, p, f)
			if err != nil {
				return err
			}
			pp.CopyCommit = c
			g, err := destinationGeneration(dest, p)
			if err != nil {
				return err
			}
			pp.DestinationGeneration = g
		}
		gen, warnings, err := NewVault(root).DepartureGeneration(p, req.kind(), pp.DestinationGeneration)
		if err != nil {
			return err
		}
		pp.Generation = gen
		plan.Warnings = append(plan.Warnings, warnings...)
		plan.Projects = append(plan.Projects, pp)
	}
	return planLeftovers(root, sets, tracked, plan)
}

// lsTreeFiles lists every tracked file under trees at commit, with its oid.
func lsTreeFiles(root, commit string, trees []string) ([]DeleteFile, error) {
	out, err := gitCmd(root, 30*time.Second, append([]string{"-c", "core.quotepath=off", "ls-tree", "-r", "-z", "--full-tree", commit, "--"}, trees...)...)
	if err != nil {
		return nil, fmt.Errorf("list the tracked footprint: %w", err)
	}
	var files []DeleteFile
	for _, rec := range strings.Split(out, "\x00") {
		meta, rel, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 3 {
			return nil, fmt.Errorf("unparseable ls-tree record %q", rec)
		}
		files = append(files, DeleteFile{Path: rel, OID: f[2]})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// findVerifiedCopy is § Delete steps 1 and 4 for one project: among every
// ancestor of the destination's tip (not only its first-parent line) that
// carries a Vp-Copy-Project trailer naming p and a Vp-Copy-Source that is one
// of V's remotes, find one whose own F equals its trailer's F, which also
// equals F(V HEAD) and F(destination tip). It returns that copy commit.
func findVerifiedCopy(root string, dest *destination, p, fHead string) (string, error) {
	sources := map[string]bool{}
	urls, err := VaultRemoteURLs(root)
	if err != nil {
		return "", err
	}
	for _, u := range urls {
		if n, ok := normaliseRemoteURL(u); ok {
			sources[n] = true
		}
	}
	fTip, _, err := footprintHash(dest.snap.Dir, dest.snap.Tip, p)
	if err != nil {
		return "", fmt.Errorf("footprint of %q at the destination tip: %w", p, err)
	}
	commits, err := commitsWithTrailer(dest.snap.Dir, dest.snap.Tip, trailerCopyProject)
	if err != nil {
		return "", err
	}
	var candidates, mismatch []string
	for _, c := range commits {
		if !containsString(c.Trailers[trailerCopyProject], p) || !anySource(c.Trailers[trailerCopySource], sources) {
			continue
		}
		candidates = append(candidates, shortSHA(c.SHA))
		trailerF := footprintTrailer(c.Trailers[trailerCopyFootprint], p)
		fC, _, err := footprintHash(dest.snap.Dir, c.SHA, p)
		if err != nil {
			return "", err
		}
		if trailerF != "" && fC == trailerF && fC == fHead && fC == fTip {
			return c.SHA, nil
		}
		mismatch = append(mismatch, fmt.Sprintf("%s: F(commit)=%s trailer=%s", shortSHA(c.SHA), fC, trailerF))
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("refusing: no commit on %s carries a %s: %s trailer whose %s is a remote of this vault; copy the project there first",
			dest.URL, trailerCopyProject, p, trailerCopySource)
	}
	return "", fmt.Errorf("refusing: no copy of %q on %s matches this vault: F(this vault's HEAD)=%s, F(destination tip)=%s; candidates: %s. "+
		"Either %q changed here after the copy, or the copy was changed or partly reverted there",
		p, dest.URL, fHead, fTip, strings.Join(mismatch, "; "), p)
}

// destinationGeneration is the generation of the destination tip's own record
// for p, 0 when it holds none. A record there that cannot be read refuses.
func destinationGeneration(dest *destination, p string) (int, error) {
	rel := departure.RelPath(p)
	blob, found, err := committedBlobAt(dest.snap.Dir, dest.snap.Tip, rel)
	if err != nil {
		return 0, fmt.Errorf("read the destination's %s: %w", rel, err)
	}
	if !found {
		return 0, nil
	}
	rec := departure.Parse(p, blob)
	if rec.Malformed != "" {
		return 0, fmt.Errorf("refusing: the destination's %s cannot be read (%s)", rel, rec.Malformed)
	}
	return rec.Generation, nil
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if strings.TrimSpace(s) == want {
			return true
		}
	}
	return false
}

func anySource(values []string, sources map[string]bool) bool {
	for _, v := range values {
		if n, ok := normaliseRemoteURL(v); ok && sources[n] {
			return true
		}
	}
	return false
}

// footprintTrailer returns the F that a "<p> v1:<hex>" trailer value gives
// for p, or "".
func footprintTrailer(values []string, p string) string {
	for _, v := range values {
		if name, f, ok := strings.Cut(strings.TrimSpace(v), " "); ok && name == p {
			return strings.TrimSpace(f)
		}
	}
	return ""
}

// planLeftoversOnly is the re-run of a delete that died after it published.
// It proceeds only if every remote's LIVE tip contains a delete commit whose
// trailers name each project with the footprint its departure record carries:
// the command never deletes leftovers against an unpublished local delete.
func planLeftoversOnly(held *vaultlock.Held, req DeleteRequest, plan *DeletePlan) error {
	root := held.Root()
	for _, p := range req.Projects {
		rec, _ := departure.Find(root, p)
		if rec.Malformed != "" || rec.Footprint == "" {
			return fmt.Errorf("refusing: %q departed, but its departure record names no footprint, so nothing proves a published delete removed it; its leftovers are not touched", p)
		}
		plan.Projects = append(plan.Projects, DeleteProjectPlan{Project: p, Footprint: rec.Footprint, Generation: rec.Generation, CopyCommit: rec.CopyCommit})
	}
	for _, r := range plan.Remotes {
		tip, err := liveTip(root, r, plan.Branch)
		if err != nil {
			return fmt.Errorf("refusing: %w; the leftovers are removed only once every remote is confirmed", err)
		}
		if tip == "" {
			return fmt.Errorf("refusing: remote %s has no %s; the leftovers are removed only once every remote holds the delete", r, plan.Branch)
		}
		commits, err := commitsWithTrailer(root, tip, trailerDeleteProject)
		if err != nil {
			return err
		}
		for _, pp := range plan.Projects {
			if !publishedDelete(commits, pp.Project, pp.Footprint) {
				return fmt.Errorf("refusing: remote %s does not hold a delete commit for %q with footprint %s; the delete is not published there, so its leftovers are not touched (not present)",
					r, pp.Project, pp.Footprint)
			}
		}
	}
	sets, err := collectDeleteTrees(root, req.Projects)
	if err != nil {
		return err
	}
	tracked := map[string]bool{}
	for _, p := range req.Projects {
		files, err := lsTreeFiles(root, plan.Head, ProjectTrees(p))
		if err != nil {
			return err
		}
		for _, f := range files {
			tracked[f.Path] = true
		}
	}
	if err := planLeftovers(root, sets, tracked, plan); err != nil {
		return err
	}
	if len(plan.Leftovers) == 0 {
		return fmt.Errorf("refusing: %s already departed and %w: not present; nothing was changed", strings.Join(req.Projects, ", "), errNothingLeft)
	}
	return nil
}

func publishedDelete(commits []trailerCommit, p, f string) bool {
	for _, c := range commits {
		if containsString(c.Trailers[trailerDeleteProject], p) && footprintTrailer(c.Trailers[trailerDeleteFP], p) == f {
			return true
		}
	}
	return false
}

// deleteDigest binds what moves, not whole-vault HEADs (§ Command surface ›
// Dry run and digest).
func deleteDigest(root string, req DeleteRequest, plan *DeletePlan) string {
	h := sha256.New()
	line := func(fields ...string) { fmt.Fprintf(h, "%s\n", strings.Join(fields, "\x00")) }
	line(deleteDigestFormat)
	line("command", deleteCommand, string(plan.Mode))
	line("projects", strings.Join(req.Projects, " "))
	line("identity", vaultIdentity(root))
	line("kind", string(plan.Kind), "label", plan.To)
	for _, pp := range plan.Projects {
		line("footprint", pp.Project, pp.Footprint)
		for _, f := range pp.Tracked {
			line("file", f.Path, f.OID)
		}
	}
	for _, l := range plan.Leftovers {
		line("leftover", l.Path, l.SHA256)
	}
	return "v1:" + hex.EncodeToString(h.Sum(nil))
}

// vaultIdentity is V's identity (§ Context): its sorted, normalised remote
// URLs, or its absolute path when it has none.
func vaultIdentity(root string) string {
	urls, _ := VaultRemoteURLs(root)
	var ids []string
	for _, u := range urls {
		if n, ok := normaliseRemoteURL(u); ok {
			ids = append(ids, n)
		}
	}
	if len(ids) == 0 {
		return root
	}
	sort.Strings(ids)
	return strings.Join(ids, " ")
}

// applyFullDelete is § Delete steps 7 to 9. It returns the published commit.
func applyFullDelete(held *vaultlock.Held, req DeleteRequest, plan *DeletePlan) (string, error) {
	root := held.Root()
	runID, err := newDeleteRunID()
	if err != nil {
		return "", err
	}
	m := lifecycleMarker{Command: deleteCommand, RunID: runID, Parent: plan.Head, Rerun: req.commandLine(root, "")}
	// Rule 1: the marker before the first write.
	if err := writeLifecycleMarker(held, m); err != nil {
		return "", err
	}
	rollback := deleteRollback(root, req.Projects)
	abandon := func(cause error) error {
		if rerr := rollback(); rerr != nil {
			return fmt.Errorf("%w; and the rollback failed: %v (the marker is kept; re-run the same command: %s)", cause, rerr, m.Rerun)
		}
		if cerr := clearLifecycleMarker(held, runID); cerr != nil {
			return fmt.Errorf("%w; and clearing the marker failed: %v", cause, cerr)
		}
		return cause
	}

	v := NewVault(root)
	var records []string
	for _, pp := range plan.Projects {
		rel, _, _, err := v.RecordDepartureForDelete(pp.Project, plan.Kind, plan.To, DepartureFacts{
			CopyCommit: pp.CopyCommit, Footprint: pp.Footprint, DestinationGeneration: pp.DestinationGeneration,
		})
		if err != nil {
			return "", abandon(fmt.Errorf("record the departure of %q: %w (nothing was removed)", pp.Project, err))
		}
		records = append(records, rel)
	}
	if err := deleteAfterRecords(); err != nil {
		return "", err
	}

	res, err := CommitSplitPurgeLocked(held, SplitPurgeCommit{
		Slugs:      req.Projects,
		Records:    records,
		Message:    "vault project delete: " + strings.Join(req.Projects, " "),
		ExpectHead: plan.Head,
		Trailers:   deleteTrailers(plan, runID),
	})
	if err != nil && (res == nil || res.CommitSHA == "") {
		return "", abandon(err)
	}
	commit, herr := gitCmd(root, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
	if herr != nil {
		return "", fmt.Errorf("read the delete commit: %w (the marker is kept; re-run the same command: %s)", herr, m.Rerun)
	}
	if err := setLifecycleMarkerCommit(held, runID, commit); err != nil {
		return "", fmt.Errorf("record the delete commit in the marker: %w (re-run the same command: %s)", err, m.Rerun)
	}
	m.Commit = commit
	// Step 8, the postcheck (and CommitSplitPurgeLocked's own assertion).
	if err == nil {
		err = deletePostcheck(root, plan, records)
	}
	if err != nil {
		if rerr := rollBackRun(held, m, rollback); rerr != nil {
			return "", fmt.Errorf("postcheck: %w; and the rollback failed: %v", err, rerr)
		}
		return "", fmt.Errorf("refusing: the delete commit failed its postcheck and was reset: %w", err)
	}

	if err := deleteBeforePublish(); err != nil {
		return "", err
	}
	// Step 9, exact publish. A reset under rules 4 or 6 also removes the
	// record: it is in the commit.
	if err := exactPublish(held, plan.Branch, plan.Remotes, m, rollback, false); err != nil {
		return "", err
	}
	return commit, nil
}

// deleteTrailers is § Delete step 7's trailer block, plus the run's Vp-Run.
func deleteTrailers(plan *DeletePlan, runID string) string {
	var b strings.Builder
	for _, pp := range plan.Projects {
		fmt.Fprintf(&b, "%s: %s\n", trailerDeleteProject, pp.Project)
	}
	fmt.Fprintf(&b, "%s: %s\n", trailerDeleteKind, plan.Kind)
	if plan.To != "" {
		fmt.Fprintf(&b, "%s: %s\n", trailerDeleteTo, plan.To)
	}
	for _, pp := range plan.Projects {
		fmt.Fprintf(&b, "%s: %s %s\n", trailerDeleteFP, pp.Project, pp.Footprint)
	}
	fmt.Fprintf(&b, "%s\n", lifecycleRunTrailerLine(runID))
	return b.String()
}

// deletePostcheck is § Delete step 8: every project departed, and nothing
// uncommitted over the footprint, the records and Audits/.surface. Other
// projects' dirt is expected and ignored.
func deletePostcheck(root string, plan *DeletePlan, records []string) error {
	var paths []string
	for _, pp := range plan.Projects {
		if _, ok := departure.Find(root, pp.Project); !ok {
			return fmt.Errorf("%q does not read as departed after the commit", pp.Project)
		}
		paths = append(paths, ProjectTrees(pp.Project)...)
	}
	paths = append(append(paths, records...), "Audits/.surface")
	out, err := gitCmd(root, 30*time.Second, append([]string{"status", "--porcelain=v1", "-uno", "--"}, paths...)...)
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if out != "" {
		return fmt.Errorf("uncommitted state remains over the deleted paths: %s", strings.ReplaceAll(out, "\n", "; "))
	}
	return nil
}

// deleteRollback undoes a delete's uncommitted writes over the current HEAD
// (after a reset, the parent): the footprint's tracked files are unstaged and
// any git rm removed are restored, and each record and Audits/.surface are
// restored from HEAD, or the record removed when HEAD has none. It restores
// only paths git removed, so it never overwrites an edit.
func deleteRollback(root string, projects []string) func() error {
	return func() error {
		var errs []string
		for _, p := range projects {
			trees := ProjectTrees(p)
			if out, _ := gitCmd(root, 30*time.Second, append([]string{"ls-tree", "--name-only", "HEAD", "--"}, trees...)...); out != "" {
				if _, err := gitCmd(root, 30*time.Second, append([]string{"reset", "-q", "--"}, trees...)...); err != nil {
					errs = append(errs, fmt.Sprintf("unstage %s: %v", p, err))
				}
				deleted, err := gitCmd(root, 30*time.Second, append([]string{"-c", "core.quotepath=off", "ls-files", "--deleted", "-z", "--"}, trees...)...)
				if err != nil {
					errs = append(errs, fmt.Sprintf("list removed files of %s: %v", p, err))
				} else if rels := splitZ([]byte(deleted)); len(rels) > 0 {
					if _, err := gitCmd(root, 60*time.Second, append([]string{"checkout", "HEAD", "--"}, rels...)...); err != nil {
						errs = append(errs, fmt.Sprintf("restore %s: %v", p, err))
					}
				}
			}
			rel := departure.RelPath(p)
			if _, found, _ := ReadCommittedBlob(root, rel); found {
				if err := RestoreFromHEAD(root, rel); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", rel, err))
				}
			} else {
				_, _ = gitCmd(root, 10*time.Second, "reset", "-q", "--", rel)
				if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
					if _, err := vaultfs.Delete(root, rel, ""); err != nil {
						errs = append(errs, fmt.Sprintf("%s: %v", rel, err))
					}
				}
			}
		}
		const surfaceRel = "Audits/.surface"
		if _, found, _ := ReadCommittedBlob(root, surfaceRel); found {
			if dirty, err := HasUncommittedChanges(root, surfaceRel); err == nil && dirty {
				if err := RestoreFromHEAD(root, surfaceRel); err != nil {
					errs = append(errs, fmt.Sprintf("%s: %v", surfaceRel, err))
				}
			}
		}
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "; "))
		}
		return nil
	}
}

// removeDeleteLeftovers is § Delete step 10: remove exactly the planned
// leftovers, each only if it still hashes as planned (vaultfs.Delete's
// compare-and-set), then the emptied directories. A changed leftover is kept
// and reported.
func removeDeleteLeftovers(root string, plan *DeletePlan) (files, dirs int, kept []PurgeCleanupEntry) {
	hashes := make(map[string]string, len(plan.Leftovers))
	for _, l := range plan.Leftovers {
		hashes[l.Path] = l.SHA256
	}
	return CleanupPurgedTrees(root, plan.sets, hashes)
}

// deleteUndoLines are § Undo's lines for this run.
func deleteUndoLines(root, commit string, remotes []string) []string {
	lines := []string{fmt.Sprintf("git -C %s revert %s", root, commit)}
	for _, r := range remotes {
		lines = append(lines, fmt.Sprintf("git -C %s push %s HEAD:refs/heads/%s", root, r, branchOrMain(root)))
	}
	return lines
}

func newDeleteRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make a run id: %w", err)
	}
	return "delete-" + time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:]), nil
}
