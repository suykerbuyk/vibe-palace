// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestComputeVaultStaleness_Old confirms a fetch age past the threshold warns
// with the "run a pull" message and carries LastFetched + AgeHours.
func TestComputeVaultStaleness_Old(t *testing.T) {
	fetchedAt := time.Now().Add(-72 * time.Hour)
	vs := computeVaultStaleness(72*time.Hour, fetchedAt, true)
	if !vs.Warn {
		t.Errorf("expected Warn=true for a 72h age")
	}
	if !strings.Contains(vs.Message, "run a pull") {
		t.Errorf("message = %q, want a run-a-pull nudge", vs.Message)
	}
	if vs.LastFetched == nil || !vs.LastFetched.Equal(fetchedAt) {
		t.Errorf("LastFetched not carried through: %v", vs.LastFetched)
	}
	if vs.AgeHours < 71 || vs.AgeHours > 73 {
		t.Errorf("AgeHours = %v, want ~72", vs.AgeHours)
	}
}

// TestComputeVaultStaleness_Recent confirms a fresh fetch age under the threshold
// does not warn and leaves the message empty.
func TestComputeVaultStaleness_Recent(t *testing.T) {
	fetchedAt := time.Now().Add(-2 * time.Hour)
	vs := computeVaultStaleness(2*time.Hour, fetchedAt, true)
	if vs.Warn {
		t.Errorf("expected Warn=false for a 2h age")
	}
	if vs.Message != "" {
		t.Errorf("expected empty message, got %q", vs.Message)
	}
	if vs.LastFetched == nil {
		t.Errorf("expected LastFetched carried even when not warning")
	}
}

// TestComputeVaultStaleness_Unknown confirms an unknown fetch age warns with the
// never-fetched message and omits LastFetched.
func TestComputeVaultStaleness_Unknown(t *testing.T) {
	vs := computeVaultStaleness(0, time.Time{}, false)
	if !vs.Warn {
		t.Errorf("expected Warn=true when fetch age is unknown")
	}
	if !strings.Contains(vs.Message, "never fetched") {
		t.Errorf("message = %q, want a never-fetched note", vs.Message)
	}
	if vs.LastFetched != nil {
		t.Errorf("unknown age must omit LastFetched, got %v", vs.LastFetched)
	}
}

// TestComputeVaultStaleness_BoundaryNoWarn confirms exactly-at-threshold does not
// warn (strictly greater-than triggers the warning).
func TestComputeVaultStaleness_BoundaryNoWarn(t *testing.T) {
	fetchedAt := time.Now().Add(-vaultStaleThreshold)
	vs := computeVaultStaleness(vaultStaleThreshold, fetchedAt, true)
	if vs.Warn {
		t.Errorf("exactly-at-threshold age must not warn")
	}
}

// TestBootstrapPopulatesVaultStaleness confirms AssembleBootstrap attaches a
// VaultStaleness field without error on a non-git temp vault (the field is always
// present; here the vault is not a git repo so the fetch age is unknown → Warn).
func TestBootstrapPopulatesVaultStaleness(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := BootstrapContextTool(resolver, vault)

	params := json.RawMessage(`{"project":"test-proj"}`)
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	br, ok := result.(BootstrapResult)
	if !ok {
		t.Fatalf("result type = %T, want BootstrapResult", result)
	}
	if br.VaultStaleness == nil {
		t.Fatalf("expected VaultStaleness to be populated")
	}
	// A bare temp dir is not a git repo → fetch age unknown → warns.
	if !br.VaultStaleness.Warn {
		t.Errorf("expected Warn=true on a non-git vault (unknown fetch age)")
	}
	// A warning must also surface in the human-visible directive line.
	if !strings.Contains(br.PostBootstrapInstructions, br.VaultStaleness.Message) {
		t.Errorf("expected the staleness message to be appended to PostBootstrapInstructions")
	}
}
