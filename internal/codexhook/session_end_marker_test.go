// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package codexhook

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// seedMarkers writes a marker for each id into cwd and returns a checker that
// asserts exactly which of them survived.
func seedMarkers(t *testing.T, cwd string, ids ...string) {
	t.Helper()
	pinMarkerNow(t, time.Now())
	for _, id := range ids {
		if err := WriteSessionMarker(cwd, id, "model-"+id); err != nil {
			t.Fatal(err)
		}
	}
}

func assertMarker(t *testing.T, cwd, id string, want bool) {
	t.Helper()
	_, err := os.Stat(SessionMarkerPath(cwd, id))
	if want && err != nil {
		t.Fatalf("marker %s should have SURVIVED but is gone: %v", id, err)
	}
	if !want && err == nil {
		t.Fatalf("marker %s should have been REMOVED but is still there", id)
	}
}

// A6 — ProcessSessionEnd removes its own marker and only its own, on the
// gate-skip, archive-failure and success arms alike.
//
// 🔴 This calls ProcessSessionEnd, exactly as every other SessionEnd test in
// this package does. That is deliberate: a cleanup reachable only through
// HandleSessionEnd would be one no assertion here could exercise and no
// mutation could turn red.
func TestCodexProcessSessionEndRemovesOnlyItsOwnMarker(t *testing.T) {
	t.Run("gate skip (no project signal)", func(t *testing.T) {
		root := t.TempDir()
		testutil.InitProject(t, root, "demo")
		v := storage.NewVault(root)
		// A bare cwd: GateCapture refuses on the missing .vibe-palace.toml
		// signal and ProcessSessionEnd returns nil before any archive work.
		cwd := t.TempDir()
		seedMarkers(t, cwd, "thr_mine", "thr_other")
		source := writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
		var out bytes.Buffer
		if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_mine", TranscriptPath: source, CWD: cwd}, v, "demo", &out, Options{}); err != nil {
			t.Fatalf("a gate skip must return nil: %v", err)
		}
		assertMarker(t, cwd, "thr_mine", false)
		assertMarker(t, cwd, "thr_other", true)
	})

	t.Run("archive failure (oversize transcript)", func(t *testing.T) {
		root := t.TempDir()
		testutil.InitProject(t, root, "demo")
		v := storage.NewVault(root)
		cwd := markedCwd(t, "demo")
		seedMarkers(t, cwd, "thr_mine", "thr_other")
		content := make([]byte, archive.DefaultMaxArchiveBytes+1)
		if _, err := rand.Read(content); err != nil {
			t.Fatal(err)
		}
		source := writeTranscript(t, content)
		var out bytes.Buffer
		if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_mine", TranscriptPath: source, CWD: cwd}, v, "demo", &out, Options{}); err == nil {
			t.Fatal("an oversize transcript must fail")
		}
		assertMarker(t, cwd, "thr_mine", false)
		assertMarker(t, cwd, "thr_other", true)
	})

	t.Run("success", func(t *testing.T) {
		root := t.TempDir()
		testutil.InitProject(t, root, "demo")
		v := storage.NewVault(root)
		cwd := markedCwd(t, "demo")
		seedMarkers(t, cwd, "thr_mine", "thr_other")
		source := writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
		var out bytes.Buffer
		launcher := &recordingLauncher{out: &out}
		if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_mine", TranscriptPath: source, CWD: cwd}, v, "demo", &out,
			Options{Launch: launcher.launch}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "archived:") {
			t.Fatalf("the success arm did not archive: %q", out.String())
		}
		assertMarker(t, cwd, "thr_mine", false)
		assertMarker(t, cwd, "thr_other", true)
	})
}

