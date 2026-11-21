// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// The signed project rename. Tests that exercise rollback, locking and wording
// stub renameProve and inject the signManifest seam, so they run where ssh-keygen
// is absent; tests that sign for real call requireCmd first (newSignFix does).

const renameManifestBody = "{\n  \"project_slug\": \"old\",\n  \"vault_rel_session_note\": \"Projects/old/sessions/s%d.md\"%s\n}\n"

type renameFix struct {
	*signFix
	manifests []string // vault-relative, Projects/old/transcripts/...
}

func gitV(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitenv.SafeGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRenameFix builds a git vault (no remote) holding project "old" with three
// manifests: a.json and b.json signed, c.json unsigned and carrying an unknown key.
// withSigning=false builds the fixture without a real key (no ssh-keygen needed).
func newRenameFix(t *testing.T, sign bool) *renameFix {
	t.Helper()
	var f *renameFix
	if sign {
		f = &renameFix{signFix: newSignFix(t)}
	} else {
		tmp := t.TempDir()
		f = &renameFix{signFix: &signFix{tmp: tmp, vault: filepath.Join(tmp, "vault"), keyPath: filepath.Join(tmp, "nokey")}}
		f.allowedSigners = filepath.Join(tmp, "allowed")
		f.hostConfig(t, true)
	}
	v := f.vault
	if err := os.MkdirAll(v, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInitVault(t, v)
	write := func(rel, body string) {
		p := filepath.Join(v, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".vibe-palace/vault.toml", "format = 2\n")
	write(".gitignore", "*.bak\npalace/.local/\n.vp-locks/\n")
	for i := 1; i <= 3; i++ {
		write(fmt.Sprintf("Projects/old/sessions/s%d.md", i), fmt.Sprintf("---\nproject: old\n---\nsession %d\n", i))
	}
	write("Projects/old/resume.md", "---\nproject: old\n---\n# old — Working Context\n")
	testInitProject(t, v)
	for i, name := range []string{"a", "b", "c"} {
		extra := ""
		if name == "c" {
			extra = ",\n  \"regenerated\": {\"by\": \"repair\"}"
		}
		rel := "Projects/old/transcripts/" + name + ".manifest.json"
		write(rel, fmt.Sprintf(renameManifestBody, i+1, extra))
		f.manifests = append(f.manifests, rel)
		if sign && name != "c" {
			if err := Sign(filepath.Join(v, rel), f.signOpts()); err != nil {
				t.Fatalf("sign fixture manifest: %v", err)
			}
		} else if name != "c" {
			write(rel+".sig", "-----BEGIN SSH SIGNATURE-----\nfake "+name+"\n-----END SSH SIGNATURE-----\n")
		}
	}
	if err := os.Chmod(filepath.Join(v, f.manifests[0]), 0o600); err != nil {
		t.Fatal(err)
	}
	gitV(t, v, "add", "-A")
	gitV(t, v, "commit", "-q", "-m", "seed old")
	return f
}

func testInitProject(t *testing.T, v string) {
	t.Helper()
	for _, rel := range []string{"commands/README.md", "skills/README.md"} {
		p := filepath.Join(v, "Projects", "old", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *renameFix) req() storage.RenameRequest {
	return storage.RenameRequest{Vault: f.vault, From: "old", To: "new", Signer: RenameSigner{}}
}

func (f *renameFix) head(t *testing.T) string { return gitV(t, f.vault, "rev-parse", "HEAD") }

// dirty is `git status --porcelain` minus the lock sidecar directory.
func (f *renameFix) dirty(t *testing.T) string {
	t.Helper()
	return gitV(t, f.vault, "status", "--porcelain", "--", ".", ":!.vp-locks")
}

func (f *renameFix) noMarker(t *testing.T) {
	t.Helper()
	if exists(filepath.Join(f.vault, ".git", "vp-lifecycle-pending")) {
		t.Error("a lifecycle marker was left standing")
	}
}

// tree lists every file under the vault except .git and .vp-locks.
func (f *renameFix) tree(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(f.vault, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(f.vault, p)
		if d.IsDir() && (rel == ".git" || rel == ".vp-locks") {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// assertOldIsHEAD is the rollback proof: every tracked file under Projects/old
// (every .sig included) is byte-identical to HEAD, and Projects/new and
// palace/new are gone. It is the OUTER rename rollback that does this (vacate the
// destination first, then check out the source from HEAD); neither `reset --keep`
// (a no-op on a move before the commit) nor the per-manifest failRestore restores
// the old tree.
func (f *renameFix) assertOldIsHEAD(t *testing.T) {
	t.Helper()
	files := strings.Fields(gitV(t, f.vault, "ls-tree", "-r", "--name-only", "HEAD", "--", "Projects/old"))
	if len(files) < 7 {
		t.Fatalf("fixture: HEAD holds only %d files under Projects/old", len(files))
	}
	for _, rel := range files {
		show := exec.Command("git", "-C", f.vault, "show", "HEAD:"+rel)
		show.Env = gitenv.SafeGitEnv()
		want, err := show.Output()
		if err != nil {
			t.Fatal(err)
		}
		got, rerr := os.ReadFile(filepath.Join(f.vault, filepath.FromSlash(rel)))
		if rerr != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is not byte-identical to HEAD after the rollback (err=%v)", rel, rerr)
		}
	}
	for _, gone := range []string{"Projects/new", "palace/new"} {
		if exists(filepath.Join(f.vault, gone)) {
			t.Errorf("%s still exists after the rollback", gone)
		}
	}
	if d := f.dirty(t); d != "" {
		t.Errorf("git status not clean after the rollback:\n%s", d)
	}
	f.noMarker(t)
}

// T1: happy path with the real signer.
func TestRenameSigned_ReSignsEverySignedManifest(t *testing.T) {
	f := newRenameFix(t, true)
	before := f.head(t)

	// Config-drift case: after Preflight, the host config's key changes to one that
	// does not exist. The snapshot is the rule, so the rewrite must not notice.
	swap(t, &renameProve, func(vaultRoot string, opts SignOptions) string {
		if r := signingKeyUnusable(opts); r != "" {
			return r
		}
		if r := proveSigning(vaultRoot, opts); r != "" {
			return r
		}
		f.hostConfigKey(t, true, filepath.Join(f.tmp, "gone-after-preflight"))
		return ""
	})

	res, err := storage.ApplyRename(f.req())
	if err != nil {
		t.Fatalf("ApplyRename: %v", err)
	}
	if res.Commit == "" {
		t.Fatal("no commit")
	}
	if n := gitV(t, f.vault, "rev-list", "--count", before+"..HEAD"); n != "1" {
		t.Errorf("the rename made %s commits, want exactly 1", n)
	}
	for _, name := range []string{"a", "b"} {
		m := filepath.Join(f.vault, "Projects/new/transcripts", name+".manifest.json")
		if err := f.verify(m); err != nil {
			t.Errorf("%s: the moved signature does not verify against the new bytes: %v", name, err)
		}
		if !strings.Contains(string(mustRead(t, m)), `"project_slug": "new"`) {
			t.Errorf("%s: project_slug not rewritten", name)
		}
	}
	c := filepath.Join(f.vault, "Projects/new/transcripts/c.manifest.json")
	if exists(SignaturePath(c)) {
		t.Error("the unsigned manifest gained a signature: signedness must be kept, never upgraded")
	}
	if !strings.Contains(string(mustRead(t, c)), `"regenerated": {"by": "repair"}`) {
		t.Error("an unknown manifest key did not survive the rewrite (a *Manifest round trip would drop it)")
	}
	// M-B9: the rewrite keeps the file's mode (WithInheritPerm).
	fi, err := os.Stat(filepath.Join(f.vault, "Projects/new/transcripts/a.manifest.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("a.manifest.json mode = %v (err %v), want 0600 kept", fi.Mode().Perm(), err)
	}
	for _, rel := range f.manifests {
		if exists(filepath.Join(f.vault, filepath.FromSlash(rel))) {
			t.Errorf("%s still exists at the old slug", rel)
		}
	}
}

// T2: a key that cannot sign refuses at plan time, writing nothing.
func TestRenameSigned_UnusableKeyRefusesAtPlanTime(t *testing.T) {
	cases := map[string]func(t *testing.T, f *renameFix){
		"key removed": func(t *testing.T, f *renameFix) {
			if err := os.Remove(f.keyPath); err != nil {
				t.Fatal(err)
			}
		},
		"sign_mode unset with a .sig present": func(t *testing.T, f *renameFix) { f.hostConfig(t, false) },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRenameFix(t, true)
			breakIt(t, f)
			head, tree := f.head(t), f.tree(t)

			plan, perr := storage.PlanRename(f.req())
			if perr == nil || plan == nil || len(plan.Refusals) == 0 {
				t.Fatalf("the dry-run plan did not refuse: plan=%+v err=%v", plan, perr)
			}
			_, err := storage.ApplyRename(f.req())
			var re *storage.RenameRefusalsError
			if !errors.As(err, &re) {
				t.Fatalf("want *RenameRefusalsError, got %T: %v", err, err)
			}
			f.noMarker(t)
			if f.head(t) != head || f.dirty(t) != "" {
				t.Error("the vault changed")
			}
			if got := f.tree(t); strings.Join(got, "\n") != strings.Join(tree, "\n") {
				t.Errorf("the vault's file set changed:\nbefore %v\nafter  %v", tree, got)
			}
			if exists(filepath.Join(f.vault, "Projects/new")) {
				t.Error("Projects/new exists")
			}
		})
	}
}

// T2b: an encrypted key and no agent. The probe must fail fast with the tool's own
// message, never prompt (askpass must not run) and never hang. This is the case a
// stat-and-open check cannot see.
func TestRenameSigned_EncryptedKeyNoAgentFailsFast(t *testing.T) {
	f := newRenameFix(t, true)
	enc := filepath.Join(f.tmp, "id_enc")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "correct horse", "-f", enc).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	f.hostConfigKey(t, true, enc)
	marker := filepath.Join(f.tmp, "askpass-ran")
	askpass := filepath.Join(f.tmp, "askpass.sh")
	if err := os.WriteFile(askpass, []byte("#!/bin/sh\ntouch "+marker+"\necho x\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_ASKPASS", askpass)
	t.Setenv("DISPLAY", ":0") // askpass is only consulted when a display is set
	os.Unsetenv("SSH_AUTH_SOCK")
	t.Setenv("SSH_AUTH_SOCK", "")
	head, tree := f.head(t), f.tree(t)

	start := time.Now()
	_, err := storage.ApplyRename(f.req())
	if el := time.Since(start); el > 9*time.Second {
		t.Errorf("the refusal took %v, want well under the 10 s probe bound", el)
	}
	var re *storage.RenameRefusalsError
	if !errors.As(err, &re) {
		t.Fatalf("want *RenameRefusalsError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "probe signature failed") {
		t.Errorf("the refusal should come from the probe: %v", err)
	}
	if testing.Verbose() {
		t.Logf("refusal: %v (%v)", firstLine(err.Error()), time.Since(start))
	}
	if exists(marker) {
		t.Error("SSH_ASKPASS ran: the signer was not non-interactive")
	}
	f.noMarker(t)
	if f.head(t) != head || strings.Join(f.tree(t), "\n") != strings.Join(tree, "\n") || f.dirty(t) != "" {
		t.Error("the vault changed")
	}
}

// failNth returns a signManifest that does the real signing but fails the nth call.
func failNth(t *testing.T, n int, calls *int) {
	t.Helper()
	swap(t, &signManifest, func(path string, o SignOptions) error {
		*calls++
		if *calls == n {
			return errors.New("injected signer failure")
		}
		return Sign(path, o)
	})
}

// stubProve makes Preflight succeed without touching ssh-keygen, and returns the
// snapshot the tests inject.
func stubProve(t *testing.T) {
	t.Helper()
	swap(t, &renameProve, func(string, SignOptions) string { return "" })
}

// T3: the signer fails on manifest 2 of 3, inside the rewrite and before the commit.
func TestRenameSigned_SignerFailureMidRewriteRollsBackToHEAD(t *testing.T) {
	f := newRenameFix(t, false)
	stubProve(t)
	calls := 0
	swap(t, &signManifest, func(string, SignOptions) error {
		calls++
		if calls == 2 {
			return errors.New("injected signer failure")
		}
		return nil
	})
	head := f.head(t)
	_, err := storage.ApplyRename(f.req())
	if err == nil {
		t.Fatal("the rename succeeded despite the signer failure")
	}
	if !errors.Is(err, ErrRenameManifestSigning) {
		t.Errorf("error is not ErrRenameManifestSigning: %v", err)
	}
	if !strings.Contains(err.Error(), "the rename was rolled back (Projects/old restored from HEAD, Projects/new removed)") {
		t.Errorf("the rolled-back outcome is missing: %v", err)
	}
	if f.head(t) != head {
		t.Error("a commit was made")
	}
	f.assertOldIsHEAD(t)
}

// T4: the restore inside failRestore fails too. The error names the rename and the
// destination pair, not the link remedy; the cause is still the signer error; the
// outer rollback still puts the old tree back.
func TestRenameSigned_RestoreFailureWordingNamesTheRename(t *testing.T) {
	f := newRenameFix(t, false)
	stubProve(t)
	swap(t, &signManifest, func(string, SignOptions) error { return errors.New("injected signer failure") })
	swap(t, &restoreBytes, func(string, string, []byte) error { return errors.New("injected restore failure") })
	_, err := storage.ApplyRename(f.req())
	if err == nil {
		t.Fatal("the rename succeeded")
	}
	msg := err.Error()
	for _, want := range []string{"vault rename old -> new", "Projects/new/transcripts/", "nothing to re-run here", "rolled back"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the error lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "re-run the link") {
		t.Errorf("the rename error still carries the link remedy:\n%s", msg)
	}
	if !errors.Is(err, ErrRenameManifestSigning) || !errors.Is(err, ErrManifestSigning) {
		t.Errorf("the helper error is not in the chain: %v", err)
	}
	f.assertOldIsHEAD(t)
}

// T7a: a held destination-path lock times out the rewrite (bounded, never blocking),
// and the run rolls back.
func TestRenameSigned_DestinationLockTimeoutRollsBack(t *testing.T) {
	f := newRenameFix(t, false)
	stubProve(t)
	swap(t, &signManifest, func(string, SignOptions) error { return nil })
	swap(t, &renameLockTimeout, 300*time.Millisecond)

	// Only the DESTINATION path is held: a rewrite that locked the source path
	// instead would not contend and the rename would wrongly succeed.
	dest, err := vaultlock.Acquire(f.vault, filepath.Join(f.vault, "Projects/new/transcripts/b.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The outer rollback takes per-path locks itself while it vacates the
	// destination, so the holder lets go once the timeout has had time to fire.
	go func() {
		time.Sleep(900 * time.Millisecond)
		_ = dest()
	}()
	start := time.Now()
	_, rerr := storage.ApplyRename(f.req())
	if rerr == nil || !errors.Is(rerr, vaultlock.ErrLockWaitTimeout) {
		t.Fatalf("want a lock timeout, got %v", rerr)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the lock wait was not bounded")
	}
	f.assertOldIsHEAD(t)
}

// T7c: exactly one per-manifest lock is held at a time during the rewrite. The
// injected signer runs under the manifest's lock; from there it probes every
// manifest path: its own must be held, every other must be free.
func TestRenameSigned_OneManifestLockAtATime(t *testing.T) {
	f := newRenameFix(t, false)
	stubProve(t)
	var maxHeld, calls int
	swap(t, &signManifest, func(path string, o SignOptions) error {
		calls++
		held := 0
		for _, name := range []string{"a", "b", "c"} {
			p := filepath.Join(f.vault, "Projects/new/transcripts", name+".manifest.json")
			rel, ok, err := vaultlock.TryAcquire(f.vault, p)
			if err != nil {
				return err
			}
			if ok {
				_ = rel()
			} else {
				held++
			}
		}
		if held > maxHeld {
			maxHeld = held
		}
		if held != 1 {
			return fmt.Errorf("%d manifest locks held while re-signing %s, want exactly 1", held, path)
		}
		return nil
	})
	if _, err := storage.ApplyRename(f.req()); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if calls != 2 || maxHeld != 1 {
		t.Errorf("signer calls=%d maxHeld=%d, want 2 calls and max 1", calls, maxHeld)
	}
}

// T7b: RewriteManifest's own body: one bounded acquire, no other lock, no config
// reload, no directory creation.
func TestRenameSigner_RewriteManifestLockAndConfigDiscipline(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "rename_signer.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	found := false
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "RewriteManifest" {
			continue
		}
		found = true
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.SelectorExpr:
				if x, ok := fn.X.(*ast.Ident); ok {
					calls[x.Name+"."+fn.Sel.Name]++
				}
			case *ast.Ident:
				calls[fn.Name]++
			}
			return true
		})
	}
	if !found {
		t.Fatal("RewriteManifest not found")
	}
	if calls["vaultlock.AcquireWithTimeout"] != 1 {
		t.Errorf("want exactly one vaultlock.AcquireWithTimeout, got %d", calls["vaultlock.AcquireWithTimeout"])
	}
	for _, banned := range []string{
		"vaultlock.Acquire", "vaultlock.AcquireHeld", "vaultlock.TryAcquire", "vaultlock.AcquirePair",
		"lockManifest", "SignOptionsForProject", "LoadConfig", "os.MkdirAll", "os.Mkdir", "os.MkdirTemp", "EnsureVaultDir",
	} {
		if calls[banned] != 0 {
			t.Errorf("RewriteManifest must not call %s", banned)
		}
	}
}

// T7d: no non-test file in this package takes the vault root as a lock target
// (acyclicity: the archive side never waits on the root commit lock).
func TestArchive_NeverAcquiresTheVaultRootLock(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(token.NewFileSet(), n, nil, 0)
		if perr != nil {
			t.Fatal(perr)
		}
		ast.Inspect(file, func(nd ast.Node) bool {
			call, ok := nd.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if x, ok := sel.X.(*ast.Ident); ok && x.Name == "vaultlock" && (sel.Sel.Name == "AcquireHeld" || sel.Sel.Name == "AcquirePair") {
					t.Errorf("%s calls vaultlock.%s", n, sel.Sel.Name)
				}
			}
			return true
		})
	}
}

// B9 census: the only atomicfile.Write of a manifest in non-test archive code sits
// in writeManifestBytes (restoreBytes, a variable, is the one named exception);
// classification is by ENCLOSING FUNCTION, not by how the path argument is spelled.
func TestManifestWriteCensus_OnlyTheBytesCore(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	// Functions that legitimately call atomicfile.Write for something that is not
	// a manifest: none today besides the two named below, so any new one fails
	// closed and must be classified here on purpose.
	allowed := map[string]bool{"writeManifestBytes": true}
	sawCore := false
	callers := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, n, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v (this census fails closed)", n, perr)
		}
		for _, d := range file.Decls {
			switch dd := d.(type) {
			case *ast.FuncDecl:
				if dd.Body == nil {
					continue
				}
				ast.Inspect(dd.Body, func(nd ast.Node) bool {
					if c, ok := nd.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "writeManifestBytes" {
							callers[dd.Name.Name] = true
						}
					}
					if isAtomicWrite(nd) {
						if dd.Name.Name == "writeManifestBytes" {
							sawCore = true
						}
						if !allowed[dd.Name.Name] {
							t.Errorf("atomicfile.Write outside the bytes core: %s (in %s)", fset.Position(nd.Pos()), dd.Name.Name)
						}
					}
					return true
				})
			case *ast.GenDecl:
				// package-level var func literals: only restoreBytes may write.
				for _, sp := range dd.Specs {
					vs, ok := sp.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, val := range vs.Values {
						ast.Inspect(val, func(nd ast.Node) bool {
							if isAtomicWrite(nd) && !(i < len(vs.Names) && vs.Names[i].Name == "restoreBytes") {
								t.Errorf("atomicfile.Write in package-level %s outside restoreBytes: %s", vs.Names[0].Name, fset.Position(nd.Pos()))
							}
							return true
						})
					}
				}
			}
		}
	}
	for _, want := range []string{"writeManifestSigned", "RewriteManifest"} {
		if !callers[want] {
			t.Errorf("%s no longer calls writeManifestBytes: a manifest write has left the core", want)
		}
	}
	if !sawCore {
		t.Error("the census did not find atomicfile.Write in writeManifestBytes: the core or this test drifted")
	}
}

