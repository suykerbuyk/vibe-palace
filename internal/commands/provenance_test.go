// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// The upgrade and reset surfaces classify a vault copy exactly as `vp config
// sync` does (templates.ClassifyVaultCopy). A copy of an earlier shipped
// version is vp's bytes that sync prunes — stale, not an override — and a
// reset of vp-shipped bytes needs no backup (Review M3).

func earlierRestart(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "templates", "testdata", "earlier", "commands", "restart.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func TestPlanOne_ClassifiesByProvenance(t *testing.T) {
	const ref = "code-digger/references/prompt-deep-dive.md"
	vault := t.TempDir()
	putFile(t, vault, "Templates/commands/wrap.md", embedded(t, "command:wrap"))
	putFile(t, vault, "Templates/commands/capture.md", crlf(embedded(t, "command:capture")))
	putFile(t, vault, "Templates/commands/restart.md", earlierRestart(t))
	putFile(t, vault, "Templates/commands/cancel-plan.md", "# my cancel-plan\n")

	want := map[string]ChangeKind{
		"wrap":        ChangeUnchanged,
		"capture":     ChangeUnchanged, // CRLF, line endings aside
		"restart":     ChangeStale,
		"cancel-plan": ChangeOverride,
	}
	for _, c := range planFor(t, vault, "command", "") {
		if w, ok := want[c.Name]; ok {
			if c.Kind != w {
				t.Errorf("%s: kind %s, want %s", c.Name, c.Kind, w)
			}
		} else if c.Kind != ChangeUnneeded {
			t.Errorf("%s: kind %s, want unneeded", c.Name, c.Kind)
		}
		if c.EmbeddedRel != "commands/"+c.Name+".md" {
			t.Errorf("%s: EmbeddedRel = %q", c.Name, c.EmbeddedRel)
		}
	}

	putFile(t, vault, "Templates/skills/"+ref, "# my reference\n")
	skill := planFor(t, vault, "skill", ref)
	if len(skill) != 1 || skill[0].EmbeddedRel != "skills/"+ref || skill[0].Kind != ChangeOverride {
		t.Errorf("skill reference: %+v", skill)
	}
}

func TestReset_ShippedCopyNeedsNoBackup(t *testing.T) {
	vault := t.TempDir()
	putFile(t, vault, "Templates/commands/restart.md", earlierRestart(t))
	putFile(t, vault, "Templates/commands/capture.md", crlf(embedded(t, "command:capture")))
	putFile(t, vault, "Templates/commands/wrap.md", embedded(t, "command:wrap"))
	const mine = "# my cancel-plan\n"
	putFile(t, vault, "Templates/commands/cancel-plan.md", mine)

	plan := planFor(t, vault, "command", "")
	if err := CheckResetPaths(plan); err != nil {
		t.Fatal(err)
	}
	out, err := Reset(plan)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		mirror      bool
		prov        templates.Provenance
		lineEndings bool
	}
	wants := map[string]want{
		"restart":     {true, templates.ProvenanceEarlier, false},
		"capture":     {true, templates.ProvenanceCurrent, true},
		"wrap":        {true, templates.ProvenanceCurrent, false},
		"cancel-plan": {false, templates.ProvenanceOperator, false},
	}
	if len(out) != len(wants) {
		t.Fatalf("outcomes = %+v", out)
	}
	for _, o := range out {
		w := wants[o.Name]
		if !o.Removed || o.Mirror != w.mirror || o.Provenance != w.prov || o.LineEndings != w.lineEndings {
			t.Errorf("%s: %+v, want %+v", o.Name, o, w)
		}
		if w.mirror && o.Backup != "" {
			t.Errorf("%s: a backup of vp-shipped bytes: %s", o.Name, o.Backup)
		}
	}
	for _, o := range out {
		if o.Name == "cancel-plan" {
			bk := templates.BackupName("Templates/commands/cancel-plan.md", []byte(mine))
			if o.Backup != bk {
				t.Errorf("operator copy backup = %q, want %q", o.Backup, bk)
			}
		}
	}
	entries, _ := os.ReadDir(filepath.Join(vault, "Templates", "commands"))
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "cancel-plan.md.") || !strings.HasSuffix(entries[0].Name(), ".bak") {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("Templates/commands/ = %v, want only the operator copy's backup", names)
	}
}
