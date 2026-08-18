// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// matchesOnly decides whether the given resource name matches an --only
// filter for the given resource type. Commands match exactly. Skills
// match either exactly (nested identifier form) or when `only` names the
// skill directory (prefix "<only>/") so `--only startup-analyst` picks
// up every file under that skill.
func matchesOnly(resourceType, name, only string) bool {
	if resourceType != "skill" {
		return name == only
	}
	if name == only {
		return true
	}
	return strings.HasPrefix(name, only+"/")
}

// ChangeKind classifies how an embedded template compares to its vault copy.
type ChangeKind string

const (
	// ChangeNew means a vault copy is to be created from scratch.
	//
	// Plan no longer emits this: every template Plan enumerates comes from
	// the embedded corpus, so an absent vault copy is always ChangeUnneeded.
	// The kind remains because Apply/ApplyWithBackup accept a caller-built
	// []Change and still owe a create path the correct backup policy (there
	// are no prior bytes to preserve).
	ChangeNew ChangeKind = "new"
	// ChangeUpdated means the embedded template differs from the vault copy.
	ChangeUpdated ChangeKind = "updated"
	// ChangeUnchanged means the embedded template matches the vault copy.
	ChangeUnchanged ChangeKind = "unchanged"
	// ChangeUnneeded means no vault copy exists and none is wanted: the
	// embedded floor (precedence Tier 5, internal/context/precedence.go)
	// already serves this resource, and the bytes a write would produce are
	// that same floor verbatim. Materializing it would create a Tier 4 vault
	// mirror that shadows the binary forever after — the drift ADR-008
	// Phase 3 pruned and made the reconciler override-only. Absence of an
	// override is not work to do.
	ChangeUnneeded ChangeKind = "unneeded"
)

// Change describes a single template's upgrade status.
type Change struct {
	// Name is the resource name (e.g. "restart").
	Name string
	// ResourceType is "command" or "skill".
	ResourceType string
	// Kind is the comparison result.
	Kind ChangeKind
	// EmbeddedContent is the source-of-truth content.
	EmbeddedContent string
	// VaultContent is the current vault copy; empty when no vault copy
	// exists (Kind == ChangeUnneeded, or a caller-built ChangeNew).
	VaultContent string
	// EmbeddedHash is the first 7 hex chars of SHA-256(EmbeddedContent).
	EmbeddedHash string
	// VaultHash is the first 7 hex chars of SHA-256(VaultContent); empty
	// when no vault copy exists (Kind == ChangeUnneeded, or a caller-built
	// ChangeNew).
	VaultHash string
	// VaultPath is the filesystem path where the vault copy lives or would live.
	VaultPath string
	// VaultRoot is the root of the vault that owns VaultPath, used to stamp the
	// .surface version on write. Threading it here is what lets the shared
	// atomicfile primitive stamp structurally — commands/skills upgrade
	// previously stamped nowhere.
	VaultRoot string
}

// PlanOptions configures which templates Upgrade considers.
type PlanOptions struct {
	// ResourceTypes lists which resource types to include. Empty means
	// {"command"}. Accepts "command" and "skill". For "skill" the Plan
	// emits one Change per file (SKILL.md + each reference), with
	// Change.Name carrying the nested identifier "<skill>/<relpath>" so
	// callers can group by skill directory.
	ResourceTypes []string
	// Only, when non-empty, restricts the plan to the named resource.
	// For skills it matches against the nested name (prefix match on
	// "<skill>/" is also accepted so --only <skill> picks up every file
	// under that skill).
	Only string
}

