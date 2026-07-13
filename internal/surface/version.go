// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package surface tracks the MCP tool-surface version stamped into a vault on
// every successful schema-bearing vault write. The stamp ("<dir>/.surface") is
// a TOML record that lets a future verifier detect a vault written by a newer
// client and refuse to write incompatible data over it.
//
// surface is a LEAF package: it imports only the standard library and
// BurntSushi/toml. It deliberately does NOT import internal/storage (whose
// path helpers are (*Vault) methods) — doing so would create the import cycle
// storage -> atomicfile -> surface -> storage. ResolveStampDir therefore
// reimplements the vault-layout mapping as pure string logic.
package surface

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// MCPSurfaceVersion is the current MCP tool-surface schema version. It is a
// hand-bumped monotonic integer, separate from the release version. Bump it
// whenever the MCP tool surface changes in a way that affects what gets written
// into the vault, so older binaries gate against vaults written by newer ones.
//
// Baseline 1: fresh for vibe-palace (NOT inherited from vibe-vault's counter).
const MCPSurfaceVersion int = 1

// Stamp models the on-disk .surface TOML file recording the latest writer.
type Stamp struct {
	Surface     int    `toml:"surface"`
	LastWriter  string `toml:"last_writer,omitempty"`
	LastWriteAt string `toml:"last_write_at,omitempty"`
}

// stampFilename is the per-directory record relative to a stamp directory.
const stampFilename = ".surface"

// ReadStamp reads the .surface file from stampDir.
//
// A missing file returns Stamp{Surface: 0}, nil. Malformed TOML returns a
// wrapped error.
func ReadStamp(stampDir string) (Stamp, error) {
	path := filepath.Join(stampDir, stampFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Stamp{Surface: 0}, nil
		}
		return Stamp{}, fmt.Errorf("read .surface: %w", err)
	}
	var s Stamp
	if err := toml.Unmarshal(data, &s); err != nil {
		return Stamp{}, fmt.Errorf("parse .surface: %w", err)
	}
	return s, nil
}

// WriteStamp atomically writes <stampDir>/.surface with a surface-only stamp
// (the emitted file is exactly "surface = N\n").
//
// Byte-stable per version: WriteStamp is a no-op when an existing stamp is
// already at a surface version >= the requested version, so the file is never
// rewritten once it exists at the current version. This keeps the tracked stamp
// byte-identical across writers and vault writes, eliminating merge churn.
//
// The writerFingerprint parameter is retained for signature compatibility with
// callers but is no longer persisted. The provenance fields (LastWriter,
// LastWriteAt) remain on the Stamp struct solely so ReadStamp can decode legacy
// on-disk stamps that still contain them.
//
// The .surface file is written via a direct temp-file + rename rather than
// internal/atomicfile.Write — surface must not import atomicfile (which imports
// surface to stamp on vault writes), so it keeps a private minimal copy here.
func WriteStamp(stampDir string, version int, writerFingerprint string) error {
	existing, err := ReadStamp(stampDir)
	if err != nil {
		return err
	}
	if existing.Surface >= version {
		return nil
	}

	if err := os.MkdirAll(stampDir, 0o755); err != nil {
		return fmt.Errorf("create stamp dir: %w", err)
	}

	s := Stamp{Surface: version}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(s); err != nil {
		return fmt.Errorf("encode .surface: %w", err)
	}

	path := filepath.Join(stampDir, stampFilename)
	if err := writeStampFileAtomic(path, buf.Bytes()); err != nil {
		return fmt.Errorf("write .surface: %w", err)
	}
	return nil
}

// writeStampFileAtomic writes data to path via temp-file + rename in the same
// directory (safe cross-rename), with 0o644 perms.
func writeStampFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".vp-surface-tmp-*")
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
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	removeTemp = false
	return nil
}

// stampedDirs caches stamp directories already written in this process. Because
// MCPSurfaceVersion is a process constant, a directory needs stamping at most
// once per process — this bounds bulk paths (e.g. migrate writing tens of
// thousands of drawers/entities/triples, all mapping to one palace/<p>/.surface)
// to a single write instead of one per append. Side effect: last_write_at
// reflects the first write of the process, not the last (acceptable for
// best-effort provenance metadata).
var stampedDirs sync.Map // map[string]struct{}

