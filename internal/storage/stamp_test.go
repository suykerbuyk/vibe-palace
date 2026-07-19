// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// writeFileDirect writes a precondition file straight to disk, bypassing the
// stamping writers, so a test can prove the writer-under-test does its own
// stamping (not a leftover stamp from a setup helper).
func writeFileDirect(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertStamped fails unless stampDir/.surface records the current surface.
func assertStamped(t *testing.T, stampDir string) {
	t.Helper()
	s, err := surface.ReadStamp(stampDir)
	if err != nil {
		t.Fatalf("ReadStamp(%s): %v", stampDir, err)
	}
	if s.Surface != surface.MCPSurfaceVersion {
		t.Fatalf("stamp at %s = surface %d, want %d", stampDir, s.Surface, surface.MCPSurfaceVersion)
	}
}

// TestEveryVaultWriterStamps is the enumeration guard for the surface
// handshake: each public method that writes into the vault must leave a
// .surface stamp at its root. A new write site that forgets to stamp (whole-file
// via atomicfile.Write, or append via v.stamp) fails here. Each case uses its
// own vault so the per-process stamp cache never masks a missing stamp.
//
// Both stamping routes stay covered here after the dead whole-file project
// writers (WriteWorkflow/WriteKnowledge/WriteDoc/WriteAbsorbed — test-only
// callers, deleted) were removed:
//
//   - atomicfile.Write called directly under a held lock: WriteResume (with the
//     assert-absent "" guard, its only create path).
//   - v.lockedWrite → atomicfile.Write: WriteSession, UpdateTaskStatus,
//     RetireTask, WriteVaultProjectConfig.
//   - v.stamp after a non-replace write: AppendIteration, AppendDrawer, the KG
//     writers.
//
// Stamp-dir resolution from a path NESTED below the project root (the thing the
// old WriteDoc case incidentally covered) is still exercised: CreateTask and
// friends write Projects/<slug>/tasks/<slug>.md yet must stamp Projects/<slug>.
func TestEveryVaultWriterStamps(t *testing.T) {
	const proj = "proj"
	const wing = "facts"
	const room = "general"

	projectsRoot := func(vault string) string { return filepath.Join(vault, "Projects", proj) }
	palaceRoot := func(vault string) string { return filepath.Join(vault, "palace", proj) }

	cases := []struct {
		name string
		// run performs the write under test and returns the stamp dir to check.
		run func(t *testing.T, v *Vault, vault string) string
	}{
		{"WriteResume", func(t *testing.T, v *Vault, vault string) string {
			if err := v.WriteResume(proj, "x", ""); err != nil {
				t.Fatal(err)
			}
			return projectsRoot(vault)
		}},
		{"AppendIterationOwned", func(t *testing.T, v *Vault, vault string) string {
			if _, _, err := v.AppendIterationOwned(proj, "t", "x", nil); err != nil {
				t.Fatal(err)
			}
			return projectsRoot(vault)
		}},
		{"WriteSession", func(t *testing.T, v *Vault, vault string) string {
			if _, err := v.WriteSession(proj, SessionMeta{Date: "2026-06-06"}, "body"); err != nil {
				t.Fatal(err)
			}
			return projectsRoot(vault)
		}},
		{"CreateTask", func(t *testing.T, v *Vault, vault string) string {
			if err := v.CreateTask(proj, TaskSpec{Slug: "t1", Title: "T1", Content: "body", Priority: "high"}); err != nil {
				t.Fatal(err)
			}
			return projectsRoot(vault)
		}},
		{"UpdateTaskStatus", func(t *testing.T, v *Vault, vault string) string {
			p, err := v.TaskFile(proj, "t1")
			if err != nil {
				t.Fatal(err)
			}
			writeFileDirect(t, p, []byte("# T1\n\n**Status:** pending\n"))
			// "completed" is no longer in the write set — a task reaches a
			// terminal state by moving (RetireTask), not by being stamped.
			if err := v.UpdateTaskStatus(proj, "t1", "in_progress"); err != nil {
				t.Fatal(err)
			}
			return projectsRoot(vault)
		}},
		{"RetireTask", func(t *testing.T, v *Vault, vault string) string {
			p, err := v.TaskFile(proj, "t1")
			if err != nil {
				t.Fatal(err)
			}
			writeFileDirect(t, p, []byte("# T1\n\n**Status:** pending\n"))
			if err := v.RetireTask(proj, "t1"); err != nil {
				t.Fatal(err)
			}
			return projectsRoot(vault)
		}},
		{"WriteVaultProjectConfig", func(t *testing.T, v *Vault, vault string) string {
			if _, _, err := v.WriteVaultProjectConfig(proj); err != nil {
				t.Fatal(err)
			}
			return projectsRoot(vault)
		}},
		{"WriteScoringConfig", func(t *testing.T, v *Vault, vault string) string {
			if err := v.WriteScoringConfig(proj, map[string]ScoringRoomOverride{
				room: {High: []string{"kw"}},
			}, 0); err != nil {
				t.Fatal(err)
			}
			return projectsRoot(vault)
		}},
		{"AppendDrawer", func(t *testing.T, v *Vault, vault string) string {
			if err := v.AppendDrawer(proj, wing, room, Drawer{Content: "hello"}); err != nil {
				t.Fatal(err)
			}
			return palaceRoot(vault)
		}},
		{"DeleteDrawer", func(t *testing.T, v *Vault, vault string) string {
			p, err := v.DrawerFile(proj, wing, room)
			if err != nil {
				t.Fatal(err)
			}
			id := DrawerID(wing, "hello")
			line, _ := json.Marshal(Drawer{ID: id, Content: "hello"})
			writeFileDirect(t, p, append(line, '\n'))
			if err := v.DeleteDrawer(proj, wing, room, id); err != nil {
				t.Fatal(err)
			}
			return palaceRoot(vault)
		}},
		{"AddEntity", func(t *testing.T, v *Vault, vault string) string {
			if err := v.AddEntity(proj, Entity{ID: "e1", Name: "n", Type: "concept"}); err != nil {
				t.Fatal(err)
			}
			return palaceRoot(vault)
		}},
		{"AddTriple", func(t *testing.T, v *Vault, vault string) string {
			if err := v.AddTriple(proj, Triple{Subject: "a", Predicate: "rel", Object: "b"}); err != nil {
				t.Fatal(err)
			}
			return palaceRoot(vault)
		}},
		{"InvalidateTriple", func(t *testing.T, v *Vault, vault string) string {
			p, err := v.KGTriplePath(proj, "a", "rel", "b")
			if err != nil {
				t.Fatal(err)
			}
			tr, _ := json.MarshalIndent(Triple{Subject: "a", Predicate: "rel", Object: "b"}, "", "  ")
			writeFileDirect(t, p, tr)
			if err := v.InvalidateTriple(proj, "a", "rel", "b", "2026-06-06"); err != nil {
				t.Fatal(err)
			}
			return palaceRoot(vault)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vault := t.TempDir()
			v := NewVault(vault)
			stampDir := tc.run(t, v, vault)
			assertStamped(t, stampDir)
		})
	}
}
