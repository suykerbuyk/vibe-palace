// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// This file is the small, neutral storage surface `vp vault migrate-to` needs
// (ADR-013 lifecycle design, migrate-to design-v3 §6). It adds no new mechanism:
// every export is a thin wrapper over an existing package-private function, plus
// one read-only verified-copy probe. The staged orchestration and the hard stop
// live in the CLI command, never here — there is deliberately no storage helper
// that performs copy and delete together.

// ProjectVaultBinding returns the vault root the host's global config binds slug
// to in [project_vaults], resolved to an absolute root. ok is false when there
// is no entry for slug; a non-nil err means an entry exists but is unreadable or
// invalid (the same boundVaultRoot validation the resolver applies). migrate-to
// uses it to preflight the re-point that checkBindSlug would otherwise refuse
// only AFTER the irreversible delete.
func ProjectVaultBinding(slug string) (root string, ok bool, err error) {
	bindings, cfgPath, rerr := readProjectVaults()
	if rerr != nil {
		return "", false, rerr
	}
	target, has := bindings[slug]
	if !has {
		return "", false, nil
	}
	root, verr := boundVaultRoot(cfgPath, slug, target)
	if verr != nil {
		return "", true, verr
	}
	return root, true, nil
}

// ErrNothingLeft is the exported form of the leftovers-only sentinel ApplyDelete
// returns when a project is already fully departed and nothing remains under its
// trees. migrate-to treats it as "the delete already landed" on a resume, not an
// error.
var ErrNothingLeft = errNothingLeft

// SameVaultRoot reports whether two paths name the same vault root. Exported
// form of sameVaultRoot, for the migrate-to no-op ("already bound to <dest>")
// check.
func SameVaultRoot(a, b string) bool { return sameVaultRoot(a, b) }

// NormaliseRemoteURL reduces a git remote URL to a scheme/user/port-insensitive
// host/path key, returning ok=false when the input is not a URL it can
// normalise. Exported form of normaliseRemoteURL. migrate-to runs the same
// comparison checkBindSlug does as a preflight before the irreversible delete,
// so the --moved-to URL cannot be one bind would later reject.
func NormaliseRemoteURL(raw string) (string, bool) { return normaliseRemoteURL(raw) }

// PublishOrder returns the full lifecycle push order for the vault at root: the
// remotes.toml order among configured remotes, then every other configured
// remote. Exported form of lifecycleRemoteOrder. migrate-to prints this as the
// complete push blast radius so a configured remote absent from remotes.toml is
// never an unnamed push target.
func PublishOrder(root string) ([]string, error) { return lifecycleRemoteOrder(root) }

// ReadRecordedRemotes parses the tracked .vibe-palace/remotes.toml of the vault
// at root into its recorded published remotes, in file order. A vault without
// the file records nothing (nil, nil).
//
// It is a NEUTRAL reader, distinct from the clone path's readRecordedRemotes: it
// applies no credential or clone validation and surfaces no "refusing to clone"
// error, so a migrate-to caller never mis-names the command in an error.
func ReadRecordedRemotes(root string) ([]RecordedRemote, error) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(remotesFile)))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", remotesFile, err)
	}
	var rec recordedRemotes
	if _, derr := toml.Decode(string(data), &rec); derr != nil {
		return nil, fmt.Errorf("parse %s: %w", remotesFile, derr)
	}
	out := make([]RecordedRemote, 0, len(rec.Remote))
	for _, r := range rec.Remote {
		out = append(out, RecordedRemote{Name: r.Name, URL: r.URL})
	}
	return out, nil
}

// ConfiguredRemoteURL returns the live URL of the named git remote of the vault
// at root (`git remote get-url`). migrate-to uses it only for the F10 edge a
// recorded remotes.toml does not cover: a vault with configured remotes but no
// recorded entry, where the chosen push-order name still needs a URL.
func ConfiguredRemoteURL(root, name string) (string, error) {
	out, err := gitCmd(root, 10*time.Second, "remote", "get-url", name)
	if err != nil {
		return "", fmt.Errorf("read remote %q url of %s: %w", name, root, err)
	}
	url := strings.TrimSpace(out)
	if url == "" {
		return "", fmt.Errorf("remote %q of %s has no url", name, root)
	}
	return url, nil
}

// VerifiedCopyAt reports whether the destination vault's published remote holds
// a verified copy of the CURRENT HEAD of the source vault for slug. It is the
// read-only probe migrate-to uses to decide whether the copy stage can be
// skipped on a resume.
//
// Note the sides: findVerifiedCopy takes the SOURCE root (it reads the source's
// own remote URLs and requires Vp-Copy-Source to be one of them), a FETCHED
// destination snapshot, the slug, and the footprint of SOURCE HEAD. So this
// wrapper takes sourceRoot plus destRemoteURL, not the other way round.
//
// It takes no vault lock and makes no commit, but it is not side-effect free: it
// fetches a private blobless snapshot of destRemoteURL into scratch (network + a
// temporary repo, closed before returning). It writes no vault git tip.
//
// A false result is benign and must not be read as "the copy never happened": if
// the source has MOVED since the copy, no verified copy of the *current* source
// HEAD exists at the destination, which is exactly the signal to run the copy.
// ApplyCopy's own redo/short-circuit then handles an already-applied or
// in-flight copy.
func VerifiedCopyAt(sourceRoot, destRemoteURL, slug string) (present bool, commit string, err error) {
	headOut, err := gitCmd(sourceRoot, 10*time.Second, "rev-parse", "HEAD")
	if err != nil {
		return false, "", fmt.Errorf("read source HEAD of %s: %w", sourceRoot, err)
	}
	head := strings.TrimSpace(headOut)
	fHead, n, err := footprintHash(sourceRoot, head, slug)
	if err != nil {
		return false, "", err
	}
	if n == 0 {
		// The source holds no tracked content for slug at HEAD: there is nothing
		// whose copy could be verified. Fall through to the copy.
		return false, "", nil
	}
	dest, err := fetchDestination(destRemoteURL)
	if err != nil {
		return false, "", err
	}
	defer dest.close()
	c, ferr := findVerifiedCopy(sourceRoot, dest, slug, fHead)
	if ferr != nil {
		// No verified copy of the current source HEAD at the destination tip (or
		// an unreadable candidate): benign for a probe — fall through to the copy.
		// A genuine fetch/permissions failure was already surfaced by
		// fetchDestination above; any error findVerifiedCopy adds here (a missing
		// copy) is reported later, concretely, by ApplyCopy — not swallowed for good.
		return false, "", nil
	}
	return true, c, nil
}
