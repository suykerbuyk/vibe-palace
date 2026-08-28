// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// Every test in this file runs against EPHEMERAL vaults under t.TempDir(), and
// the fixture slugs are `alpha` and `beta`, which name no real project. Nothing
// here reads, writes or removes anything in the operator's bound vault — which
// matters more for this file than for the plan tests, because these actions
// create, copy and DELETE.

// splitDest returns a destination path that does NOT exist. apply refuses a
// destination that exists at all, so every happy-path test must name one inside
// a temp dir rather than the temp dir itself.
func splitDest(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "new-vault")
}

// splitPlannedParams runs plan and returns params carrying the digest it
// minted, ready for apply/verify/purge. Going through the real plan call rather
// than reaching for buildSplitManifest is deliberate: the bind these actions
// enforce is against a digest a CALLER received, and a test that mints its own
// would not exercise that path.
func splitPlannedParams(t *testing.T, root, dest string, slugs ...string) vaultSplitParams {
	t.Helper()
	p := splitPlanParams(dest, slugs...)
	res, err := callSplit(t, root, p)
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

// ---------------------------------------------------------------------------
// Mutation proof 1: apply WITHOUT manifest_sha256 refuses.
// ---------------------------------------------------------------------------

// TestVaultSplitApply_WithoutManifestSHARefuses pins the bind that makes every
// other guarantee in this slice meaningful.
//
// The manifest is server-side: a caller never carries the rows, only the
// digest. That digest is therefore the ENTIRE link between what an operator
// read and approved in a plan and what apply is about to copy. Without it,
// apply would be "copy whatever the source looks like right now" — which is not
// the operation anyone approved, and would silently include a project added
// between the two calls.
//
// It must also refuse BEFORE creating anything. A destination scaffolded by a
// call that was going to be refused anyway is a destination the next apply then
// refuses for existing, which strands the operation on its own debris.
func TestVaultSplitApply_WithoutManifestSHARefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := splitDest(t)

	p := splitPlanParams(dest, "alpha")
	p.Action = "apply"
	// ManifestSHA256 deliberately left empty.

	_, err := callSplit(t, root, p)
	if err == nil {
		t.Fatal("apply without manifest_sha256 must refuse")
	}
	if !strings.Contains(err.Error(), "manifest_sha256 is required") {
		t.Errorf("refusal must name the missing bind, got: %v", err)
	}
	if !apperr.IsCaller(err) {
		t.Errorf("a missing required parameter is a CALLER fault, got: %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("refused apply must create nothing at %s (stat err: %v)", dest, statErr)
	}
}

// TestVaultSplitApply_WrongManifestSHARefuses is the other half of the bind: a
// digest that is present but does not describe the source is refused too.
//
// Without this, "required" would mean "non-empty" and any string would pass.
func TestVaultSplitApply_WrongManifestSHARefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := splitDest(t)

	p := splitPlanParams(dest, "alpha")
	p.Action = "apply"
	p.ManifestSHA256 = strings.Repeat("0", 64)

	_, err := callSplit(t, root, p)
	if err == nil {
		t.Fatal("apply with a manifest_sha256 that does not match the source must refuse")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("refusal must name the mismatch, got: %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("refused apply must create nothing at %s", dest)
	}
}

// TestVaultSplitApply_SourceChangedAfterPlanRefuses proves the bind is a real
// TOCTOU guard and not a checksum of the request.
//
// The digest covers the source BYTES, so a file that changes between plan and
// apply invalidates it. This is the failure the two-call shape exists to catch:
// an operator reads a plan, walks away, something writes to the vault, and the
// apply they come back to would otherwise copy a tree nobody reviewed.
func TestVaultSplitApply_SourceChangedAfterPlanRefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")
	p.Action = "apply"

	// The source moves under the approved plan.
	writeSplitFile(t, root, "Projects/alpha/resume.md", "# resume, edited after the plan\n")

	_, err := callSplit(t, root, p)
	if err == nil {
		t.Fatal("apply must refuse after the source changed under an approved plan")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("refusal must name the mismatch, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 2: a destination that ALREADY EXISTS refuses.
// ---------------------------------------------------------------------------

// TestVaultSplitApply_ExistingDestinationRefuses pins the R1 finding, and the
// case it pins is the one an older, more obvious guard misses.
//
// VaultReconciler.Plan emits ActionCreate only when the destination directory
// is ABSENT, and the vault data-format stamp is written only inside that branch.
// An operator who runs `mkdir -p /path/to/new-vault` first therefore gets
// ActionUnchanged, no stamp, and a vault born at format 0 — which then receives
// current-format data and reports itself unmigrated forever. A guard phrased as
// "refuse if the destination already contains palace/ or Projects/" passes this
// case cleanly, which is why the rule is the stricter one: refuse if the
// destination exists AT ALL.
//
// The subtests are the two shapes that matter: an EMPTY directory (the one the
// weaker guard misses) and a non-empty one.
func TestVaultSplitApply_ExistingDestinationRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(t *testing.T, dest string)
	}{
		{
			name: "empty directory pre-created by mkdir",
			prepare: func(t *testing.T, dest string) {
				if err := os.MkdirAll(dest, 0o755); err != nil {
					t.Fatalf("mkdir dest: %v", err)
				}
			},
		},
		{
			name: "directory holding unrelated content",
			prepare: func(t *testing.T, dest string) {
				if err := os.MkdirAll(filepath.Join(dest, "notes"), 0o755); err != nil {
					t.Fatalf("mkdir dest: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dest, "notes", "x.md"), []byte("x"), 0o644); err != nil {
					t.Fatalf("write dest file: %v", err)
				}
			},
		},
		{
			name: "a plain file at the destination path",
			prepare: func(t *testing.T, dest string) {
				if err := os.WriteFile(dest, []byte("not a vault"), 0o644); err != nil {
					t.Fatalf("write dest file: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := splitFixtureVault(t, "alpha")
			dest := splitDest(t)
			p := splitPlannedParams(t, root, dest, "alpha")
			p.Action = "apply"
			tc.prepare(t, dest)

			before := snapshotTree(t, dest)

			_, err := callSplit(t, root, p)
			if err == nil {
				t.Fatal("apply must refuse a destination that already exists")
			}
			if !strings.Contains(err.Error(), "already exists") {
				t.Errorf("refusal must say the destination exists, got: %v", err)
			}
			if !apperr.IsCaller(err) {
				t.Errorf("a bad destination is a CALLER fault, got: %v", err)
			}
			if after := snapshotTree(t, dest); !equalStringMaps(before, after) {
				t.Errorf("refused apply must not touch the destination\nbefore: %v\nafter:  %v", before, after)
			}
		})
	}
}

// TestVaultSplitApply_RelativeDestinationRefuses pins that a destination is an
// absolute host path.
//
// A relative one resolves against the SERVER's working directory — a location
// no caller can see and which has nothing to do with either vault. The schema
// has always described the parameter as an absolute path; this is that sentence
// with a refusal behind it.
func TestVaultSplitApply_RelativeDestinationRefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	p := splitPlanParams("relative/new-vault", "alpha")
	p.Action = "apply"
	p.ManifestSHA256 = strings.Repeat("a", 64)

	_, err := callSplit(t, root, p)
	if err == nil {
		t.Fatal("apply must refuse a relative destination")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("refusal must name the absolute-path rule, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 3: a FORMAT-0 source refuses, before any copy.
// ---------------------------------------------------------------------------

// TestVaultSplitApply_Format0SourceRefusesBeforeCopy pins R17.
//
// The data-format gate is a READ gate on knowledge-graph accessors, not on file
// copies — so nothing about copying a format-0 vault fails on its own. That is
// exactly the danger: the destination is scaffolded by the reconciler and is
// therefore born at format 1, so a copy of old-encoding triple files lands in a
// vault that declares itself current. QueryEntity and ListTriples then
// undercount silently, and no error is ever raised anywhere.
//
// The refusal must land BEFORE the destination is created, not merely before
// the copy loop: a scaffolded destination is the thing the next apply refuses
// to reuse, so refusing late would strand the operator on debris.
//
// The test passes a syntactically valid but wrong digest, so a handler that
// checked the bind before the format would fail with "mismatch" instead — the
// assertion on the error text is what distinguishes the two orderings.
func TestVaultSplitApply_Format0SourceRefusesBeforeCopy(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	// Un-stamp the source: absence of .vibe-palace/vault.toml IS format 0.
	if err := os.RemoveAll(filepath.Join(root, ".vibe-palace")); err != nil {
		t.Fatalf("remove source format stamp: %v", err)
	}
	if got, err := surface.ReadFormat(root); err != nil || got != 0 {
		t.Fatalf("fixture source must read format 0, got %d (err %v)", got, err)
	}

	dest := splitDest(t)
	p := splitPlanParams(dest, "alpha")
	p.Action = "apply"
	p.ManifestSHA256 = strings.Repeat("b", 64)

	_, err := callSplit(t, root, p)
	if err == nil {
		t.Fatal("apply must refuse a format-0 source")
	}
	if !strings.Contains(err.Error(), "data format 0") {
		t.Errorf("refusal must name the source data format (not the digest), got: %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Errorf("format refusal must land before the destination is scaffolded, but %s exists", dest)
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 4: after apply, destination ReadDir is EXACTLY the allow-list.
// ---------------------------------------------------------------------------

// TestVaultSplitApply_DestinationTreesAreExactlyTheAllowList is leak gate 1
// asserted directly against the filesystem, and it is the proof the whole
// fail-closed-on-leak requirement rests on.
//
// It reads the destination with os.ReadDir rather than through
// ListAllProjects — R14. ListAllProjects keeps only directories whose names
// pass slug.Validate and silently drops files, symlinks and invalid-slug
// directories, so it applies the very filter the copy path already applied and
// would report a clean tree by construction. ReadDir sees what is actually
// there.
//
// The fixture vault holds `beta` as well as `alpha`, and only `alpha` is
// requested: a split that copied both would produce a destination that verifies
// against its own manifest and still leaks a whole project.
func TestVaultSplitApply_DestinationTreesAreExactlyTheAllowList(t *testing.T) {
	root := splitFixtureVault(t, "alpha", "beta")
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")
	p.Action = "apply"

	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	for _, tree := range []string{"palace", "Projects"} {
		ents, err := os.ReadDir(filepath.Join(dest, tree))
		if err != nil {
			t.Fatalf("read destination %s/: %v", tree, err)
		}
		var names []string
		for _, e := range ents {
			if !e.IsDir() {
				t.Errorf("destination %s/%s is not a directory (type %s)", tree, e.Name(), e.Type())
			}
			names = append(names, e.Name())
		}
		sort.Strings(names)
		if len(names) != 1 || names[0] != "alpha" {
			t.Errorf("destination %s/ = %v, want exactly [alpha]", tree, names)
		}
	}

	// The other half of the gate: nothing arrived ALONGSIDE the two trees.
	rootEnts, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read destination root: %v", err)
	}
	for _, e := range rootEnts {
		if !splitDestRootAllowed[e.Name()] {
			t.Errorf("destination root holds unexpected %q", e.Name())
		}
	}

	// Vault-global artifacts stayed behind. Neither include_ flag was set.
	for _, rel := range []string{"Knowledge", "Audits"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err == nil {
			t.Errorf("destination has %s/ though it was not included", rel)
		}
	}

	// And beta's bytes are nowhere in the destination at all.
	for _, rel := range []string{"palace/beta", "Projects/beta"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err == nil {
			t.Errorf("destination holds %s, which was never in the allow-list", rel)
		}
	}
}

// TestVaultSplitApply_CopiesTheManifestAndNothingElse pins the subtract set
// across the copy, and pins that the source is untouched.
//
// commit-log.anchor is the sharp one: it names a SHA in the source's git
// history, and the destination is a fresh repository where that SHA does not
// exist. Copying it would land a dangling reference that reads as authoritative.
// commit-log.md — the history itself — does travel.
func TestVaultSplitApply_CopiesTheManifestAndNothingElse(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")
	p.Action = "apply"

	sourceBefore := snapshotTree(t, root)

	res, err := callSplit(t, root, p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res["complete"] != true {
		t.Errorf("apply payload must end with complete:true, got %v", res["complete"])
	}
	if got := res["dest_format"]; got != float64(surface.RequiredDataFormat) {
		t.Errorf("dest_format = %v, want %d", got, surface.RequiredDataFormat)
	}

	for _, rel := range []string{
		"palace/alpha/kg/entities.jsonl",
		"palace/alpha/drawers/w/r/drawers.jsonl",
		"Projects/alpha/resume.md",
		"Projects/alpha/iterations.md",
		"Projects/alpha/commit-log.md",
	} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s did not travel: %v", rel, err)
		}
	}

	// The subtract set. Every one of these is a file that EXISTS in the source
	// fixture, so its absence here is a decision and not an accident.
	for _, rel := range []string{
		"Projects/alpha/commit-log.anchor",
		"palace/alpha/.local/embed-cache/d1.vec",
	} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err == nil {
			t.Errorf("%s is in the subtract set and must not have travelled", rel)
		}
	}

	// The destination's own stamps are self-written: surface-version only, with
	// none of the provenance fields this binary never writes. Comparing them to
	// WriterFingerprint(dest) would be checking a field WriteStamp does not
	// persist (R12) — this checks the thing that IS observable.
	for _, dir := range []string{"palace/alpha", "Projects/alpha"} {
		st, err := surface.ReadStamp(filepath.Join(dest, filepath.FromSlash(dir)))
		if err != nil {
			t.Fatalf("read destination stamp %s: %v", dir, err)
		}
		if st.LastWriter != "" || st.LastWriteAt != "" {
			t.Errorf("destination stamp %s carries inherited provenance %+v", dir, st)
		}
	}

	if after := snapshotTree(t, root); !equalStringMaps(sourceBefore, after) {
		t.Error("apply must not mutate the source vault")
	}
}

// TestVaultSplitVerify_PassesAfterApplyAndCatchesTampering pins both directions
// of the inventory comparison.
//
// A missing file is an incomplete copy. An EXTRA file is a leak — and it is the
// direction a "did everything arrive?" check would never look in, which is why
// verify compares both ways.
func TestVaultSplitVerify_PassesAfterApplyAndCatchesTampering(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")

	p.Action = "apply"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	p.Action = "verify"
	res, err := callSplit(t, root, p)
	if err != nil {
		t.Fatalf("verify must pass on a destination apply just wrote: %v", err)
	}
	if res["complete"] != true {
		t.Errorf("verify payload must end with complete:true, got %v", res["complete"])
	}
	if remotes, ok := res["dest_remotes"].([]any); ok && len(remotes) > 0 {
		t.Errorf("split configures no remotes, got %v", remotes)
	}

	t.Run("a slug that was never in the allow-list fails verify", func(t *testing.T) {
		writeSplitFile(t, dest, "Projects/gamma/resume.md", "# leaked\n")
		if _, err := callSplit(t, root, p); err == nil {
			t.Fatal("verify must fail when an unlisted slug is present in the destination")
		} else if !strings.Contains(err.Error(), "allow-list") {
			t.Errorf("failure must name the allow-list, got: %v", err)
		}
		if err := os.RemoveAll(filepath.Join(dest, "Projects", "gamma")); err != nil {
			t.Fatalf("clean up: %v", err)
		}
	})

	t.Run("altered content fails verify", func(t *testing.T) {
		writeSplitFile(t, dest, "Projects/alpha/resume.md", "# tampered\n")
		if _, err := callSplit(t, root, p); err == nil {
			t.Fatal("verify must fail when destination content differs from the manifest")
		} else if !strings.Contains(err.Error(), "content differs") {
			t.Errorf("failure must name the differing content, got: %v", err)
		}
	})
}

// TestVaultSplitVerify_MissingDestinationRefuses pins that verify does not
// invent a passing result for a destination that was never applied.
func TestVaultSplitVerify_MissingDestinationRefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")
	p.Action = "verify"

	if _, err := callSplit(t, root, p); err == nil {
		t.Fatal("verify must refuse a destination that does not exist")
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 5: purge removes the source trees — and never with RemoveAll.
// ---------------------------------------------------------------------------

// TestVaultSplitPurge_RemovesSourceTreesAfterVerify pins the destructive half.
//
// purge walks: regular files through vaultfs.Delete (which owns the
// .git-segment refusal, containment, the per-path lock and the compare-and-set)
// and then empty directories bottom-up through vaultfs.RemoveNoLock. There is
// no recursive removal primitive in the tree and this action does not become
// the first one.
//
// The fixture holds `beta` alongside `alpha` so the test can assert the blast
// radius: an unrequested project must be exactly as it was.
func TestVaultSplitPurge_RemovesSourceTreesAfterVerify(t *testing.T) {
	root := splitFixtureVault(t, "alpha", "beta")
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")

	betaBefore := snapshotTree(t, filepath.Join(root, "Projects", "beta"))

	p.Action = "apply"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	p.Action = "purge"
	res, err := callSplit(t, root, p)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if res["complete"] != true {
		t.Errorf("purge payload must end with complete:true, got %v", res["complete"])
	}
	if got, _ := res["files_removed"].(float64); got == 0 {
		t.Error("purge reported removing no files")
	}

	// The allow-listed trees are gone, root and all.
	for _, rel := range []string{"palace/alpha", "Projects/alpha"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s must be gone after purge (stat err: %v)", rel, err)
		}
	}
	// Including the subtract-set files inside them: those never travelled, but
	// the tree containing them is going away, so they go too.
	if _, err := os.Stat(filepath.Join(root, "Projects", "alpha", "commit-log.anchor")); !os.IsNotExist(err) {
		t.Error("purge must remove subtract-set files inside a purged tree")
	}

	// The unrequested project is untouched.
	if after := snapshotTree(t, filepath.Join(root, "Projects", "beta")); !equalStringMaps(betaBefore, after) {
		t.Error("purge must not touch a project outside the allow-list")
	}

	// The destination still holds everything.
	for _, rel := range []string{"Projects/alpha/resume.md", "palace/alpha/kg/entities.jsonl"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(rel))); err != nil {
			t.Errorf("destination lost %s: %v", rel, err)
		}
	}
}

// TestVaultSplitPurge_RefusesWithoutAVerifiedDestination is the guard that
// makes purge safe to expose at all.
//
// The destination is the only other copy of what purge is about to delete. That
// an operator ran verify a moment ago is not a fact this process can observe,
// so purge re-runs it rather than trusting a claim. Without this the tool would
// happily delete a project whose destination was never written.
func TestVaultSplitPurge_RefusesWithoutAVerifiedDestination(t *testing.T) {
	t.Run("destination never applied", func(t *testing.T) {
		root := splitFixtureVault(t, "alpha")
		dest := splitDest(t)
		p := splitPlannedParams(t, root, dest, "alpha")
		p.Action = "purge"

		before := snapshotTree(t, root)
		if _, err := callSplit(t, root, p); err == nil {
			t.Fatal("purge must refuse when the destination does not exist")
		}
		if after := snapshotTree(t, root); !equalStringMaps(before, after) {
			t.Error("a refused purge must remove nothing from the source")
		}
	})

	t.Run("destination applied then damaged", func(t *testing.T) {
		root := splitFixtureVault(t, "alpha")
		dest := splitDest(t)
		p := splitPlannedParams(t, root, dest, "alpha")

		p.Action = "apply"
		if _, err := callSplit(t, root, p); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if err := os.Remove(filepath.Join(dest, "Projects", "alpha", "resume.md")); err != nil {
			t.Fatalf("damage destination: %v", err)
		}

		before := snapshotTree(t, root)
		p.Action = "purge"
		_, err := callSplit(t, root, p)
		if err == nil {
			t.Fatal("purge must refuse when the destination no longer verifies")
		}
		if !strings.Contains(err.Error(), "still") || !strings.Contains(err.Error(), "only copy") {
			t.Errorf("refusal must say the source is still the only copy, got: %v", err)
		}
		if after := snapshotTree(t, root); !equalStringMaps(before, after) {
			t.Error("a refused purge must remove nothing from the source")
		}
	})
}

// TestVaultSplitPurge_WithoutManifestSHARefuses pins purge's own bind.
//
// It is a SEPARATE bind from apply's, not an inherited one: purge is a distinct
// call that deletes, and it re-derives and re-checks the digest itself rather
// than treating "an apply happened earlier" as authorisation.
func TestVaultSplitPurge_WithoutManifestSHARefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")

	p.Action = "apply"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}

	before := snapshotTree(t, root)
	p.Action = "purge"
	p.ManifestSHA256 = ""
	_, err := callSplit(t, root, p)
	if err == nil {
		t.Fatal("purge without manifest_sha256 must refuse")
	}
	if !apperr.IsCaller(err) {
		t.Errorf("a missing required parameter is a CALLER fault, got: %v", err)
	}
	if after := snapshotTree(t, root); !equalStringMaps(before, after) {
		t.Error("a refused purge must remove nothing from the source")
	}
}

// TestVaultSplitPurge_UsesNoRecursiveRemoval is a source assertion, not a
// behavioural one, and it is deliberately both.
//
// The behavioural tests above prove the trees are gone; they cannot prove HOW.
// os.RemoveAll would pass every one of them while bypassing the .git-segment
// refusal, ResolveSafePath containment, the per-path advisory lock and the
// compare-and-set that vaultfs.Delete owns — and it would do so silently, which
// is exactly the class of defect a passing test suite hides. So the ban is
// asserted against the source text of the files that implement the split.
func TestVaultSplitPurge_UsesNoRecursiveRemoval(t *testing.T) {
	// It walks the AST rather than grepping the text, and that is not
	// fastidiousness: the ban is on the CALL, and these files discuss the ban in
	// their own comments. A substring check would fire on the prose explaining
	// why the call is absent — a test that fails on a correct file teaches
	// people to weaken it, which is how the real assertion gets lost.
	for _, name := range []string{"vault_split.go", "vault_split_apply.go"} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// Any RemoveAll, from any package. Naming only os.RemoveAll would
			// pin the receiver rather than the operation, and a recursive
			// removal reached through a different package is the same defect.
			if sel.Sel.Name == "RemoveAll" {
				t.Errorf("%s:%d calls RemoveAll: removal goes through vaultfs.Delete "+
					"and vaultfs.RemoveNoLock, which carry the .git-segment refusal, "+
					"containment, the per-path lock and the compare-and-set that a "+
					"recursive primitive skips silently",
					name, fset.Position(sel.Pos()).Line)
			}
			return true
		})
	}
}

