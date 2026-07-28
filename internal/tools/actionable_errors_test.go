// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
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
