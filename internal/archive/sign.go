// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Signing modes. Phase 6 supports these two; others can be added by
// extending the switch in Sign / VerifySignature.
const (
	SignModeNone = ""
	SignModeSSH  = "ssh"
	SignModeGPG  = "gpg"
)

// DefaultSSHNamespace is used when SignOptions.Namespace is empty.
// ssh-keygen -Y sign requires a namespace; making it archive-specific
// prevents a signature produced here from being accepted for a
// different protocol by mistake.
const DefaultSSHNamespace = "vibe-palace-archive"

// SignOptions controls whether and how an archive's manifest is
// signed. Mode=="" disables signing.
type SignOptions struct {
	Mode string // "", "ssh", "gpg"

	// SSH: path to the private key used by ssh-keygen -Y sign.
	// GPG: optional key id passed via -u (empty uses default key).
	Key string

	// SSH only: signing namespace. Defaults to DefaultSSHNamespace.
	Namespace string

	// NonInteractive makes the signer unable to prompt: no stdin, no controlling
	// terminal (ssh-keygen reads a passphrase from /dev/tty, not stdin), no
	// askpass program and, for gpg, no pinentry. A key that needs a passphrase
	// then fails at once instead of prompting or hanging. Only the signed project
	// rename sets it; the zero value keeps every other caller (vp archive create
	// --sign, the link re-sign) byte for byte as it was.
	NonInteractive bool

	// Timeout, when positive, kills the signer subprocess after that long. Zero
	// means no bound (the historical behavior).
	Timeout time.Duration
}

// Enabled reports whether these options will produce a signature.
func (s SignOptions) Enabled() bool { return s.Mode != "" }

// VerifyOptions controls signature verification during `vp archive verify`.
type VerifyOptions struct {
	// AllowedSigners, for SSH, is a path to an OpenSSH allowed_signers
	// file used by ssh-keygen -Y verify. Without it, signature
	// verification is skipped (hash verification still runs).
	AllowedSigners string

	// Identity, for SSH, matches against the allowed_signers file's
	// principal field (e.g., an email).
	Identity string
}

// ErrSignatureMissing indicates no .sig file exists alongside the manifest.
var ErrSignatureMissing = errors.New("signature file missing")

// SignaturePath returns the canonical .sig path for a manifest.
func SignaturePath(manifestPath string) string {
	return manifestPath + ".sig"
}

// Sign produces a detached signature over the manifest bytes and
// writes it next to the manifest as <manifest>.sig. Signing is
// shelled out to ssh-keygen / gpg to avoid a native crypto dependency
// and to reuse the user's existing keyring configuration.
//
// Manifest signing (rather than archive signing) is deliberate: the
// manifest already pins the archive via source_sha256, so a valid
// manifest signature transitively vouches for the compressed bytes.
// ADR-001, "Alternatives considered" documents the trade-off.
func Sign(manifestPath string, opts SignOptions) error {
	switch opts.Mode {
	case SignModeNone:
		return nil
	case SignModeSSH:
		return signSSH(manifestPath, opts)
	case SignModeGPG:
		return signGPG(manifestPath, opts)
	default:
		return fmt.Errorf("unknown signing mode %q", opts.Mode)
	}
}

func signSSH(manifestPath string, opts SignOptions) error {
	if opts.Key == "" {
		return fmt.Errorf("ssh signing requires a key path")
	}
	if _, err := os.Stat(opts.Key); err != nil {
		return fmt.Errorf("ssh signing key: %w", err)
	}
	ns := opts.Namespace
	if ns == "" {
		ns = DefaultSSHNamespace
	}
	sigPath := SignaturePath(manifestPath)
	// ssh-keygen -Y sign writes <input>.sig; we ensure any prior
	// signature is cleared first so a stale sig cannot linger after
	// a failed sign attempt.
	_ = os.Remove(sigPath)
	ctx, cancel := signContext(opts)
	defer cancel()
	cmd := signCommand(ctx, opts, "ssh-keygen", []string{"-Y", "sign", "-n", ns, "-f", opts.Key, manifestPath})
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh-keygen sign: %w: %s", err, string(out))
	}
	if _, err := os.Stat(sigPath); err != nil {
		return fmt.Errorf("ssh-keygen sign produced no signature at %s: %w", sigPath, err)
	}
	return nil
}

