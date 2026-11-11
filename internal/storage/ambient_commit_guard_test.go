// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// ambientCommitFixture builds a committable git repo and installs it as the
// ambient configured vault root, returning the root and a vault-relative path
// whose file exists and is stageable.
//
// 🔴 THE FIXTURE MUST REACH THE COMMIT, AND THAT IS NOT FREE.
// commitAndPushPathsCore calls prepareCommitPaths first and returns early when
// nothing is left to commit, so a fixture with no real repo, or nothing
// stageable, never reaches the seat — and the "no panic" assertion would then be
// satisfied by a write that never happened. Each row below carries a
// non-vacuity sub-test that proves the same fixture DOES commit when the ambient
// root points elsewhere.
func ambientCommitFixture(t *testing.T) (root, rel string) {
	t.Helper()
	root = initTestRepo(t)
	rel = "Projects/p/notes.md"
	writeFile(t, root, rel, "body\n")
	return root, rel
}

// recoverPanicMessage runs f and returns the recovered panic text, or "" if it
// did not panic.
func recoverPanicMessage(t *testing.T, f func()) (msg string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			msg, _ = r.(string)
		}
	}()
	f()
	return ""
}

// TestCommitAndPushPathsRefusesAmbientVault is SEAT 1.
//
// 🔴 IT ASSERTS THE SEAT LABEL, NOT MERELY "A PANIC". The public path continues
// into commitPathspec, whose independent Seat 2 guard panics too — so a row that
// only asserted "a panic naming the root" would stay GREEN with Seat 1's own
// guard deleted, and would report an inert arm as pinned. The `seat:` line is
// what makes the single-arm mutation discriminate.
func TestCommitAndPushPathsRefusesAmbientVault(t *testing.T) {
	root, rel := ambientCommitFixture(t)
	restore := surface.SetAmbientVaultRoot(root)
	defer restore()

	msg := recoverPanicMessage(t, func() {
		_, _ = CommitAndPushPaths(root, "msg", []string{rel}, false)
	})
	if msg == "" {
		t.Fatalf("CommitAndPushPaths returned without panicking for a commit to the ambient vault root %s", root)
	}
	if !strings.Contains(msg, root) {
		t.Errorf("panic does not name the configured root %s: %s", root, msg)
	}
	if !strings.Contains(msg, "seat:  commitAndPushPathsCore") {
		t.Errorf("panic did not come from Seat 1 — Seat 2 is masking it. Got: %s", msg)
	}
}

// TestCommitAndPushPathsCommitsWhenNotTheAmbientRoot is the non-vacuity control
// for Seat 1: the identical fixture must actually commit when the ambient root
// is some other directory. Without it, the row above could be passing on a
// fixture that never reached the seat at all.
func TestCommitAndPushPathsCommitsWhenNotTheAmbientRoot(t *testing.T) {
	root, rel := ambientCommitFixture(t)
	restore := surface.SetAmbientVaultRoot(t.TempDir())
	defer restore()

	res, err := CommitAndPushPaths(root, "msg", []string{rel}, false)
	if err != nil {
		t.Fatalf("CommitAndPushPaths: %v", err)
	}
	if res == nil || res.CommitSHA == "" {
		t.Fatalf("fixture did not reach the commit (res=%+v) — the Seat 1 row would be inert", res)
	}
}

// TestCommitPathspecRefusesAmbientVault is SEAT 2, driven through a caller that
// provably SKIPS Seat 1.
//
// 🔴 commitPathspec is called DIRECTLY, not through CommitAndPushPaths. Going
// through the public entry point would cross Seat 1 first, and the row would
// pass on Seat 1's panic while saying nothing about Seat 2.
func TestCommitPathspecRefusesAmbientVault(t *testing.T) {
	root, rel := ambientCommitFixture(t)
	gitRun(t, root, "add", "--", rel)
	restore := surface.SetAmbientVaultRoot(root)
	defer restore()

	msg := recoverPanicMessage(t, func() {
		_ = commitPathspec(root, gitCommitTimeout, "msg", []string{rel})
	})
	if msg == "" {
		t.Fatalf("commitPathspec returned without panicking for a commit to the ambient vault root %s", root)
	}
	if !strings.Contains(msg, "seat:  commitPathspec") {
		t.Errorf("panic did not come from Seat 2. Got: %s", msg)
	}
}

