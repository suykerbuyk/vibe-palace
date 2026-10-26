// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"slices"
	"testing"
)

// TestMigrateToExportsMatchInternals pins the thin wrappers to the
// package-private functions they expose, so a future rename cannot silently
// change what migrate-to calls.
func TestMigrateToExportsMatchInternals(t *testing.T) {
	a := t.TempDir()
	if !SameVaultRoot(a, a) {
		t.Error("SameVaultRoot(a,a) = false")
	}
	if SameVaultRoot(a, t.TempDir()) {
		t.Error("SameVaultRoot of two different dirs = true")
	}
	got, ok := NormaliseRemoteURL("ssh://git@example.com:22/team/vault.git")
	want, wok := normaliseRemoteURL("ssh://git@example.com:22/team/vault.git")
	if got != want || ok != wok || !ok {
		t.Errorf("NormaliseRemoteURL mismatch: (%q,%v) vs (%q,%v)", got, ok, want, wok)
	}
}

// TestPublishOrderNamesUnrecordedRemote proves PublishOrder returns the full
// push blast radius: a configured remote absent from remotes.toml is still in
// the order (the F3 guarantee migrate-to prints).
func TestPublishOrderNamesUnrecordedRemote(t *testing.T) {
	dir := initTestRepo(t)
	b1, b2 := initBareRemote(t), initBareRemote(t)
	writeFile(t, dir, "Projects/p/resume.md", "p\n")
	writeFile(t, dir, ".vibe-palace/remotes.toml", "[[remote]]\nname = \"origin\"\nurl = \""+fileURL(b1)+"\"\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "seed")
	gitRun(t, dir, "remote", "add", "origin", fileURL(b1))
	gitRun(t, dir, "remote", "add", "backup", fileURL(b2)) // NOT in remotes.toml
	order, err := PublishOrder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(order, "origin") || !slices.Contains(order, "backup") {
		t.Fatalf("PublishOrder %v must contain both origin (recorded) and backup (unrecorded)", order)
	}
	if order[0] != "origin" {
		t.Errorf("recorded origin should lead the push order, got %v", order)
	}
}

// TestReadRecordedRemotesNeutral proves the reader parses name/url and, unlike
// the clone reader, is absent-tolerant (nil,nil) and raises no "refusing to
// clone" error.
func TestReadRecordedRemotesNeutral(t *testing.T) {
	dir := initTestRepo(t)
	if rr, err := ReadRecordedRemotes(dir); err != nil || rr != nil {
		t.Fatalf("absent remotes.toml: got (%v,%v), want (nil,nil)", rr, err)
	}
	writeFile(t, dir, ".vibe-palace/remotes.toml",
		"[[remote]]\nname = \"origin\"\nurl = \"git@h:/a.git\"\n[[remote]]\nname = \"m\"\nurl = \"git@h:/b.git\"\n")
	rr, err := ReadRecordedRemotes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rr) != 2 || rr[0].Name != "origin" || rr[1].URL != "git@h:/b.git" {
		t.Fatalf("parsed %+v", rr)
	}
}

// TestConfiguredRemoteURL returns the live URL of a named remote.
func TestConfiguredRemoteURL(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", fileURL(bare))
	got, err := ConfiguredRemoteURL(dir, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if got != fileURL(bare) {
		t.Fatalf("ConfiguredRemoteURL = %q, want %q", got, fileURL(bare))
	}
	if _, err := ConfiguredRemoteURL(dir, "nope"); err == nil {
		t.Error("absent remote should error")
	}
}

// TestVerifiedCopyAtFalseWhenNoCopy proves the probe reports false (benignly,
// no error) when the destination tip holds no verified copy of the source HEAD
// — the signal to run the copy. It still fetches and closes a dest snapshot.
func TestVerifiedCopyAtFalseWhenNoCopy(t *testing.T) {
	srcDir, _ := lcSource(t) // source holds project p
	// A destination vault that exists and has a HEAD, but never received p.
	destDir := initTestRepo(t)
	destBare := initBareRemote(t)
	gitRun(t, destBare, "config", "uploadpack.allowFilter", "true")
	gitRun(t, destBare, "config", "uploadpack.allowAnySHA1InWant", "true")
	writeFile(t, destDir, "Projects/other/resume.md", "other\n")
	gitRun(t, destDir, "add", "-A")
	gitRun(t, destDir, "commit", "-m", "seed dest")
	gitRun(t, destDir, "remote", "add", "origin", fileURL(destBare))
	gitRun(t, destDir, "push", "-q", "origin", "main")

	present, commit, err := VerifiedCopyAt(srcDir, fileURL(destBare), "p")
	if err != nil {
		t.Fatalf("probe errored: %v", err)
	}
	if present || commit != "" {
		t.Fatalf("no copy at dest, but probe reported present=%v commit=%q", present, commit)
	}
}

// TestVerifiedCopyAtNoContent returns false when the source holds no tracked
// content for the slug at HEAD (nothing to verify).
func TestVerifiedCopyAtNoContent(t *testing.T) {
	srcDir, _ := lcSource(t)
	destBare := initBareRemote(t)
	gitRun(t, destBare, "config", "uploadpack.allowFilter", "true")
	gitRun(t, destBare, "config", "uploadpack.allowAnySHA1InWant", "true")
	// Give the dest a HEAD so fetchDestination can read it.
	destDir := initTestRepo(t)
	writeFile(t, destDir, "x", "x\n")
	gitRun(t, destDir, "add", "-A")
	gitRun(t, destDir, "commit", "-m", "s")
	gitRun(t, destDir, "remote", "add", "origin", fileURL(destBare))
	gitRun(t, destDir, "push", "-q", "origin", "main")

	present, _, err := VerifiedCopyAt(srcDir, fileURL(destBare), "absent-slug")
	if err != nil || present {
		t.Fatalf("absent slug: present=%v err=%v, want false,nil", present, err)
	}
}
