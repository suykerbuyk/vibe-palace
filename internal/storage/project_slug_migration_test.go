// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// ONE-SHOT: deleted with project_slug_migration.go.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	slugFrom = "quantum-ng"
	slugTo   = "qa-metabuild-system"
)

func slugWrite(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func slugRead(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func drawerRow(id, hall, content, sourceType, ref string) string {
	return fmt.Sprintf(`{"id":"%s","hall":"%s","content":"%s","source_type":"%s","source_ref":"%s","filed_at":"2026-08-22T00:00:00Z","added_by":"capture"}`,
		id, hall, content, sourceType, ref)
}

// slugEnv isolates git and the host config for one test.
func slugEnv(t *testing.T) string {
	t.Helper()
	if !GitAvailable() {
		t.Skip("git not in PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	return home
}

// slugFixture builds a real-git vault shaped like the live one: a source
// project with every stored-identifier class, a pre-existing target with the
// colliding session note, scaffolds and stray decisions, an ignored .bak, an
// Audits baseline, and a host-local embed cache.
// slugFakeProc builds a /proc-shaped directory that passes the self-visibility
// floor: this process, plus enough ordinary entries that the scan is looking
// at something like a host's process table (code review round 2, D2). Extra
// entries are added by the caller.
func slugFakeProc(t *testing.T) string {
	t.Helper()
	proc := t.TempDir()
	mk := func(pid int, comm string, argv []string, exe string) {
		d := filepath.Join(proc, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		if exe != "" {
			_ = os.Symlink(exe, filepath.Join(d, "exe"))
		}
	}
	mk(os.Getpid(), "vp", []string{"vp", "migrate", "project-slug"}, "/usr/local/bin/vp")
	for i, p := range []struct {
		comm, exe string
		argv      []string
	}{
		{"systemd", "/usr/lib/systemd/systemd", []string{"/usr/lib/systemd/systemd", "--user"}},
		{"bash", "/usr/bin/bash", []string{"-bash"}},
		{"sshd-session", "", []string{"sshd-session:", "user@pts/0"}}, // no exe: non-dumpable, and NOT a refusal
		{"ssh-agent", "", []string{"/usr/bin/ssh-agent", "-D"}},
		{"tmux: server", "/usr/bin/tmux", []string{"tmux"}},
		{"node", "/usr/bin/node", []string{"node", "/opt/app/server.js"}},
		{"git", "/usr/bin/git", []string{"git", "status"}},
		{"sleep", "/usr/bin/sleep", []string{"sleep", "60"}},
	} {
		mk(9000+i, p.comm, p.argv, p.exe)
	}
	return proc
}

func slugFixture(t *testing.T) string {
	t.Helper()
	slugEnv(t)
	root := t.TempDir()
	gitRun(t, root, "init", "-b", "main")
	gitRun(t, root, "config", "user.email", "test@example.com")
	gitRun(t, root, "config", "user.name", "Test")
	P := "Projects/" + slugFrom + "/"
	Q := "Projects/" + slugTo + "/"
	slugWrite(t, root, ".gitignore", "*.bak\npalace/.local/\n.vp-locks/\n")
	slugWrite(t, root, P+"sessions/2026-08-22-0f0d1eb5-01.md",
		"---\nsession_id: 2026-08-22-0f0d1eb5-01\nproject: "+slugFrom+"\niteration: 1\nnote_path: "+P+"sessions/2026-08-22-0f0d1eb5-01.md\narchive: "+P+"transcripts/2026-08-22-aaaa.manifest.json\n---\n\nBody mentions quantum-ng as prose.\n")
	slugWrite(t, root, P+"sessions/2026-08-22-0f0d1eb5-02.md",
		"---\nsession_id: 2026-08-22-0f0d1eb5-02\nproject: "+slugFrom+"\niteration: 2\nnote_path: "+P+"sessions/2026-08-22-0f0d1eb5-02.md\n---\n\nSecond.\n")
	slugWrite(t, root, P+"transcripts/2026-08-22-aaaa.manifest.json",
		"{\n  \"session_id\": \"aaaa\",\n  \"project_slug\": \""+slugFrom+"\",\n  \"vault_rel_session_note\": \""+P+"sessions/2026-08-22-0f0d1eb5-01.md\"\n}\n")
	slugWrite(t, root, P+"transcripts/2026-08-22-aaaa.jsonl.zst", "zstd-bytes")
	slugWrite(t, root, P+"transcripts/2026-08-22-aaaa.manifest.json.1234.bak", "old manifest backup\n")
	slugWrite(t, root, P+"tasks/active-task.md", "# Active\n\n**Status:** planning\n")
	slugWrite(t, root, P+"tasks/done/done-task.md", "# Done\n\n**Status:** done\n")
	slugWrite(t, root, P+"tasks/cancelled/cancelled-task.md", "# Cancelled\n\n**Status:** cancelled\n")
	slugWrite(t, root, P+"memory/one-build-at-a-time.md", "---\nname: one-build-at-a-time\n---\nquantum-ng prose\n")
	slugWrite(t, root, P+"notes/escalation.md", "---\ntype: security\nproject: "+slugFrom+"\ntracked_by: "+P+"tasks/active-task.md\n---\nbody\n")
	slugWrite(t, root, P+"resume.md", "---\nproject: "+slugFrom+"\n---\n\n# "+slugFrom+" — Working Context\n\ntext\n")
	slugWrite(t, root, P+"workflow.md", "# "+slugFrom+" — Workflow\n\nsteps\n")
	slugWrite(t, root, P+"iterations.md", "## Iteration 1 — start\n\n## Iteration 2 — next\n")
	slugWrite(t, root, P+"commit-log.md", "## commit abc\n")
	slugWrite(t, root, P+"commit-log.anchor", "f01cbe4\n")
	slugWrite(t, root, P+"commands/README.md", "old commands readme\n")
	slugWrite(t, root, P+"skills/README.md", "old skills readme\n")
	slugWrite(t, root, P+".surface", "surface = 7\n")

	R := "palace/" + slugFrom + "/"
	d1 := "Decision one."
	d2 := "Decision two."
	g1 := "General chunk."
	slugWrite(t, root, R+".surface", "surface = 7\n")
	slugWrite(t, root, R+"kg/entities.jsonl", `{"id":"project-quantum-ng","name":"quantum-ng","type":"project"}`+"\n")
	slugWrite(t, root, R+"kg/triples/quantum-ng_0ce7606f--mentioned_in_efba2031--s1_abcd1234.json", "{\"subject\": \"quantum-ng\"}\n")
	slugWrite(t, root, R+"drawers/"+slugFrom+"/decisions/drawers.jsonl",
		drawerRow(DrawerID(slugFrom, d1), "decisions", d1, "decision", "session/2026-08-22-0f0d1eb5-01#decision/0/"+DrawerID(slugFrom, d1))+"\n"+
			drawerRow(DrawerID(slugFrom, d2), "decisions", d2, "decision", "session/2026-08-22-0f0d1eb5-02#decision/0/"+DrawerID(slugFrom, d2))+"\n")
	slugWrite(t, root, R+"drawers/"+slugFrom+"/general/drawers.jsonl",
		drawerRow(DrawerID(slugFrom, g1), "facts", g1, "session", "aaaa")+"\n")

	s1 := "Stray decision."
	slugWrite(t, root, Q+".surface", "surface = 6\n")
	slugWrite(t, root, Q+"commands/README.md", "new commands readme\n")
	slugWrite(t, root, Q+"skills/README.md", "new skills readme\n")
	slugWrite(t, root, Q+"sessions/2026-08-22-0f0d1eb5-01.md",
		"---\nsession_id: 2026-08-22-0f0d1eb5-01\nproject: "+slugTo+"\niteration: 1\nnote_path: "+Q+"sessions/2026-08-22-0f0d1eb5-01.md\n---\n\nStray Grok session.\n")
	slugWrite(t, root, "palace/"+slugTo+"/.surface", "surface = 3\n")
	slugWrite(t, root, "palace/"+slugTo+"/drawers/"+slugTo+"/decisions/drawers.jsonl",
		drawerRow(DrawerID(slugTo, s1), "decisions", s1, "decision", "session/2026-08-22-0f0d1eb5-01#decision/0/"+DrawerID(slugTo, s1))+"\n")

	slugWrite(t, root, "Audits/baseline.json",
		"{\n  \"dimensions\": {\n    \"archive-roundtrip\": {\n      \"reason\": \"see Projects/quantum-ng/transcripts/2026-08-22-aaaa.manifest.json for why\",\n      \"accepted\": [\n        \""+P+"transcripts/2026-08-22-aaaa.manifest.json\"\n      ]\n    }\n  }\n}\n")
	slugWrite(t, root, "Audits/.surface", "surface = 3\n")

	C := "palace/.local/embed-cache/"
	slugWrite(t, root, C+slugFrom+"/"+DrawerID(slugFrom, d1)+".vec", "vec-d1")
	slugWrite(t, root, C+slugFrom+"/"+DrawerID(slugFrom, d2)+".vec", "vec-d2")
	slugWrite(t, root, C+slugFrom+"/"+DrawerID(slugFrom, g1)+".vec", "vec-g1")
	slugWrite(t, root, C+slugFrom+"/note."+slugFrom+".2026-08-22-0f0d1eb5-01.c0.vec", "vec-note-legit-01")
	slugWrite(t, root, C+slugFrom+"/iter."+slugFrom+".1.m0.raw.c0.vec", "vec-iter1")
	slugWrite(t, root, C+slugFrom+"/orphan0.vec", "vec-orphan")
	slugWrite(t, root, C+slugTo+"/"+DrawerID(slugTo, s1)+".vec", "vec-stray-drawer")
	slugWrite(t, root, C+slugTo+"/note."+slugTo+".2026-08-22-0f0d1eb5-01.c0.vec", "vec-stray-note")

	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "fixture")
	return root
}

func slugCounts(t *testing.T, root string) []byte {
	t.Helper()
	p, err := PlanProjectSlugMigration(root, slugFrom, slugTo)
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Counts.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func slugOpts(t *testing.T, root string) SlugApplyOptions {
	t.Helper()
	return SlugApplyOptions{
		Root: root, From: slugFrom, To: slugTo,
		ExpectHead:  gitRun(t, root, "rev-parse", "HEAD"),
		Expect:      slugCounts(t, root),
		JournalPath: filepath.Join(t.TempDir(), "journal.tsv"),
		ProcDir:     slugFakeProc(t),
		Home:        t.TempDir(),
	}
}

// treeHash hashes every file under root. With withGit false it skips .git and
// the host-local .vp-locks sidecars, for comparing a working tree across a
// rollback (git keeps the reset-away objects, which is fine).
func treeHash(t *testing.T, root string, withGit ...bool) string {
	t.Helper()
	h := sha256.New()
	skipGit := len(withGit) > 0 && !withGit[0]
	var paths []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && skipGit && (d.Name() == ".git" || d.Name() == ".vp-locks") {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(h, "%s\x00%x\n", rel, sha256.Sum256(b))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestSlugPlanWritesNothing(t *testing.T) {
	root := slugFixture(t)
	before := treeHash(t, root)
	p, err := PlanProjectSlugMigration(root, slugFrom, slugTo)
	if err != nil {
		t.Fatal(err)
	}
	if after := treeHash(t, root); after != before {
		t.Fatal("planning changed the vault")
	}
	want := map[string]int{"W1": 2, "W1b": 1, "W2": 2, "W2b": 1, "W3": 1, "W4": 1, "W5": 1, "W6": 2, "W7": 1,
		"W8": 1, "W9": 3, "W10": 3, "W10b": 2, "W10c": 1}
	for k, v := range want {
		if p.Counts.Classes[k] != v {
			t.Errorf("class %s = %d, want %d", k, p.Counts.Classes[k], v)
		}
	}
	if len(p.Counts.K0Delete) != 4 || len(p.Counts.K0Rename) != 2 {
		t.Errorf("K0: %d deletes, %d renames; want 4 and 2: %+v", len(p.Counts.K0Delete), len(p.Counts.K0Rename), p.Counts)
	}
	if len(p.Counts.K1Untracked) != 1 {
		t.Errorf("untracked = %v, want the one .bak", p.Counts.K1Untracked)
	}
}

func TestSlugApplyEndToEnd(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root)
	var log strings.Builder
	o.Log = &log
	res, err := ApplyProjectSlugMigration(o)
	if err != nil {
		t.Fatalf("apply: %v\nlog:\n%s", err, log.String())
	}
	for i, k := range res.K {
		if k == "" {
			t.Fatalf("K%d not recorded", i)
		}
	}
	// RB8 asserts this live: after the apply and the cache step the tree is
	// clean, with every cache artifact ignored (imp2 round-1 N6).
	if dirt, err := slugDirt(root); err != nil || len(dirt) != 0 {
		t.Fatalf("a successful apply must leave a clean tree, got %v (err %v)", dirt, err)
	}
	if n := gitRun(t, root, "rev-list", "--count", o.ExpectHead+"..HEAD"); n != "3" {
		t.Fatalf("commits = %s, want 3", n)
	}
	Q := "Projects/" + slugTo + "/"
	for _, gone := range []string{"Projects/" + slugFrom, "palace/" + slugFrom, "palace/.local/embed-cache/" + slugFrom} {
		if slugExists(root, gone) {
			t.Errorf("%s still exists", gone)
		}
	}
	for _, tf := range []string{"tasks/active-task.md", "tasks/done/done-task.md", "tasks/cancelled/cancelled-task.md"} {
		if !slugExists(root, Q+tf) {
			t.Errorf("task %s did not keep its relative location", tf)
		}
	}
	if !slugExists(root, Q+"transcripts/2026-08-22-aaaa.manifest.json.1234.bak") {
		t.Error(".bak was not carried")
	}
	s01 := slugRead(t, root, Q+"sessions/2026-08-22-0f0d1eb5-01.md")
	if !strings.Contains(s01, "\nproject: "+slugTo+"\n") || !strings.Contains(s01, "note_path: "+Q+"sessions/2026-08-22-0f0d1eb5-01.md") ||
		!strings.Contains(s01, "archive: "+Q+"transcripts/2026-08-22-aaaa.manifest.json") || !strings.Contains(s01, "Body mentions quantum-ng as prose.") {
		t.Errorf("session -01 rewrite wrong:\n%s", s01)
	}
	stray := slugRead(t, root, Q+"sessions/2026-08-22-0f0d1eb5-03.md")
	if !strings.Contains(stray, "session_id: 2026-08-22-0f0d1eb5-03") || !strings.Contains(stray, "iteration: 3\n") ||
		!strings.Contains(stray, "note_path: "+Q+"sessions/2026-08-22-0f0d1eb5-03.md") || !strings.Contains(stray, "Stray Grok session.") {
		t.Errorf("stray note not renumbered:\n%s", stray)
	}
	man := slugRead(t, root, Q+"transcripts/2026-08-22-aaaa.manifest.json")
	if !strings.Contains(man, `"project_slug": "`+slugTo+`",`) || !strings.Contains(man, `"vault_rel_session_note": "`+Q+`sessions/2026-08-22-0f0d1eb5-01.md"`) {
		t.Errorf("manifest rewrite wrong:\n%s", man)
	}
	bl := slugRead(t, root, "Audits/baseline.json")
	if !strings.Contains(bl, `"`+Q+`transcripts/2026-08-22-aaaa.manifest.json"`) || !strings.Contains(bl, "see Projects/quantum-ng/transcripts/") {
		t.Errorf("baseline rewrite wrong:\n%s", bl)
	}
	if !strings.Contains(slugRead(t, root, "Audits/.surface"), "7") {
		t.Error("Audits/.surface not stamped")
	}
	if got := slugRead(t, root, Q+"workflow.md"); !strings.HasPrefix(got, "# "+slugTo+" — Workflow") {
		t.Errorf("workflow H1: %q", got)
	}
	if got := slugRead(t, root, Q+"notes/escalation.md"); !strings.Contains(got, "tracked_by: "+Q+"tasks/active-task.md") {
		t.Errorf("tracked_by: %q", got)
	}
	dec := slugRead(t, root, "palace/"+slugTo+"/drawers/"+slugTo+"/decisions/drawers.jsonl")
	for _, c := range []string{"Decision one.", "Decision two.", "Stray decision."} {
		if !strings.Contains(dec, `"id":"`+DrawerID(slugTo, c)+`"`) {
			t.Errorf("decisions room lacks rehashed/held %s:\n%s", c, dec)
		}
	}
	if !strings.Contains(dec, "#decision/0/"+DrawerID(slugTo, "Decision one.")+`"`) || !strings.Contains(dec, `"session/2026-08-22-0f0d1eb5-03#decision/0/`) {
		t.Errorf("source_ref rewrite wrong:\n%s", dec)
	}
	if slugExists(root, "palace/"+slugTo+"/premerge-stray-decisions.jsonl") {
		t.Error("hold file survived K2")
	}
	C := "palace/.local/embed-cache/" + slugTo + "/"
	if slugRead(t, root, C+DrawerID(slugTo, "Decision one.")+".vec") != "vec-d1" {
		t.Error("drawer vector not renamed to the rehashed id")
	}
	if slugRead(t, root, C+"note."+slugTo+".2026-08-22-0f0d1eb5-01.c0.vec") != "vec-note-legit-01" {
		t.Error("legit -01 note vector must own the -01 key after the migration")
	}
	if slugRead(t, root, C+"iter."+slugTo+".1.m0.raw.c0.vec") != "vec-iter1" {
		t.Error("iter vector not renamed")
	}
	if !slugExists(root, C+slugCacheMarker) || !slugExists(root, C+slugStrayList) {
		t.Error("marker or stray list missing")
	}
	if res.Cache.StrayDeleted != 1 || res.Cache.OrphansSaved != 1 {
		t.Errorf("cache result %+v", res.Cache)
	}
	if !strings.Contains(log.String(), "K2 ") || !strings.Contains(log.String(), "LIVE=") {
		t.Errorf("log lacks progress lines:\n%s", log.String())
	}

	// Idempotency by tree state.
	o2 := o
	o2.JournalPath = filepath.Join(t.TempDir(), "j2.tsv")
	r2, err := ApplyProjectSlugMigration(o2)
	if err != nil || !r2.AlreadyApplied {
		t.Fatalf("re-apply: %+v %v", r2, err)
	}
	cr, err := RunSlugCachePhase(root, slugFrom, slugTo, filepath.Join(t.TempDir(), "c.tsv"), o.ProcDir, o.Home, false, nil)
	if err != nil || !cr.NothingToDo {
		t.Fatalf("cache re-run: %+v %v", cr, err)
	}
}

func TestSlugPlanRefusesUnexpectedCollisionAndMoveRefusesExistingDestination(t *testing.T) {
	root := slugFixture(t)
	slugWrite(t, root, "Projects/"+slugTo+"/memory/one-build-at-a-time.md", "target copy\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "collision")
	if _, err := PlanProjectSlugMigration(root, slugFrom, slugTo); err == nil || !strings.Contains(err.Error(), "unexpected collision") {
		t.Fatalf("plan must refuse an unexpected collision, got %v", err)
	}
	// The move primitive itself refuses an existing destination.
	src := "Projects/" + slugFrom + "/memory/one-build-at-a-time.md"
	dst := "Projects/" + slugTo + "/memory/one-build-at-a-time.md"
	if err := slugMove(root, src, dst, nil); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("slugMove must refuse, got %v", err)
	}
	if !slugExists(root, src) || slugRead(t, root, dst) != "target copy\n" {
		t.Fatal("a refused move touched a file")
	}
}

func TestSlugApplyRefusesExpectAndHeadMismatch(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root)
	o.ExpectHead = strings.Repeat("0", 40)
	if _, err := ApplyProjectSlugMigration(o); err == nil || !strings.Contains(err.Error(), "--expect-head") {
		t.Fatalf("want an expect-head refusal, got %v", err)
	}
	o = slugOpts(t, root)
	o.Expect = []byte("{}\n")
	if _, err := ApplyProjectSlugMigration(o); err == nil || !strings.Contains(err.Error(), "--expect") {
		t.Fatalf("want an --expect refusal, got %v", err)
	}
	if n := gitRun(t, root, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("a refused run committed (%s commits)", n)
	}
}

func TestSlugAgentScanFindsEveryClient(t *testing.T) {
	proc := slugFakeProc(t)
	mk := func(pid int, comm, cmdline, exe string) {
		d := filepath.Join(proc, fmt.Sprint(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.ReplaceAll(cmdline, " ", "\x00")+"\x00"), 0o644)
		if exe != "" {
			os.Symlink(exe, filepath.Join(d, "exe"))
		}
	}
	mk(10, "claude", "claude --dangerously-skip-permissions", "/home/u/.local/share/claude/versions/2.1.278")
	mk(11, "2.1.278", "/x/y", "/home/u/.local/share/claude/versions/2.1.278")
	mk(12, "node", "node /usr/lib/node_modules/@anthropic-ai/claude-code/cli.js", "/usr/bin/node")
	mk(13, "vp", "vp mcp", "/home/u/.local/bin/vp")
	mk(14, "vp", "vp hook", "/home/u/.local/bin/vp")
	mk(15, "grok-1.0.34-li", "/home/u/.grok/downloads/grok-1.0.34", "/home/u/.grok/downloads/grok-1.0.34-linux-x86_64")
	mk(16, "bash", "bash", "/usr/bin/bash")
	// pid 16 is an ordinary process; 9001-9003 come from slugFakeProc and
	// include two with NO exe link, which must not be refused (D1).
	off, err := slugAgentScan(proc, os.Getpid(), "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(off, "\n")
	for _, pid := range []string{"pid 10 ", "pid 11 ", "pid 12 ", "pid 13 ", "pid 14 ", "pid 15 "} {
		if !strings.Contains(joined, pid) {
			t.Errorf("guard missed %s:\n%s", pid, joined)
		}
	}
	for _, pid := range []string{"pid 16 ", "pid 9001 ", "pid 9002 ", "pid 9003 "} {
		if strings.Contains(joined, pid) {
			t.Errorf("guard wrongly refused %s", pid)
		}
	}
	if err := SlugGuard(true, proc, false); err == nil {
		t.Fatal("the guard must refuse a LIVE vault while agents run")
	}
	if err := SlugGuard(false, proc, false); err != nil {
		t.Fatalf("a non-LIVE copy is exempt, got %v", err)
	}
}

func TestSlugApplyRefusesLiveVaultWithAgentAlive(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root) // Home has no config -> LIVE (fail-closed)
	d := filepath.Join(o.ProcDir, "4242")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "comm"), []byte("claude\n"), 0o644)
	head := o.ExpectHead
	_, err := ApplyProjectSlugMigration(o)
	if err == nil || !strings.Contains(err.Error(), "LIVE vault") {
		t.Fatalf("want a guard refusal, got %v", err)
	}
	if got := gitRun(t, root, "rev-parse", "HEAD"); got != head {
		t.Fatal("a refused run committed")
	}
	if dirt, _ := slugDirt(root); len(dirt) > 0 {
		t.Fatalf("a refused run left dirt: %v", dirt)
	}
}

func TestClassifySlugVault(t *testing.T) {
	root := slugFixture(t)
	home := t.TempDir()
	if live, _ := ClassifySlugVault(root, home); !live {
		t.Error("a missing home config must classify LIVE (fail-closed)")
	}
	cfg := filepath.Join(home, ".config", "vibe-palace", "config.toml")
	os.MkdirAll(filepath.Dir(cfg), 0o755)
	os.WriteFile(cfg, []byte("vault_path = \"~/not-expanded\"\n"), 0o644)
	if live, _ := ClassifySlugVault(root, home); !live {
		t.Error("an unstat-able vault_path must classify LIVE")
	}
	os.WriteFile(cfg, []byte("vault_path = \""+root+"/\"\n"), 0o644)
	if live, _ := ClassifySlugVault(root, home); !live {
		t.Error("the configured vault (same inode) must classify LIVE")
	}
	other := t.TempDir()
	os.WriteFile(cfg, []byte("vault_path = \""+other+"\"\n"), 0o644)
	if live, d := ClassifySlugVault(root, home); live {
		t.Errorf("a remote-less copy at another inode must be exempt: %s", d)
	}
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare")
	gitRun(t, root, "remote", "add", "origin", bare)
	if live, _ := ClassifySlugVault(root, home); !live {
		t.Error("a vault with a remote must classify LIVE")
	}
}

func TestSlugApplyRefusesWhenRemoteIsAheadOrBehind(t *testing.T) {
	root := slugFixture(t)
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare")
	gitRun(t, root, "remote", "add", "origin", bare)
	gitRun(t, root, "push", "-q", "origin", "main")
	slugWrite(t, root, "unrelated.md", "x\n")
	gitRun(t, root, "add", "unrelated.md")
	gitRun(t, root, "commit", "-q", "-m", "ahead")
	o := slugOpts(t, root)
	if _, err := ApplyProjectSlugMigration(o); err == nil || !strings.Contains(err.Error(), "ahead or behind") {
		t.Fatalf("want an ahead/behind refusal, got %v", err)
	}
}

func TestSlugPreCommitCheckRefusesForeignDirtAndMovedHead(t *testing.T) {
	root := slugFixture(t)
	head := gitRun(t, root, "rev-parse", "HEAD")
	if err := SlugPreCommitCheck(root, head, nil); err != nil {
		t.Fatalf("clean tree: %v", err)
	}
	slugWrite(t, root, "Projects/vibe-palace/sessions/foreign.md", "capture\n")
	if err := SlugPreCommitCheck(root, head, nil); err == nil || !strings.Contains(err.Error(), "foreign dirt") {
		t.Fatalf("want foreign dirt, got %v", err)
	}
	if err := SlugPreCommitCheck(root, strings.Repeat("1", 40), map[string]bool{"Projects/vibe-palace/sessions/foreign.md": true}); err == nil || !strings.Contains(err.Error(), "HEAD moved") {
		t.Fatalf("want HEAD moved, got %v", err)
	}
	// Integration: foreign dirt that appears between K0 and K1's commit.
	os.Remove(filepath.Join(root, "Projects/vibe-palace/sessions/foreign.md"))
	o := slugOpts(t, root)
	prev := slugMigrationBeforeCommit
	slugMigrationBeforeCommit = func(step string) error {
		if step == "K1" {
			slugWrite(t, root, "Projects/vibe-palace/sessions/late.md", "capture\n")
		}
		return nil
	}
	t.Cleanup(func() { slugMigrationBeforeCommit = prev })
	_, err := ApplyProjectSlugMigration(o)
	if err == nil || !strings.Contains(err.Error(), "K1") || !strings.Contains(err.Error(), "foreign dirt") {
		t.Fatalf("want K1 foreign dirt refusal, got %v", err)
	}
}

func TestSlugPostCommitCheckFailsOnEveryDeviation(t *testing.T) {
	slugEnv(t)
	root := t.TempDir()
	gitRun(t, root, "init", "-b", "main")
	gitRun(t, root, "config", "user.email", "t@e.com")
	gitRun(t, root, "config", "user.name", "T")
	body := strings.Repeat("line\n", 40)
	slugWrite(t, root, "a/f.md", body)
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "base")
	base := gitRun(t, root, "rev-parse", "HEAD")
	// A rename that also edits the file: R < 100.
	os.MkdirAll(filepath.Join(root, "b"), 0o755)
	os.Rename(filepath.Join(root, "a/f.md"), filepath.Join(root, "b/f.md"))
	slugWrite(t, root, "b/f.md", body+"edit\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "rename+edit")
	want := []SlugNameStatus{{Status: "R100", Old: "a/f.md", New: "b/f.md"}}
	if err := SlugPostCommitCheck(root, base, want, nil); err == nil || !strings.Contains(err.Error(), "not R100") {
		t.Fatalf("want a not-R100 failure, got %v", err)
	}
	// HEAD^ is not prev: a foreign commit slipped in.
	if err := SlugPostCommitCheck(root, strings.Repeat("2", 40), want, nil); err == nil || !strings.Contains(err.Error(), "foreign commit") {
		t.Fatalf("want a HEAD^ failure, got %v", err)
	}
	// A pure rename passes; an unexpected extra line fails; dirt fails.
	mid := gitRun(t, root, "rev-parse", "HEAD")
	os.MkdirAll(filepath.Join(root, "c"), 0o755)
	os.Rename(filepath.Join(root, "b/f.md"), filepath.Join(root, "c/f.md"))
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "pure")
	pure := []SlugNameStatus{{Status: "R100", Old: "b/f.md", New: "c/f.md"}}
	if err := SlugPostCommitCheck(root, mid, pure, nil); err != nil {
		t.Fatalf("a pure rename must pass: %v", err)
	}
	if err := SlugPostCommitCheck(root, mid, nil, nil); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("want unexpected-line failure, got %v", err)
	}
	slugWrite(t, root, "stray.txt", "dirt\n")
	if err := SlugPostCommitCheck(root, mid, pure, nil); err == nil || !strings.Contains(err.Error(), "dirt") {
		t.Fatalf("want a dirt failure, got %v", err)
	}
}

// slugJournalFile writes a journal for root, with the vault header the real
// tool writes, so a replay of it is not refused as foreign.
func slugJournalFile(t *testing.T, root, path string, lines ...string) string {
	t.Helper()
	ident, err := slugVaultIdent(root)
	if err != nil {
		t.Fatal(err)
	}
	body := "#vault\t" + ident + "\t\n"
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSlugJournalRefusesNonEmptyAndReplayStates(t *testing.T) {
	slugEnv(t)
	dir := t.TempDir()
	root := t.TempDir()
	jp := filepath.Join(dir, "journal.tsv")
	os.WriteFile(jp, []byte("mv\ta\tb\n"), 0o644)
	if _, err := OpenSlugJournal(jp, root); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("want a non-empty refusal, got %v", err)
	}
	// Missing journal: zero, no error.
	if r, err := ReplaySlugJournal(t.TempDir(), filepath.Join(dir, "absent.tsv"), false); err != nil || r.Lines != 0 || r.Done != 0 {
		t.Fatalf("missing journal: %+v %v", r, err)
	}
	slugWrite(t, root, "x/moved.bin", "m")  // dst present, src absent -> done
	slugWrite(t, root, "y/notyet.bin", "n") // src present, dst absent -> not-done
	j2 := slugJournalFile(t, root, filepath.Join(dir, "j2.tsv"),
		"mv\ty/notyet.bin\tz/notyet.bin", "mv\tw/moved.bin\tx/moved.bin")
	r, err := ReplaySlugJournal(root, j2, false)
	if err != nil || r.Done != 1 || r.NotDone != 1 {
		t.Fatalf("replay: %+v %v", r, err)
	}
	if !slugExists(root, "w/moved.bin") {
		t.Fatal("done line not moved back")
	}
	// Re-entrant: a second replay skips the recorded line.
	r, err = ReplaySlugJournal(root, j2, false)
	if err != nil || r.Done != 0 || r.PreviouslyReplayed != 1 || r.NotDone != 1 {
		t.Fatalf("re-run: %+v %v", r, err)
	}
	// Two not-done lines is a failure.
	slugWrite(t, root, "y/second.bin", "s")
	j3 := slugJournalFile(t, root, filepath.Join(dir, "j3.tsv"),
		"mv\ty/notyet.bin\tz/a", "mv\ty/second.bin\tz/b")
	if _, err := ReplaySlugJournal(root, j3, false); err == nil || !strings.Contains(err.Error(), "not-done") {
		t.Fatalf("want a not-done failure, got %v", err)
	}
	// Both present is a conflict.
	slugWrite(t, root, "z/c", "c")
	j4 := slugJournalFile(t, root, filepath.Join(dir, "j4.tsv"), "mv\ty/notyet.bin\tz/c")
	if _, err := ReplaySlugJournal(root, j4, false); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("want a conflict, got %v", err)
	}
}

