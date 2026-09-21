// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"strings"
	"testing"
)

// operatorPastedBlock is commented text an operator pastes into the host-local
// file: notes and disabled examples the writer never parses.
const operatorPastedBlock = `
# operator notes — pasted by hand, never parsed by the writer.
# Scoring for this project was last reviewed on this host by hand.
#
# [palace.scoring.rooms.graphics]
# high = ["segfault"]
#
# [search]
# default_limit = 25
`

// seedRealTemplate writes the REAL shipped host-local header, followed by an
// operator-pasted commented block, as a project's host-local config, and returns
// its path. It isolates XDG first, so nothing lands in the read-only fixture.
//
// 🔴 THE SPECIMEN IS THE SHIPPED HEADER, NOT A HAND-WRITTEN FIXTURE, and that
// is the whole point of this file. renderConfigMetaHeader(MetaKindHostProject)
// is byte for byte what WriteHostScoringConfig seeds a new file with, so it is
// what every host-local config is born from. A fixture invented here would only
// ever contain what the test author remembered to put in it — and the defect
// these tests exist for is a writer that discarded what it never read, which a
// fixture cannot model because the author would have to think of the thing
// being discarded first. The pasted block adds the other text a real file
// carries: comments the operator wrote.
func seedRealTemplate(t *testing.T, project string) (*Vault, string) {
	t.Helper()
	v, cfgPath := hostLocalEnv(t, project, "")
	writeFileAt(t, cfgPath, renderConfigMetaHeader(MetaKindHostProject)+operatorPastedBlock)
	return v, cfgPath
}

func writeRoundTrip(t *testing.T, v *Vault, rooms map[string]ScoringRoomOverride) {
	t.Helper()
	if _, _, err := v.WriteHostScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("WriteHostScoringConfig: %v", err)
	}
}

// TestWriteHostScoringConfig_RoundTripPreservesTheRealTemplate is the regression this
// change exists for, and it is a ROUND TRIP rather than an expected-bytes
// assertion: it asserts that every line of the ORIGINAL survives the write,
// whatever those lines happen to be.
//
// The distinction is load-bearing. The previous implementation decoded the file
// into a map[string]any and re-encoded the whole map, and a map has nowhere to
// hold a comment — so it deleted the file header, the "managed by vp" warning,
// the `# kind` schema key, the commented [search], [chunker], [palace.rooms] and
// [palace.llm] blocks, about 22 lines, to record one learned keyword. A test
// asserting the bytes we EXPECT to write passes against that writer, because the
// expectation is written by the same person who forgot the comments exist.
//
// Break it: restore the old tail —
//
//	var buf bytes.Buffer
//	toml.NewEncoder(&buf).Encode(m)
//	atomicfile.Write(v.Root, cfgPath, buf.Bytes())
//
// Every commented line in the specimen disappears and this test names the first
// one it cannot find.
func TestWriteHostScoringConfig_RoundTripPreservesTheRealTemplate(t *testing.T) {
	v, cfgPath := seedRealTemplate(t, "proj")
	original, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	writeRoundTrip(t, v, map[string]ScoringRoomOverride{
		"debugging": {High: []string{"undefined: strings"}, Low: []string{"local main"}},
	})

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(after)

	// THE ROUND TRIP. The specimen carries no ACTIVE [palace.scoring] section —
	// its scoring example is commented out — so every single line of the original
	// must still be present. No allowance, no exceptions: the assertion is over
	// whatever the specimen contains, not over a list this test maintains.
	for i, line := range strings.Split(strings.TrimRight(string(original), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.Contains(got, line) {
			t.Errorf("line %d of the original did not survive the write:\n  lost: %q\n\nfile after write:\n%s",
				i+1, line, got)
			return
		}
	}

	// And the write actually did its job.
	for _, want := range []string{"[palace.scoring.rooms.debugging]", "undefined: strings", "local main"} {
		if !strings.Contains(got, want) {
			t.Errorf("merged scoring missing %q:\n%s", want, got)
		}
	}

	// The result is still valid TOML that resolves through the real loader — a
	// splice that preserved every byte but produced an unparseable file would
	// satisfy the loop above.
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("config is no longer loadable after the write: %v\n%s", err, got)
	}
	if ov, ok := cfg.PalaceScoringOverrides["debugging"]; !ok || len(ov.High) != 1 || ov.High[0] != "undefined: strings" {
		t.Errorf("LoadConfig did not resolve the merged override: %+v", cfg.PalaceScoringOverrides)
	}
}

// TestWriteHostScoringConfig_RoundTripReplacesOnlyTheScoringSection covers the case
// the specimen cannot: a file that ALREADY has an active scoring block. The old
// block must be replaced rather than appended beside, and every line outside it
// must survive.
//
// Break it: make spliceScoringSections append unconditionally instead of
// dropping the matched ranges. The file then carries two
// [palace.scoring.rooms.debugging] tables and TOML decoding fails outright.
func TestWriteHostScoringConfig_RoundTripReplacesOnlyTheScoringSection(t *testing.T) {
	v, cfgPath := seedRealTemplate(t, "proj")

	// First write creates the active scoring section.
	writeRoundTrip(t, v, map[string]ScoringRoomOverride{
		"debugging": {High: []string{"first"}},
	})
	// Second write merges a different room.
	writeRoundTrip(t, v, map[string]ScoringRoomOverride{
		"testing": {Medium: []string{"second"}},
	})

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(after)

	// The header's and the operator's comments are still there after TWO writes.
	for _, want := range []string{
		"# Host-local per-project config for vibe-palace.",
		`kind = "host-project"`,
		"# operator notes — pasted by hand, never parsed by the writer.",
		"# [search]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("a second write lost %q:\n%s", want, got)
		}
	}
	// Both rooms survive the merge, and neither section is duplicated.
	if n := strings.Count(got, "[palace.scoring.rooms.debugging]"); n != 1 {
		t.Errorf("debugging section appears %d times, want exactly 1:\n%s", n, got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("merge lost a keyword:\n%s", got)
	}
	if _, err := v.LoadConfig("proj"); err != nil {
		t.Fatalf("file is not valid TOML after two writes: %v\n%s", err, got)
	}
}

// TestWriteHostScoringConfig_NoOpLeavesTheFileByteIdentical: a write whose merged
// result equals what is on disk must not touch the file at all. An mtime bump
// for no change is churn a human then has to explain; this defect was first
// noticed as a (then vault-resident) config showing up as blocking vault dirt.
//
// Break it: drop the `merged == string(existing)` guard before atomicfile.Write.
// The bytes stay equal but the file is rewritten, and the mtime assertion fails.
func TestWriteHostScoringConfig_NoOpLeavesTheFileByteIdentical(t *testing.T) {
	v, cfgPath := seedRealTemplate(t, "proj")
	rooms := map[string]ScoringRoomOverride{"debugging": {High: []string{"once"}}}

	writeRoundTrip(t, v, rooms)
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st1, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	writeRoundTrip(t, v, rooms)
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Errorf("an idempotent write changed the bytes:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Errorf("a no-op write rewrote the file (mtime %v -> %v) — that is churn for no change",
			st1.ModTime(), st2.ModTime())
	}
}
