// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

// ONE-SHOT: deleted with cmd_migrate_authored_only.go.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/palace"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// authoredMigrationVault builds a populated, pushed vault for the migration
// command: project alpha with a tracked drawer, an authored and an extracted
// triple, and a committed surface stamp at MCPSurfaceVersion under
// Projects/alpha/.surface, wired to a bare origin.
func authoredMigrationVault(t *testing.T) (root, bare string) {
	t.Helper()
	setupTestVaultEnv(t)
	root, bare = newRepoWithOrigin(t)
	mkfile(t, root, ".vibe-palace/vault.toml", fmt.Sprintf("format = %d\n", surface.RequiredDataFormat))
	mkfile(t, root, "Projects/alpha/resume.md", "# alpha\n")
	mkfile(t, root, "palace/alpha/drawers/d1.md", "drawer\n")
	mkfile(t, root, "palace/alpha/kg/triples/authored.json", `{"subject":"a","predicate":"uses","object":"b"}`+"\n")
	mkfile(t, root, "palace/alpha/kg/triples/extracted.json", `{"subject":"c","predicate":"uses","object":"d","extracted_at":"2026-01-01","source_session":"s1"}`+"\n")
	mkfile(t, root, "Projects/alpha/.surface", fmt.Sprintf("surface = %d\n", surface.MCPSurfaceVersion))
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "populate")
	gitRun(t, root, "push", "-q", "origin", "main")
	return root, bare
}

// writeAttest writes a valid attestation file and returns its path.
func writeAttest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "attest.txt")
	body := fmt.Sprintf("writer-hosts: hostA=v%d.%d.0@2026-10-09\nquantum-ng-rows-closed: 2026-10-08\n",
		surface.MCPSurfaceVersion, surface.RequiredDataFormat)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runAO(t *testing.T, root string, apply bool, attest string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runAuthoredOnlyMigration(root, apply, attest, time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), &out, &errb)
	return code, out.String(), errb.String()
}

// TestAOAttestationRequiredOnApply is "Attestation input" (12-S3): --yes without
// --attest refuses and prints the required lines; the plan prints them too.
func TestAOAttestationRequiredOnApply(t *testing.T) {
	root, _ := authoredMigrationVault(t)
	code, _, errb := runAO(t, root, true, "")
	if code != cli.ExitUser {
		t.Fatalf("exit = %d, want refusal; stderr:\n%s", code, errb)
	}
	if !strings.Contains(errb, "requires --attest") || !strings.Contains(errb, "writer-hosts:") {
		t.Errorf("refusal should name --attest and the required lines; got:\n%s", errb)
	}
	// The plan prints the two lines.
	_, out, _ := runAO(t, root, false, "")
	if !strings.Contains(out, "writer-hosts:") || !strings.Contains(out, "quantum-ng-rows-closed:") {
		t.Errorf("plan should print the two attestation lines; got:\n%s", out)
	}
}

// TestAOAttestationUnreadableAndMissingRefuse covers the unreadable-file and
// missing-line cases.
func TestAOAttestationUnreadableAndMissingRefuse(t *testing.T) {
	root, _ := authoredMigrationVault(t)
	// Unreadable.
	code, _, errb := runAO(t, root, true, filepath.Join(t.TempDir(), "nope.txt"))
	if code != cli.ExitUser || !strings.Contains(errb, "attestation") {
		t.Errorf("unreadable attestation should refuse; exit %d, stderr:\n%s", code, errb)
	}
	// Missing quantum-ng line.
	bad := filepath.Join(t.TempDir(), "bad.txt")
	os.WriteFile(bad, []byte(fmt.Sprintf("writer-hosts: h=v%d.%d.0@2026-10-09\n", surface.MCPSurfaceVersion, surface.RequiredDataFormat)), 0o644)
	code, _, errb = runAO(t, root, true, bad)
	if code != cli.ExitUser || !strings.Contains(errb, "quantum-ng-rows-closed") {
		t.Errorf("missing quantum-ng line should refuse naming it; exit %d, stderr:\n%s", code, errb)
	}
}

// TestAOAttestationVersionFloorNamesHost: a host below the floor refuses naming
// that host, and the floor is re-derived from the binary (surface-10
// reconciliation correction 2), not hardcoded.
func TestAOAttestationVersionFloorNamesHost(t *testing.T) {
	root, _ := authoredMigrationVault(t)
	old := filepath.Join(t.TempDir(), "old.txt")
	// One minor below the floor.
	os.WriteFile(old, []byte(fmt.Sprintf("writer-hosts: laggard=v%d.%d.0@2026-10-09\nquantum-ng-rows-closed: 2026-10-08\n",
		surface.MCPSurfaceVersion, surface.RequiredDataFormat-1)), 0o644)
	code, _, errb := runAO(t, root, true, old)
	if code != cli.ExitUser || !strings.Contains(errb, "laggard") {
		t.Errorf("a host below the floor should refuse naming it; exit %d, stderr:\n%s", code, errb)
	}
}

