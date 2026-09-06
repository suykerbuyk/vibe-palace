// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"strings"
	"testing"
)

// surfaceFixture is one synthetic package holding every shape the rule must
// separate. Each function is a claim, asserted below in BOTH directions — a rule
// that only ever proves it can fire is half a rule, and the half it is missing is
// the one that keeps it enabled.
//
// The literals here are deliberately the REAL prose. A fixture that used a
// stand-in would prove the walk works and prove nothing about the probes, which
// are the part that goes stale.
const surfaceFixture = `package fixture

import (
	"errors"
	"fmt"

	stderrors "errors"

	"example.com/surface"
)

type Result struct {
	Summary string
	Details []string
	Err     error
}

// OMISSION: binds the error and re-renders it from the numeric fields. This is
// internal/wrapstate/preflight.go's exact former shape — the /vpc-wrap halt path
// where ok:false stops the wrap and this string is all the agent can relay.
func RerendersFromFields(err error) string {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return fmt.Sprintf("binary v%d < vault v%d at %s", ie.BinarySurface, ie.VaultSurface, ie.StampDir)
	}
	return err.Error()
}

// OMISSION: binds the error and reads only its fields into typed output. Reading
// the numbers is not reaching the remedy.
func FieldsOnly(err error) (int, int) {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return ie.BinarySurface, ie.VaultSurface
	}
	return 0, 0
}

// DIVERGENCE: a hand-written second copy of the prose. This is what
// internal/check/surface.go carried, and what had already drifted from the
// producer with nothing pinning it.
func HandWrittenCopy() []string {
	return []string{
		"Upgrade:  cd ~/code/vibe-palace && git pull && make install",
		"Override (at risk):  VP_SURFACE_GATE=warn <command>",
	}
}

// DIVERGENCE: half a copy. Carrying one probe and dropping the other still
// strands a host that cannot upgrade right now, so one probe is enough to fire.
func HalfACopy() string {
	return "run: cd ~/code/vibe-palace && git pull && make install"
}

// CLEAN: sources the lines from the producer.
func SourcesRemediation(err error) *Result {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return &Result{
			Summary: fmt.Sprintf("binary v%d < vault v%d", ie.BinarySurface, ie.VaultSurface),
			Details: ie.Remediation(),
		}
	}
	return nil
}

// CLEAN: passes the error's own text through.
func PassesErrorText(err error) string {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return ie.Error()
	}
	return ""
}

// CLEAN: the internal/surface/gate.go shape — binds only to DECIDE whether to
// print, then prints the error it was given. err.Error() delivers the same bytes
// as ie.Error(), and errors.As is what just proved err's chain holds it.
func PrintsTheSourceError(err error) string {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return err.Error()
	}
	return ""
}

// CLEAN: a completely unrelated errors.As binding. The rule keys on the TYPE, so
// this must not be dragged in.
func UnrelatedBinding(err error) string {
	var pe *surface.VaultUnreachableError
	if errors.As(err, &pe) {
		return pe.Path
	}
	return ""
}

// CLEAN: a string literal that is not the remediation. Flagging prose merely
// because it mentions the subsystem is the noise that gets a gate disabled.
func UnrelatedProse() string {
	return "the surface gate refused this write; see vp_surface_check"
}

// ---- The GUARD CLAUSE and the four HAND-IT-ON shapes. All five are correct code
// ---- and all five were flagged. Each false positive's only documented escape was
// ---- a baseline entry exempting the function from half (b) permanently, so a rule
// ---- that reddens idiomatic Go erodes itself while staying green.

// CLEAN: the guard clause — the most idiomatic error handling in Go. The if BODY is
// the FAILURE branch; the success path is everything after it.
func GuardClause(err error) string {
	var ie *surface.IncompatibleError
	if !errors.As(err, &ie) {
		return ""
	}
	return ie.Error()
}

// CLEAN: the guard clause with an else, so both halves of the success path count.
func GuardClauseWithElse(err error) string {
	var ie *surface.IncompatibleError
	if !errors.As(err, &ie) {
		return ""
	} else {
		_ = err
	}
	return ie.Error()
}

// CLEAN: delegates rendering to a helper. The value still carries the remedy.
func DelegatesToRenderer(err error) []string {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return renderRemedy(ie)
	}
	return nil
}

func renderRemedy(ie *surface.IncompatibleError) []string { return ie.Remediation() }

// CLEAN: returns the error, deferring rendering to its caller.
func ReturnsTheError(err error) error {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return ie
	}
	return nil
}

// CLEAN: stores it in a struct field.
func StoresTheError(err error) *Result {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return &Result{Err: ie}
	}
	return nil
}

// CLEAN: wraps it.
func WrapsTheError(err, other error) error {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return errors.Join(other, ie)
	}
	return nil
}

// ---- The four RESPELLINGS of the omission. Each renders from fields only and each
// ---- walked straight past the rule.

// OMISSION (E1): the motivating specimen spelled with switch. A CaseClause is not
// an *ast.IfStmt, so the scope widened to the whole function and the default
// branch — the NOT-an-IncompatibleError path — satisfied the rule.
func SwitchCaseRerendersFromFields(err error) string {
	var ie *surface.IncompatibleError
	switch {
	case errors.As(err, &ie):
		return fmt.Sprintf("binary v%d < vault v%d", ie.BinarySurface, ie.VaultSurface)
	default:
		return err.Error()
	}
}

// OMISSION (E2): a type switch binds without errors.As at all.
func TypeSwitchRerendersFromFields(err error) string {
	switch e := err.(type) {
	case *surface.IncompatibleError:
		return fmt.Sprintf("binary v%d at %s", e.BinarySurface, e.StampDir)
	}
	return ""
}

// OMISSION (E3): a type assertion, likewise.
func TypeAssertRerendersFromFields(err error) string {
	if e, ok := err.(*surface.IncompatibleError); ok {
		return fmt.Sprintf("vault v%d", e.VaultSurface)
	}
	return ""
}

// OMISSION (E4): := instead of var. An *ast.AssignStmt, never an *ast.ValueSpec.
func ShortDeclRerendersFromFields(err error) string {
	ie := &surface.IncompatibleError{}
	if errors.As(err, &ie) {
		return fmt.Sprintf("binary v%d", ie.BinarySurface)
	}
	return ""
}

// OMISSION: the guard clause used to HIDE the defect — the failure branch renders,
// the success path does not. The negated scoping must catch this too, or the fix
// for the false positive would open a matching hole.
func GuardClauseRerendersFromFields(err error) string {
	var ie *surface.IncompatibleError
	if !errors.As(err, &ie) {
		return err.Error()
	}
	return fmt.Sprintf("binary v%d < vault v%d", ie.BinarySurface, ie.VaultSurface)
}

// ---- Second adversarial pass. Every shape below is the SAME defect or the SAME
// ---- correct code as one already pinned, one keystroke away in spelling.

// OMISSION: the errors.As result held in a temporary bool. No conditional wraps
// the call, so the binding has no known success scope — and err.Error() on the
// FAILURE path must not satisfy it.
func BoolTempRerendersFromFields(err error) string {
	var ie *surface.IncompatibleError
	ok := errors.As(err, &ie)
	if !ok {
		return err.Error()
	}
	return fmt.Sprintf("binary v%d", ie.BinarySurface)
}

// OMISSION: same, positive polarity, with the source render at the tail.
func BoolTempPositiveRerendersFromFields(err error) string {
	var ie *surface.IncompatibleError
	ok := errors.As(err, &ie)
	if ok {
		return fmt.Sprintf("binary v%d", ie.BinarySurface)
	}
	return err.Error()
}

// OMISSION: the E1 switch with the CASE negated — the default branch is now the
// success path and it renders from fields.
func NegatedCaseRerendersFromFields(err error) string {
	var ie *surface.IncompatibleError
	switch {
	case !errors.As(err, &ie):
		return err.Error()
	default:
		return fmt.Sprintf("binary v%d", ie.BinarySurface)
	}
}

// OMISSION: a negated type assertion — the ELSE arm is the success path.
func NegatedTypeAssertRerendersFromFields(err error) string {
	if e, ok := err.(*surface.IncompatibleError); !ok {
		return err.Error()
	} else {
		return fmt.Sprintf("binary v%d", e.BinarySurface)
	}
}

// CLEAN: the same negated assertion, rendering correctly in its else arm.
func NegatedTypeAssertRenders(err error) string {
	if e, ok := err.(*surface.IncompatibleError); !ok {
		return "not a surface mismatch"
	} else {
		return e.Error()
	}
}

// OMISSION: var x = &T{} — a ValueSpec with no explicit type. Ordinary Go, and
// it fell in the gap between the 'var x *T' and 'x := &T{}' handling.
func VarInitNoTypeRerendersFromFields(err error) string {
	var ie = &surface.IncompatibleError{}
	if errors.As(err, &ie) {
		return fmt.Sprintf("binary v%d", ie.BinarySurface)
	}
	return ""
}

// OMISSION: two bindings into the SAME name, the compliant one first. Deduping by
// name alone dropped the second and the defect rode in behind the first.
func SameNameSecondRerendersFromFields(a, b error) string {
	var ie *surface.IncompatibleError
	if errors.As(a, &ie) {
		return ie.Error()
	}
	if errors.As(b, &ie) {
		return fmt.Sprintf("binary v%d", ie.BinarySurface)
	}
	return ""
}

// CLEAN: the value escapes inside a composite literal.
func EscapesInSlice(err error) []error {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return []error{ie}
	}
	return nil
}

// CLEAN: a POSITIONAL struct literal — no KeyValueExpr to match on.
func EscapesPositionally(err error) *Result {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return &Result{"", nil, ie}
	}
	return nil
}

// CLEAN: the value escapes over a channel.
func EscapesOverChannel(err error, ch chan error) {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		ch <- ie
	}
}

// CLEAN: escapes through a variadic spread of a composite literal.
func EscapesVariadic(err, other error) error {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return errors.Join([]error{other, ie}...)
	}
	return nil
}

// ---- Third pass: the five BINDING SPELLINGS the rule could not see. Each was
// ---- filed in seven-binding-spellings-the-remediation-rule-cannot-see as a MISS —
// ---- the defect is present in full and the rule stayed green — and each is paired
// ---- with the correct code one keystroke away, because a widening that fires on
// ---- the defect and on its compliant twin buys nothing.

// OMISSION (S1): the binding target arrives as a PARAMETER, so it was never in
// declared and errors.As was not recognised as binding anything.
func ParamTargetRerendersFromFields(err error, ie *surface.IncompatibleError) string {
	if errors.As(err, &ie) {
		return fmt.Sprintf("binary v%d < vault v%d", ie.BinarySurface, ie.VaultSurface)
	}
	return ""
}

// CLEAN (S1): the same parameter binding, rendering the way out.
func ParamTargetRenders(err error, ie *surface.IncompatibleError) string {
	if errors.As(err, &ie) {
		return ie.Error()
	}
	return ""
}

// OMISSION (S2): a PLAIN type assertion statement — no comma-ok, no if-init, so
// typeAssertBinding was never consulted. It panics when the type is wrong, which
// is precisely why the success scope is the REST OF THE LIST.
func PlainTypeAssertRerendersFromFields(err error) string {
	e := err.(*surface.IncompatibleError)
	return fmt.Sprintf("binary v%d < vault v%d at %s", e.BinarySurface, e.VaultSurface, e.StampDir)
}

// CLEAN (S2): the same plain assertion, rendering the way out.
func PlainTypeAssertRenders(err error) string {
	e := err.(*surface.IncompatibleError)
	return e.Error()
}

// OMISSION (S3): the binding lives in a package-level var holding a FUNC LITERAL.
// The decl loop visited only *ast.FuncDecl, so this body was never walked at all.
var FuncLitRerendersFromFields = func(err error) string {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return fmt.Sprintf("binary v%d < vault v%d", ie.BinarySurface, ie.VaultSurface)
	}
	return ""
}

// CLEAN (S3): the same func literal, rendering the way out.
var FuncLitRenders = func(err error) string {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		return ie.Error()
	}
	return ""
}

// OMISSION (S4): the errors package under an ALIAS. isErrorsAs hard-coded the
// identifier "errors", so a one-word import rename evaded half (b) entirely.
func AliasedErrorsRerendersFromFields(err error) string {
	var ie *surface.IncompatibleError
	if stderrors.As(err, &ie) {
		return fmt.Sprintf("binary v%d < vault v%d", ie.BinarySurface, ie.VaultSurface)
	}
	return err.Error()
}

// CLEAN (S4): the same aliased call, rendering the way out.
func AliasedErrorsRenders(err error) string {
	var ie *surface.IncompatibleError
	if stderrors.As(err, &ie) {
		return ie.Error()
	}
	return ""
}

// OMISSION (S5): err is SHADOWED inside the success scope, so the err.Error() the
// source arm accepted renders a DIFFERENT error — an accurate, well-formed,
// useless message, satisfying the rule with text that never carried the remedy.
func ShadowedSourceRerendersFromFields(err error) string {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		err := fmt.Errorf("binary v%d < vault v%d", ie.BinarySurface, ie.VaultSurface)
		return err.Error()
	}
	return ""
}

// CLEAN (S5): err is shadowed in a NESTED block that does not govern the render,
// so the outer err.Error() still delivers the producer's bytes. Rejecting a source
// render merely because the name is redeclared SOMEWHERE in the scope would redden
// this, and a shadow check that is not scope-aware is a false positive generator.
func ShadowedElsewhereStillRenders(err error) string {
	var ie *surface.IncompatibleError
	if errors.As(err, &ie) {
		if other := errors.New("unrelated"); other != nil {
			err := other
			_ = err
		}
		return err.Error()
	}
	return ""
}
`

