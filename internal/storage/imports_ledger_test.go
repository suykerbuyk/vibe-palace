// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// The host-local ledger writers are refused in the ambient vault before any
// directory or lock exists, and write normally elsewhere.
func TestHostLocalLedgerWritersAreRefusedInTheAmbientVault(t *testing.T) {
	ledger := func(root string) string {
		return filepath.Join(root, "palace", ".local", "imports", "p", "l.jsonl")
	}
	ctl := t.TempDir()
	v := NewVault(ctl)
	if err := v.AppendHostLocalLine(ledger(ctl), []byte("a\n")); err != nil {
		t.Fatal(err)
	}
	if err := v.UpdateHostLocalLedger(ledger(ctl), func(cur []byte) ([]byte, error) {
		if string(cur) != "a\n" {
			t.Errorf("current = %q", cur)
		}
		return []byte("b\n"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(ledger(ctl)); string(got) != "a\nb\n" {
		t.Fatalf("ledger = %q", got)
	}

	amb := t.TempDir()
	t.Cleanup(surface.SetAmbientVaultRoot(amb))
	av := NewVault(amb)
	for name, f := range map[string]func(){
		"append": func() { _ = av.AppendHostLocalLine(ledger(amb), []byte("x\n")) },
		"update": func() {
			_ = av.UpdateHostLocalLedger(ledger(amb), func([]byte) ([]byte, error) { return []byte("x\n"), nil })
		},
	} {
		if msg := dirSeatPanic(f); !strings.Contains(msg, "CONFIGURED vault") {
			t.Errorf("%s: no tripwire refusal, got %q", name, msg)
		}
	}
	if _, err := os.Lstat(filepath.Join(amb, "palace")); err == nil {
		t.Error("palace/ was created in the ambient vault")
	}
	if _, err := os.Lstat(filepath.Join(amb, ".vp-locks")); err == nil {
		t.Error(".vp-locks/ was created in the ambient vault")
	}
}
