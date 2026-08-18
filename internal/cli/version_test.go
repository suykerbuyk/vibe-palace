package cli

import "testing"

func TestBuildInfoString(t *testing.T) {
	tests := []struct {
		name string
		info BuildInfo
		want string
	}{
		{
			"full",
			BuildInfo{Version: "1.2.3", Commit: "abc1234", BuildDate: "2026-01-01"},
			"vp 1.2.3 (abc1234 2026-01-01)",
		},
		{
			"no commit",
			BuildInfo{Version: "1.0.0", Commit: "unknown", BuildDate: "unknown"},
			"vp 1.0.0",
		},
		{
			"commit only",
			BuildInfo{Version: "dev", Commit: "abc1234", BuildDate: "unknown"},
			"vp dev (abc1234)",
		},
		{
			"empty",
			BuildInfo{Version: "dev"},
			"vp dev",
		},
		// The load-bearing case. A clean-only assertion is today's bug wearing
		// a green check: a stub that always reports clean satisfies every row
		// above and none of these.
		{
			"dirty build is announced without a flag",
			BuildInfo{Version: "1.2.3", Commit: "abc1234", BuildDate: "2026-01-01", Dirty: DirtyBuild},
			"vp 1.2.3 (abc1234 2026-01-01) DIRTY: built from uncommitted changes",
		},
		{
			"dirty with no usable commit still announces",
			BuildInfo{Version: "dev", Commit: "unknown", BuildDate: "unknown", Dirty: DirtyBuild},
			"vp dev DIRTY: built from uncommitted changes",
		},
		{
			"explicitly clean says nothing",
			BuildInfo{Version: "1.2.3", Commit: "abc1234", BuildDate: "2026-01-01", Dirty: "false"},
			"vp 1.2.3 (abc1234 2026-01-01)",
		},
		{
			// Absence is not a value: an unstamped build has not observed its
			// own source state and must not be rendered as either claim.
			"unstamped says nothing",
			BuildInfo{Version: "1.2.3", Commit: "abc1234", BuildDate: "2026-01-01"},
			"vp 1.2.3 (abc1234 2026-01-01)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.info.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildInfoShort(t *testing.T) {
	info := BuildInfo{Version: "1.2.3", Commit: "abc", BuildDate: "today"}
	if got := info.Short(); got != "vp 1.2.3" {
		t.Errorf("Short() = %q, want %q", got, "vp 1.2.3")
	}
}

// TestBuildInfoIsDirty pins that only a POSITIVE stamp counts. The tempting
// shape — treating anything that is not "true" as clean — is what lets an
// unstamped build masquerade as a verified-clean one.
func TestBuildInfoIsDirty(t *testing.T) {
	for _, tc := range []struct {
		dirty string
		want  bool
	}{
		{DirtyBuild, true},
		{"false", false},
		{"", false},
		{"TRUE", false},
		{"yes", false},
	} {
		if got := (BuildInfo{Dirty: tc.dirty}).IsDirty(); got != tc.want {
			t.Errorf("IsDirty() with Dirty=%q = %v, want %v", tc.dirty, got, tc.want)
		}
	}
}
