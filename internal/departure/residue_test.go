// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package departure

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
)

// A departed project's Projects/<slug>/ survives a pull on every host that had
// an ignored or machine-local file under it. These pin that such a directory is
// still a departure (departed-ignores-a-directory-holding-only-ignored-residue,
// reproduced as case B on a throwaway vault at 61ce092), that anything git
// would carry keeps the slug live, and that every "cannot tell" is live.

func rgit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitenv.SafeGitEnv("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// departedVault is a git vault in which Projects/alpha/ was committed, then
// removed by a commit that also carries its moved-to-vault record — what a
// host holds after pulling a split purge. Projects/keep/ stays live. The
// .gitignore is the canonical vault set plus one non-canonical line.
func departedVault(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatal("git is required: these rows pin a git-backed rule and must not pass by skipping")
	}
	root := t.TempDir()
	rgit(t, root, "init", "-q", "-b", "main")
	put(t, root, ".gitignore", "palace/.local/\n*.bak\n*.new\n.vp-locks/\n.vp-fs-probe-*\n")
	put(t, root, "Projects/keep/resume.md", "keep\n")
	put(t, root, "Projects/alpha/resume.md", "alpha\n")
	put(t, root, "Projects/alpha/transcripts/a.manifest.json", "{}\n")
	rgit(t, root, "add", "-A")
	rgit(t, root, "commit", "-q", "-m", "seed")
	rgit(t, root, "rm", "-q", "-r", "Projects/alpha")
	record(t, root, Record{Slug: "alpha", Kind: MovedToVault, To: "git@example.invalid:q/quantum-vault.git", Date: "2026-09-25"})
	rgit(t, root, "add", "-A")
	rgit(t, root, "commit", "-q", "-m", "depart alpha")
	return root
}

// captureWarnings routes slog to a buffer for the test and returns it.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	resetResidueWarnMemo()
	t.Cleanup(resetResidueWarnMemo)
	return &buf
}

func warnings(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "departure residue undecidable:")
}

