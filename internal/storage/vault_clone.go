// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// CloneVault is `vp vault clone <url> <path> [--bind <project>...]`: make a new
// local vault from a published remote, wire every remote the vault records of
// itself, and — with --bind — name it this host's PRIMARY vault for those
// projects in one compare-and-set write of the host config. No commit and no
// push in any vault; the default vault is only read. CLI only.
//
// Design: task lifecycle-commands-design-plan, § Clone (steps 1–9, Secondary
// remotes, Primary, Bind core) and § Admin procedure step 6.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// cloneDigestFormat tags the dry-run digest.
const cloneDigestFormat = "vp-vault-clone/1"

// Refusals of CloneVault, each its own sentinel.
var (
	ErrCloneUnreachable   = errors.New("the remote cannot be reached")
	ErrCloneNotAVault     = errors.New("not a vault yet")
	ErrCloneFormat        = errors.New("the remote's data format is not this binary's")
	ErrCloneRemoteUnknown = errors.New("the URL is not one of the vault's recorded remotes")
	ErrCloneMirror        = errors.New("a recorded mirror cannot be used")
	ErrCloneExpect        = errors.New("the plan's digest is not the --expect digest")
)

// CloneRequest is one `vp vault clone`.
type CloneRequest struct {
	URL string
	// Path is the new vault's absolute path, after ~ expansion.
	Path   string
	Bind   []string
	DryRun bool
	Expect string
}

// CloneRemote is one remote the clone gets.
type CloneRemote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// State is "cloned" for the remote cloned from, "in sync" for a mirror at
	// the cloned tip, or "behind" for a mirror whose tip is an ancestor of it.
	State string `json:"state"`
}

