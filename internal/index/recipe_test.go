// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package index

import "testing"

// Sum is canonical: several custom rooms, each with several keywords, given in
// different orders and as fresh maps every time, give one Sum. A Sum that ranged
// over the map would differ from call to call, and chunks.fingerprint would flap.
func TestRecipeSumIsCanonicalOverCustomRooms(t *testing.T) {
	sums := map[string]bool{}
	for i := range 200 {
		kws := map[string][]string{}
		rooms := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
		for j := range rooms {
			r := rooms[(i+j)%len(rooms)]
			if i%2 == 0 {
				kws[r] = []string{"k1-" + r, "k2-" + r, "k3-" + r}
			} else {
				kws[r] = []string{"k3-" + r, "k1-" + r, "k2-" + r}
			}
		}
		r := ChunkRecipe{IndexerVersion: IndexerVersion, Transcript: TranscriptRecipe{CustomRoomKeywords: kws}}
		sums[r.Sum()] = true
	}
	if len(sums) != 1 {
		t.Fatalf("one recipe gave %d different Sums over 200 builds, want 1", len(sums))
	}
}
