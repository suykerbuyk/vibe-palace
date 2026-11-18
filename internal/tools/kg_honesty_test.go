// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

// KG honesty (task transcript-opt-out-empties-the-knowledge-graph, plan v3):
// when the transcript tier is excluded, every KG surface says WHY the graph is
// empty instead of serving a bare zero.
//
// 🔴 EVERY TEST HERE ASSERTS MARSHALLED OUTPUT through the PRODUCTION handler
// (Q1), so a missing key fails it and no new symbol is needed for it to
// compile. The fixtures are real built projects: seedAuthoredProject runs a
// production RebuildExplicit, so the `authored` state is derived, not assumed
// (Q3). The default host config EXCLUDES transcripts, so the included-tier
// controls flip a value the default does not have (Q2).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

const khProject = "test-proj"

// khCall runs a tool's production handler and returns its marshalled result.
func khCall(t *testing.T, tool mcp.Tool, params string) string {
	t.Helper()
	out, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("%s: %v", tool.Name, err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// khAuthoredVault is a born-current vault whose project khProject is in the
// `authored` coverage state: asserted, not assumed.
func khAuthoredVault(t *testing.T) *storage.Vault {
	t.Helper()
	sandboxHostEnv(t)
	v := bornCurrentTestVault(t, t.TempDir())
	seedAuthoredProject(t, v, khProject)
	assertCoverageState(t, v, search.CoverageAuthored)
	return v
}

func assertCoverageState(t *testing.T, v *storage.Vault, want search.CoverageState) {
	t.Helper()
	cov, err := coverageForProject(v, khProject)
	if err != nil {
		t.Fatalf("PRESENCE: coverage unreadable: %v", err)
	}
	if cov.State != want {
		t.Fatalf("PRESENCE: the fixture reads %q, want %q: %s", cov.State, want, cov.Reason)
	}
}

// khWriteHostProjectConfig writes the host-local project config (the layer
// that decides per project whether this host indexes transcripts).
func khWriteHostProjectConfig(t *testing.T, body string) {
	t.Helper()
	p, err := storage.HostProjectConfigPath(khProject)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func khAddFact(t *testing.T, v *storage.Vault) {
	t.Helper()
	for _, n := range []string{"Alice", "Bob"} {
		if err := v.AddEntity(khProject, storage.Entity{ID: strings.ToLower(n), Name: n, Type: "person",
			CreatedAt: "2026-06-06T00:00:00Z", Origin: storage.OriginAuthored}); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.AddAuthoredTriple(khProject, storage.Triple{Subject: "Alice", Predicate: "knows",
		Object: "Bob", Confidence: 1, ValidFrom: "2026-06-06"}); err != nil {
		t.Fatal(err)
	}
}

func mustHave(t *testing.T, who, wire string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(wire, s) {
			t.Errorf("%s: output lacks %s.\n  wire: %s", who, s, wire)
		}
	}
}

func mustNotHave(t *testing.T, who, wire string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if strings.Contains(wire, s) {
			t.Errorf("%s: output carries %s and must not.\n  wire: %s", who, s, wire)
		}
	}
}

const (
	khExcluded = `"kg_status":"tier_excluded"`
	khWording  = "archive-extracted"
)

// T2: stats says why, and keeps its counts.
//
// Mutation: delete the kg_status assignment in kgStatsHandler.
func TestKGStatsSaysWhyOnAnExcludedTier(t *testing.T) {
	v := khAuthoredVault(t)
	wire := khCall(t, KGStatsTool(v), `{"project":"`+khProject+`"}`)
	mustHave(t, "vp_kg_stats", wire, khExcluded, khWording, `"entity_count":0`, `"triple_count":0`)

	// Q3 (plan §7 Q3): a few authored facts must not read as the whole graph.
	khAddFact(t, v)
	wire = khCall(t, KGStatsTool(v), `{"project":"`+khProject+`"}`)
	mustHave(t, "vp_kg_stats with authored facts", wire, khExcluded, `"entity_count":2`)
}

// T2: query and timeline say why, ONLY on an empty result, and `complete`
// stays the last key.
//
// Mutation: derive the status on a non-empty result (M13) / delete it (M5).
func TestKGQueryAndTimelineSayWhyOnlyWhenEmpty(t *testing.T) {
	v := khAuthoredVault(t)
	for _, tc := range []struct {
		name   string
		tool   mcp.Tool
		params string
	}{
		{"vp_kg_query", KGQueryTool(v), `{"project":"` + khProject + `","entity":"Nobody"}`},
		{"vp_kg_timeline", KGTimelineTool(v), `{"project":"` + khProject + `","entity":"Nobody"}`},
	} {
		wire := khCall(t, tc.tool, tc.params)
		mustHave(t, tc.name+" (empty)", wire, khExcluded, khWording, `"triples":[]`)
		if i, j := strings.Index(wire, `"kg_status"`), strings.Index(wire, `"complete"`); i < 0 || j < i {
			t.Errorf("%s: `complete` must stay the terminal key, after kg_status: %s", tc.name, wire)
		}
		if !strings.HasSuffix(wire, `"complete":true}`) {
			t.Errorf("%s: the terminal sentinel moved: %s", tc.name, wire)
		}
	}

	khAddFact(t, v)
	for _, tc := range []struct {
		name   string
		tool   mcp.Tool
		params string
	}{
		{"vp_kg_query", KGQueryTool(v), `{"project":"` + khProject + `","entity":"Alice"}`},
		{"vp_kg_timeline", KGTimelineTool(v), `{"project":"` + khProject + `","entity":"Alice"}`},
	} {
		wire := khCall(t, tc.tool, tc.params)
		mustHave(t, tc.name+" (hit)", wire, `"Alice"`)
		mustNotHave(t, tc.name+" (hit)", wire, `kg_status`, `kg_reason`)
	}
}

// topLevelKeys returns the keys of a JSON object's TOP level in wire order.
// 🔴 Needed because index_coverage_detail embeds search.Coverage, which now
// carries its own kg_status: a substring search for "kg_status" is satisfied by
// the NESTED copy and cannot see the top-level field at all (the third
// inert-guard question: does the fixture contain the thing the mutation acts
// on). This was found by the M15b probe, which left the first draft green.
func topLevelKeys(t *testing.T, wire string) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(wire))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not an object: %v %v", tok, err)
	}
	var keys []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func keyIndex(keys []string, k string) int {
	for i, x := range keys {
		if x == k {
			return i
		}
	}
	return -1
}

