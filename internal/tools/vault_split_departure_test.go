// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/project"
)

// writeDepartureRecord puts a departure record into a vault the way
// storage.RecordDeparture would (it cannot be used here: the slug's tree must
// be absent, and the fixtures below describe slugs that were never present).
func writeDepartureRecord(t *testing.T, root string, r departure.Record) {
	t.Helper()
	b, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeSplitFile(t, root, departure.RelPath(r.Slug), string(b))
}

// T17. A successful purge writes one moved-to-vault record per purged slug,
// labelled by departure_to, and returns their paths; a refused purge writes
// none.
func TestPurgeRecordsADepartureOnlyOnSuccess(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		root := splitFixtureVault(t, "alpha", "beta")
		dest := splitDest(t)
		p := splitPlannedParams(t, root, dest, "alpha", "beta")
		p.Action = "apply"
		if _, err := callSplit(t, root, p); err != nil {
			t.Fatalf("apply: %v", err)
		}
		p.Action = "purge"
		p.DepartureTo = "git@example.com:me/quantum-vault.git"
		res, err := callSplit(t, root, p)
		if err != nil {
			t.Fatalf("purge: %v", err)
		}
		got, _ := res["departure_records"].([]any)
		if len(got) != 2 || got[0] != departure.RelPath("alpha") || got[1] != departure.RelPath("beta") {
			t.Fatalf("departure_records = %v, want one record per purged slug", res["departure_records"])
		}
		for _, s := range []string{"alpha", "beta"} {
			d, ok := project.Departed(root, s)
			if !ok || d.Source != "record" || d.Kind != departure.MovedToVault || d.To != p.DepartureTo {
				t.Errorf("%s: Departed = %+v %v, want moved-to-vault to the label, from the record", s, d, ok)
			}
		}
		// The destination holds the projects and no record claiming they left.
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(departure.Dir))); err == nil {
			t.Error("purge must not write departure records into the destination")
		}
	})

	t.Run("refused_purge_records_nothing", func(t *testing.T) {
		root := splitFixtureVault(t, "alpha")
		dest := splitDest(t)
		p := splitPlannedParams(t, root, dest, "alpha")
		p.Action = "apply"
		if _, err := callSplit(t, root, p); err != nil {
			t.Fatalf("apply: %v", err)
		}
		splitPurgeAfterVerify = func() {
			writeSplitFile(t, root, "Projects/alpha/sessions/2026-09-23-late-01.md", "late\n")
		}
		t.Cleanup(func() { splitPurgeAfterVerify = nil })
		p.Action = "purge"
		if _, err := callSplit(t, root, p); err == nil {
			t.Fatal("test premise: purge must refuse an unmanifested file")
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(departure.RelPath("alpha")))); err == nil {
			t.Error("a refused purge wrote a departure record for a project that is still here")
		}
	})
}

// T18. departure_to is a label synced to every host: a host path is refused
// BEFORE anything is removed, and the parameter is refused on any action other
// than purge rather than silently ignored.
func TestPurgeRefusesAnAbsoluteDepartureTo(t *testing.T) {
	for _, bad := range []string{"/home/me/quantum-vault", "~/quantum-vault", "~"} {
		t.Run(bad, func(t *testing.T) {
			root := splitFixtureVault(t, "alpha")
			dest := splitDest(t)
			p := splitPlannedParams(t, root, dest, "alpha")
			p.Action = "apply"
			if _, err := callSplit(t, root, p); err != nil {
				t.Fatalf("apply: %v", err)
			}
			before := snapshotTree(t, root)
			p.Action = "purge"
			p.DepartureTo = bad
			_, err := callSplit(t, root, p)
			if err == nil || !apperr.IsCaller(err) || !strings.Contains(err.Error(), "host path") {
				t.Fatalf("purge with departure_to=%q must be refused as a caller error naming the host path, got %v", bad, err)
			}
			if after := snapshotTree(t, root); !equalStringMaps(before, after) {
				t.Error("a refused departure_to must leave the source untouched")
			}
		})
	}

	root := splitFixtureVault(t, "alpha")
	p := splitPlanParams(splitDest(t), "alpha")
	p.DepartureTo = "quantum vault"
	if _, err := callSplit(t, root, p); err == nil || !strings.Contains(err.Error(), `only to action "purge"`) {
		t.Errorf("departure_to on plan must be refused, not ignored; got %v", err)
	}
}

// B2. include_audits carries the audit reports and baseline, but NOT the
// departure records: they describe the SOURCE vault's history, and copied into
// the destination they would claim a slug left a vault it was never in.
func TestSplitIncludeAuditsLeavesDepartureRecordsBehind(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	writeSplitFile(t, root, "Audits/2026-09-01-vault-audit.md", "# an audit\n")
	// gamma left the SOURCE vault long ago, renamed to delta.
	writeDepartureRecord(t, root, departure.Record{Slug: "gamma", Kind: departure.Renamed, To: "delta"})
	writeSplitFile(t, root, "Projects/delta/resume.md", "delta lives here\n")

	dest := splitDest(t)
	plan := splitPlanParams(dest, "alpha")
	plan.IncludeAudits = true
	res, err := callSplit(t, root, plan)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	plan.ManifestSHA256, _ = res["manifest_sha256"].(string)
	plan.Action = "apply"
	if _, err := callSplit(t, root, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Audits", "2026-09-01-vault-audit.md")); err != nil {
		t.Errorf("include_audits must still carry the audit report: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(departure.RelPath("gamma")))); err == nil {
		t.Error("include_audits copied the source's departure record into the destination")
	}
	if d, ok := project.Departed(dest, "gamma"); ok {
		t.Errorf("in the destination, gamma never existed, yet it reads as departed: %+v", d)
	}
	plan.Action = "verify"
	if _, err := callSplit(t, root, plan); err != nil {
		t.Errorf("verify must pass with the departure records left behind: %v", err)
	}

	// A record that ARRIVES after the plan (another host's departure, pulled)
	// no longer breaks purge's re-bind under include_audits.
	writeDepartureRecord(t, root, departure.Record{Slug: "epsilon", Kind: departure.MovedToVault})
	plan.Action = "purge"
	if _, err := callSplit(t, root, plan); err != nil {
		t.Errorf("a departure record written after plan must not break purge's bind: %v", err)
	}
}

// B2, merge. The same rule on the way back in: a source vault's departure
// records do not travel into the vault a merge folds it into.
func TestMergeIncludeAuditsLeavesDepartureRecordsBehind(t *testing.T) {
	dest := mergeDestVault(t, "alpha")
	source := mergeSourceVault(t, "beta")
	writeDepartureRecord(t, source, departure.Record{Slug: "gamma", Kind: departure.Renamed, To: "beta"})

	p := mergeParams(source, "beta")
	p.IncludeAudits = true
	res, err := callMerge(t, dest, p)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	p.ManifestSHA256, _ = res["manifest_sha256"].(string)
	p.Action = "apply"
	if _, err := callMerge(t, dest, p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "Audits", "2026-07-01-vault.md")); err != nil {
		t.Errorf("include_audits must still carry the source's audit report: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(departure.RelPath("gamma")))); err == nil {
		t.Error("merge copied the source's departure record into the destination")
	}
	if d, ok := project.Departed(dest, "gamma"); ok {
		t.Errorf("gamma never existed in the destination, yet it reads as departed: %+v", d)
	}
}
