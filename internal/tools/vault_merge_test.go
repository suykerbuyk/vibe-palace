// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// Every test in this file runs against TWO ephemeral vaults under t.TempDir():
// a source holding `alpha`/`beta` and a destination holding a disjoint slug.
// Neither is the operator's bound vault, and no fixture names a real
// confidentiality slug.

// mergeDestVault builds a format-1 destination vault that is a git repository
// with no remotes, pre-populated with the given (disjoint) slugs plus one
// vault-global learning and one audit report.
//
// The learning and the audit matter: they are the destination state the
// set-difference gate must find unchanged when include_* is false, and a
// destination with an EMPTY Knowledge/ would pass that gate vacuously.
func mergeDestVault(t *testing.T, slugs ...string) string {
	t.Helper()
	root := splitFixtureVault(t, slugs...)
	writeSplitFile(t, root, "Knowledge/learnings/dest-own-lesson.md", "# a lesson the destination already had\n")
	writeSplitFile(t, root, "Audits/2026-08-01-vault.md", "# an audit the destination already had\n")
	mergeGitInit(t, root)
	return root
}

// mergeSourceVault builds a format-1 source vault holding the given slugs plus
// its own vault-global artifacts, so copy-none has something to decline.
func mergeSourceVault(t *testing.T, slugs ...string) string {
	t.Helper()
	root := splitFixtureVault(t, slugs...)
	writeSplitFile(t, root, "Knowledge/learnings/source-lesson.md", "# a lesson from the source vault\n")
	writeSplitFile(t, root, "Audits/2026-07-01-vault.md", "# an audit from the source vault\n")
	return root
}

// mergeGitInit makes root a real repository. storage.ListRemotes shells out to
// `git remote`, and its error is a hard refusal — so a destination that is not
// a repository refuses every apply, which is correct and would otherwise make
// every test here fail for the wrong reason.
func mergeGitInit(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "test"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unavailable or failing (%v): %s", err, out)
		}
	}
}

func mergeParams(source string, slugs ...string) vaultMergeParams {
	return vaultMergeParams{Action: "plan", Source: source, Slugs: slugs}
}

