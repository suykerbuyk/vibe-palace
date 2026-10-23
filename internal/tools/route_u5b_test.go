// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultstack"
)

const routePlanBody = "This is a real plan body written into the vault so the create arm accepts it. " +
	"It is well past the two-hundred-byte floor the create arm enforces for a task plan, so the write proceeds " +
	"and lands in the routed vault where the project is bound, which is exactly what these routing tests prove."

func rwrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// routingHost sets a hermetic host with two v2 vaults A (startup) and B, and a
// [project_vaults] table built from bind (slug -> "A"|"B"). It returns the roots
// and the built snapshot. With build==false it returns an UNBUILT snapshot.
func routingHost(t *testing.T, bind map[string]string, build bool) (vaultA, vaultB string, snap storage.ProjectVaultSnapshot) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	vaultA = filepath.Join(home, "vault-a")
	vaultB = filepath.Join(home, "vault-b")
	manifest := fmt.Sprintf("format = %d\n", surface.RequiredDataFormat)
	rwrite(t, filepath.Join(vaultA, ".vibe-palace", "vault.toml"), manifest)
	rwrite(t, filepath.Join(vaultB, ".vibe-palace", "vault.toml"), manifest)

	var body strings.Builder
	body.WriteString("vault_path = \"" + vaultA + "\"\n\n[project_vaults]\n")
	for slug, which := range bind {
		root := vaultA
		if which == "B" {
			root = vaultB
		}
		body.WriteString(slug + " = \"" + root + "\"\n")
	}
	cfgPath, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	rwrite(t, cfgPath, body.String())

	if !build {
		return vaultA, vaultB, storage.ProjectVaultSnapshot{} // unbuilt
	}
	snap, err = storage.BuildProjectVaultSnapshot(context.Background())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	return vaultA, vaultB, snap
}

