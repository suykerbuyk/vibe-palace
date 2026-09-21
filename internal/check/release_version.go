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

// CheckReleaseVersion reports whether the running binary's release
// major.minor agrees with surface.MCPSurfaceVersion.RequiredDataFormat. It is
// the check-time half of the versioning task filed 2026-09-12, extended by
// ADR-011 Decision 5 to the minor component: `.github/workflows/release.yml`
// guards a pushed TAG against both constants, but nothing previously checked
// a binary already built and running.
//
// Two version forms carry a tag, and both are checked (releaseMajorMinor):
//
//   - An exact MAJOR.MINOR.PATCH release build. Agreeing is Pass; disagreeing
//     is Fail, because the tag itself is wrong or the binary predates a bump.
//   - A git-describe build off a tag — MAJOR.MINOR.PATCH-<count>-g<sha>, or
//     MAJOR.MINOR.PATCH-dirty at the tag itself, either with an optional
//     -dirty — which is what the Makefile stamps (`git describe --tags
//     --always --dirty`, leading v stripped). Its base tag is compared:
//   - equal is Pass;
//   - BEHIND the constants is Info, not Fail: an unreleased dev build is
//     legitimate, but a base tag left behind by a bump is how v5.0.0
//     stayed the newest tag for 198 commits after the constants moved to
//     6.2 with no signal. The row names the tag to cut.
//   - AHEAD of the constants is Fail. The base tag is an ancestor of this
//     build, release.yml refused it unless it matched the constants at its
//     own commit, and both constants only ever rise — so constants below
//     an ancestor release mean a bump was reverted, and this binary would
//     write vaults that a released binary already stamped at a higher
//     surface or format. That is never a legitimate dev state.
//
// Anything else — empty (un-stamped), "dev", a bare SHA from a tagless repo,
// a pre-release tag such as 7.2.0-rc1, a goreleaser snapshot — carries no
// base tag to compare and passes as nothing to check.
func CheckReleaseVersion() Result {
	r := Result{Name: "Release version"}
	wantMajor, wantMinor := surface.MCPSurfaceVersion, surface.RequiredDataFormat

	major, minor, exact, ok := releaseMajorMinor(BuildVersion)
	if !ok {
		r.Status = Pass
		r.Summary = "no release tag in the build version (" + displayVersion(BuildVersion) + ") — nothing to check"
		return r
	}

	if !exact {
		// The tag itself: everything before the describe suffix, which
		// releaseMajorMinor has just proved starts at the first '-'.
		base := "v" + strings.SplitN(BuildVersion, "-", 2)[0]
		switch {
		case major == wantMajor && minor == wantMinor:
			r.Status = Pass
			r.Summary = fmt.Sprintf("dev build %s, base tag %s matches MCPSurfaceVersion.RequiredDataFormat %d.%d", BuildVersion, base, wantMajor, wantMinor)
		case major < wantMajor || (major == wantMajor && minor < wantMinor):
			r.Status = Info
			r.Summary = fmt.Sprintf("dev build %s: base tag %s is behind MCPSurfaceVersion.RequiredDataFormat %d.%d — no release carries these constants yet", BuildVersion, base, wantMajor, wantMinor)
			r.Details = []string{
				fmt.Sprintf("  the newest tag reachable from this build is %s, but the source it was built from is at %d.%d", base, wantMajor, wantMinor),
				fmt.Sprintf("  cut v%d.%d.0 when this work is released", wantMajor, wantMinor),
			}
		default:
			r.Status = Fail
			r.Summary = fmt.Sprintf("dev build %s: base tag %s is AHEAD of MCPSurfaceVersion.RequiredDataFormat %d.%d", BuildVersion, base, wantMajor, wantMinor)
			r.Details = []string{
				fmt.Sprintf("  %s is an ancestor of this build, so it was released with constants of at least %d.%d; this source has %d.%d", base, major, minor, wantMajor, wantMinor),
				"  both constants only rise: a bump was reverted, and this binary would write vaults a released binary already stamped higher. Restore the constants.",
			}
		}
		return r
	}

	if major == wantMajor && minor == wantMinor {
		r.Status = Pass
		r.Summary = fmt.Sprintf("v%d.%d matches MCPSurfaceVersion.RequiredDataFormat %d.%d", major, minor, wantMajor, wantMinor)
		return r
	}

	r.Status = Fail
	r.Summary = fmt.Sprintf("v%d.%d disagrees with MCPSurfaceVersion.RequiredDataFormat %d.%d", major, minor, wantMajor, wantMinor)
	r.Details = []string{
		fmt.Sprintf("  running binary version %q, but MCPSurfaceVersion.RequiredDataFormat is %d.%d", BuildVersion, wantMajor, wantMinor),
		"  cut vN.M.y matching MCPSurfaceVersion.RequiredDataFormat, or this binary predates a bump — restart the AI host after `make install`.",
	}
	return r
}

func displayVersion(v string) string {
	if v == "" {
		return "unstamped"
	}
	return v
}

// releaseMajorMinor parses the MAJOR.MINOR of the tag a version string names.
// exact is true for a release build, MAJOR.MINOR.PATCH and nothing more, and
// false for a git-describe build off that tag: PATCH followed by
// -<count>-g<hex sha>, by -dirty, or by both in that order. Anything else
// (empty, "dev", a bare SHA, a pre-release or snapshot suffix) is !ok.
func releaseMajorMinor(v string) (major, minor int, exact, ok bool) {
	if v == "" {
		return 0, 0, false, false
	}
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return 0, 0, false, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false, false
	}
	patch, suffix, hasSuffix := strings.Cut(parts[2], "-")
	if _, err := strconv.Atoi(patch); err != nil {
		return 0, 0, false, false
	}
	if !hasSuffix {
		return major, minor, true, true
	}
	if !isDescribeSuffix(suffix) {
		return 0, 0, false, false
	}
	return major, minor, false, true
}

// isDescribeSuffix reports whether s is what `git describe --tags --always
// --dirty` appends after a tag's PATCH: "<count>-g<sha>", "dirty", or
// "<count>-g<sha>-dirty". A pre-release tag's own suffix ("rc1", or
// "rc1-3-gabc" when described) does not match.
func isDescribeSuffix(s string) bool {
	s, dirty := strings.CutSuffix(s, "-dirty")
	if s == "dirty" && !dirty {
		return true
	}
	count, sha, found := strings.Cut(s, "-g")
	if !found || count == "" || sha == "" {
		return false
	}
	for _, c := range count {
		if c < '0' || c > '9' {
			return false
		}
	}
	for _, c := range sha {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
