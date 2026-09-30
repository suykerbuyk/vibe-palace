// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// vp vault copy: the receiver-run half of a project move. This process serves
// V, the destination. It reads the source vault only through its published git
// remote, into a private blobless snapshot, copies the project footprint from
// that snapshot into V, commits once and publishes exactly that commit.
//
// Design: task lifecycle-commands-design-plan, § Copy, § Command surface (Dry
// run and digest, Locks, Exact publish) and unit U5. Every refusal lives here;
// the CLI (cmd/vp) and the MCP tool (internal/tools) only render.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// vaultManifestRel is the vault manifest's vault-relative path, as git names it.
const vaultManifestRel = ".vibe-palace/vault.toml"

// copyCommand is the lifecycle command name the pending marker records.
const copyCommand = "vault copy"

// copyDigestFormat tags the copy digest. A change to its inputs gets a new tag.
const copyDigestFormat = "vp-vault-copy-digest/1"

// Copy trailer keys (§ Copy step 7). Delete searches B's history for them.
const (
	TrailerCopyProject      = "Vp-Copy-Project"
	TrailerCopySource       = "Vp-Copy-Source"
	TrailerCopySourceCommit = "Vp-Copy-Source-Commit"
	TrailerCopyFootprint    = "Vp-Copy-Footprint"
)

// ErrCopyRefused wraps every refusal copy makes before it writes: the caller's
// input or the vaults' state, never a fault.
var ErrCopyRefused = errors.New("vault copy refused")

func copyRefuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCopyRefused, fmt.Sprintf(format, a...))
}

// CopyRequest is one `vp vault copy` invocation.
type CopyRequest struct {
	// Vault is V, the served vault: an absolute path.
	Vault string
	// Projects are the slugs to copy, under the same names.
	Projects []string
	// From is the source vault's published remote URL.
	From string
	// At pins the source commit the dry run planned against. Empty means the
	// source tip.
	At string
	// Expect, when set, must equal the plan's digest.
	Expect string
	// DryRun stops after the plan (§ Copy step 5).
	DryRun bool
}

