// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// literalNameCases pairs a vault path holding pathspec syntax with a file the
// path would match as a pattern. A leading ':' is pathspec magic: ':!x.md' and
// ':(exclude)x.md' exclude x.md, so read as patterns they name every OTHER
// path, and the decoy is simply some other dirty file.
var literalNameCases = []struct {
	named, decoy string
	posixOnly    bool // the name is not a valid Windows file name
}{
	{"Projects/p/a[1].md", "Projects/p/a1.md", false},
	{"Projects/p/b*.md", "Projects/p/bz.md", true},
	{"Projects/p/c?.md", "Projects/p/cz.md", true},
	{":!x.md", "Projects/p/other.md", true},
	{":(exclude)x.md", "Projects/p/other.md", true},
}

func skipInvalidName(t *testing.T, posixOnly bool) {
	t.Helper()
	if posixOnly && runtime.GOOS == "windows" {
		t.Skip("name is not a valid Windows file name")
	}
}

func headNames(t *testing.T, dir string) []string {
	t.Helper()
	return strings.Split(gitRun(t, dir, "-c", "core.quotepath=off", "show", "--name-only", "--format=", "HEAD"), "\n")
}

// A vault path is a name, never a pattern: committing a file whose name holds
// pathspec syntax must commit exactly that file and nothing it would match.
func TestCommitAndPushPathsReadsPathsLiterally(t *testing.T) {
	for _, tc := range literalNameCases {
		t.Run(tc.named, func(t *testing.T) {
			skipInvalidName(t, tc.posixOnly)
			dir := initTestRepo(t)
			writeFile(t, dir, tc.named, "named\n")
			writeFile(t, dir, tc.decoy, "nobody named me\n")
			if _, err := CommitAndPushPaths(dir, "probe", []string{tc.named}, false); err != nil {
				t.Fatal(err)
			}
			if got := headNames(t, dir); !slices.Equal(got, []string{tc.named}) {
				t.Fatalf("commit holds %q, want exactly [%q]", got, tc.named)
			}
			if staged := gitRun(t, dir, "-c", "core.quotepath=off", "diff", "--cached", "--name-only"); staged != "" {
				t.Fatalf("left staged after the commit: %q", staged)
			}
		})
	}
}

// Git matches a wildcard pathspec literally first when a file of that exact
// name exists, so * and ? leak only once the named file is gone: committing
// its deletion must not also commit, or leave staged, a file the name matches.
func TestCommitAndPushPathsCommitsADeletionLiterally(t *testing.T) {
	for _, tc := range literalNameCases {
		t.Run(tc.named, func(t *testing.T) {
			skipInvalidName(t, tc.posixOnly)
			dir := initTestRepo(t)
			writeFile(t, dir, tc.named, "named\n")
			gitRun(t, dir, "add", "-A")
			gitRun(t, dir, "commit", "-q", "-m", "seed")
			gitRun(t, dir, "--literal-pathspecs", "rm", "-q", "--", tc.named)
			gitRun(t, dir, "reset", "-q")
			writeFile(t, dir, tc.decoy, "nobody named me\n")
			if _, err := CommitAndPushPaths(dir, "probe", []string{tc.named}, false); err != nil {
				t.Fatal(err)
			}
			got := gitRun(t, dir, "-c", "core.quotepath=off", "show", "--name-status", "--format=", "HEAD")
			if got != "D\t"+tc.named {
				t.Fatalf("commit holds %q, want only the deletion of %q", got, tc.named)
			}
			if staged := gitRun(t, dir, "-c", "core.quotepath=off", "diff", "--cached", "--name-only"); staged != "" {
				t.Fatalf("left staged after the commit: %q", staged)
			}
		})
	}
}

// filterStageablePaths keeps an absent path only when git tracks it; a glob
// match on some other tracked file is not tracking.
func TestFilterStageablePathsSkipsAnAbsentPatternName(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/p/x1.md", "tracked\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	keep, skipped := filterStageablePaths(dir, []string{"Projects/p/x[1].md"})
	if len(keep) != 0 || !slices.Equal(skipped, []string{"Projects/p/x[1].md"}) {
		t.Fatalf("keep = %q, skipped = %q", keep, skipped)
	}
}

