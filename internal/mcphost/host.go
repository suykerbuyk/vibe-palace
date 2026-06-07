// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package mcphost is the cross-host registry for registering the vibe-palace
// MCP server with the AI coding hosts that consume it. The MCP server itself
// (`vp mcp`, JSON-RPC over stdio) is standardized and host-agnostic — every
// host runs the identical server. What differs per host is the *registration*:
// where the "run vp mcp" pointer is written and in what shape. The registry
// captures exactly that difference and nothing else.
//
// Three registration kinds are deliberately represented so the abstraction is
// proven to span them, rather than collapsing into a Claude-shaped interface
// with bolt-ons:
//
//   - Claude  — a generated local marketplace + ~/.claude/settings.json (a
//     workaround for a Claude Code bug where bare mcpServers registration did
//     not persist). Delegated wholesale to internal/plugin; see ClaudeHost.
//   - Grok    — the official `grok mcp add` CLI owns Grok's TOML schema, so we
//     shell out and never model it ourselves (lowest coupling). See GrokHost.
//   - Zed     — a JSON-with-comments settings.json edited surgically via
//     tailscale/hujson so the operator's comments survive. See ZedHost.
//
// Neovim, Cursor, and OpenCode remain documented-manual for now; the Host
// interface is shaped to absorb them later without restructuring.
package mcphost

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

// Host is one AI coding host that can have the vibe-palace MCP server
// registered with it. Implementations span file-writer (Zed), command-driven
// (Grok), and marketplace (Claude) registration kinds.
type Host interface {
	// Name is the stable lowercase identifier ("claude", "grok", "zed").
	Name() string
	// Flag is the `vp mcp install` selector flag ("--claude-plugin", "--grok",
	// "--zed").
	Flag() string
	// Detected reports whether this host is present on the machine (config dir
	// or CLI). Registration and status reporting are gated on it.
	Detected() bool
	// Installed reports whether the vibe-palace MCP entry is already registered
	// with this host. Returns (false, nil) when the host is absent.
	Installed() (bool, error)
	// Install registers the vibe-palace MCP server with this host, idempotently,
	// backing up any file it edits. projectRoot is used to ensure AGENTS.md
	// carries the managed behavioral block (the cross-host baseline); hosts that
	// manage behavior another way (Claude) may ignore it. Progress is written to
	// out (nil discards).
	Install(version, projectRoot string, out io.Writer) error
	// Uninstall reverses Install. Idempotent: a no-op when nothing is registered.
	Uninstall(out io.Writer) error
}

// Registry returns the canonical host list in install-precedence order. cmd/vp
// dispatches `vp mcp install`/`uninstall` flags against it, and internal/check
// iterates it for status rows.
func Registry() []Host {
	return []Host{
		ClaudeHost{},
		NewGrokHost(),
		NewZedHost(),
	}
}

// --- shared helpers ---

// ensureAgentsFile guarantees projectRoot/AGENTS.md exists and carries the
// managed vibe-palace block. AGENTS.md is the behavioral baseline every
// non-Claude host reads (it teaches vp_bootstrap_context + the vpc-*/vps-*
// triggers). agentfile.Wire requires the file to already exist, so we create an
// empty one first when missing. Targeting AGENTS.md specifically (rather than
// agentfile.WireAll) avoids disturbing a CLAUDE.md the operator manages.
func ensureAgentsFile(projectRoot string) (agentfile.Result, error) {
	return agentfile.EnsureManaged(filepath.Join(projectRoot, "AGENTS.md"), "AGENTS.md")
}

// backupFile copies path to path+".vp.bak" before mutation. No-op when the file
// does not yet exist.
func backupFile(path string) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(path+".vp.bak", raw, 0o644)
}

// compressHome replaces the home-directory prefix of p with "~" for display.
func compressHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}
