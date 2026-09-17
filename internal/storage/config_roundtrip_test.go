// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedRealTemplate writes the REAL shipped vault-project template as a project's
// config and returns its path.
//
// 🔴 THE SPECIMEN IS THE SHIPPED TEMPLATE, NOT A HAND-WRITTEN FIXTURE, and that
// is the whole point of this file. VaultProjectTemplateContent() is byte for byte
// what WriteVaultProjectConfig writes, so it is what every project config in a
// real vault is born from. A fixture invented here would only ever contain what
// the test author remembered to put in it — and the defect these tests exist for
// is a writer that discarded what it never read, which a fixture cannot model
// because the author would have to think of the thing being discarded first.
func seedRealTemplate(t *testing.T, v *Vault, project string) string {
	t.Helper()
	cfgPath, err := v.ProjectConfigFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(VaultProjectTemplateContent()), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// TestWriteScoringConfig_RoundTripPreservesTheRealTemplate is the regression this
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
// Every commented line in the template disappears and this test names the first
// one it cannot find.
func TestWriteScoringConfig_RoundTripPreservesTheRealTemplate(t *testing.T) {
	v := NewVault(t.TempDir())
	cfgPath := seedRealTemplate(t, v, "proj")
	original, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := v.WriteScoringConfig("proj", map[string]ScoringRoomOverride{
		"debugging": {High: []string{"undefined: strings"}, Low: []string{"local main"}},
	}, 0); err != nil {
		t.Fatalf("WriteScoringConfig: %v", err)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(after)

	// THE ROUND TRIP. The template carries no ACTIVE [palace.scoring] section —
	// its scoring example is commented out — so every single line of the original
	// must still be present. No allowance, no exceptions: the assertion is over
	// whatever the template contains, not over a list this test maintains.
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

// TestWriteScoringConfig_RoundTripReplacesOnlyTheScoringSection covers the case
// the template cannot: a file that ALREADY has an active scoring block. The old
// block must be replaced rather than appended beside, and every line outside it
// must survive.
//
// Break it: make spliceScoringSections append unconditionally instead of
// dropping the matched ranges. The file then carries two
// [palace.scoring.rooms.debugging] tables and TOML decoding fails outright.
func TestWriteScoringConfig_RoundTripReplacesOnlyTheScoringSection(t *testing.T) {
	v := NewVault(t.TempDir())
	cfgPath := seedRealTemplate(t, v, "proj")

	// First write creates the active scoring section.
	if err := v.WriteScoringConfig("proj", map[string]ScoringRoomOverride{
		"debugging": {High: []string{"first"}},
	}, 0); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Second write merges a different room.
	if err := v.WriteScoringConfig("proj", map[string]ScoringRoomOverride{
		"testing": {Medium: []string{"second"}},
	}, 0); err != nil {
		t.Fatalf("second write: %v", err)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(after)

	// The template's own comments are still there after TWO writes.
	for _, want := range []string{
		"# Per-project overrides for this vault project.",
		"# kind identifies this schema — do not change.",
		"# [palace.llm]",
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

// TestWriteScoringConfig_NoOpLeavesTheFileByteIdentical: a write whose merged
// result equals what is on disk must not touch the file at all. On a tracked
// vault file an mtime bump is dirt a human then has to explain, and the whole
// reason this defect was noticed is a config showing up as blocking vault dirt.
//
// Break it: drop the `merged == string(existing)` guard before atomicfile.Write.
// The bytes stay equal but the file is rewritten, and the mtime assertion fails.
func TestWriteScoringConfig_NoOpLeavesTheFileByteIdentical(t *testing.T) {
	v := NewVault(t.TempDir())
	cfgPath := seedRealTemplate(t, v, "proj")
	rooms := map[string]ScoringRoomOverride{"debugging": {High: []string{"once"}}}

	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("first write: %v", err)
	}
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st1, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := v.WriteScoringConfig("proj", rooms, 0); err != nil {
		t.Fatalf("second write: %v", err)
	}
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
		t.Errorf("a no-op write rewrote the file (mtime %v -> %v) — that is vault dirt for no change",
			st1.ModTime(), st2.ModTime())
	}
}
