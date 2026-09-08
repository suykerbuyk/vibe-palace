// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

// seedOverCapResume builds the smallest vault that produces a resume-discipline
// finding, and returns the artifact path the audit will name.
func seedOverCapResume(t *testing.T, v *storage.Vault, project string, size int) string {
	t.Helper()
	dir := filepath.Join(v.Root, "Projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume.md"), []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	return "Projects/" + project + "/resume.md"
}

func loadTestBaseline(t *testing.T, v *storage.Vault) vaultaudit.Baseline {
	t.Helper()
	b, err := vaultaudit.LoadBaseline(filepath.Join(v.Root, filepath.FromSlash(vaultaudit.BaselineRelPath)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 🔴 TestRunAuditVault_AcceptDoesNotRaiseARecordedMeasurement drives the real
// --accept path end to end, which nothing did before this change: the whole branch
// — LoadBaseline, Regenerate, Save — had no test at any layer.
//
// This is the flag-plumbing half of the guard. The rule itself is pinned at package
// level in vaultaudit; what this proves is that the CLI actually reaches it.
func TestRunAuditVault_AcceptDoesNotRaiseARecordedMeasurement(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	rel := seedOverCapResume(t, vault, "alpha", check.ResumeMaxBytes+500)

	prior := vaultaudit.Baseline{Dimensions: map[string]vaultaudit.DimensionBaseline{
		vaultaudit.DimResumeDiscipline: {
			Reason:   "accepted debt",
			Accepted: []string{rel},
			Measured: map[string]int64{rel: int64(check.ResumeMaxBytes + 1)},
		},
	}}
	basePath := filepath.Join(vault.Root, filepath.FromSlash(vaultaudit.BaselineRelPath))
	if err := prior.Save(vault.Root, basePath); err != nil {
		t.Fatal(err)
	}

	if code := runAuditVault(vault, auditVaultOpts{accept: true}, "2026-09-05", io.Discard); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}

	got := loadTestBaseline(t, vault).Dimensions[vaultaudit.DimResumeDiscipline].Measured[rel]
	if got != int64(check.ResumeMaxBytes+1) {
		t.Fatalf("measured = %d, want exactly %d — a plain --accept must never raise a "+
			"recorded measurement, or an operator absorbing an unrelated finding erases the guard",
			got, check.ResumeMaxBytes+1)
	}
}

// TestRunAuditVault_AcceptRaiseRecordsTheCurrentMeasurement — the other direction, so
// the pair fails in both. An assertion that only checked "did not raise" would pass
// against a --raise that was never wired up at all.
func TestRunAuditVault_AcceptRaiseRecordsTheCurrentMeasurement(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	size := check.ResumeMaxBytes + 500
	rel := seedOverCapResume(t, vault, "alpha", size)

	prior := vaultaudit.Baseline{Dimensions: map[string]vaultaudit.DimensionBaseline{
		vaultaudit.DimResumeDiscipline: {
			Reason:   "accepted debt",
			Accepted: []string{rel},
			Measured: map[string]int64{rel: int64(check.ResumeMaxBytes + 1)},
		},
	}}
	basePath := filepath.Join(vault.Root, filepath.FromSlash(vaultaudit.BaselineRelPath))
	if err := prior.Save(vault.Root, basePath); err != nil {
		t.Fatal(err)
	}

	if code := runAuditVault(vault, auditVaultOpts{accept: true, raise: true}, "2026-09-05", io.Discard); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}

	got := loadTestBaseline(t, vault).Dimensions[vaultaudit.DimResumeDiscipline].Measured[rel]
	if got != int64(size) {
		t.Fatalf("measured = %d, want exactly %d — --raise re-records the artifacts in "+
			"this run's NEW set", got, size)
	}
}

// TestRunAuditVault_AcceptRecordsAMeasurementForALegacyEntry — the convergence path
// at CLI level: a baseline written before measurements existed picks one up on the
// next accept, so the file moves onto the richer form instead of grandfathering.
func TestRunAuditVault_AcceptRecordsAMeasurementForALegacyEntry(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	size := check.ResumeMaxBytes + 500
	rel := seedOverCapResume(t, vault, "alpha", size)

	prior := vaultaudit.Baseline{Dimensions: map[string]vaultaudit.DimensionBaseline{
		vaultaudit.DimResumeDiscipline: {Reason: "accepted debt", Accepted: []string{rel}},
	}}
	basePath := filepath.Join(vault.Root, filepath.FromSlash(vaultaudit.BaselineRelPath))
	if err := prior.Save(vault.Root, basePath); err != nil {
		t.Fatal(err)
	}

	if code := runAuditVault(vault, auditVaultOpts{accept: true}, "2026-09-05", io.Discard); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK", code)
	}

	got := loadTestBaseline(t, vault).Dimensions[vaultaudit.DimResumeDiscipline].Measured[rel]
	if got != int64(size) {
		t.Fatalf("measured = %d, want %d recorded for an entry that had none", got, size)
	}
}

