// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// LockRelPath is the vault-relative path of the templates lock file.
const LockRelPath = ".vibe-palace/templates.lock"

// LockEntry records what the binary last wrote for a single
// vault-relative template path. EmbeddedSHA is the canonical digest
// from the binary's embedded corpus at write time; WrittenAt is the
// UTC wall-clock moment of the materialize/reconcile action.
type LockEntry struct {
	EmbeddedSHA string    `toml:"embedded_sha"`
	WrittenAt   time.Time `toml:"written_at"`
}

// Lock is the in-memory form of <vault>/.vibe-palace/templates.lock.
// Entries keys are vault-relative paths using forward slashes
// (e.g. "Templates/commands/wrap.md"). BurntSushi/toml round-trips
// quoted bare-string keys, so separators survive intact.
type Lock struct {
	Entries map[string]LockEntry `toml:"entries"`
}

// ReadLock loads the templates lock sidecar. An absent file is not
// an error — callers get an empty, initialized Lock so they can
// populate Entries without a nil-map check. Malformed TOML is an
// error. The returned Lock is by value; mutations on the caller's
// copy do not affect future reads.
func ReadLock(vaultRoot string) (Lock, error) {
	lockPath := filepath.Join(vaultRoot, LockRelPath)
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Lock{Entries: make(map[string]LockEntry)}, nil
		}
		return Lock{}, fmt.Errorf("read lock %s: %w", lockPath, err)
	}
	var l Lock
	if _, err := toml.Decode(string(data), &l); err != nil {
		return Lock{}, fmt.Errorf("decode lock %s: %w", lockPath, err)
	}
	if l.Entries == nil {
		l.Entries = make(map[string]LockEntry)
	}
	return l, nil
}

// WriteLock persists the lock sidecar atomically. The write sequence
// is: ensure <vault>/.vibe-palace/ exists with 0o755 → marshal TOML
// → write to "<path>.tmp" with 0o644 → os.Rename to the final path.
// Readers therefore never observe a partially-written file; parallel
// writers converge on last-writer-wins without truncation.
func WriteLock(vaultRoot string, l Lock) error {
	lockPath := filepath.Join(vaultRoot, LockRelPath)
	dir := filepath.Dir(lockPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	if l.Entries == nil {
		l.Entries = make(map[string]LockEntry)
	}
	tmp, err := os.CreateTemp(dir, "templates.lock.*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp lock in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if anything below fails.
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := toml.NewEncoder(tmp).Encode(l); err != nil {
		tmp.Close()
		return fmt.Errorf("encode lock: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp lock: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod tmp lock: %w", err)
	}
	if err := os.Rename(tmpName, lockPath); err != nil {
		return fmt.Errorf("rename tmp lock to %s: %w", lockPath, err)
	}
	return nil
}
