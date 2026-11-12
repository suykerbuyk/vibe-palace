// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// rawCounter returns the counter file's text, bypassing ReadGeneration.
func rawCounter(t *testing.T, v *storage.Vault, project string) string {
	t.Helper()
	p, err := v.IndexGenerationPath(project)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// killPurgeHelper forks the purge-kill helper (a destructive Tx that has run
// past the purge-ledger step, which is after its mid-Tx bump) and SIGKILLs it.
// Commit and Release never ran.
func killPurgeHelper(t *testing.T, v *storage.Vault) {
	t.Helper()
	h := startHelper(t, v, "purge-kill")
	h.ready(t)
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = h.cmd.Wait()
}

// TestGenbumpReproKillLeavesTrustedCounter is the done-definition's first
// form: after a real SIGKILL of a writer inside a destructive Tx, a lock-free
// ReadGeneration must not hand back a clean counter, because any equality
// witness would trust it for a store whose durable write was never announced.
//
// Mutation: drop the in-flight flag write in beginDestructive. ReadGeneration
// then returns a clean counter and this test fails.
func TestGenbumpReproKillLeavesTrustedCounter(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)
	killPurgeHelper(t, v)

	g, err := ReadGeneration(v, "alpha")
	if err == nil {
		t.Fatalf("lock-free ReadGeneration returned a clean counter %v after a writer died mid-destructive-Tx; any equality witness trusts it", g)
	}
}

// TestAKilledDestructiveTxLeavesTheCounterInFlightUntilTheNextLock pins the
// protocol across a real SIGKILL:
//
//	(a) the raw counter file carries the in-flight flag;
//	(b) ReadGeneration reports ErrWriteInFlight with the zero Gen, and
//	    writes nothing (the raw file is the same after the read);
//	(c) Lock + Release with no writes repairs it: the epoch differs from the
//	    flagged counter's, the flag is cleared, Gen moved, and ReadGeneration
//	    is clean again.
//
// Mutations: for (a) and (b), drop the flag write in beginDestructive, or
// route DropTranscriptTier's mid-Tx bump through plain bumpGenFile (the kill
// lands after that bump, so the flag would be cleared); for (c), drop the
// repair in ensureGenFile.
func TestAKilledDestructiveTxLeavesTheCounterInFlightUntilTheNextLock(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)
	killPurgeHelper(t, v)

	flagged := rawCounter(t, v, "alpha")
	fields := strings.Fields(flagged)
	if len(fields) != 3 || fields[2] != inFlightMark {
		t.Fatalf("(a) raw counter after SIGKILL = %q, want %q as its third field", flagged, inFlightMark)
	}
	gp, _ := v.IndexGenerationPath("alpha")
	_, flaggedInFile, err := readGenRaw(gp)
	if err != nil || !flaggedInFile {
		t.Fatalf("(a) readGenRaw = flag %v err %v, want flag set", flaggedInFile, err)
	}
	rawG, _, _ := readGenRaw(gp)

	g, err := ReadGeneration(v, "alpha")
	if !errors.Is(err, ErrWriteInFlight) {
		t.Fatalf("(b) ReadGeneration err = %v, want ErrWriteInFlight", err)
	}
	if g != (Gen{}) {
		t.Fatalf("(b) ReadGeneration returned %v with the error, want the zero Gen", g)
	}
	if after := rawCounter(t, v, "alpha"); after != flagged {
		t.Fatalf("(b) ReadGeneration changed the counter file: %q -> %q (a reader must never write)", flagged, after)
	}

	mustTx(t, v, func(tx *Tx) error {
		if got := tx.Generation(); got.Epoch == rawG.Epoch || got.Gen <= rawG.Gen {
			t.Errorf("(c) Lock returned %v over the flagged %v, want a new epoch and a higher Gen", got, rawG)
		}
		return nil
	})
	if after := rawCounter(t, v, "alpha"); len(strings.Fields(after)) != 2 {
		t.Fatalf("(c) raw counter after Lock+Release = %q, want the flag cleared", after)
	}
	clean, err := ReadGeneration(v, "alpha")
	if err != nil {
		t.Fatalf("(c) ReadGeneration after repair: %v", err)
	}
	if clean.Epoch == rawG.Epoch || clean.Gen <= rawG.Gen {
		t.Fatalf("(c) repaired counter %v did not move past the flagged %v", clean, rawG)
	}
}

