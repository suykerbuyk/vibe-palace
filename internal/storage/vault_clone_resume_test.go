// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
)

// stopAfterRename runs req with the run killed between the rename and the
// bind: <path> is in place, carrying the clone marker, and nothing is bound.
func (f *cloneFixture) stopAfterRename(t *testing.T, req CloneRequest) {
	t.Helper()
	old := cloneAfterRename
	cloneAfterRename = func() error { return errKilledClone }
	defer func() { cloneAfterRename = old }()
	if _, err := CloneVault(context.Background(), req); !errors.Is(err, errKilledClone) {
		t.Fatalf("the stopped run: err = %v", err)
	}
	if _, err := os.Stat(cloneMarkerPath(req.Path)); err != nil {
		t.Fatalf("the stopped clone carries no marker: %v", err)
	}
}

// wantResumeRefusal: the re-run refused in clone's words, naming the path,
// with why in the message; nothing was bound and <path> is still there.
func (f *cloneFixture) wantResumeRefusal(t *testing.T, req CloneRequest, cfgBefore, why string) error {
	t.Helper()
	_, err := CloneVault(context.Background(), req)
	if err == nil {
		t.Fatal("the re-run resumed; want a refusal")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "refusing to clone") || !strings.Contains(msg, req.Path) || strings.Contains(msg, "vault init") ||
		!strings.Contains(msg, why) {
		t.Fatalf("want a clone refusal naming %s and saying %q, got: %v", req.Path, why, err)
	}
	if got := bindRead(t, f.cfg); got != cfgBefore {
		t.Fatalf("a refused re-run changed the host config:\n%s", got)
	}
	if _, err := os.Stat(req.Path); err != nil {
		t.Fatalf("a refused re-run removed %s", req.Path)
	}
	return err
}

// wireLikeAClone gives dir the remotes a clone of f would have.
func (f *cloneFixture) wireLikeAClone(t *testing.T, dir string) {
	t.Helper()
	rebindGit(t, dir, "remote", "add", "mirror", fileURL(f.mirror))
	rebindGit(t, dir, "fetch", "-q", "mirror")
}

// Probe (a): an unrelated vault at <path> naming B's URLs, clean and at B's
// tip, is not resumed: it carries no marker.
func TestCloneResumeProbeA_UnrelatedVaultWithTheSameURLs(t *testing.T) {
	f := newCloneFixture(t)
	rebindGit(t, filepath.Dir(f.target), "clone", "-q", fileURL(f.origin), f.target)
	f.wireLikeAClone(t, f.target)
	pre := bindRead(t, f.cfg)
	err := f.wantResumeRefusal(t, f.req("qa"), pre, "carries no .git/"+cloneMarkerName)
	if !errors.Is(err, ErrInitPathExists) {
		t.Fatalf("err = %v", err)
	}
}

// Probe (b): a stopped clone whose remotes.toml was edited, whose mirror was
// removed, and whose real mirror diverged, refuses.
func TestCloneResumeProbeB_EditedRemotesRemovedMirrorDivergedMirror(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	rebindWrite(t, filepath.Join(f.target, filepath.FromSlash(remotesFile)),
		renderRemotesFile([]RecordedRemote{{Name: "origin", URL: fileURL(f.origin)}}))
	rebindGit(t, f.target, "remote", "remove", "mirror")
	f.commitB(t, "Projects/qa/mirror-only.md", "m\n")
	rebindGit(t, f.B, "push", "-q", "mirror", "main")
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "not an unfinished clone")
}

// Probe (c): a manual shallow `git clone --depth 1` is not resumed.
func TestCloneResumeProbeC_ManualShallowClone(t *testing.T) {
	f := newCloneFixture(t)
	rebindGit(t, filepath.Dir(f.target), "clone", "-q", "--depth", "1", fileURL(f.origin), f.target)
	f.wireLikeAClone(t, f.target)
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "carries no .git/"+cloneMarkerName)
}

// Probe (d): a stopped clone with an unpushed commit deleting Projects/orch
// refuses: its HEAD is not contained in the live tip.
func TestCloneResumeProbeD_UnpushedCommitDeletingAProject(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	rebindGit(t, f.target, "rm", "-rq", "Projects/orch")
	rebindGit(t, f.target, "commit", "-qm", "drop orch")
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "is not contained in")
}

