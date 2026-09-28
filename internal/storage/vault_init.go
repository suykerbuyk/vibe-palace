// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// InitVault is `vp vault init <path> --remote name=url...`: create a NEW, empty
// vault at a path that does not exist, record its remotes in the tracked
// .vibe-palace/remotes.toml, make one commit on main and publish it to every
// remote with exact publish, the first push setting upstream. It is vault-only:
// unlike `vp init` it onboards no project and writes no host config.
//
// A run that dies part-way is finished by re-running the same command: the
// init marker, written right after the scaffold, is how the re-run knows the
// path is its own unfinished vault rather than somebody's directory.
//
// Design: task lifecycle-commands-design-plan, § Command surface, § Clone ›
// Secondary remotes, § Admin procedure step 1, and unit U4.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// initMarkerCommand names vault init in its pending marker. It is the one
// command whose marker has no parent: its commit is the vault's root commit.
const initMarkerCommand = "vault init"

// RecordedRemote is one entry of .vibe-palace/remotes.toml.
type RecordedRemote struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func (r RecordedRemote) spec() string { return r.Name + "=" + r.URL }

// InitVaultRequest is what `vp vault init` asks for.
type InitVaultRequest struct {
	// Path is the new vault's absolute path, after ~ expansion.
	Path string
	// Remotes are the vault's remotes in publish order; the first is the
	// upstream.
	Remotes []RecordedRemote
	DryRun  bool
	// Scaffold creates the vault at dest: reconcile.ScaffoldNewVault, the one
	// scaffold that stamps the data format. It is passed in because
	// internal/reconcile imports this package.
	Scaffold func(ctx context.Context, dest string) error
}

// InitVaultReport is what InitVault did, or would do under DryRun.
type InitVaultReport struct {
	Path     string           `json:"path"`
	Branch   string           `json:"branch"`
	Remotes  []RecordedRemote `json:"remotes"`
	Upstream string           `json:"upstream"`
	Commit   string           `json:"commit,omitempty"`
	Files    []string         `json:"files,omitempty"`
	DryRun   bool             `json:"dry_run"`
	// Resumed is set when this run finished an earlier, interrupted init.
	Resumed bool `json:"resumed,omitempty"`
	// PublishedTo are the remotes whose live tip holds the commit.
	PublishedTo []string `json:"published_to,omitempty"`
	Undo        []string `json:"undo,omitempty"`
}

// Refusals of InitVault, each its own sentinel so a caller (and a test) can
// tell them apart.
var (
	ErrInitPathExists      = errors.New("the path already exists")
	ErrInitNested          = errors.New("the path is inside a vault or a git work tree")
	ErrInitRemoteNotEmpty  = errors.New("the remote is not empty")
	ErrInitRemoteBad       = errors.New("invalid --remote")
	ErrInitNoRemote        = errors.New("vp vault init needs at least one --remote")
	ErrInitRemoteUnreached = errors.New("the remote cannot be reached")
)

// remoteNameRE is a conservative git remote name: what `git remote add`
// accepts without surprises in a refspec.
var remoteNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const initBranch = "main"

// Test seams, no-ops in production: initAfterMarker runs right after the init
// marker is written, initAfterCommit after the commit is recorded in it and
// before the publish. A seam that returns an error stops the run on the spot,
// as a killed process would: nothing is cleaned up.
var (
	initAfterMarker = func() error { return nil }
	initAfterCommit = func() error { return nil }
)

// InitVault creates, commits and publishes a new vault — or, over the path of
// its own interrupted run, finishes it. See the file comment.
func InitVault(ctx context.Context, req InitVaultRequest) (*InitVaultReport, error) {
	path, err := initAbsPath(req.Path)
	if err != nil {
		return nil, err
	}
	if err := initCheckRemotes(req.Remotes); err != nil {
		return nil, err
	}
	if err := RefuseIfGitDisabled(path, "initialise a new vault"); err != nil {
		return nil, err
	}
	// Lstat, not Stat: a dangling symlink exists as far as creating a vault
	// there is concerned.
	if _, err := os.Lstat(path); err == nil {
		return initResume(ctx, path, req)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("vault init: %s: %w", path, err)
	}
	return initFresh(ctx, path, req)
}

