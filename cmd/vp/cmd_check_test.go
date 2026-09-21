// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/mcphost"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// TestCheckCommand asserts that the human and --json renderings of the full
// suite agree in the same environment: the human [FAIL] row count equals the
// report's fail tally, and the human exit code is ExitUser exactly when the
// report's exit_code is 1. The merge-driver row is CheckSurfaceMergeDriver's
// hard Fail, so it is the case that can tell a correct failure-to-exit mapping
// from one that ignores a single failure.
func TestCheckCommand(t *testing.T) {
	cases := []struct {
		name     string
		seed     func(t *testing.T, vault string)
		wantFail int
	}{
		{name: "healthy", seed: func(*testing.T, string) {}, wantFail: 0},
		{
			name: "merge driver",
			seed: func(t *testing.T, vault string) {
				if err := os.WriteFile(filepath.Join(vault, ".gitattributes"),
					[]byte("*.surface merge=vp-surface\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantFail: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := healthyCheckEnv(t)
			tc.seed(t, e.Vault)

			human, humanCode := runFullCheckHuman(t)
			rep, _ := runFullCheckJSON(t)
			if rep.Summary.Fail != tc.wantFail {
				t.Fatalf("precondition: summary.fail = %d, want %d: %+v", rep.Summary.Fail, tc.wantFail, rep.Checks)
			}
			if got := strings.Count(human, "[FAIL]"); got != rep.Summary.Fail {
				t.Errorf("human output has %d [FAIL] rows, --json reports %d:\n%s", got, rep.Summary.Fail, human)
			}
			if (humanCode == cli.ExitUser) != (rep.ExitCode == 1) {
				t.Errorf("human exit = %d but --json exit_code = %d — the renderings disagree on whether the run failed",
					humanCode, rep.ExitCode)
			}
		})
	}
}

// TestCheckParityWithConfigSyncDryRun proves the Phase 4 acceptance
// criterion: when an artifact has drifted, both `vp check` and
// `vp config sync --dry-run` see it. We drift the cwd-project config and
// assert check emits a "Project" row referencing the drifted file while
// sync --dry-run reports a Project-tier action against the same path.
func TestCheckParityWithConfigSyncDryRun(t *testing.T) {
	unconfiguredCheckEnv(t)
	// No global config — both surfaces should agree it's missing.
	fv, _ := cli.ParseFlags(checkFlags, nil)
	checkOut := captureStdout(t, func() { runCheck(cli.BuildInfo{Version: "test"}, fv) })
	if !strings.Contains(checkOut, "Config:") {
		t.Errorf("vp check missing Config row:\n%s", checkOut)
	}
	if !strings.Contains(checkOut, "[FAIL]") {
		t.Errorf("vp check should report a [FAIL] when global config absent:\n%s", checkOut)
	}

	syncOut := captureStdout(t, func() {
		runConfigSync([]string{"--project-root", t.TempDir(), "--dry-run"})
	})
	if !strings.Contains(syncOut, "GlobalConfig") || !strings.Contains(syncOut, "[Skip]") {
		t.Errorf("config sync --dry-run should report GlobalConfig Skip when missing:\n%s", syncOut)
	}
}

// TestCheckReportsTheRetiredVaultProjectConfig: the "Vault project" row the
// retired VaultProject reconciler emitted is gone, and in its place the full
// suite carries the "Vault project config" survivor row. The two names share a
// prefix, so the assertion keys on the rendered "<name>:" — a bare substring
// match on "Vault project" would be satisfied by the new row alone and pin
// nothing.
func TestCheckReportsTheRetiredVaultProjectConfig(t *testing.T) {
	healthyCheckEnv(t)

	fv, _ := cli.ParseFlags(checkFlags, nil)
	out := captureStdout(t, func() { runCheck(cli.BuildInfo{Version: "test"}, fv) })
	if strings.Contains(out, "] Vault project:") {
		t.Errorf("vp check still emits the retired Vault project row:\n%s", out)
	}
	if !strings.Contains(out, "] Vault project config:") {
		t.Errorf("expected the Vault project config survivor row in vp check output:\n%s", out)
	}
	// The row names the project the cwd's .vibe-palace.toml declares, which
	// proves the fixture's [project] table is what detection read — a
	// top-level name key is ignored, and detection would fall back to the
	// temp dir's basename.
	if !strings.Contains(out, "checktest") {
		t.Errorf("expected the detected project \"checktest\" in vp check output:\n%s", out)
	}
}

// TestCheckFullSuiteEmitsEveryProducerRow is the anti-divergence guard for the
// one thing this package still does twice.
//
// `vp check --check NAME` dispatches check.Producers. `vp check` with no
// filter does NOT: gatherCheckResults calls the underlying check functions
// directly, because it interleaves them with reconciler rows and deliberately
// emits Surface LAST (the closing line of the report) rather than first, which
// is where ProducerOrder puts it. Those are two call sites for one set of
// named checks, and the failure mode is silent: add a producer, forget
// gatherCheckResults, and the check is reachable over MCP while `vp check`
// — the surface a human actually reads — never mentions it.
//
// Rather than assert a hand-written row list (a third copy of the concept),
// this derives the expected names from the registry itself and requires the
// full suite to carry every one.
func TestCheckFullSuiteEmitsEveryProducerRow(t *testing.T) {
	e := healthyCheckEnv(t)

	rep, _ := runFullCheckJSON(t)
	got := map[string]bool{}
	for _, c := range rep.Checks {
		got[c.Name] = true
	}

	for _, sel := range check.ProducerOrder {
		for _, r := range check.Producers[sel](e.Vault) {
			if !got[r.Name] {
				t.Errorf("selector %q produces row %q, which the full `vp check` suite never emits — "+
					"gatherCheckResults has fallen behind check.Producers", sel, r.Name)
			}
		}
	}
}

// TestCheckFullSuiteReportsInjectedMCPHosts proves the MCP host rows come from
// mcpHostRegistry: a detected+registered fake reads pass, a detected fake that
// is not registered reads info, and the registry is asked exactly once.
func TestCheckFullSuiteReportsInjectedMCPHosts(t *testing.T) {
	e := healthyCheckEnv(t)
	e.Hosts = []mcphost.Host{
		fakeMCPHost{name: "fakeon", detected: true, installed: true},
		fakeMCPHost{name: "fakeoff", detected: true, installed: false},
	}

	rep, _ := runFullCheckJSON(t)
	if got := checkRow(t, rep, "MCP host: fakeon"); got.Status != "pass" {
		t.Errorf("MCP host: fakeon = %+v, want pass", got)
	}
	if got := checkRow(t, rep, "MCP host: fakeoff"); got.Status != "info" ||
		!strings.Contains(got.Detail, "vp mcp install --fakeoff") {
		t.Errorf("MCP host: fakeoff = %+v, want info naming `vp mcp install --fakeoff`", got)
	}
	if *e.HostCalls != 1 {
		t.Errorf("mcpHostRegistry called %d times, want 1", *e.HostCalls)
	}
}

// TestCheckFullSuiteRoutesEmbedderRowThroughSeam proves the Embedder row is
// built through newVaultEmbedder — so setupTestVaultEnv's default-on forbid
// guard covers every runCheck caller — and that the Settings-fail gate in
// gatherCheckResults skips the row without constructing anything.
func TestCheckFullSuiteRoutesEmbedderRowThroughSeam(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		e := healthyCheckEnv(t)
		rep, _ := runFullCheckJSON(t)
		if got := checkRow(t, rep, "Embedder"); got.Status != "pass" || !strings.Contains(got.Detail, "384 dimensions") {
			t.Errorf("Embedder = %+v, want pass reporting 384 dimensions", got)
		}
		if *e.EmbedderCalls != 1 {
			t.Errorf("newVaultEmbedder constructed %d times, want 1", *e.EmbedderCalls)
		}
	})

	t.Run("dimensions error", func(t *testing.T) {
		healthyCheckEnv(t)
		calls := stubVaultEmbedder(t, &dimsFailEmbedder{MockEmbedder: embedder.NewMock(384)})
		rep, code := runFullCheckJSON(t)
		if got := checkRow(t, rep, "Embedder"); got.Status != "fail" || !strings.Contains(got.Detail, "embedder dimensions") {
			t.Errorf("Embedder = %+v, want fail naming the dimensions error", got)
		}
		if rep.ExitCode != 1 || code != cli.ExitUser {
			t.Errorf("exit_code = %d / code = %d, want 1 / ExitUser", rep.ExitCode, code)
		}
		if *calls != 1 {
			t.Errorf("newVaultEmbedder constructed %d times, want 1", *calls)
		}
	})

	t.Run("settings fail skips", func(t *testing.T) {
		e := healthyCheckEnv(t)
		// A type error in a key the Config row's narrow vault_path decode
		// tolerates: Config passes, CheckSettings' full LoadConfig fails, and
		// the Embedder row must be skipped without a construction.
		cfgPath, err := storage.VaultConfigFilePath()
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, werr := f.WriteString("http_port = \"x\"\n")
		if cerr := f.Close(); werr != nil || cerr != nil {
			t.Fatalf("append to %s: %v / %v", cfgPath, werr, cerr)
		}

		rep, _ := runFullCheckJSON(t)
		if got := checkRow(t, rep, "Config"); got.Status != "pass" {
			t.Fatalf("precondition: Config = %+v, want pass", got)
		}
		if got := checkRow(t, rep, "Settings"); got.Status != "fail" {
			t.Fatalf("precondition: Settings = %+v, want fail", got)
		}
		if got := checkRow(t, rep, "Embedder"); got.Status != "skip" {
			t.Errorf("Embedder = %+v, want skip when Settings fails", got)
		}
		if *e.EmbedderCalls != 0 {
			t.Errorf("newVaultEmbedder constructed %d times, want 0 behind a failed Settings row", *e.EmbedderCalls)
		}
	})
}

