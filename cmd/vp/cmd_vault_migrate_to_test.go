// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// migrateVault builds a published, migrated vault: a work tree with the given
// files plus vault.toml and a remotes.toml recording its origin, pushed to a
// fresh bare that allows blobless fetch. Returns (dir, bare).
func migrateVault(t *testing.T, files map[string]string) (dir, bare string) {
	t.Helper()
	dir, bare = t.TempDir(), t.TempDir()
	copyGit(t, bare, "init", "-q", "--bare", "-b", "main")
	copyGit(t, bare, "config", "uploadpack.allowFilter", "true")
	copyGit(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	copyGit(t, dir, "init", "-q", "-b", "main")
	copyGit(t, dir, "config", "user.email", "t@example.com")
	copyGit(t, dir, "config", "user.name", "T")
	files[".vibe-palace/vault.toml"] = fmt.Sprintf("format = %d\nauthored_only = \"2026-10-04\"\n", surface.RequiredDataFormat)
	files[".vibe-palace/remotes.toml"] = "[[remote]]\nname = \"origin\"\nurl = \"file://" + bare + "\"\n"
	for rel, c := range files {
		copyWrite(t, dir, rel, c)
	}
	copyGit(t, dir, "add", "-A")
	copyGit(t, dir, "commit", "-q", "-m", "seed")
	copyGit(t, dir, "remote", "add", "origin", "file://"+bare)
	copyGit(t, dir, "push", "-q", "origin", "main")
	return dir, bare
}

type migrateHost struct {
	home, cfg              string
	source, srcBare        string
	dest, destBare         string
	checkout, marker, slug string
}

// newMigrateHost builds a hermetic host: a default (source) vault holding "qa",
// a destination vault without it, a global config bound to the source, and a
// checkout of qa. It chdirs into the checkout.
func newMigrateHost(t *testing.T) *migrateHost {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	source, srcBare := migrateVault(t, map[string]string{
		"Projects/qa/resume.md":   "# qa\n",
		"palace/qa/drawers.jsonl": "{}\n",
	})
	dest, destBare := migrateVault(t, map[string]string{"Projects/resident/resume.md": "r\n"})

	cfg, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	copyWrite(t, filepath.Dir(cfg), filepath.Base(cfg), "vault_path = \""+source+"\"\n")

	checkout := filepath.Join(home, "code", "qa")
	marker := filepath.Join(checkout, ".vibe-palace.toml")
	copyWrite(t, checkout, ".vibe-palace.toml", "[project]\nname = \"qa\"\n")

	old, _ := os.Getwd()
	if err := os.Chdir(checkout); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	return &migrateHost{
		home: home, cfg: cfg, source: source, srcBare: srcBare,
		dest: dest, destBare: destBare, checkout: checkout, marker: marker, slug: "qa",
	}
}

func runMigrate(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runVaultMigrateTo(cmdVaultMigrateTo(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func headHasTree(t *testing.T, dir, tree string) bool {
	t.Helper()
	return strings.Contains(copyGit(t, dir, "ls-tree", "-r", "--name-only", "HEAD"), tree)
}

// TestMigrateTo_EndToEnd walks plan -> copy -> delete and asserts the move
// landed, the binding is the global tier, the tracked marker is byte-unchanged,
// and the fleet follow-up prints pull before clone --bind.
func TestMigrateTo_EndToEnd(t *testing.T) {
	h := newMigrateHost(t)
	markerBefore, err := os.ReadFile(h.marker)
	if err != nil {
		t.Fatal(err)
	}

	// Stage A: PLAN. No git-tip mutation; full blast radius named.
	code, out, errOut := runMigrate(t, h.dest)
	if code != cli.ExitOK {
		t.Fatalf("plan exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "migrate-to PLAN") || !strings.Contains(out, "copy digest:") {
		t.Fatalf("plan output missing pieces:\n%s", out)
	}
	if !strings.Contains(out, "--copy") {
		t.Fatalf("plan did not print the --copy next line:\n%s", out)
	}
	if headHasTree(t, h.dest, "Projects/qa/") {
		t.Fatal("plan wrote the project to the destination")
	}

	// Stage B: COPY, then HARD STOP with the delete plan.
	code, out, errOut = runMigrate(t, h.dest, "--copy")
	if code != cli.ExitOK {
		t.Fatalf("copy exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "HARD STOP") || !strings.Contains(out, "--delete") {
		t.Fatalf("copy did not hard-stop into the delete plan:\n%s", out)
	}
	if !headHasTree(t, h.dest, "Projects/qa/") {
		t.Fatal("copy did not land the project at the destination HEAD")
	}
	if !headHasTree(t, h.source, "Projects/qa/") {
		t.Fatal("copy must not touch the source (delete is a separate stage)")
	}

	// Stage C: DELETE + bind + fleet.
	code, out, errOut = runMigrate(t, h.dest, "--delete")
	if code != cli.ExitOK {
		t.Fatalf("delete exit %d: %s\nstdout:%s", code, errOut, out)
	}
	if headHasTree(t, h.source, "Projects/qa/") {
		t.Fatal("delete did not remove the project from the source")
	}
	if _, found := departure.Read(h.source, "qa"); !found {
		t.Fatal("delete did not write a departure record in the source")
	}
	cfgBytes, _ := os.ReadFile(h.cfg)
	if !strings.Contains(string(cfgBytes), "qa = \""+h.dest+"\"") {
		t.Fatalf("binding not written to the global config:\n%s", cfgBytes)
	}
	// Fleet order: pull BEFORE clone --bind.
	iPull := strings.Index(out, "vp vault pull")
	iClone := strings.Index(out, "clone")
	if iPull < 0 || iClone < 0 || iPull > iClone {
		t.Fatalf("fleet follow-up must print pull before clone --bind:\n%s", out)
	}
	if !strings.Contains(out, "--bind qa") {
		t.Fatalf("fleet follow-up missing clone --bind qa:\n%s", out)
	}

	// No-tracked-vault_path: the tracked marker is byte-unchanged and carries no vault_path.
	markerAfter, err := os.ReadFile(h.marker)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(markerBefore, markerAfter) {
		t.Fatalf("the tracked marker changed:\nbefore=%q\nafter=%q", markerBefore, markerAfter)
	}
	if strings.Contains(string(markerAfter), "vault_path") {
		t.Fatalf("the tracked marker gained a vault_path:\n%s", markerAfter)
	}

	// Resume idempotency: a second full run is a no-op (already bound to dest).
	code, out, errOut = runMigrate(t, h.dest)
	if code != cli.ExitOK || !strings.Contains(out, "already bound") {
		t.Fatalf("re-run not a no-op: exit %d out=%q err=%q", code, out, errOut)
	}
}

// TestMigrateTo_CopyDeleteTogetherRefused is the hard-stop collapse guard: the
// two mutating stages can never be one invocation.
func TestMigrateTo_CopyDeleteTogetherRefused(t *testing.T) {
	h := newMigrateHost(t)
	code, _, errOut := runMigrate(t, h.dest, "--copy", "--delete")
	if code != cli.ExitUser {
		t.Fatalf("exit %d, want ExitUser", code)
	}
	if !strings.Contains(errOut, "cannot be combined") {
		t.Fatalf("refusal did not name the collapse guard:\n%s", errOut)
	}
}

// TestMigrateTo_NonDefaultSourceRefused_CwdLocal: a tier-1 .vibe-palace/local.toml
// override (source `cwd-local:`) makes the checkout resolve non-default; v1 refuses.
func TestMigrateTo_NonDefaultSourceRefused_CwdLocal(t *testing.T) {
	h := newMigrateHost(t)
	// Point the override somewhere that is NOT dest so the no-op does not fire first.
	other := t.TempDir()
	copyWrite(t, filepath.Join(h.checkout, ".vibe-palace"), "local.toml", "vault_path = \""+other+"\"\n")
	code, _, errOut := runMigrate(t, h.dest)
	if code != cli.ExitUser {
		t.Fatalf("exit %d, want ExitUser", code)
	}
	if !strings.Contains(errOut, "default vault") {
		t.Fatalf("refusal did not cite default-source-only:\n%s", errOut)
	}
}

// TestMigrateTo_NonDefaultSourceRefused_Binding: a global [project_vaults].<slug>
// entry makes the checkout resolve `binding:` to a third vault; v1 refuses.
func TestMigrateTo_NonDefaultSourceRefused_Binding(t *testing.T) {
	h := newMigrateHost(t)
	// A third vault that actually holds qa, so the resolver's stale-binding guard
	// passes and the source resolves `binding:` (not an error).
	third, _ := migrateVault(t, map[string]string{"Projects/qa/resume.md": "# qa\n", "palace/qa/drawers.jsonl": "{}\n"})
	appendFile(t, h.cfg, "\n[project_vaults]\nqa = \""+third+"\"\n")
	code, _, errOut := runMigrate(t, h.dest)
	if code != cli.ExitUser {
		t.Fatalf("exit %d, want ExitUser", code)
	}
	if !strings.Contains(errOut, "default vault") {
		t.Fatalf("refusal did not cite default-source-only (binding: source):\n%s", errOut)
	}
}

// TestMigrateTo_NonDefaultSourceRefused_CwdMarker: an UNTRACKED tracked-marker
// vault_path (source `cwd:`, the deprecation-window tier-1 lever) is non-default.
func TestMigrateTo_NonDefaultSourceRefused_CwdMarker(t *testing.T) {
	h := newMigrateHost(t)
	// Make the checkout a git repo and give its marker a vault_path, left UNTRACKED
	// so the resolver honours it as tier-1 `cwd:` rather than the U4 tracked refusal.
	copyGit(t, h.checkout, "init", "-q", "-b", "main")
	copyGit(t, h.checkout, "config", "user.email", "t@example.com")
	copyGit(t, h.checkout, "config", "user.name", "T")
	other := t.TempDir()
	// vault_path at TOP LEVEL (above [project]) so TOML does not scope it into the table.
	copyWrite(t, h.checkout, ".vibe-palace.toml", "vault_path = \""+other+"\"\n[project]\nname = \"qa\"\n")
	code, _, errOut := runMigrate(t, h.dest)
	if code != cli.ExitUser {
		t.Fatalf("exit %d, want ExitUser", code)
	}
	if !strings.Contains(errOut, "default vault") {
		t.Fatalf("refusal did not cite default-source-only (cwd: source):\n%s", errOut)
	}
}

// TestMigrateTo_DestAbsentRefused: creation is out of scope.
func TestMigrateTo_DestAbsentRefused(t *testing.T) {
	h := newMigrateHost(t)
	absent := filepath.Join(h.home, "not-a-vault")
	code, _, errOut := runMigrate(t, absent)
	if code != cli.ExitUser {
		t.Fatalf("exit %d, want ExitUser", code)
	}
	if !strings.Contains(errOut, "vault init") {
		t.Fatalf("refusal did not point at vault init/clone:\n%s", errOut)
	}
}

// TestMigrateTo_RemoteMatchPreflightRefusesBeforeDelete: the F2 preflight refuses
// before ApplyDelete. The tampered --moved-to is FETCHABLE and holds a verified
// copy (so ApplyDelete would proceed if it ran) but is NOT a live remote of dest,
// so only the preflight can explain the source staying intact. (Reordering
// ApplyDelete before the preflight makes this test RED — it would delete.)
func TestMigrateTo_RemoteMatchPreflightRefusesBeforeDelete(t *testing.T) {
	h := newMigrateHost(t)
	if code, _, errOut := runMigrate(t, h.dest, "--copy"); code != cli.ExitOK {
		t.Fatalf("copy exit %d: %s", code, errOut)
	}
	// A second reachable bare that MIRRORS dest (so it holds the verified copy),
	// but is not a configured remote of dest.
	other := t.TempDir()
	copyGit(t, other, "init", "-q", "--bare", "-b", "main")
	copyGit(t, other, "config", "uploadpack.allowFilter", "true")
	copyGit(t, other, "config", "uploadpack.allowAnySHA1InWant", "true")
	copyGit(t, h.dest, "push", "-q", "file://"+other, "main")
	// Point the dest's RECORDED remote at `other`; its LIVE remote stays destBare.
	copyWrite(t, filepath.Join(h.dest, ".vibe-palace"), "remotes.toml",
		"[[remote]]\nname = \"origin\"\nurl = \"file://"+other+"\"\n")

	code, _, errOut := runMigrate(t, h.dest, "--delete")
	if code != cli.ExitUser {
		t.Fatalf("exit %d, want ExitUser (preflight refuse)", code)
	}
	if !strings.Contains(errOut, "no live remote") && !strings.Contains(errOut, "matches no live remote") {
		t.Fatalf("refusal did not name the remote mismatch:\n%s", errOut)
	}
	// Source untouched — only the preflight (not a failed fetch) explains this,
	// because `other` is fetchable and holds the copy.
	if !headHasTree(t, h.source, "Projects/qa/") {
		t.Fatal("the source project was deleted despite the preflight refusal")
	}
	if _, found := departure.Read(h.source, "qa"); found {
		t.Fatal("a departure record was written despite the preflight refusal")
	}
}

// appendFile appends to an existing file (copyWrite overwrites).
func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// TestMigrateTo_AlreadyBoundElsewhere_PreflightRefuses pins M1 directly: with a
// foreign [project_vaults].<slug>, the delete PREFLIGHT refuses (before
// ApplyDelete) naming [project_vaults]. Removing the M1 check makes this RED (a
// later check gives a different message).
func TestMigrateTo_AlreadyBoundElsewhere_PreflightRefuses(t *testing.T) {
	h := newMigrateHost(t)
	third, _ := migrateVault(t, map[string]string{"Projects/qa/resume.md": "# qa\n"})
	appendFile(t, h.cfg, "\n[project_vaults]\nqa = \""+third+"\"\n")
	m := &migrateContext{
		source: h.source, slug: "qa", dest: h.dest, checkout: h.checkout,
		destRemoteURL: "file://" + h.destBare,
	}
	var errOut bytes.Buffer
	if code := migrateDeletePreflight("vp vault migrate-to", m, &errOut); code != cli.ExitUser {
		t.Fatalf("preflight exit %d, want ExitUser\n%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "[project_vaults]") {
		t.Fatalf("refusal did not name the foreign binding:\n%s", errOut.String())
	}
}

// TestMigrateTo_AlreadyBoundElsewhere_SourceIntact is the Chair's CLI break-it
// test: a foreign binding must never leave the project deleted-and-unbound. (The
// foreign binding is caught at source resolution for the forward path; either
// way, source HEAD and the departure record are untouched.)
func TestMigrateTo_AlreadyBoundElsewhere_SourceIntact(t *testing.T) {
	h := newMigrateHost(t)
	third, _ := migrateVault(t, map[string]string{"Projects/qa/resume.md": "# qa\n", "palace/qa/drawers.jsonl": "{}\n"})
	appendFile(t, h.cfg, "\n[project_vaults]\nqa = \""+third+"\"\n")
	code, _, _ := runMigrate(t, h.dest, "--delete")
	if code == cli.ExitOK {
		t.Fatal("a foreign binding must not let --delete succeed")
	}
	if !headHasTree(t, h.source, "Projects/qa/") {
		t.Fatal("the source project was deleted under a foreign binding")
	}
	if _, found := departure.Read(h.source, "qa"); found {
		t.Fatal("a departure record was written under a foreign binding")
	}
}

// TestMigrateTo_BlastRadiusNamesUnrecordedRemote (M2): a configured remote absent
// from remotes.toml must appear in the printed blast radius, in the plan and in
// --copy. RED if printBlastRadius prints only recorded names.
func TestMigrateTo_BlastRadiusNamesUnrecordedRemote(t *testing.T) {
	h := newMigrateHost(t)
	backup := t.TempDir()
	copyGit(t, backup, "init", "-q", "--bare", "-b", "main")
	copyGit(t, backup, "config", "uploadpack.allowFilter", "true")
	copyGit(t, backup, "config", "uploadpack.allowAnySHA1InWant", "true")
	copyGit(t, h.dest, "remote", "add", "backup", "file://"+backup) // NOT in remotes.toml
	copyGit(t, h.dest, "push", "-q", "backup", "main")              // keep it in sync for the copy

	_, planOut, _ := runMigrate(t, h.dest)
	if !strings.Contains(planOut, "backup") {
		t.Fatalf("plan blast radius omitted the unrecorded remote:\n%s", planOut)
	}
	code, copyOut, errOut := runMigrate(t, h.dest, "--copy")
	if code != cli.ExitOK {
		t.Fatalf("copy exit %d: %s", code, errOut)
	}
	if !strings.Contains(copyOut, "backup") {
		t.Fatalf("--copy blast radius omitted the unrecorded remote:\n%s", copyOut)
	}
}

// TestMigrateTo_DepartureToMismatchRefused (M3): an existing moved-to-vault record
// whose To normalises elsewhere refuses before ApplyDelete. RED if the
// recNorm!=wantNorm refuse is deleted.
func TestMigrateTo_DepartureToMismatchRefused(t *testing.T) {
	h := newMigrateHost(t)
	rec := departure.Record{Slug: "qa", Kind: departure.MovedToVault, To: "git@other.example:different/x.git", Date: "2026-10-26"}
	b, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	copyWrite(t, filepath.Join(h.source, filepath.FromSlash(departure.RelPath("qa"))), "", string(b))

	code, _, errOut := runMigrate(t, h.dest, "--delete")
	if code != cli.ExitUser {
		t.Fatalf("exit %d, want ExitUser", code)
	}
	if !strings.Contains(errOut, "not the selected destination") {
		t.Fatalf("refusal did not name the To mismatch:\n%s", errOut)
	}
	if !headHasTree(t, h.source, "Projects/qa/") {
		t.Fatal("the source project was deleted despite the To-mismatch refusal")
	}
}

// TestMigrateTo_CopyRequestArgsAreStable (N3): the CopyRequest inputs
// (cleaned-abs vault + chosen remote URL) are byte-identical across independent
// stage-B builds, so copyRerunLine matches on a resume.
func TestMigrateTo_CopyRequestArgsAreStable(t *testing.T) {
	h := newMigrateHost(t)
	d1, err := cleanAbsVaultArg(h.dest)
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := cleanAbsVaultArg(h.dest)
	if d1 != d2 || !filepath.IsAbs(d1) || d1 != filepath.Clean(d1) {
		t.Fatalf("cleanAbsVaultArg not stable/clean: %q vs %q", d1, d2)
	}
	order, err := storage.PublishOrder(h.source)
	if err != nil {
		t.Fatal(err)
	}
	u1, err := pickRemoteURL(h.source, order, "")
	if err != nil {
		t.Fatal(err)
	}
	u2, _ := pickRemoteURL(h.source, order, "")
	if u1 != u2 {
		t.Fatalf("pickRemoteURL not stable: %q vs %q", u1, u2)
	}
}
