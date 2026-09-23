// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// Two clones of one bare remote. B (the returned dir) is the STALE host: it
// holds work under Projects/old/. A is a throwaway clone that makes old depart
// and pushes. Both start with Projects/old/, palace/old/ and Projects/keep/.

func departureFixture(t *testing.T) (b, bare string) {
	t.Helper()
	b, bare = syncSeedRemote(t)
	writeFile(t, b, "Projects/old/resume.md", "old\n")
	writeFile(t, b, "palace/old/drawers/old/general/drawers.jsonl", "{\"id\":\"d1\"}\n")
	writeFile(t, b, "Projects/keep/resume.md", "keep\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-m", "seed projects")
	gitRun(t, b, "push", "origin", "main")
	return b, bare
}

// departOnRemote makes "old" leave, from a second clone, and pushes it.
// kind "" removes the trees with no record (the pre-record shape).
func departOnRemote(t *testing.T, bare string, kind departure.Kind, to string, extra func(clone string)) {
	t.Helper()
	advanceRemoteMulti(t, bare, func(clone string) {
		switch kind {
		case departure.Renamed:
			gitRun(t, clone, "mv", "Projects/old", "Projects/"+to)
			gitRun(t, clone, "mv", "palace/old", "palace/"+to)
		default:
			gitRun(t, clone, "rm", "-q", "-r", "Projects/old", "palace/old")
		}
		if kind != "" {
			b, err := (departure.Record{Slug: "old", Kind: kind, To: to, Date: "2026-09-23"}).Encode()
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, clone, departure.RelPath("old"), string(b))
		}
		if extra != nil {
			extra(clone)
		}
	})
}

type repoState struct {
	head, status string
	refs         map[string]string
}

func snapshotRepo(t *testing.T, dir string) repoState {
	t.Helper()
	refs := map[string]string{}
	for l := range strings.SplitSeq(gitRun(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes"), "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			refs[f[0]] = f[1]
		}
	}
	return repoState{
		head:   gitRun(t, dir, "rev-parse", "HEAD"),
		status: gitRun(t, dir, "status", "--porcelain=v1", "-uall"),
		refs:   refs,
	}
}

func asDeparted(t *testing.T, err error) *DepartedWorkError {
	t.Helper()
	var d *DepartedWorkError
	if !errors.As(err, &d) {
		t.Fatalf("want a *DepartedWorkError, got %T: %v", err, err)
	}
	return d
}

// T1. The core case, over a real merge: an unpushed commit under Projects/old/
// and an incoming rename. Nothing merges; HEAD, the working tree and every
// remote-tracking ref except the one just fetched are exactly as they were,
// and the remedy names the real vault root.
func TestPullRefusesAnUnpushedCommitUnderAnIncomingRename(t *testing.T) {
	b, bare := departureFixture(t)
	// A second remote that has ALSO advanced: fetching it would move its
	// tracking ref, so after a refusal that skips it, it must not have moved.
	mirror := initBareRemote(t)
	gitRun(t, b, "remote", "add", "mirror", mirror)
	gitRun(t, b, "push", "mirror", "main")
	gitRun(t, b, "fetch", "mirror")
	advanceRemote(t, mirror, "Projects/keep/mirror.md", "mirror advance\n")

	departOnRemote(t, bare, departure.Renamed, "new", nil)
	writeFile(t, b, "Projects/old/x.md", "work on the old slug\n")
	gitRun(t, b, "add", "Projects/old/x.md")
	gitRun(t, b, "commit", "-m", "stale host work")
	before := snapshotRepo(t, b)

	res, err := Pull(b, []string{"origin", "mirror"})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	d := asDeparted(t, res.RemoteResults["origin"])
	msg := d.Error()
	for _, want := range []string{"Projects/old/x.md", "(unpushed commit)", `renamed to "new"`,
		"git -C " + b + " branch hold/departed-old-", "git -C " + b + " reset --hard origin/main", "under Projects/new/"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal must contain %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "<vault>") {
		t.Errorf("the refusal must name the real vault root, not a placeholder:\n%s", msg)
	}
	if res.RemoteResults["mirror"] == nil || !strings.Contains(res.RemoteResults["mirror"].Error(), "skipped") {
		t.Errorf("later remotes must be skipped, got %v", res.RemoteResults["mirror"])
	}

	after := snapshotRepo(t, b)
	if after.head != before.head {
		t.Errorf("HEAD moved %s -> %s", before.head, after.head)
	}
	if after.status != before.status {
		t.Errorf("the working tree changed:\nbefore %q\nafter  %q", before.status, after.status)
	}
	if _, err := os.Stat(filepath.Join(b, ".git", "MERGE_HEAD")); err == nil {
		t.Error("a merge is in progress")
	}
	if _, err := os.Stat(filepath.Join(b, "Projects", "new")); err == nil {
		t.Error("the rename was merged into the working tree")
	}
	for ref, sha := range before.refs {
		if ref == "refs/remotes/origin/main" {
			continue
		}
		if after.refs[ref] != sha {
			t.Errorf("remote-tracking ref %s moved %s -> %s; only the fetched ref may move", ref, sha, after.refs[ref])
		}
	}
	if after.refs["refs/remotes/origin/main"] != gitRun(t, bare, "rev-parse", "main") {
		t.Error("test premise: origin/main should be the fetched remote tip")
	}
}

