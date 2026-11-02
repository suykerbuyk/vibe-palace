// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcphost

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/suykerbuyk/vibe-palace/internal/codexhook"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const (
	codexBinary = "codex"
	vpBinary    = "vp"
)

// CodexHost registers the MCP server in Codex CLI/IDE's shared config.toml.
type CodexHost struct {
	configPath    string
	homeDir       func() (string, error)
	codexLookPath func() (string, error)
	vpLookPath    func() (string, error)
	// selfExe names the executable image running this process. Tests replace
	// it, and must: under `go test` os.Executable() is the package's .test
	// binary, so every Install test would otherwise trip pinVPOnPath. Mirrors
	// internal/surface's replaceable readSelfExe, for the same reason.
	selfExe func() (string, error)
	runner  func(args ...string) ([]byte, error)
}

func NewCodexHost() *CodexHost {
	h := &CodexHost{configPath: defaultCodexConfigPath(), homeDir: os.UserHomeDir}
	h.codexLookPath = func() (string, error) { return exec.LookPath(h.Executables()[0]) }
	h.vpLookPath = func() (string, error) { return exec.LookPath(h.Executables()[1]) }
	h.selfExe = os.Executable
	h.runner = func(args ...string) ([]byte, error) {
		return exec.Command(h.Executables()[0], args...).CombinedOutput()
	}
	return h
}