// TestAOApplyEmbedsAttestationVerbatim: a valid apply records the file's bytes
// verbatim under Migration-Attestation: and leaves the commit unpushed while
// pushing the tag.
func TestAOApplyEmbedsAttestationVerbatim(t *testing.T) {
	root, bare := authoredMigrationVault(t)
	attest := writeAttest(t)
	attBytes, _ := os.ReadFile(attest)
	beforeTip := bareMain(t, bare)

	code, out, errb := runAO(t, root, true, attest)
	if code != cli.ExitOK {
		t.Fatalf("apply exit = %d; stderr:\n%s", code, errb)
	}
	msg := gitRun(t, root, "log", "-1", "--format=%B", "HEAD")
	if !strings.Contains(msg, "Migration-Attestation:") {
		t.Errorf("commit message lacks the Migration-Attestation: heading:\n%s", msg)
	}
	if !strings.Contains(msg, strings.TrimRight(string(attBytes), "\n")) {
		t.Errorf("commit message does not hold the attestation bytes verbatim:\nmsg:\n%s\nattest:\n%s", msg, attBytes)
	}
	// The commit was NOT pushed: the bare origin's branch is unchanged.
	if got := bareMain(t, bare); got != beforeTip {
		t.Errorf("the commit was pushed to origin: tip %s -> %s", beforeTip, got)
	}
	// The tag WAS pushed.
	tags := gitRun(t, bare, "tag", "--list", "pre-authored-only-*")
	if !strings.Contains(tags, "pre-authored-only-2026-10-09") {
		t.Errorf("the tag was not pushed to origin; bare tags: %q", tags)
	}
	// The output names the exact fast-forward push.
	if !strings.Contains(out, "git push origin HEAD:main") {
		t.Errorf("output does not print the operator push line; got:\n%s", out)
	}
}

// TestAODryRunWritesNothing is "Dry run writes nothing" (12-S6): the tree,
// porcelain and local tags are unchanged, and no tag reached the bare remote.
func TestAODryRunWritesNothing(t *testing.T) {
	root, bare := authoredMigrationVault(t)
	attest := writeAttest(t)
	porcelain, head := gitPorcelain(t, root), gitHead(t, root)
	localTags := gitRun(t, root, "tag", "--list")
	bareTags := gitRun(t, bare, "tag", "--list")

	// A dry run DOES fetch (one per remote, which updates remote-tracking refs in
	// .git), so the whole-.git digest legitimately changes; the worktree, status,
	// HEAD and every tag must not (12-S6).
	code, _, errb := runAO(t, root, false, attest)
	if code != cli.ExitOK {
		t.Fatalf("dry run exit = %d; stderr:\n%s", code, errb)
	}
	if got := gitPorcelain(t, root); got != porcelain {
		t.Errorf("the dry run changed git status:\n%s", got)
	}
	if got := gitHead(t, root); got != head {
		t.Error("the dry run moved HEAD")
	}
	if got := gitRun(t, root, "tag", "--list"); got != localTags {
		t.Errorf("the dry run created a local tag: %q", got)
	}
	if got := gitRun(t, bare, "tag", "--list"); got != bareTags {
		t.Errorf("the dry run pushed a tag to the remote: %q", got)
	}
}

// TestAOMarkerAbsentPrecondition: a second run refuses because the marker is
// present.
func TestAOMarkerAbsentPrecondition(t *testing.T) {
	root, _ := authoredMigrationVault(t)
	attest := writeAttest(t)
	if code, _, errb := runAO(t, root, true, attest); code != cli.ExitOK {
		t.Fatalf("first apply failed: %d\n%s", code, errb)
	}
	code, _, errb := runAO(t, root, true, attest)
	if code != cli.ExitUser || !strings.Contains(errb, "already") {
		t.Errorf("a second run should refuse (marker present); exit %d, stderr:\n%s", code, errb)
	}
}