// T2. Uncommitted, untracked work under the departed slug is protected too.
func TestPullRefusesDirtUnderAnIncomingDeparture(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.Renamed, "new", nil)
	writeFile(t, b, "Projects/old/memory/n.md", "uncommitted memory\n")
	before := snapshotRepo(t, b)

	res, _ := Pull(b, []string{"origin"})
	d := asDeparted(t, res.RemoteResults["origin"])
	if !strings.Contains(d.Error(), "Projects/old/memory/n.md   (uncommitted)") {
		t.Errorf("the uncommitted file must be named as such:\n%s", d.Error())
	}
	if after := snapshotRepo(t, b); after.head != before.head || after.status != before.status {
		t.Errorf("HEAD or the working tree changed: %+v -> %+v", before, after)
	}
}

// T3. The refusal lands BEFORE the heal pass: a phantom-dirty template the
// heal would otherwise discard stays dirty.
func TestPullRefusesBeforeTheHealPass(t *testing.T) {
	b, bare := departureFixture(t)
	writeFile(t, b, phantomTemplate, "A\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-m", "seed template")
	gitRun(t, b, "push", "origin", "main")
	departOnRemote(t, bare, departure.Renamed, "new", func(clone string) {
		writeFile(t, clone, phantomTemplate, "B\n")
	})
	writeFile(t, b, phantomTemplate, "B\n") // the remote's bytes, uncommitted: heal-eligible
	writeFile(t, b, "Projects/old/x.md", "work\n")
	gitRun(t, b, "add", "Projects/old/x.md")
	gitRun(t, b, "commit", "-m", "stale host work")

	res, _ := Pull(b, []string{"origin"})
	asDeparted(t, res.RemoteResults["origin"])
	if len(res.HealedTemplates) != 0 {
		t.Errorf("the heal pass ran before the refusal: %v", res.HealedTemplates)
	}
	if !strings.Contains(gitRun(t, b, "status", "--porcelain"), phantomTemplate) {
		t.Error("the heal-eligible template was discarded; the refusal must precede the heal pass")
	}
}

// T4 (guard). Local work that is NOT under the departed slug pulls normally.
func TestPullWithUnrelatedLocalWorkMergesNormally(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.Renamed, "new", nil)
	writeFile(t, b, "Projects/keep/work.md", "unrelated\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-m", "unrelated work")

	res, err := Pull(b, []string{"origin"})
	if err != nil || res.RemoteResults["origin"] != nil {
		t.Fatalf("an unrelated pull must succeed: %v / %v", err, res.RemoteResults["origin"])
	}
	if _, err := os.Stat(filepath.Join(b, "Projects", "new", "resume.md")); err != nil {
		t.Errorf("the rename must have merged: %v", err)
	}
}

