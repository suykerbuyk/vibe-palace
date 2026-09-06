// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package surface

import (
	"errors"
	"strings"
	"testing"
)

func withSelfExe(t *testing.T, target string, err error) {
	t.Helper()
	prev := readSelfExe
	readSelfExe = func() (string, error) { return target, err }
	t.Cleanup(func() { readSelfExe = prev })
}

// TestSelfImageReplaced_DeletedMarkerIsTheSignal — the " (deleted)" suffix is
// the whole detection. It is what Linux appends once the running inode is
// unlinked, which is exactly what `make install` does under a live server.
func TestSelfImageReplaced_DeletedMarkerIsTheSignal(t *testing.T) {
	withSelfExe(t, "/home/u/.local/bin/vp (deleted)", nil)
	replaced, image := SelfImageReplaced()
	if !replaced {
		t.Fatal("a deleted image was not detected")
	}
	if !strings.Contains(image, "(deleted)") {
		t.Fatalf("image = %q, want the RAW link target so a caller can name it "+
			"without re-reading /proc", image)
	}
}

// TestSelfImageReplaced_LiveImageIsSilent is the positive control's twin: the
// healthy case must produce nothing, or the advisory becomes the ignorable
// warning it exists not to be.
func TestSelfImageReplaced_LiveImageIsSilent(t *testing.T) {
	withSelfExe(t, "/home/u/.local/bin/vp", nil)
	if replaced, image := SelfImageReplaced(); replaced || image != "" {
		t.Fatalf("a live image reported replaced=%v image=%q", replaced, image)
	}
}

// TestSelfImageReplaced_NoProcIsQuiet — a host without procfs (macOS, Windows,
// a container without /proc) must report false rather than guess. The signal is
// unavailable there, which is not the same as the condition being present.
func TestSelfImageReplaced_NoProcIsQuiet(t *testing.T) {
	withSelfExe(t, "", errors.New("readlink /proc/self/exe: no such file or directory"))
	if replaced, _ := SelfImageReplaced(); replaced {
		t.Fatal("an unreadable /proc/self/exe was treated as a stale image")
	}
}

// TestStaleBinaryAdvisory_StatesOutcomeAndBothChoices pins the operator ruling
// this text implements: advise before mutating, then let the operator decide.
// A notice that omits either choice is not an option, it is a complaint.
func TestStaleBinaryAdvisory_StatesOutcomeAndBothChoices(t *testing.T) {
	got := StaleBinaryAdvisory("/home/u/.local/bin/vp (deleted)")

	for _, want := range []string{
		"SUCCEEDED",                       // the outcome, stated first
		"/home/u/.local/bin/vp (deleted)", // which image
		"restart this AI host",            // how to stop
		"To proceed anyway",               // how to continue
		"vp check --check stale-mcp",      // how to re-derive it
	} {
		if !strings.Contains(got, want) {
			t.Errorf("advisory omits %q — a stranded operator must be able to read\n"+
				"their way out of it. Full text:\n%s", want, got)
		}
	}
	if strings.Contains(got, "refused") && !strings.Contains(got, "Nothing has been refused") {
		t.Error("the advisory reads as a refusal; it must not")
	}
}
