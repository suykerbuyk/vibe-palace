// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// fallbackCmdRe extracts the command a persona shim's MCP-less fallback names.
// It matches only the plain `vp skills show <name>` span — the closing
// backtick must follow the name — so the `vp skills show <name> --section
// <ref>` span in the same text is never taken for it.
var fallbackCmdRe = regexp.MustCompile("`vp skills show ([a-z0-9-]+)`")

const fallbackSearchClause = "search your deferred or MCP tools for `vp_skill` first"

// personaShim is one fallback-bearing shim file and the skill it names.
type personaShim struct {
	path, name string
}

// personaShimsUnder lists the vps-* persona shims under a project or plugin
// root: .cursor/rules/vps-<name>.mdc and <skillsDir>/vps-<name>/SKILL.md for
// each skills dir given.
func personaShimsUnder(t *testing.T, cursorRules string, skillsDirs ...string) []personaShim {
	t.Helper()
	var out []personaShim
	if cursorRules != "" {
		matches, _ := filepath.Glob(filepath.Join(cursorRules, "vps-*.mdc"))
		for _, m := range matches {
			out = append(out, personaShim{m, strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), "vps-"), ".mdc")})
		}
	}
	for _, d := range skillsDirs {
		matches, _ := filepath.Glob(filepath.Join(d, "vps-*", "SKILL.md"))
		for _, m := range matches {
			out = append(out, personaShim{m, strings.TrimPrefix(filepath.Base(filepath.Dir(m)), "vps-")})
		}
	}
	return out
}

// fallbackArgv reads a persona shim and returns the argv its fallback names,
// asserting the search-first trigger and that the command names this shim's
// own skill.
func fallbackArgv(t *testing.T, s personaShim) []string {
	t.Helper()
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, fallbackSearchClause) {
		t.Errorf("%s: fallback lacks the search-first trigger", s.path)
	}
	m := fallbackCmdRe.FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("%s: want exactly one `vp skills show <name>` span, got %d:\n%s", s.path, len(m), body)
	}
	if m[0][1] != s.name {
		t.Fatalf("%s: fallback names skill %q, want %q", s.path, m[0][1], s.name)
	}
	return strings.Fields(strings.Trim(m[0][0], "`"))
}

// runFallback execs argv the way an agent's shell would — the bare `vp` from
// PATH — in dir, with the test's sandboxed environment.
func runFallback(t *testing.T, dir string, argv []string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code = 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("exec %v: %v", argv, err)
	}
	return o.String(), e.String(), code
}

// cliSkill splits `vp skills show` output into its source tier and body.
func cliSkill(t *testing.T, name, out string) (source, body string) {
	t.Helper()
	header, rest, ok := strings.Cut(out, "\n\n")
	prefix := "# skill: " + name + " | source: "
	if !ok || !strings.HasPrefix(header, prefix) {
		t.Fatalf("unexpected `vp skills show %s` output:\n%s", name, out)
	}
	if i := strings.LastIndex(rest, "\nReferences (fetch with --section=<name>):\n"); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimPrefix(header, prefix), rest
}

// toolSkill calls the real vp_skill handler and splits its frame into the
// source tier and body.
func toolSkill(t *testing.T, resolver *vpctx.Resolver, name string) (source, body string) {
	t.Helper()
	params, _ := json.Marshal(map[string]string{"name": name})
	res, err := tools.SkillCmdTool(resolver).Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("vp_skill %s: %v", name, err)
	}
	frame, _ := res.(string)
	m := regexp.MustCompile(`\| Source: (\S+)\n`).FindStringSubmatch(frame)
	first := strings.Index(frame, "\n\n---\n\n")
	last := strings.LastIndex(frame, "\n\n---\n\n")
	if m == nil || first < 0 || last <= first {
		t.Fatalf("unexpected vp_skill frame:\n%s", frame)
	}
	return m[1], frame[first+len("\n\n---\n\n") : last]
}

// sameBody compares two skill bodies after trimming at most one trailing
// newline from each: the CLI pads a body that lacks one.
func sameBody(a, b string) bool {
	return strings.TrimSuffix(a, "\n") == strings.TrimSuffix(b, "\n")
}

