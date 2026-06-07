// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/tailscale/hujson"
)

// zedServerName is the context_servers key under which the vibe-palace MCP
// server is registered in Zed's settings.json.
const zedServerName = "vibe-palace"

// ZedHost registers the vibe-palace MCP server with the Zed editor by adding a
// context_servers entry to settings.json. Zed's settings file is JWCC (JSON
// With Commas and Comments), and operators heavily annotate it, so the edit is
// performed surgically via tailscale/hujson: only the one context_servers entry
// is added/updated and every comment, trailing comma, and formatting choice
// elsewhere in the file is preserved byte-for-byte.
//
// The entry intentionally omits an env block. Zed launches MCP servers with its
// own inherited environment, and its handling of "${VAR}" placeholders is
// unverified — so we rely on inheritance (matching a working hand-written
// entry) rather than risk baking literal "${XAI_API_KEY}" strings.
type ZedHost struct {
	// settingsPath is the absolute path to Zed's settings.json. Injectable for
	// tests; NewZedHost resolves the platform default.
	settingsPath string
	// lookPath reports whether `zed` is resolvable; nil uses exec.LookPath.
	lookPath func() (string, error)
}

// NewZedHost returns a ZedHost pointed at the platform-default settings.json
// ($XDG_CONFIG_HOME/zed/settings.json, falling back to ~/.config/zed/settings.json).
func NewZedHost() *ZedHost {
	return &ZedHost{
		settingsPath: defaultZedSettingsPath(),
		lookPath:     func() (string, error) { return exec.LookPath("zed") },
	}
}

func defaultZedSettingsPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "zed", "settings.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "zed", "settings.json")
}

func (*ZedHost) Name() string { return "zed" }
func (*ZedHost) Flag() string { return "--zed" }

// Detected reports whether Zed is present: its config directory exists, or
// `zed` is on PATH.
func (h *ZedHost) Detected() bool {
	if info, err := os.Stat(filepath.Dir(h.settingsPath)); err == nil && info.IsDir() {
		return true
	}
	if h.lookPath != nil {
		if _, err := h.lookPath(); err == nil {
			return true
		}
	}
	return false
}

// Installed reports whether a context_servers.vibe-palace entry exists.
func (h *ZedHost) Installed() (bool, error) {
	val, err := h.parse()
	if err != nil {
		return false, err
	}
	if val == nil {
		return false, nil
	}
	return val.Find("/context_servers/"+zedServerName) != nil, nil
}

// Install adds (or updates) the context_servers.vibe-palace entry and ensures
// AGENTS.md carries the managed block. Idempotent: when the entry already
// matches the desired shape, the file is left untouched.
func (h *ZedHost) Install(_, projectRoot string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}

	val, err := h.parse()
	if err != nil {
		return fmt.Errorf("parse %s: %w", compressHome(h.settingsPath), err)
	}
	if val == nil {
		v, _ := hujson.Parse([]byte("{}\n"))
		val = &v
	}

	entry, err := json.Marshal(zedEntry())
	if err != nil {
		return err
	}

	if cur := val.Find("/context_servers/" + zedServerName); cur != nil {
		same, err := sameJSON(cur, entry)
		if err != nil {
			return err
		}
		if same {
			fmt.Fprintf(out, "vibe-palace already configured in %s\n", compressHome(h.settingsPath))
			if _, werr := ensureAgentsFile(projectRoot); werr != nil {
				fmt.Fprintf(out, "  warning: wire AGENTS.md: %v\n", werr)
			}
			return nil
		}
	}

	var patch []byte
	if val.Find("/context_servers") == nil {
		patch = []byte(`[{"op":"add","path":"/context_servers","value":{"` + zedServerName + `":` + string(entry) + `}}]`)
	} else {
		patch = []byte(`[{"op":"add","path":"/context_servers/` + zedServerName + `","value":` + string(entry) + `}]`)
	}
	if err := val.Patch(patch); err != nil {
		return fmt.Errorf("patch context_servers: %w", err)
	}
	// Format normalizes the whole file to hujson's canonical style. This is a
	// deliberate operator choice (favoring a consistently tidy settings.json
	// over a minimal one-line diff): JSON Patch inserts the new value compactly,
	// and without Format it would be jammed onto the prior line. Format preserves
	// every comment, so annotations survive the reflow; the pre-edit file is kept
	// at .vp.bak. Do not drop this to chase a smaller diff without revisiting
	// that decision.
	val.Format()

	if err := backupFile(h.settingsPath); err != nil {
		return fmt.Errorf("backup %s: %w", compressHome(h.settingsPath), err)
	}
	if err := h.write(val.Pack()); err != nil {
		return err
	}

	if _, err := ensureAgentsFile(projectRoot); err != nil {
		fmt.Fprintf(out, "  warning: wire AGENTS.md: %v\n", err)
	}

	fmt.Fprintf(out, "vibe-palace registered with Zed:\n")
	fmt.Fprintf(out, "  Settings: %s\n", compressHome(h.settingsPath))
	fmt.Fprintf(out, "Restart Zed (or reload settings) to activate.\n")
	return nil
}

// Uninstall removes the context_servers.vibe-palace entry. Idempotent.
func (h *ZedHost) Uninstall(out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	val, err := h.parse()
	if err != nil {
		return fmt.Errorf("parse %s: %w", compressHome(h.settingsPath), err)
	}
	if val == nil || val.Find("/context_servers/"+zedServerName) == nil {
		fmt.Fprintf(out, "vibe-palace not configured in %s\n", compressHome(h.settingsPath))
		return nil
	}

	if err := val.Patch([]byte(`[{"op":"remove","path":"/context_servers/` + zedServerName + `"}]`)); err != nil {
		return fmt.Errorf("patch context_servers: %w", err)
	}
	val.Format()
	if err := backupFile(h.settingsPath); err != nil {
		return fmt.Errorf("backup %s: %w", compressHome(h.settingsPath), err)
	}
	if err := h.write(val.Pack()); err != nil {
		return err
	}
	fmt.Fprintf(out, "vibe-palace removed from Zed (%s).\n", compressHome(h.settingsPath))
	return nil
}

// zedEntry is the context_servers value for vibe-palace. No env block — see the
// ZedHost doc comment.
func zedEntry() map[string]any {
	return map[string]any{
		"command": "vp",
		"args":    []any{"mcp"},
	}
}

// parse reads and parses settings.json. Returns (nil, nil) when the file is
// absent or blank — callers treat that as "start from {}".
func (h *ZedHost) parse() (*hujson.Value, error) {
	data, err := os.ReadFile(h.settingsPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	val, err := hujson.Parse(data)
	if err != nil {
		return nil, err
	}
	return &val, nil
}

func (h *ZedHost) write(data []byte) error {
	if err := os.MkdirAll(filepath.Dir(h.settingsPath), 0o755); err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	return os.WriteFile(h.settingsPath, data, 0o644)
}

// sameJSON reports whether a hujson value and a standard-JSON blob are
// semantically equal, normalizing key order and whitespace via a round-trip
// through encoding/json so formatting differences do not force a rewrite.
func sameJSON(cur *hujson.Value, want []byte) (bool, error) {
	std := cur.Clone()
	std.Standardize()
	a, err := canonicalJSON(std.Pack())
	if err != nil {
		return false, err
	}
	b, err := canonicalJSON(want)
	if err != nil {
		return false, err
	}
	return bytes.Equal(a, b), nil
}

func canonicalJSON(b []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}
