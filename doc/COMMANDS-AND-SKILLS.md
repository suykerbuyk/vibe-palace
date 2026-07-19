# Commands and Skills

**Last updated:** 2026-05-31

Vibe-palace lets users create custom **commands** and **skills** as plain
markdown files. No recompilation, no config edits, no frontend-specific
plumbing. Drop a `.md` file in the right directory and any MCP-capable AI
frontend can discover and use it.

This document covers the two resource types, where they live, how to create
them, and how they reach the AI.

---

## Concepts

### Commands — Imperative Instructions

A command is a plain markdown file (no frontmatter) containing step-by-step
instructions. When invoked, the AI is told to **execute these instructions
now** — not summarize, not discuss, but perform them.

Use commands for repeatable workflows: session bootstrap, documentation
updates, architecture reviews, dependency audits, release checklists.

**Built-in commands** (embedded in the `vp` binary):

| Name | Purpose |
|------|---------|
| `restart` | Sync vault, restore context, sweep orphaned plans |
| `wrap` | Update resume and dependent docs to reflect current state |
| `stage` | Prepare a commit: light gate, author commit.msg, stage files by path (the commit-prep subset of wrap) |
| `review-plan` | Critical architecture review of a task plan |
| `cancel-plan` | Archive a planned task found not worth implementing |
| `capture` | Record a session summary with decisions and open threads |
| `execute-plan` | Dispatch subagents per phase to execute a task plan |
| `license` | Add or update dual MIT/Apache-2.0 licensing |
| `makefile` | Audit or create a Makefile facade for the build system |
| `vault-audit` | Adversarial whole-vault audit: runs `vp audit vault` and adds the Layer-2 human-judgment pass |

### Skills — Behavioral Guidelines

A skill is a markdown file **with YAML frontmatter** that defines a persona
or analytical framework the AI adopts for the session. Skills have two
required frontmatter fields:

- `name` — identifier used in MCP tool calls
- `description` — trigger conditions; tells the AI when to activate

Skills can include a `references/` subdirectory with supporting documents
(checklists, frameworks, domain knowledge) that the AI can pull in as needed.

---

## 5-Tier Palace-Scoped Precedence

Resource resolution uses five tiers. First match wins — no merging across
tiers.

| Tier | Location | Scope |
|------|----------|-------|
| 1 (highest) | `{vault}/Projects/{project}/commands/{wing}/{room}/{name}.md` | Room-specific |
| 2 | `{vault}/Projects/{project}/commands/{wing}/.wing/{name}.md` | Wing-specific |
| 3 | `{vault}/Projects/{project}/commands/{name}.md` | Project-wide |
| 4 | `{vault}/Templates/commands/{name}.md` | Shared across all projects |
| 5 (lowest) | Compiled into the `vp` binary | Fallback defaults |

Same structure applies with `skills/` in place of `commands/`.

**`.wing/`** is a sentinel directory that distinguishes wing-level resources
from room subdirectories. Without it, a file at `commands/backend/deploy.md`
would be ambiguous — is `backend` a wing or a room?

**`{vault}`** is the `vault_path` from `~/.config/vibe-palace/config.toml`
(e.g., `~/obsidian/VibeVault`).

**`{project}`** is the project slug detected from the working directory or
passed explicitly via the `project` parameter.

When `wing` and `room` are not provided, resolution falls back to the 3-tier
chain (tiers 3–5: project > vault > embedded).

### Override behavior

To customize a built-in command for one project, create a file with the same
name in the project's `commands/` directory. The project version
completely replaces the lower-tier version — there is no inheritance or
merging.

To add a vault-wide command that applies to all projects, place it in
`Templates/commands/`.

To scope a command to a specific wing or room, place it in the appropriate
subdirectory under `commands/{wing}/.wing/` or `commands/{wing}/{room}/`.

---

## Creating a Command

### 1. Choose a location

