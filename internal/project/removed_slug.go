// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	slugpkg "github.com/suykerbuyk/vibe-palace/internal/slug"
)

// removedSlugGit and removedSlugTimeout are TEST SEAMS: the git binary the
// probe runs and how long it may take. A hook calls this on the capture path,
// so the probe must return promptly however git behaves.
var (
	removedSlugGit     = "git"
	removedSlugTimeout = 5 * time.Second
)

// RemovedSlug reports a slug whose Projects/<slug>/ is ABSENT from the vault
// but appears in the vault's git history: the project existed and was removed
// or renamed away. commit and subject name the last commit that touched it.
//
// 🔴 THIS IS WHAT A STALE CHECKOUT LOOKS LIKE. A checkout whose
// .vibe-palace.toml still names a renamed-away slug would otherwise lazily
// re-scaffold Projects/<old>/ on its next write — the reason the old
// quantum-ng checkout had to be quarantined after its migration. Every
// LEGITIMATE "marker names the slug, directory absent" case has NO history for
// the slug: a first write on a host whose vault has not pulled the other
// host's `vp init` yet, a `vp init` that failed after writing the marker, a
// brand-new project's first hook capture. History is the discriminator.
//
// It costs one Lstat on the common path. git runs only when the directory is
// absent, under a timeout, and regardless of git_enabled: the kill switch
// governs vault MUTATION, and this is a read.
//
// FAIL-OPEN, deliberately. A non-git vault (including one nested inside
// another repository), a failing or missing git, a timeout or a shallow clone
// missing the history all answer "not removed", which is exactly today's
// behaviour. Failing closed would refuse the legitimate first write on any
// vault where git misbehaves.
//
// Residual, stated rather than hidden: this is called by RequireKnownProject's
// marker arm, the hook, vp_capture_session and vp_memory_harvest; the other
// project-taking MCP writers (vp_append_iteration, vp_update_resume,
// vp_memory_write, vp_kg_add, vp_enqueue_iteration_summary) are not gated
// here. The durable choke point for all of them is the tracked slug departure
// record of task slug-departure-record-and-marker-redirect (epic
// project-relocation-rename-and-vault-split), which should become the first,
// authoritative source this predicate consults, with git history kept as the
// fallback.
func RemovedSlug(vaultRoot, slug string) (removed bool, commit, subject string) {
	if vaultRoot == "" || slugpkg.Validate(slug) != nil {
		return false, "", ""
	}
	if _, err := os.Lstat(filepath.Join(vaultRoot, "Projects", slug)); !os.IsNotExist(err) {
		// Present, or not inspectable: either way not evidence of removal.
		return false, "", ""
	}
	// ONE timeout for the whole probe, both git calls included.
	ctx, cancel := context.WithTimeout(context.Background(), removedSlugTimeout)
	defer cancel()
	// 🔴 ONLY THE VAULT'S OWN HISTORY IS EVIDENCE. `git -C <vault>` walks UP to
	// any enclosing repository when the vault is not its own top level, and an
	// enclosing repo that once held <vault>/Projects/<slug>/ would answer
	// "removed" for a vault that never had it — refusing a legitimate write,
	// the fail-CLOSED direction this probe must never take. The same class as
	// tasks/done/vault-sync-pushes-the-enclosing-repo-of-a-nested-vault.md; the
	// rule mirrors storage.InspectVaultGit's VaultGitNested test (show-toplevel
	// compared with the vault root, both symlink-resolved), which this package
	// cannot import because storage imports it.
	top, err := removedSlugGitOut(ctx, vaultRoot, "rev-parse", "--show-toplevel")
	if err != nil || !sameResolvedDir(strings.TrimSpace(top), vaultRoot) {
		return false, "", ""
	}
	out, err := removedSlugGitOut(ctx, vaultRoot, "log", "-1", "--format=%H%x00%s", "--", "Projects/"+slug+"/")
	if err != nil {
		return false, "", ""
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return false, "", ""
	}
	sha, subj, _ := strings.Cut(line, "\x00")
	return true, sha, subj
}

// removedSlugGitOut runs one read-only git command in dir under ctx and
// returns its stdout.
func removedSlugGitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, removedSlugGit, append([]string{"-C", dir}, args...)...)
	cmd.Env = gitenv.SafeGitEnv("GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	// A git that ignores the kill must not hold the caller on its pipes.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	return string(out), err
}

// sameResolvedDir reports whether a and b are the same directory once both
// are symlink-resolved (git reports a resolved top level; a vault path may
// not be). Unresolvable means "not the same": the probe then fails open.
func sameResolvedDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && filepath.Clean(ra) == filepath.Clean(rb)
}

// RefuseRemovedSlug is RemovedSlug as a refusal, for writers that take a slug
// with no repo context (vp_capture_session, vp_memory_harvest). nil when the slug was
// not removed.
func RefuseRemovedSlug(vaultRoot, slug string) error {
	if removed, commit, subject := RemovedSlug(vaultRoot, slug); removed {
		return removedSlugRefusal(slug, "", commit, subject)
	}
	return nil
}

// removedSlugRefusal names only remedies that exist today: editing the marker,
// and `vp init`, which scaffolds Projects/<slug>/ directly rather than through
// RequireKnownProject.
func removedSlugRefusal(slug, markerPath, commit, subject string) error {
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	named := ""
	if markerPath != "" {
		named = fmt.Sprintf(" the marker %s names it, but", markerPath)
	}
	return fmt.Errorf(
		"refusing to write vault artifacts for project %q:%s Projects/%s/ was removed from the vault "+
			"(last touched by %s %q).\n"+
			"Whatever names %q is stale. If the project was renamed, set [project].name in .vibe-palace.toml "+
			"to the new slug (check `vp_list_projects`). To bring %q back deliberately, run `vp init` in its checkout.",
		slug, named, slug, short, subject, slug, slug)
}
