// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// split-purge-commits-its-own-result (U1), storage half: the commit guard in
// every committer, its raw-git recoveries, and the reconcile paths (push
// rejection, pull merge) that must wait for a purge holding the lock.

// crashedPurgeVault is a vault in which a split purge of "alpha" (both
// trees) and "orch" (Projects/ only) wrote its records and then stopped:
// optionally after `git rm` had staged the tracked removal. "keep" has a
// change someone else staged.
func crashedPurgeVault(t *testing.T, rmStaged bool) string {
	t.Helper()
	dir := initTestRepo(t)
	for rel, body := range map[string]string{
		"Projects/alpha/resume.md":              "alpha\n",
		"Projects/alpha/memory/m.md":            "memory\n",
		"palace/alpha/kg/entities.jsonl":        "{}\n",
		"Projects/orch/resume.md":               "orch\n",
		"Projects/keep/resume.md":               "keep\n",
		"Projects/alpha/transcripts/x.json.bak": "ignored\n",
	} {
		writeFile(t, dir, rel, body)
	}
	writeFile(t, dir, ".gitignore", "*.bak\n.vp-locks/\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	writeFile(t, dir, "Projects/keep/resume.md", "someone else's staged edit\n")
	gitRun(t, dir, "add", "Projects/keep/resume.md")
	for _, s := range []string{"alpha", "orch"} {
		b, err := (departure.Record{Slug: s, Kind: departure.MovedToVault, To: "q"}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, dir, departure.RelPath(s), string(b))
	}
	if rmStaged {
		gitRun(t, dir, "rm", "-r", "-q", "--", "Projects/alpha", "palace/alpha", "Projects/orch")
	}
	return dir
}

// Row 7: while a record is pending, every committer refuses — the core (tidy,
// harvest, vp_vault_sync paths, task auto-commits), CommitRemovals and the
// verified prune — and leaves nothing staged. A commit that merely CARRIES
// the record is refused too (N3): after a crash it would publish the record
// without its deletions.
func TestCommitGuardRefusesEveryCommitterWhileADepartureIsPending(t *testing.T) {
	dir := crashedPurgeVault(t, false)
	writeFile(t, dir, "Projects/keep/memory/new.md", "a typed write meanwhile\n")
	writeFile(t, dir, "Projects/keep/sessions/2026-09-27-abcd-01.md", "a capture meanwhile: tidy would sweep it\n")
	stagedBefore := gitRun(t, dir, "diff", "--cached", "--name-only")
	head := gitRun(t, dir, "rev-parse", "HEAD")

	attempts := map[string]func() error{
		"tidy": func() error { _, err := TidyVault(dir, false); return err },
		"harvest-shaped": func() error {
			_, _, err := CommitAndPushPathsWithDowngrade(dir, "harvest", []string{"Projects/keep/memory"}, false)
			return err
		},
		"vault-commit-of-the-record-itself": func() error {
			_, err := CommitAndPushPaths(dir, "commit the record", []string{departure.RelPath("alpha")}, false)
			return err
		},
		"commit-removals": func() error {
			_, err := CommitRemovals(dir, "remove", []string{"Projects/keep/resume.md"})
			return err
		},
		"verified-prune": func() error {
			_, _, _, err := PruneMirrorsVerifiedWithDowngrade(dir, []string{"Projects/orch/resume.md"}, false, acceptOnly(nil, "orch\n"))
			return err
		},
	}
	for name, try := range attempts {
		t.Run(name, func(t *testing.T) {
			err := try()
			if !errors.Is(err, ErrPendingDeparture) {
				t.Fatalf("%s = %v, want the pending-departure refusal", name, err)
			}
			for _, w := range []string{"did not finish", "finish it:", "or undo it:", "stay uncommitted until recovery"} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("refusal lacks %q:\n%s", w, err)
				}
			}
			if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
				t.Errorf("%s committed: HEAD %s -> %s", name, head, got)
			}
			if got := gitRun(t, dir, "diff", "--cached", "--name-only"); got != stagedBefore {
				t.Errorf("a refused %s left staged:\n%s\nwant:\n%s", name, got, stagedBefore)
			}
		})
	}

	// Once the purge is finished (here by hand), every committer works again.
	gitRun(t, dir, "rm", "-r", "-q", "--", "Projects/alpha", "palace/alpha", "Projects/orch")
	gitRun(t, dir, "add", "--", departure.RelPath("alpha"), departure.RelPath("orch"))
	gitRun(t, dir, "commit", "-q", "-m", "finish", "--", departure.RelPath("alpha"), departure.RelPath("orch"), "Projects/alpha", "palace/alpha", "Projects/orch")
	if _, _, err := CommitAndPushPathsWithDowngrade(dir, "harvest", []string{"Projects/keep/memory"}, false); err != nil {
		t.Errorf("after the purge is committed the harvest must work: %v", err)
	}
}

