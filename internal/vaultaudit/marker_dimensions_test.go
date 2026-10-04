// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The two marker-gated audit changes (task
// tidy-pull-and-audit-behaviour-keyed-on-the-migration-marker, Scope 3
// "Audits"; R3 for the malformed marker).

func auditGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

const (
	extractedTriple = `{"subject":"bob","predicate":"uses","object":"go","extracted_at":"2026-10-04T00:00:00Z"}`
	authoredTriple  = `{"subject":"alice","predicate":"works_on","object":"atlas","origin":"authored"}`
	entityLines     = `{"id":"alice","name":"alice","type":"unknown"}` + "\n" +
		`{"id":"person-bob","name":"Bob","type":"person","created_at":"2026-10-04T00:00:00Z"}` + "\n"
)

// markerVault is a git vault with tracked extracted and authored KG records, a
// tracked drawer, and a store (q) with no drawers at all. With migrated, the
// manifest carries the marker.
func markerVault(t *testing.T, manifest string) *storage.Vault {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	v := storage.NewVault(t.TempDir())
	root := v.Root
	auditGit(t, root, "init", "-q", "-b", "main")
	auditGit(t, root, "config", "user.email", "t@example.com")
	auditGit(t, root, "config", "user.name", "T")
	writeFile(t, root, ".vibe-palace/vault.toml", manifest)
	writeFile(t, root, "palace/p/kg/triples/bob--uses--go.json", extractedTriple)
	writeFile(t, root, "palace/p/kg/triples/alice--works_on--atlas.json", authoredTriple)
	writeFile(t, root, "palace/p/kg/entities.jsonl", entityLines)
	writeFile(t, root, "palace/p/drawers/w/r/drawers.jsonl", `{"id":"d"}`+"\n")
	writeFile(t, root, "palace/q/.surface", "surface = 8\n")
	mkdirs(t, root, "Projects", "p")
	mkdirs(t, root, "Projects", "q")
	auditGit(t, root, "add", "-A")
	auditGit(t, root, "commit", "-q", "-m", "vault")
	return v
}

const (
	unmigratedManifest = "format = 2\n"
	migratedManifest   = "format = 2\nauthored_only = \"2026-10-04\"\n"
	malformedManifest  = "format = 2\nauthored_only = 5\n"
)

func resultFor(t *testing.T, r Report, name string) DimensionResult {
	t.Helper()
	for _, d := range r.Dimensions {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no %s row", name)
	return DimensionResult{}
}

// kg-tracked-extracted, migrated: the tracked extracted triple and the
// extracted entity line are reported; the authored triple is not; and
// kg-portability's output is unchanged. Mutant: repurposing kg-portability.
func TestKGTrackedExtracted_ReportsOnAMigratedVault(t *testing.T) {
	v := markerVault(t, migratedManifest)
	findings, unknowns, err := auditKGTrackedExtracted(v)
	if err != nil || len(unknowns) > 0 {
		t.Fatalf("err %v, unknowns %v", err, unknowns)
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.Artifact)
	}
	slices.Sort(got)
	want := []string{"palace/p/kg/entities.jsonl", "palace/p/kg/triples/bob--uses--go.json"}
	if !slices.Equal(got, want) {
		t.Errorf("findings %v, want %v", got, want)
	}
	before, _, _ := auditKGPortability(markerVault(t, unmigratedManifest))
	after, _, _ := auditKGPortability(v)
	if len(before) != len(after) {
		t.Errorf("kg-portability changed with the marker: %v -> %v", before, after)
	}
}

// Audit changes are off without the marker (10-S1): kg-tracked-extracted
// reports nothing, and palace-store-drawers runs (q has no drawers). Mutant:
// an ungated kg-tracked-extracted; an unconditional palace-store-drawers skip.
func TestMarkerAuditChanges_OffWithoutTheMarker(t *testing.T) {
	v := markerVault(t, unmigratedManifest)
	r, err := Run(v)
	if err != nil {
		t.Fatal(err)
	}
	if d := resultFor(t, r, DimKGTrackedExtracted); len(d.New) > 0 || d.Status == StatusSkipped {
		t.Errorf("kg-tracked-extracted without the marker: %+v", d)
	}
	d := resultFor(t, r, DimPalaceStoreDrawers)
	if d.Status == StatusSkipped || !slices.ContainsFunc(d.New, func(f Finding) bool { return f.Artifact == "q" }) {
		t.Errorf("palace-store-drawers did not run as before: %+v", d)
	}
}

// palace-store-drawers skipped when migrated (10-S1): an ignored drawer residue
// produces no finding, and the row says why. Mutant: no skip.
func TestPalaceStoreDrawers_SkippedWhenMigrated(t *testing.T) {
	v := markerVault(t, migratedManifest)
	r, err := Run(v)
	if err != nil {
		t.Fatal(err)
	}
	d := resultFor(t, r, DimPalaceStoreDrawers)
	if d.Status != StatusSkipped || len(d.New) > 0 || !strings.Contains(d.Skipped, "migration marker") {
		t.Errorf("want skipped with the reason: %+v", d)
	}
	if md := r.Render("2026-10-04", v.Root); !strings.Contains(md, "Not run on this vault") {
		t.Errorf("the report does not say why it was skipped")
	}
}

// A malformed marker: one kg-tracked-extracted finding naming authored_only,
// and palace-store-drawers runs (the more inclusive report). Mutant: the error
// read as unmigrated (no finding); the error failing the dimension (unknown).
func TestMarkerAuditChanges_MalformedMarker(t *testing.T) {
	v := markerVault(t, malformedManifest)
	r, err := Run(v)
	if err != nil {
		t.Fatal(err)
	}
	d := resultFor(t, r, DimKGTrackedExtracted)
	if len(d.New) != 1 || !strings.Contains(d.New[0].Detail, "authored_only") {
		t.Errorf("want one finding naming authored_only: %+v", d)
	}
	if p := resultFor(t, r, DimPalaceStoreDrawers); p.Status == StatusSkipped {
		t.Errorf("palace-store-drawers skipped on an unreadable marker: %+v", p)
	}
}
