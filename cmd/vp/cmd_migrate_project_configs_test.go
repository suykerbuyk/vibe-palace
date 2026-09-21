// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

// ONE-SHOT: deleted with cmd_migrate_project_configs.go.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

const (
	alphaConfig = "[meta]\nkind = \"project\"\n\n[palace.scoring]\nmin_score = 0.4\n\n[palace.scoring.rooms.decisions]\nhigh = 0.9\n\n[search]\nk = 5\n"
	betaConfig  = "[summarization]\nenabled = true\n"
	alphaRel    = "Projects/alpha/config.toml"
	betaRel     = "Projects/beta/config.toml"
)

// retirementVault builds a real-git vault holding two TRACKED project configs
// and a committed surface stamp at stamp (0: none). withRemote wires it to a
// bare origin and pushes; bare is "" otherwise. The host config enables git.
func retirementVault(t *testing.T, withRemote bool, stamp int) (root, bare string) {
	t.Helper()
	setupTestVaultEnv(t)
	if withRemote {
		root, bare = newRepoWithOrigin(t)
	} else {
		root = t.TempDir()
		gitRun(t, root, "init", "-b", "main")
		gitRun(t, root, "config", "user.email", "test@test.com")
		gitRun(t, root, "config", "user.name", "Test")
		mkfile(t, root, ".gitignore", ".vp-locks/\n")
	}
	mkfile(t, root, alphaRel, alphaConfig)
	mkfile(t, root, betaRel, betaConfig)
	mkfile(t, root, "Projects/alpha/commands/README.md", "commands\n")
	if stamp > 0 {
		mkfile(t, root, "palace/alpha/.surface", fmt.Sprintf("surface = %d\n", stamp))
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-m", "fixture")
	if withRemote {
		gitRun(t, root, "push", "-q", "origin", "main")
	}
	return root, bare
}

// setRetireAfterDeleteHook injects a failure point into the apply for one test.
func setRetireAfterDeleteHook(t *testing.T, f func(n int) error) {
	t.Helper()
	prev := retireAfterDelete
	retireAfterDelete = f
	t.Cleanup(func() { retireAfterDelete = prev })
}

// runRetirement runs the command body against root and returns its exit code,
// stdout and stderr.
func runRetirement(t *testing.T, root string, apply bool) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runProjectConfigRetirement(root, apply, &out, &errb)
	return code, out.String(), errb.String()
}

func commitCount(t *testing.T, root string) int {
	t.Helper()
	var n int
	fmt.Sscan(gitRun(t, root, "rev-list", "--count", "HEAD"), &n)
	return n
}

func exists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// requireRefusedUntouched asserts an apply refused (ExitUser) naming want, and
// left both configs and HEAD exactly as they were.
func requireRefusedUntouched(t *testing.T, root, head string, code int, stderr, want string) {
	t.Helper()
	if code != cli.ExitUser {
		t.Errorf("exit = %d, want %d (refusal)\nstderr:%s", code, cli.ExitUser, stderr)
	}
	if !strings.Contains(stderr, want) {
		t.Errorf("refusal does not say %q; stderr:\n%s", want, stderr)
	}
	for _, rel := range []string{alphaRel, betaRel} {
		if !exists(root, rel) {
			t.Errorf("%s was deleted by a refused run", rel)
		}
	}
	if got := gitHead(t, root); got != head {
		t.Errorf("a refused run committed: HEAD %s -> %s", head, got)
	}
}

// TestProjectConfigRetirementPlannerWritesNothing is D3: the whole tree (.git
// included), the porcelain and HEAD are identical across a planning call.
func TestProjectConfigRetirementPlannerWritesNothing(t *testing.T) {
	root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
	mkfile(t, root, "Projects/gamma/config.toml", "[search]\nk = 1\n") // untracked too
	// git status may refresh the index, so it runs before the digest is taken
	// and after it is compared: the digest covers .git too.
	porcelain, head := gitPorcelain(t, root), gitHead(t, root)
	tree := treeDigest(t, root)

	plan, err := planProjectConfigRetirement(root)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Configs) != 3 {
		t.Fatalf("planned %d configs, want 3 — the fixture measures nothing", len(plan.Configs))
	}
	if got := treeDigest(t, root); got != tree {
		t.Error("the planner changed a byte of the tree (.git included)")
	}
	if got := gitPorcelain(t, root); got != porcelain {
		t.Errorf("the planner changed git status:\nbefore:\n%s\nafter:\n%s", porcelain, got)
	}
	if got := gitHead(t, root); got != head {
		t.Errorf("the planner moved HEAD %s -> %s", head, got)
	}
}

