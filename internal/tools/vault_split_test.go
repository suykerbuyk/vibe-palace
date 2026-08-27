// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// Every test in this file runs against an EPHEMERAL vault under t.TempDir().
// None of them touches the operator's bound vault, and none of them names a
// real confidentiality slug: the fixtures are `alpha` and `beta`, which exist
// nowhere.

// splitFixtureVault builds a format-1 vault with the given slugs present in
// BOTH trees, each carrying one file per tree plus the artifacts the subtract
// set is supposed to remove.
func splitFixtureVault(t *testing.T, slugs ...string) string {
	t.Helper()
	root := t.TempDir()

	if err := surface.WriteFormat(root, surface.RequiredDataFormat); err != nil {
		t.Fatalf("stamp vault format: %v", err)
	}

	for _, s := range slugs {
		writeSplitFile(t, root, "palace/"+s+"/kg/entities.jsonl", `{"id":"e1"}`)
		writeSplitFile(t, root, "palace/"+s+"/drawers/w/r/drawers.jsonl", `{"id":"d1"}`)
		// Subtract-set specimens on the store side.
		writeSplitFile(t, root, "palace/"+s+"/.surface", "surface = 1\n")
		writeSplitFile(t, root, "palace/"+s+"/.local/embed-cache/d1.vec", "cache")

		writeSplitFile(t, root, "Projects/"+s+"/resume.md", "# resume\n")
		writeSplitFile(t, root, "Projects/"+s+"/iterations.md", "# iterations\n")
		writeSplitFile(t, root, "Projects/"+s+"/commit-log.md", "landed commits\n")
		// Subtract-set specimens on the history side. commit-log.anchor is the
		// one this slice must prove absent from the hashed inventory.
		writeSplitFile(t, root, "Projects/"+s+"/commit-log.anchor", "b35abe3\n")
		writeSplitFile(t, root, "Projects/"+s+"/.surface", "surface = 1\n")
	}
	return root
}

func writeSplitFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func splitPlanParams(dest string, slugs ...string) vaultSplitParams {
	return vaultSplitParams{Action: "plan", Slugs: slugs, Destination: dest}
}

