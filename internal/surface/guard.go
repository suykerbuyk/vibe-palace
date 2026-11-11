// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowNonTempVaultEnv, when set to a non-empty value, disables the
// under-test vault-write tripwire. Reserved for the rare case where a test
// genuinely needs to write a vault outside the OS temp dir.
const allowNonTempVaultEnv = "VP_ALLOW_NONTEMP_VAULT"

// GuardTestVaultWrite panics when a `go test` process is about to write a
// vault it must not write. It is seated at the first statement of both vault
// write funnels — atomicfile.writeAtomic and storage.appendUnderLock — so it
// runs before any refusal those funnels make and before the content write, and
// it is a no-op in a production binary.
//
// 🔴 IT IS NOT "BEFORE ANY FILESYSTEM EFFECT" ON THE APPEND PATH, AND THIS
// COMMENT WILL NOT CLAIM OTHERWISE. On the whole-file funnel the seat does
// precede every effect: writeAtomic's MkdirAll and temp file both come after
// it. On the append path a CALLER may already have touched the vault before
// appendUnderLock is entered — AppendIterationOwned calls EnsureVaultDir and
// then vaultlock.Acquire, and Acquire creates <vault>/.vp-locks/ and a lock
// sidecar which vaultlock never unlinks. So a test appending to the operator's
// configured vault leaves those behind and only then panics. Closing that is
// the filed follow-on task
// `append-entry-paths-touch-the-vault-before-the-tripwire-fires`; it is NOT
// this seat's job and is deliberately not attempted here.
//
// Two arms, refusing for two different reasons:
//
//   - ARM A, HARM: the write is FOR the operator's configured vault root (the
//     init-time snapshot in ambient.go). Nothing under test legitimately writes
//     that, so this arm is NOT waivable by VP_ALLOW_NONTEMP_VAULT. It fires
//     even when the configured vault happens to live under os.TempDir(), which
//     arm B cannot see.
//   - ARM B, HYGIENE: the vault lives outside os.TempDir() — the signature of a
//     test that forgot to isolate XDG_CONFIG_HOME (the recurring stray
//     `Projects/p/` scaffold). Every properly-isolated test uses t.TempDir().
//     Set VP_ALLOW_NONTEMP_VAULT=1 to override this arm only.
//
// Arm B is deliberately kept rather than replaced by arm A: the ambient
// snapshot is the tier-3 global root only, so a test reaching a real vault
// through a tier-1 or tier-2 binding, or writing any other non-temp directory,
// is caught by arm B and by nothing else.
//
// Arm B's PREDICATE and DECISION ORDER are unchanged from the pre-U1 guard —
// the override is still consulted before pathUnderTempDir, and pathUnderTempDir
// still decides — which is why no existing test's verdict moves. Its panic TEXT
// did change (it now says "outside the OS temp dir" rather than "the real
// vault", because arm A is the one that can truthfully say "the real vault").
// Predicate and order, not the diagnostic: a reader checking "unchanged" should
// check the branch structure, not the string.
//
// The panic, rather than a returned error, is intentional: the three sibling
// refusals at this seat return typed errors, but StampForPath's own error is
// swallowed by a slog.Warn at its caller, and the incident this guard exists
// for was silent. A returned error can be logged and ignored; a panic cannot,
// short of a deliberate recover. It costs nothing in production because
// testing.Testing() gates it off before anything else runs, and it strands no
// flock: every caller that holds one releases it with defer.
//
// 🔴 CLAUSE ORDER IS LOAD-BEARING, NOT STYLE. Clauses (1) and (2) must stay
// above every path comparison, and sameRoot must never be reached with an
// empty string: filepath.Abs("") returns the PROCESS CWD, not an error.
// ResolveStampDir special-cases vaultPath == "" before it calls Abs; this seat
// has no such filter upstream of it, so folding the empty check down into
// sameRoot would turn GuardTestVaultWrite("", "") into a comparison of the
// process cwd against the ambient root. sameRoot panics on an empty argument
// so that mistake fails loudly instead of silently.
func GuardTestVaultWrite(vaultPath, writePath string) {
	if vaultPath == "" {
		return // (1) MUST BE FIRST — see the clause-order note above
	}
	if !testing.Testing() {
		return // (2) the signal: a go test binary, set at link time by cmd/go
	}
	// (3) ARM A. The `a != ""` test is what keeps sameRoot off an empty root.
	if a := ambientVaultRoot(); a != "" && sameRoot(vaultPath, a) {
		panic(fmt.Sprintf(
			"vp: refusing a write to the CONFIGURED vault under test\n"+
				"  target: %s\n"+
				"  vault:  %s\n"+
				"  this is the ambient configured vault root, resolved at process start.\n"+
				"A test is writing the operator's real vault — isolate it with "+
				"t.Setenv(\"XDG_CONFIG_HOME\", t.TempDir()) (or a temp --vault).\n"+
				"%s does NOT waive this arm.",
			writePath, vaultPath, allowNonTempVaultEnv))
	}
	// ARM B, and only arm B, is waivable.
	if os.Getenv(allowNonTempVaultEnv) != "" {
		return
	}
	if pathUnderTempDir(vaultPath) {
		return
	}
	panic(fmt.Sprintf(
		"vp: refusing vault write to non-temp path under test\n"+
			"  target: %s\n"+
			"  vault:  %s\n"+
			"A test is writing a vault outside the OS temp dir — isolate it with "+
			"t.Setenv(\"XDG_CONFIG_HOME\", t.TempDir()) (or a temp --vault).\n"+
			"Set %s=1 to override.",
		writePath, vaultPath, allowNonTempVaultEnv))
}