// TestProjectConfigRetirementApplyReplans is D4: an override committed between
// the dry run and the apply is in the apply's transcript. The apply re-plans.
func TestProjectConfigRetirementApplyReplans(t *testing.T) {
	root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
	if code, out, errs := runRetirement(t, root, false); code != cli.ExitOK || !strings.Contains(out, "min_score = 0.4") {
		t.Fatalf("dry run: exit %d, stdout:\n%s\nstderr:%s", code, out, errs)
	}
	mkfile(t, root, alphaRel, strings.Replace(alphaConfig, "min_score = 0.4", "min_score = 0.55", 1))
	gitRun(t, root, "commit", "-qam", "operator override between dry run and apply")

	code, out, errs := runRetirement(t, root, true)
	if code != cli.ExitOK {
		t.Fatalf("apply: exit %d\nstdout:%s\nstderr:%s", code, out, errs)
	}
	if !strings.Contains(out, "min_score = 0.55") || strings.Contains(out, "min_score = 0.4") {
		t.Errorf("the apply's transcript does not carry the override committed after the dry run:\n%s", out)
	}
}

// TestProjectConfigRetirementTranscript: the report carries each file's scoring
// through the host writer's renderer, names dropped sections, and says so when
// a file has no scoring at all.
func TestProjectConfigRetirementTranscript(t *testing.T) {
	root, _ := retirementVault(t, false, 0)
	code, out, errs := runRetirement(t, root, false)
	if code != cli.ExitOK {
		t.Fatalf("dry run: exit %d\nstderr:%s", code, errs)
	}
	want, _, err := storage.RenderRetiredScoring([]byte(alphaConfig))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(want, "\n"), "\n") {
		if !strings.Contains(out, "    "+line+"\n") {
			t.Errorf("transcript lacks the rendered line %q:\n%s", line, out)
		}
	}
	for _, s := range []string{"dropped, no per-project tier: search", "palace.scoring: none", "dropped, no per-project tier: summarization", "dry run: nothing was deleted"} {
		if !strings.Contains(out, s) {
			t.Errorf("transcript lacks %q:\n%s", s, out)
		}
	}
	if !exists(root, alphaRel) || !exists(root, betaRel) {
		t.Error("the dry run deleted a config")
	}
}

