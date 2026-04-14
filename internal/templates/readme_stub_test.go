// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import "testing"

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
