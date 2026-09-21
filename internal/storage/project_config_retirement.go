// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// ONE-SHOT. Everything in this file exists for `vp migrate project-configs`
// (cmd/vp/cmd_migrate_project_configs.go), the one-time retirement of the
// per-project vault configs, Projects/<slug>/config.toml, that vp no
// longer reads. Delete this file, its test and that command together; the only
// other thing to remove with them is the planProjectConfigRetirement entry in
// internal/sourceaudit's plannerFuncs.
//
// It lives in package storage, not beside the command, because it needs the
// package's unexported renderScoringSections, gitCmd, rebaseInProgress,
// treeEntryOID and gitBlob, and exporting any of those permanently for a
// one-time command would outlive the command.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// RenderRetiredScoring decodes a retired Projects/<slug>/config.toml and returns
// what it carried that still has a home: its palace.scoring subtree, rendered by
// renderScoringSections — the host-local writer's own renderer, so the block is
// exactly what that writer would put in <config dir>/vibe-palace/projects/<slug>.toml.
// scoring is "" when the file carries no palace.scoring.
//
// dropped names every other section the file carries, which has no per-project
// tier any more (top-level tables other than [meta] and [palace], and [palace]
// sub-tables other than scoring), sorted.
//
// The file is decoded with a real TOML parser and nothing is located by
// scanning lines: live files carry indented headers, a bare [palace], rooms
// with no [palace.scoring] parent, and no [meta] at all, and every one of those
// is ordinary TOML.
func RenderRetiredScoring(data []byte) (scoring string, dropped []string, err error) {
	var top map[string]any
	if err := toml.Unmarshal(data, &top); err != nil {
		return "", nil, fmt.Errorf("decode: %w", err)
	}
	for key, v := range top {
		if key == "meta" || key == "palace" {
			continue
		}
		if _, isTable := v.(map[string]any); isTable {
			dropped = append(dropped, key)
		}
	}
	if p, ok := top["palace"]; ok {
		pal, ok := p.(map[string]any)
		if !ok {
			return "", nil, fmt.Errorf("palace is %T, want a table", p)
		}
		for key, v := range pal {
			if key == "scoring" {
				continue
			}
			if _, isTable := v.(map[string]any); isTable {
				dropped = append(dropped, "palace."+key)
			}
		}
		if s, ok := pal["scoring"]; ok {
			sc, ok := s.(map[string]any)
			if !ok {
				return "", nil, fmt.Errorf("palace.scoring is %T, want a table", s)
			}
			scoring, err = renderScoringSections(sc)
			if err != nil {
				return "", nil, err
			}
		}
	}
	sort.Strings(dropped)
	return scoring, dropped, nil
}

