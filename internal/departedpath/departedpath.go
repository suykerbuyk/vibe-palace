// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package departedpath is the ONE rule for "has this project departed from this
// vault", and the write refusal built on it.
//
// 🔴 THE RECORD WINS OVER THE DIRECTORY, EVERYWHERE (Chair ruling, U15). While
// Audits/departures/<p>.json exists — of any kind, readable or not — p is
// departed, whatever Projects/<p>/ holds. A tracked file, a stray untracked one
// or a `vp init` re-scaffold does not reopen the slug: the only way back is a
// git revert of the delete (which removes the record in the same commit), or
// a future adopt. departure.Find is built on RecordExists, and every write
// funnel refuses through Refuse: vaultfs (the CLI and MCP raw file tools),
// atomicfile.Write/WriteStream and storage's append writer (every storage
// writer: capture, iterations, memory, tasks, KG, drawers), and the commit
// backstop.
//
// A LEAF package, importing only the standard library: atomicfile and vaultfs
// sit below internal/departure (departure → slug → vaultfs → atomicfile), so
// the rule has to live beneath all of them. The record location is pinned to
// departure.RelPath by departure's TestDepartedpathReadsTheRecordWhereDepartureWritesIt.
//
// Task: lc-u15-vaultfs-refuses-writes-into-a-departed-project.
package departedpath

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrDeparted is the refusal of a write under a departed project's trees.
var ErrDeparted = errors.New("the project has departed from this vault")

// RecordDir is departure.Dir (pinned by a departure test).
const RecordDir = "Audits/departures"

// RecordRel is the vault-relative record path for slug: departure.RelPath.
func RecordRel(slug string) string { return RecordDir + "/" + slug + ".json" }

