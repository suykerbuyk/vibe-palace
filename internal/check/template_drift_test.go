// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// writeTemplateMirror materializes one vault-side template copy with the given
// bytes, creating parent directories. relPath is embedded-relative
// ("commands/wrap.md"), so the vault key is "Templates/" + relPath.
func writeTemplateMirror(t *testing.T, vaultRoot, relPath, body string) {
	t.Helper()
	target := filepath.Join(vaultRoot, "Templates", filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatalf("write mirror %s: %v", relPath, err)
	}
}

// anEmbeddedTemplate returns the RelPath of some embedded resource, so the
// tests below name a real corpus member without hard-coding one that a later
// rename could quietly turn into a no-op.
func anEmbeddedTemplate(t *testing.T) string {
	t.Helper()
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("walk embedded: %v", err)
	}
	if len(resources) == 0 {
		t.Fatal("embedded corpus is empty")
	}
	return resources[0].RelPath
}

// TestCheckTemplateDriftSkipsWithoutVault covers the registry's degradation
// contract: every vault-scoped producer answers an empty root with Skip rather
// than resolving one from the process cwd.
func TestCheckTemplateDriftSkipsWithoutVault(t *testing.T) {
	got := CheckTemplateDrift("")
	if got.Status != Skip {
		t.Errorf("empty vault root: status = %v, want Skip (%+v)", got.Status, got)
	}
}

// TestCheckTemplateDriftCleanVaultPasses pins the override-only model's healthy
// state: NO vault mirror at all. A vault that has never been materialized is in
// sync, because the embedded floor serves every template.
func TestCheckTemplateDriftCleanVaultPasses(t *testing.T) {
	vault := t.TempDir()
	got := CheckTemplateDrift(vault)
	if got.Status != Pass {
		t.Errorf("bare vault: status = %v, want Pass (%+v)", got.Status, got)
	}
	if len(got.Details) != 0 {
		t.Errorf("bare vault: Details = %v, want none — a clean check must be "+
			"silent, or the remedy text trains the reader to skim it", got.Details)
	}
}

// TestCheckTemplateDriftReportsDivergedMirror is the branch that matters, and it
// is written to FAIL if the producer stops detecting drift.
//
// A mirror whose bytes differ from the embedded corpus, with no lock entry, is
// the rollout-ordering hazard this check replaced a prose paragraph with: the
// vault serves a template the binary did not ship, so a command can be handed
// arguments the binary no longer accepts.
func TestCheckTemplateDriftReportsDivergedMirror(t *testing.T) {
	vault := t.TempDir()
	rel := anEmbeddedTemplate(t)

	// Prove the negative first: with no mirror, this same vault passes. Without
	// this, a producer that returned Info unconditionally would look correct.
	if before := CheckTemplateDrift(vault); before.Status != Pass {
		t.Fatalf("precondition: bare vault should Pass, got %v (%+v)", before.Status, before)
	}

	writeTemplateMirror(t, vault, rel, "this is not the embedded body\n")

	got := CheckTemplateDrift(vault)
	if got.Status != Info {
		t.Fatalf("diverged mirror: status = %v, want Info (%+v)", got.Status, got)
	}
	key := "Templates/" + rel
	var named bool
	for _, d := range got.Details {
		if strings.Contains(d, key) {
			named = true
			break
		}
	}
	if !named {
		t.Errorf("diverged mirror: Details never name %s — an aggregate that "+
			"reports a count without the path is not actionable\nDetails: %v",
			key, got.Details)
	}
	if !strings.HasPrefix(got.Summary, "1 override(s) of built-ins kept (of ") {
		t.Errorf("diverged mirror: Summary = %q, want it to report 1 kept "+
			"override of the corpus total", got.Summary)
	}
}