// UntrackedAmong returns the members of rels (vault-relative) that git does not
// track in root's index, in rels' order. It is the census's one git call, named
// so review can hold it: read-only, `git ls-files --error-unmatch` per path.
// Exit 1 is "not tracked"; any other failure is an error, never "untracked".
func UntrackedAmong(root string, rels []string) ([]string, error) {
	var out []string
	for _, rel := range rels {
		cmd := exec.Command("git", "--literal-pathspecs", "ls-files", "--error-unmatch", "--", rel)
		cmd.Dir = root
		cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err == nil {
			continue
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			out = append(out, rel)
			continue
		}
		return nil, fmt.Errorf("git ls-files --error-unmatch -- %s: %w: %s", rel, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// operationInProgress names a merge, cherry-pick, revert or rebase that is
// mid-flight in root's repository, or returns "". The three *_HEAD states are
// probed the way rebaseInProgress probes its state directories: through
// `rev-parse --git-path`, so a linked worktree's own git dir is the one read.
func operationInProgress(root string) (string, error) {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD"} {
		p, err := gitCmd(root, 5*time.Second, "rev-parse", "--git-path", name)
		if err != nil {
			return "", fmt.Errorf("locate %s: %w", name, err)
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		if _, err := os.Lstat(p); err == nil {
			return name, nil
		}
	}
	if rebaseInProgress(root) {
		return "a rebase", nil
	}
	return "", nil
}

// isStampPath mirrors the stamp locations surface.CheckCompatible scans
// (internal/surface/version.go, its `patterns` list). Copied rather than
// exported because this file is one-shot. If that list changes before this
// file is deleted, change this one with it.
func isStampPath(rel string) bool {
	if path.Base(rel) != ".surface" {
		return false
	}
	switch dir := path.Dir(rel); {
	case dir == "Templates", dir == "Audits":
		return true
	default:
		parts := strings.Split(dir, "/")
		return len(parts) == 2 && (parts[0] == "Projects" || parts[0] == "palace")
	}
}

// decodeStamp parses .surface bytes read from git rather than from disk.
func decodeStamp(data []byte) (surface.Stamp, error) {
	var s surface.Stamp
	if err := toml.Unmarshal(data, &s); err != nil {
		return surface.Stamp{}, err
	}
	return s, nil
}

// maxSurfaceAt returns the highest surface stamp COMMITTED at rev across every
// stamp location, 0 when there is none. A stamp that cannot be read or parsed
// is an error, never a 0: an unreadable floor is not a low one.
func maxSurfaceAt(root, rev string) (int, error) {
	out, err := gitCmd(root, 10*time.Second, "ls-tree", "-r", "-z", "--name-only", rev, "--", "Projects", "palace", "Templates", "Audits")
	if err != nil {
		return 0, fmt.Errorf("list %s: %w", rev, err)
	}
	highest := 0
	for _, rel := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		if rel == "" || !isStampPath(rel) {
			continue
		}
		oid, found, err := treeEntryOID(root, rev, rel)
		if err != nil {
			return 0, err
		}
		if !found {
			continue
		}
		data, err := gitBlob(root, oid)
		if err != nil {
			return 0, err
		}
		s, err := decodeStamp(data)
		if err != nil {
			return 0, fmt.Errorf("%s at %s is malformed: %w", rel, rev, err)
		}
		if s.Surface > highest {
			highest = s.Surface
		}
	}
	return highest, nil
}

// CheckProjectConfigRetirement runs every precondition of the retirement's
// apply, in order, and returns the first refusal. It deletes nothing and never
// commits; the fetches refresh remote-tracking refs only. The caller has
// already required root to be the top level of its own repository.
//
// The stamp check is the reason this command waits for the fleet: a host still
// on a binary older than floor would read these files and re-create them. The
// floor must therefore be COMMITTED — in HEAD when there is no remote, and at
// every remote's tip otherwise, with each tip an ancestor of HEAD so the commit
// this run makes lands on top of what every other host already pulled.
func CheckProjectConfigRetirement(root string, floor int) error {
	if err := RefuseIfGitDisabled(root, "retire the per-project vault configs"); err != nil {
		return err
	}
	if err := CheckCommitIdentity(root); err != nil {
		return err
	}
	op, err := operationInProgress(root)
	if err != nil {
		return err
	}
	if op != "" {
		return fmt.Errorf("%s is in progress in %s; finish or abort it first", op, root)
	}
	dirt, err := gitCmd(root, 10*time.Second, "status", "--porcelain", "--untracked-files=all", "--", "Projects")
	if err != nil {
		return fmt.Errorf("git status -- Projects: %w", err)
	}
	if dirt != "" {
		return fmt.Errorf("Projects/ has uncommitted changes; commit or tidy them first (vp vault tidy):\n%s", dirt)
	}
	branch, err := currentBranch(root)
	if err != nil {
		return err
	}
	remotes, err := ListRemotes(root)
	if err != nil {
		return fmt.Errorf("list remotes: %w", err)
	}
	if len(remotes) == 0 {
		return requireFloorAt(root, "HEAD", floor)
	}
	for _, remote := range remotes {
		if _, err := gitCmd(root, 60*time.Second, "fetch", "-q", remote); err != nil {
			return fmt.Errorf("fetch %s: %w", remote, err)
		}
		tip := "refs/remotes/" + remote + "/" + branch
		if _, err := gitCmd(root, 10*time.Second, "rev-parse", "--verify", "-q", tip); err != nil {
			return fmt.Errorf("%s/%s does not resolve after a fetch", remote, branch)
		}
		if err := requireFloorAt(root, tip, floor); err != nil {
			return err
		}
		if _, err := gitCmd(root, 10*time.Second, "merge-base", "--is-ancestor", tip, "HEAD"); err != nil {
			return fmt.Errorf("%s/%s is not an ancestor of HEAD; pull first (vp vault sync)", remote, branch)
		}
	}
	return nil
}

func requireFloorAt(root, rev string, floor int) error {
	got, err := maxSurfaceAt(root, rev)
	if err != nil {
		return err
	}
	if got < floor {
		return fmt.Errorf("the surface stamp committed at %s is %d, below %d: every host must run a surface-%d binary, "+
			"and a stamp at %d must be committed and pushed, before the configs can be retired", rev, got, floor, floor, floor)
	}
	return nil
}

// RetiringConfig is one tracked config the retirement removes, with the sha256
// of the bytes the planner read: vaultfs.Delete refuses if the file changed
// since.
type RetiringConfig struct {
	Rel    string
	Sha256 string
}

// RetireProjectConfigs deletes every config, then commits the removals as ONE
// commit through CommitRemovals. On any failure after the first deletion it
// restores every file from HEAD (`git checkout HEAD -- <paths>`) and returns the
// error, so a failed run leaves no deletion and no " D" dirt behind. Only
// tracked files are ever passed here; the command refuses while any untracked
// config exists, so HEAD holds every byte a restore needs.
//
// afterDelete is a test seam, nil in production: it is called after the n-th
// deletion (1-based), and an error it returns fails the run at that point.
func RetireProjectConfigs(root, message string, configs []RetiringConfig, afterDelete func(n int) error) (string, error) {
	var deleted []string
	restore := func(cause error) (string, error) {
		if len(deleted) == 0 {
			return "", cause
		}
		args := append([]string{"--literal-pathspecs", "checkout", "HEAD", "--"}, deleted...)
		if _, err := gitCmd(root, 30*time.Second, args...); err != nil {
			return "", fmt.Errorf("%w (and restoring from HEAD failed: %v — run: git -C %s checkout HEAD -- %s)",
				cause, err, root, strings.Join(deleted, " "))
		}
		return "", cause
	}
	for i, c := range configs {
		if _, err := vaultfs.Delete(root, c.Rel, c.Sha256); err != nil {
			return restore(fmt.Errorf("delete %s: %w", c.Rel, err))
		}
		deleted = append(deleted, c.Rel)
		if afterDelete != nil {
			if err := afterDelete(i + 1); err != nil {
				return restore(err)
			}
		}
	}
	if len(deleted) == 0 {
		return "", nil
	}
	res, err := CommitRemovals(root, message, deleted)
	if err != nil {
		var left *RemovalsLeftInHEADError
		if errors.As(err, &left) {
			// The commit landed without some paths; restore exactly those.
			deleted = left.Paths
		}
		return restore(err)
	}
	return res.CommitSHA, nil
}
