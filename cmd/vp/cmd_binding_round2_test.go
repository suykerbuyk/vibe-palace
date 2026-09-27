// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// bindingHostEnv is a hermetic host: HOME/XDG under tmp, a live (global)
// vault, a bound vault for project "qa", and a checkout directory.
type bindingHostEnv struct {
	tmp, cfg, live, bound, proj string
}

func newBindingHostEnv(t *testing.T) bindingHostEnv {
	t.Helper()
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	e := bindingHostEnv{
		tmp:   tmp,
		cfg:   filepath.Join(home, ".config", "vibe-palace", "config.toml"),
		live:  filepath.Join(tmp, "live-vault"),
		bound: filepath.Join(tmp, "bound-vault"),
		proj:  filepath.Join(tmp, "qa-checkout"),
	}
	for _, d := range []string{filepath.Dir(e.cfg), e.proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustVault(t, e.live)
	mustVault(t, e.bound)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, ".config"))
	e.writeConfig(t, "qa = \""+e.bound+"\"\n")
	return e
}

// writeConfig writes the global config: the live vault, git disabled (no
// commit or push can leave the fixture), then a [project_vaults] table.
func (e bindingHostEnv) writeConfig(t *testing.T, bindings string) {
	t.Helper()
	writeTestFile(t, e.cfg, "vault_path = \""+e.live+"\"\ngit_enabled = false\n\n[project_vaults]\n"+bindings)
}

// runHookPayload feeds payload to runHook on stdin.
func runHookPayload(t *testing.T, tmp string, payload map[string]string) int {
	t.Helper()
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
	orig := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = orig })
	code := runHook(cli.BuildInfo{Version: "test"})
	vplog.Close()
	return code
}

// countFiles counts regular files under dir (0 when dir is absent).
func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			n++
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return n
}

// C1: a bound project's marker with one syntax error captures NOTHING — not
// into the bound vault, and above all not into the live one. MUTATION
// CONTRACT: return the marker parse error untyped and this goes RED (the
// hook's global fallback captures into the live vault).
func TestHookBrokenMarkerOfBoundProjectCapturesNothing(t *testing.T) {
	e := newBindingHostEnv(t)
	writeTestFile(t, filepath.Join(e.proj, ".vibe-palace.toml"), "[project]\nname = \"qa\"\ntags = [\n")

	code := runHookPayload(t, e.tmp, map[string]string{"hook_event_name": "Stop", "session_id": "broken", "cwd": e.proj})

	if n := countFiles(t, filepath.Join(e.live, "Projects")); n != 0 {
		t.Fatalf("a broken marker of a bound project captured %d file(s) into the LIVE vault", n)
	}
	if n := countFiles(t, filepath.Join(e.bound, "Projects")); n != 0 {
		t.Errorf("a broken marker captured %d file(s) into the bound vault", n)
	}
	if code != cli.ExitOK {
		t.Errorf("exit %d, want ExitOK", code)
	}
}

// C5: `vp memory harvest` in a bound checkout writes the BOUND vault, never
// the live one. MUTATION CONTRACT: resolve the root with the global-only
// vaultRoot() again and this goes RED.
func TestMemoryHarvestCLIWritesTheBoundVault(t *testing.T) {
	live, cfgPath := harvestCLIFixture(t, "git_enabled = false")
	bound := filepath.Join(t.TempDir(), "bound-vault")
	mustVault(t, bound)
	writeTestFile(t, cfgPath, "vault_path = \""+live+"\"\ngit_enabled = false\n\n[project_vaults]\nharvp = \""+bound+"\"\n")

	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdMemoryHarvest())
	if code != cli.ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if n := countFiles(t, filepath.Join(live, "Projects", "harvp", "memory")); n != 0 {
		t.Fatalf("harvest wrote %d memory file(s) into the LIVE vault", n)
	}
	if n := countFiles(t, filepath.Join(bound, "Projects", "harvp", "memory")); n == 0 {
		t.Errorf("harvest wrote nothing into the bound vault\nstdout: %s", stdout)
	}
}

