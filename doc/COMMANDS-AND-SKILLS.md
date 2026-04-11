# Commands and Skills

**Last updated:** 2026-04-11

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
| `review-plan` | Critical architecture review of a task plan |
| `cancel-plan` | Archive a planned task found not worth implementing |
| `capture` | Record a session summary with decisions and open threads |

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

For simple skills without references, a flat file also works:
`{vault}/Templates/skills/{name}.md`

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

Six MCP tools provide the runtime interface. Any MCP-capable frontend can
call them — Claude Code, Cursor, VS Code, or a custom client.

### Discovery

| Tool | Parameters | Returns |
|------|-----------|---------|
| `vp_list_commands` | `project` (optional), `wing` (optional), `room` (optional) | Names, sources, and brief descriptions |
| `vp_list_skills` | `project` (optional), `wing` (optional), `room` (optional) | Names, sources, and brief descriptions |

### Read (raw content)

| Tool | Parameters | Returns |
|------|-----------|---------|
| `vp_get_command` | `name` (required), `project` (optional), `wing` (optional), `room` (optional) | Raw markdown content + source tier |
| `vp_get_skill` | `name` (required), `project` (optional), `wing` (optional), `room` (optional) | Raw markdown content + source tier |

### Execute / Activate

| Tool | Parameters | Returns |
|------|-----------|---------|
| `vp_cmd` | `name` (optional), `project` (optional), `wing` (optional), `room` (optional) | Execution frame (see below), or discovery list if name omitted |
| `vp_skill` | `name` (optional), `project` (optional), `wing` (optional), `room` (optional) | Activation frame (see below), or discovery list if name omitted |

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
```

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
| Claude Code | `/restart` (mapped via skills config in `.claude/settings.json`) |
| Cursor | Rules file maps keywords to `vp_cmd` calls |
| Custom MCP client | Direct `vp_cmd` / `vp_skill` tool calls |
| CLI fallback | `vv inject` prints context; `vp cmd restart` runs via CLI |

---

## Directory Layout Reference

```
{vault}/
├── Templates/
│   ├── commands/               # Vault-wide commands
│   │   ├── restart.md
│   │   ├── wrap.md
│   │   ├── review-plan.md
│   │   └── cancel-plan.md
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
internal/context/templates/
├── commands/
│   ├── restart.md
│   ├── wrap.md
│   ├── review-plan.md
│   ├── cancel-plan.md
│   └── capture.md
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