// StampForPath resolves the stamp directory for a successful vault write at
// writePath under vaultPath and stamps it with MCPSurfaceVersion. It is the
// single entry point callers (internal/atomicfile and the append-style writers)
// use as a best-effort side effect after a write.
//
// Returns nil (no-op) when the write is outside the vault, under an excluded
// path (.local/.git/.bak/.surface), or already stamped this process. Stamp
// errors are returned for the caller to log; callers must NOT fail the primary
// write on a stamp error.
func StampForPath(vaultPath, writePath string) error {
	stampDir, err := ResolveStampDir(vaultPath, writePath)
	if err != nil {
		return err
	}
	if stampDir == "" {
		return nil
	}
	// Tripwire: a `go test` process writing a vault outside os.TempDir() is an
	// unisolated test polluting the real vault. Fail fast at the write site.
	// Runs before the memoization short-circuit so it fires on every write,
	// and only for in-vault writes (stampDir != "").
	GuardTestVaultWrite(vaultPath, writePath)
	if _, done := stampedDirs.Load(stampDir); done {
		return nil
	}
	if err := WriteStamp(stampDir, MCPSurfaceVersion, WriterFingerprint(vaultPath)); err != nil {
		return err
	}
	stampedDirs.Store(stampDir, struct{}{})
	return nil
}

// unrecognizedTopWarn keys a sync.Once per unrecognized top-level directory so
// the stderr warning fires at most once per process per name.
var (
	unrecognizedTopWarnMu sync.Mutex
	unrecognizedTopWarn   = map[string]*sync.Once{}
)

// ResolveStampDir maps a vault write target (writePath) under vaultPath to the
// directory whose .surface file should be touched.
//
// Returns ("", nil) when:
//   - writePath is outside vaultPath (host-local write)
//   - writePath is under an excluded segment: .local (machine-local),
//     .git, or has a .bak / .surface basename (non-schema / self)
//   - writePath is under vaultPath but the top-level dir is not
//     Projects / palace / Templates (a stderr warning fires once per process
//     per top-level name)
//
// Recognized layouts (vibe-palace; note: NO agentctx/ subdir, unlike vv):
//   - <vault>/Projects/<p>/...   → <vault>/Projects/<p>
//   - <vault>/palace/<p>/...     → <vault>/palace/<p>   (excluding .local/)
//   - <vault>/Templates/...      → <vault>/Templates
func ResolveStampDir(vaultPath, writePath string) (string, error) {
	if vaultPath == "" {
		return "", nil
	}
	absVault, err := filepath.Abs(vaultPath)
	if err != nil {
		return "", fmt.Errorf("abs vault path: %w", err)
	}
	absWrite, err := filepath.Abs(writePath)
	if err != nil {
		return "", fmt.Errorf("abs write path: %w", err)
	}
	rel, err := filepath.Rel(absVault, absWrite)
	if err != nil {
		// Different volumes / unrelated paths — host-local write.
		return "", nil
	}
	if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		// Outside the vault — host-local write, no stamp, no warning.
		return "", nil
	}

	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", nil
	}

	// Excluded segments / basenames — non-schema writes are not stamped.
	base := parts[len(parts)-1]
	if strings.HasSuffix(base, ".bak") || base == stampFilename {
		return "", nil
	}
	for _, seg := range parts {
		if seg == ".local" || seg == ".git" {
			return "", nil
		}
	}

	top := parts[0]
	switch top {
	case "Projects":
		if len(parts) < 2 || parts[1] == "" {
			return "", nil
		}
		return filepath.Join(absVault, "Projects", parts[1]), nil
	case "palace":
		if len(parts) < 2 || parts[1] == "" {
			return "", nil
		}
		return filepath.Join(absVault, "palace", parts[1]), nil
	case "Templates":
		return filepath.Join(absVault, "Templates"), nil
	default:
		// Vault-relative but unrecognized — warn once per top-level name.
		warnUnrecognizedTopOnce(top, writePath)
		return "", nil
	}
}

// warnUnrecognizedTopOnce emits the unrecognized-vault-path warning for the
// given top-level directory at most once per process.
func warnUnrecognizedTopOnce(top, writePath string) {
	unrecognizedTopWarnMu.Lock()
	once, ok := unrecognizedTopWarn[top]
	if !ok {
		once = &sync.Once{}
		unrecognizedTopWarn[top] = once
	}
	unrecognizedTopWarnMu.Unlock()
	once.Do(func() {
		fmt.Fprintf(os.Stderr, "vp: warning — vault write at unrecognized path %q (no .surface stamp)\n", writePath)
	})
}

// WriterFingerprint returns an 8-hex-char prefix of sha256(hostname+vaultPath).
// The raw hostname is never written to the vault.
func WriterFingerprint(vaultPath string) string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	sum := sha256.Sum256([]byte(host + vaultPath))
	return hex.EncodeToString(sum[:])[:8]
}

