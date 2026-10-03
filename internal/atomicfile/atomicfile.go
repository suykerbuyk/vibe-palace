// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package atomicfile provides the whole-file write primitives that vault
// content goes through. Each writes via a temp-file + same-directory rename
// and, on success, best-effort stamps the MCP surface version for the vault
// containing the target (see internal/surface). It is the consolidation point
// for vibe-palace's whole-file vault writers so surface stamping is structural
// rather than per-call discipline.
//
// Two shapes, one core:
//
//   - Write takes the bytes. Use it whenever the content is already in memory.
//   - WriteStream takes a writer callback, for content that must not be
//     buffered whole — today a compressed transcript, which is the largest
//     thing the vault holds.
//
// Append-style writers (JSONL appends, O_EXCL single-file creates) cannot use a
// whole-file replace primitive; they call surface.StampForPath directly after a
// successful write instead.
//
// Neither primitive gates on the MCP SURFACE version. That fail-stop lives at
// the dispatch seam and stays there (doc/adr/010-surface-gate-at-the-dispatch-seam.md);
// a write primitive that refused on it would put a fail-stop underneath every
// CLI path and every recovery path at once.
//
// They DO refuse two things, because every vault writer funnels through them
// (internal/departedpath, task lc-u15-vaultfs-refuses-writes-into-a-departed-project):
//
//   - any write under a departed project's trees — the departure record wins
//     over whatever the directory holds;
//   - any write under Audits/departures/, except by the lifecycle commands,
//     which pass ForDepartureRecord with the vault's live root-lock token.
//
// atomicfile imports surface, departedpath and vaultlock (all leaves). None of
// them imports atomicfile, so the dependency graph stays acyclic.
package atomicfile

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// config holds resolved write options.
type config struct {
	perm        os.FileMode
	inheritPerm bool
	fsync       bool
	dirFsync    bool
	// recordHeld admits a write under Audits/departures/: the vault root lock
	// token of a lifecycle command (ForDepartureRecord).
	recordHeld *vaultlock.Held
}

// Option configures a Write call.
type Option func(*config)

// There is no WithPerm option. Its only caller was templates.Executor.Write,
// deleted with the template reset that overwrote a vault override
// (upgrade-overwrite-resets-vault-template-overrides); an option nobody passes
// is what sourceaudit's `uninvoked` rule exists to catch. Every write is 0o644
// unless WithInheritPerm keeps an existing file's mode.

// WithInheritPerm makes Write inherit the existing target file's mode when the
// target exists, falling back to the default 0o644 when it does not.
// Used by writers that must preserve user-set permissions on managed files.
func WithInheritPerm() Option { return func(c *config) { c.inheritPerm = true } }

// WithFsync makes Write fsync the temp file before rename, for callers that
// want durability beyond rename atomicity.
func WithFsync() Option { return func(c *config) { c.fsync = true } }

// WithDirFsync makes Write fsync the target's directory after the rename, so
// the rename itself survives a crash, not only the bytes. Pair it with
// WithFsync for a write that must be durable once Write returns. On Windows a
// directory cannot be opened for sync and NTFS journals the rename, so it is a
// no-op there.
//
// The directory sync runs after the rename, so when it fails Write returns an
// error although the new content is already in place: the caller cannot read
// an error as "the target is unchanged", only as "the write may not be
// durable".
func WithDirFsync() Option { return func(c *config) { c.dirFsync = true } }

// syncObserver, when non-nil, is called with the path of every fsync this
// package completes: the temp file before its rename (WithFsync), and the
// directory after it (WithDirFsync). A TEST SEAM, like writeObserver: a
// durability step leaves no other trace, so without it a caller that drops
// one of the options passes every test.
var (
	syncObserverMu sync.Mutex
	syncObserver   func(path string)
)

// SetSyncObserver installs f as the fsync observer and returns a function that
// restores the previous one. Intended for tests only.
func SetSyncObserver(f func(path string)) (restore func()) {
	syncObserverMu.Lock()
	prev := syncObserver
	syncObserver = f
	syncObserverMu.Unlock()
	return func() {
		syncObserverMu.Lock()
		syncObserver = prev
		syncObserverMu.Unlock()
	}
}

func notifySync(path string) {
	syncObserverMu.Lock()
	f := syncObserver
	syncObserverMu.Unlock()
	if f != nil {
		f(path)
	}
}

// ForDepartureRecord admits a write under Audits/departures/. held must be the
// live root-lock token of the vault written: only a lifecycle command holding
// the whole vault may write a departure record (vaultfs.WriteDepartureRecord).
func ForDepartureRecord(held *vaultlock.Held) Option {
	return func(c *config) { c.recordHeld = held }
}

// writeObserver, when non-nil, is called with the absolute path of every
// content write this package completes.
//
// 🔴 TEST SEAM, fired from writeAtomic — the temp-plus-rename core BOTH Write
// and WriteStream reach — so every content write this package completes is
// observed, whichever entry point produced it. It exists because "how many
// times did this run write THIS FILE" is not answerable anywhere else. A
// counter placed in a command's own executor
// counts writes THROUGH THAT EXECUTOR, which is the wrong question: the
// regression that matters is a second writer appearing BESIDE the executor, and
// such a writer never passes the executor's counter. Every task-file write in
// the tree bottoms out here — sourceaudit's vaultWriteFunnel exists to keep it
// that way — so this is the one place a per-file write count is honest.
//
// Guarded by its own mutex rather than left as a bare var: tests that install it
// may run alongside others in the same binary.
var (
	writeObserverMu sync.Mutex
	writeObserver   func(absPath string)
)