// The refusal's own recoveries, run as written on a crashed vault — before and
// after `git rm` had staged — with a Projects-only slug among them. Finishing
// leaves exactly the other party's staged change; undoing leaves HEAD as it
// was.
func TestPendingDepartureRecoveriesWorkAsWritten(t *testing.T) {
	cmdOf := func(t *testing.T, msg, label string) string {
		t.Helper()
		m := regexp.MustCompile(`(?m)^  - ` + regexp.QuoteMeta(label) + `: (.*)$`).FindStringSubmatch(msg)
		if m == nil {
			t.Fatalf("no %q line in:\n%s", label, msg)
		}
		return m[1]
	}
	run := func(t *testing.T, sh string) {
		t.Helper()
		if out, err := exec.Command("sh", "-c", sh).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", sh, err, out)
		}
	}
	for _, rmStaged := range []bool{false, true} {
		name := map[bool]string{false: "before-git-rm", true: "after-git-rm"}[rmStaged]
		t.Run("finish/"+name, func(t *testing.T) {
			dir := crashedPurgeVault(t, rmStaged)
			err := refuseOnPendingDepartures(dir)
			if err == nil {
				t.Fatal("test premise: records are pending")
			}
			if !rmStaged {
				// Before its git rm, a crashed purge looks exactly like a record
				// forged over a live project: the advice never removes a tree
				// that still holds tracked files. It says to re-run instead.
				if fin := cmdOf(t, err.Error(), "finish it"); !strings.HasPrefix(fin, "re-run the command") || strings.Contains(err.Error(), "git -C "+dir+" rm -r") {
					t.Fatalf("finish advice before git rm must be a re-run, never a git rm -r:\n%s", err)
				}
				return
			}
			run(t, cmdOf(t, err.Error(), "finish it"))
			if got := gitRun(t, dir, "status", "--porcelain=v1", "-uall"); got != "M  Projects/keep/resume.md" {
				t.Errorf("after finish it:\n%s", got)
			}
			if got := gitRun(t, dir, "ls-files", "--", "Projects/alpha", "palace/alpha", "Projects/orch"); got != "" {
				t.Errorf("tracked files remain after finish it:\n%s", got)
			}
			if st := gitRun(t, dir, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(st, departure.RelPath("orch")) || strings.Contains(st, "keep") {
				t.Errorf("finish it must commit both records and nothing of keep:\n%s", st)
			}
		})
		t.Run("undo/"+name, func(t *testing.T) {
			dir := crashedPurgeVault(t, rmStaged)
			head := gitRun(t, dir, "rev-parse", "HEAD")
			err := refuseOnPendingDepartures(dir)
			if err == nil {
				t.Fatal("test premise: records are pending")
			}
			run(t, cmdOf(t, err.Error(), "or undo it"))
			if got := gitRun(t, dir, "status", "--porcelain=v1", "-uall"); got != "M  Projects/keep/resume.md" {
				t.Errorf("after undo it:\n%s", got)
			}
			if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
				t.Errorf("undo moved HEAD")
			}
			if _, err := os.Stat(filepath.Join(dir, "Projects/alpha/transcripts/x.json.bak")); err != nil {
				t.Errorf("undo lost an ignored file: %v", err)
			}
		})
	}
}

