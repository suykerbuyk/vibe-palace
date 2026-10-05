// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/coder/hnsw"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// distanceRegistrationErr is what checkDistanceRegistered found, before any
// test ran. TestDistanceNameIsPinned reports it.
var distanceRegistrationErr error

// TestMain checks the HNSW distance registration before any test can construct
// an index. A registration that had moved out of init() into a constructor
// would be masked by whichever earlier test happened to construct one; here
// nothing has.
func TestMain(m *testing.M) {
	if os.Getenv(helperModeEnv) != "" {
		os.Exit(runHelper())
	}
	distanceRegistrationErr = checkDistanceRegistered()
	os.Exit(testutil.RunHermetic(m))
}

// checkDistanceRegistered proves, on a raw library graph, that the library's
// registry maps distanceName to cosineNaNGuard. The registry is unexported and
// has no lookup, so the only way to read it is an Export (which looks the
// function up by code pointer and writes its name) followed by an Import
// (which resolves the name back to a function).
func checkDistanceRegistered() error {
	g := hnsw.NewGraph[uint64]()
	g.Distance = cosineNaNGuard
	g.Add(hnsw.MakeNode(uint64(0), []float32{1, 0, 0}))
	var buf bytes.Buffer
	if err := g.Export(&buf); err != nil {
		return fmt.Errorf("export with cosineNaNGuard: %w", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte(distanceName)) {
		return fmt.Errorf("exported graph does not name %q", distanceName)
	}
	imported := hnsw.NewGraph[uint64]()
	if err := imported.Import(&buf); err != nil {
		return fmt.Errorf("import: %w", err)
	}
	if reflect.ValueOf(imported.Distance).Pointer() != reflect.ValueOf(cosineNaNGuard).Pointer() {
		return fmt.Errorf("%q resolves to a function other than cosineNaNGuard", distanceName)
	}
	return nil
}