// SetWriteObserver installs f as the write observer and returns a function that
// restores the previous one. Intended for tests only; production code never
// calls it and nothing in this package behaves differently when it is unset.
func SetWriteObserver(f func(absPath string)) (restore func()) {
	writeObserverMu.Lock()
	prev := writeObserver
	writeObserver = f
	writeObserverMu.Unlock()
	return func() {
		writeObserverMu.Lock()
		writeObserver = prev
		writeObserverMu.Unlock()
	}
}

// notifyWrite reports a completed write to the observer, if one is installed.
func notifyWrite(absPath string) {
	writeObserverMu.Lock()
	f := writeObserver
	writeObserverMu.Unlock()
	if f != nil {
		f(absPath)
	}
}

// Write atomically writes data to absPath: it creates parent directories,
// writes a temp file in the same directory, optionally fsyncs, chmods, and
// renames it over absPath (retrying the rename on the transient Windows sharing
// failures classified by retryableRenameErr). On success it best-effort stamps
// the surface version for vaultRoot/absPath; pass vaultRoot == "" to skip
// stamping (non-vault writes). A stamp failure is logged and never fails the
// write.
func Write(vaultRoot, absPath string, data []byte, opts ...Option) error {
	cfg := config{perm: 0o644}
	for _, o := range opts {
		o(&cfg)
	}
	return writeAtomic(vaultRoot, absPath, cfg, func(f *os.File) error {
		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("write temp: %w", err)
		}
		return nil
	})
}

// WriteStream is Write for content that must not be held in memory: it opens
// the temp file, hands it to fill as an io.Writer, and then completes the same
// chmod + rename + stamp that Write does. Pass vaultRoot == "" to skip stamping
// (non-vault destinations).
//
// fill's error is returned unwrapped, so the caller's own vocabulary survives —
// a compression failure reads as a compression failure, not as "write temp".
// Any failure leaves absPath untouched and removes the temp file.
//
// # It stamps, and it takes no options
//
// A streamed file is CONTENT, so this behaves like Write and not like the
// removal sink (vaultfs.RemoveNoLock / RenameNoLock), which deliberately does
// not stamp because it writes no content.
//
// There is deliberately no opts parameter. This primitive has exactly one
// caller (archive.compressFile), and options nobody passes are the shape
// sourceaudit's `uninvoked` rule exists to catch. In particular it does NOT
// fsync: the hand-rolled temp+rename it replaced did not either, and matching
// existing durability rather than silently improving it is the same parity rule
// the append primitive followed. Add an option here when a second caller needs
// one, not before.
func WriteStream(vaultRoot, absPath string, fill func(io.Writer) error) error {
	return writeAtomic(vaultRoot, absPath, config{perm: 0o644}, func(f *os.File) error {
		return fill(f)
	})
}

// writeAtomic is the shared temp-plus-rename core behind Write and WriteStream.
// It owns every step except producing the bytes: parent directories, the temp
// file, the optional fsync, the permission mode, the retrying rename, and the
// surface stamp. A caller that hand-rolls any of those is the bypass this
// package exists to delete.
//
// fill's error is returned UNWRAPPED. Each caller therefore keeps its own error
// vocabulary, and the temp file is removed on every failure path.
func writeAtomic(vaultRoot, absPath string, cfg config, fill func(*os.File) error) error {
	// No write lands under a departed project's trees (departedpath): this is
	// the funnel every storage writer and vaultfs write goes through.
	if err := departedpath.RefuseAbs(vaultRoot, absPath); err != nil {
		return err
	}
	if err := departedpath.RefuseRecordAbs(vaultRoot, absPath); err != nil {
		if h := cfg.recordHeld; h == nil || h.RequireRoot() != nil || !sameDir(h.Root(), vaultRoot) {
			return err
		}
	}
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	perm := cfg.perm
	if cfg.inheritPerm {
		if info, err := os.Stat(absPath); err == nil {
			perm = info.Mode().Perm()
		}
	}

	tmp, err := os.CreateTemp(dir, ".vp-atomic-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			os.Remove(tmpPath)
		}
	}()

	if err := fill(tmp); err != nil {
		tmp.Close()
		return err
	}
	if cfg.fsync {
		if err := tmp.Sync(); err != nil {
			tmp.Close()
			return fmt.Errorf("sync temp: %w", err)
		}
		notifySync(tmpPath)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := renameWithRetry(tmpPath, absPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	removeTemp = false
	if cfg.dirFsync {
		if err := syncDir(dir); err != nil {
			return fmt.Errorf("sync dir: %w", err)
		}
	}

	if vaultRoot != "" {
		if err := surface.StampForPath(vaultRoot, absPath); err != nil {
			slog.Warn("surface stamp failed", "path", absPath, "err", err)
		}
	}
	// 🔴 NOTIFY FROM THE SHARED CORE, NOT FROM EACH ENTRY POINT. The observer
	// first lived in Write alone, while its own comment claimed "every task-file
	// write in the tree bottoms out here". That was true of the corpus and false
	// of the code: WriteStream reaches this same temp-plus-rename core and
	// notified nothing, so routing one task write through it would have silenced
	// the seam and quietly lapsed the one-write-per-file guarantee it exists to
	// hold — with no test failing to say so. Sitting here, the claim is true by
	// construction and stays true for the NEXT entry point somebody adds.
	notifyWrite(absPath)
	return nil
}

// sameDir reports whether a and b name one directory, symlinks resolved.
func sameDir(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// syncDir fsyncs a directory so a rename or create inside it is durable. It is
// a no-op on Windows (see WithDirFsync).
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return err
	}
	notifySync(dir)
	return nil
}