// TestAuditVault_RaiseWithoutAcceptIsAUsageError — fail closed, and before the vault
// is opened, so the error is reported on argument grounds rather than depending on
// whatever vault the host happens to resolve.
func TestAuditVault_RaiseWithoutAcceptIsAUsageError(t *testing.T) {
	if code := cmdAuditVault().Run([]string{"--raise"}); code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser — --raise alone must not be a silent no-op", code)
	}
}

// TestAuditVault_SynopsisNamesEveryFlag: the synopsis is a hand-written string that
// nothing derives from Flags, so it rots silently the moment a flag is added. This is
// the cheapest possible guard against that.
func TestAuditVault_SynopsisNamesEveryFlag(t *testing.T) {
	cmd := cmdAuditVault()
	for _, f := range cmd.Flags {
		if !strings.Contains(cmd.Synopsis, f.Name) {
			t.Errorf("synopsis %q does not name %s", cmd.Synopsis, f.Name)
		}
	}
}

// TestAuditVault_DescriptionNamesEveryDimension is the CLI twin of the MCP tool's
// derivation pin, and it exists for the same reason: this description named the same
// stale five dimensions its MCP sibling did, from a separate copy of the list. Both now
// build from vaultaudit.DimensionNames, and this fails if either stops.
//
// It mirrors TestAuditVault_SynopsisNamesEveryFlag above — same shape, same argument: a
// help string that enumerates something the code already holds must be derived from it,
// or it is a second answer waiting to disagree with the first.
func TestAuditVault_DescriptionNamesEveryDimension(t *testing.T) {
	desc := cmdAuditVault().Description

	names := vaultaudit.DimensionNames()
	if len(names) == 0 {
		t.Fatal("vaultaudit.DimensionNames returned nothing — the registry is empty")
	}
	for _, name := range names {
		if !strings.Contains(desc, name) {
			t.Errorf("vp audit vault description omits dimension %q\n  description: %s", name, desc)
		}
	}
}

// TestAuditVault_ReacceptWithoutAcceptIsAUsageError — same shape and same argument as
// its --raise twin above: an override that silently overrides nothing leaves the
// operator believing they cleared a refusal that was never evaluated.
func TestAuditVault_ReacceptWithoutAcceptIsAUsageError(t *testing.T) {
	if code := cmdAuditVault().Run([]string{"--reaccept"}); code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser — --reaccept alone must not be a silent no-op", code)
	}
}

// 🔴 TestRunAuditVault_AcceptRefusesARecrossedArtifact is the defect closed, driven
// through the real CLI path. The baseline below is the shape a DROP leaves behind: the
// artifact accepts nothing any more, and the only thing that survived is the record
// that it was once accepted. A plain --accept must refuse, and must write NOTHING —
// a refusal that had already half-written the baseline would be worse than no refusal.
func TestRunAuditVault_AcceptRefusesARecrossedArtifact(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	rel := seedOverCapResume(t, vault, "alpha", check.ResumeMaxBytes+5000)

	prior := vaultaudit.Baseline{Dimensions: map[string]vaultaudit.DimensionBaseline{
		vaultaudit.DimResumeDiscipline: {
			Reason:        "accepted debt",
			Accepted:      []string{},
			PriorAccepted: []string{rel},
		},
	}}
	basePath := filepath.Join(vault.Root, filepath.FromSlash(vaultaudit.BaselineRelPath))
	if err := prior.Save(vault.Root, basePath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}

	if code := runAuditVault(vault, auditVaultOpts{accept: true}, "2026-09-07", io.Discard); code != cli.ExitUser {
		t.Fatalf("exit = %d, want ExitUser — accepting a re-crossing without --reaccept re-arms "+
			"the ratchet at the new figure with nothing recording that this has happened before", code)
	}

	after, err := os.ReadFile(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("the refused run still wrote the baseline:\n--- before ---\n%s\n--- after ---\n%s",
			before, after)
	}
}

