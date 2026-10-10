// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// ONE-SHOT. Everything in this file exists for `vp migrate
// authored-only-palace` (cmd/vp/cmd_migrate_authored_only.go), the one-time,
// revertible migration of a populated vault to the authored-only layout
// (ADR-014 decision 11). Delete this file, its test and that command together.
//
// It lives in package storage, not beside the command, because it reuses the
// package's unexported git plumbing — gitCmd, requireFloorAt, maxSurfaceAt,
// operationInProgress, currentBranch, unmergedPathsZ, dropDerivedPaths,
// isDerivedPath, ClassifyTriple/ClassifyEntityLine, readTripleFile, splitNUL —
// and exporting any of those permanently for a one-shot command would outlive
// the command.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// AuthoredOnlyGitState is the single fetch-then-read result that path selection
// and every precondition check share, so they all agree on what each remote
// holds (ADR-014 decision 11, "one fetch per remote"). It is produced once, at
// the start of a run, by ReadAuthoredOnlyGitState.
type AuthoredOnlyGitState struct {
	// Branch is the current branch; a detached HEAD never reaches here.
	Branch string
	// Tips is one entry per remote, nil when the vault has no remote.
	Tips []RemoteTip
}

// RemoteTip is one remote's resolved tracking ref after the fetch.
type RemoteTip struct {
	Remote string // e.g. "origin"
	Ref    string // refs/remotes/<remote>/<branch>
}

// Label is the remote/branch pair for a refusal or a plan line.
func (t RemoteTip) Label(branch string) string { return t.Remote + "/" + branch }

// ReadAuthoredOnlyGitState runs the opening checks that need no remote, then
// fetches each remote once and reads its tracking ref immediately, with nothing
// run in between. The opening checks mirror CheckProjectConfigRetirement's
// (project_config_retirement.go): git enabled, a commit identity, and a named
// branch. A detached HEAD refuses here, before any fetch, because
// refs/remotes/<r>/<branch> cannot be resolved without a branch.
func ReadAuthoredOnlyGitState(root string) (*AuthoredOnlyGitState, error) {
	if err := RefuseIfGitDisabled(root, "migrate the vault to the authored-only layout"); err != nil {
		return nil, err
	}
	if err := CheckCommitIdentity(root); err != nil {
		return nil, err
	}
	if op, err := operationInProgress(root); err != nil {
		return nil, err
	} else if op != "" {
		return nil, fmt.Errorf("%s is in progress in %s; finish or abort it first", op, root)
	}
	branch, err := currentBranch(root)
	if err != nil {
		return nil, err
	}
	remotes, err := ListRemotes(root)
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	st := &AuthoredOnlyGitState{Branch: branch}
	for _, remote := range remotes {
		if _, err := gitCmd(root, 60*time.Second, "fetch", "-q", remote); err != nil {
			return nil, fmt.Errorf("fetch %s: %w", remote, err)
		}
		tip := "refs/remotes/" + remote + "/" + branch
		if _, err := gitCmd(root, 10*time.Second, "rev-parse", "--verify", "-q", tip); err != nil {
			return nil, fmt.Errorf("%s/%s does not resolve after a fetch", remote, branch)
		}
		st.Tips = append(st.Tips, RemoteTip{Remote: remote, Ref: tip})
	}
	return st, nil
}

// RequireSurfaceFloorAtRemoteTips runs BOTH halves — the surface floor and the
// ancestry — at every remote tip, or at HEAD when there is no remote. It is the
// normal path's check, factored from CheckProjectConfigRetirement's loop
// (project_config_retirement.go:243-258) with migration-specific text. Its two
// halves are separately callable: the empty-vault path calls
// RequireRemoteTipsAreAncestors alone, because a stampless vault has no stamp to
// meet a floor with.
func RequireSurfaceFloorAtRemoteTips(root string, floor int, st *AuthoredOnlyGitState) error {
	if err := RequireSurfaceFloorAtTips(root, floor, st); err != nil {
		return err
	}
	return RequireRemoteTipsAreAncestors(root, st)
}

