// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoots are the trees this repository audits: its own source.
var repoRoots = []string{"..", filepath.Join("..", "..", "cmd")}

const baselinePath = "baseline.json"

// writeFixture drops a throwaway Go package in a temp dir and returns its path.
func writeFixture(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func ids(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.ID())
	}
	return out
}

// TestFindsAPlantedWriteOnlyField is the analyzer's own mutation test, and it is
// NOT ceremony.
//
// Twice while building this package, a bug made it report ZERO findings while
// looking perfectly healthy: first a directory walk that skipped its own root, then
// a type resolver that could not see `pkg.Type{...}` composite literals. Both times
// the output was a clean bill of health for a tree that was full of defects. THAT IS
// THE EXACT DISEASE THIS PACKAGE EXISTS TO CURE, and an analyzer that cannot prove it
// finds a known bug is worth nothing.
//
// So: plant the note_path bug — a record the code CONSTRUCTS, with one tagged field
// it forgets to assign — and demand the analyzer name it.
func TestFindsAPlantedWriteOnlyField(t *testing.T) {
	dir := writeFixture(t, `package fixture

type Record struct {
	Name     string `+"`yaml:\"name\"`"+`
	Forgotten string `+"`yaml:\"forgotten,omitempty\"`"+`
}

func Build() Record {
	r := Record{Name: "x"}
	return r
}

func Use() string { return Build().Forgotten }
`)

	findings, err := Run(dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var found bool
	for _, f := range findings {
		if f.Kind == KindWriteOnlyField && strings.HasSuffix(f.Symbol, "Record.Forgotten") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the analyzer did NOT find a planted write-only field — it is reporting a clean "+
			"bill of health for a tree that contains the exact bug it exists to catch.\ngot: %v", ids(findings))
	}
}

// TestIgnoresDeserializedStructs is the other half, and it protects the gate from
// itself: a noisy gate is a disabled gate.
//
// An MCP *Params struct is populated by json.Unmarshal through reflection, so NO
// field is ever hand-assigned. Flagging those would bury the real findings under
// dozens of false positives and the whole check would be switched off within a week.
func TestIgnoresDeserializedStructs(t *testing.T) {
	dir := writeFixture(t, `package fixture

import "encoding/json"

type params struct {
	Query string `+"`json:\"query\"`"+`
	Limit int    `+"`json:\"limit,omitempty\"`"+`
}

func Handle(raw []byte) string {
	var p params
	_ = json.Unmarshal(raw, &p)
	return p.Query
}
`)

	findings, err := Run(dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, f := range findings {
		if f.Kind == KindWriteOnlyField {
			t.Errorf("flagged a field on a DESERIALIZED struct (%s) — no field of an unmarshaled "+
				"struct is ever hand-assigned, so this would fire on every MCP params type in the tree "+
				"and get the gate switched off", f.Symbol)
		}
	}
}

// TestFindsAPlantedUninvokedFunc — the "capability built, nothing invokes it" class.
func TestFindsAPlantedUninvokedFunc(t *testing.T) {
	dir := writeFixture(t, `package fixture

func Invoked() int   { return 1 }
func NeverCalled() int { return 2 }

func Entry() int { return Invoked() }
`)

	findings, err := Run(dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var sawUninvoked, sawInvoked bool
	for _, f := range findings {
		if f.Kind != KindUninvoked {
			continue
		}
		if strings.HasSuffix(f.Symbol, ".NeverCalled") {
			sawUninvoked = true
		}
		if strings.HasSuffix(f.Symbol, ".Invoked") {
			sawInvoked = true
		}
	}
	if !sawUninvoked {
		t.Errorf("did not flag a function nothing calls; got %v", ids(findings))
	}
	if sawInvoked {
		t.Error("flagged a function that IS called — a false positive here disables the gate")
	}
}

// TestFuncValuesCountAsInvoked guards the biggest false-positive risk in the
// uninvoked check: a handler assigned as a VALUE (`Handler: myFunc`) is invoked
// later, through that value. Without this, every handler, option and callback in the
// tree looks dead and the check is useless.
func TestFuncValuesCountAsInvoked(t *testing.T) {
	dir := writeFixture(t, `package fixture

type tool struct{ Handler func() int }

func handlerFn() int { return 1 }

func Register() tool { return tool{Handler: handlerFn} }
`)

	findings, err := Run(dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, f := range findings {
		if f.Kind == KindUninvoked && strings.HasSuffix(f.Symbol, ".handlerFn") {
			t.Error("flagged a function passed as a VALUE — it is invoked through that value, and " +
				"flagging it would fire on every MCP handler in the tree")
		}
	}
}

// TestBaselineCanOnlyShrink pins THE RATCHET.
//
// A baseline that only guards against NEW findings rots into a lie: you fix
// something, the list keeps claiming it is broken, and eventually the list means
// nothing. So a baseline entry that is no longer a finding must ALSO fail. That is
// what makes this an audit that compounds rather than a checklist that decays.
func TestBaselineCanOnlyShrink(t *testing.T) {
	b := Baseline{Entries: []BaselineEntry{
		{ID: "uninvoked fixture.Gone", Reason: "was fixed, but still recorded"},
		{ID: "uninvoked fixture.Still", Reason: "known"},
	}}
	findings := []Finding{
		{Kind: KindUninvoked, Symbol: "fixture.Still"},
		{Kind: KindUninvoked, Symbol: "fixture.Brand.New"},
	}

	added, stale := b.Diff(findings)

	if len(added) != 1 || added[0].Symbol != "fixture.Brand.New" {
		t.Errorf("new debt not reported as added: %v", ids(added))
	}
	if len(stale) != 1 || stale[0].ID != "uninvoked fixture.Gone" {
		t.Errorf("a FIXED baseline entry was not reported stale — the baseline is allowed to rot: %v", stale)
	}
}

// TestSourceAuditGate is the gate itself. It runs the audit over this repository and
// compares against the accepted baseline.
//
// Regenerate with:  go test ./internal/sourceaudit -update-baseline
func TestSourceAuditGate(t *testing.T) {
	findings, err := Run(repoRoots...)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	if *updateBaseline {
		b := Baseline{}
		for _, f := range findings {
			b.Entries = append(b.Entries, BaselineEntry{ID: f.ID(), Reason: "TODO: accepted at baseline creation — explain or fix"})
		}
		if err := b.Save(baselinePath); err != nil {
			t.Fatalf("save baseline: %v", err)
		}
		t.Logf("wrote %s with %d entries", baselinePath, len(b.Entries))
		return
	}

	base, err := LoadBaseline(baselinePath)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	added, stale := base.Diff(findings)

	for _, f := range added {
		t.Errorf("NEW source-audit finding (%s)\n  %s\n  at %s\n  %s\n"+
			"  This is the note_path / capability-built-nothing-invokes-it class. Fix it, or — if it is "+
			"genuinely intended — add it to %s WITH A REASON.",
			f.Kind, f.Symbol, f.Pos, f.Detail, baselinePath)
	}
	for _, e := range stale {
		t.Errorf("STALE baseline entry: %q is recorded as accepted debt but is NO LONGER a finding.\n"+
			"  It was fixed and the baseline was not updated. Remove it from %s.\n"+
			"  (The baseline may only SHRINK — that is what keeps it honest.)", e.ID, baselinePath)
	}
}
