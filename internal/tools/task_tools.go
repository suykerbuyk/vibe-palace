// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/taskgraph"
)

// ---------------------------------------------------------------------------
// vp_list_tasks
// ---------------------------------------------------------------------------

type listTasksParams struct {
	Project     string `json:"project"`
	IncludeDone bool   `json:"include_done,omitempty"`
	// IncludeIcebox surfaces tasks the project KNOWS about but has not
	// scheduled. Default false: a backlog that carries everything known is a
	// knowledge base, not a backlog, and that is what made this list unreadable.
	IncludeIcebox bool `json:"include_icebox,omitempty"`
}

var listTasksSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project":        {"type": "string", "description": "Project slug."},
		"include_done":   {"type": "boolean", "description": "Include done/cancelled tasks (default false)."},
		"include_icebox": {"type": "boolean", "description": "Include iceboxed tasks — known, deliberately not scheduled (default false)."}
	},
	"required": ["project"]
}`)

func ListTasksTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_list_tasks",
		Description: "List tasks for a project. Each task may carry a parent (its epic) and a " +
			"list of dependencies; an epic is simply a task that others name as their parent. " +
			"Iceboxed tasks are excluded unless include_icebox is set.",
		Schema:  listTasksSchema,
		Handler: listTasksHandler(vault),
	}
}

func listTasksHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p listTasksParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		tasks, err := vault.ListTasks(p.Project, p.IncludeDone)
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		if !p.IncludeIcebox {
			tasks = storage.DropIcebox(tasks)
		}
		if tasks == nil {
			tasks = []storage.TaskMeta{}
		}
		return map[string]any{"tasks": tasks}, nil
	}
}

// ---------------------------------------------------------------------------
// vp_get_task
// ---------------------------------------------------------------------------

// taskExcerptCap bounds the rune-safe excerpt returned when the caller drops
// the inline body (include_content=false). ~1.5 KB is enough to orient the
// agent; the full body is then fetched via ContentURI with vp_read_resource.
const taskExcerptCap = 1500

type getTaskParams struct {
	Project string `json:"project"`
	Task    string `json:"task"`
	// IncludeContent is tri-state: nil or true ⇒ full inline Content (the
	// pre-Phase-2 behaviour, byte-identical for local Claude); false ⇒ drop the
	// body and return only ContentURI + a bounded Excerpt.
	IncludeContent *bool `json:"include_content,omitempty"`
}

type getTaskResult struct {
	Meta storage.TaskMeta `json:"meta"`
	// Content is the full task body. Omitted (empty) when include_content=false.
	Content string `json:"content,omitempty"`
	// ContentURI and ContentSize are ALWAYS set. ContentURI addresses the full
	// body for vp_read_resource; ContentSize is its length in bytes.
	ContentURI  string `json:"content_uri"`
	ContentSize int    `json:"content_size"`
	// Excerpt is a rune-safe leading slice of the body, set only when the inline
	// Content was dropped (include_content=false).
	Excerpt string `json:"excerpt,omitempty"`
}

var getTaskSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project":         {"type": "string", "description": "Project slug."},
		"task":            {"type": "string", "description": "Task slug."},
		"include_content": {"type": "boolean", "description": "Include the full inline task body. Default true. When false, a large body is dropped in favour of content_uri + a short excerpt (fetch the full body via vp_read_resource); a small body that cannot be truncated is still returned inline. content present means the body is complete; excerpt present means it is partial."}
	},
	"required": ["project", "task"]
}`)

func GetTaskTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_task",
		Description: "Get full task detail including metadata and content.",
		Schema:      getTaskSchema,
		Handler:     getTaskHandler(vault),
	}
}

func getTaskHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p getTaskParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Task == "" {
			return nil, fmt.Errorf("task is required")
		}
		meta, content, err := vault.GetTask(p.Project, p.Task)
		if err != nil {
			return nil, fmt.Errorf("get task: %w", err)
		}

		res := getTaskResult{
			Meta:        meta,
			ContentURI:  mcp.TaskURI(p.Project, p.Task),
			ContentSize: len(content),
		}
		// Tri-state: nil/true keeps the full inline body (Claude unchanged).
		// false drops the body for a host that truncates large results — but only
		// when it is actually large: a body that already fits within taskExcerptCap
		// cannot truncate, so we still return it inline (in Content) rather than as
		// an Excerpt, sparing the agent a vp_read_resource round-trip for content it
		// would receive in full anyway. Content present ⇒ complete body; Excerpt
		// present ⇒ partial, fetch the rest via ContentURI.
		switch {
		case p.IncludeContent == nil || *p.IncludeContent:
			res.Content = content
		case len(content) <= taskExcerptCap:
			res.Content = content
		default:
			res.Excerpt = runeSafeExcerpt(content, taskExcerptCap)
		}
		return res, nil
	}
}

