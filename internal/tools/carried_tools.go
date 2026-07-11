// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Carried-forward bullet editors (vp_carried_*). These operate on the reserved
// ### Carried forward sub-section inside ## Open Threads of a project's
// resume.md. The markdown machinery lives in internal/mdutil (pure string ops);
// vp_carried_add and vp_carried_remove push the edit through
// storage.(*Vault).EditResume, which holds the per-path vaultlock across the
// read→modify→write so concurrent editors cannot lose an update.
//
// vp_carried_promote_to_task reuses the project's existing task-creation backend
// (storage.(*Vault).CreateTask — the same path vp_manage_task create uses)
// rather than reimplementing task-file writing, and pushes its resume edit
// through EditResume too: it creates the task first (task lock taken and
// released inside CreateTask), then removes the bullet under the resume lock, so
// the two per-path locks are held sequentially and never nested.

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/mdutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---------------------------------------------------------------------------
// vp_carried_add
// ---------------------------------------------------------------------------

var carriedAddSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"slug": {"type": "string", "description": "Unique identifier for the carried-forward item (e.g. 'dry-run-coverage-gap')."},
		"title": {"type": "string", "description": "Short one-line description of the item."},
		"body": {"type": "string", "description": "Longer prose detail (optional). Appended after the title."}
	},
	"required": ["project", "slug", "title"]
}`)

// CarriedAddTool appends a bullet to the ### Carried forward sub-section.
func CarriedAddTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_carried_add",
		Mutating: true,
		Description: "Append a bullet to the reserved ### Carried forward " +
			"sub-section inside ## Open Threads of a project's resume.md. The " +
			"bullet is emitted in canonical form '- **{slug}** — {title} {body}'. " +
			"Returns a hard error if the slug already exists (case-insensitive) " +
			"or if the ### Carried forward sub-section is absent.",
		Schema: carriedAddSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project string `json:"project"`
				Slug    string `json:"slug"`
				Title   string `json:"title"`
				Body    string `json:"body"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if args.Slug == "" {
				return nil, fmt.Errorf("slug is required")
			}
			if args.Title == "" {
				return nil, fmt.Errorf("title is required")
			}

			return editResume(vault, args.Project, args.Slug, "", func(content string) (string, error) {
				return mdutil.AddCarriedBullet(content, openThreadsSection, args.Slug, args.Title, args.Body)
			})
		},
	}
}

// ---------------------------------------------------------------------------
// vp_carried_remove
// ---------------------------------------------------------------------------

var carriedRemoveSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"slug": {"type": "string", "description": "Slug of the carried-forward bullet to remove (case-insensitive)."}
	},
	"required": ["project", "slug"]
}`)

// CarriedRemoveTool removes a bullet from the ### Carried forward sub-section.
func CarriedRemoveTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_carried_remove",
		Mutating: true,
		Description: "Remove a bullet from the ### Carried forward sub-section " +
			"of a project's resume.md. The slug match is case-insensitive. " +
			"Returns a hard error (listing the available slugs) if the slug is " +
			"not found.",
		Schema: carriedRemoveSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project string `json:"project"`
				Slug    string `json:"slug"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if args.Slug == "" {
				return nil, fmt.Errorf("slug is required")
			}

			return editResume(vault, args.Project, args.Slug, "", func(content string) (string, error) {
				return mdutil.RemoveCarriedBullet(content, openThreadsSection, args.Slug)
			})
		},
	}
}

// ---------------------------------------------------------------------------
// vp_carried_promote_to_task
// ---------------------------------------------------------------------------

var carriedPromoteSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {"type": "string", "description": "Project slug."},
		"slug": {"type": "string", "description": "Slug of the carried-forward bullet to promote (case-insensitive)."},
		"new_task_slug": {"type": "string", "description": "Slug for the new task file (lowercase alphanumeric + hyphens, no .md extension)."}
	},
	"required": ["project", "slug", "new_task_slug"]
}`)

// CarriedPromoteToTaskTool moves a ### Carried forward bullet to a new task file
// (via the shared CreateTask backend) and removes it from the carried list.
func CarriedPromoteToTaskTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_carried_promote_to_task",
		Mutating: true,
		Description: "Promote a ### Carried forward bullet to a new project task " +
			"and remove it from the carried list. The task file is created via " +
			"the same backend as vp_manage_task create " +
			"(Projects/<project>/tasks/<new_task_slug>.md, status pending); the " +
			"carried bullet's slug becomes the task title and its body becomes the " +
			"task description. Hard error if the carried slug is not found or the " +
			"target task already exists.",
		Schema: carriedPromoteSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Project     string `json:"project"`
				Slug        string `json:"slug"`
				NewTaskSlug string `json:"new_task_slug"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}
			if args.Project == "" {
				return nil, fmt.Errorf("project is required")
			}
			if args.Slug == "" {
				return nil, fmt.Errorf("slug is required")
			}
			if args.NewTaskSlug == "" {
				return nil, fmt.Errorf("new_task_slug is required")
			}

			absPath, err := vault.ResumeFile(args.Project)
			if err != nil {
				return nil, err
			}

			// This handler touches two files, each guarded by its own per-path
			// vaultlock. INVARIANT: the two locks are SEQUENTIAL, never NESTED —
			// CreateTask (step 2) acquires and releases the task file's lock
			// before EditResume (step 3) acquires resume.md's. There is therefore
			// no lock order between them to invert, and nothing to document.
			// vaultlock.Acquire is a blocking LOCK_EX with no LOCK_NB and no
			// timeout, so a nested acquisition would turn any future inversion
			// into a permanent hang rather than an error; do not hoist the resume
			// lock across the CreateTask call.

			// Step 1 (lock-free, read-only): validate the carried bullet exists
			// and capture its body BEFORE anything is created. This content is a
			// validation snapshot only — it is never written back. Step 3 re-reads
			// the authoritative content under the resume lock.
			content, _, err := readResumeForEdit(vault, args.Project)
			if err != nil {
				return nil, err
			}
			bullet, err := mdutil.GetCarriedBullet(content, openThreadsSection, args.Slug)
			if err != nil {
				return nil, err
			}

			// Step 2: create the task via the shared backend (errors if it
			// already exists or if new_task_slug is not a valid slug). CreateTask
			// takes and releases the task file's lock internally.
			taskBody := buildPromotedTaskBody(bullet.Slug, bullet.Body)
			if err := vault.CreateTask(args.Project, args.NewTaskSlug, bullet.Slug, taskBody, "medium"); err != nil {
				return nil, fmt.Errorf("create task: %w", err)
			}

			taskPath, err := vault.TaskFile(args.Project, args.NewTaskSlug)
			if err != nil {
				return nil, err
			}

			// Step 3: remove the bullet from the AUTHORITATIVE resume content
			// under the resume lock. The task file already exists at this point,
			// so both failure modes get an actionable error. (The two-file
			// operation remains non-atomic across a crash — a task can exist while
			// its bullet remains — exactly as before; locking does not change that.)
			var updated string
			var mutateErr error
			err = vault.EditResume(args.Project, func(cur string) (string, error) {
				next, err := mdutil.RemoveCarriedBullet(cur, openThreadsSection, args.Slug)
				if err != nil {
					mutateErr = err
					return "", err
				}
				updated = next
				return next, nil
			})
			if err != nil {
				if mutateErr != nil {
					return nil, fmt.Errorf("task created at %s but failed to remove carried bullet: %w", taskPath, mutateErr)
				}
				return nil, fmt.Errorf("task created at %s but failed to write resume: %w", taskPath, err)
			}

			return map[string]any{
				"project":       args.Project,
				"vault_path":    absPath,
				"bytes_written": len(updated),
				"slug":          args.Slug,
				"new_task_slug": args.NewTaskSlug,
				"task_path":     taskPath,
			}, nil
		},
	}
}

// buildPromotedTaskBody builds the description body for a promoted carried
// bullet, recording its provenance. CreateTask supplies the title and status
// header; this is the content that follows.
func buildPromotedTaskBody(bulletSlug, bulletBody string) string {
	header := fmt.Sprintf("**Source:** Promoted from `### Carried forward` bullet `%s`\n\n## Description\n\n", bulletSlug)
	if bulletBody != "" {
		return header + bulletBody
	}
	return header + "(no body — add details here)"
}
