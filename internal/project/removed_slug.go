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

	"github.com/suykerbuyk/vibe-palace/internal/departure"
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

// Departure is a slug that LEFT this vault, and what to tell whoever still
// names it. It is the answer of Departed.
type Departure struct {
	Slug string
	// Source is "record" (Audits/departures/<slug>.json, authoritative) or
	// "history" (the git-history fallback: the slug existed, where it went is
	// unknown).
	Source string
	// Kind, To and Date come from the LAST record of the rename chain, so To
	// is where the project lives now. Via lists the intermediate slugs when
	// the chain has more than one hop.
	Kind departure.Kind
	To   string
	Date string
	Via  []string
	// Malformed carries a record that exists but cannot be read; the slug is
	// still departed.
	Malformed string
	// Commit and Subject name the last commit that touched Projects/<slug>/,
	// for the history source only.
	Commit, Subject string
}

// Departed reports a slug whose Projects/<slug>/ is ABSENT from the vault and
// which is known to have LEFT it: renamed to another slug, or moved to another
// vault.
//
// 🔴 THIS IS WHAT A STALE CHECKOUT LOOKS LIKE. A checkout whose
// .vibe-palace.toml still names a departed slug would otherwise lazily
// re-scaffold Projects/<old>/ on its next write — the reason the old
// quantum-ng checkout had to be quarantined after its migration. Every
// LEGITIMATE "marker names the slug, directory absent" case has no evidence
// of departure: a first write on a host whose vault has not pulled the other
// host's `vp init` yet, a `vp init` that failed after writing the marker, a
// brand-new project's first hook capture.
//
// Two sources, in order:
//
//  1. THE DEPARTURE RECORD (package departure), authoritative. It says WHERE
//     the project went, so the refusal can redirect, and it works on a vault
//     with no git at all. A rename chain is followed to its live end.
//  2. GIT HISTORY, the fallback: Projects/<slug>/ has history in the vault's
//     own repository and is now absent. It proves the slug existed, not where
//     it went.
//
// It costs one Lstat on the common path. The record read and git run only
// when the directory is absent; git under a timeout, and regardless of
// git_enabled: the kill switch governs vault MUTATION, and this is a read.
//
// FAIL-OPEN on the fallback, deliberately. A non-git vault (including one
// nested inside another repository), a failing or missing git, a timeout or a
// shallow clone missing the history all answer "not departed", which is
// exactly the behaviour before the fallback existed. Failing closed would
// refuse the legitimate first write on any vault where git misbehaves.
//
// Residual, stated rather than hidden: this is called by RequireKnownProject's
// marker arm, the hook, vp_capture_session and vp_memory_harvest; the other
// project-taking MCP writers (vp_append_iteration, vp_update_resume,
// vp_memory_write, vp_kg_add, vp_enqueue_iteration_summary) are not gated
// here.
func Departed(vaultRoot, slug string) (Departure, bool) {
	if vaultRoot == "" || slugpkg.Validate(slug) != nil {
		return Departure{}, false
	}
	if _, err := os.Lstat(filepath.Join(vaultRoot, "Projects", slug)); !os.IsNotExist(err) {
		// Present, or not inspectable: either way not evidence of departure.
		return Departure{}, false
	}
	if chain, ok := departure.Resolve(vaultRoot, slug); ok {
		last := chain[len(chain)-1]
		d := Departure{Slug: slug, Source: "record", Kind: last.Kind, To: last.To, Date: last.Date, Malformed: last.Malformed}
		for _, r := range chain[:len(chain)-1] {
			d.Via = append(d.Via, r.To)
		}
		return d, true
	}
	if removed, commit, subject := historyRemoved(vaultRoot, slug); removed {
		return Departure{Slug: slug, Source: "history", Commit: commit, Subject: subject}, true
	}
	return Departure{}, false
}

// historyRemoved is the git-history fallback of Departed: Projects/<slug>/ is
// absent but has history in the vault's own repository. commit and subject
// name the last commit that touched it. The caller has already checked that
// the directory is absent.
func historyRemoved(vaultRoot, slug string) (removed bool, commit, subject string) {
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

// RefuseDeparted is Departed as a refusal, for writers that take a slug with
// no repo context. nil when the slug has not departed.
func RefuseDeparted(vaultRoot, slug string) error {
	if d, ok := Departed(vaultRoot, slug); ok {
		return departureRefusal(d, "")
	}
	return nil
}

// Redirect is the one-line "where it went, what to set" for a departure, the
// sentence every refusal and warning shares.
func (d Departure) Redirect() string {
	via := ""
	if len(d.Via) > 0 {
		via = fmt.Sprintf(" (by way of %q)", strings.Join(d.Via, `", "`))
	}
	on := ""
	if d.Date != "" {
		on = " on " + d.Date
	}
	switch {
	case d.Source == "history":
		short := d.Commit
		if len(short) > 12 {
			short = short[:12]
		}
		return fmt.Sprintf("Projects/%s/ was removed from the vault (last touched by %s %q). Where it went is not "+
			"recorded: if it was renamed, set [project].name to the new slug; if it moved to another vault, point vault_path at it",
			d.Slug, short, d.Subject)
	case d.Malformed != "":
		return fmt.Sprintf("it departed this vault, but %s cannot be read (%s)", departure.RelPath(d.Slug), d.Malformed)
	case d.Kind == departure.Renamed:
		return fmt.Sprintf("it was renamed to %q%s%s; set [project].name = %q", d.To, via, on, d.To)
	default: // departure.MovedToVault
		dest := "a destination that was not recorded"
		if d.To != "" {
			dest = fmt.Sprintf("%q", d.To)
		}
		return fmt.Sprintf("it moved to another vault, %s%s%s; point vault_path at that vault", dest, via, on)
	}
}

// departureRefusal names only remedies that exist today: editing the marker
// or vault_path, and `vp init`, which scaffolds Projects/<slug>/ directly
// rather than through RequireKnownProject.
func departureRefusal(d Departure, markerPath string) error {
	named := ""
	if markerPath != "" {
		named = fmt.Sprintf(" the marker %s still names it, and", markerPath)
	}
	return fmt.Errorf(
		"refusing to write vault artifacts for project %q:%s %s.\n"+
			"Whatever names %q is stale (check `vp_list_projects`). To bring %q back deliberately, run `vp init` in its checkout.",
		d.Slug, named, d.Redirect(), d.Slug, d.Slug)
}
