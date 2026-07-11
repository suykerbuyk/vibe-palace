// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// WriteResume writes content to the project's resume file, creating directories
// as needed. This writes to the tier-1 (project override) path so the resolver
// picks it up immediately.
func (v *Vault) WriteResume(project, content string) error {
	path, err := v.ResumeFile(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure project dir: %w", err)
	}
	return v.lockedWrite(path, []byte(content))
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

// WriteWorkflow writes content to the project's workflow file, creating
// parent directories as needed. Overwrites any existing file — callers
// wanting append-merge semantics should read, merge, then call this.
func (v *Vault) WriteWorkflow(project, content string) error {
	path, err := v.WorkflowFile(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure project dir: %w", err)
	}
	return v.lockedWrite(path, []byte(content))
}

// WriteKnowledge writes content to the project's knowledge file.
// Same semantics as WriteWorkflow.
func (v *Vault) WriteKnowledge(project, content string) error {
	path, err := v.KnowledgeFile(project)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure project dir: %w", err)
	}
	return v.lockedWrite(path, []byte(content))
}

// WriteDoc writes content to a project-scoped doc file. rel is resolved
// by DocFile (e.g. "architecture.md"). Creates parent directories.
func (v *Vault) WriteDoc(project, rel, content string) error {
	path, err := v.DocFile(project, rel)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure doc dir: %w", err)
	}
	return v.lockedWrite(path, []byte(content))
}

// WriteAbsorbed writes content to a file under the project's absorbed/
// scratch directory (used by `vp absorb` for resume-suggestions handoff).
func (v *Vault) WriteAbsorbed(project, rel, content string) error {
	path, err := v.AbsorbedFile(project, rel)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure absorbed dir: %w", err)
	}
	return v.lockedWrite(path, []byte(content))
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
