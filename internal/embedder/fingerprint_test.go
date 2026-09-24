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
