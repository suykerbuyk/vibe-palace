// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package plugin

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSurfaceStamp(t *testing.T) {
	cases := []struct {
		version, commit, want string
	}{
		{"0.1.0", "abc1234", "0.1.0-abc1234"},
		{"0.1.0", "unknown", "0.1.0"},
		{"0.1.0", "", "0.1.0"},
		{"", "abc1234", "abc1234"},
		{"unknown", "abc1234", "abc1234"},
		{"", "", "dev"},
		{"unknown", "unknown", "dev"},
		{"0.1.0", "feat/branch", "0.1.0-feat-branch"},
		{"..", "", "dev"},
		{"", "..", "dev"},
		{".", "abc", "abc"},
	}
	for _, tc := range cases {
		got := SurfaceStamp(tc.version, tc.commit)
		if got != tc.want {
			t.Errorf("SurfaceStamp(%q, %q) = %q, want %q", tc.version, tc.commit, got, tc.want)
		}
	}
}

func TestCacheInstallDirUsesStamp(t *testing.T) {
	isolate(t)
	stamp := SurfaceStamp("0.1.0", "deadbeef")
	dir := CacheInstallDir(stamp)
	wantTail := filepath.Join("cache", MarketplaceName, pluginName, stamp)
	if !strings.HasSuffix(dir, wantTail) {
		t.Errorf("CacheInstallDir(%q) = %q, want suffix %q", stamp, dir, wantTail)
	}
}
