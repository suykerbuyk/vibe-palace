// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestVaultFetchAge_OldTrackingRef sets the origin/main tracking ref mtime far
// in the past and asserts VaultFetchAge reports a large age with known=true —
// purely from os.Stat, no network.
func TestVaultFetchAge_OldTrackingRef(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t) // pushes main → origin, so refs/remotes/origin/main exists

	ref := filepath.Join(dir, ".git", "refs", "remotes", "origin", "main")
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(ref, old, old); err != nil {
		t.Fatalf("Chtimes on tracking ref: %v", err)
	}

	age, fetchedAt, known := VaultFetchAge(dir)
	if !known {
		t.Fatalf("expected known=true with a real tracking ref")
	}
	if age < 71*time.Hour || age > 73*time.Hour {
		t.Errorf("age = %v, want ~72h", age)
	}
	if fetchedAt.IsZero() {
		t.Errorf("expected non-zero fetchedAt")
	}
}

// TestVaultFetchAge_RecentTrackingRef confirms a freshly-set ref mtime yields a
// small age and known=true.
func TestVaultFetchAge_RecentTrackingRef(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t)

	ref := filepath.Join(dir, ".git", "refs", "remotes", "origin", "main")
	now := time.Now()
	if err := os.Chtimes(ref, now, now); err != nil {
		t.Fatalf("Chtimes on tracking ref: %v", err)
	}

	age, _, known := VaultFetchAge(dir)
	if !known {
		t.Fatalf("expected known=true with a real tracking ref")
	}
	if age > time.Minute {
		t.Errorf("age = %v, want small (< 1m)", age)
	}
}

// TestVaultFetchAge_NoRemote confirms a repo with no configured remote reports
// known=false (nothing to be stale against) — network-free.
func TestVaultFetchAge_NoRemote(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initTestRepo(t) // no remote configured

	age, fetchedAt, known := VaultFetchAge(dir)
	if known {
		t.Errorf("expected known=false with no remote, got age=%v", age)
	}
	if !fetchedAt.IsZero() || age != 0 {
		t.Errorf("unknown result must be zero-valued, got age=%v fetchedAt=%v", age, fetchedAt)
	}
}

// TestVaultFetchAge_RemoteButNoTrackingRef confirms known=false when a remote is
// configured but was never fetched (no tracking ref, no FETCH_HEAD).
func TestVaultFetchAge_RemoteButNoTrackingRef(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir := initTestRepo(t)
	// Add a remote WITHOUT pushing/fetching → no refs/remotes/origin/main, no FETCH_HEAD.
	gitRun(t, dir, "remote", "add", "origin", filepath.Join(t.TempDir(), "never.git"))

	_, _, known := VaultFetchAge(dir)
	if known {
		t.Errorf("expected known=false when remote was never fetched")
	}
}

// TestVaultFetchAge_PrefersOrigin confirms origin is chosen even when it is not
// the first configured remote, and that its tracking ref mtime drives the age.
func TestVaultFetchAge_PrefersOrigin(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	dir, _ := repoWithRemote(t) // origin already configured + pushed
	// Add a second remote earlier in name order but never fetched.
	gitRun(t, dir, "remote", "add", "aaa", filepath.Join(t.TempDir(), "aaa.git"))

	ref := filepath.Join(dir, ".git", "refs", "remotes", "origin", "main")
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(ref, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	age, _, known := VaultFetchAge(dir)
	if !known {
		t.Fatalf("expected origin's tracking ref to be found, known=true")
	}
	if age < 47*time.Hour || age > 49*time.Hour {
		t.Errorf("age = %v, want ~48h (from origin ref)", age)
	}
}