func signGPG(manifestPath string, opts SignOptions) error {
	sigPath := SignaturePath(manifestPath)
	_ = os.Remove(sigPath)
	args := []string{"--batch", "--yes"}
	if opts.NonInteractive {
		// Never open a pinentry window: a key that needs a passphrase and has no
		// agent-cached one fails here.
		args = append(args, "--pinentry-mode", "error")
	}
	args = append(args, "--armor", "--output", sigPath, "--detach-sign")
	if opts.Key != "" {
		args = append(args, "-u", opts.Key)
	}
	args = append(args, manifestPath)
	ctx, cancel := signContext(opts)
	defer cancel()
	cmd := signCommand(ctx, opts, "gpg", args)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gpg detach-sign: %w: %s", err, string(out))
	}
	return nil
}

// signContext is the context a signer subprocess runs under: bounded by
// opts.Timeout when set, unbounded otherwise.
func signContext(opts SignOptions) (context.Context, context.CancelFunc) {
	if opts.Timeout > 0 {
		return context.WithTimeout(context.Background(), opts.Timeout)
	}
	return context.WithCancel(context.Background())
}

// signCommand is THE one builder of a signer subprocess. The signed rename's
// preflight probe and its real re-signing both build their command here, so the
// probe proves exactly the command and environment the rewrite will run.
//
// With opts.NonInteractive the child gets: a null stdin (the zero Cmd.Stdin);
// no SSH_ASKPASS and SSH_ASKPASS_REQUIRE=never, so no askpass program runs; and
// no controlling terminal (detachFromTerminal: setsid on unix, a detached
// console-less process on Windows), because ssh-keygen opens the terminal
// device itself for a passphrase and a null stdin does not stop that. A hung
// child is killed by the context (WaitDelay bounds the wait on its pipes).
func signCommand(ctx context.Context, opts SignOptions, name string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	if !opts.NonInteractive {
		return cmd
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SSH_ASKPASS=") || strings.HasPrefix(kv, "SSH_ASKPASS_REQUIRE=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env, "SSH_ASKPASS_REQUIRE=never")
	cmd.WaitDelay = 2 * time.Second
	detachFromTerminal(cmd)
	return cmd
}

// VerifySignature validates the signature next to the manifest. It
// returns ErrSignatureMissing if no .sig exists. The mode is inferred
// from the manifest's sibling files: SSH signatures written by
// ssh-keygen -Y sign begin with "-----BEGIN SSH SIGNATURE-----"; GPG
// armored signatures begin with "-----BEGIN PGP SIGNATURE-----".
//
// For SSH, AllowedSigners and Identity must be set; otherwise the
// signature's cryptographic integrity cannot be anchored to a named
// principal and verification is refused rather than silently skipped.
func VerifySignature(manifestPath string, vo VerifyOptions) error {
	sigPath := SignaturePath(manifestPath)
	sigBytes, err := os.ReadFile(sigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrSignatureMissing
		}
		return fmt.Errorf("read signature: %w", err)
	}
	header := string(sigBytes)
	switch {
	case containsHeader(header, "-----BEGIN SSH SIGNATURE-----"):
		return verifySSH(manifestPath, vo)
	case containsHeader(header, "-----BEGIN PGP SIGNATURE-----"):
		return verifyGPG(manifestPath)
	default:
		return fmt.Errorf("unrecognized signature format in %s", sigPath)
	}
}

func containsHeader(s, header string) bool {
	// Check only the first ~128 bytes — headers are always at the top.
	max := min(len(s), 128)
	return indexOf(s[:max], header) >= 0
}

func indexOf(haystack, needle string) int {
	// Tiny helper to avoid strings.Contains allocation in a hot-ish
	// path; functionally equivalent.
	n := len(needle)
	for i := 0; i+n <= len(haystack); i++ {
		if haystack[i:i+n] == needle {
			return i
		}
	}
	return -1
}

func verifySSH(manifestPath string, vo VerifyOptions) error {
	if vo.AllowedSigners == "" || vo.Identity == "" {
		return fmt.Errorf("ssh signature verify requires AllowedSigners and Identity")
	}
	sigPath := SignaturePath(manifestPath)
	// ssh-keygen -Y verify reads the signed content from stdin.
	cmd := exec.Command("ssh-keygen", "-Y", "verify",
		"-f", vo.AllowedSigners,
		"-I", vo.Identity,
		"-n", DefaultSSHNamespace,
		"-s", sigPath,
	)
	in, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("open manifest: %w", err)
	}
	defer in.Close()
	cmd.Stdin = in
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ssh-keygen verify: %w: %s", err, string(out))
	}
	return nil
}

func verifyGPG(manifestPath string) error {
	sigPath := SignaturePath(manifestPath)
	cmd := exec.Command("gpg", "--verify", sigPath, manifestPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gpg verify: %w: %s", err, string(out))
	}
	return nil
}
