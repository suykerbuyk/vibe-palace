// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"context"
	"testing"
)

// flushingVW records Puts and Flushes.
type flushingVW struct {
	*recordingVW
	flushes int
}

func (f *flushingVW) Flush() error {
	f.flushes++
	return nil
}

// PutVectors is the search engine's vector-only commit: it writes through the
// writer, flushes it once per batch, moves the counter's gen and not its epoch
// (an append), and is refused once the Tx is finished. Held and Project
// report the Tx as the embed cache's writer checks it.
func TestPutVectorsIsAnAppendUnderAHeldTx(t *testing.T) {
	v := newVault(t)
	ctx := context.Background()
	tx, err := Lock(ctx, v, "alpha", NoTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if !tx.Held() || tx.Project() != "alpha" {
		t.Fatalf("Held=%v Project=%q, want true and alpha", tx.Held(), tx.Project())
	}
	before := tx.Generation()
	w := &flushingVW{recordingVW: newRecordingVW(nil)}
	if err := tx.PutVectors(w, map[string][]float32{"b": {2}, "a": {1}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after := tx.Generation()
	if after.Gen != before.Gen+1 || after.Epoch != before.Epoch {
		t.Fatalf("counter %v -> %v; want gen+1 and the same epoch", before, after)
	}
	if len(w.puts) != 2 || w.flushes != 1 {
		t.Fatalf("puts %v, flushes %d; want 2 puts and one flush", w.puts, w.flushes)
	}
	if tx.Held() {
		t.Fatal("Held after Commit")
	}
	if err := tx.PutVectors(w, map[string][]float32{"c": {3}}); err == nil {
		t.Fatal("PutVectors on a committed Tx succeeded")
	}
	if len(w.puts) != 2 {
		t.Fatalf("a refused PutVectors wrote: %v", w.puts)
	}
}