// T2: bootstrap carries the status as a TOP-LEVEL key beside kg_snapshot,
// before the terminal sentinel.
func TestBootstrapSaysWhyOnAnExcludedTier(t *testing.T) {
	v := khAuthoredVault(t)
	br := bootstrapFor(t, v)
	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatal(err)
	}
	if br.KGStatus != search.KGTierExcluded || !strings.Contains(br.KGReason, khWording) {
		t.Errorf("bootstrap top-level kg_status/kg_reason = %q / %q", br.KGStatus, br.KGReason)
	}
	keys := topLevelKeys(t, string(raw))
	a, b, c := keyIndex(keys, "kg_snapshot"), keyIndex(keys, "kg_status"), keyIndex(keys, "complete")
	if !(a >= 0 && b > a && c > b) {
		t.Errorf("top-level order: want kg_snapshot < kg_status < complete, got %d %d %d in %v", a, b, c, keys)
	}
}

// T2b2 (C1): the stop-class early return carries the status too. The decision
// is taken ABOVE that return, once, and the payload that takes it must not be
// an unexplained zero.
//
// Q1: the real bootstrapHandler. Q2: the dirty vault is the only difference
// from the normal-return test above. Q3: VaultDirt is asserted non-nil, so the
// early return really was taken.
//
// Mutation (M15): move the honesty decision below the stop-class gate.
func TestBootstrapStopClassReturnStillSaysWhy(t *testing.T) {
	sandboxHostEnv(t)
	v := newGitBackedTestVault(t)
	seedAuthoredProject(t, v, khProject)
	assertCoverageState(t, v, search.CoverageAuthored)
	dirtyFile(t, v.Root, "Projects/"+khProject+"/tasks/hand-edited.md",
		"# Hand edited\n\n**Status:** pending\n**Priority:** high\n\nbody\n")

	br := bootstrapOn(t, v)
	if br.VaultDirt == nil {
		t.Fatal("PRESENCE: the dirty vault raised no vault_dirt; the stop-class return was not taken")
	}
	raw, _ := json.Marshal(br)
	keys := topLevelKeys(t, string(raw))
	if keyIndex(keys, "kg_snapshot") < 0 || keyIndex(keys, "kg_status") < 0 {
		t.Errorf("the stop-class return lacks a top-level kg_snapshot/kg_status: %v", keys)
	}
	if br.KGStatus != search.KGTierExcluded {
		t.Errorf("stop-class kg_status = %q", br.KGStatus)
	}
}

