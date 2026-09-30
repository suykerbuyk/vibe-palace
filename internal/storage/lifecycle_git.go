// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// Git plumbing for the project lifecycle commands (vault copy, vault project
// delete, vault rename, vault init, vault clone): every network call they
// make, the private snapshot of another vault's published remote, the
// all-ancestor trailer search and the footprint hash F.
//
// Design: task lifecycle-commands-design-plan, § Context (terms), § Command
// surface, § Copy, § Delete and unit U3. This file has no CLI or MCP surface;
// the command units call it.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
)

// lifecycleNetTimeout is the limit on every small network git call the
// lifecycle commands make, most of them while they hold the vault's root lock:
// ls-remote, the per-remote fetch of a live tip, and the publish push of init
// and delete, whose commits carry next to nothing.
//
// What a limit here does, and does not: at the deadline the `git` process is
// killed, and only it. A child that holds the output pipe (ssh, above all)
// lives on, and the call returns when that child exits. So a push over a slow
// or stalled link is NOT cut at the limit, and a committer blocked on the
// vault's lock does not wait a bounded time; the limit only bounds a git that
// is itself stuck.
const lifecycleNetTimeout = 30 * time.Second

// lifecycleBulkTimeout is the one limit on every lifecycle step that moves a
// project's worth of data: the snapshot fetch, the blob fetch and the
// footprint checkout (all before the lock is taken), `vp vault clone`'s clone,
// and the copy's `git add`, `git commit` and publish push (under the receiving
// vault's lock). A project here is a few thousand files and a few hundred MiB,
// and the link speed is not ours to know: thirty minutes is about 160 MiB at
// 0.1 MiB/s. The same caveat as lifecycleNetTimeout applies to what the
// deadline kills.
const lifecycleBulkTimeout = 30 * time.Minute

// footprintHashVersion tags F. A later change to ClassifyProjectPath gets a new
// version, so trailers written under an older one are read under their own.
const footprintHashVersion = "v1"

// ErrRemoteHasNoHead is returned for a remote with no HEAD: an empty
// repository, so not a vault yet.
var ErrRemoteHasNoHead = errors.New("remote has no HEAD")

// lifecycleGitExtra is what a lifecycle git call in dir adds to SafeGitEnv:
// no prompts, and ssh BatchMode composed onto (never in place of) the
// operator's own ssh command. It returns the extra environment and the
// leading `-c` arguments. Every exec site builds cmd.Env as
// SafeGitEnv(extra...), so the repo-local-variable stripping is never skipped.
func lifecycleGitExtra(dir string) (extra []string, pre []string) {
	var core string
	if dir != "" {
		// core.sshCommand as git would see it here, with the stripped
		// environment, so an inherited GIT_DIR cannot answer for another repo.
		cmd := exec.Command("git", "config", "--get", "core.sshCommand")
		cmd.Dir = dir
		cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0")
		if out, err := cmd.Output(); err == nil {
			core = strings.TrimSpace(string(out))
		}
	}
	override, pre := gitenv.SSHBatchModeOverride(os.Environ(), core)
	return append([]string{"GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true"}, override...), pre
}

// lifecycleGit runs one git command in dir under the lifecycle environment,
// with timeout. Output is trimmed; a failure carries git's own message, as
// gitCmd's does.
func lifecycleGit(dir string, timeout time.Duration, args ...string) (string, error) {
	return lifecycleGitWith(dir, timeout, nil, nil, args...)
}

// lifecycleGitWith is lifecycleGit with a standard input and with env added to
// the lifecycle environment; either may be nil.
func lifecycleGitWith(dir string, timeout time.Duration, stdin io.Reader, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	extra, pre := lifecycleGitExtra(dir)
	cmd := exec.CommandContext(ctx, "git", append(pre, args...)...)
	cmd.Dir = dir
	cmd.Env = SafeGitEnv(append(extra, env...)...)
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("timed out after %s: %w", timeout, ctx.Err())
		}
		return trimmed, &GitError{Detail: gitDetailLine(trimmed), Err: err}
	}
	return trimmed, nil
}

