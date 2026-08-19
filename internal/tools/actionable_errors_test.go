// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestActionableErrors_DispatchSubstrings is the acceptance gate for
// mcp-errors-actionable: intentional bad calls must return tool errors a model
// can correct on the next turn (field + remedy), classified as caller friction
// so health does not amber-wash correct guards.
//
// Message-substring stable (not full-string golden) so copy edits do not thrash.
// vp_cmd is read-only and goes through Registry.Dispatch. Mutating tools
// (vp_manage_task, vp_vault_sync) call the registered Handler directly — the
// surface gate needs a vault on context via an unexported key, and these cases
// are handler-body rejections after schema validation would already have passed.
func TestActionableErrors_DispatchSubstrings(t *testing.T) {
	resolver, _ := testResolverOnly(t)

	// --- vp_cmd unknown name via Dispatch ---
	cmdReg := mcp.NewServer(storage.NewVault(t.TempDir())).Registry()
	cmdReg.MustRegister(CmdTool(resolver))
	_, cmdErr := cmdReg.Dispatch(context.Background(), "vp_cmd", json.RawMessage(`{"name":"no-such-cmd-xyz"}`))
	if cmdErr == nil {
		t.Fatal("vp_cmd unknown name: expected error")
	}

	// --- vp_manage_task bare required via Handler (empty string clears schema) ---
	taskVault := storage.NewVault(t.TempDir())
	manage := ManageTaskTool(taskVault)
	_, noProjectErr := manage.Handler(context.Background(), json.RawMessage(
		`{"project":"","action":"cancel","task":"some-task"}`))
	if noProjectErr == nil {
		t.Fatal("manage_task empty project: expected error")
	}
	_, noTaskErr := manage.Handler(context.Background(), json.RawMessage(
		`{"project":"proj","action":"cancel","task":""}`))
	if noTaskErr == nil {
		t.Fatal("manage_task empty task: expected error")
	}

	// --- vp_vault_sync bare push refuse-on-dirty via Handler ---
	root := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	gitT(t, root, "push", "origin", "main")
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}
	syncTool := VaultSyncTool(storage.NewVault(root))
	params, _ := json.Marshal(vaultSyncParams{Action: "push"})
	_, dirtyErr := syncTool.Handler(context.Background(), params)
	if dirtyErr == nil {
		t.Fatal("vault_sync dirty push: expected error")
	}

	cases := []struct {
		name       string
		err        error
		substrings []string
		caller     bool
	}{
		{
			name: "vp_cmd_unknown_name",
			err:  cmdErr,
			substrings: []string{
				"not found",
				"vp_cmd",
				"list",
			},
			caller: true,
		},
		{
			name: "vp_manage_task_empty_project",
			err:  noProjectErr,
			substrings: []string{
				"project",
				"required",
			},
			caller: true,
		},
		{
			name: "vp_manage_task_empty_task",
			err:  noTaskErr,
			substrings: []string{
				"task",
				"required",
			},
			caller: true,
		},
		{
			name: "vp_vault_sync_dirty_push",
			err:  dirtyErr,
			substrings: []string{
				"uncommitted",
				"dirty.txt",
				"vp_vault_status",
			},
			caller: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := tc.err.Error()
			for _, sub := range tc.substrings {
				if !strings.Contains(msg, sub) {
					t.Errorf("error %q missing substring %q", msg, sub)
				}
			}
			if tc.caller && !apperr.IsCaller(tc.err) {
				t.Errorf("error should be apperr.Caller (fault=caller); got %T: %v", tc.err, tc.err)
			}
		})
	}
}

// TestActionableErrors_SkillUnknownName mirrors the vp_cmd list-remedy on the
// skill path (same residual finding; skill is a sibling tool).
func TestActionableErrors_SkillUnknownName(t *testing.T) {
	resolver, _ := testResolverOnly(t)
	reg := mcp.NewServer(storage.NewVault(t.TempDir())).Registry()
	reg.MustRegister(SkillCmdTool(resolver))

	_, err := reg.Dispatch(context.Background(), "vp_skill", json.RawMessage(`{"name":"no-such-skill-xyz"}`))
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
	msg := err.Error()
	for _, sub := range []string{"not found", "vp_skill", "list"} {
		if !strings.Contains(msg, sub) {
			t.Errorf("error %q missing substring %q", msg, sub)
		}
	}
	if !apperr.IsCaller(err) {
		t.Errorf("unknown skill should be apperr.Caller; got %T: %v", err, err)
	}
}