func surfaceIDs(t *testing.T) []string {
	t.Helper()
	findings, err := Run(writeFixture(t, surfaceFixture))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindSurfaceRemediationLost {
			out = append(out, f.Symbol)
		}
	}
	return out
}

// TestSurfaceRemediationFlagsBothHalves is the rule's mutation proof.
//
// Both halves matter and they fail differently. DIVERGENCE rots two copies apart
// silently; OMISSION leaves a stranded host with an accurate, well-formed,
// useless message. The producer's own near-miss is the evidence that neither is
// hypothetical: "git pull && make install" was asserted NOWHERE in
// internal/surface, so the upgrade half could have been deleted with the whole
// suite staying green.
func TestSurfaceRemediationFlagsBothHalves(t *testing.T) {
	got := surfaceIDs(t)
	for _, want := range []struct{ symbol, why string }{
		{"fixture.RerendersFromFields", "(b) re-renders the error from its numeric fields — the preflight's exact former shape"},
		{"fixture.FieldsOnly", "(b) binds the error and never reaches the remedy"},
		{"fixture.HandWrittenCopy", "(a) a hand-written second copy of the prose"},
		{"fixture.HalfACopy", "(a) half a copy — one probe is enough, since dropping the override strands a host that cannot upgrade now"},

		// The four respellings of the SAME omission. Each renders from fields only.
		{"fixture.SwitchCaseRerendersFromFields", "(E1) the motivating specimen spelled with switch — a CaseClause is not an *ast.IfStmt, so the scope widened to the whole function and the default branch satisfied the rule"},
		{"fixture.TypeSwitchRerendersFromFields", "(E2) a type switch binds the error without errors.As at all"},
		{"fixture.TypeAssertRerendersFromFields", "(E3) a type assertion, likewise"},
		{"fixture.ShortDeclRerendersFromFields", "(E4) := instead of var — an *ast.AssignStmt, never an *ast.ValueSpec"},
		{"fixture.GuardClauseRerendersFromFields", "the guard clause hiding the defect: the FAILURE branch renders and the success path does not"},

		// Second pass: the same defect, one keystroke away in spelling.
		{"fixture.BoolTempRerendersFromFields", "errors.As held in a temporary bool — no conditional wraps the call, so the source render on the FAILURE path must not satisfy it"},
		{"fixture.BoolTempPositiveRerendersFromFields", "the same with positive polarity and the source render at the tail"},
		{"fixture.NegatedCaseRerendersFromFields", "a NEGATED case clause: the default branch is the success path and it renders from fields"},
		{"fixture.NegatedTypeAssertRerendersFromFields", "a negated type assertion — the ELSE arm is the success path"},
		{"fixture.VarInitNoTypeRerendersFromFields", "var x = &T{} — a ValueSpec with no explicit type, in the gap between the var and := handling"},
		{"fixture.SameNameSecondRerendersFromFields", "two bindings into the same name; deduping by name alone dropped the second"},

		// Third pass: the five binding SPELLINGS filed as misses. Each carries the
		// defect in full and each walked past the rule untouched.
		{"fixture.ParamTargetRerendersFromFields", "(S1) the binding target is a PARAMETER — declaredIncompatibleIdents walked the body only, so errors.As bound nothing the rule could see"},
		{"fixture.PlainTypeAssertRerendersFromFields", "(S2) a PLAIN type assertion statement — typeAssertBinding was consulted only from an if-init, so the whole statement form was invisible"},
		{"fixture.FuncLitRerendersFromFields", "(S3) a binding inside a package-level var holding a FUNC LITERAL — the decl loop visited only *ast.FuncDecl, so the body was never walked"},
		{"fixture.AliasedErrorsRerendersFromFields", "(S4) an ALIASED errors import — isErrorsAs hard-coded the identifier `errors`, so a one-word rename evaded half (b) entirely"},
		{"fixture.ShadowedSourceRerendersFromFields", "(S5) err SHADOWED inside the success scope — the source arm accepted an err.Error() that renders a different error entirely"},
	} {
		if !slices.Contains(got, want.symbol) {
			t.Errorf("%s should have been flagged: %s. A ratchet that cannot see the defect it was "+
				"written for is coverage in name only.\n  got: %v", want.symbol, want.why, got)
		}
	}
}

