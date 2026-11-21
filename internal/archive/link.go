// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LinkSessionNote updates an existing manifest's VaultRelSessionNote
// field in place. Used by vp_capture_session after writing a session
// note so the archive -> note link is bidirectional (ADR-001 Phase 4).
//
// The write is crash-safe at the manifest level — a failed write leaves the
// prior manifest intact — because WriteManifest goes through the shared
// temp-plus-rename primitive. There is still no fsync contract.
//
// vaultRoot reaches the primitive's surface stamp; pass "" to skip stamping
// (e.g. manifests outside any vault). "" also means there is no lock namespace,
// so the read-modify-write below runs unserialized — see lockManifest.
//
// # LOCKING POSTURE: BLOCKING
//
// This form takes the manifest's per-path vaultlock with a BLOCKING LOCK_EX
// that has NO TIMEOUT (ADR-003). Under contention it waits, forever if it must.
// That is correct for every caller it has — internal/capture, and
// vaultaudit.ApplyBackfill behind the mutates()-wrapped `vp archive link` /
// `vp_archive_link` — because all of them are already fail-stop.
//
// 🔴 IT IS NOT CORRECT EVERYWHERE. A caller with no timeout and no
// cancellation does not degrade when this blocks; it HANGS, with no error and
// no log. internal/hook is exactly that shape, which is why it calls
// TryLinkSessionNote instead. Before you reach for this function from a new
// path, ask whether that path can afford to wait forever. If it cannot, use
// TryLinkSessionNote and route ErrManifestLocked into a non-fatal branch.
//
// NESTING: the caller must NOT already hold this manifest's vaultlock — flock
// does not recurse, so a second acquire self-deadlocks permanently.
// ApplyBackfill's "sessions-directory lock, then release, THEN LinkSessionNote"
// sequence exists for this reason and must stay sequential.
func LinkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote string) error {
	return linkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote, LockBlocking)
}

// TryLinkSessionNote is the NON-BLOCKING sibling of LinkSessionNote: same
// read-modify-write, same result, but it REFUSES rather than waits when another
// writer already holds the manifest's per-path lock, returning an error that
// wraps ErrManifestLocked and writing nothing.
//
// It exists for internal/hook. `cmd/vp/commands.go` registers cmdHook UNWRAPPED
// and `cmd/vp/cmd_hook.go` passes context.Background() into hook.Run, so there
// is no timeout anywhere on that path: a blocking acquire there would wedge
// SessionEnd silently. Losing a back-link is already a non-fatal, logged outcome
// in that code; hanging a SessionEnd is not an outcome at all.
//
// Callers must treat ErrManifestLocked as a routine contention result, not a
// fault: check it with errors.Is and log rather than fail.
func TryLinkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote string) error {
	return linkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote, LockNonBlocking)
}

// linkSessionNote is the one implementation behind both exported forms. The
// lock spans the whole ReadManifest -> mutate -> WriteManifest sequence, because
// the lost-update window opens at the READ: without it, a linker that read
// before a concurrent Create's rename could write its stale body back and roll
// that Create's record out from under the archive that is actually on disk.
//
// The lock is taken HERE, never inside WriteManifest — that primitive is shared
// with Create and sits under vaultaudit's documented note-then-manifest
// sequence, and a non-reentrant, timeout-free LOCK_EX inside it would make that
// sequence a lock-order constraint (ADR-003).
func linkSessionNote(vaultRoot, manifestPath, vaultRelSessionNote string, posture LockPosture) error {
	release, err := lockManifest(vaultRoot, manifestPath, posture)
	if err != nil {
		return err
	}
	defer release()

	m, err := ReadManifest(manifestPath)
	if err != nil {
		return err
	}
	if m.VaultRelSessionNote == vaultRelSessionNote {
		return nil // idempotent no-op
	}
	m.VaultRelSessionNote = vaultRelSessionNote

	// A rewrite keeps a SIGNED manifest signed, or refuses, or says it could not.
	// "Signed" means a .sig already sits beside the manifest: the callers here
	// have no sign options and config can change after a manifest was signed, so
	// the sidecar is the signal. An unsigned manifest stays unsigned and costs
	// one stat, no config read (the hook's hot path).
	signed, err := signatureExists(manifestPath)
	if err != nil {
		return err
	}
	if !signed {
		// Was atomicWriteManifest: a hand-rolled WriteManifest-to-".tmp" plus
		// os.Rename. WriteManifest now owns the temp file and the rename, so the
		// wrapper had nothing left to do, and it stamps, so the stampVault call
		// that used to follow this line is gone with it.
		return writeManifestSigned(vaultRoot, manifestPath, m, SignOptions{}, failLeaveUnsigned, nil)
	}
	signOpts, reason := resignOptions(vaultRoot, m.ProjectSlug)
	if reason != "" {
		// Nothing has been written: the manifest and its signature are as they
		// were and still verify. The operator either restores the signing
		// configuration or removes the .sig deliberately.
		return fmt.Errorf("%w: %s (%s); the manifest and its signature were left untouched; restore the project's [archive] signing "+
			"configuration, or remove %s deliberately so the link can proceed unsigned",
			ErrSignedManifestCannotResign, manifestPath, reason, SignaturePath(manifestPath))
	}
	prior, err := captureManifestState(manifestPath)
	if err != nil {
		return err
	}
	return writeManifestSigned(vaultRoot, manifestPath, m, signOpts, failRestore, prior)
}

