// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"path/filepath"
	"sort"
)

// ManifestSigner is the seam through which the project rename writes a
// transcript manifest. storage owns the interface and archive implements it
// (archive imports storage, so the dependency stays one way).
type ManifestSigner interface {
	// Preflight proves the project's configured signing works. It writes nothing
	// in the vault. It returns an options snapshot resolved from `from` (opaque
	// to storage) or a refusal reason.
	Preflight(vaultRoot, from string) (snapshot any, refusal string)
	// RewriteManifest is the ONLY way rename writes a *.manifest.json. destAbs is
	// the manifest's path AFTER the move; snapshot is Preflight's, unchanged (nil
	// when the project holds no signed manifest).
	RewriteManifest(vaultRoot, destAbs string, data []byte, snapshot any) error
}

// renameManifestPreflight is the planning-time check for a project's transcript
// manifests. Any manifest needs a Signer (decision: fail closed, since storage
// has no other manifest writer); a signed one additionally needs the signer's
// Preflight proof. It writes nothing.
func renameManifestPreflight(vault, from string, signer ManifestSigner) (snapshot any, refusals []string) {
	dir := filepath.Join(vault, "Projects", from, "transcripts")
	manifests, err := filepath.Glob(filepath.Join(dir, "*.manifest.json"))
	if err != nil || len(manifests) == 0 {
		return nil, nil
	}
	if signer == nil {
		return nil, []string{"rename needs a manifest writer: this project holds transcript manifests and this caller did not configure a ManifestSigner"}
	}
	signed, _ := filepath.Glob(filepath.Join(dir, "*.manifest.json.sig"))
	if len(signed) == 0 {
		return nil, nil
	}
	sort.Strings(signed)
	snap, reason := signer.Preflight(vault, from)
	if reason != "" {
		return nil, []string{reason}
	}
	return snap, nil
}

// renameSigningFailure reports whether cause is a manifest re-signing failure,
// which gets the "rolled back" suffix. Detected by the typed marker so storage
// does not import archive.
func renameSigningFailure(cause error) bool {
	var m interface{ RenameSigningFailure() bool }
	return errors.As(cause, &m) && m.RenameSigningFailure()
}