// TestCommitPathspecCommitsWhenNotTheAmbientRoot is Seat 2's non-vacuity control.
func TestCommitPathspecCommitsWhenNotTheAmbientRoot(t *testing.T) {
	root, rel := ambientCommitFixture(t)
	gitRun(t, root, "add", "--", rel)
	restore := surface.SetAmbientVaultRoot(t.TempDir())
	defer restore()

	if err := commitPathspec(root, gitCommitTimeout, "msg", []string{rel}); err != nil {
		t.Fatalf("commitPathspec: %v — the Seat 2 row would be inert", err)
	}
}

// TestGitCommitAllAtRefusesAmbientVault is SEAT 3. This is the helper whose own
// comment says its bare `git add -A` must never run against the real vault; that
// half of the precondition is now code.
func TestGitCommitAllAtRefusesAmbientVault(t *testing.T) {
	root, _ := ambientCommitFixture(t)
	restore := surface.SetAmbientVaultRoot(root)
	defer restore()

	msg := recoverPanicMessage(t, func() {
		_ = GitCommitAllAt(root, "msg", testClock())
	})
	if msg == "" {
		t.Fatalf("GitCommitAllAt returned without panicking for a commit to the ambient vault root %s", root)
	}
	if !strings.Contains(msg, "seat:  GitCommitAllAt") {
		t.Errorf("panic did not come from Seat 3. Got: %s", msg)
	}
}

// TestGitCommitAllAtCommitsWhenNotTheAmbientRoot is Seat 3's non-vacuity control.
func TestGitCommitAllAtCommitsWhenNotTheAmbientRoot(t *testing.T) {
	root, _ := ambientCommitFixture(t)
	restore := surface.SetAmbientVaultRoot(t.TempDir())
	defer restore()

	if err := GitCommitAllAt(root, "msg", testClock()); err != nil {
		t.Fatalf("GitCommitAllAt: %v — the Seat 3 row would be inert", err)
	}
}

// 🔴 SEATS 4-6: THE CONTROL IS THE EVIDENCE, NOT THE ASSERTION.
//
// An earlier version of these three rows called each seat with an empty fixture
// (nil entries, no derived paths, an empty plan). The refusal assertion passed
// and the mutations went red — but for the wrong reason: with a NON-ambient root
// those fixtures returned early, long before the commit, so the control proved
// only "no sentinel", which is true whether or not a commit would ever have
// happened. The rows demonstrated that the ENTRY CHECK fires. They did not
// demonstrate that a would-be commit was prevented at the commit line.
//
// Each row below therefore has a control that makes the unguarded call actually
// COMMIT, and asserts the commit's effect. That control is what makes the
// refusal arm evidence of protection rather than evidence of an early return.

// migratedVaultWithTrackedDrawer is a migrated vault (marker in HEAD, derived
// ignore lines in place) that has a derived path tracked again — the state a
// clean merge of a lagging host's drawer commit produces, and the only state in
// which untrackDerivedAfterMerge reaches its commit at derived_merge.go:368.
func migratedVaultWithTrackedDrawer(t *testing.T) (root, rel string) {
	t.Helper()
	root = layMigratedVault(t)
	rel = "palace/p/drawers/w2/b.jsonl"
	writeFile(t, root, rel, "new\n")
	// -f: the migration added the derived ignore lines, so a plain add skips it.
	gitRun(t, root, "add", "-f", "--", rel)
	gitRun(t, root, "commit", "-q", "-m", "re-track a drawer")
	return root, rel
}

