// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// U7 scenario 5: a whole move (copy into Q, delete from P) in which each
// command is killed and then simply re-run, with no manual step. The kills use
// the commands' own seams (copyTestHook, deleteAfterRecords,
// deleteBeforePublish), which stop a run the way a killed process stops:
// nothing rolled back, the marker left standing. The CLI-driven scenarios
// live in internal/integration/lifecycle_e2e_test.go; a kill needs these
// in-process seams.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// moveWorld is P with its remote, and Q with two remotes, as the admin host
// holds them.
type moveWorld struct {
	P, PBare         string
	Q, QOrigin, QMir string
}

func newMoveWorld(t *testing.T) *moveWorld {
	t.Helper()
	w := &moveWorld{}
	w.P = initTestRepo(t)
	home := os.Getenv("HOME")
	w.PBare = initBareRemote(t)
	for _, b := range []string{w.PBare} {
		gitRun(t, b, "config", "uploadpack.allowFilter", "true")
		gitRun(t, b, "config", "uploadpack.allowAnySHA1InWant", "true")
	}
	writeFile(t, w.P, ".vibe-palace/vault.toml", formatManifest())
	writeFile(t, w.P, ".gitignore", "*.bak\npalace/.local/\n")
	writeFile(t, w.P, "Projects/qms/resume.md", "# qms\n")
	writeFile(t, w.P, "Projects/qms/sessions/s1.md", "a session\n")
	writeFile(t, w.P, "palace/qms/kg/entities.jsonl", "{}\n")
	writeFile(t, w.P, "Projects/orch/resume.md", "# orch\n")
	writeFile(t, w.P, "Projects/stays/resume.md", "# stays\n")
	gitRun(t, w.P, "add", "-A")
	gitRun(t, w.P, "commit", "-q", "-m", "personal vault")
	gitRun(t, w.P, "remote", "add", "github", fileURL(w.PBare))
	gitRun(t, w.P, "push", "-q", "github", "main")
	// An ignored leftover, which the delete removes after its publish.
	writeFile(t, w.P, "Projects/qms/transcripts/x.manifest.json.1.bak", "old\n")

	w.Q = initTestRepo(t)
	t.Setenv("HOME", home)
	w.QOrigin, w.QMir = initBareRemote(t), initBareRemote(t)
	for _, b := range []string{w.QOrigin, w.QMir} {
		gitRun(t, b, "config", "uploadpack.allowFilter", "true")
		gitRun(t, b, "config", "uploadpack.allowAnySHA1InWant", "true")
	}
	writeFile(t, w.Q, ".vibe-palace/vault.toml", formatManifest())
	gitRun(t, w.Q, "add", "-A")
	gitRun(t, w.Q, "commit", "-q", "-m", "quantum vault")
	gitRun(t, w.Q, "remote", "add", "origin", fileURL(w.QOrigin))
	gitRun(t, w.Q, "remote", "add", "github", fileURL(w.QMir))
	gitRun(t, w.Q, "push", "-q", "origin", "main")
	gitRun(t, w.Q, "push", "-q", "github", "main")
	return w
}

func (w *moveWorld) copyReq() CopyRequest {
	return CopyRequest{Vault: w.Q, Projects: []string{"qms", "orch"}, From: fileURL(w.PBare)}
}

func (w *moveWorld) deleteReq() DeleteRequest {
	return DeleteRequest{Projects: []string{"qms", "orch"}, MovedTo: fileURL(w.QOrigin)}
}

// 5a. The copy is killed mid-copy, then re-run: the re-run rolls the partial
// trees back and copies afresh; then the delete is killed after its commit,
// then re-run: the re-run publishes that same commit and removes the leftovers.
func TestLifecycleMoveKilledTwiceFinishesOnReRun(t *testing.T) {
	w := newMoveWorld(t)

	withCopyHook(t, func(stage string, i int, _ string) error {
		if stage == "copied" && i == 1 {
			return errCopySimulatedKill
		}
		return nil
	})
	if _, err := ApplyCopy(w.copyReq()); !errors.Is(err, errCopySimulatedKill) {
		t.Fatalf("copy: err = %v", err)
	}
	if !markerFound(t, w.Q) {
		t.Fatal("the killed copy left no marker")
	}
	copyTestHook = nil
	cres, err := ApplyCopy(w.copyReq())
	if err != nil {
		t.Fatalf("the copy re-run refused: %v", err)
	}
	if cres.Redo != RedoRolledBack || cres.Commit == "" {
		t.Fatalf("copy re-run: redo %s, commit %q", cres.Redo, cres.Commit)
	}
	for _, b := range []string{w.QOrigin, w.QMir} {
		if gitRun(t, b, "rev-parse", "main") != cres.Commit {
			t.Fatalf("%s is not at the copy commit", b)
		}
	}

	var delCommit string
	seam(t, &deleteBeforePublish, func() error {
		delCommit = gitRun(t, w.P, "rev-parse", "HEAD")
		return errKilled
	})
	if _, err := ApplyDelete(w.P, w.deleteReq()); !errors.Is(err, errKilled) {
		t.Fatalf("delete: err = %v", err)
	}
	if !markerFound(t, w.P) || !pathPresent(filepath.Join(w.P, "Projects/qms/transcripts/x.manifest.json.1.bak")) {
		t.Fatal("the killed delete left no marker, or removed the leftovers before publishing")
	}
	deleteBeforePublish = func() error { return nil }
	dres, err := ApplyDelete(w.P, w.deleteReq())
	if err != nil {
		t.Fatalf("the delete re-run refused: %v", err)
	}
	if dres.Redo != RedoPublished || gitRun(t, w.PBare, "rev-parse", "main") != delCommit {
		t.Fatalf("delete re-run: redo %s; P's remote must hold the killed run's commit %s", dres.Redo, delCommit)
	}
	if pathPresent(filepath.Join(w.P, "Projects/qms/transcripts/x.manifest.json.1.bak")) || markerFound(t, w.P) {
		t.Fatal("the re-run did not finish: leftovers or marker remain")
	}
}

// 5b. The delete is killed after writing its records and before its commit;
// the re-run rolls those records back and deletes afresh.
func TestLifecycleDeleteKilledBeforeItsCommitFinishesOnReRun(t *testing.T) {
	w := newMoveWorld(t)
	if _, err := ApplyCopy(w.copyReq()); err != nil {
		t.Fatal(err)
	}
	parent := gitRun(t, w.P, "rev-parse", "HEAD")
	seam(t, &deleteAfterRecords, func() error { return errKilled })
	if _, err := ApplyDelete(w.P, w.deleteReq()); !errors.Is(err, errKilled) {
		t.Fatalf("err = %v", err)
	}
	deleteAfterRecords = func() error { return nil }
	res, err := ApplyDelete(w.P, w.deleteReq())
	if err != nil {
		t.Fatalf("the re-run refused: %v", err)
	}
	if res.Redo != RedoRolledBack || res.Commit == "" || gitRun(t, w.P, "rev-parse", res.Commit+"~1") != parent {
		t.Fatalf("redo %s commit %q", res.Redo, res.Commit)
	}
	if gitRun(t, w.PBare, "rev-parse", "main") != res.Commit {
		t.Fatal("the redone delete is not published")
	}
}
