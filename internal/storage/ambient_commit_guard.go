// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// ErrAmbientVaultCommit marks a commit refused because its repository is the
// operator's configured vault root and the process is a `go test` binary.
//
// 🔴 IT IS ITS OWN CLASS, NOT A PER-REMOTE OUTCOME. A refusal to commit the
// operator's vault is not a remote's problem: recorded in
// PullResult.RemoteResults it would be reported as "remote X failed" and would
// make pullSweepStops skip the remaining remotes as "failed in a way the pull
// does not merge past", neither of which is true of it. pullCore therefore
// propagates it as a hard error (see its use there) rather than recording it.
//
// Today a recorded non-nil per-remote error does still surface — FailedRemotes
// counts it, RemoteVerdict returns non-empty, and every caller of RemoteVerdict
// treats that as an error. So this classification is not fixing a live swallow;
// it is keeping one from being introduced by a future sentinel-classification
// edit, which pullSweepStops already does for two other sentinels.
var ErrAmbientVaultCommit = errors.New("refusing to commit to the configured vault under test")

// refuseAmbientCommit is the ERROR arm of the ambient-commit guard, for the
// commit seats whose enclosing vault-root lock is released MANUALLY.
//
// Where the release is deferred, the seat calls surface.GuardTestVaultCommit and
// panics instead — that form is louder and cannot be ignored by a caller. These
// three cannot use it: vaultpull.go holds the vault commit lock across the merge
// with no defer anywhere, vaultsync.go's reconcile path releases by hand, and a
// live recover() sits on a path that reaches both. A panic there could leave the
// flock held for the life of the process, and vaultlock.Acquire is a no-timeout
// LOCK_EX, so the next acquisition would block forever rather than fail.
//
// All three callers return an error their own callers already check, so the
// commit is prevented and the refusal propagates. Nothing is logged-and-dropped;
// TestAmbientCommitErrorIsNotSwallowed pins that.
func refuseAmbientCommit(vaultRoot, what string) error {
	if !surface.AmbientVaultRootUnderTest(vaultRoot) {
		return nil
	}
	return fmt.Errorf("%w: %s refused a commit to %s — the ambient configured vault root, "+
		"resolved at process start; isolate the test with t.Setenv(\"XDG_CONFIG_HOME\", t.TempDir()) "+
		"(or a temp --vault)", ErrAmbientVaultCommit, what, vaultRoot)
}