// TestSlugCrashDuringCacheThenRollback is the fixture crash drill: the run
// dies after n journalled operations; git restores the tracked tree; the
// journal restores everything else; a second replay is a no-op.
func TestSlugCrashDuringCacheThenRollback(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root)
	before := treeHash(t, root, false)
	boom := errors.New("injected crash")
	prev := slugMigrationAfterOp
	slugMigrationAfterOp = func(n int) error {
		if n == 4 { // 1 .bak + the journalled stray list + stray delete + 1 rename
			return boom
		}
		return nil
	}
	t.Cleanup(func() { slugMigrationAfterOp = prev })
	if _, err := ApplyProjectSlugMigration(o); !errors.Is(err, boom) {
		t.Fatalf("want the injected crash, got %v", err)
	}
	gitRun(t, root, "reset", "-q", "--hard", o.ExpectHead)
	gitRun(t, root, "clean", "-q", "-fd", "--", "Projects/"+slugTo, "palace/"+slugTo)
	r, err := ReplaySlugJournal(root, o.JournalPath, false)
	if err != nil || r.Done != 4 || r.NotDone != 0 {
		t.Fatalf("replay: %+v %v", r, err)
	}
	os.Remove(filepath.Join(root, "palace/.local/embed-cache/"+slugTo+"/"+slugStrayList))
	if after := treeHash(t, root, false); after != before {
		t.Fatal("the rollback did not restore the vault byte for byte")
	}
	if got := gitRun(t, root, "rev-parse", "HEAD"); got != o.ExpectHead {
		t.Fatalf("HEAD %s after rollback, want %s", got, o.ExpectHead)
	}
	// A second replay of the SAME journal must be loud, not a quiet success:
	// every line is already replayed, so the rollback did nothing (C2).
	r2, err := ReplaySlugJournal(root, o.JournalPath, false)
	if err == nil || !strings.Contains(err.Error(), "refusing to report success") {
		t.Fatalf("a repeated replay must refuse, got %+v %v", r2, err)
	}
	if r2.Done != 0 || r2.PreviouslyReplayed != 4 {
		t.Fatalf("second replay states: %+v", r2)
	}
}