// CloneReport is what CloneVault did, or would do under DryRun.
type CloneReport struct {
	Path     string        `json:"path"`
	URL      string        `json:"url"`
	Branch   string        `json:"branch"`
	Tip      string        `json:"tip"`
	Remotes  []CloneRemote `json:"remotes"`
	Bind     []string      `json:"bind,omitempty"`
	Bindings []string      `json:"project_vaults_lines,omitempty"`
	// ConfigChange is the bind's change to the host config.
	ConfigChange string   `json:"config_change,omitempty"`
	ConfigPath   string   `json:"config_path,omitempty"`
	BackupPath   string   `json:"backup_path,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	Digest       string   `json:"digest"`
	// Command is the exact real-run command line, --expect filled in.
	Command string `json:"command"`
	DryRun  bool   `json:"dry_run"`
	// Resumed: <path> was an earlier run's clone, stopped before its bind,
	// and this run finished (or, dry, would finish) it.
	Resumed bool     `json:"resumed,omitempty"`
	Undo    []string `json:"undo,omitempty"`
}

// Test seams, no-ops in production. cloneBeforeRename runs between the plan
// and the rename; cloneAfterRename between the rename and the bind — a seam
// that returns an error stops the run there, as a killed process would;
// cloneBeforeBind just before the real bind, fresh run or re-run.
var (
	cloneBeforeRename = func() {}
	cloneAfterRename  = func() error { return nil }
	cloneBeforeBind   = func() {}
)

// cloneMarkerName is the host-local mark of a clone this command started and
// has not finished: <path>/.git/vp-clone-pending, written in the scratch
// before the rename and removed only after the bind succeeds. A re-run resumes
// only a <path> that carries it.
const cloneMarkerName = "vp-clone-pending"

// cloneMarker is what the mark records: the URL cloned from, the cloned tip,
// the run, and every remote wired, as name=url.
type cloneMarker struct {
	URL     string   `json:"url"`
	Tip     string   `json:"tip"`
	RunID   string   `json:"run_id"`
	Remotes []string `json:"remotes"`
	Created string   `json:"created"`
}

// ErrCloneCredentials refuses a URL carrying credentials. A clone writes every
// URL into .git/config, and the vault's remotes.toml is tracked and published,
// so a token in either would travel; use an ssh key or a credential helper.
var ErrCloneCredentials = errors.New("the URL carries credentials")

// cloneScratchStale is how old a .<name>.vp-clone-* sibling must be before a
// later run removes it. A live run's clone step is bounded by
// lifecycleSnapshotTimeout, and the rest takes seconds, so a sibling this old
// belongs to a run that died.
const cloneScratchStale = 3 * lifecycleSnapshotTimeout

// CloneVault clones, checks, wires and optionally binds. See the file comment.
func CloneVault(ctx context.Context, req CloneRequest) (*CloneReport, error) {
	if strings.TrimSpace(req.URL) == "" {
		return nil, fmt.Errorf("vp vault clone needs a remote URL")
	}
	if redactURL(req.URL) != req.URL {
		return nil, fmt.Errorf("refusing to clone: %w: %s; use an ssh key or a credential helper instead",
			ErrCloneCredentials, redactURL(req.URL))
	}
	for i, s := range req.Bind {
		if slices.Contains(req.Bind[:i], s) {
			return nil, fmt.Errorf("--bind names %q twice", s)
		}
	}
	// Step 1: the path, by the vault init rule: absent, with a parent, inside
	// no vault (the default vault included) and no git work tree.
	if strings.TrimSpace(req.Path) == "" || !filepath.IsAbs(req.Path) {
		return nil, fmt.Errorf("refusing to clone: %q is not an absolute path", req.Path)
	}
	path := filepath.Clean(req.Path)
	if err := RefuseIfGitDisabled(path, "clone a vault"); err != nil {
		return nil, err
	}
	// A dry run writes nothing, so it takes no lock and reaps nothing. A real
	// run, fresh or re-run, holds <parent>/.<name>.vp-clone.lock throughout:
	// without it a re-run could resume and bind a clone whose fresh run is
	// still binding, and that run's cleanup would then remove the bound path.
	var reaped []string
	if !req.DryRun {
		lock := cloneLockPath(path)
		release, ok, err := vaultlock.TryAcquireFile(lock)
		if err != nil {
			return nil, fmt.Errorf("refusing to clone: lock %s: %w", lock, err)
		}
		if !ok {
			return nil, fmt.Errorf("refusing to clone: another vp vault clone into %s is running (it holds %s); wait for it to finish, then run the command again", path, lock)
		}
		defer func() { _ = release() }()
		reaped = reapCloneScratch(path)
	}
	if _, err := os.Lstat(path); err == nil {
		rep, err := resumeClone(req, path)
		if rep != nil {
			rep.Warnings = append(reaped, rep.Warnings...)
		}
		return rep, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("refusing to clone: %s: %w", path, err)
	}
	if err := initCheckParent(path); err != nil {
		return nil, cloneWording(err)
	}

	// Step 2: probe the remote.
	head, err := lsRemoteHead(req.URL)
	if err != nil {
		if errors.Is(err, ErrRemoteHasNoHead) {
			return nil, fmt.Errorf("refusing to clone: %w: %s has no HEAD (its first push has not happened)", ErrCloneNotAVault, req.URL)
		}
		return nil, cloneUnreachable(req.URL, err)
	}
	rep := &CloneReport{Path: path, URL: req.URL, Branch: head.Branch, Tip: head.Tip, Bind: req.Bind, DryRun: req.DryRun, Warnings: reaped}

	// Step 3: a full clone into a scratch sibling on <path>'s filesystem.
	scratch, err := cloneScratchPath(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if scratch != "" {
			_ = os.RemoveAll(scratch)
		}
	}()
	if _, err := lifecycleGit(filepath.Dir(path), lifecycleSnapshotTimeout, "clone", "--quiet", "--branch", head.Branch, "--", req.URL, scratch); err != nil {
		return nil, cloneUnreachable(req.URL, err)
	}
	if tip, err := gitCmd(scratch, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}"); err == nil {
		rep.Tip = tip
	}

	// Step 4: a vault of this binary's data format.
	if err := cloneCheckFormat(scratch, req.URL); err != nil {
		return nil, err
	}

	// Step 5: the remotes the vault records of itself.
	recorded, err := readRecordedRemotes(scratch)
	if err != nil {
		return nil, err
	}
	remotes, warnings, err := wireCloneRemotes(scratch, req.URL, head.Branch, rep.Tip, recorded)
	if err != nil {
		return nil, err
	}
	rep.Remotes = remotes
	rep.Warnings = append(rep.Warnings, warnings...)

	// Step 6: the bind plan, checked against the scratch clone.
	if err := clonePlanBind(rep, req, path, scratch); err != nil {
		return nil, err
	}
	if req.DryRun {
		return rep, nil
	}
	if err := cloneCheckExpect(rep, req); err != nil {
		return nil, err
	}
	runID, err := writeCloneMarker(scratch, req.URL, rep.Tip, rep.Remotes)
	if err != nil {
		return nil, err
	}
	cloneBeforeRename()

	// Step 7: the rename.
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("refusing to clone: %w: %s appeared while cloning", ErrInitPathExists, path)
	}
	if err := os.Rename(scratch, path); err != nil {
		return nil, fmt.Errorf("move the clone into place: %w", err)
	}
	scratch = ""
	if err := cloneAfterRename(); err != nil {
		return nil, err
	}

	// Steps 8 and 9. A failed bind removes the fresh <path>.
	return finishClone(rep, req, path, runID)
}

// cloneLockPath is <parent>/.<name>.vp-clone.lock.
func cloneLockPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".vp-clone.lock")
}

func cloneMarkerPath(root string) string {
	return filepath.Join(root, ".git", cloneMarkerName)
}

// writeCloneMarker writes the mark into the scratch clone's .git, durably,
// and returns its run id.
func writeCloneMarker(scratch, rawURL, tip string, remotes []CloneRemote) (string, error) {
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("make a run id: %w", err)
	}
	m := cloneMarker{URL: rawURL, Tip: tip, RunID: "clone-" + hex.EncodeToString(id[:]), Created: time.Now().UTC().Format(time.RFC3339)}
	for _, r := range remotes {
		m.Remotes = append(m.Remotes, r.Name+"="+r.URL)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(cloneMarkerPath(scratch), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("write the clone marker: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write the clone marker: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("write the clone marker: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("write the clone marker: %w", err)
	}
	return m.RunID, nil
}

// readCloneMarker reads the mark; found is false when there is none.
func readCloneMarker(root string) (m cloneMarker, found bool, err error) {
	data, err := os.ReadFile(cloneMarkerPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, true, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, true, err
	}
	if m.URL == "" || m.RunID == "" {
		return m, true, errors.New("it names no URL or run")
	}
	return m, true, nil
}

// clonePlanBind is step 6: BindProjectVaults as a dry run over checkRoot, the
// [project_vaults] lines, and the digest and real-run line.
func clonePlanBind(rep *CloneReport, req CloneRequest, path, checkRoot string) error {
	var cfgBytes []byte
	if len(req.Bind) > 0 {
		brep, err := BindProjectVaults(BindVaultsRequest{Slugs: req.Bind, VaultPath: path, CheckRoot: checkRoot, Mode: BindMoved, DryRun: true})
		if err != nil {
			return fmt.Errorf("refusing to clone: %w (nothing was written)", err)
		}
		rep.ConfigPath = brep.ConfigPath
		rep.ConfigChange = brep.Change
		for _, s := range req.Bind {
			rep.Bindings = append(rep.Bindings, fmt.Sprintf("%s = %q", s, path))
		}
		if brep.ConfigPath != "" {
			cfgBytes, _ = os.ReadFile(brep.ConfigPath)
		}
	}
	rep.Digest = cloneDigest(req, path, rep.Remotes, cfgBytes)
	rep.Command = cloneCommandLine(req, path, rep.Digest)
	return nil
}

func cloneCheckExpect(rep *CloneReport, req CloneRequest) error {
	if req.Expect != "" && req.Expect != rep.Digest {
		return fmt.Errorf("refusing to clone: %w: the plan's digest is %s, not %s; something changed since the dry run (the recorded remotes, the host config or the default vault's records); run the dry run again",
			ErrCloneExpect, rep.Digest, req.Expect)
	}
	return nil
}

// cloneCheckFormat is step 4: a vault manifest at this binary's data format.
func cloneCheckFormat(root, rawURL string) error {
	if _, err := os.Stat(surface.VaultManifestPath(root)); err != nil {
		return fmt.Errorf("refusing to clone: %w: %s holds no %s", ErrCloneNotAVault, rawURL, ".vibe-palace/vault.toml")
	}
	format, err := surface.ReadFormat(root)
	if err != nil {
		return fmt.Errorf("refusing to clone: %w: %v", ErrCloneFormat, err)
	}
	if format != surface.RequiredDataFormat {
		return fmt.Errorf("refusing to clone: %w: the vault is at data format %d and this binary requires %d; upgrade whichever side is older, then clone again",
			ErrCloneFormat, format, surface.RequiredDataFormat)
	}
	return nil
}

// finishClone is steps 8 and 9: the bind against <path> itself (it verifies,
// and restores the config on a failed verify), then the report. ownRun is the
// run id of the clone this run made, whose <path> a failed bind removes; it is
// "" for a re-run finishing an earlier run's clone, which stays re-runnable.
// Even this run's clone is kept when the host config binds a project to it or
// its marker is no longer this run's: then something else has taken it over.
func finishClone(rep *CloneReport, req CloneRequest, path, ownRun string) (*CloneReport, error) {
	if len(req.Bind) > 0 {
		cloneBeforeBind()
		brep, err := BindProjectVaults(BindVaultsRequest{Slugs: req.Bind, VaultPath: path, Mode: BindMoved})
		if err != nil {
			if ownRun == "" {
				return nil, fmt.Errorf("refusing: the bind failed; %s is kept, and re-running the same command finishes it: %w", path, err)
			}
			if why := cloneTakenOver(path, ownRun); why != "" {
				return nil, fmt.Errorf("refusing: the bind failed; %s is kept because %s: %w", path, why, err)
			}
			if rerr := os.RemoveAll(path); rerr != nil {
				return nil, fmt.Errorf("refusing: the bind failed (%w), and removing %s failed too: %v", err, path, rerr)
			}
			return nil, fmt.Errorf("refusing: the bind failed, so %s was removed again: %w", path, err)
		}
		rep.BackupPath = brep.BackupPath
		rep.ConfigChange = brep.Change
		rep.Warnings = append(rep.Warnings, brep.Warnings...)
	}
	// Only now is the clone finished: a re-run over it refuses from here on.
	if err := os.Remove(cloneMarkerPath(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("the clone is finished, but removing %s failed (%v); remove it by hand", cloneMarkerPath(path), err))
	}
	rep.Undo = []string{"rm -rf " + shellQuote(path)}
	if len(req.Bind) > 0 {
		rep.Undo = append(rep.Undo, fmt.Sprintf("cp -- %s %s   # or delete the [%s] lines printed above", shellQuote(rep.BackupPath), shellQuote(rep.ConfigPath), projectVaultsKey))
		rep.Warnings = append(rep.Warnings, "restart every `vp mcp` on this host: a running server resolved its vault at startup")
	}
	return rep, nil
}

// cloneTakenOver says why this run's fresh clone at path must not be removed
// after its bind failed, or "" when it may: the host config binds a project to
// path (a concurrent bind), or its marker is gone or another run's. An
// unreadable config counts as binding it.
func cloneTakenOver(path, runID string) string {
	bindings, cfgPath, err := readProjectVaults()
	if err != nil {
		return fmt.Sprintf("the host config cannot be read to rule out a binding to it (%v)", err)
	}
	for s, v := range bindings {
		if abs, err := expandTilde(v); err == nil && sameVaultRoot(abs, path) {
			return fmt.Sprintf("%s binds %s to it", cfgPath, s)
		}
	}
	if m, found, err := readCloneMarker(path); err != nil || !found || m.RunID != runID {
		return "its clone marker is no longer this run's"
	}
	return ""
}

// resumeClone is a re-run over an existing <path>. It finishes the bind only
// when <path> is this command's own unfinished clone of the same URL, checked
// afresh: it carries the clone marker naming that URL and the remotes it
// wired; it is the top level of its own git repository, inside no vault or
// work tree, with a clean tree, at this data format; its remotes are exactly
// the marked ones, which are the ones it records (or origin alone); its HEAD
// is contained in the live tip of <url>; and every mirror passes the mirror
// check again against that tip. Anything else refuses and is left untouched:
// vp vault clone never adopts some other directory.
func resumeClone(req CloneRequest, path string) (*CloneReport, error) {
	refuseAs := func(sentinel error, why string) (*CloneReport, error) {
		return nil, fmt.Errorf("refusing to clone: %w: %s is not an unfinished clone of %s (%w: %s); vp vault clone never adopts or overwrites another directory. Choose another path",
			ErrInitPathExists, path, req.URL, sentinel, why)
	}
	refuse := func(why string) (*CloneReport, error) {
		return nil, fmt.Errorf("refusing to clone: %w: %s is not an unfinished clone of %s (%s); vp vault clone never adopts or overwrites another directory. Choose another path",
			ErrInitPathExists, path, req.URL, why)
	}
	if state, _ := InspectVaultGit(path); state != VaultGitOK {
		return refuse("it is not the top level of its own git repository")
	}
	marker, found, err := readCloneMarker(path)
	if !found {
		return refuse("it carries no .git/" + cloneMarkerName + ", which vp vault clone writes before the move and removes once the clone is finished")
	}
	if err != nil {
		return refuse(fmt.Sprintf(".git/%s cannot be read: %v", cloneMarkerName, err))
	}
	target, _ := normaliseRemoteURL(req.URL)
	if mn, _ := normaliseRemoteURL(marker.URL); mn != target {
		return refuse(fmt.Sprintf("it is an unfinished clone of %s", redactURL(marker.URL)))
	}
	if err := initCheckParent(path); err != nil {
		return nil, cloneWording(err)
	}
	status, err := gitCmd(path, 30*time.Second, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return refuse(fmt.Sprintf("git status fails there: %v", err))
	}
	if status != "" {
		return refuse("its tree has changes: " + strings.ReplaceAll(status, "\n", "; "))
	}
	if err := cloneCheckFormat(path, req.URL); err != nil {
		return refuse(err.Error())
	}
	recorded, err := readRecordedRemotes(path)
	if err != nil {
		return nil, err
	}
	want := recorded
	if len(want) == 0 {
		want = []RecordedRemote{{Name: "origin", URL: req.URL}}
	}
	if !sameRemoteSpecs(marker.Remotes, want) {
		return refuse(fmt.Sprintf("its marker names remotes %v, it records %v", marker.Remotes, remoteSpecs(want)))
	}
	names := strings.Fields(mustGit(path, "remote"))
	if len(names) != len(want) {
		return refuse(fmt.Sprintf("it has remotes %v, its record names %d", names, len(want)))
	}
	self := -1
	for i, r := range want {
		got := mustGit(path, "remote", "get-url", r.Name)
		gn, _ := normaliseRemoteURL(got)
		wn, _ := normaliseRemoteURL(r.URL)
		if got == "" || gn != wn {
			return refuse(fmt.Sprintf("its remote %s is %q, its record says %q", r.Name, got, r.URL))
		}
		if wn == target {
			self = i
		}
	}
	if self < 0 {
		return refuse("none of its remotes is that URL")
	}

	// The network: <url>'s live tip must contain HEAD, and each mirror must
	// pass the mirror check again against that tip.
	branch := branchOrMain(path)
	head, err := gitCmd(path, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return refuse(fmt.Sprintf("it has no HEAD commit: %v", err))
	}
	live, err := liveTip(path, want[self].Name, branch)
	if err != nil {
		return refuseAs(ErrCloneUnreachable, fmt.Sprintf("%s cannot be read to confirm it: %v", redactURL(req.URL), err))
	}
	if ok, err := tipContains(path, head, live); err != nil || !ok {
		return refuse(fmt.Sprintf("its HEAD %s is not contained in %s's live %s tip %s", shortSHA(head), redactURL(req.URL), branch, shortSHA(live)))
	}
	// Bind against the vault as published now, never a stale HEAD: the vault
	// may since have recorded a project's departure, which the bind must see.
	var forwarded string
	if head != live && req.DryRun {
		forwarded = fmt.Sprintf("the real run first fast-forwards %s from %s to %s's live tip %s, and checks the bind against that", path, shortSHA(head), redactURL(req.URL), shortSHA(live))
	} else if head != live {
		// A fast-forward merges incoming vault commits, so it runs the same
		// departure guard as every pull; with HEAD behind the tip there is no
		// local work for it to strand, so it refuses only on a broken state.
		if _, err := lifecycleGit(path, lifecycleNetTimeout, "fetch", "--quiet", "--no-tags", want[self].Name); err != nil {
			return refuseAs(ErrCloneUnreachable, fmt.Sprintf("fetch %s: %v", redactURL(req.URL), err))
		}
		if err := guardIncomingDepartures(path, want[self].Name, branch); err != nil {
			return refuse(err.Error())
		}
		if _, err := gitCmd(path, 30*time.Second, "merge", "--ff-only", "--quiet", live); err != nil {
			return refuse(fmt.Sprintf("its HEAD %s does not fast-forward to %s's live tip %s: %v", shortSHA(head), redactURL(req.URL), shortSHA(live), err))
		}
		forwarded = fmt.Sprintf("fast-forwarded %s from %s to %s's live tip %s", path, shortSHA(head), redactURL(req.URL), shortSHA(live))
		head = live
	}
	remotes := []CloneRemote{{Name: want[self].Name, URL: redactURL(want[self].URL), State: "cloned"}}
	var warnings []string
	for i, r := range want {
		if i == self {
			continue
		}
		cr, warn, err := checkCloneMirror(path, r, branch, live)
		if err != nil {
			return refuseAs(ErrCloneMirror, err.Error())
		}
		remotes = append(remotes, cr)
		if warn != "" {
			warnings = append(warnings, warn)
		}
	}

	rep := &CloneReport{Path: path, URL: req.URL, Branch: branch, Tip: head, Remotes: remotes, Bind: req.Bind, DryRun: req.DryRun, Resumed: true}
	rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s is an earlier run's clone of %s, stopped before its bind: this run finishes it", path, req.URL))
	if forwarded != "" {
		rep.Warnings = append(rep.Warnings, forwarded)
	}
	rep.Warnings = append(rep.Warnings, warnings...)
	if err := clonePlanBind(rep, req, path, path); err != nil {
		return nil, err
	}
	if req.DryRun {
		return rep, nil
	}
	if err := cloneCheckExpect(rep, req); err != nil {
		return nil, err
	}
	return finishClone(rep, req, path, "")
}

// remoteSpecs is rs as name=url lines.
func remoteSpecs(rs []RecordedRemote) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name+"="+r.URL)
	}
	return out
}

// sameRemoteSpecs: the marker's name=url lines are exactly want, in any
// order, URLs compared by repository.
func sameRemoteSpecs(marked []string, want []RecordedRemote) bool {
	norm := func(name, url string) string {
		if n, ok := normaliseRemoteURL(url); ok {
			url = n
		}
		return name + "=" + url
	}
	var a, b []string
	for _, m := range marked {
		name, url, _ := strings.Cut(m, "=")
		a = append(a, norm(name, url))
	}
	for _, r := range want {
		b = append(b, norm(r.Name, r.URL))
	}
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func mustGit(dir string, args ...string) string {
	out, _ := gitCmd(dir, 10*time.Second, args...)
	return out
}

// cloneScratchPath is <parent>/.<name>.vp-clone-<unix>-<random>, beside
// <path> so the rename stays on one filesystem; the time lets a later run tell
// a dead run's scratch from a live one.
func cloneScratchPath(path string) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("make a scratch name: %w", err)
	}
	return filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.vp-clone-%d-%s", filepath.Base(path), time.Now().Unix(), hex.EncodeToString(b[:]))), nil
}

// reapCloneScratch removes the .<name>.vp-clone-* siblings a killed run left,
// once they are older than cloneScratchStale, and says which.
func reapCloneScratch(path string) []string {
	prefix := "." + filepath.Base(path) + ".vp-clone-"
	ents, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil
	}
	var reaped []string
	for _, e := range ents {
		name, ok := strings.CutPrefix(e.Name(), prefix)
		if !ok || !e.IsDir() {
			continue
		}
		born := time.Time{}
		if stamp, _, ok := strings.Cut(name, "-"); ok {
			var unix int64
			if _, err := fmt.Sscanf(stamp, "%d", &unix); err == nil {
				born = time.Unix(unix, 0)
			}
		}
		if born.IsZero() {
			if info, err := e.Info(); err == nil {
				born = info.ModTime()
			}
		}
		if born.IsZero() || time.Since(born) < cloneScratchStale {
			continue
		}
		abs := filepath.Join(filepath.Dir(path), e.Name())
		if err := os.RemoveAll(abs); err == nil {
			reaped = append(reaped, "removed "+abs+", a scratch directory an earlier, stopped clone left")
		}
	}
	return reaped
}

// cloneWording re-words vault init's shared path refusals for clone, keeping
// the sentinel.
func cloneWording(err error) error {
	return &cloneWordedError{msg: "refusing to clone: " + strings.ReplaceAll(err.Error(), "vault init: ", ""), err: err}
}

type cloneWordedError struct {
	msg string
	err error
}

func (e *cloneWordedError) Error() string { return e.msg }
func (e *cloneWordedError) Unwrap() error { return e.err }

// cloneUnreachable is the refusal for a remote that cannot be read, naming
// the Q3 remedy. No URL here carries credentials: CloneVault and
// readRecordedRemotes refuse those first.
func cloneUnreachable(rawURL string, err error) error {
	return fmt.Errorf("refusing to clone: %w: %s: %s. If it needs an ssh key this shell does not offer, export "+
		"GIT_SSH_COMMAND='ssh -i <key> -o IdentitiesOnly=yes -o IdentityAgent=none' and run it again",
		ErrCloneUnreachable, rawURL, err.Error())
}

// readRecordedRemotes reads the tracked .vibe-palace/remotes.toml; a vault
// without one records nothing.
func readRecordedRemotes(root string) ([]RecordedRemote, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(remotesFile)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", remotesFile, err)
	}
	var rec recordedRemotes
	if _, err := toml.Decode(string(data), &rec); err != nil {
		return nil, fmt.Errorf("refusing to clone: %s does not parse: %w", remotesFile, err)
	}
	var out []RecordedRemote
	for _, r := range rec.Remote {
		// Untrusted input: the file came from the remote. A credential here
		// would land in this host's .git/config.
		if redactURL(r.URL) != r.URL {
			return nil, fmt.Errorf("refusing to clone: %w: %s records remote %q as %s; use an ssh key or a credential helper instead",
				ErrCloneCredentials, remotesFile, r.Name, redactURL(r.URL))
		}
		out = append(out, RecordedRemote{Name: r.Name, URL: r.URL})
	}
	if len(out) > 0 {
		if err := initCheckRemotes(out); err != nil {
			return nil, fmt.Errorf("refusing to clone: %s: %w", remotesFile, err)
		}
	}
	return out, nil
}

// wireCloneRemotes is § Clone › Secondary remotes: <url> must match one
// recorded entry by repository, whose name the clone's remote takes; every
// other entry is added and fetched, and one that is unreachable, or whose tip
// is neither the cloned tip nor an ancestor of it, refuses. A mirror merely
// behind warns. With nothing recorded, <url> is origin only.
func wireCloneRemotes(scratch, rawURL, branch, tip string, recorded []RecordedRemote) ([]CloneRemote, []string, error) {
	if len(recorded) == 0 {
		return []CloneRemote{{Name: "origin", URL: redactURL(rawURL), State: "cloned"}}, nil, nil
	}
	want, ok := normaliseRemoteURL(rawURL)
	idx := -1
	for i, r := range recorded {
		if n, nok := normaliseRemoteURL(r.URL); ok && nok && n == want {
			idx = i
			break
		}
	}
	if idx < 0 {
		var names []string
		for _, r := range recorded {
			names = append(names, r.Name+"="+redactURL(r.URL))
		}
		return nil, nil, fmt.Errorf("refusing to clone: %w: %s is none of %s (%s); clone from one of them",
			ErrCloneRemoteUnknown, redactURL(rawURL), remotesFile, strings.Join(names, ", "))
	}
	self := recorded[idx]
	if self.Name != "origin" {
		if _, err := gitCmd(scratch, 10*time.Second, "remote", "rename", "origin", self.Name); err != nil {
			return nil, nil, fmt.Errorf("name the clone's remote %s: %w", self.Name, err)
		}
	}
	out := []CloneRemote{{Name: self.Name, URL: redactURL(self.URL), State: "cloned"}}
	var warnings []string
	for i, r := range recorded {
		if i == idx {
			continue
		}
		if _, err := gitCmd(scratch, 10*time.Second, "remote", "add", r.Name, r.URL); err != nil {
			return nil, nil, fmt.Errorf("add the remote %s: %w", r.Name, err)
		}
		cr, warn, err := checkCloneMirror(scratch, r, branch, tip)
		if err != nil {
			return nil, nil, fmt.Errorf("refusing to clone: %w: %s", ErrCloneMirror, err.Error())
		}
		out = append(out, cr)
		if warn != "" {
			warnings = append(warnings, warn)
		}
	}
	return out, warnings, nil
}

// checkCloneMirror is the mirror check for the configured remote r of repo: it
// is read live and fetched, and its tip must be tip (in sync) or an ancestor
// of it (behind, a warning); unreachable, branchless or diverged is an error.
func checkCloneMirror(repo string, r RecordedRemote, branch, tip string) (CloneRemote, string, error) {
	mtip, err := liveTip(repo, r.Name, branch)
	if err != nil {
		return CloneRemote{}, "", fmt.Errorf("%s (%s) cannot be read: %s", r.Name, redactURL(r.URL), err.Error())
	}
	if _, err := lifecycleGit(repo, lifecycleNetTimeout, "fetch", "--quiet", "--no-tags", r.Name); err != nil {
		return CloneRemote{}, "", fmt.Errorf("fetch %s (%s): %s", r.Name, redactURL(r.URL), err.Error())
	}
	switch {
	case mtip == tip:
		return CloneRemote{Name: r.Name, URL: redactURL(r.URL), State: "in sync"}, "", nil
	case mtip == "":
		return CloneRemote{}, "", fmt.Errorf("mirror %s has no %s", r.Name, branch)
	}
	behind, err := tipContains(repo, mtip, tip)
	if err != nil || !behind {
		return CloneRemote{}, "", fmt.Errorf("mirror %s is at %s, which is neither the cloned tip %s nor behind it; "+
			"the vault's remotes have diverged, an operator matter", r.Name, shortSHA(mtip), shortSHA(tip))
	}
	return CloneRemote{Name: r.Name, URL: redactURL(r.URL), State: "behind"},
		fmt.Sprintf("mirror %s is behind the cloned tip (%s is an ancestor of %s)", r.Name, shortSHA(mtip), shortSHA(tip)), nil
}

// cloneDigest binds the recorded remotes, the bind set, the host config's
// sha256 and the default vault's record bytes for each bound project. It
// leaves out the remote tip on purpose: the bind rules re-run against
// whatever the real run clones.
func cloneDigest(req CloneRequest, path string, remotes []CloneRemote, cfg []byte) string {
	h := sha256.New()
	line := func(fields ...string) { fmt.Fprintf(h, "%s\n", strings.Join(fields, "\x00")) }
	line(cloneDigestFormat)
	line("path", path)
	for _, r := range remotes {
		n, ok := normaliseRemoteURL(r.URL)
		if !ok {
			n = r.URL
		}
		line("remote", r.Name, n)
	}
	line("bind", strings.Join(req.Bind, " "))
	if len(req.Bind) > 0 {
		sum := sha256.Sum256(cfg)
		line("config", hex.EncodeToString(sum[:]))
		global, _, _ := ResolveGlobalVaultPath()
		for _, s := range req.Bind {
			b, _ := os.ReadFile(filepath.Join(global, filepath.FromSlash(departure.RelPath(s))))
			sum := sha256.Sum256(b)
			line("record", s, hex.EncodeToString(sum[:]))
		}
	}
	return "v1:" + hex.EncodeToString(h.Sum(nil))
}

// cloneCommandLine is the real-run command, --expect filled in.
func cloneCommandLine(req CloneRequest, path, digest string) string {
	// The URL as given (CloneVault refused any credential in it): the line is
	// for pasting, so it must run as printed; every word is shell-quoted.
	parts := []string{"vp", "vault", "clone", shellQuote(req.URL), shellQuote(path)}
	if len(req.Bind) > 0 {
		parts = append(parts, "--bind")
		for _, b := range req.Bind {
			parts = append(parts, shellQuote(b))
		}
	}
	return strings.Join(append(parts, "--expect", digest), " ")
}