// dimsFailEmbedder is a MockEmbedder whose Dimensions fails.
type dimsFailEmbedder struct{ *embedder.MockEmbedder }

func (dimsFailEmbedder) Dimensions() (int, error) { return 0, errors.New("dimensions unavailable") }

// TestFullCheckExecsNoAgentCLI is the lock on the full suite's hermeticity.
// It puts a recording sentinel for every binary a registered MCP host reports
// (mcphost.Host.Executables — derived from what each host actually runs, not
// from its Name) first on PATH, runs the full suite in healthyCheckEnv, and
// asserts:
//
//   - the sentinel log is empty. This catches a NEW exec route that bypasses
//     the stubbed registry, such as a future check running `grok --version`.
//     It does not catch a route through an agent CLI no host reports; the
//     package-wide sentinel PATH routed to test-infra-consolidation-ephemeral-vault
//     is what covers that.
//   - mcpHostRegistry was asked once and newVaultEmbedder constructed once.
//     These counts catch a revert of either seam: the real registry or a
//     direct embedder.NewONNX would leave them at zero.
func TestFullCheckExecsNoAgentCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sentinels are sh scripts; Windows is out of scope for cmd/vp host isolation")
	}
	names := map[string]bool{}
	for _, h := range mcphost.Registry() {
		for _, x := range h.Executables() {
			names[x] = true
		}
	}
	if len(names) == 0 {
		t.Fatal("no registered MCP host reports an executable — the sentinel half of this lock would be vacuous")
	}

	dir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "sentinel.log")
	for name := range names {
		body := "#!/bin/sh\necho \"${0##*/}|$*\" >> \"$VP_SENTINEL_LOG\"\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("VP_SENTINEL_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name := range names {
		if got, err := exec.LookPath(name); err != nil || got != filepath.Join(dir, name) {
			t.Fatalf("LookPath(%q) = %q, %v — the sentinel is not first on PATH", name, got, err)
		}
	}

	e := healthyCheckEnv(t)
	runFullCheckJSON(t)

	if raw, err := os.ReadFile(logPath); err == nil && len(raw) > 0 {
		t.Errorf("the full vp check suite executed an agent CLI:\n%s", raw)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if e.HostCalls == nil || *e.HostCalls != 1 {
		t.Errorf("mcpHostRegistry calls = %s, want 1 — the host seam was bypassed", formatIntPtr(e.HostCalls))
	}
	if e.EmbedderCalls == nil || *e.EmbedderCalls != 1 {
		t.Errorf("newVaultEmbedder constructions = %s, want 1 — the embedder seam was bypassed", formatIntPtr(e.EmbedderCalls))
	}
}

func TestCheckCommandConstructor(t *testing.T) {
	info := cli.BuildInfo{Version: "test"}
	cmd := cmdCheck(info)
	if cmd.Name != "check" {
		t.Errorf("name = %q", cmd.Name)
	}
	if cmd.Run == nil {
		t.Error("Run is nil")
	}
}

// TestRegisteredToolCount verifies the JSON binary block's tool count reflects
// the full registered MCP surface (engine-gated tools included).
func TestRegisteredToolCount(t *testing.T) {
	if got := registeredToolCount(); got <= 0 {
		t.Fatalf("registeredToolCount() = %d, want > 0", got)
	}
}

// TestBinaryInfo verifies the JSON binary metadata block carries this binary's
// surface version, the build commit, and a positive tool count.
func TestBinaryInfo(t *testing.T) {
	bi := binaryInfo(cli.BuildInfo{Commit: "deadbeef"})
	if bi.Surface != surface.MCPSurfaceVersion {
		t.Errorf("surface = %d, want %d", bi.Surface, surface.MCPSurfaceVersion)
	}
	if bi.Commit != "deadbeef" {
		t.Errorf("commit = %q, want deadbeef", bi.Commit)
	}
	if bi.Tools <= 0 {
		t.Errorf("tools = %d, want > 0", bi.Tools)
	}
}

// TestCheckJSONOutput drives `vp check --json` against a missing global config
// (config check fails) and asserts the JSON shape parses, exit_code is 1, and
// the human renderer is bypassed.
func TestCheckJSONOutput(t *testing.T) {
	unconfiguredCheckEnv(t)

	fvJSON, _ := cli.ParseFlags(checkFlags, []string{"--json"})
	var code int
	out := captureStdout(t, func() {
		code = runCheck(cli.BuildInfo{Version: "test", Commit: "abc"}, fvJSON)
	})

	var rep check.JSONReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if rep.Version != 1 {
		t.Errorf("version = %d, want 1", rep.Version)
	}
	if rep.Binary.Surface != surface.MCPSurfaceVersion {
		t.Errorf("binary.surface = %d, want %d", rep.Binary.Surface, surface.MCPSurfaceVersion)
	}
	if rep.Binary.Tools <= 0 {
		t.Errorf("binary.tools = %d, want > 0", rep.Binary.Tools)
	}
	if len(rep.Checks) == 0 {
		t.Error("expected at least one check")
	}
	// Missing global config → Config fails → exit_code 1, returned ExitUser.
	if rep.Summary.Fail == 0 {
		t.Error("expected at least one failing check with no global config")
	}
	if rep.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", rep.ExitCode)
	}
	if code != cli.ExitUser {
		t.Errorf("runCheck exit = %d, want ExitUser", code)
	}
	// The Surface check is always present as the closing row.
	var sawSurface bool
	for _, c := range rep.Checks {
		if c.Name == "Surface" {
			sawSurface = true
		}
	}
	if !sawSurface {
		t.Error("expected a Surface check in the JSON report")
	}
}

