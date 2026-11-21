// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
)

// ONE helper owns "write a manifest and keep its signature honest".
//
// Every code path in this package that changes a manifest's bytes goes through
// writeManifestSigned, so a manifest is never left with a signature over bytes
// that no longer exist (task
// link-session-note-rewrites-a-signed-manifest-without-re-signing). The caller
// holds the manifest's per-path vaultlock, exactly as WriteManifest's callers
// do; the helper takes none (ADR-003: RMW callers must not double-acquire).
//
// What this does and does not buy: the only thing that checks a signature is
// `vp archive verify`, and only when a .sig exists AND allowed_signers and
// signer_identity are configured. This makes that check correct for operators
// who sign; it changes nothing for anyone who does not.

var (
	// ErrManifestSigning wraps the signer's error when a LINK rewrite could not
	// be re-signed. The previous manifest and signature were put back, or the
	// error text says exactly what was not.
	ErrManifestSigning = errors.New("archive: re-signing the manifest failed")

	// ErrSignedManifestCannotResign is returned by the link functions when a
	// .sig sits beside the manifest but the project's signing options cannot
	// produce a new one (no sign_mode, key gone, config unreadable). Nothing was
	// written: the manifest and its signature are as they were and still verify.
	ErrSignedManifestCannotResign = errors.New("archive: the manifest is signed but cannot be re-signed")
)

// Test seams, production values by default.
var (
	// restoreBytes puts previously read bytes back through the write funnel.
	restoreBytes = func(vaultRoot, path string, b []byte) error {
		return atomicfile.Write(vaultRoot, path, b)
	}
	// renameForBackupHook, when non-nil, replaces the raw rename Create's
	// changed-source arm uses to preserve the prior manifest and its signature,
	// so a test can fail one of the two renames. Nil in production. The real
	// vaultfs.RenameNoLock call stays inside archive.Create (see there), because
	// the source-audit baseline attributes that raw sink to Create.
	renameForBackupHook func(src, dst string) error
)

// failurePolicy says what writeManifestSigned does when signing fails.
type failurePolicy int

const (
	// failRemove: remove the manifest and its signature (RepairManifest: the
	// orphan stays an orphan and a retry is possible).
	failRemove failurePolicy = iota
	// failRestore: put the previous manifest and signature back byte for byte
	// (the link rewrite: the archive is untouched, so the old pair is still
	// coherent).
	failRestore
	// failLeaveUnsigned: keep the NEW manifest with no signature beside it
	// (Create: the new archive and manifest are one coherent pair, and the old
	// manifest no longer describes the new archive, so it is never restored).
	failLeaveUnsigned
)

// manifestState is the raw pre-call bytes of a manifest and its signature,
// either of which may be absent. Only failRestore reads it.
type manifestState struct {
	manifest    []byte
	hasManifest bool
	sig         []byte
	hasSig      bool
}

// captureManifestState reads the manifest and its .sig as they are now.
func captureManifestState(manifestPath string) (*manifestState, error) {
	st := &manifestState{}
	if b, err := os.ReadFile(manifestPath); err == nil {
		st.manifest, st.hasManifest = b, true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}
	if b, err := os.ReadFile(SignaturePath(manifestPath)); err == nil {
		st.sig, st.hasSig = b, true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read signature %s: %w", SignaturePath(manifestPath), err)
	}
	return st, nil
}

// signatureExists reports whether a .sig sits beside the manifest.
func signatureExists(manifestPath string) (bool, error) {
	_, err := os.Lstat(SignaturePath(manifestPath))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("stat signature %s: %w", SignaturePath(manifestPath), err)
	}
}

// removeSignature removes the .sig and reports whether the absence is CONFIRMED:
// a successful removal, or a file that was not there. Any other failure is an
// unconfirmed state and must never be described as "unsigned".
func removeSignature(manifestPath string) (confirmed bool, err error) {
	sigPath := SignaturePath(manifestPath)
	if rerr := removeForRollback(sigPath); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return false, fmt.Errorf("remove signature %s: %w", sigPath, rerr)
	}
	return true, nil
}