// B. The reproduction: one ignored backup is the whole directory, and it is a
// departure through Find, Resolve and List alike.
func TestResidue_IgnoredFilesOnlyIsDeparted(t *testing.T) {
	for name, rel := range map[string]string{
		"bak":         "Projects/alpha/transcripts/a.manifest.json.0.bak",
		"new":         "Projects/alpha/x.new",
		"vp-locks":    "Projects/alpha/.vp-locks/l",
		"local":       "Projects/alpha/sub/.local/f",
		"empty-dirs":  "",
		"fs-probe":    "Projects/alpha/.vp-fs-probe-1",
		"two-classes": "Projects/alpha/a.bak",
	} {
		t.Run(name, func(t *testing.T) {
			root := departedVault(t)
			if rel != "" {
				put(t, root, rel, "residue\n")
			}
			if name == "empty-dirs" {
				if err := os.MkdirAll(filepath.Join(root, "Projects/alpha/transcripts/deep"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if name == "two-classes" {
				put(t, root, "Projects/alpha/.local/imported-sessions.jsonl", "{}\n")
			}
			rec, ok := Find(root, "alpha")
			if !ok || rec.Kind != MovedToVault {
				t.Fatalf("Projects/alpha/ holds only residue: Find = %+v %v, want the departure", rec, ok)
			}
			if chain, ok := Resolve(root, "alpha"); !ok || len(chain) != 1 {
				t.Errorf("Resolve = %+v %v, want the one record", chain, ok)
			}
			if got := List(root); len(got) != 1 || got[0].Slug != "alpha" {
				t.Errorf("List = %+v, want alpha", got)
			}
		})
	}
}

// B, by git's other ignore sources: a .gitignore above the slug tree and
// info/exclude. A hand-written matcher of the root .gitignore would miss both.
func TestResidue_HonoursEveryIgnoreSource(t *testing.T) {
	t.Run("gitignore-above-the-tree", func(t *testing.T) {
		root := departedVault(t)
		put(t, root, "Projects/.gitignore", "*.tmp\n")
		rgit(t, root, "add", "Projects/.gitignore")
		rgit(t, root, "commit", "-q", "-m", "ignore tmp")
		put(t, root, "Projects/alpha/x.tmp", "scratch\n")
		if _, ok := Find(root, "alpha"); !ok {
			t.Error("a file a Projects/.gitignore ignores is residue")
		}
	})
	t.Run("info-exclude", func(t *testing.T) {
		root := departedVault(t)
		put(t, root, ".git/info/exclude", "*.scratch\n")
		put(t, root, "Projects/alpha/x.scratch", "scratch\n")
		if _, ok := Find(root, "alpha"); !ok {
			t.Error("a file info/exclude ignores is residue")
		}
	})
}

// The directory wins: anything git would carry keeps the slug live — a vp init
// bring-back's untracked scaffold, a lone untracked stamp, residue beside it.
func TestResidue_ContentKeepsTheSlugLive(t *testing.T) {
	for name, rel := range map[string]string{
		"bring-back":     "Projects/alpha/commands/README.md",
		"lone-surface":   "Projects/alpha/.surface",
		"beside-residue": "Projects/alpha/memory/probe-note.md",
	} {
		t.Run(name, func(t *testing.T) {
			root := departedVault(t)
			put(t, root, "Projects/alpha/transcripts/a.manifest.json.0.bak", "residue\n")
			put(t, root, rel, "content\n")
			if _, ok := Find(root, "alpha"); ok {
				t.Errorf("%s is content git would carry: the slug is live", rel)
			}
			if got := List(root); len(got) != 0 {
				t.Errorf("List = %+v, want nothing", got)
			}
		})
	}
}

// No record, residue only: the slug is live and git is never asked (the
// record gates the probe). The zero-process pin across BOTH seams lives in
// internal/project, which reaches this through Departed.
func TestResidue_NoRecordIsLiveWithoutAskingGit(t *testing.T) {
	root := departedVault(t)
	put(t, root, "Projects/beta/x.bak", "residue\n")
	defer func(g string) { residueGit = g }(residueGit)
	residueGit = filepath.Join(t.TempDir(), "must-not-run")
	buf := captureWarnings(t)

	if _, ok := Find(root, "beta"); ok {
		t.Error("no record: never departed")
	}
	if warnings(buf) != 0 {
		t.Errorf("no record must not reach git at all, got warnings:\n%s", buf)
	}
}

// A rename chain whose middle hop is residue-only still resolves to its end.
func TestResidue_ChainThroughAResidueOnlyHop(t *testing.T) {
	root := departedVault(t)
	record(t, root, Record{Slug: "old", Kind: Renamed, To: "alpha"})
	// alpha itself is recorded as moved-to-vault, and its directory is residue.
	put(t, root, "Projects/alpha/a.bak", "residue\n")
	chain, ok := Resolve(root, "old")
	if !ok || len(chain) != 2 || chain[1].Slug != "alpha" || chain[1].Kind != MovedToVault {
		t.Errorf("Resolve(old) = %+v %v, want old -> alpha (moved-to-vault)", chain, ok)
	}
}

// Tracing must not make a residue-only slug undecidable: a user tracing git
// (by env or by global config) would otherwise keep every such slug live.
func TestResidue_TracingIsSilenced(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[trace2]\n\tnormalTarget = 2\n\tperfTarget = 2\n\teventTarget = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string][2]string{
		"GIT_TRACE":          {"GIT_TRACE", "1"},
		"GIT_TRACE2":         {"GIT_TRACE2", "1"},
		"trace2-in-a-config": {"GIT_CONFIG_GLOBAL", cfg},
	} {
		t.Run(name, func(t *testing.T) {
			root := departedVault(t)
			put(t, root, "Projects/alpha/a.bak", "residue\n")
			t.Setenv(env[0], env[1])
			buf := captureWarnings(t)
			if _, ok := Find(root, "alpha"); !ok {
				t.Errorf("with %s=%s a residue-only slug must still be departed; warnings:\n%s", env[0], env[1], buf)
			}
		})
	}
}

// "Cannot tell" is live. A vault that is not its own repository is a
// permanent configuration and is silent; a runtime fault warns once.
func TestResidue_CannotTellIsLive(t *testing.T) {
	fakeGit := func(t *testing.T, body string) string {
		t.Helper()
		if runtime.GOOS == "windows" {
			t.Skip("needs a POSIX shell script as the fake git")
		}
		real, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "git")
		script := "#!/bin/sh\ncase \"$*\" in *rev-parse*) exec " + real + " \"$@\";; esac\n" + body + "\n"
		if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rows := []struct {
		name  string
		setup func(t *testing.T) string // returns the vault root
		warns int
	}{
		{"non-git-vault", func(t *testing.T) string {
			root := t.TempDir()
			record(t, root, Record{Slug: "alpha", Kind: MovedToVault})
			put(t, root, "Projects/alpha/a.bak", "residue\n")
			return root
		}, 0},
		{"nested-in-an-enclosing-repo", func(t *testing.T) string {
			outer := t.TempDir()
			rgit(t, outer, "init", "-q", "-b", "main")
			root := filepath.Join(outer, "vault")
			record(t, root, Record{Slug: "alpha", Kind: MovedToVault})
			put(t, root, "Projects/alpha/a.bak", "residue\n")
			return root
		}, 0},
		{"git-missing", func(t *testing.T) string {
			root := departedVault(t)
			put(t, root, "Projects/alpha/a.bak", "residue\n")
			residueGit = filepath.Join(t.TempDir(), "no-such-git")
			return root
		}, 1},
		{"timeout", func(t *testing.T) string {
			root := departedVault(t)
			put(t, root, "Projects/alpha/a.bak", "residue\n")
			if runtime.GOOS == "windows" {
				t.Skip("needs a POSIX shell script as the fake git")
			}
			p := filepath.Join(t.TempDir(), "git")
			if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			residueGit, residueTimeout = p, 200*time.Millisecond
			return root
		}, 1},
		{"broken-fsmonitor", func(t *testing.T) string {
			root := departedVault(t)
			put(t, root, "Projects/alpha/a.bak", "residue\n")
			hook := filepath.Join(t.TempDir(), "fsmonitor")
			if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'fatal: boom' >&2\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			rgit(t, root, "config", "core.fsmonitor", hook)
			return root
		}, 1},
		{"stdout-not-nul-terminated", func(t *testing.T) string {
			root := departedVault(t)
			put(t, root, "Projects/alpha/a.bak", "residue\n")
			residueGit = fakeGit(t, "printf 'Projects/alpha/x'")
			return root
		}, 1},
		{"stdout-outside-the-tree", func(t *testing.T) string {
			root := departedVault(t)
			put(t, root, "Projects/alpha/a.bak", "residue\n")
			residueGit = fakeGit(t, "printf 'Projects/keep/resume.md\\0'")
			return root
		}, 1},
		{"unreadable-subdirectory", func(t *testing.T) string {
			if runtime.GOOS == "windows" || os.Geteuid() == 0 {
				t.Skip("needs POSIX permissions and a non-root user: root reads through mode 000, so this row would pass for the wrong reason")
			}
			root := departedVault(t)
			put(t, root, "Projects/alpha/sub/real-work.md", "content\n")
			sub := filepath.Join(root, "Projects/alpha/sub")
			if err := os.Chmod(sub, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
			return root
		}, 1},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			defer func(g string, d time.Duration) { residueGit, residueTimeout = g, d }(residueGit, residueTimeout)
			root := r.setup(t)
			buf := captureWarnings(t)
			if _, ok := Find(root, "alpha"); ok {
				t.Fatalf("cannot tell: the slug must stay live")
			}
			if got := warnings(buf); got != r.warns {
				t.Errorf("warnings = %d, want %d:\n%s", got, r.warns, buf)
			}
			// Once per slug and cause: a second call inside the TTL is silent.
			Find(root, "alpha")
			if got := warnings(buf); got != r.warns {
				t.Errorf("a second call warned again (%d, want %d):\n%s", got, r.warns, buf)
			}
		})
	}
}

// The warning's shape: its category is what vplog counts and bootstrap's
// health alert prints, it carries no fault=caller, it re-warns after the TTL
// (shorter than the 24 h health window), and a new cause warns at once.
func TestResidue_WarningShapeTTLAndCause(t *testing.T) {
	defer func(n func() time.Time) { residueNow = n }(residueNow)
	now := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	residueNow = func() time.Time { return now }
	buf := captureWarnings(t)

	warnResidueUndecidable("alpha", "cause-1")
	line := buf.String()
	if !strings.Contains(line, `msg="departure residue undecidable: `) {
		t.Errorf("the message must start with the category, got %q", line)
	}
	if strings.Contains(line, "fault=") {
		t.Errorf("the warning must carry no fault attribute, got %q", line)
	}
	warnResidueUndecidable("alpha", "cause-1")
	if warnings(buf) != 1 {
		t.Fatalf("inside the TTL: want 1 warning, got %d", warnings(buf))
	}
	warnResidueUndecidable("alpha", "cause-2")
	if warnings(buf) != 2 {
		t.Errorf("a new cause must warn at once: got %d", warnings(buf))
	}
	now = now.Add(residueWarnTTL + time.Minute)
	warnResidueUndecidable("alpha", "cause-1")
	if warnings(buf) != 3 {
		t.Errorf("past the TTL the same cause must warn again: got %d", warnings(buf))
	}
	if residueWarnTTL >= 24*time.Hour {
		t.Errorf("residueWarnTTL %v must be shorter than the 24 h health window", residueWarnTTL)
	}
}

// probeResidue's timeout covers both git calls, so a hung git cannot hold a
// dispatch for longer than it.
func TestResidue_ProbeHonoursItsDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell script as the fake git")
	}
	root := departedVault(t)
	p := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(g string, d time.Duration) { residueGit, residueTimeout = g, d }(residueGit, residueTimeout)
	residueGit, residueTimeout = p, 200*time.Millisecond
	captureWarnings(t)
	start := time.Now()
	v, _ := probeResidue(root, "Projects/alpha/")
	if v != residueFault {
		t.Errorf("a hung git is a fault, got %v", v)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("probe took %v", el)
	}
}

