// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"
)

// ambientRoot is the operator's configured vault root, resolved ONCE at package
// init and then held for the life of the process.
//
// 🔴 WHY init, AND WHY HERE.
//
// init runs before the generated test main calls TestMain, so this snapshot is
// taken against the PRE-hermetic environment: testutil.RunHermetic's
// XDG_CONFIG_HOME redirect, and every later t.Setenv, come after it. A
// write-time resolve would instead read the checked-in fixture, whose
// config.toml deliberately carries no vault_path, and the guard's ambient arm
// would be vacuous in every test in the tree — the inert check this guard
// exists to be the opposite of.
//
// The value is therefore STALE BY DESIGN for the rest of the process, and that
// is the point: a test must not be able to move the definition of "the
// operator's vault" out from under the guard that is watching it.
//
// It lives in surface, not storage, because surface is in EVERY binary that can
// reach a vault write seat, while storage is not: eight packages reach
// atomicfile or vaultfs without linking storage (internal/atomicfile,
// commands, context, enrichment, shims, templates, vaultfs, wrapstate), and
// internal/commands -> templates.PreserveBackup -> vaultfs.Create is a real
// vault write with a caller-supplied root in one of them. An init in storage
// would leave the guard unarmed in exactly those binaries.
var (
	ambientRootMu sync.Mutex
	ambientRoot   string
)

func init() {
	// Gated on the signal: GuardTestVaultWrite returns before it ever reads
	// this value in a non-test process, so the production binary does no
	// config I/O here at all.
	if !testing.Testing() {
		return
	}
	ambientRoot = resolveAmbientRoot()
}

// resolveAmbientRoot reimplements storage.VaultRoot's two field reads as
// leaf-safe logic. surface cannot import internal/storage: that is the import
// cycle storage -> atomicfile -> surface -> storage this package's own header
// records, and ResolveStampDir already made the same move for the vault layout
// ("reimplements the vault-layout mapping as pure string logic").
//
// It mirrors storage.VaultRoot exactly on the three points where a copy can
// silently diverge, each of which internal/storage's TestInlinedAmbientRoot...
// pins against the real resolver:
//
//  1. os.UserConfigDir, NOT os.Getenv("XDG_CONFIG_HOME") — the two differ when
//     XDG is unset, which is the normal state of an operator host, and
//     UserConfigDir is what VaultConfigFilePath uses.
//  2. expandTilde, copied from storage's own, so a ~-prefixed vault_path
//     resolves to the same directory both readers name.
//  3. filepath.Abs, which cleans a trailing separator — the operator's own
//     config carries one.
//
// It deliberately does NOT reimplement tier 1 (.vibe-palace/local.toml) or
// tier 2 ([project_vaults]) of ResolveVaultBinding. This is the tier-3 global
// root only, so a test that reaches a real vault through a tier-1 or tier-2
// binding is caught by the non-temp arm, not by this one.
//
// Every failure is "no ambient root" rather than an error: an absent or
// unreadable host config means there is no configured vault to protect, and a
// tripwire that cannot name a root must not refuse anything.
func resolveAmbientRoot() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	var cfg struct {
		VaultPath string `toml:"vault_path"`
	}
	if _, err := toml.DecodeFile(filepath.Join(dir, "vibe-palace", "config.toml"), &cfg); err != nil {
		return ""
	}
	if cfg.VaultPath == "" {
		return ""
	}
	p, err := expandAmbientTilde(cfg.VaultPath)
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	return abs
}

// expandAmbientTilde is storage.VaultRoot's expandTilde, copied rather than
// imported (the cycle above). Keep the two in step; the parity test in
// internal/storage is what catches a drift.
func expandAmbientTilde(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[1:]), nil
	}
	return path, nil
}

// ambientVaultRoot returns the snapshot, or "" when no vault is configured.
func ambientVaultRoot() string {
	ambientRootMu.Lock()
	defer ambientRootMu.Unlock()
	return ambientRoot
}

// SetAmbientVaultRoot installs root as the ambient vault root and returns a
// function that restores the previous one. Tests only: the real value is the
// init-time snapshot, and a test that needs the guard to fire on a temp
// directory installs that directory here rather than writing the real vault.
// Mirrors atomicfile.SetMisboundRefuse's setter-with-restore shape.
func SetAmbientVaultRoot(root string) (restore func()) {
	ambientRootMu.Lock()
	prev := ambientRoot
	ambientRoot = root
	ambientRootMu.Unlock()
	return func() {
		ambientRootMu.Lock()
		ambientRoot = prev
		ambientRootMu.Unlock()
	}
}

// AmbientVaultRootForTest exposes the snapshot so a test in another package can
// compare it with storage.ResolveGlobalVaultPath (the parity check on the
// copied derivation above) or report it from a helper process.
func AmbientVaultRootForTest() string { return ambientVaultRoot() }
