// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// splitLabel is the moved-to-vault destination a split records: the new
// vault's remote URL (operator ruling O3).
const splitLabel = "git@example.invalid:team/quantum-vault.git"

// splitHost is a host after a split landed: a global config whose vault_path
// is a stamped default vault holding a moved-to-vault record for slug, and a
// stamped ~/quantum-vault that holds slug and whose origin is splitLabel.
func splitHost(t *testing.T, home, slug string) (global, quantum string) {
	t.Helper()
	global = rebindVault(t, filepath.Join(home, "global-vault"))
	quantum = rebindVault(t, filepath.Join(home, "quantum-vault"), slug)
	rebindGit(t, quantum, "init", "-q")
	rebindGit(t, quantum, "remote", "add", "origin", splitLabel)
	if _, err := NewVault(global).RecordDeparture(slug, departure.MovedToVault, splitLabel); err != nil {
		t.Fatal(err)
	}
	cfg, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	rebindWrite(t, cfg, "vault_path = \""+global+"\"\n")
	return global, quantum
}

// bindCheckout makes a checkout under home naming slug.
func bindCheckout(t *testing.T, home, name, slug string) string {
	t.Helper()
	co := filepath.Join(home, "code", name)
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), "[project]\nname = \""+slug+"\"\n")
	return co
}

func movedReq(slug string, checkouts ...string) BindRequest {
	return BindRequest{Slug: slug, VaultPath: "~/quantum-vault", Mode: BindMoved, Checkouts: checkouts}
}

func bindRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Test 1: moved mode binds, every named checkout resolves through the binding,
// and so does a fresh worktree of one (it gets only the committed marker).
func TestBindMovedBindsEveryCheckout(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git unavailable")
	}
	home := rebindEnv(t)
	_, quantum := splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	coA := bindCheckout(t, home, "qa", "qa")
	coB := bindCheckout(t, home, "qa-second", "qa")
	rebindGit(t, coA, "init", "-q")
	rebindGit(t, coA, "add", ".vibe-palace.toml")
	rebindGit(t, coA, "commit", "-qm", "marker")
	wt := filepath.Join(home, "code", "qa-wt")
	rebindGit(t, coA, "worktree", "add", "-q", wt)

	rep, err := BindProjectVault(movedReq("qa", coA, coB))
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if len(rep.Checkouts) != 2 {
		t.Fatalf("report checkouts = %+v", rep.Checkouts)
	}
	for _, co := range []string{coA, coB, wt} {
		p, src, err := ResolveVaultPath(co)
		if err != nil || p != quantum || src != "binding:"+cfg+"#qa" {
			t.Errorf("%s resolves (%q, %q, %v); want %q through the binding", co, p, src, err, quantum)
		}
	}
	if !strings.Contains(bindRead(t, cfg), "qa = \"~/quantum-vault\"") {
		t.Errorf("config does not carry the binding as given:\n%s", bindRead(t, cfg))
	}
}