// TestCheckFlagParse verifies the --check flag parses its value (single name
// and comma-separated list form) via ParseFlags.
func TestCheckFlagParse(t *testing.T) {
	fv, err := cli.ParseFlags(checkFlags, []string{"--check", "surface"})
	if err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}
	if got := fv.Get("--check"); got != "surface" {
		t.Errorf("--check = %q, want surface", got)
	}

	fv2, err := cli.ParseFlags(checkFlags, []string{"--check", "surface,foo"})
	if err != nil {
		t.Fatalf("ParseFlags comma: %v", err)
	}
	if got := fv2.Get("--check"); got != "surface,foo" {
		t.Errorf("--check = %q, want surface,foo", got)
	}
}

// TestCheckSurfaceOnlyHuman verifies the human renderer for --check surface
// emits only the Surface row and never loads the embedder (no "Embedder"
// progress line on stderr — the whole point of selective execution).
func TestCheckSurfaceOnlyHuman(t *testing.T) {
	fv, _ := cli.ParseFlags(checkFlags, []string{"--check", "surface"})
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runCheck(cli.BuildInfo{Version: "test"}, fv)
		})
	})
	if !strings.Contains(stdout, "Surface") {
		t.Errorf("expected a Surface row in output:\n%s", stdout)
	}
	if strings.Contains(stdout, "Embedder") || strings.Contains(stderr, "Embedder") {
		t.Errorf("surface-only check must not load the embedder:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

// TestCheckSurfaceOnlyJSON verifies --check surface --json emits exactly one
// Surface check, summary counts that reflect only it, and a binary block whose
// surface is the real constant, tools is zeroed (registeredToolCount skipped),
// and commit is the passed build commit.
func TestCheckSurfaceOnlyJSON(t *testing.T) {
	fv, _ := cli.ParseFlags(checkFlags, []string{"--check", "surface", "--json"})
	out := captureStdout(t, func() {
		runCheck(cli.BuildInfo{Version: "test", Commit: "feedface"}, fv)
	})

	var rep check.JSONReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(rep.Checks) != 1 {
		t.Fatalf("expected exactly one check, got %d:\n%s", len(rep.Checks), out)
	}
	if rep.Checks[0].Name != "Surface" {
		t.Errorf("check name = %q, want Surface", rep.Checks[0].Name)
	}
	total := rep.Summary.Pass + rep.Summary.Fail + rep.Summary.Skip + rep.Summary.Info
	if total != 1 {
		t.Errorf("summary totals = %d, want 1 (only the Surface row)", total)
	}
	if rep.Binary.Surface != surface.MCPSurfaceVersion {
		t.Errorf("binary.surface = %d, want %d", rep.Binary.Surface, surface.MCPSurfaceVersion)
	}
	if rep.Binary.Tools != 0 {
		t.Errorf("binary.tools = %d, want 0 (registeredToolCount skipped)", rep.Binary.Tools)
	}
	if rep.Binary.Commit != "feedface" {
		t.Errorf("binary.commit = %q, want feedface", rep.Binary.Commit)
	}
}

// TestCheckUnknownName verifies an unknown --check name fails fast with
// ExitUser and an "unknown check" diagnostic on stderr.
func TestCheckUnknownName(t *testing.T) {
	fv, _ := cli.ParseFlags(checkFlags, []string{"--check", "foo"})
	var code int
	stderr := captureStderr(t, func() {
		code = runCheck(cli.BuildInfo{Version: "test"}, fv)
	})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser", code)
	}
	if !strings.Contains(stderr, "unknown check") {
		t.Errorf("expected 'unknown check' on stderr, got:\n%s", stderr)
	}
}

// TestCheckCommandRunSurfaceOnly is the full-stack proof: it drives the real
// command-dispatch path (cmdCheck(...).Run), exercising ParseFlags → runCheck →
// runSelectedChecks → ToJSON exactly as a `vp check --check surface --json`
// invocation does — not runCheck in isolation. It asserts the wired flag yields
// a single Surface check, a surface-only binary block, ExitOK, and no embedder
// load on stderr.
func TestCheckCommandRunSurfaceOnly(t *testing.T) {
	cmd := cmdCheck(cli.BuildInfo{Version: "test", Commit: "feedface"})
	var code int
	var stderr string
	out := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			code = cmd.Run([]string{"--check", "surface", "--json"})
		})
	})
	if code != cli.ExitOK {
		t.Errorf("exit code = %d, want ExitOK\nstdout:\n%s", code, out)
	}
	if strings.Contains(stderr, "Embedder") {
		t.Errorf("surface-only dispatch must not load the embedder, stderr:\n%s", stderr)
	}
	var rep check.JSONReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(rep.Checks) != 1 || rep.Checks[0].Name != "Surface" {
		t.Fatalf("expected exactly one Surface check, got %d:\n%s", len(rep.Checks), out)
	}
	if rep.Binary.Surface != surface.MCPSurfaceVersion {
		t.Errorf("binary.surface = %d, want %d", rep.Binary.Surface, surface.MCPSurfaceVersion)
	}
	if rep.Binary.Tools != 0 {
		t.Errorf("binary.tools = %d, want 0 (registeredToolCount skipped)", rep.Binary.Tools)
	}
	if rep.Binary.Commit != "feedface" {
		t.Errorf("binary.commit = %q, want feedface", rep.Binary.Commit)
	}
}

