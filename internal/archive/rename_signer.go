// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// RenameSigner is the storage.ManifestSigner the project rename uses: it proves
// the project's signing works BEFORE anything is written (Preflight) and writes
// every moved manifest through the one signing core (RewriteManifest).
type RenameSigner struct{}

// ErrRenameManifestSigning wraps the cause when the signed rename could not
// re-sign a manifest. The rename is rolled back by its outer rollback.
var ErrRenameManifestSigning error = &renameSigningError{"archive: signed rename could not re-sign a manifest"}

// renameSigningError is the sentinel's type. RenameSigningFailure is the marker
// storage looks for (by interface, so it need not import archive) to append the
// "rolled back" outcome to the error.
type renameSigningError struct{ msg string }

func (e *renameSigningError) Error() string            { return e.msg }
func (*renameSigningError) RenameSigningFailure() bool { return true }

// Bounds and seams, production values by default.
var (
	// renameProbeTimeout bounds the preflight's throwaway sign and verify.
	renameProbeTimeout = 10 * time.Second
	// renameSignTimeout bounds each real re-sign during the rewrite.
	renameSignTimeout = 30 * time.Second
	// renameLockTimeout bounds the per-manifest lock wait (a test shrinks it).
	renameLockTimeout = 30 * time.Second
	// renameTempDir resolves the probe's temp root (a test points it at a vault).
	renameTempDir = os.TempDir
	// renameProve is the preflight's proof that signing works: the cheap key
	// checks plus the throwaway sign and verify. A test that exercises rollback
	// or locking, not signing, replaces it so it runs without ssh-keygen.
	renameProve = func(vaultRoot string, opts SignOptions) string {
		if reason := signingKeyUnusable(opts); reason != "" {
			return reason
		}
		return proveSigning(vaultRoot, opts)
	}
)

// renameSnapshot is what Preflight hands back and RewriteManifest uses as-is:
// the signing options resolved from the SOURCE slug before the move.
type renameSnapshot struct {
	opts SignOptions
}

// Preflight proves the project's configured signing works. It writes nothing in
// the vault and returns the options snapshot resolved from `from`, or a refusal.
func (RenameSigner) Preflight(vaultRoot, from string) (any, string) {
	opts, err := SignOptionsForProject(vaultRoot, from)
	if err != nil {
		return nil, fmt.Sprintf("the project holds signed manifests but its signing options cannot be read: %v", err)
	}
	if !opts.Enabled() {
		return nil, "the project holds signed manifests but [archive] sign_mode is not set, so they cannot be re-signed after the rename"
	}
	// The rename signs non-interactively and bounded, exactly as the probe does.
	opts.NonInteractive = true
	opts.Timeout = renameSignTimeout
	if reason := renameProve(vaultRoot, opts); reason != "" {
		return nil, signRefusal(opts, reason)
	}
	return renameSnapshot{opts: opts}, ""
}

func signRefusal(opts SignOptions, reason string) string {
	return fmt.Sprintf("the project holds signed manifests but signing (mode %s, key %q) is not usable: %s; "+
		"no rename was started and nothing was written. A rename signs non-interactively, so a passphrase-protected key is not supported",
		opts.Mode, opts.Key, reason)
}

