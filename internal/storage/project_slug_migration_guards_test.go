// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// ONE-SHOT: deleted with project_slug_migration.go.
//
// One test per guard that code review round 1 found unpinned (CS1), plus the
// guards added in that round: the journalled cache marker (C1), the
// journal-bound .replayed file (C2), the vault-bound journal (C6) and the
// identity-based process scan (C5). Every test here is written so that
// REVERTING its guard makes it fail — that is the point of the file.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// ---------------------------------------------------------------- C3: the ten

// 1. A drawer whose id is not DrawerID(from, content) means the store is not
// what the migration assumes, so the re-hash must refuse rather than invent.
func TestSlugGuardDrawerIDInvariant(t *testing.T) {
	good := drawerRow(DrawerID(slugFrom, "A fact."), "facts", "A fact.", "session", "s1")
	if _, _, _, err := slugRehashRoom([]byte(good+"\n"), slugFrom, slugTo, "r"); err != nil {
		t.Fatalf("a consistent row must rehash: %v", err)
	}
	bad := drawerRow("deadbeef", "facts", "A fact.", "session", "s1")
	_, _, _, err := slugRehashRoom([]byte(bad+"\n"), slugFrom, slugTo, "r")
	if err == nil || !strings.Contains(err.Error(), "deadbeef") {
		t.Fatalf("a row whose id does not match its content must be refused, got %v", err)
	}
}

// 2. The applied rewrite must equal the counted plan, class for class: it is
// the tool's own answer to "did K2 do what the report promised".
func TestSlugGuardAppliedClassesMustEqualPlan(t *testing.T) {
	planned := map[string]int{"W1": 3, "W2": 1}
	if err := slugAssertClasses(map[string]int{"W1": 3, "W2": 1}, planned); err != nil {
		t.Fatalf("equal counts must pass: %v", err)
	}
	if err := slugAssertClasses(map[string]int{"W1": 4, "W2": 1}, planned); err == nil || !strings.Contains(err.Error(), "W1") {
		t.Fatalf("a class that rewrote more than planned must be refused, got %v", err)
	}
	if err := slugAssertClasses(map[string]int{"W1": 3, "W2": 1, "W9": 2}, planned); err == nil || !strings.Contains(err.Error(), "W9") {
		t.Fatalf("a class the plan did not count must be refused, got %v", err)
	}
}

// 3. A cache destination that already exists with DIFFERENT bytes is someone
// else's vector: clobbering it would serve the wrong embedding.
func TestSlugGuardCacheDestinationDifferentBytes(t *testing.T) {
	root := t.TempDir()
	jp := filepath.Join(t.TempDir(), "j.tsv")
	j, err := OpenSlugJournal(jp, root)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	slugWrite(t, root, "a/x.vec", "source bytes")
	slugWrite(t, root, "b/x.vec", "identical")
	slugWrite(t, root, "b/y.vec", "identical")
	res := &SlugCacheResult{}
	// Identical bytes: the source is saved, not clobbered, and counted.
	slugWrite(t, root, "a/y.vec", "identical")
	if err := slugCacheMove(root, "a/y.vec", "b/y.vec", j, res); err != nil {
		t.Fatalf("a duplicate must be saved, not refused: %v", err)
	}
	if res.DuplicatesSaved != 1 {
		t.Errorf("duplicates-saved = %d, want 1", res.DuplicatesSaved)
	}
	err = slugCacheMove(root, "a/x.vec", "b/x.vec", j, res)
	if err == nil || !strings.Contains(err.Error(), "different bytes") {
		t.Fatalf("a destination with different bytes must be refused, got %v", err)
	}
	if slugRead(t, root, "b/x.vec") != "identical" {
		t.Fatal("the refusal must leave the destination untouched")
	}
}