// resetResidueWarnMemo forgets every logged warning, so each row that counts
// warnings starts from none. The memo is process-wide: those rows are not
// t.Parallel.
func resetResidueWarnMemo() {
	residueWarned.Range(func(k, _ any) bool {
		residueWarned.Delete(k)
		return true
	})
}

// D1. The non-git check reads git's ENGLISH message. Under a translated locale
// a non-git vault must still be silent "not departed", never a warning. The
// row needs the host's translation to bite, so it proves that first and skips
// with a reason when it would pass vacuously.
func TestResidue_NonGitVaultIsSilentUnderATranslatedLocale(t *testing.T) {
	t.Setenv("LANGUAGE", "de")
	t.Setenv("LANG", "en_US.UTF-8")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "")
	root := t.TempDir()
	probe := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel")
	probe.Env = gitenv.SafeGitEnv()
	if out, _ := probe.CombinedOutput(); strings.Contains(string(out), "not a git repository") {
		t.Skipf("git prints English under LANGUAGE=de here (no translation or locale), so this row cannot bite: %s", out)
	}
	record(t, root, Record{Slug: "alpha", Kind: MovedToVault})
	put(t, root, "Projects/alpha/a.bak", "residue\n")
	buf := captureWarnings(t)
	if _, ok := Find(root, "alpha"); ok {
		t.Error("a non-git vault cannot judge residue: the slug stays live")
	}
	if n := warnings(buf); n != 0 {
		t.Errorf("a non-git vault is a permanent configuration, never a warning; got %d:\n%s", n, buf)
	}
}