// TestUntrackDerivedAfterMergeRefusesAmbientVault is SEAT 5, the error arm,
// against a fixture that genuinely reaches the commit.
func TestUntrackDerivedAfterMergeRefusesAmbientVault(t *testing.T) {
	// 🔴 THE CONTROL: the unguarded call must COMMIT. It untracks the derived
	// path and commits that index edit; asserting the path is no longer tracked
	// is asserting the commit happened.
	t.Run("control_the_unguarded_call_commits", func(t *testing.T) {
		root, rel := migratedVaultWithTrackedDrawer(t)
		restore := surface.SetAmbientVaultRoot(t.TempDir())
		defer restore()

		if !tracked(t, root, rel) {
			t.Fatalf("fixture invariant broken: %s is not tracked, so there is nothing to untrack", rel)
		}
		head := gitRun(t, root, "rev-parse", "HEAD")
		untracked, err := untrackDerivedAfterMerge(root)
		if err != nil {
			t.Fatalf("untrackDerivedAfterMerge: %v", err)
		}
		if !slices.Contains(untracked, rel) {
			t.Fatalf("untracked = %q, want it to contain %s — the fixture did not reach the commit", untracked, rel)
		}
		if tracked(t, root, rel) {
			t.Errorf("%s is still tracked, so no commit landed", rel)
		}
		if after := gitRun(t, root, "rev-parse", "HEAD"); after == head {
			t.Errorf("HEAD did not move (%s) — the control proves no commit, so the refusal arm proves nothing", after)
		}
	})

	t.Run("refuses", func(t *testing.T) {
		root, _ := migratedVaultWithTrackedDrawer(t)
		restore := surface.SetAmbientVaultRoot(root)
		defer restore()
		head := gitRun(t, root, "rev-parse", "HEAD")
		_, err := untrackDerivedAfterMerge(root)
		if !errors.Is(err, ErrAmbientVaultCommit) {
			t.Fatalf("untrackDerivedAfterMerge err = %v, want ErrAmbientVaultCommit", err)
		}
		if after := gitRun(t, root, "rev-parse", "HEAD"); after != head {
			t.Errorf("HEAD moved to %s despite the refusal — a commit landed", after)
		}
	})
}

// TestHealDerivedConflictsRefusesAmbientVault is SEAT 4, driven through
// PRODUCTION WIRING (storage.Pull → mergeFetchedTip → healConflicts) against a
// real conflicted merge on a derived path — the only state in which
// healDerivedConflicts reaches its `git commit --no-edit` at derived_merge.go:291.
func TestHealDerivedConflictsRefusesAmbientVault(t *testing.T) {
	// A migrated host, and a lagging host that edited the drawer the migration
	// untracked: merging the lagging tip conflicts on exactly that derived path.
	setup := func(t *testing.T) (*mergeWorld, string) {
		t.Helper()
		w := newMergeWorld(t)
		mig := w.host(t)
		migrateFixture(t, mig)
		lag := w.host(t)
		editDrawer(t, lag)
		gitRun(t, lag, "push", "-q", "origin", "main")
		return w, mig
	}

	// 🔴 THE CONTROL: the unguarded heal must CONCLUDE THE MERGE WITH A COMMIT.
	// res.Derived.Healed naming the drawer, with no merge left in progress, is
	// that commit's effect.
	t.Run("control_the_unguarded_heal_commits", func(t *testing.T) {
		_, mig := setup(t)
		restore := surface.SetAmbientVaultRoot(t.TempDir())
		defer restore()

		res, err := Pull(mig, []string{"origin"})
		if err != nil {
			t.Fatalf("Pull: %v", err)
		}
		if !slices.Contains(res.Derived.Healed, fixtureDrawer) {
			t.Fatalf("Healed = %q, want it to contain %s — the fixture did not reach the heal's commit",
				res.Derived.Healed, fixtureDrawer)
		}
		if mergeInProgress(mig) {
			t.Error("a merge is still in progress, so the heal did not commit")
		}
	})

	t.Run("refuses", func(t *testing.T) {
		_, mig := setup(t)
		restore := surface.SetAmbientVaultRoot(mig)
		defer restore()

		_, err := Pull(mig, []string{"origin"})
		if !errors.Is(err, ErrAmbientVaultCommit) {
			t.Fatalf("Pull err = %v, want ErrAmbientVaultCommit", err)
		}
		// The seat label distinguishes Seat 4 from Seat 5, which the same pull
		// would otherwise reach on a clean merge.
		if !strings.Contains(err.Error(), "healDerivedConflicts") {
			t.Errorf("refusal did not come from Seat 4: %v", err)
		}
	})
}

