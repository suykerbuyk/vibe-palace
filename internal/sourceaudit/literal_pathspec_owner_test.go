// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The tests for the literal-pathspec opt-out rule. Its healthy steady state is
// zero findings, so each break below is asserted to fire beside a tree of the
// sanctioned shapes that must stay clean.

const literalGitenv = `package gitenv

const literalPathspecs = "GIT_LITERAL_PATHSPECS=1"

const GlobPathspecs = "GIT_LITERAL_PATHSPECS=0"

var modes = []string{"GIT_GLOB_PATHSPECS", "GIT_ICASE_PATHSPECS"}

func SafeGitEnv(extra ...string) []string { return append([]string{literalPathspecs}, extra...) }
`

const literalStorage = `package storage

import "example.com/gitenv"

// CLEAN: the one owner.
func GitPathIgnored(vaultPath, rel string) (bool, error) {
	_ = gitenv.SafeGitEnv(gitenv.GlobPathspecs)
	return false, nil
}

// CLEAN: literal flags strengthen the default; a ':' inside a revision is not
// a leading one.
func show(rel string) []string {
	return []string{"--literal-pathspecs", "show", "HEAD:" + rel, "--", "palace/"}
}
`

func literalFindings(t *testing.T, pkgs map[string]string) []string {
	t.Helper()
	findings, err := Run(writeFixtureTree(t, pkgs))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindLiteralPathspecOptOut {
			out = append(out, f.Symbol)
		}
	}
	return out
}

func literalTree(extra string) map[string]string {
	pkgs := map[string]string{"gitenv": literalGitenv, "storage": literalStorage}
	if extra != "" {
		pkgs["tools"] = extra
	}
	return pkgs
}

func TestLiteralPathspecOwnerIsSilentOnTheSanctionedShapes(t *testing.T) {
	if got := literalFindings(t, literalTree("")); len(got) != 0 {
		t.Fatalf("findings on the sanctioned tree: %q", got)
	}
}

func TestLiteralPathspecOwnerReportsEachBypass(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"part a: GlobPathspecs outside its owner", `package tools

import "example.com/gitenv"

func listAll() []string { return gitenv.SafeGitEnv(gitenv.GlobPathspecs) }
`, "tools.listAll -> GlobPathspecs"},
		{"part a: under an alias, in a package-level var", `package tools

import ge "example.com/gitenv"

var env = ge.SafeGitEnv(ge.GlobPathspecs)
`, "tools.env -> GlobPathspecs"},
		{"part a: under a dot import", `package tools

import . "example.com/gitenv"

func listAll() []string { return SafeGitEnv(GlobPathspecs) }
`, "tools.listAll -> GlobPathspecs"},
		{"part b: the variable spelled by hand", `package tools

import "example.com/gitenv"

func listAll() []string { return gitenv.SafeGitEnv("GIT_LITERAL_PATHSPECS=0") }
`, "tools.listAll -> GIT_LITERAL_PATHSPECS"},
		{"part b: an inherited mode set by hand", `package tools

func env() []string { return []string{"GIT_GLOB_PATHSPECS=1"} }
`, "tools.env -> GIT_GLOB_PATHSPECS"},
		{"part b: --no-literal-pathspecs turns literal mode back off", `package tools

func args() []string { return []string{"--no-literal-pathspecs", "ls-files", "--", "palace/"} }
`, "tools.args -> --no-literal-pathspecs"},
		{"part c: long-form magic", `package tools

const localPathspec = ":(glob)palace/*/.local/**"
`, "tools.localPathspec -> magic :("},
		{"part c: short-form exclude", `package tools

func args() []string { return []string{"status", "--", ".", ":!.vp-locks"} }
`, "tools.args -> magic :!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := literalFindings(t, literalTree(tc.src)); !slices.Equal(got, []string{tc.want}) {
				t.Fatalf("findings = %q, want [%q]", got, tc.want)
			}
		})
	}
}

// A test file may spell anything: fixtures and probes need the strings.
func TestLiteralPathspecOwnerIgnoresTests(t *testing.T) {
	root := writeFixtureTree(t, literalTree(""))
	src := "package tools\n\nvar env = []string{\"GIT_LITERAL_PATHSPECS=0\", \":(glob)x\"}\n"
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tools", "probe_test.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	findings, err := Run(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		if f.Kind == KindLiteralPathspecOptOut {
			t.Errorf("finding in a test file: %s", f.Symbol)
		}
	}
}

func TestLiteralPathspecOwnerAnchors(t *testing.T) {
	got := literalFindings(t, map[string]string{"tools": "package tools\n"})
	for _, want := range []string{
		"sourceaudit.literalPathspecOwner/ABSENT/gitenv.GlobPathspecs",
		"sourceaudit.literalPathspecOwner/ABSENT/storage.GitPathIgnored",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing anchor %q in %q", want, got)
		}
	}
}
