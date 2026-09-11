// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
)

// TestSkillsShowDirBody exercises runSkillsShow against the embedded
// startup-analyst skill: SKILL.md body + sorted references list.
func TestSkillsShowDirBody(t *testing.T) {
	resolver := vpctx.NewResolver(t.TempDir())

	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{Name: "startup-analyst"})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "# skill: startup-analyst | source: embedded") {
		t.Errorf("missing header, got %q", s[:min(120, len(s))])
	}
	if !strings.Contains(s, "References (fetch with --section=<name>):") {
		t.Error("missing references block")
	}
	for _, r := range []string{"capex-opex", "competitive-landscape", "funding-sources",
		"reality-validation", "strategic-partnerships"} {
		if !strings.Contains(s, "  - "+r+"\n") {
			t.Errorf("missing ref %q", r)
		}
	}
}

// TestSkillsShowSection verifies --section fetches just the reference
// body (no references list, no wrapping).
func TestSkillsShowSection(t *testing.T) {
	resolver := vpctx.NewResolver(t.TempDir())

	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{
		Name:    "startup-analyst",
		Section: "capex-opex",
	})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "section: capex-opex | source: embedded") {
		t.Error("missing section header")
	}
	if strings.Contains(s, "References (fetch with") {
		t.Error("--section output should not emit references block")
	}
}

// TestSkillsShowMissingSkill returns a user-tier error.
func TestSkillsShowMissingSkill(t *testing.T) {
	resolver := vpctx.NewResolver(t.TempDir())

	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{Name: "no-such-skill"})
	if rc == 0 {
		t.Fatalf("expected non-zero rc")
	}
	if !strings.Contains(errOut.String(), "not found") {
		t.Errorf("stderr=%q", errOut.String())
	}
}

// TestSkillsShowProjectOverride proves runSkillsShow uses the project
// tier — a project-scoped SKILL.md should shadow the embedded copy.
func TestSkillsShowProjectOverride(t *testing.T) {
	vaultRoot := t.TempDir()
	target := filepath.Join(vaultRoot, "Projects", "myproj", "skills", "startup-analyst", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# Overridden\n\nproject body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := vpctx.NewResolver(vaultRoot)

	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{
		Name:    "startup-analyst",
		Project: "myproj",
	})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "source: project") {
		t.Errorf("want source: project, got %q", s)
	}
	if !strings.Contains(s, "project body") {
		t.Error("missing project-tier body")
	}
	// Embedded references should still fall through.
	if !strings.Contains(s, "  - capex-opex\n") {
		t.Error("references should fall through from embedded even when project overrides SKILL.md")
	}
}

// TestSkillsListContainsEmbedded exercises the `vp skills list`
// plumbing: the resolver must surface the embedded startup-analyst
// skill in the Summary output printed by printSkillsTable.
func TestSkillsListContainsEmbedded(t *testing.T) {
	// We drive the command object directly because its Run closure
	// calls openProjectVault(); instead we call the shared library
	// used inside that closure.
	resolver := vpctx.NewResolver(t.TempDir())

	// Reuse the `commands.List` call path (exercised inside Run).
	// The printer is also exercised to ensure formatting doesn't crash.
	var buf bytes.Buffer
	summaries, err := commands.List(resolver, "skill", "", "", "", 60)
	if err != nil {
		t.Fatal(err)
	}
	printSkillsTable(&buf, summaries, "")
	s := buf.String()
	if !strings.Contains(s, "startup-analyst") {
		t.Errorf("missing startup-analyst in table, got %q", s)
	}
	if !strings.Contains(s, "embedded") {
		t.Error("missing embedded source tag")
	}
}

