// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
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

// ErrNoEmbeddedTemplate is Plan's error when PlanOptions.Only names nothing
// the binary embeds: the name is not a built-in, so there is nothing of vp's
// to compare, upgrade or reset.
var ErrNoEmbeddedTemplate = errors.New("no embedded template")

// ChangeKind classifies how an embedded template compares to its vault copy.
type ChangeKind string

const (
	// ChangeOverride means a vault Templates/ copy exists and differs from the
	// embedded template: an operator's override of a built-in. It is reported,
	// never written. Neither `vp commands upgrade` nor `vp skills upgrade`
	// changes one; only an explicit, named `vp commands reset` / `vp skills
	// reset` removes it (Reset), keeping a backup.
	ChangeOverride ChangeKind = "override"
	// ChangeUnchanged means the vault copy is the embedded template, line
	// endings aside (templates.ClassifyVaultCopy: current): a mirror.
	ChangeUnchanged ChangeKind = "unchanged"
	// ChangeStale means the vault copy is an earlier shipped version of the
	// built-in (templates.ClassifyVaultCopy: earlier; a row of the frozen
	// shipped.txt manifest). It is vp's bytes, not an operator's override:
	// `vp config sync` prunes it, and until then it shadows the current
	// built-in. It is not pending work for the upgrade commands and is not
	// counted as an override kept; a reset removes it without a backup.
	ChangeStale ChangeKind = "stale"
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
	// exists (Kind == ChangeUnneeded).
	VaultContent string
	// EmbeddedHash is the first 7 hex chars of SHA-256(EmbeddedContent).
	EmbeddedHash string
	// VaultHash is the first 7 hex chars of SHA-256(VaultContent); empty
	// when no vault copy exists (Kind == ChangeUnneeded).
	VaultHash string
	// VaultPath is the filesystem path where the vault copy lives or would live.
	VaultPath string
	// VaultRoot is the root of the vault that owns VaultPath. Reset works in
	// vault-relative paths — the locked vaultfs primitives it removes and backs
	// up through take (root, rel) — and derives rel from the two.
	VaultRoot string
	// EmbeddedRel is the built-in's path under the embedded templates root
	// ("commands/wrap.md", "skills/chair/SKILL.md"): what the vault copy's
	// provenance is judged against.
	EmbeddedRel string
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
// (ADR-008 Phase 3), and classifies a vault copy exactly as that reconciler
// does (templates.ClassifyVaultCopy): no vault copy is ChangeUnneeded, never
// work to do; the current embedded copy (line endings aside) is
// ChangeUnchanged; an earlier shipped version is ChangeStale, vp's bytes that
// `vp config sync` prunes; anything else — a genuine local override — is
// ChangeOverride, and a plan is only ever a report of it: no caller writes
// embedded bytes over an override. The upgrade commands list overrides as
// kept, and the reset commands hand the named ones to Reset, which removes
// them (keeping a backup of an override) so the embedded floor serves them.
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
		return nil, fmt.Errorf("%w named %q", ErrNoEmbeddedTemplate, opts.Only)
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

	embeddedRel, err := filepath.Rel(filepath.Join(resolver.VaultRoot(), "Templates"), vaultPath)
	if err != nil {
		return Change{}, fmt.Errorf("%s: %w", vaultPath, err)
	}
	c := Change{
		Name:            name,
		ResourceType:    resourceType,
		EmbeddedContent: embedded,
		EmbeddedHash:    shortHash(embedded),
		VaultPath:       vaultPath,
		VaultRoot:       resolver.VaultRoot(),
		EmbeddedRel:     filepath.ToSlash(embeddedRel),
	}
	if !haveVault {
		// No vault copy means no local override: the embedded floor already
		// serves this resource and there is nothing to materialize. Planning
		// absence as a template to create is what once re-created the
		// byte-identical Templates/ mirrors that `vp config sync` prunes,
		// silently inverting ADR-008 Phase 3.
		c.Kind = ChangeUnneeded
		return c, nil
	}
	c.VaultContent = vaultContent
	c.VaultHash = shortHash(vaultContent)
	switch templates.ClassifyVaultCopy(c.EmbeddedRel, []byte(vaultContent)) {
	case templates.ProvenanceCurrent:
		c.Kind = ChangeUnchanged
	case templates.ProvenanceEarlier:
		c.Kind = ChangeStale
	default:
		c.Kind = ChangeOverride
	}
	return c, nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:7]
}