// Tests 2-9: every refusal leaves the config byte-identical.
func TestBindRefusals(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git unavailable")
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, home, global, quantum, cfg string) BindRequest
		want  string
	}{
		{"no_departure_record", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			_ = os.Remove(filepath.Join(global, filepath.FromSlash(departure.RelPath("qa"))))
			return movedReq("qa")
		}, "holds no departure record"},
		{"label_mismatch", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			rebindGit(t, quantum, "remote", "set-url", "origin", "git@example.invalid:other/vault.git")
			return movedReq("qa")
		}, "no remote of"},
		{"empty_label_without_allow", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			rebindWrite(t, filepath.Join(global, filepath.FromSlash(departure.RelPath("qa"))),
				`{"format":"vp-departure/1","slug":"qa","kind":"moved-to-vault","to":"","date":"2026-09-27"}`+"\n")
			return movedReq("qa")
		}, "names no destination"},
		{"relative_target", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			t.Chdir(home)
			r := movedReq("qa")
			r.VaultPath = "quantum-vault"
			return r
		}, "not an absolute path"},
		{"missing_target", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			r := movedReq("qa")
			r.VaultPath = "~/no-such-vault"
			return r
		}, "cannot be read"},
		{"wrong_format", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			rebindWrite(t, filepath.Join(quantum, ".vibe-palace", "vault.toml"), "format = 1\n")
			return movedReq("qa")
		}, "data format"},
		{"target_not_holding_slug", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			if err := os.RemoveAll(filepath.Join(quantum, "Projects", "qa")); err != nil {
				t.Fatal(err)
			}
			return movedReq("qa")
		}, "holds neither"},
		{"target_is_default_vault", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			r := movedReq("qa")
			r.VaultPath = global
			return r
		}, "default vault"},
		{"slug_still_present_in_default", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			rebindWrite(t, filepath.Join(global, "Projects", "qa", "resume.md"), "# live\n")
			return movedReq("qa")
		}, "holds no departure record"},
		{"unreadable_config", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			rebindWrite(t, cfg, "vault_path = \""+global+"\"\n[project_vaults\n")
			return movedReq("qa")
		}, "host config unreadable"},
		{"malformed_table", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			rebindWrite(t, cfg, "vault_path = \""+global+"\"\n\n[PROJECT_VAULTS]\nqa = \"~/quantum-vault\"\n")
			return movedReq("qa")
		}, "vault binding rejected"},
		{"repoint_refused", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			other := rebindVault(t, filepath.Join(home, "other-vault"))
			rebindWrite(t, cfg, "vault_path = \""+global+"\"\n\n[project_vaults]\nqa = \""+other+"\"\n")
			return movedReq("qa")
		}, "re-pointing a binding is refused"},
		{"checkout_names_another_project", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			return movedReq("qa", bindCheckout(t, home, "other", "other"))
		}, "names project \"other\""},
		{"git_unavailable_for_label", func(t *testing.T, home, global, quantum, cfg string) BindRequest {
			t.Setenv("PATH", "")
			return movedReq("qa")
		}, "read the remotes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := rebindEnv(t)
			global, quantum := splitHost(t, home, "qa")
			cfg, _ := VaultConfigFilePath()
			req := c.setup(t, home, global, quantum, cfg)
			before := bindRead(t, cfg)
			_, err := BindProjectVault(req)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
			if got := bindRead(t, cfg); got != before {
				t.Errorf("a refused bind changed the config:\n%s", got)
			}
		})
	}
}

// Test 3 (allow side) and test 9 (same root): an unlabelled record binds with
// allow_unlabelled, and binding the same root twice is a no-op.
func TestBindAllowUnlabelledAndAlreadyBound(t *testing.T) {
	home := rebindEnv(t)
	global, _ := splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	rebindWrite(t, filepath.Join(global, filepath.FromSlash(departure.RelPath("qa"))),
		`{"format":"vp-departure/1","slug":"qa","kind":"moved-to-vault","to":"","date":"2026-09-27"}`+"\n")
	req := movedReq("qa")
	req.AllowUnlabelled = true
	if _, err := BindProjectVault(req); err != nil {
		t.Fatalf("allow_unlabelled bind: %v", err)
	}
	_ = os.Remove(cfg + ".bak")
	after := bindRead(t, cfg)
	rep, err := BindProjectVault(req)
	if err != nil || rep.Change != "already bound" {
		t.Fatalf("second bind = %q, %v; want already bound", rep.Change, err)
	}
	if bindRead(t, cfg) != after {
		t.Error("an already-bound bind rewrote the config")
	}
	if _, err := os.Stat(cfg + ".bak"); !os.IsNotExist(err) {
		t.Error("an already-bound bind wrote a .bak")
	}
}