// TestSkillsCommandMetadata touches the three *cli.Command factory
// functions so their top-level metadata (Name, Synopsis, Subcommands)
// is exercised. Run closures themselves require a real project vault
// and are covered via the end-to-end integration suite.
func TestSkillsCommandMetadata(t *testing.T) {
	cs := cmdSkills()
	if cs.Name != "skills" {
		t.Errorf("name = %q", cs.Name)
	}
	if cs.Run != nil {
		t.Error("cmdSkills Run should be nil — parent help is rendered by the dispatcher")
	}
	// Subcommands is derived by the registry, not declared by the constructor —
	// so the dispatcher's parent/leaf branch is asked of a REGISTERED command.
	if len(registeredCommand(t, "skills").Subcommands) == 0 {
		t.Error("the registry did not populate skills' Subcommands, so the dispatcher would " +
			"treat this parent as a leaf and call its nil Run")
	}
	// The registered children, pinned by name: `skills reset` is the only one
	// that writes the vault.
	kids := strings.Join(registeredChildren(t, "skills"), ",")
	if kids != "skills list,skills show,skills upgrade,skills reset" {
		t.Errorf("skills children = %s", kids)
	}
	cl := cmdSkillsList()
	if cl.Name != "skills list" {
		t.Errorf("list name = %q", cl.Name)
	}
	if len(cl.Flags) == 0 {
		t.Error("cmdSkillsList missing flags")
	}
	cshow := cmdSkillsShow()
	if cshow.Name != "skills show" {
		t.Errorf("show name = %q", cshow.Name)
	}
	// Run with no args should fail on missing positional.
	if rc := cshow.Run(nil); rc == 0 {
		t.Error("cmdSkillsShow with no args should fail")
	}
	// Run with unknown flag should fail parsing.
	if rc := cshow.Run([]string{"--nope"}); rc == 0 {
		t.Error("cmdSkillsShow with bad flag should fail")
	}
}

// TestSkillsShowEmptyBody covers the edge case where SKILL.md is empty
// so the trailing-newline logic in runSkillsShow skips the append.
func TestSkillsShowEmptyBody(t *testing.T) {
	vaultRoot := t.TempDir()
	target := filepath.Join(vaultRoot, "Templates", "skills", "empty", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := vpctx.NewResolver(vaultRoot)
	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{Name: "empty"})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errOut.String())
	}
}

// TestSkillsShowSectionMissing covers the --section error path in
// runSkillsShow; happy-path already covered above.
func TestSkillsShowSectionMissing(t *testing.T) {
	resolver := vpctx.NewResolver(t.TempDir())
	var out, errOut bytes.Buffer
	rc := runSkillsShow(&out, &errOut, resolver, skillsShowOpts{
		Name: "startup-analyst", Section: "not-there",
	})
	if rc == 0 {
		t.Fatal("expected non-zero rc")
	}
	if !strings.Contains(errOut.String(), "not found") {
		t.Errorf("stderr=%q", errOut.String())
	}
}

// TestPrintSkillsTableEmpty + TestPrintSkillsTableProject round out
// the formatter branches so printSkillsTable coverage stays tight.
func TestPrintSkillsTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	printSkillsTable(&buf, nil, "")
	if !strings.Contains(buf.String(), "No skills available.") {
		t.Errorf("got %q", buf.String())
	}
}

func TestPrintSkillsTableProject(t *testing.T) {
	var buf bytes.Buffer
	printSkillsTable(&buf, []commands.Summary{{Name: "a", Source: "project", Brief: "b"}}, "myproj")
	s := buf.String()
	if !strings.Contains(s, `project "myproj"`) {
		t.Errorf("missing project header: %q", s)
	}
}

// skillsShowEnv is the sandbox the `vp skills show` scope tests run in: HOME
// and XDG_CONFIG_HOME are fresh temp dirs, the vault sits outside HOME, and the
// project directory is a SUBDIRECTORY of HOME. It must never be HOME itself:
// both marker walks stop at $HOME, so a marker there would never be read and a
// must-fail test would pass or fail for the wrong reason.
type skillsShowEnv struct {
	home, vault, proj string
}

func newSkillsShowEnv(t *testing.T) skillsShowEnv {
	t.Helper()
	root := t.TempDir()
	env := skillsShowEnv{
		home:  filepath.Join(root, "home"),
		vault: filepath.Join(root, "vault"),
		proj:  filepath.Join(root, "home", "code", "proj"),
	}
	for _, d := range []string{filepath.Join(env.home, ".config"), env.vault, env.proj} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// USERPROFILE and APPDATA are Windows' HOME and config dir: set them too,
	// as setupTestVaultEnv does, so no run can reach the host's real ones.
	t.Setenv("HOME", env.home)
	t.Setenv("USERPROFILE", env.home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(env.home, ".config"))
	t.Setenv("APPDATA", filepath.Join(env.home, ".config"))
	return env
}