// 4. A scaffold directory that holds anything but README.md is not the
// scaffold the plan says it is, so deleting it would delete real content.
func TestSlugGuardScaffoldDirMustHoldOnlyREADME(t *testing.T) {
	root := slugFixture(t)
	slugWrite(t, root, "Projects/"+slugFrom+"/commands/vpc-real.md", "a real command, not a scaffold\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "an extra file in the scaffold dir")
	_, err := PlanProjectSlugMigration(root, slugFrom, slugTo)
	if err == nil || !strings.Contains(err.Error(), "commands") {
		t.Fatalf("a scaffold dir with an extra file must be refused, got %v", err)
	}
}

// 5. Removing the emptied source tree must refuse if any file is left: that
// file would be deleted with the directory.
func TestSlugGuardRemoveEmptyTreeRefusesLeftover(t *testing.T) {
	root := t.TempDir()
	slugWrite(t, root, "tree/sub/leftover.txt", "not empty\n")
	err := slugRemoveEmptyTree(root, "tree")
	if err == nil || !strings.Contains(err.Error(), "leftover.txt") {
		t.Fatalf("a leftover file must be refused, got %v", err)
	}
	if !slugExists(root, "tree/sub/leftover.txt") {
		t.Fatal("the refusal must leave the file in place")
	}
	if err := os.Remove(filepath.Join(root, "tree", "sub", "leftover.txt")); err != nil {
		t.Fatal(err)
	}
	if err := slugRemoveEmptyTree(root, "tree"); err != nil {
		t.Fatalf("an empty tree must be removed: %v", err)
	}
	if slugExists(root, "tree") {
		t.Fatal("the empty tree is still there")
	}
}

// 6. An `archive:` line whose target does not exist after K1 would be rewritten
// into a dangling link.
func TestSlugGuardArchiveTargetMustExist(t *testing.T) {
	root := slugFixture(t)
	slugWrite(t, root, "Projects/"+slugFrom+"/sessions/2026-08-22-0f0d1eb5-03.md",
		"---\nsession_id: 2026-08-22-0f0d1eb5-03\nproject: "+slugFrom+
			"\narchive: Projects/"+slugFrom+"/transcripts/2026-08-22-gone.manifest.json\n---\n\nbody\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "a session naming a missing archive")
	_, err := PlanProjectSlugMigration(root, slugFrom, slugTo)
	if err == nil || !strings.Contains(err.Error(), "archive target") {
		t.Fatalf("a missing archive target must be refused, got %v", err)
	}
}

// 7. A manifest must carry exactly one project_slug: zero means the rewrite
// had nothing to do, and two means one of them would survive unrewritten.
func TestSlugGuardProjectSlugAppearsExactlyOnce(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"twice", "{\n  \"project_slug\": \"" + slugFrom + "\",\n  \"project_slug\": \"" + slugFrom + "\",\n  \"session_id\": \"zzzz\"\n}\n"},
		{"never", "{\n  \"session_id\": \"zzzz\"\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := slugFixture(t)
			slugWrite(t, root, "Projects/"+slugFrom+"/transcripts/2026-08-22-zzzz.manifest.json", tc.body)
			slugWrite(t, root, "Projects/"+slugFrom+"/transcripts/2026-08-22-zzzz.jsonl.zst", "z")
			gitRun(t, root, "add", "-A")
			gitRun(t, root, "commit", "-q", "-m", "a manifest with "+tc.name+" project_slug")
			_, err := PlanProjectSlugMigration(root, slugFrom, slugTo)
			if err == nil || !strings.Contains(err.Error(), "project_slug appears") {
				t.Fatalf("project_slug %s must be refused, got %v", tc.name, err)
			}
		})
	}
}

// 8. A baseline entry that is not a unique token cannot be rewritten by a
// textual replace without risking the wrong occurrence.
func TestSlugGuardBaselineTokenMustBeUnique(t *testing.T) {
	one := `{"dimensions":{"archive-roundtrip":{"accepted":["Projects/` + slugFrom + `/transcripts/m.json"]}}}`
	if _, n, err := slugRewriteBaseline([]byte(one), slugFrom, slugTo); err != nil || n != 1 {
		t.Fatalf("a unique entry must rewrite: n=%d err=%v", n, err)
	}
	// The same path listed twice: the token is no longer unique, so a textual
	// replace could rewrite the wrong one.
	dup := `{"dimensions":{"archive-roundtrip":{"accepted":["Projects/` + slugFrom + `/transcripts/m.json",` +
		`"Projects/` + slugFrom + `/transcripts/m.json"]}}}`
	_, _, err := slugRewriteBaseline([]byte(dup), slugFrom, slugTo)
	if err == nil || !strings.Contains(err.Error(), "unique token") {
		t.Fatalf("a repeated token must be refused, got %v", err)
	}
}

// 9. A held target row whose re-hashed id already exists in the merged file
// would make two different texts share one id.
func TestSlugGuardHeldRowIDCollision(t *testing.T) {
	root := slugFixture(t)
	// Give the target's stray decision the SAME content as a source decision:
	// after the re-hash both rows carry DrawerID(to, content).
	same := "Decision one."
	slugWrite(t, root, "palace/"+slugTo+"/drawers/"+slugTo+"/decisions/drawers.jsonl",
		drawerRow(DrawerID(slugTo, same), "decisions", same, "decision",
			"session/2026-08-22-0f0d1eb5-01#decision/0/"+DrawerID(slugTo, same))+"\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "a held row that will collide")
	o := slugOpts(t, root)
	_, err := ApplyProjectSlugMigration(o)
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("a colliding held row must be refused, got %v", err)
	}
}

// ---------------------------------------------------------------- C1