// Test 10: new mode binds a slug the default vault never held; the checkout
// then resolves the target, so `vp init` there scaffolds into it.
func TestBindNewMode(t *testing.T) {
	home := rebindEnv(t)
	global := rebindVault(t, filepath.Join(home, "global-vault"))
	target := rebindVault(t, filepath.Join(home, "quantum-vault"))
	cfg, _ := VaultConfigFilePath()
	rebindWrite(t, cfg, "vault_path = \""+global+"\"\n")
	co := bindCheckout(t, home, "chimera", "chimera")

	if _, err := BindProjectVault(BindRequest{Slug: "chimera", VaultPath: target, Mode: BindNew, Checkouts: []string{co}}); err != nil {
		t.Fatalf("new bind: %v", err)
	}
	if p, _, err := ResolveVaultPath(co); err != nil || p != target {
		t.Errorf("checkout resolves %q, %v; want %q", p, err, target)
	}
	rebindWrite(t, filepath.Join(global, "Projects", "later", "resume.md"), "x\n")
	if _, err := BindProjectVault(BindRequest{Slug: "later", VaultPath: target, Mode: BindNew}); err == nil ||
		!strings.Contains(err.Error(), "not a project born elsewhere") {
		t.Errorf("new mode must refuse a slug the default vault holds, got %v", err)
	}
}

// Test 11: the splice changes only its key — comments and every other key are
// byte-preserved — and appends a new table at EOF, never above top-level keys.
func TestBindChangesOnlyItsKey(t *testing.T) {
	home := rebindEnv(t)
	global, _ := splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()

	t.Run("appends_the_table_at_eof", func(t *testing.T) {
		base := "# operator note\nvault_path = \"" + global + "\"\nhttp_port = 7423\n\n[search]\ndefault_limit = 10 # kept\n"
		rebindWrite(t, cfg, base)
		if _, err := BindProjectVault(movedReq("qa")); err != nil {
			t.Fatal(err)
		}
		if got, want := bindRead(t, cfg), base+"\n[project_vaults]\nqa = \"~/quantum-vault\"\n"; got != want {
			t.Errorf("config =\n%s\nwant\n%s", got, want)
		}
	})
	t.Run("inserts_into_an_existing_table", func(t *testing.T) {
		other := rebindVault(t, filepath.Join(home, "other-vault"))
		base := "vault_path = \"" + global + "\"\n\n[project_vaults]\n# bindings\nbee = \"" + other + "\"\n\n[search]\ndefault_limit = 10\n"
		rebindWrite(t, cfg, base)
		if _, err := BindProjectVault(movedReq("qa")); err != nil {
			t.Fatal(err)
		}
		want := strings.Replace(base, "bee = \""+other+"\"\n", "bee = \""+other+"\"\nqa = \"~/quantum-vault\"\n", 1)
		if got := bindRead(t, cfg); got != want {
			t.Errorf("config =\n%s\nwant\n%s", got, want)
		}
	})
}

// Test 12: the write is a compare-and-set on bytes — a change between the read
// and the write refuses and leaves the other writer's bytes in place.
func TestBindRefusesConcurrentConfigChange(t *testing.T) {
	home := rebindEnv(t)
	splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	orig := bindBeforeWrite
	t.Cleanup(func() { bindBeforeWrite = orig })
	var theirs string
	bindBeforeWrite = func() {
		theirs = bindRead(t, cfg) + "# another writer\n"
		rebindWrite(t, cfg, theirs)
	}
	if _, err := BindProjectVault(movedReq("qa")); err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("want a compare-and-set refusal, got %v", err)
	}
	if got := bindRead(t, cfg); got != theirs {
		t.Errorf("the other writer's bytes were overwritten:\n%s", got)
	}
}

