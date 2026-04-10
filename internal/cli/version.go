package cli

import "fmt"

// BuildInfo holds version metadata injected at build time via ldflags.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// String returns a human-readable version string.
func (b BuildInfo) String() string {
	s := "vp " + b.Version
	if b.Commit != "" && b.Commit != "unknown" {
		s += " (" + b.Commit
		if b.BuildDate != "" && b.BuildDate != "unknown" {
			s += " " + b.BuildDate
		}
		s += ")"
	}
	return s
}

// Short returns just the version number.
func (b BuildInfo) Short() string {
	return fmt.Sprintf("vp %s", b.Version)
}
