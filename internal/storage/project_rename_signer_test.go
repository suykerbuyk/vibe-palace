// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
)

// recordingSigner is the storage tests' ManifestSigner: a plain atomic write (no
// signing) that records every call, so existing rename tests keep their meaning
// and the seam's dispatch can be counted.
type recordingSigner struct {
	mu        sync.Mutex
	preflight int
	refusal   string
	snapshot  any
	dests     []string
	snaps     []any
	failOn    int // 1-based manifest index to fail, 0 = never
	failErr   error
}

func (s *recordingSigner) Preflight(vaultRoot, from string) (any, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preflight++
	return s.snapshot, s.refusal
}

func (s *recordingSigner) RewriteManifest(vaultRoot, destAbs string, data []byte, snapshot any) error {
	s.mu.Lock()
	s.dests = append(s.dests, destAbs)
	s.snaps = append(s.snaps, snapshot)
	n := len(s.dests)
	s.mu.Unlock()
	if s.failOn == n {
		return s.failErr
	}
	return atomicfile.Write(vaultRoot, destAbs, data, atomicfile.WithInheritPerm(), atomicfile.WithFsync())
}

// A signed project with no refusal from the signer plans and renames, and the
// snapshot Preflight returned reaches every RewriteManifest unchanged.
func TestRename_SignedProjectSnapshotReachesEveryRewrite(t *testing.T) {
	f := newRenFix(t)
	writeFile(t, f.V, "Projects/old/transcripts/y.manifest.json",
		"{\n  \"project_slug\": \"old\",\n  \"vault_rel_session_note\": \"Projects/old/sessions/2026-10-05-ab-01.md\"\n}\n")
	writeFile(t, f.V, "Projects/old/transcripts/y.manifest.json.sig", "-----BEGIN SSH SIGNATURE-----\nfake\n-----END SSH SIGNATURE-----\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "signed")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	sg := &recordingSigner{snapshot: "SNAP"}
	req := f.req()
	req.Signer = sg
	if _, err := ApplyRename(req); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if sg.preflight != 1 {
		t.Errorf("Preflight calls = %d, want 1", sg.preflight)
	}
	if len(sg.dests) != 2 {
		t.Fatalf("RewriteManifest calls = %d, want 2 (one per manifest)", len(sg.dests))
	}
	for i, d := range sg.dests {
		if !strings.Contains(filepath.ToSlash(d), "/Projects/new/transcripts/") {
			t.Errorf("manifest %d reached the signer with a non-destination path %s", i, d)
		}
		if sg.snaps[i] != "SNAP" {
			t.Errorf("manifest %d got snapshot %v, want the Preflight snapshot", i, sg.snaps[i])
		}
	}
}

// A signer's Preflight refusal joins the plan's refusals: PlanRename shows it and
// ApplyRename refuses before the marker, with the tree untouched.
func TestRename_PreflightRefusalIsAPlanRefusal(t *testing.T) {
	f := newRenFix(t)
	writeFile(t, f.V, "Projects/old/transcripts/x.manifest.json.sig", "sig\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "signed")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	sg := &recordingSigner{refusal: "signing is not usable: PROBE-REASON"}
	req := f.req()
	req.Signer = sg

	plan, perr := PlanRename(req)
	if perr == nil || plan == nil || !strings.Contains(strings.Join(plan.Refusals, "\n"), "PROBE-REASON") {
		t.Fatalf("the dry-run plan did not carry the preflight refusal: plan=%+v err=%v", plan, perr)
	}
	_, err := ApplyRename(req)
	assertRefused(t, err, "PROBE-REASON")
	var re *RenameRefusalsError
	if !errors.As(err, &re) {
		t.Fatalf("want *RenameRefusalsError, got %T", err)
	}
	if renPresent(f.V, "Projects/new/resume.md") || !renPresent(f.V, "Projects/old/resume.md") {
		t.Error("the tree changed despite the refusal")
	}
	if len(sg.dests) != 0 {
		t.Error("a manifest was written despite the refusal")
	}
	if got := gitRun(t, f.V, "rev-parse", "HEAD"); got != head {
		t.Error("HEAD moved")
	}
	if st := gitRun(t, f.V, "status", "--porcelain", "--", ".", ":!.vp-locks"); strings.TrimSpace(st) != "" {
		t.Errorf("git status not clean after refusal:\n%s", st)
	}
	if _, found, _ := readLifecycleMarker(f.V); found {
		t.Error("a lifecycle marker was written")
	}
}

// Nil Signer with any manifest refuses (fail closed: storage has no other writer).
func TestRename_NilSignerRefusesAnyManifest(t *testing.T) {
	f := newRenFix(t)
	req := f.req()
	req.Signer = nil
	_, err := ApplyRename(req)
	assertRefused(t, err, "rename needs a manifest writer")
	if !renPresent(f.V, "Projects/old/resume.md") {
		t.Error("the project moved despite the refusal")
	}
	// A signed project refuses too, and writes nothing.
	writeFile(t, f.V, "Projects/old/transcripts/x.manifest.json.sig", "sig\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "signed")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	_, err = ApplyRename(req)
	assertRefused(t, err, "rename needs a manifest writer")
}

// B9 storage pin (a): in slugScanClasses a manifest is dispatched ONLY through the
// seam; the raw loop carries a production guard.
func TestRename_ManifestsGoOnlyThroughTheSeam(t *testing.T) {
	// (b) callback count: N manifests, N RewriteManifest calls with destination paths.
	f := newRenFix(t)
	writeFile(t, f.V, "Projects/old/transcripts/y.manifest.json",
		"{\n  \"project_slug\": \"old\",\n  \"vault_rel_session_note\": \"Projects/old/sessions/2026-10-05-ab-01.md\"\n}\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "two manifests")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	sg := &recordingSigner{}
	req := f.req()
	req.Signer = sg
	if _, err := ApplyRename(req); err != nil {
		t.Fatal(err)
	}
	if len(sg.dests) != 2 {
		t.Fatalf("seam calls = %d, want 2", len(sg.dests))
	}
	// (a) the guard: the raw loop refuses a manifest entry, and a nil signer cannot write one.
	err := (renameSigning{}).rewriteManifest(t.TempDir(), "old", "new", "x", "x", nil)
	if err == nil || !strings.Contains(err.Error(), errRawManifestWrite.Error()) {
		t.Errorf("a manifest with no signer must hit errRawManifestWrite, got %v", err)
	}
	// AST: in slugScanClasses the only atomicfile.Write sits in a branch that is
	// guarded by `w.manifest` having already `continue`d.
	fset := token.NewFileSet()
	file, perr := parser.ParseFile(fset, "project_rename_rewrite.go", nil, 0)
	if perr != nil {
		t.Fatal(perr)
	}
	sawManifestDispatch := false
	ast.Inspect(file, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != "slugScanClasses" {
			return true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "rewriteManifest" {
				sawManifestDispatch = true
			}
			return true
		})
		return false
	})
	if !sawManifestDispatch {
		t.Error("slugScanClasses no longer dispatches manifests through rewriteManifest")
	}
}

// T3b: every manifest was handed to the signer, then the run fails at the
// "rewritten" seam, still before the commit. The OUTER rollback (vacate the
// destination, then check the source out of HEAD) puts the old tree back; neither
// `reset --keep` (a no-op on a move) nor any per-manifest restore does.
func TestRename_FailureAfterEveryManifestWasRewrittenRollsBackToHEAD(t *testing.T) {
	f := newRenFix(t)
	writeFile(t, f.V, "Projects/old/transcripts/x.manifest.json.sig", "-----BEGIN SSH SIGNATURE-----\nfake\n-----END SSH SIGNATURE-----\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "signed")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	sg := &recordingSigner{}
	req := f.req()
	req.Signer = sg
	old := renameTestHook
	renameTestHook = func(stage string) error {
		if stage == "rewritten" {
			return errors.New("injected failure after the rewrite")
		}
		return nil
	}
	defer func() { renameTestHook = old }()

	_, err := ApplyRename(req)
	if err == nil || !strings.Contains(err.Error(), "injected failure after the rewrite") {
		t.Fatalf("want the injected failure, got %v", err)
	}
	if len(sg.dests) != 1 {
		t.Fatalf("the signer saw %d manifests, want 1 (all rewritten before the failure)", len(sg.dests))
	}
	if got := gitRun(t, f.V, "rev-parse", "HEAD"); got != head {
		t.Error("a commit was made")
	}
	for _, rel := range strings.Fields(gitRun(t, f.V, "ls-tree", "-r", "--name-only", "HEAD", "--", "Projects/old")) {
		want := gitRun(t, f.V, "show", "HEAD:"+rel)
		got, rerr := os.ReadFile(filepath.Join(f.V, filepath.FromSlash(rel)))
		if rerr != nil || strings.TrimSpace(string(got)) != want {
			t.Errorf("%s differs from HEAD after the rollback (err=%v)", rel, rerr)
		}
	}
	if renPresent(f.V, "Projects/new") || renPresent(f.V, "palace/new") {
		t.Error("the destination survived the rollback")
	}
	if st := gitRun(t, f.V, "status", "--porcelain", "--", ".", ":!.vp-locks"); strings.TrimSpace(st) != "" {
		t.Errorf("git status not clean:\n%s", st)
	}
	if _, found, _ := readLifecycleMarker(f.V); found {
		t.Error("the lifecycle marker stuck")
	}
}

// T3c: the run fails between the commit and the marker's record of it (stage
// "committed"). The in-memory marker already names the commit, so the rollback
// resets to the parent and restores the old tree, .sig included.
func TestRename_FailureAfterTheCommitBeforeTheMarkerRecordsItRollsBackToParent(t *testing.T) {
	f := newRenFix(t)
	writeFile(t, f.V, "Projects/old/transcripts/x.manifest.json.sig", "-----BEGIN SSH SIGNATURE-----\nfake\n-----END SSH SIGNATURE-----\n")
	gitRun(t, f.V, "add", "-A")
	gitRun(t, f.V, "commit", "-q", "-m", "signed")
	gitRun(t, f.V, "push", "-q", "origin", "main")
	head := gitRun(t, f.V, "rev-parse", "HEAD")
	req := f.req()
	req.Signer = &recordingSigner{}
	old := renameTestHook
	renameTestHook = func(stage string) error {
		if stage == "committed" {
			return errors.New("injected failure after the commit")
		}
		return nil
	}
	defer func() { renameTestHook = old }()

	_, err := ApplyRename(req)
	if err == nil || !strings.Contains(err.Error(), "injected failure after the commit") {
		t.Fatalf("want the injected failure, got %v", err)
	}
	if strings.Contains(err.Error(), "rolling back also failed") {
		t.Errorf("the rollback itself failed: %v", err)
	}
	if got := gitRun(t, f.V, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD is %s, want the pre-rename parent %s", got, head)
	}
	files := strings.Fields(gitRun(t, f.V, "ls-tree", "-r", "--name-only", "HEAD", "--", "Projects/old"))
	sawSig := false
	for _, rel := range files {
		sawSig = sawSig || strings.HasSuffix(rel, ".sig")
		want := gitRun(t, f.V, "show", "HEAD:"+rel)
		got, rerr := os.ReadFile(filepath.Join(f.V, filepath.FromSlash(rel)))
		if rerr != nil || strings.TrimSpace(string(got)) != want {
			t.Errorf("%s differs from HEAD after the rollback (err=%v)", rel, rerr)
		}
	}
	if !sawSig {
		t.Error("fixture: no .sig under Projects/old")
	}
	if renPresent(f.V, "Projects/new") || renPresent(f.V, "palace/new") {
		t.Error("the destination survived the rollback")
	}
	if _, found, _ := readLifecycleMarker(f.V); found {
		t.Error("the lifecycle marker stuck")
	}
}
