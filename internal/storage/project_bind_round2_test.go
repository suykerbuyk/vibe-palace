// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// racingGit puts a git wrapper first on PATH that appends appendText to file
// whenever its arguments contain trigger, then runs the real git.
func racingGit(t *testing.T, home, trigger, file, appendText string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	bin := filepath.Join(home, "racing-bin")
	body := "#!/bin/sh\ncase \"$*\" in *" + trigger + "*) printf '%s' '" + appendText + "' >> '" + file + "';; esac\nexec " + realGit + " \"$@\"\n"
	rebindWrite(t, filepath.Join(bin, "git"), body)
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// K2: a binding written by another process while the bind asks the target for
// its remotes is never overwritten — the bindings are parsed from the bytes
// the compare-and-set compares against. MUTATION CONTRACT: parse the bindings
// from one read and splice a second, and the other writer's binding is lost.
func TestBindCannotBeRacedIntoARepoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh wrapper")
	}
	home := rebindEnv(t)
	splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	other := rebindVault(t, filepath.Join(home, "other-vault"), "qa")
	racingGit(t, home, "get-regexp", cfg, "\n[project_vaults]\nqa = \""+other+"\"\n")

	_, err := BindProjectVault(movedReq("qa"))
	if err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("want a compare-and-set refusal, got %v", err)
	}
	if got := bindRead(t, cfg); !strings.Contains(got, "qa = \""+other+"\"") || strings.Contains(got, "quantum-vault") {
		t.Errorf("the other writer's binding was overwritten:\n%s", got)
	}
}

// K2 (rename): a config change while the rename greps the checkout refuses the
// rename; the other writer's bytes survive and the marker is untouched.
func TestRebindRenameCannotBeRaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh wrapper")
	}
	home := rebindEnv(t)
	global := rebindVault(t, filepath.Join(home, "global-vault"))
	quantum := rebindVault(t, filepath.Join(home, "quantum-vault"), "new-slug")
	cfg, _ := VaultConfigFilePath()
	rebindWrite(t, cfg, "vault_path = \""+global+"\"\n\n[project_vaults]\nold-slug = \"~/quantum-vault\"\n")
	co := filepath.Join(home, "code", "proj")
	tp := filepath.Join(co, ".vibe-palace.toml")
	rebindWrite(t, tp, rebindToml)
	rebindGit(t, co, "init", "-q")
	racingGit(t, home, "grep", cfg, "# another writer\n")

	if _, err := RebindCheckout(rebindRename(co, quantum)); err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("want a compare-and-set refusal, got %v", err)
	}
	if got := bindRead(t, cfg); !strings.Contains(got, "# another writer") || strings.Contains(got, "new-slug") {
		t.Errorf("the rename overwrote the other writer:\n%s", got)
	}
	if bindRead(t, tp) != rebindToml {
		t.Error("a refused rename changed the marker")
	}
}

// K2: the splice itself never re-points a key that is already there.
func TestSpliceNeverRepoints(t *testing.T) {
	if _, _, err := spliceProjectVault("vault_path = \"/g\"\n[project_vaults]\nqa = \"/old\"\n", "qa", "/new"); err == nil ||
		!strings.Contains(err.Error(), "already binds") {
		t.Errorf("splice over an existing key = %v, want a refusal", err)
	}
}

// Fold: a table written without a header (dotted keys, inline table) cannot
// take an appended header — invalid TOML — so the bind refuses.
func TestBindRefusesAHeaderlessTable(t *testing.T) {
	home := rebindEnv(t)
	global, _ := splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	other := rebindVault(t, filepath.Join(home, "other-vault"))
	for name, body := range map[string]string{
		"dotted": "project_vaults.zz = \"" + other + "\"\n",
		"inline": "project_vaults = { zz = \"" + other + "\" }\n",
	} {
		t.Run(name, func(t *testing.T) {
			rebindWrite(t, cfg, "vault_path = \""+global+"\"\n"+body)
			before := bindRead(t, cfg)
			if _, err := BindProjectVault(movedReq("qa")); err == nil || !strings.Contains(err.Error(), "without a [project_vaults] header") {
				t.Fatalf("want a headerless-table refusal, got %v", err)
			}
			if bindRead(t, cfg) != before {
				t.Error("a refused bind changed the config")
			}
		})
	}
}

