// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// newDivergentVault builds a vault whose two trees DISAGREE, which is the whole
// point: palace-only, Projects-only, and both-trees projects all coexist in the
// live vault, and an enumerator that reads one tree cannot see the other.
func newDivergentVault(t *testing.T) *Vault {
	t.Helper()
	root := t.TempDir()

	mk := func(parts ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(append([]string{root}, parts...)...), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// In BOTH trees.
	mk("palace", "vibe-palace")
	mk("Projects", "vibe-palace")

	// palace/ only — indexed, but no history was ever written.
	mk("palace", "mandelbulb")

	// Projects/ only — history captured, never drawer-indexed. This is the
	// class ListProjects is blind to (live vault: 73 session notes).
	mk("Projects", "rusty-can", "sessions")

	// Noise that must be filtered identically in both trees.
	mk("palace", ".local")
	mk("Projects", ".local")
	mk("palace", "Not A Slug")
	if err := os.WriteFile(filepath.Join(root, "palace", "loose.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	return &Vault{Root: root}
}

func TestListAllProjects_UnionOfBothTrees(t *testing.T) {
	v := newDivergentVault(t)

	got, err := v.ListAllProjects()
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}

	want := []ProjectPresence{
		{Slug: "mandelbulb", InPalace: true, InProjects: false},
		{Slug: "rusty-can", InPalace: false, InProjects: true},
		{Slug: "vibe-palace", InPalace: true, InProjects: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ListAllProjects() = %+v\nwant %+v", got, want)
	}
}

// TestListAllProjects_SeesWhatListProjectsCannot is the regression that matters:
// it pins the exact blind spot REVERSAL 1 found. A Projects/-only project is
// invisible to ListProjects, and a vault-global caller that reaches for
// ListProjects audits a corpus it never looked at.
func TestListAllProjects_SeesWhatListProjectsCannot(t *testing.T) {
	v := newDivergentVault(t)

	palaceOnly, err := v.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(palaceOnly, "rusty-can") {
		t.Fatal("premise broken: ListProjects is supposed to be blind to Projects/-only projects")
	}

	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(all, func(p ProjectPresence) bool { return p.Slug == "rusty-can" }) {
		t.Fatal("ListAllProjects missed a Projects/-only project — it is blind to exactly the " +
			"notes the union enumerator exists to reach")
	}
}

// TestListAllProjects_FilterMatchesListProjects pins the two enumerators to ONE
// filter. If they disagreed about what counts as a project (a slug rule applied
// in one tree and not the other), the union would report drift that is really
// just an inconsistent filter -- an auditor inventing findings is worse than one
// that misses them, because it trains you to wave off the real ones.
func TestListAllProjects_FilterMatchesListProjects(t *testing.T) {
	v := newDivergentVault(t)

	fromListProjects, err := v.ListProjects()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(fromListProjects)

	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	var fromUnion []string
	for _, p := range all {
		if p.InPalace {
			fromUnion = append(fromUnion, p.Slug)
		}
	}

	if !slices.Equal(fromListProjects, fromUnion) {
		t.Fatalf("the two enumerators disagree about palace/: ListProjects=%v union(InPalace)=%v",
			fromListProjects, fromUnion)
	}

	// And the noise is gone from both: .local, a loose file, and an invalid slug.
	for _, bad := range []string{".local", "loose.txt", "Not A Slug"} {
		if slices.Contains(fromUnion, bad) {
			t.Errorf("%q must not be enumerated as a project", bad)
		}
	}
}

func TestListAllProjects_Complete(t *testing.T) {
	cases := []struct {
		name string
		p    ProjectPresence
		want bool
	}{
		{"both trees", ProjectPresence{InPalace: true, InProjects: true}, true},
		{"palace only", ProjectPresence{InPalace: true}, false},
		{"projects only", ProjectPresence{InProjects: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.Complete(); got != c.want {
				t.Fatalf("Complete() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestListAllProjects_MissingTreesAreNotErrors: an absent tree contributes
// nothing. A fresh vault with no palace/ yet is not a broken vault.
func TestListAllProjects_MissingTreesAreNotErrors(t *testing.T) {
	v := &Vault{Root: t.TempDir()}

	got, err := v.ListAllProjects()
	if err != nil {
		t.Fatalf("empty vault should enumerate cleanly, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty vault enumerated %+v, want none", got)
	}
}

// TestListAllProjects_LiveVaultCanary runs the enumerator against a REAL vault
// and reports what ListProjects cannot see. It is opt-in:
//
//	VP_LIVE_VAULT=~/obsidian/vibe-palace-vault go test ./internal/storage/ -run LiveVaultCanary -v -count=1
//
// -count=1 matters: the vault is OUTSIDE the module, so `go test` cannot see its
// contents change and will serve a CACHED verdict for a vault it never re-read.
//
// It exists because every bug this enumerator was built to fix survived a green
// unit suite. Fixtures are built by the same person holding the same wrong
// mental model; the live vault is not. It asserts the INVARIANT (the union is a
// superset, and it is strictly larger whenever the trees diverge) and PRINTS the
// counts rather than pinning them -- a hard-coded census rots by the next run,
// so record the grep, never the count.
func TestListAllProjects_LiveVaultCanary(t *testing.T) {
	root := os.Getenv("VP_LIVE_VAULT")
	if root == "" {
		t.Skip("set VP_LIVE_VAULT=<vault root> to run the live canary")
	}
	v := &Vault{Root: root}

	palaceOnly, err := v.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatalf("ListAllProjects: %v", err)
	}

	if len(all) < len(palaceOnly) {
		t.Fatalf("the union (%d) is smaller than palace/ alone (%d) — it is not a union",
			len(all), len(palaceOnly))
	}

	var invisible, phantom int
	for _, p := range all {
		switch {
		case !p.InPalace:
			invisible++
			notes, _ := filepath.Glob(filepath.Join(root, "Projects", p.Slug, "sessions", "*.md"))
			t.Logf("INVISIBLE to ListProjects: %-20s %d session notes", p.Slug, len(notes))
		case !p.InProjects:
			phantom++
			t.Logf("phantom (palace/ only, no history): %s", p.Slug)
		}
	}
	t.Logf("union=%d  palace-only-enumerator=%d  invisible=%d  phantom=%d",
		len(all), len(palaceOnly), invisible, phantom)

	// Every project ListProjects found must still be in the union. A union that
	// drops a project is worse than the blind spot it replaces.
	for _, s := range palaceOnly {
		if !slices.ContainsFunc(all, func(p ProjectPresence) bool { return p.Slug == s }) {
			t.Errorf("union dropped %q, which ListProjects found", s)
		}
	}
}

// TestListAllProjects_UnreadableTreeIsAnError: the auditor's own invariant --
// "I could not look" is NOT "there was nothing there." An existing but
// unreadable tree must fail loudly rather than silently enumerate a subset.
func TestListAllProjects_UnreadableTreeIsAnError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	palaceDir := filepath.Join(root, "palace")
	if err := os.MkdirAll(palaceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(palaceDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(palaceDir, 0o755) })

	v := &Vault{Root: root}
	if _, err := v.ListAllProjects(); err == nil {
		t.Fatal("an unreadable palace/ must be an error, not an empty enumeration — " +
			"silently returning a subset is how an audit reports a clean bill of health " +
			"for a corpus it could not read")
	}
}