// newRoutingServer wires a real routing MCP server over startup vault A: a
// vault-stack registry whose BuildFunc returns a mock-embedder stack for any
// root (erroring for failRoot), RegisterAll over the startup stack, the U5b
// resolver, and the given launch. It returns the handshaken server.
func newRoutingServer(t *testing.T, vaultA string, snap storage.ProjectVaultSnapshot, failRoot string, launch detachlaunch.LaunchFunc) *mcp.Server {
	t.Helper()
	cfg := storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"}
	build := func(ctx context.Context, root string) (*vaultstack.VaultStack, error) {
		if failRoot != "" && root == failRoot {
			return nil, fmt.Errorf("test: refusing to build a stack for %q", root)
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
	opts := []RegisterOption{WithConfig(cfg)}
	if launch != nil {
		opts = append(opts, WithLaunch(launch))
	}
	RegisterAll(srv.Registry(), startup.Resolver, startup.Vault, startup.Engine, opts...)
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

func routeCall(t *testing.T, srv *mcp.Server, name string, args map[string]any) (isErr bool, raw string) {
	t.Helper()
	call, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	resp := srv.HandleMessage(context.Background(), json.RawMessage(call))
	b, _ := json.Marshal(resp)
	s := string(b)
	return strings.Contains(s, `"isError":true`), s
}

func initV2Project(t *testing.T, root, slug string) {
	t.Helper()
	if err := surface.WriteFormat(root, surface.RequiredDataFormat); err != nil {
		t.Fatalf("stamp format %s: %v", root, err)
	}
	testutil.InitProject(t, root, slug)
}

// TestRoute_BoundElsewhereWriteLandsInR — a vp_manage_task create for a project
// bound to B, driven through HandleMessage (the installStack path), writes the
// task under B, never under the startup vault A. FAILS if the resolver stops
// routing (lands in A) or the gate refuses.
func TestRoute_BoundElsewhereWriteLandsInR(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"qa": "B"}, true)
	initV2Project(t, vaultB, "qa")
	srv := newRoutingServer(t, vaultA, snap, "", nil)

	isErr, raw := routeCall(t, srv, "vp_manage_task", map[string]any{
		"project": "qa", "action": "create", "task": "routed-task",
		"title": "Routed", "priority": "P1", "content": routePlanBody,
	})
	if isErr {
		t.Fatalf("routed create must succeed: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(vaultB, "Projects", "qa", "tasks", "routed-task.md")); err != nil {
		t.Fatalf("task did not land in bound vault B: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultA, "Projects", "qa")); err == nil {
		t.Fatal("task landed in the startup vault A — not routed")
	}
}

// TestRoute_EngineGatedToolRoutes — vp_refresh_index (registered only when
// engine != nil, reads the ctx stack's ENGINE and vault) routes to B: the
// rebuild it launches names B's root, not A's. FAILS if the engine/vault shadow
// is dropped (no launch, or A's root).
func TestRoute_EngineGatedToolRoutes(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"qa": "B"}, true)
	initV2Project(t, vaultB, "qa")
	// A session note makes qa non-empty in B, so refresh spawns a rebuild.
	sess, err := storage.NewVault(vaultB).SessionDir("qa")
	if err != nil {
		t.Fatal(err)
	}
	rwrite(t, filepath.Join(sess, "2026-08-12-1111aaaa-01.md"), "the ledger design and the chunk store")

	rec := &routeLauncher{}
	srv := newRoutingServer(t, vaultA, snap, "", rec.launch)

	isErr, raw := routeCall(t, srv, "vp_refresh_index", map[string]any{"project": "qa"})
	if isErr {
		t.Fatalf("routed refresh must succeed: %s", raw)
	}
	args := strings.Join(rec.last(), " ")
	if !strings.Contains(args, vaultB) {
		t.Fatalf("rebuild did not target bound vault B: args=%s", args)
	}
	if strings.Contains(args, vaultA) {
		t.Fatalf("rebuild targeted the startup vault A: args=%s", args)
	}
}

// TestRoute_BoundElsewhereReadRoutes — a vp_get_task READ for a B-bound project
// returns B's task (decision A: reads route). Seed B only; a non-routed read hits
// A, where qa does not exist. FAILS if reads stay on the startup vault.
func TestRoute_BoundElsewhereReadRoutes(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"qa": "B"}, true)
	initV2Project(t, vaultB, "qa")
	if err := storage.NewVault(vaultB).CreateTask("qa", storage.TaskSpec{Slug: "only-in-b", Title: "OnlyInB", Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	srv := newRoutingServer(t, vaultA, snap, "", nil)

	isErr, raw := routeCall(t, srv, "vp_get_task", map[string]any{"project": "qa", "task": "only-in-b"})
	if isErr {
		t.Fatalf("routed read must find B's task: %s", raw)
	}
	if !strings.Contains(raw, "only-in-b") {
		t.Fatalf("read did not return B's task: %s", raw)
	}
}

// TestRoute_CrossVaultMoveRefusedNonVacuous — a move whose project→A and
// to_project→B spans two vaults is refused (RouteTwoRoots naming both roots) AND
// neither tree changes. A test that only checked "errors" would stay green if a
// later edit wrongly routed the move to one side; the byte-for-byte tree check
// catches that.
func TestRoute_CrossVaultMoveRefusedNonVacuous(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"qa": "B"}, true)
	initV2Project(t, vaultA, "local")
	initV2Project(t, vaultB, "qa")
	if err := storage.NewVault(vaultA).CreateTask("local", storage.TaskSpec{Slug: "mover", Title: "Mover", Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	srv := newRoutingServer(t, vaultA, snap, "", nil)

	beforeA := treeSig(t, vaultA)
	beforeB := treeSig(t, vaultB)
	isErr, raw := routeCall(t, srv, "vp_manage_task", map[string]any{
		"project": "local", "to_project": "qa", "task": "mover", "action": "move",
	})
	if !isErr {
		t.Fatalf("a cross-vault move must be refused: %s", raw)
	}
	if !strings.Contains(raw, "cross-vault move") || !strings.Contains(raw, vaultA) || !strings.Contains(raw, vaultB) {
		t.Fatalf("refusal must name the cross-vault move and BOTH roots: %s", raw)
	}
	if treeSig(t, vaultA) != beforeA {
		t.Fatal("startup vault A changed despite the move refusal")
	}
	if treeSig(t, vaultB) != beforeB {
		t.Fatal("bound vault B changed despite the move refusal")
	}
}

// TestRoute_SameVaultMoveRoutes — a move whose both slugs are bound to B routes
// to B and renames inside B (one stack can serve it).
func TestRoute_SameVaultMoveRoutes(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"qa": "B", "qa2": "B"}, true)
	initV2Project(t, vaultB, "qa")
	initV2Project(t, vaultB, "qa2")
	if err := storage.NewVault(vaultB).CreateTask("qa", storage.TaskSpec{Slug: "movee", Title: "Movee", Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	srv := newRoutingServer(t, vaultA, snap, "", nil)

	isErr, raw := routeCall(t, srv, "vp_manage_task", map[string]any{
		"project": "qa", "to_project": "qa2", "task": "movee", "action": "move",
	})
	if isErr {
		t.Fatalf("a same-vault move must route and succeed: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(vaultB, "Projects", "qa2", "tasks", "movee.md")); err != nil {
		t.Fatalf("same-vault move did not land the task under qa2 in B: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultA, "Projects")); err == nil {
		t.Fatal("the move touched the startup vault A")
	}
}

// TestRoute_UnbuiltSnapshotWriteRefusedReadUnaffected — under an UNBUILT
// snapshot a project-keyed WRITE fails closed (RouteBrokenOrUnbuilt) while a READ
// is unaffected (served from the startup vault).
func TestRoute_UnbuiltSnapshotWriteRefusedReadUnaffected(t *testing.T) {
	vaultA, _, unbuilt := routingHost(t, map[string]string{"qa": "B"}, false)
	initV2Project(t, vaultA, "qa") // so the read has something to return from A
	if err := storage.NewVault(vaultA).CreateTask("qa", storage.TaskSpec{Slug: "in-a", Title: "InA", Priority: "P1"}); err != nil {
		t.Fatal(err)
	}
	srv := newRoutingServer(t, vaultA, unbuilt, "", nil)

	isErr, raw := routeCall(t, srv, "vp_manage_task", map[string]any{
		"project": "qa", "action": "create", "task": "nope", "title": "N", "priority": "P1", "content": routePlanBody,
	})
	if !isErr || !strings.Contains(raw, "routing snapshot") {
		t.Fatalf("unbuilt snapshot must fail a WRITE closed naming the snapshot: %s", raw)
	}
	isErr, raw = routeCall(t, srv, "vp_get_task", map[string]any{"project": "qa", "task": "in-a"})
	if isErr {
		t.Fatalf("a READ must be unaffected under an unbuilt snapshot (served from A): %s", raw)
	}
	if !strings.Contains(raw, "in-a") {
		t.Fatalf("read did not return the startup vault's task: %s", raw)
	}
}

// TestRoute_StackForBuildFailureFailsClosed — when the routed stack cannot be
// built, the request surfaces that error and the startup vault is untouched (no
// silent fallback to startup).
func TestRoute_StackForBuildFailureFailsClosed(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"qa": "B"}, true)
	beforeA := treeSig(t, vaultA)
	srv := newRoutingServer(t, vaultA, snap, vaultB /* fail to build B */, nil)

	isErr, raw := routeCall(t, srv, "vp_manage_task", map[string]any{
		"project": "qa", "action": "create", "task": "x", "title": "X", "priority": "P1", "content": routePlanBody,
	})
	if !isErr || !strings.Contains(raw, "refusing to build a stack") {
		t.Fatalf("a StackFor build failure must surface as the tool error: %s", raw)
	}
	if treeSig(t, vaultA) != beforeA {
		t.Fatal("startup vault A changed on a routed build failure (silent fallback to startup)")
	}
}

// TestRoute_UnboundProjectServedFromStartup — a project with no [project_vaults]
// entry routes to the startup vault (the adopted default; unchanged).
func TestRoute_UnboundProjectServedFromStartup(t *testing.T) {
	vaultA, _, snap := routingHost(t, map[string]string{"qa": "B"}, true)
	initV2Project(t, vaultA, "solo")
	srv := newRoutingServer(t, vaultA, snap, "", nil)

	isErr, raw := routeCall(t, srv, "vp_manage_task", map[string]any{
		"project": "solo", "action": "create", "task": "s", "title": "S", "priority": "P1", "content": routePlanBody,
	})
	if isErr {
		t.Fatalf("an unbound project must be served from the startup vault: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(vaultA, "Projects", "solo", "tasks", "s.md")); err != nil {
		t.Fatalf("unbound write did not land in the startup vault A: %v", err)
	}
}

// TestRoute_GateClassifiesAgainstStartupRootNotWriteRoot pins the G2-fix: the
// gate passes the STARTUP root (not the routed writeRoot) to RouteForRequest.
// An adversarial resolver installs stack B for EVERY request, so writeRoot == B.
// The request names `solo` (UNBOUND — no [project_vaults] entry, so startup-root-
// relative it resolves to A) and `qa` (bound to B): startup-relative that is a
// TWO-ROOT cross-vault move and MUST be refused. If the gate instead classified
// against writeRoot == B, `solo` would be Unbound→B and `qa` BoundHere→B — one
// root B — and the move would be WRONGLY allowed. This test FAILS (the refusal
// message disappears) if tools.go's gate is changed to pass writeRoot.
func TestRoute_GateClassifiesAgainstStartupRootNotWriteRoot(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"qa": "B"}, true) // solo is unbound
	initV2Project(t, vaultA, "solo")
	initV2Project(t, vaultB, "qa")

	cfg := storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"}
	build := func(ctx context.Context, root string) (*vaultstack.VaultStack, error) {
		v := storage.NewVault(root)
		eng := search.NewEngine(embedder.NewMock(384), v, cfg)
		return vaultstack.NewVaultStack(root, v, cfg, eng, vpctx.NewResolver(root), nil), nil
	}
	reg := vaultstack.NewRegistry(build, embedder.NewSharedModelCache())
	t.Cleanup(func() { reg.Close() })
	startupA, err := reg.StackFor(context.Background(), vaultA)
	if err != nil {
		t.Fatalf("build startup stack: %v", err)
	}
	stackB, err := reg.StackFor(context.Background(), vaultB)
	if err != nil {
		t.Fatalf("build B stack: %v", err)
	}

	srv := mcp.NewServer(startupA.Vault) // VaultFromContext (startupRoot) == A
	srv.SetProjectVaultSnapshot(snap)
	RegisterAll(srv.Registry(), startupA.Resolver, startupA.Vault, startupA.Engine, WithConfig(cfg))
	// Adversarial: ALWAYS install stack B, so the gate's writeRoot is B for this
	// request even though the startup-relative route is a two-root move.
	srv.Registry().SetStackResolver(func(ctx context.Context, params json.RawMessage) (*vaultstack.VaultStack, error) {
		return stackB, nil
	})
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	isErr, raw := routeCall(t, srv, "vp_manage_task", map[string]any{
		"project": "solo", "to_project": "qa", "task": "x", "action": "move",
	})
	if !isErr || !strings.Contains(raw, "cross-vault move") {
		t.Fatalf("gate must classify against the STARTUP root (A) and refuse this two-root move; got: %s", raw)
	}
	if !strings.Contains(raw, vaultA) || !strings.Contains(raw, vaultB) {
		t.Fatalf("the cross-vault-move refusal must name both roots: %s", raw)
	}
}

// routeLauncher records detached-launch invocations.
type routeLauncher struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *routeLauncher) launch(_ string, args []string, _ string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, slices.Clone(args))
	return 4242, nil
}

func (r *routeLauncher) last() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}