func isAtomicWrite(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "atomicfile" && sel.Sel.Name == "Write"
}

// Windows-compile guard note: sign_detach_windows.go is compile-checked only.

// T10: the probe's temp dir is removed and nothing lands in the vault; with the
// temp root INSIDE the vault the preflight refuses and creates nothing there.
func TestRenameSigner_ProbeHygieneAndTempRootInsideVault(t *testing.T) {
	f := newRenameFix(t, true)
	opts := f.signOpts()
	opts.NonInteractive = true

	tmpRoot := t.TempDir()
	swap(t, &renameTempDir, func() string { return tmpRoot })
	if r := proveSigning(f.vault, opts); r != "" {
		t.Fatalf("probe failed: %s", r)
	}
	if ents, _ := os.ReadDir(tmpRoot); len(ents) != 0 {
		t.Errorf("probe left %d entries in the temp root", len(ents))
	}
	// Failure path also cleans up.
	bad := opts
	bad.Key = filepath.Join(f.tmp, "missing")
	if r := proveSigning(f.vault, bad); r == "" {
		t.Error("a missing key passed the probe")
	}
	if ents, _ := os.ReadDir(tmpRoot); len(ents) != 0 {
		t.Errorf("a failed probe left %d entries in the temp root", len(ents))
	}

	inside := filepath.Join(f.vault, "scratch-tmp")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	swap(t, &renameTempDir, func() string { return inside })
	r := proveSigning(f.vault, opts)
	if r == "" || !strings.Contains(r, "outside the vault") {
		t.Errorf("a temp root inside the vault must refuse, got %q", r)
	}
	if ents, _ := os.ReadDir(inside); len(ents) != 0 {
		t.Errorf("the probe created %d artifacts inside the vault", len(ents))
	}
}

