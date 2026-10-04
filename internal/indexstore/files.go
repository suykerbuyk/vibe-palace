// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The files of one project's host-local index, under index/<p>/ (ADR-014
// decision 2).
const (
	chunksFile = "chunks.jsonl"
	ledgerFile = "ledger.jsonl"
	kgDir      = "kg"
	kgFile     = "records.jsonl"
	graphFile  = "hnsw.idx"
)

// projectFiles are the paths of one project's index files.
type projectFiles struct {
	dir, chunks, ledger, kg, graph, fingerprint string
}

func filesFor(vault *storage.Vault, project string) (projectFiles, error) {
	dir, err := vault.IndexDir(project)
	if err != nil {
		return projectFiles{}, err
	}
	return projectFiles{
		dir:    dir,
		chunks: filepath.Join(dir, chunksFile),
		ledger: filepath.Join(dir, ledgerFile),
		kg:     filepath.Join(dir, kgDir, kgFile),
		graph:  filepath.Join(dir, graphFile),

		fingerprint: filepath.Join(dir, fingerprintFile),
	}, nil
}

// readFileFn is the one read of an index file, a seam so a test can count
// reads (an append must not rescan the store).
var readFileFn = os.ReadFile

// readLines reads a JSONL file and returns every complete line, the byte
// offset just past the last complete line, and whether the file ends in a torn
// (unterminated) line. A missing file is empty.
//
// A torn final line is skipped: it is a write a crash cut short, never a
// committed record, because every commit step's ledger entry comes last. An
// incremental reader resumes from the returned offset, so it never skips past
// a line that is still being written.
func readLines(path string) (lines [][]byte, complete int64, torn bool, err error) {
	data, err := readFileFn(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("indexstore: read %s: %w", path, err)
	}
	end := bytes.LastIndexByte(data, '\n') + 1
	torn = end < len(data)
	if torn {
		// Debug, not Warn: a lock-free reader meets a torn tail every time it
		// reads while an append is in flight, which during an ingest is often.
		// The cut, made under the commit lock, warns (truncateTornTail).
		slog.Debug("index store: skipping a torn final line", "path", path, "bytes", len(data)-end)
	}
	for _, l := range bytes.Split(data[:end], []byte{'\n'}) {
		if len(bytes.TrimSpace(l)) > 0 {
			lines = append(lines, l)
		}
	}
	return lines, int64(end), torn, nil
}

// decodeLines decodes each line into a fresh T, skipping (with a warning) a
// malformed interior line.
func decodeLines[T any](path string, lines [][]byte, valid func(*T) bool) []T {
	out := make([]T, 0, len(lines))
	for _, l := range lines {
		var v T
		if err := json.Unmarshal(l, &v); err != nil || (valid != nil && !valid(&v)) {
			slog.Warn("index store: skipping a malformed line", "path", path, "err", err)
			continue
		}
		out = append(out, v)
	}
	return out
}

// encodeLines encodes each value as one JSON line.
func encodeLines[T any](vals []T) ([]byte, error) {
	var buf bytes.Buffer
	for _, v := range vals {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// truncateTornTail cuts a torn final line off path before an append, so the
// append never glues itself onto half a record. It reports whether it cut
// anything; a cut is more than an append, so the caller changes the epoch.
// Called under the commit lock.
func truncateTornTail(path string) (bool, error) {
	data, err := readFileFn(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("indexstore: read %s: %w", path, err)
	}
	end := bytes.LastIndexByte(data, '\n') + 1
	if end == len(data) {
		return false, nil
	}
	slog.Warn("index store: cutting a torn final line before appending", "path", path, "bytes", len(data)-end)
	return true, writeFile(path, data[:end])
}

// tailIsTorn reports whether path ends in an unterminated line, reading only
// its last byte, so an append does not reread the file.
func tailIsTorn(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("indexstore: open %s: %w", path, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return false, err
	}
	if st.Size() == 0 {
		return false, nil
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], st.Size()-1); err != nil {
		return false, err
	}
	return b[0] != '\n', nil
}

// fileEmpty reports whether path is missing or holds no bytes.
func fileEmpty(path string) (bool, error) {
	st, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("indexstore: stat %s: %w", path, err)
	}
	return st.Size() == 0, nil
}
