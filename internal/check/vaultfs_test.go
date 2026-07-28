// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// probeFilesIn returns the probe files currently sitting in a vault root.
func probeFilesIn(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", root, err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), probeNamePrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestCheckVaultFilesystemConcurrent is THE regression test for this fix, and
// it fails against the previous implementation.
//
// That version used a single FIXED probe name and let EEXIST fall through to
// the Fail branch — the branch that reports "filesystem rejects ':' in
// filenames (NTFS/exFAT?)" and instructs the operator to RELOCATE THEIR VAULT.
// The vault is synced across machines and shared by several projects, and this
// check now runs from every host's restart preflight automatically, so two
// near-simultaneous restarts were enough to hand somebody a confident, wrong
// and expensive remediation.
//
// Nothing about a concurrent peer is evidence about the filesystem, so the
// only acceptable verdicts here are Pass (we probed) or Skip (we could not),
// never Fail.
func TestCheckVaultFilesystemConcurrent(t *testing.T) {
	root := t.TempDir()

	const workers = 24
	results := make([]Result, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range workers {
		wg.Go(func() {
			<-start // maximize the overlap rather than letting them stagger
			results[i] = CheckVaultFilesystem(root)
		})
	}
	close(start)
	wg.Wait()

	for i, r := range results {
		if r.Status == Fail {
			t.Errorf("worker %d reported Fail under concurrency — a peer probe is not "+
				"a hostile filesystem: summary=%q err=%v", i, r.Summary, r.Err)
		}
		if r.Status != Pass {
			// A temp dir on any CI filesystem accepts ":", so every worker
			// should genuinely have probed. A Skip here means the workers were
			// colliding and degrading rather than getting unique names.
			t.Errorf("worker %d = %v (%q), want Pass: unique names should let every "+
				"concurrent caller probe for real", i, r.Status, r.Summary)
		}
	}

	if left := probeFilesIn(t, root); len(left) != 0 {
		t.Errorf("%d probe file(s) survived a concurrent run: %v", len(left), left)
	}
}

// TestProbeNamesAreUnique pins the property the concurrency fix rests on,
// directly rather than through timing. Two calls in the same nanosecond tick
// must still differ — which is what probeSeq is for, and why PID+timestamp
// alone was not enough for a long-lived `vp mcp` dispatching vp_check
// concurrently.
func TestProbeNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		n := probeName()
		if seen[n] {
			t.Fatalf("probeName() collided with itself: %q", n)
		}
		seen[n] = true
	}
}

// TestProbeNameCarriesTheColon guards against the vacuity failure: the ":" IS
// the experiment. A probe name that lost its colon would create fine, remove
// fine, and report Pass on an NTFS vault — a check that has stopped testing
// anything while looking perfectly healthy. It must also stay hidden (leading
// dot) and stay matchable by the sweep and the .gitignore pattern.
func TestProbeNameCarriesTheColon(t *testing.T) {
	n := probeName()
	if !strings.Contains(n, ":") {
		t.Errorf("probe name %q has no ':' — the check would pass vacuously on a "+
			"filesystem that rejects colons", n)
	}
	if !strings.HasPrefix(n, ".vp-fs-probe-") {
		t.Errorf("probe name %q lost the prefix the sweep and the vault .gitignore "+
			"pattern both key on", n)
	}
}

// TestCheckVaultFilesystemSweepsStaleProbesOnly covers both halves of the
// cleanup contract at once, because they are in tension: the sweep must delete
// residue from a process that died mid-check, and must NOT delete a peer
// host's in-flight probe.
//
// Residue is not cosmetic. A probe file at the vault root matches no sweepRule
// in storage.classifyDirty, so it becomes Reported dirt, and Reported dirt
// makes SyncFlow refuse to sync outright. One crashed check would wedge the
// vault until a human deleted a file they never created.
func TestCheckVaultFilesystemSweepsStaleProbesOnly(t *testing.T) {
	root := t.TempDir()

	stale := filepath.Join(root, probeNamePrefix+"999999-1-1"+probeNameSuffix)
	if err := os.WriteFile(stale, nil, 0o644); err != nil {
		t.Fatalf("write stale probe: %v", err)
	}
	old := time.Now().Add(-2 * staleProbeAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	// A peer host mid-probe: same prefix, but written just now.
	live := filepath.Join(root, probeNamePrefix+"888888-2-2"+probeNameSuffix)
	if err := os.WriteFile(live, nil, 0o644); err != nil {
		t.Fatalf("write live probe: %v", err)
	}

	if r := CheckVaultFilesystem(root); r.Status != Pass {
		t.Fatalf("check = %v (%q), want Pass", r.Status, r.Summary)
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale probe survived the sweep (err=%v) — it would become Reported "+
			"dirt and wedge `vp vault sync`", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the sweep deleted a peer host's in-flight probe (%v) — trading one "+
			"race for another", err)
	}
}

// TestCheckVaultFilesystemUnexplainedErrorStillFails is the other side of the
// EEXIST change: widening the Skip branches must not have swallowed the real
// verdict. This check exists to catch a hostile filesystem, and a version that
// degraded every error to Skip would be permanently, quietly useless.
//
// A ":"-rejecting filesystem cannot be mounted from a test, so this drives the
// same branch with a different unexpected creation error: a vault root that
// stats fine but is a regular FILE, so creating a child yields ENOTDIR —
// neither ErrPermission nor ErrExist.
func TestCheckVaultFilesystemUnexplainedErrorStillFails(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "vault-is-a-file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := CheckVaultFilesystem(notADir)
	if r.Status != Fail {
		t.Fatalf("status = %v (%q), want Fail — an unexplained creation failure must "+
			"still reach the verdict this check exists for", r.Status, r.Summary)
	}
	if r.Err == nil {
		t.Error("Fail must carry the underlying error")
	}
	joined := strings.Join(r.Details, "\n")
	if !strings.Contains(joined, "Relocate the vault") {
		t.Errorf("Fail must keep its remediation, got:\n%s", joined)
	}
}

// TestCheckVaultFilesystemPermissionIsSkip pins the pre-existing inconclusive
// branch, so the EEXIST addition sits alongside a tested sibling rather than
// next to an untested assumption.
func TestCheckVaultFilesystemPermissionIsSkip(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny access")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil { // r-x: cannot create
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })

	r := CheckVaultFilesystem(root)
	if r.Status != Skip {
		t.Errorf("status = %v (%q), want Skip — a permission error is not evidence "+
			"about the filesystem's handling of ':'", r.Status, r.Summary)
	}
}
