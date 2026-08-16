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
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/scopetoken"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
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
// TOCTOU), and the write goes through atomicfile.Write directly rather than
// lockedWrite: lockedWrite re-acquires the same lock, and vaultlock.Acquire is a
// blocking LOCK_EX, so the re-entry would be a permanent self-deadlock.
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
		// A matching digest is exactly the case the bake survives: the readers
		// serve EXPANDED bodies alongside a digest over the RAW bytes, so this
		// check runs after the compare-and-set passes, not instead of it.
		relPath, rerr := filepath.Rel(v.Root, path)
		if rerr != nil {
			relPath = path
		}
		if err := scopetoken.CheckWriteBack(relPath, string(existing), content); err != nil {
			return err
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

// AppendIterationOwned mints the iteration number server-side and appends the
// narrative under a SINGLE hold of the per-path vaultlock, so "read the current
// max" and "append max+1" are one critical section. Two concurrent callers
// therefore serialize on the lock and can never mint a duplicate number — the
// check-then-act race that a caller-supplied number (derived from an unlocked
// read of iterations.md) is prone to, and the counter corruption iteration 191
// closed in capture.
//
// It writes the canonical "## Iteration N — title" header itself; the caller
// supplies only title and body. When override is non-nil its value is honored
// verbatim (e.g. to backfill a missing iteration). It returns both the number
// actually written (n) and the number it would have minted from the file
// (derived), so a caller can report — loudly — when an override disagrees with
// the live vault instead of silently correcting a stale caller.
func (v *Vault) AppendIterationOwned(project, title, body string, override *int) (n int, derived int, err error) {
	path, err := v.IterationsFile(project)
	if err != nil {
		return 0, 0, err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return 0, 0, fmt.Errorf("ensure project dir: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return 0, 0, fmt.Errorf("lock iterations file: %w", err)
	}
	defer release()

	// Everything below runs UNDER THE ONE LOCK: the read of the current max and
	// the append are a single critical section, so a concurrent
	// AppendIterationOwned cannot slip its own append in between and force a
	// duplicate number. Keep the append inline here rather than delegating to a
	// helper that re-acquires this lock — vaultlock.Acquire is a blocking LOCK_EX
	// with no timeout, so re-entry would hang, not error.
	derived, err = wrapstate.NextIterFromIterationsMD(path)
	if err != nil {
		return 0, 0, fmt.Errorf("derive iteration number: %w", err)
	}
	n = derived
	if override != nil {
		n = *override
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return 0, 0, fmt.Errorf("open iterations file: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(0, 2); err != nil {
		return 0, 0, fmt.Errorf("seek iterations file: %w", err)
	}
	header := wrapstate.FormatIterationHeader(n, title)
	entry := "\n---\n" + header + "\n\n" + strings.TrimSpace(body) + "\n"
	if _, err := f.WriteString(entry); err != nil {
		return 0, 0, fmt.Errorf("write iteration: %w", err)
	}
	v.stamp(path)
	return n, derived, nil
}

// ListTriples returns all triples for a project by globbing the triples directory.
func (v *Vault) ListTriples(project string) ([]Triple, error) {
	if err := v.checkFormatGate(); err != nil {
		return nil, err
	}
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
