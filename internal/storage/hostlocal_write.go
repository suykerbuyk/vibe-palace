// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteHostLocalWithBackup replaces a file that lives OUTSIDE the vault (the
// global config, a checkout's .vibe-palace.toml) with next, keeping old as
// <path>.bak. It returns the backup's path.
//
// Host-local files are not vault files, so this takes no vaultlock and does
// not use atomicfile's permission/fsync semantics. It keeps a .bak because,
// unlike a vault file, there may be no committed copy standing behind the
// file, so the backup is the only pre-image it has. The write is
// temp-then-rename, so a reader sees the old bytes or the new ones, never a
// torn mix.
//
// 🔴 IT KEEPS THE FILE WHAT IT WAS. The existing file's permission bits are
// preserved — a 0600 config holding an API key stays 0600, and its .bak and
// temp are created 0600 too — and a symlinked path (a dotfile manager's
// config.toml) is written THROUGH to its resolved target, which is replaced in
// place, rather than the link being replaced by a regular file.
//
// It is the one implementation behind `vp config upgrade` (reconcile's
// host-local branch), `vp config bind` and the checkout rebind; the error
// texts are theirs.
func WriteHostLocalWithBackup(path string, old, next []byte) (string, error) {
	target, mode, err := hostLocalTarget(path)
	if err != nil {
		return "", err
	}
	backupPath := path + ".bak"
	if err := writeFileMode(backupPath, old, mode); err != nil {
		return "", fmt.Errorf("create backup: %w", err)
	}
	if err := replaceFileMode(target, next, mode); err != nil {
		return "", err
	}
	return backupPath, nil
}

// restoreHostLocalCAS puts prev back at path ONLY if path still holds wrote —
// the bytes this process wrote. A file that changed since is another writer's,
// and overwriting it with an older pre-image would lose their change: it is
// left alone and the error says so. The .bak is not touched, so it remains the
// pre-image a failed restore points at. Mode and symlink handling are
// WriteHostLocalWithBackup's.
func restoreHostLocalCAS(path string, wrote, prev []byte) error {
	cur, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-read %s before restoring: %w", path, err)
	}
	if !bytes.Equal(cur, wrote) {
		return fmt.Errorf("%s changed after this write (another writer); it was NOT restored", path)
	}
	target, mode, err := hostLocalTarget(path)
	if err != nil {
		return err
	}
	return replaceFileMode(target, prev, mode)
}

// hostLocalTarget resolves path through any symlinks to the file actually
// written, and returns that file's permission bits (0644 if it cannot be
// read, which only happens when it does not exist yet).
func hostLocalTarget(path string) (string, fs.FileMode, error) {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", 0, fmt.Errorf("resolve %s: %w", path, err)
	}
	mode := fs.FileMode(0o644)
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
	}
	return target, mode, nil
}

// replaceFileMode replaces target with data at mode, temp-then-rename in
// target's own directory.
func replaceFileMode(target string, data []byte, mode fs.FileMode) error {
	return writeFileMode(target, data, mode)
}

// writeFileMode writes data to path with exactly mode, and never lets the
// bytes sit in a file of any other mode — not even briefly.
//
// 🔴 A NEW FILE, NEVER THE OLD ONE. Truncating and rewriting an existing file
// (os.WriteFile) puts the new bytes into it while it still has its OLD mode: a
// .bak left 0644 by an older vp would hold a 0600 config's bytes, world
// readable, until a chmod caught up. Instead the bytes go into a fresh temp
// file created 0600 (os.CreateTemp), set to mode BEFORE anything is written,
// then renamed over path — so path holds either its old bytes or the new ones
// at the new mode. The explicit chmod also defeats the umask, which would
// otherwise narrow a 0644 config's rewrite.
func writeFileMode(path string, data []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	tmpPath := tmp.Name()
	fail := func(prefix string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s: %w", prefix, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail("write temp", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail("write temp", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
