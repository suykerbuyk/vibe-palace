// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 🔴 THE STRUCTURAL HALF OF THE CALL-SITE AGREEMENT INVARIANT.
//
// ArchiveLinkResult once carried a CanonicalIsStub field. Exactly one of the
// three production back-link call sites consulted it — internal/capture's Codex
// receipt — and because that receipt is always an auto-capture stub and always
// the only note at Codex SessionEnd, the flag was always true and the back-link
// was never written. Every Codex session stranded its own transcript, silently,
// for months. internal/hook described the same rule in a comment and implemented
// nothing; internal/capture/session.go linked unconditionally. One rule, three
// sites, one of each.
//
// The rule is now "consulted at NO call site", and the field is DELETED rather
// than merely unread, so restoring that exact flag means re-adding a struct
// field: a reviewable change instead of a one-word edit.
//
// 🔴 THIS TEST IS NOT THE RATCHET, AND MUST NOT BE MISTAKEN FOR ONE. It is keyed
// on an IDENTIFIER, and an identifier is renamed for free. Both adversarial
// reviewers defeated it in one line (2026-11-07) by writing the same old rule at
// a third call site as `meta.Tag != storage.TagAutoCapture` — which re-adds
// nothing this test looks for, so it stayed green while the defect was back.
//
// What actually holds the invariant is two things, and this is neither:
//
//   - TestIntegrationBackLinkCallSitesAgreeOnAStubOnlySession (internal/integration)
//     — one arm per session-capture writer, each reddening for its own site. A
//     guard spelled ANY way reddens the arm of the site it is written at.
//   - TestBackLinkWriterCensus (internal/archive) — keyed on the CALL, not on any
//     name, so a NEW back-link writer cannot appear without an arm.
//
// This test survives as the cheap, specific check that the original flag has not
// literally come back, and as the record of why it is gone. Do not delete it, and
// do not rely on it.
func TestArchiveLinkResultCarriesNoStubFlag(t *testing.T) {
	root := repoRootFromStorage(t)

	// find, never git ls-files: a file that is present but untracked is exactly
	// the one a sweep must not miss, and ls-files reports clean by omission.
	var hits []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".wt", ".cache", "third_party", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// This file names the field in prose on purpose; it is the record of why
		// the field is gone, and excluding it is what lets that record exist.
		if filepath.Base(path) == "archive_link_stub_flag_test.go" {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "CanonicalIsStub") {
				continue
			}
			// A comment explaining the deletion is the point, not a violation.
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "//") {
				continue
			}
			rel, _ := filepath.Rel(root, path)
			hits = append(hits, rel+":"+itoa(i+1)+": "+strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(hits) > 0 {
		t.Fatalf("CanonicalIsStub is back in the tree:\n  %s\n\n"+
			"The back-link rule must be consulted at EVERY call site or at NONE; one of each is\n"+
			"what stranded every Codex transcript. If this field is being restored deliberately,\n"+
			"restore it at all three sites, update\n"+
			"TestIntegrationBackLinkCallSitesAgreeOnAStubOnlySession, and re-read the task\n"+
			"codex-back-link-withheld-by-a-guard-only-one-call-site-applies before deleting this test.",
			strings.Join(hits, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// repoRootFromStorage walks up from this package to the module root, so the test
// does not depend on the working directory a runner happens to choose.
func repoRootFromStorage(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not find the module root above the storage package")
	return ""
}
