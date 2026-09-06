// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// strandedBootstrap builds a vault one surface version AHEAD of this binary —
// exactly what every un-upgraded host sees the moment a newer one writes — and
// returns the bootstrap payload it would receive on the FIRST call of a session,
// before any tool has been attempted.
//
// 🔴 IT ASSERTS THAT WHAT IT RETURNS IS THE STOP-CLASS PAYLOAD, and that check
// is here rather than in each caller on purpose. Every test below measures a
// delivery contract — the remedy reaches the structured field, the alert leads
// the directive — and after the advisory gate those contracts hold on the shape
// production actually ships when stranded, which carries no advisory
// instruments. Measured on a mixed payload they would be asserting properties of
// a document vp never emits. The gate itself is pinned in
// bootstrap_advisory_gate_test.go; this is the guarantee that these tests are
// standing on it.
func strandedBootstrap(t *testing.T) BootstrapResult {
	t.Helper()
	root := t.TempDir()
	vault := bornCurrentTestVault(t, root)
	if err := surface.WriteStamp(filepath.Join(root, "Projects", "test-proj"), surface.MCPSurfaceVersion+1, "tester"); err != nil {
		t.Fatalf("stamp ahead vault: %v", err)
	}
	br := runBootstrap(t, vault, vpctx.NewResolver(root))

	if br.SurfaceMismatch == nil {
		t.Fatal("a vault AHEAD of this binary produced no surface_mismatch, so this is not the stop-class " +
			"payload and nothing below is measuring the shape a stranded host receives")
	}
	if present := advisoryFieldsPresent(br); len(present) > 0 {
		t.Fatalf("the stranded payload carries advisory instrument(s) %v — the tests below measure "+
			"delivery on the STOP-CLASS branch, and a mixed payload is not a shape vp emits", present)
	}
	return br
}