// T2b (C1): coverage that cannot be read, on an empty KG, is NOT silence.
//
// The host-local project config is made undecodable, which fails LoadConfig in
// the coverage derivation while the KG (which reads no config) stays readable.
//
// Mutation (M9): treat a coverage error as "omit".
func TestAnEmptyKGWithUnreadableCoverageSaysSo(t *testing.T) {
	v := khAuthoredVault(t)
	khWriteHostProjectConfig(t, "this is = = not toml [[[")
	if _, err := coverageForProject(v, khProject); err == nil {
		t.Fatal("PRESENCE: coverage is still readable; the fixture does not exercise the unreadable path")
	}
	const unavailable = `"kg_status":"coverage_unavailable"`

	mustHave(t, "vp_kg_stats", khCall(t, KGStatsTool(v), `{"project":"`+khProject+`"}`), unavailable, "could not determine why")
	mustHave(t, "vp_kg_query", khCall(t, KGQueryTool(v), `{"project":"`+khProject+`","entity":"Nobody"}`), unavailable)
	mustHave(t, "vp_kg_timeline", khCall(t, KGTimelineTool(v), `{"project":"`+khProject+`","entity":"Nobody"}`), unavailable)

	resolver := vpctx.NewResolver(v.Root)
	out, err := BootstrapContextTool(resolver, v, nil).Handler(context.Background(), json.RawMessage(`{"project":"`+khProject+`"}`))
	if err != nil {
		t.Skipf("bootstrap itself refuses an unreadable project config (%v); nothing to explain there", err)
	}
	if br, ok := out.(BootstrapResult); !ok || br.KGStatus != search.KGCoverageUnavailable || !strings.Contains(br.KGReason, "could not determine why") {
		t.Errorf("bootstrap top-level kg_status/kg_reason on unreadable coverage: %+v", out)
	}

	// With a POPULATED graph nothing is being mis-explained: stay silent.
	khAddFact(t, v)
	mustNotHave(t, "vp_kg_stats populated", khCall(t, KGStatsTool(v), `{"project":"`+khProject+`"}`), "kg_status")
}

// T2c (C4): an empty KG on a host in another coverage state carries THAT
// state's label, never `not_built` and never `tier_excluded`.
//
// Mutation (M10): map absent/stale/legacy to unbuilt.
func TestAnEmptyKGCarriesItsOwnCoverageLabel(t *testing.T) {
	sandboxHostEnv(t)
	t.Run("unbuilt", func(t *testing.T) {
		v := bornCurrentTestVault(t, t.TempDir())
		seedNote(t, v, khProject)
		assertCoverageState(t, v, search.CoverageUnbuilt)
		wire := khCall(t, KGStatsTool(v), `{"project":"`+khProject+`"}`)
		mustHave(t, "unbuilt", wire, `"kg_status":"unbuilt"`)
		mustNotHave(t, "unbuilt", wire, "not_built", "tier_excluded")
	})
	t.Run("absent", func(t *testing.T) {
		v := bornCurrentTestVault(t, t.TempDir())
		testutil.InitProject(t, v.Root, khProject)
		assertCoverageState(t, v, search.CoverageAbsent)
		wire := khCall(t, KGStatsTool(v), `{"project":"`+khProject+`"}`)
		mustHave(t, "absent", wire, `"kg_status":"absent"`)
		mustNotHave(t, "absent", wire, "not_built", "tier_excluded")
	})
}

