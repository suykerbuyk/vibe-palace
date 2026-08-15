// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package capture

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// WriteSession records the host attribution VERBATIM from the params — the
// resolution (derive/declare/unknown) belongs to the MCP handler, and capture
// must neither second-guess it nor invent one.
func TestWriteSessionRecordsHostVerbatim(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project:    "test-proj",
		Summary:    "Session with derived host attribution.",
		Host:       "Zed",
		HostSource: storage.HostSourceDerived,
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	meta, _, err := vault.ReadSession("test-proj", result.SessionID[:10], ParseFingerprint(result.SessionID), result.Iteration)
	if err != nil {
		t.Fatalf("read session back: %v", err)
	}
	if meta.Host != "Zed" {
		t.Errorf("host = %q, want %q", meta.Host, "Zed")
	}
	if meta.HostSource != storage.HostSourceDerived {
		t.Errorf("host_source = %q, want %q", meta.HostSource, storage.HostSourceDerived)
	}

	data, err := os.ReadFile(filepath.Join(vault.Root, result.NotePath))
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(string(data), "host: Zed") {
		t.Error("note frontmatter missing 'host: Zed'")
	}
	if !strings.Contains(string(data), "host_source: derived") {
		t.Error("note frontmatter missing 'host_source: derived'")
	}
}

// A writer that makes no host claim produces a note with NO host key at all —
// absence of the key means "no claim was made", which is distinct from the
// explicit "unknown" both real writers record when they looked and found
// nothing.
//
// This pins WriteSession's omitempty contract, and ONLY that. It names no
// caller on purpose: the comment used to say "(the hook path)", and stayed
// green while the hook started setting both fields — a test asserting a
// property of a path it never exercises. Every caller in the tree now makes a
// claim; the contract still has to hold for the one that does not.
func TestWriteSessionNoHostClaimOmitsKey(t *testing.T) {
	vault := testVault(t)

	result, err := WriteSession(context.Background(), vault, nil, SessionParams{
		Project: "test-proj",
		Summary: "Hook-path capture with no host claim.",
	})
	if err != nil {
		t.Fatalf("WriteSession: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(vault.Root, result.NotePath))
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	body := string(data)
	if strings.Contains(body, "\nhost:") {
		t.Error("note carries a host key with no claim behind it")
	}
	if strings.Contains(body, "\nhost_source:") {
		t.Error("note carries a host_source key with no claim behind it")
	}
}
