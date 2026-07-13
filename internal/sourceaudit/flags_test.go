// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import "flag"

// updateBaseline regenerates baseline.json from the current tree.
// Usage: go test ./internal/sourceaudit -update-baseline
var updateBaseline = flag.Bool("update-baseline", false, "rewrite baseline.json from the current findings")