// TestRunAuditVault_AcceptReacceptAcceptsARecrossedArtifact — the override works, and
// it does NOT erase the history it overrode. A second re-crossing must meet the same
// refusal as the first; an override that cleared the record would make the third
// crossing look like a first, which is the failure this whole change is about.
func TestRunAuditVault_AcceptReacceptAcceptsARecrossedArtifact(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	rel := seedOverCapResume(t, vault, "alpha", check.ResumeMaxBytes+5000)

	prior := vaultaudit.Baseline{Dimensions: map[string]vaultaudit.DimensionBaseline{
		vaultaudit.DimResumeDiscipline: {
			Reason:        "accepted debt",
			Accepted:      []string{},
			PriorAccepted: []string{rel},
		},
	}}
	basePath := filepath.Join(vault.Root, filepath.FromSlash(vaultaudit.BaselineRelPath))
	if err := prior.Save(vault.Root, basePath); err != nil {
		t.Fatal(err)
	}

	code := runAuditVault(vault, auditVaultOpts{accept: true, reaccept: true}, "2026-09-07", io.Discard)
	if code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK — --reaccept is the sanctioned way through the refusal", code)
	}

	got := loadTestBaseline(t, vault).Dimensions[vaultaudit.DimResumeDiscipline]
	if !slices.Contains(got.Accepted, rel) {
		t.Fatalf("accepted = %v, want the re-crossing accepted", got.Accepted)
	}
	if !slices.Contains(got.PriorAccepted, rel) {
		t.Fatalf("prior_accepted = %v, want the history RETAINED — the override is a licence to "+
			"accept, never an erasure of the record that made it necessary", got.PriorAccepted)
	}
}

// TestRunAuditVault_AcceptRecordsPriorAcceptanceForAFirstCrossing — the other end of
// the same rule. The first --accept of an artifact with no history must SUCCEED (that
// is today's path, unchanged) and must leave the record behind, or the refusal above
// can never fire on any artifact this binary accepted.
func TestRunAuditVault_AcceptRecordsPriorAcceptanceForAFirstCrossing(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	rel := seedOverCapResume(t, vault, "alpha", check.ResumeMaxBytes+500)

	if code := runAuditVault(vault, auditVaultOpts{accept: true}, "2026-09-07", io.Discard); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK — a first acceptance has no history to refuse", code)
	}

	got := loadTestBaseline(t, vault).Dimensions[vaultaudit.DimResumeDiscipline]
	if !slices.Contains(got.PriorAccepted, rel) {
		t.Fatalf("prior_accepted = %v, want the first acceptance recorded — without this the "+
			"refusal is unreachable for every artifact accepted from here on", got.PriorAccepted)
	}
}

// 🔴 TestRunAuditVault_ReportWarnsBeforeTheRefusalRefuses — the two halves of this
// guard must agree, and only an end-to-end render proves they do. The refusal fires on
// a command the operator types LATER; if the report they read first says nothing about
// the artifact's history, the refusal arrives out of nowhere and reads as a bug in the
// tool rather than as the escalation it is. Asserting the annotation at the Diff layer
// does not prove it survives into the rendered document.
func TestRunAuditVault_ReportWarnsBeforeTheRefusalRefuses(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	rel := seedOverCapResume(t, vault, "alpha", check.ResumeMaxBytes+5000)

	prior := vaultaudit.Baseline{Dimensions: map[string]vaultaudit.DimensionBaseline{
		vaultaudit.DimResumeDiscipline: {
			Reason:        "accepted debt",
			Accepted:      []string{},
			PriorAccepted: []string{rel},
		},
	}}
	basePath := filepath.Join(vault.Root, filepath.FromSlash(vaultaudit.BaselineRelPath))
	if err := prior.Save(vault.Root, basePath); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := runAuditVault(vault, auditVaultOpts{}, "2026-09-07", &out); code != cli.ExitOK {
		t.Fatalf("exit = %d, want ExitOK — the audit is advisory and never blocks", code)
	}

	body := out.String()
	if !strings.Contains(body, "ACCEPTED BEFORE, DROPPED, AND BACK") {
		t.Fatalf("the rendered report does not warn that %s has crossed before:\n%s", rel, body)
	}
	if !strings.Contains(body, "--reaccept") {
		t.Fatalf("the rendered report names no way through the refusal it is warning about:\n%s", body)
	}
}
