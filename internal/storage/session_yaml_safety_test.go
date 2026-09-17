// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 🔴 A FIXTURE TEST COULD NOT HAVE CAUGHT THE DEFECT THESE TESTS EXIST FOR, and
// that is the whole argument for their shape.
//
// The bug was that yaml.Marshal wrote bytes yaml.Unmarshal refuses. A fixture
// pins the bytes we BELIEVED we would write — a fixture asserting the emitted
// `- |4-` would have PASSED while shipping a two-project outage. Only asking the
// real reader to read the real writer's output can fail on this, so the
// round-trip property is the centre of this file and the table is its seed
// corpus, shared with the fuzz target below.

// roundTripCorpus is every string shape measured against yaml.v3 v3.0.1, plus
// the live specimen. The three marked unsafe are the ones that took down
// Projects/vibe-palace and Projects/quantum-ng.
//
// Re-derive the failing members of this corpus against a bare yaml.v3 — they
// are a property of the LIBRARY, not of this package, so they can change under
// us on a dependency bump without any of our code moving:
//
//	yaml.Marshal(struct{ D []string }{D: []string{s}}) then yaml.Unmarshal it back
var roundTripCorpus = []struct {
	name   string
	s      string
	unsafe bool // trips yaml.v3 as a SEQUENCE item before normalization
}{
	{"plain", "a normal decision", false},
	{"multiline", "line one\nline two", false},
	{"single line leading space", " starts with a space", false},
	{"later line indented", "first line\n  indented second", false},
	{"later line tabbed", "first\n\tsecond", false},
	{"blank line inside", "a\n\nb", false},
	{"crlf", "a\r\nb", false},
	{"trailing spaces", "a   \nb", false},
	{"trailing newline", "a\nb\n", true}, // parses, but comes back SHORT — see normalizeYAMLScalar
	{"empty", "", false},

	// The three trigger shapes.
	{"first line leading space", " a\nb", true},
	{"first line leading tab", "\ta\nb", true},
	{"first line empty", "\nb", true},

	// The live specimen, verbatim. Its decisions entry began with a newline;
	// the mirrored "## Decisions" body section in the real note shows the "- "
	// alone with the content on the following line.
	{"live specimen", "\n<parameter name=\"files_changed\">internal/sourceaudit/git_env_funnel.go, git_env_funnel_test.go (implemented via subagent, verified independently)", true},
}

// TestSessionFileRoundTrips is the property test, and it is the one that would
// have caught this.
//
// Every corpus member goes through the REAL writer (marshalSessionFile) and the
// REAL reader (ParseFrontmatter), in every field position that carries
// caller-supplied prose — because the trigger is position-dependent: a leading
// space is fatal in a sequence item and harmless in a top-level scalar, so
// testing one position would certify the other falsely.
//
// Break: delete the normalizeSessionMeta call in marshalSessionFile. The four
// `unsafe: true` rows go red in the sequence positions.
func TestSessionFileRoundTrips(t *testing.T) {
	for _, tc := range roundTripCorpus {
		t.Run(tc.name, func(t *testing.T) {
			for _, pos := range []struct {
				where string
				meta  SessionMeta
			}{
				{"decisions", SessionMeta{ID: "x", Decisions: []string{tc.s}}},
				{"open_threads", SessionMeta{ID: "x", OpenThreads: []string{tc.s}}},
				{"files_changed", SessionMeta{ID: "x", FilesChanged: []string{tc.s}}},
				{"summary", SessionMeta{ID: "x", Summary: tc.s}},
				{"title", SessionMeta{ID: "x", Title: tc.s}},
			} {
				data, _, err := marshalSessionFile(pos.meta, "## Summary\n\nbody\n")
				if err != nil {
					t.Fatalf("%s: marshalSessionFile refused a string it should have repaired: %v\n"+
						"input %q", pos.where, err, tc.s)
				}
				back, _, perr := ParseFrontmatter(data)
				if perr != nil {
					t.Fatalf("%s: the writer produced bytes its own reader refuses: %v\n"+
						"input %q\nbytes:\n%s", pos.where, perr, tc.s, data)
				}
				// 🔴 PARSING IS NOT ENOUGH. A string ending in "\n" parses back
				// SHORT, and checking only that the bytes parsed is exactly how
				// the first survey of this bug missed that shape. What we wrote
				// must be what we read.
				want, _ := normalizeYAMLScalar(tc.s)
				if got := firstProseField(back, pos.where); got != want {
					t.Fatalf("%s: the value did not survive the round trip.\n  wrote %q\n  read  %q",
						pos.where, want, got)
				}
			}
		})
	}
}