// TestAOUnpushedAndDirtyRefuse covers the "unpushed" and "dirty" preconditions.
func TestAOUnpushedAndDirtyRefuse(t *testing.T) {
	// Unpushed: a local commit the remote does not hold.
	root, _ := authoredMigrationVault(t)
	mkfile(t, root, "Projects/alpha/sessions/s.md", "note\n")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "local only")
	code, _, errb := runAO(t, root, true, writeAttest(t))
	if code != cli.ExitUser || !strings.Contains(errb, "push first") {
		t.Errorf("an unpushed commit should refuse; exit %d, stderr:\n%s", code, errb)
	}

	// Dirty: an uncommitted change.
	root2, _ := authoredMigrationVault(t)
	mkfile(t, root2, "Projects/alpha/resume.md", "dirty\n")
	code, _, errb = runAO(t, root2, true, writeAttest(t))
	if code != cli.ExitUser || !strings.Contains(errb, "uncommitted") {
		t.Errorf("a dirty tree should refuse; exit %d, stderr:\n%s", code, errb)
	}
}

// TestAOTagReusedAfterCrash is "Tag reused after a crash" (mig-S2): a tag of
// today's name already on HEAD is reused, not duplicated.
func TestAOTagReusedAfterCrash(t *testing.T) {
	root, bare := authoredMigrationVault(t)
	// Simulate the crash-after-tag-push state: the tag exists on HEAD, locally
	// and on the remote, but no migration commit was made.
	gitRun(t, root, "tag", "pre-authored-only-2026-10-09", "HEAD")
	gitRun(t, root, "push", "-q", "origin", "refs/tags/pre-authored-only-2026-10-09")
	code, out, errb := runAO(t, root, true, writeAttest(t))
	if code != cli.ExitOK {
		t.Fatalf("re-run after crash failed: %d\n%s", code, errb)
	}
	if !strings.Contains(out, "pre-authored-only-2026-10-09") || strings.Contains(out, "pre-authored-only-2026-10-09-2") {
		t.Errorf("the re-run should reuse the existing tag, not create -2; out:\n%s", out)
	}
	// Exactly one such tag exists, on the remote too.
	tags := strings.Fields(gitRun(t, bare, "tag", "--list", "pre-authored-only-*"))
	if len(tags) != 1 {
		t.Errorf("remote has %d pre-authored-only tags, want 1: %v", len(tags), tags)
	}
}

// TestAOTagSuffixedOnReapply is "Tag suffixed on re-apply" (mig-S2): migrate,
// revert, re-apply the same day → the second run creates pre-...-2, and the
// first tag is unchanged.
func TestAOTagSuffixedOnReapply(t *testing.T) {
	root, bare := authoredMigrationVault(t)
	attest := writeAttest(t)
	if code, _, errb := runAO(t, root, true, attest); code != cli.ExitOK {
		t.Fatalf("first apply: %d\n%s", code, errb)
	}
	firstTag := gitRun(t, root, "rev-parse", "pre-authored-only-2026-10-09")
	// Publish the commit (so the revert has somewhere to land) then revert it.
	gitRun(t, root, "push", "-q", "origin", "HEAD:main")
	gitRun(t, root, "revert", "--no-edit", "HEAD")
	gitRun(t, root, "push", "-q", "origin", "HEAD:main")

	code, out, errb := runAO(t, root, true, attest)
	if code != cli.ExitOK {
		t.Fatalf("re-apply: %d\n%s", code, errb)
	}
	if !strings.Contains(out, "pre-authored-only-2026-10-09-2") {
		t.Errorf("re-apply should create the -2 tag; out:\n%s", out)
	}
	// The first tag still points where it did (no force-move).
	if got := gitRun(t, root, "rev-parse", "pre-authored-only-2026-10-09"); got != firstTag {
		t.Errorf("the first tag moved: %s -> %s", firstTag, got)
	}
	_ = bare
}

// TestAOTagDatedInUTC is "Tag and marker dated in UTC" (12-N3): a clock at 23:30
// in UTC-05:00 is day D+1 in UTC, and both the tag and the marker take D+1.
func TestAOTagDatedInUTC(t *testing.T) {
	root, _ := authoredMigrationVault(t)
	zone := time.FixedZone("UTC-5", -5*3600)
	local := time.Date(2026, 10, 9, 23, 30, 0, 0, zone) // 2026-10-10 04:30 UTC
	var out, errb bytes.Buffer
	code := runAuthoredOnlyMigration(root, true, writeAttest(t), local.UTC(), &out, &errb)
	if code != cli.ExitOK {
		t.Fatalf("apply: %d\n%s", code, errb.String())
	}
	if !strings.Contains(out.String(), "pre-authored-only-2026-10-10") {
		t.Errorf("tag should be dated the UTC day 2026-10-10; out:\n%s", out.String())
	}
	marker := gitRun(t, root, "show", "HEAD:.vibe-palace/vault.toml")
	if !strings.Contains(marker, `authored_only = "2026-10-10"`) {
		t.Errorf("marker should be the UTC day 2026-10-10; vault.toml:\n%s", marker)
	}
}