// TestActionableErrors_BootstrapProjectRefusalsAreCallerFault closes the gap
// de331dd left on the highest-traffic tool on the surface: vp_bootstrap_context
// fires at every session start on every host, and its project refusals were
// returned unwrapped, so makeHandler stamped fault=internal and vp_health went
// amber for guards that worked.
//
// 🔴 THE TRAP THIS TEST IS BUILT TO AVOID. {} on BootstrapContextToolExplicit is
// refused by the schema (required:["project"]) BEFORE any handler runs, and
// makeHandler ALREADY stamps fault=caller on schema rejection
// (internal/mcp/tools.go validation branch). So a Registry.Dispatch of {}
// asserting IsCaller passes with every apperr.Caller in context_tools.go
// deleted. That is precisely the fake-pin shape this epic just retired on
// serve-wiring-test-passes-for-the-wrong-reason.
//
// So every case here calls tool.Handler DIRECTLY, bypassing schema validation,
// and asserts the error is NOT an *mcp.ValidationError — mechanizing "we
// actually reached resolveBootstrapProject" instead of trusting the input shape
// to get us there.
func TestActionableErrors_BootstrapProjectRefusalsAreCallerFault(t *testing.T) {
	vault, resolver := testSetup(t)

	// A marked cwd naming a project the vault does NOT have, so the stdio
	// tool's cwd-default path gets past detection and is refused by the
	// exists-arm — the live amber-wash sighting on this task.
	const absentSlug = "amber-wash-absent"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, project.ConfigFileName),
		[]byte("[project]\nname = \""+absentSlug+"\"\n"), 0o644); err != nil {
		t.Fatalf("write project marker: %v", err)
	}
	t.Chdir(dir)

	stdio := BootstrapContextTool(resolver, vault, nil)
	explicit := BootstrapContextToolExplicit(resolver, vault, nil)

	// Guard the fixture itself: if the vault DID carry Projects/<absentSlug>/,
	// the exists-arm case below would sail through to a successful bootstrap
	// and silently stop testing anything.
	if dirPath, err := vault.ProjectDir(absentSlug); err != nil {
		t.Fatalf("vault.ProjectDir(%q): %v", absentSlug, err)
	} else if _, err := os.Stat(dirPath); err == nil {
		t.Fatalf("fixture vault unexpectedly has Projects/%s/ — the exists-arm case would not fire", absentSlug)
	}

	cases := []struct {
		name   string
		tool   mcp.Tool
		params string
		// substrings assert the message stayed actionable: apperr.Caller is
		// transparent, and this task must not weaken the text de331dd wrote.
		substrings []string
	}{
		{
			name:       "transport_refusal",
			tool:       explicit,
			params:     `{"project":""}`,
			substrings: []string{"project is required", "this transport does not default project from cwd"},
		},
		{
			name:       "exists_arm",
			tool:       stdio,
			params:     `{"project":""}`,
			substrings: []string{"project is required", absentSlug, "absent from the vault"},
		},
		{
			name:       "invalid_slug",
			tool:       stdio,
			params:     `{"project":"Not A Slug"}`,
			substrings: []string{"invalid project"},
		},
		{
			name:       "malformed_params",
			tool:       stdio,
			params:     `{"project":`,
			substrings: []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.tool.Handler(context.Background(), json.RawMessage(tc.params))
			if err == nil {
				t.Fatal("expected an error")
			}
			// Anti-trap control: prove we are classifying a HANDLER error. A
			// ValidationError here would mean the schema refused first and this
			// case proves nothing about apperr.Caller.
			var ve *mcp.ValidationError
			if errors.As(err, &ve) {
				t.Fatalf("case reached schema validation, not the handler: %v", err)
			}
			for _, sub := range tc.substrings {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q missing substring %q", err.Error(), sub)
				}
			}
			if !apperr.IsCaller(err) {
				t.Errorf("refusal must be apperr.Caller (fault=caller), else vp_health ambers for a guard that worked; got %T: %v", err, err)
			}
		})
	}
}

// TestBootstrapCwdFaultStaysInternal is the counterpart: the one sibling in
// resolveBootstrapProject deliberately left UNWRAPPED. os.Getwd fails when the
// server process's own working directory has been removed or become unreadable
// — an I/O fault in vp's environment, not a caller supplying bad input — so
// amber is the correct signal and classifying it caller would hide a broken
// process behind the friction counter.
//
// There is no portable way to make os.Getwd fail from a test, so this pins the
// DECISION at the source rather than the behavior: the cwd branch must not be
// wrapped. It fails if someone later "completes" the classification by wrapping
// all four siblings uniformly.
func TestBootstrapCwdFaultStaysInternal(t *testing.T) {
	src, err := os.ReadFile("context_tools.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	const marker = "cannot resolve cwd for defaulting"
	body := string(src)
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("cwd-fault return not found — did the message change? update this pin with it")
	}
	// The return statement opens on the line carrying the marker.
	lineStart := strings.LastIndex(body[:i], "\n") + 1
	line := body[lineStart : strings.Index(body[i:], "\n")+i]
	if strings.Contains(line, "apperr.Caller") {
		t.Errorf("the os.Getwd fault is wrapped as a caller error: %q\n"+
			"It is an I/O fault in vp's own environment; amber is correct for it. "+
			"If this is a deliberate reversal, change the comment above it and this test together.", strings.TrimSpace(line))
	}
}
