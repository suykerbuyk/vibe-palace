// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"sync/atomic"

	"github.com/coder/hnsw"
)

// countingCosine is a TEST-ONLY distance: cosineNaNGuard plus a count of every
// evaluation, so cost assertions count library work instead of timing it. It
// is a named package-level function registered in this file's own init(), so
// a graph using it can still Export. No production path installs it.
func countingCosine(a, b []float32) float32 {
	distanceEvals.Add(1)
	return cosineNaNGuard(a, b)
}

// distanceEvals counts countingCosine calls.
var distanceEvals atomic.Int64

// countingDistanceName is countingCosine's registered name, distinct from
// distanceName.
const countingDistanceName = "vp-test-counting-cosine"

func init() {
	hnsw.RegisterDistanceFunc(countingDistanceName, countingCosine)
}
