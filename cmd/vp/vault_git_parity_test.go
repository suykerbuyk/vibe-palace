// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// parityShape selects what a parity fixture makes movable.
type parityShape struct {
	remote      bool // a bare origin exists
	remoteAhead bool // origin holds a commit the vault lacks (pull, fetch move)
	localAhead  bool // the vault holds a commit origin lacks (push moves)
	artifact    bool // an uncommitted capture artifact (tidy, commit move)
}

// parityVault builds a vault that is its own repository, bound through the
// test's isolated global config, shaped so the operation under test would
// change something. It returns the vault and its bare origin ("" without one).
func parityVault(t *testing.T, shape parityShape) (vault, bare string) {
	t.Helper()
	vault = setupTestVaultEnv(t)
	gitInVault(t, vault, "init", "-q", "-b", "main")
	gitInVault(t, vault, "config", "user.email", "test@test.com")
	gitInVault(t, vault, "config", "user.name", "Test")
	putVaultFile(t, vault, ".gitignore", strings.Join(storage.CanonicalGitignorePatterns, "\n")+"\n")
	putVaultFile(t, vault, "README.md", "seed\n")
	gitInVault(t, vault, "add", "-A")
	gitInVault(t, vault, "commit", "-qm", "seed")
	if shape.remote {
		bare = filepath.Join(t.TempDir(), "origin.git")
		gitInVault(t, vault, "init", "-q", "--bare", "-b", "main", bare)
		gitInVault(t, vault, "remote", "add", "origin", bare)
		gitInVault(t, vault, "push", "-q", "-u", "origin", "main")
	}
	if shape.remoteAhead {
		other := cloneVault(t, bare)
		gitInVault(t, other, "config", "user.email", "o@test.com")
		gitInVault(t, other, "config", "user.name", "O")
		putVaultFile(t, other, "REMOTE.md", "from elsewhere\n")
		gitInVault(t, other, "add", "REMOTE.md")
		gitInVault(t, other, "commit", "-qm", "remote advance")
		gitInVault(t, other, "push", "-q", "origin", "main")
	}
	if shape.localAhead {
		putVaultFile(t, vault, "LOCAL.md", "local\n")
		gitInVault(t, vault, "add", "LOCAL.md")
		gitInVault(t, vault, "commit", "-qm", "local advance")
	}
	if shape.artifact {
		putVaultFile(t, vault, "Projects/foo/sessions/x.md", "session body\n")
	}
	return vault, bare
}

