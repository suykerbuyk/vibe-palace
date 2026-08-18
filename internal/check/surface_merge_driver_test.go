// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// writeVaultFile creates a file (and its parents) under root.
func writeVaultFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// newFixtureVault returns a vault root with a plausible shape, so the walk has
// real directories to traverse and the dirs>0 guard is exercised rather than
// stubbed.
func newFixtureVault(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeVaultFile(t, root, "Projects/demo/resume.md", "# demo\n")
	writeVaultFile(t, root, "Projects/demo/tasks/open.md", "# task\n")
	return root
}

// TestSurfaceMergeDriver_FlagsTheAttribute is the load-bearing case: the shape
// the deleted Behavioral Note forbade.
func TestSurfaceMergeDriver_FlagsTheAttribute(t *testing.T) {
	root := newFixtureVault(t)
	writeVaultFile(t, root, ".gitattributes", "*.surface merge=vp-surface\n")

	r := CheckSurfaceMergeDriver(storage.NewVault(root))

	if r.Status != Fail {
		t.Fatalf("status = %v, want Fail (summary %q)", r.Status, r.Summary)
	}
	joined := strings.Join(r.Details, "\n")
	if !strings.Contains(joined, ".gitattributes:1") {
		t.Errorf("details do not name the offending file:line — got:\n%s", joined)
	}
	if !strings.Contains(joined, "*.surface") {
		t.Errorf("details do not name the pattern the attribute is attached to — got:\n%s", joined)
	}
	// The need-to-know text must ride the failing row and nowhere else.
	if !strings.Contains(joined, "silently falls back to a") {
		t.Errorf("failing row does not carry the why (git's silent text merge) — got:\n%s", joined)
	}
}

// TestSurfaceMergeDriver_NeedToKnowTextOnlyOnFailure pins that the hazard text
// is attached to a Fail and to nothing else. A rule whose explanation prints on
// every healthy run teaches the reader to skim it (the 289 lesson).
func TestSurfaceMergeDriver_NeedToKnowTextOnlyOnFailure(t *testing.T) {
	root := newFixtureVault(t)

	clean := CheckSurfaceMergeDriver(storage.NewVault(root))
	if clean.Status != Pass {
		t.Fatalf("clean vault status = %v, want Pass (%q)", clean.Status, clean.Summary)
	}
	if len(clean.Details) != 0 {
		t.Errorf("clean row carries %d detail line(s), want 0:\n%s",
			len(clean.Details), strings.Join(clean.Details, "\n"))
	}

	writeVaultFile(t, root, ".gitattributes", "*.surface merge=vp-surface\n")
	dirty := CheckSurfaceMergeDriver(storage.NewVault(root))
	if !strings.Contains(strings.Join(dirty.Details, "\n"), surfaceMergeDriverHazard[0]) {
		t.Error("failing row is missing the hazard text")
	}
}

// TestSurfaceMergeDriver_NeverEchoesAHostPath pins the 303 rule: this check must
// state the constraint, never a host-local location. It reads no git config at
// all, so the strongest available assertion is that no output line carries an
// absolute host root or a config path.
func TestSurfaceMergeDriver_NeverEchoesAHostPath(t *testing.T) {
	root := newFixtureVault(t)
	writeVaultFile(t, root, ".gitattributes", "*.surface merge=vp-surface\n")

	r := CheckSurfaceMergeDriver(storage.NewVault(root))
	all := r.Summary + "\n" + strings.Join(r.Details, "\n")

	if strings.Contains(all, root) {
		t.Errorf("output echoes the absolute vault root — paths in this check must be vault-relative:\n%s", all)
	}
	for _, banned := range []string{".gitconfig", "/home/", "/Users/", "--global"} {
		if strings.Contains(all, banned) {
			t.Errorf("output names host-local location %q — write the CONSTRAINT, never the PATH:\n%s", banned, all)
		}
	}
}

