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
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
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
// The counter file may carry a third field, a marker (genMark):
//
//   - "w" (markDestructive): a destructive Tx (Tx.beginDestructive) sets it
//     with its first bump and its finishing bump (finishLocked) clears it. A
//     lock-free reader meets it as ErrWriteInFlight from ReadGeneration.
//   - "a" (markAppend): an append-only Tx (Tx.beginAppend) sets it before its
//     first write WITHOUT changing gen or epoch, and the finishing bump clears
//     it. While the writer lives, ReadGeneration returns the unchanged counter,
//     so a search keeps its fast path; if the writer died (the marker is set and
//     the commit lock is free) ReadGeneration returns the same gen and epoch
//     with Stale set.
//
// A marker is not part of Gen's identity except through Stale, so Gen stays
// comparable with ==. The next Lock repairs a marker a dead writer left
// (ensureGenFile).
type Gen struct {
	Gen   uint64
	Epoch uint64
	// Stale marks a counter whose append-only writer died before its
	// finishing bump (ReadGeneration). It is part of ==, so no value a reader
	// remembered ever equals it, and, being a value and not an error, a reader
	// that reloads on it remembers it instead of reloading on every read.
	Stale bool
}

// ReadGeneration reads a project's counter without taking any lock. The file
// is only ever replaced by an atomic rename, so a reader never sees a torn
// value. A missing counter reads as the zero Gen, not an error.
//
// A counter whose destructive flag ("w") is set returns the zero Gen and an
// error wrapping ErrWriteInFlight: a destructive Tx is running, or died before
// its finishing bump, so the counter no longer says whether the store changed.
// A reader must not install, persist, delete or remember anything on such a
// counter.
//
// A counter whose append marker ("a") is set means an append-only Tx wrote, or
// is about to write, before its finishing bump. ReadGeneration then probes the
// project's commit lock without waiting (vaultlock.CommitLockFree):
//
//   - held: the writer is alive. The counter is returned exactly as it stood
//     before the Tx, so a reader's remembered value still matches and a live
//     append costs a search nothing;
//   - free, and a re-read still shows the marker: the writer died. The same gen
//     and epoch are returned with Stale set, a value no reader remembers, so
//     every reader reloads once and sees the append, with no writer involved.
//     A reader must not take a witnessed load of a persisted graph on it (a
//     persisted graph may omit the append: Engine.ensureIndex);
//   - free, but the re-read finds the marker gone: the writer finished between
//     the read and the probe, and the fresh counter is returned.
//
// ReadGeneration never writes: a marker is cleared by the writer's finish, or
// repaired by the next Lock if the writer died.
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
	g, mark, err := readGenRaw(path)
	if err != nil {
		return Gen{}, err
	}
	switch mark {
	case markDestructive:
		return Gen{}, fmt.Errorf("%w (%s)", ErrWriteInFlight, path)
	case markAppend:
		return resolveAppendMarker(vault, project, path, g)
	}
	return g, nil
}

// resolveAppendMarker decides, for a counter read with the append marker set,
// whether its writer is alive (see ReadGeneration).
func resolveAppendMarker(vault *storage.Vault, project, path string, g Gen) (Gen, error) {
	free, err := commitLockFree(vault, project)
	if err != nil {
		return Gen{}, err
	}
	if !free {
		return g, nil // a live appender: the pre-Tx counter
	}
	g2, mark2, err := readGenRaw(path)
	if err != nil {
		return Gen{}, err
	}
	switch {
	case mark2 == markDestructive:
		return Gen{}, fmt.Errorf("%w (%s)", ErrWriteInFlight, path)
	case mark2 == markAppend && g2 == g:
		g.Stale = true
		return g, nil
	}
	return g2, nil // finished, or superseded by a newer commit, between the read and the probe
}

// commitLockFree probes the project's commit lock (see ReadGeneration). It is
// the one place a lock-free reader touches the lock, so it opens with the
// ambient-vault guard, ahead of the flock, like every function that takes an
// index lock.
func commitLockFree(vault *storage.Vault, project string) (bool, error) {
	guardAmbientIndex(vault) // FIRST: before the flock, so nothing is held when it fires
	lockPath, err := vault.IndexCommitLockPath(project)
	if err != nil {
		return false, err
	}
	return vaultlock.CommitLockFree(lockPath, probeHeldHook)
}

// ErrWriteInFlight is returned (wrapped) by ReadGeneration while the counter's
// in-flight flag is set. See Gen.
var ErrWriteInFlight = errors.New("indexstore: a destructive write is in flight")

// genMark is the optional third field of the counter file (see Gen).
type genMark byte

const (
	markNone        genMark = 0
	markDestructive genMark = 'w' // a destructive Tx is in flight (inFlightMark)
	markAppend      genMark = 'a' // an append-only Tx is in flight (appendMark)
)

