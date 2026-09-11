// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package templates owns the compiled-in template corpus — the embedded
// floor every command and skill resolves from when no override exists —
// and the helpers the override-only vault reconcile is built on: corpus
// enumeration and hashing, provenance (provenance.go: whether a vault copy is
// the current embedded copy, an earlier shipped version from the frozen
// shipped.txt manifest, or an operator's override), the never-overwriting
// backup a template reset keeps (backup.go), and the per-project README stubs.
// Nothing here writes embedded bytes into a vault's Templates/, and nothing
// reads or writes host-local state: the retired .vibe-palace/templates.lock is
// read by no vp from this release.
package templates

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

//go:embed templates
var defaultTemplates embed.FS

// embeddedRoot is the top-level directory inside the embedded FS.
const embeddedRoot = "templates"

// FS returns the embedded template filesystem. The root directory
// inside the returned FS is "templates/" — callers must include that
// prefix when reading files directly. Exported so
// internal/context/precedence.go can continue to resolve tier-5
// resources without owning a duplicate //go:embed directive.
func FS() embed.FS {
	return defaultTemplates
}

// Resource identifies one embedded markdown file.
type Resource struct {
	// RelPath is relative within the embedded templates root — no
	// leading "templates/" prefix. Examples: "commands/wrap.md",
	// "workflow.md".
	RelPath string
	// Bytes is the canonical embedded content.
	Bytes []byte
	// SHA256 is the hex-encoded digest (64 chars) of Bytes.
	SHA256 string
}

// WalkEmbedded returns every embedded markdown resource under the
// compiled-in templates tree. Non-markdown files are skipped; the
// result is ordered by RelPath for deterministic output. The walk
// recurses into subdirectories (e.g. skills/<name>/SKILL.md and
// skills/<name>/references/*.md), and RelPath is always reported with
// forward-slash separators regardless of host OS — lock keys and the
// 5-tier resolver depend on that stability.
func WalkEmbedded() ([]Resource, error) {
	var out []Resource
	err := fs.WalkDir(defaultTemplates, embeddedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".md") {
			return nil
		}
		data, err := fs.ReadFile(defaultTemplates, p)
		if err != nil {
			return fmt.Errorf("read embedded %q: %w", p, err)
		}
		rel := strings.TrimPrefix(p, embeddedRoot+"/")
		sum := sha256.Sum256(data)
		out = append(out, Resource{
			RelPath: rel,
			Bytes:   data,
			SHA256:  hex.EncodeToString(sum[:]),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// realEmbeddedSHA is the production implementation backing the
// EmbeddedSHA function variable. It returns the ProvenanceKey of the
// embedded resource at relPath (no "templates/" prefix) or ("", false)
// when no such resource exists. The embedded corpus holds no CR, so the key
// equals the plain sha256 of every template today.
func realEmbeddedSHA(relPath string) (string, bool) {
	data, err := fs.ReadFile(defaultTemplates, path.Join(embeddedRoot, relPath))
	if err != nil {
		return "", false
	}
	return ProvenanceKey(data), true
}

// EmbeddedSHA looks up the ProvenanceKey of an embedded resource by its
// root-relative path: the key a vault copy must match to be the CURRENT
// embedded copy (ClassifyVaultCopy). Declared as a package-level function
// variable so tests can override it (restore via defer) without rebuilding
// the binary — same convention as time.Now injection in the stdlib.
var EmbeddedSHA func(relPath string) (string, bool) = realEmbeddedSHA
