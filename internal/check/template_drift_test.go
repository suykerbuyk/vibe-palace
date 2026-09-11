// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
// A vault copy whose bytes the binary did not ship is the rollout-ordering
// hazard this check replaced a prose paragraph with: the vault serves a
// template the binary did not ship, so a command can be handed arguments the
// binary no longer accepts.
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

// earlierRestart is the committed earlier-version fixture (the templates
// package's TestEarlierFixtureIsAShippedVersion pins it).
func earlierRestart(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "templates", "testdata", "earlier", "commands", "restart.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func embeddedBody(t *testing.T, rel string) string {
	t.Helper()
	rs, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.RelPath == rel {
			return string(r.Bytes)
		}
	}
	t.Fatalf("no embedded %s", rel)
	return ""
}

// TestTemplateDriftRowsClassify: every row is classified by provenance alone —
// no lock is read — and an operator's override is Info, never Pass, so it
// stays visible to restart and wrap (the M3 ruling).
func TestTemplateDriftRowsClassify(t *testing.T) {
	wrap := embeddedBody(t, "commands/wrap.md")
	capture := embeddedBody(t, "commands/capture.md")
	vault := t.TempDir()
	writeTemplateMirror(t, vault, "commands/wrap.md", wrap)
	writeTemplateMirror(t, vault, "commands/capture.md", strings.ReplaceAll(capture, "\n", "\r\n"))
	writeTemplateMirror(t, vault, "commands/restart.md", earlierRestart(t))
	writeTemplateMirror(t, vault, "workflow.md", "# my workflow\n")
	wfSHA, _ := templates.EmbeddedSHA("workflow.md")

	want := map[string]struct {
		status  Status
		summary string
	}{
		"commands/wrap.md":    {Info, "drift (byte-identical to the current embedded copy; pending prune)"},
		"commands/capture.md": {Info, "drift (identical, line endings aside, to the current embedded copy; pending prune)"},
		"commands/restart.md": {Info, "drift (an earlier shipped version of commands/restart.md; pending prune — it shadows the current built-in until then)"},
		"workflow.md":         {Info, "operator override of a built-in (kept; shadows embedded " + wfSHA[:12] + ")"},
	}
	for _, row := range TemplateDriftRows(vault, "Templates", "Templates") {
		rel := strings.TrimPrefix(row.Name, "Templates:Templates/")
		w, ok := want[rel]
		if !ok {
			if row.Status != Pass || row.Summary != "served from embedded floor" {
				t.Errorf("%s: %v %q, want Pass (nothing on disk)", rel, row.Status, row.Summary)
			}
			continue
		}
		if row.Status != w.status || row.Summary != w.summary {
			t.Errorf("%s: %v %q\n want %v %q", rel, row.Status, row.Summary, w.status, w.summary)
		}
	}

	t.Run("not reached directly", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks need a privilege on Windows")
		}
		vault := t.TempDir()
		elsewhere := filepath.Join(vault, "Elsewhere")
		if err := os.MkdirAll(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(elsewhere, "wrap.md"), []byte(wrap), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(vault, "Templates"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(vault, "Templates", "commands")); err != nil {
			t.Fatal(err)
		}
		var seen bool
		for _, row := range TemplateDriftRows(vault, "Templates", "Templates") {
			if row.Name != "Templates:Templates/commands/wrap.md" {
				continue
			}
			seen = true
			if row.Status != Info || !strings.HasPrefix(row.Summary, NotReachedDirectly+" (") ||
				!strings.Contains(row.Summary, "symlink (Templates/commands)") {
				t.Errorf("row = %v %q", row.Status, row.Summary)
			}
		}
		if !seen {
			t.Fatal("no row for Templates/commands/wrap.md")
		}
		agg := CheckTemplateDrift(vault)
		if agg.Status != Info || !strings.Contains(agg.Summary, "override(s) of built-ins kept") {
			t.Errorf("aggregate = %v %q: a path never followed is counted with the kept overrides", agg.Status, agg.Summary)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("unreadable-file fixture needs a non-root POSIX user")
		}
		vault := t.TempDir()
		writeTemplateMirror(t, vault, "commands/wrap.md", wrap)
		p := filepath.Join(vault, "Templates", "commands", "wrap.md")
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		agg := CheckTemplateDrift(vault)
		if agg.Status != Fail || !strings.HasPrefix(agg.Summary, "1 of ") {
			t.Errorf("aggregate = %v %q, want Fail", agg.Status, agg.Summary)
		}
	})

	t.Run("vault root unresolvable", func(t *testing.T) {
		rows := TemplateDriftRows(filepath.Join(t.TempDir(), "gone"), "Templates", "Templates")
		for _, r := range rows {
			if r.Status != Fail || r.Err == nil {
				t.Fatalf("%s: %v, want Fail with an error", r.Name, r.Status)
			}
		}
	})
}

