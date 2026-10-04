// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// VaultMigrated reports whether the vault carries the migration marker,
// authored_only in .vibe-palace/vault.toml (ADR-014, "the migration marker").
// The marker is never inferred from the ignore lines. A malformed marker is an
// error naming the key, never a silent "unmigrated".
func VaultMigrated(root string) (bool, error) {
	m, err := surface.ReadVaultManifest(root)
	if err != nil {
		return false, err
	}
	return m.AuthoredOnly != "", nil
}

// RenamePending reads this host's rename-pending record for project: the slug it
// is being renamed to. ok is false when there is no record.
func (v *Vault) RenamePending(project string) (to string, ok bool, err error) {
	path, err := v.IndexRenamePendingPath(project)
	if err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read rename-pending record: %w", err)
	}
	to = strings.TrimSpace(string(data))
	if err := slug.Validate(to); err != nil {
		return "", false, fmt.Errorf("rename-pending record %s: target: %w", path, err)
	}
	return to, true, nil
}

// ListRenamePending returns every rename-pending record on this host, old slug
// to target. An absent directory is no records. A record whose name or content
// is not a valid slug is an error, so a damaged record is reported, never
// silently dropped.
func (v *Vault) ListRenamePending() (map[string]string, error) {
	entries, err := os.ReadDir(v.IndexRenamePendingDir())
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list rename-pending records: %w", err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		// A dot-name is not a record: an atomic write's temp file, say.
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		to, ok, err := v.RenamePending(e.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			out[e.Name()] = to
		}
	}
	return out, nil
}

// IndexReapable reports whether project's host-local index store,
// palace/.local/index/<project>/, may be removed. It is the one rule the index
// sweep scans by and the lifecycle removal re-checks under the project's commit
// lock (indexstore.LifecycleTx.RemoveProject). It is true only when:
//
//   - ProjectExists(project) is false. That is the rule indexstore.Lock refuses
//     by, so nothing reapable is writable and nothing writable is reaped. It is
//     NOT the embed cache's keep rule: a palace/<project>/ holding only .local/
//     is not a store, and a departed slug whose palace/<project>/ still holds an
//     untracked regular file (the legacy ingested-archives.jsonl) is one;
//   - this host holds no rename-pending record for project. Only the host that
//     runs vp vault rename has one, and the store stays until the record goes.
//
// reason names why a store is kept, for the sweep's report.
func (v *Vault) IndexReapable(project string) (ok bool, reason string, err error) {
	exists, err := v.ProjectExists(project)
	if err != nil {
		return false, "", err
	}
	if exists {
		return false, "the project exists", nil
	}
	to, pending, err := v.RenamePending(project)
	if err != nil {
		return false, "", err
	}
	if pending {
		return false, "rename pending to " + to, nil
	}
	return true, "", nil
}

// IndexReapCandidates lists the projects whose index store IndexReapable allows
// the sweep to remove. Dot-named entries (the counter, tombstones, the
// rename-pending records) are never candidates.
//
// It reuses the embed-cache sweep's guards, so a vault that is far likelier
// mis-resolved or unreadable than empty removes nothing: a missing or empty index
// root, a symlinked index root or palace/.local, a Projects/ that is not a real
// directory, and an empty project listing all yield no candidates.
func (v *Vault) IndexReapCandidates() ([]string, error) {
	root := v.IndexRootDir()
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read index root: %w", err)
	}
	var slugs []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || !e.IsDir() || slug.Validate(e.Name()) != nil {
			continue
		}
		slugs = append(slugs, e.Name())
	}
	if len(slugs) == 0 {
		return nil, nil
	}
	if !v.indexSweepDecidable(root) {
		return nil, nil
	}
	var out []string
	for _, s := range slugs {
		ok, _, err := v.IndexReapable(s)
		if err != nil {
			return out, err
		}
		if ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// IndexTombstones lists the tombstoned index directories under the index root,
// as absolute paths, sorted.
func (v *Vault) IndexTombstones() ([]string, error) {
	root := v.IndexRootDir()
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read index root: %w", err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), IndexTombstonePrefix) {
			out = append(out, filepath.Join(root, e.Name()))
		}
	}
	if len(out) > 0 && !v.indexRootNotSymlinked(root) {
		return nil, nil
	}
	sort.Strings(out)
	return out, nil
}

// indexRootNotSymlinked reports that neither the index root nor palace/.local
// above it is a symlink: a removal through a symlinked root would delete
// wherever it points, outside this vault.
func (v *Vault) indexRootNotSymlinked(root string) bool {
	for _, p := range []string{filepath.Dir(root), root} {
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&fs.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// indexSweepDecidable applies the embed-cache sweep's guards (scanCaches).
func (v *Vault) indexSweepDecidable(root string) bool {
	if !v.indexRootNotSymlinked(root) {
		return false
	}
	if fi, err := os.Stat(filepath.Join(v.Root, "Projects")); err != nil || !fi.IsDir() {
		return false
	}
	presence, err := v.ListAllProjects()
	return err == nil && len(presence) > 0
}
