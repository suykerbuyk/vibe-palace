// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod. The Makefile lives there and the test needs to invoke it
// where make itself would run.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

// TestMakefileDerivesDirtyFromVersion drives the REAL Makefile and asserts the
// ldflags it would pass. It overrides VERSION rather than dirtying the tree, so
// the assertion is deterministic and — the point of the whole unit — it proves
// the marker is derived from $(VERSION) and from nothing else. If someone later
// swaps the derivation for a second git call, VERSION would stop controlling
// the answer and these rows would disagree with git instead of with each other.
//
// `make -n` prints the recipe without running it, so no binary is built and no
// file is touched.
func TestMakefileDerivesDirtyFromVersion(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make not installed")
	}
	root := moduleRoot(t)

	for _, tc := range []struct {
		name    string
		version string
		want    string
	}{
		{"describe reports dirty", "ef1d518-dirty", "main.dirty=true"},
		{"describe is clean", "ef1d518", "main.dirty=false"},
		{"tagged and dirty", "v0.2.0-3-gabc1234-dirty", "main.dirty=true"},
		{"tagged and clean", "v0.2.0-3-gabc1234", "main.dirty=false"},
		{"no git at all", "dev", "main.dirty=false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("make", "-n", "install", "VERSION="+tc.version)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make -n install: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("VERSION=%q did not yield %q in the ldflags.\n%s",
					tc.version, tc.want, out)
			}
		})
	}
}

// TestStampedBinaryReportsDirty builds a REAL vp and reads what it prints.
//
// It does NOT reuse buildVPBinary: that helper caches one binary per process
// behind a sync.Once and builds with no ldflags at all, so it could never carry
// a stamp. It also does not re-derive the expected value by running git — the
// binary is stamped with a value this test chose, and the assertion is against
// that value, so a pass means the binary carried the answer rather than that
// the test could run git.
func TestStampedBinaryReportsDirty(t *testing.T) {
	for _, tc := range []struct {
		name       string
		dirty      string
		wantMarker bool
	}{
		{"dirty", "true", true},
		{"clean", "false", false},
		{"unstamped", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "vp")
			if runtime.GOOS == "windows" {
				bin += ".exe"
			}
			ldflags := "-X main.version=9.9.9 -X main.commit=abc1234 -X main.buildDate=2026-01-01"
			if tc.dirty != "" {
				ldflags += " -X main.dirty=" + tc.dirty
			}
			build := exec.Command("go", "build", "-ldflags", ldflags, "-o", bin,
				"github.com/suykerbuyk/vibe-palace/cmd/vp")
			build.Stderr = os.Stderr
			if err := build.Run(); err != nil {
				t.Fatalf("build: %v", err)
			}

			out, err := exec.Command(bin, "version").CombinedOutput()
			if err != nil {
				t.Fatalf("vp version: %v\n%s", err, out)
			}
			got := strings.TrimSpace(string(out))

			// No flag: the marker must be in the output a human gets from the
			// bare command.
			hasMarker := strings.Contains(got, "DIRTY")
			if hasMarker != tc.wantMarker {
				t.Errorf("vp version = %q; DIRTY present=%v, want %v", got, hasMarker, tc.wantMarker)
			}
			if !strings.Contains(got, "9.9.9") || !strings.Contains(got, "abc1234") {
				t.Errorf("vp version lost the stamped values: %q", got)
			}
		})
	}
}
