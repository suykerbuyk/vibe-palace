// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package agentfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ResultKind classifies what Wire did to the target file.
type ResultKind int

const (
	// Unchanged means the block was already present with the current content hash.
	Unchanged ResultKind = iota
	// Added means no block existed before; one was appended.
	Added
	// Updated means a block existed but had different content; it was replaced in place.
	Updated
)

// Result is what Wire reports back to the caller. PrevSha is the sha from
// the previous opening delimiter when Kind is Updated, so the caller can
// surface "updated (was abc1234)" in status rows if useful.
type Result struct {
	Kind    ResultKind
	PrevSha string
}

// ScanBlock reads path and reports whether a managed block is present and,
// if so, what sha the opening delimiter carries. Returns (sha, true, nil) on
// a present block, ("", false, nil) when no block exists, and ("", false,
// err) only on IO errors other than NotExist.
func ScanBlock(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	existing := blockRegexp.Find(data)
	if existing == nil {
		return "", false, nil
	}
	if m := shaRegexp.FindSubmatch(existing); m != nil {
		return string(m[1]), true, nil
	}
	return "", true, nil
}

// blockRegexp matches the full managed block, including both delimiters.
// Non-greedy `.*?` under (?s) keeps us from swallowing a second block if one
// is somehow present.
var blockRegexp = regexp.MustCompile(`(?s)<!-- vibe-palace:begin[^>]*-->.*?<!-- vibe-palace:end -->`)

// shaRegexp extracts the content-hash token from an opening delimiter.
var shaRegexp = regexp.MustCompile(`sha=([0-9a-f]+)`)

// Wire reads target.Path, appends or refreshes the vibe-palace managed block
// idempotently, and writes atomically (tmp + fsync + rename) so a crash
// mid-write cannot corrupt the host file. Content outside the delimiters is
// preserved byte-for-byte. CRLF line endings are preserved when the existing
// file uses them.
func Wire(target Target) (Result, error) {
	data, err := os.ReadFile(target.Path)
	if err != nil {
		return Result{}, fmt.Errorf("read %s: %w", target.Path, err)
	}

	useCRLF := bytes.Contains(data, []byte("\r\n"))
	eol := "\n"
	if useCRLF {
		eol = "\r\n"
	}

	newBlock := managedBlock()
	if useCRLF {
		newBlock = strings.ReplaceAll(newBlock, "\n", "\r\n")
	}

	existing := blockRegexp.Find(data)
	if existing != nil {
		var prevSha string
		if m := shaRegexp.FindSubmatch(existing); m != nil {
			prevSha = string(m[1])
		}
		if bytes.Equal(existing, []byte(newBlock)) {
			return Result{Kind: Unchanged, PrevSha: prevSha}, nil
		}
		idx := bytes.Index(data, existing)
		out := make([]byte, 0, len(data)-len(existing)+len(newBlock))
		out = append(out, data[:idx]...)
		out = append(out, []byte(newBlock)...)
		out = append(out, data[idx+len(existing):]...)
		if err := atomicWrite(target.Path, out); err != nil {
			return Result{}, err
		}
		return Result{Kind: Updated, PrevSha: prevSha}, nil
	}

	// No existing block: append. Ensure the host file ends in EOL, add a
	// blank separator line (only if the file had prior content), then the
	// block, then a trailing EOL so the file still ends cleanly.
	out := make([]byte, 0, len(data)+len(newBlock)+2*len(eol))
	out = append(out, data...)
	if len(data) > 0 && !bytes.HasSuffix(out, []byte(eol)) {
		out = append(out, []byte(eol)...)
	}
	if len(data) > 0 {
		out = append(out, []byte(eol)...)
	}
	out = append(out, []byte(newBlock)...)
	out = append(out, []byte(eol)...)

	if err := atomicWrite(target.Path, out); err != nil {
		return Result{}, err
	}
	return Result{Kind: Added}, nil
}

// atomicWrite writes data to a sibling tempfile in the same directory, fsyncs
// it, then renames over path. Same-directory rename is atomic on POSIX. The
// tmp file inherits the target's mode so wiring does not change permissions.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".vp-agent-*")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("write temp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("fsync temp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp %s: %w", tmp, err)
	}
	if info, err := os.Stat(path); err == nil {
		_ = os.Chmod(tmp, info.Mode())
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
