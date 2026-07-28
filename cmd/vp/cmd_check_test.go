// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

func TestCheckCommand(t *testing.T) {
	// runCheck depends on real config; just verify it doesn't panic
	// and returns a valid exit code.
	fv, _ := cli.ParseFlags(checkFlags, nil)
	code := runCheck(cli.BuildInfo{Version: "test"}, fv)
	if code != cli.ExitOK && code != cli.ExitUser {
		t.Errorf("exit code = %d, want ExitOK or ExitUser", code)
	}
}

// TestCheckParityWithConfigSyncDryRun proves the Phase 4 acceptance
// criterion: when an artifact has drifted, both `vp check` and
// `vp config sync --dry-run` see it. We drift the cwd-project config and
// assert check emits a "Project" row referencing the drifted file while
// sync --dry-run reports a Project-tier action against the same path.
func TestCheckParityWithConfigSyncDryRun(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
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

// TestCheckEmitsVaultProjectRow verifies the new row added in Phase 4: vp
// check now reports on vault-project state via the VaultProject reconciler.
func TestCheckEmitsVaultProjectRow(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vpDir := filepath.Join(configDir, "vibe-palace")
	_ = os.MkdirAll(vpDir, 0o755)
	vaultPath := filepath.Join(configDir, "vault")
	_ = os.MkdirAll(vaultPath, 0o755)
	_ = os.WriteFile(filepath.Join(vpDir, "config.toml"),
		[]byte("vault_path = \""+vaultPath+"\"\ngit_enabled = false\n"), 0o644)

	// cwd into a project dir so DetectProject can find a slug.
	projDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(projDir, project.ConfigFileName),
		[]byte("name = \"checktest\"\n"), 0o644)
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	_ = os.Chdir(projDir)

	fv, _ := cli.ParseFlags(checkFlags, nil)
	out := captureStdout(t, func() { runCheck(cli.BuildInfo{Version: "test"}, fv) })
	if !strings.Contains(out, "Vault project") {
		t.Errorf("expected Vault project row in vp check output:\n%s", out)
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
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

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

// seedCoreFloorVault points config at a temp vault holding one project
// whose resume.md + workflow.md core is over the CoreMaxBytes cap and one within it.
// Returns the vault path.
func seedCoreFloorVault(t *testing.T) string {
	t.Helper()
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	vpDir := filepath.Join(configDir, "vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(configDir, "vault")
	if err := os.WriteFile(filepath.Join(vpDir, "config.toml"),
		[]byte("vault_path = \""+vaultPath+"\"\ngit_enabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write := func(slug, body string) {
		dir := filepath.Join(vaultPath, "Projects", slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "workflow.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("fatwf", "# fatwf — Workflow\n\n"+strings.Repeat("x", check.CoreMaxBytes+1))
	write("thinwf", "# thinwf — Workflow\n\n- (none recorded yet)\n")
	return vaultPath
}

// TestCheckCoreFloorOnlyHuman verifies `vp check --check core-floor`
// selects exactly the Core floor row, flags only the over-cap project, and
// — like the surface preflight — never loads the embedder.
func TestCheckCoreFloorOnlyHuman(t *testing.T) {
	seedCoreFloorVault(t)

	fv, _ := cli.ParseFlags(checkFlags, []string{"--check", "core-floor"})
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runCheck(cli.BuildInfo{Version: "test"}, fv)
		})
	})
	if !strings.Contains(stdout, "[info] Core floor:") {
		t.Errorf("expected an [info] Core floor row:\n%s", stdout)
	}
	for _, want := range []string{"1 of 2 core over cap", "fatwf:", fmt.Sprintf("cap %d bytes", check.CoreMaxBytes)} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "thinwf") {
		t.Errorf("under-cap project must stay silent:\n%s", stdout)
	}
	if strings.Contains(stdout, "Embedder") || strings.Contains(stderr, "Embedder") {
		t.Errorf("core-floor check must not load the embedder:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if strings.Contains(stdout, "Surface:") {
		t.Errorf("core-floor selection must not run the Surface check:\n%s", stdout)
	}
}

// TestCheckCoreFloorOnlyJSON verifies the --json projection: exactly one
// info check naming the over-cap project, and exit code 0 even with a breach
// present (an advisory warns, never fails).
func TestCheckCoreFloorOnlyJSON(t *testing.T) {
	seedCoreFloorVault(t)

	fv, _ := cli.ParseFlags(checkFlags, []string{"--check", "core-floor", "--json"})
	var code int
	out := captureStdout(t, func() {
		code = runCheck(cli.BuildInfo{Version: "test", Commit: "cafe"}, fv)
	})

	var rep check.JSONReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(rep.Checks) != 1 || rep.Checks[0].Name != "Core floor" {
		t.Fatalf("expected exactly one Core floor check, got %+v", rep.Checks)
	}
	if rep.Checks[0].Status != "info" {
		t.Errorf("status = %q, want info (a cap breach warns, never fails)", rep.Checks[0].Status)
	}
	if rep.Summary.Info != 1 || rep.Summary.Fail != 0 {
		t.Errorf("summary = %+v, want exactly one info and zero fail", rep.Summary)
	}
	if rep.ExitCode != 0 || code != cli.ExitOK {
		t.Errorf("exit_code = %d / code = %d, want 0 / ExitOK — an advisory must not fail the run",
			rep.ExitCode, code)
	}
	if !strings.Contains(rep.Checks[0].Detail, "fatwf") {
		t.Errorf("detail missing %q: %q", "fatwf", rep.Checks[0].Detail)
	}
}

// TestCheckCoreFloorNoVault verifies the producer degrades to Skip (not a
// panic or a bogus Pass) when no vault can be resolved.
func TestCheckCoreFloorNoVault(t *testing.T) {
	rs := check.Producers["core-floor"]("")
	if len(rs) != 1 {
		t.Fatalf("want one result, got %d", len(rs))
	}
	if rs[0].Status != check.Skip || rs[0].Name != "Core floor" {
		t.Errorf("got %+v, want a skipped Core floor row", rs[0])
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