// RequireSurfaceFloorAtTips is the floor half: every remote tip carries a
// committed stamp at or above floor. With no remote it checks HEAD, as the
// 6->7 precedent does (project_config_retirement.go:239-241).
func RequireSurfaceFloorAtTips(root string, floor int, st *AuthoredOnlyGitState) error {
	if len(st.Tips) == 0 {
		return requireFloorAt(root, "HEAD", floor)
	}
	for _, t := range st.Tips {
		got, err := maxSurfaceAt(root, t.Ref)
		if err != nil {
			return err
		}
		if got < floor {
			return fmt.Errorf("the surface stamp committed at %s is %d, below %d: every host must run a surface-%d "+
				"binary, and a stamp at %d must be committed and pushed, before the vault can be migrated",
				t.Label(st.Branch), got, floor, floor, floor)
		}
	}
	return nil
}

// RequireRemoteTipsAreAncestors is the ancestor half: every remote tip is an
// ancestor of HEAD. Without it a remote commit that adds a drawer would merge in
// TRACKED, because ignore rules do not apply to merges. With no remote it holds.
func RequireRemoteTipsAreAncestors(root string, st *AuthoredOnlyGitState) error {
	for _, t := range st.Tips {
		if _, err := gitCmd(root, 10*time.Second, "merge-base", "--is-ancestor", t.Ref, "HEAD"); err != nil {
			return fmt.Errorf("%s is not an ancestor of HEAD; pull first (vp vault sync)", t.Label(st.Branch))
		}
	}
	return nil
}

