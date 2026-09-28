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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
)

// lifecycleNetTimeout bounds every network git call the lifecycle commands
// make while they may hold the vault's root lock (ls-remote, the per-remote
// fetch of a live tip, push). A blocked committer on the host therefore waits
// a bounded time per remote.
const lifecycleNetTimeout = 30 * time.Second

// lifecycleSnapshotTimeout bounds the blobless snapshot fetch and the
// footprint checkout. Those run BEFORE the lock is taken and move the most
// data (the live personal vault's commit graph is ~16 MiB of trees).
const lifecycleSnapshotTimeout = 5 * time.Minute

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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	extra, pre := lifecycleGitExtra(dir)
	cmd := exec.CommandContext(ctx, "git", append(pre, args...)...)
	cmd.Dir = dir
	cmd.Env = SafeGitEnv(extra...)
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
// ssh prompt can only wait out the timeout.
func withCredentialHint(err error) error {
	if err == nil {
		return nil
	}
	hint := "if this is a credential failure, export GIT_SSH_COMMAND='ssh -i <key> -o IdentitiesOnly=yes -o IdentityAgent=none' and re-run"
	if os.Getenv("GIT_SSH") != "" && os.Getenv("GIT_SSH_COMMAND") == "" {
		hint = "GIT_SSH is set, so vp could not add ssh BatchMode and an ssh prompt may have waited out the " +
			lifecycleNetTimeout.String() + " timeout unanswered; " + hint + " (GIT_SSH_COMMAND takes precedence over GIT_SSH)"
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
		return remoteHead{}, withCredentialHint(fmt.Errorf("read %s: %w", url, err))
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
// holds the commit graph and trees and fetches blobs lazily on checkout.
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
	if _, err := lifecycleGit(s.Dir, lifecycleSnapshotTimeout, "fetch", "--filter=blob:none", "--no-tags", "--quiet",
		url, "+refs/heads/"+branch+":"+snapshotRef); err != nil {
		_ = s.Close()
		return nil, withCredentialHint(fmt.Errorf("fetch %s %s: %w", url, branch, err))
	}
	tip, err := gitCmd(s.Dir, 10*time.Second, "rev-parse", "--verify", snapshotRef+"^{commit}")
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("read snapshot tip: %w", err)
	}
	s.Tip = tip
	return s, nil
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
// over it sees exactly the committed footprint and nothing else.
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
	ctx, cancel := context.WithTimeout(context.Background(), lifecycleSnapshotTimeout)
	defer cancel()
	extra, pre := lifecycleGitExtra(s.Dir)
	cmd := exec.CommandContext(ctx, "git", append(pre, args...)...)
	cmd.Dir = s.Dir
	cmd.Env = SafeGitEnv(append(extra, "GIT_INDEX_FILE="+index)...)
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
