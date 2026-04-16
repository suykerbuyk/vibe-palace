// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"fmt"
	"os"
	"path/filepath"
)

// ClaimPath returns the filesystem path for a session's claim sentinel.
// claimDir is the already-resolved directory (typically <CWD>/.vibe-palace).
func ClaimPath(claimDir, sessionID string) string {
	return filepath.Join(claimDir, "claimed-"+sessionID)
}

// IsClaimed reports whether a claim sentinel exists for the given session.
func IsClaimed(claimDir, sessionID string) bool {
	_, err := os.Stat(ClaimPath(claimDir, sessionID))
	return err == nil
}

// WriteClaim writes a claim sentinel for the given session. Creates the
// parent directory if needed. The noteID is written as the sentinel's
// content so operators can trace which vault note claimed the session.
func WriteClaim(claimDir, sessionID, noteID string) error {
	path := ClaimPath(claimDir, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create claim dir: %w", err)
	}
	return os.WriteFile(path, []byte(noteID+"\n"), 0o644)
}