// TestSlugStrayListIsExactAndResumable proves deletion uses the recorded
// list's sha256, never a name rule: after a crash that already renamed the
// legit -01 vector into the stray's name, a resumed cache phase keeps it.
func TestSlugStrayListIsExactAndResumable(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root)
	boom := errors.New("crash after the legit note rename")
	prev := slugMigrationAfterOp
	// ops: 1 .bak, 2 the journalled stray list, 3 stray rm, 4-6 drawer vecs,
	// 7 iter rename, 8 note -01 rename
	slugMigrationAfterOp = func(n int) error {
		if n == 8 {
			return boom
		}
		return nil
	}
	t.Cleanup(func() { slugMigrationAfterOp = prev })
	if _, err := ApplyProjectSlugMigration(o); !errors.Is(err, boom) {
		t.Fatalf("want the crash, got %v", err)
	}
	slugMigrationAfterOp = prev
	C := "palace/.local/embed-cache/" + slugTo + "/"
	list := slugRead(t, root, C+slugStrayList)
	if !strings.Contains(list, "note."+slugTo+".2026-08-22-0f0d1eb5-01.c0.vec") || strings.Count(list, "\n") != 1 {
		t.Fatalf("stray list must name exactly the stray note vector:\n%s", list)
	}
	if slugRead(t, root, C+"note."+slugTo+".2026-08-22-0f0d1eb5-01.c0.vec") != "vec-note-legit-01" {
		t.Fatal("precondition: the legit vector now holds the stray's name")
	}
	cr, err := RunSlugCachePhase(root, slugFrom, slugTo, filepath.Join(t.TempDir(), "resume.tsv"), o.ProcDir, o.Home, false, nil)
	if err != nil {
		t.Fatalf("resumed cache phase: %v", err)
	}
	if cr.StrayDeleted != 0 {
		t.Errorf("the resumed run deleted %d files by name", cr.StrayDeleted)
	}
	if slugRead(t, root, C+"note."+slugTo+".2026-08-22-0f0d1eb5-01.c0.vec") != "vec-note-legit-01" {
		t.Fatal("the legit -01 vector was deleted")
	}
	if !slugExists(root, C+slugCacheMarker) {
		t.Fatal("marker not written by the resumed run")
	}
	if cr2, err := RunSlugCachePhase(root, slugFrom, slugTo, filepath.Join(t.TempDir(), "again.tsv"), o.ProcDir, o.Home, false, nil); err != nil || !cr2.NothingToDo {
		t.Fatalf("marker present must be nothing to do: %+v %v", cr2, err)
	}
}