// TestAORevertRestoresDataAndSurface is the revert row: after migrate + revert
// the pre-migration tree is restored and the surface stamp survives (normal
// path), so the gate still holds.
func TestAORevertRestoresDataAndSurface(t *testing.T) {
	root, _ := authoredMigrationVault(t)
	beforeTree := gitRun(t, root, "rev-parse", "HEAD^{tree}")
	if code, _, errb := runAO(t, root, true, writeAttest(t)); code != cli.ExitOK {
		t.Fatalf("apply: %d\n%s", code, errb)
	}
	gitRun(t, root, "revert", "--no-edit", "HEAD")
	if got := gitRun(t, root, "rev-parse", "HEAD^{tree}"); got != beforeTree {
		t.Errorf("revert did not restore the tree: %s -> %s", beforeTree, got)
	}
	// The committed surface stamp at MCPSurfaceVersion survives the revert.
	s, err := surface.ReadStamp(filepath.Join(root, "Projects", "alpha"))
	if err != nil || s.Surface != surface.MCPSurfaceVersion {
		t.Errorf("Projects/alpha/.surface after revert = %d (err %v), want %d", s.Surface, err, surface.MCPSurfaceVersion)
	}
}

// TestAOEmptyVaultPathApplies: a stampless, palace-free vault takes the
// empty-vault path and gains Audits/.surface and the marker.
func TestAOEmptyVaultPathApplies(t *testing.T) {
	setupTestVaultEnv(t)
	root, _ := newRepoWithOrigin(t) // seed only: no palace/, no stamp
	mkfile(t, root, ".vibe-palace/vault.toml", fmt.Sprintf("format = %d\n", surface.RequiredDataFormat))
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "format stamp")
	gitRun(t, root, "push", "-q", "origin", "main")
	code, out, errb := runAO(t, root, true, writeAttest(t))
	if code != cli.ExitOK {
		t.Fatalf("empty-vault apply: %d\n%s", code, errb)
	}
	if !strings.Contains(out, "empty-vault") {
		t.Errorf("output should name the empty-vault path; out:\n%s", out)
	}
	s, err := surface.ReadStamp(filepath.Join(root, "Audits"))
	if err != nil || s.Surface != surface.MCPSurfaceVersion {
		t.Errorf("Audits/.surface = %d (err %v), want %d", s.Surface, err, surface.MCPSurfaceVersion)
	}
}

// emptyMigrationVault builds a stampless, palace-free, pushed vault: the
// empty-vault path's fixture, wired to a bare origin.
func emptyMigrationVault(t *testing.T) (root, bare string) {
	t.Helper()
	setupTestVaultEnv(t)
	root, bare = newRepoWithOrigin(t)
	mkfile(t, root, ".vibe-palace/vault.toml", fmt.Sprintf("format = %d\n", surface.RequiredDataFormat))
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "format stamp")
	gitRun(t, root, "push", "-q", "origin", "main")
	return root, bare
}

// TestAONormalPathDataFormatBehindRefuses is the "data format behind the binary"
// precondition on the normal path: a clean, pushed vault whose data format is
// below the binary's required format refuses.
func TestAONormalPathDataFormatBehindRefuses(t *testing.T) {
	if surface.RequiredDataFormat < 1 {
		t.Skip("binary requires data format 0; nothing is below it")
	}
	root, _ := authoredMigrationVault(t)
	// Lower the vault's data format below the binary's, then commit and push so the
	// clean-and-pushed precondition still holds and only the format is broken.
	mkfile(t, root, ".vibe-palace/vault.toml", fmt.Sprintf("format = %d\n", surface.RequiredDataFormat-1))
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "downgrade format")
	gitRun(t, root, "push", "-q", "origin", "main")
	code, _, errb := runAO(t, root, true, writeAttest(t))
	if code != cli.ExitUser || !strings.Contains(errb, "data format") {
		t.Errorf("a vault behind the data format should refuse naming it; exit %d, stderr:\n%s", code, errb)
	}
}

