// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"regexp"
	"strings"
	"testing"
)

func TestBlockContentHashStable(t *testing.T) {
	// Content hash must be deterministic and dependent on blockBody; two
	// calls return the same value, and it's a 7-hex-char prefix.
	h1 := blockContentHash()
	h2 := blockContentHash()
	if h1 != h2 {
		t.Fatalf("hash non-deterministic: %q vs %q", h1, h2)
	}
	if !regexp.MustCompile(`^[0-9a-f]{7}$`).MatchString(h1) {
		t.Errorf("hash %q does not match 7-hex-char format", h1)
	}
}

func TestBlockOpenDelimFormat(t *testing.T) {
	d := blockOpenDelim()
	want := regexp.MustCompile(`^<!-- vibe-palace:begin v=\d+ sha=[0-9a-f]{7} -->$`)
	if !want.MatchString(d) {
		t.Errorf("open delim %q does not match expected format", d)
	}
}

func TestManagedBlockStructure(t *testing.T) {
	b := managedBlock()
	if !strings.HasPrefix(b, "<!-- vibe-palace:begin ") {
		t.Errorf("block missing opening delim: %q", b)
	}
	if !strings.HasSuffix(b, blockCloseDelim) {
		t.Errorf("block missing closing delim: %q", b)
	}
	if !strings.Contains(b, "vp_bootstrap_context") {
		t.Error("block body missing bootstrap_context reference")
	}
	if !strings.Contains(b, "vpc-<name>") {
		t.Error("block body missing vpc-<name> pattern")
	}
	if !strings.Contains(b, "vp_list_commands") {
		t.Error("block body missing list_commands reference")
	}
}