// callSplit invokes the registered handler the way MCP would, so the tests
// exercise the tool's real entry point rather than only its internals.
func callSplit(t *testing.T, root string, p vaultSplitParams) (map[string]any, error) {
	t.Helper()
	tool := VaultSplitTool(storage.NewVault(root))
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	res, err := tool.Handler(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	// Round-trip through JSON so the assertions see the wire shape a client
	// gets, not the Go struct.
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Mutation proof 1: an unknown slug REFUSES.
// ---------------------------------------------------------------------------

// TestVaultSplitPlan_UnknownSlugRefuses pins the allow-list's failure mode.
//
// The mutation this kills: dropping the unknown-slug check so a typo plans an
// empty selection. That produces a manifest whose digest is perfectly valid and
// whose content is nothing — the operator would only find out after apply, on a
// destination that is quietly missing a project. Inclusion is an allow-list, so
// a name that matches nothing is a refusal, never an empty set.
func TestVaultSplitPlan_UnknownSlugRefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := filepath.Join(t.TempDir(), "new-vault")

	_, err := callSplit(t, root, splitPlanParams(dest, "alpha", "nosuchproject"))
	if err == nil {
		t.Fatal("plan accepted an unknown slug; it must refuse")
	}
	if !strings.Contains(err.Error(), "nosuchproject") {
		t.Errorf("refusal does not name the offending slug: %v", err)
	}
	if !apperr.IsCaller(err) {
		t.Errorf("unknown slug is a caller fault, got a system fault: %v", err)
	}

	// And the same call without the bad slug must succeed, or the test above
	// would also pass on a plan that refuses everything.
	if _, err := callSplit(t, root, splitPlanParams(dest, "alpha")); err != nil {
		t.Fatalf("plan for a known slug failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 2: a symlink in an allow-listed tree is a PLAN REFUSAL.
// ---------------------------------------------------------------------------

// TestVaultSplitPlan_SymlinkInAllowListedTreeRefuses pins the Lstat contract.
//
// The mutation this kills is either of the two wrong answers:
//
//   - SKIP: the path is absent from the manifest and absent from the
//     destination, and nothing downstream ever says so.
//   - FOLLOW: the link's target is read and filed under an allow-listed slug.
//     The target can be another project, or outside the vault entirely, and the
//     destination-side membership gate only ever sees the allow-listed slug —
//     so a follow leaks bytes past every check that comes after this one.
//
// The link here points at a file belonging to a slug that is NOT in the
// allow-list, which is exactly the leak shape.
func TestVaultSplitPlan_SymlinkInAllowListedTreeRefuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows")
	}
	root := splitFixtureVault(t, "alpha", "beta")
	dest := filepath.Join(t.TempDir(), "new-vault")

	// Baseline: alpha plans clean before the link exists, so a later failure is
	// attributable to the link and not to the fixture.
	if _, err := callSplit(t, root, splitPlanParams(dest, "alpha")); err != nil {
		t.Fatalf("baseline plan failed: %v", err)
	}

	link := filepath.Join(root, "Projects", "alpha", "leaked.md")
	target := filepath.Join(root, "Projects", "beta", "resume.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot create symlink on this filesystem: %v", err)
	}

	_, err := callSplit(t, root, splitPlanParams(dest, "alpha"))
	if err == nil {
		t.Fatal("plan accepted a symlink in an allow-listed tree; it must refuse")
	}
	if !strings.Contains(err.Error(), "Projects/alpha/leaked.md") {
		t.Errorf("refusal does not name the offending path: %v", err)
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("refusal does not say why: %v", err)
	}
}

// TestVaultSplitPlan_SymlinkUnderPrunedDirDoesNotRefuse is the other half of
// the same contract, and it is why the refusal is scoped rather than global.
//
// palace/{p}/.local is machine-local: it never travels, so its contents are not
// this tool's business. A walk that refused on a link inside an embed cache
// would fail plans over bytes that were never going to move. Pruned means never
// descended, so the link is never seen.
func TestVaultSplitPlan_SymlinkUnderPrunedDirDoesNotRefuse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows")
	}
	root := splitFixtureVault(t, "alpha")
	dest := filepath.Join(t.TempDir(), "new-vault")

	link := filepath.Join(root, "palace", "alpha", ".local", "embed-cache", "linked.vec")
	if err := os.Symlink(filepath.Join(root, "Projects", "alpha", "resume.md"), link); err != nil {
		t.Skipf("cannot create symlink on this filesystem: %v", err)
	}

	if _, err := callSplit(t, root, splitPlanParams(dest, "alpha")); err != nil {
		t.Fatalf("a symlink inside a pruned machine-local tree must not refuse a plan: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Mutation proof 3: commit-log.anchor is ABSENT from the hashed inventory.
// ---------------------------------------------------------------------------

// TestVaultSplitPlan_SubtractSetIsAbsentFromInventory pins the minus-set.
//
// commit-log.anchor holds the SHA of a source-vault commit. A destination is a
// fresh repository, so that SHA does not exist there and the file is a dangling
// reference the moment it lands. commit-log.md — the history it anchors — DOES
// travel; only the anchor is false in the destination.
//
// The mutation this kills is dropping the anchor from the minus-set while
// leaving it on the do-not-copy list. The two are the SAME set: a path in the
// manifest but not copied fails verify, and a path copied to satisfy the
// manifest lands bytes that are false. This asserts on the manifest rows
// directly, because the payload deliberately does not carry them.
func TestVaultSplitPlan_SubtractSetIsAbsentFromInventory(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := filepath.Join(t.TempDir(), "new-vault")

	m, err := buildSplitManifest(storage.NewVault(root), splitPlanParams(dest, "alpha"))
	if err != nil {
		t.Fatalf("buildSplitManifest: %v", err)
	}

	got := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		got[e.Path] = true
	}

	for _, subtracted := range []string{
		"Projects/alpha/commit-log.anchor",
		"Projects/alpha/.surface",
		"palace/alpha/.surface",
		"palace/alpha/.local/embed-cache/d1.vec",
	} {
		if got[subtracted] {
			t.Errorf("%s is in the hashed inventory; the subtract set must remove it", subtracted)
		}
	}

	// The fixture writes every one of those files, so an inventory that lost
	// them by walking nothing at all would pass the loop above. Pin what MUST
	// travel — including commit-log.md, whose anchor is the file being removed.
	for _, kept := range []string{
		"Projects/alpha/resume.md",
		"Projects/alpha/iterations.md",
		"Projects/alpha/commit-log.md",
		"palace/alpha/kg/entities.jsonl",
		"palace/alpha/drawers/w/r/drawers.jsonl",
	} {
		if !got[kept] {
			t.Errorf("%s is missing from the hashed inventory; it must travel", kept)
		}
	}
}

// ---------------------------------------------------------------------------
// Supporting refusals and contracts
// ---------------------------------------------------------------------------

// TestVaultSplitPlan_DestinationInsideVaultRefuses pins the inverse containment
// check at PLAN time. A destination inside the vault was never going to be
// legal; discovering that at apply wastes an operator's approval of a manifest.
func TestVaultSplitPlan_DestinationInsideVaultRefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")

	inside := filepath.Join(root, "Projects", "alpha", "spawn")
	_, err := callSplit(t, root, splitPlanParams(inside, "alpha"))
	if err == nil {
		t.Fatal("plan accepted a destination inside the bound vault")
	}
	if !apperr.IsCaller(err) {
		t.Errorf("destination refusal is a caller fault, got a system fault: %v", err)
	}
}

