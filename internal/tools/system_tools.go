// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// vaultSyncMu serializes concurrent vault git operations.
var vaultSyncMu sync.Mutex

// ---------------------------------------------------------------------------
// vp_init
// ---------------------------------------------------------------------------

type initParams struct {
	Path   string   `json:"path"`
	Name   string   `json:"name,omitempty"`
	Domain string   `json:"domain,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

var initSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"path":   {"type": "string", "description": "Absolute path to the project directory."},
		"name":   {"type": "string", "description": "Project name slug (default: auto-detect)."},
		"domain": {"type": "string", "description": "Domain (e.g. work, personal, opensource)."},
		"tags":   {"type": "array", "items": {"type": "string"}, "description": "Project tags."}
	},
	"required": ["path"]
}`)

func InitProjectTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_init",
		Mutating:    true,
		Description: "Initialize a new vibe-palace project: create .vibe-palace.toml and vault directories.",
		Schema:      initSchema,
		Handler:     initProjectHandler(vault),
	}
}

func initProjectHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p initParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Path == "" {
			return nil, fmt.Errorf("path is required")
		}
		if !filepath.IsAbs(p.Path) {
			return nil, fmt.Errorf("path must be absolute, got %q", p.Path)
		}
		if strings.Contains(p.Path, "..") {
			return nil, fmt.Errorf("path must not contain '..'")
		}

		configPath := filepath.Join(p.Path, project.ConfigFileName)
		if _, err := os.Stat(configPath); err == nil {
			return nil, fmt.Errorf("%s already exists at %s", project.ConfigFileName, p.Path)
		}

		name := p.Name
		if name == "" {
			detected, err := project.DetectProject(p.Path)
			if err == nil {
				name = detected
			} else {
				name = filepath.Base(p.Path)
			}
		}
		if err := slug.Validate(name); err != nil {
			return nil, fmt.Errorf("invalid project name %q: %w", name, err)
		}

		// Write .vibe-palace.toml.
		content := fmt.Sprintf("[project]\nname = %q\n", name)
		if p.Domain != "" {
			content += fmt.Sprintf("domain = %q\n", p.Domain)
		}
		if len(p.Tags) > 0 {
			quoted := make([]string, len(p.Tags))
			for i, tag := range p.Tags {
				quoted[i] = fmt.Sprintf("%q", tag)
			}
			content += fmt.Sprintf("tags = [%s]\n", strings.Join(quoted, ", "))
		}

		if err := os.MkdirAll(p.Path, 0755); err != nil {
			return nil, fmt.Errorf("create project dir: %w", err)
		}
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			return nil, fmt.Errorf("write config: %w", err)
		}

		// Create vault directories.
		tasksDir, err := vault.TasksDir(name)
		if err == nil {
			os.MkdirAll(filepath.Join(tasksDir, "done"), 0755)
			os.MkdirAll(filepath.Join(tasksDir, "cancelled"), 0755)
		}

		return map[string]string{"status": "initialized", "project": name, "path": p.Path}, nil
	}
}

// ---------------------------------------------------------------------------
// vp_vault_sync
// ---------------------------------------------------------------------------

type vaultSyncParams struct {
	Action  string   `json:"action"`
	Paths   []string `json:"paths"`
	Message string   `json:"message"`
}

var vaultSyncSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "description": "Action: pull, push, or sync."},
		"paths": {"type": "array", "items": {"type": "string"}, "description": "Optional explicit vault-relative paths to stage and commit before pushing. When provided, ONLY these paths are committed (never git add -A); other dirty files are left untouched. When omitted, push/sync refuse to run on a dirty vault."},
		"message": {"type": "string", "description": "Commit message. Required when paths is provided."}
	},
	"required": ["action"]
}`)

func VaultSyncTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_sync",
		Mutating: true,
		Description: "Pull, push, or sync the vault git repository with configured " +
			"remotes. With no paths, push/sync refuse to run if the vault has " +
			"uncommitted changes (accidental-half-written-state guard). Pass an " +
			"explicit paths list (plus message) to stage and commit ONLY those " +
			"paths before pushing — other dirty files are left untouched; git " +
			"add -A is never used.",
		Schema:  vaultSyncSchema,
		Handler: vaultSyncHandler(vault),
	}
}

func vaultSyncHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p vaultSyncParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}

		switch p.Action {
		case "pull", "push", "sync":
		default:
			return nil, fmt.Errorf("invalid action %q: must be pull, push, or sync", p.Action)
		}

		vaultSyncMu.Lock()
		defer vaultSyncMu.Unlock()

		root := vault.Root

		// Explicit-path entry point: stage and commit ONLY the supplied
		// paths (never git add -A), then push when the action calls for it.
		// This is the ONLY way to mutate vault history through this tool; the
		// bare push/sync path below keeps its refuse-on-dirty guard intact.
		if len(p.Paths) > 0 {
			if p.Message == "" {
				return nil, fmt.Errorf("message is required when paths are provided")
			}
			doPush := p.Action == "push" || p.Action == "sync"
			res, err := storage.CommitAndPushPaths(root, p.Message, p.Paths, doPush)
			if err != nil {
				return nil, fmt.Errorf("commit: %w", err)
			}
			remoteResults := map[string]string{}
			for name, rerr := range res.RemoteResults {
				if rerr != nil {
					remoteResults[name] = rerr.Error()
				} else {
					remoteResults[name] = "ok"
				}
			}
			return map[string]any{
				"status":         "ok",
				"action":         p.Action,
				"committed":      res.CommitSHA != "",
				"commit_sha":     res.CommitSHA,
				"pushed":         doPush,
				"remote_results": remoteResults,
			}, nil
		}

		remotes, err := gitRemoteList(root)
		if err != nil {
			return nil, fmt.Errorf("discover remotes: %w", err)
		}

		var output strings.Builder

		if p.Action == "pull" || p.Action == "sync" {
			out, err := gitPull(root, remotes)
			output.WriteString(out)
			if err != nil {
				return nil, fmt.Errorf("pull: %w", err)
			}
		}

		if p.Action == "push" || p.Action == "sync" {
			out, err := gitPush(root, remotes)
			output.WriteString(out)
			if err != nil {
				return nil, fmt.Errorf("push: %w", err)
			}
		}

		return map[string]string{
			"status": "ok",
			"action": p.Action,
			"output": output.String(),
		}, nil
	}
}

func gitRemoteList(root string) ([]string, error) {
	cmd := exec.Command("git", "-C", root, "remote")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git remote: %w (is %s a git repo?)", err, root)
	}
	var remotes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			remotes = append(remotes, line)
		}
	}
	if len(remotes) == 0 {
		return nil, fmt.Errorf("no git remotes configured in %s", root)
	}
	return remotes, nil
}

func gitPull(root string, remotes []string) (string, error) {
	var buf strings.Builder
	for _, remote := range remotes {
		cmd := exec.Command("git", "-C", root, "pull", remote, "main")
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		err := cmd.Run()
		fmt.Fprintf(&buf, "[pull %s] %s\n", remote, strings.TrimSpace(combined.String()))
		if err != nil {
			return buf.String(), fmt.Errorf("%s: %w", remote, err)
		}
	}
	return buf.String(), nil
}

func gitPush(root string, remotes []string) (string, error) {
	// Check for clean state.
	cmd := exec.Command("git", "-C", root, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git status: %w", err)
	}
	if len(bytes.TrimSpace(out)) > 0 {
		return "", fmt.Errorf("vault has uncommitted changes, commit before pushing")
	}

	var buf strings.Builder
	for _, remote := range remotes {
		cmd := exec.Command("git", "-C", root, "push", remote, "main")
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		err := cmd.Run()
		fmt.Fprintf(&buf, "[push %s] %s\n", remote, strings.TrimSpace(combined.String()))
		if err != nil {
			return buf.String(), fmt.Errorf("%s: %w", remote, err)
		}
	}
	return buf.String(), nil
}

// ---------------------------------------------------------------------------
// vp_refresh_index
// ---------------------------------------------------------------------------

type refreshIndexParams struct {
	Project string `json:"project"`
}

var refreshIndexSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."}
	},
	"required": ["project"]
}`)

func RefreshIndexTool(engine *search.Engine) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_refresh_index",
		Description: "Rebuild the semantic search index for a project from scratch.",
		Schema:      refreshIndexSchema,
		Handler:     refreshIndexHandler(engine),
	}
}

func refreshIndexHandler(engine *search.Engine) mcp.HandlerFunc {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		var p refreshIndexParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if err := engine.Rebuild(ctx, p.Project); err != nil {
			return nil, fmt.Errorf("rebuild index: %w", err)
		}
		return map[string]string{"status": "rebuilt", "project": p.Project}, nil
	}
}
