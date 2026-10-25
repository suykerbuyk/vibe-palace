// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// trackedMarkerTranscript is a minimal Claude Code JSONL transcript (mirrors
// internal/hook's fakeTranscript) so the control subtest produces a real capture.
const trackedMarkerTranscript = `{"type":"permission-mode","permissionMode":"default","sessionId":"test-session"}
{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"role":"assistant","model":"claude-opus-4-6","content":"hi there"}}
`

// gitInRepo runs a git command in dir with a hermetic identity.
func gitInRepo(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitenv.SafeGitEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// runHookForCwd drives runHook with a payload whose cwd is projectDir (≠ the
// process cwd), through stdin, the way the real Claude Code hook does. A non-empty
// transcriptPath makes a SessionEnd run that archives.
func runHookForCwd(t *testing.T, projectDir, transcriptPath, tmp string) int {
	t.Helper()
	payload := map[string]string{
		"hook_event_name": "SessionEnd",
		"session_id":      "tracked-marker-session",
		"cwd":             projectDir,
	}
	if transcriptPath != "" {
		payload["transcript_path"] = transcriptPath
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	stdinFile := filepath.Join(tmp, "payload.json")
	writeTestFile(t, stdinFile, string(body))
	f, err := os.Open(stdinFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	origStdin := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = origStdin })
	code := runHook(cli.BuildInfo{Version: "test"})
	vplog.Close()
	return code
}

// manifestCount counts the transcript manifests captured under a vault's project.
func manifestCount(t *testing.T, vaultRoot, slug string) int {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(vaultRoot, "Projects", slug, "transcripts", "*.manifest.json"))
	return len(m)
}

// TestHookRefusesTrackedMarkerWithVaultPath (U4 integration): a real git repo with
// a COMMITTED .vibe-palace.toml carrying vault_path must capture NOTHING, and the
// U4-specific refusal ("git-tracked" / "identity only") must reach the log — the
// resolver refusal, which `vp hook` honors as capture-nothing.
//
// 🔴 BREAK-IT: there is NO disagreeing tier-2 binding in this fixture, so the ONLY
// thing that can make the hook capture nothing is the U4 tracked-marker refusal.
// Delete the U4 block in resolve.go and the tracked marker's vault_path is honored
// (tier-1) → the hook CAPTURES into markerVault and the "git-tracked" log assertion
// goes RED (confirmed: without the block, resolution returns the tier-1 path and no
// refusal is logged). The payload cwd is the repo, asserted ≠ the process cwd, and
// IsTrackedMarker's real git path is exercised (not the markerIsTracked seam).
func TestHookRefusesTrackedMarkerWithVaultPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	configDir := filepath.Join(home, ".config", "vibe-palace")
	liveVault := filepath.Join(tmp, "live-vault")
	markerVault := filepath.Join(tmp, "marker-vault")
	repo := filepath.Join(tmp, "qa-checkout")
	for _, d := range []string{configDir, repo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Both are real, project-initialised vaults: the live (global default) vault and
	// the vault the committed marker points at (so that, WITHOUT U4, the hook would
	// actually capture there — making "git-tracked absent" the clean differentiator).
	testutil.InitProject(t, liveVault, "qa")
	testutil.InitProject(t, markerVault, "qa")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// Global default = live vault; NO [project_vaults] binding (no disagreeing tier-2).
	writeTestFile(t, filepath.Join(configDir, "config.toml"), "vault_path = \""+liveVault+"\"\n")

	// A git repo whose .vibe-palace.toml is COMMITTED and carries vault_path.
	gitInRepo(t, repo, "init", "-q")
	writeTestFile(t, filepath.Join(repo, ".vibe-palace.toml"),
		"vault_path = \""+markerVault+"\"\n[project]\nname = \"qa\"\n")
	gitInRepo(t, repo, "add", ".vibe-palace.toml")
	gitInRepo(t, repo, "commit", "-q", "-m", "add marker")

	if cwd, _ := os.Getwd(); cwd == repo {
		t.Fatalf("test invariant: payload cwd (%s) must differ from the process cwd", repo)
	}

	t.Run("refusal_logs_u4_sentinel_and_captures_nothing", func(t *testing.T) {
		code := runHookForCwd(t, repo, "", tmp)
		if code != cli.ExitOK {
			t.Errorf("exit = %d, want ExitOK (a hook failure is reported, never blocking)", code)
		}
		if n := manifestCount(t, liveVault, "qa"); n != 0 {
			t.Fatalf("captured %d transcript(s) into the live vault; a tracked-marker refusal must capture nothing", n)
		}
		if n := manifestCount(t, markerVault, "qa"); n != 0 {
			t.Fatalf("captured %d transcript(s) into the marker's vault; the U4 refusal must capture nothing", n)
		}
		joined := strings.Join(logLines(t, liveVault), "\n")
		// U4-SPECIFIC: the refusal error text, not merely the shared "refusing to
		// capture" line (which the pre-existing disagreement refusal also logs).
		if !strings.Contains(joined, "git-tracked") || !strings.Contains(joined, "identity only") {
			t.Errorf("the U4 tracked-marker refusal text did not reach vp.log (so the test cannot tell U4 from any other refusal):\n%s", joined)
		}
	})

	t.Run("control_cleaned_marker_captures_via_runHook", func(t *testing.T) {
		// Remove the offending vault_path; commit the identity-only marker.
		writeTestFile(t, filepath.Join(repo, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
		gitInRepo(t, repo, "commit", "-qam", "identity only")
		transcript := filepath.Join(tmp, "transcript.jsonl")
		writeTestFile(t, transcript, trackedMarkerTranscript)

		before := manifestCount(t, liveVault, "qa")
		code := runHookForCwd(t, repo, transcript, tmp)
		if code != cli.ExitOK {
			t.Fatalf("control exit = %d, want ExitOK", code)
		}
		// Identity-only marker + no tier-2 → tier-3 live vault → the hook CAPTURES.
		// A capture artifact is the authoritative proof the run was NOT refused (a
		// refusal captures nothing); the shared vp.log still carries the refusal
		// subtest's line, so this is asserted on the manifest, not the log.
		if after := manifestCount(t, liveVault, "qa"); after <= before {
			t.Errorf("the cleaned marker must let the hook capture into the tier-3 live vault; manifests %d → %d", before, after)
		}
	})
}
