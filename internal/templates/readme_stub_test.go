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
//
// It also pins the Templates/ warning to what is still true. Neither `vp
// config sync` nor an upgrade command discards an override any more, so the
// stub must not say either does; the reset verb removes one on request and
// keeps a backup, and an unedited copy of a built-in is byte-identical to it
// and is pruned.
func TestRenderReadmeStub_PointsAtRealSources(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		wants []string
	}{
		{"commands", []string{"vp_get_command", "Projects/<slug>/", "override → promote",
			"never reset by", "vp commands reset <slug>", "keeps a backup", "edit it before syncing"}},
		{"skills", []string{"vp skills show", "override → promote",
			"never reset by", "vp skills reset <slug>", "keeps a backup", "edit it before syncing"}},
	} {
		stub := RenderReadmeStub(tc.kind)
		for _, w := range tc.wants {
			if !strings.Contains(stub, w) {
				t.Errorf("%s stub does not mention %q:\n%s", tc.kind, w, stub)
			}
		}
		for _, bad := range []string{"discarded by", "reset to the embedded copy by", "--overwrite"} {
			if strings.Contains(stub, bad) {
				t.Errorf("%s stub still says %q:\n%s", tc.kind, bad, stub)
			}
		}
		if n := strings.Count(stub, "\n"); n > 40 {
			t.Errorf("%s stub is %d lines; keep it at 40 or fewer so it gets read", tc.kind, n)
		}
	}
}
