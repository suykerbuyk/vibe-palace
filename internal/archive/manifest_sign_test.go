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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// These tests drive the REAL ssh-keygen: a stale signature is a property of real
// signed bytes, and a fake signer would prove nothing about whether verify
// passes. Under CI a missing tool FAILS them (testutil.RequireTool); locally it
// skips.
//
// What they pin: every path that changes a manifest's bytes leaves it correctly
// signed, or explicitly unsigned, or untouched; never with a stale signature.

const (
	signTestIdentity = "g1@example.com"
	signTestProject  = "demo"
)

var signTestNow = time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)

type signFix struct {
	tmp, vault, src         string
	keyPath, allowedSigners string
}

// swap replaces *p for the test and restores it afterwards.
func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func newSignFix(t *testing.T) *signFix {
	t.Helper()
	requireCmd(t, "ssh-keygen")
	tmp := t.TempDir()
	f := &signFix{tmp: tmp, vault: filepath.Join(tmp, "vault"), src: filepath.Join(tmp, "src.jsonl")}
	f.keyPath, f.allowedSigners = genSSHKey(t, tmp, signTestIdentity)
	testutil.InitProject(t, f.vault, signTestProject)
	f.writeSource(t, sampleClaudeJSONL)
	f.hostConfig(t, true)
	return f
}