// withCredentialHint adds the credential fix to a failed network read, and
// names the one case BatchMode cannot cover: GIT_SSH (a program, not a
// command line) set without GIT_SSH_COMMAND, where vp adds no BatchMode, so an
// ssh prompt can only wait out the timeout. limit is the limit the failed call
// ran under, so the hint names the wait that actually happened.
func withCredentialHint(err error, limit time.Duration) error {
	if err == nil {
		return nil
	}
	hint := "if this is a credential failure, export GIT_SSH_COMMAND='ssh -i <key> -o IdentitiesOnly=yes -o IdentityAgent=none' and re-run"
	if os.Getenv("GIT_SSH") != "" && os.Getenv("GIT_SSH_COMMAND") == "" {
		hint = "GIT_SSH is set, so vp could not add ssh BatchMode and an ssh prompt may have waited out the " +
			limit.String() + " timeout unanswered; " + hint + " (GIT_SSH_COMMAND takes precedence over GIT_SSH)"
	}
	return fmt.Errorf("%w; %s", err, hint)
}

// remoteHead is what `ls-remote --symref <url> HEAD` says: the default branch
// and its tip.
type remoteHead struct {
	Branch string
	Tip    string
}

// lsRemoteHead resolves url's default branch and tip in one network call. A
// plain `ls-remote <url> HEAD` returns only a sha, so --symref is required to
// learn the branch.
func lsRemoteHead(url string) (remoteHead, error) {
	// A neutral directory, never the process cwd: ls-remote needs no
	// repository, and a cwd inside one would lend it that repo's config.
	out, err := lifecycleGit(os.TempDir(), lifecycleNetTimeout, "ls-remote", "--symref", "--", url, "HEAD")
	if err != nil {
		return remoteHead{}, withCredentialHint(fmt.Errorf("read %s: %w", url, err), lifecycleNetTimeout)
	}
	var h remoteHead
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD":
			h.Branch = strings.TrimPrefix(fields[1], "refs/heads/")
		case len(fields) == 2 && fields[1] == "HEAD":
			h.Tip = fields[0]
		}
	}
	if h.Tip == "" || h.Branch == "" {
		return remoteHead{}, fmt.Errorf("%s: %w: not a vault yet (its first push has not happened)", url, ErrRemoteHasNoHead)
	}
	return h, nil
}

// remoteSnapshot is a private, blobless copy of another vault's published
// branch: a bare repository under the user cache dir, inside no vault, that
// holds the commit graph and trees and no file contents. A caller that reads
// file contents (a size, a `show`, a checkout) calls fetchBlobs first: git
// serves a missing blob by fetching it in a subprocess of its own, one
// connection per file, which a project of a few thousand files does not
// survive.
type remoteSnapshot struct {
	Dir    string // the bare repository
	URL    string
	Branch string
	Tip    string // the fetched tip of Branch
	root   string // the per-snapshot scratch root Close removes
}

// snapshotRef is where the fetched tip lives inside the snapshot.
const snapshotRef = "refs/vp-snapshot/tip"

// newRemoteSnapshot blobless-fetches url's branch into a fresh scratch
// directory under os.UserCacheDir()/vibe-palace/snap/. The caller must Close
// it. A server without filter support does a full fetch instead, which is
// correct, only slower.
func newRemoteSnapshot(url, branch string) (*remoteSnapshot, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("locate the user cache dir for a remote snapshot: %w", err)
	}
	parent := filepath.Join(cache, "vibe-palace", "snap")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}
	root, err := os.MkdirTemp(parent, "snap-")
	if err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}
	s := &remoteSnapshot{Dir: filepath.Join(root, "repo.git"), URL: url, Branch: branch, root: root}
	if _, err := lifecycleGit(root, lifecycleNetTimeout, "init", "--bare", "-q", s.Dir); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("init snapshot: %w", err)
	}
	if _, err := lifecycleGit(s.Dir, lifecycleBulkTimeout, "fetch", "--filter=blob:none", "--no-tags", "--quiet",
		url, "+refs/heads/"+branch+":"+snapshotRef); err != nil {
		_ = s.Close()
		return nil, withCredentialHint(fmt.Errorf("fetch %s %s: %w", url, branch, err), lifecycleBulkTimeout)
	}
	tip, err := gitCmd(s.Dir, 10*time.Second, "rev-parse", "--verify", snapshotRef+"^{commit}")
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("read snapshot tip: %w", err)
	}
	s.Tip = tip
	return s, nil
}

