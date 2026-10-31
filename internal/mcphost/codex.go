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

	"github.com/BurntSushi/toml"
	"github.com/suykerbuyk/vibe-palace/internal/codexhook"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const codexBinary = "codex"

// CodexHost registers the MCP server in Codex CLI/IDE's shared config.toml.
type CodexHost struct {
	configPath string
	homeDir    func() (string, error)
	lookPath   func() (string, error)
	runner     func(args ...string) ([]byte, error)
}

func NewCodexHost() *CodexHost {
	h := &CodexHost{configPath: defaultCodexConfigPath(), homeDir: os.UserHomeDir}
	h.lookPath = func() (string, error) { return exec.LookPath(h.Executables()[0]) }
	h.runner = func(args ...string) ([]byte, error) {
		return exec.Command(h.Executables()[0], args...).CombinedOutput()
	}
	return h
}

func defaultCodexConfigPath() string {
	if root := os.Getenv("CODEX_HOME"); root != "" {
		return filepath.Join(root, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml")
}

func (*CodexHost) Name() string          { return "codex" }
func (*CodexHost) Flag() string          { return "--codex" }
func (*CodexHost) Executables() []string { return []string{codexBinary} }

func (h *CodexHost) Detected() bool {
	if _, err := os.Stat(filepath.Dir(h.configPath)); err == nil {
		return true
	}
	if h.lookPath != nil {
		_, err := h.lookPath()
		return err == nil
	}
	return false
}

func (h *CodexHost) Installed() (bool, error) {
	raw, err := codexConfigRead(h.configPath)
	if err != nil || raw == nil {
		return false, err
	}
	server, err := readCodexServer(raw)
	if err != nil || server == nil {
		return false, err
	}
	if server.Enabled != nil && !*server.Enabled {
		return false, nil
	}
	return h.ownsCommand(server.Command), nil
}

func (h *CodexHost) Install(_ string, projectRoot string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	raw, err := codexConfigRead(h.configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", compressHome(h.configPath), err)
	}
	server, err := readCodexServer(raw)
	if err != nil {
		return fmt.Errorf("read Codex MCP config: %w", err)
	}
	if server != nil && !h.ownsCommand(server.Command) {
		return fmt.Errorf("refusing to overwrite foreign Codex MCP server %q command %q", codexServerName, server.Command)
	}
	enabled := true
	patched, err := patchCodexServerTable(raw, codexServerConfig{Command: "vp", Args: []string{"mcp"}, Enabled: &enabled})
	if err != nil {
		return fmt.Errorf("edit %s: %w", compressHome(h.configPath), err)
	}
	if string(patched) != string(raw) {
		if err := backupFile(h.configPath); err != nil {
			return fmt.Errorf("backup %s: %w", compressHome(h.configPath), err)
		}
		if err := writeCodexConfig(h.configPath, patched); err != nil {
			return err
		}
	}
	if _, err := ensureAgentsFile(projectRoot); err != nil {
		fmt.Fprintf(out, "warning: wire AGENTS.md: %v\n", err)
	}
	h.installUserSkills(out)
	h.installCodexHooks(projectRoot, out)
	fmt.Fprintf(out, "vibe-palace registered with Codex in %s; restart Codex to activate.\n", compressHome(h.configPath))
	return nil
}

func (h *CodexHost) Uninstall(out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	raw, err := codexConfigRead(h.configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", compressHome(h.configPath), err)
	}
	server, err := readCodexServer(raw)
	if err != nil {
		return fmt.Errorf("read Codex MCP config: %w", err)
	}
	if server == nil {
		fmt.Fprintf(out, "vibe-palace is not configured in %s.\n", compressHome(h.configPath))
		return h.removeHostArtifacts(out)
	}
	if !h.ownsCommand(server.Command) {
		return fmt.Errorf("refusing to disable foreign Codex MCP server %q command %q", codexServerName, server.Command)
	}
	enabled := false
	patched, err := patchCodexServerTable(raw, codexServerConfig{Command: server.Command, Args: server.Args, Enabled: &enabled})
	if err != nil {
		return fmt.Errorf("edit %s: %w", compressHome(h.configPath), err)
	}
	if string(patched) != string(raw) {
		if err := backupFile(h.configPath); err != nil {
			return fmt.Errorf("backup %s: %w", compressHome(h.configPath), err)
		}
		if err := writeCodexConfig(h.configPath, patched); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "vibe-palace disabled in Codex config %s; approval policy retained.\n", compressHome(h.configPath))
	return h.removeHostArtifacts(out)
}

func (h *CodexHost) installUserSkills(out io.Writer) {
	home, err := h.userHome()
	if err != nil {
		fmt.Fprintf(out, "warning: install Codex user skills: %v\n", err)
		return
	}
	resolver := vpctx.NewResolver("")
	if vault, err := storage.OpenVaultGlobal(); err == nil {
		resolver = vpctx.NewResolver(vault.Root)
	}
	if _, err := shims.InstallCodexUserSkills(resolver, filepath.Join(home, ".agents", "skills")); err != nil {
		fmt.Fprintf(out, "warning: install Codex user skills: %v\n", err)
	}
}

func (h *CodexHost) removeUserSkills(out io.Writer) error {
	home, err := h.userHome()
	if err != nil {
		fmt.Fprintf(out, "warning: remove Codex user skills: %v\n", err)
		return nil
	}
	if err := shims.RemoveCodexUserSkills(filepath.Join(home, ".agents", "skills")); err != nil {
		return fmt.Errorf("remove Codex user skills: %w", err)
	}
	return nil
}

func (h *CodexHost) installCodexHooks(projectRoot string, out io.Writer) {
	if disabled, unknown := codexHooksDisabled(h.configPath, filepath.Join(projectRoot, ".codex", "config.toml")); disabled {
		fmt.Fprintln(out, "Codex hooks disabled by features.hooks=false; no hook was activated.")
		return
	} else if unknown {
		fmt.Fprintln(out, "Codex hook status unknown at a higher configuration layer; install only the Vibe-Palace user hook.")
	}
	if h.lookPath == nil {
		fmt.Fprintln(out, "warning: Codex hooks not installed: vp executable could not be resolved")
		return
	}
	vpPath, err := h.lookPath()
	if err != nil {
		fmt.Fprintf(out, "warning: Codex hooks not installed: resolve vp executable: %v\n", err)
		return
	}
	if !filepath.IsAbs(vpPath) {
		vpPath, err = filepath.Abs(vpPath)
		if err != nil {
			fmt.Fprintf(out, "warning: Codex hooks not installed: resolve vp path: %v\n", err)
			return
		}
	}
	if err := codexhook.InstallSessionStartHook(filepath.Join(filepath.Dir(h.configPath), "hooks.json"), vpPath); err != nil {
		fmt.Fprintf(out, "warning: install Codex SessionStart hook: %v\n", err)
		return
	}
	if err := codexhook.InstallSessionEndHook(filepath.Join(filepath.Dir(h.configPath), "hooks.json"), vpPath); err != nil {
		fmt.Fprintf(out, "warning: install Codex SessionEnd hook: %v\n", err)
		return
	}
	fmt.Fprintln(out, "Codex SessionStart and SessionEnd hooks configured; review/trust them in Codex before relying on bootstrap or archival.")
}

func (h *CodexHost) removeHostArtifacts(out io.Writer) error {
	if err := h.removeUserSkills(out); err != nil {
		return err
	}
	if err := codexhook.RemoveSessionStartHook(filepath.Join(filepath.Dir(h.configPath), "hooks.json")); err != nil {
		return fmt.Errorf("remove Codex SessionStart hook: %w", err)
	}
	if err := codexhook.RemoveSessionEndHook(filepath.Join(filepath.Dir(h.configPath), "hooks.json")); err != nil {
		return fmt.Errorf("remove Codex SessionEnd hook: %w", err)
	}
	return nil
}

func codexHooksDisabled(configPaths ...string) (disabled, unknown bool) {
	for _, path := range configPaths {
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			unknown = true
			continue
		}
		var doc map[string]any
		if _, err := toml.Decode(string(raw), &doc); err != nil {
			unknown = true
			continue
		}
		features, _ := doc["features"].(map[string]any)
		if enabled, ok := features["hooks"].(bool); ok && !enabled {
			return true, unknown
		}
	}
	return false, unknown
}

func (h *CodexHost) userHome() (string, error) {
	if h.homeDir == nil {
		return "", fmt.Errorf("home directory lookup is unavailable")
	}
	return h.homeDir()
}

func (h *CodexHost) ownsCommand(command string) bool {
	if command == "vp" {
		return true
	}
	if command == "" || h.lookPath == nil {
		return false
	}
	want, err := h.lookPath()
	if err != nil {
		return false
	}
	gotAbs, gotErr := filepath.Abs(command)
	wantAbs, wantErr := filepath.Abs(want)
	if gotErr != nil || wantErr != nil {
		return false
	}
	gotResolved, gotErr := filepath.EvalSymlinks(gotAbs)
	wantResolved, wantErr := filepath.EvalSymlinks(wantAbs)
	return gotErr == nil && wantErr == nil && gotResolved == wantResolved
}

func readCodexServer(raw []byte) (*codexServerConfig, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var doc map[string]any
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return nil, err
	}
	servers, _ := doc["mcp_servers"].(map[string]any)
	values, _ := servers[codexServerName].(map[string]any)
	if values == nil {
		return nil, nil
	}
	server := &codexServerConfig{}
	server.Command, _ = values["command"].(string)
	switch args := values["args"].(type) {
	case []string:
		server.Args = args
	case []any:
		for _, arg := range args {
			if value, ok := arg.(string); ok {
				server.Args = append(server.Args, value)
			}
		}
	}
	if enabled, ok := values["enabled"].(bool); ok {
		server.Enabled = &enabled
	}
	return server, nil
}

func writeCodexConfig(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
