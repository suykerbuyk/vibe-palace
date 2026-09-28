// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// cloneFixture is a host after a move landed: a default vault (vault_path)
// recording qa and orch as moved to vault B, and B published to two bare
// remotes, origin and mirror, which its tracked remotes.toml records.
type cloneFixture struct {
	home, global, cfg string
	B, origin, mirror string
	target            string // where the clone goes
}

func newCloneFixture(t *testing.T) *cloneFixture {
	t.Helper()
	f := &cloneFixture{home: rebindEnv(t)}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	f.global = rebindVault(t, filepath.Join(f.home, "global-vault"))
	cfg, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	f.cfg = cfg
	rebindWrite(t, cfg, "vault_path = \""+f.global+"\"\n")
	f.origin, f.mirror = initBareRemote(t), initBareRemote(t)
	f.B = filepath.Join(t.TempDir(), "b")
	rebindVault(t, f.B, "qa", "orch")
	rebindWrite(t, filepath.Join(f.B, filepath.FromSlash(remotesFile)),
		renderRemotesFile([]RecordedRemote{{Name: "origin", URL: fileURL(f.origin)}, {Name: "mirror", URL: fileURL(f.mirror)}}))
	rebindGit(t, f.B, "init", "-q", "-b", "main")
	rebindGit(t, f.B, "add", "-A")
	rebindGit(t, f.B, "commit", "-qm", "vault B")
	for name, bare := range map[string]string{"origin": f.origin, "mirror": f.mirror} {
		rebindGit(t, f.B, "remote", "add", name, fileURL(bare))
		rebindGit(t, f.B, "push", "-q", name, "main")
	}
	for _, s := range []string{"qa", "orch"} {
		if _, err := NewVault(f.global).RecordDeparture(s, departure.MovedToVault, fileURL(f.origin)); err != nil {
			t.Fatal(err)
		}
	}
	f.target = filepath.Join(f.home, "quantum-vault")
	return f
}

func (f *cloneFixture) req(bind ...string) CloneRequest {
	return CloneRequest{URL: fileURL(f.origin), Path: f.target, Bind: bind}
}

// commitB commits a change in B (not pushed).
func (f *cloneFixture) commitB(t *testing.T, rel, body string) {
	t.Helper()
	rebindWrite(t, filepath.Join(f.B, filepath.FromSlash(rel)), body)
	rebindGit(t, f.B, "add", "-A")
	rebindGit(t, f.B, "commit", "-qm", "change "+rel)
}

// assertNothingWritten: no <path>, no scratch sibling, the config unchanged.
func (f *cloneFixture) assertNothingWritten(t *testing.T, cfgBefore string) {
	t.Helper()
	if _, err := os.Lstat(f.target); !os.IsNotExist(err) {
		t.Errorf("%s exists after a refusal", f.target)
	}
	ents, _ := os.ReadDir(filepath.Dir(f.target))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".vp-clone-") {
			t.Errorf("scratch %s left behind", e.Name())
		}
	}
	if got := bindRead(t, f.cfg); got != cfgBefore {
		t.Errorf("the host config changed:\n%s", got)
	}
}

