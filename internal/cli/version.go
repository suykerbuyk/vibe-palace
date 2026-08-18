package cli

import "fmt"

// DirtyBuild is the value main.dirty carries when the working tree had
// uncommitted changes at build time. The Makefile derives it from $(VERSION) —
// the single `git describe --tags --always --dirty` call — so it can never
// disagree with the version string the way a second git invocation could.
const DirtyBuild = "true"

// BuildInfo holds version metadata injected at build time via ldflags.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string

	// Dirty reports whether the working tree had uncommitted changes at build
	// time: DirtyBuild ("true"), "false", or EMPTY when the build did not
	// stamp it at all.
	//
	// Empty is not "clean". A plain `go build`, the CI smoke build, the AUR
	// PKGBUILD and goreleaser all produce a binary with no dirty stamp, and
	// claiming those are clean would be the exact failure this field exists to
	// end — an instrument reporting a state it cannot observe. Only the literal
	// DirtyBuild is a positive claim; everything else is silence.
	//
	// It is a separate field rather than a suffix on Commit because Commit is
	// consumed as an identity token, not just printed: plugin.SurfaceStamp
	// builds the Claude plugin cache DIRECTORY NAME and the plugin.json version
	// field from it (internal/plugin/plugin.go), and that file is read by
	// Claude Code, outside this repo, where nothing in this tree can verify
	// what shape it tolerates. Encoding a second fact into Commit would churn
	// that path between clean and dirty builds of one commit.
	Dirty string
}

// IsDirty reports a POSITIVE dirty claim. An unstamped build is not dirty and
// is not clean — it is unknown, and callers that need that distinction should
// test Dirty directly rather than inferring clean from a false here.
func (b BuildInfo) IsDirty() bool { return b.Dirty == DirtyBuild }

// String returns a human-readable version string.
//
// A dirty build says so here, with no flag required. That is the whole point:
// the two live occurrences this fixes were both binaries built from uncommitted
// source whose `vp version` output was indistinguishable from a clean build at
// the same commit, so nothing an operator would casually run could tell them.
// The marker is appended outside the parenthesised commit/date group and is
// deliberately shouted, because it is a provenance warning rather than a
// detail — the vault records what a binary wrote, and this is the only claim
// about which binary that was.
func (b BuildInfo) String() string {
	s := "vp " + b.Version
	if b.Commit != "" && b.Commit != "unknown" {
		s += " (" + b.Commit
		if b.BuildDate != "" && b.BuildDate != "unknown" {
			s += " " + b.BuildDate
		}
		s += ")"
	}
	// Outside the commit guard on purpose: a build can be dirty AND carry no
	// usable commit, and that is precisely when a reader most needs telling.
	if b.IsDirty() {
		s += " DIRTY: built from uncommitted changes"
	}
	return s
}

// Short returns just the version number.
func (b BuildInfo) Short() string {
	return fmt.Sprintf("vp %s", b.Version)
}