// TestVaultSplitPlan_Format0SourceRefuses pins the read gate.
//
// Format is a READ gate on KG accessors: a format-0 vault holds triple files in
// the old encoding. Hashing them and later stamping the destination format 1
// produces a destination that LOOKS current while its KG reads silently
// undercount. Refusing here means apply never starts on such a tree.
func TestVaultSplitPlan_Format0SourceRefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := filepath.Join(t.TempDir(), "new-vault")

	// Unstamp: absence of the manifest IS format 0.
	if err := os.RemoveAll(filepath.Join(root, ".vibe-palace")); err != nil {
		t.Fatal(err)
	}

	_, err := callSplit(t, root, splitPlanParams(dest, "alpha"))
	if err == nil {
		t.Fatal("plan accepted a format-0 source vault")
	}
	if !strings.Contains(err.Error(), "data format") {
		t.Errorf("refusal does not name the format gate: %v", err)
	}
}

// TestVaultSplitPlan_NonPlanActionRefuses pins that the handler re-checks the
// action rather than trusting the enum. A tool whose refusal lives only in a
// schema silently gains an action the day the schema is widened.
func TestVaultSplitPlan_NonPlanActionRefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := filepath.Join(t.TempDir(), "new-vault")

	for _, action := range []string{"apply", "verify", "purge", ""} {
		p := splitPlanParams(dest, "alpha")
		p.Action = action
		if _, err := callSplit(t, root, p); err == nil {
			t.Errorf("action %q was accepted; only plan is implemented", action)
		}
	}
}

// TestVaultSplitPlan_EmptySlugsRefuses pins that an empty allow-list selects
// nothing rather than everything.
func TestVaultSplitPlan_EmptySlugsRefuses(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := filepath.Join(t.TempDir(), "new-vault")

	if _, err := callSplit(t, root, splitPlanParams(dest)); err == nil {
		t.Fatal("plan accepted an empty slug list")
	}
}

// TestVaultSplitPlan_ReportsDriftAndDoesNotGuess pins that a slug present in
// only one tree is REPORTED, not resolved. Drift is the common case in a real
// vault, and its disposition is owned by another task.
func TestVaultSplitPlan_ReportsDriftAndDoesNotGuess(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := filepath.Join(t.TempDir(), "new-vault")

	// `orphan` has history and no store: Projects-only drift.
	writeSplitFile(t, root, "Projects/orphan/resume.md", "# orphan\n")

	res, err := callSplit(t, root, splitPlanParams(dest, "orphan"))
	if err != nil {
		t.Fatalf("a drift-only slug must plan, not refuse: %v", err)
	}

	drift, _ := res["drift"].([]any)
	if len(drift) != 1 {
		t.Fatalf("drift rows = %d, want 1: %v", len(drift), res["drift"])
	}
	row := drift[0].(map[string]any)
	if row["slug"] != "orphan" || row["in_palace"] != false || row["in_projects"] != true {
		t.Errorf("drift row does not describe the orphan: %v", row)
	}
	if row["requested"] != true {
		t.Errorf("drift row does not mark the slug as requested: %v", row)
	}

	// The palace side simply contributes nothing; it is not an error.
	trees, _ := res["trees"].([]any)
	if len(trees) != 2 {
		t.Fatalf("tree rows = %d, want 2", len(trees))
	}
	for _, tr := range trees {
		row := tr.(map[string]any)
		if row["tree"] == "palace/orphan" && row["present"] != false {
			t.Errorf("absent store tree reported present: %v", row)
		}
	}
}

