// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func vpcWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func vpcDetailFor(r Result, rel string) (string, bool) {
	for _, d := range r.Details {
		if strings.HasPrefix(d, rel+" ") {
			return d, true
		}
	}
	return "", false
}

// TestCheckVaultProjectConfig_ListsCommittedAndUntrackedSurvivors: a committed
// survivor is invisible to git status and tidy, and an untracked one is
// invisible to git ls-files. The row must list both, each with the path and the
// removal command, which is why it enumerates the filesystem and not git.
func TestCheckVaultProjectConfig_ListsCommittedAndUntrackedSurvivors(t *testing.T) {
	root := t.TempDir()
	vpcWrite(t, root, "Projects/committed/config.toml", "[palace.scoring.rooms.general]\nhigh = [\"x\"]\n")
	vpcWrite(t, root, "Projects/committed/resume.md", "# resume\n")
	plGit(t, root, "init", "-q")
	plGit(t, root, "add", "-A")
	plGit(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "seed")
	vpcWrite(t, root, "Projects/handedit/config.toml", "# restored from a backup\n")

	r := CheckVaultProjectConfig(root)
	if r.Status != Info {
		t.Fatalf("status = %v, want Info: %+v", r.Status, r)
	}
	if !strings.Contains(r.Summary, "2 retired") {
		t.Errorf("summary = %q, want it to count both survivors", r.Summary)
	}
	for _, rel := range []string{"Projects/committed/config.toml", "Projects/handedit/config.toml"} {
		line, ok := vpcDetailFor(r, rel)
		if !ok {
			t.Errorf("details do not name %s: %v", rel, r.Details)
			continue
		}
		if !strings.Contains(line, "vp vault delete "+rel) {
			t.Errorf("line for %s = %q, want the removal command", rel, line)
		}
	}
	if len(r.Details) == 0 || r.Details[0] != vaultProjectConfigHeader {
		t.Errorf("first detail line = %v, want the header stating the file is not read", r.Details)
	}
}

// TestCheckVaultProjectConfig_PassWhenNone: no survivor is Pass, and neither a
// deeper config.toml nor a project directory without one is a survivor.
func TestCheckVaultProjectConfig_PassWhenNone(t *testing.T) {
	root := t.TempDir()
	vpcWrite(t, root, "Projects/alpha/resume.md", "# resume\n")
	vpcWrite(t, root, "Projects/alpha/doc/config.toml", "# not the project config\n")
	vpcWrite(t, root, "Projects/beta/commands/README.md", "stub\n")

	r := CheckVaultProjectConfig(root)
	if r.Status != Pass || r.Summary != "none" {
		t.Fatalf("got %+v, want Pass/none", r)
	}
}

// TestCheckVaultProjectConfig_NoProjectsDir: a vault with no Projects/ has no
// survivor; it is Pass, not an error.
func TestCheckVaultProjectConfig_NoProjectsDir(t *testing.T) {
	r := CheckVaultProjectConfig(t.TempDir())
	if r.Status != Pass {
		t.Fatalf("got %+v, want Pass", r)
	}
}

// TestCheckVaultProjectConfig_NonRegularSurvivorIsNamed: a directory (or link)
// at the path is still something at the path. It is listed, and the row does
// not print a vp vault delete that would refuse it.
func TestCheckVaultProjectConfig_NonRegularSurvivorIsNamed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Projects", "odd", "config.toml"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := CheckVaultProjectConfig(root)
	line, ok := vpcDetailFor(r, "Projects/odd/config.toml")
	if r.Status != Info || !ok {
		t.Fatalf("got %+v, want Info naming Projects/odd/config.toml", r)
	}
	if !strings.Contains(line, "not a regular file") || strings.Contains(line, "vp vault delete") {
		t.Errorf("line = %q, want it marked non-regular with no vp vault delete", line)
	}
}

// TestVaultProjectConfigProducer: the selector reaches the row, and degrades to
// Skip with no vault.
func TestVaultProjectConfigProducer(t *testing.T) {
	rs, err := RunSelected("", "vault-project-config")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if len(rs) != 1 || rs[0].Status != Skip {
		t.Fatalf("no vault: got %+v, want one Skip row", rs)
	}

	root := t.TempDir()
	vpcWrite(t, root, "Projects/p/config.toml", "x\n")
	rs, err = RunSelected(root, "vault-project-config")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if len(rs) != 1 || rs[0].Name != "Vault project config" || rs[0].Status != Info {
		t.Fatalf("got %+v, want one Info row", rs)
	}
}
