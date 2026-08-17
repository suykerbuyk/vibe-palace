// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// seedSession writes an empty session note under a given fingerprint.
func seedSession(t *testing.T, root, project, date, fp string, iter int) {
	t.Helper()
	dir := filepath.Join(root, "Projects", project, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := date + "-" + fp + "-0" + string(rune('0'+iter)) + ".md"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWriterIdentityIsDerivedFromTheVaultPath is the load-bearing assertion.
//
// The whole hazard is that a fingerprint hashes the VAULT PATH as well as the
// hostname, so the same machine writing to a relocated vault becomes a different
// identity and its future notes stop matching its past ones. If the reported
// value did not actually vary with the path, this row would be reassuring and
// wrong — the exact failure mode it exists to prevent.
func TestWriterIdentityIsDerivedFromTheVaultPath(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()

	ra := CheckWriterIdentity(a)
	rb := CheckWriterIdentity(b)

	fpA, fpB := surface.WriterFingerprint(a), surface.WriterFingerprint(b)
	if fpA == fpB {
		t.Fatalf("fixture broken: two distinct vault paths hashed alike (%s) — "+
			"there is no path-dependence left to assert", fpA)
	}
	if !strings.Contains(ra.Summary, fpA) {
		t.Errorf("summary %q does not report this host's fingerprint %s for vault %s", ra.Summary, fpA, a)
	}
	if !strings.Contains(rb.Summary, fpB) {
		t.Errorf("summary %q does not report this host's fingerprint %s for vault %s", rb.Summary, fpB, b)
	}
	if ra.Summary == rb.Summary {
		t.Errorf("the SAME identity was reported for two different vault paths (%q) — the fingerprint is "+
			"no longer derived from the vault path, so a relocated vault would report an unchanged "+
			"identity and the misattribution this row exists to prevent becomes invisible", ra.Summary)
	}
}

// TestWriterIdentityCountsSessionsPerFingerprint proves the tally is real: it
// separates this host's notes from another identity's, which is the question
// ("which of these are mine?") that used to be answered by inference.
func TestWriterIdentityCountsSessionsPerFingerprint(t *testing.T) {
	root := t.TempDir()
	mine := surface.WriterFingerprint(root)
	const theirs = "0123abcd"
	if mine == theirs {
		t.Skip("fixture collision with the synthetic foreign fingerprint")
	}

	seedSession(t, root, "proj", "2026-08-17", mine, 1)
	seedSession(t, root, "proj", "2026-08-17", theirs, 2)
	seedSession(t, root, "other", "2026-08-16", theirs, 3)
	// Legacy host-agnostic note: carries no identity and must not be attributed.
	dir := filepath.Join(root, "Projects", "proj", "sessions")
	if err := os.WriteFile(filepath.Join(dir, "2026-08-15-07.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := CheckWriterIdentity(root)
	if r.Status != Pass {
		t.Errorf("Status = %v, want Pass: this host HAS written here", r.Status)
	}
	if !strings.Contains(r.Summary, "1 of 3") {
		t.Errorf("summary %q should report 1 of 3 sessions — 1 mine, 2 theirs, and the legacy "+
			"host-agnostic note attributed to NOBODY", r.Summary)
	}
	joined := strings.Join(r.Details, "\n")
	if !strings.Contains(joined, mine+": 1 session(s)") || !strings.Contains(joined, theirs+": 2 session(s)") {
		t.Errorf("details do not break the tally down per identity:\n%s", joined)
	}
	if !strings.Contains(joined, "<- this host") {
		t.Error("details do not mark which identity is this host's — the reader is left inferring it again")
	}
}

// TestWriterIdentityIsQuietWhenHealthyAndExplainsWhenNot pins the 289 contract
// (a row that fires on every healthy vault teaches the reader to skim it) AND
// clause 3 (the why is emitted only when the condition fires).
func TestWriterIdentityIsQuietWhenHealthyAndExplainsWhenNot(t *testing.T) {
	// Healthy: this host has written here.
	healthy := t.TempDir()
	seedSession(t, healthy, "proj", "2026-08-17", surface.WriterFingerprint(healthy), 1)
	hr := CheckWriterIdentity(healthy)
	if hr.Status != Pass {
		t.Errorf("Status = %v, want Pass on a vault this host has written to", hr.Status)
	}
	if strings.Contains(strings.Join(hr.Details, "\n"), "MOVING A VAULT") {
		t.Error("the derivation why is emitted on a HEALTHY vault — clause 3 requires it only when the " +
			"condition fires, or it becomes noise the reader learns to skip")
	}

	// Notable: sessions exist, none of them this host's — what a relocated vault
	// (or a fresh clone) looks like from the inside.
	moved := t.TempDir()
	seedSession(t, moved, "proj", "2026-08-17", "0123abcd", 1)
	mr := CheckWriterIdentity(moved)
	if mr.Status != Info {
		t.Fatalf("Status = %v, want Info when this host has written nothing but other identities exist", mr.Status)
	}
	why := strings.Join(mr.Details, "\n")
	for _, want := range []string{"MOVING A VAULT", "vaultPath", "never written to the vault"} {
		if !strings.Contains(why, want) {
			t.Errorf("the Info branch does not explain %q — the deleted Behavioral Note has no "+
				"need-to-know path left:\n%s", want, why)
		}
	}

	// Empty vault: nothing written by anyone is not a finding about this host.
	if er := CheckWriterIdentity(t.TempDir()); er.Status != Pass {
		t.Errorf("Status = %v on an empty vault, want Pass — an unwritten vault is an absence, not a signal", er.Status)
	}
}

// TestWriterIdentitySkipsWithoutAVault pins the degradation contract every other
// producer honours.
func TestWriterIdentitySkipsWithoutAVault(t *testing.T) {
	if r := CheckWriterIdentity(""); r.Status != Skip {
		t.Errorf("Status = %v, want Skip with no vault configured", r.Status)
	}
}

// absPathToken matches a host-rooted absolute path anywhere in a string. A
// repo-relative citation like internal/surface/version.go is deliberately NOT
// matched: the disease is a HOST-rooted path, which is false on every other
// machine, not a source reference that is true everywhere.
var absPathToken = regexp.MustCompile(`(^|\s|\()/\S+`)

// TestWriterIdentityNeverEmitsTheVaultPath is the regression that matters most
// for a row delivered at session start.
//
// writer-identity sits on the restart and wrap selector lists, and PrintRows
// prints Details on Pass — so anything here reaches an agent every session and is
// one copy-paste from a synced resume.md. A host-rooted absolute path in that
// position is precisely the defect `vault-abs-paths` exists to catch, which would
// make the check built to stop inference the thing reintroducing the disease.
//
// 277's corollary: write the CONSTRAINT, never the PATH. The fingerprint IS the
// constraint, and it is derived FROM the path without ever quoting it.
func TestWriterIdentityNeverEmitsTheVaultPath(t *testing.T) {
	cases := []struct {
		name string
		root func(t *testing.T) string
	}{
		{"pass, this host has written", func(t *testing.T) string {
			r := t.TempDir()
			seedSession(t, r, "proj", "2026-08-17", surface.WriterFingerprint(r), 1)
			return r
		}},
		{"info, this host has written nothing", func(t *testing.T) string {
			r := t.TempDir()
			seedSession(t, r, "proj", "2026-08-17", "0123abcd", 1)
			return r
		}},
		{"pass, empty vault", func(t *testing.T) string { return t.TempDir() }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t)
			r := CheckWriterIdentity(root)
			text := r.Summary + "\n" + strings.Join(r.Details, "\n")

			if strings.Contains(text, root) {
				t.Errorf("the vault root leaked into the row:\n%s", text)
			}
			if m := absPathToken.FindString(text); m != "" {
				t.Errorf("a host-rooted absolute path %q leaked into the row — this reaches an agent "+
					"at every session start and is one copy-paste from a synced document:\n%s", m, text)
			}
			if !strings.Contains(text, surface.WriterFingerprint(root)) {
				t.Errorf("the fingerprint is absent, so removing the path removed the answer too:\n%s", text)
			}
		})
	}
}

// TestWriterIdentitySkipBranchAlsoHidesThePath covers the third exit, which the
// other tests cannot reach: an unreadable Projects/ directory.
//
// It matters because the natural implementation of that branch wraps the os
// error — and an os error carries the absolute path it failed on. That is the
// same leak as the Details one, arriving through the door nobody exercises.
func TestWriterIdentitySkipBranchAlsoHidesThePath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 does not deny access, so the error branch is unreachable")
	}
	root := t.TempDir()
	projects := filepath.Join(root, "Projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(projects, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(projects, 0o755) })

	r := CheckWriterIdentity(root)
	if r.Status != Skip {
		t.Fatalf("Status = %v, want Skip when Projects/ cannot be read (got summary %q) — "+
			"if this branch is no longer reachable the leak it guards is untested", r.Status, r.Summary)
	}
	text := r.Summary + "\n" + strings.Join(r.Details, "\n")
	if strings.Contains(text, root) {
		t.Errorf("the vault root leaked through the error branch: %q", text)
	}
	if m := absPathToken.FindString(text); m != "" {
		t.Errorf("a host-rooted absolute path %q leaked through the error branch — an os error carries "+
			"the path it failed on, so wrapping it here reintroduces the leak: %q", m, text)
	}
}
