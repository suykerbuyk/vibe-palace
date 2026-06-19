// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// allowNonTempVaultEnv, when set to a non-empty value, disables the
// under-test vault-write tripwire. Reserved for the rare case where a test
// genuinely needs to write a vault outside the OS temp dir.
const allowNonTempVaultEnv = "VP_ALLOW_NONTEMP_VAULT"

// runningUnderGoTest reports whether the current process is a `go test`
// binary. It checks the binary name suffix and the presence of the test
// framework's registered flags. It deliberately does NOT import the testing
// package, which would pull test-only flag registration into the production
// binary.
func runningUnderGoTest() bool {
	if strings.HasSuffix(os.Args[0], ".test") {
		return true
	}
	return flag.Lookup("test.v") != nil
}

// GuardTestVaultWrite panics when a `go test` process is about to write a
// vault that lives OUTSIDE the OS temp directory — the signature of a test
// that forgot to isolate XDG_CONFIG_HOME and is polluting the operator's real
// vault (the recurring stray `Projects/p/` scaffold). It is a no-op in the
// production `vp` binary (whose os.Args[0] is not a *.test binary) and when the
// vault path is under os.TempDir() (every properly-isolated test uses
// t.TempDir()). Set VP_ALLOW_NONTEMP_VAULT=1 to override.
//
// The panic, rather than a logged warning, is intentional: it turns a silent
// real-vault write into an immediate, attributable test failure at the write
// site.
func GuardTestVaultWrite(vaultPath, writePath string) {
	if vaultPath == "" {
		return
	}
	if !runningUnderGoTest() {
		return
	}
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
			"A test is writing the real vault — isolate it with "+
			"t.Setenv(\"XDG_CONFIG_HOME\", t.TempDir()) (or a temp --vault).\n"+
			"Set %s=1 to override.",
		writePath, vaultPath, allowNonTempVaultEnv))
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
