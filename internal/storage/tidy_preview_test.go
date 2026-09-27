// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"slices"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// D2 (split-and-sweep-reporting-defects-found-by-rehearsal-a2): the tidy dry
// run must reach the same verdict as a real tidy on the same state. A real
// tidy refuses (the U1 commit guard) when it has something to sweep while a
// departure record is uncommitted, and is a no-op when it has nothing to sweep;
// the dry run said "would sweep" and exited 0 in the first case.

func pendingRecordVault(t *testing.T, sweepable bool) string {
	t.Helper()
	dir := initTestRepo(t)
	if sweepable {
		writeFile(t, dir, "Projects/vibe-palace/sessions/2026-09-27.md", "session\n")
	}
	b, err := (departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: "q"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, departure.RelPath("alpha"), string(b))
	return dir
}

func TestTidyPreviewAgreesWithTidyVaultOnTheCommitGuard(t *testing.T) {
	t.Run("something to sweep: both refuse", func(t *testing.T) {
		dir := pendingRecordVault(t, true)
		head := gitRun(t, dir, "rev-parse", "HEAD")

		res, perr := TidyPreview(dir)
		if res == nil {
			t.Fatalf("preview returned no result: %v", perr)
		}
		if !hasPath(res.Swept, "Projects/vibe-palace/sessions/2026-09-27.md") {
			t.Errorf("preview must still classify: Swept = %q", res.Swept)
		}
		_, rerr := TidyVault(dir, false)

		var pp, rp *PendingDepartureError
		if !errors.As(perr, &pp) || !errors.Is(perr, ErrPendingDeparture) {
			t.Fatalf("dry-run verdict = %v, want the pending-departure refusal", perr)
		}
		if !errors.As(rerr, &rp) {
			t.Fatalf("test premise: a real tidy refuses, got %v", rerr)
		}
		if !slices.Equal(pp.Records, rp.Records) {
			t.Errorf("records differ: dry run %q, real %q", pp.Records, rp.Records)
		}
		if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
			t.Errorf("a refused tidy moved HEAD")
		}
	})
	t.Run("nothing to sweep: neither refuses", func(t *testing.T) {
		dir := pendingRecordVault(t, false)
		res, perr := TidyPreview(dir)
		if perr != nil || res == nil || len(res.Swept) != 0 {
			t.Fatalf("preview = %v, %v; want a clean no-op verdict", res, perr)
		}
		if _, rerr := TidyVault(dir, false); rerr != nil {
			t.Fatalf("test premise: an empty sweep is a no-op, got %v", rerr)
		}
	})
}
