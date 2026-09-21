// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectDirFixture lays down entries under <root>/Projects/<slug>/. A name
// ending in "/" is created as a directory; any other name as a file holding
// "x".
func projectDirFixture(t *testing.T, root, slug string, entries ...string) string {
	t.Helper()
	dir := filepath.Join(root, "Projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		p := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(e, "/")))
		if strings.HasSuffix(e, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestClassifyProjectDir(t *testing.T) {
	cases := []struct {
		name    string
		entries []string // nil means no Projects/<slug> at all
		want    ProjectDirState
	}{
		{"absent", nil, ProjectAbsent},
		{"phantom: memory/ only", []string{"memory/m.md"}, ProjectPhantom},
		{"phantom: .surface, transcripts/, empty tasks/done/", []string{".surface", "transcripts/t.jsonl", "tasks/done/"}, ProjectPhantom},
		{"phantom: empty directory", []string{}, ProjectPhantom},
		{"phantom: markers and history names that are DIRECTORIES", []string{"config.toml/", "commands/README.md/", "resume.md/", "iterations.md/"}, ProjectPhantom},
		{"scaffold: commands/README.md alone", []string{"commands/README.md"}, ProjectScaffoldOnly},
		{"scaffold: skills/README.md alone", []string{"skills/README.md"}, ProjectScaffoldOnly},
		// config.toml stopped being a scaffold marker when its writer retired:
		// alone it is a survivor of the retired per-project vault config, not
		// an initialised project. (The row that paired it with a stray FILE
		// named commands went with it: with no marker left present, that
		// ENOTDIR is now returned, which the error rows below already pin.)
		{"phantom: config.toml alone (a survivor of the retired vault config)", []string{"config.toml"}, ProjectPhantom},
		{"content: resume.md", []string{"resume.md"}, ProjectWithContent},
		{"content: iterations.md", []string{"iterations.md"}, ProjectWithContent},
		{"content: sessions/x.md", []string{"sessions/x.md"}, ProjectWithContent},
		{"content: tasks/done/deep/x.md", []string{"tasks/done/deep/x.md"}, ProjectWithContent},
		{"content wins over a scaffold", []string{"config.toml", "commands/README.md", "skills/README.md", "resume.md"}, ProjectWithContent},

		// Live shapes, re-derived 2026-09-19 through the MCP vault tools.
		{"live atlassian-vault: history, no config.toml", []string{"resume.md", "iterations.md", "sessions/s.md", "tasks/t.md"}, ProjectWithContent},
		{"live qa-metabuild-system: one session, nothing else", []string{"sessions/one.md"}, ProjectWithContent},
		{"live tools: sessions only, no config.toml", []string{"sessions/2026-05-09-01.md", "sessions/2026-05-13-04.md"}, ProjectWithContent},
		{"live recmeet-v1: READMEs and .surface, no config.toml", []string{".surface", "commands/README.md", "skills/README.md"}, ProjectScaffoldOnly},
		{"live go-proxmox: config.toml, READMEs, empty tasks/", []string{"config.toml", "commands/README.md", "skills/README.md", "tasks/done/", "tasks/cancelled/"}, ProjectScaffoldOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.entries != nil {
				projectDirFixture(t, root, "proj", tc.entries...)
			}
			got, err := ClassifyProjectDir(root, "proj")
			if err != nil {
				t.Fatalf("ClassifyProjectDir: %v", err)
			}
			if got != tc.want {
				t.Errorf("state = %s, want %s", got, tc.want)
			}
		})
	}
	t.Run("errors", classifyProjectDirErrorRows)
	t.Run("unreadable sessions", classifyProjectDirUnreadableSessionsRows)
}

func classifyProjectDirErrorRows(t *testing.T) {
	t.Run("invalid slug", func(t *testing.T) {
		root := t.TempDir()
		if _, err := ClassifyProjectDir(root, "a:b"); err == nil {
			t.Fatal("an invalid slug classified without error")
		}
	})
	t.Run("Projects/<slug> is a regular file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "Projects"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "Projects", "proj"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ClassifyProjectDir(root, "proj")
		if err == nil || !strings.Contains(err.Error(), "Projects/proj is not a directory") {
			t.Fatalf("err = %v, want it to name Projects/proj as not a directory", err)
		}
	})
	// Refused deliberately: ListAllProjects excludes symlinked projects, and a
	// writer must not follow a link out of the vault's tree on this say-so.
	t.Run("Projects/<slug> is a symlink to a project with history", func(t *testing.T) {
		root := t.TempDir()
		target := projectDirFixture(t, t.TempDir(), "elsewhere", "resume.md", "config.toml")
		if err := os.MkdirAll(filepath.Join(root, "Projects"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "Projects", "proj")); err != nil {
			t.Skipf("symlink: %v", err)
		}
		_, err := ClassifyProjectDir(root, "proj")
		if err == nil || !strings.Contains(err.Error(), "Projects/proj is not a directory") {
			t.Fatalf("err = %v, want a symlinked project refused as not a directory", err)
		}
	})
	// Stat errors on the markers and on resume.md name the vault-relative path
	// and drop the *PathError's absolute one. Both shapes need no permissions,
	// so they run as root too: ENOTDIR from a regular FILE named commands (the
	// only marker candidate, so the error is returned), and ELOOP from a
	// resume.md symlink that points at itself.
	statErrRow := func(t *testing.T, name string, setup func(dir string) error, wantRel string) {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := projectDirFixture(t, root, "proj")
			if err := setup(dir); err != nil {
				t.Skipf("setup: %v", err)
			}
			_, err := ClassifyProjectDir(root, "proj")
			if err == nil {
				t.Fatal("a failed stat classified without error")
			}
			if !strings.Contains(err.Error(), wantRel) {
				t.Errorf("error does not name %s vault-relative: %v", wantRel, err)
			}
			if strings.Contains(err.Error(), root) {
				t.Errorf("error carries the host's absolute vault path: %v", err)
			}
		})
	}
	statErrRow(t, "marker stat error: a lone regular FILE named commands", func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "commands"), []byte("x"), 0o644)
	}, "Projects/proj/commands/README.md")
	statErrRow(t, "resume.md stat error: a symlink loop", func(dir string) error {
		return os.Symlink("resume.md", filepath.Join(dir, "resume.md"))
	}, "Projects/proj/resume.md")
}