// T3: the control. The same empty KG on a host that INCLUDES transcripts must
// NOT claim an exclusion.
//
// Mutation (M2): the fixture flip is itself the probe; (M4): config-only
// predicate.
func TestAnIncludedTierHostDoesNotClaimExclusion(t *testing.T) {
	v := khAuthoredVault(t)
	khWriteHostProjectConfig(t, "[index]\ntranscripts = true\n")
	cov, err := coverageForProject(v, khProject)
	if err != nil {
		t.Fatal(err)
	}
	if cov.State == search.CoverageAuthored {
		t.Fatalf("PRESENCE: opting in left the project authored (%s); the control is inert", cov.Reason)
	}

	wire := khCall(t, KGStatsTool(v), `{"project":"`+khProject+`"}`)
	mustNotHave(t, "vp_kg_stats included", wire, "tier_excluded", "archive-extracted")
	mustHave(t, "vp_kg_stats included", wire, `"kg_status":"empty"`)
	for _, tool := range []mcp.Tool{KGQueryTool(v), KGTimelineTool(v)} {
		mustNotHave(t, tool.Name+" included", khCall(t, tool, `{"project":"`+khProject+`","entity":"Nobody"}`), "kg_status", "tier_excluded")
	}
	if br := bootstrapFor(t, v); br.KGStatus == search.KGTierExcluded {
		t.Errorf("bootstrap on an included-tier host claims %q", br.KGStatus)
	}
}

// T4: a populated KG on an included-tier host serializes exactly as before.
func TestAPopulatedIncludedTierKGIsUnchanged(t *testing.T) {
	sandboxHostEnv(t)
	v := bornCurrentTestVault(t, t.TempDir())
	seedNote(t, v, khProject)
	khAddFact(t, v)

	wire := khCall(t, KGStatsTool(v), `{"project":"`+khProject+`"}`)
	const golden = `{"entity_count":2,"triple_count":1,"current_facts":1,"expired_facts":0,"predicate_types":["knows"],"type_breakdown":{"person":2}}`
	if wire != golden {
		t.Errorf("vp_kg_stats of a populated KG changed.\n  got:  %s\n  want: %s", wire, golden)
	}
	mustNotHave(t, "vp_kg_query hit", khCall(t, KGQueryTool(v), `{"project":"`+khProject+`","entity":"Alice"}`), "kg_status", "kg_reason")
}

// T2 (traverse, C6): the not-found error carries ITS OWN palace-graph hint on
// an excluded tier; it asserts that text, not the KG status.
//
// Mutation (M14): change the hint text.
func TestTraverseNotFoundHintsAtTheExcludedTier(t *testing.T) {
	v := khAuthoredVault(t)
	_, err := TraverseTool(v).Handler(context.Background(), json.RawMessage(`{"project":"`+khProject+`","start":"nope/none"}`))
	if err == nil {
		t.Fatal("PRESENCE: traversing a missing node did not fail")
	}
	if !strings.Contains(err.Error(), "transcript-derived rooms are not in the graph") {
		t.Errorf("the not-found error does not say the transcript tier is excluded: %v", err)
	}
}

func TestTraverseNotFoundIsUnchangedOnAnIncludedTier(t *testing.T) {
	v := khAuthoredVault(t)
	khWriteHostProjectConfig(t, "[index]\ntranscripts = true\n")
	_, err := TraverseTool(v).Handler(context.Background(), json.RawMessage(`{"project":"`+khProject+`","start":"nope/none"}`))
	if err == nil {
		t.Fatal("PRESENCE: traversing a missing node did not fail")
	}
	if strings.Contains(err.Error(), "transcript") {
		t.Errorf("an included-tier host's traverse error claims a transcript exclusion: %v", err)
	}
}

// T2: vp_index_status (the MCP face of `vp index status`) carries the status.
func TestIndexStatusToolSaysWhyOnAnExcludedTier(t *testing.T) {
	v := khAuthoredVault(t)
	wire := khCall(t, IndexStatusTool(v), `{"project":"`+khProject+`"}`)
	mustHave(t, "vp_index_status", wire, khExcluded, khWording)
}

