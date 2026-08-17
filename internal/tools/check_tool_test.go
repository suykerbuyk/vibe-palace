// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// callCheck runs the handler and asserts a clean CheckSuiteResult back.
func callCheck(t *testing.T, vault *storage.Vault, params map[string]any) CheckSuiteResult {
	t.Helper()
	tool := CheckTool(vault)
	raw, _ := json.Marshal(params)
	res, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out, ok := res.(CheckSuiteResult)
	if !ok {
		t.Fatalf("result type = %T, want CheckSuiteResult", res)
	}
	return out
}

// writeResume creates <vault>/Projects/<slug>/resume.md with the given body.
// Ported with the resume-refs cases from the retired resume_refs_tool_test.go.
func writeResume(t *testing.T, vaultRoot, slug, body string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write resume: %v", err)
	}
}

// row returns the named check row, or fails.
func row(t *testing.T, out CheckSuiteResult, name string) CheckRow {
	t.Helper()
	for _, r := range out.Checks {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %q row in %+v", name, out.Checks)
	return CheckRow{}
}

func TestCheckTool_Name(t *testing.T) {
	if got := CheckTool(storage.NewVault(t.TempDir())).Name; got != "vp_check" {
		t.Errorf("tool name = %q, want vp_check", got)
	}
}

func TestCheckTool_NotMutating(t *testing.T) {
	if got := CheckTool(storage.NewVault(t.TempDir())).Mutating; got {
		t.Fatalf("vp_check must be read-only (Mutating=false), got %v", got)
	}
}

// TestCheckTool_ConstructorDoesNotTouchVault pins N2: registeredToolCount
// (cmd/vp/cmd_check.go) and cmd/vp/tool_surface_golden_test.go both register the
// whole tool set against an EMPTY vault root purely to count tools. A
// constructor that resolves producers, validates the root, or precomputes
// anything against the vault breaks `vp check --json` and the golden test in a
// place with no obvious connection to this tool.
func TestCheckTool_ConstructorDoesNotTouchVault(t *testing.T) {
	// An empty root must construct without panicking and yield a complete tool.
	tool := CheckTool(storage.NewVault(""))
	if tool.Name == "" || tool.Handler == nil || len(tool.Schema) == 0 {
		t.Fatalf("constructor against empty vault produced an incomplete tool: %+v", tool)
	}

	// Constructing against a real vault must not read or write it: the
	// directory contents are byte-identical afterwards.
	root := t.TempDir()
	writeResume(t, root, "untouched", "# untouched\n")
	before := treeSnapshot(t, root)
	_ = CheckTool(storage.NewVault(root))
	if after := treeSnapshot(t, root); after != before {
		t.Errorf("constructor performed vault I/O:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestCheckTool_RegistersAgainstEmptyVault is the direct regression for the
// counting paths above: the full registry must build against storage.NewVault("").
func TestCheckTool_RegistersAgainstEmptyVault(t *testing.T) {
	v := storage.NewVault("")
	srv := mcp.NewServer(v)
	RegisterAll(srv.Registry(), vpctx.NewResolver(""), v, nil)

	found := false
	for _, tl := range srv.Registry().List() {
		if tl.Name == "vp_check" {
			found = true
		}
	}
	if !found {
		t.Fatalf("vp_check not registered against an empty vault root")
	}
}

// treeSnapshot renders a stable description of every file under root (path,
// size, mode) so a test can assert nothing was created, removed, or rewritten.
func treeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if info.IsDir() {
			fmt.Fprintf(&b, "d %s\n", rel)
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(&b, "f %s %d %x\n", rel, info.Size(), sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return b.String()
}

// TestCheckStatusProjection pins the whole four-state mapping. check.Status is
// `type Status int`, so a missed case would ship bare integers to agents — and
// the selector set spans checks that can Fail (surface) and checks that never
// do (the advisory scans), so all four states are reachable here.
func TestCheckStatusProjection(t *testing.T) {
	cases := []struct {
		in   check.Status
		want string
	}{
		{check.Pass, "pass"},
		{check.Info, "info"},
		{check.Skip, "skip"},
		{check.Fail, "fail"},
	}
	for _, c := range cases {
		if got := checkStatusString(c.in); got != c.want {
			t.Errorf("checkStatusString(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCheckAggregateIsAdvisoryWorstOf documents H3/N3: the roll-up is worst-of,
// and it is ADVISORY. It cannot be authoritative because the underlying checks
// legitimately disagree about an absent vault — a vanished root has surface
// reporting Fail while resume-refs reports Pass — so a single verdict would be
// wrong for four of the five rows. Consumers key off the rows.
func TestCheckAggregateIsAdvisoryWorstOf(t *testing.T) {
	cases := []struct {
		name string
		in   []check.Result
		want check.Status
	}{
		{"all pass", []check.Result{{Status: check.Pass}, {Status: check.Pass}}, check.Pass},
		{"skip outranks pass", []check.Result{{Status: check.Pass}, {Status: check.Skip}}, check.Skip},
		{"info outranks skip", []check.Result{{Status: check.Skip}, {Status: check.Info}}, check.Info},
		{"fail outranks all", []check.Result{{Status: check.Info}, {Status: check.Fail}, {Status: check.Pass}}, check.Fail},
		{"empty", nil, check.Pass},
	}
	for _, c := range cases {
		if got := checkAggregateStatus(c.in); got != c.want {
			t.Errorf("%s: aggregate = %v, want %v", c.name, got, c.want)
		}
	}

	if got := checkSummaryLine([]check.Result{
		{Status: check.Pass}, {Status: check.Info}, {Status: check.Skip}, {Status: check.Fail},
	}); got != "4 checks: 1 pass, 1 info, 1 skip, 1 fail" {
		t.Errorf("summary = %q", got)
	}
}

// TestCheckTool_DefaultRunsEveryProducer covers H2: an omitted `checks`
// argument must run the whole cheap suite, never error. Agents and templates
// omit optional arguments constantly.
func TestCheckTool_DefaultRunsEveryProducer(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	out := callCheck(t, vault, map[string]any{})

	if len(out.Checks) != len(check.ProducerOrder) {
		t.Fatalf("ran %d checks, want %d (one per producer): %+v",
			len(out.Checks), len(check.ProducerOrder), out.Checks)
	}
	if out.Summary == "" {
		t.Errorf("summary must be a one-line tally, got empty")
	}
	for _, r := range out.Checks {
		switch r.Status {
		case "pass", "info", "skip", "fail":
		default:
			t.Errorf("row %q has unmapped status %q (check.Status is an int; it must never marshal raw)", r.Name, r.Status)
		}
	}
}

// TestCheckTool_DeterministicOrder covers N1: Go randomizes map iteration and
// the default-all path is the first iteration of check.Producers anywhere. The
// declared order must make repeat runs byte-identical in ordering.
func TestCheckTool_DeterministicOrder(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	names := func(out CheckSuiteResult) []string {
		var n []string
		for _, r := range out.Checks {
			n = append(n, r.Name)
		}
		return n
	}

	first := names(callCheck(t, vault, map[string]any{}))
	for i := range 8 {
		got := names(callCheck(t, vault, map[string]any{}))
		if strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("run %d reordered checks:\n first: %v\n got:   %v", i+2, first, got)
		}
	}

	// And the order is the DECLARED one, not an accident of the map.
	var want []string
	for _, sel := range check.ProducerOrder {
		for _, r := range check.Producers[sel](vault.Root) {
			want = append(want, r.Name)
		}
	}
	if strings.Join(first, ",") != strings.Join(want, ",") {
		t.Errorf("checks[] order = %v, want ProducerOrder %v", first, want)
	}
}

// TestCheckTool_SelectorSubset proves an explicit list runs exactly what it names.
func TestCheckTool_SelectorSubset(t *testing.T) {
	vault := storage.NewVault(t.TempDir())

	out := callCheck(t, vault, map[string]any{"checks": []string{"resume-refs"}})

	if len(out.Checks) != 1 {
		t.Fatalf("ran %d checks for a one-name filter: %+v", len(out.Checks), out.Checks)
	}
	if out.Checks[0].Name != "Resume refs" {
		t.Errorf("row name = %q, want Resume refs", out.Checks[0].Name)
	}
}

// TestCheckTool_UnknownNameErrors keeps the CLI's semantics: an unknown name is
// an error, never a silent skip that reports a clean bill of health.
func TestCheckTool_UnknownNameErrors(t *testing.T) {
	tool := CheckTool(storage.NewVault(t.TempDir()))
	raw, _ := json.Marshal(map[string]any{"checks": []string{"resume-refs", "no-such-check"}})

	if _, err := tool.Handler(context.Background(), raw); err == nil {
		t.Fatalf("unknown check name must error")
	}
}

// TestCheckTool_ResumeRefsBreaches is ported from the retired
// vp_check_resume_refs wrapper: a resume committing host-local plan references
// is reported as info with each offending path enumerated, and the fenced
// reference is ignored. It also pins that details survives as an ARRAY —
// check.ToJSON's folded string would lose the per-breach structure.
func TestCheckTool_ResumeRefsBreaches(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	body := "## State\n\n" +
		"plan: ~/.claude/plans/active.md\n" +
		"old:  /home/dev/.claude/plans/older.md\n\n" +
		"```\n~/.claude/plans/ignored.md\n```\n"
	writeResume(t, vault.Root, "refproj", body)

	out := callCheck(t, vault, map[string]any{"checks": []string{"resume-refs"}})

	r := row(t, out, "Resume refs")
	if r.Status != "info" {
		t.Fatalf("status = %q, want info\n%+v", r.Status, out)
	}
	if len(r.Details) < 2 {
		t.Fatalf("details must stay an array of per-breach lines, got %v", r.Details)
	}
	joined := strings.Join(r.Details, "\n")
	for _, want := range []string{"~/.claude/plans/active.md", "/home/dev/.claude/plans/older.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("details missing %q: %v", want, r.Details)
		}
	}
	if strings.Contains(joined, "ignored.md") {
		t.Errorf("fenced reference must be ignored: %v", r.Details)
	}
}

// TestCheckTool_ResumeRefsClean is the other half of the ported wrapper case.
func TestCheckTool_ResumeRefsClean(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	writeResume(t, vault.Root, "tidy", "# tidy\n\nSee tasks/done/task-1.md.\n")

	out := callCheck(t, vault, map[string]any{"checks": []string{"resume-refs"}})

	r := row(t, out, "Resume refs")
	if r.Status != "pass" {
		t.Errorf("status = %q, want pass\n%+v", r.Status, out)
	}
	if len(r.Details) != 0 {
		t.Errorf("details = %v, want empty on pass", r.Details)
	}
}

// TestCheckTool_EmptyVaultRoot is the N4 port: one of only two tests anywhere
// that dispatch a read-only check tool against Root == "". No vault configured →
// skip (non-halting), mirroring the check's own contract.
func TestCheckTool_EmptyVaultRoot(t *testing.T) {
	out := callCheck(t, storage.NewVault(""), map[string]any{"checks": []string{"resume-refs"}})

	r := row(t, out, "Resume refs")
	if r.Status != "skip" {
		t.Errorf("status = %q, want skip for empty vault path", r.Status)
	}

	// The whole suite must also survive an empty root without panicking, and
	// the aggregate stays advisory: the rows disagree (surface reports info,
	// the vault-scoped checks report skip), which is exactly why consumers key
	// off the rows.
	all := callCheck(t, storage.NewVault(""), map[string]any{})
	if len(all.Checks) != len(check.ProducerOrder) {
		t.Fatalf("empty-root default run produced %d rows, want %d", len(all.Checks), len(check.ProducerOrder))
	}
}

// TestCheckTool_UsesBoundVaultNotCwd is the H1 regression guard. The handler
// must scan the vault bound at REGISTRATION, never re-resolve one from the
// process working directory: `vp mcp` is long-lived and its cwd is the host's
// launch directory, not the agent's project.
func TestCheckTool_UsesBoundVaultNotCwd(t *testing.T) {
	// The vault the tool is bound to.
	bound := storage.NewVault(t.TempDir())
	writeResume(t, bound.Root, "boundproj", "plan: ~/.claude/plans/bound.md\n")

	// A DIFFERENT vault, which the process cwd resolves to via a
	// .vibe-palace.toml override — the exact path runSelectedChecks takes.
	decoy := t.TempDir()
	writeResume(t, decoy, "decoyproj", "plan: ~/.claude/plans/decoy.md\n")
	cwd := t.TempDir()
	toml := "vault_path = " + strconv.Quote(decoy) + "\n"
	if err := os.WriteFile(filepath.Join(cwd, ".vibe-palace.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write .vibe-palace.toml: %v", err)
	}
	t.Chdir(cwd)

	// Precondition: cwd really does resolve to the decoy, so a handler that
	// re-resolved from cwd would report decoyproj.
	got, _, err := storage.ResolveVaultPath(cwd)
	if err != nil {
		t.Fatalf("ResolveVaultPath(%s): %v", cwd, err)
	}
	if got != decoy {
		t.Fatalf("test precondition broken: cwd resolves to %q, want the decoy %q", got, decoy)
	}

	out := callCheck(t, bound, map[string]any{"checks": []string{"resume-refs"}})

	joined := strings.Join(row(t, out, "Resume refs").Details, "\n")
	if !strings.Contains(joined, "boundproj") {
		t.Errorf("tool did not scan the bound vault: %q", joined)
	}
	if strings.Contains(joined, "decoyproj") {
		t.Errorf("tool re-resolved the vault from the process cwd: %q", joined)
	}
}

// TestCheckTool_NeverLoadsEmbedder mirrors the four CLI regression tests
// (cmd/vp/cmd_check_test.go) that scan output for the string "Embedder".
// check.Run reaches CheckEmbedder → embedder.NewONNX on a healthy install, which
// costs tens of seconds and a ~90MB download on a cold cache; the selector path
// this tool is built on never calls check.Run at all.
func TestCheckTool_NeverLoadsEmbedder(t *testing.T) {
	out := callCheck(t, storage.NewVault(t.TempDir()), map[string]any{})

	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Assert on the ROW NAMES, not on a substring of the whole payload. The
	// hazard is the embedder being LOADED, and its evidence is a row called
	// "Embedder". A substring oracle also matches the word wherever it appears
	// incidentally — a vault path, a summary, a detail — and t.TempDir() bakes
	// the TEST'S OWN NAME into the path it hands out, so any check that echoes
	// its vault root failed this for a reason that had nothing to do with the
	// embedder. Row names cannot be fooled that way and still catch the thing.
	var payload struct {
		Checks []struct {
			Name string `json:"name"`
		} `json:"checks"`
	}
	if uerr := json.Unmarshal(raw, &payload); uerr != nil {
		t.Fatalf("decode check payload: %v\n%s", uerr, raw)
	}
	if len(payload.Checks) == 0 {
		t.Fatalf("vp_check returned no rows — nothing was asserted:\n%s", raw)
	}
	for _, row := range payload.Checks {
		if row.Name == "Embedder" {
			t.Fatalf("vp_check emitted an Embedder row — it must never reach check.Run:\n%s", raw)
		}
	}
	for _, sel := range check.ProducerOrder {
		if strings.Contains(strings.ToLower(sel), "embed") {
			t.Errorf("selector %q would put the embedder on the cheap path", sel)
		}
	}
}