// K3: every spelling of one repository normalises to the same host/path; the
// host is case-folded, the path is not; a non-URL is no match at all.
func TestNormaliseRemoteURL(t *testing.T) {
	const want = "gitlab.mdh.quantum.com/quantum/vibe-palace-vault"
	for _, u := range []string{
		"git@gitlab.mdh.quantum.com:quantum/vibe-palace-vault.git",
		"gitlab.mdh.quantum.com:quantum/vibe-palace-vault",
		"ssh://git@gitlab.mdh.quantum.com/quantum/vibe-palace-vault.git",
		"ssh://git@GitLab.MDH.quantum.com:22/quantum/vibe-palace-vault",
		"https://gitlab.mdh.quantum.com/quantum/vibe-palace-vault/",
		"https://user@gitlab.mdh.quantum.com/quantum/vibe-palace-vault.git",
	} {
		if got, ok := normaliseRemoteURL(u); !ok || got != want {
			t.Errorf("normaliseRemoteURL(%q) = (%q, %v), want %q", u, got, ok, want)
		}
	}
	for _, u := range []string{
		"git@gitlab.mdh.quantum.com:Quantum/vibe-palace-vault.git", // path case matters
		"git@gitlab.mdh.quantum.com:quantum/other-vault.git",
		"git@github.com:quantum/vibe-palace-vault.git",
	} {
		if got, _ := normaliseRemoteURL(u); got == want {
			t.Errorf("normaliseRemoteURL(%q) = %q; a different repository must not match", u, got)
		}
	}
	for _, u := range []string{"", "quantum vault", "/abs/local/path", "relative/path"} {
		if got, ok := normaliseRemoteURL(u); ok {
			t.Errorf("normaliseRemoteURL(%q) = %q, ok; want not a URL", u, got)
		}
	}
}

// K3: moved mode matches the recorded destination against the target's remotes
// in any spelling of the same repository, and still refuses a different one or
// a non-URL label. MUTATION CONTRACT: compare raw strings again and the
// spelling subtests go RED.
func TestBindMatchesRemoteSpellings(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git unavailable")
	}
	for name, c := range map[string]struct {
		remote string
		want   string // "" = binds
	}{
		"ssh_url":         {"ssh://git@example.invalid/team/quantum-vault.git", ""},
		"https_no_suffix": {"https://Example.Invalid/team/quantum-vault", ""},
		"trailing_slash":  {"https://example.invalid/team/quantum-vault/", ""},
		"path_case":       {"git@example.invalid:Team/quantum-vault.git", "no remote of"},
		"other_repo":      {"git@example.invalid:team/other.git", "no remote of"},
	} {
		t.Run(name, func(t *testing.T) {
			home := rebindEnv(t)
			_, quantum := splitHost(t, home, "qa")
			rebindGit(t, quantum, "remote", "set-url", "origin", c.remote)
			_, err := BindProjectVault(movedReq("qa"))
			switch {
			case c.want == "" && err != nil:
				t.Errorf("remote %q must match the record %q: %v", c.remote, splitLabel, err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("remote %q: err = %v, want one containing %q", c.remote, err, c.want)
			}
		})
	}
	t.Run("non_url_label", func(t *testing.T) {
		home := rebindEnv(t)
		global, _ := splitHost(t, home, "qa")
		rebindWrite(t, filepath.Join(global, filepath.FromSlash(departure.RelPath("qa"))),
			`{"format":"vp-departure/1","slug":"qa","kind":"moved-to-vault","to":"the quantum vault","date":"2026-09-27"}`+"\n")
		if _, err := BindProjectVault(movedReq("qa")); err == nil || !strings.Contains(err.Error(), "not a git URL") {
			t.Errorf("a non-URL label must refuse, got %v", err)
		}
	})
}