// T7: every surface's status and reason EQUAL what KGHonesty returns for the
// arguments that surface uses (cov, covErr, kgEmpty per the plan's
// convention), so a surface that re-derives from config, or words its own
// reason, fails here.
//
// Mutation: hard-code a different reason string in one surface.
func TestEverySurfaceAgreesWithKGHonesty(t *testing.T) {
	v := khAuthoredVault(t)
	cov, err := coverageForProject(v, khProject)
	if err != nil {
		t.Fatal(err)
	}
	wantSt, wantWhy := search.KGHonesty(&cov, nil, true)
	if wantSt != search.KGTierExcluded {
		t.Fatalf("PRESENCE: the helper says %q for the authored fixture", wantSt)
	}
	call := func(tool mcp.Tool, params string) any {
		out, err := tool.Handler(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		return out
	}
	p := `{"project":"` + khProject + `"}`
	q := `{"project":"` + khProject + `","entity":"Nobody"}`
	stats := call(KGStatsTool(v), p).(kgStatsResult)
	query := call(KGQueryTool(v), q).(kgTripleListResult)
	tl := call(KGTimelineTool(v), q).(kgTripleListResult)
	idx := call(IndexStatusTool(v), p).(search.Coverage)
	br := bootstrapFor(t, v)
	for _, c := range []struct {
		who    string
		st     search.KGStatus
		reason string
	}{
		{"vp_kg_stats", stats.KGStatus, stats.KGReason},
		{"vp_kg_query", query.KGStatus, query.KGReason},
		{"vp_kg_timeline", tl.KGStatus, tl.KGReason},
		{"vp_index_status", idx.KGStatus, idx.KGReason},
		{"bootstrap", br.KGStatus, br.KGReason},
	} {
		if c.st != wantSt || c.reason != wantWhy {
			t.Errorf("%s: (%q, %q) disagrees with KGHonesty (%q, %q)", c.who, c.st, c.reason, wantSt, wantWhy)
		}
	}
}

// C1 once-per-bootstrap: the honesty decision and the index_coverage fields
// share ONE CoverageState derivation (no second derivation below the gate).
// Counted through the production archive-manifest seam: a derivation lists the
// transcript directory, so the count of ListEntries calls is the count of
// derivations for the bootstrapped project. The OTHER projects' tally is
// absent here (one project in the vault).
func TestBootstrapDerivesProjectCoverageOnce(t *testing.T) {
	v := khAuthoredVault(t)
	n := 0
	defer search.SetCoverageListEntriesHook(func(root, project string) ([]*archive.Entry, error) {
		if project == khProject {
			n++
		}
		return archive.ListEntries(root, project)
	})()
	_ = bootstrapFor(t, v)
	if n == 0 {
		t.Fatal("PRESENCE: bootstrap derived no coverage at all")
	}
	if n > 1 {
		t.Errorf("bootstrap derived the project's coverage %d times; the KG decision and index_coverage must share one", n)
	}
}

// C5 fast path: a NON-empty query/timeline answer derives no coverage at all
// (coverage is a manifest scan per call); an empty one derives it. Counted
// through the production archive-manifest seam.
//
// Mutation (M13): derive coverage on a non-empty result.
func TestAHitDerivesNoCoverage(t *testing.T) {
	v := khAuthoredVault(t)
	khAddFact(t, v)
	n := 0
	defer search.SetCoverageListEntriesHook(func(root, project string) ([]*archive.Entry, error) {
		n++
		return archive.ListEntries(root, project)
	})()
	for _, tool := range []mcp.Tool{KGQueryTool(v), KGTimelineTool(v)} {
		khCall(t, tool, `{"project":"`+khProject+`","entity":"Alice"}`)
	}
	if n != 0 {
		t.Errorf("a non-empty answer derived coverage %d times; the fast path must derive none", n)
	}
	khCall(t, KGQueryTool(v), `{"project":"`+khProject+`","entity":"Nobody"}`)
	if n == 0 {
		t.Error("PRESENCE: an empty answer derived no coverage, so the counter cannot see a derivation")
	}
}