// TestAOEmptyVaultPreconditionMatrix is the empty-vault per-precondition row
// (mig-B1): on an empty vault, each refuses when only its own condition is
// broken. The surface-9 floor half is NOT among them (a stampless empty vault
// would fail it), which TestAuthoredOnlyEmptyPathSkipsFloorRead proves directly.
func TestAOEmptyVaultPreconditionMatrix(t *testing.T) {
	t.Run("attestation missing", func(t *testing.T) {
		root, _ := emptyMigrationVault(t)
		code, _, errb := runAO(t, root, true, "")
		if code != cli.ExitUser || !strings.Contains(errb, "requires --attest") {
			t.Errorf("empty vault without --attest should refuse; exit %d, stderr:\n%s", code, errb)
		}
	})
	t.Run("marker present", func(t *testing.T) {
		root, _ := emptyMigrationVault(t)
		mkfile(t, root, ".vibe-palace/vault.toml",
			fmt.Sprintf("format = %d\nauthored_only = \"2026-10-09\"\n", surface.RequiredDataFormat))
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-q", "-m", "pre-set marker")
		gitRun(t, root, "push", "-q", "origin", "main")
		code, _, errb := runAO(t, root, true, writeAttest(t))
		if code != cli.ExitUser || !strings.Contains(errb, "already") {
			t.Errorf("an already-marked empty vault should refuse; exit %d, stderr:\n%s", code, errb)
		}
	})
	t.Run("dirty", func(t *testing.T) {
		root, _ := emptyMigrationVault(t)
		mkfile(t, root, "Projects/beta/resume.md", "dirty\n")
		code, _, errb := runAO(t, root, true, writeAttest(t))
		if code != cli.ExitUser || !strings.Contains(errb, "uncommitted") {
			t.Errorf("a dirty empty vault should refuse; exit %d, stderr:\n%s", code, errb)
		}
	})
	t.Run("unpushed", func(t *testing.T) {
		root, _ := emptyMigrationVault(t)
		mkfile(t, root, "Projects/beta/resume.md", "local\n")
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-q", "-m", "local only")
		code, _, errb := runAO(t, root, true, writeAttest(t))
		if code != cli.ExitUser || !strings.Contains(errb, "push first") {
			t.Errorf("an unpushed empty vault should refuse; exit %d, stderr:\n%s", code, errb)
		}
	})
	t.Run("MERGE_HEAD", func(t *testing.T) {
		root, _ := emptyMigrationVault(t)
		head := gitRun(t, root, "rev-parse", "HEAD")
		mkfile(t, root, ".git/MERGE_HEAD", head+"\n")
		code, _, errb := runAO(t, root, true, writeAttest(t))
		if code != cli.ExitUser || !strings.Contains(errb, "in progress") {
			t.Errorf("a MERGE_HEAD empty vault should refuse; exit %d, stderr:\n%s", code, errb)
		}
	})
	t.Run("data format behind", func(t *testing.T) {
		if surface.RequiredDataFormat < 1 {
			t.Skip("binary requires data format 0")
		}
		root, _ := emptyMigrationVault(t)
		mkfile(t, root, ".vibe-palace/vault.toml", fmt.Sprintf("format = %d\n", surface.RequiredDataFormat-1))
		gitRun(t, root, "add", "-A")
		gitRun(t, root, "commit", "-q", "-m", "downgrade")
		gitRun(t, root, "push", "-q", "origin", "main")
		code, _, errb := runAO(t, root, true, writeAttest(t))
		if code != cli.ExitUser || !strings.Contains(errb, "data format") {
			t.Errorf("an empty vault behind the data format should refuse; exit %d, stderr:\n%s", code, errb)
		}
	})
	t.Run("remote tip not an ancestor", func(t *testing.T) {
		root, bare := emptyMigrationVault(t)
		// Advance ONLY the remote with a non-palace commit (still empty: no palace/
		// content, no stamp), leaving the local HEAD untouched. The remote tip is
		// then strictly ahead of HEAD, so HEAD holds nothing the tip lacks (the
		// pushed check passes) but the tip is not an ancestor of HEAD — isolating the
		// ancestor condition from the unpushed one.
		clone := t.TempDir()
		gitRun(t, clone, "clone", "-q", bare, ".")
		gitRun(t, clone, "config", "user.email", "o@test.com")
		gitRun(t, clone, "config", "user.name", "O")
		mkfile(t, clone, "Projects/beta/resume.md", "remote\n")
		gitRun(t, clone, "add", "-A")
		gitRun(t, clone, "commit", "-q", "-m", "remote only")
		gitRun(t, clone, "push", "-q", "origin", "main")
		code, _, errb := runAO(t, root, true, writeAttest(t))
		if code != cli.ExitUser || !strings.Contains(errb, "ancestor") {
			t.Errorf("a diverged empty vault should refuse naming ancestry; exit %d, stderr:\n%s", code, errb)
		}
	})
}