// TestVaultSplitReadOnlyPredicateCoversSliceTwo pins the gate refinement across
// the widened enum.
//
// The predicate is an ALLOW-LIST of the actions proven to write nothing, never a
// deny-list of the ones known to write. A fifth action added tomorrow is
// therefore refused by a stale binary until someone deliberately names it —
// whereas a deny-list would admit it by default, and that mistake is silent.
func TestVaultSplitReadOnlyPredicateCoversSliceTwo(t *testing.T) {
	for _, tc := range []struct {
		action   string
		readOnly bool
	}{
		{"plan", true},
		{"verify", true},
		{"apply", false},
		{"purge", false},
		{"merge", false},
		{"", false},
	} {
		raw := []byte(`{"action":"` + tc.action + `","slugs":["alpha"],"destination":"/tmp/x"}`)
		if got := vaultSplitReadOnly(raw); got != tc.readOnly {
			t.Errorf("action %q: read-only = %v, want %v", tc.action, got, tc.readOnly)
		}
	}

	// An unmarshal failure must gate, not admit.
	if vaultSplitReadOnly([]byte(`{"action":42}`)) {
		t.Error("a payload that will not unmarshal must be gated, not admitted as read-only")
	}
}

// TestVaultSplitSchemaAdvertisesExactlyWhatIsBuilt pins the enum against the
// dispatch.
//
// A tool that lists an action it cannot perform is the honest-instruments
// defect in its purest form. vp_vault_merge is named throughout the design and
// is NOT built, so nothing about it may appear on this surface.
func TestVaultSplitSchemaAdvertisesExactlyWhatIsBuilt(t *testing.T) {
	schema := string(vaultSplitSchema)
	for _, action := range []string{"plan", "apply", "verify", "purge"} {
		if !strings.Contains(schema, `"`+action+`"`) {
			t.Errorf("schema enum is missing the implemented action %q", action)
		}
	}
	if strings.Contains(schema, "merge") {
		t.Error("schema must not mention merge: vp_vault_merge is not built")
	}
	if !strings.Contains(schema, "manifest_sha256") {
		t.Error("schema must expose manifest_sha256: apply, verify and purge all require it")
	}
	// That the HANDLER also refuses an action outside this enum — rather than
	// leaning on the schema validator one layer up — is pinned by
	// TestVaultSplitUnimplementedActionRefuses. It is not repeated here: two
	// tests asserting one property drift apart, and the survivor is believed.
}

// equalStringMaps compares two snapshotTree results.
func equalStringMaps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
