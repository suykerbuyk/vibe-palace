// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The departure-record-writer rule: every caller of a privileged record entry
// point is a finding (allowed ones are baseline entries), matched by package
// identity, and a vanished record writer is reported rather than passed.

const recVaultfs = `package vaultfs

import "example.com/internal/atomicfile"

func WriteDepartureRecord(h any, slug string, data []byte) (bool, error) {
	return false, atomicfile.Write("", "", data, atomicfile.ForDepartureRecord(h))
}
func RemoveDepartureRecord(h any, slug string) error { return nil }
`

const recAtomicfile = `package atomicfile

func Write(root, abs string, data []byte, opts ...any) error { return nil }
func ForDepartureRecord(h any) any                           { return h }
`

const recStorage = `package storage

import "example.com/internal/vaultfs"

type Vault struct{}

func (v *Vault) writeDeparture() error {
	_, err := vaultfs.WriteDepartureRecord(nil, "p", nil)
	return err
}
func (v *Vault) RecordDeparture(slug string) error { return v.writeDeparture() }
`

// recordWriterFindings runs the rule over the three owner packages plus
// extra, keyed by vault-relative FILE path (a writeFixtureTree key is a
// directory, whose one file is fixture.go).
func recordWriterFindings(t *testing.T, extra map[string]string) []string {
	t.Helper()
	root := writeFixtureTree(t, map[string]string{
		"internal/vaultfs":    recVaultfs,
		"internal/atomicfile": recAtomicfile,
		"internal/storage":    recStorage,
	})
	for rel, src := range extra {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := Run(root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindDepartureRecordWriter {
			out = append(out, f.Symbol)
		}
	}
	return out
}

func TestDepartureRecordWriterNamesEveryCaller(t *testing.T) {
	got := recordWriterFindings(t, nil)
	want := []string{"storage.Vault.writeDeparture", "vaultfs.WriteDepartureRecord"}
	if !slices.Equal(got, want) {
		t.Fatalf("findings = %v, want %v", got, want)
	}

	// Break-it: a rogue caller of each entry point, under an import alias, and
	// through a method value, is reported by its own name.
	rogue := `package tools

import (
	vfs "example.com/internal/vaultfs"
	"example.com/internal/storage"
)

func forgeRecord() { _, _ = vfs.WriteDepartureRecord(nil, "keep", nil) }
func dropRecord()  { _ = vfs.RemoveDepartureRecord(nil, "p") }
func viaMethod(v *storage.Vault) { f := v.RecordDeparture; _ = f("keep") }
`
	got = recordWriterFindings(t, map[string]string{"internal/tools/rogue.go": rogue})
	for _, w := range []string{"tools.forgeRecord", "tools.dropRecord", "tools.viaMethod"} {
		if !slices.Contains(got, w) {
			t.Errorf("rogue caller %s not reported: %v", w, got)
		}
	}

	// A caller in a test file is not production and is not reported.
	got = recordWriterFindings(t, map[string]string{"internal/tools/rogue_test.go": rogue})
	if slices.Contains(got, "tools.forgeRecord") {
		t.Errorf("a test-file caller was reported: %v", got)
	}
}

// Vacuity: with the record writer renamed away, the rule reports itself
// absent instead of passing on nothing.
func TestDepartureRecordWriterReportsAVanishedWriter(t *testing.T) {
	findings, err := Run(writeFixtureTree(t, map[string]string{"internal/storage": "package storage\n\nfunc F() {}\n"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Kind == KindDepartureRecordWriter && f.Symbol == "sourceaudit.departureRecordWriter/ABSENT/vaultfs.WriteDepartureRecord" {
			return
		}
	}
	t.Fatal("the ABSENT finding is missing")
}