// Probe (e): a stopped clone moved inside the default vault refuses: the
// re-run runs initCheckParent too.
func TestCloneResumeProbeE_NestedInTheDefaultVault(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req())
	nested := filepath.Join(f.global, "quantum-vault")
	if err := os.Rename(f.target, nested); err != nil {
		t.Fatal(err)
	}
	req := f.req()
	req.Path = nested
	pre := bindRead(t, f.cfg)
	err := f.wantResumeRefusal(t, req, pre, "is inside the vault")
	if !errors.Is(err, ErrInitNested) {
		t.Fatalf("err = %v", err)
	}
}

// The marker must name the same URL: a clone stopped from the mirror does not
// resume under the origin URL.
func TestCloneResumeRefusesAMarkerOfAnotherURL(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, CloneRequest{URL: fileURL(f.mirror), Path: f.target})
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "it is an unfinished clone of "+fileURL(f.mirror))
}

// The marker's remotes must be the ones the clone records.
func TestCloneResumeRefusesAMarkerOfOtherRemotes(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	data, err := os.ReadFile(cloneMarkerPath(f.target))
	if err != nil {
		t.Fatal(err)
	}
	var m cloneMarker
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	m.Remotes = m.Remotes[:1]
	data, _ = json.Marshal(m)
	rebindWrite(t, cloneMarkerPath(f.target), string(data))
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "its marker names remotes")
}

// A dirty tree refuses, an untracked file included.
func TestCloneResumeRefusesADirtyTree(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	rebindWrite(t, filepath.Join(f.target, "Projects", "qa", "stray.md"), "s\n")
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "its tree has changes")
}

// The mirror check runs again: a real mirror that diverged after the stop
// refuses.
func TestCloneResumeRerunsTheMirrorCheck(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	f.commitB(t, "Projects/qa/mirror-only.md", "m\n")
	rebindGit(t, f.B, "push", "-q", "mirror", "main")
	pre := bindRead(t, f.cfg)
	err := f.wantResumeRefusal(t, f.req("qa"), pre, "diverged")
	if !errors.Is(err, ErrCloneMirror) {
		t.Fatalf("err = %v", err)
	}
}

// The remote count: an extra remote refuses, even one in sync.
func TestCloneResumeRefusesAnExtraRemote(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	extra := initBareRemote(t)
	rebindGit(t, f.B, "push", "-q", fileURL(extra), "main")
	rebindGit(t, f.target, "remote", "add", "extra", fileURL(extra))
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "its record names 2")
}

// The per-remote URL: a mirror pointed at another (in-sync) repository
// refuses.
func TestCloneResumeRefusesARemoteAtAnotherURL(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	other := initBareRemote(t)
	rebindGit(t, f.B, "push", "-q", fileURL(other), "main")
	rebindGit(t, f.target, "remote", "set-url", "mirror", fileURL(other))
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "its remote mirror is")
}

// InspectVaultGit: a stopped clone turned bare-configured is not the top
// level of a work tree, and the re-run says so first.
func TestCloneResumeRefusesWhatIsNotItsOwnWorkTree(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	rebindGit(t, f.target, "config", "core.bare", "true")
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "not the top level of its own git repository")
}

// --expect on the re-run: a digest that is not the plan's refuses, and the
// clone stays resumable.
func TestCloneResumeChecksExpect(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	run := f.req("qa")
	run.Expect = "v1:0000"
	pre := bindRead(t, f.cfg)
	if _, err := CloneVault(context.Background(), run); !errors.Is(err, ErrCloneExpect) {
		t.Fatalf("err = %v", err)
	}
	if bindRead(t, f.cfg) != pre {
		t.Fatal("a refused --expect bound")
	}
	if _, err := os.Stat(cloneMarkerPath(f.target)); err != nil {
		t.Fatal("the marker is gone after a refused --expect")
	}
}

// A bind that fails on the re-run keeps <path> and its marker: the same
// command can finish it later.
func TestCloneResumeKeepsThePathWhenTheBindFails(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	old := cloneBeforeBind
	cloneBeforeBind = func() { _ = os.Remove(filepath.Join(f.global, "Audits", "departures", "qa.json")) }
	t.Cleanup(func() { cloneBeforeBind = old })
	_, err := CloneVault(context.Background(), f.req("qa"))
	if err == nil || !strings.Contains(err.Error(), "is kept") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(cloneMarkerPath(f.target)); err != nil {
		t.Fatalf("the re-run's failed bind did not keep %s and its marker: %v", f.target, err)
	}
}

