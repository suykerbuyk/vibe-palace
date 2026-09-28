// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package departure

import (
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/departedpath"
)

// departedpath sits below this package (atomicfile and vaultfs, which this
// package reaches through internal/slug, both import it), so it reads the
// record at a path of its own. This pins the two together: the one departure
// rule and every write refusal must look exactly where the record is written.
func TestDepartedpathReadsTheRecordWhereDepartureWritesIt(t *testing.T) {
	for _, s := range []string{"p", "qa-metabuild-system", "orchestrator"} {
		if got, want := departedpath.RecordRel(s), RelPath(s); got != want {
			t.Fatalf("departedpath reads %s, departure writes %s", got, want)
		}
	}
}