// TestTemplateDriftRowsMatchAggregate pins that the two shapes cannot disagree.
// The CLI prints the per-resource rows and the MCP surface prints the aggregate;
// they are one classification, and a reader comparing the two outputs must not
// see a drift row in one and a clean verdict in the other.
func TestTemplateDriftRowsMatchAggregate(t *testing.T) {
	vault := t.TempDir()
	rel := anEmbeddedTemplate(t)
	writeTemplateMirror(t, vault, rel, "diverged\n")

	rows := TemplateDriftRows(vault, "Templates", "Templates")
	var rowDrift int
	for _, r := range rows {
		if r.Status == Info {
			rowDrift++
		}
	}

	agg := CheckTemplateDrift(vault)
	if rowDrift == 0 {
		t.Fatal("per-resource rows report no drift for a diverged mirror")
	}
	if agg.Status != Info {
		t.Fatalf("rows report %d drifting, aggregate says %v", rowDrift, agg.Status)
	}
	if len(agg.Details) < rowDrift {
		t.Errorf("aggregate Details carry %d lines for %d drifting rows — the "+
			"aggregate is dropping findings", len(agg.Details), rowDrift)
	}
}

// TestTemplateDriftProducerReachesRegistry covers the selector path end to end.
// The classification existing is not the point of this change; being reachable
// by an MCP caller is, and only the registry proves that.
func TestTemplateDriftProducerReachesRegistry(t *testing.T) {
	vault := t.TempDir()
	writeTemplateMirror(t, vault, anEmbeddedTemplate(t), "diverged\n")

	results, err := RunSelected(vault, "template-drift")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want exactly one aggregate row, got %d: %v", len(results), names(results))
	}
	if results[0].Name != "Template drift" {
		t.Errorf("row name = %q, want %q — the MCP envelope keys on it",
			results[0].Name, "Template drift")
	}
	if results[0].Status != Info {
		t.Errorf("status = %v, want Info for a diverged mirror", results[0].Status)
	}
}