// TestSurfaceMergeDriver_Cases covers the near-misses that must stay green and
// the variants that must not.
func TestSurfaceMergeDriver_Cases(t *testing.T) {
	cases := []struct {
		name string
		rel  string
		body string
		want Status
	}{
		{
			name: "bare driver token, the half-remembered re-add",
			rel:  ".gitattributes",
			body: "*.surface vp-surface\n",
			want: Fail,
		},
		{
			name: "nested .gitattributes is found, proving the walk descends",
			rel:  "Projects/demo/.gitattributes",
			body: "*.surface merge=vp-surface\n",
			want: Fail,
		},
		{
			name: "a commented-out line is not an attribute",
			rel:  ".gitattributes",
			body: "# *.surface merge=vp-surface\n",
			want: Pass,
		},
		{
			name: "an unrelated .gitattributes is fine",
			rel:  ".gitattributes",
			body: "*.md text\n*.zst binary\n",
			want: Pass,
		},
		{
			name: "a different merge driver is not ours to police",
			rel:  ".gitattributes",
			body: "*.json merge=ours\n",
			want: Pass,
		},
		{
			name: "a pattern merely named vp-surface is not an attribute",
			rel:  ".gitattributes",
			body: "vp-surface text\n",
			want: Pass,
		},
		{
			name: "host-local .git/info/attributes is deliberately out of scope",
			rel:  ".git/info/attributes",
			body: "*.surface merge=vp-surface\n",
			want: Pass,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := newFixtureVault(t)
			writeVaultFile(t, root, tc.rel, tc.body)
			r := CheckSurfaceMergeDriver(storage.NewVault(root))
			if r.Status != tc.want {
				t.Fatalf("status = %v, want %v (summary %q, details %v)",
					r.Status, tc.want, r.Summary, r.Details)
			}
		})
	}
}

// TestSurfaceMergeDriver_PassSummaryNamesWhatItInspected is the anti-vacuity
// assertion for the healthy path.
//
// The live vault has NO .gitattributes at all, so "0 findings" is the correct
// answer and cannot itself distinguish a working walk from a broken one — the
// 296 shape, with the usual tripwire unavailable. The Pass row therefore has to
// SAY what it traversed: zero files is a signal only when the directory count
// beside it proves the walk ran.
func TestSurfaceMergeDriver_PassSummaryNamesWhatItInspected(t *testing.T) {
	root := newFixtureVault(t)

	r := CheckSurfaceMergeDriver(storage.NewVault(root))
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass", r.Status)
	}
	if !strings.Contains(r.Summary, "0 .gitattributes") {
		t.Errorf("summary does not report the inspected file count: %q", r.Summary)
	}
	if strings.Contains(r.Summary, "0 dirs walked") {
		t.Errorf("summary claims a Pass while reporting no traversal: %q", r.Summary)
	}
	if !strings.Contains(r.Summary, "dirs walked") {
		t.Errorf("summary does not report the directory count, so its silence is unfalsifiable: %q", r.Summary)
	}
}

// TestSurfaceMergeDriver_WithheldVerdictOnAnUntraversedRoot pins that a walk
// which traverses nothing reports a withheld verdict rather than a clean one.
func TestSurfaceMergeDriver_WithheldVerdictOnAnUntraversedRoot(t *testing.T) {
	root := t.TempDir() // exists, but contains no directories at all

	r := CheckSurfaceMergeDriver(storage.NewVault(root))
	if r.Status == Pass {
		t.Fatalf("an untraversed root reported Pass — that is a vacuous green: %q", r.Summary)
	}
	if !strings.Contains(r.Summary, "withheld") {
		t.Errorf("summary = %q, want a withheld verdict", r.Summary)
	}
}

// TestSurfaceMergeDriver_NoVault pins the degradation contract every wrapped
// producer shares.
func TestSurfaceMergeDriver_NoVault(t *testing.T) {
	if r := CheckSurfaceMergeDriver(storage.NewVault("")); r.Status != Skip {
		t.Errorf("empty root status = %v, want Skip", r.Status)
	}
	if r := CheckSurfaceMergeDriver(nil); r.Status != Skip {
		t.Errorf("nil vault status = %v, want Skip", r.Status)
	}
}

// TestSurfaceMergeDriver_SelectorWiring pins that the check is reachable by the
// name the delivery templates use. A producer nothing dispatches is this
// project's signature defect.
func TestSurfaceMergeDriver_SelectorWiring(t *testing.T) {
	root := newFixtureVault(t)
	writeVaultFile(t, root, ".gitattributes", "*.surface merge=vp-surface\n")

	results, err := RunSelected(root, "surface-merge-driver")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d rows, want 1: %v", len(results), results)
	}
	if results[0].Status != Fail {
		t.Errorf("dispatched row status = %v, want Fail", results[0].Status)
	}
}
