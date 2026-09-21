// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// vaultSubtree digests every path under root, EXCLUDING .surface: it stamps a
// timestamp and the writing binary's identity, so it differs between two runs
// by construction and would make any equality claim unsatisfiable.
//
// One helper, used for BOTH trees. Two helpers would let the comparison drift
// into comparing two different things and still pass.
func vaultSubtree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[filepath.ToSlash(rel)+"/"] = "<dir>"
			return nil
		}
		if d.Name() == ".surface" {
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("digest %s: %v", root, err)
	}
	return out
}

func subtreeDiff(want, got map[string]string) []string {
	var out []string
	for k, v := range got {
		if old, ok := want[k]; !ok {
			out = append(out, "unexpected: "+k)
		} else if old != v {
			out = append(out, "content differs: "+k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			out = append(out, "missing: "+k)
		}
	}
	sort.Strings(out)
	return out
}

// TestVpInit_CLIRepairsMCPInitializedProject is the load-bearing regression for
// this whole change, and it spans both surfaces on purpose.
//
// The failure it locks was a PAIR of decisions that were individually
// defensible and together made a project permanently unrepairable:
//
//  1. The MCP vp_init handler wrote a hand-rolled two-line .vibe-palace.toml,
//     mkdir'd tasks/{done,cancelled} with discarded errors, and returned
//     {"status":"initialized"} unconditionally. The vault side — then
//     Projects/<slug>/config.toml (since retired) and the commands/skills
//     scaffold — was never created.
//  2. `vp init` returned early the moment .vibe-palace.toml existed.
//
// So the marker written by (1) was the reason (2) would never repair it. An
// operator's only recovery was to delete the marker by hand — which is exactly
// what this test refuses to do at step (2).
//
// The assertion is tree EQUALITY against a CLI-only reference, not a list of
// files: a checklist passes as soon as the named paths exist, and the bug was
// never about the paths anyone thought to name.
func TestVpInit_CLIRepairsMCPInitializedProject(t *testing.T) {
	const slug = "repairme"

	var reference, repaired map[string]string

	// Each half gets its own initTestEnv, so each has its own global config
	// pointing at its own vault. Sharing one would make the second run resolve
	// the first run's vault and compare a tree with itself.
	t.Run("cli-only-reference", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg"))
		initTestEnv(t, false)
		vaultDir := filepath.Join(t.TempDir(), "vault")
		projDir := t.TempDir()
		markProjectDir(t, projDir)

		if code := cmdInit(cli.BuildInfo{Version: "test"}).Run(
			[]string{projDir, "--name", slug, "--vault-path", vaultDir, "--no-git"},
		); code != cli.ExitOK {
			t.Fatalf("reference init exit = %d", code)
		}
		reference = vaultSubtree(t, filepath.Join(vaultDir, "Projects", slug))
		if len(reference) == 0 {
			t.Fatal("reference vault subtree is empty; the test would prove nothing")
		}
	})

	t.Run("mcp-then-cli", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg"))
		initTestEnv(t, false)
		vaultDir := filepath.Join(t.TempDir(), "vault")
		if err := os.MkdirAll(vaultDir, 0o755); err != nil {
			t.Fatal(err)
		}
		projDir := t.TempDir()
		markProjectDir(t, projDir)

		// (1) The MCP surface goes first, against the same vault.
		tool := tools.InitProjectTool(storage.NewVault(vaultDir))
		params, _ := json.Marshal(map[string]any{"path": projDir, "name": slug})
		if _, err := tool.Handler(context.Background(), params); err != nil {
			t.Fatalf("vp_init handler: %v", err)
		}

		// (2) The CLI runs over the SAME tree, and nothing is deleted first.
		if _, err := os.Stat(filepath.Join(projDir, ".vibe-palace.toml")); err != nil {
			t.Fatalf("the MCP call left no marker, so the repair premise is gone: %v", err)
		}
		out := captureStdout(t, func() {
			if code := cmdInit(cli.BuildInfo{Version: "test"}).Run(
				[]string{projDir, "--name", slug, "--vault-path", vaultDir, "--no-git"},
			); code != cli.ExitOK {
				t.Fatalf("repair init exit = %d\n%s", code, "")
			}
		})
		if strings.Contains(out, "[FAIL]") {
			t.Errorf("the repairing `vp init` reported a failure row:\n%s", out)
		}

		repaired = vaultSubtree(t, filepath.Join(vaultDir, "Projects", slug))
	})

	if d := subtreeDiff(reference, repaired); len(d) > 0 {
		t.Errorf("an MCP-initialized project that `vp init` then repaired does not match a CLI-only project:\n  %s",
			strings.Join(d, "\n  "))
	}
}
