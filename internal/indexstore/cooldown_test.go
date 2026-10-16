// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"os"
	"testing"
	"time"
)

// TestRunCompletedRoundTrip: a stamped marker reads back the same instant.
func TestRunCompletedRoundTrip(t *testing.T) {
	v := newVault(t)
	want := time.Date(2026, 10, 16, 9, 8, 24, 0, time.UTC)
	if err := WriteRunCompleted(v, want); err != nil {
		t.Fatalf("WriteRunCompleted: %v", err)
	}
	got, ok, err := ReadRunCompleted(v)
	if err != nil || !ok {
		t.Fatalf("ReadRunCompleted: ok=%v err=%v", ok, err)
	}
	if !got.Equal(want) {
		t.Fatalf("round trip: got %v, want %v", got, want)
	}
}

// TestRunCompletedFailOpen: a missing, empty, or torn marker reads back
// (zero, false, nil) — never an error that a caller could invert into a defer.
func TestRunCompletedFailOpen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bytes []byte
		none  bool
	}{
		{name: "missing", none: true},
		{name: "empty", bytes: nil},
		{name: "whitespace", bytes: []byte("   \n")},
		{name: "torn-rfc3339", bytes: []byte("2026-10-16T09:08:2")},
		{name: "garbage", bytes: []byte("not a timestamp at all")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newVault(t)
			if !tc.none {
				if err := os.MkdirAll(v.IndexLocksDir(), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(v.IndexRunCompletedPath(), tc.bytes, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, ok, err := ReadRunCompleted(v)
			if err != nil {
				t.Fatalf("fail-open must return a nil error, got %v", err)
			}
			if ok {
				t.Fatalf("a %s marker must read back ok=false, got %v", tc.name, got)
			}
			// And the shared predicate must therefore report NOT active, so a run
			// with a bad/missing marker proceeds.
			if RunCooldownActive(v, time.Now(), time.Hour) {
				t.Fatalf("a %s marker must not make the cooldown active", tc.name)
			}
		})
	}
}

// TestRunCooldownActive: active within the window, inactive outside it, and
// disabled when cooldown <= 0.
func TestRunCooldownActive(t *testing.T) {
	v := newVault(t)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if err := WriteRunCompleted(v, now.Add(-5*time.Minute)); err != nil {
		t.Fatalf("WriteRunCompleted: %v", err)
	}
	if !RunCooldownActive(v, now, 10*time.Minute) {
		t.Fatal("a run 5m ago must be within a 10m cooldown")
	}
	if RunCooldownActive(v, now, 3*time.Minute) {
		t.Fatal("a run 5m ago must be outside a 3m cooldown")
	}
	if RunCooldownActive(v, now, 0) {
		t.Fatal("a zero cooldown disables the throttle")
	}
	if RunCooldownActive(v, now, -1) {
		t.Fatal("a negative cooldown disables the throttle")
	}
}
