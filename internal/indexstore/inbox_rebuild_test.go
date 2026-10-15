// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func readWanted(t *testing.T, v *storage.Vault) bool {
	t.Helper()
	want, err := ReadRebuildWanted(v, "alpha")
	if err != nil {
		t.Fatalf("ReadRebuildWanted: %v", err)
	}
	return want
}

func firsts(t *testing.T, v *storage.Vault) []string {
	t.Helper()
	got, err := ReadFirsts(v, "alpha")
	if err != nil {
		t.Fatalf("ReadFirsts: %v", err)
	}
	return got
}

// TestRebuildMarkerCoexistsWithShasAndSurvivesShaWrites (U1 / ADR-016 decision
// 5): the generic rebuild-corpus marker is a reserved non-sha line that every
// sha reader drops and every sha writer PRESERVES. So NoteFirst and DropFirsts,
// which rewrite the inbox's shas, never lose a marker a cold search recorded,
// and the marker never leaks into the sha list a holder ingests.
func TestRebuildMarkerCoexistsWithShasAndSurvivesShaWrites(t *testing.T) {
	v := newVault(t)
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)

	// A marker, written on its own, sets RebuildWanted but leaves Firsts empty.
	if err := NoteRebuild(context.Background(), v, "alpha", NoTimeout); err != nil {
		t.Fatal(err)
	}
	if !readWanted(t, v) {
		t.Fatal("NoteRebuild did not set the marker")
	}
	if got := firsts(t, v); len(got) != 0 {
		t.Fatalf("Firsts = %v, want empty — the marker is not a sha", got)
	}

	// A sha write (NoteFirst) must preserve the marker AND record the sha.
	if err := NoteFirst(context.Background(), v, "alpha", a, NoTimeout); err != nil {
		t.Fatal(err)
	}
	if err := NoteFirst(context.Background(), v, "alpha", b, NoTimeout); err != nil {
		t.Fatal(err)
	}
	if !readWanted(t, v) {
		t.Fatal("NoteFirst dropped the marker while rewriting the shas")
	}
	if got := firsts(t, v); !slices.Equal(got, []string{a, b}) {
		t.Fatalf("Firsts = %v, want [a b]", got)
	}

	// Dropping a served sha keeps the marker; the inbox file survives because
	// the marker alone keeps it.
	mustTx(t, v, func(tx *Tx) error { return tx.DropFirsts([]string{a, b}) })
	if !readWanted(t, v) {
		t.Fatal("DropFirsts dropped the marker")
	}
	if got := firsts(t, v); len(got) != 0 {
		t.Fatalf("Firsts after dropping both = %v, want empty", got)
	}
	pf, _ := filesFor(v, "alpha")
	if _, err := os.Stat(filepath.Join(pf.dir, inboxFile)); err != nil {
		t.Fatalf("the inbox was removed while a marker was still set (stat err %v)", err)
	}

	// Clearing the marker with shas present keeps the shas.
	if err := NoteFirst(context.Background(), v, "alpha", a, NoTimeout); err != nil {
		t.Fatal(err)
	}
	if err := ClearRebuild(context.Background(), v, "alpha", NoTimeout); err != nil {
		t.Fatal(err)
	}
	if readWanted(t, v) {
		t.Fatal("ClearRebuild did not clear the marker")
	}
	if got := firsts(t, v); !slices.Equal(got, []string{a}) {
		t.Fatalf("Firsts after clearing the marker = %v, want [a] — the sha must survive", got)
	}

	// Clearing the marker when no sha remains removes the inbox file.
	mustTx(t, v, func(tx *Tx) error { return tx.DropFirsts([]string{a}) })
	if _, err := os.Stat(filepath.Join(pf.dir, inboxFile)); !os.IsNotExist(err) {
		t.Fatalf("an inbox with no marker and no sha is still there (stat err %v)", err)
	}

	// The lock-free reader agrees with the Tx reader.
	if want, err := ReadRebuildWanted(v, "alpha"); err != nil || want {
		t.Fatalf("ReadRebuildWanted = (%v, %v), want (false, nil)", want, err)
	}
}