// The clone makes a vault at <path> with every recorded remote, binds both
// projects in ONE config write (the .bak is the pre-clone config), and a
// checkout then resolves the clone.
func TestCloneBindsEveryProjectInOneWrite(t *testing.T) {
	f := newCloneFixture(t)
	pre := bindRead(t, f.cfg)
	rep, err := CloneVault(context.Background(), f.req("qa", "orch"))
	if err != nil {
		t.Fatal(err)
	}
	if format, err := surface.ReadFormat(f.target); err != nil || format != surface.RequiredDataFormat {
		t.Fatalf("the clone is not a vault: %d, %v", format, err)
	}
	remotes := strings.Fields(rebindGit(t, f.target, "remote"))
	slices.Sort(remotes)
	if strings.Join(remotes, ",") != "mirror,origin" {
		t.Fatalf("remotes = %v", remotes)
	}
	if got := rebindGit(t, f.target, "rev-parse", "mirror/main"); got != rep.Tip {
		t.Fatalf("mirror not fetched: %s vs %s", got, rep.Tip)
	}
	cfg := bindRead(t, f.cfg)
	for _, s := range []string{"qa", "orch"} {
		if !strings.Contains(cfg, s+" = \""+f.target+"\"") {
			t.Errorf("config lacks the %s binding:\n%s", s, cfg)
		}
	}
	if b := bindRead(t, f.cfg+".bak"); b != pre {
		t.Fatalf("config.toml.bak is not the pre-clone config:\n%s", b)
	}
	co := bindCheckout(t, f.home, "qa", "qa")
	if res, err := ResolveVaultBinding(co); err != nil || !sameVaultRoot(res.Path, f.target) {
		t.Fatalf("the checkout resolves %+v, %v", res, err)
	}
	if len(rep.Undo) != 2 || !strings.HasPrefix(rep.Undo[0], "rm -rf "+f.target) {
		t.Fatalf("undo = %q", rep.Undo)
	}
}

// The dry run clones, checks and stops: nothing is left behind, and it prints
// the exact real-run line with --expect. The line runs; a wrong --expect
// refuses.
func TestCloneDryRunPrintsTheRealRunLineAndWritesNothing(t *testing.T) {
	f := newCloneFixture(t)
	pre := bindRead(t, f.cfg)
	req := f.req("qa")
	req.DryRun = true
	rep, err := CloneVault(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.assertNothingWritten(t, pre)
	want := "vp vault clone " + fileURL(f.origin) + " " + f.target + " --bind qa --expect " + rep.Digest
	if rep.Command != want || !strings.Contains(rep.ConfigChange, "qa") {
		t.Fatalf("command = %q\nwant      %q\nchange %q", rep.Command, want, rep.ConfigChange)
	}
	bad := f.req("qa")
	bad.Expect = "v1:00"
	if _, err := CloneVault(context.Background(), bad); !errors.Is(err, ErrCloneExpect) {
		t.Fatalf("a wrong --expect: %v", err)
	}
	f.assertNothingWritten(t, pre)
	good := f.req("qa")
	good.Expect = rep.Digest
	if _, err := CloneVault(context.Background(), good); err != nil {
		t.Fatalf("the printed line must run: %v", err)
	}
}

// path exists: an existing directory, even empty, refuses and nothing is
// written.
func TestCloneRefusesAnExistingPath(t *testing.T) {
	f := newCloneFixture(t)
	if err := os.Mkdir(f.target, 0o755); err != nil {
		t.Fatal(err)
	}
	pre := bindRead(t, f.cfg)
	if _, err := CloneVault(context.Background(), f.req("qa")); !errors.Is(err, ErrInitPathExists) {
		t.Fatalf("err = %v", err)
	}
	if ents, _ := os.ReadDir(f.target); len(ents) != 0 {
		t.Fatal("the existing directory was written")
	}
	if bindRead(t, f.cfg) != pre {
		t.Fatal("the config changed")
	}
}

// unreachable remote: the Q3 remedy is named.
func TestCloneRefusesAnUnreachableRemote(t *testing.T) {
	f := newCloneFixture(t)
	pre := bindRead(t, f.cfg)
	_, err := CloneVault(context.Background(), CloneRequest{URL: "file://" + filepath.Join(f.home, "no-such-remote"), Path: f.target})
	if !errors.Is(err, ErrCloneUnreachable) || !strings.Contains(err.Error(), "GIT_SSH_COMMAND='ssh -i <key>") {
		t.Fatalf("err = %v", err)
	}
	f.assertNothingWritten(t, pre)
}

// R1: a <url> carrying credentials refuses before any git runs, and the
// refusal does not repeat the secret.
func TestCloneRefusesACredentialURL(t *testing.T) {
	f := newCloneFixture(t)
	pre := bindRead(t, f.cfg)
	_, err := CloneVault(context.Background(), CloneRequest{URL: "https://someone:s3cr3t-t0ken@127.0.0.1:9/vault.git", Path: f.target})
	if !errors.Is(err, ErrCloneCredentials) || strings.Contains(err.Error(), "s3cr3t-t0ken") {
		t.Fatalf("err = %v", err)
	}
	f.assertNothingWritten(t, pre)
}

// R1: a remotes.toml entry carrying credentials refuses: the file is the
// remote's, and its token would land in this host's .git/config.
func TestCloneRefusesACredentialInRemotesFile(t *testing.T) {
	f := newCloneFixture(t)
	f.commitB(t, remotesFile, renderRemotesFile([]RecordedRemote{
		{Name: "origin", URL: fileURL(f.origin)},
		{Name: "mirror", URL: "file://tok:s3cret@localhost" + filepath.ToSlash(f.mirror)},
	}))
	rebindGit(t, f.B, "push", "-q", "origin", "main")
	pre := bindRead(t, f.cfg)
	_, err := CloneVault(context.Background(), f.req())
	if !errors.Is(err, ErrCloneCredentials) || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v", err)
	}
	f.assertNothingWritten(t, pre)
}