// TestAOKgAddAfterMigrationStaysTracked is "vp_kg_add after migration": the
// migrated .gitignore covers drawers and the ingest ledger but NOT kg/, so a
// triple written after the migration is still tracked (another host can pull
// it). Kills an ignore line that covers kg/.
func TestAOKgAddAfterMigrationStaysTracked(t *testing.T) {
	root, _ := authoredMigrationVault(t)
	if code, _, errb := runAO(t, root, true, writeAttest(t)); code != cli.ExitOK {
		t.Fatalf("apply: %d\n%s", code, errb)
	}
	// A new triple, as vp_kg_add would write it, under palace/<p>/kg/triples/.
	rel := "palace/alpha/kg/triples/new.json"
	mkfile(t, root, rel, `{"subject":"x","predicate":"uses","object":"y","origin":"authored"}`+"\n")
	// It must NOT be ignored: git status -uall shows it as untracked (an ignored
	// file would not appear), and it stages cleanly.
	status := gitRun(t, root, "status", "--porcelain", "--untracked-files=all", "--", rel)
	if !strings.Contains(status, rel) {
		t.Errorf("a post-migration kg triple is ignored (not shown by status); the ignore lines must not cover kg/:\n%q", status)
	}
	gitRun(t, root, "add", "--", rel)
	if tracked := gitRun(t, root, "ls-files", "--", rel); !strings.Contains(tracked, rel) {
		t.Errorf("the kg triple did not stage as tracked: %q", tracked)
	}
}

// TestAONoLedgerApplies is the "no ledger precondition" row (mig-S1): a vault
// with no ingest ledger still applies, and the plan prints the --no-embed /
// --skip recommendation. Kills a hard ledger precondition.
func TestAONoLedgerApplies(t *testing.T) {
	root, _ := authoredMigrationVault(t) // the fixture writes no ingested-archives.jsonl
	// The plan prints the recommendation with its --skip advice.
	_, out, _ := runAO(t, root, false, writeAttest(t))
	if !strings.Contains(out, "--no-embed") || !strings.Contains(out, "--skip") {
		t.Errorf("the plan should print the --no-embed recommendation with --skip; got:\n%s", out)
	}
	// And the apply succeeds despite the absence of a ledger.
	if code, _, errb := runAO(t, root, true, writeAttest(t)); code != cli.ExitOK {
		t.Errorf("apply should succeed with no ledger; exit %d, stderr:\n%s", code, errb)
	}
}

// TestAOV8SweepAfterCommitIsIgnored is "v8-shaped sweep after the commit": after
// the migration, a v8 enricher writing a drawer (a decision drawer, say) lands on
// an ignored path, so the tree stays clean. Kills a missing ignore line.
func TestAOV8SweepAfterCommitIsIgnored(t *testing.T) {
	root, _ := authoredMigrationVault(t)
	if code, _, errb := runAO(t, root, true, writeAttest(t)); code != cli.ExitOK {
		t.Fatalf("apply: %d\n%s", code, errb)
	}
	// A v8-shaped sweep enricher writes a new decision drawer under the ignored
	// drawers/ path.
	mkfile(t, root, "palace/alpha/drawers/decisions/d-new.md", "a decision an old binary swept in\n")
	// git status -uall stays empty: the drawer is ignored by the migrated
	// .gitignore (palace/*/drawers/).
	if out := gitRun(t, root, "status", "--porcelain", "--untracked-files=all"); strings.TrimSpace(out) != "" {
		t.Errorf("a post-migration drawer write left the tree dirty; the ignore line is missing:\n%s", out)
	}
	// And tidy reports nothing and sweeps nothing (round-1 12-S4).
	scan, err := storage.TidyScan(root)
	if err != nil {
		t.Fatalf("tidy scan: %v", err)
	}
	if len(scan.Reported) != 0 || len(scan.Swept) != 0 {
		t.Errorf("tidy after a v8 sweep write is not a no-op: Reported=%v Swept=%v", scan.Reported, scan.Swept)
	}
}

// TestAOEmptyVaultSurfaceGoneAfterRevert is the empty-vault half of "Surface
// survives the revert" (S1/S2; 12-S4): the empty-vault path writes the vault's
// ONLY stamp (Audits/.surface). After a revert that stamp is gone, so the vault
// no longer gates an older binary — and the printed rollback text warns the
// operator to re-stamp Audits/.surface in the same push.
func TestAOEmptyVaultSurfaceGoneAfterRevert(t *testing.T) {
	root, _ := emptyMigrationVault(t)
	code, out, errb := runAO(t, root, true, writeAttest(t))
	if code != cli.ExitOK {
		t.Fatalf("empty-vault apply: %d\n%s", code, errb)
	}
	// S1: the apply's rollback text names the empty-vault re-stamp requirement.
	if !strings.Contains(out, "re-stamp Audits/.surface") || !strings.Contains(out, "UNGATED") {
		t.Errorf("empty-vault rollback text must warn to re-stamp Audits/.surface; got:\n%s", out)
	}
	// The migration wrote Audits/.surface at the current surface.
	if s, _ := surface.ReadStamp(filepath.Join(root, "Audits")); s.Surface != surface.MCPSurfaceVersion {
		t.Fatalf("empty-vault apply did not write Audits/.surface at %d (got %d)", surface.MCPSurfaceVersion, s.Surface)
	}
	// Revert the migration commit: the vault's only stamp is removed (ReadStamp
	// returns surface 0 — no stamp — rather than an error).
	gitRun(t, root, "revert", "--no-edit", "HEAD")
	if s, _ := surface.ReadStamp(filepath.Join(root, "Audits")); s.Surface != 0 {
		t.Errorf("Audits/.surface (surface %d) survived the revert; the empty-vault revert must remove the vault's only stamp", s.Surface)
	}
	// The now-stampless vault gates nothing: CheckCompatible admits even the
	// current binary (and, by the same absence, any older one — the surface-level
	// TestCheckCompatibleAsEmptyVaultRevertUngates pins the -1 case).
	if err := surface.CheckCompatible(root); err != nil {
		t.Errorf("a reverted empty vault should be ungated (no stamp), but CheckCompatible refused: %v", err)
	}
}

