// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Case B of departed-ignores-a-directory-holding-only-ignored-residue at the
// MCP surface: a departed project whose Projects/alpha/ survived a pull
// holding one ignored backup. The probe that found it (61ce092) had
// vp_memory_write answer {"status":"written"} for it.

func residueGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitenv.SafeGitEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func residueDepartedVault(t *testing.T) *storage.Vault {
	t.Helper()
	root := t.TempDir()
	residueGit(t, root, "init", "-q", "-b", "main")
	putFile(t, root, ".gitignore", "*.bak\n.vp-locks/\npalace/.local/\n")
	putFile(t, root, "Projects/keep/resume.md", "keep\n")
	putFile(t, root, "Projects/alpha/resume.md", "alpha\n")
	residueGit(t, root, "add", "-A")
	residueGit(t, root, "commit", "-q", "-m", "seed")
	residueGit(t, root, "rm", "-q", "-r", "Projects/alpha")
	b, err := (departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: "git@example.invalid:q/quantum-vault.git", Date: "2026-09-25"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, root, departure.RelPath("alpha"), string(b))
	residueGit(t, root, "add", "-A")
	residueGit(t, root, "commit", "-q", "-m", "depart alpha")
	putFile(t, root, "Projects/alpha/transcripts/a.manifest.json.0.bak", "residue\n")
	return storage.NewVault(root)
}

// The dispatch seam refuses the write, with the redirect, and writes nothing.
func TestResidueOnlyDepartedSlugIsRefusedAtTheSeam(t *testing.T) {
	vault := residueDepartedVault(t)
	srv := seamServer(t, vault)
	text, isErr := callTool(t, srv, "vp_memory_write", map[string]any{
		"project": "alpha", "rel": "probe-note.md", "name": "probe-note",
		"description": "probe", "type": "project", "body": "residue probe",
	})
	if !isErr || !strings.Contains(text, "moved to another vault") || !strings.Contains(text, "git@example.invalid:q/quantum-vault.git") {
		t.Fatalf("vp_memory_write on a residue-only departed slug: isError=%v text=%q, want the redirect refusal", isErr, text)
	}
	if _, err := os.Stat(filepath.Join(vault.Root, "Projects", "alpha", "memory", "probe-note.md")); err == nil {
		t.Error("the refused write still landed a memory file")
	}
}

// vp_list_projects lists the slug as departed, and ONLY as departed; search
// knows no such project.
func TestResidueOnlyDepartedSlugIsListedAsDepartedOnly(t *testing.T) {
	vault := residueDepartedVault(t)
	srv := seamServer(t, vault)
	text, isErr := callTool(t, srv, "vp_list_projects", map[string]any{})
	if isErr {
		t.Fatalf("vp_list_projects: %s", text)
	}
	var out struct {
		Projects []string `json:"projects"`
		Departed []struct {
			Slug string `json:"slug"`
		} `json:"departed"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	if slices.Contains(out.Projects, "alpha") {
		t.Errorf("projects = %v: a residue-only departed slug is not a project here", out.Projects)
	}
	if !slices.Contains(out.Projects, "keep") {
		t.Errorf("projects = %v: the live project must stay listed", out.Projects)
	}
	if len(out.Departed) != 1 || out.Departed[0].Slug != "alpha" {
		t.Errorf("departed = %+v, want alpha", out.Departed)
	}

	exists, err := search.ProjectExists(vault, "alpha")
	if err != nil || exists {
		t.Errorf("search.ProjectExists(alpha) = %v, %v; want false", exists, err)
	}
	if exists, _ := search.ProjectExists(vault, "keep"); !exists {
		t.Error("search.ProjectExists(keep) must stay true")
	}
}