// R3: remotes.toml is untrusted: an option-shaped name, a slash name and two
// entries naming one repository each refuse.
func TestCloneRefusesAnInvalidRemotesFile(t *testing.T) {
	for name, entries := range map[string][]RecordedRemote{
		"option-shaped name":   {{Name: "origin", URL: ""}, {Name: "-x", URL: "file:///elsewhere/x.git"}},
		"slash name":           {{Name: "origin", URL: ""}, {Name: "a/b", URL: "file:///elsewhere/x.git"}},
		"duplicate repository": {{Name: "origin", URL: ""}, {Name: "again", URL: ""}},
	} {
		f := newCloneFixture(t)
		for i := range entries {
			if entries[i].URL == "" {
				entries[i].URL = fileURL(f.origin)
			}
		}
		f.commitB(t, remotesFile, renderRemotesFile(entries))
		rebindGit(t, f.B, "push", "-q", "origin", "main")
		pre := bindRead(t, f.cfg)
		if _, err := CloneVault(context.Background(), f.req()); !errors.Is(err, ErrInitRemoteBad) {
			t.Fatalf("%s: err = %v", name, err)
		}
		f.assertNothingWritten(t, pre)
	}
}

// R2: the digest binds the host config and the default vault's record bytes:
// a change to either after the dry run makes --expect refuse.
func TestCloneExpectBindsTheConfigAndTheRecord(t *testing.T) {
	for name, change := range map[string]func(f *cloneFixture){
		"host config": func(f *cloneFixture) {
			rebindWrite(t, f.cfg, bindRead(t, f.cfg)+"# an edit after the dry run\n")
		},
		"departure record": func(f *cloneFixture) {
			p := filepath.Join(f.global, filepath.FromSlash(departure.RelPath("qa")))
			rebindWrite(t, p, strings.Replace(bindRead(t, p), `"date": "`, `"date": "1`, 1))
		},
	} {
		f := newCloneFixture(t)
		dry := f.req("qa")
		dry.DryRun = true
		rep, err := CloneVault(context.Background(), dry)
		if err != nil {
			t.Fatal(err)
		}
		change(f)
		pre := bindRead(t, f.cfg)
		run := f.req("qa")
		run.Expect = rep.Digest
		if _, err := CloneVault(context.Background(), run); !errors.Is(err, ErrCloneExpect) {
			t.Fatalf("%s changed: err = %v", name, err)
		}
		f.assertNothingWritten(t, pre)
	}
}

