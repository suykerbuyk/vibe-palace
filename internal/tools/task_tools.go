// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
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
	// Epic, Standalone and EpicsOnly are the three OPTIONAL, MUTUALLY-EXCLUSIVE
	// derivation modes. With none set the tool returns the flat vault listing
	// exactly as before; with one set the graph is built and the corresponding
	// derived view is returned. Setting two or more is a caller error.
	//
	// Epic returns the transitive subtree rooted at an epic or story slug.
	Epic string `json:"epic,omitempty"`
	// Standalone returns only standalone tasks (no parent, no children).
	Standalone bool `json:"standalone,omitempty"`
	// EpicsOnly returns only the root epics with transitive open/total counts.
	EpicsOnly bool `json:"epics_only,omitempty"`
}

// taskWithRole is a TaskMeta carrying its DERIVED role ("epic"|"story"|"task")
// alongside. TaskMeta is embedded so every existing task JSON field is preserved
// byte-for-byte and `role` rides beside them; the flat no-param path still
// returns plain storage.TaskMeta, so only the derived modes pay for the field.
type taskWithRole struct {
	storage.TaskMeta
	Role string `json:"role"`
}

// epicRow is one root-epic roll-up returned by epics_only: the epic's own
// header fields plus the TRANSITIVE descendant counts under it (open excludes
// the archive, total includes it — the fixed formula from `vp tasks epics`).
type epicRow struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
	Open     int    `json:"open"`
	Total    int    `json:"total"`
	Role     string `json:"role"`
}

var listTasksSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project":        {"type": "string", "description": "Project slug."},
		"include_done":   {"type": "boolean", "description": "Include done/cancelled tasks (default false)."},
		"include_icebox": {"type": "boolean", "description": "Include iceboxed tasks — known, deliberately not scheduled (default false)."},
		"epic":           {"type": "string", "description": "Return the subtree (transitive) rooted at this epic or story slug. Mutually exclusive with standalone and epics_only."},
		"standalone":     {"type": "boolean", "description": "Return only standalone tasks — no parent and no children. Mutually exclusive with epic and epics_only."},
		"epics_only":     {"type": "boolean", "description": "Return only the root epics, each with transitive open/total descendant counts. Mutually exclusive with epic and standalone."}
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

		// At most ONE derivation mode may be requested. Reject the nonsensical
		// combination up front, before touching the vault or the graph.
		modes := 0
		if p.Epic != "" {
			modes++
		}
		if p.Standalone {
			modes++
		}
		if p.EpicsOnly {
			modes++
		}
		if modes > 1 {
			return nil, fmt.Errorf(
				"epic, standalone, and epics_only are mutually exclusive: set at most one")
		}

		// Fast path — no derivation requested. Byte-identical to the historical
		// behaviour: the flat vault listing, icebox dropped unless asked, returned
		// as plain []storage.TaskMeta under {"tasks": ...}. The graph is NOT built
		// here.
		if modes == 0 {
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

		// A derived view was requested: build the whole-set graph once.
		g, err := taskgraph.BuildFromVault(vault, p.Project)
		if err != nil {
			return nil, fmt.Errorf("build task graph: %w", err)
		}
		includeArchived := p.IncludeDone

		switch {
		case p.EpicsOnly:
			// Root epics only. g.Epics also carries nested stories, so filter to
			// IsRootEpic. Counts are TRANSITIVE descendants excluding the epic node
			// itself; the formula is fixed (OPEN excludes the archive, TOTAL
			// includes it) — include_done governs only whether a fully-archived
			// root epic is LISTED, matching `vp tasks epics`.
			rows := []epicRow{}
			for _, slug := range g.Epics {
				if !g.IsRootEpic(slug) {
					continue
				}
				n := g.Nodes[slug]
				if n.Meta.Done && !p.IncludeDone {
					continue
				}
				total, _ := g.Subtree(slug, p.IncludeIcebox, true)
				open, _ := g.Subtree(slug, p.IncludeIcebox, false)
				rows = append(rows, epicRow{
					Slug:     slug,
					Title:    n.Meta.Title,
					Priority: n.Meta.Priority,
					Status:   n.Meta.Status,
					Open:     descendantCount(slug, open.Members),
					Total:    descendantCount(slug, total.Members),
					Role:     "epic",
				})
			}
			return map[string]any{"epics": rows}, nil

		case p.Epic != "":
			// Any grouping node — an epic OR a story — is accepted; a leaf is not.
			slug := p.Epic
			if _, ok := g.Nodes[slug]; !ok {
				return nil, fmt.Errorf("no such task: %q", slug)
			}
			if g.Role(slug) == "task" {
				return nil, fmt.Errorf(
					"epic must name an epic or story (a task with children); %q is a leaf task", slug)
			}
			sub, _ := g.Subtree(slug, p.IncludeIcebox, includeArchived)
			return map[string]any{"tasks": membersWithRole(g, sub.Members)}, nil

		default: // p.Standalone
			// The Epic=="" bucket of GroupedArchived: tasks that are nobody's child
			// and nobody's parent. Every such member's role is "task".
			var members []string
			for _, grp := range g.GroupedArchived(p.IncludeIcebox, includeArchived) {
				if grp.Epic == "" {
					members = grp.Members
					break
				}
			}
			return map[string]any{"tasks": membersWithRole(g, members)}, nil
		}
	}
}

