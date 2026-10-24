// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// VRR-T1 (primary, fail-first): on a multi-vault host, vp_read_resource for a URI
// whose project is bound to B must return B's body, not the startup vault A's. The
// same project/slug is seeded in both vaults with different bodies, so a wrong
// route is caught (not a not-found pass). Red before the RouteParams hook (the tool
// routes on the absent project param -> startup), green after.
func TestRouteResources_VRR_T1_ReadResourceRoutesToBoundVault(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	const slug = "vrr-diverge"
	bodyA := "# Task p/vrr-diverge\n\nVRR-BODY-IN-VAULT-A — startup vault, must NOT be served.\n"
	bodyB := "# Task p/vrr-diverge\n\nVRR-BODY-IN-VAULT-B — the bound vault, the correct body.\n"
	rwrite(t, filepath.Join(tasksDir(t, vaultA, "p"), slug+".md"), bodyA)
	rwrite(t, filepath.Join(tasksDir(t, vaultB, "p"), slug+".md"), bodyB)

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	isErr, raw := routeCall(t, srv, "vp_read_resource", map[string]any{"uri": mcp.TaskURI("p", slug)})
	if isErr {
		t.Fatalf("vp_read_resource errored: %s", raw)
	}
	if !strings.Contains(raw, "VRR-BODY-IN-VAULT-B") {
		t.Fatalf("vp_read_resource did not serve the bound vault B: %s", raw)
	}
	if strings.Contains(raw, "VRR-BODY-IN-VAULT-A") {
		t.Fatalf("vp_read_resource served the STARTUP vault A: %s", raw)
	}
}

// dispatchReadResourceContent drives vp_read_resource through Registry.Dispatch
// (the in-process twin of makeHandler) and returns the decoded result. Dispatch
// returns the handler's raw value (readResourceResult).
func dispatchReadResourceContent(t *testing.T, srv *mcp.Server, uri string) (readResourceResult, error) {
	t.Helper()
	params, err := json.Marshal(map[string]any{"uri": uri})
	if err != nil {
		t.Fatal(err)
	}
	res, derr := srv.Registry().Dispatch(context.Background(), "vp_read_resource", json.RawMessage(params))
	if derr != nil {
		return readResourceResult{}, derr
	}
	rr, ok := res.(readResourceResult)
	if !ok {
		t.Fatalf("Dispatch returned %T, want readResourceResult: %+v", res, res)
	}
	return rr, nil
}

// VRR-T2: three-surface parity on a multi-vault host — native resources/read,
// vp_read_resource, and vp_get_task must ALL return the bound vault B's body for
// the same task (vp_get_task is project-keyed and already routes to B, so its
// inclusion makes the triple a route-to-B pin, not a mutual-equality tautology).
func TestRouteResources_VRR_T2_ThreeSurfaceParity(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	const slug = "vrr-parity"
	rwrite(t, filepath.Join(tasksDir(t, vaultA, "p"), slug+".md"), "# A\n\nVRR-PARITY-A-must-not-win\n")
	rwrite(t, filepath.Join(tasksDir(t, vaultB, "p"), slug+".md"), "# B\n\nVRR-PARITY-B-is-correct\n")

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	uri := mcp.TaskURI("p", slug)

	native := resourceText(t, readResourceMsg(t, srv, uri))
	if !strings.Contains(native, "VRR-PARITY-B-is-correct") || strings.Contains(native, "VRR-PARITY-A-must-not-win") {
		t.Fatalf("native resources/read did not serve bound vault B: %q", native)
	}
	isErr, readRaw := routeCall(t, srv, "vp_read_resource", map[string]any{"uri": uri})
	if isErr || !strings.Contains(readRaw, "VRR-PARITY-B-is-correct") || strings.Contains(readRaw, "VRR-PARITY-A-must-not-win") {
		t.Fatalf("vp_read_resource did not serve bound vault B: %s", readRaw)
	}
	isErr, taskRaw := routeCall(t, srv, "vp_get_task", map[string]any{"project": "p", "task": slug})
	if isErr || !strings.Contains(taskRaw, "VRR-PARITY-B-is-correct") || strings.Contains(taskRaw, "VRR-PARITY-A-must-not-win") {
		t.Fatalf("vp_get_task did not serve bound vault B: %s", taskRaw)
	}
}

