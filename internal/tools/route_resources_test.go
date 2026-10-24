// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultstack"
)

// newRoutingResourceServer is the sibling of newRoutingServer (route_u5b_test.go)
// that ALSO registers the content-resource surface, so a resources/read request
// can be driven through HandleMessage the same way routeCall drives tools/call.
// It is kept separate (not folded into newRoutingServer) so the existing routing
// tests do not all start advertising resources.
//
// failRoot (when non-empty) makes the vault-stack BuildFunc error for that one
// root, so a routed resources/read for a project bound there surfaces the
// StackFor build error — the T6b fixture.
func newRoutingResourceServer(t *testing.T, vaultA string, snap storage.ProjectVaultSnapshot, failRoot string) *mcp.Server {
	t.Helper()
	cfg := storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"}
	build := func(ctx context.Context, root string) (*vaultstack.VaultStack, error) {
		if failRoot != "" && root == failRoot {
			return nil, errBuildRefused
		}
		v := storage.NewVault(root)
		eng := search.NewEngine(embedder.NewMock(384), v, cfg)
		return vaultstack.NewVaultStack(root, v, cfg, eng, vpctx.NewResolver(root), nil), nil
	}
	reg := vaultstack.NewRegistry(build, embedder.NewSharedModelCache())
	t.Cleanup(func() { reg.Close() })
	startup, err := reg.StackFor(context.Background(), vaultA)
	if err != nil {
		t.Fatalf("build startup stack: %v", err)
	}
	srv := mcp.NewServer(startup.Vault)
	srv.SetProjectVaultSnapshot(snap)
	RegisterAll(srv.Registry(), startup.Resolver, startup.Vault, startup.Engine, WithConfig(cfg))
	// Closed-over fallback is the STARTUP stack, exactly as bootstrap wires it.
	RegisterResources(srv, startup.Resolver, startup.Vault)
	srv.Registry().SetStackResolver(func(ctx context.Context, params json.RawMessage) (*vaultstack.VaultStack, error) {
		root, rerr := storage.RouteForRequest(params, startup.Root, snap)
		if rerr != nil || root == startup.Root {
			return startup, nil
		}
		return reg.StackFor(ctx, root)
	})
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	return srv
}

var errBuildRefused = errRouteResources("test: refusing to build a stack for the bound root")

type errRouteResources string

func (e errRouteResources) Error() string { return string(e) }

// readResourceMsg sends a native resources/read request and returns the raw
// JSON-RPC response message.
func readResourceMsg(t *testing.T, srv *mcp.Server, uri string) mcplib.JSONRPCMessage {
	t.Helper()
	msg, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "resources/read",
		"params": map[string]any{"uri": uri},
	})
	if err != nil {
		t.Fatalf("marshal resources/read: %v", err)
	}
	return srv.HandleMessage(context.Background(), json.RawMessage(msg))
}