// A6b — the arms ProcessSessionEnd never reaches.
//
// 🔴 This is a NEW harness: before this change no test called HandleSessionEnd
// at all. Each case returns before ProcessSessionEnd, so only the defer inside
// HandleSessionEnd can clean up, and deleting that defer turns this test red
// while A6 stays green.
func TestCodexHandleSessionEndRemovesOnPreProcessArms(t *testing.T) {
	event := func(cwd, id string) string {
		payload, _ := json.Marshal(map[string]string{
			"session_id": id, "cwd": cwd, "transcript_path": writeTranscriptPath(t),
			"hook_event_name": "SessionEnd", "reason": "other",
		})
		return string(payload)
	}

	t.Run("no project signal", func(t *testing.T) {
		cwd := t.TempDir()
		seedMarkers(t, cwd, "thr_mine", "thr_other")
		var out bytes.Buffer
		if err := HandleSessionEnd(strings.NewReader(event(cwd, "thr_mine")), &out, Options{}); err == nil {
			t.Fatal("a cwd with no project marker must fail")
		}
		assertMarker(t, cwd, "thr_mine", false)
		assertMarker(t, cwd, "thr_other", true)
	})

	t.Run("project marker with no name", func(t *testing.T) {
		cwd := t.TempDir()
		if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"), []byte("[project]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		seedMarkers(t, cwd, "thr_mine", "thr_other")
		var out bytes.Buffer
		if err := HandleSessionEnd(strings.NewReader(event(cwd, "thr_mine")), &out, Options{}); err == nil {
			t.Fatal("an unusable project marker must fail")
		}
		assertMarker(t, cwd, "thr_mine", false)
		assertMarker(t, cwd, "thr_other", true)
	})

	t.Run("refused vault binding", func(t *testing.T) {
		// 🔴 THE SWALLOWED vault_path IS DELIBERATE. Placed BELOW [project] it
		// is scoped into that table, and the resolver refuses the binding with
		// ErrVaultBindingRejected — a binding that was FOUND and REFUSED, which
		// is the live-vault-protection class. Same fixture shape as
		// cmd/vp/cmd_codex_hook_parity_test.go.
		cwd := t.TempDir()
		stray := filepath.Join(t.TempDir(), "stray-vault")
		if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"),
			[]byte("[project]\nname = \"codexrefuse\"\nvault_path = \""+stray+"\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		seedMarkers(t, cwd, "thr_mine", "thr_other")
		var out bytes.Buffer
		if err := HandleSessionEnd(strings.NewReader(event(cwd, "thr_mine")), &out, Options{}); err == nil {
			t.Fatal("a refused vault binding must fail")
		}
		assertMarker(t, cwd, "thr_mine", false)
		assertMarker(t, cwd, "thr_other", true)
	})
}

func writeTranscriptPath(t *testing.T) string {
	t.Helper()
	return writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
}

// The paired case for the no-MCP-note assertion in
// TestSessionEndArchivesTranscriptExactly: when a canonical note DOES carry
// this session's id, the receipt's linker must find it and the manifest's
// reverse link must name it.
//
// This is the join this unit exists to close, exercised one layer below the
// MCP handler: the handler's job is only to put archive_session_id on the note.
func TestSessionEndBackLinksAPreWrittenMCPNote(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "demo")
	v := storage.NewVault(root)
	cwd := markedCwd(t, "demo")

	canonical, err := v.WriteSessionRef("demo", storage.SessionMeta{
		Date: "2026-11-02", ArchiveSessionID: "thr_joined",
		Host: storage.HostCodex, HostSource: storage.HostSourceDeclared,
		Tag: "implementation", Summary: "an MCP note that knows its host session",
	}, "mcp note body")
	if err != nil {
		t.Fatal(err)
	}

	source := writeTranscript(t, []byte("{\"codex_record\":\"x\"}\n"))
	var out bytes.Buffer
	if err := ProcessSessionEnd(nil, SessionEndEvent{SessionID: "thr_joined", TranscriptPath: source, CWD: cwd}, v, "demo", &out, Options{}); err != nil {
		t.Fatal(err)
	}
	entries, err := archive.ListEntries(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(entries))
	}
	if got := entries[0].Manifest.VaultRelSessionNote; got != canonical.NotePath {
		t.Fatalf("manifest back-link = %q, want the canonical note %q", got, canonical.NotePath)
	}
}
