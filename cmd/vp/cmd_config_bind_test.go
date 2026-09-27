// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

const cliBindLabel = "git@example.invalid:team/quantum-vault.git"

// cliBindHost is a hermetic host after a split (see storage's splitHost),
// with two checkouts of "qa".
func cliBindHost(t *testing.T) (cfg, quantum string, checkouts []string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, ".config"))
	global := filepath.Join(home, "global-vault")
	quantum = filepath.Join(home, "quantum-vault")
	for _, v := range []string{global, quantum} {
		if err := surface.WriteFormat(v, surface.RequiredDataFormat); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(quantum, "Projects", "qa", "resume.md"), "# qa\n")
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", cliBindLabel}} {
		cmd := exec.Command("git", append([]string{"-C", quantum}, args...)...)
		cmd.Env = gitenv.SafeGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	b, err := (departure.Record{Slug: "qa", Kind: departure.MovedToVault, To: cliBindLabel, Date: "2026-09-27"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(global, filepath.FromSlash(departure.RelPath("qa"))), string(b))
	cfg, err = storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, cfg, "vault_path = \""+global+"\"\n")
	for _, name := range []string{"qa", "qa-second"} {
		co := filepath.Join(home, "code", name)
		writeTestFile(t, filepath.Join(co, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
		checkouts = append(checkouts, co)
	}
	return cfg, quantum, checkouts
}

// Test 29 (CLI half): `vp config bind` calls the bind and verifies EVERY
// repeated --checkout. MUTATION CONTRACT: drop the BindProjectVault call from
// runConfigBind and this goes RED (nothing is written).
func TestConfigBindVaultCallsBind(t *testing.T) {
	cfg, quantum, cos := cliBindHost(t)
	var out, errOut bytes.Buffer
	code := runConfigBind([]string{"qa", "--vault", "~/quantum-vault", "--checkout", cos[0], "--checkout", cos[1], "--json"}, &out, &errOut)
	if code != cli.ExitOK {
		t.Fatalf("exit %d\nstderr: %s", code, errOut.String())
	}
	var rep storage.BindReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, out.String())
	}
	if len(rep.Checkouts) != 2 {
		t.Errorf("both --checkout values must be verified, got %+v", rep.Checkouts)
	}
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "qa = \"~/quantum-vault\"") {
		t.Errorf("config not bound:\n%s", body)
	}
	for _, co := range cos {
		if p, _, err := storage.ResolveVaultPath(co); err != nil || p != quantum {
			t.Errorf("%s resolves %q, %v; want %q", co, p, err, quantum)
		}
	}
}

// A refused bind exits ExitUser and says why; usage errors do too.
func TestConfigBindRefusesAndValidatesUsage(t *testing.T) {
	cfg, _, _ := cliBindHost(t)
	before, _ := os.ReadFile(cfg)
	for name, args := range map[string][]string{
		"no_slug":        {"--vault", "~/quantum-vault"},
		"no_vault":       {"qa"},
		"refused_target": {"qa", "--vault", "~/no-such-vault"},
	} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runConfigBind(args, &out, &errOut); code != cli.ExitUser || errOut.Len() == 0 {
				t.Errorf("exit %d stderr %q; want ExitUser with a reason", code, errOut.String())
			}
		})
	}
	if after, _ := os.ReadFile(cfg); !bytes.Equal(after, before) {
		t.Error("a refused bind changed the config")
	}
}

// Fold: a repeated --vault refuses, and --checkout=~/… is tilde-expanded.
func TestConfigBindVaultOnceAndTildeCheckout(t *testing.T) {
	cfg, quantum, cos := cliBindHost(t)
	before, _ := os.ReadFile(cfg)
	var out, errOut bytes.Buffer
	// The same, valid vault twice: only the repeat guard can refuse this.
	if code := runConfigBind([]string{"qa", "--vault", "~/quantum-vault", "--vault", "~/quantum-vault"}, &out, &errOut); code != cli.ExitUser {
		t.Errorf("a repeated --vault exited %d, want ExitUser", code)
	}
	if after, _ := os.ReadFile(cfg); !bytes.Equal(after, before) {
		t.Error("a refused bind changed the config")
	}
	home, _ := os.UserHomeDir()
	rel, err := filepath.Rel(home, cos[0])
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := runConfigBind([]string{"qa", "--vault", "~/quantum-vault", "--checkout=~/" + rel, "--json"}, &out, &errOut); code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	var rep storage.BindReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || len(rep.Checkouts) != 1 || rep.Checkouts[0].Resolved != quantum {
		t.Errorf("a ~/ --checkout was not expanded and verified: %+v (%v)", rep.Checkouts, err)
	}
}
