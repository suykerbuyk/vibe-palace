// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"errors"
	"os"
	"path/filepath"
)

// CheckVaultFilesystem probes whether the vault's filesystem accepts a ":" in a
// filename. NTFS/exFAT reject it, which is what forced a vault relocation off a
// Windows path: knowledge-graph triples and other artifacts historically carried
// colons (session IDs, URLs) straight into filenames. The current triple encoder
// no longer emits colons, but this reports the hostile filesystem PLAINLY at
// `vp check` time rather than letting a write fail cryptically later.
//
//   - Pass: the filesystem accepts ":" (ext4/APFS/most POSIX filesystems).
//   - Fail: the filesystem rejects ":" (NTFS/exFAT under WSL, a Windows drive) —
//     the vault should be relocated to a POSIX filesystem.
//   - Skip: no vault configured, or the probe could not be created for an
//     unrelated reason (permissions), which is not evidence either way.
func CheckVaultFilesystem(vaultRoot string) Result {
	r := Result{Name: "Vault filesystem"}
	if vaultRoot == "" {
		r.Status = Skip
		r.Summary = "no vault configured"
		return r
	}
	if _, err := os.Stat(vaultRoot); err != nil {
		r.Status = Skip
		r.Summary = "vault root not present"
		return r
	}

	probe := filepath.Join(vaultRoot, ".vp-fs-probe-a:b")
	// Clean up any stale probe from an interrupted prior run first.
	_ = os.Remove(probe)
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		// EINVAL/ENOTSUP etc. from the ":" is the signal we care about; a
		// permission error is inconclusive and reported as Skip.
		if errors.Is(err, os.ErrPermission) {
			r.Status = Skip
			r.Summary = "could not probe (permission denied)"
			return r
		}
		r.Status = Fail
		r.Summary = "filesystem rejects ':' in filenames (NTFS/exFAT?)"
		r.Details = []string{
			"This vault cannot reliably store artifacts whose names contain ':'.",
			"Relocate the vault to a POSIX filesystem (ext4/APFS): set vault_path in",
			"~/.config/vibe-palace/config.toml, then move the vault directory there.",
		}
		r.Err = err
		return r
	}
	f.Close()
	_ = os.Remove(probe)
	r.Status = Pass
	r.Summary = "accepts ':' in filenames"
	return r
}
