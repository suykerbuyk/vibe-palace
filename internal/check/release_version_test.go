// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// withBuildVersion sets BuildVersion for the duration of the test and restores
// it afterward — BuildVersion is a package-level var set once by main() in a
// real binary, so a test manipulating it must not leak the value across
// t.Parallel siblings.
func withBuildVersion(t *testing.T, v string) {
	t.Helper()
	prev := BuildVersion
	BuildVersion = v
	t.Cleanup(func() { BuildVersion = prev })
}

// TestCheckReleaseVersion covers every version form the build can carry: the
// Makefile's `git describe --tags --always --dirty` (leading v stripped),
// goreleaser's exact {{.Version}}, and the un-stamped/"dev" fallbacks.
func TestCheckReleaseVersion(t *testing.T) {
	s, f := surface.MCPSurfaceVersion, surface.RequiredDataFormat
	v := func(major, minor int, rest string) string { return fmt.Sprintf("%d.%d.0%s", major, minor, rest) }
	cases := []struct {
		name    string
		version string
		want    Status
		summary string // substring the summary must carry
		detail  string // substring some detail line must carry, "" for none
	}{
		// Exact release builds: today's contract, unchanged.
		{"exact agrees", v(s, f, ""), Pass, "matches", ""},
		{"exact major ahead", v(s+1, f, ""), Fail, "disagrees", "cut vN.M.y"},
		{"exact minor ahead", v(s, f+1, ""), Fail, "disagrees", "cut vN.M.y"},
		{"exact major behind", v(s-1, f, ""), Fail, "disagrees", "cut vN.M.y"},

		// Describe builds off a tag matching the constants.
		{"describe agrees", v(s, f, "-12-gcb45ced"), Pass, "matches", ""},
		{"describe agrees dirty", v(s, f, "-12-gcb45ced-dirty"), Pass, "matches", ""},
		{"at tag dirty agrees", v(s, f, "-dirty"), Pass, "matches", ""},

		// Describe builds whose base tag a bump left behind: the v5.0.0 case.
		{"describe major behind", "5.0.0-198-gcb45ced", Info, "base tag v5.0.0 is behind", fmt.Sprintf("cut v%d.%d.0", s, f)},
		{"describe minor behind", v(s, f-1, "-3-gabcdef0"), Info, "is behind", fmt.Sprintf("cut v%d.%d.0", s, f)},
		{"describe behind dirty", "5.0.0-198-gcb45ced-dirty", Info, "is behind", fmt.Sprintf("cut v%d.%d.0", s, f)},
		{"at tag dirty behind", "5.0.0-dirty", Info, "is behind", fmt.Sprintf("cut v%d.%d.0", s, f)},

		// Describe builds whose base tag is ahead of the constants: a reverted bump.
		{"describe major ahead", v(s+1, f, "-4-gabcdef0"), Fail, "AHEAD", "Restore the constants"},
		{"describe minor ahead", v(s, f+1, "-4-gabcdef0-dirty"), Fail, "AHEAD", "Restore the constants"},

		// Nothing to check.
		{"unstamped", "", Pass, "nothing to check", ""},
		{"dev fallback", "dev", Pass, "nothing to check", ""},
		{"bare sha, tagless repo", "4220e4c", Pass, "nothing to check", ""},
		{"bare sha dirty", "4220e4c-dirty", Pass, "nothing to check", ""},
		{"pre-release tag", v(s, f, "-rc1"), Pass, "nothing to check", ""},
		{"pre-release tag described", v(s, f, "-rc1-3-gabcdef0"), Pass, "nothing to check", ""},
		{"goreleaser snapshot", fmt.Sprintf("%d.%d.1-SNAPSHOT-abcdef0", s, f), Pass, "nothing to check", ""},
		{"garbage", "not-a-version-at-all", Pass, "nothing to check", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withBuildVersion(t, tc.version)
			r := CheckReleaseVersion()
			if r.Status != tc.want {
				t.Fatalf("BuildVersion=%q: status = %v, want %v: %+v", tc.version, r.Status, tc.want, r)
			}
			if !strings.Contains(r.Summary, tc.summary) {
				t.Errorf("BuildVersion=%q: summary %q does not contain %q", tc.version, r.Summary, tc.summary)
			}
			if tc.detail != "" && !strings.Contains(strings.Join(r.Details, "\n"), tc.detail) {
				t.Errorf("BuildVersion=%q: details %q do not contain %q", tc.version, r.Details, tc.detail)
			}
		})
	}
}

func TestReleaseMajorMinor(t *testing.T) {
	cases := []struct {
		v         string
		wantMajor int
		wantMinor int
		wantExact bool
		wantOK    bool
	}{
		{"5.0.0", 5, 0, true, true},
		{"6.2.0", 6, 2, true, true},
		{"5.0.0-198-gcb45ced", 5, 0, false, true},
		{"5.0.0-198-gcb45ced-dirty", 5, 0, false, true},
		{"5.0.0-dirty", 5, 0, false, true},
		{"", 0, 0, false, false},
		{"dev", 0, 0, false, false},
		{"4220e4c", 0, 0, false, false}, // no dots at all
		{"not-a-version-at-all", 0, 0, false, false},
		{"a.0.0", 0, 0, false, false},                  // non-numeric major
		{"5.a.0", 0, 0, false, false},                  // non-numeric minor
		{"5.0.a", 0, 0, false, false},                  // non-numeric patch
		{"5.0.0-rc1", 0, 0, false, false},              // pre-release tag
		{"5.0.0-rc1-3-gabcdef0", 0, 0, false, false},   // described pre-release tag
		{"5.0.0-3-g", 0, 0, false, false},              // no sha
		{"5.0.0--gabcdef0", 0, 0, false, false},        // no count
		{"5.0.0-x3-gabcdef0", 0, 0, false, false},      // non-numeric count
		{"5.0.0-3-gABCDEFZ", 0, 0, false, false},       // not a lowercase hex sha
		{"5.0.0-dirty-dirty", 0, 0, false, false},      // doubled marker
		{"5.0.1-SNAPSHOT-abcdef0", 0, 0, false, false}, // goreleaser snapshot
	}
	for _, tc := range cases {
		t.Run(tc.v, func(t *testing.T) {
			major, minor, exact, ok := releaseMajorMinor(tc.v)
			if major != tc.wantMajor || minor != tc.wantMinor || exact != tc.wantExact || ok != tc.wantOK {
				t.Errorf("releaseMajorMinor(%q) = (%d, %d, exact=%v, ok=%v), want (%d, %d, exact=%v, ok=%v)",
					tc.v, major, minor, exact, ok, tc.wantMajor, tc.wantMinor, tc.wantExact, tc.wantOK)
			}
		})
	}
}

func TestReleaseVersionProducer(t *testing.T) {
	withBuildVersion(t, fmt.Sprintf("%d.0.0", surface.MCPSurfaceVersion+1))
	results, err := RunSelected("", "release-version")
	if err != nil {
		t.Fatalf("RunSelected: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("want one result, got %d", len(results))
	}
	if results[0].Name != "Release version" || results[0].Status != Fail {
		t.Errorf("got %+v, want a failing Release version row", results[0])
	}
}
