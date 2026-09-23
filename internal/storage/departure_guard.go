// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	slugpkg "github.com/suykerbuyk/vibe-palace/internal/slug"
)

// departureGuardTimeout bounds each git call the guard makes. It is a TEST
// SEAM only in the sense that tests never need to change it.
const departureGuardTimeout = 30 * time.Second

// departureGuardMaxPaths bounds how many paths the refusal lists by name.
const departureGuardMaxPaths = 12

// guardIncomingDepartures refuses to bring <remote>/<branch> onto this host's
// work when the incoming commits take a project away (a rename, a split purge,
// any removal of its trees) and this host holds work under that project —
// unpushed commits or uncommitted dirt — that the merge or rebase would
// re-create or conflict on. It must be called AFTER the remote was fetched and
// BEFORE anything merges, rebases or heals.
//
// 🔴 EVERY MERGE AND REBASE OF INCOMING VAULT COMMITS CALLS THIS. There are
// exactly three in the tree — pullCore's merge, reconcileIfAhead's rebase and
// pushCommitted's rebase on a rejected push — plus SyncVault's pre-flight, which
// runs it before its own tidy commit so a refusal leaves HEAD untouched. They
// are all in this package, which the CLI, the MCP tools and the SessionEnd
// hook's harvest all reach, so no front end can bypass it.
// TestEveryVaultMergeOrRebaseCallsTheDepartureGuard pins that.
//
// WHICH SLUGS DEPARTED is keyed on the TREES, not the records: a slug departs
// when the incoming side deletes files under it and neither of its trees
// (ProjectTrees) exists at the incoming tip. That catches a departure with no
// record — the live quantum-ng rename predates records, and older binaries
// write none. A record, when the incoming side carries one, only supplies the
// wording (where it went).
//
// FAIL-OPEN on any git error: the operation then behaves exactly as it did
// before this guard, and the merge or rebase reports its own failure. A guard
// that could wedge every pull on a flaky git would be a new outage, not a
// safeguard. git_enabled=false never reaches here: every caller sits behind
// RefuseIfGitDisabled.
func guardIncomingDepartures(vaultPath, remote, branch string) error {
	ref := remote + "/" + branch
	g := func(args ...string) ([]byte, error) { return departureGit(vaultPath, args...) }

	// 1. Anything incoming at all?
	if _, err := g("merge-base", "--is-ancestor", ref, "HEAD"); err == nil {
		return nil
	}
	baseOut, err := g("merge-base", "HEAD", ref)
	if err != nil {
		return nil
	}
	base := strings.TrimSpace(string(baseOut))
	if base == "" {
		return nil
	}

	// 2. Slugs whose trees the incoming side removes.
	removed, err := g("diff", "--name-only", "-z", "--no-renames", "--diff-filter=D", base, ref, "--", "Projects", "palace")
	if err != nil {
		return nil
	}
	candidates := map[string]bool{}
	for _, p := range splitZ(removed) {
		parts := strings.SplitN(p, "/", 3)
		if len(parts) >= 3 && slugpkg.Validate(parts[1]) == nil {
			candidates[parts[1]] = true
		}
	}
	departed := map[string]departure.Record{}
	for s := range candidates {
		trees := ProjectTrees(s)
		left, err := g(append([]string{"ls-tree", "--name-only", ref, "--"}, trees...)...)
		if err != nil {
			return nil
		}
		if len(bytes.TrimSpace(left)) > 0 {
			continue // still here at the incoming tip: not a departure
		}
		rec := departure.Record{Slug: s}
		if data, err := g("show", ref+":"+departure.RelPath(s)); err == nil {
			rec = departure.Parse(s, data)
		}
		departed[s] = rec
	}
	if len(departed) == 0 {
		return nil
	}

	// 3. This host's work under a departed slug: unpushed commits, then dirt.
	var hits []departedPath
	seen := map[string]bool{}
	add := func(p, how string) {
		if seen[p] || ClassifyProjectPath(p) == ProjectMachineLocal {
			return
		}
		for s := range departed {
			for _, tree := range ProjectTrees(s) {
				if strings.HasPrefix(p, tree+"/") {
					seen[p] = true
					hits = append(hits, departedPath{Path: p, Slug: s, How: how})
					return
				}
			}
		}
	}
	local, err := g("diff", "--name-only", "-z", "--no-renames", base, "HEAD")
	if err != nil {
		return nil
	}
	for _, p := range splitZ(local) {
		add(p, "unpushed commit")
	}
	dirt, err := g("status", "--porcelain=v1", "-z", "-uall", "--no-renames")
	if err != nil {
		return nil
	}
	for _, e := range splitZ(dirt) {
		if len(e) > 3 {
			add(e[3:], "uncommitted")
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Path < hits[j].Path })

	derr := &DepartedWorkError{VaultPath: vaultPath, Remote: remote, Ref: ref, Paths: hits}
	for s, rec := range departed {
		for _, h := range hits {
			if h.Slug == s {
				derr.Departures = append(derr.Departures, rec)
				break
			}
		}
	}
	sort.Slice(derr.Departures, func(i, j int) bool { return derr.Departures[i].Slug < derr.Departures[j].Slug })

	// The category before the first colon is what vplog counts and what
	// vp_bootstrap_context's health alert prints — the only channel that
	// reaches an operator from the unattended SessionEnd harvest.
	var slugs []string
	for _, d := range derr.Departures {
		slugs = append(slugs, d.Slug)
	}
	slog.Warn("vault departed work: refused to merge or rebase incoming commits onto work under a departed project",
		"remote", remote, "slugs", strings.Join(slugs, ","), "paths", len(hits))
	return derr
}