// inFlightMark and appendMark are the third field's text.
const (
	inFlightMark = "w"
	appendMark   = "a"
)

// errMalformedGen marks a counter file that exists but does not parse: a
// truncation, or a hand edit. A reader reports it; a holder of the commit lock
// replaces it (ensureGenFile).
var errMalformedGen = errors.New("indexstore: malformed generation")

// readGenRaw reads the counter and its marker. A missing counter reads as the
// zero Gen.
func readGenRaw(path string) (Gen, genMark, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Gen{}, markNone, nil
	}
	if err != nil {
		return Gen{}, markNone, fmt.Errorf("indexstore: read generation: %w", err)
	}
	fields := strings.Fields(string(data))
	mark := markNone
	switch {
	case len(fields) == 2:
	case len(fields) == 3 && fields[2] == inFlightMark:
		mark = markDestructive
	case len(fields) == 3 && fields[2] == appendMark:
		mark = markAppend
	default:
		return Gen{}, markNone, fmt.Errorf("%w %s: %q", errMalformedGen, path, data)
	}
	g, err1 := strconv.ParseUint(fields[0], 10, 64)
	e, err2 := strconv.ParseUint(fields[1], 10, 64)
	if err1 != nil || err2 != nil || e == 0 {
		return Gen{}, markNone, fmt.Errorf("%w %s: %q", errMalformedGen, path, data)
	}
	return Gen{Gen: g, Epoch: e}, mark, nil
}

func writeGenFile(path string, g Gen, mark genMark) error {
	return writeFile(path, []byte(formatGen(g, mark)))
}

// formatGen is the counter file's content: "gen epoch" in decimal, plus the
// marker while a Tx is in flight (inFlightMark, appendMark).
func formatGen(g Gen, mark genMark) string {
	if mark != markNone {
		return fmt.Sprintf("%d %d %c\n", g.Gen, g.Epoch, rune(mark))
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
// error until then. A marker is reported, not acted on.
//
// A binary older than the append marker reads "a" as malformed, so its Lock
// lands here: it rewrites the counter to gen 0 with a new epoch (a full reload,
// no data loss) and erases the marker. The killed-append guarantee therefore
// holds only once every writer runs a binary that knows the marker.
func loadGenFile(path string) (Gen, genMark, error) {
	g, mark, err := readGenRaw(path)
	if errors.Is(err, errMalformedGen) {
		slog.Warn("index store change counter is malformed; recreating it with a new epoch",
			"path", path, "err", err)
	} else if err != nil || g.Epoch != 0 {
		return g, mark, err
	}
	e, err := newEpoch(0)
	if err != nil {
		return Gen{}, markNone, err
	}
	g = Gen{Gen: 0, Epoch: e}
	return g, markNone, writeGenFile(path, g, markNone)
}

// ensureGenFile is Lock's read of the counter, under the project's commit
// lock. It creates or replaces a counter as loadGenFile does.
//
// It also repairs a set marker. Under the commit lock a marker can only belong
// to a writer that died: a live Tx clears its marker in finishLocked before it
// releases the lock, and the OS releases the lock of a dead process.
//
//   - "w": a destructive write of that writer may be on disk with no bump to
//     announce it, so the repair bumps Gen with a new epoch and clears it: every
//     reader and writer that remembers the counter reloads in full.
//   - "a": an append of that writer may be on disk with no bump. The repair
//     bumps Gen and keeps the epoch (an append extends the store as a prefix):
//     every reader that remembers the counter reloads, and so does the writer's
//     state cache (keyed on the Gen Lock found).
func ensureGenFile(path string) (Gen, error) {
	g, mark, err := loadGenFile(path)
	if err != nil || mark == markNone {
		return g, err
	}
	if mark == markAppend {
		slog.Warn("index store change counter was left append-in-flight by a dead writer; repairing it",
			"path", path)
		return bumpGen(path, false, markNone)
	}
	slog.Warn("index store change counter was left in-flight by a dead writer; repairing it with a new epoch",
		"path", path)
	return bumpGen(path, true, markNone)
}

// bumpGenFile advances the counter after a commit that wrote something: Gen by
// one, and the epoch too when changeEpoch is set. Called under the commit
// lock. It writes the counter without the in-flight flag.
func bumpGenFile(path string, changeEpoch bool) (Gen, error) {
	return bumpGen(path, changeEpoch, markNone)
}

// bumpGen is bumpGenFile with the marker chosen by the caller. It ignores a
// marker it finds: a Tx's own marker must not turn its later bumps into
// repairs.
func bumpGen(path string, changeEpoch bool, mark genMark) (Gen, error) {
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
	return g, writeGenFile(path, g, mark)
}
