// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"os"
	"path/filepath"
	"slices"
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

// TestFuncSeamInValueSpecCountsAsInvoked pins the fix for a FALSE POSITIVE the gate
// shipped with — and a false positive is the more dangerous failure of the two.
//
// `var EmbeddedSHA = realEmbeddedSHA` is this repo's standard package-level test
// seam. The first version of this analyzer visited KeyValueExpr, AssignStmt,
// CallExpr and ReturnStmt but NOT ValueSpec, so the seam's real implementation was
// reported DEAD while running in production on every single command. A gate that
// calls live code dead teaches everyone to wave off its findings, which is how you
// get a disabled gate without anyone ever deciding to disable it.
func TestFuncSeamInValueSpecCountsAsInvoked(t *testing.T) {
	dir := writeFixture(t, `package fixture

func realImpl(s string) (string, bool) { return s, true }

// The seam: a package-level var holding the real implementation, swapped in tests.
var Impl func(string) (string, bool) = realImpl

func Entry(s string) (string, bool) { return Impl(s) }
`)

	findings, err := Run(dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, f := range findings {
		if f.Kind == KindUninvoked && strings.HasSuffix(f.Symbol, ".realImpl") {
			t.Fatalf("flagged the real implementation behind a package-level func seam as uninvoked. " +
				"It runs in production through the var. This is a FALSE POSITIVE, and it is exactly the " +
				"one that got templates.realEmbeddedSHA into the first baseline.")
		}
	}
}

// TestStdlibDispatchedMethodsAreExempt — a method the STANDARD LIBRARY calls can
// never have an in-tree call site, so flagging it is pure noise.
func TestStdlibDispatchedMethodsAreExempt(t *testing.T) {
	dir := writeFixture(t, `package fixture

type wrapErr struct{ err error }

func (e *wrapErr) Error() string { return "wrapped" }
func (e *wrapErr) Unwrap() error { return e.err }

func Make(err error) error { return &wrapErr{err: err} }
`)

	findings, err := Run(dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, f := range findings {
		if f.Kind == KindUninvoked && strings.HasSuffix(f.Symbol, "wrapErr.Unwrap") {
			t.Errorf("flagged Unwrap on an error type. errors.Is/As call it during chain traversal — " +
				"the dispatcher is the stdlib, so no in-tree call site can EVER exist and this finding " +
				"can never be actioned.")
		}
	}
}

// TestInTreeInterfaceMethodNobodyCallsIsStillFlagged is the OTHER half of the
// exemption, and it is the half that matters.
//
// The tempting rule — "exempt any method that satisfies an interface" — would have
// hidden the most valuable finding in the entire first triage: six
// reconcile.*.Requires() methods that satisfy reconcile.Reconciler (an interface the
// tree really uses) and declare a dependency graph that the driver loop re-derives by
// hand in a switch and NEVER READS. Satisfying an interface nobody dispatches on is
// not an exemption; it is this project's signature bug wearing a contract.
//
// So: plant exactly that shape — an in-tree interface, a real implementation of it, a
// driver that holds the interface and calls only SOME of its methods — and demand the
// uncalled one still be named.
// writeMultiPkgFixture writes several packages under one root, so a test can pin
// CROSS-PACKAGE behaviour. srcByPkg maps package name → file body.
func writeMultiPkgFixture(t *testing.T, srcByPkg map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for pkg, src := range srcByPkg {
		dir := filepath.Join(root, pkg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, pkg+".go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// 🔴 TestDeadCodeIsNotResurrectedByASameNamedFuncElsewhere pins the 206 bug.
//
// The matcher is AST-only and cannot resolve what `x` is in `x.Diff()`, so it keys on
// bare names. That silently marked internal/sourceaudit's genuinely-dead Baseline.Diff
// and LoadBaseline as INVOKED the moment internal/vaultaudit — which does not import
// sourceaudit — defined and called its own. The gate then reported those two TRUE
// baseline entries as STALE and demanded their removal.
//
// A false negative that hides a finding is bad. One that REWRITES THE BASELINE
// corrupts the ratchet the whole epic leans on. Names like Save, Load, Run and Diff
// are not exotic; they are Go, so any new package could do this again.
//
// The rule: a name declared in ONE package stays permissive (nothing to confuse it
// with); a name declared in TWO OR MORE is import-scoped.
func TestDeadCodeIsNotResurrectedByASameNamedFuncElsewhere(t *testing.T) {
	root := writeMultiPkgFixture(t, map[string]string{
		// alpha.Diff is DEAD. Nothing anywhere calls it.
		"alpha": `package alpha

func Diff() string { return "dead — nobody calls this" }
`,
		// beta declares its OWN Diff and calls it. beta does NOT import alpha, so this
		// call cannot possibly be a call to alpha.Diff.
		"beta": `package beta

func Diff() string { return "live" }

func Drive() string { return Diff() }
`,
	})

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := ids(findings)
	if !slices.Contains(got, "uninvoked alpha.Diff") {
		t.Fatalf("alpha.Diff is DEAD and was not flagged — beta's identically-named, "+
			"live Diff resurrected it. A package that does not import alpha cannot call "+
			"alpha.Diff.\n  findings: %v", got)
	}
	if slices.Contains(got, "uninvoked beta.Diff") {
		t.Errorf("beta.Diff IS called, in its own package — flagging it is a false positive: %v", got)
	}
}

// TestImportScopingDoesNotFlagAGenuineCrossPackageCall is the other half: the scoping
// must not create a FALSE POSITIVE. A package that DOES import the declarer, and calls
// the function, satisfies it — even when the name is ambiguous tree-wide.
//
// 203 established the asymmetry that makes this test matter: a gate calling LIVE code
// dead is worse than one that misses something, because it teaches everyone to wave off
// its findings.
func TestImportScopingDoesNotFlagAGenuineCrossPackageCall(t *testing.T) {
	root := writeMultiPkgFixture(t, map[string]string{
		"alpha": `package alpha

func Diff() string { return "live, called from gamma" }
`,
		// beta makes the name AMBIGUOUS, which is what turns on import scoping.
		"beta": `package beta

func Diff() string { return "also live" }

func Drive() string { return Diff() }
`,
		// gamma imports alpha and calls it. This MUST satisfy alpha.Diff.
		"gamma": `package gamma

import "example.com/fixture/alpha"

func Drive() string { return alpha.Diff() }
`,
	})

	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := ids(findings); slices.Contains(got, "uninvoked alpha.Diff") {
		t.Fatalf("alpha.Diff IS called from gamma, which imports alpha — flagging it is "+
			"the false positive 203 warned about.\n  findings: %v", got)
	}
}

func TestInTreeInterfaceMethodNobodyCallsIsStillFlagged(t *testing.T) {
	dir := writeFixture(t, `package fixture

type Reconciler interface {
	Name() string
	Requires() []string
}

type vaultRec struct{}

func (r *vaultRec) Name() string     { return "Vault" }
func (r *vaultRec) Requires() []string { return []string{"GlobalConfig"} }

// The driver holds the interface — and calls Name(), never Requires().
func Drive() string {
	var r Reconciler = &vaultRec{}
	return r.Name()
}
`)

	findings, err := Run(dir)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var flagged bool
	for _, f := range findings {
		if f.Kind == KindUninvoked && strings.HasSuffix(f.Symbol, "vaultRec.Requires") {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("did NOT flag an in-tree interface method that no driver ever calls. The exemption has "+
			"overreached: this is the reconcile.Requires() shape, six real instances of which declared a "+
			"dependency graph nothing read. Blanket-exempting interface methods hides this class.\ngot: %v",
			ids(findings))
	}
}

// TestBaselineRegenPreservesReasons protects the ratchet's memory.
//
// The reason string is the whole value of a baseline entry: it is where a human
// recorded WHY a finding is accepted (dispatched by an interface, owned by task X,
// deliberate). Regenerating the baseline is routine — every deletion forces one — and
// if regeneration stamps TODO over every reason, then the first regen erases the
// triage that produced them, and the list decays back into the undifferentiated blob
// it started as. Survivors keep their reason; only genuinely new entries get a TODO.
func TestBaselineRegenPreservesReasons(t *testing.T) {
	prior := Baseline{Entries: []BaselineEntry{
		{ID: "uninvoked fixture.Kept", Reason: "dispatched via the plugin registry; see task foo"},
		{ID: "uninvoked fixture.Fixed", Reason: "this one gets deleted from the tree"},
	}}
	findings := []Finding{
		{Kind: KindUninvoked, Symbol: "fixture.Kept"},
		{Kind: KindUninvoked, Symbol: "fixture.Fresh"},
	}

	regen := prior.Regenerate(findings)

	byID := map[string]string{}
	for _, e := range regen.Entries {
		byID[e.ID] = e.Reason
	}
	if len(regen.Entries) != 2 {
		t.Fatalf("regenerated baseline should hold exactly the current findings; got %d", len(regen.Entries))
	}
	if got := byID["uninvoked fixture.Kept"]; got != "dispatched via the plugin registry; see task foo" {
		t.Errorf("a surviving entry LOST its reason on regen — the triage that produced it is erased and "+
			"the baseline decays back into an undifferentiated list. got %q", got)
	}
	if _, ok := byID["uninvoked fixture.Fixed"]; ok {
		t.Error("a fixed finding survived regeneration — the baseline must only shrink")
	}
	if got := byID["uninvoked fixture.Fresh"]; !strings.Contains(got, "TODO") {
		t.Errorf("a NEW entry must be marked TODO so it cannot pass as triaged; got %q", got)
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
		// Regenerate, do NOT rebuild from scratch: a survivor keeps the reason a human
		// wrote for it. Stamping TODO over every entry on each regen would erase the
		// triage and let the list rot back into an undifferentiated blob.
		prior, err := LoadBaseline(baselinePath)
		if err != nil {
			t.Fatalf("load baseline: %v", err)
		}
		b := prior.Regenerate(findings)
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