// D3 (split-and-sweep-reporting-defects-found-by-rehearsal-a2): a committed
// record that is staged-deleted but still on disk has two porcelain entries
// ("D " and "??"). The guard must name it once — in the record list, the slug
// list, the finish and undo recipes and the commit subject — not once per entry.
func TestPendingDepartureNamesATwiceListedRecordOnce(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/keep/resume.md", "keep\n")
	b, err := (departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: "q"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	rec := departure.RelPath("alpha")
	writeFile(t, dir, rec, string(b))
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed")
	gitRun(t, dir, "rm", "-q", "--cached", "--", rec)
	if st := gitRun(t, dir, "status", "--porcelain=v1", "-uall", "--", departure.Dir); st != "D  "+rec+"\n?? "+rec {
		t.Fatalf("test premise: the record must show twice, got:\n%s", st)
	}
	err = refuseOnPendingDepartures(dir)
	var pe *PendingDepartureError
	if !errors.As(err, &pe) {
		t.Fatalf("want a PendingDepartureError, got %v", err)
	}
	if len(pe.Records) != 1 || pe.Records[0] != rec {
		t.Errorf("Records = %q, want exactly [%s]", pe.Records, rec)
	}
	msg := err.Error()
	// Once per list: the refusal line names it once, the finish recipe twice
	// (git add -- and git commit --), the undo recipe once.
	lines := strings.Split(msg, "\n")
	want := map[string]int{"refusing to commit: ": 1, "  - finish it: ": 2, "  - or undo it: ": 1}
	for prefix, n := range want {
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) {
				found = true
				if got := strings.Count(line, rec); got != n {
					t.Errorf("%q names %s %d times, want %d:\n%s", prefix, rec, got, n, line)
				}
			}
		}
		if !found {
			t.Errorf("no %q line in:\n%s", prefix, msg)
		}
	}
	if !strings.Contains(msg, "a departure of alpha did not finish") || !strings.Contains(msg, `"vault split: purge alpha (finished by hand)"`) {
		t.Errorf("slug list or subject names alpha more than once:\n%s", msg)
	}
}

// Row 6 (N1): a rejected push's reconcile — fetch, guard, merge — waits for a
// purge holding the vault commit lock, instead of merging over the purge's
// staged removal (or stashing it out from under it).
func TestPushReconcileWaitsForTheCommitLock(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeFile(t, dir, "Projects/alpha/resume.md", "alpha\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed alpha")
	gitRun(t, dir, "push", "-q", "origin", "main")
	advanceRemote(t, bare, "Projects/keep/elsewhere.md", "another host\n")
	commitLocal(t, dir, "Projects/keep/local.md", "this host\n")

	// "The purge": holds the lock with its removal staged.
	release, err := vaultlock.Acquire(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "rm", "-r", "-q", "--", "Projects/alpha")

	done := make(chan *PushResult, 1)
	go func() {
		res := &PushResult{}
		pushCommitted(dir, []string{"origin"}, "main", nil, res)
		done <- res
	}()
	select {
	case <-done:
		t.Fatal("the reconcile ran while the purge held the lock")
	case <-time.After(700 * time.Millisecond):
	}
	if got := gitRun(t, dir, "diff", "--cached", "--name-status"); got != "D\tProjects/alpha/resume.md" {
		t.Errorf("the purge's staged removal was disturbed while it held the lock: %q", got)
	}
	if got := gitRun(t, dir, "stash", "list"); got != "" {
		t.Errorf("something autostashed during the hold: %q", got)
	}
	gitRun(t, dir, "commit", "-q", "-m", "purge alpha", "--", "Projects/alpha")
	if err := release(); err != nil {
		t.Fatal(err)
	}
	res := <-done
	if err := res.RemoteResults["origin"]; err != nil {
		t.Fatalf("push after the purge: %v", err)
	}
	if log := gitRun(t, dir, "log", "--format=%s", "-3"); !strings.Contains(log, "purge alpha") {
		t.Errorf("the purge commit must survive the reconcile:\n%s", log)
	}
	if got := gitRun(t, dir, "ls-files", "--", "Projects/alpha"); got != "" {
		t.Errorf("the purged tree came back: %q", got)
	}
}