// CheckAuthoredOnlyGitPosture runs the clean-and-pushed preconditions shared by
// both paths (ADR-014 decision 11, "a clean local vault"): a clean working tree,
// no unmerged index entry anywhere, no stash entry touching palace/, and pushed
// (HEAD equals every remote tip — rev-list --count <tip>..HEAD = 0, which with
// the ancestry half is equality). The operation-in-progress check runs earlier,
// in ReadAuthoredOnlyGitState.
func CheckAuthoredOnlyGitPosture(root string, st *AuthoredOnlyGitState) error {
	dirt, err := gitCmd(root, 10*time.Second, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(dirt) != "" {
		return fmt.Errorf("the vault has uncommitted changes; commit or tidy them first (vp vault tidy):\n%s", dirt)
	}
	unmerged, err := unmergedPathsZ(root)
	if err != nil {
		return err
	}
	if len(unmerged) > 0 {
		return fmt.Errorf("the index has %d unmerged entry/entries (e.g. %s); resolve the conflict first", len(unmerged), unmerged[0].path)
	}
	if err := refuseStashTouchingPalace(root); err != nil {
		return err
	}
	for _, t := range st.Tips {
		ahead, err := gitCmd(root, 10*time.Second, "rev-list", "--count", t.Ref+"..HEAD")
		if err != nil {
			return fmt.Errorf("count commits ahead of %s: %w", t.Label(st.Branch), err)
		}
		if strings.TrimSpace(ahead) != "0" {
			return fmt.Errorf("%s commit(s) on HEAD are not on %s; push first (vp vault sync)", strings.TrimSpace(ahead), t.Label(st.Branch))
		}
	}
	return nil
}

// refuseStashTouchingPalace refuses when any stash entry touches a palace/ path:
// a later `git stash pop` would bring a derived file back into a migrated vault.
func refuseStashTouchingPalace(root string) error {
	list, err := gitCmd(root, 10*time.Second, "stash", "list", "--format=%gd")
	if err != nil {
		return fmt.Errorf("list stash entries: %w", err)
	}
	for _, ref := range strings.Split(strings.TrimSpace(list), "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		names, err := gitCmd(root, 10*time.Second, "stash", "show", "--name-only", ref)
		if err != nil {
			return fmt.Errorf("inspect stash entry %s: %w", ref, err)
		}
		for _, name := range strings.Split(names, "\n") {
			if strings.HasPrefix(strings.TrimSpace(name), "palace/") {
				return fmt.Errorf("stash entry %s touches palace/ (%s); drop or apply it first", ref, strings.TrimSpace(name))
			}
		}
	}
	return nil
}

// VaultEmptyForAuthoredOnly reports whether the vault is empty as the
// empty-vault path requires (ADR-014 decision 11, "What \"empty\" means"): no
// tracked palace/ content AND no .surface stamp, both at HEAD and at every
// remote tip. reason names the first HEAD-or-tip that made it not empty, for the
// plan output. A malformed stamp is an error, never "no stamp".
func VaultEmptyForAuthoredOnly(root string, st *AuthoredOnlyGitState) (empty bool, reason string, err error) {
	type rev struct{ name, ref string }
	revs := []rev{{"HEAD", "HEAD"}}
	for _, t := range st.Tips {
		revs = append(revs, rev{t.Label(st.Branch), t.Ref})
	}
	for _, r := range revs {
		out, err := gitCmd(root, 10*time.Second, "ls-tree", "-r", "--name-only", r.ref, "--", "palace")
		if err != nil {
			return false, "", fmt.Errorf("list palace/ at %s: %w", r.name, err)
		}
		if strings.TrimSpace(out) != "" {
			return false, r.name + " has tracked palace/ content", nil
		}
		got, err := maxSurfaceAt(root, r.ref)
		if err != nil {
			return false, "", err
		}
		if got > 0 {
			return false, fmt.Sprintf("%s carries a surface-%d stamp", r.name, got), nil
		}
	}
	return true, "", nil
}

// --- Tag selection (ADR-014 decision 4; mig-S2) ---

// AuthoredOnlyTag is the chosen pre-authored-only tag and how it was chosen.
type AuthoredOnlyTag struct {
	Name   string // pre-authored-only-<date>[-<n>]
	Reused bool   // already on HEAD (locally or on a remote): reuse, never re-create
}

// ChooseAuthoredOnlyTag picks the tag name for this run, idempotently (mig-S2).
// The base is pre-authored-only-<date> (date is the run's UTC day). It checks
// the base and each suffixed name locally and on every remote (git ls-remote
// --tags):
//
//   - a name already on HEAD, locally or on any remote: reuse it (a crash
//     between the tag push and the commit);
//   - a name on another commit: skip to the next suffix (pre-...-<n>, n from 2);
//   - the first name absent everywhere: a new tag on HEAD.
//
// It never force-moves or deletes a tag.
func ChooseAuthoredOnlyTag(root string, st *AuthoredOnlyGitState, date string) (AuthoredOnlyTag, error) {
	head, err := gitCmd(root, 10*time.Second, "rev-parse", "HEAD")
	if err != nil {
		return AuthoredOnlyTag{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	head = strings.TrimSpace(head)
	base := "pre-authored-only-" + date
	for n := 0; ; n++ {
		name := base
		if n == 1 {
			continue // suffixes start at 2
		}
		if n >= 2 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		commits, err := tagCommitsEverywhere(root, st, name)
		if err != nil {
			return AuthoredOnlyTag{}, err
		}
		if len(commits) == 0 {
			return AuthoredOnlyTag{Name: name, Reused: false}, nil
		}
		onHead := true
		for _, c := range commits {
			if c != head {
				onHead = false
				break
			}
		}
		if onHead {
			return AuthoredOnlyTag{Name: name, Reused: true}, nil
		}
		// taken by another commit; try the next suffix.
	}
}

// tagCommitsEverywhere returns the commit OID each place name resolves to —
// locally and on every remote — deduplicated. An absent tag contributes
// nothing, so an empty result means the name is free everywhere.
func tagCommitsEverywhere(root string, st *AuthoredOnlyGitState, name string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(oid string) {
		oid = strings.TrimSpace(oid)
		if oid == "" || seen[oid] {
			return
		}
		seen[oid] = true
		out = append(out, oid)
	}
	if local, err := gitCmd(root, 10*time.Second, "rev-parse", "--verify", "-q", "refs/tags/"+name); err == nil {
		add(local)
	}
	for _, t := range st.Tips {
		ls, err := gitCmd(root, 30*time.Second, "ls-remote", "--tags", t.Remote, "refs/tags/"+name)
		if err != nil {
			return nil, fmt.Errorf("ls-remote --tags %s: %w", t.Remote, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(ls), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 1 && fields[0] != "" {
				add(fields[0])
			}
		}
	}
	return out, nil
}

// CreateAndPushAuthoredOnlyTag creates the tag locally if it is absent and
// pushes refs/tags/<name> to every remote that lacks it at HEAD. It never
// force-pushes, deletes or moves a tag.
func CreateAndPushAuthoredOnlyTag(root string, st *AuthoredOnlyGitState, tag AuthoredOnlyTag) error {
	if _, err := gitCmd(root, 10*time.Second, "rev-parse", "--verify", "-q", "refs/tags/"+tag.Name); err != nil {
		if _, err := gitCmd(root, 10*time.Second, "tag", tag.Name, "HEAD"); err != nil {
			return fmt.Errorf("create tag %s: %w", tag.Name, err)
		}
	}
	head, err := gitCmd(root, 10*time.Second, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}
	head = strings.TrimSpace(head)
	for _, t := range st.Tips {
		// Consult the REMOTE alone (never the local tag just created): push only
		// when the remote does not already carry this tag at HEAD.
		ls, err := gitCmd(root, 30*time.Second, "ls-remote", "--tags", t.Remote, "refs/tags/"+tag.Name)
		if err != nil {
			return fmt.Errorf("ls-remote --tags %s: %w", t.Remote, err)
		}
		hasAtHead := false
		for _, line := range strings.Split(strings.TrimSpace(ls), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 1 && strings.TrimSpace(fields[0]) == head {
				hasAtHead = true
			}
		}
		if hasAtHead {
			continue
		}
		if _, err := gitCmd(root, 60*time.Second, "push", t.Remote, "refs/tags/"+tag.Name); err != nil {
			return fmt.Errorf("push tag %s to %s: %w", tag.Name, t.Remote, err)
		}
	}
	return nil
}

// --- KG classification and the derived-file plan ---

// isMempalaceTriple reports whether a tracked triple is a mempalace import:
// extracted_at set, no source_session, and no explicit origin (ADR-014 lines
// "Mempalace triples are always extracted"; internal/migrate/mempalace.go). The
// migration force-classifies such a triple as EXTRACTED, overriding
// ClassifyTriple's rule 3 (which would keep one carrying valid_to as authored).
func isMempalaceTriple(t Triple) bool {
	return t.Origin == "" && t.ExtractedAt != "" && t.SourceSession == ""
}

// classifyTripleForMigration is ClassifyTriple with the mempalace override on
// top. It is the predicate the migration drops and keeps by.
func classifyTripleForMigration(t Triple) string {
	if isMempalaceTriple(t) {
		return OriginExtracted
	}
	return ClassifyTriple(t)
}

// AuthoredOnlyProjectPlan is one project's classified KG plus the derived index
// paths the migration removes from it.
type AuthoredOnlyProjectPlan struct {
	Project string
	// DropTriples are tracked extracted triple files (vault-relative) to delete.
	DropTriples []string
	// StampTriples are authored triple files whose on-disk origin is not yet
	// authored, which the migration rewrites with origin: authored.
	StampTriples []string
	// AuthoredTriplesKept is every authored triple kept (stamped or already
	// stamped), for the plan's kept-record count and the Projects-only decision.
	AuthoredTriplesKept int
	// EntitiesRel is the project's kg/entities.jsonl, "" when it has none.
	EntitiesRel string
	// EntitiesAuthored / EntitiesExtracted are its authored / extracted lines.
	EntitiesAuthored  int
	EntitiesExtracted int
	// DropDerived are the tracked derived paths (drawers, ingested-archives) to
	// delete.
	DropDerived []string
}

// AuthoredOnlyPlan is the whole vault's migration plan, computed read-only.
type AuthoredOnlyPlan struct {
	Projects []AuthoredOnlyProjectPlan
	// ResidueSlugs are departed slugs whose palace/<slug>/ holds only untracked
	// legacy residue, removed by the apply.
	ResidueSlugs []string
}

// DropPaths is every tracked path the apply removes (extracted triples, derived
// drawers/ledger, and entities files with no authored line).
func (p AuthoredOnlyPlan) DropPaths() []string {
	var out []string
	for _, pr := range p.Projects {
		out = append(out, pr.DropTriples...)
		out = append(out, pr.DropDerived...)
		if pr.EntitiesRel != "" && pr.EntitiesAuthored == 0 && pr.EntitiesExtracted > 0 {
			out = append(out, pr.EntitiesRel)
		}
	}
	return out
}

// PlanAuthoredOnlyMigration reads the vault and classifies its tracked KG and
// derived index paths, per project, with classifyTripleForMigration and
// ClassifyEntityLine. It writes nothing. It is the one planner both the dry run
// and the apply use.
func PlanAuthoredOnlyMigration(v *Vault) (*AuthoredOnlyPlan, error) {
	root := v.Root
	out, _, err := gitCmdStdin(root, 2*time.Minute, "", "ls-files", "-z", "--", "palace")
	if err != nil {
		return nil, fmt.Errorf("list tracked palace files: %w", err)
	}
	byProject := map[string]*AuthoredOnlyProjectPlan{}
	proj := func(p string) *AuthoredOnlyProjectPlan {
		if byProject[p] == nil {
			byProject[p] = &AuthoredOnlyProjectPlan{Project: p}
		}
		return byProject[p]
	}
	for _, rel := range splitNUL([]byte(out)) {
		if rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		if len(parts) < 2 || parts[0] != "palace" || slug.Validate(parts[1]) != nil {
			continue
		}
		p := parts[1]
		if isDerivedPath(rel) {
			proj(p).DropDerived = append(proj(p).DropDerived, rel)
			continue
		}
		if len(parts) < 4 || parts[2] != "kg" {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		switch {
		case len(parts) >= 5 && parts[3] == "triples" && strings.HasSuffix(rel, ".json"):
			t, err := readTripleFile(abs)
			if err != nil {
				return nil, fmt.Errorf("read triple %s: %w", rel, err)
			}
			if classifyTripleForMigration(t) == OriginAuthored {
				proj(p).AuthoredTriplesKept++
				if t.Origin != OriginAuthored {
					proj(p).StampTriples = append(proj(p).StampTriples, rel)
				}
			} else {
				proj(p).DropTriples = append(proj(p).DropTriples, rel)
			}
		case len(parts) == 4 && parts[3] == "entities.jsonl":
			pr := proj(p)
			pr.EntitiesRel = rel
			auth, ext, err := countEntityLinesByOrigin(abs)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", rel, err)
			}
			pr.EntitiesAuthored = auth
			pr.EntitiesExtracted = ext
		}
	}
	plan := &AuthoredOnlyPlan{}
	for _, p := range sortedKeys(byProject) {
		pr := byProject[p]
		sort.Strings(pr.DropTriples)
		sort.Strings(pr.StampTriples)
		sort.Strings(pr.DropDerived)
		plan.Projects = append(plan.Projects, *pr)
	}
	residue, err := residueSlugs(root)
	if err != nil {
		return nil, err
	}
	plan.ResidueSlugs = residue
	return plan, nil
}

func sortedKeys(m map[string]*AuthoredOnlyProjectPlan) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// countEntityLinesByOrigin counts the non-empty lines of an entities file by
// ClassifyEntityLine. A line that does not decode is an error.
func countEntityLinesByOrigin(path string) (authored, extracted int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), maxEntityLine)
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var e Entity
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return 0, 0, fmt.Errorf("line %d: %w", line, err)
		}
		if ClassifyEntityLine(e) == OriginAuthored {
			authored++
		} else {
			extracted++
		}
	}
	return authored, extracted, sc.Err()
}