// A finished clone loses its marker, so the same command run again refuses.
func TestCloneFinishedCloneIsNotResumedAgain(t *testing.T) {
	f := newCloneFixture(t)
	if _, err := CloneVault(context.Background(), f.req("qa")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cloneMarkerPath(f.target)); !os.IsNotExist(err) {
		t.Fatalf("a finished clone still carries its marker: %v", err)
	}
	pre := bindRead(t, f.cfg)
	f.wantResumeRefusal(t, f.req("qa"), pre, "carries no .git/"+cloneMarkerName)
}

// A dry run writes nothing, so it reaps no stale scratch either.
func TestCloneDryRunDoesNotReap(t *testing.T) {
	f := newCloneFixture(t)
	stale := filepath.Join(filepath.Dir(f.target), fmt.Sprintf(".%s.vp-clone-%d-dead", filepath.Base(f.target), time.Now().Add(-2*cloneScratchStale).Unix()))
	if err := os.MkdirAll(filepath.Join(stale, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dry := f.req("qa")
	dry.DryRun = true
	if _, err := CloneVault(context.Background(), dry); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("a dry run removed a stale scratch")
	}
}

// cloneWording: vault init's nested-path refusal reads as clone's, and keeps
// its sentinel.
func TestCloneWordsTheInitPathRefusals(t *testing.T) {
	f := newCloneFixture(t)
	req := f.req()
	req.Path = filepath.Join(f.global, "inside")
	_, err := CloneVault(context.Background(), req)
	if !errors.Is(err, ErrInitNested) || !strings.HasPrefix(err.Error(), "refusing to clone: ") || strings.Contains(err.Error(), "vault init") {
		t.Fatalf("err = %v", err)
	}
}

// shellQuote (lifecycle_copy.go, shared with vault copy): plain words alone; anything else single-quoted, a single quote
// escaped, and the empty word as ”.
func TestCloneShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/quantum-vault":    "/home/u/quantum-vault",
		"git@host:q/v.git":         "git@host:q/v.git",
		"":                         "''",
		"/home/u/my vault":         "'/home/u/my vault'",
		"it's":                     `'it'\''s'`,
		"$HOME/v":                  "'$HOME/v'",
		"a;rm -rf ~":               "'a;rm -rf ~'",
		"file:///srv/v`id`":        "'file:///srv/v`id`'",
		"https://h/v.git?x=1&y=2":  "'https://h/v.git?x=1&y=2'",
		"/tmp/*":                   "'/tmp/*'",
		"line\nbreak":              "'line\nbreak'",
		"ssh://git@h:22/~u/v.git":  "ssh://git@h:22/~u/v.git",
		"--bind":                   "--bind",
		"v1:0123456789abcdef":      "v1:0123456789abcdef",
		"a\"b":                     `'a"b'`,
		`back\slash`:               `'back\slash'`,
		"tab\there":                "'tab\there'",
		"(sub)":                    "'(sub)'",
		"x|y":                      "'x|y'",
		"<in":                      "'<in'",
		"!bang":                    "'!bang'",
		"#hash":                    "'#hash'",
		"{brace}":                  "'{brace}'",
		"[glob]":                   "'[glob]'",
		"q?":                       "'q?'",
		"semi;":                    "'semi;'",
		"amp&":                     "'amp&'",
		"caret^":                   "'caret^'",
		"it''s":                    `'it'\'''\''s'`,
		"plain-word_1.2+3=4,5@6~7": "plain-word_1.2+3=4,5@6~7",
		"pct%":                     "'pct%'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// lsRemoteHead ends its options with "--": a URL that begins with '-' is
// read as a repository, never as an option to git. Without it, this URL runs
// its --upload-pack command against the repository named HEAD beside it.
func TestLsRemoteHeadTreatsADashURLAsARepository(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir) // lsRemoteHead runs in os.TempDir()
	rebindGit(t, dir, "init", "-q", "--bare", "HEAD")
	ran := filepath.Join(dir, "upload-pack-ran")
	if _, err := lsRemoteHead("--upload-pack=touch " + ran + "; git-upload-pack"); err == nil {
		t.Fatal("an option-shaped URL was read as a remote")
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the URL was taken as git's --upload-pack option and its command ran")
	}
}