// TestNormalizationPreservesMeaning pins that the repair is minimal.
//
// A repair that satisfied the parser by flattening content would pass
// TestSessionFileRoundTrips while destroying the note, so the round trip alone
// is not enough. Safe strings must survive BYTE-IDENTICAL, and unsafe ones must
// lose only the leading whitespace that is the measured trigger.
//
// Break: change normalizeYAMLScalar to strings.TrimSpace(s) — every "later line
// indented" style row keeps round-tripping and this goes red instead.
func TestNormalizationPreservesMeaning(t *testing.T) {
	for _, tc := range roundTripCorpus {
		got, changed := normalizeYAMLScalar(tc.s)
		if !tc.unsafe {
			if changed || got != tc.s {
				t.Errorf("%s: a string that round-trips fine was modified anyway: %q -> %q",
					tc.name, tc.s, got)
			}
			continue
		}
		if !changed {
			t.Errorf("%s: an unsafe string was left alone: %q", tc.name, tc.s)
			continue
		}
		// Only EDGE whitespace may go. What remains must be a contiguous slice of
		// the original with nothing reordered, reindented or dropped from the
		// middle — a repair that satisfied the parser by flattening interior
		// structure would pass the round trip while destroying the note.
		if !strings.Contains(tc.s, got) {
			t.Errorf("%s: normalization changed more than edge whitespace.\n  in:  %q\n  out: %q",
				tc.name, tc.s, got)
		}
		if strings.TrimSpace(tc.s) != strings.TrimSpace(got) {
			t.Errorf("%s: normalization altered the content itself.\n  in:  %q\n  out: %q",
				tc.name, tc.s, got)
		}
	}
}

