// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"errors"
	"os"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// A process that appends under the commit lock and dies before its counter
// bump leaves records no counter announces. A long-lived writer whose state
// cache still matches the unchanged counter must not run blind to them: the
// cache is valid only while the store files are the sizes it recorded
// (task indexstore-writer-cache-blind-to-uncounted-appends).

// warmWriter gives this process (the long-lived writer) a ledgered alpha and a
// cached state matching the current counter, and returns the vault.
func warmWriter(t *testing.T) *storage.Vault {
	t.Helper()
	v := newVault(t)
	fakeArchives(t)
	mustTx(t, v, func(tx *Tx) error {
		tx.UseRecipe(testRecipe)
		_, err := tx.EnsureLedger(nil)
		return err
	})
	return v
}

// writeThenExit runs the "write-then-exit" helper with write, and checks it
// left the counter's gen and epoch where it found them.
//
// The helper's Tx now sets the append marker before its write (U3), so a dying
// helper leaves a counter marked "a", and the next Lock would repair it with a
// gen bump, which invalidates the writer's cached state by gen. These tests
// pin the OTHER protection: the cache is valid only while the store files keep
// the sizes it recorded. That is what protects a writer when the dying process
// left NO marker (a binary older than the marker, or a kill before the marker
// write). The fixture therefore strips the marker, rewriting the counter to the
// clean two-field form of the same gen and epoch, which is exactly what such a
// process leaves.
func writeThenExit(t *testing.T, v *storage.Vault, write string) {
	t.Helper()
	before := readGen(t, v, "alpha")
	h := startHelper(t, v, "write-then-exit", "VP_INDEXSTORE_WRITE="+write)
	h.wait(t)
	if after := readGen(t, v, "alpha"); after != before {
		t.Fatalf("fixture: the helper moved the counter %v -> %v; it must exit before its bump", before, after)
	}
	gp, err := v.IndexGenerationPath("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gp, []byte(formatGen(before, markNone)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func genOf(t *testing.T, v *storage.Vault) Gen {
	t.Helper()
	var g Gen
	mustTx(t, v, func(tx *Tx) error { g = tx.Generation(); return nil })
	return g
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestWriterCacheSeesAnUncountedAppend(t *testing.T) {
	t.Run("the same archive is not re-ingested", func(t *testing.T) {
		v := warmWriter(t)
		writeThenExit(t, v, "commit-S-A")
		pf, err := filesFor(v, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		chunks, ledger := fileSize(t, pf.chunks), fileSize(t, pf.ledger)
		mustTx(t, v, func(tx *Tx) error {
			tx.UseRecipe(testRecipe)
			l, err := tx.Ledger()
			if err != nil {
				return err
			}
			if p := l.Pending([]ArchiveRef{{SessionID: "S", SHA: "A"}}); len(p) != 0 {
				t.Errorf("archive A reads as pending after the crashed commit: %v", p)
			}
			return tx.CommitArchive(commitOf("S", "A", "2026-05-13", "X", "Y"), noVectors{})
		})
		if c, l := fileSize(t, pf.chunks), fileSize(t, pf.ledger); c != chunks || l != ledger {
			t.Errorf("re-committing A appended again: chunks.jsonl %d -> %d bytes, ledger.jsonl %d -> %d bytes", chunks, c, ledger, l)
		}
	})

	t.Run("the failure count is the true one", func(t *testing.T) {
		v := warmWriter(t)
		mustTx(t, v, func(tx *Tx) error { return tx.RecordFailure("S2", "B", errors.New("first")) })
		writeThenExit(t, v, "fail-S2-B") // the second failure, uncounted
		mustTx(t, v, func(tx *Tx) error { return tx.RecordFailure("S2", "B", errors.New("third")) })
		if n := ledgered(t, v).Ledger().FailureCount("B"); n != 3 {
			t.Errorf("FailureCount(B) = %d after three failures, want 3", n)
		}
	})

	t.Run("a different archive of the session supersedes", func(t *testing.T) {
		v := warmWriter(t)
		writeThenExit(t, v, "commit-S-A")
		a2 := commitOf("S", "A2", "2026-05-14", "Z")
		mustTx(t, v, func(tx *Tx) error {
			tx.UseRecipe(testRecipe)
			err := tx.CommitArchive(a2, noVectors{})
			if !errors.Is(err, ErrOtherArchive) {
				t.Errorf("CommitArchive(A2) = %v, want ErrOtherArchive: the session's ledger names A", err)
				return nil
			}
			return tx.Supersede(a2, noVectors{})
		})
		l := ledgered(t, v).Ledger()
		if _, gone := l.superseded["A"]; !gone {
			t.Error("archive A is not recorded superseded, so Reap would never drop its chunks")
		}
		s, ok := l.Session("S")
		if !ok || s.State != StateLive || s.SHA != "A2" || s.Generation != 2 {
			t.Errorf("session S = %+v (found %v), want live A2 at generation 2", s, ok)
		}
	})
}
