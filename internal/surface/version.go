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
// Bumped 1->2 once the vault-portability epic's write-path changes landed and
// every host ran the surface-v1 binary: the KG triple-filename migration and
// the armed data-format axis changed what gets written into the vault, so an
// older binary must gate against a vault a v2 host has written.
//
// Bumped 2->3 (2026-09-05). Five distinct on-disk shapes moved while the
// counter stood still. Each is something a v2 binary either never writes or
// cannot read, so a v2 host must gate against a vault a v3 host has touched:
//
//   - SESSION-NOTE FRONTMATTER GAINED KEYS. `host` and `host_source` (1963057),
//     then `entrypoint` (9ad131b). SessionMeta is yaml.Marshal'd straight to
//     disk, so these are new lines in every note a v3 binary writes, and
//     c894fc0 then changed the VALUE vocabulary so one client writes different
//     bytes than it did a week earlier.
//   - A NEW ARTIFACT WITH NO v2 READER: palace/<project>/ingested-archives.jsonl
//     (c46f6be). A v2 binary neither emits it nor knows to skip what it records.
//   - CreateTask EMITS AN UNCONDITIONAL `## Context` (b22fd8a), so every task
//     file written from there forward carries a section a v2 binary never
//     produced; d55c96b then rewrote the existing ones to match.
//   - Audits/baseline.json GAINED `measured` (b621a18). This one is the sharpest,
//     because it is silent in the WRITE direction: a v2 binary parses the file
//     and ignores the key, then drops the whole block on its next
//     `vp audit vault --accept` with no error.
//   - EVERY DATE-STAMPED VAULT FILENAME MOVED FROM UTC TO THE WRITER'S LOCAL
//     ZONE (182be37) — session notes, transcript stems, manifests, audit
//     reports. Two binaries in different zones disagree about a file's NAME,
//     which no content check can reconcile after the fact.
//
// 🔴 RE-DERIVE THIS LIST, DO NOT EXTEND IT BY MEMORY. The commits above were
// each confirmed against their diffs, and three plausible-sounding candidates
// were REJECTED on inspection: e4f0f16 (archive-manifest serialisation) is
// locking only and changes no bytes, 0bfbf3a touches internal/storage/drawers.go
// but changes no drawer write shape, and ba79454's internal/storage/tasks.go
// half is a detector, not a writer. A commit that touches a writer is not a
// commit that changes what the writer writes. The queries that find the real
// ones:
//
//	git log -S'yaml:"' -- internal/storage/sessions.go
//	git log -S'json:"' -- internal/vaultaudit/baseline.go internal/archive/manifest.go
//
// and the audit dimension set is the `dims` literal in internal/vaultaudit/audit.go
// — never a count written down here, which is the claim 9b19134 deleted from two
// documents for rotting.
//
// 🔴 A BUMP STRANDS EVERY HOST THAT HAS NOT RUN `make install`, vault-wide and
// at once: CheckCompatible takes the MAX across every stamp, so the first v3
// write anywhere raises the floor for everybody. That is the intended effect,
// and it is why the remediation text in IncompatibleError.Error() below is a
// TESTED contract rather than a convenience — a stranded host has to be able to
// read its way out. `vp check --check writer-identity` derives how many hosts
// that is; do not record the number here.
const MCPSurfaceVersion int = 3

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
//     Projects / palace / Templates / Audits (a stderr warning fires once per
//     process per top-level name)
//
// Recognized layouts (vibe-palace; note: NO agentctx/ subdir, unlike vv):
//   - <vault>/Projects/<p>/...   → <vault>/Projects/<p>
//   - <vault>/palace/<p>/...     → <vault>/palace/<p>   (excluding .local/)
//   - <vault>/Templates/...      → <vault>/Templates
//   - <vault>/Audits/...         → <vault>/Audits       (vault-global audit output)
//
// Any root added here MUST also be added to CheckCompatible's glob list, or the
// stamp is written and never read.
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
	case "Audits":
		// Vault-global audit output. Stamped like Templates (one stamp for the
		// whole root, not one per subdir) because the reports are not
		// project-scoped -- the thing being audited is the vault.
		return filepath.Join(absVault, "Audits"), nil
	default:
		// Vault-relative but unrecognized — warn once per top-level name.
		warnUnrecognizedTopOnce(top, writePath)
		return "", nil
	}
}