// TestMarshalReportsWhatItNormalized pins the visibility requirement.
//
// A silent repair is a silent edit to a historical record, and it is the same
// defect as a silent skip one layer down. The writer must name the field it
// touched, and must say nothing when it touched nothing.
//
// Break: return nil instead of `normalized` from marshalSessionFile.
func TestMarshalReportsWhatItNormalized(t *testing.T) {
	_, clean, err := marshalSessionFile(SessionMeta{ID: "x", Decisions: []string{"fine"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(clean) != 0 {
		t.Errorf("a clean write reported normalization it did not do: %v", clean)
	}

	_, dirty, err := marshalSessionFile(SessionMeta{
		ID:        "x",
		Summary:   "\nleading newline",
		Decisions: []string{"ok", "\nrepaired"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(dirty, " ")
	for _, want := range []string{"summary", "decisions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("normalization repaired %s and did not say so; reported %v", want, dirty)
		}
	}
	// The INDEX matters: "decisions was repaired" sends a reader through the
	// whole list, and entry 0 was fine.
	if !strings.Contains(joined, "decisions[1]") {
		t.Errorf("the report must name WHICH entry was repaired, got %v", dirty)
	}
}

// TestVerifyDetectsAnUnreadableNote tests the GUARD ITSELF, directly.
//
// marshalSessionFile normalizes before it verifies, and normalization currently
// repairs every shape known to trip yaml.v3 — so there is no input string that
// reaches the refusal today, and a test that hunted for one would be asserting
// a library detail rather than our behaviour. What must be pinned is that the
// detector WORKS, because the wiring is what protects against the shape nobody
// has met yet. (The fuzz target below is the other half: if verify were removed
// and normalization missed something, it would find bytes that fail
// ParseFrontmatter with a nil error.)
//
// Break: make verifySessionRoundTrip return nil unconditionally.
func TestVerifyDetectsAnUnreadableNote(t *testing.T) {
	want := SessionMeta{ID: "x", Decisions: []string{"a decision"}}

	// Bytes that do not parse at all — the two live specimens' shape.
	unreadable := []byte("---\ndecisions:\n    - |4-\n      <parameter name=\"x\">y\n---\n")
	if err := verifySessionRoundTrip(unreadable, want); err == nil {
		t.Fatal("verifySessionRoundTrip accepted bytes ParseFrontmatter refuses — the guard is not " +
			"running, and an unreadable note would reach disk to take a project's index down later")
	} else if !strings.Contains(err.Error(), "cannot be read back") {
		t.Errorf("the refusal must say what went wrong in the writer's own terms, got: %v", err)
	}

	// Bytes that parse but carry a DIFFERENT value — the silent-corruption
	// shape, which is the one a parse-only check cannot see.
	drifted := []byte("---\nsession_id: x\ndecisions:\n    - something else\n---\n")
	if err := verifySessionRoundTrip(drifted, want); err == nil {
		t.Fatal("verifySessionRoundTrip accepted a note whose value changed in transit. A round trip " +
			"that only checks the bytes parse is how the trailing-newline shape went unnoticed")
	}
}

// firstProseField reads back whichever caller-supplied field a round-trip case
// wrote, so the assertion can compare values rather than only parse success.
func firstProseField(m SessionMeta, where string) string {
	pick := func(ss []string) string {
		if len(ss) == 0 {
			return ""
		}
		return ss[0]
	}
	switch where {
	case "decisions":
		return pick(m.Decisions)
	case "open_threads":
		return pick(m.OpenThreads)
	case "files_changed":
		return pick(m.FilesChanged)
	case "summary":
		return m.Summary
	case "title":
		return m.Title
	}
	return ""
}

// TestLiveVaultSessionNotesAllRoundTrip is the live-corpus half, and it is here
// because a bug that passes every fixture and dies on the real corpus is this
// project's signature failure.
//
// It matters operationally rather than theoretically: `vp vault tidy` and every
// RewriteSession caller re-marshal notes that are already at rest, so a note
// that cannot be re-emitted is one a maintenance pass would refuse — or, before
// this fix, would quietly rewrite into an unreadable file.
//
// It reads the vault and writes nothing. Notes that cannot be PARSED are
// counted and skipped rather than failed on: two such notes exist today and
// repairing them is the operator's call, deliberately out of scope here.
//
// Break: delete the normalizeSessionMeta call — if any live note carries a
// trigger shape this goes red; if none does, the test says so via t.Log rather
// than passing silently on an empty walk.
func TestLiveVaultSessionNotesAllRoundTrip(t *testing.T) {
	root := liveVaultRoot(t)

	projects, err := os.ReadDir(filepath.Join(root, "Projects"))
	if err != nil {
		t.Skipf("no Projects dir under %s: %v", root, err)
	}

	var scanned, unparseable, remarshaled int
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(root, "Projects", p.Name(), "sessions", "*.md"))
		for _, m := range matches {
			scanned++
			data, rerr := os.ReadFile(m)
			if rerr != nil {
				t.Errorf("read %s: %v", m, rerr)
				continue
			}
			meta, body, perr := ParseFrontmatter(data)
			if perr != nil {
				// Already-broken on disk. Piece 1, the operator's.
				unparseable++
				continue
			}
			out, _, merr := marshalSessionFile(meta, body)
			if merr != nil {
				t.Errorf("a note at rest cannot be re-emitted, so every RewriteSession caller "+
					"(vault tidy, archive link, friction scoring, enrichment) would refuse it:\n"+
					"  %s\n  %v", m, merr)
				continue
			}
			if _, _, verr := ParseFrontmatter(out); verr != nil {
				t.Errorf("re-emitting a note at rest produced bytes the reader refuses — a maintenance "+
					"pass would have written this to disk:\n  %s\n  %v", m, verr)
				continue
			}
			remarshaled++
		}
	}

	if scanned == 0 {
		t.Fatal("no session notes scanned at all — the walk is not seeing the vault, and a passing " +
			"verdict here would be vacuous")
	}
	t.Logf("live corpus: %d note(s) scanned, %d re-marshaled and round-tripped, %d unparseable on disk "+
		"(already-broken; out of scope here)", scanned, remarshaled, unparseable)
}

// liveVaultRoot resolves the host's vault, or skips.
//
// EXACTLY ONE SKIP IS LEGITIMATE: this host has no vault (CI, a fresh clone).
// Anything else must fail, or the live half of this suite silently stops
// running the day an env var is renamed.
func liveVaultRoot(t *testing.T) string {
	t.Helper()
	if root := os.Getenv("VP_LIVE_VAULT"); root != "" {
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("VP_LIVE_VAULT=%q is set but not present: %v", root, err)
		}
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir and no VP_LIVE_VAULT: %v", err)
	}
	root := filepath.Join(home, "vibe-palace-vault")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("no vault configured on this host — the one legitimate skip: %v", err)
	}
	return root
}

// FuzzSessionFileRoundTrip reaches the shapes nobody measured.
//
// The defect was precisely "we wrote bytes we could not read", and the measured
// table above is a list of triggers someone thought to try. Fuzzing is the only
// tool here that finds the next one, and it is cheap because the table is
// already the seed corpus.
//
// 🔴 GATED OUT OF -short, on the skipUnlessFullSuite precedent in
// internal/sourceaudit: `make test` passes -short, so an ungated fuzz target
// would tax every local run. `go test -fuzz=FuzzSessionFileRoundTrip
// ./internal/storage/` runs it deliberately. Seeds still execute as ordinary
// table cases on every non-short run, so the corpus never goes stale.
func FuzzSessionFileRoundTrip(f *testing.F) {
	if testing.Short() {
		f.Skip("skipped under -short: `make test` runs -short and a fuzz target there taxes every " +
			"local run. Run it deliberately with `go test -fuzz=FuzzSessionFileRoundTrip ./internal/storage/`.")
	}
	for _, tc := range roundTripCorpus {
		f.Add(tc.s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		meta := SessionMeta{ID: "x", Summary: s, Decisions: []string{s}, OpenThreads: []string{s}}
		data, _, err := marshalSessionFile(meta, "")
		if err != nil {
			// A loud refusal is a correct outcome: the guard caught something
			// normalization could not repair. Silence is what must not happen.
			return
		}
		if _, _, perr := ParseFrontmatter(data); perr != nil {
			t.Fatalf("marshalSessionFile returned a nil error and bytes ParseFrontmatter refuses.\n"+
				"input %q\nerror %v\nbytes:\n%s", s, perr, data)
		}
	})
}