// Row 6 (N1): a pull's merge waits for the lock the same way.
func TestPullMergeWaitsForTheCommitLock(t *testing.T) {
	dir := initTestRepo(t)
	bare := initBareRemote(t)
	gitRun(t, dir, "remote", "add", "origin", bare)
	writeFile(t, dir, "Projects/alpha/resume.md", "alpha\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed alpha")
	gitRun(t, dir, "push", "-q", "origin", "main")
	advanceRemote(t, bare, "Projects/keep/elsewhere.md", "another host\n")

	release, err := vaultlock.Acquire(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "rm", "-r", "-q", "--", "Projects/alpha")

	done := make(chan error, 1)
	go func() {
		res, err := Pull(dir, []string{"origin"})
		if err == nil {
			err = res.RemoteResults["origin"]
		}
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("the merge ran while the purge held the lock")
	case <-time.After(700 * time.Millisecond):
	}
	if got := gitRun(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved under a held lock")
	}
	gitRun(t, dir, "commit", "-q", "-m", "purge alpha", "--", "Projects/alpha")
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("pull after the purge: %v", err)
	}
	if got := gitRun(t, dir, "ls-files", "--", "Projects/alpha"); got != "" {
		t.Errorf("the purged tree came back through the merge: %q", got)
	}
}

// writeDeparture with requireAbsent refuses while the tree is present; the
// write-before-removal form (requireAbsent=false) reports whether it created
// the record (a rollback removes it) or overwrote one (a rollback restores it).
func TestWriteDepartureRequireAbsentAndCreated(t *testing.T) {
	dir := initTestRepo(t)
	v := NewVault(dir)
	writeFile(t, dir, "Projects/alpha/resume.md", "still here\n")
	if _, _, err := v.writeDeparture(nil, departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: ""}, true); err == nil {
		t.Fatal("a requireAbsent write must still refuse while the tree exists")
	}
	rel, created, err := v.writeDeparture(nil, departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: "q"}, false)
	if err != nil || !created || rel != departure.RelPath("alpha") {
		t.Fatalf("first write = %q %v %v", rel, created, err)
	}
	if _, created, err := v.writeDeparture(nil, departure.Record{Slug: "alpha", Kind: departure.MovedToVault, To: "q2"}, false); err != nil || created {
		t.Fatalf("an overwrite must report created=false: %v %v", created, err)
	}
}

// D2: the pending-record scan parses `status -z` untrimmed. A modified
// COMMITTED record lists first as " M …"; trimmed, its path lost a byte, and
// the generated undo then removed a committed record instead of restoring it.
func TestPendingDepartureRecoveriesForAnOverwrittenCommittedRecord(t *testing.T) {
	build := func(t *testing.T) (string, string) {
		dir := crashedPurgeVault(t, true)
		// alpha's record was committed by an earlier departure; the crashed
		// purge overwrote it.
		gitRun(t, dir, "add", "--", departure.RelPath("alpha"))
		gitRun(t, dir, "commit", "-q", "-m", "an earlier departure", "--", departure.RelPath("alpha"))
		committed := readFile(t, dir, departure.RelPath("alpha"))
		writeFile(t, dir, departure.RelPath("alpha"), strings.Replace(committed, `"to": "q"`, `"to": "q-again"`, 1))
		return dir, committed
	}
	cmdOf := func(t *testing.T, msg, label string) string {
		t.Helper()
		m := regexp.MustCompile(`(?m)^  - ` + regexp.QuoteMeta(label) + `: (.*)$`).FindStringSubmatch(msg)
		if m == nil {
			t.Fatalf("no %q line in:\n%s", label, msg)
		}
		return m[1]
	}
	sh := func(t *testing.T, c string) {
		t.Helper()
		if out, err := exec.Command("sh", "-c", c).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", c, err, out)
		}
	}
	t.Run("undo-restores-the-committed-record", func(t *testing.T) {
		dir, committed := build(t)
		err := refuseOnPendingDepartures(dir)
		if err == nil {
			t.Fatal("test premise: records are pending")
		}
		sh(t, cmdOf(t, err.Error(), "or undo it"))
		if got := readFile(t, dir, departure.RelPath("alpha")); got != committed {
			t.Errorf("undo must restore the committed record byte-for-byte:\n%s", got)
		}
		if got := gitRun(t, dir, "status", "--porcelain=v1", "-uall"); got != "M  Projects/keep/resume.md" {
			t.Errorf("after undo:\n%s", got)
		}
	})
	t.Run("finish-commits-the-new-record", func(t *testing.T) {
		dir, _ := build(t)
		err := refuseOnPendingDepartures(dir)
		if err == nil {
			t.Fatal("test premise: records are pending")
		}
		sh(t, cmdOf(t, err.Error(), "finish it"))
		if got := gitRun(t, dir, "status", "--porcelain=v1", "-uall"); got != "M  Projects/keep/resume.md" {
			t.Errorf("after finish:\n%s", got)
		}
		if got := gitRun(t, dir, "show", "HEAD:"+departure.RelPath("alpha")); !strings.Contains(got, `"q-again"`) {
			t.Errorf("finish must commit the overwritten record:\n%s", got)
		}
	})
}