// Test 13: a checkout whose own vault_path names another vault makes the
// post-write verification fail (R2), and the config is restored byte-for-byte.
func TestBindRestoresPreImageWhenACheckoutShadows(t *testing.T) {
	home := rebindEnv(t)
	splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	other := rebindVault(t, filepath.Join(home, "other-vault"))
	co := filepath.Join(home, "code", "qa")
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), "vault_path = \""+other+"\"\n[project]\nname = \"qa\"\n")
	before := bindRead(t, cfg)

	_, err := BindProjectVault(movedReq("qa", co))
	if err == nil || !errors.Is(err, ErrVaultBindingRejected) || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("want a typed verification failure that restored the config, got %v", err)
	}
	if got := bindRead(t, cfg); got != before {
		t.Errorf("config not restored:\n%s", got)
	}
}

// Test 14: the remote-URL helper never reads "could not run git" as "no
// remotes", and reads only the vault's own config.
func TestVaultRemoteURLs(t *testing.T) {
	if !GitAvailable() {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	rebindGit(t, repo, "init", "-q")
	if urls, err := VaultRemoteURLs(repo); err != nil || len(urls) != 0 {
		t.Errorf("no remotes = (%v, %v), want (nil, nil)", urls, err)
	}
	rebindGit(t, repo, "remote", "add", "origin", splitLabel)
	if urls, err := VaultRemoteURLs(repo); err != nil || len(urls) != 1 || urls[0] != splitLabel {
		t.Errorf("origin = (%v, %v)", urls, err)
	}
	if _, err := VaultRemoteURLs(t.TempDir()); err == nil {
		t.Error("a directory that is not a repository must be an error, not \"no remotes\"")
	}
	t.Setenv("PATH", "")
	if _, err := VaultRemoteURLs(repo); err == nil {
		t.Error("git missing must be an error, not \"no remotes\"")
	}
}

// Test 15: a dry run writes nothing.
func TestBindDryRunWritesNothing(t *testing.T) {
	home := rebindEnv(t)
	splitHost(t, home, "qa")
	cfg, _ := VaultConfigFilePath()
	before := bindRead(t, cfg)
	req := movedReq("qa")
	req.DryRun = true
	rep, err := BindProjectVault(req)
	if err != nil || !strings.Contains(rep.Change, "qa = ") {
		t.Fatalf("dry run = %+v, %v", rep, err)
	}
	if bindRead(t, cfg) != before {
		t.Error("a dry run changed the config")
	}
}

// Test 19: a rename moves the [project_vaults] key, so the renamed checkout
// keeps resolving the vault it was bound to.
func TestRebindRenameMovesTheBindingKey(t *testing.T) {
	home := rebindEnv(t)
	global := rebindVault(t, filepath.Join(home, "global-vault"))
	quantum := rebindVault(t, filepath.Join(home, "quantum-vault"), "new-slug")
	cfg, _ := VaultConfigFilePath()
	rebindWrite(t, cfg, "vault_path = \""+global+"\"\n\n[project_vaults]\nold-slug = \"~/quantum-vault\"\n")
	co := filepath.Join(home, "code", "proj")
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), rebindToml)

	rep, err := RebindCheckout(rebindRename(co, quantum))
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	got := bindRead(t, cfg)
	if strings.Contains(got, "old-slug") || !strings.Contains(got, "new-slug = \"~/quantum-vault\"") {
		t.Errorf("the binding key did not move:\n%s", got)
	}
	if rep.BindingChange == "" {
		t.Error("the report does not state the moved key")
	}
	if p, src, err := ResolveVaultPath(co); err != nil || p != quantum || src != "binding:"+cfg+"#new-slug" {
		t.Errorf("renamed checkout resolves (%q, %q, %v)", p, src, err)
	}
}

