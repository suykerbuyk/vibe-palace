// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// seedManifest writes a transcript manifest (and its archive companion) for project
// p. noteRel is what the manifest claims to back-link to; "" means it links nowhere.
func seedManifest(t *testing.T, vault *storage.Vault, p, sessionID, noteRel string) string {
	t.Helper()
	dir := filepath.Join(vault.Root, "Projects", p, "transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stem := "2026-07-14-" + sessionID
	m := archive.Manifest{
		SchemaVersion:       1,
		Adapter:             "claude-code",
		SessionID:           sessionID,
		ProjectSlug:         p,
		CapturedAt:          "2026-07-14T01:00:00Z",
		VaultRelSessionNote: noteRel,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mp := filepath.Join(dir, stem+".manifest.json")
	if err := os.WriteFile(mp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, stem+".jsonl.zst"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return "Projects/" + p + "/transcripts/" + stem + ".manifest.json"
}

// seedNote writes a session note so a manifest's back-link can resolve.
func seedNote(t *testing.T, vault *storage.Vault, p, name string) string {
	t.Helper()
	dir := filepath.Join(vault.Root, "Projects", p, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("# note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return "Projects/" + p + "/sessions/" + name
}

// 🔴 MUTATION TEST — THE ONE THAT MATTERS.
//
// internal/sourceaudit was twice built with a bug that made it report ZERO findings
// on a tree full of defects: the disease inside the tool built to cure it. An auditor
// that CANNOT FAIL is worse than no auditor, because it issues a clean bill of health.
//
// So: hand the auditor a vault with three KNOWN, DELIBERATE defects and assert it
// finds exactly those three. If someone breaks the dimension so it returns nothing,
// this test goes red — which is the only reason to trust the clean-vault test below.
func TestArchiveRoundTrip_FindsKnownDefects(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// Defect 1: a manifest that back-links to nothing. The transcript is stranded.
	stranded := seedManifest(t, vault, "alpha", "aaaa", "")

	// Defect 2: a manifest whose back-link points at a note that does not exist.
	// A dangling link is WORSE than a missing one: it reports success and delivers
	// nothing.
	dangling := seedManifest(t, vault, "alpha", "bbbb", "Projects/alpha/sessions/ghost.md")

	// Defect 3: the same, in a project that exists ONLY under Projects/ — invisible
	// to the old palace-only enumerator, and the whole reason phase 2 happened.
	orphanProject := seedManifest(t, vault, "history-only", "cccc", "")

	// A CORRECT manifest, to prove the auditor is not simply flagging everything.
	note := seedNote(t, vault, "alpha", "2026-07-14-good.md")
	good := seedManifest(t, vault, "alpha", "dddd", note)

	findings, unknowns, err := auditArchiveRoundTrip(vault)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(unknowns) != 0 {
		t.Fatalf("unknowns = %v, want none — every tree was readable", unknowns)
	}

	got := map[string]string{}
	for _, f := range findings {
		got[f.Artifact] = f.Detail
	}

	for _, want := range []string{stranded, dangling, orphanProject} {
		if _, ok := got[want]; !ok {
			t.Errorf("THE AUDITOR MISSED A DELIBERATE DEFECT: %s\n  found: %v", want, got)
		}
	}
	if _, wrong := got[good]; wrong {
		t.Errorf("the auditor flagged a CORRECT manifest (%s) — an auditor that invents "+
			"findings teaches you to wave off the real ones", good)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %d, want exactly 3", len(findings))
	}

	if !strings.Contains(got[dangling], "DANGLING") {
		t.Errorf("the dangling-link finding does not say so: %q", got[dangling])
	}
}

// TestArchiveRoundTrip_CleanVaultIsClean — the other half of the mutation pair. This
// test is only meaningful BECAUSE the test above proves the auditor can fail.
func TestArchiveRoundTrip_CleanVaultIsClean(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	note := seedNote(t, vault, "alpha", "2026-07-14-good.md")
	seedManifest(t, vault, "alpha", "aaaa", note)

	findings, unknowns, err := auditArchiveRoundTrip(vault)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("a correctly-linked vault produced findings: %+v", findings)
	}
	if len(unknowns) != 0 {
		t.Fatalf("unknowns = %v, want none", unknowns)
	}
}

// TestArchiveRoundTrip_UnreadableTranscriptsIsUnknown pins the auditor's own
// invariant onto itself: a tree it cannot READ is `unknown`, never `pass`. This is
// the vp_health bug of 201 (it reported `healthy` for a log it could not read),
// applied to the thing doing the auditing.
func TestArchiveRoundTrip_UnreadableTranscriptsIsUnknown(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	vault := storage.NewVault(t.TempDir())
	seedManifest(t, vault, "alpha", "aaaa", "")

	dir := filepath.Join(vault.Root, "Projects", "alpha", "transcripts")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, unknowns, err := auditArchiveRoundTrip(vault)
	if err != nil {
		t.Fatalf("an unreadable tree is an UNKNOWN, not a hard error: %v", err)
	}
	if len(unknowns) == 0 {
		t.Fatal("an unreadable transcripts dir was silently treated as empty — " +
			"\"I could not look\" is not \"there was nothing there\"")
	}
}

// TestRun_UnknownDoesNotMasqueradeAsPass: status precedence. UNKNOWN outranks PASS
// (a corner you could not read is not a corner that is clean), but it does NOT
// outrank FAIL (drift you CAN see is not made less real by a corner you cannot).
func TestRun_UnknownOutranksPassButNotFail(t *testing.T) {
	cases := []struct {
		name     string
		findings []Finding
		unknowns []string
		want     Status
	}{
		{"clean", nil, nil, StatusPass},
		{"blind", nil, []string{"could not read x"}, StatusUnknown},
		{"drift", []Finding{f(DimArchiveRoundTrip, "a")}, nil, StatusFail},
		{"drift and blind", []Finding{f(DimArchiveRoundTrip, "a")}, []string{"blind"}, StatusFail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := newDimensionResult(DimArchiveRoundTrip, "cmd", Baseline{}, c.findings, c.unknowns)
			if got.Status != c.want {
				t.Fatalf("status = %q, want %q", got.Status, c.want)
			}
		})
	}
}

// TestRun_AcceptedDebtPassesButIsStillCounted: a dimension carrying 82 accepted
// findings and 0 new ones PASSES — but it is not CLEAN, and the report must not
// render those identically or it teaches its reader the debt is gone.
func TestRun_AcceptedDebtPassesButIsStillCounted(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	stranded := seedManifest(t, vault, "alpha", "aaaa", "")

	base := Baseline{Dimensions: map[string]DimensionBaseline{
		DimArchiveRoundTrip: {Reason: "owned by capture-note-archive-link-never-closes",
			Accepted: []string{stranded}},
	}}
	if err := base.Save(filepath.Join(vault.Root, filepath.FromSlash(BaselineRelPath))); err != nil {
		t.Fatalf("save baseline (does Audits/ exist? phase 0 wired it): %v", err)
	}

	report, err := Run(vault)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	d := report.Dimensions[0]

	if d.Status != StatusPass {
		t.Fatalf("status = %q, want pass — the debt is accepted", d.Status)
	}
	if report.Failed() {
		t.Fatal("accepted debt must not fail the report")
	}
	if d.Accepted != 1 {
		t.Fatalf("accepted = %d, want 1 — passing is not the same as clean, and the "+
			"report must say so", d.Accepted)
	}
	if d.Evidence == "" {
		t.Error("no evidence command: record the grep, never the count")
	}
}

// TestRun_FixingTheBugForcesTheBaselineToShrink is the property the whole design
// turns on, and the reason an accepted-ID set beat a date watermark.
//
// When the archive-link backfill lands, the accepted manifests stop being findings.
// The audit then reports them STALE and DEMANDS their removal — so fixing the bug
// mechanically compels the record to be corrected. A watermark would have kept
// silently passing and learned nothing.
func TestRun_FixingTheBugForcesTheBaselineToShrink(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	// The bug: a stranded transcript, accepted as known debt.
	note := seedNote(t, vault, "alpha", "2026-07-14-good.md")
	stranded := seedManifest(t, vault, "alpha", "aaaa", "")
	base := Baseline{Dimensions: map[string]DimensionBaseline{
		DimArchiveRoundTrip: {Reason: "known debt", Accepted: []string{stranded}},
	}}
	if err := base.Save(filepath.Join(vault.Root, filepath.FromSlash(BaselineRelPath))); err != nil {
		t.Fatal(err)
	}
	if report, err := Run(vault); err != nil || report.Failed() {
		t.Fatalf("precondition: accepted debt should pass (err=%v)", err)
	}

	// THE BACKFILL LANDS: the manifest now back-links to a real note.
	seedManifest(t, vault, "alpha", "aaaa", note)

	report, err := Run(vault)
	if err != nil {
		t.Fatal(err)
	}
	d := report.Dimensions[0]

	if len(d.Stale) != 1 || d.Stale[0].Artifact != stranded {
		t.Fatalf("stale = %+v, want the repaired manifest — the audit must DEMAND the "+
			"baseline shrink once the fix lands, or the record rots into a lie", d.Stale)
	}
	if d.Status != StatusFail {
		t.Fatalf("status = %q, want fail: a stale entry is a ratchet failure", d.Status)
	}

	// And the operator's remedy is a regen, which drops the repaired entry.
	regen := base.Regenerate(report.Findings())
	if len(regen.Dimensions[DimArchiveRoundTrip].Accepted) != 0 {
		t.Fatalf("regen kept a repaired artifact: %+v — the baseline may only SHRINK",
			regen.Dimensions[DimArchiveRoundTrip].Accepted)
	}
}

// TestRun_LiveVaultCanary runs the real dimension against a real vault. Opt-in:
//
//	VP_LIVE_VAULT=~/obsidian/vibe-palace-vault go test ./internal/vaultaudit/ -run LiveVaultCanary -v -count=1
//
// 🔴 -count=1 IS NOT OPTIONAL, AND IT BIT THE AUTHOR OF THIS TEST.
//
// The vault lives OUTSIDE the module, so `go test` cannot see that its contents
// changed and will happily serve a CACHED result. Writing the baseline and re-running
// without -count=1 reported `new=105, accepted=0` — the pre-baseline answer — for a
// vault whose debt was, on disk, fully accepted. A canary that reports a stale verdict
// is worse than no canary: it is an instrument confidently describing a vault it did
// not look at, which is the exact failure this whole epic is named after. (Same class
// as the wrapstate/doc/TESTING.md caching note in resume.md.)
//
// It ASSERTS THE INVARIANT and PRINTS the census rather than pinning counts — every
// hand-recorded number in this project has rotted, including both of the ones in the
// plan this package implements. Record the grep, never the count.
func TestRun_LiveVaultCanary(t *testing.T) {
	root := os.Getenv("VP_LIVE_VAULT")
	if root == "" {
		t.Skip("set VP_LIVE_VAULT=<vault root> to run the live canary")
	}
	vault := storage.NewVault(root)

	report, err := Run(vault)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, d := range report.Dimensions {
		t.Logf("%s: status=%s new=%d accepted=%d stale=%d unknown=%d",
			d.Name, d.Status, len(d.New), d.Accepted, len(d.Stale), len(d.Unknowns))
		t.Logf("  evidence: %s", d.Evidence)
		for _, u := range d.Unknowns {
			t.Logf("  UNKNOWN: %s", u)
		}
		for i, f := range d.New {
			if i == 5 {
				t.Logf("  … and %d more", len(d.New)-5)
				break
			}
			t.Logf("  NEW: %s — %s", f.Artifact, f.Detail)
		}
	}

	// The auditor must not silently find nothing on a vault we KNOW has stranded
	// transcripts. A clean sweep on a vault this old is an auditor that is not
	// looking — unless a baseline is already accepting the debt.
	total := 0
	for _, d := range report.Dimensions {
		total += len(d.New) + d.Accepted
	}
	if total == 0 {
		t.Log("NOTE: zero findings AND zero accepted debt on the live vault. Either the " +
			"archive link was fixed, or the auditor is not looking. Verify by hand: " +
			EvidenceArchiveRoundTrip)
	}
}
