// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func caHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// caFix builds a migrated source vault holding project `p` (published to a bare
// remote) and a migrated destination vault with its own remote.
func caFix(t *testing.T) (dest, srcURL string) {
	t.Helper()
	caHome(t)
	_, srcBare := vcRepo(t, map[string]string{
		"Projects/p/resume.md":                    "---\nproject: p\n---\n# p — Working Context\nstuff\n",
		"Projects/p/sessions/2026-10-05-ab-01.md": "---\nproject: p\n---\nbody\n",
		"palace/p/kg/entities.jsonl":              "{}\n",
	})
	dest, _ = vcRepo(t, map[string]string{"Projects/resident/resume.md": "r\n"})
	return dest, "file://" + srcBare
}

func caReq(dest, srcURL string, projects ...string) storage.CopyRequest {
	return storage.CopyRequest{Vault: dest, Projects: projects, From: srcURL}
}

func caPresent(dir, rel string) bool {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return err == nil
}

// TestCopyProjectAs_HappyPath: copy p --as newp → dest holds newp, not p; p
// departed renamed→newp; both commits; source untouched; record cleared.
func TestCopyProjectAs_HappyPath(t *testing.T) {
	dest, srcURL := caFix(t)
	res, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "newp")
	if err != nil {
		t.Fatalf("CopyProjectAs: %v", err)
	}
	if res.Outcome != CopyAsCopiedAndRenamed {
		t.Fatalf("outcome = %q, want copied-and-renamed", res.Outcome)
	}
	if !caPresent(dest, "Projects/newp/resume.md") || !caPresent(dest, "palace/newp/kg/entities.jsonl") {
		t.Error("newp footprint missing in the destination")
	}
	if caPresent(dest, "Projects/p") || caPresent(dest, "palace/p") {
		t.Error("the old slug p survived in the destination")
	}
	rec, found := departure.Read(dest, "p")
	if !found || rec.Kind != departure.Renamed || rec.To != "newp" {
		t.Fatalf("p departure record: found=%v %+v", found, rec)
	}
	if res.Copy == nil || res.Copy.Commit == "" || res.Rename == nil || res.Rename.Commit == "" {
		t.Errorf("want both a copy and a rename commit, got %+v", res)
	}
	// SF2: the rename-pending record is cleared (fail-able), replacing the
	// vacuous "no index/p leaks".
	if _, ok, _ := storage.NewVault(dest).RenamePending("p"); ok {
		t.Error("the rename-pending record was not cleared")
	}
}

// TestCopyProjectAs_SingleProjectOnly: --as with two projects refuses.
func TestCopyProjectAs_SingleProjectOnly(t *testing.T) {
	dest, srcURL := caFix(t)
	_, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p", "q"), "newp")
	if err == nil {
		t.Fatal("want a refusal for two projects with --as")
	}
}

// TestCopyProjectAs_SameNameRefused: --as equal to the project refuses.
func TestCopyProjectAs_SameNameRefused(t *testing.T) {
	dest, srcURL := caFix(t)
	_, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "p")
	if err == nil {
		t.Fatal("want a refusal when --as equals the project")
	}
}

// TestCopyProjectAs_DryRunWritesNothing: --dry-run previews the copy and writes
// nothing (no copy, no rename).
func TestCopyProjectAs_DryRunWritesNothing(t *testing.T) {
	dest, srcURL := caFix(t)
	req := caReq(dest, srcURL, "p")
	req.DryRun = true
	res, err := CopyProjectAs(context.Background(), storage.NewVault(dest), req, "newp")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.Outcome != CopyAsDryRun {
		t.Fatalf("outcome = %q, want dry-run", res.Outcome)
	}
	if res.Copy == nil || res.Copy.Plan == nil || res.Copy.Plan.Digest == "" {
		t.Error("dry run did not return a copy plan with a digest")
	}
	if caPresent(dest, "Projects/p") || caPresent(dest, "Projects/newp") {
		t.Error("the dry run wrote to the destination")
	}
}

