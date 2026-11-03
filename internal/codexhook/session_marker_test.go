// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// pinMarkerNow fixes the marker clock for one test. Pinning rather than
// sleeping is the whole reason markerNow is a var: a TTL boundary and a
// "written later" case are otherwise unconstructible on purpose.
func pinMarkerNow(t *testing.T, at time.Time) {
	t.Helper()
	orig := markerNow
	markerNow = func() time.Time { return at }
	t.Cleanup(func() { markerNow = orig })
}

func markerFiles(t *testing.T, cwd string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(cwd, markerDirName, sessionMarkerPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func startEvent(cwd, sessionID, model string) string {
	payload, _ := json.Marshal(map[string]string{
		"cwd": cwd, "hook_event_name": "SessionStart",
		"session_id": sessionID, "model": model,
	})
	return string(payload)
}

// A1 — the marker lands, at the exact path, with the three fields, and
// written_at survives the round trip through time.Time.
func TestCodexSessionStartWritesSessionMarker(t *testing.T) {
	cwd := markerFixture(t, "codex-workspace")
	at := time.Date(2026, 11, 2, 15, 4, 5, 123456789, time.UTC)
	pinMarkerNow(t, at)

	var out bytes.Buffer
	if err := HandleSessionStart(strings.NewReader(startEvent(cwd, "thr_a1", "gpt-5-codex")), &out); err != nil {
		t.Fatal(err)
	}

	path := SessionMarkerPath(cwd, "thr_a1")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no marker at the expected path %s: %v", path, err)
	}
	var doc sessionMarker
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("marker is not the documented JSON: %v (%s)", err, raw)
	}
	if doc.SessionID != "thr_a1" {
		t.Errorf("marker session_id = %q, want %q", doc.SessionID, "thr_a1")
	}
	if doc.Model != "gpt-5-codex" {
		t.Errorf("marker model = %q, want %q", doc.Model, "gpt-5-codex")
	}
	if !doc.WrittenAt.Equal(at) {
		t.Errorf("written_at = %s, want %s — sub-second precision must survive the round trip", doc.WrittenAt, at)
	}
	// The bootstrap injection is the hook's contract and must still be emitted.
	if !strings.Contains(out.String(), "vp_bootstrap_context") {
		t.Errorf("writing the marker cost the bootstrap injection: %s", out.String())
	}
}

// A2 — no marker is written for a checkout whose project marker is absent OR
// unusable. The second case is what distinguishes `err == nil && found` from
// `found` alone: an unnamed project returns found == TRUE with an error.
func TestCodexSessionStartSkipsMarkerWithoutAUsableProject(t *testing.T) {
	t.Run("no .vibe-palace.toml", func(t *testing.T) {
		cwd := t.TempDir()
		var out bytes.Buffer
		_ = HandleSessionStart(strings.NewReader(startEvent(cwd, "thr_nosignal", "m")), &out)
		if got := markerFiles(t, cwd); len(got) != 0 {
			t.Fatalf("a checkout with no project signal got a marker: %v", got)
		}
	})
	t.Run("project marker with no name", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"), []byte("[project]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Precondition: this is the found==true + err!=nil shape, not found==false.
		if _, found, err := SessionStartContext(cwd); !found || err == nil {
			t.Fatalf("fixture is not the discriminating case: found=%v err=%v", found, err)
		}
		var out bytes.Buffer
		_ = HandleSessionStart(strings.NewReader(startEvent(cwd, "thr_noname", "m")), &out)
		if got := markerFiles(t, cwd); len(got) != 0 {
			t.Fatalf("an unusable project marker still got a session marker: %v", got)
		}
	})
}

