// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultstack"
)

// poisonRoot is a vault root whose path carries a recognizable sentinel. A
// handler that forgot to shadow its captured vault reads THIS vault, and a
// vault op surfaces the sentinel in its error/output; one that reads the ctx
// stack never touches it.
const poisonMarker = "CLOSEDOVER_POISON"

func newTaskedVault(t *testing.T, root, project, slug, title string) *storage.Vault {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.InitProject(t, root, project)
	v := storage.NewVault(root)
	if err := v.CreateTask(project, storage.TaskSpec{Slug: slug, Title: title, Priority: "P1"}); err != nil {
		t.Fatalf("seed task in %s: %v", root, err)
	}
	return v
}

// TestListTasksReadsVaultFromCtx is the focused POSITIVE proof that a REAL
// rebound handler reads the ctx stack's vault, not the one it closed over at
// construction. It calls the handler directly (no gate), so it isolates the
// rebind itself. It FAILS if listTasksHandler drops its `vault := vaultFromCtx`
// shadow (it would list the closed-over poison vault's task instead).
func TestListTasksReadsVaultFromCtx(t *testing.T) {
	ctxRoot := t.TempDir()
	ctxVault := newTaskedVault(t, ctxRoot, "proj", "ctx-only-task", "CtxOnly")
	poisonRoot := filepath.Join(t.TempDir(), poisonMarker)
	poisonVault := newTaskedVault(t, poisonRoot, "proj", "poison-only-task", "PoisonMarker")

	// Constructor closes over the POISON vault; the ctx carries the real one.
	h := ListTasksTool(poisonVault).Handler
	ctx := vaultstack.WithStack(context.Background(), &vaultstack.VaultStack{Root: ctxRoot, Vault: ctxVault})

	res, err := h(ctx, json.RawMessage(`{"project":"proj"}`))
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), "ctx-only-task") {
		t.Fatalf("handler did not read the ctx stack vault (expected ctx-only-task): %s", raw)
	}
	if strings.Contains(string(raw), "poison-only-task") {
		t.Fatalf("handler read its closed-over poison vault: %s", raw)
	}
}

// TestEveryProjectKeyedToolResolvesVaultFromCtx is the OMISSION-GUARD table. A
// single-tool test (above) cannot catch a handler whose constructor someone
// forgot to rebind — and that gap matters before U5b flips the cross-vault
// refusal into a route. This drives EVERY registered project-keyed tool through
// the production tools/call path with the request stack pointing at a clean
// vault and the handlers' closed-over vault poisoned, and asserts no response
// ever surfaces the poison vault. A forgotten shadow turns this red.
//
// It cannot positively assert the ctx vault for every tool (each does different
// work and many need params beyond `project`, so they fail schema validation
// before the handler and pass this table vacuously) — the negative is the
// coverage claim, and TestMutatingToolsWriteToCtxVault adds positive write
// assertions for the mutating tools that matter most before U5b.
//
// A NON-NIL engine is passed to RegisterAll (G1) so the eight engine-gated
// project-keyed tools (vp_search, vp_capture_session, vp_get_friction_trends,
// vp_search_sessions, vp_get_session_detail, vp_get_project_context,
// vp_get_effectiveness, vp_refresh_index) are registered and scanned here. The
// two that read the ENGINE rather than the vault (vp_search, vp_refresh_index)
// can escape a root-in-response scan, so they get dedicated direct assertions
// (TestSearchReadsEngineFromCtx, TestRefreshIndexReadsEngineFromCtx).
func TestEveryProjectKeyedToolResolvesVaultFromCtx(t *testing.T) {
	ctxRoot := t.TempDir()
	ctxVault := storage.NewVault(ctxRoot)
	poisonRoot := filepath.Join(t.TempDir(), poisonMarker)
	if err := os.MkdirAll(poisonRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	poisonVault := storage.NewVault(poisonRoot)

	// Lightweight mock-embedder engines (no ONNX model): the ctx engine wraps the
	// clean vault, the closed-over engine wraps the poison vault.
	cfg := storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"}
	ctxEngine := search.NewEngine(embedder.NewMock(384), ctxVault, cfg)
	t.Cleanup(func() { ctxEngine.Close() })
	poisonEngine := search.NewEngine(embedder.NewMock(384), poisonVault, cfg)
	t.Cleanup(func() { poisonEngine.Close() })

	// Server vault == ctx vault, so the mutating gate (which reads the context
	// vault, not the stack) admits the call; handlers are registered over the
	// POISON vault + engine, so only a correct ctx-first read avoids it.
	srv := mcp.NewServer(ctxVault)
	RegisterAll(srv.Registry(), vpctx.NewResolver(poisonRoot), poisonVault, poisonEngine)
	srv.Registry().SetStackResolver(func(context.Context, json.RawMessage) (*vaultstack.VaultStack, error) {
		return &vaultstack.VaultStack{Root: ctxRoot, Vault: ctxVault, Resolver: vpctx.NewResolver(ctxRoot), Engine: ctxEngine}, nil
	})
	// Built-empty snapshot so project-keyed mutating writes are not fail-closed.
	snap, err := storage.BuildProjectVaultSnapshot(context.Background())
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	srv.SetProjectVaultSnapshot(snap)

	// Handshake so tools/call is served.
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize",
		"params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`))
	srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))

	tested := 0
	for _, info := range srv.Registry().List() {
		if !strings.Contains(string(info.Schema), `"project"`) {
			continue // not a project-keyed tool
		}
		tested++
		call, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": info.Name, "arguments": map[string]any{"project": "proj"}},
		})
		resp := srv.HandleMessage(context.Background(), json.RawMessage(call))
		raw, _ := json.Marshal(resp)
		if strings.Contains(string(raw), poisonMarker) {
			t.Errorf("%s reached its closed-over (poison) vault — a ctx-first rebind is missing: %s", info.Name, raw)
		}
	}
	if tested == 0 {
		t.Fatal("no project-keyed tools were exercised — the table is vacuous, check the filter")
	}
	t.Logf("exercised %d project-keyed tools against the poison vault", tested)
}

