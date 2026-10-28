// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"bytes"
	"testing"
)

// TestSetGateWriterSwapsAndRestores pins the exported seam that lets a cmd/vp
// test observe the gate's warn-only output. It sets writer 1, then writer 2,
// restores 2→1, and proves which writer the next warning reaches by WRITING AND
// READING — never by == os.Stderr, which the io.Writer widen makes meaningless
// and which the nested-restore case must not claim. Must not t.Parallel: it
// mutates the package-global gateStderr.
func TestSetGateWriterSwapsAndRestores(t *testing.T) {
	t.Setenv("VP_SURFACE_QUIET", "")
	var buf1, buf2 bytes.Buffer

	restore1 := SetGateWriter(&buf1)
	t.Cleanup(restore1)
	restore2 := SetGateWriter(&buf2)

	// buf2 is the active sink: a warn-only mismatch lands there, not in buf1.
	EnforceWarnOnly(aheadVault(t))
	if buf2.Len() == 0 {
		t.Fatal("warning did not reach the active writer (buf2)")
	}
	if buf1.Len() != 0 {
		t.Fatalf("warning leaked to the inactive writer (buf1): %q", buf1.String())
	}

	// Restore must put back the PREVIOUS writer (buf1), not os.Stderr.
	restore2()
	buf1.Reset()
	buf2.Reset()
	EnforceWarnOnly(aheadVault(t))
	if buf1.Len() == 0 {
		t.Fatal("after restore, warning did not reach the previous writer (buf1)")
	}
	if buf2.Len() != 0 {
		t.Fatalf("after restore, warning still reached buf2: %q", buf2.String())
	}
}

// TestSetGateWriterRejectsNil pins the nil guard: a nil sink would otherwise
// panic later inside Fprintln, at a confusing place (e.g. inside runHook).
func TestSetGateWriterRejectsNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("SetGateWriter(nil) did not panic")
		}
	}()
	SetGateWriter(nil)
}