// R4: killed between the rename and the bind, the re-run of the same line
// finishes the bind on the existing clone.
func TestCloneReRunFinishesAStoppedBind(t *testing.T) {
	f := newCloneFixture(t)
	old := cloneAfterRename
	cloneAfterRename = func() error { return errKilledClone }
	t.Cleanup(func() { cloneAfterRename = old })
	dry := f.req("qa")
	dry.DryRun = true
	plan, err := CloneVault(context.Background(), dry)
	if err != nil {
		t.Fatal(err)
	}
	run := f.req("qa")
	run.Expect = plan.Digest
	if _, err := CloneVault(context.Background(), run); !errors.Is(err, errKilledClone) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(f.target); err != nil || strings.Contains(bindRead(t, f.cfg), "qa =") {
		t.Fatal("want: the clone in place and no binding yet")
	}
	cloneAfterRename = old
	rep, err := CloneVault(context.Background(), run) // the same line, --expect included
	if err != nil {
		t.Fatalf("the re-run must finish the bind: %v", err)
	}
	if !rep.Resumed || !strings.Contains(bindRead(t, f.cfg), "qa = \""+f.target+"\"") {
		t.Fatalf("resumed %v; config:\n%s", rep.Resumed, bindRead(t, f.cfg))
	}
}

var errKilledClone = errors.New("killed (test)")

// R4: a path that is not such a clone refuses in clone's words, naming the
// path, and is left untouched.
func TestCloneRefusesAnExistingPathThatIsNotItsClone(t *testing.T) {
	f := newCloneFixture(t)
	g := newCloneFixture(t) // another vault's clone at the same place
	g.target = f.target
	if _, err := CloneVault(context.Background(), CloneRequest{URL: fileURL(g.origin), Path: f.target}); err != nil {
		t.Fatal(err)
	}
	_, err := CloneVault(context.Background(), f.req("qa"))
	if !errors.Is(err, ErrInitPathExists) || !strings.Contains(err.Error(), "refusing to clone") ||
		!strings.Contains(err.Error(), f.target) || strings.Contains(err.Error(), "vault init") {
		t.Fatalf("err = %v", err)
	}
}

