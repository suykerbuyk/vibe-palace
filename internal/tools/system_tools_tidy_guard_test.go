// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// D2 (split-and-sweep-reporting-defects-found-by-rehearsal-a2), MCP arm: the
// dry run predicts a real tidy's refusal as DATA — a would_refuse field and a
// summary clause on a normal result — not as a tool error, which would read as
// "the dry run failed" (Chair ruling, 2026-09-27).
func TestVaultTidy_DryRunReportsWouldRefuse(t *testing.T) {
	sandboxHostEnv(t)
	root := initVaultRepo(t)
	initCommittedProject(t, root, "vibe-palace")
	vault := storage.NewVault(root)
	tool := VaultTidyTool(vault)
	mustWrite(t, vault, "Projects/vibe-palace/sessions/2026-09-27.md", "session\n")

	dry := func() map[string]any {
		t.Helper()
		params, _ := json.Marshal(vaultTidyParams{DryRun: true})
		res, err := tool.Handler(context.Background(), params)
		if err != nil {
			t.Fatalf("a dry run whose prediction is a refusal must not be a tool error: %v", err)
		}
		return res.(map[string]any)
	}

	if m := dry(); m["would_refuse"] != nil {
		t.Fatalf("no pending record: would_refuse must be absent, got %v", m["would_refuse"])
	}

	b, err := (departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: "q"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, departure.RelPath("alpha"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}

	m := dry()
	wr, _ := m["would_refuse"].(string)
	if !strings.Contains(wr, "refusing to commit") || !strings.Contains(wr, departure.RelPath("alpha")) {
		t.Errorf("would_refuse = %q, want the guard's refusal naming the record", wr)
	}
	if s, _ := m["summary"].(string); !strings.Contains(s, "a real tidy would refuse") {
		t.Errorf("summary = %q, want it to say a real tidy would refuse", s)
	}
	if m["status"] != "ok" || m["dry_run"] != true || m["committed"] != false {
		t.Errorf("want a normal dry-run result, got status=%v dry_run=%v committed=%v", m["status"], m["dry_run"], m["committed"])
	}
	if !strings.Contains(strings.Join(m["swept"].([]string), ","), "sessions/2026-09-27.md") {
		t.Errorf("the result must still carry what would be swept: %v", m["swept"])
	}
}