// TestAORollbackGuidancePrinted is S1: both paths print the revert command and
// its consequences. The normal path says surface 9 survives (no re-stamp); the
// empty-vault path warns to re-stamp Audits/.surface.
func TestAORollbackGuidancePrinted(t *testing.T) {
	t.Run("normal path", func(t *testing.T) {
		root, _ := authoredMigrationVault(t)
		_, out, _ := runAO(t, root, false, writeAttest(t))
		if !strings.Contains(out, "git revert HEAD") {
			t.Errorf("normal-path plan should print the revert command; got:\n%s", out)
		}
		if !strings.Contains(out, "survives") {
			t.Errorf("normal-path rollback text should say surface 9 survives; got:\n%s", out)
		}
	})
	t.Run("empty-vault path", func(t *testing.T) {
		root, _ := emptyMigrationVault(t)
		_, out, _ := runAO(t, root, false, writeAttest(t))
		if !strings.Contains(out, "git revert HEAD") {
			t.Errorf("empty-vault plan should print the revert command; got:\n%s", out)
		}
		if !strings.Contains(out, "re-stamp Audits/.surface") {
			t.Errorf("empty-vault rollback text should warn to re-stamp Audits/.surface; got:\n%s", out)
		}
	})
}

// TestAOEndToEndRevertV9KeepsRunning is Acceptance-1, the "End-to-end revert, v9
// keeps running" row (Q2, XC5). After migrate + revert, a current-surface capture
// (transcript + decisions) runs through the SessionEnd hook, and then tidy. It
// asserts: capture writes nothing derived into the tracked tree — the decisions
// land in the host-local store, never a tracked decision drawer (kills a
// marker-gated capture redirection); tidy stages no derived path (kills a
// marker-gated staging guard); and a second clone with no local store finds the
// re-tracked drawer chunk through search's first build (kills a first build that
// ignores tracked drawers). The capture and sweep behaviours are NOT keyed on the
// marker, so a reverted — unmarked — vault still behaves correctly.
func TestAOEndToEndRevertV9KeepsRunning(t *testing.T) {
	// The enricher returns a decision, so the capture exercises the decision path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"summary\":\"Auto-enriched.\",\"decisions\":[\"Chose the authored-only layout\"],\"open_threads\":[],\"tag\":\"implementation\"}"}}]}`))
	}))
	defer srv.Close()
	t.Setenv("VP_TEST_AO_ENRICH_KEY", "sk-ao-test")

	root, _ := authoredMigrationVault(t)
	// The shared fixture's raw d1.md is minimal; a properly-indexable tracked
	// drawer with searchable content goes through AppendDrawer (drawers.jsonl).
	v := storage.NewVault(root)
	wing := palace.DetectWing("alpha", "")
	const room = "facts"
	const drawerContent = "authentication uses bearer tokens issued by the gateway"
	if err := v.AppendDrawer("alpha", wing, room, storage.Drawer{
		Content: drawerContent, Hall: "facts", SourceType: "session",
		SourceRef: "sess-1", FiledAt: "2026-04-01T10:00:00Z",
	}); err != nil {
		t.Fatalf("append drawer: %v", err)
	}
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "add searchable drawer")
	gitRun(t, root, "push", "-q", "origin", "main")

	// 1. Migrate, then revert. After the revert the marker is gone and the drawer
	// is re-tracked.
	if code, _, errb := runAO(t, root, true, writeAttest(t)); code != cli.ExitOK {
		t.Fatalf("apply: %d\n%s", code, errb)
	}
	gitRun(t, root, "revert", "--no-edit", "HEAD")
	if m, _ := storage.VaultMigrated(root); m {
		t.Fatal("marker should be gone after the revert")
	}
	drawerRel := "palace/alpha/" + filepath.ToSlash(drawerFileRel(t, v, wing, room, root))
	if tracked := gitRun(t, root, "ls-files", "--", drawerRel); !strings.Contains(tracked, drawerRel) {
		t.Fatalf("the drawer was not re-tracked by the revert: %q", tracked)
	}

	// 2. A current-surface capture (transcript + decisions) through the SessionEnd
	// hook, on the reverted vault. Enrichment is enabled against the test server.
	writeIsolatedHostConfig(t, "[enrichment]\n"+
		"enabled = true\n"+
		"provider = \"openai\"\n"+
		"model = \"ao-enrich-model\"\n"+
		"api_key_env = \"VP_TEST_AO_ENRICH_KEY\"\n"+
		"base_url = \""+srv.URL+"\"\n"+
		"max_tokens = 512\ntimeout_seconds = 10\n")

	cwd := t.TempDir()
	mkfile(t, cwd, ".vibe-palace.toml", "[project]\nname = \"alpha\"\n")
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(t.TempDir(), "transcript.jsonl")
	mkfile(t, filepath.Dir(transcript), "transcript.jsonl",
		"{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n"+
			"{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"model\":\"claude-opus-4-6\",\"content\":\"hi there\"}}\n")

	if _, err := hook.Run(context.Background(), hook.Payload{
		SessionID:      "ao-revert-session",
		TranscriptPath: transcript,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, hook.RunOptions{
		VaultRoot:   root,
		ProjectSlug: "alpha",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
	}); err != nil {
		t.Fatalf("SessionEnd hook: %v", err)
	}

	// Capture writes nothing derived into the tracked tree: the decision landed in
	// the host-local index store, not a tracked decision drawer.
	if ds, err := v.ListDrawers("alpha", wing, search.DecisionRoom); err == nil && len(ds) != 0 {
		t.Errorf("the capture wrote %d decision drawer(s) into the tracked tree; decisions must go to the store only", len(ds))
	}
	store, err := indexstore.ReadStore(v, "alpha")
	if err != nil {
		t.Fatalf("read index store: %v", err)
	}
	sawDecision := false
	for _, c := range store.Chunks(false) {
		if c.Room == search.DecisionRoom {
			sawDecision = true
			break
		}
	}
	if !sawDecision {
		t.Error("the capture's decision did not reach the host-local store; the capture redirection must not be marker-gated")
	}

	// 3. Tidy stages no derived path: nothing under palace/.local/, no drawer file,
	// no ingest ledger.
	scan, err := storage.TidyScan(root)
	if err != nil {
		t.Fatalf("tidy scan: %v", err)
	}
	for _, p := range scan.Swept {
		if strings.Contains(p, "/.local/") || strings.HasSuffix(p, "ingested-archives.jsonl") || strings.Contains(p, "/drawers/") {
			t.Errorf("tidy staged a derived path after the revert: %s", p)
		}
	}

	// 4. A second clone with no local store finds the re-tracked drawer chunk
	// through search's first build (the glide path).
	cloneDir := filepath.Join(t.TempDir(), "clone")
	gitRun(t, filepath.Dir(cloneDir), "clone", "-q", root, "clone")
	if _, err := os.Stat(filepath.Join(cloneDir, "palace", ".local")); err == nil {
		t.Fatal("the clone already has a local store; the fixture must start with none")
	}
	cloneVault := storage.NewVault(cloneDir)
	eng := search.NewEngine(embedder.NewMock(384), cloneVault, storage.Config{SearchDefaultLimit: 50})
	defer eng.Close()
	if _, err := eng.Rebuild(context.Background(), "alpha"); err != nil {
		t.Fatalf("first build on the clone: %v", err)
	}
	results, err := eng.Search(context.Background(), "authentication bearer tokens", search.SearchFilters{Project: "alpha", Limit: 50})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	found := false
	for _, r := range results {
		if strings.Contains(r.Content, "bearer tokens") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("search on a fresh clone did not find the re-tracked drawer chunk among %d result(s)", len(results))
	}
}

// drawerFileRel returns the vault-relative-from-palace/<project>/ path of the
// drawer file AppendDrawer wrote, discovered by walking the drawers tree, so the
// test does not hard-code the wing/room directory layout.
func drawerFileRel(t *testing.T, v *storage.Vault, wing, room, root string) string {
	t.Helper()
	path, err := v.DrawerFile("alpha", wing, room)
	if err != nil {
		t.Fatalf("drawer file path: %v", err)
	}
	rel, err := filepath.Rel(filepath.Join(root, "palace", "alpha"), path)
	if err != nil {
		t.Fatalf("relativise drawer path: %v", err)
	}
	return rel
}