// ---------------------------------------------------------------------------
// vp_manage_task
// ---------------------------------------------------------------------------

type manageTaskParams struct {
	Project  string `json:"project"`
	Action   string `json:"action"`
	Task     string `json:"task"`
	Title    string `json:"title,omitempty"`
	Content  string `json:"content,omitempty"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
	// Parent and DependsOn are POINTERS because they are tri-state on
	// set_relations: absent means "leave it alone", and a present-but-empty value
	// means "clear it". A plain string could not tell those apart, so a caller
	// updating only the dependencies would silently unparent the task.
	Parent    *string   `json:"parent,omitempty"`
	DependsOn *[]string `json:"depends_on,omitempty"`
	// ApprovedByHuman is ATTESTATION, not AUTHORIZATION. See the friction note
	// on manageTaskSchema.
	ApprovedByHuman bool `json:"approved_by_human,omitempty"`
}

// minTaskContentBytes is the floor a create body must clear.
//
// This is FRICTION, not proof. An agent can trivially pad to 200 bytes, and
// nothing here inspects whether the body is a plan or 200 bytes of lorem ipsum.
// What the floor actually catches is the observed failure: an agent that keeps
// the real plan host-locally and drops a short pointer ("see PLAN.md", a bare
// URL, a one-word body) into the vault, leaving the vault — the thing that
// survives the session — holding nothing a future reader can act on. It removes
// the accidental version of that mistake. It does not remove the deliberate one.
const minTaskContentBytes = 200

// manageTaskSchema multiplexes four actions over one tool. Two of them carry
// FRICTION — deliberately, and with a precisely bounded claim:
//
//   - create requires a `content` body (and the handler additionally requires it
//     to clear minTaskContentBytes).
//   - retire requires `approved_by_human`.
//
// approved_by_human is a boolean the AGENT PASSES TO ITSELF. There is no
// approval primitive anywhere in this codebase — no MCP elicitation, no prompt,
// no out-of-band channel to a human. Nothing verifies the attestation, and an
// agent can set it to true having asked nobody. It is ATTESTATION, not
// AUTHORIZATION.
//
// Nor is this door the only door. At least six others reach the identical
// on-disk state: vp_vault_write, vp_vault_edit, vp_vault_move, vp_vault_delete,
// the `vp vault move` CLI (which never enters internal/mcp at all), and plain
// Bash `mv` — the vault is an ordinary git checkout on an ordinary filesystem.
//
// What this buys is real but narrow: it removes the shortest, default,
// didn't-notice path to an agent quietly closing out its own work. It does NOT
// make a task un-retirable without a human, and reading it that way is actively
// harmful — a human who believes "a task CANNOT be retired without me" stops
// reading the diffs, and that is the one property this code does not have.
//
// Three implementation constraints, each a silent-failure trap:
//
//  1. `$schema` is PINNED to 2020-12 explicitly. compileSchema (internal/mcp/
//     tools.go) never calls DefaultDraft, so the library falls back to the
//     latest draft only by accident. If anyone later sets DefaultDraft(Draft6),
//     or a sibling schema introduces a draft-6 `$schema`, if/then is not a
//     keyword in that draft and EVERY conditional in EVERY tool silently stops
//     enforcing — no error, no warning. The pin makes this schema immune.
//
//  2. The conditionals live under `allOf` as TWO separate `if`/`then` members.
//     A JSON object holds one `if` key; two sibling `if`s would silently
//     overwrite each other and only the last would ever run.
//
//  3. `content` and `approved_by_human` are NOT in the top-level `required`
//     array. `required` is enforced server-side before the handler runs
//     (internal/mcp/tools.go, validateParams) and applies to ALL four actions —
//     putting them there would reject every update_status, retire and cancel.
//
// See TestManageTaskSchema_RetireWithoutApprovalIsRefused_CANARY, which fails
// loudly if the draft ever regresses and the conditionals go quiet.
var manageTaskSchema = json.RawMessage(`{
	"$schema": "https://json-schema.org/draft/2020-12/schema",
	"type": "object",
	"properties": {
		"project":  {"type": "string", "description": "Project slug."},
		"action":   {"type": "string", "enum": ["create", "update_status", "set_relations", "retire", "cancel"], "description": "Action: create, update_status, set_relations, retire, or cancel."},
		"task":     {"type": "string", "description": "Task slug."},
		"title":    {"type": "string", "description": "Task title (for create)."},
		"content":  {"type": "string", "description": "Task content body. REQUIRED for create, and must be at least 200 bytes of real plan — not a pointer to a plan stored elsewhere. Do not include a '# Title' heading or '**Status:**'/'**Priority:**'/'**Parent:**'/'**Depends:**' lines; create writes those itself."},
		"priority": {"type": "string", "description": "Priority: low, medium, high, critical (for create)."},
		"status":   {"type": "string", "enum": ["pending", "in_progress", "blocked", "icebox"], "description": "New status (for update_status). 'icebox' means known but deliberately not scheduled — it stays in the active directory and is hidden from default listings. Terminal states are not reachable here: use action=retire or action=cancel, which move the file."},
		"parent":   {"type": "string", "description": "Slug of this task's parent (for create or set_relations). An EPIC is simply a task that others name as their parent — there is no separate epic type. Pass \"\" with set_relations to clear it."},
		"depends_on": {"type": "array", "items": {"type": "string"}, "description": "Slugs this task depends on (for create or set_relations). A dependency on a retired or cancelled task counts as SATISFIED. Pass [] with set_relations to clear."},
		"approved_by_human": {"type": "boolean", "description": "REQUIRED for retire, and must be true. Set this ONLY when the human has actually said the task is done. Nothing verifies this — it is your own attestation, not an authorization check."}
	},
	"required": ["project", "action", "task"],
	"allOf": [
		{
			"if":   {"properties": {"action": {"const": "create"}}, "required": ["action"]},
			"then": {"required": ["content"]}
		},
		{
			"if":   {"properties": {"action": {"const": "retire"}}, "required": ["action"]},
			"then": {"required": ["approved_by_human"]}
		},
		{
			"if":   {"properties": {"action": {"const": "set_relations"}}, "required": ["action"]},
			"then": {"anyOf": [{"required": ["parent"]}, {"required": ["depends_on"]}]}
		}
	]
}`)

func ManageTaskTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_manage_task",
		Mutating: true,
		Description: "Create, update, relate, retire, or cancel a task. create requires a substantive content body; " +
			"retire requires approved_by_human=true, which is your own attestation that the human said the task " +
			"is done — set it only when that is true. set_relations sets a task's parent (its epic) and/or its " +
			"dependencies; structure is derived from those two edges, so an epic is any task others point at.",
		Schema:  manageTaskSchema,
		Handler: manageTaskHandler(vault),
	}
}

// checkParentCycle refuses a parent edge that would close a loop.
//
// Parent cycles are rejected; DEPENDENCY cycles are not. The asymmetry is
// deliberate. A dependency cycle is a real state a plan can pass through while
// it is being written ("A needs B, and actually B needs A — one of these is
// wrong"), and taskgraph reports it loudly without falling over, so refusing the
// write would just make the agent fight the tool. A parent cycle has no
// legitimate transient form: it means a task is inside its own epic, which is
// not a plan in progress, it is nonsense — and it is the shape that makes every
// reader walk in circles.
//
// Cheap by construction: it walks a parent chain, never the whole graph.
func checkParentCycle(vault *storage.Vault, project, task string, parent *string) error {
	if parent == nil || *parent == "" || *parent == task {
		// Self-parenting is caught in storage (normalizeRelations); nothing to
		// walk for a clear.
		return nil
	}

	g, err := taskgraph.BuildFromVault(vault, project)
	if err != nil {
		return fmt.Errorf("build task graph: %w", err)
	}

	// Walk up from the PROPOSED parent. If the chain reaches the task being
	// re-parented, the new edge would close a loop.
	seen := map[string]bool{}
	for cur := *parent; cur != ""; {
		if cur == task {
			return fmt.Errorf(
				"parent %q would create a cycle: %q is already above it in the parent chain — "+
					"a task cannot be inside its own epic",
				*parent, task)
		}
		if seen[cur] {
			// A cycle that already exists on disk. Report it rather than
			// spinning; the graph surfaces it as a finding.
			return fmt.Errorf(
				"parent chain above %q already contains a cycle (run `vp tasks` to see it) — "+
					"fix that before adding another edge",
				*parent)
		}
		seen[cur] = true

		n, ok := g.Nodes[cur]
		if !ok {
			return nil // dangling ancestor: the chain ends, no loop is possible
		}
		cur = n.Meta.Parent
	}
	return nil
}

func manageTaskHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p manageTaskParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}
		if p.Task == "" {
			return nil, fmt.Errorf("task is required")
		}

		switch p.Action {
		case "create":
			// The schema already requires `content` to be PRESENT on create; the
			// length floor lives here because a schema minLength would report as a
			// bare validation failure with no way to say why 200 bytes is the line.
			// Honest label: this is friction, not proof. It catches the agent that
			// stashed the real plan host-locally and wrote a pointer (or a one-word
			// body) into the vault. It does not, and cannot, catch padding.
			if len(p.Content) < minTaskContentBytes {
				return nil, fmt.Errorf(
					"create requires a real plan body: content is %d bytes, minimum %d — "+
						"write the plan itself into the vault, not a pointer to a plan stored elsewhere "+
						"(the vault is what outlives this session; a host-local file is not)",
					len(p.Content), minTaskContentBytes)
			}
			title := p.Title
			if title == "" {
				title = p.Task
			}
			priority := p.Priority
			if priority == "" {
				priority = "medium"
			}
			spec := storage.TaskSpec{Slug: p.Task, Title: title, Content: p.Content, Priority: priority}
			if p.Parent != nil {
				spec.Parent = *p.Parent
			}
			if p.DependsOn != nil {
				spec.Depends = *p.DependsOn
			}
			if err := vault.CreateTask(p.Project, spec); err != nil {
				return nil, fmt.Errorf("create task: %w", err)
			}
			return map[string]string{"status": "created", "task": p.Task}, nil

		case "update_status":
			if p.Status == "" {
				return nil, fmt.Errorf("status is required for update_status action")
			}
			if err := vault.UpdateTaskStatus(p.Project, p.Task, p.Status); err != nil {
				return nil, fmt.Errorf("update task status: %w", err)
			}
			return map[string]string{"status": p.Status, "task": p.Task}, nil

		case "set_relations":
			if p.Parent == nil && p.DependsOn == nil {
				return nil, fmt.Errorf("set_relations requires parent and/or depends_on")
			}
			if err := checkParentCycle(vault, p.Project, p.Task, p.Parent); err != nil {
				return nil, err
			}
			if err := vault.SetTaskRelations(p.Project, p.Task, storage.TaskRelations{
				Parent:  p.Parent,
				Depends: p.DependsOn,
			}); err != nil {
				return nil, fmt.Errorf("set task relations: %w", err)
			}
			result := map[string]any{"status": "relations_set", "task": p.Task}
			if p.Parent != nil {
				result["parent"] = *p.Parent
			}
			if p.DependsOn != nil {
				result["depends_on"] = *p.DependsOn
			}
			return result, nil

		case "retire":
			// The schema requires approved_by_human to be PRESENT on retire; this
			// requires it to be TRUE (and re-checks presence for the Dispatch-free
			// callers that reach the handler directly).
			//
			// Friction, not authorization: the agent can set this to true having
			// asked nobody, and storage.RetireTask is unchanged — vp_vault_move,
			// `vp vault move`, and plain `mv` all still reach the same on-disk
			// state. All this removes is the shortest didn't-notice path.
			if !p.ApprovedByHuman {
				return nil, fmt.Errorf(
					"retire requires explicit human approval — the human must say the task is done. " +
						"Set approved_by_human=true ONLY after they have actually said so; " +
						"nothing here verifies it, so it is your attestation, not a check")
			}
			if err := vault.RetireTask(p.Project, p.Task); err != nil {
				return nil, fmt.Errorf("retire task: %w", err)
			}
			return map[string]string{"status": "retired", "task": p.Task}, nil

		case "cancel":
			if err := vault.CancelTask(p.Project, p.Task); err != nil {
				return nil, fmt.Errorf("cancel task: %w", err)
			}
			return map[string]string{"status": "cancelled", "task": p.Task}, nil

		default:
			return nil, fmt.Errorf("invalid action %q: must be create, update_status, retire, or cancel", p.Action)
		}
	}
}
