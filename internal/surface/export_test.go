// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import "sync"

// These two seams reset per-process caches so a test can exercise the
// first-time-through path more than once. They live HERE, in a _test.go, rather
// than beside the code they reset — because a test-only helper sitting in a
// non-test file is indistinguishable, to any reader and to the source-audit gate,
// from a production capability that nothing invokes. Moving them costs nothing and
// removes the ambiguity at the source instead of annotating it away.

// resetStampCacheForTest clears the per-process stamped-dir cache.
func resetStampCacheForTest() {
	stampedDirs.Range(func(k, _ any) bool {
		stampedDirs.Delete(k)
		return true
	})
}

// resetUnrecognizedTopWarnForTest clears the once-per-name warning cache.
func resetUnrecognizedTopWarnForTest() {
	unrecognizedTopWarnMu.Lock()
	defer unrecognizedTopWarnMu.Unlock()
	unrecognizedTopWarn = map[string]*sync.Once{}
}