// writeGlobalConfig writes the global config.toml; an empty vault writes a
// config with no vault_path at all.
func (e skillsShowEnv) writeGlobalConfig(t *testing.T, vault string) {
	t.Helper()
	body := "# no vault_path\n"
	if vault != "" {
		body = "vault_path = \"" + filepath.ToSlash(vault) + "\"\n"
	}
	writeTestFile(t, filepath.Join(e.home, ".config", "vibe-palace", "config.toml"), body)
}

// writeMarker writes the project's .vibe-palace.toml. vault, when set, becomes
// a top-level vault_path; slug, when set, becomes [project].name.
func (e skillsShowEnv) writeMarker(t *testing.T, dir, vault, slug string) {
	t.Helper()
	var sb strings.Builder
	if vault != "" {
		sb.WriteString("vault_path = \"" + filepath.ToSlash(vault) + "\"\n\n")
	}
	if slug != "" {
		sb.WriteString("[project]\nname = \"" + slug + "\"\n")
	}
	writeTestFile(t, filepath.Join(dir, ".vibe-palace.toml"), sb.String())
}

// writeProjectSkill writes Projects/<slug>/skills/<name>/SKILL.md in the vault.
func (e skillsShowEnv) writeProjectSkill(t *testing.T, slug, name, body string) {
	t.Helper()
	writeTestFile(t, filepath.Join(e.vault, "Projects", slug, "skills", name, "SKILL.md"), body)
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runSkillsShowCmd drives the real `vp skills show` Run closure — flag
// parsing, Getwd, the scope seam and runSkillsShow — from the current
// directory, and returns its exit code, stdout and stderr.
func runSkillsShowCmd(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code = cmdSkillsShow().Run(args)
		})
	})
	return code, stdout, stderr
}

// TestSkillsShowDefaultsProjectFromCwd pins the fix for the fallback's
// precedence gap: run from inside a project, with no --project, `vp skills
// show` must serve that project's override — the tier vp_skill serves there.
// At 22f0d3f it printed `source: embedded`, because only an explicit --project
// reached the project tier.
func TestSkillsShowDefaultsProjectFromCwd(t *testing.T) {
	env := newSkillsShowEnv(t)
	env.writeGlobalConfig(t, env.vault)
	env.writeMarker(t, env.proj, "", "proj1")
	env.writeProjectSkill(t, "proj1", "startup-analyst", "# Overridden\n\nproj1 body\n")
	sub := filepath.Join(env.proj, "internal", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{env.proj, sub} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			t.Chdir(dir)
			code, out, errOut := runSkillsShowCmd(t, "startup-analyst")
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, errOut)
			}
			if !strings.Contains(out, "# skill: startup-analyst | source: project\n") {
				t.Errorf("want the cwd project's tier, got %q", firstLine(out))
			}
			if !strings.Contains(out, "proj1 body") {
				t.Errorf("want the project override body, got %q", out)
			}
			if errOut != "" {
				t.Errorf("no stderr expected, got %q", errOut)
			}
		})
	}
}

// TestSkillsShowExplicitProjectWins: --project names a different project than
// the cwd one, and it wins.
func TestSkillsShowExplicitProjectWins(t *testing.T) {
	env := newSkillsShowEnv(t)
	env.writeGlobalConfig(t, env.vault)
	env.writeMarker(t, env.proj, "", "proj1")
	env.writeProjectSkill(t, "proj1", "startup-analyst", "proj1 body\n")
	env.writeProjectSkill(t, "proj2", "startup-analyst", "proj2 body\n")
	t.Chdir(env.proj)

	code, out, errOut := runSkillsShowCmd(t, "startup-analyst", "--project", "proj2")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "source: project") || !strings.Contains(out, "proj2 body") || strings.Contains(out, "proj1 body") {
		t.Errorf("explicit --project proj2 must win over the cwd project, got %q", out)
	}
}

