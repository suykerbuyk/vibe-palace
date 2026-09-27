// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func departedWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The row's three states: none (Pass), departed caches present (Info, named,
// with the remedy), and undecidable (Info, naming the guard — never "none").
func TestCheckDepartedCaches(t *testing.T) {
	record := func(t *testing.T, root string) {
		b, err := (departure.Record{Slug: "gone", Kind: departure.MovedToVault,
			To: "git@example.test:q/v.git", Date: "2026-09-27"}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		departedWrite(t, root, departure.RelPath("gone"), string(b))
		departedWrite(t, root, "palace/.local/embed-cache/gone/aaaaaaaa.vec", "VVVV")
	}

	t.Run("none", func(t *testing.T) {
		root := t.TempDir()
		departedWrite(t, root, "Projects/keep/resume.md", "k\n")
		r := CheckDepartedCaches(storage.NewVault(root))
		if r.Status != Pass || r.Summary != "none" {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("present", func(t *testing.T) {
		root := t.TempDir()
		departedWrite(t, root, "Projects/keep/resume.md", "k\n")
		record(t, root)
		r := CheckDepartedCaches(storage.NewVault(root))
		if r.Status != Info || !strings.Contains(r.Summary, "gone") ||
			len(r.Details) == 0 || !strings.Contains(r.Details[0], "vp vault pull") {
			t.Errorf("got %+v", r)
		}
		if _, err := os.Lstat(filepath.Join(root, "palace/.local/embed-cache/gone/aaaaaaaa.vec")); err != nil {
			t.Errorf("the row removed the cache: %v", err)
		}
	})
	t.Run("undecidable", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "Projects"), 0o755); err != nil {
			t.Fatal(err)
		}
		record(t, root)
		r := CheckDepartedCaches(storage.NewVault(root))
		if r.Status != Info || !strings.HasPrefix(r.Summary, "undecidable: ") {
			t.Errorf("an empty listing must read undecidable, never none: %+v", r)
		}
	})
}
