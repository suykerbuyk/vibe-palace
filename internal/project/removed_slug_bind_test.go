// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package project

import (
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// The redirect names the bind that will actually succeed: a project removed
// with no departure record can only be bound with --new (moved mode needs a
// record), and a moved record naming no destination needs --allow-unlabelled.
// A labelled move needs neither.
func TestRedirectNamesTheBindThatWillSucceed(t *testing.T) {
	removed := gitVaultWithHistory(t, "gone", "rm")
	if err := RefuseDeparted(removed, "gone"); err == nil || !strings.Contains(err.Error(), "vp config bind gone --vault <path> --new") {
		t.Errorf("removed-not-recorded redirect = %v, want the --new bind", err)
	}

	vault := t.TempDir()
	writeDeparture(t, vault, departure.Record{Slug: "bare", Kind: departure.MovedToVault})
	writeDeparture(t, vault, departure.Record{Slug: "labelled", Kind: departure.MovedToVault, To: "git@example.com:me/q.git"})
	if err := RefuseDeparted(vault, "bare"); err == nil || !strings.Contains(err.Error(), "--allow-unlabelled") {
		t.Errorf("unlabelled redirect = %v, want --allow-unlabelled", err)
	}
	err := RefuseDeparted(vault, "labelled")
	if err == nil || strings.Contains(err.Error(), "--allow-unlabelled") || strings.Contains(err.Error(), "--new") {
		t.Errorf("labelled redirect = %v, want the plain bind", err)
	}
}