func (f *signFix) writeSource(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(f.src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// hostConfig points the process at a sandboxed host config that does (or does
// not) carry this project's [archive] signing keys, where LoadConfig reads them.
func (f *signFix) hostConfig(t *testing.T, signing bool) {
	t.Helper()
	f.hostConfigKey(t, signing, f.keyPath)
}

// hostConfigKey is hostConfig with the sign_key it names chosen by the caller, so
// a test can point the project at a key that does not exist.
func (f *signFix) hostConfigKey(t *testing.T, signing bool, key string) {
	t.Helper()
	cfgHome := filepath.Join(f.tmp, "cfg")
	if err := os.MkdirAll(filepath.Join(cfgHome, "vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# host config for the signing tests\n"
	if signing {
		body += fmt.Sprintf("[archive]\nsign_mode = \"ssh\"\nsign_key = %q\nallowed_signers = %q\nsigner_identity = %q\n",
			key, f.allowedSigners, signTestIdentity)
	}
	if err := os.WriteFile(filepath.Join(cfgHome, "vibe-palace", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("APPDATA", cfgHome)
	t.Setenv("HOME", f.tmp)
	t.Setenv("USERPROFILE", f.tmp)
}

func (f *signFix) signOpts() SignOptions {
	return SignOptions{Mode: SignModeSSH, Key: f.keyPath}
}

func (f *signFix) create(t *testing.T, session string, sign bool) (*CreateResult, error) {
	t.Helper()
	opts := CreateOptions{
		Adapter: ClaudeCodeAdapterName, SessionID: session, SourcePath: f.src,
		VaultRoot: f.vault, ProjectSlug: signTestProject, Now: signTestNow,
	}
	if sign {
		opts.Sign = f.signOpts()
	}
	return Create(opts)
}

func (f *signFix) mustCreate(t *testing.T, session string, sign bool) *CreateResult {
	t.Helper()
	res, err := f.create(t, session, sign)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return res
}

// verify checks the detached signature beside manifestPath.
func (f *signFix) verify(manifestPath string) error {
	return VerifySignature(manifestPath, VerifyOptions{AllowedSigners: f.allowedSigners, Identity: signTestIdentity})
}

// verifyBackup checks a PRESERVED pair: <bak> and sigBackupPath(<bak>). The
// preserved signature is named for the backup, not for a live manifest, so the
// pair is copied back under live-style names in a scratch directory first.
func (f *signFix) verifyBackup(t *testing.T, bak string) error {
	t.Helper()
	dir := t.TempDir()
	m := filepath.Join(dir, "x.manifest.json")
	if err := os.WriteFile(m, mustRead(t, bak), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SignaturePath(m), mustRead(t, sigBackupPath(bak)), 0o644); err != nil {
		t.Fatal(err)
	}
	return f.verify(m)
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

const signTestNote = "Projects/demo/sessions/2026-04-15-aaaa-01.md"

// L1: sign, link, verify. The link must leave a signature that verifies.
func TestLinkSessionNote_ReSignsASignedManifest(t *testing.T) {
	f := newSignFix(t)
	res := f.mustCreate(t, "l1", true)
	if err := f.verify(res.ManifestPath); err != nil {
		t.Fatalf("control: the freshly created manifest must verify: %v", err)
	}
	before := mustRead(t, res.ManifestPath)

	if err := LinkSessionNote(f.vault, res.ManifestPath, signTestNote); err != nil {
		t.Fatalf("LinkSessionNote: %v", err)
	}
	m, err := ReadManifest(res.ManifestPath)
	if err != nil || m.VaultRelSessionNote != signTestNote {
		t.Fatalf("the link was not written (err=%v, got %q)", err, m.VaultRelSessionNote)
	}
	if bytes.Equal(before, mustRead(t, res.ManifestPath)) {
		t.Fatal("fixture: the link did not change the manifest bytes, so a stale signature could not show")
	}
	if err := f.verify(res.ManifestPath); err != nil {
		t.Fatalf("the signature no longer verifies after the link (stale signature): %v", err)
	}
}

// L2: an unsigned manifest stays unsigned, and no config is needed to link it.
func TestLinkSessionNote_UnsignedStaysUnsigned(t *testing.T) {
	f := newSignFix(t) // signing IS configured, so an over-eager link would sign
	res := f.mustCreate(t, "l2", false)
	if err := LinkSessionNote(f.vault, res.ManifestPath, signTestNote); err != nil {
		t.Fatalf("LinkSessionNote: %v", err)
	}
	if exists(SignaturePath(res.ManifestPath)) {
		t.Error("linking an UNSIGNED manifest wrote a .sig: signedness must be maintained, never upgraded")
	}
}

// L3: the idempotent link rewrites nothing, so it touches neither file.
func TestLinkSessionNote_IdempotentLeavesManifestAndSignatureAlone(t *testing.T) {
	f := newSignFix(t)
	res := f.mustCreate(t, "l3", true)
	if err := LinkSessionNote(f.vault, res.ManifestPath, signTestNote); err != nil {
		t.Fatal(err)
	}
	m1, s1 := mustRead(t, res.ManifestPath), mustRead(t, SignaturePath(res.ManifestPath))
	// A signer that fails would make any second re-sign visible as an error.
	swap(t, &signManifest, func(string, SignOptions) error { return errors.New("the idempotent link must not sign") })
	if err := LinkSessionNote(f.vault, res.ManifestPath, signTestNote); err != nil {
		t.Fatalf("the idempotent link failed (it re-signed?): %v", err)
	}
	if !bytes.Equal(m1, mustRead(t, res.ManifestPath)) || !bytes.Equal(s1, mustRead(t, SignaturePath(res.ManifestPath))) {
		t.Error("the idempotent link changed the manifest or its signature")
	}
}

// L4: a re-sign that fails after the rewrite puts the previous manifest AND
// signature back byte for byte, and the restored pair still verifies.
func TestLinkSessionNote_ReSignFailureRestoresThePriorPair(t *testing.T) {
	f := newSignFix(t)
	res := f.mustCreate(t, "l4", true)
	m0, s0 := mustRead(t, res.ManifestPath), mustRead(t, SignaturePath(res.ManifestPath))

	swap(t, &signManifest, func(string, SignOptions) error { return errors.New("injected signing failure") })
	err := LinkSessionNote(f.vault, res.ManifestPath, signTestNote)
	if !errors.Is(err, ErrManifestSigning) {
		t.Fatalf("err = %v, want ErrManifestSigning", err)
	}
	if !bytes.Equal(m0, mustRead(t, res.ManifestPath)) {
		t.Error("the previous manifest was not restored byte for byte")
	}
	if !bytes.Equal(s0, mustRead(t, SignaturePath(res.ManifestPath))) {
		t.Error("the previous signature was not restored byte for byte")
	}
	if verr := f.verify(res.ManifestPath); verr != nil {
		t.Errorf("the restored pair does not verify: %v", verr)
	}
	if !strings.Contains(err.Error(), "restored byte for byte") {
		t.Errorf("the error must say what was restored: %v", err)
	}
}

// L5: a .sig exists but signing is no longer usable: refuse, change nothing.
func TestLinkSessionNote_SignedButCannotResignIsRefused(t *testing.T) {
	f := newSignFix(t)
	res := f.mustCreate(t, "l5", true)
	m0, s0 := mustRead(t, res.ManifestPath), mustRead(t, SignaturePath(res.ManifestPath))

	f.hostConfig(t, false) // the project no longer has sign_mode
	err := LinkSessionNote(f.vault, res.ManifestPath, signTestNote)
	if !errors.Is(err, ErrSignedManifestCannotResign) {
		t.Fatalf("err = %v, want ErrSignedManifestCannotResign", err)
	}
	if !bytes.Equal(m0, mustRead(t, res.ManifestPath)) || !bytes.Equal(s0, mustRead(t, SignaturePath(res.ManifestPath))) {
		t.Error("a refused link changed the manifest or its signature")
	}
	if verr := f.verify(res.ManifestPath); verr != nil {
		t.Errorf("a refused link must leave the pair verifying: %v", verr)
	}
	if !strings.Contains(err.Error(), "left untouched") {
		t.Errorf("the refusal must say nothing was changed: %v", err)
	}
}

// L6a: the restore fails and the signature removal succeeds: the manifest as it
// stands is unsigned and the error says so.
func TestLinkSessionNote_RestoreFailureThenRemovalLeavesItUnsigned(t *testing.T) {
	f := newSignFix(t)
	res := f.mustCreate(t, "l6a", true)
	swap(t, &signManifest, func(string, SignOptions) error { return errors.New("injected signing failure") })
	// Fail the restore of the MANIFEST only. The old signature is then genuinely
	// restored beside a manifest it does not cover, so removing it is load-bearing.
	swap(t, &restoreBytes, func(vaultRoot, p string, b []byte) error {
		if strings.HasSuffix(p, ".sig") {
			return atomicfile.Write(vaultRoot, p, b)
		}
		return errors.New("injected restore failure")
	})

	err := LinkSessionNote(f.vault, res.ManifestPath, signTestNote)
	if err == nil {
		t.Fatal("want an error")
	}
	if exists(SignaturePath(res.ManifestPath)) {
		t.Error("a .sig remains after a failed manifest restore: it would be stale against the rewritten manifest")
	}
	for _, want := range []string{"injected signing failure", "injected restore failure", "UNSIGNED"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "POSSIBLY STALE") {
		t.Errorf("the removal succeeded, so the signature must not be called possibly stale: %v", err)
	}
}

// L6b: the restore fails AND the signature removal fails: report BOTH and call
// the signature possibly stale. "Unsigned" is never claimed, because nothing
// established it.
func TestLinkSessionNote_RestoreAndRemovalBothFailIsPossiblyStale(t *testing.T) {
	f := newSignFix(t)
	res := f.mustCreate(t, "l6b", true)
	swap(t, &signManifest, func(string, SignOptions) error { return errors.New("injected signing failure") })
	// Fail the restore of the MANIFEST only. The old signature is then genuinely
	// restored beside a manifest it does not cover, so removing it is load-bearing.
	swap(t, &restoreBytes, func(vaultRoot, p string, b []byte) error {
		if strings.HasSuffix(p, ".sig") {
			return atomicfile.Write(vaultRoot, p, b)
		}
		return errors.New("injected restore failure")
	})
	// The first removal (the helper's clear-before-sign) must work so signing is
	// reached; every later removal fails.
	var calls atomic.Int32
	swap(t, &removeForRollback, func(p string) error {
		if calls.Add(1) == 1 {
			return vaultfs.RemoveNoLock(p)
		}
		return errors.New("injected removal failure")
	})

	err := LinkSessionNote(f.vault, res.ManifestPath, signTestNote)
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	for _, want := range []string{"injected signing failure", "injected restore failure", "injected removal failure", "POSSIBLY STALE"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error is missing %q (both failures must be reported): %v", want, err)
		}
	}
	if strings.Contains(strings.ToLower(msg), "unsigned") {
		t.Errorf("the error claims the manifest is unsigned though removal was not confirmed: %v", err)
	}
	// The state really is the dangerous one: the old signature was restored, the
	// manifest could not be, and the signature could not be removed.
	if !exists(SignaturePath(res.ManifestPath)) {
		t.Error("fixture: the restored signature should still be on disk in this scenario")
	}
}

// L7: Create's changed-source arm with a call that carries no Sign (the hook,
// MCP and Codex shape). No signature may sit beside the new manifest, and the
// preserved manifest keeps its own, verifying, signature.
func TestCreateChangedSource_PriorSignatureMovesWithTheBackup(t *testing.T) {
	f := newSignFix(t)
	first := f.mustCreate(t, "l7", true)
	oldManifest, oldSig := mustRead(t, first.ManifestPath), mustRead(t, SignaturePath(first.ManifestPath))

	f.writeSource(t, sampleClaudeJSONL+`{"type":"user","message":{"role":"user","content":"more"}}`+"\n")
	second := f.mustCreate(t, "l7", false)
	if second.Skipped {
		t.Fatal("fixture: the changed source must not be skipped as idempotent")
	}
	if exists(SignaturePath(second.ManifestPath)) {
		t.Error("a .sig sits beside the NEW manifest: it covers the old manifest's bytes (stale)")
	}
	bak := fmt.Sprintf("%s.%s.bak", first.ManifestPath, shortHash(mustManifest(t, oldManifest).SourceSHA256))
	if !bytes.Equal(oldManifest, mustRead(t, bak)) {
		t.Error("the preserved .bak is not the prior manifest byte for byte")
	}
	if !bytes.Equal(oldSig, mustRead(t, sigBackupPath(bak))) {
		t.Error("the prior signature was not preserved at the .sig.bak beside the backup")
	}
	if err := f.verifyBackup(t, bak); err != nil {
		t.Errorf("the preserved manifest no longer verifies against its preserved signature: %v", err)
	}
}

func mustManifest(t *testing.T, raw []byte) *Manifest {
	t.Helper()
	p := filepath.Join(t.TempDir(), "m.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// L8: changed-source Create whose re-signing fails. The result is ONE coherent
// state: the new archive and new manifest agree, no signature sits beside them,
// and the prior pair is preserved verbatim as <bak> and its .sig.bak. The old pair
// is NOT restored over the new archive.
func TestCreateChangedSource_SigningFailureLeavesACoherentUnsignedNewPair(t *testing.T) {
	f := newSignFix(t)
	first := f.mustCreate(t, "l8", true)
	oldManifest, oldSig := mustRead(t, first.ManifestPath), mustRead(t, SignaturePath(first.ManifestPath))

	f.writeSource(t, sampleClaudeJSONL+`{"type":"user","message":{"role":"user","content":"changed"}}`+"\n")
	swap(t, &signManifest, func(string, SignOptions) error { return errors.New("injected signing failure") })
	_, err := f.create(t, "l8", true)
	if err == nil {
		t.Fatal("want the signing failure to be returned")
	}

	live, rerr := ReadManifest(first.ManifestPath)
	if rerr != nil {
		t.Fatalf("no live manifest after the failed signing: %v", rerr)
	}
	if live.SourceSHA256 == mustManifest(t, oldManifest).SourceSHA256 {
		t.Fatal("the live manifest is the OLD one: the old pair was restored over the new archive")
	}
	sum, herr := hashArchive(first.ArchivePath)
	if herr != nil || sum != live.SourceSHA256 {
		t.Errorf("the archive and the live manifest do not agree (hash %s vs manifest %s, err %v)", sum, live.SourceSHA256, herr)
	}
	if exists(SignaturePath(first.ManifestPath)) {
		t.Error("a signature sits beside the new manifest after a failed signing")
	}
	bak := fmt.Sprintf("%s.%s.bak", first.ManifestPath, shortHash(mustManifest(t, oldManifest).SourceSHA256))
	if !bytes.Equal(oldManifest, mustRead(t, bak)) || !bytes.Equal(oldSig, mustRead(t, sigBackupPath(bak))) {
		t.Error("the prior manifest and signature were not preserved byte for byte")
	}
	if verr := f.verifyBackup(t, bak); verr != nil {
		t.Errorf("the preserved pair does not verify: %v", verr)
	}
	for _, want := range []string{
		"injected signing failure", "NOT signed",
		"on disk now: the new archive " + first.ArchivePath + " is present",
		"the previous manifest is preserved at " + bak,
		"the previous signature is preserved at " + sigBackupPath(bak),
		"a live manifest at " + first.ManifestPath + " is present",
		"a live signature at " + SignaturePath(first.ManifestPath) + " is absent",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}
}

// L8b: a fresh Create whose signing fails keeps the pair, unsigned, and says so.
func TestCreateFresh_SigningFailureLeavesTheUnsignedPair(t *testing.T) {
	f := newSignFix(t)
	swap(t, &signManifest, func(string, SignOptions) error { return errors.New("injected signing failure") })
	_, err := f.create(t, "l8b", true)
	if err == nil || !strings.Contains(err.Error(), "NOT signed") {
		t.Fatalf("err = %v, want a NOT signed error", err)
	}
	base := filepath.Join(f.vault, "Projects", signTestProject, "transcripts", "2026-04-15-l8b")
	if !exists(base+".manifest.json") || !exists(base+".jsonl.zst") {
		t.Error("the new pair was not left in place")
	}
	if exists(base + ".manifest.json.sig") {
		t.Error("a .sig exists after a failed signing")
	}
}

// failOn returns a renameForBackup that fails any rename whose destination or
// source matches, and otherwise performs the real rename.
func failOn(fail func(src, dst string) bool) func(src, dst string) error {
	return func(src, dst string) error {
		if fail(src, dst) {
			return fmt.Errorf("injected rename failure %s -> %s", filepath.Base(src), filepath.Base(dst))
		}
		return vaultfs.RenameNoLock(src, dst)
	}
}

// L12: the .sig -> .sig.bak rename fails and the undo of the first rename
// succeeds: the previous manifest and signature are back at the live names, the
// new archive is on disk, and no backup is left behind.
func TestCreateChangedSource_SecondRenameFailureUndoesTheFirst(t *testing.T) {
	f := newSignFix(t)
	first := f.mustCreate(t, "l12", true)
	oldManifest, oldSig := mustRead(t, first.ManifestPath), mustRead(t, SignaturePath(first.ManifestPath))
	oldHash := mustManifest(t, oldManifest).SourceSHA256

	f.writeSource(t, sampleClaudeJSONL+`{"type":"user","message":{"role":"user","content":"l12"}}`+"\n")
	swap(t, &renameForBackupHook, failOn(func(_, dst string) bool { return strings.HasSuffix(dst, ".sig.bak") }))
	_, err := f.create(t, "l12", true)
	if err == nil {
		t.Fatal("want the rename failure returned")
	}
	if !bytes.Equal(oldManifest, mustRead(t, first.ManifestPath)) || !bytes.Equal(oldSig, mustRead(t, SignaturePath(first.ManifestPath))) {
		t.Error("the previous manifest and signature are not back at the live names byte for byte")
	}
	bak := fmt.Sprintf("%s.%s.bak", first.ManifestPath, shortHash(oldHash))
	if exists(bak) || exists(sigBackupPath(bak)) {
		t.Error("a backup was left behind after the undo")
	}
	if sum, herr := hashArchive(first.ArchivePath); herr != nil || sum == oldHash {
		t.Errorf("fixture: the new archive should be on disk (hash %s, old %s, err %v)", sum, oldHash, herr)
	}
	for _, want := range []string{"injected rename failure", "left in place", "retry the capture"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}
}

// L13: the second rename fails AND the undo fails. The error returns BOTH and
// describes the on-disk state exactly: the old manifest at <bak>, no live
// manifest, the old signature still at the live .sig name, the new archive in
// place.
func TestCreateChangedSource_SecondRenameAndUndoBothFail(t *testing.T) {
	f := newSignFix(t)
	first := f.mustCreate(t, "l13", true)
	oldManifest, oldSig := mustRead(t, first.ManifestPath), mustRead(t, SignaturePath(first.ManifestPath))
	oldHash := mustManifest(t, oldManifest).SourceSHA256
	bak := fmt.Sprintf("%s.%s.bak", first.ManifestPath, shortHash(oldHash))

	f.writeSource(t, sampleClaudeJSONL+`{"type":"user","message":{"role":"user","content":"l13"}}`+"\n")
	swap(t, &renameForBackupHook, failOn(func(src, dst string) bool {
		return strings.HasSuffix(dst, ".sig.bak") || (src == bak && dst == first.ManifestPath)
	}))
	_, err := f.create(t, "l13", true)
	if err == nil {
		t.Fatal("want an error")
	}
	if !bytes.Equal(oldManifest, mustRead(t, bak)) {
		t.Error("the previous manifest is not at <bak>")
	}
	if exists(first.ManifestPath) {
		t.Error("a manifest exists at the live name though the undo failed")
	}
	if !bytes.Equal(oldSig, mustRead(t, SignaturePath(first.ManifestPath))) {
		t.Error("the previous signature is not still at the live .sig name")
	}
	if exists(sigBackupPath(bak)) {
		t.Error("the signature was moved despite the injected failure")
	}
	if sum, herr := hashArchive(first.ArchivePath); herr != nil || sum == oldHash {
		t.Errorf("fixture: the new archive should be on disk (hash %s, old %s, err %v)", sum, oldHash, herr)
	}
	msg := err.Error()
	for _, want := range []string{
		"injected rename failure",            // the second rename
		"undoing the manifest rename failed", // and the undo, both reported
		"NO manifest exists at " + first.ManifestPath,
		"the previous manifest is at " + bak,
		"is still at " + SignaturePath(first.ManifestPath),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}
}

// L10: the INVARIANT THIS TEST PINS IS NARROW AND NAMED: direct calls to
// WriteManifest in the non-test .go files of internal/archive are confined to
// writeManifestSigned. It is an AST walk keyed on the call, so renaming a
// variable cannot defeat it, and it fails closed on a parse error.
//
// It does NOT establish that no other package writes a manifest: it cannot see a
// raw writer in another package, or one behind interface dispatch. The one such
// writer that exists, the project rename's manifest rewrite, is pinned below as a
// NAMED EXCEPTION so that moving or removing it forces this test to be revisited.
// A repository-level writer audit is the funnel rule's job, tracked in task
// funnel-rule-misses-multi-value-path-producers; no second mechanism is built here.
func TestArchivePackageWritesManifestsOnlyThroughTheSigningHelper(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var offenders []string
	sawHelperCall := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v (this census fails closed)", name, perr)
		}
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "WriteManifest" {
					return true
				}
				if fd.Name.Name == "writeManifestSigned" {
					sawHelperCall = true
					return true
				}
				offenders = append(offenders, fmt.Sprintf("%s (in %s)", fset.Position(call.Pos()), fd.Name.Name))
				return true
			})
		}
	}
	if !sawHelperCall {
		t.Error("the census found no WriteManifest call in writeManifestSigned: the helper or this test has drifted")
	}
	if len(offenders) > 0 {
		t.Errorf("WriteManifest is called directly outside writeManifestSigned, so a manifest could be rewritten without re-signing:\n  %s",
			strings.Join(offenders, "\n  "))
	}

	// The named exception, pinned: the project rename rewrites manifest bytes
	// itself (no helper, no lock, no re-sign; it refuses a signed project in its
	// pre-flight instead).
	src, rerr := os.ReadFile(filepath.Join("..", "storage", "project_rename_rewrite.go"))
	if rerr != nil {
		t.Fatalf("the named exception (the project rename writer) is gone or moved: %v; update this test and the rename pre-flight", rerr)
	}
	for _, want := range []string{"isManifest", `.manifest.json"`, "atomicfile.Write("} {
		if !strings.Contains(string(src), want) {
			t.Errorf("project_rename_rewrite.go no longer contains %q: the named manifest-writing exception moved; "+
				"revisit this test and storage.renameSignedManifestRefusals", want)
		}
	}
}

// bakGlob lists the preserved manifests (<manifest>.<tag>.bak) beside a manifest.
func bakGlob(t *testing.T, manifestPath string) []string {
	t.Helper()
	m, err := filepath.Glob(manifestPath + ".*.bak")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, p := range m {
		if !strings.HasSuffix(p, ".sig.bak") { // a preserved signature, not a manifest
			out = append(out, p)
		}
	}
	return out
}

// L1b (BLOCKER 1): a prior manifest that does not parse still has its signature.
// ReadManifest fails, so Create sees no prior manifest, but the .sig beside it
// must not stay beside the replacement. Signing is disabled for the call, so
// nothing else would remove it.
func TestCreate_MalformedSignedPriorManifestNeverLeavesAStaleSignature(t *testing.T) {
	f := newSignFix(t)
	first := f.mustCreate(t, "l1b", true)
	oldSig := mustRead(t, SignaturePath(first.ManifestPath))
	garbage := []byte("{ this is not a manifest\n")
	if err := os.WriteFile(first.ManifestPath, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(first.ManifestPath); err == nil {
		t.Fatal("fixture: the prior manifest must be unreadable")
	}

	second, err := f.create(t, "l1b", false)
	if err != nil {
		t.Fatalf("Create must not drop the capture over an unreadable prior manifest: %v", err)
	}
	if exists(SignaturePath(second.ManifestPath)) {
		t.Fatal("a live .sig sits beside the replacement manifest it does not sign (stale signature)")
	}
	if _, rerr := ReadManifest(second.ManifestPath); rerr != nil {
		t.Errorf("the replacement manifest is not readable: %v", rerr)
	}
	baks := bakGlob(t, second.ManifestPath)
	if len(baks) != 1 || !bytes.Equal(garbage, mustRead(t, baks[0])) {
		t.Fatalf("the unreadable prior manifest was not preserved to a .bak (found %v)", baks)
	}
	if !bytes.Equal(oldSig, mustRead(t, sigBackupPath(baks[0]))) {
		t.Error("the old signature was not preserved beside the backup as .sig.bak")
	}
}

// L1c: a signature with no manifest at all (an orphaned .sig) is also moved
// aside, to a name that records it, never left beside the new manifest.
func TestCreate_SignatureWithNoManifestIsMovedAside(t *testing.T) {
	f := newSignFix(t)
	first := f.mustCreate(t, "l1c", true)
	oldSig := mustRead(t, SignaturePath(first.ManifestPath))
	if err := os.Remove(first.ManifestPath); err != nil {
		t.Fatal(err)
	}

	second, err := f.create(t, "l1c", false)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if exists(SignaturePath(second.ManifestPath)) {
		t.Fatal("an orphaned .sig is still beside the new manifest")
	}
	sigs, _ := filepath.Glob(second.ManifestPath + ".*.sig.bak")
	if len(sigs) != 1 || !bytes.Equal(oldSig, mustRead(t, sigs[0])) {
		t.Errorf("the orphaned signature was not preserved under a recording name (found %v)", sigs)
	}
	if len(bakGlob(t, second.ManifestPath)) != 0 {
		t.Error("a .bak manifest was invented for a manifest that did not exist")
	}
}

// L5b (BLOCKER 2): the configured key is missing. The link must refuse BEFORE
// writing, with ErrSignedManifestCannotResign, rather than rewrite the manifest
// and fail later at the signer.
func TestLinkSessionNote_SignedWithAMissingKeyIsRefusedBeforeAnyWrite(t *testing.T) {
	f := newSignFix(t)
	res := f.mustCreate(t, "l5b", true)
	m0, s0 := mustRead(t, res.ManifestPath), mustRead(t, SignaturePath(res.ManifestPath))

	f.hostConfigKey(t, true, filepath.Join(f.tmp, "no-such-key"))
	// A signer that is reached at all is a failure of the pre-flight.
	swap(t, &signManifest, func(string, SignOptions) error { return errors.New("the signer must not be reached") })
	err := LinkSessionNote(f.vault, res.ManifestPath, signTestNote)
	if !errors.Is(err, ErrSignedManifestCannotResign) {
		t.Fatalf("err = %v, want ErrSignedManifestCannotResign (refused before any write)", err)
	}
	if errors.Is(err, ErrManifestSigning) {
		t.Errorf("the link reached the signer and rolled back instead of refusing up front: %v", err)
	}
	if !bytes.Equal(m0, mustRead(t, res.ManifestPath)) || !bytes.Equal(s0, mustRead(t, SignaturePath(res.ManifestPath))) {
		t.Error("a refused link changed the manifest or its signature")
	}
	if !strings.Contains(err.Error(), "no-such-key") {
		t.Errorf("the refusal must name the unusable key: %v", err)
	}
}

// The pre-flight's key checks, one per way a key can be unusable. gpg's resolution
// goes through the listSecretKeys seam (it signs nothing and needs no keyring).
func TestSigningKeyUnusable(t *testing.T) {
	f := newSignFix(t) // also proves ssh-keygen is present
	dir := t.TempDir()

	t.Run("ssh: a real key is usable", func(t *testing.T) {
		if r := signingKeyUnusable(SignOptions{Mode: SignModeSSH, Key: f.keyPath}); r != "" {
			t.Errorf("a real key was reported unusable: %s", r)
		}
	})
	for name, key := range map[string]string{
		"ssh: no key configured":  "",
		"ssh: missing key":        filepath.Join(dir, "nope"),
		"ssh: key is a directory": dir,
	} {
		t.Run(name, func(t *testing.T) {
			if r := signingKeyUnusable(SignOptions{Mode: SignModeSSH, Key: key}); r == "" {
				t.Error("an unusable ssh key was reported usable")
			}
		})
	}
	t.Run("ssh: an unreadable key", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("file permissions do not stop this reader here")
		}
		k := filepath.Join(dir, "unreadable")
		if err := os.WriteFile(k, []byte("x"), 0o000); err != nil {
			t.Fatal(err)
		}
		if r := signingKeyUnusable(SignOptions{Mode: SignModeSSH, Key: k}); r == "" {
			t.Error("an unreadable key was reported usable")
		}
	})
	t.Run("unknown mode", func(t *testing.T) {
		if r := signingKeyUnusable(SignOptions{Mode: "carrier-pigeon"}); r == "" {
			t.Error("an unknown signing mode was reported usable")
		}
	})
	t.Run("gpg", func(t *testing.T) {
		requireCmd(t, "gpg")
		swap(t, &listSecretKeys, func(string) ([]byte, error) { return nil, errors.New("gpg: no secret key") })
		if r := signingKeyUnusable(SignOptions{Mode: SignModeGPG, Key: "ABCD"}); r == "" {
			t.Error("a gpg key that does not resolve was reported usable")
		}
		swap(t, &listSecretKeys, func(string) ([]byte, error) { return []byte("tru::1:0:0:3:1:5\n"), nil })
		if r := signingKeyUnusable(SignOptions{Mode: SignModeGPG}); r == "" {
			t.Error("a keyring with no secret key was reported usable")
		}
		swap(t, &listSecretKeys, func(string) ([]byte, error) { return []byte("sec:u:255:22:ABCD:1:::u:::scESC:::+:::23::0:\n"), nil })
		if r := signingKeyUnusable(SignOptions{Mode: SignModeGPG, Key: "ABCD"}); r != "" {
			t.Errorf("a resolvable gpg secret key was reported unusable: %s", r)
		}
	})
}

// P2: when WriteManifest itself fails AFTER the prior pair moved to .bak/.sig.bak,
// the error states the exact on-disk state. The write is made to fail by making
// the transcripts directory read-only once the backups are done.
func TestCreateChangedSource_WriteFailureAfterBackupStatesTheOnDiskState(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("directory permissions do not stop the writer here")
	}
	f := newSignFix(t)
	first := f.mustCreate(t, "p2w", true)
	oldHash := mustManifest(t, mustRead(t, first.ManifestPath)).SourceSHA256
	bak := fmt.Sprintf("%s.%s.bak", first.ManifestPath, shortHash(oldHash))
	dir := filepath.Dir(first.ManifestPath)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	f.writeSource(t, sampleClaudeJSONL+`{"type":"user","message":{"role":"user","content":"p2w"}}`+"\n")
	swap(t, &renameForBackupHook, func(src, dst string) error {
		if err := vaultfs.RenameNoLock(src, dst); err != nil {
			return err
		}
		if strings.HasSuffix(dst, ".sig.bak") { // the last backup step: now break the write
			return os.Chmod(dir, 0o500)
		}
		return nil
	})
	_, err := f.create(t, "p2w", true)
	if err == nil {
		t.Fatal("want the write failure returned")
	}
	for _, want := range []string{
		"write manifest",
		"on disk now: the new archive " + first.ArchivePath + " is present",
		"the previous manifest is preserved at " + bak,
		"the previous signature is preserved at " + sigBackupPath(bak),
		"a live manifest at " + first.ManifestPath + " is absent",
		"a live signature at " + SignaturePath(first.ManifestPath) + " is absent",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error is missing %q: %v", want, err)
		}
	}
}

