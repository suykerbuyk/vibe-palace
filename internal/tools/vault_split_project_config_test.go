// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The retired per-project vault config, Projects/<slug>/config.toml, is in the
// split subtract set, anchored by path DEPTH. These tests pin both halves of
// that anchor and that subtracting the file did not strand purge.

// TestVaultSplit_ProjectConfigDoesNotTravel: the project config is absent from
// the hashed inventory and from the destination, while a same-named file one
// level deeper travels. The deeper file is the reason the match is by depth and
// not by base name.
func TestVaultSplit_ProjectConfigDoesNotTravel(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	writeSplitFile(t, root, "Projects/alpha/doc/config.toml", "# a document, not the project config\n")
	dest := splitDest(t)

	m, err := buildSplitManifest(storage.NewVault(root), splitPlanParams(dest, "alpha"))
	if err != nil {
		t.Fatalf("buildSplitManifest: %v", err)
	}
	inManifest := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		inManifest[e.Path] = true
	}
	if inManifest["Projects/alpha/config.toml"] {
		t.Error("Projects/alpha/config.toml is in the hashed inventory; the subtract set must remove it")
	}
	if !inManifest["Projects/alpha/doc/config.toml"] {
		t.Error("Projects/alpha/doc/config.toml is missing from the hashed inventory; only the project config is subtracted")
	}

	p := splitPlannedParams(t, root, dest, "alpha")
	p.Action = "apply"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "Projects", "alpha", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("destination gained Projects/alpha/config.toml (lstat err: %v); it must not travel", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Projects", "alpha", "doc", "config.toml")); err != nil {
		t.Errorf("destination lost Projects/alpha/doc/config.toml: %v", err)
	}
}

// TestVaultSplitPurge_RemovesTheSubtractedProjectConfig: subtracting the project
// config removes its compare-and-set guard, not its deletion. Purge walks every
// regular file in the purged tree and deletes this one unguarded, as it does
// .surface.
func TestVaultSplitPurge_RemovesTheSubtractedProjectConfig(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	cfg := filepath.Join(root, "Projects", "alpha", "config.toml")
	if _, err := os.Stat(cfg); err != nil {
		t.Fatalf("fixture must seed the project config: %v", err)
	}
	dest := splitDest(t)
	p := splitPlannedParams(t, root, dest, "alpha")

	p.Action = "apply"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	p.Action = "purge"
	if _, err := callSplit(t, root, p); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := os.Lstat(cfg); !os.IsNotExist(err) {
		t.Errorf("purge left Projects/alpha/config.toml behind (lstat err: %v)", err)
	}
}

// TestVaultMerge_ProjectConfigDoesNotTravel: merge shares split's subtract set,
// so a source slug's Projects/<slug>/config.toml is neither hashed into the
// merge manifest nor carried into the destination, while the same name one
// level deeper is both. Merge reaches the slug trees through walkSplitTree —
// the same walker split uses — so this pins the exclusion on merge's own
// entry point rather than assuming it from split's.
func TestVaultMerge_ProjectConfigDoesNotTravel(t *testing.T) {
	source := mergeSourceVault(t, "alpha")
	writeSplitFile(t, source, "Projects/alpha/doc/config.toml", "# a document, not the project config\n")
	if _, err := os.Stat(filepath.Join(source, "Projects", "alpha", "config.toml")); err != nil {
		t.Fatalf("fixture must seed the source project config: %v", err)
	}
	dest := mergeDestVault(t, "gamma")

	m, err := buildMergeManifest(storage.NewVault(dest), mergeParams(source, "alpha"), true)
	if err != nil {
		t.Fatalf("buildMergeManifest: %v", err)
	}
	hashed := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		hashed[e.Path] = true
	}
	if hashed["Projects/alpha/config.toml"] {
		t.Error("Projects/alpha/config.toml is in the merge manifest; the subtract set must remove it")
	}
	if !hashed["Projects/alpha/doc/config.toml"] {
		t.Error("Projects/alpha/doc/config.toml is missing from the merge manifest; only the project config is subtracted")
	}

	p := mergePlanned(t, dest, source, "alpha")
	p.Action = "apply"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "Projects", "alpha", "config.toml")); !os.IsNotExist(err) {
		t.Errorf("merge carried Projects/alpha/config.toml into the destination (lstat err: %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Projects", "alpha", "doc", "config.toml")); err != nil {
		t.Errorf("merge did not carry Projects/alpha/doc/config.toml: %v", err)
	}
}
