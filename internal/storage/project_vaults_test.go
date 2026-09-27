// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// bindFixture is a hermetic host: $HOME and XDG_CONFIG_HOME under a temp dir,
// a global config whose vault_path is a real vault, and room for a
// [project_vaults] table (ADR-012).
type bindFixture struct {
	home   string
	cfg    string
	global string
}

func newBindFixture(t *testing.T) *bindFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cfgDir := filepath.Join(home, ".config", "vibe-palace")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &bindFixture{home: home, cfg: filepath.Join(cfgDir, "config.toml"), global: makeTestVault(t, filepath.Join(home, "global-vault"))}
	f.writeConfig(t, "")
	return f
}

// writeConfig rewrites the global config: vault_path, then extra verbatim.
func (f *bindFixture) writeConfig(t *testing.T, extra string) {
	t.Helper()
	body := "vault_path = \"" + f.global + "\"\n" + extra
	if err := os.WriteFile(f.cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// checkout makes a directory under $HOME holding a .vibe-palace.toml with body.
func (f *bindFixture) checkout(t *testing.T, name, body string) string {
	t.Helper()
	dir := filepath.Join(f.home, "code", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, ".vibe-palace.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// makeTestVault creates dir as a vault: a directory holding the manifest.
func makeTestVault(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".vibe-palace", "vault.toml"), []byte("format = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stubGitSlug replaces the resolver's git-origin derivation for one test and
// counts its calls.
func stubGitSlug(t *testing.T, slug string) *int {
	t.Helper()
	calls := 0
	orig := gitRemoteSlug
	gitRemoteSlug = func(string) string { calls++; return slug }
	t.Cleanup(func() { gitRemoteSlug = orig })
	return &calls
}

func mustResolve(t *testing.T, cwd string) Resolution {
	t.Helper()
	r, err := ResolveVaultBinding(cwd)
	if err != nil {
		t.Fatalf("ResolveVaultBinding(%s): %v", cwd, err)
	}
	return r
}

func mustReject(t *testing.T, cwd string) error {
	t.Helper()
	_, err := ResolveVaultBinding(cwd)
	if err == nil {
		t.Fatalf("ResolveVaultBinding(%s) succeeded; want a refusal", cwd)
	}
	if !errors.Is(err, ErrVaultBindingRejected) {
		t.Fatalf("refusal does not wrap ErrVaultBindingRejected, so `vp hook` would fall back to the live vault: %v", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("refusal wraps fs.ErrNotExist, which CheckConfigAt and `vp skills show` read as \"no config\": %v", err)
	}
	return err
}

// Test 1: the precedence matrix.
func TestResolveBinding_PrecedenceMatrix(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")

	t.Run("tier2_binding", func(t *testing.T) {
		dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
		r := mustResolve(t, dir)
		if r.Path != quantum {
			t.Errorf("Path = %q, want %q", r.Path, quantum)
		}
		if want := "binding:" + f.cfg + "#qa"; r.Source != want {
			t.Errorf("Source = %q, want %q", r.Source, want)
		}
	})
	t.Run("tier3_unbound_name", func(t *testing.T) {
		stubGitSlug(t, "")
		dir := f.checkout(t, "other", "[project]\nname = \"other\"\n")
		r := mustResolve(t, dir)
		if r.Path != f.global || !strings.HasPrefix(r.Source, "global:") {
			t.Errorf("got (%q, %q), want the global vault", r.Path, r.Source)
		}
	})
	t.Run("tier1_alone", func(t *testing.T) {
		alt := makeTestVault(t, filepath.Join(f.home, "alt"))
		dir := f.checkout(t, "loose", "vault_path = \""+alt+"\"\n[project]\nname = \"loose\"\n")
		r := mustResolve(t, dir)
		if r.Path != alt || !strings.HasPrefix(r.Source, "cwd:") {
			t.Errorf("got (%q, %q), want the cwd override", r.Path, r.Source)
		}
	})
	t.Run("tiers_1_and_2_agree", func(t *testing.T) {
		dir := f.checkout(t, "qa-agree", "vault_path = \""+quantum+"/\"\n[project]\nname = \"qa\"\n")
		r := mustResolve(t, dir)
		if r.Path != filepath.Clean(quantum) || !strings.HasPrefix(r.Source, "cwd:") {
			t.Errorf("got (%q, %q), want the agreeing root with a cwd: source", r.Path, r.Source)
		}
	})
}

// Test 2 (R2): a checkout vault_path and a host binding that disagree refuse,
// naming both sources and both roots. B2 removes the check and the checkout
// silently wins.
func TestResolveRefusesCwdBindingDisagreement(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	stale := makeTestVault(t, filepath.Join(f.home, "stale-vault"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")
	dir := f.checkout(t, "qa", "vault_path = \""+stale+"\"\n[project]\nname = \"qa\"\n")

	err := mustReject(t, dir)
	for _, want := range []string{filepath.Join(dir, ".vibe-palace.toml"), f.cfg, quantum, stale} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q:\n%v", want, err)
		}
	}
}

// Test 3: a binding whose target is not a vault refuses, and resolution
// creates nothing there (the U3 probe: `vp status` fabricated a missing root).
// B3 removes the existence check.
func TestResolveRefusesMissingBoundVault(t *testing.T) {
	f := newBindFixture(t)
	missing := filepath.Join(f.home, "no-such-vault")
	notVault := filepath.Join(f.home, "plain-dir")
	if err := os.MkdirAll(notVault, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"missing": missing, "no_manifest": notVault} {
		t.Run(name, func(t *testing.T) {
			f.writeConfig(t, "\n[project_vaults]\nqa = \""+target+"\"\n")
			dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
			err := mustReject(t, dir)
			if !strings.Contains(err.Error(), target) {
				t.Errorf("refusal does not name the target %s: %v", target, err)
			}
		})
	}
	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("resolution created %s (err %v); a missing bound vault must not be fabricated", missing, err)
	}
}

// Test 4 (R4): a marker naming no project, while the git-origin slug is bound,
// refuses instead of falling back to the global (live) vault. B4 removes it.
func TestResolveRefusesUnnamedMarkerWithBoundGitSlug(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")
	stubGitSlug(t, "qa")

	t.Run("marker_without_name", func(t *testing.T) {
		dir := f.checkout(t, "qa", "[meta]\nversion_major = 1\n")
		err := mustReject(t, dir)
		if !strings.Contains(err.Error(), "names no project") {
			t.Errorf("refusal does not say the marker names no project: %v", err)
		}
	})
	t.Run("no_marker", func(t *testing.T) {
		dir := f.checkout(t, "bare", "")
		err := mustReject(t, dir)
		if !strings.Contains(err.Error(), "no .vibe-palace.toml") {
			t.Errorf("refusal does not say there is no marker: %v", err)
		}
	})
	t.Run("invalid_name_counts_as_none", func(t *testing.T) {
		dir := f.checkout(t, "bad", "[project]\nname = \"Not A Slug\"\n")
		mustReject(t, dir)
	})
}

// Test 5: a host that binds nothing never runs git to resolve a vault. B9
// makes the git derivation unconditional.
func TestResolveUnboundHostNeverExecsGit(t *testing.T) {
	f := newBindFixture(t)
	calls := stubGitSlug(t, "qa")
	dir := f.checkout(t, "qa", "[meta]\nversion_major = 1\n")

	for name, extra := range map[string]string{"no_table": "", "empty_table": "\n[project_vaults]\n"} {
		t.Run(name, func(t *testing.T) {
			f.writeConfig(t, extra)
			r := mustResolve(t, dir)
			if r.Path != f.global {
				t.Errorf("Path = %q, want the global vault", r.Path)
			}
		})
	}
	if *calls != 0 {
		t.Errorf("git-origin derivation ran %d time(s) on a host with no bindings", *calls)
	}
}

// Test 6: the marker names an unbound project while its git-origin slug is
// bound. The marker is the identity, so this resolves tier 3 with a warning.
func TestResolveWarnsWhenGitSlugBoundButMarkerNameIsNot(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	f.writeConfig(t, "\n[project_vaults]\nsonic-buildimage = \""+quantum+"\"\n")
	stubGitSlug(t, "sonic-buildimage")
	dir := f.checkout(t, "community-sonic", "[project]\nname = \"community-sonic\"\n")

	r := mustResolve(t, dir)
	if r.Path != f.global {
		t.Errorf("Path = %q, want the global vault (the git slug's binding is not this checkout's)", r.Path)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "community-sonic") || !strings.Contains(r.Warnings[0], "sonic-buildimage") {
		t.Errorf("Warnings = %q, want one naming both slugs", r.Warnings)
	}
}

// Test 7: every unreadable or malformed table refuses. B7 decodes the table
// through a case-insensitive struct field.
func TestResolveBinding_MalformedTableRefused(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
	cases := map[string]string{
		"case_variant_table": "\n[PROJECT_VAULTS]\nqa = \"" + quantum + "\"\n",
		"invalid_slug_key":   "\n[project_vaults]\n\"Bad Key\" = \"" + quantum + "\"\n",
		"non_string_value":   "\n[project_vaults]\nqa = 3\n",
		"empty_value":        "\n[project_vaults]\nqa = \"  \"\n",
		"not_a_table":        "project_vaults = \"" + quantum + "\"\n",
		"unparseable":        "\n[project_vaults\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			f.writeConfig(t, extra)
			mustReject(t, dir)
		})
	}
	t.Run("dangling_symlink_config", func(t *testing.T) {
		if err := os.Remove(f.cfg); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(f.home, "nowhere.toml"), f.cfg); err != nil {
			t.Skipf("symlink: %v", err)
		}
		mustReject(t, dir)
	})
}

// Test 8: the binding reaches a fresh worktree, which gets only the committed
// (identity-only) marker. This is why the binding lives in host config, not in
// an untracked file beside the marker.
func TestResolveBindingReachesAFreshWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")
	repo := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
	wt := filepath.Join(f.home, "code", "qa-wt")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = SafeGitEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", ".vibe-palace.toml")
	git("commit", "-q", "-m", "marker")
	git("worktree", "add", "-q", wt)

	r := mustResolve(t, wt)
	if r.Path != quantum || !strings.HasPrefix(r.Source, "binding:") {
		t.Errorf("fresh worktree resolved (%q, %q), want the bound vault", r.Path, r.Source)
	}
}

// Test 9: the machine-wide resolvers ignore project bindings even when the
// process sits inside a bound checkout. B5 moves tier 2 into
// ResolveGlobalVaultPath.
func TestMachineWideSurfacesIgnoreProjectBindings(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")
	dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")
	t.Chdir(dir)

	got, src, err := ResolveGlobalVaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != f.global || !strings.HasPrefix(src, "global:") {
		t.Errorf("ResolveGlobalVaultPath = (%q, %q), want the global vault", got, src)
	}
	v, err := OpenVaultGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if v.Root != f.global {
		t.Errorf("OpenVaultGlobal().Root = %q, want %q", v.Root, f.global)
	}
}

// Test 10: the MCP drift check sees tier 2 — a server bound through a binding
// is not stale, and one bound elsewhere is reported with the binding source.
// B8 hands CheckVaultBinding a tier-less resolver.
func TestStaleBindingAcceptsTier2Binding(t *testing.T) {
	f := newBindFixture(t)
	quantum := makeTestVault(t, filepath.Join(f.home, "quantum-vault"))
	f.writeConfig(t, "\n[project_vaults]\nqa = \""+quantum+"\"\n")
	dir := f.checkout(t, "qa", "[project]\nname = \"qa\"\n")

	if err := CheckVaultBinding(quantum, dir); err != nil {
		t.Errorf("a server bound through the binding reports %v", err)
	}
	var stale *StaleBindingError
	if err := CheckVaultBinding(f.global, dir); !errors.As(err, &stale) {
		t.Fatalf("a server bound to the global vault must be stale once the binding exists, got %v", err)
	}
	if !strings.HasPrefix(stale.Source, "binding:") {
		t.Errorf("stale Source = %q, want the binding source", stale.Source)
	}
}

// Test 12: a swallowed vault_path matches BOTH sentinels, so every existing
// ErrSwallowedVaultPath test and every new ErrVaultBindingRejected call site
// agree.
func TestSwallowedStillMatchesBothSentinels(t *testing.T) {
	f := newBindFixture(t)
	dir := f.checkout(t, "tw", "[project]\nname = \"tw\"\nvault_path = \"/tmp/tw\"\n")
	_, err := ResolveVaultBinding(dir)
	if !errors.Is(err, ErrSwallowedVaultPath) || !errors.Is(err, ErrVaultBindingRejected) {
		t.Errorf("swallowed vault_path must match both sentinels, got %v", err)
	}
}

// Test 16: LoadConfig decodes a config carrying [project_vaults].
func TestLoadConfigAcceptsProjectVaults(t *testing.T) {
	f := newBindFixture(t)
	f.writeConfig(t, "\n[project_vaults]\nqa = \"~/quantum-vault\"\n")
	if _, err := NewVault(f.global).LoadConfig("qa"); err != nil {
		t.Fatalf("LoadConfig with [project_vaults]: %v", err)
	}
}

// CwdMarker reports the same file the resolver reads.
func TestCwdMarkerMatchesResolverWalk(t *testing.T) {
	f := newBindFixture(t)
	alt := makeTestVault(t, filepath.Join(f.home, "alt"))
	dir := f.checkout(t, "loose", "vault_path = \""+alt+"\"\n")
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	path, vp, err := CwdMarker(sub)
	if err != nil {
		t.Fatal(err)
	}
	r := mustResolve(t, sub)
	if "cwd:"+path != r.Source || vp != alt {
		t.Errorf("CwdMarker = (%q, %q), resolver source %q", path, vp, r.Source)
	}
}

// A marker whose [project] holds a non-string name (or project is not a
// table) resolved before ADR-012 and still does: the name is read tolerantly.
func TestResolveToleratesAnOddProjectTable(t *testing.T) {
	f := newBindFixture(t)
	for name, body := range map[string]string{
		"non_string_name": "[project]\nname = 3\n",
		"project_scalar":  "project = \"x\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := f.checkout(t, name, body)
			if r := mustResolve(t, dir); r.Path != f.global {
				t.Errorf("Path = %q, want the global vault", r.Path)
			}
		})
	}
}