- **All projects**: `{vault}/Templates/commands/{name}.md`
- **One project**: `{vault}/Projects/{project}/commands/{name}.md`
- **One wing**: `{vault}/Projects/{project}/commands/{wing}/.wing/{name}.md`
- **One room**: `{vault}/Projects/{project}/commands/{wing}/{room}/{name}.md`

### 2. Write the markdown

Commands are plain markdown — no frontmatter, no special syntax. Write
instructions the way you'd brief a capable developer:

```markdown
Audit Go module dependencies for this project.

1. Run `go list -m all` to get the full dependency tree
2. Run `go mod verify` to check integrity
3. Check for known vulnerabilities with `govulncheck ./...`
4. Report: total deps, any verification failures, any CVEs
5. If CVEs found, propose `go get` commands to upgrade affected modules
```

Save this as `audit-deps.md` and it's immediately available.

### 3. Guidelines for effective commands

- **Be imperative**: "Run X", "Check Y", "Report Z" — not "You could try..."
- **Reference MCP tools by name**: The AI knows its tools; tell it which to use
- **Include verification steps**: Don't just say "do it", say "then confirm it worked"
- **Keep it self-contained**: A command should work without external context
- **Use conditionals for branching**: "If X, do A; otherwise do B"

---

## Creating a Skill

### 1. Create the directory structure

```
{vault}/Templates/skills/{name}/
    SKILL.md              # Required: frontmatter + guidelines
    references/           # Optional: supporting documents
        checklist.md
        framework.md
```

Skills are always **directory-form** — a `{name}/` subdirectory
containing `SKILL.md` (and optionally a `references/` tree). Flat-file
skills (`skills/{name}.md`) are not supported; the resolver refuses
them and the embedded seed ships only directory-form skills. The
directory is the unit of override: a project-tier
`Projects/<slug>/skills/{name}/SKILL.md` shadows the vault's persona
entry, while each `references/<section>.md` file falls through
independently to the next tier if the higher tier doesn't supply it.
This lets a project override the persona without re-authoring every
reference file.

### Frontmatter schema

`SKILL.md` begins with a YAML frontmatter fence parsed by
`internal/skills/frontmatter.go`:

| Field | Type | Purpose |
|-------|------|---------|
| `name` | string | Canonical identifier (matches the directory name). |
| `description` | string | Model-facing trigger description. |
| `triggers` | `[]string` | Optional keyword/phrase hints for shim rendering. |
| `paths` | `[]string` | Optional glob hints consumed by Claude Code / Cursor. |
| `lifetime` | string | `"postural"` (default) or `"transactional"`. |

A missing `lifetime` defaults to `"postural"` — the skill stays
active across turns until `vps-clear` or a new session. Malformed or
missing frontmatter produces a parser error; Phase 1 requires every
directory-form skill to carry a frontmatter block.

### Per-file fallthrough

`ResolveSkillDir(name)` locates the first tier supplying a
`SKILL.md` and returns it. `ResolveSkillSection(name, section)`
walks the 5-tier chain independently for each
`references/<section>.md`. Concretely: if a project override ships
only `SKILL.md` and a single custom reference, every other reference
continues to resolve from vault or embedded tiers. The
`ReferenceNames` list returned by `ResolveSkillDir` is the union of
reference basenames discovered at every tier.

### 2. Write the SKILL.md

Skills require YAML frontmatter with `name` and `description`:

```markdown
---
name: code-reviewer
description: >
  Rigorous code reviewer. Triggers when the user asks for a code review,
  shares a diff, or asks "what do you think of this code?"
---

# Code Reviewer

You are a senior staff engineer performing a thorough code review.

Focus on: correctness, edge cases, performance cliffs, API design.
Do NOT comment on style or formatting unless it obscures intent.

## Review Structure

1. **Summary**: What does this change do?
2. **Correctness**: Will it work? What breaks?
3. **Design**: Is the abstraction right?
4. **Verdict**: Ship / Ship with changes / Rethink
```

