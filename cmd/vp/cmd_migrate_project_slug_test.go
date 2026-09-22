// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

// ONE-SHOT: deleted with cmd_migrate_project_slug.go.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const (
	slugCmdFrom = "src-proj"
	slugCmdTo   = "dst-proj"
)

// slugCmdVault is a minimal real-git vault with one collision (.surface) so
// all three commits happen.
func slugCmdVault(t *testing.T) string {
	t.Helper()
	root := setupTestVaultEnv(t)
	gitRun(t, root, "init", "-b", "main")
	gitRun(t, root, "config", "user.email", "test@test.com")
	gitRun(t, root, "config", "user.name", "Test")
	P := "Projects/" + slugCmdFrom + "/"
	mkfile(t, root, ".gitignore", "*.bak\npalace/.local/\n.vp-locks/\n")
	mkfile(t, root, P+"resume.md", "---\nproject: "+slugCmdFrom+"\n---\n\n# "+slugCmdFrom+" — Working Context\n")
	mkfile(t, root, P+"sessions/2026-01-01-abcd1234-01.md",
		"---\nproject: "+slugCmdFrom+"\nnote_path: "+P+"sessions/2026-01-01-abcd1234-01.md\n---\nbody\n")
	mkfile(t, root, P+".surface", "surface = 7\n")
	mkfile(t, root, "palace/"+slugCmdFrom+"/.surface", "surface = 7\n")
	c := "A chunk."
	mkfile(t, root, "palace/"+slugCmdFrom+"/drawers/"+slugCmdFrom+"/general/drawers.jsonl",
		fmt.Sprintf(`{"id":"%s","hall":"facts","content":"%s","source_type":"session","source_ref":"s1","filed_at":"2026-01-01T00:00:00Z"}`+"\n",
			storage.DrawerID(slugCmdFrom, c), c))
	mkfile(t, root, "Projects/"+slugCmdTo+"/.surface", "surface = 6\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "fixture")
	return root
}

// slugCmdFakeProc is a /proc-shaped directory that passes the guard's
// self-visibility floor (code review round 2, D2).
func slugCmdFakeProc(t *testing.T) string {
	t.Helper()
	proc := t.TempDir()
	mk := func(pid int, comm string, argv []string, exe string) {
		d := filepath.Join(proc, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)
		if exe != "" {
			os.Symlink(exe, filepath.Join(d, "exe"))
		}
	}
	mk(os.Getpid(), "vp", []string{"vp", "migrate", "project-slug"}, "/usr/local/bin/vp")
	for i, c := range []string{"systemd", "bash", "sshd-session", "ssh-agent", "tmux", "node", "git", "sleep"} {
		mk(9000+i, c, []string{"/usr/bin/" + c}, "/usr/bin/"+c)
	}
	return proc
}

func slugCmdSeams(t *testing.T, procDir string) {
	t.Helper()
	prevProc, prevHome := slugProcDir, slugHomeDir
	home := t.TempDir()
	slugProcDir = procDir
	slugHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { slugProcDir, slugHomeDir = prevProc, prevHome })
}

func runSlugCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	fv, err := cli.ParseFlags(migrateProjectSlugFlags, args)
	if err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runMigrateProjectSlug(fv, &out, &errb)
	return code, out.String(), errb.String()
}

func slugCmdTreeHash(t *testing.T, root string) [32]byte {
	t.Helper()
	var paths []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		fmt.Fprintf(h, "%s %x\n", p, sha256.Sum256(b))
	}
	var s [32]byte
	copy(s[:], h.Sum(nil))
	return s
}

func TestMigrateProjectSlugReportJSONOnStdoutLiveOnStderr(t *testing.T) {
	root := slugCmdVault(t)
	slugCmdSeams(t, slugCmdFakeProc(t))
	before := slugCmdTreeHash(t, root)
	code, out, errOut := runSlugCmd(t, "--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--json")
	if code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var c storage.SlugCounts
	if err := json.Unmarshal([]byte(out), &c); err != nil {
		t.Fatalf("stdout is not the counts JSON: %v\n%s", err, out)
	}
	if c.From != slugCmdFrom || c.To != slugCmdTo || c.K1Tracked == 0 {
		t.Errorf("counts: %+v", c)
	}
	if strings.Contains(out, root) {
		t.Error("the JSON must not carry the vault root")
	}
	if !strings.Contains(errOut, "LIVE=") || strings.Contains(out, "LIVE=") {
		t.Errorf("the LIVE line belongs on stderr only; stderr=%q", errOut)
	}
	if slugCmdTreeHash(t, root) != before {
		t.Fatal("report mode wrote to the vault")
	}
}

