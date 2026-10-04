// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package indexstore

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Gen is a project's store change counter, palace/.local/index/.generation/<p>
// (ADR-014 decision 7, "Freshness in a running server").
//
//   - Gen grows by exactly one on every Tx commit that wrote anything.
//   - Epoch changes on every commit that did more than append (a supersede, a
//     discard, a delete, a relabel, a torn-line truncation, a reap, an owner
//     line rewritten). A graph write bumps Gen only.
//
// A running engine reloads a project in full on any change to Gen or Epoch
// (there is no incremental load); the epoch tells the store's own writer cache
// that its remembered state is no longer a prefix of the files. That reload
// rule belongs to search-index-completeness-and-build-serialization; this
// package owns only the counter.
//
// Epoch is never 0 in a counter that exists, so the zero Gen means "no counter
// yet".
type Gen struct {
	Gen   uint64
	Epoch uint64
}

// ReadGeneration reads a project's counter without taking any lock. The file
// is only ever replaced by an atomic rename, so a reader never sees a torn
// value. A missing counter reads as the zero Gen, not an error.
//
// The reader rule: read the counter BEFORE loading the state it guards, and
// remember that value. A write that lands during the load then moves the
// counter past the remembered value, so the next comparison reloads. Read
// after the load, the counter could already include a write the loaded state
// missed, and that write would never be picked up.
func ReadGeneration(vault *storage.Vault, project string) (Gen, error) {
	path, err := vault.IndexGenerationPath(project)
	if err != nil {
		return Gen{}, err
	}
	return readGenFile(path)
}

// errMalformedGen marks a counter file that exists but does not parse: a
// truncation, or a hand edit. A reader reports it; a holder of the commit lock
// replaces it (ensureGenFile).
var errMalformedGen = errors.New("indexstore: malformed generation")

func readGenFile(path string) (Gen, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Gen{}, nil
	}
	if err != nil {
		return Gen{}, fmt.Errorf("indexstore: read generation: %w", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return Gen{}, fmt.Errorf("%w %s: %q", errMalformedGen, path, data)
	}
	g, err1 := strconv.ParseUint(fields[0], 10, 64)
	e, err2 := strconv.ParseUint(fields[1], 10, 64)
	if err1 != nil || err2 != nil || e == 0 {
		return Gen{}, fmt.Errorf("%w %s: %q", errMalformedGen, path, data)
	}
	return Gen{Gen: g, Epoch: e}, nil
}

func writeGenFile(path string, g Gen) error {
	return writeFile(path, []byte(formatGen(g)))
}

// formatGen is the counter file's content: "gen epoch" in decimal.
func formatGen(g Gen) string {
	return fmt.Sprintf("%d %d\n", g.Gen, g.Epoch)
}

// newEpoch returns a random non-zero epoch different from old. Random, so a
// counter deleted by hand and recreated can never match the epoch an engine
// remembers.
func newEpoch(old uint64) (uint64, error) {
	var b [8]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, fmt.Errorf("indexstore: random epoch: %w", err)
		}
		if e := binary.LittleEndian.Uint64(b[:]); e != 0 && e != old {
			return e, nil
		}
	}
}

// ensureGenFile creates a missing counter with Gen 0 and a random epoch, and
// returns the counter as it stands. Called under the project's commit lock.
//
// A malformed counter is replaced the same way, with a warning. Leaving it
// would make every Lock on the project fail for good. Replacing it is safe:
// the new epoch matches nothing an engine remembers, so every engine does a
// full reload. Lock-free readers (ReadGeneration) keep reporting the malformed
// file as an error until then.
func ensureGenFile(path string) (Gen, error) {
	g, err := readGenFile(path)
	if errors.Is(err, errMalformedGen) {
		slog.Warn("index store change counter is malformed; recreating it with a new epoch",
			"path", path, "err", err)
	} else if err != nil || g.Epoch != 0 {
		return g, err
	}
	e, err := newEpoch(0)
	if err != nil {
		return Gen{}, err
	}
	g = Gen{Gen: 0, Epoch: e}
	return g, writeGenFile(path, g)
}

// bumpGenFile advances the counter after a commit that wrote something: Gen by
// one, and the epoch too when changeEpoch is set. Called under the commit lock.
func bumpGenFile(path string, changeEpoch bool) (Gen, error) {
	g, err := ensureGenFile(path)
	if err != nil {
		return Gen{}, err
	}
	g.Gen++
	if changeEpoch {
		if g.Epoch, err = newEpoch(g.Epoch); err != nil {
			return Gen{}, err
		}
	}
	return g, writeGenFile(path, g)
}