// TestSearchReadsEngineFromCtx covers the first engine-READING tool the poison
// root scan is weak on. The ctx engine knows project "proj"; the closed-over
// (poison) engine does NOT. A ctx-first handler searches the known project (an
// unbuilt-index or empty result, never "unknown project"); one that read its
// closed-over engine hits the poison engine, where "proj" is unknown. It FAILS
// if searchHandler drops its `engine := engineFromCtx` shadow.
func TestSearchReadsEngineFromCtx(t *testing.T) {
	ctxVault, ctxEngine := noteOnlyProjectFixture(t, "proj", map[string]string{
		"2026-08-12-1111aaaa-01": "the ledger design and the chunk store",
	})
	t.Cleanup(func() { ctxEngine.Close() })

	poisonRoot := filepath.Join(t.TempDir(), poisonMarker) // no "proj" here
	if err := os.MkdirAll(poisonRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	poisonVault := storage.NewVault(poisonRoot)
	poisonEngine := search.NewEngine(embedder.NewMock(384), poisonVault,
		storage.Config{SearchDefaultLimit: 10, EmbedderModel: "test-model"})
	t.Cleanup(func() { poisonEngine.Close() })

	h := SearchTool(poisonEngine).Handler
	ctx := vaultstack.WithStack(context.Background(),
		&vaultstack.VaultStack{Root: ctxVault.Root, Vault: ctxVault, Engine: ctxEngine})

	_, err := h(ctx, json.RawMessage(`{"project":"proj","query":"ledger"}`))
	if err != nil && strings.Contains(err.Error(), "unknown project") {
		t.Fatalf("search read its closed-over poison engine (project unknown there): %v", err)
	}
}

// TestRefreshIndexReadsEngineFromCtx covers the second engine-reading tool. The
// ctx vault's project has a session note (TrulyEmpty=false → a rebuild spawns);
// the closed-over (poison) vault's project is truly empty (TrulyEmpty=true → the
// handler refuses with "nothing to refresh", no spawn). The recorded launch must
// fire and name the CTX vault root. It FAILS if refreshIndexHandler drops its
// engine shadow (no spawn) or its vault shadow (spawn names the poison root).
func TestRefreshIndexReadsEngineFromCtx(t *testing.T) {
	ctxVault, ctxEngine := noteOnlyProjectFixture(t, "proj", map[string]string{
		"2026-08-12-2222bbbb-01": "work captured so the project is not truly empty",
	})
	t.Cleanup(func() { ctxEngine.Close() })
	poisonVault, poisonEngine := noteOnlyProjectFixture(t, "proj", nil) // truly empty
	t.Cleanup(func() { poisonEngine.Close() })

	rec := &recLauncher{}
	h := RefreshIndexTool(poisonEngine, poisonVault, rec.launch).Handler
	ctx := vaultstack.WithStack(context.Background(),
		&vaultstack.VaultStack{Root: ctxVault.Root, Vault: ctxVault, Engine: ctxEngine})

	if _, err := h(ctx, json.RawMessage(`{"project":"proj"}`)); err != nil {
		t.Fatalf("refresh refused a non-empty ctx project (read the poison engine?): %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("expected exactly one rebuild launch, got %d (handler read the empty poison engine)", rec.count())
	}
	args := strings.Join(rec.lastArgs(), " ")
	if !strings.Contains(args, ctxVault.Root) {
		t.Fatalf("rebuild did not target the ctx vault root %q: args=%s", ctxVault.Root, args)
	}
	if strings.Contains(args, poisonMarker) {
		t.Fatalf("rebuild targeted the closed-over poison vault: args=%s", args)
	}
}

// treeSig is a stable signature (sorted relpath:size) of every regular file
// under root, so a test can prove a write did or did not land in a vault.
func treeSig(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, p)
			lines = append(lines, rel+":"+strconv.FormatInt(info.Size(), 10))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// TestMutatingToolsWriteToCtxVault is the G2 POSITIVE guard: for the mutating
// project-keyed tools where a missed ctx-first shadow would mean a wrong-vault
// WRITE, it runs the handler with a VALID payload against a ctx stack pointing
// at a clean vault while the handler is constructed over a POISON vault, and
// proves the write landed in the CTX vault and NOT the poison one. The poison
// scan in the table above cannot catch these: several need props beyond
// `project` (so `{project}` fails schema validation and never runs), and a
// successful write need not echo the vault root. Each case FAILS if that
// handler's shadow is removed (the write would mutate the poison tree).
func TestMutatingToolsWriteToCtxVault(t *testing.T) {
	newV2Project := func(t *testing.T) (root string, v *storage.Vault) {
		t.Helper()
		root = t.TempDir()
		if err := surface.WriteFormat(root, surface.RequiredDataFormat); err != nil {
			t.Fatalf("stamp format: %v", err)
		}
		testutil.InitProject(t, root, "proj")
		return root, storage.NewVault(root)
	}

	cases := []struct {
		name   string
		tool   func(v *storage.Vault) mcp.Tool
		params string
	}{
		{"vp_manage_task", ManageTaskTool, `{"project":"proj","action":"create","task":"g2-task","title":"T","priority":"P1","content":"` +
			strings.Repeat("This is a real plan body written into the vault so the create arm accepts it. ", 5) + `"}`},
		{"vp_memory_write", MemoryWriteTool, `{"project":"proj","rel":"g2.md","name":"g2","type":"project","body":"x"}`},
		{"vp_kg_add", KGAddTool, `{"project":"proj","subject":"a","predicate":"uses","object":"b"}`},
		{"vp_append_iteration", AppendIterationTool, `{"project":"proj","title":"T","narrative":"N"}`},
		// vp_update_resume params are filled per-case below (CAS needs the sha).
		{"vp_update_resume", UpdateResumeTool, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctxRoot, ctxVault := newV2Project(t)
			poisonRoot, poisonVault := newV2Project(t)

			params := tc.params
			if tc.name == "vp_update_resume" {
				// Compare-and-set against the CTX vault's current resume sha.
				_, _, sha, _ := vpctx.NewResolver(ctxRoot).ResolveDigest("resume", "proj")
				b, _ := json.Marshal(map[string]string{
					"project": "proj", "content": "new resume body", "expected_sha256": sha,
				})
				params = string(b)
			}

			h := tc.tool(poisonVault).Handler // closed over the POISON vault
			ctx := vaultstack.WithStack(context.Background(),
				&vaultstack.VaultStack{Root: ctxRoot, Vault: ctxVault})

			poisonBefore := treeSig(t, poisonRoot)
			ctxBefore := treeSig(t, ctxRoot)

			if _, err := h(ctx, json.RawMessage(params)); err != nil {
				t.Fatalf("%s handler errored: %v", tc.name, err)
			}

			if got := treeSig(t, poisonRoot); got != poisonBefore {
				t.Fatalf("%s wrote into its closed-over POISON vault — ctx-first shadow missing", tc.name)
			}
			if got := treeSig(t, ctxRoot); got == ctxBefore {
				t.Fatalf("%s did not write into the ctx vault (nothing changed) — write did not follow the ctx stack", tc.name)
			}
		})
	}
}