func defaultCodexConfigPath() string {
	if root := os.Getenv("CODEX_HOME"); root != "" {
		return filepath.Join(root, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml")
}

func (*CodexHost) Name() string          { return "codex" }
func (*CodexHost) Flag() string          { return "--codex" }
func (*CodexHost) Executables() []string { return []string{codexBinary, vpBinary} }

func (h *CodexHost) Detected() bool {
	if _, err := os.Stat(filepath.Dir(h.configPath)); err == nil {
		return true
	}
	if h.codexLookPath != nil {
		_, err := h.codexLookPath()
		return err == nil
	}
	return false
}

func (h *CodexHost) Installed() (bool, error) {
	raw, err := codexConfigRead(h.configPath)
	if err != nil || raw == nil {
		return false, err
	}
	server, err := readCodexServer(raw)
	if err != nil || server == nil {
		return false, err
	}
	if server.Enabled != nil && !*server.Enabled {
		return false, nil
	}
	return h.ownsCommand(server.Command), nil
}

func (h *CodexHost) Install(_ string, projectRoot string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	// Pinned BEFORE anything is read or written: a refusal must not leave
	// config.toml patched and hooks.json not. It guards the MCP entry too,
	// which is written as the same bare "vp", so it runs even when
	// features.hooks=false suppresses the hook half.
	if err := h.pinVPOnPath(); err != nil {
		return err
	}
	raw, err := codexConfigRead(h.configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", compressHome(h.configPath), err)
	}
	server, err := readCodexServer(raw)
	if err != nil {
		return fmt.Errorf("read Codex MCP config: %w", err)
	}
	if server != nil && !h.ownsCommand(server.Command) {
		return fmt.Errorf("refusing to overwrite foreign Codex MCP server %q command %q", codexServerName, server.Command)
	}
	enabled := true
	patched, err := patchCodexServerTable(raw, codexServerConfig{Command: "vp", Args: []string{"mcp"}, Enabled: &enabled})
	if err != nil {
		return fmt.Errorf("edit %s: %w", compressHome(h.configPath), err)
	}
	if string(patched) != string(raw) {
		if err := backupFile(h.configPath); err != nil {
			return fmt.Errorf("backup %s: %w", compressHome(h.configPath), err)
		}
		if err := writeCodexConfig(h.configPath, patched); err != nil {
			return err
		}
	}
	if _, err := ensureAgentsFile(projectRoot); err != nil {
		fmt.Fprintf(out, "warning: wire AGENTS.md: %v\n", err)
	}
	h.installUserSkills(out)
	h.installCodexHooks(projectRoot, out)
	fmt.Fprintf(out, "vibe-palace registered with Codex in %s; restart Codex to activate.\n", compressHome(h.configPath))
	return nil
}

func (h *CodexHost) Uninstall(out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	raw, err := codexConfigRead(h.configPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", compressHome(h.configPath), err)
	}
	server, err := readCodexServer(raw)
	if err != nil {
		return fmt.Errorf("read Codex MCP config: %w", err)
	}
	if server == nil {
		fmt.Fprintf(out, "vibe-palace is not configured in %s.\n", compressHome(h.configPath))
		return h.removeHostArtifacts(out)
	}
	if !h.ownsCommand(server.Command) {
		return fmt.Errorf("refusing to disable foreign Codex MCP server %q command %q", codexServerName, server.Command)
	}
	enabled := false
	patched, err := patchCodexServerTable(raw, codexServerConfig{Command: server.Command, Args: server.Args, Enabled: &enabled})
	if err != nil {
		return fmt.Errorf("edit %s: %w", compressHome(h.configPath), err)
	}
	if string(patched) != string(raw) {
		if err := backupFile(h.configPath); err != nil {
			return fmt.Errorf("backup %s: %w", compressHome(h.configPath), err)
		}
		if err := writeCodexConfig(h.configPath, patched); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "vibe-palace disabled in Codex config %s; approval policy retained.\n", compressHome(h.configPath))
	return h.removeHostArtifacts(out)
}

func (h *CodexHost) installUserSkills(out io.Writer) {
	home, err := h.userHome()
	if err != nil {
		fmt.Fprintf(out, "warning: install Codex user skills: %v\n", err)
		return
	}
	resolver := vpctx.NewResolver("")
	if vault, err := storage.OpenVaultGlobal(); err == nil {
		resolver = vpctx.NewResolver(vault.Root)
	}
	if _, err := shims.InstallCodexUserSkills(resolver, filepath.Join(home, ".agents", "skills")); err != nil {
		fmt.Fprintf(out, "warning: install Codex user skills: %v\n", err)
	}
}

func (h *CodexHost) removeUserSkills(out io.Writer) error {
	home, err := h.userHome()
	if err != nil {
		fmt.Fprintf(out, "warning: remove Codex user skills: %v\n", err)
		return nil
	}
	if err := shims.RemoveCodexUserSkills(filepath.Join(home, ".agents", "skills")); err != nil {
		return fmt.Errorf("remove Codex user skills: %w", err)
	}
	return nil
}

func (h *CodexHost) installCodexHooks(projectRoot string, out io.Writer) {
	if disabled, unknown := codexHooksDisabled(h.configPath, filepath.Join(projectRoot, ".codex", "config.toml")); disabled {
		fmt.Fprintln(out, "Codex hooks disabled by features.hooks=false; no hook was activated.")
		return
	} else if unknown {
		fmt.Fprintln(out, "Codex hook status unknown at a higher configuration layer; install only the Vibe-Palace user hook.")
	}
	hooksPath := filepath.Join(filepath.Dir(h.configPath), "hooks.json")
	changed, err := codexhook.InstallHooks(hooksPath)
	if err != nil {
		fmt.Fprintf(out, "warning: install Codex hooks: %v\n", err)
		return
	}
	// Silent on a no-op. writeHooks short-circuits on byte equality, so a
	// repeat install changes nothing and has nothing to re-trust; a banner on
	// every run is worse than none. One call means one bool, so this is "if
	// and only if the file changed" with no partial-success path to qualify.
	if !changed {
		return
	}
	fmt.Fprint(out, codexHookTrustAdvisory(compressHome(hooksPath)))
}

// codexHookTrustAdvisory renders the notice for an install that rewrote the
// Codex hooks file.
//
// 🔴 STANDING CONTRACT FOR THIS NOTICE. Change the wording only within it:
//
//	The notice states only (a) what vp did, and (b) what vp cannot determine.
//	It never states that anything is unaffected, safe, or will not run.
//
// This exists because the notice has overclaimed three times, each time while
// trying to be helpful about what the operator need NOT worry about, and each
// time without the knowledge to make the promise. The first draft asserted the
// hooks had been untrusted; the second asserted they would not run; the third
// asserted that a formatting-only or foreign-entry rewrite left trust intact.
// All three were false on a reachable path.
//
// What vp genuinely cannot determine, and why:
//   - The trigger is a BYTE change, which is wider than a trust change: Codex
//     hashes the parsed hook, so a reformat flips the bytes while trust holds.
//   - But a byte change is also NARROWER than it looks in the other direction.
//     Codex's trust record is positional — `<hooks.json>:<event>:<group>:<hook>`
//     — and codexhook.replaceOwnedRows appends the owned group LAST, so its
//     index is the count of preserved foreign groups. Adding, removing or
//     reordering a foreign group moves the owned hook to a different trust key
//     and the old record stops authorising it. Trust can therefore be lost by
//     an edit vp did not make, to an entry vp does not own.
//   - vp cannot read Codex's trust state at all: the hash does not reproduce,
//     and a stale entry from a previous command string survives a rewrite.
//
// So the notice reports the write and the ignorance, and leaves the verdict to
// Codex. The three fields named are the three measured to be hashed;
// additionalContextLimit is deliberately absent, because its hashed-ness was
// never probed.
//
// It states what happened first and then the choices, following
// internal/surface.StaleBinaryAdvisory, because a notice that opens with the
// problem reads as a refusal.
func codexHookTrustAdvisory(path string) string {
	return "⚠ CODEX HOOKS REWRITTEN — REVIEW AND TRUST THEM IN CODEX.\n\n" +
		"Wrote " + path + ". Codex keys hook trust to the hook's command, timeout and\n" +
		"matcher, and its trust record is positional, so a hook's place in the file is\n" +
		"part of its identity too. vp cannot read Codex's trust state, cannot tell you\n" +
		"whether this rewrite invalidated it, and cannot grant trust for you.\n\n" +
		"Codex says NOTHING about an untrusted hook: no warning, no \"skipped\" line. The\n" +
		"only symptom is that bootstrap and session capture quietly stop, and trust is\n" +
		"per-hook, so capture can go silent while bootstrap still works.\n\n" +
		"    To arm them:  open Codex and accept the hook review prompt for the\n" +
		"                  Vibe-Palace SessionStart and SessionEnd hooks, if it appears.\n" +
		"    To verify:    a trusted run prints `hook: SessionStart` lines.\n\n" +
		"This is printed only when an install actually rewrote the file.\n"
}

func (h *CodexHost) removeHostArtifacts(out io.Writer) error {
	if err := h.removeUserSkills(out); err != nil {
		return err
	}
	if err := codexhook.RemoveSessionStartHook(filepath.Join(filepath.Dir(h.configPath), "hooks.json")); err != nil {
		return fmt.Errorf("remove Codex SessionStart hook: %w", err)
	}
	if err := codexhook.RemoveSessionEndHook(filepath.Join(filepath.Dir(h.configPath), "hooks.json")); err != nil {
		return fmt.Errorf("remove Codex SessionEnd hook: %w", err)
	}
	return nil
}

func codexHooksDisabled(configPaths ...string) (disabled, unknown bool) {
	for _, path := range configPaths {
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			unknown = true
			continue
		}
		var doc map[string]any
		if _, err := toml.Decode(string(raw), &doc); err != nil {
			unknown = true
			continue
		}
		features, _ := doc["features"].(map[string]any)
		if enabled, ok := features["hooks"].(bool); ok && !enabled {
			return true, unknown
		}
	}
	return false, unknown
}

func (h *CodexHost) userHome() (string, error) {
	if h.homeDir == nil {
		return "", fmt.Errorf("home directory lookup is unavailable")
	}
	return h.homeDir()
}

// pinRemedy closes every pin refusal. A refusal that names the mismatch
// without naming the way out is an incomplete diagnostic.
const pinRemedy = "Codex runs the hook and the MCP server as a bare \"vp\", resolved through PATH,\n" +
	"so writing this now would configure Codex to run a different program.\n" +
	"Put the intended binary's directory first on PATH, or re-run the vp that\n" +
	"`which vp` already finds."

// pinVPOnPath refuses the install unless the "vp" PATH resolves to is the
// executable running it.
//
// Both the Codex hook command and the Codex MCP server entry are written as a
// bare "vp", so Codex resolves that name through PATH at run time. This pins,
// at install time, that the name means what the operator thinks it means. A
// shadowing binary earlier on PATH — measured as a plain shell script named
// "vp" — otherwise gets configured as the hook, and Codex reports nothing at
// all when a hook is untrusted or wrong, so the failure is invisible.
//
// 🔴 This is an IDENTITY check, not a product-authenticity check. It rejects
// any distinct executable image, including another genuine but different vp:
// an older install, a second checkout's build. Deliberate. A content check (a
// version string, buildinfo) would accept an older genuine vp, which is still
// the wrong program for the hook just written, and is forgeable by anything
// that prints the expected text. Identity needs no parser.
//
// It refuses rather than warning because an install has no work to lose. That
// is the opposite ruling to internal/surface.SelfImageReplaced, which must
// never gate a vault write, and the reason the doctrine does not transfer is
// stated there: refusing a write would lose work to protect a schema.
//
// Sibling: internal/check.installedVP asks a related question for the
// stale-MCP report. It cannot be reused — internal/check imports this package,
// so importing it back would be an import cycle.
func (h *CodexHost) pinVPOnPath() error {
	if h.vpLookPath == nil {
		return fmt.Errorf("refusing to install: no way to resolve %q on PATH\n%s", vpBinary, pinRemedy)
	}
	onPath, err := h.vpLookPath()
	if err != nil {
		return fmt.Errorf("refusing to install: no %q found on PATH: %w\n%s", vpBinary, err, pinRemedy)
	}
	if h.selfExe == nil {
		return fmt.Errorf("refusing to install: no way to identify the running %q image\n%s", vpBinary, pinRemedy)
	}
	self, err := h.selfExe()
	if err != nil {
		return fmt.Errorf("refusing to install: cannot identify the running %q image: %w\n%s", vpBinary, err, pinRemedy)
	}
	if sameExecutable(onPath, self) {
		return nil
	}
	return fmt.Errorf("refusing to install: the %q on PATH is not this binary\n  PATH %q resolves to: %s\n  this binary is:      %s\n%s",
		vpBinary, vpBinary, onPath, self, pinRemedy)
}

// sameExecutable reports whether a and b name the same executable image.
//
// Clean/Abs equality is tried FIRST, and symlinks resolved only if that
// disagrees. The order is load-bearing, not an optimisation: ownsCommand below
// resolves symlinks ONLY and reports false when either side cannot be resolved,
// which would refuse every hermetic test comparing synthetic paths such as
// /usr/bin/vp that do not exist on the machine running the test. This is the
// shape internal/atomicfile already uses for the same question.
func sameExecutable(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if filepath.Clean(absA) == filepath.Clean(absB) {
		return true
	}
	resolvedA, errA := filepath.EvalSymlinks(absA)
	resolvedB, errB := filepath.EvalSymlinks(absB)
	return errA == nil && errB == nil && resolvedA == resolvedB
}

func (h *CodexHost) ownsCommand(command string) bool {
	if command == "vp" {
		return true
	}
	if command == "" || h.vpLookPath == nil {
		return false
	}
	want, err := h.vpLookPath()
	if err != nil {
		return false
	}
	gotAbs, gotErr := filepath.Abs(command)
	wantAbs, wantErr := filepath.Abs(want)
	if gotErr != nil || wantErr != nil {
		return false
	}
	gotResolved, gotErr := filepath.EvalSymlinks(gotAbs)
	wantResolved, wantErr := filepath.EvalSymlinks(wantAbs)
	return gotErr == nil && wantErr == nil && gotResolved == wantResolved
}

func readCodexServer(raw []byte) (*codexServerConfig, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, nil
	}
	var doc map[string]any
	if _, err := toml.Decode(string(raw), &doc); err != nil {
		return nil, err
	}
	servers, _ := doc["mcp_servers"].(map[string]any)
	values, _ := servers[codexServerName].(map[string]any)
	if values == nil {
		return nil, nil
	}
	server := &codexServerConfig{}
	server.Command, _ = values["command"].(string)
	switch args := values["args"].(type) {
	case []string:
		server.Args = args
	case []any:
		for _, arg := range args {
			if value, ok := arg.(string); ok {
				server.Args = append(server.Args, value)
			}
		}
	}
	if enabled, ok := values["enabled"].(bool); ok {
		server.Enabled = &enabled
	}
	return server, nil
}

func writeCodexConfig(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