// snapshotNoLazyFetch forbids git to fetch a missing object on its own. Every
// read of file contents in a snapshot runs with it, whether or not fetchBlobs
// has run: a blob that is missing then makes `show` and the checkout fail, and
// makes the -l listing print BAD for its size, instead of starting a fetch per
// file. git honours it from 2.45.0 (and in the May 2024 maintenance releases
// of older series); a git that does not know it ignores it and fetches lazily,
// which is slow and still correct.
const snapshotNoLazyFetch = "GIT_NO_LAZY_FETCH=1"

// git runs one git command in the snapshot under the lifecycle environment,
// with lazy fetching forbidden.
func (s *remoteSnapshot) git(timeout time.Duration, args ...string) (string, error) {
	return lifecycleGitWith(s.Dir, timeout, nil, []string{snapshotNoLazyFetch}, args...)
}

// readFile returns the content of the file rel at commit. Whether the file
// exists is decided from the tree, which a snapshot always holds: found is
// false only when commit has no entry at rel. A file the tree lists and git
// cannot read (its blob was not fetched, the object is corrupt) is an error
// that names it, never "absent": `git show <commit>:<rel>` exits 128 for both,
// and reading the failure as absence would turn a missing blob into "this is
// not a vault" or into a departure record that was never looked at. Absent
// means the listing exited 0 and printed nothing; a listing that fails is
// returned as an error too, or the same swallow would only have moved one call
// earlier.
func (s *remoteSnapshot) readFile(commit, rel string) (content string, found bool, err error) {
	if _, found, err = treeEntryOID(s.Dir, commit, rel); err != nil || !found {
		return "", false, err
	}
	content, err = s.git(lifecycleBulkTimeout, "show", commit+":"+rel)
	if err != nil {
		return "", true, fmt.Errorf("read %s at %s from the snapshot of %s: %w", rel, shortSHA(commit), s.URL, err)
	}
	return content, true, nil
}

// fetchBlobs brings every blob under paths at commit into the snapshot in ONE
// request, then proves they arrived. paths are trees or files; one that commit
// does not hold contributes nothing.
//
// The request is the one git makes for its own lazy fetch, with every id at
// once: `-c fetch.negotiationAlgorithm=noop` keeps git from sending `have`
// lines, so the server sees wants only, and a want needs nothing but the
// server's filter support (protocol v2 allows a want for any object).
func (s *remoteSnapshot) fetchBlobs(commit string, paths []string) error {
	out, err := gitCmd(s.Dir, 60*time.Second, append([]string{"ls-tree", "-r", "-z", "--full-tree", commit, "--"}, paths...)...)
	if err != nil {
		return fmt.Errorf("list the files to fetch at %s: %w", shortSHA(commit), err)
	}
	seen := map[string]bool{}
	var ids []string
	for rec := range strings.SplitSeq(out, "\x00") {
		meta, _, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta) // mode type oid
		// A symlink is a blob and is fetched (the copy refuses it afterwards); a
		// submodule is a commit the remote does not hold, and is left out.
		if !ok || len(f) != 3 || f[1] != "blob" || seen[f[2]] {
			continue
		}
		seen[f[2]] = true
		ids = append(ids, f[2])
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := lifecycleGitWith(s.Dir, lifecycleBulkTimeout, strings.NewReader(strings.Join(ids, "\n")+"\n"), nil,
		"-c", "fetch.negotiationAlgorithm=noop", "fetch", "--no-tags", "--no-write-fetch-head",
		"--recurse-submodules=no", "--filter=blob:none", "--quiet", "--stdin", s.URL); err != nil {
		return withCredentialHint(fmt.Errorf("fetch the %d file(s) under %s from %s: %w", len(ids), strings.Join(paths, ", "), s.URL, err), lifecycleBulkTimeout)
	}
	return s.requireComplete(commit, paths)
}

