// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// 🔴 THE ADVISORY GATE ON THE LIVE PATH.
//
// bootstrap_instrument_ceiling_test.go measures the gate's ARITHMETIC against
// two fixtures. These tests measure the gate itself, through the registered
// tool, on a real vault — because a property inferred from a fixture is not a
// property of the tool (288), and both of the defects this file's neighbours
// record were found only by driving the live surface.
//
// The premise every negative here rests on is that VaultStaleness is attached
// UNCONDITIONALLY on an ungated payload — warn or not, it is the one advisory
// that is always present. That makes it the honest probe for "the gate fired":
// a health or audit-staleness assertion on a temp vault would pass vacuously,
// because neither condition holds there in the first place. The premise is
// asserted, not assumed, in TestAdvisoriesRideAnUngatedPayload below.

// advisoryFieldsPresent reports which advisory instruments a payload carries, in
// DECLARATION ORDER.
//
// It is a slice rather than a map because callers report the first offender by
// name and Go randomizes map iteration: the same broken payload would name a
// different field on each run, which makes a failure hard to recognise as the
// one you saw yesterday.
func advisoryFieldsPresent(br BootstrapResult) []string {
	var present []string
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"vault_staleness", br.VaultStaleness != nil},
		{"health", br.Health != nil},
		{"audit_staleness", br.AuditStaleness != nil},
		{"friction_trend", br.FrictionTrend != nil},
	} {
		if f.set {
			present = append(present, f.name)
		}
	}
	return present
}

// advisoryProbe is one piece of prose that must NOT appear in the directive of a
// gated payload, together with the producer outputs it was taken from.
//
// 🔴 THE SAMPLES ARE THE POINT. A probe is a substring search, and a substring
// that no producer emits searches for nothing while looking exactly like a test.
// Two of the four probes here were previously written from the ceiling fixture's
// invented prose — "vp_trends" and "another machine may have written" — and
// neither string exists anywhere in assembleBootstrap's output, so with the gate
// disabled the vault-staleness alert sat in the directive and ZERO probes fired.
// Every probe now carries the producer output it was derived from, and
// advisoryProseProbes fails if the text is not actually in it: a producer
// reworded tomorrow breaks this loudly instead of quietly disarming the probe.
type advisoryProbe struct {
	text    string
	why     string
	samples []string
}

// advisoryProseProbes returns the probe set, each one VERIFIED against the
// producer that emits it.
func advisoryProseProbes(t *testing.T) []advisoryProbe {
	t.Helper()

	errors := vplog.Summary{
		Status:         vplog.StatusErrors,
		WarnCounts:     map[string]int{"mcp.makeHandler": 10},
		CallerFriction: 8,
	}
	warnings := vplog.Summary{Status: vplog.StatusWarnings, WarnCounts: map[string]int{"hook": 4}}
	unknown := vplog.Summary{Status: vplog.StatusUnknown, LogPath: "/v/palace/.local/vp.log", ScanError: "permission denied"}

	staleFetch := computeVaultStaleness(72*time.Hour, time.Date(2026, 8, 16, 15, 0, 0, 0, time.UTC), true)
	unknownFetch := computeVaultStaleness(0, time.Time{}, false)

	auditStale := vaultaudit.CheckStaleness(staleAuditVault(t, "2026-06-14", 61), worstCaseFrictionNow)
	auditNever := vaultaudit.CheckStaleness(neverAuditedVault(t), worstCaseFrictionNow)

	probes := []advisoryProbe{
		{
			text:    "vp logged",
			why:     "the health verdict for warnings and errors (healthMessage)",
			samples: []string{healthMessage(errors), healthMessage(warnings)},
		},
		{
			text:    "vp health is UNKNOWN",
			why:     "the health verdict when the log cannot be read (healthMessage)",
			samples: []string{healthMessage(unknown)},
		},
		{
			text:    "caller-side rejection",
			why:     "the caller-friction line (callerFrictionMessage) — directive-only, no JSON field",
			samples: []string{callerFrictionMessage(errors)},
		},
		{
			text:    "vault last fetched",
			why:     "the vault-staleness line for a known, stale fetch (computeVaultStaleness)",
			samples: []string{staleFetch.Message},
		},
		{
			text:    "vault fetch age unknown",
			why:     "the vault-staleness line when the fetch age is unknown (computeVaultStaleness)",
			samples: []string{unknownFetch.Message},
		},
		{
			text:    "Recent friction is rising",
			why:     "the friction-trend line (capture.ComputeFrictionTrend)",
			samples: []string{worstCaseAdvisoryFriction().Message},
		},
		{
			text:    "run `vp audit vault`",
			why:     "the audit-staleness line, in every branch (vaultaudit.CheckStaleness)",
			samples: []string{auditStale.Message, auditNever.Message},
		},
	}

	// 🔴 THE VERIFICATION. Without it this function is a list of hopes.
	for _, p := range probes {
		if len(p.samples) == 0 {
			t.Fatalf("probe %q carries no producer sample, so nothing establishes that it matches anything", p.text)
		}
		for i, sample := range p.samples {
			if sample == "" {
				t.Fatalf("probe %q sample %d is empty — its producer went silent, so this probe is unanchored", p.text, i)
			}
			if !strings.Contains(sample, p.text) {
				t.Fatalf("probe %q (%s) is NOT a substring of its producer's output, so it searches the "+
					"directive for text vp cannot emit and would never fire.\nproducer sample %d: %q\n\n"+
					"Take the probe from the producer; do not write prose beside it.", p.text, p.why, i, sample)
			}
		}
	}
	return probes
}