// R1: a re-run binds against the live tip, never a stale HEAD. B records qa's
// departure after the stop; the same line re-run must not bind qa.
func TestCloneResumeBindsAgainstTheLiveTipNotAStaleHead(t *testing.T) {
	f := newCloneFixture(t)
	f.stopAfterRename(t, f.req("qa"))
	rebindGit(t, f.B, "rm", "-rq", "Projects/qa")
	if _, _, err := NewVault(f.B).writeDeparture(nil, departure.Record{Slug: "qa", Kind: departure.MovedToVault, To: "git@elsewhere.example:q/v.git"}, true); err != nil {
		t.Fatal(err)
	}
	rebindGit(t, f.B, "add", "-A")
	rebindGit(t, f.B, "commit", "-qm", "qa departs B")
	rebindGit(t, f.B, "push", "-q", "origin", "main")
	rebindGit(t, f.B, "push", "-q", "mirror", "main")
	_, err := CloneVault(context.Background(), f.req("qa"))
	if err == nil || strings.Contains(bindRead(t, f.cfg), "qa =") {
		t.Fatalf("a re-run bound qa to a vault that has since recorded its departure: err = %v", err)
	}
}

// R2: a re-run while a fresh run is binding refuses on the clone lock, so the
// fresh run's bind lands and its path stays.
func TestCloneLockStopsAReRunDuringAFreshRunsBind(t *testing.T) {
	f := newCloneFixture(t)
	var rerun error
	ran := false
	old := bindBeforeWrite
	bindBeforeWrite = func() {
		if !ran {
			ran = true
			_, rerun = CloneVault(context.Background(), f.req("qa"))
		}
	}
	t.Cleanup(func() { bindBeforeWrite = old })
	if _, err := CloneVault(context.Background(), f.req("qa")); err != nil {
		t.Fatalf("the fresh run failed: %v (the re-run: %v)", err, rerun)
	}
	if rerun == nil || !strings.Contains(rerun.Error(), "another vp vault clone") {
		t.Fatalf("the concurrent re-run: err = %v", rerun)
	}
	if _, err := os.Stat(f.target); err != nil || !strings.Contains(bindRead(t, f.cfg), "qa = \""+f.target+"\"") {
		t.Fatalf("want the clone in place and bound; config:\n%s", bindRead(t, f.cfg))
	}
}

// R2 belt and braces: a fresh run whose bind fails keeps <path> when the host
// config binds a project to it by then.
func TestCloneKeepsItsPathWhenTheConfigBindsIt(t *testing.T) {
	f := newCloneFixture(t)
	old := bindBeforeWrite
	bindBeforeWrite = func() {
		rebindWrite(t, f.cfg, bindRead(t, f.cfg)+"\n[project_vaults]\nqa = \""+f.target+"\"\n")
	}
	t.Cleanup(func() { bindBeforeWrite = old })
	_, err := CloneVault(context.Background(), f.req("qa"))
	if err == nil || !strings.Contains(err.Error(), "is kept because") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(f.target); err != nil {
		t.Fatal("the failed bind removed a path the config binds")
	}
}

// R2 belt and braces: it keeps <path> when the marker is no longer this run's.
func TestCloneKeepsItsPathWhenTheMarkerIsAnotherRuns(t *testing.T) {
	f := newCloneFixture(t)
	old := bindBeforeWrite
	bindBeforeWrite = func() {
		rebindWrite(t, cloneMarkerPath(f.target), `{"url":"`+fileURL(f.origin)+`","run_id":"clone-another"}`)
		rebindWrite(t, f.cfg, bindRead(t, f.cfg)+"# touched\n") // the CAS fails
	}
	t.Cleanup(func() { bindBeforeWrite = old })
	_, err := CloneVault(context.Background(), f.req("qa"))
	if err == nil || !strings.Contains(err.Error(), "no longer this run's") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(f.target); err != nil {
		t.Fatal("the failed bind removed a path another run's marker claims")
	}
}
