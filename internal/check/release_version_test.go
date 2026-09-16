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

func TestCheckReleaseVersion_Agrees(t *testing.T) {
	withBuildVersion(t, fmt.Sprintf("%d.%d.0", surface.MCPSurfaceVersion, surface.RequiredDataFormat))
	r := CheckReleaseVersion()
	if r.Status != Pass {
		t.Fatalf("agreeing major.minor: status = %v, want Pass: %+v", r.Status, r)
	}
	if !strings.Contains(r.Summary, "matches") {
		t.Errorf("summary should say it matches: %q", r.Summary)
	}
}

func TestCheckReleaseVersion_DisagreesMajor(t *testing.T) {
	withBuildVersion(t, fmt.Sprintf("%d.%d.0", surface.MCPSurfaceVersion+1, surface.RequiredDataFormat))
	r := CheckReleaseVersion()
	if r.Status != Fail {
		t.Fatalf("disagreeing major: status = %v, want Fail: %+v", r.Status, r)
	}
	if !strings.Contains(r.Summary, "disagrees") {
		t.Errorf("summary should say it disagrees: %q", r.Summary)
	}
}

func TestCheckReleaseVersion_DisagreesMinor(t *testing.T) {
	withBuildVersion(t, fmt.Sprintf("%d.%d.0", surface.MCPSurfaceVersion, surface.RequiredDataFormat+1))
	r := CheckReleaseVersion()
	if r.Status != Fail {
		t.Fatalf("disagreeing minor: status = %v, want Fail: %+v", r.Status, r)
	}
	if !strings.Contains(r.Summary, "disagrees") {
		t.Errorf("summary should say it disagrees: %q", r.Summary)
	}
}

func TestCheckReleaseVersion_NonReleaseBuildsPass(t *testing.T) {
	for _, v := range []string{
		"",                // un-stamped test/go-run binary
		"dev",             // git describe error fallback
		"4220e4c",         // bare git-describe SHA, no tags in repo
		"5.0.0-3-gabcdef", // git-describe pseudo-version off a tag
		"5.0.0-dirty",     // dirty working tree suffix
		"not-a-version-at-all",
	} {
		t.Run(v, func(t *testing.T) {
			withBuildVersion(t, v)
			r := CheckReleaseVersion()
			if r.Status != Pass {
				t.Fatalf("BuildVersion=%q: status = %v, want Pass (nothing to check): %+v", v, r.Status, r)
			}
		})
	}
}

func TestReleaseMajorMinor(t *testing.T) {
	cases := []struct {
		v         string
		wantMajor int
		wantMinor int
		wantOK    bool
	}{
		{"5.0.0", 5, 0, true},
		{"6.2.0", 6, 2, true},
		{"", 0, 0, false},
		{"4220e4c", 0, 0, false}, // no dots at all
		{"not-a-version-at-all", 0, 0, false},
		{"a.0.0", 0, 0, false},           // non-numeric major
		{"5.a.0", 0, 0, false},           // non-numeric minor
		{"5.0.a", 0, 0, false},           // non-numeric patch, no dirty marker
		{"5.0.0-dirty", 0, 0, false},     // dirty suffix on patch
		{"5.0.0-3-gabcdef", 0, 0, false}, // git-describe pseudo-version
	}
	for _, tc := range cases {
		t.Run(tc.v, func(t *testing.T) {
			major, minor, ok := releaseMajorMinor(tc.v)
			if major != tc.wantMajor || minor != tc.wantMinor || ok != tc.wantOK {
				t.Errorf("releaseMajorMinor(%q) = (%d, %d, %v), want (%d, %d, %v)", tc.v, major, minor, ok, tc.wantMajor, tc.wantMinor, tc.wantOK)
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