// neverAuditedVault builds a vault with enough session notes to warn and no audit
// report at all — CheckStaleness's third branch.
func neverAuditedVault(t *testing.T) *storage.Vault {
	t.Helper()
	root := t.TempDir()
	vault := bornCurrentTestVault(t, root)
	sessions := filepath.Join(root, "Projects", "test-proj", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range vaultaudit.StaleChurnThreshold + 1 {
		note := filepath.Join(sessions, fmt.Sprintf("2026-08-16-never-%03d.md", i))
		if err := os.WriteFile(note, []byte("---\ndate: 2026-08-16\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return vault
}

// TestAdvisoriesRideAnUngatedPayload is THE PREMISE, and it is a test rather
// than a comment because every suppression assertion below is vacuous without
// it. A clean, compatible vault raises no stop-class alert, so the gate does not
// fire and vault_staleness is attached.
func TestAdvisoriesRideAnUngatedPayload(t *testing.T) {
	vault := newGitBackedTestVault(t)
	assertVaultClean(t, vault.Root)

	br := bootstrapOn(t, vault)

	if br.SurfaceMismatch != nil || br.VaultDirt != nil {
		t.Fatalf("test premise broken: a clean, compatible vault raised a stop-class alert "+
			"(surface_mismatch=%v vault_dirt=%v), so this measures the gated path, not the ungated one",
			br.SurfaceMismatch != nil, br.VaultDirt != nil)
	}
	if br.VaultStaleness == nil {
		t.Fatal("vault_staleness is absent from an UNGATED payload — it is attached unconditionally, " +
			"so every suppression assertion in this file would now pass without the gate doing anything")
	}
}

// 🔴 TestStopClassSuppressesAdvisories is the gate, driven live, on both of the
// conditions that open it.
//
// Stop-class and advisory instruments must not coexist in the bounded prefix.
// The reason is not tidiness: with every field populated the prefix measured
// 2,731 B against a 2,000 B host preview, and the 731 B of overflow did not land
// on the advisories — it landed on the directive, which started PAST the cut, so
// the payload carrying every alert in prose delivered none of them.
//
// The two subtests are the two ways a session is stopped, and both are exercised
// because they are computed independently and can fire alone.
func TestStopClassSuppressesAdvisories(t *testing.T) {
	t.Run("surface mismatch", func(t *testing.T) {
		br := strandedBootstrap(t)
		if br.SurfaceMismatch == nil {
			t.Fatal("test premise broken: the ahead-of-binary vault raised no surface_mismatch")
		}
		assertAdvisoriesSuppressed(t, br, "a stranded host")
	})

	t.Run("vault dirt", func(t *testing.T) {
		vault := newGitBackedTestVault(t)
		dirtyFile(t, vault.Root, "Projects/test-proj/tasks/hand-edited.md",
			"# Hand edited\n\n**Status:** pending\n**Priority:** high\n\nbody\n")

		br := bootstrapOn(t, vault)
		if br.VaultDirt == nil {
			t.Fatal("test premise broken: the dirty vault raised no vault_dirt")
		}
		assertAdvisoriesSuppressed(t, br, "a session that cannot save its work")
	})
}

// assertAdvisoriesSuppressed pins what the gate does AND what it must not do.
func assertAdvisoriesSuppressed(t *testing.T, br BootstrapResult, who string) {
	t.Helper()
	for _, finding := range advisorySuppressionFindings(t, br, who) {
		t.Error(finding)
	}
}

// advisorySuppressionFindings returns every way a payload violates the gate.
//
// It returns findings instead of failing, so the checks themselves can be put
// under test — see TestRehousingProbesFireOnRehousedProse. A probe set that
// cannot be shown to fire is indistinguishable from no probe set at all, which
// is exactly what this file shipped before: two of its four probes searched for
// prose no producer emits, and a run with the gate disabled produced zero
// rehousing failures.
func advisorySuppressionFindings(t *testing.T, br BootstrapResult, who string) []string {
	t.Helper()
	var findings []string

	for _, name := range advisoryFieldsPresent(br) {
		findings = append(findings, fmt.Sprintf(
			"%s received the advisory instrument %q. Stop-class and advisory instruments do not coexist in "+
				"the bounded prefix: the session cannot act on an advisory until the stop clears, and carrying "+
				"both is what put the directive 731 B past a 2 KB host cut.", who, name))
	}

	// 🔴 SUPPRESSED, NOT REHOUSED. Relocating an advisory line onto the directive
	// to "save" it deletes it instead: the directive is the field a cut EATS, so
	// anything moved there is on the far side of the boundary the gate exists to
	// protect. Every probe below is a substring of real producer output, checked
	// as such by advisoryProseProbes.
	for _, p := range advisoryProseProbes(t) {
		if strings.Contains(br.PostBootstrapInstructions, p.text) {
			findings = append(findings, fmt.Sprintf(
				"%s got suppressed advisory prose in the directive — %q, which is %s. The alert was rehoused "+
					"rather than suppressed, onto the one field a host cut destroys.\n%s",
				who, p.text, p.why, br.PostBootstrapInstructions))
		}
	}

	// The handles and the ranking report are NOT advisory and are not gated —
	// they are how a reader reaches the bulk at all, and a stopped session still
	// needs the resume it cannot yet write to.
	//
	// ResumeSha256 is deliberately NOT asserted: these fixtures carry no
	// resume.md, so it is empty on the ungated payload too, and asserting it
	// here would pin a property of the fixture rather than of the gate.
	if br.ResumeURI == "" || br.WorkflowURI == "" {
		findings = append(findings, fmt.Sprintf(
			"%s lost a handle field (resume_uri=%q workflow_uri=%q); the gate suppresses advisories, "+
				"not the recovery handles", who, br.ResumeURI, br.WorkflowURI))
	}
	if br.Ranking == nil {
		findings = append(findings, fmt.Sprintf("%s lost the ranking report; it is never silent and is not advisory", who))
	}
	if !br.Complete {
		findings = append(findings, fmt.Sprintf("%s got a payload without the terminal sentinel", who))
	}

	return findings
}

// 🔴 TestRehousingProbesFireOnRehousedProse IS THE PROOF THAT THE PROBES WORK,
// and it is the assertion whose absence let two dead probes sit in this file.
//
// Verifying that a probe is a substring of its producer (advisoryProseProbes)
// proves the probe COULD match. This proves the check actually reports it: for
// every probe, a gated payload — advisories nil, so no structural finding fires
// — carrying that producer's real line in its directive must produce exactly the
// rehousing finding and nothing else.
//
// The two failures it would have caught are the two that shipped: a probe that
// matches no producer (caught in advisoryProseProbes) and a probe that matches
// but is never consulted (caught here).
func TestRehousingProbesFireOnRehousedProse(t *testing.T) {
	for _, p := range advisoryProseProbes(t) {
		for i, sample := range p.samples {
			t.Run(fmt.Sprintf("%s/%d", p.text, i), func(t *testing.T) {
				// A payload that is otherwise correctly gated: no advisory
				// struct, handles intact — so the ONLY thing wrong with it is
				// the rehoused prose.
				br := worstCaseStopClass()
				br.PostBootstrapInstructions = composeDirective("After presenting this bootstrap summary, …",
					[]string{sample})

				findings := advisorySuppressionFindings(t, br, "a rehousing fixture")
				if len(findings) != 1 {
					t.Fatalf("a directive carrying %q produced %d findings, want exactly 1 (the rehousing "+
						"finding for %q). A count of 0 means the probe is never consulted; more than 1 means "+
						"the fixture is broken in some other way and this proves nothing about the probe.\n"+
						"sample: %q\nfindings: %v", p.why, len(findings), p.text, sample, findings)
				}
				if !strings.Contains(findings[0], p.text) {
					t.Errorf("the finding does not name the probe %q that produced it: %s", p.text, findings[0])
				}
			})
		}
	}
}

// TestGatedPayloadsCarryNoAdvisoryProse is the negative direction of the same
// check, on the fixture the ceiling measures: the stop-class branch's own
// directive must be clean. It is separate from the live tests because a fixture
// failure and a server failure are different bugs and should not share a name.
func TestGatedPayloadsCarryNoAdvisoryProse(t *testing.T) {
	if findings := advisorySuppressionFindings(t, worstCaseStopClass(), "the stop-class fixture"); len(findings) != 0 {
		t.Errorf("the stop-class branch of the ceiling fixture violates the gate it is supposed to model: %v", findings)
	}
}

// TestStopClassAlertsCoexistWithEachOther is the boundary of the gate, in the
// direction that is easy to get wrong.
//
// The gate suppresses ADVISORIES. It must not suppress the other stop-class
// alert: a stranded host on a dirty vault has two independent reasons it cannot
// work, and fixing the binary leaves the second one standing. An implementation
// that returned on the first stop-class hit would silently drop the second.
func TestStopClassAlertsCoexistWithEachOther(t *testing.T) {
	vault := newGitBackedTestVault(t)
	if err := surface.WriteStamp(filepath.Join(vault.Root, "Projects", "test-proj"), surface.MCPSurfaceVersion+1, "tester"); err != nil {
		t.Fatalf("stamp ahead vault: %v", err)
	}
	dirtyFile(t, vault.Root, "Projects/test-proj/tasks/hand-edited.md",
		"# Hand edited\n\n**Status:** pending\n**Priority:** high\n\nbody\n")

	br := bootstrapOn(t, vault)

	if br.SurfaceMismatch == nil {
		t.Error("the surface mismatch was dropped when the vault was also dirty — both stop-class " +
			"conditions are computed unconditionally, and they can hold at once")
	}
	if br.VaultDirt == nil {
		t.Error("the vault dirt was dropped when the binary was also stranded — fixing the binary would " +
			"leave this condition standing and unannounced")
	}
	assertAdvisoriesSuppressed(t, br, "a stranded host on a dirty vault")

	// Both alert lines reach the directive, mismatch first: append order is
	// delivery order, and the mismatch is the one that refuses every write.
	if br.SurfaceMismatch != nil && !strings.HasPrefix(br.PostBootstrapInstructions, br.SurfaceMismatch.Message) {
		t.Errorf("the surface-mismatch alert does not lead the directive on a doubly-stopped session:\n%s",
			br.PostBootstrapInstructions)
	}
	if br.VaultDirt != nil && !strings.Contains(br.PostBootstrapInstructions, br.VaultDirt.Message) {
		t.Errorf("the vault-dirt alert is absent from the directive on a doubly-stopped session:\n%s",
			br.PostBootstrapInstructions)
	}
}