// TestCopyProjectAs_NewNameAlreadyPresentRefusesBeforeCopy: an inconsistent
// destination (newp present without a renamed record) is branch (d) ambiguous —
// refuse before any copy, so p is never copied.
func TestCopyProjectAs_NewNameAlreadyPresentRefusesBeforeCopy(t *testing.T) {
	dest, srcURL := caFix(t)
	vcGit(t, dest, "init", "-q") // no-op; dest already a repo
	if err := os.MkdirAll(filepath.Join(dest, "Projects", "newp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "Projects", "newp", "resume.md"), []byte("squatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vcGit(t, dest, "add", "-A")
	vcGit(t, dest, "commit", "-q", "-m", "newp already here")
	vcGit(t, dest, "push", "-q", "origin", "main")

	_, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "newp")
	if err == nil {
		t.Fatal("want an ambiguous refusal when newp already exists")
	}
	if caPresent(dest, "Projects/p") {
		t.Error("p was copied despite the up-front refusal")
	}
}

// TestCopyProjectAs_ResumeAfterCopyDone: the copy is already present (published,
// no marker); --as runs only the rename (gate branch b).
func TestCopyProjectAs_ResumeAfterCopyDone(t *testing.T) {
	dest, srcURL := caFix(t)
	// Land the copy alone, as a published-but-not-yet-renamed state.
	if _, err := storage.ApplyCopy(caReq(dest, srcURL, "p")); err != nil {
		t.Fatalf("seed copy: %v", err)
	}
	if !caPresent(dest, "Projects/p") {
		t.Fatal("seed copy did not land p")
	}
	res, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "newp")
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if res.Outcome != CopyAsResumed {
		t.Fatalf("outcome = %q, want resumed-rename", res.Outcome)
	}
	if res.Copy != nil {
		t.Error("resume re-ran the copy")
	}
	if !caPresent(dest, "Projects/newp") || caPresent(dest, "Projects/p") {
		t.Error("resume did not rename p to newp")
	}
	// A second --as on the completed state is an idempotent no-op.
	res2, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "newp")
	if err != nil {
		t.Fatalf("second --as: %v", err)
	}
	if res2.Outcome != CopyAsAlreadyDone {
		t.Errorf("second --as outcome = %q, want already-done", res2.Outcome)
	}
}