// T5 (guard). A clean host that is only behind takes the rename.
func TestPullOfARenameWithNoLocalWorkFastForwards(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.Renamed, "new", nil)
	res, err := Pull(b, []string{"origin"})
	if err != nil || res.RemoteResults["origin"] != nil {
		t.Fatalf("a clean pull must succeed: %v / %v", err, res.RemoteResults["origin"])
	}
	if gitRun(t, b, "rev-parse", "HEAD") != gitRun(t, bare, "rev-parse", "main") {
		t.Error("the clean host did not fast-forward to the remote tip")
	}
}

// T6. Sync refuses BEFORE its own tidy commit, so HEAD does not move even
// though the dirt is a sweepable capture artifact.
func TestSyncRefusesBeforeItsTidyCommit(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.Renamed, "new", nil)
	writeFile(t, b, "Projects/old/sessions/2026-09-23-abc-01.md", "a capture on the stale host\n")
	before := snapshotRepo(t, b)

	res, err := SyncVault(b, []string{"origin"})
	if err == nil || !res.Refused {
		t.Fatalf("sync must refuse, got refused=%v err=%v", res.Refused, err)
	}
	asDeparted(t, err)
	if res.Committed {
		t.Error("sync committed before refusing")
	}
	if after := snapshotRepo(t, b); after.head != before.head || after.status != before.status {
		t.Errorf("HEAD or the working tree changed: %+v -> %+v", before, after)
	}
}

// T7. The commit-then-push path (tidy, harvest, commit --push): the new commit
// stays local, reconcileIfAhead does NOT rebase it onto the departure, and
// nothing is pushed.
func TestCommitAndPushRefusesToRebaseIntoADeparture(t *testing.T) {
	b, bare := departureFixture(t)
	writeFile(t, b, "Projects/old/x.md", "earlier unpushed work\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-m", "earlier stale work")
	earlier := gitRun(t, b, "rev-parse", "HEAD")
	departOnRemote(t, bare, departure.Renamed, "new", nil)
	remoteTip := gitRun(t, bare, "rev-parse", "main")

	writeFile(t, b, "Projects/keep/z.md", "a new unrelated commit\n")
	res, err := CommitAndPushPaths(b, "unrelated", []string{"Projects/keep/z.md"}, true)
	if err != nil {
		t.Fatalf("CommitAndPushPaths: %v", err)
	}
	asDeparted(t, res.RemoteResults["origin"])
	if got := gitRun(t, b, "rev-parse", "HEAD^"); got != earlier {
		t.Errorf("the new commit was rebased: HEAD^ = %s, want the earlier local commit %s", got, earlier)
	}
	if got := gitRun(t, bare, "rev-parse", "main"); got != remoteTip {
		t.Errorf("something was pushed: bare main %s -> %s", remoteTip, got)
	}
}

// T8. pushCommitted's own rebase, reached on a rejected push with a stale
// tracking ref and no reconcile, refuses the same way.
func TestRejectedPushDoesNotRebaseIntoADeparture(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.Renamed, "new", nil)
	writeFile(t, b, "Projects/old/x.md", "stale work\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-m", "stale work")
	head := gitRun(t, b, "rev-parse", "HEAD")
	remoteTip := gitRun(t, bare, "rev-parse", "main")

	res := &PushResult{RemoteResults: map[string]error{}}
	pushCommitted(b, []string{"origin"}, "main", nil, res)
	asDeparted(t, res.RemoteResults["origin"])
	if got := gitRun(t, b, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD was rebased %s -> %s", head, got)
	}
	if got := gitRun(t, bare, "rev-parse", "main"); got != remoteTip {
		t.Errorf("something was pushed: %s -> %s", remoteTip, got)
	}
}

