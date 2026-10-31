// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

func TestIntegrationCodexCLIListsEnabledVibePalaceAndTools(t *testing.T) {
	env := testinfra.IsolateEnv(t)
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("Codex CLI is not installed")
	}
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.WriteGlobalConfig(vault, true); err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(root, "codex-home")
	if err := os.MkdirAll(codexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := testinfra.BuildVPBinary(t)
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(bin, "mcp", "install", "--codex")
	install.Dir = project
	install.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	if output, err := install.CombinedOutput(); err != nil {
		t.Fatalf("vp mcp install --codex: %v\n%s", err, output)
	}
	cli := exec.Command(codex, "mcp", "list")
	cli.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	output, err := cli.CombinedOutput()
	if err != nil {
		t.Fatalf("codex mcp list: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "vibe-palace") {
		t.Fatalf("Codex did not list enabled MCP server:\n%s", output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, bin, "mcp")
	server.Env = append(os.Environ(), "CODEX_HOME="+codexHome, "XDG_CONFIG_HOME="+env.XDGConfigHome)
	stdin, err := server.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	server.Stderr = &stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = server.Process.Kill()
		_ = server.Wait()
	}()
	if _, err := stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-03-26\",\"capabilities\":{},\"clientInfo\":{\"name\":\"codex-cli\",\"version\":\"integration\"}}}\n")); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	response := readJSONLine(t, reader)
	if !bytes.Contains(response, []byte(`"result"`)) {
		t.Fatalf("MCP initialize failed: %s\nserver stderr:\n%s", response, stderr.String())
	}
	if _, err := stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n")); err != nil {
		t.Fatal(err)
	}
	response = readJSONLine(t, reader)
	var result struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &result); err != nil {
		t.Fatalf("decode tools/list response %q: %v", response, err)
	}
	foundBootstrap := false
	for _, tool := range result.Result.Tools {
		if tool.Name == "vp_bootstrap_context" {
			foundBootstrap = true
			break
		}
	}
	if !foundBootstrap {
		t.Fatalf("Codex MCP process did not expose vp_bootstrap_context: %s", response)
	}

	disable := exec.Command(bin, "mcp", "uninstall", "--codex")
	disable.Dir = project
	disable.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	if output, err := disable.CombinedOutput(); err != nil {
		t.Fatalf("vp mcp uninstall --codex: %v\n%s", err, output)
	}
	get := exec.Command(codex, "mcp", "get", "--json", "vibe-palace")
	get.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	output, err = get.CombinedOutput()
	if err != nil {
		t.Fatalf("codex mcp get --json vibe-palace: %v\n%s", err, output)
	}
	var state struct {
		Enabled bool `json:"enabled"`
	}
	jsonStart := bytes.IndexByte(output, '{')
	if jsonStart < 0 {
		t.Fatalf("Codex MCP state has no JSON object: %q", output)
	}
	if err := json.Unmarshal(output[jsonStart:], &state); err != nil {
		t.Fatalf("decode Codex MCP state %q: %v", output, err)
	}
	if state.Enabled {
		t.Fatalf("Codex reports disabled server as enabled: %s", output)
	}
}

func readJSONLine(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	line, err := r.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read MCP response: %v", err)
	}
	return bytes.TrimSpace(line)
}
