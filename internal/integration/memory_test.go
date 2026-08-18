// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/memory"
	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// TestIntegration_MemoryFeature proves the mcp-native-memory feature end to end
// across its full stack: the MCP tools (vp_memory_write/read/list/harvest), the
// storage layer (Projects/<slug>/memory + surface stamping), the one-way harvest
// engine (Claude native dir -> vault -> git commit), and bootstrap recall (the
// curated, body-less memory index returned by AssembleBootstrap).
func TestIntegration_MemoryFeature(t *testing.T) {
	// MCP write -> on-disk + surface stamp + bootstrap recall + read-back. This
	// path is host-agnostic (no Claude native dir, no git) — every agent gets it.
	t.Run("mcp_write_disk_bootstrap_read", func(t *testing.T) {
		h := newHarness(t, false) // mock embedder — memory recall doesn't need ONNX
		h.registerAllTools(t)

		const project = "mem-write-proj"
		const rel = "pref-tabs.md"
		body := "The user prefers tabs over spaces in all source files."

		h.callTool(t, "vp_memory_write", map[string]any{
			"project":     project,
			"rel":         rel,
			"name":        "Prefers tabs",
			"description": "Indentation preference.",
			"type":        "feedback",
			"body":        body,
		})

		// On disk under the canonical project memory dir.
		memFile := filepath.Join(h.Vault.Root, "Projects", project, "memory", rel)
		if _, err := os.Stat(memFile); err != nil {
			t.Fatalf("expected memory file at %s: %v", memFile, err)
		}
		// Surface-stamped: the atomic write touches Projects/<slug>/.surface.
		surf := filepath.Join(h.Vault.Root, "Projects", project, ".surface")
		if _, err := os.Stat(surf); err != nil {
			t.Fatalf("expected surface stamp at %s: %v", surf, err)
		}

		// Bootstrap recall surfaces the index entry (meta only) — generous budget.
		bs := tools.AssembleBootstrap(h.Resolver, h.Vault, project, "", "")
		var found bool
		for _, m := range bs.Memory {
			if m.Rel != rel {
				continue
			}
			found = true
			if m.Name != "Prefers tabs" || m.Type != "feedback" || m.Description != "Indentation preference." {
				t.Errorf("bootstrap memory meta mismatch: %+v", m)
			}
		}
		if !found {
			t.Fatalf("bootstrap recall missing %q; got %d memory entries", rel, len(bs.Memory))
		}

		// The recall index must NOT carry the body (index now, body on demand).
		blob, _ := json.Marshal(bs.Memory)
		if strings.Contains(string(blob), body) || strings.Contains(string(blob), "\"body\"") {
			t.Errorf("bootstrap memory index leaked a body: %s", blob)
		}

		// vp_memory_read round-trips the body.
		rres := h.callTool(t, "vp_memory_read", map[string]any{"project": project, "rel": rel})
		var rd struct {
			Meta struct {
				Name string `json:"name"`
				Type string `json:"type"`
				Rel  string `json:"rel"`
			} `json:"meta"`
			Body string `json:"body"`
		}
		if err := json.Unmarshal([]byte(rres), &rd); err != nil {
			t.Fatalf("parse read result: %v (raw: %.200s)", err, rres)
		}
		if rd.Meta.Rel != rel || rd.Meta.Type != "feedback" {
			t.Errorf("read meta = %+v", rd.Meta)
		}
		if !strings.Contains(rd.Body, body) {
			t.Errorf("read body = %q, want it to contain %q", rd.Body, body)
		}
	})

	// One-way harvest, full stack on the Claude path: a fake native dir resolved
	// from cwd (CLAUDE_HOME-encoded) drains into a git vault, the originals are
	// deleted, the routed files are committed, and bootstrap then recalls them.
	t.Run("harvest_full_stack_claude_path", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		h := newHarness(t, false)
		h.registerAllTools(t)
		root := h.Vault.Root

		// A real git vault so the harvest commit succeeds.
		run(t, root, "git", "init", "-q")
		run(t, root, "git", "config", "user.email", "test@example.com")
		run(t, root, "git", "config", "user.name", "Test")

		// Drive the tool's cwd-encoding resolution: point CLAUDE_HOME at a temp
		// dir and materialize the fixture at the dir NativeDirFromCwd resolves to.
		// This exercises the full MCP path (cwd -> encoded native dir) rather than
		// hand-injecting NativeDir, which the engine-level tests already cover.
		home := t.TempDir()
		t.Setenv("CLAUDE_HOME", home)
		cwd := t.TempDir()
		nativeDir, err := memory.NativeDirFromCwd(cwd)
		if err != nil {
			t.Fatalf("NativeDirFromCwd: %v", err)
		}
		if err := memorytestutil.WriteNativeMemoryFixture(nativeDir); err != nil {
			t.Fatalf("fixture: %v", err)
		}

		const project = "harvest-proj"

		hres := h.callTool(t, "vp_memory_harvest", map[string]any{
			"project": project,
			"cwd":     cwd,
			"push":    false, // local-only commit (no remotes in the test vault)
		})
		var res memory.Result
		if err := json.Unmarshal([]byte(hres), &res); err != nil {
			t.Fatalf("parse harvest result: %v (raw: %.200s)", err, hres)
		}
		if res.NativeDirMissing {
			t.Fatal("native dir should be present")
		}
		if len(res.Routed) != 3 {
			t.Errorf("expected 3 routed memories, got %v", res.Routed)
		}
		if !res.Committed {
			t.Errorf("harvest should have committed routed memory; res = %+v", res)
		}

		// The 3 typed memories now live in the vault.
		mems, err := h.Vault.ListMemories(project, 0)
		if err != nil {
			t.Fatalf("ListMemories: %v", err)
		}
		if len(mems) != 3 {
			t.Fatalf("expected 3 vault memories, got %d: %+v", len(mems), mems)
		}

		// The native dir is drained: index + typed files are gone.
		for _, f := range []string{"MEMORY.md", "pref-foo.md", "proj-bar.md", "pref-baz.md"} {
			if _, err := os.Stat(filepath.Join(nativeDir, f)); !os.IsNotExist(err) {
				t.Errorf("native %s should be drained (gone), stat err = %v", f, err)
			}
		}

		// The routed memory dir is committed → clean in the git tree.
		porcelain := gitPorcelain(t, root)
		if strings.Contains(porcelain, "Projects/"+project+"/memory") {
			t.Errorf("harvested memory should be committed (clean), still dirty:\n%s", porcelain)
		}

		// End to end: native -> vault -> bootstrap recall. The harvested names
		// appear in the recall index.
		bs := tools.AssembleBootstrap(h.Resolver, h.Vault, project, "", "")
		names := map[string]bool{}
		for _, m := range bs.Memory {
			names[m.Name] = true
		}
		for _, want := range []string{"Prefers tabs", "Project layout", "Editor of choice"} {
			if !names[want] {
				t.Errorf("bootstrap recall missing harvested memory %q; have %v", want, names)
			}
		}

		// Idempotency: a second harvest against the now-drained native dir routes
		// nothing and (memory already committed) makes no new commit. The vault is
		// unchanged.
		hres2 := h.callTool(t, "vp_memory_harvest", map[string]any{
			"project": project,
			"cwd":     cwd,
			"push":    false,
		})
		var res2 memory.Result
		if err := json.Unmarshal([]byte(hres2), &res2); err != nil {
			t.Fatalf("parse second harvest result: %v (raw: %.200s)", err, hres2)
		}
		if len(res2.Routed) != 0 {
			t.Errorf("re-harvest should route nothing, got %v", res2.Routed)
		}
		if res2.Committed {
			t.Errorf("re-harvest over committed memory must not commit again; res = %+v", res2)
		}
		if mems2, err := h.Vault.ListMemories(project, 0); err != nil || len(mems2) != 3 {
			t.Errorf("re-harvest changed the vault: len=%d err=%v", len(mems2), err)
		}
	})

	// Multi-agent (Grok/Zed) reality: no Claude native memory dir at all. Harvest
	// must report a clean skip (NativeDirMissing, no error) and leave any
	// directly-written memory intact and readable.
	t.Run("multi_agent_clean_skip", func(t *testing.T) {
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git not available")
		}
		h := newHarness(t, false)
		h.registerAllTools(t)
		root := h.Vault.Root

		run(t, root, "git", "init", "-q")
		run(t, root, "git", "config", "user.email", "test@example.com")
		run(t, root, "git", "config", "user.name", "Test")

		const project = "grok-proj"
		const rel = "note.md"
		body := "Grok session note: prefers terse commit messages."
		h.callTool(t, "vp_memory_write", map[string]any{
			"project": project,
			"rel":     rel,
			"name":    "Commit style",
			"type":    "project",
			"body":    body,
		})

		// No native dir exists (the Grok/Zed reality). Drive the engine directly
		// with a NativeDir that does not exist — the cleaner way to guarantee a
		// missing dir than fighting the cwd encoding.
		missing := filepath.Join(t.TempDir(), "no-such-host", "memory")
		res, err := memory.Harvest(memory.Options{
			VaultRoot: root,
			Project:   project,
			NativeDir: missing,
			Push:      false,
		})
		if err != nil {
			t.Fatalf("harvest over a missing native dir must not error: %v", err)
		}
		if !res.NativeDirMissing {
			t.Errorf("expected NativeDirMissing=true, got %+v", res)
		}
		if len(res.Routed) != 0 {
			t.Errorf("missing native dir must route nothing, got %v", res.Routed)
		}

		// The directly-written memory is untouched and still readable.
		rres := h.callTool(t, "vp_memory_read", map[string]any{"project": project, "rel": rel})
		var rd struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal([]byte(rres), &rd); err != nil {
			t.Fatalf("parse read result: %v (raw: %.200s)", err, rres)
		}
		if !strings.Contains(rd.Body, body) {
			t.Errorf("direct-write memory lost across a skip-harvest: %q", rd.Body)
		}
	})
}