// TestCheckTemplateDriftSummary: the halves are overrides kept and mirrors
// pending a prune — an earlier shipped version counts with the mirrors — and
// there is no "dangling lock entry" half any more.
func TestCheckTemplateDriftSummary(t *testing.T) {
	vault := t.TempDir()
	writeTemplateMirror(t, vault, "commands/wrap.md", embeddedBody(t, "commands/wrap.md"))
	writeTemplateMirror(t, vault, "commands/restart.md", earlierRestart(t))
	writeTemplateMirror(t, vault, "commands/capture.md", "# my capture\n")
	agg := CheckTemplateDrift(vault)
	want := "1 override(s) of built-ins kept, 2 mirror(s) pending a prune (of "
	if agg.Status != Info || !strings.HasPrefix(agg.Summary, want) {
		t.Errorf("aggregate = %v %q, want Info %q...", agg.Status, agg.Summary, want)
	}
	for _, rel := range []string{"commands/wrap.md", "commands/restart.md", "commands/capture.md"} {
		var named bool
		for _, d := range agg.Details {
			named = named || strings.Contains(d, "Templates/"+rel+":")
		}
		if !named {
			t.Errorf("aggregate Details never name %s", rel)
		}
	}
	if strings.Contains(agg.Summary, "dangling") {
		t.Errorf("a dangling half survived: %q", agg.Summary)
	}
}

// TestCheckTemplateDriftSummaryOmitsZeroHalves: a vault holding only a mirror
// says so, and says nothing about overrides; one holding only an override says
// nothing about mirrors.
func TestCheckTemplateDriftSummaryOmitsZeroHalves(t *testing.T) {
	vault := t.TempDir()
	writeTemplateMirror(t, vault, "commands/restart.md", earlierRestart(t))
	if got := CheckTemplateDrift(vault); !strings.HasPrefix(got.Summary, "1 mirror(s) pending a prune (of ") {
		t.Errorf("mirror only: %q", got.Summary)
	}
	vault2 := t.TempDir()
	writeTemplateMirror(t, vault2, "commands/restart.md", "# mine\n")
	if got := CheckTemplateDrift(vault2); !strings.HasPrefix(got.Summary, "1 override(s) of built-ins kept (of ") {
		t.Errorf("override only: %q", got.Summary)
	}
}

// TestCheckTemplateDriftReportsARetainedLock: a retired templates.lock that
// sync left (tracked, ignored, or on a non-git vault) is reported once, with
// the wording that stays true on a mixed fleet — "no vp from this release
// reads it", never "nothing reads it".
func TestCheckTemplateDriftReportsARetainedLock(t *testing.T) {
	writeLock := func(t *testing.T, vault string) {
		t.Helper()
		p := filepath.Join(vault, ".vibe-palace", "templates.lock")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("[entries]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lockDetail := func(r Result) int {
		n := 0
		for _, d := range r.Details {
			if strings.Contains(d, ".vibe-palace/templates.lock") {
				n++
				if !strings.Contains(d, "no vp from this release reads it") {
					t.Errorf("lock detail wording: %q", d)
				}
			}
		}
		return n
	}

	t.Run("lock only", func(t *testing.T) {
		vault := t.TempDir()
		writeLock(t, vault)
		got := CheckTemplateDrift(vault)
		if got.Status != Info || !strings.HasSuffix(got.Summary, "templates in sync; a retired templates.lock is present") {
			t.Errorf("%v %q", got.Status, got.Summary)
		}
		if lockDetail(got) != 1 {
			t.Errorf("Details = %v", got.Details)
		}
	})
	t.Run("lock and a mirror", func(t *testing.T) {
		vault := t.TempDir()
		writeLock(t, vault)
		writeTemplateMirror(t, vault, "commands/restart.md", earlierRestart(t))
		got := CheckTemplateDrift(vault)
		if got.Status != Info || !strings.HasPrefix(got.Summary, "1 mirror(s) pending a prune (of ") ||
			!strings.HasSuffix(got.Summary, "; a retired templates.lock is present") {
			t.Errorf("%v %q", got.Status, got.Summary)
		}
		if lockDetail(got) != 1 {
			t.Errorf("Details = %v", got.Details)
		}
	})
	t.Run("no lock", func(t *testing.T) {
		vault := t.TempDir()
		writeTemplateMirror(t, vault, "commands/restart.md", earlierRestart(t))
		got := CheckTemplateDrift(vault)
		if lockDetail(got) != 0 || strings.Contains(got.Summary, "templates.lock") {
			t.Errorf("a lock was reported where none exists: %q %v", got.Summary, got.Details)
		}
	})
}

// TestTemplateDriftRemedyStaysTruthful pins the remedy to what is true once
// provenance is the frozen shipped-version manifest: no host-local lock, no
// prompt, nothing to regenerate; the upgrade commands never reset an override
// and the reset verbs are named; an unedited copy of a shipped version is vp's.
func TestTemplateDriftRemedyStaysTruthful(t *testing.T) {
	text := strings.Join(templateDriftRemedy, " ")
	for _, want := range []string{
		"Projects/<slug>/commands/",
		"never overwrites one",
		"never prompts about one",
		"restored from HEAD",
		"the upgrade commands never reset one",
		"`vp commands reset NAME`",
		"keeps a backup",
		"shipped.txt is frozen",
		"edit a copy before syncing",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("remedy does not say %q", want)
		}
	}
	for _, bad := range []string{
		"templates.lock", "prompts on every sync", "-update-golden", "regenerate",
		"o/O answer", "vault-template-override-is-discarded-by-config-sync", "currently unsafe",
		"upgrade-overwrite-resets-vault-template-overrides", "--overwrite",
	} {
		if strings.Contains(text, bad) {
			t.Errorf("remedy still says %q", bad)
		}
	}
}