// departedPath is one path of this host's work under a departed slug.
type departedPath struct {
	Path, Slug string
	How        string // "unpushed commit" or "uncommitted"
}

// DepartedWorkError is guardIncomingDepartures' refusal. Its text names the
// paths and carries the hold-branch procedure (the migration runbook's Q3.1):
// the guard never runs that procedure itself.
type DepartedWorkError struct {
	VaultPath  string
	Remote     string
	Ref        string
	Departures []departure.Record // Slug always set; Kind empty when the incoming side carried no record
	Paths      []departedPath
}

func (e *DepartedWorkError) Error() string {
	var b strings.Builder
	var names []string
	for _, d := range e.Departures {
		names = append(names, fmt.Sprintf("%q (%s)", d.Slug, departureWhere(d)))
	}
	fmt.Fprintf(&b, "refusing to bring %s onto this host's work: it carries the departure of project %s, "+
		"and this host has work under it that the merge would re-create:\n", e.Ref, strings.Join(names, ", "))
	for i, p := range e.Paths {
		if i == departureGuardMaxPaths {
			fmt.Fprintf(&b, "  … and %d more\n", len(e.Paths)-departureGuardMaxPaths)
			break
		}
		fmt.Fprintf(&b, "  %s   (%s)\n", p.Path, p.How)
	}
	b.WriteString("Nothing was merged or rebased: HEAD and the working tree are as they were (a new commit this operation made itself, if any, is kept locally and was not pushed).\n")
	b.WriteString("To carry the work across (the runbook's Q3.1 procedure):\n")
	for _, d := range e.Departures {
		fmt.Fprintf(&b, "  1. Keep it:  git -C %s branch hold/departed-%s-%s HEAD\n", e.VaultPath, d.Slug, time.Now().Format("2006-01-02"))
		b.WriteString("     and copy any file listed above as (uncommitted) somewhere safe.\n")
		switch {
		case d.Kind == departure.Renamed && d.Malformed == "":
			fmt.Fprintf(&b, "  2. Re-apply each change under Projects/%s/ and palace/%s/ (rewrite the paths from %q to %q), commit it, then\n",
				d.To, d.To, d.Slug, d.To)
		case d.Kind == departure.MovedToVault && d.Malformed == "":
			dest := "a vault that was not recorded"
			if d.To != "" {
				dest = fmt.Sprintf("%q", d.To)
			}
			fmt.Fprintf(&b, "  2. This work belongs in the vault %q moved to, %s: carry it there, not here. Then\n", d.Slug, dest)
		default:
			fmt.Fprintf(&b, "  2. Where %q went is not recorded here; find out from whoever removed it and carry the work there. Then\n", d.Slug)
		}
	}
	fmt.Fprintf(&b, "     git -C %s reset --hard %s   (the hold branch is the evidence)\n", e.VaultPath, e.Ref)
	b.WriteString("  3. Run the pull again.")
	return b.String()
}

// departureWhere is the one-phrase "where it went" for a departure.
func departureWhere(d departure.Record) string {
	switch {
	case d.Malformed != "":
		return "its departure record cannot be read"
	case d.Kind == departure.Renamed:
		return fmt.Sprintf("renamed to %q", d.To)
	case d.Kind == departure.MovedToVault && d.To != "":
		return fmt.Sprintf("moved to another vault, %q", d.To)
	case d.Kind == departure.MovedToVault:
		return "moved to another vault"
	default:
		return "removed; where it went is not recorded"
	}
}

// departureGit runs one read-only git command in the vault and returns STDOUT
// only — gitCmd folds stderr in, and a warning must never be parsed as a path.
func departureGit(vaultPath string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), departureGuardTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off"}, args...)...)
	cmd.Dir = vaultPath
	cmd.Env = gitenv.SafeGitEnv("GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	cmd.WaitDelay = time.Second
	return cmd.Output()
}

// splitZ splits NUL-separated git output, dropping empty fields.
func splitZ(b []byte) []string {
	var out []string
	for f := range bytes.SplitSeq(b, []byte{0}) {
		if len(f) > 0 {
			out = append(out, string(f))
		}
	}
	return out
}