// gitIgnored reports whether git, run in repo, ignores path.
func gitIgnored(t *testing.T, repo, path string) bool {
	t.Helper()
	err := exec.Command("git", "-C", repo, "check-ignore", "-q", path).Run()
	if err == nil {
		return true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git check-ignore %s: %v", path, err)
	return false
}

// The preserved signatures must not become untracked dirt. The vault's canonical
// .gitignore (storage.CanonicalGitignorePatterns) covers "*.bak" and says nothing
// about ".sig", so a signature backup named "<...>.bak.sig" would be reported as
// dirt after every successful capture, and dirt blocks `vp vault sync`. The
// backups are therefore named "<...>.sig.bak". This test applies the REAL
// canonical patterns, in a real git repository, to both paths that create one.
func TestPreservedSignaturesAreCoveredByTheCanonicalVaultGitignore(t *testing.T) {
	f := newSignFix(t)
	if out, err := exec.Command("git", "-C", f.vault, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git is not usable here: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(f.vault, ".gitignore"), []byte(strings.Join(storage.CanonicalGitignorePatterns, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Path A: a changed-source re-create with a signed prior manifest.
	a := f.mustCreate(t, "gitA", true)
	f.writeSource(t, sampleClaudeJSONL+`{"type":"user","message":{"role":"user","content":"a"}}`+"\n")
	if _, err := f.create(t, "gitA", false); err != nil {
		t.Fatal(err)
	}
	// Path B: an orphaned signature (its manifest is gone).
	b := f.mustCreate(t, "gitB", true)
	if err := os.Remove(b.ManifestPath); err != nil {
		t.Fatal(err)
	}
	if _, err := f.create(t, "gitB", false); err != nil {
		t.Fatal(err)
	}

	var sigBaks []string
	for _, m := range []string{a.ManifestPath, b.ManifestPath} {
		g, _ := filepath.Glob(m + ".*.sig.bak")
		sigBaks = append(sigBaks, g...)
	}
	if len(sigBaks) != 2 {
		t.Fatalf("fixture: want one preserved signature per path, found %v", sigBaks)
	}
	bakManifests := bakGlob(t, a.ManifestPath)
	if len(bakManifests) != 1 {
		t.Fatalf("fixture: want one preserved manifest on path A, found %v", bakManifests)
	}
	for _, p := range append(append([]string{}, sigBaks...), bakManifests...) {
		if !gitIgnored(t, f.vault, p) {
			t.Errorf("git does not ignore %s under the canonical vault patterns: it would be untracked dirt", p)
		}
	}

	out, err := exec.Command("git", "-C", f.vault, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal(err)
	}
	status := string(out)
	if strings.Contains(status, ".bak") {
		t.Errorf("a backup shows up as untracked dirt:\n%s", status)
	}
	// Control, so this cannot pass vacuously: the new manifests ARE untracked...
	if !strings.Contains(status, filepath.Base(a.ManifestPath)) {
		t.Errorf("control: the live manifest should be listed as untracked:\n%s", status)
	}
	// ...and the old naming would have been dirt too.
	oldName := strings.TrimSuffix(sigBaks[0], ".sig.bak") + ".bak.sig"
	if err := os.WriteFile(oldName, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if gitIgnored(t, f.vault, oldName) {
		t.Error("control: a '.bak.sig' name is ignored too, so this test would not catch the old naming")
	}
}