// TestCopyProjectAs_ResidueDepartedRoutesToCopyRefusal (SF1): a <project> that
// departed in the destination, leaving only untracked residue + its departure
// record (no tracked content), must read ABSENT — a fresh `--as` routes to the
// FULL copy and gets the clear copy-side departure-record refusal, NOT a
// confusing rename "nothing is tracked" refusal from a mis-routed RESUME.
func TestCopyProjectAs_ResidueDepartedRoutesToCopyRefusal(t *testing.T) {
	dest, srcURL := caFix(t)
	// Residue under Projects/p: an untracked leftover file (not tracked content),
	// plus a committed departure record for p — the shape a prior delete leaves.
	if err := os.MkdirAll(filepath.Join(dest, "Audits", "departures"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "Audits", "departures", "p.json"),
		[]byte(`{"format":"vp-departure/1","slug":"p","kind":"deleted","date":"2026-10-05"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Commit only the record; leave an untracked residue file under Projects/p,
	// so the tree dir lingers on disk with NO tracked content.
	vcGit(t, dest, "add", "Audits/departures/p.json")
	vcGit(t, dest, "commit", "-q", "-m", "p departed")
	vcGit(t, dest, "push", "-q", "origin", "main")
	if err := os.MkdirAll(filepath.Join(dest, "Projects", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "Projects", "p", "resume.md.bak"), []byte("leftover\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "newp")
	if err == nil {
		t.Fatal("want a refusal for a departed p")
	}
	msg := err.Error()
	if !strings.Contains(msg, "departure record") {
		t.Errorf("want the clear copy-side departure-record refusal, got: %v", msg)
	}
	if strings.Contains(msg, "nothing is tracked") || strings.Contains(msg, "no project to rename") {
		t.Errorf("mis-routed to a rename refusal (residue read as present): %v", msg)
	}
}

// TestVaultCopyTool_As (SF2 parity): the MCP tool's `as` path routes through the
// same CopyProjectAs as the CLI, so both run the baseline step in the shared
// core. Plan writes nothing; apply copies-then-renames.
func TestVaultCopyTool_As(t *testing.T) {
	dest, srcURL := caFix(t)
	tool := VaultCopyTool(storage.NewVault(dest))

	plan, err := tool.Handler(context.Background(), json.RawMessage(`{"action":"plan","slugs":["p"],"from":`+jsonStr(srcURL)+`,"as":"newp"}`))
	if err != nil {
		t.Fatalf("plan --as: %v", err)
	}
	if pr := plan.(*vaultCopyResult); pr.CopyAs == nil || pr.CopyAs.Outcome != CopyAsDryRun {
		t.Fatalf("plan result %+v", pr)
	}
	if caPresent(dest, "Projects/p") || caPresent(dest, "Projects/newp") {
		t.Fatal("plan --as wrote to the vault")
	}

	out, err := tool.Handler(context.Background(), json.RawMessage(`{"action":"apply","slugs":["p"],"from":`+jsonStr(srcURL)+`,"as":"newp"}`))
	if err != nil {
		t.Fatalf("apply --as: %v", err)
	}
	res := out.(*vaultCopyResult)
	if res.CopyAs == nil || res.CopyAs.Outcome != CopyAsCopiedAndRenamed {
		t.Fatalf("apply result %+v", res)
	}
	if !caPresent(dest, "Projects/newp") || caPresent(dest, "Projects/p") {
		t.Error("the MCP --as path did not copy-then-rename")
	}
	// Parity: BaselineWarnings comes from the shared core (nil/empty here, no
	// archives), surfaced identically to the CLI path.
	if res.BaselineWarnings == nil && res.CopyAs.BaselineWarnings != nil {
		t.Error("the tool did not surface the shared core's baseline warnings")
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

// TestCopyProjectAs_AmbiguousResidualRefused (SF-a): the residual state
// (project PRESENT + newname PRESENT + a renamed→newname record) must be branch
// (d) AMBIGUOUS, NOT branch (a) DONE — branch (a) requires the project ABSENT.
func TestCopyProjectAs_AmbiguousResidualRefused(t *testing.T) {
	dest, srcURL := caFix(t)
	// Complete a real --as (p → newp, record written, p gone).
	if _, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "newp"); err != nil {
		t.Fatalf("seed --as: %v", err)
	}
	// Re-create Projects/p beside the completed rename → the residual state.
	if err := os.MkdirAll(filepath.Join(dest, "Projects", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "Projects", "p", "resume.md"), []byte("re-created\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vcGit(t, dest, "add", "-A")
	vcGit(t, dest, "commit", "-q", "-m", "p re-created beside the completed rename")
	vcGit(t, dest, "push", "-q", "origin", "main")

	if rec, found := departure.Read(dest, "p"); !found || rec.To != "newp" {
		t.Fatalf("precondition: p's record should name newp: %+v", rec)
	}
	_, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "newp")
	if err == nil {
		t.Fatal("want an ambiguous refusal for the residual (p present + newp present + record), not a done no-op")
	}
}

// TestCopyProjectAs_MarkerPendingResume: a copy committed but publish-interrupted
// (a diverged second remote keeps the commit + its copy marker). A re-run of
// --as must route to ApplyCopy's redo (finish the publish), then rename — not
// trip the rename's marker guard (SF1a).
func TestCopyProjectAs_MarkerPendingResume(t *testing.T) {
	dest, srcURL := caFix(t)
	// A second destination remote at the seed (the copy commit's parent), then a
	// pre-receive hook that rejects the push — so the copy lands on origin,
	// confirms there, but fails to confirm the mirror and keeps commit + marker.
	mirror := t.TempDir()
	vcGit(t, mirror, "init", "-q", "--bare", "-b", "main")
	vcGit(t, mirror, "config", "uploadpack.allowFilter", "true")
	vcGit(t, dest, "remote", "add", "mirror", "file://"+mirror)
	vcGit(t, dest, "push", "-q", "mirror", "main")
	hook := filepath.Join(mirror, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho refused >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "newp")
	if err == nil {
		t.Fatal("want a publish failure while the mirror rejects")
	}
	// The copy committed and its marker stands.
	if !caPresent(dest, "Projects/p") {
		t.Fatal("the copy commit did not land on the first remote")
	}
	cmd, found, cerr := storage.PendingLifecycleCommand(dest)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if !found || cmd != storage.CopyCommandName {
		t.Fatalf("want a standing copy marker, got cmd=%q found=%v", cmd, found)
	}

	// Fix the mirror and re-run: the gate routes to the copy's redo, then renames.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	res, err := CopyProjectAs(context.Background(), storage.NewVault(dest), caReq(dest, srcURL, "p"), "newp")
	if err != nil {
		t.Fatalf("resume after marker-pending copy: %v", err)
	}
	if !caPresent(dest, "Projects/newp") || caPresent(dest, "Projects/p") {
		t.Error("the resume did not finish the publish and rename to newp")
	}
	if rec, found := departure.Read(dest, "p"); !found || rec.To != "newp" {
		t.Errorf("p not departed renamed→newp after the resume: %+v", rec)
	}
	if cmd, found, _ := storage.PendingLifecycleCommand(dest); found {
		t.Errorf("a marker still stands after the resume: %q", cmd)
	}
	_ = res
}
