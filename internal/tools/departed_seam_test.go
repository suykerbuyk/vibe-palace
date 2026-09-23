// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// departedVault is a vault WITHOUT git (so only the departure record can
// answer) in which project "old" was renamed to "new", and "keep" is live.
func departedVault(t *testing.T) *storage.Vault {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"Projects/new/resume.md", "Projects/keep/resume.md"} {
		putFile(t, root, p, "live\n")
	}
	b, err := (departure.Record{Slug: "old", Kind: departure.Renamed, To: "new", Date: "2026-09-23"}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	putFile(t, root, departure.RelPath("old"), string(b))
	return storage.NewVault(root)
}

func putFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seamServer is a real MCP server over vault with every production tool
// registered — the server a host talks to.
func seamServer(t *testing.T, vault *storage.Vault) *mcp.Server {
	t.Helper()
	srv := mcp.NewServer(vault)
	eng := search.NewEngine(embedder.NewMock(384), vault, storage.Config{SearchDefaultLimit: 10})
	RegisterAll(srv.Registry(), vpctx.NewResolver(vault.Root), vault, eng)
	for _, m := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"0.1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	} {
		srv.HandleMessage(context.Background(), json.RawMessage(m))
	}
	return srv
}

// callTool sends one tools/call through HandleMessage — the JSON-RPC path every
// real MCP client uses — and returns the result text and whether it is an
// error.
func callTool(t *testing.T, srv *mcp.Server, name string, args map[string]any) (string, bool) {
	t.Helper()
	msg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := srv.HandleMessage(context.Background(), msg).(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("%s: expected a JSONRPCResponse", name)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v (%s)", name, err, raw)
	}
	if len(out.Content) == 0 {
		t.Fatalf("%s: empty content: %s", name, raw)
	}
	return out.Content[0].Text, out.IsError
}

// schemaArgs builds the smallest schema-valid WRITING invocation of a tool,
// with `project` set to slug: every required property filled by type, paths
// absolute, and an action enum pointed at a writer.
func schemaArgs(t *testing.T, name string, schema json.RawMessage, slug string) map[string]any {
	t.Helper()
	var s struct {
		Properties map[string]struct {
			Type json.RawMessage `json:"type"` // a string, or an array of them
			Enum []string        `json:"enum"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("%s schema: %v", name, err)
	}
	args := map[string]any{"project": slug}
	for _, k := range s.Required {
		if k == "project" {
			continue
		}
		p := s.Properties[k]
		var typ string
		if json.Unmarshal(p.Type, &typ) != nil {
			var types []string
			_ = json.Unmarshal(p.Type, &types)
			if len(types) > 0 {
				typ = types[0]
			}
		}
		switch {
		case k == "action" && slices.Contains(p.Enum, "create"):
			args[k] = "create"
		case len(p.Enum) > 0:
			args[k] = p.Enum[0]
		case strings.HasSuffix(k, "_path") || k == "cwd":
			args[k] = "/nonexistent/stale-checkout"
		case typ == "integer" || typ == "number":
			args[k] = 1
		case typ == "boolean":
			args[k] = false
		case typ == "array":
			args[k] = []string{"x"}
		default:
			args[k] = "x"
		}
	}
	if args["action"] == "create" {
		// vp_manage_task's create requires its body (a conditional allOf).
		args["content"] = "x"
	}
	return args
}

// projectTakingMutatingTools derives, from the live registry, every mutating
// tool whose input schema has a `project` property: the population the seam
// covers. Deriving it is what keeps the table below complete as tools are
// added.
func projectTakingMutatingTools(t *testing.T, srv *mcp.Server) []mcp.ToolInfo {
	t.Helper()
	var out []mcp.ToolInfo
	for _, ti := range srv.Registry().List() {
		if !ti.Mutating {
			continue
		}
		var s struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(ti.Schema, &s); err != nil {
			t.Fatalf("%s schema: %v", ti.Name, err)
		}
		if _, ok := s.Properties["project"]; ok {
			out = append(out, ti)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// T7. Every mutating tool that names a project — the five that had no gate at
// all included — is refused for a departed project at the dispatch seam, with
// the redirect, and Projects/old/ is not re-created.
func TestGateRefusesADepartedProjectOnEveryProjectTakingTool(t *testing.T) {
	vault := departedVault(t)
	srv := seamServer(t, vault)
	tools := projectTakingMutatingTools(t, srv)

	var names []string
	for _, ti := range tools {
		names = append(names, ti.Name)
	}
	for _, must := range []string{"vp_append_iteration", "vp_update_resume", "vp_memory_write", "vp_kg_add",
		"vp_enqueue_iteration_summary", "vp_capture_session", "vp_memory_harvest", "vp_manage_task"} {
		if !slices.Contains(names, must) {
			t.Fatalf("the derived table lost %s, so this test no longer covers it: %v", must, names)
		}
	}

	for _, ti := range tools {
		t.Run(ti.Name, func(t *testing.T) {
			text, isErr := callTool(t, srv, ti.Name, schemaArgs(t, ti.Name, ti.Schema, "old"))
			if !isErr || !strings.Contains(text, `renamed to "new"`) {
				t.Errorf("%s with project=old must be refused with the redirect; isError=%v text=%q", ti.Name, isErr, text)
			}
			if _, err := os.Stat(filepath.Join(vault.Root, "Projects", "old")); err == nil {
				t.Errorf("%s re-created Projects/old/", ti.Name)
			}
		})
	}
}

// T8. Moving a task INTO a departed project is the same resurrection, reached
// through to_project.
func TestGateRefusesAMoveIntoADepartedProject(t *testing.T) {
	vault := departedVault(t)
	srv := seamServer(t, vault)
	text, isErr := callTool(t, srv, "vp_manage_task", map[string]any{
		"project": "keep", "action": "move", "task": "some-task", "to_project": "old",
	})
	if !isErr || !strings.Contains(text, `renamed to "new"`) {
		t.Errorf("a move into a departed project must be refused with the redirect; isError=%v text=%q", isErr, text)
	}
	if _, err := os.Stat(filepath.Join(vault.Root, "Projects", "old")); err == nil {
		t.Error("the move re-created Projects/old/")
	}
}

// T13. vp_list_projects reports where departed projects went, and says nothing
// when none did.
func TestListProjectsReportsDeparted(t *testing.T) {
	text, isErr := callTool(t, seamServer(t, departedVault(t)), "vp_list_projects", map[string]any{})
	if isErr {
		t.Fatalf("vp_list_projects: %s", text)
	}
	var out struct {
		Projects []string          `json:"projects"`
		Departed []projectDeparted `json:"departed"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%v: %s", err, text)
	}
	if len(out.Departed) != 1 || out.Departed[0].Slug != "old" || out.Departed[0].Kind != "renamed" ||
		out.Departed[0].To != "new" || out.Departed[0].Date != "2026-09-23" {
		t.Errorf("departed = %+v, want old renamed to new on 2026-09-23", out.Departed)
	}

	clean := storage.NewVault(t.TempDir())
	putFile(t, clean.Root, "Projects/keep/resume.md", "live\n")
	text, _ = callTool(t, seamServer(t, clean), "vp_list_projects", map[string]any{})
	if strings.Contains(text, `"departed"`) {
		t.Errorf("no departures must mean no departed key: %s", text)
	}
}

// T14. Bootstrapping a departed project leads with the Departed instrument and
// its directive line — and still does on a DIRTY vault, where the advisories
// are suppressed, because a stale checkout is exactly the session most likely
// to be in trouble.
func TestBootstrapAlertsOnADepartedProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	vault := departedVault(t)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", vault.Root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	// Everything is untracked, so the vault is dirty.
	res := AssembleBootstrap(vpctx.NewResolver(vault.Root), vault, "old", "", "")
	if res.VaultDirt == nil {
		t.Fatal("test premise: the vault must be dirty")
	}
	if res.Departed == nil || res.Departed.Kind != "renamed" || res.Departed.To != "new" || res.Departed.Source != "record" {
		t.Fatalf("Departed = %+v, want renamed to new from the record", res.Departed)
	}
	if !strings.Contains(res.PostBootstrapInstructions, `PROJECT DEPARTED: "old" is not in this vault`) ||
		!strings.Contains(res.PostBootstrapInstructions, `set [project].name = "new"`) {
		t.Errorf("the directive must lead with the redirect, got %q", res.PostBootstrapInstructions)
	}
	if live := AssembleBootstrap(vpctx.NewResolver(vault.Root), vault, "keep", "", ""); live.Departed != nil {
		t.Errorf("a live project must carry no Departed instrument: %+v", live.Departed)
	}
}