// CopyFile is one file that travels: its vault-relative path and git blob id.
type CopyFile struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// CopyProject is one project's share of the plan.
type CopyProject struct {
	Slug      string `json:"slug"`
	Footprint string `json:"footprint"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	// ProjectsOnly is true for a project with no palace/<slug> tree.
	ProjectsOnly bool `json:"projects_only,omitempty"`
}

// CopyPlan is what the dry run shows and the real run binds.
type CopyPlan struct {
	Vault         string        `json:"vault"`
	VaultIdentity string        `json:"vault_identity"`
	Source        string        `json:"source"`
	SourceBranch  string        `json:"source_branch"`
	SourceTip     string        `json:"source_tip"`
	At            string        `json:"at"`
	Projects      []CopyProject `json:"projects"`
	Files         []CopyFile    `json:"files"`
	Bytes         int64         `json:"bytes"`
	PushTargets   []string      `json:"push_targets"`
	Digest        string        `json:"digest"`
	// Refusals is every reason the copy cannot run; empty for a runnable plan.
	Refusals []string `json:"refusals,omitempty"`
	// Command is the exact real-run command line, with --at and --expect,
	// set only for a runnable plan.
	Command string `json:"command"`
}

// CopyResult is what a real run did.
type CopyResult struct {
	Plan *CopyPlan `json:"plan,omitempty"`
	// Commit is the published lifecycle commit.
	Commit string `json:"commit,omitempty"`
	// Redo says what a pending marker from an earlier run led to.
	Redo RedoOutcome `json:"redo"`
	// Undo is the plain-git reversal of this run (§ Undo).
	Undo []string `json:"undo,omitempty"`
}

// copyTestHook, when set, runs after each file is copied (stage "copied", the
// entry index), before the commit (stage "commit"), between the commit and
// the marker's record of it (stage "committed") and between the postcheck and
// the publish (stage "publish"). A test uses it to
// inject a failure, corrupt a copied file, or simulate the process being
// killed (errCopySimulatedKill). Nil in production.
var copyTestHook func(stage string, i int, path string) error

// errCopySimulatedKill makes ApplyCopy return at once, as a killed process
// would: no rollback, marker left standing, lock released by the return.
var errCopySimulatedKill = errors.New("simulated kill")

// PlanCopy is the dry run: ApplyCopy through § Copy step 5, writing nothing
// but the private scratch snapshot, which it removes.
func PlanCopy(req CopyRequest) (*CopyPlan, error) {
	req.DryRun = true
	res, err := ApplyCopy(req)
	if res == nil {
		return nil, err
	}
	return res.Plan, err
}

// CopyCommandLine is the real-run command line for plan: every argument the
// run needs to reproduce the plan and bind it, shell-quoted.
func CopyCommandLine(p *CopyPlan) string {
	parts := []string{"vp", "vault", "copy"}
	for _, pr := range p.Projects {
		parts = append(parts, shellQuote(pr.Slug))
	}
	parts = append(parts, "--from", shellQuote(p.Source), "--at", p.At, "--vault", shellQuote(p.Vault), "--expect", p.Digest)
	return strings.Join(parts, " ")
}

// copyRerunLine is the line a pending marker records: the run's projects,
// source and vault, and nothing that a redo does not need. A redo is admitted
// only for an invocation that produces the same line, so the rollback it may
// run removes exactly the trees the unfinished run could have created.
func copyRerunLine(vault string, projects []string, from string) string {
	parts := []string{"vp", "vault", "copy"}
	for _, p := range projects {
		parts = append(parts, shellQuote(p))
	}
	parts = append(parts, "--from", shellQuote(from), "--vault", shellQuote(vault))
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:@+=,~", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ApplyCopy runs § Copy. With req.DryRun it stops after the plan. The plan is
// always computed first, by the same code, so the real run cannot do what the
// dry run did not show; with req.Expect it refuses unless the digests match.
func ApplyCopy(req CopyRequest) (*CopyResult, error) {
	projects, err := validateCopyRequest(&req)
	if err != nil {
		return nil, err
	}
	if err := RefuseIfGitDisabled(req.Vault, "copy"); err != nil {
		return nil, err
	}

	// Step 1: fetch the source, without the lock. A source that cannot be
	// read at all stops here: nothing further can be checked.
	head, err := lsRemoteHead(req.From)
	if err != nil {
		return nil, copyRefuse("%v (if this is a credentials failure, export "+
			"GIT_SSH_COMMAND='ssh -i <key> -o IdentitiesOnly=yes -o IdentityAgent=none' and re-run)", err)
	}
	snap, err := newRemoteSnapshot(req.From, head.Branch)
	if err != nil {
		return nil, copyRefuse("%v", err)
	}
	defer func() { _ = snap.Close() }()

	// From here every refusal is collected, not returned at the first: the dry
	// run lists them all (§ Command surface, Dry run and digest).
	var refusals []string
	refuse := func(err error) error {
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrCopyRefused) {
			refusals = append(refusals, strings.TrimPrefix(err.Error(), ErrCopyRefused.Error()+": "))
			return nil
		}
		return err
	}

	tip := snap.Tip
	at := req.At
	if at == "" {
		at = tip
	}
	fps := map[string]string{}
	contentCounts := map[string]int{}
	for _, p := range projects {
		f, n, err := footprintHash(snap.Dir, tip, p)
		if err != nil {
			return nil, err
		}
		fps[p], contentCounts[p] = f, n
	}
	if at != tip {
		if err := refuse(checkCopyAt(snap, at, tip, projects, fps)); err != nil {
			return nil, err
		}
	}

	// Everything below reads file contents: fetch them all in one request
	// first, the manifest and any departure record included.
	blobPaths := []string{vaultManifestRel}
	for _, p := range projects {
		blobPaths = append(append(blobPaths, ProjectTrees(p)...), departure.RelPath(p))
	}
	if err := snap.fetchBlobs(tip, blobPaths); err != nil {
		return nil, copyRefuse("%v", err)
	}

	// Step 2: source refusals, at the tip.
	files, projPlans, srcRefusals, err := copySourceRefusals(snap, tip, projects, fps, contentCounts)
	if err != nil {
		return nil, err
	}
	refusals = append(refusals, srcRefusals...)
	srcFormat, err := snapshotFormat(snap, tip)
	if err := refuse(err); err != nil {
		return nil, err
	}
	vFormat, err := surface.ReadFormat(req.Vault)
	if err != nil {
		return nil, fmt.Errorf("read %s's data format: %w", req.Vault, err)
	}
	if srcFormat != 0 && srcFormat != vFormat {
		refusals = append(refusals, fmt.Sprintf("the source is at data format %d and %s is at %d: copy moves projects only between vaults at the same format", srcFormat, req.Vault, vFormat))
	}
	var wt string
	var entries []ProjectTreeEntry
	if len(srcRefusals) == 0 {
		if wt, err = snap.checkoutFootprint(tip, projects); err != nil {
			return nil, err
		}
		entries, err = walkCopyScratch(wt, projects, files)
		if err := refuse(err); err != nil {
			return nil, err
		}
	}

	// Step 3: lock V, then preflight.
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
	rerun := copyRerunLine(req.Vault, projects, req.From)
	rollback := func() error { return removeCopyTrees(req.Vault, projects) }

	res := &CopyResult{Redo: RedoNone}
	if m, found, merr := readLifecycleMarker(req.Vault); found {
		// Rule 6: an unfinished run. A dry run never redoes; a redo is admitted
		// only for the same run, so its rollback removes only that run's trees.
		if merr != nil || req.DryRun || m.Command != copyCommand || m.Rerun != rerun {
			return nil, &LifecyclePendingError{Vault: req.Vault, Marker: m, Malformed: errString(merr)}
		}
		outcome, rm, rerr := redoLifecycle(held, copyCommand, branch, remotes, rollback)
		res.Redo = outcome
		if rerr != nil {
			return res, rerr
		}
		if outcome == RedoPublished {
			res.Commit = rm.Commit
			res.Undo = copyUndoLines(req.Vault, rm.Commit, remotes, branch)
			return res, nil
		}
		// RedoRolledBack: the unfinished run's writes are gone; run afresh.
	} else if merr != nil {
		return nil, merr
	}

	if err := refuseOnPendingDepartures(req.Vault); err != nil {
		refusals = append(refusals, err.Error())
	}
	if dirty, err := footprintDirty(req.Vault, projects); err != nil {
		return nil, err
	} else if dirty != "" {
		refusals = append(refusals, fmt.Sprintf("the footprint paths in %s are not clean:\n%s", req.Vault, dirty))
	}
	parent, err := requireHeadAtEveryRemote(held, branch, remotes)
	if err != nil {
		refusals = append(refusals, err.Error())
		if !errors.Is(err, ErrRemoteNotAtHead) && !errors.Is(err, ErrRemoteUnreachable) {
			refusals[len(refusals)-1] = "cannot check V against its remotes: " + err.Error()
		}
	}

	// Step 4: destination refusals.
	identity := vaultIdentity(req.Vault)
	dstRefusals, err := copyDestinationRefusals(req.Vault, projects, req.From)
	if err != nil {
		return nil, err
	}
	refusals = append(refusals, dstRefusals...)

	// Step 5: plan.
	plan := &CopyPlan{
		Vault: req.Vault, VaultIdentity: identity, Source: req.From, SourceBranch: head.Branch,
		SourceTip: tip, At: at, Projects: projPlans, Files: files, PushTargets: remotes,
	}
	for _, f := range files {
		plan.Bytes += f.Size
	}
	plan.Digest = copyDigest(plan, fps)
	res.Plan = plan
	if req.Expect != "" && !strings.EqualFold(req.Expect, plan.Digest) {
		refusals = append(refusals, fmt.Sprintf("digest mismatch: --expect %s, but the plan now digests to %s — what would move changed since the dry run; re-run the dry run", req.Expect, plan.Digest))
	}
	plan.Refusals = refusals
	if len(refusals) > 0 {
		return res, &CopyRefusalsError{Refusals: refusals}
	}
	plan.Command = CopyCommandLine(plan)
	if req.DryRun {
		return res, nil
	}

	// Step 6: the marker, then the copy.
	runID, err := newRunID()
	if err != nil {
		return res, err
	}
	m := lifecycleMarker{Command: copyCommand, RunID: runID, Parent: parent, Rerun: rerun}
	if err := writeLifecycleMarker(held, m); err != nil {
		return res, err
	}
	// fail rolls this run back in process: its trees, its commit if any, its
	// marker. A simulated kill skips it, as a real one would.
	fail := func(cause error) (*CopyResult, error) {
		if errors.Is(cause, errCopySimulatedKill) {
			return res, cause
		}
		if rerr := rollBackRun(held, m, rollback); rerr != nil {
			return res, fmt.Errorf("%w; rolling back also failed: %v", cause, rerr)
		}
		return res, cause
	}
	modes := map[string]string{}
	for _, f := range files {
		modes[f.Path] = f.Mode
	}
	for i, e := range entries {
		if err := CopyProjectTreeEntry(wt, req.Vault, e); err != nil {
			return fail(err)
		}
		if err := restoreExecBit(req.Vault, e.Path, modes[e.Path]); err != nil {
			return fail(err)
		}
		if copyTestHook != nil {
			if err := copyTestHook("copied", i, e.Path); err != nil {
				return fail(err)
			}
		}
	}
	if copyTestHook != nil {
		if err := copyTestHook("commit", len(entries), ""); err != nil {
			return fail(err)
		}
	}

	// Step 7: commit, carrying the run's Vp-Run trailer, so a redo can adopt
	// the commit even if the run died before recording it in the marker.
	var paths []string
	for _, p := range projects {
		paths = append(paths, ProjectTrees(p)...)
	}
	subject, trailers := copyCommitMessage(plan, fps, runID)
	cres, err := commitPathsLocked(held, subject, trailers, paths)
	if err != nil {
		return fail(fmt.Errorf("commit: %w", err))
	}
	if cres == nil || cres.CommitSHA == "" {
		return fail(fmt.Errorf("commit: nothing was committed"))
	}
	sha, err := gitCmd(req.Vault, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fail(fmt.Errorf("read the copy commit: %w", err))
	}
	if copyTestHook != nil {
		if err := copyTestHook("committed", len(entries), sha); err != nil {
			return fail(err)
		}
	}
	if err := setLifecycleMarkerCommit(held, runID, sha); err != nil {
		return fail(err)
	}
	m.Commit = sha

	// Step 8: postcheck, before anything is published.
	if err := copyPostcheck(req.Vault, sha, projects, fps); err != nil {
		return fail(err)
	}
	if copyTestHook != nil {
		if err := copyTestHook("publish", len(entries), sha); err != nil {
			return fail(err)
		}
	}

	// Step 9: exact publish. On "remote moved" it has already rolled back.
	if err := exactPublish(held, branch, remotes, m, rollback, false); err != nil {
		return res, err
	}
	res.Commit = sha
	res.Undo = copyUndoLines(req.Vault, sha, remotes, branch)
	return res, nil
}

// CopyRefusalsError is every refusal a copy plan found, listed together.
type CopyRefusalsError struct {
	Refusals []string
}

func (e *CopyRefusalsError) Error() string {
	if len(e.Refusals) == 1 {
		return ErrCopyRefused.Error() + ": " + e.Refusals[0]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d refusals:", ErrCopyRefused.Error(), len(e.Refusals))
	for _, r := range e.Refusals {
		b.WriteString("\n  - " + r)
	}
	return b.String()
}

func (e *CopyRefusalsError) Unwrap() error { return ErrCopyRefused }

// restoreExecBit gives a copied file the source's executable bit: the copy
// primitive writes 0644, and F hashes the mode, so a 100755 source file would
// otherwise fail the postcheck on every run.
func restoreExecBit(vaultPath, rel, mode string) error {
	if mode != "100755" {
		return nil
	}
	p := filepath.Join(vaultPath, filepath.FromSlash(rel))
	st, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("stat %s: %w", rel, err)
	}
	if err := os.Chmod(p, st.Mode().Perm()|0o111); err != nil {
		return fmt.Errorf("restore the executable bit of %s: %w", rel, err)
	}
	return nil
}

func validateCopyRequest(req *CopyRequest) ([]string, error) {
	if req.Vault == "" || !filepath.IsAbs(req.Vault) {
		return nil, copyRefuse("the served vault must be an absolute path, got %q", req.Vault)
	}
	req.Vault = filepath.Clean(req.Vault)
	if strings.TrimSpace(req.From) == "" {
		return nil, copyRefuse("--from names no source remote")
	}
	if _, ok := normaliseRemoteURL(req.From); !ok {
		return nil, copyRefuse("--from %q is not a remote URL (a host path is never a source: copy reads only a published remote)", req.From)
	}
	if req.At != "" && !isFullHex(req.At) {
		return nil, copyRefuse("--at %q is not a full commit id", req.At)
	}
	if len(req.Projects) == 0 {
		return nil, copyRefuse("name at least one project")
	}
	var out []string
	for _, p := range req.Projects {
		if err := slug.Validate(p); err != nil {
			return nil, copyRefuse("project %q: %v", p, err)
		}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func isFullHex(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// checkCopyAt admits a source tip that moved past --at only if --at is an
// ancestor of it and no project's footprint changed (§ Copy step 1).
func checkCopyAt(snap *remoteSnapshot, at, tip string, projects []string, fps map[string]string) error {
	if _, err := gitCmd(snap.Dir, 10*time.Second, "cat-file", "-e", at+"^{commit}"); err != nil {
		return copyRefuse("--at %s is not an ancestor of the source tip %s (it is not in the source's history): the source was rewritten since the dry run; re-run the dry run", shortSHA(at), shortSHA(tip))
	}
	if _, err := gitCmd(snap.Dir, 30*time.Second, "merge-base", "--is-ancestor", at, tip); err != nil {
		return copyRefuse("--at %s is not an ancestor of the source tip %s: the source was rewritten since the dry run; re-run the dry run", shortSHA(at), shortSHA(tip))
	}
	for _, p := range projects {
		f, _, err := footprintHash(snap.Dir, at, p)
		if err != nil {
			return err
		}
		if f != fps[p] {
			return copyRefuse("project %s changed since the dry run (footprint at %s is %s, at the source tip %s it is %s); re-run the dry run", p, shortSHA(at), f, shortSHA(tip), fps[p])
		}
	}
	return nil
}

// copySourceRefusals is § Copy step 2, read from the snapshot's trees. It also
// lists what travels: every content file of each footprint, with its blob id.
func copySourceRefusals(snap *remoteSnapshot, tip string, projects []string, fps map[string]string, counts map[string]int) ([]CopyFile, []CopyProject, []string, error) {
	var files []CopyFile
	var plans []CopyProject
	var refusals []string
	for _, p := range projects {
		if counts[p] == 0 {
			refusals = append(refusals, fmt.Sprintf("project %s is absent at the source tip %s, or its footprint holds no content file", p, shortSHA(tip)))
			continue
		}
		pf, bad, err := footprintFiles(snap, tip, p)
		if err != nil {
			return nil, nil, nil, err
		}
		refusals = append(refusals, bad...)
		// A departure record over a residue-only Projects/<p> means the project
		// already left the source. A record that cannot be read is an error, not
		// "no record".
		data, recorded, err := snap.readFile(tip, departure.RelPath(p))
		if err != nil {
			return nil, nil, nil, err
		}
		if recorded {
			rec := departure.Parse(p, []byte(data))
			projectsContent := 0
			for _, f := range pf {
				if strings.HasPrefix(f.Path, "Projects/"+p+"/") {
					projectsContent++
				}
			}
			if projectsContent == 0 {
				why := string(rec.Kind)
				if rec.Malformed != "" {
					why = "unreadable: " + rec.Malformed
				}
				refusals = append(refusals, fmt.Sprintf("project %s departed the source (%s holds a %s record and Projects/%s holds no content)", p, departure.RelPath(p), why, p))
			}
		}
		cp := CopyProject{Slug: p, Footprint: fps[p], ProjectsOnly: true}
		for _, f := range pf {
			cp.Files++
			cp.Bytes += f.Size
			if strings.HasPrefix(f.Path, "palace/"+p+"/") {
				cp.ProjectsOnly = false
			}
		}
		files = append(files, pf...)
		plans = append(plans, cp)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, plans, refusals, nil
}

// footprintFiles lists p's content files at commit, refusing any entry that is
// not a regular file (a symlink or submodule would be a hole or a leak). The
// -l sizes are read from the blobs, so snap.fetchBlobs has run over p's trees
// first: git prints BAD for a blob the snapshot lacks.
func footprintFiles(snap *remoteSnapshot, commit, p string) ([]CopyFile, []string, error) {
	args := append([]string{"ls-tree", "-r", "-l", "--full-tree", commit, "--"}, ProjectTrees(p)...)
	out, err := snap.git(60*time.Second, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list the footprint of %s: %w", p, err)
	}
	var files []CopyFile
	var bad []string
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		meta, rel, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta) // mode type oid size
		if !ok || len(f) != 4 {
			return nil, nil, fmt.Errorf("unparseable ls-tree line %q", line)
		}
		if ClassifyProjectPath(rel) != ProjectContent {
			continue
		}
		if f[0] != "100644" && f[0] != "100755" {
			bad = append(bad, fmt.Sprintf("%s is not a regular file at the source (mode %s): copy refuses symlinks and submodules rather than skipping or following them", rel, f[0]))
			continue
		}
		if f[3] == "BAD" {
			return nil, nil, fmt.Errorf("list the footprint of %s: blob %s of %s is not in the snapshot", p, f[2], rel)
		}
		var size int64
		if _, err := fmt.Sscan(f[3], &size); err != nil {
			return nil, nil, fmt.Errorf("unparseable size in ls-tree line %q", line)
		}
		files = append(files, CopyFile{Path: rel, Mode: f[0], OID: f[2], Size: size})
	}
	return files, bad, nil
}

// snapshotFormat reads the source's .vibe-palace/vault.toml at commit. A source
// without one is refused as not a vault; one that cannot be read is an error.
func snapshotFormat(snap *remoteSnapshot, commit string) (int, error) {
	data, found, err := snap.readFile(commit, vaultManifestRel)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, copyRefuse("the source at %s holds no %s: it is not a vault", shortSHA(commit), vaultManifestRel)
	}
	dir, err := os.MkdirTemp(snap.root, "fmt-")
	if err != nil {
		return 0, err
	}
	p := surface.VaultManifestPath(dir)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return 0, err
	}
	if err := os.WriteFile(p, []byte(data+"\n"), 0o600); err != nil {
		return 0, err
	}
	return surface.ReadFormat(dir)
}

// walkCopyScratch inventories the fresh footprint checkout and proves it holds
// exactly the files the source tree lists: the same paths, nothing more.
func walkCopyScratch(wt string, projects []string, files []CopyFile) ([]ProjectTreeEntry, error) {
	var entries []ProjectTreeEntry
	for _, p := range projects {
		for _, tree := range ProjectTrees(p) {
			_, es, err := WalkProjectTree(wt, filepath.Join(wt, filepath.FromSlash(tree)))
			if err != nil {
				return nil, copyRefuse("%v", err)
			}
			entries = append(entries, es...)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if len(entries) != len(files) {
		return nil, fmt.Errorf("the footprint checkout holds %d files but the source tree lists %d", len(entries), len(files))
	}
	for i := range entries {
		if entries[i].Path != files[i].Path || entries[i].Size != files[i].Size {
			return nil, fmt.Errorf("the footprint checkout does not match the source tree at %s", files[i].Path)
		}
	}
	return entries, nil
}

// footprintDirty is `git status --porcelain` over p's trees, for every p.
func footprintDirty(vaultPath string, projects []string) (string, error) {
	args := []string{"status", "--porcelain", "-uall", "--"}
	for _, p := range projects {
		args = append(args, ProjectTrees(p)...)
	}
	out, err := gitCmd(vaultPath, 30*time.Second, args...)
	if err != nil {
		return "", fmt.Errorf("git status over the footprint: %w", err)
	}
	return out, nil
}

// copyDestinationRefusals is § Copy step 4.
func copyDestinationRefusals(vaultPath string, projects []string, from string) ([]string, error) {
	var refusals []string
	for _, p := range projects {
		for _, tree := range ProjectTrees(p) {
			if _, err := os.Lstat(filepath.Join(vaultPath, filepath.FromSlash(tree))); err == nil {
				refusals = append(refusals, fmt.Sprintf("%s already holds %s: copy never merges into an existing project", vaultPath, tree))
			} else if !os.IsNotExist(err) {
				return nil, fmt.Errorf("stat %s: %w", tree, err)
			}
		}
		if _, found := departure.Read(vaultPath, p); found {
			refusals = append(refusals, fmt.Sprintf("%s holds a departure record for %s (%s): a project cannot be copied back over its own departure in v1", vaultPath, p, departure.RelPath(p)))
		}
	}
	want, _ := normaliseRemoteURL(from)
	urls, err := VaultRemoteURLs(vaultPath)
	if err != nil {
		return nil, fmt.Errorf("read %s's remotes: %w", vaultPath, err)
	}
	for _, u := range urls {
		if n, ok := normaliseRemoteURL(u); ok && n == want {
			refusals = append(refusals, fmt.Sprintf("--from %s is one of %s's own remotes: the source must be another vault", from, vaultPath))
			break
		}
	}
	return refusals, nil
}

// copyDigest binds what moves (§ Command surface, the digest table): the
// format tag, the command, the projects, V's identity, the normalised source
// URL, F per project and every file's path and blob id. Never a HEAD or a tip:
// an unrelated push to either vault leaves it unchanged.
func copyDigest(p *CopyPlan, fps map[string]string) string {
	src, _ := normaliseRemoteURL(p.Source)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\ncommand %s\n", copyDigestFormat, copyCommand)
	for _, pr := range p.Projects {
		fmt.Fprintf(&b, "project %s %s\n", pr.Slug, fps[pr.Slug])
	}
	fmt.Fprintf(&b, "vault %s\nsource %s\n", p.VaultIdentity, src)
	for _, f := range p.Files {
		fmt.Fprintf(&b, "file %s %s\n", f.OID, f.Path)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// copyCommitMessage is § Copy step 7's subject and trailers. The trailers go
// through commitPathsLocked's trailers argument, so the shared
// stampedCommitMessage places them after the hostname stamp, last, where git
// reads them.
func copyCommitMessage(p *CopyPlan, fps map[string]string, runID string) (subject, trailers string) {
	var slugs []string
	for _, pr := range p.Projects {
		slugs = append(slugs, pr.Slug)
	}
	subject = fmt.Sprintf("vault copy: %s from %s@%s", strings.Join(slugs, " "), p.Source, p.SourceTip[:12])
	var b strings.Builder
	for _, s := range slugs {
		fmt.Fprintf(&b, "%s: %s\n", TrailerCopyProject, s)
	}
	fmt.Fprintf(&b, "%s: %s\n", TrailerCopySource, p.Source)
	fmt.Fprintf(&b, "%s: %s\n", TrailerCopySourceCommit, p.SourceTip)
	for _, s := range slugs {
		fmt.Fprintf(&b, "%s: %s %s\n", TrailerCopyFootprint, s, fps[s])
	}
	b.WriteString(lifecycleRunTrailerLine(runID))
	return subject, b.String()
}

// copyPostcheck is § Copy step 8, on the committed tree, before any publish.
func copyPostcheck(vaultPath, sha string, projects []string, fps map[string]string) error {
	for _, p := range projects {
		f, _, err := footprintHash(vaultPath, sha, p)
		if err != nil {
			return err
		}
		if f != fps[p] {
			return copyRefuse("postcheck: %s's committed footprint is %s, the source's is %s — something rewrote the bytes on the way in (a git filter, or core.autocrlf converting CRLF content)", p, f, fps[p])
		}
	}
	out, err := gitCmd(vaultPath, 30*time.Second, "diff", "--name-only", "--no-renames", sha+"~1", sha)
	if err != nil {
		return fmt.Errorf("postcheck: diff the copy commit: %w", err)
	}
	for rel := range strings.SplitSeq(out, "\n") {
		if rel == "" {
			continue
		}
		inside := false
		for _, p := range projects {
			for _, tree := range ProjectTrees(p) {
				if strings.HasPrefix(rel, tree+"/") {
					inside = true
				}
			}
		}
		if !inside {
			return copyRefuse("postcheck: the copy commit touches %s, outside the copied projects' footprint", rel)
		}
	}
	if dirty, err := footprintDirty(vaultPath, projects); err != nil {
		return err
	} else if dirty != "" {
		return copyRefuse("postcheck: the footprint paths are not clean after the commit:\n%s", dirty)
	}
	return nil
}

// removeCopyTrees is copy's rollback (§ Copy › Rollback): remove this run's
// trees, which step 4 proved absent before the run, then prove the footprint
// paths clean.
func removeCopyTrees(vaultPath string, projects []string) error {
	// Through the vault's own removal primitives (vaultfs.Delete, then
	// RemoveNoLock bottom-up), never a recursive raw remove.
	for _, p := range projects {
		for _, tree := range ProjectTrees(p) {
			set, err := CollectPurgeTree(vaultPath, tree)
			if err != nil {
				return fmt.Errorf("roll back %s: %w", tree, err)
			}
			if _, _, err := RemovePurgeTree(vaultPath, set, nil); err != nil {
				return fmt.Errorf("roll back %s: %w", tree, err)
			}
		}
	}
	dirty, err := footprintDirty(vaultPath, projects)
	if err != nil {
		return err
	}
	if dirty != "" {
		return fmt.Errorf("the footprint paths are still not clean after the rollback:\n%s", dirty)
	}
	return nil
}

// copyUndoLines are § Undo's plain-git reversal of a published copy.
func copyUndoLines(vaultPath, sha string, remotes []string, branch string) []string {
	lines := []string{fmt.Sprintf("git -C %s revert --no-edit %s", shellQuote(vaultPath), sha)}
	for _, r := range remotes {
		lines = append(lines, fmt.Sprintf("git -C %s push %s HEAD:refs/heads/%s", shellQuote(vaultPath), shellQuote(r), branch))
	}
	return lines
}

func newRunID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make a run id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