// initFresh is a first run over a path that does not exist.
func initFresh(ctx context.Context, path string, req InitVaultRequest) (*InitVaultReport, error) {
	if err := initCheckParent(path); err != nil {
		return nil, err
	}
	if err := initCheckRemotesEmpty(req.Remotes); err != nil {
		return nil, err
	}
	rep := initReport(path, req)
	if req.DryRun {
		return rep, nil
	}
	if req.Scaffold == nil {
		return nil, fmt.Errorf("vault init: no scaffold")
	}

	// From here on the path is ours: it did not exist a moment ago. Any failure
	// before a remote holds the commit removes it again.
	removeNew := func() { _ = os.RemoveAll(path) }
	if err := req.Scaffold(ctx, path); err != nil {
		removeNew()
		return nil, fmt.Errorf("vault init: %w", err)
	}
	held, err := vaultlock.AcquireHeld(path, path)
	if err != nil {
		removeNew()
		return nil, fmt.Errorf("vault init: take the vault lock: %w", err)
	}
	defer held.Release()

	// The marker, right after the scaffold and before any other write, is what
	// lets a re-run recognise and finish (or remove) this vault.
	runID := make([]byte, 8)
	_, _ = rand.Read(runID)
	specs := make([]string, len(req.Remotes))
	for i, r := range req.Remotes {
		specs[i] = r.spec()
	}
	m := lifecycleMarker{
		Command: initMarkerCommand, RunID: "init-" + hex.EncodeToString(runID), Parent: "",
		Remotes: specs, Rerun: initRerun(path, req.Remotes),
	}
	if err := writeLifecycleMarker(held, m); err != nil {
		removeNew()
		return nil, fmt.Errorf("vault init: write the init marker: %w", err)
	}
	if err := initAfterMarker(); err != nil {
		return nil, err
	}

	// `git init -b main`: the scaffold's `git init` follows the host's
	// init.defaultBranch, so point the still-unborn HEAD at main before the
	// first commit.
	if _, err := gitCmd(path, 10*time.Second, "symbolic-ref", "HEAD", "refs/heads/"+initBranch); err != nil {
		removeNew()
		return nil, fmt.Errorf("vault init: set branch %s: %w", initBranch, err)
	}
	if f, ferr := surface.ReadFormat(path); ferr != nil || f != surface.RequiredDataFormat {
		removeNew()
		return nil, fmt.Errorf("vault init: the scaffold did not stamp data format %d (got %d, %v)", surface.RequiredDataFormat, f, ferr)
	}
	// The new vault is the vault root.
	vaultRoot := path
	if err := atomicfile.Write(vaultRoot, filepath.Join(vaultRoot, filepath.FromSlash(remotesFile)), []byte(renderRemotesFile(req.Remotes))); err != nil {
		removeNew()
		return nil, fmt.Errorf("vault init: write %s: %w", remotesFile, err)
	}
	for _, r := range req.Remotes {
		if _, err := gitCmd(path, 10*time.Second, "remote", "add", r.Name, r.URL); err != nil {
			removeNew()
			return nil, fmt.Errorf("vault init: add remote %s: %s", r.Name, redactURLs(err.Error(), req.Remotes))
		}
	}
	names := make([]string, len(req.Remotes))
	for i, r := range req.Remotes {
		names[i] = r.Name
	}
	msg := fmt.Sprintf("vault init: %s\n\nRemotes: %s", filepath.Base(path), strings.Join(names, ", "))
	if _, err := commitPathsLocked(held, msg, lifecycleRunTrailerLine(m.RunID), rep.Files); err != nil {
		removeNew()
		return nil, fmt.Errorf("vault init: commit: %w", err)
	}
	sha, err := gitCmd(path, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		removeNew()
		return nil, fmt.Errorf("vault init: read the commit: %w", err)
	}
	// The commit is recorded BEFORE any push: a marker without a commit
	// therefore always means nothing was published.
	if err := setLifecycleMarkerCommit(held, m.RunID, sha); err != nil {
		removeNew()
		return nil, fmt.Errorf("vault init: record the commit in the marker: %w", err)
	}
	m.Commit = sha
	rep.Commit = sha
	if err := initAfterCommit(); err != nil {
		return nil, err
	}
	return initPublish(held, rep, m, req.Remotes, false)
}

