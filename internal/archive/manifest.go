// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package archive implements the transcript archive / copyright provenance
// ledger described in doc/adr/001-transcript-archive.md.
//
// Archives are produced by per-IDE adapters (Claude Code, Zed, ...) from
// host-level session hooks. Each archive is a paired
// <name>.jsonl.zst + <name>.manifest.json set. The manifest's
// source_sha256 hashes the pre-compression JSONL, which is the
// evidentiary anchor that survives re-compression or tool upgrades.
package archive

import (
	"encoding/json"
	"fmt"
	"os"
)

// ManifestSchemaVersion is the current manifest schema version.
// Incrementing implies a breaking change; older archives remain
// readable by consulting the schema_version field.
const ManifestSchemaVersion = 1

// Manifest is the on-disk provenance record that accompanies every
// compressed transcript. The field set is frozen by ADR-001.
type Manifest struct {
	SchemaVersion       int    `json:"schema_version"`
	Adapter             string `json:"adapter"`
	AdapterVersion      string `json:"adapter_version"`
	SessionID           string `json:"session_id"`
	Model               string `json:"model,omitempty"`
	TurnCount           int    `json:"turn_count"`
	SourceSHA256        string `json:"source_sha256"`
	SourceBytes         int64  `json:"source_bytes"`
	CompressedBytes     int64  `json:"compressed_bytes"`
	GitHead             string `json:"git_head,omitempty"`
	ProjectSlug         string `json:"project_slug"`
	VaultRelSessionNote string `json:"vault_rel_session_note,omitempty"`
	CapturedAt          string `json:"captured_at"` // RFC3339 UTC
	CapturedByHostname  string `json:"captured_by_hostname,omitempty"`
	VPVersion           string `json:"vp_version,omitempty"`
}

// ReadManifest loads and validates a manifest file.
func ReadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.SchemaVersion == 0 {
		return nil, fmt.Errorf("manifest %s: missing schema_version", path)
	}
	if m.SchemaVersion > ManifestSchemaVersion {
		return nil, fmt.Errorf("manifest %s: schema_version %d newer than supported %d",
			path, m.SchemaVersion, ManifestSchemaVersion)
	}
	return &m, nil
}

// WriteManifest serializes a manifest to the given path with pretty
// indentation. Callers are responsible for ensuring the parent
// directory exists.
func WriteManifest(path string, m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	b = append(b, '\n')
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write manifest %s: %w", path, err)
	}
	return nil
}