// assertServes runs one shim's fallback command from dir and asserts it exits
// 0 and serves wantSource with wantBody.
func assertServes(t *testing.T, s personaShim, dir, wantSource, wantBody string) (stderr string) {
	t.Helper()
	argv := fallbackArgv(t, s)
	out, errOut, code := runFallback(t, dir, argv)
	if code != 0 {
		t.Fatalf("%s: `%s` exited %d\nstderr: %s", s.path, strings.Join(argv, " "), code, errOut)
	}
	src, body := cliSkill(t, s.name, out)
	if src != wantSource {
		t.Errorf("%s: `%s` served source %q, want %q", s.path, strings.Join(argv, " "), src, wantSource)
	}
	if !sameBody(body, wantBody) {
		t.Errorf("%s: `%s` served a different body than the resolver:\n--- got\n%s\n--- want\n%s", s.path, strings.Join(argv, " "), body, wantBody)
	}
	return errOut
}

// TestIntegrationSkillShimFallbackIsReachable is the reachability pin for the
// persona shims' MCP-less fallback: render every shim kind in a fresh sandbox,
// run the exact command each shim names, and assert it serves the body
// vp_skill would — for the embedded, project and vault-override tiers, and on
// a host with no vault configured at all.
//
// At 22f0d3f this fails at the first shim: it names no `vp skills show`, only
// `<vault>/Templates/skills/<name>/SKILL.md`, a file override-only Templates/
// never creates.
func TestIntegrationSkillShimFallbackIsReachable(t *testing.T) {
	bin := buildVPBinary(t)
	env := setupFreshEnv(t)
	for _, d := range []string{".cursor/rules", ".grok"} {
		if err := os.MkdirAll(filepath.Join(env.projectDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runVP(t, bin, env, nil, "init", env.projectDir,
		"--name", env.projectName, "--vault-path", env.vaultPath, "--no-git")

	// The fallback is a bare `vp`: put the test binary first on PATH. LookPath
	// resolves against this process's PATH, not cmd.Env.
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if got, err := exec.LookPath("vp"); err != nil || got != bin {
		t.Fatalf("`vp` on PATH resolves to %q (%v), want the test binary %q", got, err, bin)
	}

	resolver := vpctx.NewResolver(env.vaultPath)
	resolvedBody := func(name string) string {
		t.Helper()
		sd, _, err := resolver.ResolveSkillDir(name, "", "", "")
		if err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
		return string(sd.SkillMDBody)
	}

	projectShims := personaShimsUnder(t,
		filepath.Join(env.projectDir, shims.CursorRulesDir),
		filepath.Join(env.projectDir, shims.GrokSkillsDir),
		filepath.Join(env.projectDir, shims.ClaudeSkillsDir))
	for _, host := range []string{".cursor", ".grok", ".claude"} {
		n := 0
		for _, s := range projectShims {
			if strings.HasPrefix(s.path, filepath.Join(env.projectDir, host)+string(filepath.Separator)) {
				n++
			}
		}
		if n == 0 {
			t.Fatalf("vp init emitted no %s persona shims", host)
		}
	}

	// 1. Embedded tier: every project persona shim's command serves the body
	// the resolver serves, from the project directory.
	t.Run("embedded", func(t *testing.T) {
		for _, s := range projectShims {
			assertServes(t, s, env.projectDir, "embedded", resolvedBody(s.name))
		}
	})

	var chair personaShim
	for _, s := range projectShims {
		if s.name == "chair" && strings.HasSuffix(s.path, ".mdc") {
			chair = s
		}
	}
	if chair.path == "" {
		t.Fatal("no .cursor/rules/vps-chair.mdc")
	}

	// 2. Project tier: with a project override present, the command run from
	// the project directory (and from a subdirectory) serves it, and so does
	// the real vp_skill handler from the same directory. The override has no
	// trailing newline, so the CLI's padding is exercised.
	t.Run("project", func(t *testing.T) {
		const override = "# Chair, project override\n\nproject-tier chair body"
		writeFile(t, filepath.Join(env.vaultPath, "Projects", env.projectName, "skills", "chair", "SKILL.md"), override)
		sub := filepath.Join(env.projectDir, "internal")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{env.projectDir, sub} {
			assertServes(t, chair, dir, "project", override)
		}
		t.Chdir(env.projectDir)
		src, body := toolSkill(t, resolver, "chair")
		if src != "project" || !sameBody(body, override) {
			t.Errorf("vp_skill served source %q body %q; the fallback served the project override", src, body)
		}
	})

	// 3. Vault tier: a vault Templates/skills override of a built-in is what
	// both routes serve.
	t.Run("vault-override", func(t *testing.T) {
		const override = "---\nname: code-digger\ndescription: vault override\n---\n\nvault-tier code-digger body\n"
		writeFile(t, filepath.Join(env.vaultPath, "Templates", "skills", "code-digger", "SKILL.md"), override)
		want := resolvedBody("code-digger")
		if !strings.Contains(want, "vault-tier code-digger body") {
			t.Fatalf("resolver does not see the vault override: %q", want)
		}
		for _, s := range projectShims {
			if s.name == "code-digger" {
				assertServes(t, s, env.projectDir, "vault", want)
			}
		}
		t.Chdir(env.projectDir)
		if src, body := toolSkill(t, resolver, "code-digger"); src != "vault" || !sameBody(body, want) {
			t.Errorf("vp_skill served source %q body %q; the fallback served the vault override", src, body)
		}
	})

	// 4. User-global trees: the Grok plugin and the Claude plugin render the
	// same fallback from the global vault, and it works from the project.
	globalRoot := filepath.Join(env.home, "global")
	grokRoot := filepath.Join(env.home, shims.GrokUserPluginRel)
	claudeRoot := filepath.Join(globalRoot, "claude-plugin", "vibe-palace")
	t.Run("user-global", func(t *testing.T) {
		rep := shims.InstallGlobalSurfaces(shims.GlobalInstallOptions{
			VaultRoot:        env.vaultPath,
			GrokPluginRoot:   grokRoot,
			ClaudePluginRoot: claudeRoot,
		})
		if len(rep.Errors) > 0 {
			t.Fatalf("InstallGlobalSurfaces: %v", rep.Errors)
		}
		global := personaShimsUnder(t, "",
			filepath.Join(grokRoot, shims.PluginSkillsRel),
			filepath.Join(claudeRoot, shims.PluginSkillsRel))
		if len(global) == 0 {
			t.Fatal("no user-global persona shims rendered")
		}
		for _, s := range global {
			assertServes(t, s, env.projectDir, sourceFor(s.name), resolvedBodyScoped(t, resolver, s.name, env.projectName))
		}
	})

	// 5. No host path in any shim file of any kind — command shims, the hub,
	// persona shims, project and user-global trees.
	t.Run("no-host-path", func(t *testing.T) {
		n := 0
		for _, root := range []string{
			filepath.Join(env.projectDir, ".claude"),
			filepath.Join(env.projectDir, ".cursor"),
			filepath.Join(env.projectDir, ".grok"),
			grokRoot, claudeRoot,
		} {
			_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				data, rerr := os.ReadFile(p)
				if rerr != nil || !bytes.Contains(data, []byte("<!-- vibe-palace:shim v=")) {
					return nil
				}
				n++
				for _, bad := range []string{env.vaultPath, env.home, "Templates/", "{vault}"} {
					if bytes.Contains(data, []byte(bad)) {
						t.Errorf("%s names %q", p, bad)
					}
				}
				return nil
			})
		}
		if n == 0 {
			t.Fatal("found no shim files to check")
		}
	})

	// 6. No vault configured: a host with no global config and no marker —
	// a fresh machine, or a user-global install before `vp init` — renders
	// the user-global shims from no vault, and each command serves the
	// built-in skill with one stderr note instead of failing.
	t.Run("no-config", func(t *testing.T) {
		bare := t.TempDir()
		home := filepath.Join(bare, "home")
		cwd := filepath.Join(home, "work")
		if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Setenv("APPDATA", filepath.Join(home, ".config"))
		noVault := filepath.Join(home, shims.GrokUserPluginRel)
		rep := shims.InstallGlobalSurfaces(shims.GlobalInstallOptions{GrokPluginRoot: noVault})
		if len(rep.Errors) > 0 {
			t.Fatalf("InstallGlobalSurfaces with no vault: %v", rep.Errors)
		}
		embedded := vpctx.NewResolver("")
		global := personaShimsUnder(t, "", filepath.Join(noVault, shims.PluginSkillsRel))
		if len(global) == 0 {
			t.Fatal("no user-global persona shims rendered with no vault")
		}
		for _, s := range global {
			sd, _, err := embedded.ResolveSkillDir(s.name, "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			errOut := assertServes(t, s, cwd, "embedded", string(sd.SkillMDBody))
			if !strings.Contains(errOut, "no vault configured; showing the built-in skill") {
				t.Errorf("%s: stderr %q lacks the no-vault note", s.path, errOut)
			}
		}
	})
}

// sourceFor is the tier the user-global shims' commands serve from the
// project directory once the project and vault overrides above exist.
func sourceFor(name string) string {
	switch name {
	case "chair":
		return "project"
	case "code-digger":
		return "vault"
	default:
		return "embedded"
	}
}

// resolvedBodyScoped is the body vp_skill serves for name in project.
func resolvedBodyScoped(t *testing.T, r *vpctx.Resolver, name, project string) string {
	t.Helper()
	sd, _, err := r.ResolveSkillDir(name, project, "", "")
	if err != nil {
		t.Fatalf("resolve %s: %v", name, err)
	}
	return string(sd.SkillMDBody)
}

// TestIntegrationGrokInstallWithNoVaultIgnoresCwdTemplates: on a host with no
// vault configured, `vp mcp install --grok` renders the user-global Grok shims
// from the embedded tier only. Run from a directory that happens to hold a
// Templates/ tree — a vault checkout, say — it must not list that tree's skill
// or command as the vault tier: before the resolver's listing guard it listed
// ./Templates/skills/cwd-only as a skill, then warned "resolve skill cwd-only"
// because resolution (already guarded) could not find it.
func TestIntegrationGrokInstallWithNoVaultIgnoresCwdTemplates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake grok is a POSIX shell script")
	}
	bin := buildVPBinary(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(home, "vault-checkout")
	fakeBin := filepath.Join(root, "fakebin")
	for _, d := range []string{filepath.Join(home, ".config"), cwd, fakeBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(cwd, "Templates", "skills", "cwd-only", "SKILL.md"), "---\nname: cwd-only\ndescription: not a vault\n---\n\ncwd body\n")
	writeFile(t, filepath.Join(cwd, "Templates", "commands", "cwd-command.md"), "# not a vault command\n")
	fakeLog := filepath.Join(root, "grok.log")
	writeFile(t, filepath.Join(fakeBin, "grok"), "#!/bin/sh\necho \"$@\" >> "+fakeLog+"\nexit 0\n")
	if err := os.Chmod(filepath.Join(fakeBin, "grok"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "mcp", "install", "--grok")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"HOME="+home, "USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "APPDATA="+filepath.Join(home, ".config"),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vp mcp install --grok: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "warning: host surfaces") || strings.Contains(string(out), "cwd-only") {
		t.Errorf("install read the cwd's Templates/ tree:\n%s", out)
	}
	if b, _ := os.ReadFile(fakeLog); !strings.Contains(string(b), "mcp add") {
		t.Errorf("the fake grok was not the one run (log %q)", b)
	}
	plugin := filepath.Join(home, shims.GrokUserPluginRel)
	if _, err := os.Stat(filepath.Join(plugin, shims.PluginSkillsRel, "vps-cwd-only")); err == nil {
		t.Error("a skill from the cwd's Templates/ tree was installed as a user-global shim")
	}
	if _, err := os.Stat(filepath.Join(plugin, shims.PluginCommandsRel, "vpc-cwd-command.md")); err == nil {
		t.Error("a command from the cwd's Templates/ tree was installed as a user-global shim")
	}
	if _, err := os.Stat(filepath.Join(plugin, shims.PluginSkillsRel, "vps-chair", "SKILL.md")); err != nil {
		t.Errorf("the built-in skills were not installed: %v", err)
	}
}
