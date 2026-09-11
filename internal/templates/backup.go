// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// Backup is where PreserveBackup put (or found) a copy of a file's bytes.
type Backup struct {
	// Rel is the backup's vault-relative path, with forward slashes.
	Rel string
	// Reused is true when a backup holding exactly these bytes already existed
	// under Rel, so nothing was written.
	Reused bool
}

// ErrBackupCollision means the backup name for some bytes is already taken by a
// file holding different bytes. The name is derived from the bytes, so the
// realistic cause is an operator-edited backup. Nothing is overwritten.
var ErrBackupCollision = errors.New("backup name already holds different bytes")

// backupHashLen is how many hex characters of the content's sha256 name a
// backup. Twelve, as internal/archive/archive.go names its backups: long enough
// that two different files of one template never meet in practice, short
// enough to read.
const backupHashLen = 12

// BackupName returns the backup path PreserveBackup uses for data taken from
// the vault-relative file rel: "<rel>.<first 12 hex of sha256(data)>.bak". The
// name is a function of the bytes, so the same bytes always get the same name
// and a dry run can print exactly the name a real run will use. It always ends
// in ".bak" (every lister, the stamp resolver and the canonical .gitignore
// skip that suffix) and never contains a ':' (so it is valid on Windows).
func BackupName(rel string, data []byte) string {
	sum := sha256.Sum256(data)
	return rel + "." + hex.EncodeToString(sum[:])[:backupHashLen] + ".bak"
}

// PreserveBackup keeps a copy of data, the current bytes of the vault file
// rel, under BackupName(rel, data), and never overwrites anything to do it.
//
//   - Nothing at the name: the backup is created through vaultfs.Create — the
//     locked, no-clobber, fsynced create — and Reused is false.
//   - A regular file holding exactly data: nothing is written; Reused is true.
//     Resetting the same bytes twice therefore keeps one backup.
//   - Anything else (different bytes, a symlink, a directory):
//     ErrBackupCollision, and the file there is left alone.
//
// The bare "<rel>.bak" is never written or read. Older binaries overwrite that
// fixed name on every reset, so it can never be trusted to hold a backup, and an
// existing one is somebody else's to keep.
//
// This is the one backup policy for every template reset, commands and skills
// alike; there is deliberately no policy parameter.
func PreserveBackup(vaultRoot, rel string, data []byte) (Backup, error) {
	name := BackupName(rel, data)
	_, err := vaultfs.Create(vaultRoot, name, string(data))
	if err == nil {
		return Backup{Rel: name}, nil
	}
	if !errors.Is(err, vaultfs.ErrExists) {
		return Backup{}, fmt.Errorf("write backup %s: %w", name, err)
	}
	abs := filepath.Join(vaultRoot, filepath.FromSlash(name))
	fi, err := os.Lstat(abs)
	if err != nil {
		return Backup{}, fmt.Errorf("inspect existing backup %s: %w", name, err)
	}
	if !fi.Mode().IsRegular() {
		return Backup{}, fmt.Errorf("refusing to use %s: it exists and is not a regular file (%s): %w",
			name, fi.Mode().Type(), ErrBackupCollision)
	}
	have, err := os.ReadFile(abs)
	if err != nil {
		return Backup{}, fmt.Errorf("read existing backup %s: %w", name, err)
	}
	if sha256.Sum256(have) != sha256.Sum256(data) {
		return Backup{}, fmt.Errorf("refusing to overwrite %s: it exists with different bytes "+
			"(an edited backup?) — move or rename it, then run the reset again: %w", name, ErrBackupCollision)
	}
	return Backup{Rel: name, Reused: true}, nil
}

// HashFile returns the hex sha256 of a file on disk. A missing file
// returns ("", err) with an error that satisfies os.IsNotExist.
func HashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
