// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// 🔴 THE CALL-SITE AGREEMENT INVARIANT.
//
// THERE ARE THREE production back-link call sites, not two:
//
//	internal/hook                  — Claude Code's SessionEnd, after the claim gate
//	internal/capture/codex_receipt — Codex's SessionEnd receipt
//	internal/capture/session.go    — capture's own, when the archive already exists
//
// For months they disagreed about one rule. The Codex site tested a
// storage.ArchiveLinkResult field called CanonicalIsStub and withheld the
// back-link when the canonical note was an auto-capture stub; the Claude site
// carried a COMMENT describing that same rule and tested nothing; capture's own
// site linked unconditionally. Because the Codex receipt is always a stub and is
// always the only note at SessionEnd, the condition was always true: every Codex
// session stranded its own transcript, silently, and the suite called it correct.
//
// The rule is now "consulted at NO call site". This test holds the BEHAVIOURAL
// half: one fixture shape — a session whose ONLY note is the auto-capture stub,
// plus an archive — driven down each writer's own path, every arm asserting the
// manifest names that stub. The structural half, which guarantees this list of
// arms stays COMPLETE, is TestBackLinkWriterCensus in internal/archive.
//
// 🔴 ONE ARM PER WRITER, AND EACH REDDENS ONLY FOR ITS OWN SITE. That is not a
// design preference; it is forced, because each path masks the others:
//
//	arm      drives                     isolates                        masked by
//	codex    `vp codex hook session-end`  internal/capture/codex_receipt  —
//	claude   Stop then SessionEnd         internal/hook                   capture, unless Stop runs first
//	capture  SessionEnd alone             internal/capture/session.go     internal/hook, unless no note exists yet
//
// 🔴 EVERY LINE OF THAT TABLE WAS MEASURED, AND TWICE IT CONTRADICTED WHAT
// READING THE CODE SUGGESTED. A Claude arm driving SessionEnd alone stays GREEN
// when internal/hook is guarded, because capture's own back-link call closes the
// loop first — found by mutating after the test was green. And the suite as first
// shipped had no `capture` arm at all: both adversarial reviewers then guarded
// internal/capture/session.go as `meta.Tag != storage.TagAutoCapture` — the old
// rule, new spelling — and every test in this repository stayed green.
//
// So: do not merge these arms, do not "simplify" one into another, and do not
// add a back-link writer without adding its arm and PROVING the arm reddens by
// mutating the new site. The census test fails until you do.
//
// All three arms run the REAL BINARY through its real hook commands, so this
// measures production wiring (cmd/vp registration included) rather than a seam.
func TestIntegrationBackLinkCallSitesAgreeOnAStubOnlySession(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		env := setupFreshEnv(t)
		bin := buildVPBinary(t)
		runVP(t, bin, env, nil, "init", env.projectDir, "--name", "blproj", "--vault-path", env.vaultPath, "--no-git")

		source := filepath.Join(t.TempDir(), "rollout.jsonl")
		if err := os.WriteFile(source, []byte("{\"codex_record\":\"stub only\"}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		event, _ := json.Marshal(map[string]string{
			"session_id": "thr_stub_only", "cwd": env.projectDir,
			"transcript_path": source, "hook_event_name": "SessionEnd",
		})
		run := testinfra.RunCLI(t, env.iso.Environ(), env.projectDir, event, "codex", "hook", "session-end")
		if run.ExitCode != 0 {
			t.Fatalf("codex session-end exit=%d stdout=%s stderr=%s", run.ExitCode, run.Stdout, run.Stderr)
		}
		assertStubOnlySessionIsBackLinked(t, env.vaultPath, "blproj", "thr_stub_only")
	})

	t.Run("claude", func(t *testing.T) {
		env := setupFreshEnv(t)
		bin := buildVPBinary(t)
		runVP(t, bin, env, nil, "init", env.projectDir, "--name", "blproj", "--vault-path", env.vaultPath, "--no-git")
		// archive.Create reads git metadata from the event cwd; a repo keeps the
		// fixture closer to a real session and costs nothing here.
		initGitRepo(t, env.projectDir)

		transcript := fakeTranscript(t, "claude_stub_only")
		event := func(name string) {
			t.Helper()
			payload, _ := json.Marshal(map[string]string{
				"session_id": "claude_stub_only", "cwd": env.projectDir,
				"transcript_path": transcript, "hook_event_name": name,
			})
			run := testinfra.RunCLI(t, env.iso.Environ(), env.projectDir, payload, "hook")
			if run.ExitCode != 0 {
				t.Fatalf("hook %s exit=%d stdout=%s stderr=%s", name, run.ExitCode, run.Stdout, run.Stderr)
			}
		}
		// 🔴 STOP, THEN SessionEnd — THE SEQUENCE IS LOAD-BEARING, AND A
		// SessionEnd-ONLY ARM IS INERT. Measured 2026-11-07: with SessionEnd
		// alone, capture runs with the archive already on disk, so
		// capture.WriteSession's OWN back-link call (internal/capture/session.go)
		// closes the loop before internal/hook's call site is reached — and a
		// guard restored at hook.go changes nothing observable. The arm passed
		// under that mutation, which is the inert-guard shape this whole task is
		// about, found here by running the mutation rather than by reading.
		//
		// With Stop first the note is written while no archive exists, the claim
		// sentinel then short-circuits capture at SessionEnd, and internal/hook's
		// TryLinkSessionNote is the ONLY writer of this manifest's back-link.
		// That is the real two-event shape of a Claude session, and it is the
		// only one under which this arm measures the call site it names.
		event("Stop")
		event("SessionEnd")
		assertStubOnlySessionIsBackLinked(t, env.vaultPath, "blproj", "claude_stub_only")
	})

	// 🔴 THE THIRD ARM, added 2026-11-07 after both adversarial reviewers
	// exploited its absence. capture.WriteSession has its own back-link call
	// (internal/capture/session.go), reached whenever capture runs with the
	// archive ALREADY on disk. Both reviewers guarded it as
	// `archiveEntry != nil && meta.Tag != storage.TagAutoCapture` — the old
	// Codex rule under a different spelling — and every test in the suite,
	// including both arms above and the identifier ratchet, stayed GREEN.
	//
	// SessionEnd ALONE is the fixture that isolates this site, and the isolation
	// is exact rather than incidental: with no prior Stop, no note carries the
	// session id when the hook reaches its own linker, so
	// TryLinkArchiveToSessions reports Found=false and internal/hook writes
	// nothing. Capture then runs at the end of the same hook, finds the archive
	// already created, and its own call is the ONLY writer of this back-link.
	//
	// That is why this arm is not a duplicate of the Claude arm above, and why
	// the two must BOTH exist: Stop→SessionEnd covers internal/hook and cannot
	// see a guard here, SessionEnd-only covers capture and cannot see a guard
	// there. Each masks the other's site. Measured, not reasoned: see the
	// mutation table in the execution record.
	t.Run("capture", func(t *testing.T) {
		env := setupFreshEnv(t)
		bin := buildVPBinary(t)
		runVP(t, bin, env, nil, "init", env.projectDir, "--name", "blproj", "--vault-path", env.vaultPath, "--no-git")
		initGitRepo(t, env.projectDir)

		transcript := fakeTranscript(t, "capture_stub_only")
		payload, _ := json.Marshal(map[string]string{
			"session_id": "capture_stub_only", "cwd": env.projectDir,
			"transcript_path": transcript, "hook_event_name": "SessionEnd",
		})
		run := testinfra.RunCLI(t, env.iso.Environ(), env.projectDir, payload, "hook")
		if run.ExitCode != 0 {
			t.Fatalf("hook SessionEnd exit=%d stdout=%s stderr=%s", run.ExitCode, run.Stdout, run.Stderr)
		}
		assertStubOnlySessionIsBackLinked(t, env.vaultPath, "blproj", "capture_stub_only")
	})
}