// Plan enumerates every embedded template, compares it to the vault copy,
// and returns one Change per template. The plan is deterministic (sorted
// by ResourceType, then Name). Unchanged and Unneeded entries are included
// so callers can report them; filtering is the caller's responsibility.
//
// Plan is override-only, matching the `vp config sync` Templates reconciler
// (ADR-008 Phase 3): a template with no vault copy is ChangeUnneeded, never
// work to do. Only a vault copy that DIFFERS from embedded — a genuine local
// override — is offered as ChangeUpdated.
func Plan(resolver *vpctx.Resolver, opts PlanOptions) ([]Change, error) {
	types := opts.ResourceTypes
	if len(types) == 0 {
		types = []string{"command"}
	}

	var changes []Change
	for _, rt := range types {
		names, err := resolver.ListEmbedded(rt)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if opts.Only != "" && !matchesOnly(rt, name, opts.Only) {
				continue
			}
			c, err := planOne(resolver, rt, name)
			if err != nil {
				return nil, err
			}
			changes = append(changes, c)
		}
	}

	sort.Slice(changes, func(i, j int) bool {
		if changes[i].ResourceType != changes[j].ResourceType {
			return changes[i].ResourceType < changes[j].ResourceType
		}
		return changes[i].Name < changes[j].Name
	})

	if opts.Only != "" && len(changes) == 0 {
		return nil, fmt.Errorf("no embedded template named %q", opts.Only)
	}
	return changes, nil
}

func planOne(resolver *vpctx.Resolver, resourceType, name string) (Change, error) {
	res := fmt.Sprintf("%s:%s", resourceType, name)

	embedded, err := resolver.EmbeddedContent(res)
	if err != nil {
		return Change{}, err
	}
	vaultContent, haveVault, err := resolver.VaultContent(res)
	if err != nil {
		return Change{}, err
	}
	vaultPath, err := resolver.VaultPath(res)
	if err != nil {
		return Change{}, err
	}

	c := Change{
		Name:            name,
		ResourceType:    resourceType,
		EmbeddedContent: embedded,
		EmbeddedHash:    shortHash(embedded),
		VaultPath:       vaultPath,
		VaultRoot:       resolver.VaultRoot(),
	}
	if !haveVault {
		// No vault copy means no local override, and Apply would write
		// EmbeddedContent — the embedded floor already serving this
		// resource. There is nothing to materialize. Classifying absence as
		// ChangeNew is what re-created the byte-identical Templates/ mirrors
		// that `vp config sync` prunes, silently inverting ADR-008 Phase 3.
		c.Kind = ChangeUnneeded
		return c, nil
	}
	c.VaultContent = vaultContent
	c.VaultHash = shortHash(vaultContent)
	if c.EmbeddedHash == c.VaultHash {
		c.Kind = ChangeUnchanged
	} else {
		c.Kind = ChangeUpdated
	}
	return c, nil
}

// Apply writes the embedded content of each accepted Change to its vault
// path, creating parent directories as needed. The write is atomic per file
// (templates.Executor handles tmp+rename). Unchanged and Unneeded entries
// are ignored whether or not they are marked accepted. No .bak is left behind — see
// doc/TEMPLATE_POLICY.md for the rationale.
func Apply(accepted []Change) error {
	return applyWithPolicy(accepted, templates.BackupPolicyNever)
}

// ApplyWithBackup is like Apply but, for Updated entries, preserves the
// existing vault file to a sibling ".bak" (via rename — matches the
// legacy skills-upgrade behavior byte-for-byte) before writing. New
// entries have no prior copy to preserve; Unchanged entries are
// skipped. The backup is a single sibling ".bak"; repeated runs
// overwrite it so the on-disk surface stays bounded.
//
// Centralized .bak policy: this function and Apply differ only in the
// BackupPolicy passed to the shared templates.Executor. See
// doc/TEMPLATE_POLICY.md for the follow-up (make the asymmetry user-
// configurable or unify).
func ApplyWithBackup(accepted []Change) error {
	return applyWithPolicy(accepted, templates.BackupPolicyRename)
}

func applyWithPolicy(accepted []Change, policy templates.BackupPolicy) error {
	exec := templates.NewExecutor()
	for _, c := range accepted {
		if c.Kind == ChangeUnchanged || c.Kind == ChangeUnneeded {
			continue
		}
		// New entries have nothing to preserve regardless of policy.
		effective := policy
		if c.Kind == ChangeNew {
			effective = templates.BackupPolicyNever
		}
		if err := exec.Write(c.VaultPath, []byte(c.EmbeddedContent), templates.WriteOptions{Backup: effective, VaultRoot: c.VaultRoot}); err != nil {
			return fmt.Errorf("write %s: %w", c.VaultPath, err)
		}
	}
	return nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:7]
}