// TestSlugRollbackClearsCacheMarkerAndRetryReruns is the drill the Chair asked
// for: apply, roll back the way Rollback A does (git first, then the journal),
// then retry. The retry must actually RUN the cache step, and the stray note's
// vector must not be left on the live key.
func TestSlugRollbackClearsCacheMarkerAndRetryReruns(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root)
	C := "palace/.local/embed-cache/" + slugTo + "/"
	strayKey := C + "note." + slugTo + ".2026-08-22-0f0d1eb5-01.c0.vec"
	if _, err := ApplyProjectSlugMigration(o); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !slugExists(root, C+slugCacheMarker) {
		t.Fatal("the apply did not write the marker")
	}
	// Rollback A: git restores the tracked tree, then the journal replays.
	gitRun(t, root, "reset", "-q", "--hard", o.ExpectHead)
	gitRun(t, root, "clean", "-q", "-fd", "--", "Projects/"+slugTo, "palace/"+slugTo)
	r, err := ReplaySlugJournal(root, o.JournalPath, false)
	if err != nil {
		t.Fatalf("replay: %+v %v", r, err)
	}
	if slugExists(root, C+slugCacheMarker) {
		t.Fatal("the marker survived the rollback, so a retry would skip the whole cache step")
	}
	if slugExists(root, C+slugStrayList) {
		t.Fatal("the stray list survived the rollback")
	}
	if got := slugRead(t, root, strayKey); got != "vec-stray-note" {
		t.Fatalf("the rollback did not restore the stray vector: %q", got)
	}
	// The retry: a second, successful migration from the restored state.
	o2 := slugOpts(t, root)
	if _, err := ApplyProjectSlugMigration(o2); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := slugRead(t, root, strayKey); got != "vec-note-legit-01" {
		t.Fatalf("after the retry the -01 key holds %q; the legit note's vector must be there", got)
	}
	if slugExists(root, "palace/.local/embed-cache/"+slugFrom) {
		t.Fatal("the old-slug cache directory survived the retry")
	}
}

// A marker left behind while the source cache still exists is a state the
// marker cannot describe, and skipping there is what left a stray vector.
func TestSlugGuardRefusesStaleCacheMarker(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root)
	if _, err := ApplyProjectSlugMigration(o); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Put the source cache back, as an interrupted rollback would.
	slugWrite(t, root, "palace/.local/embed-cache/"+slugFrom+"/orphan.vec", "x")
	_, err := RunSlugCachePhase(root, slugFrom, slugTo, filepath.Join(t.TempDir(), "c.tsv"), o.ProcDir, o.Home, false, nil)
	if err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("a marker with the source cache present must be refused, got %v", err)
	}
}

// ---------------------------------------------------------------- C2

// A .replayed file from an EARLIER journal must never silence a rollback. It
// must also not turn a CORRECT rollback into a failure: Rollback A runs under
// `set -eu`, so a non-zero exit after the replay would abort the block before
// it restores the kill switch (code review round 2, D4). The replay therefore
// does the work, warns, and succeeds.
func TestSlugReplayWarnsAndStillWorksOnAStaleReplayedFile(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	jp := filepath.Join(dir, "journal.tsv")
	slugWrite(t, root, "b/one.bak", "one")
	j := slugJournalFile(t, root, jp, "mv\ta/one.bak\tb/one.bak")
	if r, err := ReplaySlugJournal(root, j, false); err != nil || r.Done != 1 {
		t.Fatalf("first replay: %+v %v", r, err)
	}
	// The operator archives the journal and a second attempt writes a NEW one
	// at the same path. The old .replayed is still beside it.
	if err := os.Rename(jp, filepath.Join(dir, "attempt-1.tsv")); err != nil {
		t.Fatal(err)
	}
	slugWrite(t, root, "b/two.bak", "two")
	slugJournalFile(t, root, jp, "mv\ta/two.bak\tb/two.bak")
	r, err := ReplaySlugJournal(root, jp, false)
	if err != nil {
		t.Fatalf("a stale .replayed must not fail the rollback: %v", err)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "discarded") {
		t.Fatalf("a stale .replayed must be reported loudly, got warnings %v", r.Warnings)
	}
	if !strings.Contains(r.String(), "WARNING:") {
		t.Fatalf("the warning must be in the printed result: %q", r.String())
	}
	if r.Done != 1 {
		t.Fatalf("the stale file must not suppress the work: %+v", r)
	}
	if !slugExists(root, "a/two.bak") {
		t.Fatal("the second journal's line was not replayed; a stale marker suppressed it")
	}
	// The fresh path: no warning, and the .replayed it writes is its own.
	slugWrite(t, root, "b/three.bak", "three")
	fresh := slugJournalFile(t, root, filepath.Join(dir, "fresh.tsv"), "mv\ta/three.bak\tb/three.bak")
	r2, err := ReplaySlugJournal(root, fresh, false)
	if err != nil || len(r2.Warnings) != 0 || r2.Done != 1 {
		t.Fatalf("a fresh journal must replay silently: %+v %v", r2, err)
	}
}