// RecordExists is THE rule: a record file for slug exists in vaultRoot. A
// record that exists but cannot be inspected counts too (fail closed): it
// exists only because the project left.
func RecordExists(vaultRoot, slug string) bool {
	if vaultRoot == "" || !plainSegment(slug) {
		return false
	}
	_, err := os.Lstat(filepath.Join(vaultRoot, filepath.FromSlash(RecordRel(slug))))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// TreeSlug returns the project whose tree rel (vault-relative) lies in:
// Projects/<p>/... or palace/<p>/..., the tree root itself included. The top
// directory matches case-insensitively, because on a case-insensitive
// filesystem projects/p IS Projects/p (on a case-sensitive one it only makes a
// write to an illegitimate twin refuse). palace/.local and other dot
// directories are not project trees.
func TreeSlug(rel string) (string, bool) {
	p := path.Clean(filepath.ToSlash(rel))
	parts := strings.Split(strings.TrimPrefix(p, "./"), "/")
	if len(parts) < 2 {
		return "", false
	}
	if !strings.EqualFold(parts[0], "Projects") && !strings.EqualFold(parts[0], "palace") {
		return "", false
	}
	if !plainSegment(parts[1]) {
		return "", false
	}
	return parts[1], true
}

func plainSegment(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, `/\`)
}

// DepartedTree reports the departed project whose tree relPath lies in, by
// the literal path AND by the path it resolves to: a symlink inside the vault
// (Projects/alias → Projects/p, or a top-level link into Projects/p) is judged
// by where the write would land.
func DepartedTree(vaultRoot, relPath string) (slug string, departed bool) {
	if vaultRoot == "" {
		return "", false
	}
	if s, ok := TreeSlug(relPath); ok && RecordExists(vaultRoot, s) {
		return s, true
	}
	if rel, ok := resolvedRel(vaultRoot, filepath.Join(vaultRoot, filepath.FromSlash(filepath.ToSlash(relPath)))); ok {
		if s, ok := TreeSlug(rel); ok && RecordExists(vaultRoot, s) {
			return s, true
		}
	}
	return "", false
}

// resolvedRel is abs with its deepest existing ancestor resolved through
// EvalSymlinks and the rest rejoined, relative to the resolved vault root; ok
// is false outside the vault.
func resolvedRel(vaultRoot, abs string) (string, bool) {
	root, err := filepath.EvalSymlinks(vaultRoot)
	if err != nil {
		return "", false
	}
	clean := filepath.Clean(abs)
	tail := ""
	for dir := clean; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			rel, err := filepath.Rel(root, filepath.Join(real, tail))
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return "", false
			}
			return filepath.ToSlash(rel), true
		}
		if filepath.Dir(dir) == dir {
			return "", false
		}
		tail = filepath.Join(filepath.Base(dir), tail)
	}
}

// Refuse returns nil unless relPath (vault-relative) lies, literally or once
// resolved, under a departed project's tree; then the worded refusal, which
// wraps ErrDeparted.
func Refuse(vaultRoot, relPath string) error {
	s, departed := DepartedTree(vaultRoot, relPath)
	if !departed {
		return nil
	}
	return refusal(vaultRoot, relPath, s)
}

// RefuseAbs is Refuse for an absolute path; a path outside vaultRoot is not
// judged.
func RefuseAbs(vaultRoot, absPath string) error {
	rel, ok := RelOfAbs(vaultRoot, absPath)
	if !ok {
		return nil
	}
	return Refuse(vaultRoot, rel)
}

// RelOfAbs is absPath relative to vaultRoot, slash-separated: lexically when
// absPath is inside vaultRoot, and otherwise once both are resolved, so a
// vault reached through a symlinked root is still recognised. ok is false
// when vaultRoot is "" or the path lies outside the vault. It is the one way
// the write primitives' path checks compute the vault-relative path.
func RelOfAbs(vaultRoot, absPath string) (string, bool) {
	if vaultRoot == "" {
		return "", false
	}
	rel, err := filepath.Rel(vaultRoot, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// Lexically outside; a symlinked root may still resolve inside.
		return resolvedRel(vaultRoot, absPath)
	}
	return filepath.ToSlash(rel), true
}

type record struct {
	Kind, To, Date string
	malformed      string
}

func refusal(vaultRoot, relPath, slug string) error {
	var rec record
	var raw struct {
		Kind string `json:"kind"`
		To   string `json:"to"`
		Date string `json:"date"`
	}
	if data, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(RecordRel(slug)))); err != nil {
		rec.malformed = err.Error()
	} else if err := json.Unmarshal(data, &raw); err != nil {
		rec.malformed = err.Error()
	} else {
		rec = record{Kind: raw.Kind, To: raw.To, Date: raw.Date}
	}
	var where, todo string
	switch {
	case rec.malformed != "":
		where = fmt.Sprintf("it has a departure record here that cannot be read (%s)", rec.malformed)
		todo = "Nothing is written under it until " + RecordRel(slug) + " is repaired in a commit"
	case rec.Kind == "moved-to-vault":
		label, cloneURL := rec.To, rec.To
		if label == "" {
			label, cloneURL = "an unrecorded vault", "<its remote URL>"
		}
		where = fmt.Sprintf("it moved to another vault, %s, on %s", label, rec.Date)
		todo = fmt.Sprintf("Write it in that vault: bind the project to it on this host — "+
			"`vp vault clone %s <path> --bind %s`, or `vp config bind %s --vault <your clone of that vault>` — "+
			"then work from the project's checkout again", cloneURL, slug, slug)
	case rec.Kind == "renamed":
		where = fmt.Sprintf("it was renamed to %q on %s", rec.To, rec.Date)
		todo = fmt.Sprintf("Write under Projects/%s/ (or palace/%s/) instead, and set [project].name = %q in the checkout's .vibe-palace.toml", rec.To, rec.To, rec.To)
	case rec.Kind == "deleted":
		where = fmt.Sprintf("it was deleted from this vault on %s", rec.Date)
		todo = "Nothing is written under it; to restore it, revert the delete commit (which removes the record in the same commit)"
	default:
		where = fmt.Sprintf("it departed (kind %q) on %s", rec.Kind, rec.Date)
		todo = "Nothing is written under it here"
	}
	return fmt.Errorf("%w: refusing to write %s: project %q is not in this vault — %s. "+
		"The departure record %s wins over whatever the directory holds. %s",
		ErrDeparted, filepath.ToSlash(relPath), slug, where, RecordRel(slug), todo)
}

// ErrRecordPath is the refusal of an ordinary write, edit, delete or move
// under Audits/departures/. Under the record-wins rule a record there is the
// most powerful file in the vault: forging one locks a live project out, and
// removing one reopens a departed project. Only the lifecycle commands write
// or remove records, through vaultfs.WriteDepartureRecord and
// vaultfs.RemoveDepartureRecord, which require the vault's live root-lock
// token.
var ErrRecordPath = errors.New("departure records are written only by the vault lifecycle commands")

// IsRecordRel reports whether rel (vault-relative) is Audits/departures or lies
// under it, with the directory names matched case-insensitively.
func IsRecordRel(rel string) bool {
	p := path.Clean(filepath.ToSlash(rel))
	parts := strings.Split(strings.TrimPrefix(p, "./"), "/")
	return len(parts) >= 2 && strings.EqualFold(parts[0], "Audits") && strings.EqualFold(parts[1], "departures")
}

// RefuseRecord returns nil unless relPath lies, literally or once resolved,
// under Audits/departures/.
func RefuseRecord(vaultRoot, relPath string) error {
	hit := IsRecordRel(relPath)
	if !hit && vaultRoot != "" {
		if rel, ok := resolvedRel(vaultRoot, filepath.Join(vaultRoot, filepath.FromSlash(filepath.ToSlash(relPath)))); ok {
			hit = IsRecordRel(rel)
		}
	}
	if !hit {
		return nil
	}
	return fmt.Errorf("%w: refusing to change %s. A departure record decides where a project lives, so only `vp vault project delete` "+
		"(and the other lifecycle commands) write or remove one; to restore a departed project, revert its departure commit with git",
		ErrRecordPath, filepath.ToSlash(relPath))
}

// RefuseRecordAbs is RefuseRecord for an absolute path; a path outside
// vaultRoot is not judged.
func RefuseRecordAbs(vaultRoot, absPath string) error {
	if vaultRoot == "" {
		return nil
	}
	rel, err := filepath.Rel(vaultRoot, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		if r, ok := resolvedRel(vaultRoot, absPath); ok {
			return RefuseRecord(vaultRoot, r)
		}
		return nil
	}
	return RefuseRecord(vaultRoot, rel)
}