// callMerge invokes the registered handler the way MCP would, so the tests
// exercise the tool's real entry point rather than only its internals.
func callMerge(t *testing.T, dest string, p vaultMergeParams) (map[string]any, error) {
	t.Helper()
	tool := VaultMergeTool(storage.NewVault(dest))
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	res, err := tool.Handler(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return m, nil
}

// mergePlanned runs plan and returns params carrying the digest it minted.
func mergePlanned(t *testing.T, dest, source string, slugs ...string) vaultMergeParams {
	t.Helper()
	p := mergeParams(source, slugs...)
	res, err := callMerge(t, dest, p)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	sha, _ := res["manifest_sha256"].(string)
	if sha == "" {
		t.Fatalf("plan returned no manifest_sha256: %v", res)
	}
	p.ManifestSHA256 = sha
	return p
}

// mergeTreeNames reads a vault's palace/ and Projects/ entries.
func mergeTreeNames(t *testing.T, root, tree string) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(root, tree))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s/%s: %v", root, tree, err)
	}
	var names []string
	for _, e := range ents {
		if tree == "palace" && e.Name() == ".local" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// ---------------------------------------------------------------------------
// Mutation proof 1: SAME-SLUG refuses.
// ---------------------------------------------------------------------------

// TestVaultMergePlan_SameSlugRefuses pins the v1 disjointness rule.
//
// It is a HARDCODED refusal, not a policy with a default and not a flag with
// one value — there is deliberately no on_slug_collision parameter to soften.
// The reason is concrete: iteration numbers are minted independently in each
// vault, so folding two histories of one slug together would have to renumber
// one side, and every "see iteration 214" citation in the surviving prose would
// then point at different work. A union that silently rewrites citations is
// worse than a refusal an operator can act on.
//
// The refusal must also NAME the task that owns the real problem, so the next
// reader does not conclude the capability is simply missing.
func TestVaultMergePlan_SameSlugRefuses(t *testing.T) {
	source := mergeSourceVault(t, "alpha", "beta")
	dest := mergeDestVault(t, "alpha", "gamma")

	// 🔴 plan AND apply, NOT verify. The collision check asks "does the
	// destination ALREADY hold this slug?", which is the right question before a
	// copy and the wrong one after: a correct merge has just put that slug
	// there. Running it at verify would make verify structurally impossible to
	// pass — the first draft of this tool did exactly that, and these tests
	// caught it. The subtest below covers what protects verify instead.
	for _, action := range []string{"plan", "apply"} {
		t.Run("action="+action, func(t *testing.T) {
			p := mergeParams(source, "alpha")
			p.Action = action
			p.ManifestSHA256 = strings.Repeat("c", 64)

			_, err := callMerge(t, dest, p)
			if err == nil {
				t.Fatal("a slug already present in the destination must refuse")
			}
			if !strings.Contains(err.Error(), "already exist in the destination") {
				t.Errorf("refusal must name the collision, got: %v", err)
			}
			if !strings.Contains(err.Error(), "first-class-task-migrate-action") {
				t.Errorf("refusal must point at the task that owns cross-project moves, got: %v", err)
			}
			if !apperr.IsCaller(err) {
				t.Errorf("a same-slug request is a CALLER fault, got: %v", err)
			}
		})
	}

	t.Run("verify cannot be reached for a colliding slug at all", func(t *testing.T) {
		// verify does not run the collision check, and does not need to. A
		// colliding request can never obtain a digest in the first place —
		// plan refuses to mint one — so there is no manifest_sha256 that
		// verify would accept, and every call refuses on the bind instead.
		// The protection is the same; it is enforced one step earlier.
		if _, err := callMerge(t, dest, mergeParams(source, "alpha")); err == nil {
			t.Fatal("plan must refuse to mint a digest for a colliding slug")
		}

		p := mergeParams(source, "alpha")
		p.Action = "verify"
		p.ManifestSHA256 = strings.Repeat("c", 64)
		_, err := callMerge(t, dest, p)
		if err == nil {
			t.Fatal("verify must refuse a digest no plan could have produced")
		}
		if !strings.Contains(err.Error(), "mismatch") {
			t.Errorf("verify's refusal here is the bind failing, got: %v", err)
		}
	})

	// The disjoint slug from the same source is fine — the refusal is per-slug,
	// not a blanket refusal of a source that happens to share any name.
	if _, err := callMerge(t, dest, mergeParams(source, "beta")); err != nil {
		t.Errorf("a disjoint slug from the same source must plan cleanly: %v", err)
	}
}

// TestVaultMergePlan_UnknownSlugRefuses pins the allow-list's other failure
// mode: a slug that is in neither tree of the SOURCE.
func TestVaultMergePlan_UnknownSlugRefuses(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	_, err := callMerge(t, dest, mergeParams(source, "alpha", "nonexistent"))
	if err == nil {
		t.Fatal("an unknown source slug must refuse")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("refusal must name the unknown slug, got: %v", err)
	}
	if !apperr.IsCaller(err) {
		t.Errorf("an unknown slug is a CALLER fault, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 2: a FORMAT-0 source refuses, before any copy.
// ---------------------------------------------------------------------------

// TestVaultMergeApply_Format0SourceRefusesBeforeCopy pins R17 on the merge
// side, where it bites harder than on split's.
//
// Merge's source is an arbitrary host path an operator typed — far likelier to
// be an old, unmigrated vault than the bound vault split reads. The data-format
// gate is a READ gate on knowledge-graph accessors, so copying old-encoding
// triple files fails nothing on its own; they simply land in a destination that
// is already stamped format 1 and report themselves current while QueryEntity
// undercounts. Nothing downstream ever raises an error.
//
// A syntactically valid but wrong digest is passed, so a handler that checked
// the bind before the format would fail with "mismatch" instead — the assertion
// on the error text is what distinguishes the two orderings. The destination
// must be untouched afterwards.
func TestVaultMergeApply_Format0SourceRefusesBeforeCopy(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	if err := os.RemoveAll(filepath.Join(source, ".vibe-palace")); err != nil {
		t.Fatalf("remove source format stamp: %v", err)
	}
	if got, err := surface.ReadFormat(source); err != nil || got != 0 {
		t.Fatalf("fixture source must read format 0, got %d (err %v)", got, err)
	}

	before := snapshotTree(t, dest)

	p := mergeParams(source, "alpha")
	p.Action = "apply"
	p.ManifestSHA256 = strings.Repeat("d", 64)

	_, err := callMerge(t, dest, p)
	if err == nil {
		t.Fatal("apply must refuse a format-0 source")
	}
	if !strings.Contains(err.Error(), "source vault is at data format 0") {
		t.Errorf("refusal must name the SOURCE data format (not the digest), got: %v", err)
	}
	if after := snapshotTree(t, dest); !equalStringMaps(before, after) {
		t.Error("a format refusal must land before any copy; the destination changed")
	}
}

// TestVaultMergeApply_Format0DestinationRefuses is the other half: the bound
// vault is checked too. It is likelier to be current, which is exactly why an
// unchecked assumption about it would survive a long time.
func TestVaultMergeApply_Format0DestinationRefuses(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	if err := os.RemoveAll(filepath.Join(dest, ".vibe-palace")); err != nil {
		t.Fatalf("remove destination format stamp: %v", err)
	}

	p := mergeParams(source, "alpha")
	p.Action = "apply"
	p.ManifestSHA256 = strings.Repeat("e", 64)

	_, err := callMerge(t, dest, p)
	if err == nil {
		t.Fatal("apply must refuse a format-0 destination")
	}
	if !strings.Contains(err.Error(), "destination vault is at data format 0") {
		t.Errorf("refusal must name the DESTINATION data format, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 3: a ListRemotes MISMATCH refuses apply.
// ---------------------------------------------------------------------------

// TestVaultMergeApply_RemoteAffirmationMismatchRefuses pins the publication
// gate.
//
// Merging a project into a vault that pushes somewhere is a publication
// decision about that project, and it is the decision this whole task exists to
// make safe. So apply refuses unless the caller restates the destination's
// remote set EXACTLY — not "at least", because a remote the caller did not name
// is a destination they were not thinking about when they approved the merge.
//
// The subtests cover both directions of mismatch, plus the case where an error
// from ListRemotes must be a refusal rather than an empty set: a destination
// that is not a repository cannot answer the question, and reading "no answer"
// as "no remotes" would make the gate pass on exactly the vault it should
// refuse.
func TestVaultMergeApply_RemoteAffirmationMismatchRefuses(t *testing.T) {
	t.Run("destination has a remote the caller did not affirm", func(t *testing.T) {
		source := mergeSourceVault(t, "alpha")
		dest := mergeDestVault(t, "gamma")
		mergeAddRemote(t, dest, "origin", "https://example.invalid/dest.git")

		p := mergePlanned(t, dest, source, "alpha")
		p.Action = "apply"
		// AffirmDestinationRemotes deliberately empty.

		before := snapshotTree(t, dest)
		_, err := callMerge(t, dest, p)
		if err == nil {
			t.Fatal("apply must refuse when the destination has an unaffirmed remote")
		}
		if !strings.Contains(err.Error(), "origin") {
			t.Errorf("refusal must name the actual remote, got: %v", err)
		}
		if !apperr.IsCaller(err) {
			t.Errorf("a bad affirmation is a CALLER fault, got: %v", err)
		}
		if after := snapshotTree(t, dest); !equalStringMaps(before, after) {
			t.Error("a refused apply must copy nothing")
		}
	})

	t.Run("caller affirms a remote the destination does not have", func(t *testing.T) {
		source := mergeSourceVault(t, "alpha")
		dest := mergeDestVault(t, "gamma")

		p := mergePlanned(t, dest, source, "alpha")
		p.Action = "apply"
		p.AffirmDestinationRemotes = []string{"origin"}

		before := snapshotTree(t, dest)
		if _, err := callMerge(t, dest, p); err == nil {
			t.Fatal("apply must refuse when the affirmation names a remote that is absent")
		}
		if after := snapshotTree(t, dest); !equalStringMaps(before, after) {
			t.Error("a refused apply must copy nothing")
		}
	})

	t.Run("an affirmed remote set that matches exactly is accepted", func(t *testing.T) {
		source := mergeSourceVault(t, "alpha")
		dest := mergeDestVault(t, "gamma")
		mergeAddRemote(t, dest, "origin", "https://example.invalid/dest.git")
		mergeAddRemote(t, dest, "backup", "https://example.invalid/backup.git")

		p := mergePlanned(t, dest, source, "alpha")
		p.Action = "apply"
		// Deliberately out of order: the gate compares sets, not typing order.
		p.AffirmDestinationRemotes = []string{"origin", "backup"}

		if _, err := callMerge(t, dest, p); err != nil {
			t.Fatalf("an exact affirmation must be accepted: %v", err)
		}
	})

	t.Run("a destination that is not a repository refuses", func(t *testing.T) {
		source := mergeSourceVault(t, "alpha")
		// Built WITHOUT mergeGitInit: no .git at all.
		dest := splitFixtureVault(t, "gamma")

		p := mergeParams(source, "alpha")
		p.Action = "apply"
		p.ManifestSHA256 = strings.Repeat("f", 64)

		_, err := callMerge(t, dest, p)
		if err == nil {
			t.Fatal("apply must refuse a destination that cannot answer for its remotes")
		}
		if !strings.Contains(err.Error(), "refusal, not an empty remote set") {
			t.Errorf("refusal must say an error is not an empty set, got: %v", err)
		}
	})
}

func mergeAddRemote(t *testing.T, root, name, url string) {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "remote", "add", name, url)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add %s: %v: %s", name, err, out)
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 4: after apply, dest ReadDir is pre-merge ∪ allow-list.
// ---------------------------------------------------------------------------

// TestVaultMergeApply_DestinationTreesArePreMergeUnionAllowList is leak gate 1,
// asserted against the filesystem by a test that CAN see both sides.
//
// The test holds the pre-merge snapshot because it took it; the tool cannot,
// because verify is a separate call. That asymmetry is why the manifest digest
// carries "destination slug names minus the allow-list" — an invariant across a
// correct merge — and why the companion test below proves the digest refuses
// when an unnamed slug appears.
//
// The source holds `beta` as well as `alpha`, and only `alpha` is requested: a
// merge that brought both would produce a destination that still verifies
// against its own row list while having leaked a whole project.
func TestVaultMergeApply_DestinationTreesArePreMergeUnionAllowList(t *testing.T) {
	source := mergeSourceVault(t, "alpha", "beta")
	dest := mergeDestVault(t, "gamma", "delta")

	beforePalace := mergeTreeNames(t, dest, "palace")
	beforeProjects := mergeTreeNames(t, dest, "Projects")

	p := mergePlanned(t, dest, source, "alpha")
	p.Action = "apply"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	for _, tc := range []struct {
		tree   string
		before []string
	}{{"palace", beforePalace}, {"Projects", beforeProjects}} {
		want := append(append([]string(nil), tc.before...), "alpha")
		sort.Strings(want)
		got := mergeTreeNames(t, dest, tc.tree)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("destination %s/ = %v, want pre-merge ∪ allow-list = %v", tc.tree, got, want)
		}
	}

	// beta was never asked for and is nowhere in the destination.
	for _, rel := range []string{"palace/beta", "Projects/beta"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err == nil {
			t.Errorf("destination holds %s, which was never in the allow-list", rel)
		}
	}

	// The content that was asked for actually arrived, and the subtract set did
	// not travel with it.
	for _, rel := range []string{
		"Projects/alpha/resume.md",
		"Projects/alpha/commit-log.md",
		"palace/alpha/kg/entities.jsonl",
	} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s did not travel: %v", rel, err)
		}
	}
	for _, rel := range []string{
		"Projects/alpha/commit-log.anchor",
		"palace/alpha/.local/embed-cache/d1.vec",
	} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s is in the subtract set and must not have travelled", rel)
		}
	}

	// The pre-existing destination projects are untouched.
	for _, s := range []string{"gamma", "delta"} {
		if _, err := os.Stat(filepath.Join(dest, "Projects", s, "resume.md")); err != nil {
			t.Errorf("pre-existing destination project %s was disturbed: %v", s, err)
		}
	}

	// And verify agrees.
	p.Action = "verify"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("verify must pass on a destination apply just wrote: %v", err)
	}
}

// TestVaultMergeVerify_UnnamedSlugArrivingRefuses is the half the tool can
// assert on its own, and it is what makes leak gate 1 real after the fact.
//
// verify cannot observe the pre-merge destination. What it CAN do is recompute
// "destination slug names minus the allow-list" — a quantity that is invariant
// across a correct merge, because same-slug is refused and a correct merge adds
// exactly the allow-list. The manifest digest carries that quantity from plan
// through apply, so a slug arriving that nobody named changes it, changes the
// digest, and refuses. This test injects exactly that.
func TestVaultMergeVerify_UnnamedSlugArrivingRefuses(t *testing.T) {
	source := mergeSourceVault(t, "alpha", "beta")
	dest := mergeDestVault(t, "gamma")

	p := mergePlanned(t, dest, source, "alpha")
	p.Action = "apply"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	p.Action = "verify"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("verify must pass before tampering: %v", err)
	}

	// A slug nobody named appears in the destination — the shape a leaking copy
	// path would produce.
	writeSplitFile(t, dest, "Projects/beta/resume.md", "# leaked from the source\n")

	_, err := callMerge(t, dest, p)
	if err == nil {
		t.Fatal("verify must refuse when a slug nobody named arrived in the destination")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("the refusal is the manifest bind failing, got: %v", err)
	}
}