// D3: in a vault nested inside another repository, a purge's records stay
// uncommitted by design, and the enclosing-repo prune commits nothing — so the
// guard must not refuse it.
func TestEnclosingRepoPruneIsNotRefusedByAPendingDeparture(t *testing.T) {
	outer := initTestRepo(t)
	vault := filepath.Join(outer, "vault")
	writeFile(t, vault, "T/wrap.md", "mirror\n")
	b, err := (departure.Record{Slug: "alpha", Kind: departure.MovedToVault}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, vault, departure.RelPath("alpha"), string(b))
	out, err := PruneMirrorsInEnclosingRepo(vault, []string{"T/wrap.md"}, acceptOnly(nil, "mirror\n"))
	if errors.Is(err, ErrPendingDeparture) {
		t.Fatalf("the enclosing-repo prune was refused by the commit guard: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(vault, "T/wrap.md")); serr == nil {
		t.Errorf("the untracked mirror must be pruned (outcome %+v, err %v)", out, err)
	}
}

// D4: preflight reads HEAD FIRST. A commit landing after that read — here
// between the tracked listing and the clean check — must make the purge
// refuse, never enter the HEAD the purge is checked against and be removed
// uncopied.
func TestPurgePreflightPinsHEADBeforeItsChecks(t *testing.T) {
	dir := initTestRepo(t)
	writeFile(t, dir, "Projects/alpha/resume.md", "alpha\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "seed alpha")
	collected := map[string]bool{"Projects/alpha/resume.md": true}
	late := "Projects/alpha/sessions/2026-09-27-late-01.md"
	defer func(f func()) { splitPurgePreflightAfterList = f }(splitPurgePreflightAfterList)
	splitPurgePreflightAfterList = func() {
		writeFile(t, dir, late, "committed by tidy in the window; never copied\n")
		gitRun(t, dir, "add", "--", late)
		gitRun(t, dir, "commit", "-q", "-m", "vault tidy: sweep 1 capture artifact", "--", late)
	}
	head, err := SplitPurgePreflight(dir, []string{"alpha"}, collected)
	splitPurgePreflightAfterList = func() {}
	if err == nil {
		b, _ := (departure.Record{Slug: "alpha", Kind: departure.MovedToVault}).Encode()
		writeFile(t, dir, departure.RelPath("alpha"), string(b))
		held, herr := vaultlock.AcquireHeld(dir, dir)
		if herr != nil {
			t.Fatal(herr)
		}
		_, err = CommitSplitPurgeLocked(held, SplitPurgeCommit{Slugs: []string{"alpha"}, Records: []string{departure.RelPath("alpha")}, Message: "purge", ExpectHead: head})
		held.Release()
	}
	if err == nil {
		t.Fatal("a commit in the preflight window must make the purge refuse")
	}
	if got := gitRun(t, dir, "ls-files", "--", late); got != late {
		t.Errorf("the file committed in the window was removed: ls-files %q", got)
	}
}