// TestProjectConfigRetirementE1WorkingTreeStampRefuses: a surface-7 stamp that
// exists only in the working tree is not a floor any other host has seen.
func TestProjectConfigRetirementE1WorkingTreeStampRefuses(t *testing.T) {
	root, _ := retirementVault(t, false, surface.MCPSurfaceVersion-1)
	mkfile(t, root, "palace/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion))
	head := gitHead(t, root)
	code, _, errs := runRetirement(t, root, true)
	requireRefusedUntouched(t, root, head, code, errs, "committed at HEAD")
}

// TestProjectConfigRetirementE2RemoteMissingStampRefuses: the stamp is
// committed locally but the remote's tip does not carry it.
func TestProjectConfigRetirementE2RemoteMissingStampRefuses(t *testing.T) {
	root, _ := retirementVault(t, true, surface.MCPSurfaceVersion-1)
	mkfile(t, root, "palace/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion))
	gitRun(t, root, "commit", "-qam", "stamp, not pushed")
	head := gitHead(t, root)
	code, _, errs := runRetirement(t, root, true)
	requireRefusedUntouched(t, root, head, code, errs, "committed at refs/remotes/origin/main")
}

// TestProjectConfigRetirementE3DivergedRemoteRefuses: the remote carries the
// stamp but has a commit HEAD does not.
func TestProjectConfigRetirementE3DivergedRemoteRefuses(t *testing.T) {
	root, bare := retirementVault(t, true, surface.MCPSurfaceVersion)
	other := t.TempDir()
	gitRun(t, other, "clone", "-q", bare, ".")
	gitRun(t, other, "config", "user.email", "o@test.com")
	gitRun(t, other, "config", "user.name", "Other")
	mkfile(t, other, "elsewhere.txt", "another host\n")
	gitRun(t, other, "add", "elsewhere.txt")
	gitRun(t, other, "commit", "-qm", "another host's commit")
	gitRun(t, other, "push", "-q", "origin", "main")

	head := gitHead(t, root)
	code, _, errs := runRetirement(t, root, true)
	requireRefusedUntouched(t, root, head, code, errs, "is not an ancestor of HEAD")
}

// TestProjectConfigRetirementE4RemotelessUsesHEAD: with no remote the floor is
// read from HEAD — refused below it, retired at it.
func TestProjectConfigRetirementE4RemotelessUsesHEAD(t *testing.T) {
	t.Run("below the floor refuses", func(t *testing.T) {
		root, _ := retirementVault(t, false, surface.MCPSurfaceVersion-1)
		head := gitHead(t, root)
		code, _, errs := runRetirement(t, root, true)
		requireRefusedUntouched(t, root, head, code, errs, "committed at HEAD")
	})
	t.Run("at the floor retires", func(t *testing.T) {
		root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
		if code, out, errs := runRetirement(t, root, true); code != cli.ExitOK {
			t.Fatalf("exit %d\nstdout:%s\nstderr:%s", code, out, errs)
		}
		if exists(root, alphaRel) || exists(root, betaRel) {
			t.Error("configs survived a successful apply")
		}
	})
}

// TestProjectConfigRetirementE5UnreachableRemoteRefuses: a remote that cannot
// be fetched refuses and is named; it is never read as "no remote".
func TestProjectConfigRetirementE5UnreachableRemoteRefuses(t *testing.T) {
	root, _ := retirementVault(t, true, surface.MCPSurfaceVersion)
	gitRun(t, root, "remote", "add", "offline", filepath.Join(t.TempDir(), "gone.git"))
	head := gitHead(t, root)
	code, _, errs := runRetirement(t, root, true)
	requireRefusedUntouched(t, root, head, code, errs, "fetch offline")
}

// TestProjectConfigRetirementE6InProgressAndDirtRefuse: a merge, cherry-pick,
// revert or rebase in progress, or dirt under Projects/, each refuse.
//
// The in-progress precondition is the named gate. With it removed, git itself
// still refuses the partial commit during a merge or cherry-pick ("cannot do a
// partial commit during a merge"), so those legs then go red on the exit code
// and the missing refusal message rather than on a deletion. That refusal is a
// second guard, not a substitute for this one: it fires only at commit time,
// after the deletions, and says nothing about revert or rebase.
func TestProjectConfigRetirementE6InProgressAndDirtRefuse(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		arrange    func(t *testing.T, root string)
	}{
		{"merge", "MERGE_HEAD is in progress", func(t *testing.T, root string) {
			mkfile(t, root, ".git/MERGE_HEAD", gitHead(t, root)+"\n")
		}},
		{"cherry-pick", "CHERRY_PICK_HEAD is in progress", func(t *testing.T, root string) {
			mkfile(t, root, ".git/CHERRY_PICK_HEAD", gitHead(t, root)+"\n")
		}},
		{"revert", "REVERT_HEAD is in progress", func(t *testing.T, root string) {
			mkfile(t, root, ".git/REVERT_HEAD", gitHead(t, root)+"\n")
		}},
		{"rebase", "a rebase is in progress", func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, ".git", "rebase-merge"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"dirt", "Projects/ has uncommitted changes", func(t *testing.T, root string) {
			mkfile(t, root, "Projects/alpha/commands/README.md", "edited\n")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
			tc.arrange(t, root)
			head := gitHead(t, root)
			code, _, errs := runRetirement(t, root, true)
			requireRefusedUntouched(t, root, head, code, errs, tc.want)
		})
	}
}

// TestProjectConfigRetirementDetachedHEADRefuses: the branch precondition
// refuses rather than guessing "main".
func TestProjectConfigRetirementDetachedHEADRefuses(t *testing.T) {
	root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
	gitRun(t, root, "checkout", "-q", "--detach")
	head := gitHead(t, root)
	code, _, errs := runRetirement(t, root, true)
	requireRefusedUntouched(t, root, head, code, errs, "not on a named branch")
}

// TestProjectConfigRetirementE7OneCommitAtomicAndIdempotent is E7:
//   - a successful apply is EXACTLY ONE commit, carrying exactly the configs;
//   - a failure after the second deletion leaves zero deletions and a clean
//     porcelain;
//   - a re-run finds nothing and changes nothing.
func TestProjectConfigRetirementE7OneCommitAtomicAndIdempotent(t *testing.T) {
	t.Run("one commit", func(t *testing.T) {
		root, bare := retirementVault(t, true, surface.MCPSurfaceVersion)
		before, origin := commitCount(t, root), bareMain(t, bare)
		if code, out, errs := runRetirement(t, root, true); code != cli.ExitOK {
			t.Fatalf("exit %d\nstdout:%s\nstderr:%s", code, out, errs)
		}
		if got := commitCount(t, root); got != before+1 {
			t.Errorf("apply made %d commits, want exactly 1", got-before)
		}
		changed := strings.Fields(gitRun(t, root, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"))
		sort.Strings(changed)
		if want := []string{alphaRel, betaRel}; strings.Join(changed, ",") != strings.Join(want, ",") {
			t.Errorf("the commit carries %v, want exactly %v", changed, want)
		}
		if p := gitPorcelain(t, root); p != "" {
			t.Errorf("apply left dirt:\n%s", p)
		}
		if got := bareMain(t, bare); got != origin {
			t.Error("apply pushed; it must leave publishing to vp vault sync")
		}
	})
	t.Run("failure after the second delete restores everything", func(t *testing.T) {
		root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
		mkfile(t, root, "Projects/gamma/config.toml", "[search]\nk = 1\n")
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-qm", "third config")
		head := gitHead(t, root)
		setRetireAfterDeleteHook(t, func(n int) error {
			if n == 2 {
				return fmt.Errorf("injected failure after delete %d", n)
			}
			return nil
		})
		code, _, errs := runRetirement(t, root, true)
		if code != cli.ExitSystem {
			t.Errorf("exit %d, want %d\nstderr:%s", code, cli.ExitSystem, errs)
		}
		for _, rel := range []string{alphaRel, betaRel, "Projects/gamma/config.toml"} {
			if !exists(root, rel) {
				t.Errorf("%s is still deleted after a failed run", rel)
			}
		}
		if p := gitPorcelain(t, root); p != "" {
			t.Errorf("a failed run left dirt:\n%s", p)
		}
		if got := gitHead(t, root); got != head {
			t.Errorf("a failed run committed: HEAD %s -> %s", head, got)
		}
	})
	t.Run("re-run is a no-op", func(t *testing.T) {
		root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
		if code, _, errs := runRetirement(t, root, true); code != cli.ExitOK {
			t.Fatalf("first apply: exit %d\nstderr:%s", code, errs)
		}
		head := gitHead(t, root)
		code, out, errs := runRetirement(t, root, true)
		if code != cli.ExitOK {
			t.Errorf("re-run: exit %d\nstderr:%s", code, errs)
		}
		if !strings.Contains(out, "untracked survivors: none (0 on disk, 0 tracked)") || !strings.Contains(out, "nothing to retire") {
			t.Errorf("re-run did not report an empty vault:\n%s", out)
		}
		if got := gitHead(t, root); got != head {
			t.Errorf("re-run committed: HEAD %s -> %s", head, got)
		}
	})
}

// TestProjectConfigRetirementE7cUntrackedRefuses is E7c(i), as amended by the
// Chair's P6 ruling: one untracked config makes the counts differ and is NAMED,
// the apply refuses telling the operator how to resolve it, and nothing is
// deleted. A stubbed derivation cannot name the file; a constant cannot make
// the counts differ.
func TestProjectConfigRetirementE7cUntrackedRefuses(t *testing.T) {
	root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
	untracked := "Projects/gamma/config.toml"
	mkfile(t, root, untracked, "[search]\nk = 1\n")
	head := gitHead(t, root)

	code, out, errs := runRetirement(t, root, true)
	if want := "untracked survivors: " + untracked + " (3 on disk, 2 tracked)"; !strings.Contains(out, want) {
		t.Errorf("census line does not NAME the untracked file with derived counts; want %q in:\n%s", want, out)
	}
	requireRefusedUntouched(t, root, head, code, errs, "vp vault delete "+untracked)
	if !exists(root, untracked) {
		t.Error("the untracked config was deleted")
	}
}

// TestProjectConfigRetirementE7cNoneIsStillPrinted is E7c(ii): on a fully
// tracked vault the line is present, with equal derived counts.
func TestProjectConfigRetirementE7cNoneIsStillPrinted(t *testing.T) {
	for _, apply := range []bool{false, true} {
		root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
		_, out, _ := runRetirement(t, root, apply)
		if !strings.Contains(out, "untracked survivors: none (2 on disk, 2 tracked)") {
			t.Errorf("apply=%v: census line missing on a fully tracked vault:\n%s", apply, out)
		}
	}
}

// TestProjectConfigRetirementE7cLinePrecedesEverything is E7c(iii): the line
// is printed before any deletion on a successful run, and before the refusal
// on a run that refuses at a later precondition.
func TestProjectConfigRetirementE7cLinePrecedesEverything(t *testing.T) {
	t.Run("before the deletions", func(t *testing.T) {
		root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
		code, out, errs := runRetirement(t, root, true)
		if code != cli.ExitOK {
			t.Fatalf("exit %d\nstderr:%s", code, errs)
		}
		census, del := strings.Index(out, "untracked survivors:"), strings.Index(out, "deleted ")
		if census < 0 || del < 0 || census > del {
			t.Errorf("census line (at %d) does not precede the first deletion (at %d):\n%s", census, del, out)
		}
	})
	t.Run("on a refusing run", func(t *testing.T) {
		root, _ := retirementVault(t, false, surface.MCPSurfaceVersion-1)
		code, out, _ := runRetirement(t, root, true)
		if code != cli.ExitUser {
			t.Fatalf("exit %d, want the stamp refusal", code)
		}
		if !strings.Contains(out, "untracked survivors: none (2 on disk, 2 tracked)") {
			t.Errorf("a run refused at a later precondition did not print the census:\n%s", out)
		}
	})
}

// TestProjectConfigRetirementRefusesUnlessOwnRepo (P7): --apply on a vault
// nested inside another repository refuses by name. The census works there —
// git answers from the enclosing repository — so only the positive guard
// stands between the apply and a commit into a repository that is not the
// vault's.
func TestProjectConfigRetirementRefusesUnlessOwnRepo(t *testing.T) {
	setupTestVaultEnv(t)
	outer := t.TempDir()
	gitRun(t, outer, "init", "-b", "main")
	gitRun(t, outer, "config", "user.email", "test@test.com")
	gitRun(t, outer, "config", "user.name", "Test")
	root := filepath.Join(outer, "vault")
	mkfile(t, root, alphaRel, alphaConfig)
	mkfile(t, root, betaRel, betaConfig)
	mkfile(t, root, "palace/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion))
	gitRun(t, outer, "add", "-A")
	gitRun(t, outer, "commit", "-qm", "enclosing repository")
	head := gitHead(t, outer)

	code, out, errs := runRetirement(t, root, true)
	if !strings.Contains(out, "untracked survivors: none (2 on disk, 2 tracked)") {
		t.Fatalf("the census did not run against the nested vault, so this measures nothing:\n%s\n%s", out, errs)
	}
	requireRefusedUntouched(t, root, gitHead(t, root), code, errs, "inside another repository")
	if got := gitHead(t, outer); got != head {
		t.Errorf("the enclosing repository was committed into: %s -> %s", head, got)
	}
}

// TestProjectConfigRetirementGatesTheResolvedRoot (R7): the root the command
// resolved is surface-gated unconditionally — even a dry run on a vault a
// newer binary stamped fail-stops, although the configured vault is fine.
func TestProjectConfigRetirementGatesTheResolvedRoot(t *testing.T) {
	t.Setenv("VP_SURFACE_GATE", "")
	root, _ := retirementVault(t, false, surface.MCPSurfaceVersion+1)
	_, stderr, code := runVaultCmdCapturingBoth(t, cmdMigrateProjectConfigs(), "--vault", root)
	if code != cli.ExitSystem {
		t.Errorf("exit %d, want %d (surface fail-stop)\nstderr:%s", code, cli.ExitSystem, stderr)
	}
	if !strings.Contains(stderr, "surface") {
		t.Errorf("refusal is not the surface gate's; stderr:\n%s", stderr)
	}
}

// twoRemoteVault is retirementVault with a second remote, "zeta", which sorts
// after "origin" so a loop that stops early never reaches it. Both remotes
// receive the surface-(v-1) fixture commit; then a surface-v stamp is committed
// and pushed to origin ONLY, so origin carries the floor and zeta does not.
func twoRemoteVault(t *testing.T) (root, zeta string) {
	t.Helper()
	root, _ = retirementVault(t, true, surface.MCPSurfaceVersion-1)
	zeta = t.TempDir()
	gitRun(t, zeta, "init", "-q", "--bare", "-b", "main")
	gitRun(t, root, "remote", "add", "zeta", zeta)
	gitRun(t, root, "push", "-q", "zeta", "main")
	mkfile(t, root, "palace/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion))
	gitRun(t, root, "commit", "-qam", "floor stamp")
	gitRun(t, root, "push", "-q", "origin", "main")
	return root, zeta
}

// TestProjectConfigRetirementEveryRemoteIsChecked (RC1): the floor and the
// ancestry are required at EVERY remote's tip. origin passes both; zeta fails
// one, and the refusal must name zeta.
func TestProjectConfigRetirementEveryRemoteIsChecked(t *testing.T) {
	t.Run("second remote lacks the stamp", func(t *testing.T) {
		root, _ := twoRemoteVault(t)
		head := gitHead(t, root)
		code, _, errs := runRetirement(t, root, true)
		requireRefusedUntouched(t, root, head, code, errs, "committed at refs/remotes/zeta/main")
	})
	t.Run("second remote carries the stamp but has diverged", func(t *testing.T) {
		root, zeta := twoRemoteVault(t)
		gitRun(t, root, "push", "-q", "zeta", "main")
		other := t.TempDir()
		gitRun(t, other, "clone", "-q", zeta, ".")
		gitRun(t, other, "config", "user.email", "o@test.com")
		gitRun(t, other, "config", "user.name", "Other")
		mkfile(t, other, "elsewhere.txt", "another host\n")
		gitRun(t, other, "add", "elsewhere.txt")
		gitRun(t, other, "commit", "-qm", "another host's commit")
		gitRun(t, other, "push", "-q", "origin", "main")
		head := gitHead(t, root)
		code, _, errs := runRetirement(t, root, true)
		requireRefusedUntouched(t, root, head, code, errs, "zeta/main is not an ancestor of HEAD")
	})
}

// TestProjectConfigRetirementRefusesAConfigItCannotRead (RC2): a tracked config
// that does not decode, or is not a regular file, is reported CANNOT RETIRE and
// refuses the whole apply — it is never deleted along with the rest.
func TestProjectConfigRetirementRefusesAConfigItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name, rel string
		arrange   func(t *testing.T, root, rel string)
	}{
		{"malformed TOML", "Projects/gamma/config.toml", func(t *testing.T, root, rel string) {
			mkfile(t, root, rel, "[palace.scoring\nmin_score = 0.4\n")
		}},
		{"symlink", "Projects/delta/config.toml", func(t *testing.T, root, rel string) {
			abs := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../alpha/commands/README.md", abs); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := retirementVault(t, false, surface.MCPSurfaceVersion)
			tc.arrange(t, root, tc.rel)
			gitRun(t, root, "add", "-A")
			gitRun(t, root, "commit", "-qm", "unreadable config")
			head := gitHead(t, root)

			code, out, errs := runRetirement(t, root, true)
			if !strings.Contains(out, tc.rel+"\n  CANNOT RETIRE:") {
				t.Errorf("transcript does not mark %s CANNOT RETIRE:\n%s", tc.rel, out)
			}
			requireRefusedUntouched(t, root, head, code, errs, tc.rel+" cannot be retired as found")
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(tc.rel))); err != nil {
				t.Errorf("%s was deleted: %v", tc.rel, err)
			}
		})
	}
}

// TestProjectConfigRetirementMalformedStampRefuses (RC3): maxSurfaceAt's
// promise — an unreadable floor is not a low one. A tip carrying a valid
// surface-v stamp AND a malformed one refuses; skipping the malformed stamp
// would let the valid one satisfy the floor.
func TestProjectConfigRetirementMalformedStampRefuses(t *testing.T) {
	root, _ := retirementVault(t, true, surface.MCPSurfaceVersion)
	mkfile(t, root, "Templates/.surface", "surface = \n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-qm", "malformed stamp")
	gitRun(t, root, "push", "-q", "origin", "main")
	head := gitHead(t, root)
	code, _, errs := runRetirement(t, root, true)
	requireRefusedUntouched(t, root, head, code, errs, "Templates/.surface at refs/remotes/origin/main is malformed")
}
