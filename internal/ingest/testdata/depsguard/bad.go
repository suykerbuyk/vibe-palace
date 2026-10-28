// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package depsguard holds a deliberately BROKEN ingest.Deps literal used ONLY by
// TestDepsGuardWalkerUsesHelper to pin the drift-guard walker to the real
// scanDepsHealers. It lives under testdata/ so the Go build ignores it and the
// main TestDepsGraphHealerDriftGuard skips it. Do NOT "fix" the nil healer here;
// its whole purpose is to be a violation the helper must flag.
package depsguard

import ing "github.com/suykerbuyk/vibe-palace/internal/ingest"

// An aliased-import nil healer: the walker must resolve `ing` to the ingest
// import path and reject the nil, which a weaker `x.Name == "ingest"` re-parse
// would silently pass.
var _ = ing.Deps{GraphHealer: nil}