// TestALiveDestructiveTxIsInFlightUntilItFinishes: from beginDestructive to
// the finishing bump ReadGeneration reports ErrWriteInFlight, through every
// step including the mid-Tx bump of DropTranscriptTier; Commit clears it and
// the Tx's gen moved by two (one pre-write bump, one finishing bump) plus the
// purge's mid-Tx bump.
//
// Mutation: route the mid-Tx bump through plain bumpGenFile. The flag is then
// cleared at the purge-chunks step, which this test observes.
func TestALiveDestructiveTxIsInFlightUntilItFinishes(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)
	if ledgerKinds(t, v)[recSession] == 0 {
		t.Fatal("PRESENCE: no recSession line, so the purge has no mid-Tx bump to test")
	}
	before, err := ReadGeneration(v, "alpha")
	if err != nil {
		t.Fatal(err)
	}

	var seen []string
	old := commitStep
	t.Cleanup(func() { commitStep = old })
	commitStep = func(step string) error {
		if _, rerr := ReadGeneration(v, "alpha"); !errors.Is(rerr, ErrWriteInFlight) {
			t.Errorf("at step %q ReadGeneration err = %v, want ErrWriteInFlight", step, rerr)
		}
		seen = append(seen, step)
		return nil
	}

	tx, err := Lock(context.Background(), v, "alpha", NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.DropTranscriptTier(nil); err != nil {
		_ = tx.Release()
		t.Fatal(err)
	}
	if len(seen) < 2 {
		t.Fatalf("saw steps %v, want the purge's steps after its mid-Tx bump", seen)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after, err := ReadGeneration(v, "alpha")
	if err != nil {
		t.Fatalf("ReadGeneration after Commit: %v", err)
	}
	if after.Epoch == before.Epoch || after.Gen != before.Gen+3 {
		t.Fatalf("counter %v -> %v, want a new epoch and Gen+3 (pre-write, mid-Tx, finishing)", before, after)
	}
	if n := len(strings.Fields(rawCounter(t, v, "alpha"))); n != 2 {
		t.Fatalf("raw counter after Commit has %d fields, want the flag cleared", n)
	}
}

// TestReleaseOfADestructiveTxClearsTheFlag: Release, like Commit, runs the
// finishing bump.
func TestReleaseOfADestructiveTxClearsTheFlag(t *testing.T) {
	v := newVault(t)
	mixedOwnerStore(t, v)
	tx, err := Lock(context.Background(), v, "alpha", NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.beginDestructive(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGeneration(v, "alpha"); !errors.Is(err, ErrWriteInFlight) {
		t.Fatalf("ReadGeneration after beginDestructive err = %v, want ErrWriteInFlight", err)
	}
	if err := tx.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadGeneration(v, "alpha"); err != nil {
		t.Fatalf("ReadGeneration after Release: %v", err)
	}
}

// TestTheCounterFlagGrammar: a third field "w" is the flag; any other third
// field, or a fourth, is malformed; the two-field form is unchanged.
func TestTheCounterFlagGrammar(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		text     string
		inFlight bool
		bad      bool
	}{
		{"3 77\n", false, false},
		{"3 77 w\n", true, false},
		{"3 77 x\n", false, true},
		{"3 77 w w\n", false, true},
		{"3\n", false, true},
	}
	for _, c := range cases {
		p := filepath.Join(dir, "g")
		if err := os.WriteFile(p, []byte(c.text), 0o644); err != nil {
			t.Fatal(err)
		}
		g, fl, err := readGenRaw(p)
		if c.bad {
			if !errors.Is(err, errMalformedGen) {
				t.Errorf("%q: err = %v, want malformed", c.text, err)
			}
			continue
		}
		if err != nil || fl != c.inFlight || g != (Gen{Gen: 3, Epoch: 77}) {
			t.Errorf("%q: got %v flag %v err %v", c.text, g, fl, err)
		}
	}
}