// TestVaultMergeVerify_AlteredContentRefuses pins the row-level half.
func TestVaultMergeVerify_AlteredContentRefuses(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	p := mergePlanned(t, dest, source, "alpha")
	p.Action = "apply"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	writeSplitFile(t, dest, "Projects/alpha/resume.md", "# tampered\n")

	p.Action = "verify"
	_, err := callMerge(t, dest, p)
	if err == nil {
		t.Fatal("verify must refuse altered content")
	}
	if !strings.Contains(err.Error(), "content differs") {
		t.Errorf("failure must name the differing content, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 5: include_learnings false ⇒ dest Knowledge file-set unchanged.
// ---------------------------------------------------------------------------

// TestVaultMergeApply_VaultGlobalCopyNoneLeavesDestinationSetUnchanged pins R20.
//
// 🔴 THE TRAP THIS PINS IS A SHARED HELPER. Split's destination is a fresh vault,
// so split's leak gate 2 asserts "Knowledge/ is EMPTY unless include_*". Merge's
// destination is the operator's live vault, which already holds learnings and
// audit reports — so that same helper would fail EVERY merge that ever ran. The
// correct assertion here is a SET DIFFERENCE: the destination gained nothing.
//
// Both fixtures carry vault-global artifacts, so neither side of the difference
// is empty and the gate cannot pass vacuously. The source's learning is
// specifically checked for absence: it is the file that would arrive if
// copy-none were not the default.
func TestVaultMergeApply_VaultGlobalCopyNoneLeavesDestinationSetUnchanged(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	beforeKnowledge := snapshotTree(t, filepath.Join(dest, "Knowledge"))
	beforeAudits := snapshotTree(t, filepath.Join(dest, "Audits"))
	if len(beforeKnowledge) == 0 || len(beforeAudits) == 0 {
		t.Fatal("fixture must give the destination its own learnings and audits, " +
			"or this gate passes vacuously")
	}

	p := mergePlanned(t, dest, source, "alpha")
	p.Action = "apply"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if after := snapshotTree(t, filepath.Join(dest, "Knowledge")); !equalStringMaps(beforeKnowledge, after) {
		t.Errorf("include_learnings=false must leave the destination Knowledge file-set "+
			"unchanged\nbefore: %v\nafter:  %v", beforeKnowledge, after)
	}
	if after := snapshotTree(t, filepath.Join(dest, "Audits")); !equalStringMaps(beforeAudits, after) {
		t.Errorf("include_audits=false must leave the destination Audits file-set "+
			"unchanged\nbefore: %v\nafter:  %v", beforeAudits, after)
	}

	// The specific file that would have arrived is absent.
	if _, err := os.Stat(filepath.Join(dest, "Knowledge", "learnings", "source-lesson.md")); err == nil {
		t.Error("the source's learning travelled though include_learnings was false")
	}

	// Templates never travels and has no flag.
	if _, err := os.Stat(filepath.Join(dest, "Templates")); err == nil {
		t.Error("merge created a Templates/ tree in the destination")
	}
}

// TestVaultMergeApply_IncludeLearningsCarriesExactlyThoseFiles is the other
// side of the flag: when the operator DOES ask, the named class travels and
// nothing else does.
//
// Without this, "copy-none by default" would be indistinguishable from "copy
// never", and the flag would be dead code nobody noticed.
func TestVaultMergeApply_IncludeLearningsCarriesExactlyThoseFiles(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	beforeAudits := snapshotTree(t, filepath.Join(dest, "Audits"))

	p := mergeParams(source, "alpha")
	p.IncludeLearnings = true
	res, err := callMerge(t, dest, p)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	p.ManifestSHA256, _ = res["manifest_sha256"].(string)
	p.Action = "apply"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "Knowledge", "learnings", "source-lesson.md")); err != nil {
		t.Errorf("include_learnings=true must carry the source's learnings: %v", err)
	}
	// The destination's own learning is still there beside it.
	if _, err := os.Stat(filepath.Join(dest, "Knowledge", "learnings", "dest-own-lesson.md")); err != nil {
		t.Errorf("merge disturbed the destination's own learning: %v", err)
	}
	// Audits was NOT included, so its set is still unchanged.
	if after := snapshotTree(t, filepath.Join(dest, "Audits")); !equalStringMaps(beforeAudits, after) {
		t.Error("include_audits=false must leave the destination Audits file-set unchanged")
	}

	p.Action = "verify"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Errorf("verify must pass after an include_learnings merge: %v", err)
	}
}