// writeManifestSigned writes m to manifestPath through the write funnel and, when
// sign is enabled, re-signs it. The caller MUST hold the manifest's vaultlock.
//
// The manifest is written FIRST, so under test a write aimed at the operator's
// configured vault panics at the tripwire seat before any signer subprocess runs
// (the signer writes the .sig itself, outside the funnel).
//
// prior is only read by failRestore and may be nil for the other policies.
func writeManifestSigned(vaultRoot, manifestPath string, m *Manifest, sign SignOptions, policy failurePolicy, prior *manifestState) error {
	b, err := MarshalManifest(m)
	if err != nil {
		return err
	}
	return writeManifestBytes(vaultRoot, manifestPath, b, sign, policy, prior, restoreRemedyLink)
}

// Remedies appended by failRestoreManifest when the restore itself failed: what
// the operator can do depends on which operation was running.
const (
	restoreRemedyLink = "re-run the link once signing works, or restore the previous manifest by hand"
	// restoreRemedyRename: the signed project rename rolls the destination pair
	// back with the rest of the move, so there is nothing to re-run here.
	restoreRemedyRename = "the rename rolls this destination pair back with the rest of the move; nothing to re-run here, " +
		"and if the rollback itself reports a failure, restore Projects/<old> from git HEAD by hand"
)

// writeManifestBytes is THE core every manifest write goes through: it writes
// RAW bytes through the write funnel, then clears any old signature and re-signs
// per policy. Working on raw bytes (not a *Manifest round trip) is what lets the
// project rename keep a manifest key this binary does not know. This function
// holds the only atomicfile.Write of a manifest path in the package; the census
// test pins that by enclosing function.
//
// wopts are passed to the write: the rename supplies inherit-perm and fsync so
// the unsigned path keeps its historical durability and mode. remedy is the
// sentence failRestoreManifest ends with when the restore fails.
func writeManifestBytes(vaultRoot, manifestPath string, data []byte, sign SignOptions, policy failurePolicy,
	prior *manifestState, remedy string, wopts ...atomicfile.Option) error {
	if err := atomicfile.Write(vaultRoot, manifestPath, data, wopts...); err != nil {
		return fmt.Errorf("write manifest %s: %w", manifestPath, err)
	}
	if !sign.Enabled() {
		return nil
	}
	// Clear any previous signature through the sink first, so the signer's own
	// raw removal is a no-op and a signer that fails before removing (a missing
	// key) cannot leave the old signature behind.
	var serr error
	if _, cerr := removeSignature(manifestPath); cerr != nil {
		serr = fmt.Errorf("clear previous signature: %w", cerr)
	} else {
		serr = signManifest(manifestPath, sign)
	}
	if serr == nil {
		return nil
	}
	switch policy {
	case failRemove:
		return failRemoveManifest(manifestPath, serr)
	case failRestore:
		return failRestoreManifest(vaultRoot, manifestPath, prior, serr, remedy)
	default:
		return failLeaveManifestUnsigned(manifestPath, serr)
	}
}

// failRemoveManifest is RepairManifest's rollback: the caller's check guarantees
// no .sig existed before the call, so everything removed here was written by it.
// Each removal failure is reported; "removed" is claimed only when true.
func failRemoveManifest(manifestPath string, serr error) error {
	var rerrs []error
	if rerr := removeForRollback(manifestPath); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		rerrs = append(rerrs, fmt.Errorf("remove manifest %s: %w", manifestPath, rerr))
	}
	if rerr := removeForRollback(SignaturePath(manifestPath)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		rerrs = append(rerrs, fmt.Errorf("remove signature %s: %w", SignaturePath(manifestPath), rerr))
	}
	if len(rerrs) > 0 {
		return fmt.Errorf("sign manifest: %w; AND the rollback failed, so the repaired manifest may still be present (%w); remove it by hand before retrying",
			serr, errors.Join(rerrs...))
	}
	return fmt.Errorf("sign manifest: %w (the repaired manifest was removed; retry once signing works)", serr)
}

