// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !linux

package storage

// ONE-SHOT: deleted with project_slug_migration.go.

import "io/fs"

// slugOwnedByUs: there is no /proc to scan off Linux, so the guard refuses
// outright there (or takes --attest-no-agents). Nothing calls this with a real
// process entry; it exists so the package builds for the Windows host, which
// runs the cache phase and its own rollback.
func slugOwnedByUs(fs.FileInfo, int) bool { return false }