// resourceText decodes a successful resources/read response to its single
// content body. It fails the test if the response is an error rather than a
// result — use a mcplib.JSONRPCError type-assert directly for the error cases
// (NOT routeCall/isError, which is the tools/call envelope, not this one).
func resourceText(t *testing.T, resp mcplib.JSONRPCMessage) string {
	t.Helper()
	rpc, ok := resp.(mcplib.JSONRPCResponse)
	if !ok {
		t.Fatalf("expected JSONRPCResponse, got %T: %+v", resp, resp)
	}
	raw, err := json.Marshal(rpc.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var result struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(result.Contents) != 1 {
		t.Fatalf("want 1 content entry, got %d: %s", len(result.Contents), raw)
	}
	return result.Contents[0].Text
}

func tasksDir(t *testing.T, root, proj string) string {
	t.Helper()
	d, err := storage.NewVault(root).TasksDir(proj)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestRouteResources_T1_TaskRoutesToBoundVault is the primary pin: the SAME
// project/slug is seeded in BOTH vaults with DIFFERENT bodies, and the native
// resources/read must return the BOUND vault's body. This fails before the fix
// (native serves the startup vault A) and passes after. Different-bodies, not
// not-found, so a template miss / slug rejection / never-called handler cannot
// make it pass for the wrong reason.
func TestRouteResources_T1_TaskRoutesToBoundVault(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	const slug = "diverge"
	bodyA := "# Task p/diverge\n\nBODY-IN-VAULT-A — the startup vault, must NOT be served.\n"
	bodyB := "# Task p/diverge\n\nBODY-IN-VAULT-B — the bound vault, the correct body.\n"
	rwrite(t, filepath.Join(tasksDir(t, vaultA, "p"), slug+".md"), bodyA)
	rwrite(t, filepath.Join(tasksDir(t, vaultB, "p"), slug+".md"), bodyB)

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	got := resourceText(t, readResourceMsg(t, srv, mcp.TaskURI("p", slug)))

	if got != bodyB {
		t.Fatalf("native resources/read served the wrong vault:\n got:   %q\n wantB: %q", got, bodyB)
	}
	if got == bodyA {
		t.Fatalf("native resources/read served the STARTUP vault A body")
	}
}

// TestRouteResources_T2_ResumeRoutesToBoundVault pins a Resolver-backed type:
// resume resolves through the precedence Resolver (never 404s — it falls through
// to the embedded default), so the fixture seeds project resume.md in BOTH vaults
// with a non-{{DATE}} distinguisher that also differs from the embedded default.
// Proves the resolver (not just the vault) is taken from the routed stack.
func TestRouteResources_T2_ResumeRoutesToBoundVault(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	rwrite(t, filepath.Join(vaultA, "Projects", "p", "resume.md"), "RESUME-MARKER-A for {{PROJECT}}\n")
	rwrite(t, filepath.Join(vaultB, "Projects", "p", "resume.md"), "RESUME-MARKER-B for {{PROJECT}}\n")

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	got := resourceText(t, readResourceMsg(t, srv, mcp.ResumeURI("p")))

	if !strings.Contains(got, "RESUME-MARKER-B") {
		t.Fatalf("resume did not route to the bound vault B: %q", got)
	}
	if strings.Contains(got, "RESUME-MARKER-A") {
		t.Fatalf("resume served the startup vault A: %q", got)
	}
	if !strings.Contains(got, "for p") {
		t.Fatalf("resolver did not expand {{PROJECT}} — a routed stack's resolver should: %q", got)
	}
}

// TestRouteResources_T3_SingleVaultByteIdentical pins the N=1 invariant: with no
// bindings, every project routes to startup, so the native read must be
// byte-identical to a direct ResolveURI against the startup resolver/vault. The
// comparison is same-invocation so {{DATE}} in resume cannot drift.
func TestRouteResources_T3_SingleVaultByteIdentical(t *testing.T) {
	vaultA, _, snap := routingHost(t, map[string]string{}, true)
	rwrite(t, filepath.Join(tasksDir(t, vaultA, "p"), "n1.md"), "# Task p/n1\n\nsingle-vault body\n")
	rwrite(t, filepath.Join(vaultA, "Projects", "p", "resume.md"), "resume body for {{PROJECT}}, no date token\n")

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	res := vpctx.NewResolver(vaultA)
	vlt := storage.NewVault(vaultA)

	for _, uri := range []string{mcp.TaskURI("p", "n1"), mcp.ResumeURI("p")} {
		native := resourceText(t, readResourceMsg(t, srv, uri))
		direct, _, err := ResolveURI(uri, res, vlt)
		if err != nil {
			t.Fatalf("ResolveURI(%s): %v", uri, err)
		}
		if native != direct {
			t.Fatalf("N=1 not byte-identical for %s:\n native: %q\n direct: %q", uri, native, direct)
		}
	}
}

// TestRouteResources_T4_NativeAgreesWithTool is the actual invariant the task
// restores: on a multi-vault host, the native resources/read and a project-keyed
// routed TOOL read (vp_get_task) must serve the same (bound) vault for the same
// task. Different bodies in A and B so "agree on B" is a real pin.
//
// NOTE: the comparison tool is vp_get_task (project-keyed, so RouteForRequest
// classifies it to B), NOT vp_read_resource. vp_read_resource takes only a URI,
// has no `project` param, and so installStack routes it to the STARTUP vault —
// it does NOT route a URI read to the bound vault. That is a pre-existing gap the
// plan/review mis-stated as "vp_read_resource already routes"; see the impl
// findings. It is out of this unit's 2-file scope and intentionally not fixed
// here.
func TestRouteResources_T4_NativeAgreesWithTool(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	rwrite(t, filepath.Join(tasksDir(t, vaultA, "p"), "parity.md"), "# A\n\nBODY-A-must-not-win\n")
	rwrite(t, filepath.Join(tasksDir(t, vaultB, "p"), "parity.md"), "# B\n\nBODY-B-is-correct\n")

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	uri := mcp.TaskURI("p", "parity")

	native := resourceText(t, readResourceMsg(t, srv, uri))
	if !strings.Contains(native, "BODY-B-is-correct") || strings.Contains(native, "BODY-A-must-not-win") {
		t.Fatalf("native read did not serve bound vault B: %q", native)
	}
	isErr, toolRaw := routeCall(t, srv, "vp_get_task", map[string]any{"project": "p", "task": "parity"})
	if isErr {
		t.Fatalf("vp_get_task errored: %s", toolRaw)
	}
	if !strings.Contains(toolRaw, "BODY-B-is-correct") || strings.Contains(toolRaw, "BODY-A-must-not-win") {
		t.Fatalf("vp_get_task did not serve bound vault B: %s", toolRaw)
	}
}

// TestRouteResources_T5_LearningServesStartup pins decision 6.1(a): learning has
// no project in its URI, so it cannot route and serves the startup vault — which
// matches vp_get_learning. Seeded in both vaults with different bodies; the
// native read must return A's (startup), never B's.
func TestRouteResources_T5_LearningServesStartup(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	const fm = "---\nname: lrn\ndescription: d\ntype: reference\n---\n"
	rwrite(t, filepath.Join(storage.NewVault(vaultA).LearningsDir(), "lrn.md"), fm+"LEARNING-BODY-A\n")
	rwrite(t, filepath.Join(storage.NewVault(vaultB).LearningsDir(), "lrn.md"), fm+"LEARNING-BODY-B\n")

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	got := resourceText(t, readResourceMsg(t, srv, mcp.LearningURI("lrn")))

	if !strings.Contains(got, "LEARNING-BODY-A") {
		t.Fatalf("learning did not serve the startup vault A: %q", got)
	}
	if strings.Contains(got, "LEARNING-BODY-B") {
		t.Fatalf("learning served the bound vault B — it has no project to route on and must serve startup: %q", got)
	}
}

// seedPoisonResources writes marker-bearing content for every routable resource
// type under root, so that if a provider wrongly reads its closed-over (poison)
// vault instead of the ctx stack, the poison marker surfaces in the response.
// learning is omitted (it has no project to route on — decision 6.1(a)).
func seedPoisonResources(t *testing.T, root string) {
	t.Helper()
	body := "poison body " + poisonMarker + "\n"
	v := storage.NewVault(root)
	rwrite(t, filepath.Join(tasksDir(t, root, "proj"), "some-task.md"), body)
	rwrite(t, filepath.Join(root, "Projects", "proj", "resume.md"), body)
	rwrite(t, filepath.Join(root, "Projects", "proj", "workflow.md"), body)
	rwrite(t, filepath.Join(root, "Projects", "proj", "doctrine.md"), body)
	rwrite(t, filepath.Join(root, "Projects", "proj", "commands", "some-cmd.md"), body)
	rwrite(t, filepath.Join(root, "Projects", "proj", "skills", "some-skill", "SKILL.md"), body)
	kf, err := v.KnowledgeFile("proj")
	if err != nil {
		t.Fatal(err)
	}
	rwrite(t, kf, body)
	itf, err := v.IterationsFile("proj")
	if err != nil {
		t.Fatal(err)
	}
	rwrite(t, itf, "## Iteration 1 — Poison\n\n"+body)
}

// TestRouteResources_T6_EveryTypeResolvesVaultFromCtx is the omission catcher:
// the resource-side analogue of TestEveryProjectKeyedToolResolvesVaultFromCtx.
// The closed-over vault/resolver is POISONED; the ctx stack is clean. For every
// resourceType except learning, a native resources/read must not surface the
// poison marker — i.e. the provider read its vault/resolver ctx-first. A future
// routable row that forgets the shadow (or names its project var something other
// than "project") turns this red. Session's not-found error path carries the
// poison ROOT directly; the rest are caught via seeded poison content.
func TestRouteResources_T6_EveryTypeResolvesVaultFromCtx(t *testing.T) {
	ctxRoot := t.TempDir()
	ctxVault := storage.NewVault(ctxRoot)
	poisonRoot := filepath.Join(t.TempDir(), poisonMarker)
	if err := os.MkdirAll(poisonRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	poisonVault := storage.NewVault(poisonRoot)
	seedPoisonResources(t, poisonRoot)

	cfg := storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"}
	ctxEngine := search.NewEngine(embedder.NewMock(384), ctxVault, cfg)
	t.Cleanup(func() { ctxEngine.Close() })

	srv := mcp.NewServer(ctxVault)
	// Providers close over the POISON resolver/vault; only a correct ctx-first
	// read avoids them.
	RegisterResources(srv, vpctx.NewResolver(poisonRoot), poisonVault)
	srv.Registry().SetStackResolver(func(context.Context, json.RawMessage) (*vaultstack.VaultStack, error) {
		return &vaultstack.VaultStack{Root: ctxRoot, Vault: ctxVault, Resolver: vpctx.NewResolver(ctxRoot), Engine: ctxEngine}, nil
	})
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	uris := map[string]string{
		"task":      mcp.TaskURI("proj", "some-task"),
		"resume":    mcp.ResumeURI("proj"),
		"workflow":  mcp.WorkflowURI("proj"),
		"doctrine":  mcp.DoctrineURI("proj"),
		"command":   mcp.CommandURI("proj", "some-cmd"),
		"skill":     mcp.SkillURI("proj", "some-skill"),
		"session":   mcp.SessionURI("proj", "2026-01-02-abcd1234-01"),
		"knowledge": mcp.KnowledgeURI("proj"),
		"iteration": mcp.IterationURI("proj", 1),
	}
	tested := 0
	for i := range resourceTypes {
		name := resourceTypes[i].name
		if name == "learning" {
			continue
		}
		uri, ok := uris[name]
		if !ok {
			t.Fatalf("resourceType %q has no T6 URI — add one so new routable types stay covered", name)
		}
		tested++
		raw, err := json.Marshal(readResourceMsg(t, srv, uri))
		if err != nil {
			t.Fatalf("marshal response for %q: %v", name, err)
		}
		if strings.Contains(string(raw), poisonMarker) {
			t.Errorf("resource %q reached its closed-over (poison) vault — a ctx-first shadow is missing: %s", name, raw)
		}
	}
	if tested == 0 {
		t.Fatal("no resource types were exercised — the table is vacuous, check the filter")
	}
	t.Logf("exercised %d routable resource types against the poison vault", tested)
}

// TestRouteResources_T6b_StackBuildErrorSurfaces pins decision 6.2 AND that
// installStack actually RAN: a bound project whose stack fails to build must
// surface as a JSON-RPC error (type mcplib.JSONRPCError), not a silent fallback
// to the startup vault A. A provider that hand-routed by parsing the URI would
// return A's body here; only a real installStack call surfaces the build error.
func TestRouteResources_T6b_StackBuildErrorSurfaces(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	rwrite(t, filepath.Join(tasksDir(t, vaultA, "p"), "x.md"),
		"# A\n\nstartup-body-must-not-be-served-on-build-failure\n")

	srv := newRoutingResourceServer(t, vaultA, snap, vaultB) // B's stack build fails
	resp := readResourceMsg(t, srv, mcp.TaskURI("p", "x"))

	if _, ok := resp.(mcplib.JSONRPCError); !ok {
		t.Fatalf("a bound-stack build failure must surface as JSONRPCError, got %T: %+v", resp, resp)
	}
	raw, _ := json.Marshal(resp)
	if strings.Contains(string(raw), "startup-body-must-not-be-served") {
		t.Fatalf("build failure silently fell back to startup vault A: %s", raw)
	}
}