// IncompatibleError is returned by CheckCompatible when a vault stamp's surface
// version exceeds MCPSurfaceVersion.
type IncompatibleError struct {
	BinarySurface int
	VaultSurface  int
	StampDir      string // worst (highest) stamp's directory
	LastWriter    string // optional, may be empty
}

// Error renders the standard remediation message.
func (e *IncompatibleError) Error() string {
	writer := e.LastWriter
	if writer == "" {
		writer = "unknown"
	}
	return fmt.Sprintf(
		"vp: this binary supports MCP surface v%d; vault target '%s' is at v%d\n"+
			"    last writer: %s (best-effort, not enforced)\n"+
			"    action:    cd ~/code/vibe-palace && git pull && make install\n"+
			"    if you cannot upgrade right now (deploy host, network outage):\n"+
			"       VP_SURFACE_GATE=warn <original-command>   (proceed at risk)",
		e.BinarySurface, e.StampDir, e.VaultSurface, writer,
	)
}

// ErrNoVault is returned by CheckCompatible when vaultPath is empty — no vault
// was configured, or none was carried in the caller's context. It is a distinct
// condition from VaultUnreachableError: "nobody said where the vault is" is a
// normal state for a read on a host that has not run `vp init`, whereas a
// configured-but-absent vault is always wrong. Callers that cannot proceed
// without a vault (every mutating path) must refuse on it; read-only paths may
// ignore it.
var ErrNoVault = errors.New("vp: no vault configured (empty vault path)")

// VaultUnreachableError is returned by CheckCompatible when vaultPath is
// non-empty but cannot be stat'd — the configured vault root is absent,
// deleted out from under a running server, or unreadable. Unlike an
// IncompatibleError this is NOT a "proceed at risk" condition: there is nothing
// to write to, so VP_SURFACE_GATE=warn does not bypass it (see gate.go).
type VaultUnreachableError struct {
	Path string
	Err  error
}

func (e *VaultUnreachableError) Error() string {
	return fmt.Sprintf(
		"vp: configured vault root '%s' is unreachable: %v\n"+
			"    the vault may have been moved, deleted, or unmounted since this process started\n"+
			"    action:    check vault_path in ~/.config/vibe-palace/config.toml, then restart the MCP server",
		e.Path, e.Err,
	)
}

func (e *VaultUnreachableError) Unwrap() error { return e.Err }

// CheckCompatible scans known stamp targets under vaultPath and returns a
// non-nil *IncompatibleError if any stamp's surface > MCPSurfaceVersion (i.e.,
// the vault was written by a newer binary than this one).
//
// A vault it cannot reach is reported, never swallowed: an empty vaultPath
// returns ErrNoVault and an un-stat-able one returns *VaultUnreachableError.
// Both were previously folded into a nil return ("best-effort; gates proceed"),
// which let gateIfMutating admit a mutating tool with no vault in context at
// all, and let a write proceed against a vault root that had been deleted.
// Distinguishing the two lets each caller decide: mutating paths refuse on
// both, read-only paths may tolerate ErrNoVault.
func CheckCompatible(vaultPath string) error {
	if vaultPath == "" {
		return ErrNoVault
	}
	if _, err := os.Stat(vaultPath); err != nil {
		return &VaultUnreachableError{Path: vaultPath, Err: err}
	}

	patterns := []string{
		filepath.Join(vaultPath, "Projects", "*", stampFilename),
		filepath.Join(vaultPath, "palace", "*", stampFilename),
		filepath.Join(vaultPath, "Templates", stampFilename),
	}

	maxSurface := 0
	worstDir := ""
	worstWriter := ""

	for _, pat := range patterns {
		matches, err := filepath.Glob(pat)
		if err != nil {
			continue
		}
		for _, m := range matches {
			stampDir := filepath.Dir(m)
			s, err := ReadStamp(stampDir)
			if err != nil {
				// Malformed stamps are not gating events; skip silently.
				continue
			}
			if s.Surface > maxSurface {
				maxSurface = s.Surface
				worstDir = stampDir
				worstWriter = s.LastWriter
			}
		}
	}

	if maxSurface > MCPSurfaceVersion {
		return &IncompatibleError{
			BinarySurface: MCPSurfaceVersion,
			VaultSurface:  maxSurface,
			StampDir:      worstDir,
			LastWriter:    worstWriter,
		}
	}
	return nil
}
