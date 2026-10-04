// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const (
	tripleA  = "palace/p/kg/triples/s/alice--works_on--atlas.json"
	tripleB  = "palace/p/kg/triples/s/bob--uses--go.json"
	entities = "palace/p/kg/entities.jsonl"
)

// tidyMigrated runs a real (local) tidy on dir and returns the result.
func tidyMigrated(t *testing.T, dir string) *TidyResult {
	t.Helper()
	res, err := TidyVault(dir, false)
	if err != nil {
		t.Fatalf("TidyVault: %v", err)
	}
	return res
}

// Authored triple, migrated: staged. Extracted, and extracted-shaped with no
// origin, migrated: reported, not staged. Mutant: deleting the triple rule
// (nothing staged); an unfiltered triple rule (extracted staged).
func TestTidy_MigratedTriplesByOrigin(t *testing.T) {
	dir := layMigratedVault(t)
	writeFile(t, dir, tripleA, `{"subject":"alice","predicate":"works_on","object":"atlas","origin":"authored"}`)
	writeFile(t, dir, tripleB, `{"subject":"bob","predicate":"uses","object":"go","origin":"extracted","extracted_at":"2026-10-04T00:00:00Z"}`)
	unstamped := "palace/p/kg/triples/s/carol--uses--rust.json"
	writeFile(t, dir, unstamped, `{"subject":"carol","predicate":"uses","object":"rust","extracted_at":"2026-10-04T00:00:00Z"}`)
	res := tidyMigrated(t, dir)
	if !slices.Contains(res.Swept, tripleA) {
		t.Errorf("authored triple not swept: %+v", res)
	}
	for _, p := range []string{tripleB, unstamped} {
		if slices.Contains(res.Swept, p) || !slices.Contains(res.Reported, p) {
			t.Errorf("%s: want reported, not swept: %+v", p, res)
		}
	}
}

// The same triples on an unmigrated vault are all swept, as at HEAD.
func TestTidy_UnmigratedTriplesAllSwept(t *testing.T) {
	dir := layUnmigratedDrawerVault(t)
	writeFile(t, dir, tripleB, `{"subject":"bob","predicate":"uses","object":"go","extracted_at":"2026-10-04T00:00:00Z"}`)
	if res := tidyMigrated(t, dir); !slices.Contains(res.Swept, tripleB) {
		t.Errorf("unmigrated extracted triple not swept: %+v", res)
	}
}

// A dirty triple tidy cannot read — deleted, or not JSON — is reported and
// tidy does not error.
func TestTidy_MigratedUnreadableTripleIsReported(t *testing.T) {
	dir := layMigratedVault(t)
	writeFile(t, dir, tripleA, `{"subject":"alice","predicate":"works_on","object":"atlas","origin":"authored"}`)
	gitRun(t, dir, "add", "--", tripleA)
	gitRun(t, dir, "commit", "-q", "-m", "authored")
	if err := os.Remove(filepath.Join(dir, tripleA)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, tripleB, `{not json`)
	res := tidyMigrated(t, dir)
	for _, p := range []string{tripleA, tripleB} {
		if slices.Contains(res.Swept, p) || !slices.Contains(res.Reported, p) {
			t.Errorf("%s: want reported, not swept: %+v", p, res)
		}
	}
}

// entities.jsonl line diff, migrated: only authored lines added, staged; one
// extractor line, or one line that does not decode, and the whole file is
// reported. Mutant: file-level staging.
func TestTidy_MigratedEntitiesLineDiff(t *testing.T) {
	authored := `{"id":"alice","name":"alice","type":"unknown","origin":"authored"}`
	extracted := `{"id":"person-bob","name":"Bob","type":"person","created_at":"2026-10-04T00:00:00Z","origin":"extracted"}`
	cases := []struct {
		name  string
		added string
		swept bool
	}{
		{"only authored lines", authored + "\n", true},
		{"one extractor line", authored + "\n" + extracted + "\n", false},
		{"one line that does not decode", "{oops\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := layMigratedVault(t)
			base := `{"id":"x","name":"x","type":"unknown"}` + "\n"
			writeFile(t, dir, entities, base)
			gitRun(t, dir, "add", "--", entities)
			gitRun(t, dir, "commit", "-q", "-m", "entities")
			writeFile(t, dir, entities, base+c.added)
			res := tidyMigrated(t, dir)
			if got := slices.Contains(res.Swept, entities); got != c.swept {
				t.Errorf("swept = %v, want %v: %+v", got, c.swept, res)
			}
			if !c.swept && !slices.Contains(res.Reported, entities) {
				t.Errorf("not reported: %+v", res)
			}
		})
	}
}

// A re-tracked drawer on a migrated vault is reported, never swept: the
// drawer sweep rule is dropped. Mutant: the drawer rule kept.
func TestTidy_MigratedDrawerIsNotSwept(t *testing.T) {
	dir := layMigratedVault(t)
	writeFile(t, dir, fixtureDrawer, `{"id":"re-tracked"}`+"\n")
	gitRun(t, dir, "add", "-f", "--", fixtureDrawer)
	gitRun(t, dir, "commit", "-q", "-m", "re-tracked by a clean merge")
	writeFile(t, dir, fixtureDrawer, `{"id":"re-tracked"}`+"\n"+`{"id":"enriched"}`+"\n")
	res := tidyMigrated(t, dir)
	if slices.Contains(res.Swept, fixtureDrawer) || !slices.Contains(res.Reported, fixtureDrawer) {
		t.Errorf("want the drawer reported, not swept: %+v", res)
	}
}

// A malformed marker fails tidy and stages nothing (R3). Mutant: the error
// read as "unmigrated".
func TestTidy_MalformedMarkerFailsTheRun(t *testing.T) {
	dir := layUnmigratedDrawerVault(t)
	writeMalformedMarker(t, dir)
	writeFile(t, dir, tripleB, `{"subject":"bob","predicate":"uses","object":"go","extracted_at":"2026-10-04T00:00:00Z"}`)
	_, err := TidyScan(dir)
	wantMarkerErr(t, "TidyScan", err)
	_, err = TidyVault(dir, false)
	wantMarkerErr(t, "TidyVault", err)
	if staged := gitRun(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("staged %q", staged)
	}
}