// lockAt plants a templates.lock entry for "Templates/"+relPath.
func lockAt(t *testing.T, vaultRoot, relPath, sha string) {
	t.Helper()
	l, err := templates.ReadLock(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	l.Entries["Templates/"+relPath] = templates.LockEntry{EmbeddedSHA: sha, WrittenAt: time.Now().UTC()}
	if err := templates.WriteLock(vaultRoot, l); err != nil {
		t.Fatal(err)
	}
}

// TestTemplateDriftOverrideRowsAreInfo pins the M3 ruling: an operator's
// override of a built-in is reported as Info, never Pass, in every lock state
// — so it stays visible to restart and wrap, which is the only notice an
// operator gets that their copy shadows the binary's. Mirror rows keep their
// "drift ... pending prune" wording, and a lock-less byte-identical copy is a
// mirror (sync's silent-adopt pre-pass prunes it), not an override.
func TestTemplateDriftOverrideRowsAreInfo(t *testing.T) {
	resources, err := templates.WalkEmbedded()
	if err != nil || len(resources) < 5 {
		t.Fatalf("need at least five embedded resources: %v", err)
	}
	emb := func(i int) (string, string, string) {
		r := resources[i]
		sha, _ := templates.EmbeddedSHA(r.RelPath)
		return r.RelPath, sha, string(r.Bytes)
	}
	vault := t.TempDir()

	case4, sha4, _ := emb(0) // lock at the embedded SHA, bytes differ
	writeTemplateMirror(t, vault, case4, "mine 4\n")
	lockAt(t, vault, case4, sha4)

	case5, _, _ := emb(1) // lock at an older baseline, bytes differ
	writeTemplateMirror(t, vault, case5, "mine 5\n")
	lockAt(t, vault, case5, strings.Repeat("b", 64))

	case6, _, _ := emb(2) // no lock, bytes differ
	writeTemplateMirror(t, vault, case6, "mine 6\n")

	mirror, sha7, body7 := emb(3) // lock at the embedded SHA, bytes equal it
	writeTemplateMirror(t, vault, mirror, body7)
	lockAt(t, vault, mirror, sha7)

	adopt, _, body8 := emb(4) // no lock, bytes equal the embedded copy
	writeTemplateMirror(t, vault, adopt, body8)

	want := map[string]string{
		case4:  "operator override of a built-in (kept; shadows embedded " + sha4[:12] + ")",
		case5:  "operator override of a built-in (kept; the embedded copy changed since this host's lock baseline)",
		case6:  "override of a built-in with no lock entry on this host (kept)",
		mirror: "drift (reconciler-owned mirror pending prune)",
		adopt:  "drift (byte-identical to current embedded; pending prune)",
	}
	for _, row := range TemplateDriftRows(vault, "Templates", "Templates") {
		rel := strings.TrimPrefix(row.Name, "Templates:Templates/")
		w, ok := want[rel]
		if !ok {
			if row.Status != Pass {
				t.Errorf("%s: status %v, want Pass (nothing on disk)", rel, row.Status)
			}
			continue
		}
		if row.Status != Info {
			t.Errorf("%s: status %v, want Info", rel, row.Status)
		}
		if row.Summary != w {
			t.Errorf("%s: summary %q, want %q", rel, row.Summary, w)
		}
	}

	agg := CheckTemplateDrift(vault)
	wantSummary := "3 override(s) of built-ins kept, 2 mirror(s) pending a prune (of "
	if agg.Status != Info || !strings.HasPrefix(agg.Summary, wantSummary) {
		t.Errorf("aggregate = %v %q, want Info %q...", agg.Status, agg.Summary, wantSummary)
	}
	for rel := range want {
		var named bool
		for _, d := range agg.Details {
			named = named || strings.Contains(d, "Templates/"+rel+":")
		}
		if !named {
			t.Errorf("aggregate Details never name %s", rel)
		}
	}
}

// TestCheckTemplateDriftSummaryOmitsZeroHalves: a vault holding only a mirror
// (or only a dangling lock entry) says so, and says nothing about overrides.
func TestCheckTemplateDriftSummaryOmitsZeroHalves(t *testing.T) {
	resources, err := templates.WalkEmbedded()
	if err != nil || len(resources) < 2 {
		t.Fatal(err)
	}
	vault := t.TempDir()
	writeTemplateMirror(t, vault, resources[0].RelPath, string(resources[0].Bytes))
	got := CheckTemplateDrift(vault)
	if !strings.HasPrefix(got.Summary, "1 mirror(s) pending a prune (of ") {
		t.Errorf("mirror only: %q", got.Summary)
	}

	vault2 := t.TempDir()
	lockAt(t, vault2, resources[1].RelPath, strings.Repeat("c", 64))
	got = CheckTemplateDrift(vault2)
	if !strings.HasPrefix(got.Summary, "1 dangling lock entr(y/ies) pending a sync (of ") {
		t.Errorf("dangling only: %q", got.Summary)
	}
}

// TestTemplateDriftRemedyStaysTruthful pins the remedy to what is true after
// both override losses were closed: it no longer says `vp config sync`
// replaces an override or that an upgrade's --overwrite resets one, it says
// the upgrade commands never reset one and names the reset verbs, it still
// steers to the project tier, it names the one remaining risk and its task,
// and it never calls a vault override of a built-in safe.
func TestTemplateDriftRemedyStaysTruthful(t *testing.T) {
	text := strings.Join(templateDriftRemedy, " ")
	for _, want := range []string{
		"Projects/<slug>/commands/",
		"no longer overwrites one",
		"restored from HEAD",
		"the upgrade commands never reset one",
		"`vp commands reset NAME`",
		"keeps a backup",
		"template-provenance-manifest-retires-the-host-local-lock",
		"still not recommended",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("remedy does not say %q", want)
		}
	}
	for _, bad := range []string{
		"o/O answer", "vault-template-override-is-discarded-by-config-sync", "currently unsafe",
		"upgrade-overwrite-resets-vault-template-overrides", "--overwrite",
	} {
		if strings.Contains(text, bad) {
			t.Errorf("remedy still says %q", bad)
		}
	}
}