// VRR-T3: learning has no project in its URI, so vp_read_resource cannot route and
// must serve the startup vault — matching vp_get_learning. Seeded in both vaults.
func TestRouteResources_VRR_T3_LearningServesStartup(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	const fm = "---\nname: lrn\ndescription: d\ntype: reference\n---\n"
	rwrite(t, filepath.Join(storage.NewVault(vaultA).LearningsDir(), "lrn.md"), fm+"VRR-LEARNING-BODY-A\n")
	rwrite(t, filepath.Join(storage.NewVault(vaultB).LearningsDir(), "lrn.md"), fm+"VRR-LEARNING-BODY-B\n")

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	isErr, raw := routeCall(t, srv, "vp_read_resource", map[string]any{"uri": mcp.LearningURI("lrn")})
	if isErr {
		t.Fatalf("vp_read_resource errored: %s", raw)
	}
	if !strings.Contains(raw, "VRR-LEARNING-BODY-A") {
		t.Fatalf("learning did not serve the startup vault A: %s", raw)
	}
	if strings.Contains(raw, "VRR-LEARNING-BODY-B") {
		t.Fatalf("learning served the bound vault B — no project to route on, must serve startup: %s", raw)
	}
}

// VRR-T4: N=1 byte-identical — on a single-vault host vp_read_resource (driven
// through the hook via Dispatch) returns the same bytes as a direct ResolveURI
// against the startup resolver/vault, same invocation (so {{DATE}} cannot drift).
func TestRouteResources_VRR_T4_SingleVaultByteIdentical(t *testing.T) {
	vaultA, _, snap := routingHost(t, map[string]string{}, true)
	rwrite(t, filepath.Join(tasksDir(t, vaultA, "p"), "n1.md"), "# Task p/n1\n\nvrr single-vault body\n")
	rwrite(t, filepath.Join(vaultA, "Projects", "p", "resume.md"), "vrr resume body for {{PROJECT}}, no date token\n")

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	res := vpctx.NewResolver(vaultA)
	vlt := storage.NewVault(vaultA)

	for _, uri := range []string{mcp.TaskURI("p", "n1"), mcp.ResumeURI("p")} {
		rr, err := dispatchReadResourceContent(t, srv, uri)
		if err != nil {
			t.Fatalf("Dispatch(%s): %v", uri, err)
		}
		direct, _, derr := ResolveURI(uri, res, vlt)
		if derr != nil {
			t.Fatalf("ResolveURI(%s): %v", uri, derr)
		}
		if rr.Content != direct {
			t.Fatalf("N=1 not byte-identical for %s:\n vp_read_resource: %q\n ResolveURI:       %q", uri, rr.Content, direct)
		}
	}
}