// failLeaveManifestUnsigned keeps the new manifest and makes sure no signature
// is left beside it. "NOT signed" is claimed only when the absence of a .sig is
// confirmed; otherwise the signature is "possibly stale" and both errors ride.
func failLeaveManifestUnsigned(manifestPath string, serr error) error {
	if confirmed, rerr := removeSignature(manifestPath); !confirmed {
		return fmt.Errorf("sign manifest: %w; the manifest was written but a signature at %s is POSSIBLY STALE "+
			"(it may cover an earlier manifest) because it could not be removed (%w); remove it by hand",
			serr, SignaturePath(manifestPath), rerr)
	}
	return fmt.Errorf("sign manifest: %w; the manifest was written and is NOT signed (no .sig remains beside it)", serr)
}

// failRestoreManifest puts the previous manifest and signature back byte for
// byte, or, when it cannot, removes the signature so nothing is left stale, and
// says only what it established.
func failRestoreManifest(vaultRoot, manifestPath string, prior *manifestState, serr error, remedy string) error {
	base := fmt.Errorf("%w: %w", ErrManifestSigning, serr)
	if prior == nil {
		// A caller bug: failRestore without the prior state. Fall back to the
		// safest claim available.
		return failLeaveManifestUnsigned(manifestPath, base)
	}
	var restoreErrs []error
	if prior.hasManifest {
		if err := restoreBytes(vaultRoot, manifestPath, prior.manifest); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore manifest %s: %w", manifestPath, err))
		}
	}
	if prior.hasSig {
		if err := restoreBytes(vaultRoot, SignaturePath(manifestPath), prior.sig); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore signature %s: %w", SignaturePath(manifestPath), err))
		}
	}
	if len(restoreErrs) == 0 {
		if !prior.hasSig {
			// There was no signature before; make sure none exists now.
			if confirmed, rerr := removeSignature(manifestPath); !confirmed {
				return fmt.Errorf("%w; the previous manifest was restored but a signature at %s is POSSIBLY STALE because it could not be removed (%w)",
					base, SignaturePath(manifestPath), rerr)
			}
		}
		return fmt.Errorf("%w; the previous manifest and signature were restored byte for byte, nothing changed", base)
	}
	joined := errors.Join(restoreErrs...)
	if confirmed, rerr := removeSignature(manifestPath); !confirmed {
		return fmt.Errorf("%w; AND restoring the previous state failed (%w); AND the signature at %s could not be removed (%w), "+
			"so it is POSSIBLY STALE against whatever manifest is on disk now; inspect and repair by hand",
			base, joined, SignaturePath(manifestPath), rerr)
	}
	return fmt.Errorf("%w; AND restoring the previous state failed (%w); the signature was removed, so the manifest as it stands is UNSIGNED; %s",
		base, joined, remedy)
}

// postBackupState describes the exact on-disk state after a Create failure that
// came AFTER the prior manifest and signature were moved aside, so a caller (and
// an operator reading a log) is never left to guess which files exist.
func postBackupState(archivePath, manifestPath, bakPath, sigBakPath string, manifestBackedUp bool) string {
	live := func(p string) string {
		if _, err := os.Lstat(p); err == nil {
			return "present"
		}
		return "absent"
	}
	prevManifest := "no previous manifest was moved (none was present)"
	if manifestBackedUp {
		prevManifest = "the previous manifest is preserved at " + bakPath
	}
	prevSig := "there was no previous signature to move"
	if sigBakPath != "" {
		prevSig = "the previous signature is preserved at " + sigBakPath
	}
	return fmt.Sprintf("on disk now: the new archive %s is present; %s; %s; a live manifest at %s is %s; a live signature at %s is %s",
		archivePath, prevManifest, prevSig, manifestPath, live(manifestPath), SignaturePath(manifestPath), live(SignaturePath(manifestPath)))
}