// TestVaultMergePlan_RefusesToOverwriteAnExistingDestinationFile pins that
// merge ADDS and never REPLACES.
//
// Slug collisions are caught by the same-slug rule; this is that same
// disjointness applied to the artifacts that do NOT partition by slug. A
// learning with a colliding filename would otherwise be silently overwritten,
// and the destination's copy is the one that would be lost.
func TestVaultMergePlan_RefusesToOverwriteAnExistingDestinationFile(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")
	// The same learning filename on both sides, with different content.
	writeSplitFile(t, source, "Knowledge/learnings/shared-name.md", "# source version\n")
	writeSplitFile(t, dest, "Knowledge/learnings/shared-name.md", "# destination version\n")

	p := mergeParams(source, "alpha")
	p.IncludeLearnings = true

	_, err := callMerge(t, dest, p)
	if err == nil {
		t.Fatal("merge must refuse to overwrite an existing destination file")
	}
	if !strings.Contains(err.Error(), "shared-name.md") {
		t.Errorf("refusal must name the colliding file, got: %v", err)
	}
	if !strings.Contains(err.Error(), "never replaces") {
		t.Errorf("refusal must say merge adds and never replaces, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Source path, registration and surface honesty.
// ---------------------------------------------------------------------------

// TestVaultMergePlan_SourcePathRefusals pins the three ways a source path can
// be wrong.
//
// The inside-the-vault case is the sharp one: RefuseDestinationInsideVault
// reads backwards here, but its actual predicate — "this operator-typed host
// path resolves OUTSIDE the vault this server owns" — is exactly the question
// merge must ask about a source. A source resolving inside the bound vault
// would make the tool copy the destination onto itself.
func TestVaultMergePlan_SourcePathRefusals(t *testing.T) {
	dest := mergeDestVault(t, "gamma")

	for _, tc := range []struct {
		name   string
		source string
		want   string
	}{
		{"relative", "relative/other-vault", "absolute"},
		{"inside the bound vault", filepath.Join(dest, "nested-vault"), "separate vault"},
		{"absent", filepath.Join(t.TempDir(), "no-such-vault"), "not readable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := callMerge(t, dest, mergeParams(tc.source, "alpha"))
			if err == nil {
				t.Fatalf("source %q must refuse", tc.source)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal must mention %q, got: %v", tc.want, err)
			}
			if !apperr.IsCaller(err) {
				t.Errorf("a bad source path is a CALLER fault, got: %v", err)
			}
		})
	}

	t.Run("empty slugs", func(t *testing.T) {
		source := mergeSourceVault(t, "alpha")
		if _, err := callMerge(t, dest, mergeParams(source)); err == nil {
			t.Fatal("an empty allow-list must refuse rather than select everything")
		}
	})
}

// TestVaultMergePlan_WritesNothing is the claim the read-only predicate makes,
// checked against the filesystem rather than against the code.
//
// It snapshots BOTH vaults: plan reads the source and the destination, and a
// plan that stamped or created anything in either would make the read-only
// admission a lie a stale binary would act on.
func TestVaultMergePlan_WritesNothing(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	sourceBefore := snapshotTree(t, source)
	destBefore := snapshotTree(t, dest)

	if _, err := callMerge(t, dest, mergeParams(source, "alpha")); err != nil {
		t.Fatalf("plan: %v", err)
	}

	if after := snapshotTree(t, source); !equalStringMaps(sourceBefore, after) {
		t.Error("plan must not write to the source vault")
	}
	if after := snapshotTree(t, dest); !equalStringMaps(destBefore, after) {
		t.Error("plan must not write to the destination vault")
	}
}

// TestVaultMergeApply_DoesNotMutateTheSource pins that merge is one-directional.
func TestVaultMergeApply_DoesNotMutateTheSource(t *testing.T) {
	source := mergeSourceVault(t, "alpha", "beta")
	dest := mergeDestVault(t, "gamma")

	before := snapshotTree(t, source)

	p := mergePlanned(t, dest, source, "alpha")
	p.Action = "apply"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if after := snapshotTree(t, source); !equalStringMaps(before, after) {
		t.Error("merge apply must leave the source vault untouched")
	}
}

// TestVaultMergeApply_WithoutManifestSHARefuses pins the TOCTOU bind.
func TestVaultMergeApply_WithoutManifestSHARefuses(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	for _, action := range []string{"apply", "verify"} {
		t.Run("action="+action, func(t *testing.T) {
			p := mergeParams(source, "alpha")
			p.Action = action

			before := snapshotTree(t, dest)
			_, err := callMerge(t, dest, p)
			if err == nil {
				t.Fatalf("%s without manifest_sha256 must refuse", action)
			}
			if !strings.Contains(err.Error(), "manifest_sha256 is required") {
				t.Errorf("refusal must name the missing bind, got: %v", err)
			}
			if !apperr.IsCaller(err) {
				t.Errorf("a missing required parameter is a CALLER fault, got: %v", err)
			}
			if after := snapshotTree(t, dest); !equalStringMaps(before, after) {
				t.Errorf("a refused %s must change nothing", action)
			}
		})
	}
}

// TestVaultMergeApply_SourceChangedAfterPlanRefuses proves the bind is a real
// TOCTOU guard and not a checksum of the request.
func TestVaultMergeApply_SourceChangedAfterPlanRefuses(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	p := mergePlanned(t, dest, source, "alpha")
	p.Action = "apply"

	writeSplitFile(t, source, "Projects/alpha/resume.md", "# edited after the plan\n")

	if _, err := callMerge(t, dest, p); err == nil {
		t.Fatal("apply must refuse after the source changed under an approved plan")
	}
}

// TestVaultMergeUnimplementedActionRefuses pins that the handler re-checks the
// action rather than trusting the enum.
//
// It names only actions that must STAY refused. `purge` is the sharp one: it
// exists on vp_vault_split and deliberately does not exist here, because merge
// removes nothing from either vault — and a tool that accepted the word because
// its sibling does would be the honest-instruments defect wearing a familiar
// name.
func TestVaultMergeUnimplementedActionRefuses(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	dest := mergeDestVault(t, "gamma")

	for _, action := range []string{"purge", "split", "sync", "Plan", "", "plan "} {
		t.Run("action="+action, func(t *testing.T) {
			p := mergeParams(source, "alpha")
			p.Action = action

			before := snapshotTree(t, dest)
			_, err := callMerge(t, dest, p)
			if err == nil {
				t.Fatalf("action %q was accepted; it is not implemented", action)
			}
			if !apperr.IsCaller(err) {
				t.Errorf("an unimplemented action is a CALLER fault, got: %v", err)
			}
			if !strings.Contains(err.Error(), "plan, apply, verify") {
				t.Errorf("refusal must name the implemented actions, got: %v", err)
			}
			if after := snapshotTree(t, dest); !equalStringMaps(before, after) {
				t.Error("a refused action must change nothing")
			}
		})
	}
}

// TestVaultMergeSchemaAdvertisesExactlyWhatIsBuilt pins the enum against the
// dispatch, and pins the absence of every parameter this design refused.
func TestVaultMergeSchemaAdvertisesExactlyWhatIsBuilt(t *testing.T) {
	// 🔴 IT PARSES THE SCHEMA RATHER THAN GREPPING IT. The first version of this
	// test looked for the substring "exclude" and failed on the schema's own
	// sentence "there is no exclude parameter" — a correct file failing its own
	// test, which is how a real assertion gets weakened into uselessness. What
	// is actually being asserted is that no such PROPERTY exists and no such
	// enum MEMBER exists, and both are structure, not text.
	var parsed struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(vaultMergeSchema, &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}

	action, ok := parsed.Properties["action"]
	if !ok {
		t.Fatal("schema has no action property")
	}
	if strings.Join(action.Enum, ",") != "plan,apply,verify" {
		t.Errorf("action enum = %v, want exactly [plan apply verify]", action.Enum)
	}

	for _, banned := range []string{"on_slug_collision", "include_templates", "exclude", "destination"} {
		if _, present := parsed.Properties[banned]; present {
			t.Errorf("schema must not expose property %q: it was refused by the design", banned)
		}
	}
	for _, want := range []string{
		"source", "slugs", "include_learnings", "include_audits",
		"affirm_destination_remotes", "manifest_sha256",
	} {
		if _, present := parsed.Properties[want]; !present {
			t.Errorf("schema is missing property %q", want)
		}
	}
	if strings.Join(parsed.Required, ",") != "action,source,slugs" {
		t.Errorf("required = %v, want [action source slugs]: manifest_sha256 is enforced "+
			"per-action by the handler, not by the schema", parsed.Required)
	}
}

// TestVaultMergeReadOnlyPredicate pins the gate refinement.
func TestVaultMergeReadOnlyPredicate(t *testing.T) {
	for _, tc := range []struct {
		action   string
		readOnly bool
	}{
		{"plan", true},
		{"verify", true},
		{"apply", false},
		{"purge", false},
		{"", false},
	} {
		raw := []byte(`{"action":"` + tc.action + `","source":"/tmp/x","slugs":["alpha"]}`)
		if got := vaultMergeReadOnly(raw); got != tc.readOnly {
			t.Errorf("action %q: read-only = %v, want %v", tc.action, got, tc.readOnly)
		}
	}
	if vaultMergeReadOnly([]byte(`{"action":42}`)) {
		t.Error("a payload that will not unmarshal must be gated, not admitted as read-only")
	}
}

// TestVaultMergeToolIsGatedAndParamAware pins the three registration
// declarations that must move together with the tool.
func TestVaultMergeToolIsGatedAndParamAware(t *testing.T) {
	tool := VaultMergeTool(storage.NewVault(t.TempDir()))
	if !tool.Mutating {
		t.Error("vp_vault_merge must be registered Mutating: apply writes")
	}
	if tool.ReadOnlyWhen == nil {
		t.Error("vp_vault_merge must carry a ReadOnlyWhen predicate, or plan is refused by a stale binary")
	}
	if !containsSplitName(MutatingToolNames, "vp_vault_merge") {
		t.Error("vp_vault_merge is missing from MutatingToolNames")
	}
	if !containsSplitName(ParamAwareToolNames, "vp_vault_merge") {
		t.Error("vp_vault_merge is missing from ParamAwareToolNames")
	}
	if containsSplitName(ReadOnlyServeToolNames, "vp_vault_merge") {
		t.Error("vp_vault_merge must never appear on ReadOnlyServeToolNames: apply writes")
	}
}

// TestVaultMergeUsesNoRecursiveRemovalOrRemoteConfiguration is a source
// assertion, and it is deliberately one.
//
// The behavioural tests prove the destination gained the right files; they
// cannot prove the tool never reached for a primitive it was forbidden. Merge
// removes nothing from either vault and configures no remote — both are
// absences, and an absence is only checkable against the source.
func TestVaultMergeUsesNoRecursiveRemovalOrRemoteConfiguration(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "vault_merge.go", nil, 0)
	if err != nil {
		t.Fatalf("parse vault_merge.go: %v", err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "RemoveAll", "Remove", "RemoveNoLock", "Rename", "RenameNoLock":
			t.Errorf("vault_merge.go:%d calls %s: merge removes nothing from either vault",
				fset.Position(sel.Pos()).Line, sel.Sel.Name)
		case "ImportVibeVault":
			t.Errorf("vault_merge.go:%d calls migrate.ImportVibeVault: it is prior art for "+
				"two-handle mechanics only, and its destination markers land in "+
				"palace/{p}/.local", fset.Position(sel.Pos()).Line)
		}
		return true
	})

	src, err := os.ReadFile("vault_merge.go")
	if err != nil {
		t.Fatalf("read vault_merge.go: %v", err)
	}
	// The one construct with no AST shape worth matching: a git subcommand
	// string. Merge reads remotes and never configures one.
	if strings.Contains(string(src), `"remote", "add"`) {
		t.Error("vault_merge.go configures a git remote: the operator wires remotes by hand")
	}
}
