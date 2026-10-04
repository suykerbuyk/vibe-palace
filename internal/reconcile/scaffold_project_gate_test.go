// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/projectdir"
)

// The init scaffold is the project-creating writer: it writes its READMEs
// into a project the vault has not initialised (absent, or a phantom holding
// only memory/) past the write primitives' uninitialised-project gate, and the
// project it leaves is Initialised.
func TestScaffoldInitialisesAnUninitialisedProject(t *testing.T) {
	for _, project := range []string{"fresh", "ph"} {
		t.Run(project, func(t *testing.T) {
			root := t.TempDir()
			if project == "ph" {
				p := filepath.Join(root, "Projects", "ph", "memory", "x.md")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("m\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if st, err := projectdir.ClassifyProjectDir(root, project); err != nil || st.Initialised() {
				t.Fatalf("before scaffold: state %v, err %v; want uninitialised", st, err)
			}
			r := NewTemplateTree(root, "Projects/"+project, TemplateTreeSeed{Mode: TemplateModeScaffold})
			plan, err := r.Plan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			rep, err := r.Apply(context.Background(), plan)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(rep.Errors) > 0 {
				t.Fatalf("apply errors: %v", rep.Errors)
			}
			st, err := projectdir.ClassifyProjectDir(root, project)
			if err != nil || st != projectdir.ProjectScaffoldOnly {
				t.Errorf("after scaffold: state %v, err %v; want %v", st, err, projectdir.ProjectScaffoldOnly)
			}
		})
	}
}