// TestVaultSplitPlan_ManifestDigestBindsTheRequest pins that the digest covers
// the allow-list and the include flags, not only the bytes.
//
// Two requests that happen to select identical bytes are still different
// splits. A later apply that re-hashes the source must not accept a digest
// minted for a different allow-list.
func TestVaultSplitPlan_ManifestDigestBindsTheRequest(t *testing.T) {
	root := splitFixtureVault(t, "alpha", "beta")
	dest := filepath.Join(t.TempDir(), "new-vault")
	vault := storage.NewVault(root)

	base, err := buildSplitManifest(vault, splitPlanParams(dest, "alpha"))
	if err != nil {
		t.Fatal(err)
	}

	// Same request, twice: the digest is stable.
	again, err := buildSplitManifest(vault, splitPlanParams(dest, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	if base.SHA256 != again.SHA256 {
		t.Errorf("digest is not stable across identical plans: %s vs %s", base.SHA256, again.SHA256)
	}

	// Order of the slug list must not change the digest.
	reordered, err := buildSplitManifest(vault, splitPlanParams(dest, "beta", "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	sorted, err := buildSplitManifest(vault, splitPlanParams(dest, "alpha", "beta"))
	if err != nil {
		t.Fatal(err)
	}
	if reordered.SHA256 != sorted.SHA256 {
		t.Errorf("digest depends on the order the caller typed the slugs")
	}

	// A different allow-list is a different digest.
	if base.SHA256 == sorted.SHA256 {
		t.Errorf("one-slug and two-slug plans share a digest")
	}

	// An include flag is part of the request even when it selects no bytes:
	// this fixture has no Knowledge/ at all, so the entry list is identical and
	// only the request binding can distinguish the two.
	withLearnings := splitPlanParams(dest, "alpha")
	withLearnings.IncludeLearnings = true
	flagged, err := buildSplitManifest(vault, withLearnings)
	if err != nil {
		t.Fatal(err)
	}
	if len(flagged.Entries) != len(base.Entries) {
		t.Fatalf("fixture has no learnings; entry counts should match (%d vs %d)",
			len(flagged.Entries), len(base.Entries))
	}
	if flagged.SHA256 == base.SHA256 {
		t.Errorf("include_learnings does not bind the digest")
	}
}

// TestVaultSplitPlan_ReportsVaultGlobalArtifactsLeftBehind pins that the leak
// surface is reported rather than left to inference. Vault-global artifacts do
// not partition by slug, so an operator cannot derive what stays behind from
// the allow-list.
func TestVaultSplitPlan_ReportsVaultGlobalArtifactsLeftBehind(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := filepath.Join(t.TempDir(), "new-vault")

	writeSplitFile(t, root, "Knowledge/learnings/one.md", "a learning\n")
	writeSplitFile(t, root, "Audits/2026-08-22.md", "an audit\n")
	writeSplitFile(t, root, "Templates/skills/x.md", "a template\n")

	res, err := callSplit(t, root, splitPlanParams(dest, "alpha"))
	if err != nil {
		t.Fatal(err)
	}

	classes := map[string]map[string]any{}
	for _, g := range res["vault_global"].([]any) {
		row := g.(map[string]any)
		classes[row["class"].(string)] = row
	}
	for _, want := range []string{"learnings", "audits", "templates"} {
		row, ok := classes[want]
		if !ok {
			t.Fatalf("vault-global class %q is not reported", want)
		}
		if row["files"].(float64) != 1 {
			t.Errorf("%s files = %v, want 1", want, row["files"])
		}
		if row["included"] != false {
			t.Errorf("%s is included by default; copy-none is the fail-closed default", want)
		}
	}

	// The defaults leave them behind, so they must not be in the inventory.
	m, err := buildSplitManifest(storage.NewVault(root), splitPlanParams(dest, "alpha"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Entries {
		if strings.HasPrefix(e.Path, "Knowledge/") ||
			strings.HasPrefix(e.Path, "Audits/") ||
			strings.HasPrefix(e.Path, "Templates/") {
			t.Errorf("vault-global path %s travelled without an include flag", e.Path)
		}
	}

	// include_learnings must actually move the file, or the flag is decoration.
	withLearnings := splitPlanParams(dest, "alpha")
	withLearnings.IncludeLearnings = true
	m2, err := buildSplitManifest(storage.NewVault(root), withLearnings)
	if err != nil {
		t.Fatal(err)
	}
	var sawLearning bool
	for _, e := range m2.Entries {
		if e.Path == "Knowledge/learnings/one.md" {
			sawLearning = true
		}
		if strings.HasPrefix(e.Path, "Audits/") {
			t.Errorf("include_learnings pulled in an audit: %s", e.Path)
		}
	}
	if !sawLearning {
		t.Error("include_learnings did not add the learning to the inventory")
	}
}

// TestVaultSplitPlan_WritesNothing is the claim the read-only predicate makes,
// checked rather than asserted: the whole vault's file set must be byte-identical
// after a plan. A predicate that admits a call which writes is the one failure
// the surface gate exists to prevent.
func TestVaultSplitPlan_WritesNothing(t *testing.T) {
	root := splitFixtureVault(t, "alpha")
	dest := filepath.Join(t.TempDir(), "new-vault")

	before := snapshotTree(t, root)
	if _, err := callSplit(t, root, splitPlanParams(dest, "alpha")); err != nil {
		t.Fatal(err)
	}
	after := snapshotTree(t, root)

	if len(before) != len(after) {
		t.Fatalf("plan changed the vault's file count: %d -> %d", len(before), len(after))
	}
	for p, sum := range before {
		if after[p] != sum {
			t.Errorf("plan modified %s", p)
		}
	}

	// And it did not create the destination. plan validates the path; only
	// apply may bring it into existence.
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("plan created the destination at %s", dest)
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		sum, _, herr := hashFile(p)
		if herr != nil {
			return herr
		}
		out[vaultRel(root, p)] = sum
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return out
}

// TestVaultSplitReadOnlyPredicate pins the gate refinement itself, including
// its fail-closed edges: an unparseable payload and an unrecognised action must
// both gate.
func TestVaultSplitReadOnlyPredicate(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   bool
	}{
		{"plan is admitted", `{"action":"plan","slugs":["alpha"],"destination":"/tmp/x"}`, true},
		{"apply gates", `{"action":"apply","slugs":["alpha"],"destination":"/tmp/x"}`, false},
		{"absent action gates", `{"slugs":["alpha"]}`, false},
		{"null gates", `null`, false},
		{"unparseable gates", `{"action":`, false},
		{"wrong type gates", `{"action":7}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vaultSplitReadOnly(json.RawMessage(tc.params)); got != tc.want {
				t.Errorf("vaultSplitReadOnly(%s) = %v, want %v", tc.params, got, tc.want)
			}
		})
	}
}

// TestVaultSplitToolIsGatedAndParamAware pins the three declarations that must
// move together: the constructor's Mutating flag, the predicate, and the
// canonical lists. A tool carrying a predicate but missing from
// ParamAwareToolNames widens what a stale binary admits without saying so.
func TestVaultSplitToolIsGatedAndParamAware(t *testing.T) {
	tool := VaultSplitTool(storage.NewVault(t.TempDir()))
	if !tool.Mutating {
		t.Error("vp_vault_split must be registered Mutating: apply/verify/purge will write")
	}
	if tool.ReadOnlyWhen == nil {
		t.Error("vp_vault_split must carry a ReadOnlyWhen predicate, or plan is refused by a stale binary")
	}
	if !containsSplitName(MutatingToolNames, "vp_vault_split") {
		t.Error("vp_vault_split is missing from MutatingToolNames")
	}
	if !containsSplitName(ParamAwareToolNames, "vp_vault_split") {
		t.Error("vp_vault_split is missing from ParamAwareToolNames")
	}
	// The read-only serve surface is an affirmative allow-list and must NOT
	// gain a tool that can also be called in a writing mode.
	if containsSplitName(ReadOnlyServeToolNames, "vp_vault_split") {
		t.Error("vp_vault_split must never appear on ReadOnlyServeToolNames")
	}
}

func containsSplitName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}
