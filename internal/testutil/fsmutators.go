// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testutil

// FSMutatorCalls names every function of package os that creates, changes or
// removes something on disk. It is the ONE inventory behind the
// "a filesystem effect must sit behind the under-test tripwire" ratchets
// (indexstore's TestWritesGoThroughThisFile and vaultlock's
// TestFilesystemEffectsAreSeatedFirst): two copies of this list would drift, and
// a drifted ratchet misses exactly the call it was narrowed to forget.
var FSMutatorCalls = map[string]bool{
	"WriteFile": true, "Rename": true, "Remove": true, "RemoveAll": true,
	"Create": true, "CreateTemp": true, "OpenFile": true, "Truncate": true,
	"MkdirAll": true, "Mkdir": true, "MkdirTemp": true, "Symlink": true,
	"Link": true, "Chmod": true, "Chtimes": true, "Chown": true,
}