### 3. Guidelines for effective skills

- **Write the `description` for the AI, not for humans**: It determines when
  the skill activates. Be specific about trigger phrases and document types.
- **Define a persona**: Skills work best when they give the AI a clear role
  with specific priorities and constraints.
- **Include anti-patterns**: "Do NOT X" is as valuable as "Do Y."
- **Use references for bulk material**: Domain knowledge, checklists, and
  frameworks go in `references/` so the main SKILL.md stays focused on
  behavior.

---

## MCP Tools

Any MCP-capable frontend can call these — Claude Code, Cursor, VS Code,
or a custom client.

### Canonical: Execute / Activate (with built-in discovery)

`vp_cmd` and `vp_skill` are the canonical entry points. Both do double
duty: with a `name`, they wrap and deliver the target; with no
arguments, they return a formatted discovery list. The managed block
that `vp init` writes into `CLAUDE.md` / `AGENTS.md` points users here,
and `vp_bootstrap_context` surfaces them via the `vpc-<name>` and
`vps-<name>` aliases.

| Tool | Parameters | Returns |
|------|-----------|---------|
| `vp_cmd` | `name` (optional), `project` (optional), `wing` (optional), `room` (optional) | Execution frame (see below), or discovery list if name omitted |
| `vp_skill` | `name` (optional), `project` (optional), `wing` (optional), `room` (optional) | Activation frame (see below), or discovery list if name omitted |

### Secondary: read-only inspection

These return structured JSON (`name`, `content`, `source` metadata)
rather than execution-framed text — useful for programmatic callers
that want raw content without the "perform these instructions" wrapper.

| Tool | Parameters | Returns |
|------|-----------|---------|
| `vp_list_commands` | `project` (optional), `wing` (optional), `room` (optional) | Names, sources, and brief descriptions |
| `vp_list_skills` | `project` (optional), `wing` (optional), `room` (optional) | Names, sources, and brief descriptions |
| `vp_get_command` | `name` (required), `project` (optional), `wing` (optional), `room` (optional) | Raw markdown content + source tier |
| `vp_get_skill` | `name` (required), `project` (optional), `wing` (optional), `room` (optional) | Raw markdown content + source tier |
| `vp_get_skill_section` | `name` (required), `section` (required), `project` (optional), `wing` (optional), `room` (optional) | `{content, source}` — one reference file from `skills/<name>/references/<section>.md` |

### Execution frames

When `vp_cmd` is called with a name, it wraps the markdown in a directive
frame that tells the AI to act:

```
=== EXECUTE COMMAND: restart ===
Project: vibe-palace | Wing: backend | Room: api | Source: project

The following are instructions for you to execute now. Follow each step.
Do not merely summarize or describe these instructions — perform them.

---

{markdown content}

---

End of command: restart
```

When `vp_skill` is called, the frame says:

```
=== ACTIVATE SKILL: startup-analyst ===
Project: (none) | Source: vault

The following describes a skill you should apply during this session.
Internalize these guidelines and apply them when relevant.

---

{markdown content}

---

End of skill: startup-analyst

References (fetch on demand via vp_get_skill_section):
  - capex-opex
  - competitive-landscape
  - funding-sources
  - reality-validation
  - strategic-partnerships
```

The references list is appended only when the resolved skill directory
actually contains one or more `references/*.md` files. Names are
deduplicated and sorted alphabetically across all five precedence
tiers.

### Fetching references

Once a skill has been activated, the references listed in the frame
can be pulled individually with `vp_get_skill_section`. Sample call:

```json
{
  "jsonrpc": "2.0", "id": 3, "method": "tools/call",
  "params": {
    "name": "vp_get_skill_section",
    "arguments": {"name": "startup-analyst", "section": "capex-opex"}
  }
}
```

The response returns `{"content": "<reference body>", "source":
"<tier>"}`. Reference resolution is **per-file**: a project may
override `SKILL.md` without shipping every reference, and the resolver
transparently falls back to vault or embedded copies for anything the
project did not override.