// Test 20: a rename refuses a conflicting <to> binding, and a <from> binding
// that points somewhere other than the vault the rename landed in. Nothing is
// written.
func TestRebindRenameRefusesABindingConflict(t *testing.T) {
	home := rebindEnv(t)
	global := rebindVault(t, filepath.Join(home, "global-vault"))
	quantum := rebindVault(t, filepath.Join(home, "quantum-vault"), "new-slug")
	elsewhere := rebindVault(t, filepath.Join(home, "elsewhere"))
	cfg, _ := VaultConfigFilePath()
	co := filepath.Join(home, "code", "proj")
	tp := filepath.Join(co, ".vibe-palace.toml")
	for name, body := range map[string]string{
		"to_bound_elsewhere":   "old-slug = \"~/quantum-vault\"\nnew-slug = \"" + elsewhere + "\"\n",
		"from_bound_elsewhere": "old-slug = \"" + elsewhere + "\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			rebindWrite(t, cfg, "vault_path = \""+global+"\"\n\n[project_vaults]\n"+body)
			rebindWrite(t, tp, rebindToml)
			before := bindRead(t, cfg)
			if _, err := RebindCheckout(rebindRename(co, quantum)); err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Fatalf("want a refusal, got %v", err)
			}
			if bindRead(t, cfg) != before || bindRead(t, tp) != rebindToml {
				t.Error("a refused rename wrote something")
			}
		})
	}
}

// Test 21: a rename whose toml write fails restores the config it already
// moved the key in.
func TestRebindRenameRestoresTheConfigWhenTheTomlWriteFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	home := rebindEnv(t)
	global := rebindVault(t, filepath.Join(home, "global-vault"))
	quantum := rebindVault(t, filepath.Join(home, "quantum-vault"), "new-slug")
	cfg, _ := VaultConfigFilePath()
	rebindWrite(t, cfg, "vault_path = \""+global+"\"\n\n[project_vaults]\nold-slug = \"~/quantum-vault\"\n")
	co := filepath.Join(home, "code", "proj")
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), rebindToml)
	before := bindRead(t, cfg)
	if err := os.Chmod(co, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(co, 0o755) })

	if _, err := RebindCheckout(rebindRename(co, quantum)); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("want a toml-write failure that restored the config, got %v", err)
	}
	if got := bindRead(t, cfg); got != before {
		t.Errorf("config not restored:\n%s", got)
	}
}

// Test 22: RebindCheckout reads the marker through the shared reader — a
// padded name reads trimmed — and refuses a checkout whose own marker is not
// the one the resolver's walk finds (a checkout at $HOME, whose marker the
// resolver ignores).
func TestRebindCheckoutUsesTheSharedMarker(t *testing.T) {
	home := rebindEnv(t)
	splitHost(t, home, "qa")
	co := filepath.Join(home, "code", "padded")
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), "[project]\nname = \" qa \"\n")
	if _, err := RebindCheckout(CheckoutRebind{Kind: RebindSplit, Checkout: co, FromSlug: "qa", VaultPath: "~/quantum-vault"}); err != nil {
		t.Errorf("a padded name must read as trimmed: %v", err)
	}
	rebindWrite(t, filepath.Join(home, ".vibe-palace.toml"), "[project]\nname = \"qa\"\n")
	if _, err := RebindCheckout(CheckoutRebind{Kind: RebindSplit, Checkout: home, FromSlug: "qa", VaultPath: "~/quantum-vault"}); err == nil ||
		!strings.Contains(err.Error(), "the resolver reads") {
		t.Errorf("want a refusal for a marker the resolver does not read, got %v", err)
	}
}

// Test 23: a marker whose [project].tags is wrong-typed still renames — the
// strict ParseProjectFile read refused it.
func TestRebindRenameToleratesAWrongTypedTag(t *testing.T) {
	home := rebindEnv(t)
	vault := rebindVault(t, filepath.Join(home, "vault"), "new-slug")
	co := filepath.Join(home, "code", "proj")
	body := strings.Replace(rebindToml, "# tags = []", "tags = \"not-a-list\"", 1)
	rebindWrite(t, filepath.Join(co, ".vibe-palace.toml"), body)
	if _, err := RebindCheckout(rebindRename(co, vault)); err != nil {
		t.Errorf("rename with a wrong-typed tags: %v", err)
	}
}