// proveSigning signs and verifies a throwaway file OUTSIDE the vault with the
// very command builder the rewrite uses, bounded by renameProbeTimeout. It
// returns "" on success. Before it creates anything it resolves the temp root
// and refuses when that lies inside the vault.
func proveSigning(vaultRoot string, opts SignOptions) string {
	tmpRoot := renameTempDir()
	if err := vaultfs.RefuseDestinationInsideVault(vaultRoot, tmpRoot); err != nil {
		return fmt.Sprintf("the temp directory %s cannot host the signing probe (%v); point TMPDIR/TEMP outside the vault", tmpRoot, err)
	}
	// The temp root is proven outside the vault above; the guard seats the same
	// expression for the source audit (and panics under test if it is the operator's
	// configured vault).
	surface.GuardTestPathWrite(tmpRoot)
	dir, err := os.MkdirTemp(tmpRoot, "vp-signprobe-")
	if err != nil {
		return fmt.Sprintf("cannot create the signing probe directory: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	probe := filepath.Join(dir, "probe.manifest.json")
	if err := os.WriteFile(probe, []byte("{\"probe\": true}\n"), 0o600); err != nil {
		return fmt.Sprintf("cannot write the signing probe: %v", err)
	}
	// One deadline spans the sign AND the verify: the probe as a whole is bounded
	// by renameProbeTimeout, not each subprocess.
	ctx, cancel := context.WithTimeout(context.Background(), renameProbeTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	popts := opts
	popts.Timeout = time.Until(deadline)
	if popts.Timeout <= 0 {
		popts.Timeout = time.Nanosecond
	}
	if err := Sign(probe, popts); err != nil {
		return "the probe signature failed: " + firstLine(err.Error())
	}
	if _, err := os.Stat(SignaturePath(probe)); err != nil {
		return "the probe signature was not produced"
	}
	if out, err := verifyProbe(ctx, popts, probe); err != nil {
		return "the probe signature did not verify: " + firstLine(err.Error()+": "+out)
	}
	return ""
}

// verifyProbe checks the probe's signature. ssh uses check-novalidate, which
// needs no allowed_signers file, so it cannot fail for an unrelated reason.
func verifyProbe(ctx context.Context, opts SignOptions, probe string) (string, error) {
	var cmd *exec.Cmd
	switch opts.Mode {
	case SignModeSSH:
		ns := opts.Namespace
		if ns == "" {
			ns = DefaultSSHNamespace
		}
		cmd = signCommand(ctx, opts, "ssh-keygen", []string{"-Y", "check-novalidate", "-n", ns, "-s", SignaturePath(probe)})
		in, err := os.Open(probe)
		if err != nil {
			return "", err
		}
		defer in.Close()
		cmd.Stdin = in
	case SignModeGPG:
		cmd = signCommand(ctx, opts, "gpg", []string{"--batch", "--verify", SignaturePath(probe), probe})
	default:
		return "", fmt.Errorf("unknown signing mode %q", opts.Mode)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// RewriteManifest writes one moved manifest's new bytes and re-signs it with the
// Preflight snapshot. It runs after the move, with the vault commit lock held by
// the caller, and takes exactly ONE bounded lock: the canonical DESTINATION
// manifest path (the key every other writer of that file uses). Nothing in here
// resolves config, creates a directory or takes a second lock.
func (RenameSigner) RewriteManifest(vaultRoot, destAbs string, data []byte, snapshot any) error {
	snap, haveSnap := snapshot.(renameSnapshot)
	release, err := vaultlock.AcquireWithTimeout(vaultRoot, destAbs, renameLockTimeout)
	if err != nil {
		return fmt.Errorf("%w: lock %s (held by a concurrent capture or link?): %w", ErrRenameManifestSigning, destAbs, err)
	}
	defer func() { _ = release() }()

	wopts := []atomicfile.Option{atomicfile.WithInheritPerm(), atomicfile.WithFsync()}
	signed, err := signatureExists(destAbs)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRenameManifestSigning, err)
	}
	if !signed {
		return writeManifestBytes(vaultRoot, destAbs, data, SignOptions{}, failLeaveUnsigned, nil, restoreRemedyRename, wopts...)
	}
	if !haveSnap {
		// A signature appeared after planning, so no snapshot proves signing.
		return fmt.Errorf("%w: %s is signed but the plan held no signing snapshot", ErrRenameManifestSigning, destAbs)
	}
	prior, err := captureManifestState(destAbs)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRenameManifestSigning, err)
	}
	if err := writeManifestBytes(vaultRoot, destAbs, data, snap.opts, failRestore, prior, restoreRemedyRename, wopts...); err != nil {
		return fmt.Errorf("%w: re-signing %s failed: %w", ErrRenameManifestSigning, destAbs, err)
	}
	return nil
}