// The inherited global pathspec modes must not reach vp's git:
// GIT_GLOB_PATHSPECS re-enables globbing and GIT_ICASE_PATHSPECS folds case;
// both also make git refuse to run beside a literal setting.
// GIT_NOGLOB_PATHSPECS passes on main and is compatible with literal mode; its
// subtest is tidiness (the variable is stripped with the rest), not a
// regression guard.
func TestCommitAndPushPathsIgnoresInheritedPathspecModes(t *testing.T) {
	for _, env := range []string{"GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS"} {
		t.Run(env, func(t *testing.T) {
			if env == "GIT_ICASE_PATHSPECS" && (runtime.GOOS == "darwin" || runtime.GOOS == "windows") {
				t.Skip("a[1].md and A[1].md are one file on a case-insensitive filesystem")
			}
			dir := initTestRepo(t)
			writeFile(t, dir, "Projects/p/a[1].md", "named\n")
			writeFile(t, dir, "Projects/p/a1.md", "nobody named me\n")
			writeFile(t, dir, "Projects/p/A[1].md", "nor me\n")
			t.Setenv(env, "1")
			if _, err := CommitAndPushPaths(dir, "probe", []string{"Projects/p/a[1].md"}, false); err != nil {
				t.Fatal(err)
			}
			if got := headNames(t, dir); !slices.Equal(got, []string{"Projects/p/a[1].md"}) {
				t.Fatalf("commit holds %q", got)
			}
		})
	}
}

// GitPathIgnored is the one git call that cannot run literally (check-ignore
// refuses the mode), so it must neutralise ':' magic itself. Each name is
// ignored by an exact .gitignore line, beside a sibling that is not. The
// nested variant is a vault that is a plain subdirectory of another repository
// (VaultGitNested): a ":(top)" prefix would look the name up at the OUTER
// repository's top and fail it.
func TestGitPathIgnoredReadsTheNameLiterally(t *testing.T) {
	for _, tc := range []struct {
		ignored, notIgnored string
		posixOnly           bool
	}{
		// Passes on main too. It guards the opt-out: without
		// gitenv.GlobPathspecs, check-ignore exits 128 under literal mode.
		{"ign[1].md", "ign1.md", false},
		{":x.md", "x.md", true},
		{":!y.md", "y.md", true},
	} {
		for _, nested := range []bool{false, true} {
			name := tc.ignored
			if nested {
				name += "/nested"
			}
			t.Run(name, func(t *testing.T) {
				skipInvalidName(t, tc.posixOnly)
				dir := initTestRepo(t)
				if nested {
					dir = filepath.Join(dir, "vault")
				}
				// A leading '!' would negate the rule; '\[' escapes the bracket.
				line := strings.NewReplacer("!", `\!`, "[", `\[`).Replace(tc.ignored)
				writeFile(t, dir, ".gitignore", line+"\n")
				if nested {
					if kind, err := InspectVaultGit(dir); err != nil || kind != VaultGitNested {
						t.Fatalf("fixture: VaultGitKind = %v, %v; want VaultGitNested", kind, err)
					}
				}
				for rel, want := range map[string]bool{tc.ignored: true, tc.notIgnored: false} {
					got, err := GitPathIgnored(dir, rel)
					if err != nil {
						t.Fatalf("GitPathIgnored(%q): %v", rel, err)
					}
					if got != want {
						t.Errorf("GitPathIgnored(%q) = %v, want %v", rel, got, want)
					}
				}
			})
		}
	}
}

// "./" alone names the vault root, so an empty rel must be refused rather than
// answered "not ignored".
func TestGitPathIgnoredRefusesAnEmptyPath(t *testing.T) {
	dir := initTestRepo(t)
	if got, err := GitPathIgnored(dir, ""); err == nil {
		t.Fatalf("GitPathIgnored(\"\") = %v, nil; want an error", got)
	}
}