func TestMigrateProjectSlugFlagRefusals(t *testing.T) {
	root := slugCmdVault(t)
	slugCmdSeams(t, slugCmdFakeProc(t))
	// Each case names the message it must refuse WITH: an exit code alone is
	// not enough, because a later refusal (a missing --expect file, say) would
	// hide a removed guard behind the same code.
	for _, tc := range []struct {
		want string
		args []string
	}{
		{"--from and --to are required", []string{"--to", slugCmdTo, "--vault", root}},
		{"--apply needs --expect", []string{"--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--apply"}},
		{"--phase must be", []string{"--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--phase", "bogus"}},
		{"--phase cache needs --apply", []string{"--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--phase", "cache"}},
		{"--attest-no-agents is accepted only with", []string{"--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--apply", "--attest-no-agents",
			"--expect", "x", "--expect-head", "y", "--journal", "z"}},
		// --force-root is this round's new flag surface: it overrides the
		// journal's vault identity, so it is accepted nowhere else.
		{"--force-root is accepted only with --revert-journal", []string{"--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--force-root",
			"--apply", "--expect", "x", "--expect-head", "y", "--journal", "z"}},
	} {
		code, _, errOut := runSlugCmd(t, tc.args...)
		if code != cli.ExitUser {
			t.Errorf("%v: exit %d, want %d", tc.args, code, cli.ExitUser)
		}
		if !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: stderr %q, want it to name %q", tc.args, errOut, tc.want)
		}
	}
}

func TestMigrateProjectSlugApplyViaCLI(t *testing.T) {
	root := slugCmdVault(t)
	slugCmdSeams(t, slugCmdFakeProc(t))
	_, counts, errOut := runSlugCmd(t, "--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--json")
	expect := filepath.Join(t.TempDir(), "counts.json")
	if err := os.WriteFile(expect, []byte(counts), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitHead(t, root)
	journal := filepath.Join(t.TempDir(), "journal.tsv")
	code, out, errOut := runSlugCmd(t, "--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root,
		"--apply", "--expect", expect, "--expect-head", head, "--journal", journal)
	if code != cli.ExitOK {
		t.Fatalf("apply exit %d\nstdout:%s\nstderr:%s", code, out, errOut)
	}
	for _, k := range []string{"K0 ", "K1 ", "K2 ", "cache: "} {
		if !strings.Contains(out, k) {
			t.Errorf("stdout lacks %q:\n%s", k, out)
		}
	}
	if !strings.Contains(errOut, "LIVE=") {
		t.Errorf("stderr lacks the LIVE line: %s", errOut)
	}
	if n := gitRun(t, root, "rev-list", "--count", head+"..HEAD"); n != "3" {
		t.Fatalf("commits = %s", n)
	}
	code, out, _ = runSlugCmd(t, "--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root,
		"--apply", "--expect", expect, "--expect-head", head, "--journal", filepath.Join(t.TempDir(), "j2.tsv"))
	if code != cli.ExitOK || !strings.Contains(out, "already applied") {
		t.Fatalf("re-apply: exit %d %s", code, out)
	}
	code, out, _ = runSlugCmd(t, "--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--revert-journal", filepath.Join(t.TempDir(), "absent.tsv"))
	if code != cli.ExitOK || !strings.Contains(out, "done=0") {
		t.Fatalf("revert of a missing journal: exit %d %s", code, out)
	}
}

func TestMigrateProjectSlugGuardRefusesViaCLI(t *testing.T) {
	root := slugCmdVault(t)
	proc := slugCmdFakeProc(t)
	os.MkdirAll(filepath.Join(proc, "999"), 0o755)
	os.WriteFile(filepath.Join(proc, "999", "comm"), []byte("claude\n"), 0o644)
	slugCmdSeams(t, proc)
	_, counts, _ := runSlugCmd(t, "--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--json")
	expect := filepath.Join(t.TempDir(), "counts.json")
	os.WriteFile(expect, []byte(counts), 0o644)
	head := gitHead(t, root)
	code, _, errOut := runSlugCmd(t, "--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root,
		"--apply", "--expect", expect, "--expect-head", head, "--journal", filepath.Join(t.TempDir(), "j.tsv"))
	if code == cli.ExitOK || !strings.Contains(errOut, "LIVE vault") {
		t.Fatalf("want a guard refusal, exit %d: %s", code, errOut)
	}
	if gitHead(t, root) != head {
		t.Fatal("a refused apply committed")
	}
}

func TestMigrateProjectSlugCheckGuard(t *testing.T) {
	root := slugCmdVault(t)
	proc := slugCmdFakeProc(t)
	slugCmdSeams(t, proc)
	code, out, errOut := runSlugCmd(t, "--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--check-guard")
	if code != cli.ExitOK || !strings.Contains(out, "GUARD OK") || !strings.Contains(errOut, "LIVE=") {
		t.Fatalf("empty process table: exit %d out=%q err=%q", code, out, errOut)
	}
	os.MkdirAll(filepath.Join(proc, "77"), 0o755)
	os.WriteFile(filepath.Join(proc, "77", "comm"), []byte("vp\n"), 0o644)
	os.WriteFile(filepath.Join(proc, "77", "cmdline"), []byte("vp\x00mcp\x00"), 0o644)
	if code, _, errOut := runSlugCmd(t, "--from", slugCmdFrom, "--to", slugCmdTo, "--vault", root, "--check-guard"); code != cli.ExitUser || !strings.Contains(errOut, "vp process") {
		t.Fatalf("a vp mcp process must fail the guard: exit %d %s", code, errOut)
	}
}