func TestSlugCachePhaseRefusesUnappliedTree(t *testing.T) {
	root := slugFixture(t)
	if _, err := RunSlugCachePhase(root, slugFrom, slugTo, filepath.Join(t.TempDir(), "c.tsv"), t.TempDir(), t.TempDir(), false, nil); err == nil || !strings.Contains(err.Error(), "not in the migrated state") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// TestSlugWindowChairExemptionIsExactAndSingle proves the ONE exemption: a
// claude process is exempt only when its own argv carries inline
// {"disableAllHooks":true} settings AND --strict-mcp-config with an inline MCP
// config that names no vibe-palace server; every other shape is refused, and
// a second exempt-looking process is refused too.
func TestSlugWindowChairExemptionIsExactAndSingle(t *testing.T) {
	good := []string{"claude", "--settings", `{"disableAllHooks":true}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "-n", "vp-window-chair"}
	if ok, why := slugWindowChairArgv(good); !ok {
		t.Fatalf("the sanctioned command line must be exempt: %s", why)
	}
	eq := []string{"claude", "--settings={\"disableAllHooks\":true}", "--strict-mcp-config", "--mcp-config={\"mcpServers\":{}}"}
	if ok, why := slugWindowChairArgv(eq); !ok {
		t.Fatalf("the --flag=value form must be exempt: %s", why)
	}
	// Captured from /proc/<pid>/cmdline of a real `claude -p` probe
	// (Claude Code 2.1.280) launched with the sanctioned flags.
	probe := []string{"claude", "-p", "Reply with ONLY a list.", "--settings", `{"disableAllHooks":true}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
	if ok, why := slugWindowChairArgv(probe); !ok {
		t.Fatalf("the captured probe command line must be exempt: %s", why)
	}
	bad := map[string][]string{
		// Claude's --mcp-config is variadic and swallows a trailing prompt as
		// a config path (it then refuses to start); the parser does the same.
		"prompt after mcp":    {"claude", "--settings", `{"disableAllHooks":true}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "a prompt"},
		"no settings":         {"claude", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`},
		"hooks not disabled":  {"claude", "--settings", `{"disableAllHooks":false}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`},
		"settings file path":  {"claude", "--settings", "/tmp/s.json", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`},
		"two settings":        {"claude", "--settings", `{"disableAllHooks":true}`, "--settings", `{}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`},
		"no strict":           {"claude", "--settings", `{"disableAllHooks":true}`, "--mcp-config", `{"mcpServers":{}}`},
		"no mcp config":       {"claude", "--settings", `{"disableAllHooks":true}`, "--strict-mcp-config"},
		"mcp config path":     {"claude", "--settings", `{"disableAllHooks":true}`, "--strict-mcp-config", "--mcp-config", "/tmp/m.json"},
		"vp server":           {"claude", "--settings", `{"disableAllHooks":true}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{"x":{"command":"/home/u/.local/bin/vp","args":["mcp"]}}}`},
		"vibe-palace by name": {"claude", "--settings", `{"disableAllHooks":true}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{"vibe-palace":{"command":"other"}}}`},
	}
	for name, argv := range bad {
		if ok, _ := slugWindowChairArgv(argv); ok {
			t.Errorf("%s: must NOT be exempt", name)
		}
	}
	proc := slugFakeProc(t)
	mk := func(pid int, comm string, argv []string) {
		d := filepath.Join(proc, fmt.Sprint(pid))
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)
		// An exe the guard can resolve: an unresolvable one is now a refusal
		// for a process of our own uid (code review round 1, C5).
		os.Symlink("/home/u/.local/share/claude/versions/2.1.280", filepath.Join(d, "exe"))
	}
	mk(100, "claude", good)
	if err := SlugGuard(true, proc, false); err != nil {
		t.Fatalf("one window Chair alone must pass the guard: %v", err)
	}
	mk(101, "claude", good)
	if err := SlugGuard(true, proc, false); err == nil || !strings.Contains(err.Error(), "second window-Chair") {
		t.Fatalf("a second window Chair must be refused, got %v", err)
	}
	os.RemoveAll(filepath.Join(proc, "101"))
	mk(102, "claude", []string{"claude", "--dangerously-skip-permissions"})
	if err := SlugGuard(true, proc, false); err == nil || !strings.Contains(err.Error(), "not the window Chair") {
		t.Fatalf("an ordinary claude session must be refused, got %v", err)
	}
	os.RemoveAll(filepath.Join(proc, "102"))
	mk(103, "claude", nil) // unreadable/empty cmdline
	os.WriteFile(filepath.Join(proc, "103", "cmdline"), nil, 0o644)
	if err := SlugGuard(true, proc, false); err == nil || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("an empty cmdline must fail closed, got %v", err)
	}
}