// TestSurfaceRemediationDoesNotFlagCleanCode pins the precision claim, and it is
// the half that keeps the gate enabled: a rule that reddens correct code teaches
// everyone to wave off its findings, and the wave-off is permanent.
//
// PrintsTheSourceError is the load-bearing case. internal/surface/gate.go binds
// the error only to decide WHETHER to print and then prints `err.Error()`. A rule
// that demanded the call be on the BOUND identifier would redden the one file
// with the most direct stake in this prose.
func TestSurfaceRemediationDoesNotFlagCleanCode(t *testing.T) {
	got := surfaceIDs(t)
	for _, unwanted := range []struct{ symbol, why string }{
		{"fixture.SourcesRemediation", "it sources the lines from the producer"},
		{"fixture.PassesErrorText", "it passes the error's own text through"},
		{"fixture.PrintsTheSourceError", "the gate.go shape — it prints the error it bound against"},
		{"fixture.UnrelatedBinding", "a different error type entirely"},
		{"fixture.UnrelatedProse", "prose that mentions the subsystem but is not the remediation"},

		// 🔴 The five idiomatic shapes. A rule whose false positives are ordinary
		// Go accumulates permanent baseline exemptions on CORRECT code, and every
		// one is a hole the ratchet no longer covers — a worse failure than the
		// one it was built to catch.
		{"fixture.GuardClause", "the guard clause — the if BODY is the FAILURE branch; the success path is after it"},
		{"fixture.GuardClauseWithElse", "the same, with an else arm"},
		{"fixture.DelegatesToRenderer", "hands the value to a renderer, which still carries the remedy"},
		{"fixture.ReturnsTheError", "returns the error, deferring rendering to the caller"},
		{"fixture.StoresTheError", "stores the error in a struct field"},
		{"fixture.WrapsTheError", "joins the error into another"},
		{"fixture.renderRemedy", "the renderer itself — it calls Remediation()"},

		// Second pass: more idiomatic ways to hand the value on.
		{"fixture.NegatedTypeAssertRenders", "a negated type assertion that renders correctly in its else arm"},
		{"fixture.EscapesInSlice", "the value escapes inside a composite literal"},
		{"fixture.EscapesPositionally", "a POSITIONAL struct literal — there is no KeyValueExpr to match"},
		{"fixture.EscapesOverChannel", "the value escapes over a channel"},
		{"fixture.EscapesVariadic", "escapes through a variadic spread"},

		// 🔴 Third pass: the compliant TWIN of every newly recognised spelling. A
		// widening that fires on the defect and on its correct twin has bought
		// nothing — it has only moved the erosion from missed findings to
		// permanent baseline exemptions on correct code.
		{"fixture.ParamTargetRenders", "(S1) the parameter binding, rendering the way out"},
		{"fixture.PlainTypeAssertRenders", "(S2) the plain type assertion, rendering the way out"},
		{"fixture.FuncLitRenders", "(S3) the func literal, rendering the way out"},
		{"fixture.AliasedErrorsRenders", "(S4) the aliased errors call, rendering the way out"},
		{"fixture.ShadowedElsewhereStillRenders", "(S5) err is shadowed in a NESTED block that does not govern the render, so the outer err.Error() still delivers the producer's bytes"},
	} {
		if slices.Contains(got, unwanted.symbol) {
			t.Errorf("%s is correct code and the rule flagged it (%s) — a noisy gate is a disabled "+
				"gate.\n  got: %v", unwanted.symbol, unwanted.why, got)
		}
	}
}