// AmbientVaultRootUnderTest reports whether root is the operator's configured
// vault root and the current process is a `go test` binary. It is the ONE
// predicate behind both commit refusals, and it is exported because
// internal/storage needs it directly for the error arm below.
//
// It reuses U1's init-time snapshot (ambient.go) and U1's sameRoot, so the
// definition of "the operator's vault" is identical for a content write and a
// commit, and there is one place for it to be wrong.
//
// 🔴 CLAUSE ORDER IS LOAD-BEARING, exactly as in GuardTestVaultWrite: clause
// (1) and the `a != ""` test in (4) are what keep sameRoot off an empty string,
// and sameRoot PANICS on one rather than comparing filepath.Abs("") — the
// process cwd — against the ambient root.
func AmbientVaultRootUnderTest(root string) bool {
	if root == "" {
		return false // (1) MUST BE FIRST — keeps sameRoot off an empty root
	}
	if !testing.Testing() {
		return false // (2) the signal, set at link time by cmd/go
	}
	a := ambientVaultRoot() // (3) U1's init-time snapshot
	return a != "" && sameRoot(root, a)
}

// GuardTestVaultCommit panics when a `go test` process is about to COMMIT to the
// operator's configured vault. what names the committing primitive, so a reader
// of the panic knows which seat fired.
//
// 🔴 ONE PREDICATE, TWO CONSEQUENCES — and the asymmetry is deliberate.
// This panicking form is used only where the enclosing vault-root lock is
// released by a DEFER, so a panic unwinds through the release:
// storage.commitAndPushPathsCore (acquire vaultsync.go:301, defer :312),
// storage.commitPathspec and storage.GitCommitAllAt.
//
// The merge and migration seats do NOT use it. There the release is MANUAL
// (vaultpull.go has no defer at all; vaultsync.go:866/:872), and a live
// recover() sits on a path that reaches them (internal/mcp/tools.go). A panic
// there could leave the vault-root flock held for the life of the process, and
// vaultlock.Acquire is a no-timeout LOCK_EX, so the next acquisition would block
// forever. Those seats call AmbientVaultRootUnderTest and return
// storage.ErrAmbientVaultCommit instead. Converting those sites to defer would
// mean holding the commit lock across every remote's network fetch, which the
// pull loop deliberately keeps outside it — a production locking change to serve
// a test-only guard, which this guard does not ask for.
//
// There is no non-temp arm and no override. git legitimately runs against
// non-temp project checkouts under test, so "outside the temp dir" carries no
// signal here; and nothing legitimately commits the configured vault under test,
// so there is nothing for VP_ALLOW_NONTEMP_VAULT to waive.
func GuardTestVaultCommit(vaultRoot, what string) {
	if !AmbientVaultRootUnderTest(vaultRoot) {
		return
	}
	panic(fmt.Sprintf(
		"vp: refusing to COMMIT to the CONFIGURED vault under test\n"+
			"  seat:  %s\n"+
			"  vault: %s\n"+
			"  this is the ambient configured vault root, resolved at process start.\n"+
			"A test is committing to the operator's real vault — isolate it with "+
			"t.Setenv(\"XDG_CONFIG_HOME\", t.TempDir()) (or a temp --vault).",
		what, vaultRoot))
}

// sameRoot reports whether two vault roots name the same directory. It
// tolerates a trailing separator (the operator's own config carries one;
// filepath.Abs cleans it, so the comparison is on cleaned paths and never on
// raw strings) and it tolerates SYMLINKS.
//
// The symlink arm is not decoration. The case it exists for is an ALIAS of the
// configured vault and the real vault both sitting under the temp root: arm B
// sees a temp path and allows the write, and without resolution arm A compares
// unequal and misses it — so both arms pass and the operator's configured vault
// is modified anyway. pathHasPrefix applies only Abs/Rel and does not resolve
// symlinks, so the resolved comparison is a second arm rather than a
// replacement, mirroring the raw-OR-resolved shape pathUnderTempDir already
// uses.
//
// It REFUSES an empty argument rather than defaulting to cwd. That is the
// code-level expression of GuardTestVaultWrite's clause-order contract: the
// empty-vaultPath return and the `a != ""` test must both run first.
func sameRoot(a, b string) bool {
	if a == "" || b == "" {
		panic("surface: sameRoot called with an empty root — the vaultPath == \"\" " +
			"and ambient != \"\" guards must run first (filepath.Abs(\"\") is cwd)")
	}
	if pathHasPrefix(a, b) && pathHasPrefix(b, a) {
		return true // same spelling, cleaned
	}
	ra, rb := resolveSymlinks(a), resolveSymlinks(b)
	return pathHasPrefix(ra, rb) && pathHasPrefix(rb, ra) // same directory
}

// pathUnderTempDir reports whether p resolves to a location under
// os.TempDir(). Both sides are cleaned and symlink-resolved so a macOS
// /var/folders temp (symlinked from /tmp) or a /tmp→/private/tmp indirection
// still matches.
func pathUnderTempDir(p string) bool {
	tmp := os.TempDir()
	return pathHasPrefix(p, tmp) || pathHasPrefix(resolveSymlinks(p), resolveSymlinks(tmp))
}

// resolveSymlinks returns the symlink-resolved absolute form of p, falling
// back to a cleaned absolute path when resolution fails (e.g. p does not yet
// exist — common for a write target's parent).
func resolveSymlinks(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// pathHasPrefix reports whether path p is the directory dir or lives beneath
// it, comparing cleaned absolute paths on path-segment boundaries (so
// "/tmpfoo" is not treated as under "/tmp").
func pathHasPrefix(p, dir string) bool {
	pa, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	da, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(da, pa)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