// assertStubOnlySessionIsBackLinked is the shared arm body: ONE rule, asserted
// identically for both hosts, so neither arm can drift into measuring something
// the other does not.
//
// 🔴 ITS TWO PRECONDITIONS ARE WHAT KEEP IT FROM GOING INERT. Restoring either
// guard reddens the final assertion only while the session's canonical note is a
// TagAutoCapture stub AND is the only note of that session. A fixture that also
// held a real note would link under the old rule too, and this test would pass
// against the exact defect it exists to catch — the inert-guard shape that
// produced the task. They are asserted, not assumed.
func assertStubOnlySessionIsBackLinked(t *testing.T, vaultPath, project, sessionID string) {
	t.Helper()

	v := storage.NewVault(vaultPath)
	rows, _, err := v.ListSessions(project, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	var note storage.SessionMeta
	n := 0
	for _, row := range rows {
		if row.ArchiveSessionID == sessionID {
			n++
			note = row
		}
	}
	if n != 1 {
		t.Fatalf("precondition: %d notes carry %s, want exactly 1 (the auto-capture stub); rows=%+v",
			n, sessionID, rows)
	}
	if note.Tag != storage.TagAutoCapture {
		t.Fatalf("precondition: note tag = %q, want %q — the fixture no longer exercises the stub path",
			note.Tag, storage.TagAutoCapture)
	}

	entries, err := archive.ListEntries(vaultPath, project)
	if err != nil {
		t.Fatal(err)
	}
	var entry *archive.Entry
	for _, e := range entries {
		if e.Manifest.SessionID == sessionID {
			entry = e
			break
		}
	}
	if entry == nil {
		t.Fatalf("precondition: no manifest for %s; entries=%d", sessionID, len(entries))
	}

	// EQUALITY, not non-empty: a back-link wired to the wrong note is not
	// visibly different from a correct one under a non-empty check.
	if got := entry.Manifest.VaultRelSessionNote; got != note.NotePath {
		t.Fatalf("manifest back-link = %q, want the stub note %q — this call site strands a stub-only session while the other does not",
			got, note.NotePath)
	}
	// And the forward direction, so a passing arm means the loop is CLOSED
	// rather than merely half-written in the new direction.
	if !strings.HasSuffix(note.Archive, ".manifest.json") {
		t.Errorf("note archive: = %q, want the manifest path", note.Archive)
	}
}