// R4: a stale .<name>.vp-clone-* sibling a killed run left is reaped by the
// next run; a fresh one (a live run's) is kept.
func TestCloneReapsAStaleScratch(t *testing.T) {
	f := newCloneFixture(t)
	parent, base := filepath.Dir(f.target), filepath.Base(f.target)
	stale := filepath.Join(parent, fmt.Sprintf(".%s.vp-clone-%d-dead", base, time.Now().Add(-2*cloneScratchStale).Unix()))
	live := filepath.Join(parent, fmt.Sprintf(".%s.vp-clone-%d-live", base, time.Now().Unix()))
	for _, d := range []string{stale, live} {
		if err := os.MkdirAll(filepath.Join(d, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := CloneVault(context.Background(), f.req("qa")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the stale scratch was not reaped")
	}
	if _, err := os.Stat(live); err != nil {
		t.Error("a live run's scratch was removed")
	}
}

// empty remote: refuses "not a vault yet".
func TestCloneRefusesAnEmptyRemote(t *testing.T) {
	f := newCloneFixture(t)
	_, err := CloneVault(context.Background(), CloneRequest{URL: fileURL(initBareRemote(t)), Path: f.target})
	if !errors.Is(err, ErrCloneNotAVault) || !strings.Contains(err.Error(), "not a vault yet") {
		t.Fatalf("err = %v", err)
	}
}

// format mismatch, either direction: refuses, and the scratch is removed.
func TestCloneRefusesAFormatMismatch(t *testing.T) {
	for _, delta := range []int{+1, -1} {
		f := newCloneFixture(t)
		// Written as bytes: WriteFormat refuses to lower a format.
		rebindWrite(t, surface.VaultManifestPath(f.B), fmt.Sprintf("format = %d\n", surface.RequiredDataFormat+delta))
		rebindGit(t, f.B, "commit", "-qam", "another format")
		rebindGit(t, f.B, "push", "-q", "origin", "main")
		rebindGit(t, f.B, "push", "-q", "mirror", "main")
		pre := bindRead(t, f.cfg)
		if _, err := CloneVault(context.Background(), f.req()); !errors.Is(err, ErrCloneFormat) {
			t.Fatalf("format %+d: err = %v", delta, err)
		}
		f.assertNothingWritten(t, pre)
	}
}

// bind target absent: the clone lacks the project; refuses, nothing written.
func TestCloneRefusesABindTargetAbsentFromTheClone(t *testing.T) {
	f := newCloneFixture(t)
	if _, err := NewVault(f.global).RecordDeparture("gone", departure.MovedToVault, fileURL(f.origin)); err != nil {
		t.Fatal(err)
	}
	pre := bindRead(t, f.cfg)
	_, err := CloneVault(context.Background(), f.req("qa", "gone"))
	if err == nil || !strings.Contains(err.Error(), "holds neither palace/gone nor Projects/gone") {
		t.Fatalf("err = %v", err)
	}
	f.assertNothingWritten(t, pre)
}

// record plus content: B holds a departure record for qa AND real Projects/qa
// content. A vault that records the project departed is not its primary.
func TestCloneRefusesATargetWithARecordOverContent(t *testing.T) {
	f := newCloneFixture(t)
	b, err := (departure.Record{Slug: "qa", Kind: departure.MovedToVault, To: "git@example.invalid:x/y.git", Date: "2026-09-27"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	f.commitB(t, departure.RelPath("qa"), string(b))
	rebindGit(t, f.B, "push", "-q", "origin", "main")
	rebindGit(t, f.B, "push", "-q", "mirror", "main")
	pre := bindRead(t, f.cfg)
	_, err = CloneVault(context.Background(), f.req("qa"))
	if err == nil || !strings.Contains(err.Error(), "holds a departure record for it") {
		t.Fatalf("err = %v", err)
	}
	f.assertNothingWritten(t, pre)
}

// no record in the default vault: a clone run before its pull refuses "pull
// first".
func TestCloneBeforeThePullRefusesPullFirst(t *testing.T) {
	f := newCloneFixture(t)
	if err := os.Remove(filepath.Join(f.global, filepath.FromSlash(departure.RelPath("qa")))); err != nil {
		t.Fatal(err)
	}
	pre := bindRead(t, f.cfg)
	_, err := CloneVault(context.Background(), f.req("qa"))
	if err == nil || !strings.Contains(err.Error(), "pull first") {
		t.Fatalf("err = %v", err)
	}
	f.assertNothingWritten(t, pre)
}

// mirror: a diverged mirror refuses, an unreachable one refuses, and one that
// is merely behind only warns.
func TestCloneMirrorStates(t *testing.T) {
	t.Run("diverged", func(t *testing.T) {
		f := newCloneFixture(t)
		f.commitB(t, "Projects/qa/on-mirror-only.md", "x\n")
		rebindGit(t, f.B, "push", "-q", "mirror", "main")
		rebindGit(t, f.B, "reset", "-q", "--hard", "HEAD~1")
		f.commitB(t, "Projects/qa/on-origin-only.md", "y\n")
		rebindGit(t, f.B, "push", "-q", "origin", "main")
		pre := bindRead(t, f.cfg)
		if _, err := CloneVault(context.Background(), f.req("qa")); !errors.Is(err, ErrCloneMirror) || !strings.Contains(err.Error(), "diverged") {
			t.Fatalf("err = %v", err)
		}
		f.assertNothingWritten(t, pre)
	})
	t.Run("unreachable", func(t *testing.T) {
		f := newCloneFixture(t)
		if err := os.RemoveAll(f.mirror); err != nil {
			t.Fatal(err)
		}
		pre := bindRead(t, f.cfg)
		if _, err := CloneVault(context.Background(), f.req("qa")); !errors.Is(err, ErrCloneMirror) {
			t.Fatalf("err = %v", err)
		}
		f.assertNothingWritten(t, pre)
	})
	t.Run("behind", func(t *testing.T) {
		f := newCloneFixture(t)
		f.commitB(t, "Projects/qa/newer.md", "z\n")
		rebindGit(t, f.B, "push", "-q", "origin", "main")
		rep, err := CloneVault(context.Background(), f.req("qa"))
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Warnings) == 0 || !strings.Contains(strings.Join(rep.Warnings, "\n"), "mirror mirror is behind") {
			t.Fatalf("warnings = %q", rep.Warnings)
		}
	})
}

// The URL must be one of the recorded remotes; its entry's name becomes the
// clone's remote name. With no remotes.toml, <url> is origin only.
func TestCloneRemoteNamingFromRemotesFile(t *testing.T) {
	f := newCloneFixture(t)
	if _, err := CloneVault(context.Background(), CloneRequest{URL: fileURL(f.mirror), Path: f.target}); err != nil {
		t.Fatal(err)
	}
	if got := rebindGit(t, f.target, "config", "--get", "branch.main.remote"); got != "mirror" {
		t.Fatalf("the cloned-from remote is %q, want mirror", got)
	}

	g := newCloneFixture(t)
	other := initBareRemote(t)
	rebindGit(t, g.B, "push", "-q", fileURL(other), "main")
	if _, err := CloneVault(context.Background(), CloneRequest{URL: fileURL(other), Path: g.target}); !errors.Is(err, ErrCloneRemoteUnknown) {
		t.Fatalf("an unrecorded URL: err = %v", err)
	}

	h := newCloneFixture(t)
	rebindGit(t, h.B, "rm", "-q", remotesFile)
	rebindGit(t, h.B, "commit", "-qm", "no remotes file")
	rebindGit(t, h.B, "push", "-q", "origin", "main")
	rep, err := CloneVault(context.Background(), h.req())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Remotes) != 1 || rep.Remotes[0].Name != "origin" || rebindGit(t, h.target, "remote") != "origin" {
		t.Fatalf("remotes = %+v", rep.Remotes)
	}
}

// A bind that fails after the rename (the default vault changed under the
// clone) removes <path> again, and the config is unchanged.
func TestCloneRemovesThePathWhenTheBindFails(t *testing.T) {
	f := newCloneFixture(t)
	pre := bindRead(t, f.cfg)
	old := cloneBeforeRename
	cloneBeforeRename = func() { _ = os.Remove(filepath.Join(f.global, filepath.FromSlash(departure.RelPath("qa")))) }
	t.Cleanup(func() { cloneBeforeRename = old })
	if _, err := CloneVault(context.Background(), f.req("qa")); err == nil || !strings.Contains(err.Error(), "was removed again") {
		t.Fatalf("err = %v", err)
	}
	f.assertNothingWritten(t, pre)
}

// SSH append: an operator GIT_SSH_COMMAND carrying -i <key> keeps the key, and
// BatchMode is appended, on the clone's own network calls.
func TestCloneKeepsTheOperatorSSHCommand(t *testing.T) {
	f := newCloneFixture(t)
	dir := t.TempDir()
	argv := filepath.Join(dir, "argv")
	fake := filepath.Join(dir, "fake-ssh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argv+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", fake+" -i /home/u/.ssh/id_mdh -o IdentitiesOnly=yes")
	if _, err := CloneVault(context.Background(), CloneRequest{URL: "ssh://git@example.invalid/vault.git", Path: f.target}); !errors.Is(err, ErrCloneUnreachable) {
		t.Fatalf("err = %v", err)
	}
	data, err := os.ReadFile(argv)
	if err != nil {
		t.Fatalf("the fake ssh never ran: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	for _, want := range []string{"/home/u/.ssh/id_mdh", "IdentitiesOnly=yes", "BatchMode=yes"} {
		if !slices.Contains(got, want) {
			t.Fatalf("ssh argv %q lacks %q", got, want)
		}
	}
}

// git_enabled = false refuses before anything is read or written.
func TestCloneRefusesWhenGitIsDisabled(t *testing.T) {
	f := newCloneFixture(t)
	rebindWrite(t, f.cfg, "vault_path = \""+f.global+"\"\ngit_enabled = false\n")
	pre := bindRead(t, f.cfg)
	if _, err := CloneVault(context.Background(), f.req("qa")); err == nil || !strings.Contains(err.Error(), "git") {
		t.Fatalf("err = %v", err)
	}
	f.assertNothingWritten(t, pre)
}
