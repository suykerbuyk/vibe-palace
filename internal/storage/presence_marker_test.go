// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
)

// presenceOf returns the InPalace slugs and the PalaceNonStores slugs.
func presenceOf(t *testing.T, root string) (stores, nonStores []string) {
	t.Helper()
	v := &Vault{Root: root}
	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range all {
		if p.InPalace {
			stores = append(stores, p.Slug)
		}
	}
	ns, err := v.PalaceNonStores()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range ns {
		nonStores = append(nonStores, n.Slug)
	}
	return stores, nonStores
}

// Presence across hosts: host A holds stray ignored drawers for a project
// with no other palace file; host B is clean. Both report the same stores and
// the same PalaceNonStores. Mutant: counting ignored files; changing only one
// of the predicate's two callers.
func TestPresence_SameAcrossHosts(t *testing.T) {
	hostA := layMigratedVault(t)
	hostB := layMigratedVault(t)
	writeFile(t, hostA, "palace/q/drawers/w/r/drawers.jsonl", "{}\n")
	writeFile(t, hostA, "palace/q/ingested-archives.jsonl", "{}\n")
	writeFile(t, hostA, "palace/q/.local/x", "x\n")
	sa, na := presenceOf(t, hostA)
	sb, nb := presenceOf(t, hostB)
	if slices.Contains(sa, "q") {
		t.Errorf("host A counts stray derived files as a store: %v", sa)
	}
	// The two callers still partition palace/: q is not a store, so it is a
	// non-store.
	if !slices.Contains(na, "q") {
		t.Errorf("host A: q is neither a store nor a non-store: stores %v, non-stores %v", sa, na)
	}
	if !slices.Equal(sa, sb) {
		t.Errorf("stores differ between hosts: A %v, B %v", sa, sb)
	}
	if !slices.Equal(slices.DeleteFunc(na, func(s string) bool { return s == "q" }), nb) {
		t.Errorf("non-stores differ beyond q: A %v, B %v", na, nb)
	}
}

// Presence before the marker (10-S2): on an unmigrated vault a project whose
// only palace/ file is a TRACKED drawer is not InPalace (drift); one that also
// holds a tracked kg/ triple is. Mutant: a derived clause gated on the
// marker; a derived clause keyed on ignore status.
func TestPresence_BeforeTheMarker(t *testing.T) {
	dir := layUnmigratedDrawerVault(t) // palace/p/ holds only a tracked drawer
	writeFile(t, dir, "Projects/p/resume.md", "r\n")
	writeFile(t, dir, "palace/q/drawers/w/r/drawers.jsonl", "{}\n")
	writeFile(t, dir, "palace/q/kg/triples/s/a.json", "{}\n")
	gitRun(t, dir, "add", "--", "Projects/p/resume.md", "palace/q")
	gitRun(t, dir, "commit", "-q", "-m", "q")
	v := &Vault{Root: dir}
	all, err := v.ListAllProjects()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ProjectPresence{}
	for _, p := range all {
		got[p.Slug] = p
	}
	if p := got["p"]; p.InPalace || !p.InProjects {
		t.Errorf("p = %+v; want Projects-only (drift): a tracked drawer is not presence", p)
	}
	if !got["q"].InPalace {
		t.Errorf("q = %+v; want InPalace through its tracked triple", got["q"])
	}
}

// Presence ignores the lock directory: palace/.local/locks/ changes nothing.
func TestPresence_IgnoresTheLockDirectory(t *testing.T) {
	dir := layMigratedVault(t)
	writeFile(t, dir, "palace/q/kg/triples/s/a.json", "{}\n")
	gitRun(t, dir, "add", "--", "palace/q")
	gitRun(t, dir, "commit", "-q", "-m", "q")
	s1, n1 := presenceOf(t, dir)
	writeFile(t, dir, "palace/.local/locks/index-run.holder", "pid 1\n")
	s2, n2 := presenceOf(t, dir)
	if !slices.Equal(s1, s2) || !slices.Equal(n1, n2) {
		t.Errorf("the lock directory changed presence: %v/%v -> %v/%v", s1, n1, s2, n2)
	}
}

// Presence kg/ clause. Migrated: a project whose only file is an untracked,
// unignored kg/ triple is not present; a tracked authored triple makes it
// present. Unmigrated: the same untracked triple makes it present, and no
// `git ls-files` runs. Mutant: the clause without the marker; untracked kg/
// residue counted after it.
func TestPresence_KGClause(t *testing.T) {
	dir := layMigratedVault(t)
	writeFile(t, dir, "palace/stray/kg/triples/s/a.json", "{}\n")
	writeFile(t, dir, "palace/kept/kg/triples/s/a.json", `{"origin":"authored"}`)
	gitRun(t, dir, "add", "--", "palace/kept")
	gitRun(t, dir, "commit", "-q", "-m", "kept")
	stores, nonStores := presenceOf(t, dir)
	if slices.Contains(stores, "stray") || !slices.Contains(nonStores, "stray") {
		t.Errorf("migrated: untracked kg/ residue counted: stores %v, non-stores %v", stores, nonStores)
	}
	if !slices.Contains(stores, "kept") {
		t.Errorf("migrated: a tracked triple not counted: %v", stores)
	}

	un := layUnmigratedDrawerVault(t)
	writeFile(t, un, "palace/stray/kg/triples/s/a.json", "{}\n")
	r := newPresenceRule(un)
	holds, err := r.holdsPresentFile(un + "/palace/stray")
	if err != nil || !holds {
		t.Errorf("unmigrated: untracked triple not counted: %v, %v", holds, err)
	}
	if r.listed {
		t.Error("unmigrated: git ls-files ran; the kg/ clause must cost nothing before the marker")
	}
}

// A malformed marker: presence is a reporter, so it takes the more inclusive
// pre-marker rule (the untracked kg/ triple counts) and logs a vp.log line
// naming authored_only. Mutant: the error silently read as "unmigrated" (no
// log line); the error failing the enumeration.
func TestPresence_MalformedMarkerIsInclusiveAndLogged(t *testing.T) {
	dir := layMigratedVault(t)
	writeFile(t, dir, "palace/stray/kg/triples/s/a.json", "{}\n")
	writeMalformedMarker(t, dir)
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	stores, _ := presenceOf(t, dir)
	if !slices.Contains(stores, "stray") {
		t.Errorf("malformed marker: want the inclusive rule (stray present), got %v", stores)
	}
	if !strings.Contains(buf.String(), "authored_only") || !strings.Contains(buf.String(), "presence") {
		t.Errorf("no vp.log line naming authored_only: %q", buf.String())
	}
}