// T9. A split purge: the work belongs in the destination vault, and the
// remedy says so instead of suggesting a rename.
func TestMovedToVaultRemedySaysTheWorkBelongsInTheDestination(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.MovedToVault, "git@example.com:me/quantum-vault.git", nil)
	writeFile(t, b, "Projects/old/x.md", "work\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-m", "stale work")

	res, _ := Pull(b, []string{"origin"})
	msg := asDeparted(t, res.RemoteResults["origin"]).Error()
	if !strings.Contains(msg, `moved to another vault, "git@example.com:me/quantum-vault.git"`) ||
		!strings.Contains(msg, "belongs in the vault") {
		t.Errorf("the moved-to-vault remedy is wrong:\n%s", msg)
	}
	if strings.Contains(msg, "rewrite the paths") {
		t.Errorf("a vault move must not be described as a rename:\n%s", msg)
	}
}

// T10. A departure with NO record — the shape of the live quantum-ng rename,
// and of any older binary — is still refused, from the removed trees.
func TestARecordlessRemovalIsStillRefused(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, "", "", nil)
	writeFile(t, b, "palace/old/drawers/old/general/extra.jsonl", "{}\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-m", "stale store work")

	res, _ := Pull(b, []string{"origin"})
	msg := asDeparted(t, res.RemoteResults["origin"]).Error()
	if !strings.Contains(msg, "where it went is not recorded") || !strings.Contains(msg, "palace/old/drawers/old/general/extra.jsonl") {
		t.Errorf("the recordless refusal is wrong:\n%s", msg)
	}
}

// T11. One Warn per refusal, under a stable category: it is what bootstrap
// health counts, and the only signal the unattended SessionEnd harvest leaves.
func TestPullRefusalWarnsWithAStableCategory(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.Renamed, "new", nil)
	writeFile(t, b, "Projects/old/x.md", "work\n")
	gitRun(t, b, "add", "-A")
	gitRun(t, b, "commit", "-m", "stale work")

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if _, err := Pull(b, []string{"origin"}); err != nil {
		t.Fatal(err)
	}
	log := buf.String()
	if strings.Count(log, `msg="vault departed work:`) != 1 || !strings.Contains(log, "slugs=old") || !strings.Contains(log, "remote=origin") {
		t.Errorf("want exactly one 'vault departed work:' Warn naming slug and remote, got:\n%s", log)
	}
}

// T12 (convention pin). Every function in non-test Go that passes "merge" or
// "rebase" as an argument (a git merge or rebase of incoming commits) must
// call guardIncomingDepartures. The subtest proves the pin fires.
func TestEveryVaultMergeOrRebaseCallsTheDepartureGuard(t *testing.T) {
	unguarded := func(fset *token.FileSet, f *ast.File) []string {
		var bad []string
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			mergesOrRebases, guarded := false, false
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "guardIncomingDepartures" {
					guarded = true
				}
				for _, a := range call.Args {
					if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if v, err := strconv.Unquote(lit.Value); err == nil && (v == "merge" || v == "rebase") {
							mergesOrRebases = true
						}
					}
				}
				return true
			})
			if mergesOrRebases && !guarded {
				bad = append(bad, fset.Position(fd.Pos()).String()+" "+fd.Name.Name)
			}
		}
		return bad
	}

	var bad []string
	var sites int
	for _, top := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(top, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			src, _ := os.ReadFile(p)
			if bytes.Contains(src, []byte(`"merge"`)) || bytes.Contains(src, []byte(`"rebase"`)) {
				sites++
			}
			bad = append(bad, unguarded(fset, f)...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if sites == 0 {
		t.Fatal("the scan found no merge or rebase at all, so it proves nothing (did the walk root move?)")
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("these functions merge or rebase incoming vault commits without guardIncomingDepartures: %v", bad)
	}

	t.Run("fires_on_an_unguarded_rebase", func(t *testing.T) {
		src := "package x\nfunc f() { gitCmd(\"\", 0, \"rebase\", \"--autostash\", \"origin/main\") }\n"
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x.go", src, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := unguarded(fset, f); len(got) != 1 {
			t.Errorf("the pin must fire on an unguarded rebase, got %v", got)
		}
	})
}
