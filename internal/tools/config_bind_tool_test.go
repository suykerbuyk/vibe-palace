// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

const bindToolLabel = "git@example.invalid:team/quantum-vault.git"

// bindToolHost is a hermetic host after a split: the global config's
// vault_path is a stamped vault recording "qa" as moved to bindToolLabel, and
// a stamped quantum vault holds qa with that origin. It returns the default
// vault (which the MCP server is bound to), the quantum vault and the config.
func bindToolHost(t *testing.T) (global, quantum, cfg string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	global = filepath.Join(home, "global-vault")
	quantum = filepath.Join(home, "quantum-vault")
	for _, v := range []string{global, quantum} {
		if err := surface.WriteFormat(v, surface.RequiredDataFormat); err != nil {
			t.Fatal(err)
		}
	}
	putFile(t, quantum, "Projects/qa/resume.md", "# qa\n")
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", bindToolLabel}} {
		cmd := exec.Command("git", append([]string{"-C", quantum}, args...)...)
		cmd.Env = gitenv.SafeGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	b, err := (departure.Record{Slug: "qa", Kind: departure.MovedToVault, To: bindToolLabel, Date: "2026-09-27"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, global, departure.RelPath("qa"), string(b))
	cfg, err = storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Dir(cfg), filepath.Base(cfg), "vault_path = \""+global+"\"\n")
	return global, quantum, cfg
}

// Test 25 + 29 (MCP half): the departed-project seam must not refuse
// vp_config_bind — binding a departed project to the vault it moved to is its
// purpose — so it takes no project/to_project property, and a call naming the
// departed slug reaches the handler and binds. MUTATION CONTRACT: rename the
// `slug` property to `project` and the call is refused by the seam.
func TestGateLeavesBindToolAlone(t *testing.T) {
	global, quantum, cfg := bindToolHost(t)
	srv := seamServer(t, storage.NewVault(global))

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	found := false
	for _, ti := range srv.Registry().List() {
		if ti.Name == "vp_config_bind" {
			found = true
			if err := json.Unmarshal(ti.Schema, &schema); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !found {
		t.Fatal("vp_config_bind is not registered on the stdio server")
	}
	for _, p := range []string{"project", "to_project"} {
		if _, ok := schema.Properties[p]; ok {
			t.Errorf("vp_config_bind takes %q, so the departed-project seam would refuse the slug it exists to bind", p)
		}
	}

	text, isErr := callTool(t, srv, "vp_config_bind", map[string]any{"slug": "qa", "vault_path": quantum})
	if isErr {
		t.Fatalf("vp_config_bind on the departed slug was refused: %s", text)
	}
	body, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "qa = \""+quantum+"\"") {
		t.Errorf("the handler did not bind:\n%s", body)
	}
}

// slugTargetAllowList is the ONE mutating tool allowed to name its target
// project by `slug` — a property the departed-project seam and its naming pin
// cannot see. Exact names, never a pattern: a pattern admits the next tool
// without anyone deciding to.
var slugTargetAllowList = map[string]bool{"vp_config_bind": true}

// slugTargetViolations lists mutating tools with a `slug` property that the
// allow-list does not name.
func slugTargetViolations(t *testing.T, infos []mcp.ToolInfo) []string {
	t.Helper()
	var bad []string
	for _, ti := range infos {
		if !ti.Mutating {
			continue
		}
		var s struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(ti.Schema, &s); err != nil {
			bad = append(bad, ti.Name+": unparseable schema")
			continue
		}
		if _, ok := s.Properties["slug"]; ok && !slugTargetAllowList[ti.Name] {
			bad = append(bad, ti.Name)
		}
	}
	sort.Strings(bad)
	return bad
}

// Test 26: the `slug` exemption is scoped to exactly vp_config_bind.
// MUTATION CONTRACT: give a second mutating tool a `slug` property, or widen
// the allow-list to a pattern, and this goes RED.
func TestOnlyTheBindToolTargetsAProjectBySlug(t *testing.T) {
	srv := seamServer(t, storage.NewVault(t.TempDir()))
	if bad := slugTargetViolations(t, srv.Registry().List()); len(bad) > 0 {
		t.Errorf("mutating tools name a target by `slug`, invisible to the departed-project seam: %v. "+
			"Name it `project`, or — only if it must operate on a departed slug — add it to slugTargetAllowList by exact name.", bad)
	}
	t.Run("fires_on_a_violating_tool", func(t *testing.T) {
		fake := []mcp.ToolInfo{
			{Name: "vp_config_fake", Mutating: true, Schema: json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string"}}}`)},
			{Name: "vp_config_bind", Mutating: true, Schema: json.RawMessage(`{"type":"object","properties":{"slug":{"type":"string"}}}`)},
		}
		if bad := slugTargetViolations(t, fake); len(bad) != 1 || bad[0] != "vp_config_fake" {
			t.Errorf("the allow-list must admit vp_config_bind and nothing else, got violations %v", bad)
		}
	})
}

// vp_config_bind takes slug or slugs, never both, and at least one.
func TestConfigBindToolSlugOrSlugs(t *testing.T) {
	for name, params := range map[string]string{
		"both":    `{"slug":"qa","slugs":["qa"],"vault_path":"/v"}`,
		"neither": `{"vault_path":"/v"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := configBindHandler(context.Background(), json.RawMessage(params)); !apperr.IsCaller(err) {
				t.Fatalf("err = %v, want a caller error", err)
			}
		})
	}
}
