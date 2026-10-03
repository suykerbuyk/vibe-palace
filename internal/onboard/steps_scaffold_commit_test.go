// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package onboard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

// gitVaultRequest is newRequest over a vault that is a git repository with a
// seed commit (the canonical .gitignore, as a real vault has) and a bare
// remote it has been pushed to.
func gitVaultRequest(t *testing.T) (req Request, vaultDir, bare string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	sandboxHost(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	req, vaultDir = newRequest(t, false)
	gitT(t, vaultDir, "init", "-q", "-b", "main")
	gitT(t, vaultDir, "config", "user.email", "test@example.com")
	gitT(t, vaultDir, "config", "user.name", "Test User")
	gitT(t, vaultDir, "config", "commit.gpgsign", "false")
	if err := storage.ReconcileVaultGitignore(vaultDir); err != nil {
		t.Fatalf("reconcile vault gitignore: %v", err)
	}
	gitT(t, vaultDir, "add", ".gitignore")
	gitT(t, vaultDir, "commit", "-q", "-m", "seed")
	bare = filepath.Join(t.TempDir(), "remote.git")
	gitT(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	gitT(t, vaultDir, "remote", "add", "origin", bare)
	gitT(t, vaultDir, "push", "-q", "origin", "main")
	return req, vaultDir, bare
}

var initPaths = []string{
	"Projects/alpha/.surface",
	"Projects/alpha/commands/README.md",
	"Projects/alpha/skills/README.md",
}

func headCommitFiles(t *testing.T, dir string) []string {
	t.Helper()
	files := strings.Split(gitT(t, dir, "show", "--name-only", "--format=", "HEAD"), "\n")
	slices.Sort(files)
	return files
}

// The incident: vp init left its own three files uncommitted.
func TestProjectScaffold_LeavesNoUncommittedInitPaths(t *testing.T) {
	req, vaultDir, _ := gitVaultRequest(t)
	outs := stepProjectScaffold(context.Background(), req)
	if len(outs) != 1 || outs[0].Status != Pass {
		t.Fatalf("outcomes = %+v, want one Pass row", outs)
	}
	if st := gitT(t, vaultDir, "status", "--porcelain", "--", "Projects/alpha"); st != "" {
		t.Errorf("init left vault paths uncommitted:\n%s", st)
	}
	if got := headCommitFiles(t, vaultDir); !slices.Equal(got, initPaths) {
		t.Errorf("init commit touched %q, want exactly %q", got, initPaths)
	}
	if !strings.Contains(strings.Join(outs[0].Details, "\n"), "committed ") {
		t.Errorf("the row does not report the commit: %+v", outs[0])
	}
}

// The acceptance criterion: a sync after init does not refuse, and pushes it.
func TestProjectScaffold_FollowingSyncDoesNotRefuse(t *testing.T) {
	req, vaultDir, bare := gitVaultRequest(t)
	stepProjectScaffold(context.Background(), req)

	res, err := storage.SyncVault(vaultDir, []string{"origin"})
	if err != nil || res.Refused {
		t.Fatalf("sync after init: refused=%v err=%v genuine dirt=%q", res.Refused, err, res.GenuineDirt)
	}
	if local, remote := gitT(t, vaultDir, "rev-parse", "HEAD"), gitT(t, bare, "rev-parse", "main"); local != remote {
		t.Errorf("remote main %s != local HEAD %s after sync", remote, local)
	}
}

// A scaffold an older binary left uncommitted (the chimera state) is committed
// by the next init even though the reconciler plans nothing.
func TestProjectScaffold_ReinitCommitsAnEarlierUncommittedScaffold(t *testing.T) {
	req, vaultDir, _ := gitVaultRequest(t)
	tt := reconcile.NewTemplateTree(vaultDir, "Projects/alpha", reconcile.TemplateTreeSeed{Mode: reconcile.TemplateModeScaffold})
	plan, err := tt.Plan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tt.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if st := gitT(t, vaultDir, "status", "--porcelain"); st == "" {
		t.Fatal("fixture: the pre-existing scaffold is not dirty")
	}

	outs := stepProjectScaffold(context.Background(), req)
	if outs[0].Status != Info {
		t.Fatalf("re-init row = %+v, want the already-present Info row", outs[0])
	}
	if st := gitT(t, vaultDir, "status", "--porcelain"); st != "" {
		t.Errorf("re-init left the earlier scaffold uncommitted:\n%s", st)
	}
	if got := headCommitFiles(t, vaultDir); !slices.Equal(got, initPaths) {
		t.Errorf("heal commit touched %q, want %q", got, initPaths)
	}
}

// A failed commit is a Fail row naming the remedy, and so a failed SideVault
// step — which is what makes `vp init` exit non-zero.
func TestProjectScaffold_CommitFailureIsAFailRow(t *testing.T) {
	req, _ := newRequest(t, false)
	sandboxHost(t)
	restore := commitScaffold
	t.Cleanup(func() { commitScaffold = restore })
	commitScaffold = func(string, string, storage.ScaffoldCommitOptions) (storage.ScaffoldCommit, error) {
		return storage.ScaffoldCommit{Paths: initPaths}, errors.New("no committer identity")
	}
	swapStepTable(t, func(steps []Step) {
		for i := range steps {
			if steps[i].Name != "project-scaffold" {
				steps[i].Run = func(context.Context, Request) []Outcome { return []Outcome{{Status: Pass}} }
			}
		}
	})

	res, err := Run(context.Background(), req, Scope{SideVault: true, SideWorkingTree: true})
	if err != nil {
		t.Fatal(err)
	}
	var scaffold []Outcome
	for _, oc := range res.Outcomes {
		if oc.Step == "project-scaffold" {
			scaffold = append(scaffold, oc)
		}
	}
	if len(scaffold) != 2 || scaffold[0].Status != Pass || scaffold[1].Status != Fail {
		t.Fatalf("scaffold outcomes = %+v, want the Pass scaffold row then a Fail commit row", scaffold)
	}
	detail := strings.Join(scaffold[1].Details, "\n")
	for _, want := range []string{"vp vault commit --paths", "vp_vault_sync", "Projects/alpha/.surface"} {
		if !strings.Contains(detail, want) {
			t.Errorf("Fail row lacks %q: %s", want, detail)
		}
	}
	if !slices.Contains(res.Failed, "project-scaffold") {
		t.Errorf("Failed = %q, want project-scaffold", res.Failed)
	}
}

// The Chair's ruling: a marker holding an older binary's stub text is not
// committed; it is an Info row naming the path.
func TestProjectScaffold_StaleStubMarkerIsAnInfoRow(t *testing.T) {
	req, vaultDir, _ := gitVaultRequest(t)
	stale := strings.Replace(templates.RenderReadmeStub("skills"), "skill", "persona", 1)
	p := filepath.Join(vaultDir, "Projects", "alpha", "skills", "README.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	outs := stepProjectScaffold(context.Background(), req)
	if len(outs) != 2 || outs[1].Status != Info || !strings.Contains(outs[1].Summary, "Projects/alpha/skills/README.md") {
		t.Fatalf("outcomes = %+v, want a second Info row naming the stale marker", outs)
	}
	if got := headCommitFiles(t, vaultDir); slices.Contains(got, "Projects/alpha/skills/README.md") {
		t.Errorf("the stale marker was committed: %q", got)
	}
	if b, _ := os.ReadFile(p); string(b) != stale {
		t.Error("the stale marker's bytes were changed")
	}
}

// Guard: never git add -A.
func TestProjectScaffold_UnrelatedDirtIsNotCommitted(t *testing.T) {
	req, vaultDir, _ := gitVaultRequest(t)
	if err := os.MkdirAll(filepath.Join(vaultDir, "Projects", "other"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, "Projects", "other", "x.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultDir, ".gitignore"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stepProjectScaffold(context.Background(), req)
	st := gitT(t, vaultDir, "status", "--porcelain", "-uall")
	for _, want := range []string{"?? Projects/other/x.md", "M .gitignore"} {
		if !strings.Contains(st, want) {
			t.Errorf("unrelated dirt %q was committed; status:\n%s", want, st)
		}
	}
}

// Guard: committed or not, a scaffold-only project is still a stray scaffold.
func TestProjectScaffold_StrayScaffoldStillReported(t *testing.T) {
	req, vaultDir, _ := gitVaultRequest(t)
	stepProjectScaffold(context.Background(), req)
	r := check.CheckStrayScaffolds(storage.NewVault(vaultDir))
	if r.Status != check.Info || !strings.Contains(strings.Join(r.Details, "\n"), "alpha") {
		t.Errorf("stray-scaffolds after a committed init = %+v, want alpha reported", r)
	}
}

// Guard: a converged re-init makes no commit.
func TestProjectScaffold_ConvergedReinitMakesNoCommit(t *testing.T) {
	req, vaultDir, _ := gitVaultRequest(t)
	stepProjectScaffold(context.Background(), req)
	head := gitT(t, vaultDir, "rev-parse", "HEAD")
	outs := stepProjectScaffold(context.Background(), req)
	if gitT(t, vaultDir, "rev-parse", "HEAD") != head {
		t.Error("a converged re-init made a commit")
	}
	if len(outs) != 1 || len(outs[0].Details) != 0 {
		t.Errorf("converged re-init outcomes = %+v, want the bare Info row", outs)
	}
}

// git_enabled = false is the operator's setting, not a fault: the scaffold row
// carries a "not committed" Detail and no Fail row is added, so `vp init` does
// not exit non-zero over it.
func TestProjectScaffold_GitDisabledIsADetailNotAFail(t *testing.T) {
	req, vaultDir, _ := gitVaultRequest(t)
	cfg := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "vibe-palace", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("git_enabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitT(t, vaultDir, "rev-parse", "HEAD")
	outs := stepProjectScaffold(context.Background(), req)
	if len(outs) != 1 || outs[0].Status != Pass {
		t.Fatalf("outcomes = %+v, want the one Pass scaffold row", outs)
	}
	if d := strings.Join(outs[0].Details, "\n"); !strings.Contains(d, "not committed") || !strings.Contains(d, "git is disabled") {
		t.Errorf("Details = %q, want the not-committed reason", d)
	}
	if gitT(t, vaultDir, "rev-parse", "HEAD") != head {
		t.Error("committed on a git-disabled host")
	}
}

// Review C5: with no committer identity the commit fails and stays a Fail row
// (the vault cannot be committed to at all), and the row names the remedy:
// git config user.name / user.email, in the vault or globally.
func TestProjectScaffold_NoIdentityFailRowNamesTheRemedy(t *testing.T) {
	req, vaultDir, _ := gitVaultRequest(t)
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
	gitT(t, vaultDir, "config", "--unset", "user.email")
	gitT(t, vaultDir, "config", "--unset", "user.name")
	gitT(t, vaultDir, "config", "user.useConfigOnly", "true")

	outs := stepProjectScaffold(context.Background(), req)
	if len(outs) != 2 || outs[1].Status != Fail {
		t.Fatalf("outcomes = %+v, want the scaffold row then a Fail commit row", outs)
	}
	detail := strings.Join(outs[1].Details, "\n")
	for _, want := range []string{"config user.name", "config user.email", "--global", "re-run `vp init`"} {
		if !strings.Contains(detail, want) {
			t.Errorf("Fail row lacks %q:\n%s", want, detail)
		}
	}
}

// Review G2: on a non-git vault the first init says why nothing was committed;
// a converged re-init, which scaffolded nothing, says nothing at all.
func TestProjectScaffold_ConvergedReinitOnANonGitVaultPrintsNoReason(t *testing.T) {
	sandboxHost(t)
	req, _ := newRequest(t, false)
	first := stepProjectScaffold(context.Background(), req)
	if d := strings.Join(first[0].Details, "\n"); !strings.Contains(d, "not a git repository") {
		t.Fatalf("first init on a non-git vault: Details = %q, want the reason", d)
	}
	again := stepProjectScaffold(context.Background(), req)
	if len(again) != 1 || len(again[0].Details) != 0 {
		t.Errorf("converged re-init on a non-git vault = %+v, want the bare Info row", again)
	}
}