// residueSlugs lists departed slugs (a record under Audits/departures/) whose
// palace/<slug>/ exists on disk but tracks nothing, so the directory holds only
// untracked legacy residue the migration removes.
func residueSlugs(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(departure.Dir)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read departures dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		s := strings.TrimSuffix(name, ".json")
		if slug.Validate(s) != nil {
			continue
		}
		dir := filepath.Join(root, "palace", s)
		if _, err := os.Stat(dir); err != nil {
			continue // no residue on disk
		}
		tracked, _, err := gitCmdStdin(root, 30*time.Second, "", "ls-files", "-z", "--", "palace/"+s)
		if err != nil {
			return nil, fmt.Errorf("list tracked palace/%s: %w", s, err)
		}
		if len(splitNUL([]byte(tracked))) == 0 {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out, nil
}

// --- The apply ---

// AuthoredOnlyApplyOptions configures ApplyAuthoredOnlyMigration.
type AuthoredOnlyApplyOptions struct {
	// Date is the UTC day (YYYY-MM-DD) written as both the marker value and the
	// tag suffix.
	Date string
	// EmptyVault selects the empty-vault path: skip the classify/drop steps and
	// write the vault-level Audits/.surface stamp.
	EmptyVault bool
	// Surface is the stamp value for the empty-vault path (surface.MCPSurfaceVersion).
	Surface int
	// Message is the full commit message, attestation bytes included.
	Message string
	// afterStage is a test seam, nil in production, called after staging and
	// before the commit.
	afterStage func() error
}

// AuthoredOnlyApplyResult is what the apply did.
type AuthoredOnlyApplyResult struct {
	CommitSHA    string
	Removed      []string
	ResidueSlugs []string
}

// ApplyAuthoredOnlyMigration performs the migration's single commit (ADR-014
// decision 11, "The steps"). The caller has already chosen and pushed the tag,
// run every precondition, and validated the attestation. On the normal path it
// classifies the KG (keeping and stamping authored records, deleting extracted
// ones and every derived drawer/ledger), rewrites each kg/entities.jsonl to its
// authored lines, writes the marker, calls the gated reconciler for the ignore
// lines, removes departed-slug residue, stages explicitly (never git add -A),
// and commits once. The empty-vault path skips the classify/drop steps and adds
// the vault-level Audits/.surface stamp.
func (v *Vault) ApplyAuthoredOnlyMigration(plan *AuthoredOnlyPlan, opts AuthoredOnlyApplyOptions) (*AuthoredOnlyApplyResult, error) {
	root := v.Root
	res := &AuthoredOnlyApplyResult{}
	var add []string // paths to `git add` explicitly (never -A)

	if !opts.EmptyVault {
		// Delete extracted triples and every derived drawer/ledger (index + disk).
		drop := plan.DropPaths()
		if err := dropDerivedPaths(root, drop, true); err != nil {
			return nil, err
		}
		res.Removed = append(res.Removed, drop...)
		// Rewrite each entities.jsonl to its authored lines (those with 0 authored
		// lines were dropped above), and stamp authored triple files.
		touched := map[string]bool{}
		for _, pr := range plan.Projects {
			if pr.EntitiesRel != "" && pr.EntitiesAuthored > 0 {
				if err := rewriteEntitiesAuthored(root, pr.EntitiesRel); err != nil {
					return nil, err
				}
				add = append(add, pr.EntitiesRel)
				touched[pr.Project] = true
			}
			for _, rel := range pr.StampTriples {
				if err := stampTripleAuthored(root, rel); err != nil {
					return nil, err
				}
				add = append(add, rel)
				touched[pr.Project] = true
			}
		}
		// A vault write under palace/<p>/ stamps palace/<p>/.surface
		// (atomicfile.Write's structural stamp). Stage that stamp so the
		// migrator's post-commit tree is clean (mig-S5); it is a surface stamp at
		// the current version, which only strengthens the gate.
		for p := range touched {
			rel := "palace/" + p + "/.surface"
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
				add = append(add, rel)
			}
		}
	}

	// Residue removal (untracked legacy palace/<slug>/ of departed slugs).
	for _, s := range plan.ResidueSlugs {
		if err := os.RemoveAll(filepath.Join(root, "palace", s)); err != nil {
			return nil, fmt.Errorf("remove legacy residue palace/%s: %w", s, err)
		}
		res.ResidueSlugs = append(res.ResidueSlugs, s)
	}

	// Marker first, then the ignore lines through the gated reconciler (mig-N5):
	// the reconciler emits the derived lines only on a vault that carries the
	// marker.
	m, err := surface.ReadVaultManifest(root)
	if err != nil {
		return nil, fmt.Errorf("read vault manifest: %w", err)
	}
	m.AuthoredOnly = surface.MarkerDate(opts.Date)
	if err := surface.WriteVaultManifest(root, m); err != nil {
		return nil, fmt.Errorf("write migration marker: %w", err)
	}
	if err := ReconcileVaultGitignore(root); err != nil {
		return nil, fmt.Errorf("reconcile .gitignore: %w", err)
	}
	add = append(add, ".gitignore")
	if err := GitAddForce(root, filepath.Join(".vibe-palace", "vault.toml")); err != nil {
		return nil, fmt.Errorf("stage marker: %w", err)
	}

	if opts.EmptyVault {
		stampDir := filepath.Join(root, "Audits")
		if err := surface.WriteStamp(stampDir, opts.Surface, ""); err != nil {
			return nil, fmt.Errorf("write Audits/.surface: %w", err)
		}
		add = append(add, filepath.Join("Audits", ".surface"))
	}

	if len(add) > 0 {
		if err := GitAdd(root, add...); err != nil {
			return nil, fmt.Errorf("stage migration files: %w", err)
		}
	}
	if opts.afterStage != nil {
		if err := opts.afterStage(); err != nil {
			return nil, err
		}
	}
	if _, _, err := gitCmdStdin(root, gitCommitTimeout, opts.Message, "commit", "-q", "-F", "-"); err != nil {
		return nil, fmt.Errorf("commit the migration: %w", err)
	}
	sha, err := gitCmd(root, 10*time.Second, "rev-parse", "--short", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve commit: %w", err)
	}
	res.CommitSHA = strings.TrimSpace(sha)
	return res, nil
}

