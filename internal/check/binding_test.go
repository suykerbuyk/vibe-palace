// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// bindingHost is a hermetic $HOME/XDG with a global config whose body the
// test supplies, and a checkout directory under it.
func bindingHost(t *testing.T, globalBody string) (home, proj string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cfgDir := filepath.Join(home, ".config", "vibe-palace")
	proj = filepath.Join(home, "code", "proj")
	for _, d := range []string{cfgDir, proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(globalBody), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, proj
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCheckConfigReportsRejectedBindingNotMissing: a refused [project_vaults]
// binding is reported as rejected, with the why in Details — never as "not
// found / run vp init". MUTATION CONTRACT: make the missing-target refusal wrap
// fs.ErrNotExist and this goes RED.
func TestCheckConfigReportsRejectedBindingNotMissing(t *testing.T) {
	home, proj := bindingHost(t, "")
	missing := filepath.Join(home, "no-such-vault")
	writeFile(t, filepath.Join(home, ".config", "vibe-palace", "config.toml"),
		"vault_path = \""+filepath.Join(home, "live")+"\"\n\n[project_vaults]\nproj = \""+missing+"\"\n")
	writeFile(t, filepath.Join(proj, ".vibe-palace.toml"), "[project]\nname = \"proj\"\n")

	_, _, r := CheckConfigAt(proj)
	if r.Status != Fail {
		t.Fatalf("Status = %v, want Fail", r.Status)
	}
	details := strings.Join(r.Details, " ")
	if strings.Contains(details, "vp init") || strings.Contains(r.Summary, "not found") {
		t.Errorf("a rejected binding was reported as a missing config: %s / %s", r.Summary, details)
	}
	if !strings.Contains(details, missing) {
		t.Errorf("Details do not name the bound target %s: %s", missing, details)
	}
}

// Test 17: the tracked-marker row (ADR-012: a committed marker is identity
// only). MUTATION CONTRACT: drop the ls-files gate and the untracked case goes
// RED (it would report Info for a marker nobody committed).
func TestCheckTrackedMarkerVaultPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	git := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = storage.SafeGitEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	const withPath = "vault_path = \"/somewhere/vault\"\n[project]\nname = \"proj\"\n"
	const identity = "[project]\nname = \"proj\"\n"

	cases := []struct {
		name   string
		body   string
		repo   bool
		commit bool
		want   Status
	}{
		{"tracked_with_vault_path", withPath, true, true, Info},
		{"untracked_with_vault_path", withPath, true, false, Pass},
		{"tracked_identity_only", identity, true, true, Pass},
		{"not_a_repo", withPath, false, false, Skip},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, proj := bindingHost(t, "vault_path = \"/tmp/v\"\n")
			writeFile(t, filepath.Join(proj, ".vibe-palace.toml"), c.body)
			if c.repo {
				git(t, proj, "init", "-q")
				if c.commit {
					git(t, proj, "add", ".vibe-palace.toml")
					git(t, proj, "commit", "-q", "-m", "marker")
				}
			}
			r := CheckTrackedMarkerVaultPath(proj)
			if r.Status != c.want {
				t.Errorf("Status = %v (%s), want %v", r.Status, r.Summary, c.want)
			}
			if c.want == Info && !strings.Contains(strings.Join(r.Details, "\n"), "[project_vaults]") {
				t.Errorf("Info row does not name the remedy table: %v", r.Details)
			}
		})
	}
	t.Run("no_marker", func(t *testing.T) {
		_, proj := bindingHost(t, "vault_path = \"/tmp/v\"\n")
		if r := CheckTrackedMarkerVaultPath(proj); r.Status != Skip {
			t.Errorf("Status = %v, want Skip", r.Status)
		}
	})
}
