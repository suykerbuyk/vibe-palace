// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package onboard

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// projectShimRow runs the command-shims step and returns its project row.
func projectShimRow(t *testing.T, req Request) Outcome {
	t.Helper()
	for _, o := range stepCommandShims(context.Background(), req) {
		if o.Name == "Slash-command shims (project)" {
			return o
		}
	}
	t.Fatal("no \"Slash-command shims (project)\" row")
	return Outcome{}
}

var shimShaRe = regexp.MustCompile(`sha=[0-9a-f]+ -->`)

// staleShim rewrites a managed shim's marker to a sha no render produces, the
// on-disk state an older binary leaves once the expected sha moves.
func staleShim(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, shimShaRe.ReplaceAll(b, []byte("sha=0000000 -->")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCommandShimsNamesReRenderedSkillShims: when `vp init` re-renders skill
// shims (a one-time re-key after an upgrade), the project row names them, and
// shows the Grok command hub as "the Grok /vpc hub" rather than a persona
// called "vpc". A run that only adds shims prints no such line.
func TestCommandShimsNamesReRenderedSkillShims(t *testing.T) {
	sandboxHost(t)
	req, _ := newRequest(t, true)
	for _, d := range []string{".grok", ".cursor/rules"} {
		if err := os.MkdirAll(filepath.Join(req.ProjectDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	first := projectShimRow(t, req)
	for _, d := range first.Details {
		if strings.Contains(d, "re-rendered") {
			t.Errorf("a first run only adds shims, but printed %q", d)
		}
	}

	staleShim(t, filepath.Join(req.ProjectDir, ".grok", "skills", "vpc", "SKILL.md"))
	staleShim(t, filepath.Join(req.ProjectDir, ".cursor", "rules", "vps-chair.mdc"))
	staleShim(t, filepath.Join(req.ProjectDir, ".grok", "skills", "vps-chair", "SKILL.md"))

	second := projectShimRow(t, req)
	want := "  skill shims re-rendered: chair, the Grok /vpc hub"
	if len(second.Details) != 1 || second.Details[0] != want {
		t.Errorf("Details = %q, want [%q]", second.Details, want)
	}
	if !strings.Contains(second.Summary, "updated 2") {
		t.Errorf("Summary = %q, want the two re-rendered names counted", second.Summary)
	}
}