// requireComplete refuses, naming the count, unless the snapshot holds every
// object under paths at commit.
func (s *remoteSnapshot) requireComplete(commit string, paths []string) error {
	missing, err := s.missingObjects(commit, paths)
	if err != nil {
		return err
	}
	if missing > 0 {
		return fmt.Errorf("the snapshot of %s lacks %d object(s) under %s at %s: the remote did not send everything it was asked for",
			s.URL, missing, strings.Join(paths, ", "), shortSHA(commit))
	}
	return nil
}

// missingObjects counts the objects under paths at commit that the snapshot
// does not hold. It asks `rev-list --objects --no-walk --missing=print` about
// the tree or blob of each path commit holds, passed as <commit>:<path> and
// never as a pathspec: a pathspec makes rev-list walk history (and report
// every older version of every file, which nobody fetched), and with --no-walk
// it drops a commit that does not touch the paths and lists nothing. The
// command fetches nothing.
//
// What this rests on: --missing is documented as a debug option for partial
// clone, and a missing blob given as a starting point (a single file's path)
// is reported as `?<id>` only from git 2.45.0, the release GIT_NO_LAZY_FETCH
// arrived in. It was tested on git 2.47.3. On an older git a missing tree's
// contents are still reported through the tree; what a missing single file
// does there is not known (an error, which fails closed, or a fetch of that
// one blob).
func (s *remoteSnapshot) missingObjects(commit string, paths []string) (int, error) {
	out, err := gitCmd(s.Dir, 30*time.Second, append([]string{"ls-tree", "-z", "--full-tree", commit, "--"}, paths...)...)
	if err != nil {
		return 0, fmt.Errorf("list %s at %s: %w", strings.Join(paths, ", "), shortSHA(commit), err)
	}
	args := []string{"rev-list", "--objects", "--no-walk", "--missing=print"}
	held := 0
	for rec := range strings.SplitSeq(out, "\x00") {
		meta, rel, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[1] == "commit" {
			continue
		}
		args = append(args, commit+":"+rel)
		held++
	}
	if held == 0 {
		return 0, nil
	}
	listed, err := gitCmd(s.Dir, 60*time.Second, args...)
	if err != nil {
		return 0, fmt.Errorf("check the snapshot holds %s at %s: %w", strings.Join(paths, ", "), shortSHA(commit), err)
	}
	missing := 0
	for line := range strings.SplitSeq(listed, "\n") {
		if strings.HasPrefix(line, "?") {
			missing++
		}
	}
	return missing, nil
}

// Close removes the snapshot and everything checked out from it.
func (s *remoteSnapshot) Close() error {
	if s == nil || s.root == "" {
		return nil
	}
	return os.RemoveAll(s.root)
}

// checkoutFootprint checks out only Projects/<p> and palace/<p> of commit, for
// each slug, into a fresh directory inside the snapshot, and returns it. The
// directory is a fresh checkout, so every file in it is tracked: a disk walk
// over it sees exactly the committed footprint and nothing else. The caller
// has run fetchBlobs over the same trees: the checkout fetches nothing, and a
// blob the snapshot lacks fails it.
func (s *remoteSnapshot) checkoutFootprint(commit string, slugs []string) (string, error) {
	wt, err := os.MkdirTemp(s.root, "wt-")
	if err != nil {
		return "", fmt.Errorf("create footprint checkout: %w", err)
	}
	var paths []string
	for _, slug := range slugs {
		for _, tree := range ProjectTrees(slug) {
			// A pathspec that matches nothing makes checkout fail, and a
			// Projects-only project (no palace/ tree) is legal.
			out, err := gitCmd(s.Dir, 30*time.Second, "ls-tree", "--name-only", commit, "--", tree)
			if err != nil {
				return "", fmt.Errorf("list %s at %s: %w", tree, commit, err)
			}
			if out != "" {
				paths = append(paths, tree)
			}
		}
	}
	if len(paths) == 0 {
		return wt, nil
	}
	index := filepath.Join(s.root, filepath.Base(wt)+".index")
	args := append([]string{"--work-tree=" + wt, "checkout", commit, "--"}, paths...)
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleBulkTimeout)
	defer cancel()
	extra, pre := lifecycleGitExtra(s.Dir)
	cmd := exec.CommandContext(ctx, "git", append(pre, args...)...)
	cmd.Dir = s.Dir
	cmd.Env = SafeGitEnv(append(extra, "GIT_INDEX_FILE="+index, snapshotNoLazyFetch)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("check out the footprint of %s: %s: %w", commit, strings.TrimSpace(string(out)), err)
	}
	return wt, nil
}