// setParityGitEnabled rewrites the isolated global config's git_enabled,
// keeping the vault binding. It returns the config path.
func setParityGitEnabled(t *testing.T, vault string, enabled bool) string {
	t.Helper()
	p, err := storage.VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	body := `vault_path = "` + vault + `"` + "\ngit_enabled = false\n"
	if enabled {
		body = `vault_path = "` + vault + `"` + "\ngit_enabled = true\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// parityFingerprint is everything a vault git operation can move: HEAD, every
// ref including remote-tracking refs, the index, and the origin's refs.
func parityFingerprint(t *testing.T, vault, bare string) string {
	t.Helper()
	index, err := os.ReadFile(filepath.Join(vault, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(index)
	parts := []string{
		gitInVault(t, vault, "rev-parse", "HEAD"),
		gitInVault(t, vault, "for-each-ref"),
		hex.EncodeToString(sum[:]),
	}
	if bare != "" {
		parts = append(parts, gitInVault(t, bare, "for-each-ref"))
	}
	return strings.Join(parts, "\n")
}

// parityOp is one class-1 operation, reachable from both surfaces.
type parityOp struct {
	name  string
	shape parityShape // what makes it move when git is enabled
	cli   func() *cli.Command
	args  []string
	tool  func(*storage.Vault) mcp.Tool
	param map[string]any
	// noMove: the operation reads only, so the enabled control cannot move the
	// fingerprint (status --no-fetch, and the dry runs). Its refusal is
	// protected by the error assertions alone.
	noMove bool
}

var parityFull = parityShape{remote: true, remoteAhead: true, localAhead: true, artifact: true}

func parityOps() []parityOp {
	return []parityOp{
		{name: "pull", shape: parityShape{remote: true, remoteAhead: true},
			cli: cmdVaultPull, tool: tools.VaultSyncTool, param: map[string]any{"action": "pull"}},
		{name: "push", shape: parityShape{remote: true, localAhead: true},
			cli: cmdVaultPush, tool: tools.VaultSyncTool, param: map[string]any{"action": "push"}},
		{name: "sync", shape: parityFull,
			cli: cmdVaultSync, tool: tools.VaultSyncTool, param: map[string]any{"action": "sync"}},
		{name: "commit", shape: parityShape{remote: true, artifact: true},
			cli: cmdVaultCommit, args: []string{"--message", "m", "--paths", "Projects/foo/sessions/x.md"},
			tool:  tools.VaultSyncTool,
			param: map[string]any{"action": "push", "message": "m", "paths": []string{"Projects/foo/sessions/x.md"}}},
		{name: "tidy", shape: parityShape{remote: true, artifact: true},
			cli: cmdVaultTidy, tool: tools.VaultTidyTool, param: map[string]any{}},
		{name: "status fetch", shape: parityShape{remote: true, remoteAhead: true},
			cli: cmdVaultStatus, tool: tools.VaultStatusTool, param: map[string]any{"refresh": true}},
		{name: "status no-fetch", shape: parityFull, noMove: true,
			cli: cmdVaultStatus, args: []string{"--no-fetch"}, tool: tools.VaultStatusTool, param: map[string]any{}},
		{name: "tidy dry-run", shape: parityFull, noMove: true,
			cli: cmdVaultTidy, args: []string{"--dry-run"}, tool: tools.VaultTidyTool, param: map[string]any{"dry_run": true}},
		// The MCP tools have no pull/push/sync dry run; these rows pin the CLI's.
		{name: "pull dry-run", shape: parityFull, noMove: true, cli: cmdVaultPull, args: []string{"--dry-run"}},
		{name: "push dry-run", shape: parityFull, noMove: true, cli: cmdVaultPush, args: []string{"--dry-run"}},
		{name: "sync dry-run", shape: parityFull, noMove: true, cli: cmdVaultSync, args: []string{"--dry-run"}},
	}
}

func callParityTool(t *testing.T, op parityOp, vault string) error {
	t.Helper()
	params, err := json.Marshal(op.param)
	if err != nil {
		t.Fatal(err)
	}
	_, err = op.tool(storage.NewVault(vault)).Handler(context.Background(), params)
	return err
}

// assertParityRefusal pins the MCP contract of the refusal: errors.Is, caller
// class (so health stays green), the pinned substring, the config path, and
// no remedy an agent could act on by editing the operator's config.
func assertParityRefusal(t *testing.T, surface string, err error, cfgPath string) {
	t.Helper()
	// Errorf, never Fatalf: a missing refusal must not stop the caller before
	// it compares the fingerprint, or a surface routed around storage (break
	// B′) would red only on the error and hide that the vault moved.
	if !errors.Is(err, storage.ErrGitDisabled) {
		t.Errorf("%s: err = %v, want ErrGitDisabled", surface, err)
		return
	}
	if !apperr.IsCaller(err) {
		t.Errorf("%s: refusal is not caller-class: %v", surface, err)
	}
	assertParityText(t, surface, err.Error(), cfgPath)
}

func assertParityText(t *testing.T, surface, text, cfgPath string) {
	t.Helper()
	if !strings.Contains(text, "git is disabled") || !strings.Contains(text, cfgPath) {
		t.Errorf("%s: %q lacks the pinned substring or the config path %s", surface, text, cfgPath)
	}
	for _, forbidden := range []string{"set git_enabled", "git_enabled = true"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("%s: %q instructs a config change (%q)", surface, text, forbidden)
		}
	}
}

// TestVaultGitParityRefusesOnBothSurfaces is acceptance item 1: under
// git_enabled = false every class-1 operation, dry runs included, refuses on
// the CLI (exit 1, the pinned text) and on MCP (errors.Is ErrGitDisabled,
// caller class, the pinned text), and the vault's fingerprint does not move.
// The refusal comes from one place: storage.RefuseIfGitDisabled.
func TestVaultGitParityRefusesOnBothSurfaces(t *testing.T) {
	for _, op := range parityOps() {
		t.Run(op.name, func(t *testing.T) {
			vault, bare := parityVault(t, op.shape)
			cfgPath := setParityGitEnabled(t, vault, false)
			before := parityFingerprint(t, vault, bare)

			_, stderr, code := runVaultCmdCapturingBoth(t, op.cli(), op.args...)
			if code != cli.ExitUser {
				t.Errorf("CLI exit = %d, want %d (ExitUser)\nstderr: %s", code, cli.ExitUser, stderr)
			}
			assertParityText(t, "CLI", stderr, cfgPath)

			if op.tool != nil {
				assertParityRefusal(t, "MCP", callParityTool(t, op, vault), cfgPath)
			}

			if after := parityFingerprint(t, vault, bare); after != before {
				t.Errorf("the vault moved under git_enabled = false:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestVaultGitParityRefusesAVaultWithNoRemotes: the refusal, not "no git
// remotes configured", is what a git-disabled host with no remotes reports —
// the preflight runs before ListRemotes on both surfaces.
func TestVaultGitParityRefusesAVaultWithNoRemotes(t *testing.T) {
	vault, _ := parityVault(t, parityShape{})
	cfgPath := setParityGitEnabled(t, vault, false)

	_, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultPull())
	if code != cli.ExitUser || strings.Contains(stderr, "no git remotes") {
		t.Errorf("CLI exit %d, stderr %q; want the refusal", code, stderr)
	}
	assertParityText(t, "CLI", stderr, cfgPath)

	err := callParityTool(t, parityOp{tool: tools.VaultSyncTool, param: map[string]any{"action": "pull"}}, vault)
	assertParityRefusal(t, "MCP", err, cfgPath)
}

// TestVaultGitParityControlsMoveTheFingerprint is acceptance item 2: with
// git_enabled = true each row's fixture changes under that operation, on
// each surface, so an unchanged fingerprint under the refusal means something.
// Read-only rows (status --no-fetch, the dry runs) cannot move and are
// protected by the error assertions alone.
func TestVaultGitParityControlsMoveTheFingerprint(t *testing.T) {
	for _, op := range parityOps() {
		if op.noMove {
			continue
		}
		t.Run(op.name+"/CLI", func(t *testing.T) {
			vault, bare := parityVault(t, op.shape)
			setParityGitEnabled(t, vault, true)
			before := parityFingerprint(t, vault, bare)
			_, stderr, code := runVaultCmdCapturingBoth(t, op.cli(), op.args...)
			if code != cli.ExitOK {
				t.Fatalf("CLI exit %d\nstderr: %s", code, stderr)
			}
			if parityFingerprint(t, vault, bare) == before {
				t.Error("the enabled CLI operation left the fingerprint unchanged")
			}
		})
		t.Run(op.name+"/MCP", func(t *testing.T) {
			vault, bare := parityVault(t, op.shape)
			setParityGitEnabled(t, vault, true)
			before := parityFingerprint(t, vault, bare)
			if err := callParityTool(t, op, vault); err != nil {
				t.Fatalf("MCP: %v", err)
			}
			if parityFingerprint(t, vault, bare) == before {
				t.Error("the enabled MCP operation left the fingerprint unchanged")
			}
		})
	}
}