// D2. Projects/<slug> that is not a directory is not a residue tree: a symlink
// (git lists it as "Projects/alpha", which the "Projects/alpha/" pathspec
// misses, so it read as departed) and a tracked regular file (which read as
// an undecidable warning). Both are live and silent, as before the probe.
func TestResidue_NonDirectoryAtTheSlugIsLiveAndSilent(t *testing.T) {
	t.Run("symlink-to-a-directory-with-content", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("needs POSIX symlinks")
		}
		root := departedVault(t)
		target := t.TempDir()
		put(t, target, "real.md", "real work\n")
		if err := os.Symlink(target, filepath.Join(root, "Projects", "alpha")); err != nil {
			t.Fatal(err)
		}
		buf := captureWarnings(t)
		if _, ok := Find(root, "alpha"); ok {
			t.Error("a symlink at Projects/alpha is not a departed residue tree")
		}
		if n := warnings(buf); n != 0 {
			t.Errorf("warnings = %d, want 0:\n%s", n, buf)
		}
	})
	t.Run("tracked-regular-file", func(t *testing.T) {
		root := departedVault(t)
		put(t, root, "Projects/alpha", "a file where the project was\n")
		rgit(t, root, "add", "Projects/alpha")
		rgit(t, root, "commit", "-q", "-m", "a file at the slug")
		buf := captureWarnings(t)
		if _, ok := Find(root, "alpha"); ok {
			t.Error("a regular file at Projects/alpha is not a departed residue tree")
		}
		if n := warnings(buf); n != 0 {
			t.Errorf("warnings = %d, want 0:\n%s", n, buf)
		}
	})
}

// A newline is a legal filename byte, and -z output does not quote it: it is
// content (or residue) like any other name, never a shape fault.
func TestResidue_NewlineInAFilenameIsNotAFault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a newline is not a legal Windows filename byte")
	}
	t.Run("ignored", func(t *testing.T) {
		root := departedVault(t)
		put(t, root, "Projects/alpha/we\nird.bak", "residue\n")
		buf := captureWarnings(t)
		if _, ok := Find(root, "alpha"); !ok {
			t.Errorf("an ignored file with a newline in its name is residue: want departed; warnings:\n%s", buf)
		}
		if n := warnings(buf); n != 0 {
			t.Errorf("warnings = %d, want 0:\n%s", n, buf)
		}
	})
	t.Run("content", func(t *testing.T) {
		root := departedVault(t)
		put(t, root, "Projects/alpha/we\nird.md", "real work\n")
		buf := captureWarnings(t)
		if _, ok := Find(root, "alpha"); ok {
			t.Error("a carried file with a newline in its name is content: want live")
		}
		if n := warnings(buf); n != 0 {
			t.Errorf("warnings = %d, want 0:\n%s", n, buf)
		}
	})
}