// initResume is a re-run over an existing path. It proceeds only when the path
// holds this command's own unfinished init, with the same remotes; anything
// else is ErrInitPathExists.
func initResume(ctx context.Context, path string, req InitVaultRequest) (*InitVaultReport, error) {
	exists := func(why string) error {
		return fmt.Errorf("%w: %s (%s). vp vault init creates a new vault and never adopts or overwrites an existing directory, empty or not",
			ErrInitPathExists, path, why)
	}
	st, err := os.Lstat(path)
	if err != nil || !st.IsDir() {
		return nil, exists("not a directory")
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		return nil, exists("not a git repository")
	}
	m, found, err := readLifecycleMarker(path)
	if !found || err != nil {
		return nil, exists("it holds no unfinished vault init; if it is a vault init that died right after its scaffold, it holds no commit and can be removed")
	}
	specs := make([]string, len(req.Remotes))
	for i, r := range req.Remotes {
		specs[i] = r.spec()
	}
	if m.Command != initMarkerCommand || !slices.Equal(m.Remotes, specs) {
		return nil, exists("it holds an unfinished " + m.Command + " with different remotes; re-run it with the remotes it was started with")
	}
	rep := initReport(path, req)
	rep.Resumed = true
	rep.Commit = m.Commit
	if req.DryRun {
		return rep, nil
	}
	held, err := vaultlock.AcquireHeld(path, path)
	if err != nil {
		return nil, fmt.Errorf("vault init: take the vault lock: %w", err)
	}
	restart := func() (*InitVaultReport, error) {
		_ = held.Release()
		if err := os.RemoveAll(path); err != nil {
			return nil, fmt.Errorf("vault init: remove the unpublished %s: %w", path, err)
		}
		return initFresh(ctx, path, req)
	}
	// No commit recorded: the commit is recorded before any push, so nothing
	// was published. Start over.
	if m.Commit == "" {
		return restart()
	}
	defer held.Release()
	head, err := gitCmd(path, 10*time.Second, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || head != m.Commit {
		return nil, fmt.Errorf("vault init: HEAD of %s is %q, not the unfinished run's %s; it is not safe to finish or remove automatically", path, head, shortSHA(m.Commit))
	}
	order, err := lifecycleRemoteOrder(path)
	if err != nil {
		return nil, fmt.Errorf("vault init: %w", err)
	}
	var has []string
	for _, r := range order {
		tip, err := liveTip(path, r, initBranch)
		if err != nil {
			return nil, fmt.Errorf("vault init: %s", redactURLs(err.Error(), req.Remotes))
		}
		ok, err := tipContains(path, m.Commit, tip)
		if err != nil {
			return nil, fmt.Errorf("vault init: %w", err)
		}
		switch {
		case ok:
			has = append(has, r)
		case tip != "":
			return nil, fmt.Errorf("%w: %s gained other history since the interrupted init; it is not safe to finish", ErrInitRemoteNotEmpty, r)
		}
	}
	if len(has) == 0 {
		return restart()
	}
	return initPublish(held, rep, m, req.Remotes, true)
}

// initPublish exact-publishes the init commit, then sets upstream.
// publishedElsewhere says a remote already holds it (a resumed run).
func initPublish(held *vaultlock.Held, rep *InitVaultReport, m lifecycleMarker, remotes []RecordedRemote, publishedElsewhere bool) (*InitVaultReport, error) {
	path := held.Root()
	order, err := lifecycleRemoteOrder(path)
	if err != nil {
		return nil, fmt.Errorf("vault init: %w", err)
	}
	if perr := exactPublish(held, initBranch, order, m, nil, publishedElsewhere); perr != nil {
		// Decide from the live remotes, never from the error: if no remote
		// holds the commit, nothing was published and the new vault goes; if
		// one does, the vault stays and the same command finishes it.
		rep.PublishedTo = initPublishedTo(path, order, m.Commit)
		msg := redactURLs(perr.Error(), remotes)
		if len(rep.PublishedTo) == 0 {
			_ = held.Release()
			_ = os.RemoveAll(path)
			return nil, fmt.Errorf("vault init: nothing was published, so %s was removed; re-run the dry run: %s", path, msg)
		}
		return rep, fmt.Errorf("vault init: %s is published to %s but not to every remote: %s. Once the cause is fixed, re-run the same command to finish: %s",
			path, strings.Join(rep.PublishedTo, ", "), msg, m.Rerun)
	}
	rep.PublishedTo = order
	// The first push sets upstream: fetch every remote so its tracking ref
	// exists, then track the first.
	for _, r := range order {
		if _, err := lifecycleGit(path, lifecycleNetTimeout, "fetch", "--quiet", "--no-tags", r); err != nil {
			return rep, fmt.Errorf("vault init: published, but fetching %s for its tracking ref failed: %s", r, redactURLs(err.Error(), remotes))
		}
	}
	rep.Upstream = order[0]
	if _, err := gitCmd(path, 10*time.Second, "branch", "--set-upstream-to="+order[0]+"/"+initBranch, initBranch); err != nil {
		return rep, fmt.Errorf("vault init: published, but setting upstream %s failed: %w", order[0], err)
	}
	rep.Undo = []string{"rm -rf " + path}
	for _, r := range remotes {
		rep.Undo = append(rep.Undo, fmt.Sprintf("delete branch %s on %s (%s), on the git host", initBranch, r.Name, redactURL(r.URL)))
	}
	return rep, nil
}

func initReport(path string, req InitVaultRequest) *InitVaultReport {
	return &InitVaultReport{
		Path: path, Branch: initBranch, Remotes: req.Remotes, Upstream: req.Remotes[0].Name,
		Files:  []string{".gitignore", ".vibe-palace/vault.toml", remotesFile},
		DryRun: req.DryRun,
	}
}

func initRerun(path string, rs []RecordedRemote) string {
	parts := []string{"vp", "vault", "init", path}
	for _, r := range rs {
		parts = append(parts, "--remote", r.Name+"="+redactURL(r.URL))
	}
	return strings.Join(parts, " ")
}

// initCheckRemotesEmpty requires every remote reachable and holding no ref at
// all: vault init publishes a vault's FIRST commit, and a remote with history
// is somebody's vault (or something else) that this must never overwrite.
func initCheckRemotesEmpty(rs []RecordedRemote) error {
	for _, r := range rs {
		out, err := lifecycleGit(os.TempDir(), lifecycleNetTimeout, "ls-remote", r.URL)
		if err != nil {
			return fmt.Errorf("%w: %s (%s): %s", ErrInitRemoteUnreached, r.Name, redactURL(r.URL), redactURLs(err.Error(), rs))
		}
		if strings.TrimSpace(out) != "" {
			return fmt.Errorf("%w: %s (%s) already has refs; vp vault init publishes a new vault's first commit and never writes over a remote with history",
				ErrInitRemoteNotEmpty, r.Name, redactURL(r.URL))
		}
	}
	return nil
}

// initPublishedTo lists the remotes whose live tip contains sha.
func initPublishedTo(path string, remotes []string, sha string) []string {
	var out []string
	for _, r := range remotes {
		tip, err := liveTip(path, r, initBranch)
		if err != nil {
			continue
		}
		if ok, err := tipContains(path, sha, tip); err == nil && ok {
			out = append(out, r)
		}
	}
	return out
}

func initAbsPath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("vp vault init needs a path")
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("vault init: %q is not an absolute path", raw)
	}
	return filepath.Clean(raw), nil
}

