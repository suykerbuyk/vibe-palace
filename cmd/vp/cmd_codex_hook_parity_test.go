// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// bindCodexEventVault builds the EVENT vault and a project cwd whose UNTRACKED
// .vibe-palace.toml binds to it, mirroring bindPayloadVault. Two vaults are the
// whole point of these tests: the process cwd resolves to the CONFIGURED vault
// (setupTestVaultEnvAt), and the event's cwd must resolve somewhere else, or a
// handler that never re-pointed anything would still pass.
//
// initialised=false leaves the project absent from the vault, which is how a
// test reaches the uninitialised gate.
func bindCodexEventVault(t *testing.T, slug string, initialised bool) (vaultRoot, cwd string) {
	t.Helper()
	tmp := t.TempDir()
	vaultRoot = filepath.Join(tmp, "event-vault")
	cwd = filepath.Join(tmp, "proj")
	for _, d := range []string{vaultRoot, cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if initialised {
		testutil.InitProject(t, vaultRoot, slug)
	}
	// Identity AND binding. The marker is untracked in a no-git temp dir, so
	// ADR-012's tier-1 vault_path applies.
	if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"),
		[]byte("vault_path = \""+vaultRoot+"\"\n[project]\nname = \""+slug+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return vaultRoot, cwd
}

func codexTranscript(t *testing.T, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func codexEventStdin(t *testing.T, sessionID, cwd, transcript string) {
	t.Helper()
	writePayloadStdin(t, map[string]string{
		"hook_event_name": "SessionEnd",
		"session_id":      sessionID,
		"cwd":             cwd,
		"transcript_path": transcript,
	})
}

func readVaultLog(t *testing.T, vaultRoot string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(storage.NewVault(vaultRoot).VaultLocalDir(), "vp.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

// TestCodexSessionEndIsExemptFromSurfaceGate mirrors TestSurfaceGateExemptsHook.
// surfaceGate resolves the CONFIGURED vault, but this hook writes the vault it
// resolves from the event's cwd, so a warning here would name the wrong one.
// The contrast arm proves the stamp is driving the test rather than the gate
// being silently inert.
func TestCodexSessionEndIsExemptFromSurfaceGate(t *testing.T) {
	t.Setenv("VP_SURFACE_QUIET", "")
	t.Setenv("VP_SURFACE_GATE", "")
	configured := setupTestVaultEnvAt(t, t.TempDir())
	stampDir := filepath.Join(configured, "Projects", "foo")
	if err := surface.WriteStamp(stampDir, surface.MCPSurfaceVersion+1, "newer"); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	t.Cleanup(surface.SetGateWriter(&buf))

	if code := surfaceGate(&cli.Command{Name: "codex hook session-end"}); code != cli.ExitOK {
		t.Fatalf("surfaceGate(codex hook session-end) exit = %d, want ExitOK", code)
	}
	if buf.Len() != 0 {
		t.Fatalf("codex hook session-end is exempt but surfaceGate warned on the configured vault:\n%s", buf.String())
	}

	buf.Reset()
	if code := surfaceGate(&cli.Command{Name: "status"}); code != cli.ExitOK {
		t.Fatalf("surfaceGate(status) exit = %d, want ExitOK (warn-only path)", code)
	}
	if buf.Len() == 0 {
		t.Fatal("contrast: a non-exempt command did NOT warn on an above-surface vault — the stamp is not driving the test")
	}
}

// TestCodexSessionEndLogsIntoTheEventVaultNotTheProcessVault is the cross-vault
// property. A gate skip is used as the log emitter because it is the only line
// the Codex path emits deterministically — on full success it logs nothing.
//
// 🔴 THE NEGATIVE IS SCOPED ON PURPOSE. It asserts that the lines the CODEX PATH
// emits do not appear in the configured vault's log — NOT that that log is
// untouched. preRun attaches the process-cwd logger before this handler runs
// (cmd/vp/main.go), and installProjectVaultSnapshot can emit a Warn into it
// before any callback exists. Those are pre-existing process-cwd logging shared
// with every command, not a leak this unit introduced. Do not "fix" this test
// by silencing them.
func TestCodexSessionEndLogsIntoTheEventVaultNotTheProcessVault(t *testing.T) {
	configured := setupTestVaultEnvAt(t, t.TempDir())
	eventVault, cwd := bindCodexEventVault(t, "codexparity", false) // uninitialised -> the gate logs

	codexEventStdin(t, "codex-xvault", cwd, codexTranscript(t, []byte("{\"codex_record\":\"x\"}\n")))

	var out, errb bytes.Buffer
	if code := runCodexSessionEnd(cli.BuildInfo{Version: "test"}, os.Stdin, &out, &errb); code != cli.ExitOK {
		t.Fatalf("a gate skip exited %d, want ExitOK; stderr=%s", code, errb.String())
	}

	// POSITIVE: the run's own line reached the EVENT vault.
	eventLog := readVaultLog(t, eventVault)
	if !strings.Contains(eventLog, "hook uninitialised project:") {
		t.Fatalf("the event vault's log is missing the run's own line — logging was never re-pointed:\n%s", eventLog)
	}
	if !strings.Contains(eventLog, `"host":"codex"`) {
		t.Errorf("the gate line carries no host attribute, so live verification cannot grep for it:\n%s", eventLog)
	}

	// NEGATIVE, scoped to what the Codex path emits.
	configuredLog := readVaultLog(t, configured)
	for _, token := range []string{"hook uninitialised project:", `"host":"codex"`} {
		if strings.Contains(configuredLog, token) {
			t.Errorf("a Codex-path line (%q) landed in the CONFIGURED vault's log:\n%s", token, configuredLog)
		}
	}
}

// TestCodexSessionEndSkipExitsZero pins the exit code for BOTH gates. A skip is
// a correct outcome; returning cli.ExitSystem would make every stale checkout
// and every uninitialised project read as a hook crash.
func TestCodexSessionEndSkipExitsZero(t *testing.T) {
	t.Run("uninitialised", func(t *testing.T) {
		setupTestVaultEnvAt(t, t.TempDir())
		eventVault, cwd := bindCodexEventVault(t, "codexskip", false)
		codexEventStdin(t, "codex-skip-uninit", cwd, codexTranscript(t, []byte("{\"codex_record\":\"x\"}\n")))
		var out, errb bytes.Buffer
		if code := runCodexSessionEnd(cli.BuildInfo{Version: "test"}, os.Stdin, &out, &errb); code != cli.ExitOK {
			t.Fatalf("exit = %d, want ExitOK; stderr=%s", code, errb.String())
		}
		if strings.Contains(out.String(), "archived:") {
			t.Errorf("a skip emitted a success line: %q", out.String())
		}
		if entries, _ := archive.ListEntries(eventVault, "codexskip"); len(entries) != 0 {
			t.Errorf("a skip archived anyway: %+v", entries)
		}
	})

	t.Run("departed", func(t *testing.T) {
		setupTestVaultEnvAt(t, t.TempDir())
		eventVault, cwd := bindCodexEventVault(t, "codexskip", true)
		rec, err := (departure.Record{Slug: "codexskip", Kind: departure.Renamed, To: "codexskip-new", Date: "2026-09-23"}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		recAbs := filepath.Join(eventVault, filepath.FromSlash(departure.RelPath("codexskip")))
		if err := os.MkdirAll(filepath.Dir(recAbs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(recAbs, rec, 0o644); err != nil {
			t.Fatal(err)
		}
		codexEventStdin(t, "codex-skip-departed", cwd, codexTranscript(t, []byte("{\"codex_record\":\"x\"}\n")))
		var out, errb bytes.Buffer
		if code := runCodexSessionEnd(cli.BuildInfo{Version: "test"}, os.Stdin, &out, &errb); code != cli.ExitOK {
			t.Fatalf("exit = %d, want ExitOK; stderr=%s", code, errb.String())
		}
		if !strings.Contains(readVaultLog(t, eventVault), "hook stale checkout:") {
			t.Errorf("no stale-checkout line in the event vault's log:\n%s", readVaultLog(t, eventVault))
		}
	})
}

// TestCodexSessionEndFailureStillExitsSystem pins the half of the exit-code
// ruling that was DEFERRED, not applied: a genuine failure still returns
// cli.ExitSystem. U2 adds gates and a diagnostic log to this same handler while
// deliberately leaving the failure signal alone, and nothing else pins that the
// failure arm survived the edit — a refactor that returned nil everywhere would
// satisfy every other test in this file.
func TestCodexSessionEndFailureStillExitsSystem(t *testing.T) {
	setupTestVaultEnvAt(t, t.TempDir())
	eventVault, cwd := bindCodexEventVault(t, "codexfail", true)

	oversize := make([]byte, archive.DefaultMaxArchiveBytes+1)
	if _, err := rand.Read(oversize); err != nil {
		t.Fatal(err)
	}
	codexEventStdin(t, "codex-fail", cwd, codexTranscript(t, oversize))

	var out, errb bytes.Buffer
	if code := runCodexSessionEnd(cli.BuildInfo{Version: "test"}, os.Stdin, &out, &errb); code != cli.ExitSystem {
		t.Fatalf("a genuine archive failure exited %d, want cli.ExitSystem (%d)", code, cli.ExitSystem)
	}
	if !strings.Contains(errb.String(), "vp codex hook session-end:") {
		t.Errorf("failure wrote no stderr diagnostic: %q", errb.String())
	}
	// And the failure is now visible in the vault log, which is the whole
	// reason the exit-code question can be settled later.
	if !strings.Contains(readVaultLog(t, eventVault), "codex hook: archive failed") {
		t.Errorf("archive failure is still invisible in vp.log:\n%s", readVaultLog(t, eventVault))
	}
	if entries, _ := archive.ListEntries(eventVault, "codexfail"); len(entries) != 0 {
		t.Errorf("a refused archive left entries behind: %+v", entries)
	}
}

// TestCodexSessionEndRecordsVPVersionFromBuildInfo proves cli.BuildInfo is
// threaded all the way from registration (commands.go) to the manifest. A build
// that adds the CreateOptions field but forgets the registration argument
// compiles and writes "", which is byte-identical to every Codex manifest
// written before this change.
func TestCodexSessionEndRecordsVPVersionFromBuildInfo(t *testing.T) {
	setupTestVaultEnvAt(t, t.TempDir())
	eventVault, cwd := bindCodexEventVault(t, "codexver", true)
	codexEventStdin(t, "codex-ver", cwd, codexTranscript(t, []byte("{\"codex_record\":\"x\"}\n")))

	var out, errb bytes.Buffer
	if code := runCodexSessionEnd(cli.BuildInfo{Version: "9.9.9-buildinfo"}, os.Stdin, &out, &errb); code != cli.ExitOK {
		t.Fatalf("exit = %d; stderr=%s", code, errb.String())
	}
	entries, err := archive.ListEntries(eventVault, "codexver")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("archive entries = %d, want 1", len(entries))
	}
	if got := entries[0].Manifest.VPVersion; got != "9.9.9-buildinfo" {
		t.Errorf("manifest vp_version = %q, want the BuildInfo version", got)
	}
	// git_head comes from the EVENT cwd, which is not a git repo here, so it is
	// legitimately empty — asserted so a later change cannot start silently
	// filling it from the process cwd.
	if got := entries[0].Manifest.GitHead; got != "" {
		t.Errorf("manifest git_head = %q, want empty: the event cwd is not a git repo, so a value can only have come from the process cwd", got)
	}
}

// TestCodexSessionEndRegisteredCommandCarriesBuildInfo pins the registration
// wiring itself: cmd/vp/commands.go must pass info to cmdCodexHookSessionEnd.
// The signature change is what makes that a compile error rather than a silent
// empty string, and this asserts the command is still registered after it.
func TestCodexSessionEndRegisteredCommandCarriesBuildInfo(t *testing.T) {
	cmd := registeredCommand(t, "codex hook session-end")
	if cmd.Run == nil {
		t.Fatal("codex hook session-end has no Run")
	}
	if cmd.MutatesVault {
		t.Fatal("codex hook session-end must stay UNWRAPPED (Codex's 3s ceiling)")
	}
}

// TestCodexSessionEndRefusedBindingLogsAndCapturesNothing covers the one
// resolution class that gets a diagnostic: a binding the resolver REFUSED.
// Today that produces no vp.log line at all, because the refusal happens before
// any vault is known. The global vault is opened to reach a log file and for no
// other reason — the exit stays ExitSystem and nothing is captured anywhere.
//
// 🔴 There is deliberately NO companion test for ErrHostConfigUnreadable, and
// no branch for it in the handler: readProjectVaults() runs unconditionally
// ahead of tier selection in storage.ResolveVaultBinding and its error returns
// immediately, so that sentinel never reaches the tier-3 global fall-through
// and Codex never captures on it. See the task's Plan C6.
func TestCodexSessionEndRefusedBindingLogsAndCapturesNothing(t *testing.T) {
	configured := setupTestVaultEnvAt(t, t.TempDir())

	// 🔴 THE SWALLOWED vault_path IS DELIBERATE — DO NOT "FIX" IT.
	// vault_path sits BELOW the [project] header, so TOML scopes it into that
	// table and the resolver refuses with ErrVaultBindingRejected
	// (storage.ErrSwallowedVaultPath). That is the shape this test needs: a
	// binding that was FOUND and REFUSED, which is the live-vault-protection
	// class. Every other fixture in this file puts vault_path above the tables
	// on purpose, because those tests need the binding to WORK.
	cwd := t.TempDir()
	stray := filepath.Join(t.TempDir(), "stray-vault")
	if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"),
		[]byte("[project]\nname = \"codexrefuse\"\nvault_path = \""+stray+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	codexEventStdin(t, "codex-refused", cwd, codexTranscript(t, []byte("{\"codex_record\":\"x\"}\n")))

	var out, errb bytes.Buffer
	code := runCodexSessionEnd(cli.BuildInfo{Version: "test"}, os.Stdin, &out, &errb)
	if code != cli.ExitSystem {
		t.Fatalf("a refused binding exited %d, want cli.ExitSystem — the exit mechanism is unchanged by U2", code)
	}
	if strings.Contains(out.String(), "archived:") {
		t.Fatalf("a refused binding emitted a success line: %q", out.String())
	}
	// Nothing captured, in the global vault or anywhere else.
	if entries, _ := archive.ListEntries(configured, "codexrefuse"); len(entries) != 0 {
		t.Errorf("a refused binding captured into the GLOBAL vault: %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(configured, "Projects", "codexrefuse")); err == nil {
		t.Error("a refused binding created Projects/codexrefuse in the GLOBAL vault")
	}
	// The one diagnostic line, in the global vault's log.
	if log := readVaultLog(t, configured); !strings.Contains(log, "refusing to capture into the live vault") {
		t.Errorf("a refused binding is still invisible in vp.log:\n%s", log)
	}
}