// StampPath returns the vault-relative path of the .surface stamp a write to
// writePath will touch, or "" when that write is stamped nowhere.
//
// It exists so a caller that has to NAME the files a write dirties — an operator
// instruction to commit them, say — does not have to join ResolveStampDir with a
// literal ".surface" and thereby own a second copy of the stamp filename.
func StampPath(vaultPath, writePath string) (string, error) {
	dir, err := ResolveStampDir(vaultPath, writePath)
	if err != nil || dir == "" {
		return "", err
	}
	absVault, err := filepath.Abs(vaultPath)
	if err != nil {
		return "", fmt.Errorf("abs vault path: %w", err)
	}
	rel, err := filepath.Rel(absVault, filepath.Join(dir, stampFilename))
	if err != nil {
		return "", fmt.Errorf("relativize stamp path: %w", err)
	}
	return filepath.ToSlash(rel), nil
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

// Remediation returns the way out, one line per element, and it is THE ONLY
// PLACE THIS PROSE EXISTS.
//
// # Why a method and not a constant
//
// A stranded host's only route back is TEXT. It cannot pull its way out — pulling
// raises the vault floor further, and the fix is a new binary, not new vault
// content — and the MCP server it is talking to is the thing that is out of date.
// So every consumer of an IncompatibleError owes the operator these lines, and the
// consumers that re-rendered them from the struct fields instead were shipping
// messages that are accurate, well-formed, and useless.
//
// The prose used to live in two independently maintained copies (here and a
// hand-written literal in internal/check/surface.go) and THEY HAD ALREADY
// DRIFTED: `action:` became `Upgrade:`, `<original-command>` became `<command>`,
// and the framing line was dropped entirely. Nothing pinned the copy, so nothing
// noticed. One source of prose makes that divergence structurally impossible —
// which is the half of the problem a method can actually solve. The other half
// (a consumer that simply declines to call it) is not expressible in Go and is
// enforced by internal/sourceaudit's surface-remediation rule instead.
//
// # The indentation contract
//
// Lines carry their RELATIVE indentation and no leading margin. Error() adds a
// uniform four-space margin to each; a list consumer (check.Result.Details,
// vp_surface_check's details[], the bootstrap alert) takes them as-is. That is
// what lets Error()'s rendered bytes stay unchanged while the same strings serve
// a JSON array.
//
// Three tests hold this down, and it is worth naming which does what, because
// this comment previously credited TestStrandedHostCanReadItsWayOut with pinning
// the rendered bytes and that test asserts SUBSTRINGS. Under substrings alone the
// last-writer margin could go from four spaces to eight with the whole suite
// green:
//
//   - TestErrorRendersExactBytes pins the rendered BYTES, both LastWriter states.
//   - TestRemediationContentIsPinned pins the CONTENT of these lines, stated
//     independently — every other assertion in the repo derives its expectation
//     from this function and so would not notice a deleted line.
//   - TestRemediationIsErrorsOnlyProseSource pins the RELATIONSHIP: Error()'s tail
//     is exactly these lines under the margin, and nothing else.
func (e *IncompatibleError) Remediation() []string {
	return []string{
		"action:    cd ~/code/vibe-palace && git pull && make install",
		"if you cannot upgrade right now (deploy host, network outage):",
		"   VP_SURFACE_GATE=warn <original-command>   (proceed at risk)",
	}
}

// Error renders the standard remediation message.
//
// 🔴 THE RENDERED BYTES ARE A PINNED CONTRACT. Remediation() was extracted from
// this function ADDITIVELY: the header line, the last-writer line and the
// four-space margin below reproduce what this rendered before the extraction,
// byte for byte, and TestErrorRendersExactBytes pins exactly that — including
// this margin, which no substring assertion can see. Change the tail by changing
// Remediation(), never by re-inlining it here.
func (e *IncompatibleError) Error() string {
	writer := e.LastWriter
	if writer == "" {
		writer = "unknown"
	}
	var tail strings.Builder
	for _, line := range e.Remediation() {
		tail.WriteString("\n    ")
		tail.WriteString(line)
	}
	return fmt.Sprintf(
		"vp: this binary supports MCP surface v%d; vault target '%s' is at v%d\n"+
			"    last writer: %s (best-effort, not enforced)",
		e.BinarySurface, e.StampDir, e.VaultSurface, writer,
	) + tail.String()
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

	// Every root ResolveStampDir can return MUST appear here. A stamp this scan
	// does not read is a stamp that gates nothing: the vault's version floor
	// would ignore it, and an older binary would be told `pass` for a vault only
	// a newer one can safely write.
	patterns := []string{
		filepath.Join(vaultPath, "Projects", "*", stampFilename),
		filepath.Join(vaultPath, "palace", "*", stampFilename),
		filepath.Join(vaultPath, "Templates", stampFilename),
		filepath.Join(vaultPath, "Audits", stampFilename),
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