The same surface is available from the CLI:

```
vp skills list
vp skills show startup-analyst
vp skills show startup-analyst --section capex-opex
```

### Write and wrap tools

The tools above cover command/skill discovery and execution. The wider
write/wrap surface lets an agent complete a session end-to-end without
shelling out to the filesystem. These are catalogued in full (with
parameters) in `doc/PRD-vibe-palace.md` §6.8–6.11; the families are:

- **Vault file CRUD** (§6.8): `vp_vault_read`, `vp_vault_list`,
  `vp_vault_exists`, `vp_vault_sha256`, `vp_vault_write`, `vp_vault_edit`,
  `vp_vault_delete`, `vp_vault_move`. Generic, schema-agnostic access over
  vault-relative paths. `vp_vault_write` performs no schema validation —
  prefer the typed writers below for files that have one.
- **Commit lifecycle** (§6.9): `vp_ingest_commit_msg` reads
  `<project>/commit.msg` off disk and writes a stamped vault copy.
- **Wrap state** (§6.11): `vp_collect_wrap_state`, `vp_stamp_iter`,
  `vp_preflight_wrap` — readiness and bookkeeping driven by `.vibe-palace/`
  anchors (see `doc/adr/002-wrap-state-anchors.md`).

The CLI mirrors the vault-CRUD family: `vp vault read|write|edit|delete|move|exists|sha256`,
plus `vp vault commit --paths <p1,p2,...> --message <msg> [--push]` for the
explicit-path commit-then-push that `vp_vault_sync`'s `paths` argument exposes.

---

## Frontend Integration

### The thin CLAUDE.md / AGENTS.md approach

The frontend config file (`.claude/`, `AGENTS.md`, Cursor rules, etc.)
should be a **tool catalog**, not a behavior specification. Its job:

1. **Declare what MCP tools exist** — list the tools the AI can call
2. **State when to call them** — "call `vp_bootstrap_context` at session
   start", "call `vp_capture_session` at end of work units"
3. **Map user shortcuts to tools** — `/restart` → `vp_cmd("restart")`

All actual behavior, workflow rules, and prompt content lives in the vault's
markdown files, served through MCP. The frontend file is the routing table;
vibe-palace is the engine.

This keeps the system **frontend-agnostic**. The same commands and skills
work identically whether the user runs Claude Code, Cursor, Windsurf, or a
custom MCP client.

### User invocation patterns

How users trigger commands depends on their frontend:

| Frontend | Invocation |
|----------|-----------|
| Claude Code | `/vpc-<name>` slash shims in `.claude/commands/` (e.g. `/vpc-restart`) |
| Grok Build | Native `/vpc-<name>` slash commands via `.grok/plugins/vibe-palace/commands/vpc-<name>.md` (first-class project plugin commands); a `/vpc` skill hub is also emitted under `.grok/skills/vpc/` for the dispatcher + usage instructions |
| Cursor | Rules file maps keywords to `vp_cmd` calls |
| Custom MCP client | Direct `vp_cmd` / `vp_skill` tool calls |
| CLI fallback | `vp inject` prints context; `vp commands restart` outputs the command via CLI |

**Claude Code bootstrap primitive.** `/vpc-restart` is the recommended
first message of every Claude Code session. Claude Code does not load
`CLAUDE.md` until after the human's first turn, so the
`BEFORE … call vp_bootstrap_context` directive in the agent-file
managed block fires on turn 2+ at the earliest. A slash shim is
resolved before turn 1 completes — making `/vpc-restart` the
deterministic turn-1 bootstrap trigger. Cursor / Zed / Copilot load
their rules files early enough that the managed-block directive does
the job there; the two mechanisms are complementary.

---

## Directory Layout Reference