// TestCheckCommandRunUnknown drives the full dispatch path for an unknown check
// name and asserts the user-facing ExitUser + diagnostic surface correctly.
func TestCheckCommandRunUnknown(t *testing.T) {
	cmd := cmdCheck(cli.BuildInfo{Version: "test"})
	var code int
	stderr := captureStderr(t, func() {
		code = cmd.Run([]string{"--check", "nope"})
	})
	if code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitUser", code)
	}
	if !strings.Contains(stderr, "unknown check") {
		t.Errorf("expected 'unknown check' on stderr, got:\n%s", stderr)
	}
}

// TestIsSurfaceOnly exercises the normalization used to decide whether the JSON
// binary block can skip the expensive tool-registry build.
func TestIsSurfaceOnly(t *testing.T) {
	cases := []struct {
		filter string
		want   bool
	}{
		{"surface", true},
		{" surface ", true},
		{"surface,", true},
		{"surface,surface", true},
		{"surface,foo", false},
		{"foo", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isSurfaceOnly(c.filter); got != c.want {
			t.Errorf("isSurfaceOnly(%q) = %v, want %v", c.filter, got, c.want)
		}
	}
}

// seedResumeCapsVault points config at a temp vault holding one project whose
// resume.md is over every cap, and returns the vault path.
func seedResumeCapsVault(t *testing.T) string {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vpDir := filepath.Join(configDir, "vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(configDir, "vault")
	projDir := filepath.Join(vaultPath, "Projects", "fatproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vpDir, "config.toml"),
		[]byte("vault_path = \""+vaultPath+"\"\ngit_enabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	b.WriteString("## Project History\n\n| # | Summary |\n|---|---------|\n")
	for i := range check.ResumeMaxHistoryRows + 1 {
		fmt.Fprintf(&b, "| %d | did a thing |\n", i)
	}
	b.WriteString("\n## Completed Plans\n\n| Task | Iteration | File |\n|------|-----------|------|\n")
	for i := range check.ResumeMaxCompletedRows + 1 {
		fmt.Fprintf(&b, "| t%d | %d | `tasks/done/t.md` |\n", i, i)
	}
	b.WriteString("\n## Notes\n\n")
	b.WriteString(strings.Repeat("x", check.ResumeMaxBytes))
	if err := os.WriteFile(filepath.Join(projDir, "resume.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return vaultPath
}

// TestCheckResumeCapsOnlyHuman verifies `vp check --check resume-caps` selects
// exactly the Resume caps row, flags the over-cap project on all three caps,
// and — like the surface preflight — never loads the embedder.
func TestCheckResumeCapsOnlyHuman(t *testing.T) {
	seedResumeCapsVault(t)

	fv, _ := cli.ParseFlags(checkFlags, []string{"--check", "resume-caps"})
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runCheck(cli.BuildInfo{Version: "test"}, fv)
		})
	})
	if !strings.Contains(stdout, "[info] Resume caps:") {
		t.Errorf("expected an [info] Resume caps row:\n%s", stdout)
	}
	for _, want := range []string{"fatproj:", "cap 25 KB", "Project History 16 rows", "Completed Plans 13 rows"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Embedder") || strings.Contains(stderr, "Embedder") {
		t.Errorf("resume-caps check must not load the embedder:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if strings.Contains(stdout, "Surface:") {
		t.Errorf("resume-caps selection must not run the Surface check:\n%s", stdout)
	}
}

// TestCheckResumeCapsOnlyJSON verifies the --json projection of the selective
// resume-caps run: exactly one info check whose detail carries every breach.
func TestCheckResumeCapsOnlyJSON(t *testing.T) {
	seedResumeCapsVault(t)

	fv, _ := cli.ParseFlags(checkFlags, []string{"--check", "resume-caps", "--json"})
	var code int
	out := captureStdout(t, func() {
		code = runCheck(cli.BuildInfo{Version: "test", Commit: "cafe"}, fv)
	})

	var rep check.JSONReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(rep.Checks) != 1 || rep.Checks[0].Name != "Resume caps" {
		t.Fatalf("expected exactly one Resume caps check, got %+v", rep.Checks)
	}
	if rep.Checks[0].Status != "info" {
		t.Errorf("status = %q, want info (a cap breach warns, never fails)", rep.Checks[0].Status)
	}
	if rep.Summary.Info != 1 || rep.Summary.Fail != 0 {
		t.Errorf("summary = %+v, want exactly one info and zero fail", rep.Summary)
	}
	if rep.ExitCode != 0 || code != cli.ExitOK {
		t.Errorf("exit_code = %d / code = %d, want 0 / ExitOK — a warning must not fail the run",
			rep.ExitCode, code)
	}
	for _, want := range []string{"fatproj", "Project History 16 rows", "Completed Plans 13 rows"} {
		if !strings.Contains(rep.Checks[0].Detail, want) {
			t.Errorf("detail missing %q: %q", want, rep.Checks[0].Detail)
		}
	}
}

// TestCheckResumeCapsNoVault verifies the producer degrades to Skip (not a
// panic or a bogus Pass) when no vault can be resolved.
func TestCheckResumeCapsNoVault(t *testing.T) {
	rs := check.Producers["resume-caps"]("")
	if len(rs) != 1 {
		t.Fatalf("want one result, got %d", len(rs))
	}
	if rs[0].Status != check.Skip || rs[0].Name != "Resume caps" {
		t.Errorf("got %+v, want a skipped Resume caps row", rs[0])
	}
}

// seedResumeRefsVault points config at a temp vault holding one project whose
// resume.md commits both a home-relative and an absolute host-local plan
// reference (plus one fenced reference that must be ignored). Returns the path.
func seedResumeRefsVault(t *testing.T) string {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vpDir := filepath.Join(configDir, "vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(configDir, "vault")
	projDir := filepath.Join(vaultPath, "Projects", "refproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vpDir, "config.toml"),
		[]byte("vault_path = \""+vaultPath+"\"\ngit_enabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	body := "# refproj\n\n## Current State\n\n" +
		"Plan lives at ~/.claude/plans/active.md\n" +
		"Older plan: /home/dev/.claude/plans/older.md\n\n" +
		"## Sample\n\n```\n~/.claude/plans/ignored.md\n```\n"
	if err := os.WriteFile(filepath.Join(projDir, "resume.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return vaultPath
}

// TestCheckResumeRefsOnlyHuman verifies `vp check --check resume-refs` selects
// exactly the Resume refs row, flags both host-local paths, ignores the fenced
// one, and — like the surface preflight — never loads the embedder.
func TestCheckResumeRefsOnlyHuman(t *testing.T) {
	seedResumeRefsVault(t)

	fv, _ := cli.ParseFlags(checkFlags, []string{"--check", "resume-refs"})
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runCheck(cli.BuildInfo{Version: "test"}, fv)
		})
	})
	if !strings.Contains(stdout, "[info] Resume refs:") {
		t.Errorf("expected an [info] Resume refs row:\n%s", stdout)
	}
	for _, want := range []string{"refproj:", "~/.claude/plans/active.md", "/home/dev/.claude/plans/older.md"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "ignored.md") {
		t.Errorf("fenced reference must not be flagged:\n%s", stdout)
	}
	if strings.Contains(stdout, "Embedder") || strings.Contains(stderr, "Embedder") {
		t.Errorf("resume-refs check must not load the embedder:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if strings.Contains(stdout, "Surface:") {
		t.Errorf("resume-refs selection must not run the Surface check:\n%s", stdout)
	}
}

// TestCheckResumeRefsOnlyJSON verifies the --json projection: exactly one info
// check whose detail enumerates each offending path, and exit code 0 even with
// a breach present (a lint warns, never fails).
func TestCheckResumeRefsOnlyJSON(t *testing.T) {
	seedResumeRefsVault(t)

	fv, _ := cli.ParseFlags(checkFlags, []string{"--check", "resume-refs", "--json"})
	var code int
	out := captureStdout(t, func() {
		code = runCheck(cli.BuildInfo{Version: "test", Commit: "cafe"}, fv)
	})

	var rep check.JSONReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(rep.Checks) != 1 || rep.Checks[0].Name != "Resume refs" {
		t.Fatalf("expected exactly one Resume refs check, got %+v", rep.Checks)
	}
	if rep.Checks[0].Status != "info" {
		t.Errorf("status = %q, want info (a host-local ref warns, never fails)", rep.Checks[0].Status)
	}
	if rep.Summary.Info != 1 || rep.Summary.Fail != 0 {
		t.Errorf("summary = %+v, want exactly one info and zero fail", rep.Summary)
	}
	if rep.ExitCode != 0 || code != cli.ExitOK {
		t.Errorf("exit_code = %d / code = %d, want 0 / ExitOK — a lint must not fail the run",
			rep.ExitCode, code)
	}
	for _, want := range []string{"~/.claude/plans/active.md", "/home/dev/.claude/plans/older.md"} {
		if !strings.Contains(rep.Checks[0].Detail, want) {
			t.Errorf("detail missing %q: %q", want, rep.Checks[0].Detail)
		}
	}
}

// TestCheckResumeRefsNoVault verifies the producer degrades to Skip when no
// vault can be resolved.
func TestCheckResumeRefsNoVault(t *testing.T) {
	rs := check.Producers["resume-refs"]("")
	if len(rs) != 1 {
		t.Fatalf("want one result, got %d", len(rs))
	}
	if rs[0].Status != check.Skip || rs[0].Name != "Resume refs" {
		t.Errorf("got %+v, want a skipped Resume refs row", rs[0])
	}
}

// TestCheckFullSuiteEmitsSummarizationQueueRow proves the new project-repo-
// rooted "Summarization queue" row (check.CheckSummarizationQueue, wired into
// gatherCheckResults right after the Phase 4 scaffold check) appears in BOTH
// the human table and the --json report when the current project's queue is
// non-empty and [summarization] is not configured — an expected backlog, not
// a stuck queue.
func TestCheckFullSuiteEmitsSummarizationQueueRow(t *testing.T) {
	healthyCheckEnv(t)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// Pin [summarization] off in the host global config — its only tier —
	// appended to the one healthyCheckEnv wrote under its per-test XDG so
	// the vault_path survives.
	appendHostConfig(t, "[summarization]\nenabled = false\n")

	queueDir := filepath.Join(cwd, ".vibe-palace", "summarization-queue")
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queueDir, "iteration-00001.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	human, _ := runFullCheckHuman(t)
	if !strings.Contains(human, "Summarization queue") || !strings.Contains(human, "not configured") {
		t.Errorf("expected a Summarization queue row naming the expected-backlog wording in human output:\n%s", human)
	}

	rep, _ := runFullCheckJSON(t)
	row := checkRow(t, rep, "Summarization queue")
	if row.Status != "info" {
		t.Errorf("Summarization queue status = %q, want info", row.Status)
	}
	if !strings.Contains(row.Detail, "not configured") {
		t.Errorf("Summarization queue detail = %q, want it to mention the config is not configured", row.Detail)
	}
}

// TestCheckSummarizationQueueNotSelectable guards against a future accidental
// merge of check.CheckSummarizationQueue into check.Producers /
// check.ProducerOrder. It is deliberately project-repo-path-scoped
// (gatherCheckResults calls it directly with cwd) rather than vault-rooted,
// which check.Producers structurally is not — see cmd_check.go's comment on
// CheckGitPostCommitHook. Neither its row name nor any plausible selector
// name for it may appear in the selective-dispatch registry.
func TestCheckSummarizationQueueNotSelectable(t *testing.T) {
	for _, name := range check.ProducerOrder {
		if name == "summarization-queue" {
			t.Fatalf("check.ProducerOrder unexpectedly contains %q — CheckSummarizationQueue must stay out of the "+
				"vault-rooted selector registry (it needs a project repo path the registry doesn't carry)", name)
		}
		for _, r := range check.Producers[name]("") {
			if r.Name == "Summarization queue" {
				t.Fatalf("selector %q unexpectedly produces a %q row", name, r.Name)
			}
		}
	}

	cmd := cmdCheck(cli.BuildInfo{Version: "test"})
	for _, guess := range []string{"summarization-queue", "summarization_queue", "summarization"} {
		var code int
		stderr := captureStderr(t, func() {
			code = cmd.Run([]string{"--check", guess})
		})
		if code != cli.ExitUser {
			t.Errorf("--check %q: exit code = %d, want ExitUser", guess, code)
		}
		if !strings.Contains(stderr, "unknown check") {
			t.Errorf("--check %q: expected 'unknown check' on stderr, got:\n%s", guess, stderr)
		}
	}
}