// K4: a 0600 config stays 0600 (its .bak too), and a symlinked config is
// written THROUGH to its target; the link survives. MUTATION CONTRACT: write
// 0644, or rename over the link, and this goes RED.
func TestBindKeepsConfigModeAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes and symlinks")
	}
	home := rebindEnv(t)
	global := rebindVault(t, filepath.Join(home, "global-vault"))
	rebindVault(t, filepath.Join(home, "q"))
	cfg, _ := VaultConfigFilePath()
	real := filepath.Join(home, "dotfiles", "config.toml")
	rebindWrite(t, real, "vault_path = \""+global+"\"\n")
	if err := os.Chmod(real, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, cfg); err != nil {
		t.Fatal(err)
	}

	if _, err := BindProjectVault(BindRequest{Slug: "fresh", VaultPath: "~/q", Mode: BindNew}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if fi, err := os.Lstat(cfg); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the config symlink was replaced (mode %v, err %v)", fi.Mode(), err)
	}
	if !strings.Contains(bindRead(t, real), "fresh = ") {
		t.Errorf("the symlink target does not carry the binding:\n%s", bindRead(t, real))
	}
	for _, p := range []string{real, cfg + ".bak"} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v (err %v), want 0600", p, st.Mode().Perm(), err)
		}
	}
}

// K4: a restore after a failed verification is a compare-and-set against the
// bytes the bind wrote — a file another writer changed meanwhile is left
// alone. MUTATION CONTRACT: restore blindly and the other writer's line is
// lost.
func TestBindRestoreDoesNotOverwriteAnotherWriter(t *testing.T) {
	home := rebindEnv(t)
	splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	other := rebindVault(t, filepath.Join(home, "other-vault"))
	co := filepath.Join(home, "code", "qa")
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), "vault_path = \""+other+"\"\n[project]\nname = \"qa\"\n")
	orig := bindBeforeVerify
	t.Cleanup(func() { bindBeforeVerify = orig })
	bindBeforeVerify = func() { rebindWrite(t, cfg, bindRead(t, cfg)+"# another writer\n") }

	_, err := BindProjectVault(movedReq("qa", co))
	if err == nil || !strings.Contains(err.Error(), "NOT restored") {
		t.Fatalf("want a verification failure whose restore refused, got %v", err)
	}
	if !strings.Contains(bindRead(t, cfg), "# another writer") {
		t.Error("the restore overwrote another writer's change")
	}
}

// K4 unit: restoreHostLocalCAS restores only its own bytes.
func TestRestoreHostLocalCAS(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.toml")
	rebindWrite(t, p, "mine\n")
	if err := restoreHostLocalCAS(p, []byte("mine\n"), []byte("prev\n")); err != nil || bindRead(t, p) != "prev\n" {
		t.Errorf("own bytes: err %v, file %q", err, bindRead(t, p))
	}
	rebindWrite(t, p, "theirs\n")
	if err := restoreHostLocalCAS(p, []byte("mine\n"), []byte("prev\n")); err == nil || bindRead(t, p) != "theirs\n" {
		t.Errorf("changed file: err %v, file %q", err, bindRead(t, p))
	}
}

