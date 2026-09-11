// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIntegrationCheckReachesRealHostRegistry is the binary-level half of the
// cmd/vp check tests' host seam. Those tests stub mcpHostRegistry, so nothing
// in cmd/vp proves the built `vp check` still asks the REAL registry; this
// does, by running the built binary with a scripted `grok` first on PATH and
// requiring the report's "MCP host: grok" row to come from it.
//
// The child environment is explicit — never os.Environ(): HOME, the config,
// cache and data dirs are empty temp dirs; HTTPS_PROXY/HTTP_PROXY point at a
// dead loopback port; PATH is a dir holding only the scripted grok, followed by
// the parent PATH (so the real Grok CLI, if installed, is shadowed and never
// runs). With no global config the Embedder row is skipped, so the model is
// never reached. `vp check --json` exits 1 without a config, which is why this
// runs the binary directly rather than through runVP, which fails the test on
// any non-zero exit.
//
// The Zed row depends on whether the parent PATH holds a `zed`, so it is not
// asserted.
func TestIntegrationCheckReachesRealHostRegistry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the scripted grok is an sh script")
	}
	bin := buildVPBinary(t)

	root := t.TempDir()
	shim := filepath.Join(root, "shim")
	logPath := filepath.Join(root, "grok.log")
	dirs := map[string]string{}
	for _, d := range []string{"home", "cfg", "cache", "data", "cwd"} {
		dirs[d] = filepath.Join(root, d)
		if err := os.MkdirAll(dirs[d], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(shim, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		`echo "$*" >> "$VP_GROK_LOG"` + "\n" +
		`case "$*" in "mcp list") echo "vibe-palace  vp mcp";; *) exit 1;; esac` + "\n"
	if err := os.WriteFile(filepath.Join(shim, "grok"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "check", "--json")
	cmd.Dir = dirs["cwd"]
	cmd.Env = []string{
		"PATH=" + shim + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + dirs["home"],
		"USERPROFILE=" + dirs["home"],
		"XDG_CONFIG_HOME=" + dirs["cfg"],
		"APPDATA=" + dirs["cfg"],
		"XDG_CACHE_HOME=" + dirs["cache"],
		"XDG_DATA_HOME=" + dirs["data"],
		"HTTPS_PROXY=http://127.0.0.1:1",
		"HTTP_PROXY=http://127.0.0.1:1",
		"NO_PROXY=",
		"VP_GROK_LOG=" + logPath,
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run vp check --json: %v", err)
		}
	}

	var rep struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			Detail string `json:"detail"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out, &rep); err != nil {
		t.Fatalf("vp check --json is not valid JSON: %v\n%s", err, out)
	}
	var grok string
	for _, c := range rep.Checks {
		if c.Name == "MCP host: grok" {
			grok = c.Status
		}
	}
	if grok != "pass" {
		t.Errorf("MCP host: grok status = %q, want pass from the scripted grok — is `vp check` still asking mcphost.Registry()?\n%s", grok, out)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if string(raw) != "mcp list\n" {
		t.Errorf("scripted grok argv log = %q, want exactly \"mcp list\\n\"", raw)
	}
}