// trailerCommit is one commit and its parsed trailers.
type trailerCommit struct {
	SHA      string
	Trailers map[string][]string
}

// commitsWithTrailer returns every ancestor of tip, INCLUDING commits off the
// first-parent line, that carries at least one trailer named key, newest first.
// `vp vault pull` merges with a plain `git merge`, so a copy commit can sit on
// a merge's second parent; a first-parent walk would miss it. Safety comes
// from the caller's footprint equalities, not from topology.
func commitsWithTrailer(repo, tip, key string) ([]trailerCommit, error) {
	const recSep, fieldSep = "\x1e", "\x1f"
	out, err := gitCmd(repo, 60*time.Second, "-c", "log.showSignature=false", "log", "--no-color",
		"--format=%H"+fieldSep+"%(trailers:only,unfold)"+recSep, tip)
	if err != nil {
		return nil, fmt.Errorf("walk the ancestors of %s: %w", tip, err)
	}
	var res []trailerCommit
	for rec := range strings.SplitSeq(out, recSep) {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		sha, body, _ := strings.Cut(rec, fieldSep)
		tr := parseTrailerBlock(body)
		if len(tr[key]) > 0 {
			res = append(res, trailerCommit{SHA: strings.TrimSpace(sha), Trailers: tr})
		}
	}
	return res, nil
}

// parseTrailerBlock parses git's `%(trailers:only,unfold)` output: one
// `Key: value` per line.
func parseTrailerBlock(body string) map[string][]string {
	tr := map[string][]string{}
	for line := range strings.SplitSeq(body, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, " \t") {
			continue
		}
		tr[k] = append(tr[k], strings.TrimSpace(v))
	}
	return tr
}

// footprintHash is F(commit, slug): sha256 over the sorted `mode oid path`
// lines of `git ls-tree -r <commit> -- Projects/<slug> palace/<slug>`, keeping
// only what ClassifyProjectPath calls project content. It reads object ids
// only, so a blobless snapshot is enough, and it is equal in any vault holding
// the same bytes at the same paths; a rebase changes shas but not blob oids,
// so F survives one. contentFiles lets a caller refuse an empty footprint,
// whose F would otherwise equal an absent project's.
func footprintHash(repo, commit, slug string) (hash string, contentFiles int, err error) {
	args := append([]string{"ls-tree", "-r", "-z", "--full-tree", commit, "--"}, ProjectTrees(slug)...)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repo
	cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0")
	raw, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", 0, fmt.Errorf("list the footprint of %s at %s: %s: %w", slug, commit, strings.TrimSpace(string(ee.Stderr)), err)
		}
		return "", 0, fmt.Errorf("list the footprint of %s at %s: %w", slug, commit, err)
	}
	var lines []string
	for rec := range strings.SplitSeq(string(raw), "\x00") {
		if rec == "" {
			continue
		}
		meta, rel, ok := strings.Cut(rec, "\t")
		if !ok {
			return "", 0, fmt.Errorf("unparseable ls-tree record %q", rec)
		}
		f := strings.Fields(meta) // mode type oid
		if len(f) != 3 {
			return "", 0, fmt.Errorf("unparseable ls-tree record %q", rec)
		}
		if ClassifyProjectPath(rel) != ProjectContent {
			continue
		}
		lines = append(lines, f[0]+" "+f[2]+" "+rel+"\n")
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return footprintHashVersion + ":" + hex.EncodeToString(h.Sum(nil)), len(lines), nil
}