// A journal all of whose lines are already replayed did nothing, and must not
// exit 0 saying so.
func TestSlugReplayRefusesNoOpSuccess(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	slugWrite(t, root, "b/one.bak", "one")
	jp := slugJournalFile(t, root, filepath.Join(dir, "j.tsv"), "mv\ta/one.bak\tb/one.bak")
	if _, err := ReplaySlugJournal(root, jp, false); err != nil {
		t.Fatalf("first replay: %v", err)
	}
	_, err := ReplaySlugJournal(root, jp, false)
	if err == nil || !strings.Contains(err.Error(), "refusing to report success") {
		t.Fatalf("a repeat replay of the same journal must refuse, got %v", err)
	}
}

// ---------------------------------------------------------------- C6

func TestSlugReplayRefusesAForeignVault(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	dir := t.TempDir()
	slugWrite(t, a, "b/one.bak", "one")
	slugWrite(t, b, "b/one.bak", "one")
	jp := slugJournalFile(t, a, filepath.Join(dir, "j.tsv"), "mv\ta/one.bak\tb/one.bak")
	_, err := ReplaySlugJournal(b, jp, false)
	if err == nil || !strings.Contains(err.Error(), "belongs to vault") {
		t.Fatalf("a replay against another vault must be refused, got %v", err)
	}
	if slugExists(b, "a/one.bak") {
		t.Fatal("the foreign vault was modified by the refused replay")
	}
	if _, err := ReplaySlugJournal(a, jp, false); err != nil {
		t.Fatalf("the journal's own vault must replay: %v", err)
	}
}

