// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package palace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/chunk"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Two override-only rooms with identical keywords tie on every text that matches
// them. scoreRooms gives a tie to the earlier entry, so the merge order decides
// the room, and Digest hashes that order. Merged in map order (HEAD), both rooms
// win across 200 builds and the digest varies; merged in sorted key order, alpha
// always wins and there is one digest.
func TestRoomClassifierIsDeterministic(t *testing.T) {
	overrides := map[string]WeightedOverride{
		"beta":  {High: []string{"zebrafish"}},
		"alpha": {High: []string{"zebrafish"}},
		"gamma": {High: []string{"quokka"}},
		"delta": {High: []string{"quokka"}},
	}
	rooms := map[string]int{}
	digests := map[string]bool{}
	for range 200 {
		rc := NewRoomClassifier(overrides, 0)
		rooms[rc.Classify("the zebrafish and the quokka", "", nil)]++
		digests[rc.Digest()] = true
	}
	if len(rooms) != 1 || rooms["alpha"] != 200 {
		t.Errorf("a two-way tie classified as %v over 200 builds, want alpha every time", rooms)
	}
	if len(digests) != 1 {
		t.Errorf("200 builds from one config gave %d digests, want 1", len(digests))
	}
}

// The digest covers the overrides and the minimum score, and is equal for equal
// configs.
func TestRoomClassifierDigest(t *testing.T) {
	base := NewRoomClassifier(nil, 0).Digest()
	if got := NewRoomClassifier(nil, 0).Digest(); got != base {
		t.Fatalf("two default classifiers: %s and %s", base, got)
	}
	if got := (&RoomClassifier{}).Digest(); got != base {
		t.Errorf("the zero classifier classifies with the defaults, so it must digest like them")
	}
	if NewRoomClassifier(map[string]WeightedOverride{"testing": {Low: []string{"pytest"}}}, 0).Digest() == base {
		t.Error("an override on an existing room left the digest unchanged")
	}
	if NewRoomClassifier(map[string]WeightedOverride{"newroom": {High: []string{"x"}}}, 0).Digest() == base {
		t.Error("an override-only room left the digest unchanged")
	}
	if NewRoomClassifier(nil, 0.9).Digest() == base {
		t.Error("a different minimum score left the digest unchanged")
	}
}

// Normalized is exactly the clamp Chunk applies, so hashing it hashes what the
// chunker uses.
func TestNormalizedIsWhatChunkUses(t *testing.T) {
	text := ""
	for i := range 400 {
		text += "Sentence number " + string(rune('a'+i%26)) + " says a little something. "
	}
	for _, c := range []chunk.ChunkConfig{{}, {MaxChars: -1, Overlap: -1}, {MaxChars: 100, Overlap: 100}, {MaxChars: 800, Overlap: 900}, {MaxChars: 300, Overlap: 20}} {
		a, b := chunk.Chunk(text, c), chunk.Chunk(text, c.Normalized())
		if len(a) != len(b) {
			t.Fatalf("config %+v: %d chunks raw, %d normalised", c, len(a), len(b))
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("config %+v: chunk %d differs", c, i)
			}
		}
	}
}

// Each part of the recipe covers exactly what its chunker reads.
func TestRecipePartsCoverTheirInputs(t *testing.T) {
	base := storage.Config{}
	sum := func(cfg storage.Config) string { return indexingFromConfig(cfg).Recipe.Sum() }
	ref := sum(base)

	changes := map[string]storage.Config{
		"chunk max chars":    {ChunkMaxChars: 500},
		"chunk overlap":      {ChunkOverlap: 50},
		"custom keyword":     {PalaceRoomKeywords: map[string][]string{"r": {"k"}}},
		"scoring override":   {PalaceScoringOverrides: map[string]storage.ScoringRoomOverride{"testing": {Low: []string{"pytest"}}}},
		"minimum room score": {PalaceMinScore: 0.9},
	}
	for what, cfg := range changes {
		if sum(cfg) == ref {
			t.Errorf("%s left Sum() unchanged", what)
		}
	}
	// Raw zeros and the explicit defaults chunk identically, and so does an
	// overlap the chunker clamps to the same value.
	if sum(storage.Config{ChunkMaxChars: 800, ChunkOverlap: 100}) != ref {
		t.Error("explicit defaults 800/100 differ from raw zeros")
	}
	if sum(storage.Config{ChunkMaxChars: 400, ChunkOverlap: 400}) != sum(storage.Config{ChunkMaxChars: 400, ChunkOverlap: 100}) {
		t.Error("an overlap clamped to max/4 differs from the same overlap given explicitly")
	}
	// The iteration and note chunkers ignore [chunker] config.
	r := indexingFromConfig(storage.Config{ChunkMaxChars: 500, ChunkOverlap: 50}).Recipe
	def := chunk.DefaultChunkConfig().Normalized()
	if r.Iteration.MaxChars != def.MaxChars || r.Iteration.Overlap != def.Overlap || r.Note != r.Iteration {
		t.Errorf("iteration/note parts = %+v / %+v, want the compiled default %+v", r.Iteration, r.Note, def)
	}
	// The indexer and extractor versions are in it.
	r0 := indexingFromConfig(base).Recipe
	r0.IndexerVersion++
	if r0.Sum() == ref {
		t.Error("IndexerVersion left Sum() unchanged")
	}
	r0 = indexingFromConfig(base).Recipe
	r0.Transcript.ExtractorVersion++
	if r0.Sum() == ref {
		t.Error("ExtractorVersion left Sum() unchanged")
	}
}

// The recipe and the classifier come from the project's own resolved config, so a
// host-local project layer reaches both.
func TestProjectIndexingReadsThePerProjectLayer(t *testing.T) {
	root := t.TempDir()
	xdg := filepath.Join(root, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	v := storage.NewVault(filepath.Join(root, "vault"))
	hostPath, err := storage.HostProjectConfigPath("aproj")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostPath, []byte("[palace.scoring]\nmin_score = 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := ProjectIndexing(v, "aproj")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ProjectIndexing(v, "bproj")
	if err != nil {
		t.Fatal(err)
	}
	if a.Recipe.Sum() == b.Recipe.Sum() {
		t.Error("a per-project min_score did not change that project's recipe")
	}
	// "fixture" is a 1.0 keyword of testing: it classifies under the default
	// minimum and not under 5.
	if got := b.Classifier.Classify("a fixture", "", nil); got != "testing" {
		t.Fatalf("default project classified %q, want testing", got)
	}
	if got := a.Classifier.Classify("a fixture", "", nil); got != "general" {
		t.Errorf("project with min_score 5 classified %q, want general: its classifier ignored its own config", got)
	}
}

// The digest covers each keyword's weight: moving an override keyword from High
// to Low changes how the classifier scores, so it must change the digest.
func TestRoomClassifierDigestCoversWeights(t *testing.T) {
	high := NewRoomClassifier(map[string]WeightedOverride{"testing": {High: []string{"pytest"}}}, 0).Digest()
	low := NewRoomClassifier(map[string]WeightedOverride{"testing": {Low: []string{"pytest"}}}, 0).Digest()
	if high == low {
		t.Fatal("moving an override keyword from High to Low left the digest unchanged")
	}
}
