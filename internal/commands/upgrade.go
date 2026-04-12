// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// ChangeKind classifies how an embedded template compares to its vault copy.
type ChangeKind string

const (
	// ChangeNew means the embedded template has no vault copy yet.
	ChangeNew ChangeKind = "new"
	// ChangeUpdated means the embedded template differs from the vault copy.
	ChangeUpdated ChangeKind = "updated"
	// ChangeUnchanged means the embedded template matches the vault copy.
	ChangeUnchanged ChangeKind = "unchanged"
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
	// VaultContent is the current vault copy; empty when Kind == ChangeNew.
	VaultContent string
	// EmbeddedHash is the first 7 hex chars of SHA-256(EmbeddedContent).
	EmbeddedHash string
	// VaultHash is the first 7 hex chars of SHA-256(VaultContent);
	// empty when Kind == ChangeNew.
	VaultHash string
	// VaultPath is the filesystem path where the vault copy lives or would live.
	VaultPath string
}

// PlanOptions configures which templates Upgrade considers.
type PlanOptions struct {
	// ResourceTypes lists which resource types to include. Empty means
	// {"command"}. Skills can be added here if/when embedded skills exist.
	ResourceTypes []string
	// Only, when non-empty, restricts the plan to the named resource.
	Only string
}

// Plan enumerates every embedded template, compares it to the vault copy,
// and returns one Change per template. The plan is deterministic (sorted
// by ResourceType, then Name). Unchanged entries are included so callers
// can report them; filtering is the caller's responsibility.
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
			if opts.Only != "" && name != opts.Only {
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
	}
	if !haveVault {
		c.Kind = ChangeNew
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
// (write to a sibling temp file, then rename). Unchanged entries are
// ignored whether or not they are marked accepted.
func Apply(accepted []Change) error {
	for _, c := range accepted {
		if c.Kind == ChangeUnchanged {
			continue
		}
		if err := writeAtomic(c.VaultPath, []byte(c.EmbeddedContent), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", c.VaultPath, err)
		}
	}
	return nil
}

func writeAtomic(dst string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".vp-upgrade-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:7]
}
