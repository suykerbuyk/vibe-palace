// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

// runAuditVaultTool drives the real MCP handler and returns its result map.
func runAuditVaultTool(t *testing.T, vault *storage.Vault, args string) map[string]any {
	t.Helper()

	res, err := AuditVaultTool(vault).Handler(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("vp_audit_vault(%s) errored: %v", args, err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("vp_audit_vault returned %T, want map[string]any", res)
	}
	return m
}

// 🔴 TestAuditVault_WriteWithoutADateSucceeds pins the operator's 2026-08-19 reversal.
//
// This call used to be REFUSED. The handler required `date` on write and returned
// "the server will not invent a date the caller already knows" — the withdrawn
// principle, enforced. The writer now owns the calendar day, so a client that sends
// nothing is the NORMAL case, not an error.
//
// MUTATION: restore the `if p.Date == "" { return nil, ... }` guard and this test goes
// red on the missing report_path (the handler errors before writing).
func TestAuditVault_WriteWithoutADateSucceeds(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	out := runAuditVaultTool(t, vault, `{"write": true}`)

	rel, ok := out["report_path"].(string)
	if !ok || rel == "" {
		t.Fatalf("write=true with no date produced no report_path (%v) — a client that sends "+
			"no date is the normal case now, not a refusal", out)
	}

	now := time.Now()
	want := vault.CalendarDay(now)
	if !strings.Contains(rel, want) {
		t.Errorf("report_path = %q, want it to carry the writer's calendar day %q", rel, want)
	}
}

// 🔴 TestAuditVault_ClientDateDoesNotWin is the load-bearing half.
//
// A guard that merely stops REFUSING is not the ruling. The ruling is that a
// client-supplied date is not an authority — so one that arrives on the wire must be
// discarded, not honoured, not merged, not preferred. A handler that quietly used
// p.Date when it happened to be present would pass the test above and still ship the
// second clock.
//
// The client date is deliberately absurd and far from any plausible writer day, so the
// assertion cannot pass by coincidence of running near a boundary.
//
// MUTATION: make the client date win (`day := p.Date` when non-empty, or pass p.Date
// to ReportRelPath/Render) and this test goes red on both assertions.
func TestAuditVault_ClientDateDoesNotWin(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	const clientDay = "1999-01-01"
	out := runAuditVaultTool(t, vault, `{"write": true, "date": "`+clientDay+`"}`)

	rel, _ := out["report_path"].(string)
	if strings.Contains(rel, clientDay) {
		t.Errorf("report_path = %q — it filed under the CLIENT's date. The writer owns the "+
			"calendar day; a client cannot choose the day of a transaction", rel)
	}

	now := time.Now()
	want := vault.CalendarDay(now)
	if !strings.Contains(rel, want) {
		t.Errorf("report_path = %q, want the writer's calendar day %q — the client date was "+
			"rejected but the writer day did not replace it", rel, want)
	}
}

// TestAuditVault_InlineRenderCarriesTheWriterDay covers the write=false path, which
// takes a different branch and used to render UNDATED.
//
// Same helper, same day, one behaviour — the point of the change is that the two paths
// cannot drift, so pinning only the written one would leave half of it unheld.
func TestAuditVault_InlineRenderCarriesTheWriterDay(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	out := runAuditVaultTool(t, vault, `{"write": false, "date": "1999-01-01"}`)

	body, ok := out["report"].(string)
	if !ok || body == "" {
		t.Fatalf("write=false produced no inline report: %v", out)
	}
	if strings.Contains(body, "1999-01-01") {
		t.Errorf("the inline render carries the CLIENT's date:\n%s", firstNLines(body, 6))
	}
	now := time.Now()
	if want := vault.CalendarDay(now); !strings.Contains(body, want) {
		t.Errorf("the inline render does not carry the writer day %q:\n%s", want, firstNLines(body, 6))
	}
}

// TestAuditVaultSchema_DoesNotCarryTheWithdrawnPrinciple pins the DELETION.
//
// The operator ordered the sentence removed, not paraphrased and not demoted to a
// comment. A schema description is the surface every agent reads, so leaving it there
// would keep teaching the withdrawn rule regardless of what the handler does.
func TestAuditVaultSchema_DoesNotCarryTheWithdrawnPrinciple(t *testing.T) {
	schema := string(auditVaultSchema)

	for _, withdrawn := range []string{
		"no business inventing",
		"the agent knows the session date",
		"required when write is true",
	} {
		if strings.Contains(schema, withdrawn) {
			t.Errorf("vp_audit_vault's schema still carries the withdrawn text %q — it was "+
				"ordered DELETED, not paraphrased", withdrawn)
		}
	}
	if !strings.Contains(schema, "IGNORED") {
		t.Error("the `date` description must say plainly that it is IGNORED — a parameter " +
			"that is silently discarded is theatre")
	}
}

func firstNLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// TestAuditVaultTool_DescriptionNamesEveryDimension pins the DERIVATION, not the wording.
//
// This description used to name five dimensions against a registry of ten, and nothing
// caught it: the surface golden covers a tool's name, mutating flag and schema digest and
// NOT its Description (the standing "nothing pins a tool's Description against its
// behaviour" gap). The description is now built from vaultaudit.DimensionNames at
// construction time, so the two cannot disagree — this test is what proves the wiring is
// still in place, and it starts failing the moment someone replaces the fmt.Sprintf with
// a typed-out list again.
func TestAuditVaultTool_DescriptionNamesEveryDimension(t *testing.T) {
	tool := AuditVaultTool(storage.NewVault(t.TempDir()))

	names := vaultaudit.DimensionNames()
	if len(names) == 0 {
		t.Fatal("vaultaudit.DimensionNames returned nothing — the registry is empty")
	}
	for _, name := range names {
		if !strings.Contains(tool.Description, name) {
			t.Errorf("vp_audit_vault description omits dimension %q\n  description: %s",
				name, tool.Description)
		}
	}
}
