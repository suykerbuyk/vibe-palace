// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// ProjectPathClass says what a vault-relative path is to a relocation: a
// rename (same vault, new slug) or a split or merge (new vault, same slug).
//
// 🔴 ONE PREDICATE, MANY ACTIONS. The class is a fact about the path. What a
// relocation DOES with each class is the caller's policy, and the callers
// differ on purpose:
//
//	class        split / merge                     rename
//	Content      manifest row, copied              moved (committed if tracked,
//	                                               journalled if not)
//	VaultBound   subtracted; the destination       moved: same repository, so
//	             stamps itself                     the value is still true
//	Retired      subtracted                        moved as dead bytes
//	MachineLocal pruned; purge removes it with     planned explicitly, never
//	             the tree                          skipped (skipping it is what
//	                                               made the one-shot fail late)
//
// Split's "subtracted" is exactly class != ProjectContent, and its pruned
// directories are MachineLocalDirNames. Whether a file is tracked by git is
// NOT a membership rule: rename asks git only to choose HOW to move a file.
type ProjectPathClass int

const (
	// ProjectContent is the project itself: it travels with every relocation.
	// Ignored backups (transcripts/*.manifest.json.<hash>.bak) are Content.
	ProjectContent ProjectPathClass = iota
	// ProjectVaultBound is meaningful only inside the vault repository that
	// holds it: the .surface stamp (the binary that last wrote THIS vault) and
	// commit-log.anchor (a commit SHA of THIS vault). A destination vault has
	// no such commit and stamps itself, so neither crosses a vault.
	ProjectVaultBound
	// ProjectRetired is Projects/<slug>/config.toml, the retired per-project
	// vault config that nothing reads as input. Matched by path depth
	// (vaultfs.IsVaultProjectConfigPath), never by base name, so
	// Projects/<slug>/doc/config.toml is Content.
	ProjectRetired
	// ProjectMachineLocal is host-local state: anything under a path component
	// named .local (palace/.local, palace/<slug>/.local and its
	// imported-sessions.jsonl, a legacy embed cache) or .vp-locks (vaultlock's
	// sidecar directory). It never crosses a vault, and a walk may prune it.
	ProjectMachineLocal
)

// String names the class for reports and test failures.
func (c ProjectPathClass) String() string {
	switch c {
	case ProjectContent:
		return "content"
	case ProjectVaultBound:
		return "vault-bound"
	case ProjectRetired:
		return "retired"
	case ProjectMachineLocal:
		return "machine-local"
	}
	return "unknown"
}

// MachineLocalDirNames are the directory names whose subtrees are
// ProjectMachineLocal, for walks that prune by DirEntry name. A caller must not
// modify it.
var MachineLocalDirNames = map[string]bool{".local": true, ".vp-locks": true}

// ClassifyProjectPath classifies a slash-separated vault-relative path.
//
// It is defined for vault-global paths too (Audits/.surface is VaultBound),
// because split and merge apply the same rule to the global artifacts they
// carry. The order is fixed: a machine-local component wins over everything,
// then the depth-anchored retired config, then the vault-bound base names.
func ClassifyProjectPath(rel string) ProjectPathClass {
	for _, comp := range strings.Split(rel, "/") {
		if MachineLocalDirNames[comp] {
			return ProjectMachineLocal
		}
	}
	// The predicate vaultfs uses to refuse writes to this path, so the two
	// cannot disagree about which file is meant.
	if vaultfs.IsVaultProjectConfigPath(rel) {
		return ProjectRetired
	}
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	if base == ".surface" || base == "commit-log.anchor" {
		return ProjectVaultBound
	}
	return ProjectContent
}

// ProjectTrees names the two trees that ARE a project in a vault, as
// slash-separated vault-relative paths. The slug is not validated here; every
// caller has already validated it.
func ProjectTrees(slug string) []string {
	return []string{"palace/" + slug, "Projects/" + slug}
}