// C5 pin: the SessionEnd hook harvests into the vault the hook resolved
// (hook.go passes opts.VaultRoot), so a bound checkout's memory lands in the
// bound vault. MUTATION CONTRACT: open the global vault in runHook and this
// goes RED.
func TestHookSessionEndHarvestsIntoTheBoundVault(t *testing.T) {
	e := newBindingHostEnv(t)
	writeTestFile(t, filepath.Join(e.proj, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
	sessDir := filepath.Join(e.tmp, "claude", "projects", "qa")
	transcript := filepath.Join(sessDir, "sess.jsonl")
	writeTestFile(t, transcript, "")
	if err := memorytestutil.WriteNativeMemoryFixture(filepath.Join(sessDir, "memory")); err != nil {
		t.Fatal(err)
	}

	runHookPayload(t, e.tmp, map[string]string{
		"hook_event_name": "SessionEnd", "session_id": "harvest", "cwd": e.proj, "transcript_path": transcript,
	})

	if n := countFiles(t, filepath.Join(e.live, "Projects", "qa", "memory")); n != 0 {
		t.Fatalf("SessionEnd harvested %d memory file(s) into the LIVE vault", n)
	}
	if n := countFiles(t, filepath.Join(e.bound, "Projects", "qa", "memory")); n == 0 {
		t.Errorf("SessionEnd harvested nothing into the bound vault")
	}
}

// G12: `vp check --check` reports a rejected binding as an error, never
// degrading it to "no vault configured". C4: a present-but-unparseable host
// config still runs the checks and leads with a Config row.
func TestRunSelectedChecksBindingRefusalAndUnreadableConfig(t *testing.T) {
	t.Run("rejected_binding_is_an_error", func(t *testing.T) {
		e := newBindingHostEnv(t)
		e.writeConfig(t, "qa = \""+filepath.Join(e.tmp, "no-such-vault")+"\"\n")
		writeTestFile(t, filepath.Join(e.proj, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
		t.Chdir(e.proj)
		if _, err := runSelectedChecks("surface"); !errors.Is(err, storage.ErrVaultBindingRejected) {
			t.Fatalf("err = %v, want ErrVaultBindingRejected", err)
		}
	})
	t.Run("unreadable_config_still_runs", func(t *testing.T) {
		e := newBindingHostEnv(t)
		writeTestFile(t, e.cfg, "vault_path = \""+e.live+"\"\n[project_vaults\n")
		writeTestFile(t, filepath.Join(e.proj, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
		t.Chdir(e.proj)
		rows, err := runSelectedChecks("surface")
		if err != nil {
			t.Fatalf("an unreadable host config stopped the preflight: %v", err)
		}
		if len(rows) < 2 || rows[0].Name != "Config" || rows[0].Status != check.Fail {
			t.Fatalf("rows = %+v, want a leading Config Fail row then the checks", rows)
		}
	})
}

// G17: the full `vp check` closes with a Surface row SKIPPED for a rejected
// binding, not "no vault configured".
func TestGatherCheckResultsSkipsSurfaceOnRejectedBinding(t *testing.T) {
	e := newBindingHostEnv(t)
	e.writeConfig(t, "qa = \""+filepath.Join(e.tmp, "no-such-vault")+"\"\n")
	writeTestFile(t, filepath.Join(e.proj, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
	t.Chdir(e.proj)
	rows := gatherCheckResults()
	last := rows[len(rows)-1]
	if last.Name != "Surface" || last.Status != check.Skip || !strings.Contains(last.Summary, "rejected") {
		t.Errorf("closing row = %+v, want Surface Skip naming the rejection", last)
	}
}

// G15: `vp plans scan` states the rejected binding instead of silently
// labelling every candidate "unmanaged".
func TestPlansScanStatesARejectedBinding(t *testing.T) {
	e := newBindingHostEnv(t)
	e.writeConfig(t, "qa = \""+filepath.Join(e.tmp, "no-such-vault")+"\"\n")
	writeTestFile(t, filepath.Join(e.proj, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
	setClaudeHome(t, filepath.Join(e.tmp, "claude"))
	t.Chdir(e.proj)
	out, code := runPlansScan(t, "--json")
	if code != cli.ExitOK {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out, "vault binding rejected") {
		t.Errorf("plans scan does not state the rejected binding:\n%s", out)
	}
}

// R2-1: a host config that is there and unreadable captures nothing and exits
// 0 — never ExitSystem, which blocks the turn, and never the global fallback.
// MUTATION CONTRACT: drop ErrHostConfigUnreadable from runHook's refusal arm
// and this goes RED on the exit code.
func TestHookUnreadableConfigCapturesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	e := newBindingHostEnv(t)
	writeTestFile(t, filepath.Join(e.proj, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
	if err := os.Chmod(e.cfg, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.cfg, 0o644) })

	code := runHookPayload(t, e.tmp, map[string]string{"hook_event_name": "Stop", "session_id": "unreadable", "cwd": e.proj})

	if code != cli.ExitOK {
		t.Errorf("exit %d, want ExitOK: a hook must never block the turn on an unreadable config", code)
	}
	for _, v := range []string{e.live, e.bound} {
		if n := countFiles(t, filepath.Join(v, "Projects")); n != 0 {
			t.Errorf("an unreadable config captured %d file(s) into %s", n, v)
		}
	}
}
