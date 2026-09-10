// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"strings"
	"testing"
)

func TestRenderReadmeStub(t *testing.T) {
	cmds := RenderReadmeStub("commands")
	if cmds == "" {
		t.Fatal("commands stub is empty")
	}
	skills := RenderReadmeStub("skills")
	if skills == "" {
		t.Fatal("skills stub is empty")
	}
	if cmds == skills {
		t.Error("commands and skills stubs should differ")
	}
	if got := RenderReadmeStub("bogus"); got != "" {
		t.Errorf("unknown kind should return empty string, got %q", got)
	}
	if got := RenderReadmeStub(""); got != "" {
		t.Errorf("empty kind should return empty string, got %q", got)
	}
}

// TestRenderReadmeStub_PointsAtRealSources pins the positive half of the
// override-only rewrite: each stub names where a built-in can actually be
// fetched, and the project tier as the place to customise. There is no
// materialized copy under <vault>/Templates/ to point at.
func TestRenderReadmeStub_PointsAtRealSources(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		wants []string
	}{
		{"commands", []string{"vp_get_command", "Projects/<slug>/", "override → promote"}},
		{"skills", []string{"vp skills show", "override → promote"}},
	} {
		stub := RenderReadmeStub(tc.kind)
		for _, w := range tc.wants {
			if !strings.Contains(stub, w) {
				t.Errorf("%s stub does not mention %q:\n%s", tc.kind, w, stub)
			}
		}
		if n := strings.Count(stub, "\n"); n > 40 {
			t.Errorf("%s stub is %d lines; keep it at 40 or fewer so it gets read", tc.kind, n)
		}
	}
}
