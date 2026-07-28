// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// probeNamePrefix is the fixed head of every filesystem-probe filename. It is
// the only thing the stale sweep below matches on, and it is the pattern the
// vault .gitignore carries (storage.CanonicalGitignorePatterns), so the two
// must stay spelled the same.
const probeNamePrefix = ".vp-fs-probe-"

// probeNameSuffix carries the ":" that is the ENTIRE POINT of this check. If a
// refactor ever drops it the probe still creates a file, still succeeds, and
// still reports Pass — a check that has stopped testing anything while looking
// perfectly healthy. TestProbeNameCarriesTheColon exists solely to catch that.
const probeNameSuffix = "-a:b"

// staleProbeAge is how old a probe file must be before the sweep will delete
// it. A live probe exists for microseconds — create, close, remove, with no
// I/O in between — so anything older than this is residue from a process that
// died mid-check, never a peer's in-flight probe.
//
// The margin is deliberately enormous relative to the probe's real lifetime.
// Getting it wrong in the too-short direction is harmless anyway: the worst
// case is that we delete a suspended peer's probe, whose own os.Remove then
// fails into the `_ =` it was already written with. Getting it wrong in the
// too-long direction leaves litter in a synced vault, which is the failure
// that actually costs someone something.
const staleProbeAge = 5 * time.Minute

// probeSeq disambiguates two probes started inside the same nanosecond tick by
// two goroutines of the SAME process. PID+nanos alone is unique across
// processes but not within one, and vp_check can be dispatched concurrently by
// a single long-lived `vp mcp` server.
var probeSeq atomic.Uint64

// probeName builds a collision-free probe filename. Uniqueness matters more
// than it looks: the vault is synced across machines and shared by several
// projects, and CheckVaultFilesystem now runs from every host's restart
// preflight, so two hosts probing within the same second is ordinary rather
// than exotic. Under the old FIXED name the second one lost the O_EXCL race
// and was told its filesystem rejects ":" — a confident, wrong diagnosis whose
// remediation is "relocate your vault".
func probeName() string {
	return fmt.Sprintf("%s%d-%d-%d%s",
		probeNamePrefix, os.Getpid(), time.Now().UnixNano(), probeSeq.Add(1), probeNameSuffix)
}

// sweepStaleProbes removes probe files left behind by a process that died
// between creating and deleting one. The old fixed-name implementation got
// this for free — it simply removed its own name before re-creating it — and a
// unique name defeats that, so the cleanup has to become an explicit sweep or
// the vault root slowly fills with litter nobody put there.
//
// Litter here is not cosmetic. A probe file at the vault root matches no
// sweepRule in storage.classifyDirty, so it lands in Reported, and Reported
// dirt makes SyncFlow REFUSE TO SYNC ("uncommitted non-artifact file(s) need
// review"). One crashed check would wedge the vault's sync until a human
// deleted a file they never created.
//
// The age gate is what keeps this from being a different race: a peer host's
// in-flight probe is milliseconds old and is left strictly alone. Errors are
// ignored throughout — this is opportunistic cleanup, and a vault we cannot
// tidy is not evidence about whether the filesystem accepts ":".
func sweepStaleProbes(vaultRoot string) {
	// os.ReadDir + prefix match, NOT filepath.Glob: a vault path containing a
	// glob metacharacter ("[" is legal in a directory name) would make Glob
	// silently match nothing, and a sweep that quietly stops sweeping is how
	// litter accumulates unnoticed.
	entries, err := os.ReadDir(vaultRoot)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-staleProbeAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), probeNamePrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(filepath.Join(vaultRoot, e.Name()))
		}
	}
}

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
//     unrelated reason (permissions, or a name collision with a concurrent
//     probe), which is not evidence either way.
//
// The Fail verdict tells a human to MOVE THEIR VAULT, so every error that is
// not specifically "the filesystem refused this name" must degrade to Skip.
// Only a genuinely unexplained creation failure is allowed to Fail.
//
// This is the one check in the registry that writes anything at all. The probe
// is transient — created with O_EXCL, closed with zero bytes written, removed
// immediately — and carries no vault schema, so it is invisible to the surface
// gate's hazard model (format-incompatible data landing in a vault a newer
// binary wrote). vp_check therefore stays Mutating: false; classifying it
// otherwise would make the diagnostic suite refuse to run on exactly the
// surface-incompatible vault whose diagnostics you most need.
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

	// Clear residue from an interrupted prior run before adding our own.
	sweepStaleProbes(vaultRoot)

	probe := filepath.Join(vaultRoot, probeName())
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		// EINVAL/ENOTSUP etc. from the ":" is the signal we care about.
		// Everything else is inconclusive and must not be dressed up as a
		// filesystem verdict.
		if errors.Is(err, os.ErrPermission) {
			r.Status = Skip
			r.Summary = "could not probe (permission denied)"
			return r
		}
		// os.ErrExist is fs.ErrExist; spelled os.* to match os.ErrPermission
		// above. With a PID+nanosecond+counter name this should be
		// unreachable, which is exactly why it is handled explicitly: if the
		// naming scheme ever regresses, the symptom must be an honest "could
		// not tell" and never a false "relocate your vault".
		if errors.Is(err, os.ErrExist) {
			r.Status = Skip
			r.Summary = "could not probe (concurrent probe held the name)"
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