// T15. A cwd-defaulted bootstrap from a stale checkout names the redirect.
func TestBootstrapCwdDefaultNamesTheRedirect(t *testing.T) {
	vault := departedVault(t)
	repo := t.TempDir()
	putFile(t, repo, ".vibe-palace.toml", "[project]\nname = \"old\"\n")
	t.Chdir(repo)
	_, err := resolveBootstrapProject("", vault, true)
	if err == nil || !strings.Contains(err.Error(), `set [project].name = "new"`) {
		t.Errorf("cwd default on a stale checkout must name the redirect, got %v", err)
	}
}

// T21. The seam keys on parameter NAMES. This pins the convention it relies
// on: a mutating tool may name a project only as project, to_project or
// project_path. The subtest proves the pin fires on a violating tool.
func TestEveryMutatingToolNamesItsTargetProjectConventionally(t *testing.T) {
	violations := func(tools []mcp.ToolInfo) []string {
		var bad []string
		for _, ti := range tools {
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
			for k := range s.Properties {
				if strings.Contains(k, "project") && k != "project" && k != "to_project" && k != "project_path" {
					bad = append(bad, ti.Name+"."+k)
				}
			}
		}
		sort.Strings(bad)
		return bad
	}

	srv := seamServer(t, storage.NewVault(t.TempDir()))
	if bad := violations(srv.Registry().List()); len(bad) > 0 {
		t.Errorf("mutating tools name a project outside the convention the departed-project seam keys on "+
			"(project / to_project / project_path): %v. Rename the property, or teach "+
			"mcp.refuseDepartedProject about it.", bad)
	}

	t.Run("fires_on_a_violating_tool", func(t *testing.T) {
		fake := []mcp.ToolInfo{{Name: "vp_fake", Mutating: true,
			Schema: json.RawMessage(`{"type":"object","properties":{"target_project":{"type":"string"}}}`)}}
		if bad := violations(fake); len(bad) != 1 || bad[0] != "vp_fake.target_project" {
			t.Errorf("the pin must fire on a mutating tool naming its project target_project, got %v", bad)
		}
	})
}

// G5. vp_init is the deliberate way back for a departed slug; the seam must
// not be able to refuse it. It takes no project parameter at all.
func TestGateLeavesVpInitAlone(t *testing.T) {
	srv := seamServer(t, storage.NewVault(t.TempDir()))
	for _, ti := range srv.Registry().List() {
		if ti.Name != "vp_init" {
			continue
		}
		var s struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(ti.Schema, &s); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.Properties["project"]; ok {
			t.Error("vp_init now takes a project parameter, so the departed-project seam would refuse the one tool that reopens a departed slug")
		}
		if _, ok := s.Properties["to_project"]; ok {
			t.Error("vp_init now takes to_project")
		}
		return
	}
	t.Fatal("vp_init is not registered")
}