```
{vault}/
├── Templates/
│   ├── commands/               # Vault-wide commands
│   │   ├── cancel-plan.md
│   │   ├── capture.md
│   │   ├── execute-plan.md
│   │   ├── license.md
│   │   ├── makefile.md
│   │   ├── restart.md
│   │   ├── review-plan.md
│   │   ├── stage.md
│   │   ├── vault-audit.md
│   │   └── wrap.md
│   └── skills/                 # Vault-wide skills
│       └── startup-analyst/
│           ├── SKILL.md
│           └── references/
│               ├── capex-opex.md
│               └── competitive-landscape.md
└── Projects/
    └── {project}/
        ├── commands/           # Project overrides + palace-scoped
        │   ├── restart.md      # Project scope (overrides Templates)
        │   └── backend/        # Wing directory
        │       ├── .wing/
        │       │   └── deploy.md   # Wing scope (backend wing)
        │       └── api/
        │           └── validate.md # Room scope (backend/api room)
        └── skills/             # Same layout as commands/
```

Embedded defaults (compiled into `vp`):

```
internal/templates/templates/
├── commands/
│   ├── cancel-plan.md
│   ├── capture.md
│   ├── execute-plan.md
│   ├── license.md
│   ├── makefile.md
│   ├── restart.md
│   ├── review-plan.md
│   └── wrap.md
├── resume.md
└── workflow.md
```

---

## Implementation Details

For contributors working on the command/skill system itself:

- **`internal/context/precedence.go`** — `Resolver` struct implements 5-tier
  palace-scoped lookup. `ResolveScoped(resource, project, wing, room)` returns
  `(content, source, error)`. `ListResourcesScoped(resourceType, project,
  wing, room)` deduplicates across tiers (higher wins). The legacy
  `Resolve(resource, project)` and `ListResources(resourceType, project)`
  delegate with empty wing/room. Resource names are validated against path
  traversal.

### Resource-identifier grammar

Resolver methods accept the following identifier forms. Phase 5 of
`vps-skill-artifacts-cross-ide` extended the grammar so the upgrade
machinery can address per-file skill resources; the original directory
form still works for top-level SKILL.md access.

| Identifier                            | Resolves to                                       |
|---------------------------------------|---------------------------------------------------|
| `workflow`                            | `workflow.md` (tier-walked)                       |
| `resume`                              | `resume.md` (tier-walked)                         |
| `command:<name>`                      | `commands/<name>.md`                              |
| `skill:<name>`                        | `skills/<name>/SKILL.md` (directory form)         |
| `skill:<name>/<relpath>`              | `skills/<name>/<relpath>` (per-file nested form)  |

The nested form `skill:<name>/<relpath>` is what `commands.Plan`
consumes for per-file skill diffs — e.g.
`skill:startup-analyst/references/capex-opex.md` addresses that single
reference. `ListEmbedded("skill")` returns every nested identifier
(SKILL.md + references) so the two-SHA upgrade path can emit one
`Change` per file; `ListResourcesScoped("skill", …)` continues to
return skill *names* for the directory-oriented surfaces
(`vp skills list`, `vp_list_skills` MCP tool). The `<relpath>` is
checked for traversal (no leading `/`, no `..`, no empty segments, no
backslashes).

- **`internal/tools/cmd_tools.go`** — `CmdTool()` and `SkillCmdTool()` return
  the `vp_cmd` and `vp_skill` MCP tools. `buildExecutionFrame()` wraps content
  in the directive frame. `buildDiscoveryList()` generates the listing when
  called with no name. `extractBrief()` pulls the first non-heading line for
  discovery output.

- **`internal/tools/command_tools.go`** — `GetCommandTool()`, `GetSkillTool()`,
  `ListCommandsTool()`, `ListSkillsTool()` return the four read-only MCP tools.

- **`internal/tools/register.go`** — `RegisterAll()` wires all tools into the
  MCP registry.

- **`internal/mcp/tools.go`** — `Registry` validates JSON schemas at
  registration time and dispatches handler calls at runtime.