// Fail-closed, and the order that decides when the walk error surfaces:
// resume.md, iterations.md, sessions/, tasks/, stopping at the first content.
func classifyProjectDirUnreadableSessionsRows(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	lock := func(t *testing.T, dir string) {
		t.Helper()
		sd := filepath.Join(dir, "sessions")
		if err := os.Chmod(sd, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sd, 0o755) })
	}
	t.Run("resume.md decides before the unreadable sessions/ is walked", func(t *testing.T) {
		root := t.TempDir()
		lock(t, projectDirFixture(t, root, "proj", "resume.md", "sessions/s.md"))
		got, err := ClassifyProjectDir(root, "proj")
		if err != nil || got != ProjectWithContent {
			t.Fatalf("got (%s, %v), want (with-content, nil)", got, err)
		}
	})
	t.Run("config.toml only: the unreadable sessions/ is an error, not no-content", func(t *testing.T) {
		root := t.TempDir()
		lock(t, projectDirFixture(t, root, "proj", "config.toml", "sessions/s.md"))
		_, err := ClassifyProjectDir(root, "proj")
		if err == nil {
			t.Fatal("an unreadable sessions/ classified without error: fail-open")
		}
		if !strings.Contains(err.Error(), "Projects/proj/sessions") {
			t.Errorf("error does not name Projects/proj/sessions vault-relative: %v", err)
		}
		if strings.Contains(err.Error(), root) {
			t.Errorf("error carries the host's absolute vault path: %v", err)
		}
	})
	// Fail-closed at ANY depth, not only at the walk root: an unreadable
	// subdirectory of sessions/ may hold history too. With only config.toml
	// otherwise, swallowing the error below the root would read this project
	// as scaffold-only.
	t.Run("config.toml only: an unreadable sessions/2026/ is an error at depth", func(t *testing.T) {
		root := t.TempDir()
		dir := projectDirFixture(t, root, "proj", "config.toml", "sessions/2026/s.md")
		sub := filepath.Join(dir, "sessions", "2026")
		if err := os.Chmod(sub, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
		_, err := ClassifyProjectDir(root, "proj")
		if err == nil {
			t.Fatal("an unreadable sessions/2026/ classified without error: fail-open below the walk root")
		}
		if !strings.Contains(err.Error(), "Projects/proj/sessions/2026") {
			t.Errorf("error does not name Projects/proj/sessions/2026 vault-relative: %v", err)
		}
		if strings.Contains(err.Error(), root) {
			t.Errorf("error carries the host's absolute vault path: %v", err)
		}
	})
}

func TestProjectDirStateStringAndInitialised(t *testing.T) {
	for _, tc := range []struct {
		s    ProjectDirState
		str  string
		init bool
	}{
		{ProjectAbsent, "absent", false},
		{ProjectPhantom, "phantom", false},
		{ProjectScaffoldOnly, "scaffold-only", true},
		{ProjectWithContent, "with-content", true},
	} {
		if tc.s.String() != tc.str || tc.s.Initialised() != tc.init {
			t.Errorf("%d: String()=%q Initialised()=%v, want %q %v", int(tc.s), tc.s.String(), tc.s.Initialised(), tc.str, tc.init)
		}
	}
}