// descendantCount counts a subtree's members EXCLUDING the root itself. Subtree
// returns "root + transitive descendants", so the root is dropped — the same
// count `vp tasks epics` reports.
func descendantCount(root string, members []string) int {
	n := 0
	for _, m := range members {
		if m != root {
			n++
		}
	}
	return n
}

// membersWithRole maps subtree/standalone member slugs to their TaskMeta
// augmented with the derived role, skipping any slug absent from the node set
// (Subtree only ever yields present nodes, so this is defensive). It always
// returns a non-nil slice so the JSON envelope carries [] rather than null.
func membersWithRole(g *taskgraph.Graph, members []string) []taskWithRole {
	out := make([]taskWithRole, 0, len(members))
	for _, m := range members {
		n, ok := g.Nodes[m]
		if !ok {
			continue
		}
		out = append(out, taskWithRole{TaskMeta: n.Meta, Role: g.Role(m)})
	}
	return out
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

// getTaskResult is the canonical instance of the defect this task fixes, and
// the worst-measured one on the surface.
//
// 🔴 THE RECOVERY HANDLE USED TO BE DECLARED AFTER THE BODY IT RESCUES.
// encoding/json emits struct fields in declaration order and nothing on the
// response path re-serializes through a map (mcp.marshalResult hands the value
// straight to mcplib.NewToolResultJSON), so declaration order is wire order is
// CUT order on a host with a flat inline cap. Measured 2026-08-12 against the
// live vault: vp_get_task on mlnx-sw-os/switch-image-pipeline-via-slaved-vm
// returned 192,060 bytes with content_uri at byte 191,956 — 172 KB past the
// 19,968-byte cut. The hatch was reachable exactly when the body already fit
// and gone exactly when it did not.
//
// Worse, it made the OTHER mitigation unreachable too: include_content=false is
// opt-in, and an agent can only opt in if it knows the handle exists — which it
// learns from content_uri, which it never received.
//
// So the layout is now the one vp_read_resource has always had: address and
// size first, body last. Field order here is a transport contract; see
// TestSurfaceHandlesPrecedeBulk and TestSurfaceTruncatedPrefixIsDetectable.
type getTaskResult struct {
	Meta storage.TaskMeta `json:"meta"`
	// ContentURI and ContentSize are ALWAYS set. ContentURI addresses the full
	// body for vp_read_resource; ContentSize is its length in bytes, so a caller
	// holding a truncated document can tell how much of the body it is missing.
	ContentURI  string `json:"content_uri"`
	ContentSize int    `json:"content_size"`
	// Excerpt is a rune-safe leading slice of the body, set only when the inline
	// Content was dropped (include_content=false). Bounded by taskExcerptCap, so
	// it rides above Content with the handles rather than with the bulk.
	Excerpt string `json:"excerpt,omitempty"`
	// Content is the full task body — the only unbounded field, and therefore
	// the one a host cut is allowed to land in. Omitted (empty) when
	// include_content=false.
	Content string `json:"content,omitempty"`

	// 🔴 THE TERMINAL SENTINEL — last field, no omitempty, always true on a
	// successful return. Its ABSENCE is the signal: present ⇒ every byte of
	// this document arrived, absent ⇒ the host cut it, whether or not the host
	// said so (Claude Code truncates silently). Reordering alone only makes a
	// cut RECOVERABLE; it does not make it DETECTABLE, and an agent that cannot
	// detect the cut never decides to reach for content_uri. No omitempty
	// because a false bool would vanish and make "cut" and "whole" serialize
	// identically. Anything declared after this field re-opens the hole.
	Complete bool `json:"complete"`
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
		Name: "vp_get_task",
		Description: "Get full task detail including metadata and content. The result LEADS with `content_uri` + `content_size` and ENDS with `complete`: " +
			"if you do not see `complete: true`, your host truncated the body — read it in pages via `content_uri` with vp_read_resource.",
		Schema:  getTaskSchema,
		Handler: getTaskHandler(vault),
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
			Complete:    true,
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
	// Section names the H2 block that amend replaces or appends. It is what makes
	// a repeated amend converge rather than duplicate.
	Section string `json:"section,omitempty"`
	// ApprovedByHuman is ATTESTATION, not AUTHORIZATION. See the friction note
	// on manageTaskSchema.
	ApprovedByHuman bool `json:"approved_by_human,omitempty"`
	// ToProject is the DESTINATION project of action=move.
	//
	// 🔴 IT REACHED manageTaskSchema LAST, AFTER THE ARM WORKED, and the order
	// was the point. THE SCHEMA ADVERTISES ONLY WHAT IS BUILT
	// (internal/tools/vault_split.go:34-38): a surface that lists a capability
	// the code does not have is the honest-instruments defect in its purest
	// form. Through the refuse-only phase this field and the `move` enum entry
	// were both deliberately absent, so validateParams rejected action=move
	// against the enum before the handler could run and the arm was reachable
	// only in-process. It is on the wire now because the arm now performs the
	// whole operation — rename, destination provenance, source tombstone — and
	// not one step before.
	//
	// A plain string, not a pointer: unlike parent and depends_on it is not
	// tri-state. There is no "clear the destination" — a move without one is
	// not a move.
	ToProject string `json:"to_project,omitempty"`
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

// manageTaskSchema multiplexes nine actions over one tool. Three of them carry
// FRICTION — deliberately, and with a precisely bounded claim:
//
//   - create requires a `content` body (and the handler additionally requires it
//     to clear minTaskContentBytes).
//   - overwrite requires a `content` body, and the handler additionally refuses
//     an archived task and any body that disagrees with the current header.
//   - retire requires `approved_by_human`.
//
// `move` requires `to_project`, which is a REQUIRED PARAMETER rather than
// friction: a move with no destination is not a move. The refusals that make it
// safe — archived source, dangling edge, occupied destination — live in
// storage.MoveTaskToProject, where they can measure the vault.
//
// amend carries no friction and deliberately no minimum length: a decision worth
// recording is often one line ("Option B; the re-key is unjustified"), and a
// floor there would only teach agents to pad. create's floor exists because a
// task with no plan is useless to a future reader; an amend's value does not
// scale with its size.
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
		"action":   {"type": "string", "enum": ["create", "amend", "overwrite", "set_meta", "update_status", "set_relations", "retire", "cancel", "move"], "description": "Action: create, amend, overwrite, set_meta, update_status, set_relations, retire, cancel, or move."},
		"task":     {"type": "string", "description": "Task slug."},
		"title":    {"type": "string", "description": "Task title (for create or set_meta). set_meta is the ONLY way to change a title after creation \u2014 and a title stating a premise you have since disproved keeps reaching every agent at session start as the headline, because this is the field vp_list_tasks and vp_bootstrap_context surface."},
		"content":  {"type": "string", "description": "Task content body. REQUIRED for create (min 200 bytes of real plan, not a pointer to a plan stored elsewhere), for amend (the section body), and for overwrite. Do not include a '# Title' heading or '**Status:**'/'**Priority:**'/'**Parent:**'/'**Depends:**' lines; create writes those itself and amend must never touch them. For amend, do not include an '## H2' heading either — the 'section' parameter supplies it; use '###' for sub-headings. \ud83d\udd34 For OVERWRITE the meaning INVERTS: content is the ENTIRE file including the '# Title' line and the whole header block, and every header field must MATCH the task as it stands \u2014 a body that changes title, status, priority, parent or depends is REFUSED, because each of those has its own action."},
		"section":  {"type": "string", "description": "REQUIRED for amend: the H2 heading TEXT (no '##' markup) of the section to replace, or to append if absent. Amend is keyed on this so a repeated amend CONVERGES instead of duplicating the section. Use it to record decisions, review findings and reversals into a task's plan — e.g. section=\"Decision (iter 205)\"."},
		"priority": {"type": "string", "description": "Priority: low, medium, high, critical (for create or set_meta). set_meta is the ONLY way to re-prioritize an existing task."},
		"status":   {"type": "string", "enum": ["pending", "in_progress", "blocked", "icebox"], "description": "New status (for update_status). 'icebox' means known but deliberately not scheduled — it stays in the active directory and is hidden from default listings. Terminal states are not reachable here: use action=retire or action=cancel, which move the file."},
		"parent":   {"type": "string", "description": "Slug of this task's parent (for create or set_relations). An EPIC is simply a task that others name as their parent — there is no separate epic type. Pass \"\" with set_relations to clear it."},
		"depends_on": {"type": "array", "items": {"type": "string"}, "description": "Slugs this task depends on (for create or set_relations). A dependency on a retired or cancelled task counts as SATISFIED. Pass [] with set_relations to clear."},
		"approved_by_human": {"type": "boolean", "description": "REQUIRED for retire, and must be true. Set this ONLY when the human has actually said the task is done. Nothing verifies this — it is your own attestation, not an authorization check."},
		"to_project": {"type": "string", "description": "REQUIRED for move, and meaningless for every other action: the DESTINATION project slug (from vp_list_projects) to move the task INTO. 'project' names where the task is NOW. Only ACTIVE tasks move — an archived body is the record of what happened in the project it happened in. The move is REFUSED if the task's parent or any depends_on slug does not resolve in the destination (fix it first with action=set_relations, or move the counterpart across too), and refused if a task of the same slug already lives there. On success it appends a \"Moved from <source>\" section to the task in the destination and files a tombstone at Projects/<source>/tasks/cancelled/<task>.md recording where the work went."}
	},
	"required": ["project", "action", "task"],
	"allOf": [
		{
			"if":   {"properties": {"action": {"const": "create"}}, "required": ["action"]},
			"then": {"required": ["content"]}
		},
		{
			"if":   {"properties": {"action": {"const": "amend"}}, "required": ["action"]},
			"then": {"required": ["section", "content"]}
		},
		{
			"if":   {"properties": {"action": {"const": "overwrite"}}, "required": ["action"]},
			"then": {"required": ["content"]}
		},
		{
			"if":   {"properties": {"action": {"const": "retire"}}, "required": ["action"]},
			"then": {"required": ["approved_by_human"]}
		},
		{
			"if":   {"properties": {"action": {"const": "set_relations"}}, "required": ["action"]},
			"then": {"anyOf": [{"required": ["parent"]}, {"required": ["depends_on"]}]}
		},
		{
			"if":   {"properties": {"action": {"const": "set_meta"}}, "required": ["action"]},
			"then": {"anyOf": [{"required": ["title"]}, {"required": ["priority"]}]}
		},
		{
			"if":   {"properties": {"action": {"const": "move"}}, "required": ["action"]},
			"then": {"required": ["to_project"]}
		}
	]
}`)

func ManageTaskTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_manage_task",
		Mutating: true,
		Description: "Create, amend, overwrite, update, relate, retire, cancel, or move a task between projects. create requires a substantive content body; " +
			"retire requires approved_by_human=true, which is your own attestation that the human said the task " +
			"is done — set it only when that is true. set_relations sets a task's parent (its epic) and/or its " +
			"dependencies; structure is derived from those two edges, so an epic is any task others point at. " +
			"amend is the SECTION writer and the normal way to change a task's PLAN: it replaces the named H2 " +
			"`section` (or appends it if absent). The result's `op` is `replaced` or `appended` — a same-name " +
			"re-run converges by replacing, and that collision is now visible instead of silent. " +
			"Use it whenever a plan is superseded — a task whose body still states a premise you have disproved " +
			"is a task that will be implemented wrong. overwrite replaces an ACTIVE task's WHOLE file and is the " +
			"only path to text amend cannot address: the preamble above the first H2, an H2 heading's own wording, " +
			"or a whole-file migration. Its `content` is the entire file, header block included, and every header " +
			"field must match the task as it stands — title, status, priority, parent and depends each have their " +
			"own action, so a body that changes one is refused rather than silently becoming a second writer. " +
			"Prefer amend for a single section; reach for overwrite only when amend cannot express the change. " +
			"move relocates an ACTIVE task into another project named by `to_project` and records where it went at " +
			"BOTH ends: a \"Moved from <source>\" section on the task in the destination, and a tombstone at " +
			"Projects/<source>/tasks/cancelled/<task>.md. It NEVER writes parent or depends_on — an edge that would " +
			"dangle in the destination is refused and names set_relations as the owner of the fix.",
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
		// Empty-string guards (schema required only checks key presence). Caller
		// fault: name the field and a next-turn remedy. apperr.Caller keeps the
		// rejection out of health amber-wash.
		if p.Project == "" {
			return nil, apperr.Caller(fmt.Errorf(
				"vp_manage_task: 'project' is required — pass the project slug (from vp_list_projects or vp_bootstrap_context)"))
		}
		if p.Task == "" {
			return nil, apperr.Caller(fmt.Errorf(
				"vp_manage_task: 'task' is required — pass the task slug to create or act on (from vp_list_tasks)"))
		}

		switch p.Action {
		case "create":
			// 🔴 ONE OF THE TWO ACTIONS THAT CAN SCAFFOLD A PROJECT, so one of
			// the two that are gated. The other is `move`, whose DESTINATION
			// side reaches the same directory-creating call for a project that
			// need not exist; it carries the identical RequireKnownProject
			// call on `to_project`. This comment named create as the ONLY such
			// action and justified it by enumeration — true until move existed,
			// and rewritten here in the change that made it false rather than
			// left for a later audit to trust.
			//
			// Verified rather than assumed, and the enumeration is why the list
			// is exactly these two. CreateTask and MoveTaskToProject are the
			// only task writers that reach a directory-creating call (EnsureDir,
			// internal/storage/tasks.go) without first requiring a file to exist
			// at the path they create: create's project may be brand new, and
			// move's DESTINATION project may be. Every other action is closed by
			// construction — moveTask (retire/cancel) stats its source, errors
			// "task not found", and creates only inside the source's own
			// project; move's own SOURCE side stats the active task file for the
			// same reason and so cannot scaffold either; amend / set_meta /
			// update_status / set_relations / overwrite all read the task file
			// first and return on error, so their atomicfile.Write — which DOES
			// os.MkdirAll its parent — is unreachable for a project that does
			// not exist.
			//
			// `project` here is a free-string slug with no accompanying path,
			// so a typo'd or hallucinated one would otherwise materialize a
			// phantom Projects/<slug>/ tree — the iter-245 junk-project class
			// that RequireKnownProject was added to close, on a far more
			// agent-trafficked tool than the commit writers it was wired into.
			//
			// The empty repoRoot is deliberate and load-bearing: there is no
			// repo to authorize against, so only the Projects/<slug>/-exists
			// arm applies. A KNOWN project that is not the caller's cwd still
			// passes — cross-project create is a feature, not the defect.
			if err := project.RequireKnownProject(p.Project, vault.Root, ""); err != nil {
				return nil, apperr.Caller(err)
			}
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
			return taskWriteResult(vault, p.Project, p.Task, "create",
				map[string]any{"status": "created", "task": p.Task}), nil

		case "amend":
			// No length floor here, unlike create. See the note on manageTaskSchema.
			if p.Section == "" {
				return nil, fmt.Errorf("section is required for amend action: it names the H2 heading to replace or append")
			}
			if p.Content == "" {
				return nil, fmt.Errorf("content is required for amend action: it is the body of the section")
			}
			op, err := vault.AmendTask(p.Project, p.Task, p.Section, p.Content)
			if err != nil {
				return nil, fmt.Errorf("amend task: %w", err)
			}
			return taskWriteResult(vault, p.Project, p.Task, "amend",
				map[string]any{"status": "amended", "task": p.Task, "section": p.Section, "op": op}), nil

		case "overwrite":
			// The whole-file writer. It is the ONLY typed path to text amend
			// cannot address — the preamble above the first H2, an H2 heading's
			// own wording, a whole-file migration — and under the ruled
			// server-owns-vault architecture it is the SUCCESSOR to `vp tasks
			// edit`, not a parallel to it: when there is no local vault for an
			// editor to open, an absent overwrite means the preamble has no
			// writer at all.
			if p.Content == "" {
				return nil, apperr.Caller(fmt.Errorf(
					"content is required for overwrite action: it is the ENTIRE task file, " +
						"header block included — not a section body and not a fragment"))
			}

			// Read the task as it stands. This resolves across active/done/
			// cancelled, which is what lets the archived check below see an
			// archived slug at all.
			current, _, err := vault.GetTask(p.Project, p.Task)
			if err != nil {
				return nil, fmt.Errorf("overwrite task: %w", err)
			}

			// 🔴 ACTIVE ONLY. Vault.OverwriteTaskFile deliberately honors
			// whatever resolveTaskFile returns and documents the archived
			// question as the CALLER's — so the refusal lives HERE, matching
			// the CLI's guard in cmd_tasks.go rather than duplicating a rule
			// into the storage writer. A done/cancelled task's body is a record
			// of what happened; editing it in place rewrites history silently.
			if current.Done {
				return nil, apperr.Caller(fmt.Errorf(
					"overwrite task %q: task is archived (done/cancelled) — its body is a record of "+
						"what happened, and rewriting it in place would silently revise history. "+
						"Overwrite works on ACTIVE tasks only, matching `vp tasks edit`",
					p.Task))
			}

			// 🔴 HEADER SMUGGLING is refused by the WRITER, not here.
			// storage.OverwriteTaskFile compares the proposed header against
			// disk and returns a *storage.HeaderChangeError already marked
			// apperr.Caller, so the %w wrap below preserves the classification.
			//
			// This used to be a handler-local pre-check, and that was the
			// defect: `vp tasks edit` reached the same writer with no header
			// diff, so a hand-edited Status line saved cleanly through the CLI
			// and was refused through MCP. One rule on the shared writer covers
			// both surfaces; a second copy beside the CLI would have been a
			// second implementation to drift.
			if err := vault.OverwriteTaskFile(p.Project, p.Task, p.Content); err != nil {
				return nil, fmt.Errorf("overwrite task: %w", err)
			}
			return taskWriteResult(vault, p.Project, p.Task, "overwrite",
				map[string]any{"status": "overwritten", "task": p.Task}), nil

		case "set_meta":
			// Title and Priority are the two header fields that had no writer at all
			// until 205: create stamped them once and nothing could ever change them.
			// Deliberately CANNOT set status or edges — three writers, disjoint fields,
			// so there is never a second way to write one.
			if p.Title == "" && p.Priority == "" {
				return nil, fmt.Errorf("set_meta requires title and/or priority")
			}
			edit := storage.TaskMetaEdit{}
			if p.Title != "" {
				edit.Title = &p.Title
			}
			if p.Priority != "" {
				edit.Priority = &p.Priority
			}
			if err := vault.SetTaskMeta(p.Project, p.Task, edit); err != nil {
				return nil, fmt.Errorf("set task meta: %w", err)
			}
			result := map[string]any{"status": "meta_set", "task": p.Task}
			if p.Title != "" {
				result["title"] = p.Title
			}
			if p.Priority != "" {
				result["priority"] = p.Priority
			}
			return taskWriteResult(vault, p.Project, p.Task, "set_meta", result), nil

		case "update_status":
			if p.Status == "" {
				return nil, fmt.Errorf("status is required for update_status action")
			}
			if err := vault.UpdateTaskStatus(p.Project, p.Task, p.Status); err != nil {
				return nil, fmt.Errorf("update task status: %w", err)
			}
			return taskWriteResult(vault, p.Project, p.Task, "update_status",
				map[string]any{"status": p.Status, "task": p.Task}), nil

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
			return taskWriteResult(vault, p.Project, p.Task, "set_relations", result), nil

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
			return taskWriteResult(vault, p.Project, p.Task, "retire",
				map[string]any{"status": "retired", "task": p.Task}), nil

		case "cancel":
			if err := vault.CancelTask(p.Project, p.Task); err != nil {
				return nil, fmt.Errorf("cancel task: %w", err)
			}
			return taskWriteResult(vault, p.Project, p.Task, "cancel",
				map[string]any{"status": "cancelled", "task": p.Task}), nil

		case "move":
			// Relocate an ACTIVE task from `project` into `to_project` and
			// record where it went, at BOTH ends.
			//
			// 🔴 THE ORDER IS RENAME → DESTINATION PROVENANCE → SOURCE
			// TOMBSTONE, AND IT IS NOT NEGOTIABLE. Each step completes before
			// the next begins, and nothing writes content before the move.
			// Writing first and renaming after is the rename-then-rewrite shape
			// `moveTask`'s own 🔴 comment rejects, whose failure mode is the
			// live open bug `retired-task-files-keep-a-live-status-line`: a
			// surviving file left asserting something untrue. Here it would be
			// worse — a task carrying "Moved from X" while still sitting in X.
			//
			// 🔴 THE RESIDUAL IS KNOWN AND ACCEPTED. DO NOT "FIX" IT. A crash
			// (or a failure) between the rename and either provenance write
			// leaves the task CORRECTLY MOVED and merely MISSING its note: the
			// destination file is the real task in the real place, the source
			// simply carries no tombstone. That is recoverable — re-run the two
			// typed writes by hand, or file the tombstone with `create` +
			// `cancel` — and, decisively, NOTHING IN THE VAULT STATES ANYTHING
			// FALSE at any instant of the sequence. Every reordering that would
			// close this window buys it back as a window in which a file
			// asserts a move that has not happened, which is the strictly worse
			// trade this project has already ruled on twice. A missing record
			// is recoverable; a false record is believed.
			//
			// That is also why the provenance writes below report their outcome
			// as DATA rather than as the handler's error. By the time they run
			// the rename is done and committed; returning an error would tell
			// the caller the MOVE failed, which is false, and both reactions to
			// that lie are harmful — a retry gets "not found in the active
			// tasks of <source>" and the agent then holds two contradictory
			// failures for one successful move, while abandoning leaves the
			// agent believing a task it can no longer find is still where it
			// was. Same contract, and for the same reasons, as commitTaskWrite.
			//
			// 🔴 SEQUENTIAL LOCKS, NEVER NESTED (ADR-003). Each call below —
			// MoveTaskToProject, AmendTask, CreateTask, CancelTask — takes and
			// releases its OWN per-path lock and returns before the next is
			// made. No lock is held while another is acquired. vaultlock.Acquire
			// is a blocking LOCK_EX with no LOCK_NB and no timeout, so an
			// inverted order would be a permanent HANG rather than a detectable
			// error; keeping the calls strictly sequential is what makes an
			// inversion unrepresentable rather than merely avoided.
			if p.ToProject == "" {
				return nil, apperr.Caller(fmt.Errorf(
					"move requires 'to_project': the project slug to move the task INTO " +
						"(from vp_list_projects). 'project' names where the task is now"))
			}

			// 🔴 MANDATORY, NOT OPTIONAL. See the enumeration on the create arm:
			// this is the second of the two actions that can scaffold a project,
			// because MoveTaskToProject's destination side reaches EnsureDir for
			// a project that need not exist. Without this gate a typo'd
			// to_project would materialize a phantom Projects/<typo>/tasks/ tree
			// and move a real task into it, which is the iter-245 junk-project
			// class with a live task inside it.
			//
			// The empty repoRoot is the same deliberate form create uses and for
			// the same reason: there is no repo to authorize against, so only the
			// Projects/<slug>/-exists arm applies. The SOURCE project needs no
			// gate — an unknown one has no active task file, and the move stats
			// for that before it creates anything.
			if err := project.RequireKnownProject(p.ToProject, vault.Root, ""); err != nil {
				return nil, apperr.Caller(err)
			}

			// STEP 1 — the rename. It is the commit point of the whole
			// sequence: atomic, content-free, and the only step whose failure
			// means nothing happened at all. So it is the only one that returns
			// an error.
			if err := vault.MoveTaskToProject(p.Project, p.Task, p.ToProject); err != nil {
				return nil, fmt.Errorf("move task: %w", err)
			}

			result := map[string]any{
				"status":       "moved",
				"task":         p.Task,
				"from_project": p.Project,
				"to_project":   p.ToProject,
			}

			// One provenance record, composed once, rendered at both ends, from
			// ONE clock read — so the destination section and the source
			// tombstone cannot disagree about the day or the commit. The WRITER
			// owns the clock (ADR-006): no caller supplies a date.
			prov := vault.NewMoveProvenance(p.Project, p.ToProject, p.Task, time.Now())

			// STEP 2 — destination provenance. AmendTask resolves the ACTIVE
			// path of the project it is GIVEN, so passing to_project reaches
			// the file the rename just created.
			//
			// The heading is provenance, never a claim: amend is keyed on the
			// heading text and cannot revise it, so a heading is written once
			// and is permanent. See MoveProvenance.DestinationHeading.
			if _, err := vault.AmendTask(p.ToProject, p.Task, prov.DestinationHeading(), prov.DestinationBody()); err != nil {
				result["provenance"] = "failed"
				result["provenance_error"] = fmt.Sprintf(
					"%v — THE TASK IS MOVED AND IS SAFE at Projects/%s/tasks/%s.md; only the provenance "+
						"note failed. Do NOT re-send the move (it would refuse: the task is no longer in %q). "+
						"Record it by hand with vp_manage_task action=amend project=%s task=%s section=%q",
					err, p.ToProject, p.Task, p.Project, p.ToProject, p.Task, prov.DestinationHeading())
			} else {
				result["provenance"] = "written"
				result["provenance_section"] = prov.DestinationHeading()
			}

			// 🔴 THE COMMIT SEAM IS PER-PROJECT AND THIS WRITE DIRTIES TWO.
			// commitTaskWrite probes ONE project's three task paths, so a single
			// call would record half a rename and leave the other half as dirt —
			// and an uncommitted task file makes vp_vault_sync refuse, which
			// wedges sessions, drawers and KG triples too. Both halves are
			// committed, DESTINATION FIRST: that order never produces a commit in
			// which the task exists nowhere, whereas source-first does. The
			// second half routes through taskWriteResult like every other
			// mutating action, so this action does not escape the seam.
			//
			// It runs HERE, after step 2 and before step 3, so the destination
			// commit carries the moved file WITH its provenance section rather
			// than committing the bare rename and leaving the amend as dirt.
			// The source-side commit at the end covers both halves of the
			// source's change — the deletion the rename left and the tombstone
			// step 3 files — because they are two of the same three task paths
			// commitTaskWrite already probes.
			destCommit := commitTaskWrite(vault, p.ToProject, p.Task, "move")
			for k, v := range destCommit {
				result["destination_"+k] = v
			}

			// STEP 3 — the source tombstone, as TWO typed calls in this order:
			// create it in the source project (the slug is free there because
			// the rename removed the file), then cancel it, which is what lands
			// it in tasks/cancelled/ — the directory this project already treats
			// as the home of "why something is not here". Filing it by writing
			// straight into cancelled/ would be a second implementation of what
			// CancelTask owns.
			if err := vault.CreateTask(p.Project, prov.TombstoneSpec()); err != nil {
				result["tombstone"] = "failed"
				result["tombstone_error"] = fmt.Sprintf(
					"%v — THE TASK IS MOVED AND IS SAFE at Projects/%s/tasks/%s.md; only the source-side "+
						"record failed, so %q now has no note of where %q went. Do NOT re-send the move. "+
						"File the record by hand with vp_manage_task action=create then action=cancel, both "+
						"on project=%s task=%s",
					err, p.ToProject, p.Task, p.Project, p.Task, p.Project, p.Task)
			} else if err := vault.CancelTask(p.Project, p.Task); err != nil {
				// The tombstone exists but is still ACTIVE in the source
				// project. Its BODY says it is a tombstone, so nothing here
				// asserts the task is live — but it will show in the source's
				// open backlog until the cancel is completed, and that is what
				// this message tells the caller to do.
				result["tombstone"] = "uncancelled"
				result["tombstone_error"] = fmt.Sprintf(
					"%v — THE TASK IS MOVED AND IS SAFE at Projects/%s/tasks/%s.md, and the source-side "+
						"record was written, but it is still in %s's ACTIVE tasks instead of cancelled/. "+
						"Finish it with vp_manage_task action=cancel project=%s task=%s",
					err, p.ToProject, p.Task, p.Project, p.Project, p.Task)
			} else {
				result["tombstone"] = "written"
				result["tombstone_path"] = path.Join("Projects", p.Project, "tasks", "cancelled", p.Task+".md")
			}

			return taskWriteResult(vault, p.Project, p.Task, "move", result), nil

		default:
			// 🔴 THIS LIST MUST MATCH manageTaskSchema's action enum. The enum
			// is what validateParams rejects against, so this branch is only
			// reachable for a name the enum ACCEPTS but the switch above does
			// not handle — or by a Dispatch-free caller invoking the handler
			// directly. Either way a caller who gets here is reading this
			// sentence to find out what it may say instead, so a list missing
			// an action the tool actually has sends them away from a working
			// capability. `move` was added to the enum and to this list in the
			// same change, for exactly that reason.
			return nil, fmt.Errorf(
				"invalid action %q: must be create, amend, overwrite, set_meta, update_status, set_relations, retire, cancel, or move",
				p.Action)
		}
	}
}