// TestSkillsShowNoProjectSkipsProjectTier: --no-project, run from inside a
// project that overrides the skill, serves the next tier down — the vault
// override when there is one, else the built-in. That is how an operator
// fetches the default to start an override from.
func TestSkillsShowNoProjectSkipsProjectTier(t *testing.T) {
	env := newSkillsShowEnv(t)
	env.writeGlobalConfig(t, env.vault)
	env.writeMarker(t, env.proj, "", "proj1")
	env.writeProjectSkill(t, "proj1", "startup-analyst", "proj1 body\n")
	t.Chdir(env.proj)

	code, out, errOut := runSkillsShowCmd(t, "startup-analyst", "--no-project")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "# skill: startup-analyst | source: embedded\n") || strings.Contains(out, "proj1 body") {
		t.Errorf("--no-project must skip the project tier, got %q", firstLine(out))
	}

	writeTestFile(t, filepath.Join(env.vault, "Templates", "skills", "startup-analyst", "SKILL.md"), "vault body\n")
	code, out, errOut = runSkillsShowCmd(t, "startup-analyst", "--no-project")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "# skill: startup-analyst | source: vault\n") || !strings.Contains(out, "vault body") {
		t.Errorf("--no-project must still serve a vault override, got %q", out)
	}
}

// TestSkillsShowNoProjectRejectsScopeFlags: --no-project with any scope flag
// is a usage error, and nothing is printed to stdout.
func TestSkillsShowNoProjectRejectsScopeFlags(t *testing.T) {
	env := newSkillsShowEnv(t)
	env.writeGlobalConfig(t, env.vault)
	t.Chdir(env.proj)

	for _, extra := range [][]string{
		{"--project", "proj1"},
		{"--wing", "w"},
		{"--wing", "w", "--room", "r"},
	} {
		t.Run(strings.Join(extra, " "), func(t *testing.T) {
			args := append([]string{"startup-analyst", "--no-project"}, extra...)
			code, out, errOut := runSkillsShowCmd(t, args...)
			if code != 1 {
				t.Errorf("exit %d, want 1 (ExitUser)", code)
			}
			if !strings.Contains(errOut, "--no-project cannot be combined with --project, --wing or --room") {
				t.Errorf("stderr %q does not explain the conflict", errOut)
			}
			if out != "" {
				t.Errorf("nothing belongs on stdout, got %q", out)
			}
		})
	}
}

// TestSkillsShowWithoutConfigServesEmbedded pins the no-config degrade: with
// no global config.toml and no vault_path in any .vibe-palace.toml on the
// upward walk, `vp skills show` prints the built-in skill and one stderr line,
// instead of failing. At 22f0d3f it exited 1 with
// `read config …/vibe-palace/config.toml: open …: no such file or directory`.
//
// The cwd-relative subtest pins review N1: the no-vault resolver must not
// read ./Templates/skills or ./Projects/<slug>/skills relative to the process
// cwd — with no vault root, only the embedded tier is a tier.
func TestSkillsShowWithoutConfigServesEmbedded(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, env skillsShowEnv)
	}{
		{"no marker", func(*testing.T, skillsShowEnv) {}},
		{"name-only marker", func(t *testing.T, env skillsShowEnv) {
			env.writeMarker(t, env.proj, "", "proj1")
		}},
		{"cwd holds a Templates tree", func(t *testing.T, env skillsShowEnv) {
			env.writeMarker(t, env.proj, "", "proj1")
			writeTestFile(t, filepath.Join(env.proj, "Templates", "skills", "startup-analyst", "SKILL.md"), "cwd templates body\n")
			writeTestFile(t, filepath.Join(env.proj, "Templates", "skills", "startup-analyst", "references", "capex-opex.md"), "cwd section body\n")
			writeTestFile(t, filepath.Join(env.proj, "Projects", "proj1", "skills", "startup-analyst", "SKILL.md"), "cwd project body\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSkillsShowEnv(t)
			tc.setup(t, env)
			t.Chdir(env.proj)

			code, out, errOut := runSkillsShowCmd(t, "startup-analyst")
			if code != 0 {
				t.Fatalf("exit %d, stderr %q", code, errOut)
			}
			if !strings.Contains(out, "# skill: startup-analyst | source: embedded\n") {
				t.Errorf("want the built-in skill, got %q", firstLine(out))
			}
			if strings.Contains(out, "cwd ") {
				t.Errorf("a no-vault run read a cwd-relative tier: %q", out)
			}
			if strings.TrimSpace(errOut) != skillsShowNoVaultNote {
				t.Errorf("stderr = %q, want exactly %q", errOut, skillsShowNoVaultNote)
			}

			code, out, errOut = runSkillsShowCmd(t, "startup-analyst", "--section", "capex-opex")
			if code != 0 {
				t.Fatalf("--section: exit %d, stderr %q", code, errOut)
			}
			if !strings.Contains(out, "section: capex-opex | source: embedded\n") || strings.Contains(out, "cwd ") {
				t.Errorf("--section must serve the built-in reference, got %q", out)
			}
		})
	}

	t.Run("scope flags need a vault", func(t *testing.T) {
		env := newSkillsShowEnv(t)
		t.Chdir(env.proj)
		for _, extra := range [][]string{{"--project", "proj1"}, {"--wing", "w"}, {"--room", "r"}} {
			code, out, errOut := runSkillsShowCmd(t, append([]string{"startup-analyst"}, extra...)...)
			if code != 1 {
				t.Errorf("%v: exit %d, want 1 (ExitUser)", extra, code)
			}
			if !strings.Contains(errOut, "no vault configured, so --project, --wing and --room have no tier to read") {
				t.Errorf("%v: stderr %q does not say the flag needs a vault", extra, errOut)
			}
			if out != "" {
				t.Errorf("%v: nothing belongs on stdout, got %q", extra, out)
			}
		}
	})
}