// B7: a target that itself records the slug as departed refuses.
func TestBindRefusesATargetThatRecordsTheSlugDeparted(t *testing.T) {
	home := rebindEnv(t)
	_, quantum := splitHost(t, home, "qa")
	if err := os.RemoveAll(filepath.Join(quantum, "Projects", "qa")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewVault(quantum).RecordDeparture("qa", departure.MovedToVault, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := BindProjectVault(movedReq("qa")); err == nil || !strings.Contains(err.Error(), "holds a departure record for it") {
		t.Errorf("want a refusal for a target that records the slug departed, got %v", err)
	}
}

// B10: the postcondition runs BEFORE the write. A multi-line string inside the
// table makes the line splice land inside it; the postcondition sees another
// key change and refuses, and nothing is written.
func TestBindPostconditionRunsBeforeTheWrite(t *testing.T) {
	home := rebindEnv(t)
	global, _ := splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	rebindWrite(t, cfg, "vault_path = \""+global+"\"\n\n[project_vaults]\nzz = \"\"\"/z\nqq = x\n\"\"\"\n")
	before := bindRead(t, cfg)
	if _, err := BindProjectVault(movedReq("qa")); err == nil || !strings.Contains(err.Error(), "the edit would") {
		t.Fatalf("want the postcondition refusal, got %v", err)
	}
	if bindRead(t, cfg) != before {
		t.Error("the config was written before the postcondition refused")
	}
	if _, err := os.Stat(cfg + ".bak"); !os.IsNotExist(err) {
		t.Error("a .bak exists: the write happened")
	}
}

// B14: the rename verifies through the resolver after writing. A checkout
// whose own vault_path disagrees with the binding fails it, and the config and
// toml are restored.
func TestRebindRenameVerifiesAfterWriting(t *testing.T) {
	home := rebindEnv(t)
	global := rebindVault(t, filepath.Join(home, "global-vault"))
	quantum := rebindVault(t, filepath.Join(home, "quantum-vault"), "new-slug")
	elsewhere := rebindVault(t, filepath.Join(home, "elsewhere"))
	cfg, _ := VaultConfigFilePath()
	rebindWrite(t, cfg, "vault_path = \""+global+"\"\n\n[project_vaults]\nold-slug = \"~/quantum-vault\"\n")
	co := filepath.Join(home, "code", "proj")
	tp := filepath.Join(co, ".vibe-palace.toml")
	body := "vault_path = \"" + elsewhere + "\"\n" + rebindToml
	rebindWrite(t, tp, body)
	cfgBefore := bindRead(t, cfg)

	if _, err := RebindCheckout(rebindRename(co, quantum)); err == nil || !strings.Contains(err.Error(), "after the rename") {
		t.Fatalf("want the post-write verification refusal, got %v", err)
	}
	if bindRead(t, cfg) != cfgBefore || bindRead(t, tp) != body {
		t.Error("a failed verification did not restore the config and the marker")
	}
}

// B19: verification requires the binding itself to be what resolves — a
// checkout whose own vault_path names the same vault resolves it through tier
// 1, and the bind refuses (and restores) so that line gets removed.
func TestBindVerifyRequiresTheBindingSource(t *testing.T) {
	home := rebindEnv(t)
	_, quantum := splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	co := filepath.Join(home, "code", "qa")
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), "vault_path = \""+quantum+"\"\n[project]\nname = \"qa\"\n")
	before := bindRead(t, cfg)
	if _, err := BindProjectVault(movedReq("qa", co)); err == nil || !strings.Contains(err.Error(), "through the binding") {
		t.Fatalf("want a refusal for a checkout resolving through its own vault_path, got %v", err)
	}
	if bindRead(t, cfg) != before {
		t.Error("the config was not restored")
	}
}

// A rename whose <to> is ALREADY bound (with or without <from>) writes no
// binding, but must still verify the renamed checkout resolves through <to>:
// a checkout whose own vault_path disagrees is refused and its marker
// restored. MUTATION CONTRACT: return verify:false on either already-bound
// path and the matching subtest goes RED.
func TestRebindRenameVerifiesWhenToIsAlreadyBound(t *testing.T) {
	for name, table := range map[string]string{
		"to_only":     "new-slug = \"~/quantum-vault\"\n",
		"from_and_to": "old-slug = \"~/quantum-vault\"\nnew-slug = \"~/quantum-vault\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := rebindEnv(t)
			global := rebindVault(t, filepath.Join(home, "global-vault"))
			quantum := rebindVault(t, filepath.Join(home, "quantum-vault"), "new-slug")
			elsewhere := rebindVault(t, filepath.Join(home, "elsewhere"))
			cfg, _ := VaultConfigFilePath()
			rebindWrite(t, cfg, "vault_path = \""+global+"\"\n\n[project_vaults]\n"+table)
			co := filepath.Join(home, "code", "proj")
			tp := filepath.Join(co, ".vibe-palace.toml")
			body := "vault_path = \"" + elsewhere + "\"\n" + rebindToml
			rebindWrite(t, tp, body)

			if _, err := RebindCheckout(rebindRename(co, quantum)); err == nil || !strings.Contains(err.Error(), "after the rename") {
				t.Fatalf("want the post-write verification refusal, got %v", err)
			}
			if bindRead(t, tp) != body {
				t.Error("a failed verification did not restore the marker")
			}
		})
	}
}