// resignOptions resolves the signing options for a manifest that already has a
// signature, from the manifest's OWN project (so a bound vault resolves its own
// configuration). A non-empty reason means they cannot produce a signature.
func resignOptions(vaultRoot, projectSlug string) (SignOptions, string) {
	if vaultRoot == "" {
		return SignOptions{}, "no vault root, so there is no project configuration to sign with"
	}
	opts, err := SignOptionsForProject(vaultRoot, projectSlug)
	if err != nil {
		return SignOptions{}, err.Error()
	}
	if !opts.Enabled() {
		return SignOptions{}, "the project's [archive] sign_mode is not set"
	}
	if reason := signingKeyUnusable(opts); reason != "" {
		return SignOptions{}, reason
	}
	return opts, ""
}

// listSecretKeys is `gpg --batch --list-secret-keys --with-colons [key]`; a test
// replaces it. It signs nothing.
var listSecretKeys = func(key string) ([]byte, error) {
	args := []string{"--batch", "--list-secret-keys", "--with-colons"}
	if key != "" {
		args = append(args, key)
	}
	return exec.Command("gpg", args...).Output()
}

// signingKeyUnusable is the pre-flight for a re-sign: it returns a non-empty
// reason when the configured key cannot produce a signature, so the link refuses
// BEFORE writing anything (D2) instead of discovering the problem at the signer
// after the manifest was already rewritten. It is the cheapest reliable check and
// it never signs:
//
//   - ssh: ssh-keygen is on PATH, and the key file exists, is a regular file and
//     is readable (the same stat signSSH does first, plus a read-open);
//   - gpg: gpg is on PATH and `gpg --list-secret-keys [key]` succeeds and lists a
//     secret key (a named key must resolve; with no key named, some secret key
//     must exist).
func signingKeyUnusable(opts SignOptions) string {
	switch opts.Mode {
	case SignModeSSH:
		if _, err := exec.LookPath("ssh-keygen"); err != nil {
			return "ssh-keygen is not on PATH"
		}
		if opts.Key == "" {
			return "ssh signing requires a key path and [archive] sign_key is not set"
		}
		fi, err := os.Stat(opts.Key)
		if err != nil {
			return fmt.Sprintf("the ssh signing key is not usable: %v", err)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Sprintf("the ssh signing key %s is not a regular file", opts.Key)
		}
		f, err := os.Open(opts.Key)
		if err != nil {
			return fmt.Sprintf("the ssh signing key is not readable: %v", err)
		}
		_ = f.Close()
	case SignModeGPG:
		if _, err := exec.LookPath("gpg"); err != nil {
			return "gpg is not on PATH"
		}
		out, err := listSecretKeys(opts.Key)
		if err != nil {
			return fmt.Sprintf("the gpg signing key does not resolve (gpg --list-secret-keys %q): %v", opts.Key, err)
		}
		if !strings.Contains(string(out), "sec:") {
			return fmt.Sprintf("gpg lists no secret key for %q", opts.Key)
		}
	default:
		return fmt.Sprintf("unknown signing mode %q", opts.Mode)
	}
	return ""
}

// VaultRelPath returns the vault-relative form of an absolute path,
// with forward slashes regardless of OS. Returns the input unchanged
// if it is not inside vaultRoot.
func VaultRelPath(vaultRoot, abs string) string {
	rel, err := filepath.Rel(vaultRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return filepath.ToSlash(rel)
}
