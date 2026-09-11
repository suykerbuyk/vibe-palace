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
// fetched. There is no materialized copy under <vault>/Templates/ to point at.
//
// It also pins the override text to what is true now that provenance is a
// frozen shipped-version manifest: vault Templates/ is a supported vault-wide
// override tier beside this project directory, no vp reconciler or upgrade
// command changes an override at either tier, "safe" is scoped to exactly
// those (the generic vault tools reach any tier), an unedited copy of a
// built-in under <vault>/Templates/ — never one in this project directory —
// is pruned, and the reset verb removes an override and keeps a
// backup. The retired lock, its per-sync prompt and the old project-tier
// steer must not come back.
func TestRenderReadmeStub_PointsAtRealSources(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		wants []string
	}{
		{"commands", []string{"vp_get_command", "override → promote", "<vault>/Templates/commands/ for every",
			"No vp reconciler or upgrade command changes an override", "direct edits and reach",
			"vp commands reset <slug>", "keeps a backup", "edit it before syncing"}},
		{"skills", []string{"vp skills show", "override → promote", "<vault>/Templates/skills/ for every",
			"No vp reconciler or", "direct edits and reach any tier",
			"vp skills reset <slug>", "keeps a backup", "edit it before syncing"}},
	} {
		stub := RenderReadmeStub(tc.kind)
		// Compared with the line wrapping flattened, so rewrapping the
		// prose never breaks a pin.
		flat := strings.Join(strings.Fields(stub), " ")
		for _, w := range tc.wants {
			if !strings.Contains(flat, w) {
				t.Errorf("%s stub does not mention %q:\n%s", tc.kind, w, stub)
			}
		}
		// The prune is scoped to the vault tier: the stub is written into
		// Projects/<slug>/, where a copy of a built-in is never pruned.
		for _, w := range []string{"under <vault>/Templates/ is vp's bytes and is pruned", "a copy here is never pruned"} {
			if !strings.Contains(flat, w) {
				t.Errorf("%s stub does not scope the prune to the vault tier (%q):\n%s", tc.kind, w, stub)
			}
		}
		for _, bad := range []string{"discarded by", "reset to the embedded copy by", "--overwrite",
			"templates.lock", "prompts on every sync", "not recommended"} {
			if strings.Contains(stub, bad) {
				t.Errorf("%s stub still says %q:\n%s", tc.kind, bad, stub)
			}
		}
		if n := strings.Count(stub, "\n"); n > 40 {
			t.Errorf("%s stub is %d lines; keep it at 40 or fewer so it gets read", tc.kind, n)
		}
	}
}
