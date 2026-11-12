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
//
// The counter file may carry a third field, the in-flight flag (inFlightMark):
// a destructive Tx (Tx.beginDestructive) sets it with its first bump and its
// finishing bump (finishLocked) clears it. The flag is not part of Gen, so
// Gen stays comparable with ==; a lock-free reader meets it as
// ErrWriteInFlight from ReadGeneration, and the next Lock repairs a flag a
// dead writer left (ensureGenFile).
type Gen struct {
	Gen   uint64
	Epoch uint64
}

// ReadGeneration reads a project's counter without taking any lock. The file
// is only ever replaced by an atomic rename, so a reader never sees a torn
// value. A missing counter reads as the zero Gen, not an error.
//
// A counter whose in-flight flag is set returns the zero Gen and an error
// wrapping ErrWriteInFlight: a destructive Tx is running, or died before its
// finishing bump, so the counter no longer says whether the store changed. A
// reader must not install, persist, delete or remember anything on such a
// counter. ReadGeneration never writes: the flag is cleared by the writer's
// finish, or by the next Lock if the writer died.
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

// ErrWriteInFlight is returned (wrapped) by ReadGeneration while the counter's
// in-flight flag is set. See Gen.
var ErrWriteInFlight = errors.New("indexstore: a destructive write is in flight")

// inFlightMark is the optional third field of the counter file.
const inFlightMark = "w"

// errMalformedGen marks a counter file that exists but does not parse: a
// truncation, or a hand edit. A reader reports it; a holder of the commit lock
// replaces it (ensureGenFile).
var errMalformedGen = errors.New("indexstore: malformed generation")

// readGenFile reads the counter for a lock-free reader: a set in-flight flag
// is ErrWriteInFlight, and the zero Gen is returned with it.
func readGenFile(path string) (Gen, error) {
	g, inFlight, err := readGenRaw(path)
	if err != nil {
		return Gen{}, err
	}
	if inFlight {
		return Gen{}, fmt.Errorf("%w (%s)", ErrWriteInFlight, path)
	}
	return g, nil
}

// readGenRaw reads the counter and its in-flight flag. A missing counter reads
// as the zero Gen.
func readGenRaw(path string) (Gen, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Gen{}, false, nil
	}
	if err != nil {
		return Gen{}, false, fmt.Errorf("indexstore: read generation: %w", err)
	}
	fields := strings.Fields(string(data))
	inFlight := false
	switch {
	case len(fields) == 2:
	case len(fields) == 3 && fields[2] == inFlightMark:
		inFlight = true
	default:
		return Gen{}, false, fmt.Errorf("%w %s: %q", errMalformedGen, path, data)
	}
	g, err1 := strconv.ParseUint(fields[0], 10, 64)
	e, err2 := strconv.ParseUint(fields[1], 10, 64)
	if err1 != nil || err2 != nil || e == 0 {
		return Gen{}, false, fmt.Errorf("%w %s: %q", errMalformedGen, path, data)
	}
	return Gen{Gen: g, Epoch: e}, inFlight, nil
}

func writeGenFile(path string, g Gen, inFlight bool) error {
	return writeFile(path, []byte(formatGen(g, inFlight)))
}

// formatGen is the counter file's content: "gen epoch" in decimal, plus
// inFlightMark while a destructive Tx is in flight.
func formatGen(g Gen, inFlight bool) string {
	if inFlight {
		return fmt.Sprintf("%d %d %s\n", g.Gen, g.Epoch, inFlightMark)
	}
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

// loadGenFile reads the counter under the commit lock for a writer, creating
// a missing one with Gen 0 and a random epoch, and replacing a malformed one
// the same way, with a warning. Leaving a malformed counter would make every
// Lock on the project fail for good. Replacing it is safe: the new epoch
// matches nothing an engine remembers, so every engine does a full reload.
// Lock-free readers (ReadGeneration) keep reporting the malformed file as an
// error until then. The in-flight flag is reported, not acted on.
func loadGenFile(path string) (Gen, bool, error) {
	g, inFlight, err := readGenRaw(path)
	if errors.Is(err, errMalformedGen) {
		slog.Warn("index store change counter is malformed; recreating it with a new epoch",
			"path", path, "err", err)
	} else if err != nil || g.Epoch != 0 {
		return g, inFlight, err
	}
	e, err := newEpoch(0)
	if err != nil {
		return Gen{}, false, err
	}
	g = Gen{Gen: 0, Epoch: e}
	return g, false, writeGenFile(path, g, false)
}

// ensureGenFile is Lock's read of the counter, under the project's commit
// lock. It creates or replaces a counter as loadGenFile does.
//
// It also repairs a set in-flight flag. Under the commit lock the flag can
// only belong to a writer that died: a live destructive Tx clears it in
// finishLocked before it releases the lock, and the OS releases the lock of a
// dead process. A destructive write of that writer may be on disk with no
// bump to announce it, so the repair bumps Gen with a new epoch and clears the
// flag: every reader and writer that remembers the counter reloads.
func ensureGenFile(path string) (Gen, error) {
	g, inFlight, err := loadGenFile(path)
	if err != nil || !inFlight {
		return g, err
	}
	slog.Warn("index store change counter was left in-flight by a dead writer; repairing it with a new epoch",
		"path", path)
	return bumpGen(path, true, false)
}

// bumpGenFile advances the counter after a commit that wrote something: Gen by
// one, and the epoch too when changeEpoch is set. Called under the commit
// lock. It writes the counter without the in-flight flag.
func bumpGenFile(path string, changeEpoch bool) (Gen, error) {
	return bumpGen(path, changeEpoch, false)
}

// bumpGen is bumpGenFile with the in-flight flag chosen by the caller. It
// ignores a flag it finds: a Tx's own flag must not turn its later bumps into
// repairs.
func bumpGen(path string, changeEpoch, inFlight bool) (Gen, error) {
	g, _, err := loadGenFile(path)
	if err != nil {
		return Gen{}, err
	}
	g.Gen++
	if changeEpoch {
		if g.Epoch, err = newEpoch(g.Epoch); err != nil {
			return Gen{}, err
		}
	}
	return g, writeGenFile(path, g, inFlight)
}
