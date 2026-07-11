// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// ResumeConflictError is the typed compare-and-set refusal from WriteResume. It
// carries the ACTUAL current on-disk digest so a caller can build its retry
// payload (and its user-facing message) without a second read and without
// scraping the error string. Current is the literal "(absent)" when the file
// does not exist.
//
// It unwraps to vaultfs.ErrShaConflict so errors.Is(err, vaultfs.ErrShaConflict)
// holds uniformly across BOTH compare-and-set writers in the repo — vaultfs.Write
// and storage.WriteResume — rather than forking a second conflict concept.
type ResumeConflictError struct {
	Current  string // current on-disk sha256, or "(absent)"
	Expected string // the sha the caller asserted; "" means "assert absent"
}

func (e *ResumeConflictError) Error() string {
	expected := e.Expected
	if expected == "" {
		expected = "absent (empty expected_sha256 asserts no resume.md exists yet)"
	}
	return fmt.Sprintf("%s: have %s, expected %s", vaultfs.ErrShaConflict, e.Current, expected)
}

// Unwrap makes errors.Is(err, vaultfs.ErrShaConflict) true.
func (e *ResumeConflictError) Unwrap() error { return vaultfs.ErrShaConflict }

// WriteResume writes content to the project's resume file, creating directories
// as needed. This writes to the tier-1 (project override) path so the resolver
// picks it up immediately.
//
// The write is COMPARE-AND-SET: there is no blind whole-file overwrite path.
//
//   - expectedSha256 non-empty: the resume's current on-disk SHA-256 must equal
//     it, or the write is refused with vaultfs.ErrShaConflict and the file is
//     left untouched.
//   - expectedSha256 == "": assert-absent. The write succeeds only if no resume
//     file exists yet (the first-wrap / bootstrap create). If one DOES exist the
//     call is a conflict and is refused — an omitted guard can never silently
//     degrade to last-writer-wins.
//
// The compare happens INSIDE the held per-path lock (a compare outside it is a
// TOCTOU), and — like EditResume, whose doc comment explains the trap in full —
// the write goes through atomicfile.Write directly rather than lockedWrite:
// lockedWrite re-acquires the same lock, and vaultlock.Acquire is a blocking
// LOCK_EX, so the re-entry would be a permanent self-deadlock.
func (v *Vault) WriteResume(project, content, expectedSha256 string) error {
	path, err := v.ResumeFile(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure project dir: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock resume: %w", err)
	}
	defer release()

	existing, rerr := os.ReadFile(path)
	switch {
	case rerr == nil:
		sum := sha256.Sum256(existing)
		current := hex.EncodeToString(sum[:])
		if expectedSha256 == "" || current != expectedSha256 {
			return &ResumeConflictError{Current: current, Expected: expectedSha256}
		}
	case errors.Is(rerr, fs.ErrNotExist):
		if expectedSha256 != "" {
			return &ResumeConflictError{Current: "(absent)", Expected: expectedSha256}
		}
	default:
		return fmt.Errorf("pre-read resume: %w", rerr)
	}

	if err := atomicfile.Write(v.Root, path, []byte(content)); err != nil {
		return fmt.Errorf("write resume: %w", err)
	}
	return nil
}

// EditResume serializes a full read→modify→write of the project's resume.md
// against every other vault writer of that path (WriteResume, the CLI, a second
// MCP goroutine). mutate receives the authoritative content read under the held
// lock and returns the replacement; a mutate error aborts the edit without
// writing.
//
// This is the only sanctioned way to surgically edit resume.md: a caller that
// reads the file itself and writes it back opts out of the advisory lock, and an
// advisory lock only excludes the writers that take it — that is exactly the
// lost-update hole the vp_thread_*/vp_carried_* editors used to have.
//
// The write goes through atomicfile.Write directly rather than lockedWrite:
// lockedWrite re-acquires the same per-path lock, and vaultlock.Acquire is a
// blocking LOCK_EX with no LOCK_NB and no timeout, so the re-entry would be a
// permanent self-deadlock rather than an error.
func (v *Vault) EditResume(project string, mutate func(string) (string, error)) error {
	path, err := v.ResumeFile(project)
	if err != nil {
		return err
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock resume: %w", err)
	}
	defer release()

	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure project dir: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("resume.md not found for project %q", project)
		}
		return fmt.Errorf("read resume: %w", err)
	}

	updated, err := mutate(string(data))
	if err != nil {
		return err
	}

	if err := atomicfile.Write(v.Root, path, []byte(updated)); err != nil {
		return fmt.Errorf("write resume: %w", err)
	}
	return nil
}

// AppendIteration appends a narrative entry to the project's iterations file
// with a leading separator. The per-path vaultlock serializes concurrent
// appends (and interlocks with any whole-file rewriter of the same file).
func (v *Vault) AppendIteration(project, content string) error {
	path, err := v.IterationsFile(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure project dir: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("lock iterations file: %w", err)
	}
	defer release()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open iterations file: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(0, 2); err != nil {
		return fmt.Errorf("seek iterations file: %w", err)
	}

	entry := "\n---\n" + content
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("write iteration: %w", err)
	}
	v.stamp(path)
	return nil
}

// ListTriples returns all triples for a project by globbing the triples directory.
func (v *Vault) ListTriples(project string) ([]Triple, error) {
	triplesDir, err := v.KGTriplesDir(project)
	if err != nil {
		return nil, err
	}

	matches, err := filepath.Glob(filepath.Join(triplesDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("glob triples: %w", err)
	}
	if len(matches) == 0 {
		return nil, nil
	}

	var triples []Triple
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read triple %s: %w", m, err)
		}
		var t Triple
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("parse triple %s: %w", m, err)
		}
		triples = append(triples, t)
	}
	return triples, nil
}
