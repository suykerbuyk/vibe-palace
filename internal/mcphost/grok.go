// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/plugin"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// grokServerName is the MCP server name registered in Grok's config.toml.
const grokServerName = "vibe-palace"

// GrokHost registers the vibe-palace MCP server with xAI's Grok Build CLI by
// shelling out to the official `grok mcp` command. We never model Grok's
// config.toml schema ourselves: Grok owns it, the CLI is the documented stable
// interface, and shelling out keeps vp decoupled from Grok's internal config
// format changes over time (the lowest-coupling registration kind in the
// registry).
//
// The runner and lookPath fields are injectable so tests can assert the exact
// argv without a real `grok` binary.
type GrokHost struct {
	// runner executes a `grok` subcommand and returns its combined output.
	runner func(args ...string) ([]byte, error)
	// lookPath reports whether `grok` is resolvable (returns its path, or error).
	lookPath func() (string, error)
	// homeDir resolves the user home (for ~/.grok detection); nil uses os.
	homeDir func() (string, error)
}

// NewGrokHost returns a GrokHost wired to the real `grok` CLI.
func NewGrokHost() *GrokHost {
	return &GrokHost{
		runner: func(args ...string) ([]byte, error) {
			return exec.Command("grok", args...).CombinedOutput()
		},
		lookPath: func() (string, error) { return exec.LookPath("grok") },
		homeDir:  os.UserHomeDir,
	}
}

func (*GrokHost) Name() string { return "grok" }
func (*GrokHost) Flag() string { return "--grok" }

// Detected reports whether Grok Build is present: `grok` on PATH, or a ~/.grok/
// directory (the CLI's data dir, present after first run even if not on PATH).
func (h *GrokHost) Detected() bool {
	if _, err := h.lookPath(); err == nil {
		return true
	}
	if home, err := h.home(); err == nil {
		if info, err := os.Stat(filepath.Join(home, ".grok")); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// Installed reports whether vibe-palace appears in `grok mcp list`. Requires a
// runnable `grok`; returns (false, nil) when the CLI is absent so status
// reporting degrades to "not installed" rather than erroring.
func (h *GrokHost) Installed() (bool, error) {
	if _, err := h.lookPath(); err != nil {
		return false, nil
	}
	out, err := h.runner("mcp", "list")
	if err != nil {
		// `grok mcp list` with nothing configured exits non-zero on some
		// builds; treat any list failure as "not installed" rather than fatal.
		return false, nil
	}
	return strings.Contains(string(out), grokServerName), nil
}

// Install registers the server via `grok mcp add` (idempotent — add is
// add-or-update) and ensures AGENTS.md carries the managed block.
//
// Env values are passed as the literal string "${KEY}" (exec.Command performs
// no shell expansion), mirroring the Claude .mcp.json convention of deferring
// expansion to the host at server-spawn time so the operator's live key is used
// and no secret is baked into config.toml.
func (h *GrokHost) Install(_, projectRoot string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if _, err := h.lookPath(); err != nil {
		return fmt.Errorf("`grok` not found on PATH — install Grok Build (https://x.ai) or run `grok` once, then retry")
	}

	args := []string{"mcp", "add", grokServerName, "--command", "vp", "--args", "mcp"}
	for _, key := range plugin.MCPEnvPassthroughKeys() {
		args = append(args, "--env", key+"=${"+key+"}")
	}
	if cmdOut, err := h.runner(args...); err != nil {
		return fmt.Errorf("grok mcp add: %w\n%s", err, strings.TrimSpace(string(cmdOut)))
	}

	if _, err := ensureAgentsFile(projectRoot); err != nil {
		fmt.Fprintf(out, "  warning: wire AGENTS.md: %v\n", err)
	}

	// Phase 3: user-global plugin command/skill shims under ~/.grok/plugins/vibe-palace.
	emitGrokUserSurfaces(out, projectRoot)

	fmt.Fprintf(out, "vibe-palace registered with Grok (grok mcp add %s).\n", grokServerName)
	fmt.Fprintf(out, "Verify with: grok mcp doctor\n")
	return nil
}

// emitGrokUserSurfaces writes thin shims into the user-global Grok plugin tree
// (auto-trusted). Best-effort — registration still succeeds if shim write fails.
// Vault root is global config only (M1).
func emitGrokUserSurfaces(out io.Writer, _ string) {
	pluginRoot := shims.GrokUserPluginDir()
	if pluginRoot == "" {
		fmt.Fprintf(out, "  warning: host surfaces: cannot resolve home for Grok user plugin\n")
		return
	}
	vaultRoot, vaultSrc, _ := storage.ResolveGlobalVaultPath()
	if vaultSrc != "" {
		fmt.Fprintf(out, "  Vault for menu: %s (%s)\n", compressHome(vaultRoot), vaultSrc)
	}
	rep := shims.InstallGlobalSurfaces(shims.GlobalInstallOptions{
		VaultRoot:         vaultRoot,
		VaultSource:       vaultSrc,
		GrokPluginRoot:    pluginRoot,
		AllowStaleRemoval: true,
	})
	for _, e := range rep.Errors {
		fmt.Fprintf(out, "  warning: host surfaces: %s\n", e)
	}
	wrote := rep.GrokCommands.Added+rep.GrokCommands.Updated+rep.GrokCommands.Unchanged+
		rep.GrokSkills.Added+rep.GrokSkills.Updated+rep.GrokSkills.Unchanged+
		rep.GrokHub.Added+rep.GrokHub.Updated+rep.GrokHub.Unchanged > 0
	if len(rep.Errors) > 0 && !wrote {
		return
	}
	fmt.Fprintf(out, "  User plugin:  %s\n", compressHome(pluginRoot))
	fmt.Fprintf(out, "  %s\n", shims.FormatApplyCounts("Command shims", rep.GrokCommands))
	fmt.Fprintf(out, "  %s\n", shims.FormatApplyCounts("Skill shims", rep.GrokSkills))
	fmt.Fprintf(out, "  %s\n", shims.FormatApplyCounts("Hub", rep.GrokHub))
	if n := rep.RemovedTotal(); n > 0 {
		fmt.Fprintf(out, "  warning: removed %d stale managed shim file(s)\n", n)
	}
}

// Uninstall removes the server via `grok mcp remove` and deletes the
// user-global plugin tree (H3). Idempotent.
func (h *GrokHost) Uninstall(out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if _, err := h.lookPath(); err != nil {
		fmt.Fprintf(out, "`grok` not found on PATH — nothing to remove.\n")
		// Still try to clean user plugin tree if HOME is set.
	} else if cmdOut, err := h.runner("mcp", "remove", grokServerName); err != nil {
		fmt.Fprintf(out, "grok mcp remove %s: %s\n", grokServerName, strings.TrimSpace(string(cmdOut)))
	} else {
		fmt.Fprintf(out, "vibe-palace removed from Grok MCP config.\n")
	}
	if err := shims.RemoveGrokUserPlugin(); err != nil {
		fmt.Fprintf(out, "  warning: remove user plugin tree: %v\n", err)
	} else if dir := shims.GrokUserPluginDir(); dir != "" {
		fmt.Fprintf(out, "  User plugin tree removed: %s\n", compressHome(dir))
	}
	return nil
}

func (h *GrokHost) home() (string, error) {
	if h.homeDir != nil {
		return h.homeDir()
	}
	return os.UserHomeDir()
}
