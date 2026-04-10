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