func runBootstrap(t *testing.T, vault *storage.Vault, resolver *vpctx.Resolver) BootstrapResult {
	t.Helper()
	tool := BootstrapContextTool(resolver, vault, nil)
	result, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"test-proj"}`))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	br, ok := result.(BootstrapResult)
	if !ok {
		t.Fatalf("result type = %T, want BootstrapResult", result)
	}
	return br
}

// 🔴 TestStrandedHostIsToldOnTheFirstCall is the reason this alert exists.
//
// Before it, vp_bootstrap_context said NOTHING about a surface mismatch. The
// only thing that ever appeared was the generic health tally, and only AFTER a
// mutating call had already been refused — so on a fresh session, before
// anything was attempted, the payload was silent about the one condition that
// made every write impossible.
//
// The assertions are on the REMEDY, not just the diagnosis. A stranded host
// cannot pull its way out (pulling raises the vault floor further) and the MCP
// server it is talking to is the thing that is out of date, so text naming the
// command is the whole of its route back.
func TestStrandedHostIsToldOnTheFirstCall(t *testing.T) {
	br := strandedBootstrap(t)

	if br.SurfaceMismatch == nil {
		t.Fatal("a vault AHEAD of this binary produced no surface_mismatch — a stranded host's " +
			"first call of the session says nothing about the one condition that refuses every write")
	}
	sm := br.SurfaceMismatch

	if sm.BinarySurface != surface.MCPSurfaceVersion {
		t.Errorf("binary_surface = %d, want %d", sm.BinarySurface, surface.MCPSurfaceVersion)
	}
	if sm.VaultSurface != surface.MCPSurfaceVersion+1 {
		t.Errorf("vault_surface = %d, want %d", sm.VaultSurface, surface.MCPSurfaceVersion+1)
	}
	if sm.StampDir == "" {
		t.Error("stamp_dir is empty — the operator cannot tell WHICH stamp raised the floor")
	}

	// The remediation is the producer's, verbatim.
	want := (&surface.IncompatibleError{}).Remediation()
	if len(sm.Remediation) != len(want) {
		t.Fatalf("remediation = %v, want the producer's %v", sm.Remediation, want)
	}
	for i, line := range want {
		if sm.Remediation[i] != line {
			t.Errorf("remediation[%d] = %q, want %q — this field must be sourced from "+
				"surface.(*IncompatibleError).Remediation(), never re-typed", i, sm.Remediation[i], line)
		}
	}

	// 🔴 AND IT MUST REACH THE DIRECTIVE. The structured field is past the cut
	// for a host that renders only prose; post_bootstrap_instructions is the
	// field the alerts are guaranteed to ride in.
	for _, probe := range []struct{ text, why string }{
		{"git pull && make install", "the upgrade command — the ONLY way out"},
		{"VP_SURFACE_GATE=warn", "the at-risk override, for a host that cannot upgrade now"},
		{"surface", "what kind of mismatch this is"},
	} {
		if !strings.Contains(strings.ToLower(br.PostBootstrapInstructions), strings.ToLower(probe.text)) {
			t.Errorf("post_bootstrap_instructions omits %q (%s):\n%s",
				probe.text, probe.why, br.PostBootstrapInstructions)
		}
	}
}

// 🔴 TestSurfaceAlertLeadsTheDirective pins the DELIVERY CONTRACT, not a reading
// preference.
//
// post_bootstrap_instructions is the last field before the bulk and the only
// variable-length instrument in the payload, so it is the field a host preview
// cuts into — whatever sits at its tail is what the cut destroys. A measured cut
// once ended inside this field and killed a caller-friction alert mid-word while
// the capability announcement survived whole.
//
// This alert leads even the other alerts, because it is the only one that says
// the session cannot do its work at all.
func TestSurfaceAlertLeadsTheDirective(t *testing.T) {
	br := strandedBootstrap(t)
	if br.SurfaceMismatch == nil {
		t.Fatal("no surface_mismatch to order")
	}

	if !strings.HasPrefix(br.PostBootstrapInstructions, br.SurfaceMismatch.Message) {
		t.Errorf("the surface-mismatch alert does not LEAD post_bootstrap_instructions. Anything "+
			"after the alerts is the correct casualty of a host cut; the alerts are why this field "+
			"is in the surviving region at all.\ngot:\n%s", br.PostBootstrapInstructions)
	}

	// The capability announcement must still be behind it — the alert did not
	// simply replace the directive.
	if !strings.Contains(br.PostBootstrapInstructions, "After presenting this bootstrap summary") {
		t.Errorf("the base directive was lost:\n%s", br.PostBootstrapInstructions)
	}
}

// 🔴 TestCompatibleVaultRaisesNoSurfaceAlert is the other direction, and it is
// the one that keeps every OTHER alert readable.
//
// The rule on this payload is SILENT WHEN HEALTHY. An alert that fires on a
// compatible vault is how a reader is trained to skim all of them — the same
// reasoning that killed the `partial` capture status. Asserted, not assumed.
func TestCompatibleVaultRaisesNoSurfaceAlert(t *testing.T) {
	vault, resolver := testSetup(t)
	br := runBootstrap(t, vault, resolver)

	if br.SurfaceMismatch != nil {
		t.Errorf("a COMPATIBLE vault attached surface_mismatch = %+v — the field must be nil when "+
			"healthy", br.SurfaceMismatch)
	}
	for _, probe := range []string{"git pull && make install", "VP_SURFACE_GATE=warn", "SURFACE MISMATCH"} {
		if strings.Contains(br.PostBootstrapInstructions, probe) {
			t.Errorf("a compatible vault emitted remediation prose %q in the directive:\n%s",
				probe, br.PostBootstrapInstructions)
		}
	}
}

// 🔴 TestSurfaceMismatchMessageIsExact pins the DIAGNOSIS half, and it is the half
// the remedy cannot rescue.
//
// Every earlier assertion here was order-blind: it checked that "v2", "v3" and the
// stamp dir appeared SOMEWHERE. Under that, transposing BinarySurface and
// VaultSurface stays green while the alert renders "this vp binary supports MCP
// surface v3 but the vault is at v2" — from which an operator concludes the VAULT
// needs updating. That is the exact wrong action, it is unrecoverable by any amount
// of correct remediation text below it, and no test saw it. Gutting the header to
// "surface v%d/v%d %s %s", or downgrading the 🔴 prefix to "note:", were both green
// for the same reason.
//
// So the HEADER is pinned byte for byte. The tail is composed from Remediation()
// rather than restated, because the producer owns that prose and duplicating it
// here would be the very divergence this change removed — the content of the
// remediation is pinned at the producer, in internal/surface.
func TestSurfaceMismatchMessageIsExact(t *testing.T) {
	// Asymmetric on purpose: equal versions cannot detect a transposition.
	ie := &surface.IncompatibleError{BinarySurface: 2, VaultSurface: 3, StampDir: "/v/Projects/dotfiles"}

	want := "🔴 SURFACE MISMATCH: this vp binary supports MCP surface v2 but the vault is at v3 " +
		"(/v/Projects/dotfiles). Every mutating vp tool will be REFUSED until this is fixed, and " +
		"pulling the vault will not fix it — the fix is a new binary. " +
		flowRemediation(ie.Remediation())

	if got := surfaceMismatchMessage(ie); got != want {
		t.Errorf("the alert line changed.\ngot:  %q\nwant: %q\n\nIf this is a deliberate rewording, "+
			"update the literal above. If it is not, note what moved — the two version numbers are in "+
			"ROLES, and swapping them tells the operator to update the vault.", got, want)
	}
}

// TestSurfaceMismatchMessageNamesEachVersionInItsRole is the same guard stated so a
// transposition fails READABLY rather than as a wall-of-text diff.
func TestSurfaceMismatchMessageNamesEachVersionInItsRole(t *testing.T) {
	ie := &surface.IncompatibleError{BinarySurface: 2, VaultSurface: 3, StampDir: "/v/Projects/dotfiles"}
	msg := surfaceMismatchMessage(ie)

	bin := strings.Index(msg, "supports MCP surface v2")
	vault := strings.Index(msg, "vault is at v3")
	if bin < 0 {
		t.Errorf("the BINARY's version is not named as the binary's (want \"supports MCP surface v2\"):\n%s", msg)
	}
	if vault < 0 {
		t.Errorf("the VAULT's version is not named as the vault's (want \"vault is at v3\"):\n%s", msg)
	}
	if bin >= 0 && vault >= 0 && bin > vault {
		t.Errorf("the vault's version is stated before the binary's; the sentence reads backwards:\n%s", msg)
	}

	// The transposed renderings must be ABSENT. This is the assertion that was
	// missing: presence-only checks pass on a swap because both numbers are there.
	for _, wrong := range []string{"supports MCP surface v3", "vault is at v2"} {
		if strings.Contains(msg, wrong) {
			t.Errorf("the versions are TRANSPOSED — %q appears, so the alert says the vault is behind "+
				"the binary and the operator will update the VAULT:\n%s", wrong, msg)
		}
	}

	// And the severity marker, which is what makes it read as a condition alert
	// rather than a note.
	if !strings.HasPrefix(msg, "🔴 SURFACE MISMATCH:") {
		t.Errorf("the alert lost its severity prefix; it now reads as an informational note:\n%s", msg)
	}
}

// TestSurfaceMismatchMessageCarriesTheProducersProse pins that the flowed alert
// line is COMPOSED from Remediation() rather than authored beside it. The one-line
// form exists because the alerts are space-joined into one prose field, not because
// the text is different there.
func TestSurfaceMismatchMessageCarriesTheProducersProse(t *testing.T) {
	ie := &surface.IncompatibleError{BinarySurface: 2, VaultSurface: 3, StampDir: "/v/Projects/dotfiles"}
	msg := surfaceMismatchMessage(ie)

	for _, line := range ie.Remediation() {
		// The flowed form collapses the block's column padding, so compare against
		// the flowed spelling of each line rather than the raw one.
		flowed := strings.Join(strings.Fields(line), " ")
		if !strings.Contains(msg, flowed) {
			t.Errorf("the alert line drops remediation line %q:\n%s", line, msg)
		}
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("the alert line contains a newline — alerts are space-joined into one prose "+
			"field, so embedded layout lands mid-sentence:\n%q", msg)
	}
}

// 🔴 TestFlowedRemediationDoesNotInvertTheFork is MEDIUM-3.
//
// The remediation is a FORK: upgrade, or — if you cannot — override. Joined with a
// bare space those two branches run together as
//
//	… git pull && make install if you cannot upgrade right now …
//
// which reads as a CONDITION on `make install`, when `make install` IS the upgrade.
// The advice inverts, and the reader most likely to be hurt is the stranded
// operator skimming one long sentence.
func TestFlowedRemediationDoesNotInvertTheFork(t *testing.T) {
	msg := surfaceMismatchMessage(&surface.IncompatibleError{BinarySurface: 2, VaultSurface: 3, StampDir: "d"})

	if strings.Contains(msg, "make install if you cannot upgrade") {
		t.Errorf("the two branches of the fork run together, so the upgrade command reads as "+
			"conditional on being unable to upgrade:\n%s", msg)
	}
	if !strings.Contains(msg, "make install — if you cannot upgrade") {
		t.Errorf("the upgrade branch and the override branch are not separated:\n%s", msg)
	}
	// The framing line ENDS in a colon and introduces its command, so those two
	// stay joined by a space — a separator there would be the opposite error.
	if !strings.Contains(msg, "(deploy host, network outage): VP_SURFACE_GATE=warn") {
		t.Errorf("the framing line was separated from the command it introduces:\n%s", msg)
	}
	// Column padding is layout, not prose, and TrimSpace does not remove it.
	if strings.Contains(msg, "  ") {
		t.Errorf("interior alignment padding survived into the flowed line:\n%q", msg)
	}
}

// 🔴 TestNonMismatchVaultErrorsRaiseNoAlert is HIGH-1: the guard that only an
// IncompatibleError fires this alert was correct in code and pinned by NOTHING.
//
// Replace the errors.As check in assembleBootstrap with a bare `err != nil` and
// every other test in this package stays green — while a host with no vault
// configured, or one whose vault root has been unmounted, is told:
//
//	SURFACE MISMATCH: this vp binary supports MCP surface v0 but the vault is at v0 ()
//
// with a remedy of `git pull && make install`, which cannot fix either condition.
// Their actual fix is vault_path. That is THIS TASK'S OWN THESIS reappearing one
// layer out: a message that is accurate-looking, well-formed, and useless — the
// exact shape the whole change exists to remove.
//
// CheckCompatible reports three distinct conditions and only one of them is a
// version skew. ErrNoVault is a NORMAL pre-`vp init` state; VaultUnreachableError
// is a misconfiguration with a different remedy. Spending this alert on either is
// how a reader is trained to skim all of them.
//
// Each case carries a POSITIVE CONTROL: it first asserts the fixture really does
// produce the error class it is named for. Without that a fixture that quietly
// produced nil would pass this test while proving nothing.
func TestNonMismatchVaultErrorsRaiseNoAlert(t *testing.T) {
	unreadableRoot := func(t *testing.T) string {
		t.Helper()
		if os.Geteuid() == 0 {
			t.Skip("running as root: mode bits do not deny access, so this fixture cannot produce EACCES")
		}
		parent := t.TempDir()
		root := filepath.Join(parent, "vault")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(parent, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
		return root
	}

	cases := []struct {
		name string
		root func(t *testing.T) string
		// want names the condition the fixture must actually produce, so the
		// silence below is silence about the RIGHT thing.
		wantNoVault     bool
		wantUnreachable bool
	}{
		{
			name:        "no vault configured at all",
			root:        func(*testing.T) string { return "" },
			wantNoVault: true,
		},
		{
			name:            "vault root does not exist",
			root:            func(t *testing.T) string { return filepath.Join(t.TempDir(), "not-here") },
			wantUnreachable: true,
		},
		{
			name:            "vault root cannot be read",
			root:            unreadableRoot,
			wantUnreachable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t)

			// POSITIVE CONTROL: the fixture produces the condition it claims.
			err := surface.CheckCompatible(root)
			if err == nil {
				t.Fatalf("fixture produced NO error, so this case proves nothing about the guard")
			}
			var ie *surface.IncompatibleError
			if errors.As(err, &ie) {
				t.Fatalf("fixture produced an IncompatibleError (%v) — it must produce a NON-mismatch condition", err)
			}
			if tc.wantNoVault && !errors.Is(err, surface.ErrNoVault) {
				t.Fatalf("want ErrNoVault, got %T: %v", err, err)
			}
			var ue *surface.VaultUnreachableError
			if tc.wantUnreachable && !errors.As(err, &ue) {
				t.Fatalf("want *VaultUnreachableError, got %T: %v", err, err)
			}

			// THE ASSERTION: the bootstrap says nothing about a surface mismatch.
			br := runBootstrap(t, storage.NewVault(root), vpctx.NewResolver(root))

			if br.SurfaceMismatch != nil {
				t.Errorf("a %T raised surface_mismatch = %+v.\nThe remedy it carries is `git pull && "+
					"make install`, which cannot fix this condition — its fix is vault_path. An alert "+
					"that is accurate-looking, well-formed and useless is the defect this whole change "+
					"removes.", err, br.SurfaceMismatch)
			}
			for _, probe := range []string{"SURFACE MISMATCH", "git pull && make install", "VP_SURFACE_GATE=warn"} {
				if strings.Contains(br.PostBootstrapInstructions, probe) {
					t.Errorf("a %T put surface-remediation prose %q in the directive:\n%s",
						err, probe, br.PostBootstrapInstructions)
				}
			}
		})
	}
}
