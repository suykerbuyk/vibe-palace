// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// inventoryDigestFixture writes one path of every inventory class under an
// allow-listed slug, plus vault-global specimens, so a split or merge plan
// digest over it depends on exactly how each class is treated.
func inventoryDigestFixture(t *testing.T, root string) {
	t.Helper()
	for rel, body := range map[string]string{
		"Projects/alpha/sessions/2026-09-23-aaaa-01.md":       "---\nproject: alpha\n---\n# note\n",
		"Projects/alpha/transcripts/x.manifest.json":          "{}\n",
		"Projects/alpha/transcripts/x.manifest.json.1234.bak": "old manifest\n",
		"Projects/alpha/doc/config.toml":                      "# a project document, not the retired config\n",
		"Projects/alpha/notes/.surface":                       "surface = 1\n",
		"Projects/alpha/notes/commit-log.anchor":              "deadbeef\n",
		"Projects/alpha/sessions/.local/scratch.txt":          "machine-local\n",
		"Projects/alpha/.vp-locks/lock":                       "",
		"palace/alpha/.local/imported-sessions.jsonl":         "{}\n",
		"palace/alpha/drawers/alpha/general/drawers.jsonl":    "{\"id\":\"d2\"}\n",
		"Audits/.surface":                                     "surface = 1\n",
		"Audits/2026-07-01-vault.md":                          "# an audit\n",
		"Knowledge/learnings/source-lesson.md":                "# a lesson\n",
	} {
		writeSplitFile(t, root, rel, body)
	}
}

// The digests below were recorded by running this test against the code
// BEFORE split's inventory rules were delegated to storage.ClassifyProjectPath
// (main at 93d4c90). They pin that the delegation changed no manifest: an
// outstanding plan digest an operator holds must still bind.
const (
	goldenSplitDigest = "68432f828318c95153ee9e1ab3af0c8ad216481dd9586ec59b68da91df7b2759"
	goldenMergeDigest = "d96533bf43f42d14fabab314d9f98edefb3f66682f2a09f918c1f1d878fb5c4f"
)

// TestSplitManifestDigestIsUnchangedByInventorySwitch is a guard that is green
// both before and after the switch by design; it goes red if the shared
// predicate ever classifies a path differently from the rules it replaced.
func TestSplitManifestDigestIsUnchangedByInventorySwitch(t *testing.T) {
	for i := 0; i < 2; i++ {
		root := splitFixtureVault(t, "alpha")
		inventoryDigestFixture(t, root)
		p := splitPlanParams(splitDest(t), "alpha")
		p.IncludeAudits = true
		p.IncludeLearnings = true
		res, err := callSplit(t, root, p)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		got, _ := res["manifest_sha256"].(string)
		if got != goldenSplitDigest {
			t.Errorf("run %d: split plan digest %s, golden %s", i+1, got, goldenSplitDigest)
		}
	}
}

// TestMergeManifestDigestIsUnchangedByInventorySwitch is the merge twin: merge
// walks the source with the same two names split does.
func TestMergeManifestDigestIsUnchangedByInventorySwitch(t *testing.T) {
	for i := 0; i < 2; i++ {
		src := mergeSourceVault(t, "alpha")
		inventoryDigestFixture(t, src)
		dest := mergeDestVault(t, "beta")
		p := mergeParams(src, "alpha")
		p.IncludeAudits = true
		p.IncludeLearnings = true
		res, err := callMerge(t, dest, p)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		got, _ := res["manifest_sha256"].(string)
		if got != goldenMergeDigest {
			t.Errorf("run %d: merge plan digest %s, golden %s", i+1, got, goldenMergeDigest)
		}
	}
}

// legacySplitSubtracted is split's own rule VERBATIM as it stood at 93d4c90,
// before it was delegated to storage.ClassifyProjectPath. It is frozen here,
// and only here, so the parity test below compares the shared predicate with
// the rule it replaced rather than with itself.
func legacySplitSubtracted(rel string) bool {
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	if base == ".surface" || base == "commit-log.anchor" {
		return true
	}
	if vaultfs.IsVaultProjectConfigPath(rel) {
		return true
	}
	for _, comp := range strings.Split(rel, "/") {
		if comp == ".local" || comp == ".vp-locks" {
			return true
		}
	}
	return false
}

// TestSplitSubtractedEqualsProjectInventory is a guard, green both ways by
// design: over every path shape the split, merge and inventory tests use, the
// delegated predicate must agree with the rule it replaced, and split must
// prune exactly the machine-local directory names.
func TestSplitSubtractedEqualsProjectInventory(t *testing.T) {
	corpus := []string{
		"Projects/alpha/resume.md", "Projects/alpha/iterations.md", "Projects/alpha/commit-log.md",
		"Projects/alpha/commit-log.anchor", "Projects/alpha/.surface", "Projects/alpha/config.toml",
		"Projects/alpha/doc/config.toml", "Projects/alpha/notes/.surface", "Projects/alpha/notes/commit-log.anchor",
		"Projects/alpha/sessions/.local/scratch.txt", "Projects/alpha/.vp-locks/lock",
		"Projects/alpha/transcripts/x.manifest.json.1234.bak", "Projects/alpha/.localish/x.md",
		"palace/alpha/.surface", "palace/alpha/.local/embed-cache/d1.vec",
		"palace/alpha/.local/imported-sessions.jsonl", "palace/alpha/kg/entities.jsonl",
		"palace/alpha/drawers/w/r/drawers.jsonl", "palace/alpha/config.toml",
		"palace/.local/embed-cache/alpha/d1.vec", "Audits/.surface", "Audits/2026-07-01-vault.md",
		"Knowledge/learnings/l.md", "Knowledge/.surface", ".surface", "config.toml",
	}
	for _, rel := range corpus {
		if got, want := splitSubtracted(rel), legacySplitSubtracted(rel); got != want {
			t.Errorf("splitSubtracted(%q) = %v, the rule it replaced said %v (class %s)",
				rel, got, want, storage.ClassifyProjectPath(rel))
		}
	}
	if len(splitPrunedDirs) != 2 || !splitPrunedDirs[".local"] || !splitPrunedDirs[".vp-locks"] {
		t.Errorf("splitPrunedDirs = %v, want exactly .local and .vp-locks", splitPrunedDirs)
	}
}