// initCheckParent refuses a path whose parent is missing, or which lies inside
// a vault or a git work tree. Both checks run on the parent with symlinks
// RESOLVED: a lexical walk up a symlinked parent would never see the vault the
// link points into.
func initCheckParent(path string) error {
	parent := filepath.Dir(path)
	st, err := os.Stat(parent)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("vault init: the parent directory %s does not exist", parent)
	}
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("vault init: resolve %s: %w", parent, err)
	}
	for dir := real; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(surface.VaultManifestPath(dir)); err == nil {
			return fmt.Errorf("%w: %s is inside the vault %s", ErrInitNested, path, dir)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if top, err := gitCmd(real, 10*time.Second, "rev-parse", "--show-toplevel"); err == nil && top != "" {
		return fmt.Errorf("%w: %s is inside the git work tree %s", ErrInitNested, path, top)
	}
	return nil
}

// initCheckRemotes refuses no remote, a bad or repeated name, a URL with a
// control character or credentials, and two URLs naming one repository.
func initCheckRemotes(rs []RecordedRemote) error {
	if len(rs) == 0 {
		return ErrInitNoRemote
	}
	names := map[string]bool{}
	urls := map[string]string{}
	for _, r := range rs {
		if !remoteNameRE.MatchString(r.Name) {
			return fmt.Errorf("%w: remote name %q (want letters, digits, '.', '_' or '-')", ErrInitRemoteBad, r.Name)
		}
		if names[r.Name] {
			return fmt.Errorf("%w: remote name %q is given twice", ErrInitRemoteBad, r.Name)
		}
		names[r.Name] = true
		if err := checkRemoteURL(r); err != nil {
			return err
		}
		key := r.URL
		if n, ok := normaliseRemoteURL(key); ok {
			key = n
		}
		if other, dup := urls[key]; dup {
			return fmt.Errorf("%w: remotes %s and %s name the same repository", ErrInitRemoteBad, other, r.Name)
		}
		urls[key] = r.Name
	}
	return nil
}

