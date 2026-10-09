// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"fmt"
	"strings"
	"testing"
)

// T6: every input to the regime is in the fingerprint, so changing any of them
// changes it, and the same inputs always give the same line.
func TestFingerprintNamesEveryRegimeInput(t *testing.T) {
	base := Fingerprint("sentence-transformers/all-MiniLM-L6-v2", 256)
	if base != Fingerprint("sentence-transformers/all-MiniLM-L6-v2", 256) {
		t.Fatal("Fingerprint is not deterministic")
	}
	for _, want := range []string{
		fmt.Sprintf("behaviour=%d", BehaviourVersion),
		"model=sentence-transformers/all-MiniLM-L6-v2",
		"max_seq_len=256",
	} {
		if !strings.Contains(base, want) {
			t.Errorf("fingerprint %q lacks %q", base, want)
		}
	}
	if Fingerprint("other-model", 256) == base {
		t.Error("a model change must change the fingerprint")
	}
	if Fingerprint("sentence-transformers/all-MiniLM-L6-v2", 512) == base {
		t.Error("a max_seq_len change must change the fingerprint")
	}
	if BehaviourVersion != 2 {
		t.Errorf("BehaviourVersion = %d; token-level truncation ships 2", BehaviourVersion)
	}
}

// TestFingerprintGoStringIsByteIdentical is the B2 regression guard: the
// pure-Go/default regime string is pinned byte-for-byte to today's output. The
// embed cache marks a project stale on any fingerprint-string change, so a
// single changed byte here forces every default (zero-CGO) host to re-embed its
// whole corpus once at the live pure-Go rate — a multi-day re-embed triggered by
// a routine binary upgrade. If this test fails, do NOT update the literal to
// match; the change to the go string is the bug (see ADR-015, ADR-014 dec. 3).
func TestFingerprintGoStringIsByteIdentical(t *testing.T) {
	const want = "vp-embed behaviour=2 model=sentence-transformers/all-MiniLM-L6-v2 max_seq_len=256"
	if got := Fingerprint("sentence-transformers/all-MiniLM-L6-v2", 256); got != want {
		t.Fatalf("Fingerprint go string drifted:\n got %q\nwant %q\n(a changed go string forces a multi-day re-embed on every default host)", got, want)
	}
	// The explicit go backend, and the empty/unknown fallback, must equal the
	// bare Fingerprint — only ORT differs.
	if got := FingerprintBackend(BackendGo, "sentence-transformers/all-MiniLM-L6-v2", 256); got != want {
		t.Fatalf("FingerprintBackend(go) = %q, want byte-identical %q", got, want)
	}
	if got := FingerprintBackend("", "sentence-transformers/all-MiniLM-L6-v2", 256); got != want {
		t.Fatalf("FingerprintBackend(\"\") = %q, want it to default to the go string %q", got, want)
	}
}

// TestFingerprintORTAppendsBackendToken pins the ORT regime's distinct
// fingerprint namespace: it is the go string plus a trailing " backend=ort", so
// ORT-produced vectors can never be accepted into a pure-Go cache (or vice
// versa). The go string is its exact prefix, confirming only a suffix is added.
func TestFingerprintORTAppendsBackendToken(t *testing.T) {
	goFP := FingerprintBackend(BackendGo, "sentence-transformers/all-MiniLM-L6-v2", 256)
	ortFP := FingerprintBackend(BackendORT, "sentence-transformers/all-MiniLM-L6-v2", 256)
	if ortFP == goFP {
		t.Fatal("ORT fingerprint must differ from the go fingerprint")
	}
	if want := goFP + " backend=ort"; ortFP != want {
		t.Fatalf("ORT fingerprint = %q, want %q", ortFP, want)
	}
	if !strings.HasPrefix(ortFP, goFP) {
		t.Fatalf("ORT fingerprint %q must start with the go string %q (suffix-only distinction)", ortFP, goFP)
	}
}
