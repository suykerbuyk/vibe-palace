// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// BuildVersion is the running binary's version string, set once in main()
// from the same ldflags-stamped var main.version uses — before any subcommand
// dispatches, so it is safe for the long-lived `vp mcp` / `vp mcp serve`
// server too. Left empty (the zero value, matching an un-stamped test or `go
// run` binary) it is simply not a release build, and CheckReleaseVersion
// treats that as nothing to check rather than a failure.
var BuildVersion string

// CheckReleaseVersion reports whether the running binary's release major
// version agrees with surface.MCPSurfaceVersion. It is the check-time half of
// the versioning task filed 2026-09-12: `.github/workflows/release.yml`
// guards a pushed TAG against the constant, but nothing previously checked a
// binary already built and running.
//
// Only a real MAJOR.MINOR.PATCH release version is checked. A bare
// git-describe SHA, a "dev" fallback, or a pre-release/dirty suffix all mean
// "not a tagged release build" and pass without comparison — there is no tag
// major to disagree with.
func CheckReleaseVersion() Result {
	r := Result{Name: "Release version"}

	major, ok := releaseMajor(BuildVersion)
	if !ok {
		r.Status = Pass
		r.Summary = "not a tagged release build (" + displayVersion(BuildVersion) + ") — nothing to check"
		return r
	}

	if major == surface.MCPSurfaceVersion {
		r.Status = Pass
		r.Summary = fmt.Sprintf("v%d matches MCPSurfaceVersion %d", major, surface.MCPSurfaceVersion)
		return r
	}

	r.Status = Fail
	r.Summary = fmt.Sprintf("v%d disagrees with MCPSurfaceVersion %d", major, surface.MCPSurfaceVersion)
	r.Details = []string{
		fmt.Sprintf("  running binary version %q, but MCPSurfaceVersion is %d", BuildVersion, surface.MCPSurfaceVersion),
		"  cut vN.x.y matching MCPSurfaceVersion, or this binary predates a surface bump — restart the AI host after `make install`.",
	}
	return r
}

func displayVersion(v string) string {
	if v == "" {
		return "unstamped"
	}
	return v
}

// releaseMajor parses a leading MAJOR from a MAJOR.MINOR.PATCH release
// version, rejecting anything else (empty, "dev", a bare git-describe SHA, a
// "-dirty"/pre-release suffix) as not a release build.
func releaseMajor(v string) (int, bool) {
	if v == "" {
		return 0, false
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, false
	}
	// A dirty/pre-release suffix on the patch component (e.g. "0-3-gabcdef" or
	// "0-dirty") means this is a git-describe pseudo-version off a tag, not
	// the tag itself.
	if strings.ContainsAny(parts[2], "-+") {
		return 0, false
	}
	if _, err := strconv.Atoi(parts[1]); err != nil {
		return 0, false
	}
	if _, err := strconv.Atoi(parts[2]); err != nil {
		return 0, false
	}
	return major, true
}