// checkRemoteURL refuses an empty URL, any control character (TOML and git
// config both choke on them, and %q would render some as \x escapes TOML
// rejects), and credentials: a URL is tracked in remotes.toml and published to
// every clone, so it must never carry a password or token. The only userinfo
// allowed is the plain user "git" (scp-style git@host:path, or ssh://git@).
func checkRemoteURL(r RecordedRemote) error {
	u := r.URL
	if strings.TrimSpace(u) == "" {
		return fmt.Errorf("%w: remote %s has an empty URL", ErrInitRemoteBad, r.Name)
	}
	for _, c := range u {
		if c < 0x20 || c == 0x7f {
			return fmt.Errorf("%w: remote %s's URL contains a control character", ErrInitRemoteBad, r.Name)
		}
	}
	if u != strings.TrimSpace(u) {
		return fmt.Errorf("%w: remote %s's URL has surrounding whitespace", ErrInitRemoteBad, r.Name)
	}
	refuse := func() error {
		return fmt.Errorf("%w: remote %s's URL %s carries credentials; a vault's remotes are tracked and published, so use an ssh key or a credential helper instead",
			ErrInitRemoteBad, r.Name, redactURL(u))
	}
	if strings.Contains(u, "://") {
		p, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("%w: remote %s's URL does not parse", ErrInitRemoteBad, r.Name)
		}
		if p.User != nil {
			if _, hasPass := p.User.Password(); hasPass || p.User.Username() != "git" {
				return refuse()
			}
		}
		return nil
	}
	// scp-like user@host:path. Only "git@" is allowed.
	if at := strings.Index(u, "@"); at >= 0 {
		colon := strings.Index(u, ":")
		if colon < 0 || at < colon {
			if u[:at] != "git" {
				return refuse()
			}
		}
	}
	return nil
}

// redactURL replaces any userinfo other than the plain user "git" with "***".
func redactURL(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		host := rest
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			host = rest[:j]
		}
		if at := strings.LastIndex(host, "@"); at >= 0 {
			if host[:at] == "git" {
				return u
			}
			return u[:i+3] + "***@" + rest[at+1:]
		}
		return u
	}
	if at := strings.Index(u, "@"); at >= 0 {
		if colon := strings.Index(u, ":"); colon < 0 || at < colon {
			if u[:at] == "git" {
				return u
			}
			return "***@" + u[at+1:]
		}
	}
	return u
}

// redactURLs redacts every request URL that appears in s.
func redactURLs(s string, rs []RecordedRemote) string {
	for _, r := range rs {
		if red := redactURL(r.URL); red != r.URL {
			s = strings.ReplaceAll(s, r.URL, red)
		}
	}
	return s
}

// renderRemotesFile is the tracked record the vault keeps of its own remotes,
// in publish order. `vp vault clone` reads it.
func renderRemotesFile(rs []RecordedRemote) string {
	var b strings.Builder
	b.WriteString("# The remotes of this vault, in publish order; the first is the upstream.\n")
	b.WriteString("# Written by `vp vault init`; `vp vault clone` reads it to add the rest.\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "\n[[remote]]\nname = %q\nurl = %q\n", r.Name, r.URL)
	}
	return b.String()
}