// 🔴 TestSurfaceRemediationReportsVacuity proves the anti-vacuity guard is itself
// live, and this rule needs it more than most: BOTH halves are keyed on names
// that a refactor can change. Reword the upgrade line and half (a) matches
// nothing anywhere; rename IncompatibleError and half (b) sees no bindings at
// all. Either would leave the gate permanently, silently green — which is exactly
// the false-green this package exists to eliminate.
//
// A tiny fixture cannot meet the tree-wide floors and must SAY so.
func TestSurfaceRemediationReportsVacuity(t *testing.T) {
	findings, err := Run(writeFixture(t, "package fixture\n"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var details, symbols []string
	for _, f := range findings {
		if f.Kind == KindSurfaceRemediationLost && strings.Contains(f.Symbol, "/VACUOUS") {
			details = append(details, f.Detail)
			symbols = append(symbols, f.Symbol)
		}
	}
	if len(details) == 0 {
		t.Fatalf("a fixture with no producer and no bindings did NOT report vacuity, so the floors "+
			"are not running.\n  findings: %v", ids(findings))
	}

	joined := strings.Join(details, "\n")
	// The PROSE floor: every probe must be found in the producer, or the probe
	// is stale and half (a) is matching nothing.
	for _, probe := range remediationProbes {
		if !strings.Contains(joined, probe) {
			t.Errorf("vacuity did not report the missing producer probe %q — a reworded remediation "+
				"would silently disable half (a).\n  details:\n%s", probe, joined)
		}
	}
	// The BINDING floor: a rename of the type must not turn the gate green.
	if !strings.Contains(joined, "binding(s) of *surface.IncompatibleError") {
		t.Errorf("vacuity did not report the binding floor — a RENAME of IncompatibleError would "+
			"then leave half (b) matching nothing, permanently green.\n  details:\n%s", joined)
	}
	// 🔴 EVERY VACUITY FINDING NEEDS ITS OWN ID. Finding.ID() is Kind + Symbol, so
	// a shared symbol collapses all of them into ONE id — and a single baseline
	// entry carrying it would suppress every vacuity finding at once, across both
	// halves, on the one class the rule says must never be baselined. That failure
	// is silent and total.
	seen := map[string]bool{}
	for _, sym := range symbols {
		if seen[sym] {
			t.Errorf("two vacuity findings share the symbol %q, so Finding.ID() collides and ONE "+
				"baseline entry would silence both:\n  %v", sym, symbols)
		}
		seen[sym] = true
	}
	if len(symbols) < 2 {
		t.Errorf("expected a vacuity finding per instrument, got %v", symbols)
	}

	for _, d := range details {
		if !strings.Contains(d, "Do NOT baseline this entry") {
			t.Errorf("a vacuity finding must tell the reader not to baseline it — otherwise the "+
				"first person to see it silences it exactly like real debt.\n  detail: %s", d)
		}
	}
}

// TestSurfaceRemediationExemptsOnlyItsOwnFile pins the self-exemption's SCOPE.
//
// remediationProbes are necessarily literals holding the prose, so this file must
// be exempt. The exemption is scoped to the one file rather than the package,
// following vaultWriteFunnel's line on internal/vaultfs: a package-wide skip
// blinds the rule to a new offender added anywhere in it.
func TestSurfaceRemediationExemptsOnlyItsOwnFile(t *testing.T) {
	// The real tree: the rule's own file holds both probes and must not be
	// flagged, while the producer holds them and must not be flagged either.
	findings, err := Run("..")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, f := range findings {
		if f.Kind != KindSurfaceRemediationLost {
			continue
		}
		if strings.HasPrefix(f.Symbol, "sourceaudit.surface_remediation.go") {
			t.Errorf("the rule flagged its own probe definitions: %s\n  %s", f.Symbol, f.Detail)
		}
		if strings.HasPrefix(f.Symbol, "surface.version.go") {
			t.Errorf("the rule flagged the PRODUCER, which is the one place this prose belongs: "+
				"%s\n  %s", f.Symbol, f.Detail)
		}
	}
}