// LargestTrackedBlobExcluding returns the size and vault-relative path of the
// largest tracked blob in HEAD that is NOT in exclude. The migration's plan uses
// it with the dropped set as exclude to predict the largest blob after the
// commit (12-S5), so it never reports the pre-commit maximum of a dropped
// drawer. Returns 0, "", nil when the remaining tree is empty.
func LargestTrackedBlobExcluding(root string, exclude map[string]bool) (int64, string, error) {
	out, _, err := gitCmdStdin(root, 2*time.Minute, "", "ls-tree", "-r", "-l", "-z", "HEAD")
	if err != nil {
		return 0, "", fmt.Errorf("list tracked blobs: %w", err)
	}
	var max int64
	var maxRel string
	for _, rec := range splitNUL([]byte(out)) {
		if rec == "" {
			continue
		}
		// "<mode> <type> <oid> <size>\t<path>"
		meta, rel, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		if exclude[rel] {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 4 {
			continue
		}
		size, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			continue // "-" for a non-blob (tree/commit); not a tracked blob
		}
		if size > max {
			max = size
			maxRel = rel
		}
	}
	return max, maxRel, nil
}

// rewriteEntitiesAuthored rewrites a kg/entities.jsonl, keeping only the lines
// ClassifyEntityLine calls authored and stamping each with origin: authored.
func rewriteEntitiesAuthored(root, rel string) error {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), maxEntityLine)
	for line := 1; sc.Scan(); line++ {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var e Entity
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if ClassifyEntityLine(e) != OriginAuthored {
			continue
		}
		e.Origin = OriginAuthored
		enc, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal entity line %d: %w", line, err)
		}
		buf.Write(enc)
		buf.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return atomicfile.Write(root, abs, buf.Bytes())
}

// stampTripleAuthored rewrites an authored triple file with origin: authored,
// in the indented form every tracked triple file uses.
func stampTripleAuthored(root, rel string) error {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	t, err := readTripleFile(abs)
	if err != nil {
		return err
	}
	t.Origin = OriginAuthored
	return writeTripleFile(root, abs, t)
}