// The Windows no-console launch cannot run where the signing tests run (Windows CI
// does not execute them), so this pins its SOURCE: the windows file must set a
// console-less creation flag on the shared builder's SysProcAttr, and the build
// check (GOOS=windows go vet / go test -c) keeps it compiling. Behavior there is
// compile-checked only.
func TestWindowsDetachSetsNoConsoleCreationFlags(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "sign_detach_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var flagExprs []string
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "CreationFlags" {
			ast.Inspect(kv.Value, func(m ast.Node) bool {
				if i, ok := m.(*ast.Ident); ok {
					flagExprs = append(flagExprs, i.Name)
				}
				return true
			})
		}
		return true
	})
	got := strings.Join(flagExprs, " ")
	if !strings.Contains(got, "detachedProcess") || !strings.Contains(got, "createNoWindow") {
		t.Errorf("sign_detach_windows.go must set CreationFlags to detachedProcess|createNoWindow, found %q", got)
	}
	// and the shared builder must call detachFromTerminal for every non-interactive signer.
	src, rerr := os.ReadFile("sign.go")
	if rerr != nil || !strings.Contains(string(src), "detachFromTerminal(cmd)") {
		t.Errorf("signCommand no longer calls detachFromTerminal (err=%v)", rerr)
	}
}

// The probe's 10 s bound is ONE deadline over the sign and the verify, not 10 s
// each. A stub ssh-keygen takes 0.6 s to sign and 0.6 s to verify; with a 1 s
// probe bound each call alone fits but the pair does not, so the probe must
// refuse (verify is cut off) and must not take the 2 x bound a per-call timeout
// would allow.
func TestRenameSigner_ProbeBoundIsOneDeadlineForSignAndVerify(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell-script ssh-keygen stub")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nsleep 0.6\nif [ \"$2\" = sign ]; then\n  for a; do last=$a; done\n  echo sig > \"$last.sig\"\nfi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh-keygen"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	key := filepath.Join(tmp, "key")
	if err := os.WriteFile(key, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(tmp, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	swap(t, &renameTempDir, func() string { return t.TempDir() })
	swap(t, &renameProbeTimeout, 1*time.Second)
	opts := SignOptions{Mode: SignModeSSH, Key: key, NonInteractive: true}

	start := time.Now()
	r := proveSigning(vault, opts)
	if r == "" || !strings.Contains(r, "did not verify") {
		t.Errorf("sign 0.6 s + verify 0.6 s must exceed a 1 s shared bound and refuse at verify, got %q", r)
	}
	if el := time.Since(start); el > 1900*time.Millisecond {
		t.Errorf("the probe took %v; one shared 1 s deadline must cut it near 1 s", el)
	}
	// Control: a bound that fits the pair passes.
	swap(t, &renameProbeTimeout, 5*time.Second)
	if r := proveSigning(vault, opts); r != "" {
		t.Errorf("control: a 5 s bound should pass the stub, got %q", r)
	}
}

// A passphrase-protected GPG key that no agent can unlock is refused at plan time
// by the probe, fast (--pinentry-mode error: no prompt, no hang), with no vault
// write. Hermetic: a private GNUPGHOME whose agent has no pinentry program and no
// cached passphrase.
func TestRenameSigned_GPGEncryptedKeyNoAgentRefusesAtPlanTime(t *testing.T) {
	requireCmd(t, "gpg")
	requireCmd(t, "gpgconf")
	f := newRenameFix(t, false)
	home := filepath.Join(f.tmp, "gh")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GNUPGHOME", home)
	// The agent's pinentry is a stub that records it ran and then hangs, as an
	// interactive prompt would. Without --pinentry-mode error the probe would
	// reach it and stall to its bound.
	ran := filepath.Join(f.tmp, "pinentry-ran")
	pin := filepath.Join(f.tmp, "pinentry-stub.sh")
	if err := os.WriteFile(pin, []byte("#!/bin/sh\ntouch "+ran+"\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "gpg-agent.conf"), []byte("pinentry-program "+pin+"\nallow-loopback-pinentry\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kill := func() { _ = exec.Command("gpgconf", "--kill", "gpg-agent").Run() }
	t.Cleanup(kill)
	gen := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "correct horse",
		"--quick-generate-key", "vp-rename-test <vp-rename-test@example.invalid>", "ed25519", "sign", "never")
	if out, err := gen.CombinedOutput(); err != nil {
		t.Skipf("gpg cannot generate a key on this host: %v: %s", err, out)
	}
	kill() // drop the cached passphrase: the next agent starts cold

	cfgHome := filepath.Join(f.tmp, "cfg")
	body := "[archive]\nsign_mode = \"gpg\"\nsign_key = \"vp-rename-test@example.invalid\"\n"
	if err := os.WriteFile(filepath.Join(cfgHome, "vibe-palace", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	head, tree := f.head(t), f.tree(t)

	start := time.Now()
	plan, err := storage.PlanRename(f.req())
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("the plan refusal took %v, want well under the 10 s probe bound", el)
	}
	var re *storage.RenameRefusalsError
	if !errors.As(err, &re) {
		t.Fatalf("want *RenameRefusalsError, got %T: %v (plan %+v)", err, err, plan)
	}
	if !strings.Contains(err.Error(), "probe signature failed") {
		t.Errorf("the refusal should come from the probe: %v", err)
	}
	if testing.Verbose() {
		t.Logf("refusal: %v (%v)", firstLine(err.Error()), time.Since(start))
	}
	if exists(ran) {
		t.Error("the agent's pinentry ran: the signer was not non-interactive")
	}
	f.noMarker(t)
	if f.head(t) != head || f.dirty(t) != "" || strings.Join(f.tree(t), "\n") != strings.Join(tree, "\n") {
		t.Error("the vault changed")
	}
	if exists(filepath.Join(f.vault, "Projects/new")) {
		t.Error("Projects/new exists")
	}
}