// A3b — the reader itself, unstubbed. Lives here rather than in internal/tools
// because the TTL rows need markerNow, which is package-private.
func TestCodexSessionIDReader(t *testing.T) {
	const id = "thr_reader"
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	write := func(t *testing.T, cwd, name string, doc sessionMarker) {
		t.Helper()
		dir := filepath.Join(cwd, markerDirName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name      string
		build     func(t *testing.T, cwd string)
		wantID    string
		wantModel string
	}{
		{name: "zero files", build: func(*testing.T, string) {}},
		{
			name: "one well-formed",
			build: func(t *testing.T, cwd string) {
				write(t, cwd, sessionMarkerPrefix+id, sessionMarker{SessionID: id, Model: "m1", WrittenAt: now.Add(-time.Minute)})
			},
			wantID: id, wantModel: "m1",
		},
		{
			name: "two live markers are ambiguous",
			build: func(t *testing.T, cwd string) {
				write(t, cwd, sessionMarkerPrefix+"a", sessionMarker{SessionID: "a", Model: "m1", WrittenAt: now.Add(-time.Minute)})
				write(t, cwd, sessionMarkerPrefix+"b", sessionMarker{SessionID: "b", Model: "m2", WrittenAt: now.Add(-time.Second)})
			},
		},
		{
			name: "truncated json",
			build: func(t *testing.T, cwd string) {
				dir := filepath.Join(cwd, markerDirName)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, sessionMarkerPrefix+id), []byte(`{"session_id":"thr_`), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "expired by the TTL",
			build: func(t *testing.T, cwd string) {
				write(t, cwd, sessionMarkerPrefix+id, sessionMarker{SessionID: id, Model: "m1", WrittenAt: now.Add(-markerTTL - time.Minute)})
			},
		},
		{
			name: "one live beside one expired still resolves",
			build: func(t *testing.T, cwd string) {
				write(t, cwd, sessionMarkerPrefix+"live", sessionMarker{SessionID: "live", Model: "m-live", WrittenAt: now.Add(-time.Minute)})
				write(t, cwd, sessionMarkerPrefix+"dead", sessionMarker{SessionID: "dead", Model: "m-dead", WrittenAt: now.Add(-markerTTL - time.Hour)})
			},
			wantID: "live", wantModel: "m-live",
		},
		{
			// 🔴 Clock skew must not expire the NEWEST marker. A negative age is
			// not greater than the TTL; an absolute comparison would get this
			// backwards.
			//
			// The skew must EXCEED the TTL or this proves nothing: with a
			// smaller offset, |age| is under the TTL and a buggy Abs()
			// comparison reaches the same verdict as the correct signed one.
			// Measured — at +2h this row passed against an Abs() mutation.
			name: "future-dated stamp beyond the TTL is still live",
			build: func(t *testing.T, cwd string) {
				write(t, cwd, sessionMarkerPrefix+id, sessionMarker{SessionID: id, Model: "m-future", WrittenAt: now.Add(markerTTL + time.Hour)})
			},
			wantID: id, wantModel: "m-future",
		},
		{
			// Without the filename/body check a renamed file is a second
			// source of truth about which session owns this checkout.
			name: "filename disagrees with the body",
			build: func(t *testing.T, cwd string) {
				write(t, cwd, sessionMarkerPrefix+"renamed", sessionMarker{SessionID: id, Model: "m1", WrittenAt: now.Add(-time.Minute)})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinMarkerNow(t, now)
			cwd := t.TempDir()
			tc.build(t, cwd)
			gotID, gotModel := ReadSessionMarker(cwd)
			if gotID != tc.wantID || gotModel != tc.wantModel {
				t.Fatalf("ReadSessionMarker = (%q, %q), want (%q, %q)", gotID, gotModel, tc.wantID, tc.wantModel)
			}
		})
	}
}

// A6c — both no-ops. The missing-file one is load-bearing: the removal runs as
// a defer on every SessionEnd return path, including those where no marker was
// ever written.
func TestCodexRemoveSessionMarkerIsANoOp(t *testing.T) {
	cwd := t.TempDir()
	pinMarkerNow(t, time.Now())
	if err := WriteSessionMarker(cwd, "thr_keep", "m"); err != nil {
		t.Fatal(err)
	}

	t.Run("empty session id", func(t *testing.T) {
		if err := RemoveSessionMarker(cwd, ""); err != nil {
			t.Fatalf("empty id returned %v, want nil", err)
		}
		if got := markerFiles(t, cwd); len(got) != 1 {
			t.Fatalf("an empty id removed something: %v", got)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if err := RemoveSessionMarker(cwd, "thr_never_existed"); err != nil {
			t.Fatalf("a missing marker returned %v, want nil", err)
		}
	})
	t.Run("empty cwd", func(t *testing.T) {
		if err := RemoveSessionMarker("", "thr_keep"); err != nil {
			t.Fatalf("empty cwd returned %v, want nil", err)
		}
	})
}

// A7 — 🔴 THE FAIL-SAFE LOCK. Two Codex sessions live in one checkout: the
// LATER SessionStart must not disturb the earlier session's marker, and the
// reader must then refuse to name either.
//
// An earlier revision of this plan pruned "strictly older" siblings here. That
// turned a safe failure into an unsafe one: B would delete live A's marker, A's
// next capture would read only B's marker and stamp B's id onto A's note, and
// that stamp is sticky (capture.mergeCaptureMeta) and unrecoverable
// (storage.BackfillArchiveLink refuses to overwrite it). An unjoined note is a
// gap; a cross-linked note is corruption. Do not reintroduce a prune.
func TestCodexTwoLiveSessionsInOneCheckoutStayAmbiguous(t *testing.T) {
	cwd := markerFixture(t, "codex-workspace")
	base := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

	var out bytes.Buffer
	pinMarkerNow(t, base)
	if err := HandleSessionStart(strings.NewReader(startEvent(cwd, "thr_A", "model-a")), &out); err != nil {
		t.Fatal(err)
	}
	// B starts one second later, while A is still live.
	pinMarkerNow(t, base.Add(time.Second))
	if err := HandleSessionStart(strings.NewReader(startEvent(cwd, "thr_B", "model-b")), &out); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"thr_A", "thr_B"} {
		if _, err := os.Stat(SessionMarkerPath(cwd, id)); err != nil {
			t.Fatalf("marker for %s is gone — a later SessionStart disarmed a LIVE session: %v", id, err)
		}
	}
	pinMarkerNow(t, base.Add(2*time.Second))
	if id, model := ReadSessionMarker(cwd); id != "" || model != "" {
		t.Fatalf("two live sessions resolved to (%q, %q); ambiguity must return empty for both", id, model)
	}
}

// traversalIDs are the session ids that must never reach the filesystem.
//
// Each was measured against the unguarded filepath.Join construction:
//
//	"../../../../victim"  escaped the checkout entirely
//	"../../etc/cron.d/x"  landed under .vibe-palace/etc/cron.d/
//	"a/b"                 landed under .vibe-palace/codex-session-a/
//	"/etc/passwd"         landed under .vibe-palace/codex-session-/etc/
//
// 🔴 "a/b" is the load-bearing row: filepath.IsLocal("a/b") is TRUE, so a guard
// written as IsLocal alone would admit it and the marker would still leave the
// directory. Do not drop this case.
var traversalIDs = []string{
	"../../../../victim",
	"../../etc/cron.d/x",
	"a/b",
	"/etc/passwd",
	"..",
	".",
}

// TestCodexSessionMarkerRefusesTraversingSessionIDs is the containment lock on
// BOTH filesystem-touching entry points.
//
// 🔴 It asserts on the FILESYSTEM, not on the return value. A guard that
// returns an error only after os.WriteFile or os.Remove has already run would
// satisfy an error-only assertion while still writing or deleting the file.
func TestCodexSessionMarkerRefusesTraversingSessionIDs(t *testing.T) {
	pinMarkerNow(t, time.Now())

	t.Run("write creates nothing outside the marker directory", func(t *testing.T) {
		for _, id := range traversalIDs {
			t.Run(id, func(t *testing.T) {
				// A sealed tree: cwd sits deep enough that a traversing id has
				// somewhere to escape to, and `sentinel` is what it would hit.
				base := t.TempDir()
				cwd := filepath.Join(base, "a", "b", "c", "proj")
				if err := os.MkdirAll(cwd, 0o755); err != nil {
					t.Fatal(err)
				}
				before := treeSnapshot(t, base)

				err := WriteSessionMarker(cwd, id, "model")
				if err == nil {
					t.Fatalf("WriteSessionMarker accepted a traversing id %q", id)
				}
				if !errors.Is(err, errUnsafeSessionID) {
					t.Fatalf("error = %v, want errUnsafeSessionID", err)
				}
				if after := treeSnapshot(t, base); after != before {
					t.Fatalf("a refused id still touched the filesystem.\nbefore: %s\nafter:  %s", before, after)
				}
			})
		}
	})

	t.Run("remove deletes nothing outside the marker directory", func(t *testing.T) {
		for _, id := range traversalIDs {
			t.Run(id, func(t *testing.T) {
				base := t.TempDir()
				cwd := filepath.Join(base, "a", "b", "c", "proj")
				if err := os.MkdirAll(filepath.Join(cwd, markerDirName), 0o755); err != nil {
					t.Fatal(err)
				}
				// The file a traversing remove would reach, plus a legitimate
				// marker that must also survive.
				victim := filepath.Join(base, "victim")
				if err := os.WriteFile(victim, []byte("do not delete me"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := WriteSessionMarker(cwd, "thr_legit", "m"); err != nil {
					t.Fatal(err)
				}
				before := treeSnapshot(t, base)

				err := RemoveSessionMarker(cwd, id)
				if err == nil {
					t.Fatalf("RemoveSessionMarker accepted a traversing id %q", id)
				}
				if !errors.Is(err, errUnsafeSessionID) {
					t.Fatalf("error = %v, want errUnsafeSessionID", err)
				}
				if _, serr := os.Stat(victim); serr != nil {
					t.Fatalf("a refused id DELETED a file outside the marker directory: %v", serr)
				}
				if after := treeSnapshot(t, base); after != before {
					t.Fatalf("a refused id still changed the filesystem.\nbefore: %s\nafter:  %s", before, after)
				}
			})
		}
	})

	// The ordinary id must still work, or the guard is just a denial of service.
	t.Run("a real Codex session id is still accepted", func(t *testing.T) {
		cwd := t.TempDir()
		const id = "01a19e14-e936-7392-8531-bccca87d5f44"
		if err := WriteSessionMarker(cwd, id, "gpt-5-codex"); err != nil {
			t.Fatalf("a well-formed uuid id was refused: %v", err)
		}
		if _, err := os.Stat(SessionMarkerPath(cwd, id)); err != nil {
			t.Fatalf("marker not written: %v", err)
		}
		if got, _ := ReadSessionMarker(cwd); got != id {
			t.Fatalf("ReadSessionMarker = %q, want %q", got, id)
		}
		if err := RemoveSessionMarker(cwd, id); err != nil {
			t.Fatalf("remove of a well-formed id failed: %v", err)
		}
		if _, err := os.Stat(SessionMarkerPath(cwd, id)); !os.IsNotExist(err) {
			t.Fatalf("marker survived removal: %v", err)
		}
	})
}

// treeSnapshot renders every path under root, so a test can assert that a
// refusal left the filesystem byte-for-byte alone rather than merely returning
// an error.
func treeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			rel += "/"
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return strings.Join(paths, " ")
}