// TestApplyAuthoredOnlyMigrationRefusesAmbientVault is SEAT 6, against a real
// plan that reaches the migration commit.
func TestApplyAuthoredOnlyMigrationRefusesAmbientVault(t *testing.T) {
	opts := AuthoredOnlyApplyOptions{Date: "2026-10-09", Surface: surface.MCPSurfaceVersion, Message: "m\n"}

	// 🔴 THE CONTROL: the unguarded migration must COMMIT. HEAD moving is that
	// commit; an empty plan against a vault with no manifest would instead fail
	// at the manifest read, which is what the earlier version of this row did.
	t.Run("control_the_unguarded_migration_commits", func(t *testing.T) {
		root := authoredOnlyFixture(t)
		v := NewVault(root)
		plan, err := PlanAuthoredOnlyMigration(v)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		restore := surface.SetAmbientVaultRoot(t.TempDir())
		defer restore()

		head := gitRun(t, root, "rev-parse", "HEAD")
		if _, err := v.ApplyAuthoredOnlyMigration(plan, opts); err != nil {
			t.Fatalf("apply: %v", err)
		}
		if after := gitRun(t, root, "rev-parse", "HEAD"); after == head {
			t.Errorf("HEAD did not move — the control proves no commit, so the refusal arm proves nothing")
		}
	})

	t.Run("refuses", func(t *testing.T) {
		root := authoredOnlyFixture(t)
		v := NewVault(root)
		plan, err := PlanAuthoredOnlyMigration(v)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		restore := surface.SetAmbientVaultRoot(root)
		defer restore()

		head := gitRun(t, root, "rev-parse", "HEAD")
		if _, err := v.ApplyAuthoredOnlyMigration(plan, opts); !errors.Is(err, ErrAmbientVaultCommit) {
			t.Fatalf("ApplyAuthoredOnlyMigration err = %v, want ErrAmbientVaultCommit", err)
		}
		if after := gitRun(t, root, "rev-parse", "HEAD"); after != head {
			t.Errorf("HEAD moved to %s despite the refusal — a commit landed", after)
		}
	})
}

// TestAmbientCommitErrorIsNotSwallowed pins the OTHER half of the error arm: the
// sentinel reaches the caller, and it reaches it as its own class rather than as
// a per-remote outcome.
//
// 🔴 THE TWO ASSERTIONS PIN DIFFERENT THINGS, AND NEITHER IS REDUNDANT.
// "Pull returns the sentinel" is the not-swallowed half. "RemoteResults carries
// no sentinel" is the class half — recorded against a remote it would be
// reported as "remote X failed", and pullSweepStops would skip the remaining
// remotes as having "failed in a way the pull does not merge past". No remote
// did anything wrong.
func TestAmbientCommitErrorIsNotSwallowed(t *testing.T) {
	root, rel := ambientCommitFixture(t)
	origin := initBareRemote(t)
	gitRun(t, root, "remote", "add", "origin", origin)
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-m", "pre-push")
	gitRun(t, root, "push", "-q", "origin", "main")
	_ = rel

	restore := surface.SetAmbientVaultRoot(root)
	defer restore()

	res, err := Pull(root, []string{"origin"})
	if !errors.Is(err, ErrAmbientVaultCommit) {
		t.Fatalf("Pull err = %v, want ErrAmbientVaultCommit — a refused commit reported as a successful pull", err)
	}
	if res != nil {
		for remote, rerr := range res.RemoteResults {
			if errors.Is(rerr, ErrAmbientVaultCommit) {
				t.Errorf("ErrAmbientVaultCommit was recorded as a per-remote result for %q; "+
					"a refusal to commit the configured vault is not a remote's problem", remote)
			}
		}
	}
}

// testClock is a fixed time for the GitCommitAllAt rows.
func testClock() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }

// TestPruneMirrorsReconcileRefusalIsNotSwallowed is the second swallow, one
// caller further out than pullCore's, and the arm that hides it.
//
// 🔴 THE ROUTE. pruneMirrors (the verified prune, reached from
// PruneMirrorsVerifiedWithDowngrade with push=true, which is what gates the
// reconcile at vaultsync_verify.go:233) calls reconcileIfAhead, which calls
// mergeFetchedTip, which reaches Seats 4 and 5. reconcileIfAhead's default arm
// records a merge failure in reconcileErrs and returns a nil TOP-LEVEL error;
// both of its callers test only that top-level error. pruneMirrors then takes
// its no-stage early return (`if len(stage) == 0`, vaultsync_verify.go:521) and
// reports success with out.err(), which reconcileErrs never reaches.
//
// So without reconcileIfAhead's sentinel case, the guard prevents the commit
// while its own refusal is dropped and the call reports success — the exact
// silence this unit exists to replace, at a caller pullCore's fix does not
// cover.
//
// 🔴 THE FIXTURE MUST TAKE THE NO-STAGE ARM, AND GETTING THIS WRONG HIDES THE
// ROUTE. A first attempt passed a path the verifier accepts ("T/wrap.md"), so
// the prune staged and committed it — and Seat 2 (commitPathspec) panicked
// before the reconcile's refusal could be observed at all. The route looked
// protected, but by the wrong seat, and the control's "HEAD moved" was the
// PRUNE's commit rather than the reconcile's. The verifier below accepts
// NOTHING, so nothing stages, Seat 2 never fires, and the reconcile's refusal is
// the only thing that can surface — which is precisely the arm that hid it.
func TestPruneMirrorsReconcileRefusalIsNotSwallowed(t *testing.T) {
	// A migrated host, locally ahead of origin, and a lagging host whose pushed
	// drawer edit conflicts with the migration on a derived path.
	setup := func(t *testing.T) string {
		t.Helper()
		w := newMergeWorld(t)
		mig := w.host(t)
		migrateFixture(t, mig) // local commit: mig is now ahead of origin/main
		lag := w.host(t)
		editDrawer(t, lag)
		gitRun(t, lag, "push", "-q", "origin", "main")
		return mig
	}

	// 🔴 THE CONTROL: on the unguarded run the reconcile reaches the heal's
	// commit, so HEAD moves. Without this the refusal arm below could be passing
	// on a reconcile that never merged anything.
	t.Run("control_the_unguarded_reconcile_commits", func(t *testing.T) {
		mig := setup(t)
		restore := surface.SetAmbientVaultRoot(t.TempDir())
		defer restore()

		head := gitRun(t, mig, "rev-parse", "HEAD")
		// acceptOnly with no bodies accepts nothing, so the prune stages nothing
		// and its own commit never runs. Any HEAD movement is the RECONCILE's
		// commit and nothing else.
		_, out, _, err := PruneMirrorsVerifiedWithDowngrade(mig, []string{"notes.md"}, true, acceptOnly(nil))
		if err != nil {
			t.Fatalf("PruneMirrorsVerifiedWithDowngrade: %v", err)
		}
		if len(out.Committed) != 0 {
			t.Fatalf("the prune committed %q — the fixture is not on the no-stage arm, so Seat 2 could mask the route", out.Committed)
		}
		if after := gitRun(t, mig, "rev-parse", "HEAD"); after == head {
			t.Fatalf("HEAD did not move — the reconcile did not commit, so this route proves nothing")
		}
	})

	t.Run("refuses_and_the_refusal_surfaces", func(t *testing.T) {
		mig := setup(t)
		restore := surface.SetAmbientVaultRoot(mig)
		defer restore()

		head := gitRun(t, mig, "rev-parse", "HEAD")
		_, _, _, err := PruneMirrorsVerifiedWithDowngrade(mig, []string{"notes.md"}, true, acceptOnly(nil))
		if !errors.Is(err, ErrAmbientVaultCommit) {
			t.Fatalf("PruneMirrorsVerifiedWithDowngrade err = %v, want ErrAmbientVaultCommit — "+
				"the reconcile's refusal was swallowed and the prune reported success", err)
		}
		if after := gitRun(t, mig, "rev-parse", "HEAD"); after != head {
			t.Errorf("HEAD moved to %s despite the refusal", after)
		}
	})
}