// TestSkillsShowCwdVaultPathWithoutGlobalConfig proves the degrade fires only
// on the GLOBAL config's absence: a marker that sets vault_path still binds the
// vault with no global config, so the project and vault tiers are served and
// no degrade note is printed.
func TestSkillsShowCwdVaultPathWithoutGlobalConfig(t *testing.T) {
	env := newSkillsShowEnv(t)
	env.writeMarker(t, env.proj, env.vault, "proj1")
	env.writeProjectSkill(t, "proj1", "startup-analyst", "proj1 body\n")
	writeTestFile(t, filepath.Join(env.vault, "Templates", "skills", "startup-analyst", "SKILL.md"), "vault body\n")
	t.Chdir(env.proj)

	code, out, errOut := runSkillsShowCmd(t, "startup-analyst")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "source: project") || !strings.Contains(out, "proj1 body") {
		t.Errorf("want the project tier through the marker's vault_path, got %q", out)
	}
	if errOut != "" {
		t.Errorf("no degrade note expected when the marker binds a vault, got %q", errOut)
	}

	code, out, errOut = runSkillsShowCmd(t, "startup-analyst", "--no-project")
	if code != 0 {
		t.Fatalf("--no-project: exit %d, stderr %q", code, errOut)
	}
	if !strings.Contains(out, "source: vault") || !strings.Contains(out, "vault body") {
		t.Errorf("want the vault tier through the marker's vault_path, got %q", out)
	}
}

// TestSkillsShowUnsetGlobalVaultPathStaysAnError pins that the degrade is
// deliberately narrow (review L7): a global config that exists but sets no
// vault_path is a misconfiguration, and stays a usage error.
func TestSkillsShowUnsetGlobalVaultPathStaysAnError(t *testing.T) {
	env := newSkillsShowEnv(t)
	env.writeGlobalConfig(t, "")
	t.Chdir(env.proj)

	code, out, errOut := runSkillsShowCmd(t, "startup-analyst")
	if code != 1 {
		t.Errorf("exit %d, want 1 (ExitUser)", code)
	}
	if !strings.Contains(errOut, "vault_path not set") {
		t.Errorf("stderr %q should name the missing vault_path", errOut)
	}
	if strings.Contains(errOut, skillsShowNoVaultNote) || out != "" {
		t.Errorf("a config without vault_path must not degrade to the built-in: stdout %q stderr %q", out, errOut)
	}
}

// TestSkillsShowScopeMalformedMarkerStaysAnError: a .vibe-palace.toml that
// does not parse is not "no vault configured".
func TestSkillsShowScopeMalformedMarkerStaysAnError(t *testing.T) {
	env := newSkillsShowEnv(t)
	writeTestFile(t, filepath.Join(env.proj, ".vibe-palace.toml"), "vault_path = [unterminated\n")

	_, _, note, code := skillsShowScope(env.proj, "", false, "", "")
	if code != 1 || strings.Contains(note, skillsShowNoVaultNote) || !strings.Contains(note, "parse") {
		t.Errorf("code %d note %q: a malformed marker must stay a usage error", code, note)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