func TestSlugReplayRefusesAJournalWithNoVaultHeader(t *testing.T) {
	root := t.TempDir()
	jp := filepath.Join(t.TempDir(), "j.tsv")
	if err := os.WriteFile(jp, []byte("mv\ta/one.bak\tb/one.bak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaySlugJournal(root, jp, false); err == nil || !strings.Contains(err.Error(), "no vault header") {
		t.Fatalf("a headerless journal must be refused, got %v", err)
	}
}

// The saved copy a rm-vec line names must live inside that journal's own
// deleted/ directory: a journal line is not a licence to move any path.
func TestSlugReplayRefusesSavedPathOutsideTheJournal(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere.vec")
	if err := os.WriteFile(outside, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	jp := slugJournalFile(t, root, filepath.Join(dir, "j.tsv"), "rm-vec\tpalace/.local/x.vec\t"+outside)
	if _, err := ReplaySlugJournal(root, jp, false); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("a saved path outside the journal must be refused, got %v", err)
	}
	if !slugExistsAbs(outside) {
		t.Fatal("the refused replay moved the outside file anyway")
	}
}

// ---------------------------------------------------------------- C4

// A concurrent edit to a tracked file during K2 must not ride into the commit
// as if the migration had made it.
func TestSlugK2RefusesAConcurrentEditToATrackedFile(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root)
	prev := slugMigrationBeforeCommit
	slugMigrationBeforeCommit = func(step string) error {
		if step == "K2-scan" {
			// A file the rewrite pass did not touch, modified by "someone else"
			// between the rewrite and the commit.
			slugWrite(t, root, "Projects/"+slugTo+"/commit-log.md", "clobbered by a concurrent writer\n")
		}
		return nil
	}
	t.Cleanup(func() { slugMigrationBeforeCommit = prev })
	_, err := ApplyProjectSlugMigration(o)
	if err == nil || !strings.Contains(err.Error(), "foreign dirt") {
		t.Fatalf("a concurrent edit during K2 must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "commit-log.md") {
		t.Fatalf("the refusal must name the foreign path: %v", err)
	}
}

// ---------------------------------------------------------------- C5

// The guard matches a process by what its binary IS, not by what it is called.
func TestSlugAgentScanCatchesRenamedAndCopiedBinaries(t *testing.T) {
	proc := slugFakeProc(t)
	bin := t.TempDir()
	home := t.TempDir()
	// A stand-in for this tool's binary, and a byte-identical copy of it under
	// another name: `cp $(command -v vp) /tmp/zzz && /tmp/zzz mcp`.
	self := filepath.Join(bin, "vp")
	if err := os.WriteFile(self, []byte("VP BINARY BYTES"), 0o755); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(bin, "zzz")
	if err := os.WriteFile(renamed, []byte("VP BINARY BYTES"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An installed Claude Code, and a copy of it moved out of its directory.
	vers := filepath.Join(home, ".local", "share", "claude", "versions")
	if err := os.MkdirAll(vers, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(vers, "2.1.280")
	if err := os.WriteFile(claude, []byte("CLAUDE CODE BYTES"), 0o755); err != nil {
		t.Fatal(err)
	}
	offPath := filepath.Join(bin, "helper")
	if err := os.WriteFile(offPath, []byte("CLAUDE CODE BYTES"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev := slugSelfExe
	slugSelfExe = func() string { return self }
	t.Cleanup(func() { slugSelfExe = prev })

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
	// argv deliberately NOT `mcp`/`hook`: identity must be the only thing that
	// can catch this one, or the test would pass with identity matching gone.
	mk(101, "zzz", []string{renamed, "search", "-p", "x"}, renamed) // renamed vp copy
	mk(102, "helper", []string{offPath, "--print"}, offPath)        // claude copied off-path
	mk(103, "bash", []string{"bash", "-lc", "ls"}, "/bin/sh")       // an ordinary process
	off, err := slugAgentScan(proc, os.Getpid(), home)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(off, "\n")
	if !strings.Contains(joined, "pid 101") {
		t.Errorf("a renamed copy of this binary must be refused:\n%s", joined)
	}
	if !strings.Contains(joined, "pid 102") {
		t.Errorf("a Claude Code copied out of its versions directory must be refused:\n%s", joined)
	}
	if strings.Contains(joined, "pid 103") {
		t.Errorf("an ordinary process must not be refused:\n%s", joined)
	}
}

// Which shapes fail closed, and — just as important — which must NOT.
//
// An unreadable /proc/<pid>/exe is NOT evidence of hiding: it is what every
// non-dumpable process looks like, including systemd --user, (sd-pam),
// ssh-agent and the operator's own sshd-session. Measured on this host, five
// ordinary session processes have one, so refusing them would refuse on a
// perfectly frozen host (code review round 2, D1). Only "nothing readable at
// all" fails closed, and it must do so whoever owns the process (imp3 R2-S2).
func TestSlugAgentScanFailClosedCases(t *testing.T) {
	proc := slugFakeProc(t)
	// alive=true gives the entry a readable `stat`, which is what a HIDDEN
	// process still has and a VANISHED one does not.
	mk := func(pid int, comm, cmdline string, alive bool) string {
		d := filepath.Join(proc, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if comm != "" {
			os.WriteFile(filepath.Join(d, "comm"), []byte(comm+"\n"), 0o644)
		}
		if cmdline != "" {
			os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.ReplaceAll(cmdline, " ", "\x00")+"\x00"), 0o644)
		}
		if alive {
			os.WriteFile(filepath.Join(d, "stat"), []byte("stat\n"), 0o644)
		}
		return d
	}
	// 200: ours, no exe link, but comm and argv readable — an ordinary
	// non-dumpable session process. It must NOT be refused.
	mk(200, "systemd", "/usr/lib/systemd/systemd --user", true)
	// 201: nothing readable, but the process is still THERE (hidepid).
	mk(201, "", "", true)
	// 202: nothing readable and no `stat` either — it exited between the
	// listing and the reads. A vanished process is not a hidden one, and
	// refusing it would stop a run for no cause (imp3 round-3 R3-S1).
	mk(202, "", "", false)
	off, err := slugAgentScan(proc, os.Getpid(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(off, "\n")
	if strings.Contains(joined, "pid 200") {
		t.Errorf("a non-dumpable ordinary process must NOT be refused:\n%s", joined)
	}
	if !strings.Contains(joined, "pid 201") || !strings.Contains(joined, "no readable comm or cmdline") {
		t.Errorf("an entry with nothing readable must be refused:\n%s", joined)
	}
	if strings.Contains(joined, "pid 202") {
		t.Errorf("a process that vanished mid-scan must NOT be refused:\n%s", joined)
	}
	if len(off) != 1 {
		t.Errorf("exactly one refusal expected, got %v", off)
	}
	// The same entry owned by ANOTHER user is still refused: that is the
	// hidepid shape the branch exists for, and it is the only way to reach it.
	notOurs := os.Getuid() + 1
	off, err = slugAgentScanAs(proc, os.Getpid(), t.TempDir(), notOurs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(off, "\n"), "pid 201") {
		t.Errorf("another user's unreadable entry must be refused too, got %v", off)
	}
	// ... while another user's READABLE ordinary process is judged on its
	// comm and argv, and cleared.
	if strings.Contains(strings.Join(off, "\n"), "pid 200") {
		t.Errorf("another user's readable ordinary process must not be refused, got %v", off)
	}
}

// D2: a process table that cannot show the scan its own process, or that lists
// a bare handful of entries, is not this host's: refuse rather than report the
// host clear.
func TestSlugGuardSelfVisibilityFloor(t *testing.T) {
	empty := t.TempDir()
	if _, err := slugAgentScan(empty, os.Getpid(), ""); err == nil || !strings.Contains(err.Error(), "does not list this process") {
		t.Fatalf("a procDir listing nothing must refuse, got %v", err)
	}
	if err := SlugGuard(true, empty, false); err == nil || !strings.Contains(err.Error(), "does not list this process") {
		t.Fatalf("SlugGuard must refuse a procDir that cannot see itself, got %v", err)
	}
	// Self visible, but only a handful of entries: a namespace or a chroot.
	thin := t.TempDir()
	for _, pid := range []int{os.Getpid(), 2, 3} {
		d := filepath.Join(thin, strconv.Itoa(pid))
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "comm"), []byte("sh\n"), 0o644)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte("sh\x00"), 0o644)
	}
	if err := SlugGuard(true, thin, false); err == nil || !strings.Contains(err.Error(), "fewer than the floor") {
		t.Fatalf("a thin process table must refuse, got %v", err)
	}
	// A table shaped like a host's passes.
	if err := SlugGuard(true, slugFakeProc(t), false); err != nil {
		t.Fatalf("a host-shaped process table must pass: %v", err)
	}
}

// D5: a vp that was copied AND edited defeats identity matching, but it is
// still invoked as `<something> mcp` or `<something> hook`.
func TestSlugAgentScanCatchesAnMcpOrHookSubcommand(t *testing.T) {
	proc := slugFakeProc(t)
	for i, argv := range [][]string{
		{"/opt/unrelated/zzz", "mcp"},
		{"/opt/unrelated/zzz", "hook"},
	} {
		d := filepath.Join(proc, strconv.Itoa(300+i))
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "comm"), []byte("zzz\n"), 0o644)
		os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)
		os.Symlink("/opt/unrelated/zzz", filepath.Join(d, "exe"))
	}
	off, err := slugAgentScan(proc, os.Getpid(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(off, "\n")
	for _, pid := range []string{"pid 300", "pid 301"} {
		if !strings.Contains(joined, pid) {
			t.Errorf("%s runs an mcp/hook subcommand and must be refused:\n%s", pid, joined)
		}
	}
}

// ---------------------------------------------------------------- CN2

func TestSlugApplyRefusesAnUnknownSourceSlug(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root)
	if _, err := ApplyProjectSlugMigration(o); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The plan cannot be re-derived once the vault is migrated, so the options
	// are the ones the apply used, with only --from changed to a typo.
	o2 := o
	o2.From = "typo-ng"
	_, err := ApplyProjectSlugMigration(o2)
	if err == nil || !strings.Contains(err.Error(), "no history in this vault") {
		t.Fatalf("a --from that never existed must be refused, not reported as applied, got %v", err)
	}
	// The real source still reports "already applied".
	res, err := ApplyProjectSlugMigration(o)
	if err != nil || !res.AlreadyApplied {
		t.Fatalf("the migrated vault must still report already-applied: %+v %v", res, err)
	}
}

// ---------------------------------------------------------------- D3: U1-U5

// U1. A move's source must be a regular file: a symlink would be followed,
// and the thing at the other end is not what the plan counted.
func TestSlugGuardMoveRefusesANonRegularSource(t *testing.T) {
	root := t.TempDir()
	slugWrite(t, root, "real/file.md", "content\n")
	// A directory (or any non-regular file) where a file is expected: moving
	// it would move a whole tree the plan never counted. A symlink cannot
	// reach this check, because ResolveSafePath resolves it first.
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	j, err := OpenSlugJournal(filepath.Join(t.TempDir(), "j.tsv"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	err = slugMove(root, "adir", "moved-dir", j)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a non-regular source must be refused, got %v", err)
	}
	if slugExists(root, "moved-dir") {
		t.Fatal("the refusal still moved something")
	}
	if slugExists(root, "moved.md") {
		t.Fatal("the refusal still moved something")
	}
	if err := slugMove(root, "real/file.md", "moved.md", j); err != nil {
		t.Fatalf("a regular file must move: %v", err)
	}
}

// U2. The vault root lock is the only thing standing between this tool and a
// concurrent vp commit, so a held lock must stop the apply.
func TestSlugGuardRefusesWhenTheVaultRootLockIsHeld(t *testing.T) {
	root := slugFixture(t)
	o := slugOpts(t, root)
	release, ok, err := vaultlock.TryAcquire(root, root)
	if err != nil || !ok {
		t.Fatalf("could not take the lock for the test: ok=%v err=%v", ok, err)
	}
	defer func() { _ = release() }()
	_, err = ApplyProjectSlugMigration(o)
	if err == nil || !strings.Contains(err.Error(), "root lock is held") {
		t.Fatalf("a held vault root lock must refuse the apply, got %v", err)
	}
	if n := gitRun(t, root, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("the refused apply committed (%s commits)", n)
	}
}

// U3. rm-vec refuses to overwrite a saved copy: that copy is the only way
// back for a file git does not track.
func TestSlugGuardRemoveVecRefusesAnExistingSavedCopy(t *testing.T) {
	root := t.TempDir()
	jp := filepath.Join(t.TempDir(), "j.tsv")
	j, err := OpenSlugJournal(jp, root)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	rel := "palace/.local/embed-cache/x/one.vec"
	slugWrite(t, root, rel, "first")
	if err := slugRemoveVec(root, rel, j); err != nil {
		t.Fatalf("the first removal must work: %v", err)
	}
	slugWrite(t, root, rel, "second")
	err = slugRemoveVec(root, rel, j)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("a second rm-vec of the same path must refuse, got %v", err)
	}
	if !slugExists(root, rel) {
		t.Fatal("the refusal removed the file anyway, losing the second copy")
	}
}

// U4. A vault inside another repository is refused BY THE APPLY, before any
// commit: vp never stages into a repository that is not the vault's own.
func TestSlugGuardApplyRefusesANestedVaultGit(t *testing.T) {
	slugEnv(t)
	outer := t.TempDir()
	gitRun(t, outer, "init", "-b", "main")
	gitRun(t, outer, "config", "user.email", "test@example.com")
	gitRun(t, outer, "config", "user.name", "Test")
	slugWrite(t, outer, "README.md", "the enclosing repository\n")
	gitRun(t, outer, "add", "-A")
	gitRun(t, outer, "commit", "-q", "-m", "outer")
	// The vault lives inside it and has no git of its own, so `git rev-parse`
	// inside it answers for the ENCLOSING repository.
	root := filepath.Join(outer, "vault")
	slugWrite(t, root, "Projects/"+slugFrom+"/resume.md", "---\nproject: "+slugFrom+"\n---\n\n# "+slugFrom+" — Working Context\n")
	head := gitRun(t, root, "rev-parse", "HEAD")
	_, err := ApplyProjectSlugMigration(SlugApplyOptions{
		Root: root, From: slugFrom, To: slugTo,
		ExpectHead: head, Expect: []byte("{}"),
		JournalPath: filepath.Join(t.TempDir(), "j.tsv"),
		ProcDir:     slugFakeProc(t), Home: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "inside another repository") {
		t.Fatalf("an apply against a nested vault must be refused, got %v", err)
	}
	if n := gitRun(t, outer, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("the refused apply committed into the enclosing repository (%s commits)", n)
	}
}

// U5. A held row's id must equal DrawerID(to, content): a hand-edited hold
// file must not be appended into the merged room under a wrong id.
func TestSlugGuardHeldRowIDInvariant(t *testing.T) {
	content := "A held decision."
	good := drawerRow(DrawerID(slugTo, content), "decisions", content, "decision",
		"session/2026-08-22-0f0d1eb5-01#decision/0/"+DrawerID(slugTo, content))
	if _, _, err := slugHeldRows([]byte(good+"\n"), slugTo, map[string]string{}); err != nil {
		t.Fatalf("a consistent held row must pass: %v", err)
	}
	bad := drawerRow("deadbeef", "decisions", content, "decision", "session/x#decision/0/deadbeef")
	_, _, err := slugHeldRows([]byte(bad+"\n"), slugTo, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "is not DrawerID") {
		t.Fatalf("a held row with a wrong id must be refused, got %v", err)
	}
}

// G40 (imp3 R2-N2). A K0 rename whose destination already exists — the
// `premerge-stray-*.jsonl` an aborted earlier attempt leaves behind — must be
// refused rather than silently replaced.
func TestSlugGuardK0RefusesAnExistingDestination(t *testing.T) {
	root := slugFixture(t)
	p, err := PlanProjectSlugMigration(root, slugFrom, slugTo)
	if err != nil {
		t.Fatal(err)
	}
	dst := ""
	for _, r := range p.Counts.K0Rename {
		// The hold file's destination is fixed; a colliding session is
		// renamed to the next free number instead, by design.
		if strings.Contains(r.Dst, "/premerge-stray-") {
			dst = r.Dst
		}
	}
	if dst == "" {
		t.Fatal("the fixture must plan a hold-file rename for this test to mean anything")
	}
	slugWrite(t, root, dst, "left behind by an aborted attempt\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "an occupied K0 destination")
	_, err = PlanProjectSlugMigration(root, slugFrom, slugTo)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("an occupied K0 destination must be refused, got %v", err)
	}
}

// imp3 R2-N3. An `mk` line removes a file and keeps no copy, so the replay
// accepts only the two names the tool itself creates: a corrupted or
// hand-edited journal cannot turn a rollback into a delete of tracked content.
func TestSlugReplayRefusesAnMkLineNamingAnythingElse(t *testing.T) {
	root := t.TempDir()
	slugWrite(t, root, "Projects/p/resume.md", "tracked content\n")
	jp := slugJournalFile(t, root, filepath.Join(t.TempDir(), "j.tsv"), "mk\tProjects/p/resume.md\t")
	_, err := ReplaySlugJournal(root, jp, false)
	if err == nil || !strings.Contains(err.Error(), "never creates") {
		t.Fatalf("an mk line naming a tracked file must be refused, got %v", err)
	}
	if !slugExists(root, "Projects/p/resume.md") {
		t.Fatal("the refused replay deleted the file anyway")
	}
	// The two names it does create replay normally.
	ok := "palace/.local/embed-cache/" + slugTo + "/" + slugCacheMarker
	slugWrite(t, root, ok, "sha\n")
	jp2 := slugJournalFile(t, root, filepath.Join(t.TempDir(), "j2.tsv"), "mk\t"+ok+"\t")
	if _, err := ReplaySlugJournal(root, jp2, false); err != nil {
		t.Fatalf("the marker must replay: %v", err)
	}
	if slugExists(root, ok) {
		t.Fatal("the marker was not removed")
	}
}

// imp2 round-2 NIT 2. --force-root is the one sanctioned override of the
// vault-identity refusal: the vault was moved after the journal was written.
// It is recorded as a warning, never silent.
func TestSlugReplayForceRootOverridesTheVaultCheck(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	slugWrite(t, b, "dst/one.bak", "one")
	jp := slugJournalFile(t, a, filepath.Join(t.TempDir(), "j.tsv"), "mv\tsrc/one.bak\tdst/one.bak")
	if _, err := ReplaySlugJournal(b, jp, false); err == nil {
		t.Fatal("without --force-root a foreign vault must be refused")
	}
	r, err := ReplaySlugJournal(b, jp, true)
	if err != nil {
		t.Fatalf("--force-root must replay: %v", err)
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "force-root") {
		t.Fatalf("--force-root must warn about what it overrode, got %v", r.Warnings)
	}
	if !slugExists(b, "src/one.bak") {
		t.Fatal("the forced replay did not do the work")
	}
}

// imp3 round-3 R3-S2. After D1 narrowed the fail-closed rule, `ours` has one
// job left: a window-Chair-shaped process whose exe cannot be resolved is
// refused when it is OURS (we could always read our own), and judged normally
// when it belongs to another user. Nothing pinned that, so a future edit there
// was invisible.
func TestSlugWindowChairWithAnUnreadableExeFailsClosedOnlyWhenItIsOurs(t *testing.T) {
	proc := slugFakeProc(t)
	argv := []string{"claude", "--settings", `{"disableAllHooks":true}`, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`}
	d := filepath.Join(proc, "400")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d, "comm"), []byte("claude\n"), 0o644)
	os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644)
	os.WriteFile(filepath.Join(d, "stat"), []byte("stat\n"), 0o644)
	// No exe symlink: the window Chair cannot be identified.
	off, err := slugAgentScanAs(proc, os.Getpid(), t.TempDir(), os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(off, "\n"), "exe cannot be resolved") {
		t.Fatalf("our own window-Chair-shaped process with no resolvable exe must be refused, got %v", off)
	}
	// The same entry owned by someone else is not ours to fail closed on, so
	// the exemption applies on its argv alone.
	off, err = slugAgentScanAs(proc, os.Getpid(), t.TempDir(), os.Getuid()+1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(off, "\n"), "pid 400") {
		t.Fatalf("another user's window-Chair-shaped process must be exempt on argv alone, got %v", off)
	}
}

// imp2 round-3 NIT 4: the mk containment rule has two halves. This pins the
// PREFIX half, which the basename case above cannot reach.
func TestSlugReplayRefusesAnMkLineOutsideTheCacheDirectory(t *testing.T) {
	root := t.TempDir()
	rel := "Projects/p/" + slugCacheMarker // the right basename, the wrong place
	slugWrite(t, root, rel, "not the tool's marker\n")
	jp := slugJournalFile(t, root, filepath.Join(t.TempDir(), "j.tsv"), "mk\t"+rel+"\t")
	if _, err := ReplaySlugJournal(root, jp, false); err == nil || !strings.Contains(err.Error(), "never creates") {
		t.Fatalf("an mk line outside the cache directory must be refused, got %v", err)
	}
	if !slugExists(root, rel) {
		t.Fatal("the refused replay deleted the file anyway")
	}
}