// VRR-T5 (REQUIRED): parser unit test covering every fail-open case, and the
// helper's "never nil/empty" contract. This is the only test that catches a parser
// that extracts a project from a learning / unknown / malformed URI.
func TestRouteResources_VRR_T5_ParserFailOpenCases(t *testing.T) {
	// ProjectFromResourceURI.
	if p, ok := ProjectFromResourceURI(mcp.TaskURI("p", "s")); !ok || p != "p" {
		t.Fatalf("project-bearing URI: got (%q,%v), want (\"p\",true)", p, ok)
	}
	for name, uri := range map[string]string{
		"learning":       mcp.LearningURI("lrn"),
		"unknown scheme": "http://example.com/task/p/s",
		"unknown type":   mcp.ResourceScheme + "bogus/p/s",
		"too few segs":   mcp.ResourceScheme + "task/p",
		"empty type":     mcp.ResourceScheme,
	} {
		if p, ok := ProjectFromResourceURI(uri); ok || p != "" {
			t.Errorf("%s: got (%q,%v), want (\"\",false)", name, p, ok)
		}
	}

	// routeParamsFromResourceURI: success synthesizes {"project":...}; every
	// failure path returns the ORIGINAL params unchanged, never nil/empty.
	in := json.RawMessage(`{"uri":"` + mcp.TaskURI("p", "s") + `"}`)
	out := routeParamsFromResourceURI(in)
	var got struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal(out, &got); err != nil || got.Project != "p" {
		t.Fatalf("project-bearing URI: got %s (err %v), want {\"project\":\"p\"}", out, err)
	}
	for name, params := range map[string]json.RawMessage{
		"learning":    json.RawMessage(`{"uri":"` + mcp.LearningURI("lrn") + `"}`),
		"malformed":   json.RawMessage(`{"uri":"` + mcp.ResourceScheme + `task/p"}`),
		"no uri":      json.RawMessage(`{}`),
		"empty uri":   json.RawMessage(`{"uri":""}`),
		"bad json":    json.RawMessage(`{not json`),
		"unknown sch": json.RawMessage(`{"uri":"http://x/y"}`),
	} {
		res := routeParamsFromResourceURI(params)
		if len(res) == 0 {
			t.Errorf("%s: helper returned nil/empty, must return original params", name)
			continue
		}
		if !bytes.Equal(res, params) {
			t.Errorf("%s: got %s, want original params %s", name, res, params)
		}
	}
}

// VRR-T6 (REQUIRED): the same bound read must route through Registry.Dispatch (the
// in-process twin), not only makeHandler — so a one-site edit (:745 but not :409)
// cannot ship. The two dispatch paths must agree.
func TestRouteResources_VRR_T6_RoutesThroughDispatch(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	const slug = "vrr-dispatch"
	rwrite(t, filepath.Join(tasksDir(t, vaultA, "p"), slug+".md"), "# A\n\nVRR-DISPATCH-A-must-not-win\n")
	rwrite(t, filepath.Join(tasksDir(t, vaultB, "p"), slug+".md"), "# B\n\nVRR-DISPATCH-B-is-correct\n")

	srv := newRoutingResourceServer(t, vaultA, snap, "")
	rr, err := dispatchReadResourceContent(t, srv, mcp.TaskURI("p", slug))
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !strings.Contains(rr.Content, "VRR-DISPATCH-B-is-correct") || strings.Contains(rr.Content, "VRR-DISPATCH-A-must-not-win") {
		t.Fatalf("Registry.Dispatch did not route vp_read_resource to bound vault B: %q", rr.Content)
	}
}

// VRR-T7 (REQUIRED): a bound-stack build failure surfaces as a tool error and does
// NOT fall back to A's body — pinning the "do not swallow" decision for the tool
// AND that installStack actually ran (a hand-rolled parser that did not route would
// return A's body instead of erroring).
func TestRouteResources_VRR_T7_BoundStackBuildErrorSurfaces(t *testing.T) {
	vaultA, vaultB, snap := routingHost(t, map[string]string{"p": "B"}, true)
	const slug = "vrr-failroot"
	rwrite(t, filepath.Join(tasksDir(t, vaultA, "p"), slug+".md"),
		"# A\n\nVRR-FAILROOT-A-must-not-be-served\n")

	srv := newRoutingResourceServer(t, vaultA, snap, vaultB) // B's stack build fails
	isErr, raw := routeCall(t, srv, "vp_read_resource", map[string]any{"uri": mcp.TaskURI("p", slug)})
	if !isErr {
		t.Fatalf("a bound-stack build failure must surface as a tool error: %s", raw)
	}
	if strings.Contains(raw, "VRR-FAILROOT-A-must-not-be-served") {
		t.Fatalf("build failure silently fell back to startup vault A: %s", raw)
	}
}
