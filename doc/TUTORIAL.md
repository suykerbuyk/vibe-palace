# Vibe-Palace Tutorial

This guide walks you through installing vibe-palace, connecting it to your
editor, and using it in daily development.

---

## Part 1: Installation

### Prerequisites

- **Go 1.25+** — check with `go version`
- **`~/.local/bin` in PATH** — or set a custom `PREFIX` when installing
- **git** (recommended) — for vault version tracking and multi-machine sync

### Build and Install

```bash
git clone https://github.com/suykerbuyk/vibe-palace.git
cd vibe-palace
make build
make test
make install        # installs to ~/.local/bin/vp
```

Verify:

```bash
which vp
# → /home/you/.local/bin/vp
```

If `which vp` returns nothing, add `~/.local/bin` to your PATH:

```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

### Set Up Config and Vault

Run `vp init` to create the global configuration and vault directory:

```bash
vp init
```

This creates:
- `~/.config/vibe-palace/config.toml` — global configuration with commented
  documentation for every setting
- `~/vibe-palace-vault/` — vault directory for palace data
- A git repository in the vault (if git is installed)

**Custom vault location:**

```bash
vp init --vault-path ~/my-vault
```

**Disable git tracking:**

```bash
vp init --no-git
```

If you already have a config file, `vp init` skips global setup and proceeds
to project initialization (if you're in a project directory).

### Init Status Output

Each `vp init` run ends with a status table that tells you exactly what
was done, skipped, or still needs attention. It uses the same vocabulary
as `vp check` — `[pass]`, `[info]`, `[skip]`, `[FAIL]`.

Fresh install inside a Go project:

```
vp init — vibe-palace 0.1.0-dev

[pass] Global config: /home/you/.config/vibe-palace/config.toml
[pass] Vault:     /home/you/vibe-palace-vault
                  git repository initialized
[pass] Project config: /home/you/code/myapp/.vibe-palace.toml (myapp, go.mod detected)
[skip] Agent wiring: Phase 2 — CLAUDE.md / AGENTS.md bootstrap snippet not yet written

Summary: 3 ok, 1 skip. Re-run `vp init` anytime — it is idempotent.
```

The `Project config` row reports which signal marked the directory as a
project: `.git`, `.vibe-palace.toml`, or one of the supported ecosystem
manifests (`go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`,
`pom.xml`). Directories without any of these — including your `$HOME`
and the filesystem root — are force-skipped with a clear reason.

Re-running `vp init` in the same directory is safe: already-present
files report `[info]` and nothing is overwritten.

### Verify Installation

Run the built-in diagnostic to verify everything works:

```bash
vp check
```

Expected output:

```
vp check — vibe-palace installation diagnostic (0.1.0-dev)

[pass] Config:           /home/you/.config/vibe-palace/config.toml
                         vault_path = /home/you/vibe-palace-vault
[pass] Vault:            /home/you/vibe-palace-vault (exists)
[pass] Settings:         model=sentence-transformers/all-MiniLM-L6-v2  search_limit=10
[pass] Embedder:         ONNX loaded, 384 dimensions
[pass] Git:              remotes: github, vault
[pass] Config Staleness: config is up to date
[info] Project:          my-project (from .vibe-palace.toml)

All checks passed.
```

The first run downloads `all-MiniLM-L6-v2` (~90MB) from HuggingFace for
semantic search. The model is cached at `{vault}/palace/.local/models/` —
subsequent runs use the cache.

If the embedder step fails, see [Troubleshooting](#model-download-fails).

Other useful commands:

```bash
vp version   # print version
vp help      # show all commands
```

---

## Part 2: Project Setup

### Initialize a Project

From your project directory, run `vp init` to create `.vibe-palace.toml`:

```bash
cd ~/code/myapp
vp init --name myapp --domain work
```

Or let vibe-palace auto-detect the project name from the directory or git remote:

```bash
vp init
```

**Rules:**
- `name` must be a slug: lowercase letters, numbers, hyphens only (max 64 chars)
- This is the **only file** vibe-palace adds to your project directory
- The name is used as a key throughout the vault directory structure

### Understanding the Vault

After the first session capture, your vault will contain:

```
{vault}/
├── palace/
│   └── my-project/
│       ├── drawers/my-project/general/drawers.jsonl   # indexed content
│       └── kg/entities.jsonl                           # extracted entities
└── Projects/
    └── my-project/
        ├── sessions/2026-04-09-01.md                   # session record
        └── tasks/                                       # task plans
```

**palace/** holds knowledge (content chunks, vectors, KG). This is the
searchable memory.

**Projects/** holds workflow (sessions, tasks, config). This is the
collaboration state between you and the AI.

---

## Part 3: Editor Configuration

### How MCP Works

Your editor spawns `vp` as a subprocess and communicates via JSON-RPC over
stdin/stdout. The AI assistant calls tools (like `vp_bootstrap_context` or
`vp_search`) through this channel. Vibe-palace reads and writes the vault;
the AI never touches vault files directly.

```
Editor ←→ AI Model ←→ MCP Client ←→ vp (stdio) ←→ Vault
```

### Claude Code

Add to `.mcp.json` in your project root (project-scope) or `~/.claude.json`
(global):

```json
{
  "mcpServers": {
    "vibe-palace": {
      "type": "stdio",
      "command": "vp",
      "args": ["mcp"]
    }
  }
}
```

Verify: start Claude Code and check the MCP server list shows `vibe-palace`
with 38 tools available.

**Note:** Vibe-palace replaces CLAUDE.md-based context injection —
`vp_bootstrap_context` delivers workflow, resume, tasks, and sessions via MCP.

### Agent-file wiring (CLAUDE.md, AGENTS.md, .cursorrules, .rules, copilot)

When an AI starts a session in a fresh project, it needs one concrete pointer
to call `vp_bootstrap_context` and to interpret `vpc-<name>` command
triggers. `vp init` handles this by appending a delimited managed block to
any agent instruction file you already have. The detected files are:

| File | Where |
| --- | --- |
| `CLAUDE.md` | project root |
| `AGENTS.md` | project root |
| `.cursorrules` | project root |
| `.rules` | project root (Zed convention) |
| `.github/copilot-instructions.md` | `.github/` (only if the dir exists) |

`vp init` never creates these files and never creates `.github/`. If none of
them exist, init reports `[skip] Agent wiring — no agent file found` and
tells you to create one (even an empty `CLAUDE.md` works) and re-run.

The block looks like this:

```markdown
<!-- vibe-palace:begin v=1 sha=abc1234 -->
## Vibe-Palace Integration

BEFORE responding to the user's first message in a new session, call
`vp_bootstrap_context` to load project context, resume, active tasks,
recent sessions, and the command and skill manifests. Do this even if
the first message seems trivial — the returned payload shapes every
subsequent response.

When the user types `vpc-<name>` (for example `vpc-wrap`, `vpc-restart`),
call `vp_cmd` with `name=<name>` and follow the returned
instructions. `vps-<name>` works the same way via `vp_skill`.
<!-- vibe-palace:end -->
```

The wording is a binding imperative on purpose. An earlier bulleted form
was treated as passive reference material and the bootstrap call was
deferred until the user explicitly asked for it.

**Idempotency.** Re-running `vp init` is safe — if the block is already
present and the content hash (`sha=...`) matches the current template, it
reports `[info] block unchanged` and doesn't touch the file. If the block is
missing it's appended; if it's stale (hash differs, or the body was
hand-edited), it's replaced in place without touching anything outside the
delimiters.

**Symlinks.** If `CLAUDE.md` and `AGENTS.md` are symlinked to the same
underlying file, vibe-palace canonicalizes via realpath and writes once.
The status row shows both names: `CLAUDE.md (→ AGENTS.md)`.

**Removing the block.** Delete the lines between (and including) the
`<!-- vibe-palace:begin ... -->` and `<!-- vibe-palace:end -->` markers.
`vp init` will re-add the block on next run; to prevent that, delete the
agent file entirely (or add a `.vibe-palace.toml` key in a future release
once opt-out is wired up).

### Zed

Add to `~/.config/zed/settings.json` or `.zed/settings.json` in your project:

```json
{
  "context_servers": {
    "vibe-palace": {
      "command": { "path": "vp", "args": ["mcp"] }
    }
  }
}
```

Works with Claude, Gemini, and Grok via Zed's provider settings. Provider
configuration is Zed's concern, not vibe-palace's.

### Vim / Neovim

Requires an MCP client plugin (e.g., `mcphub.nvim`). Add to your
MCP server config (typically `~/.config/mcphub/servers.json`):

```json
{
  "mcpServers": {
    "vibe-palace": {
      "command": "vp",
      "args": ["mcp"]
    }
  }
}
```

The Neovim MCP ecosystem is newer — check your plugin's documentation for
the exact config format.

### Open Code

Add to `opencode.json` in your project root or `~/.config/opencode/`:

```json
{
  "mcp": {
    "vibe-palace": {
      "type": "local",
      "command": ["vp", "mcp"],
      "enabled": true
    }
  }
}
```

**Note:** Open Code uses an array for `command` (unlike other editors).

---

## Part 4: CLI Commands

The `vp` binary includes commands for human use alongside the MCP server.
Run `vp help` for the full list, or `vp <command> --help` for details.

### Project Status

```bash
vp status                   # palace overview: sessions, tasks, recent activity
vp sessions                 # list recent sessions with dates and tags
vp tasks                    # list active tasks with priority and status
vp tasks --done             # include completed and cancelled tasks
```

### Search

```bash
vp search "authentication"  # semantic search across palace content
```

### Room Classification Management

```bash
vp audit rooms              # report on classification quality
vp audit rooms --apply      # reclassify mismatched drawers
vp tune rooms --estimate    # estimate LLM cost for weight tuning
vp tune rooms               # propose keyword weight adjustments via LLM
vp discover rooms --estimate  # estimate LLM cost for keyword discovery
vp discover rooms           # discover new keywords from unclassified content
```

### Project Initialization

```bash
vp init                     # create global config (if needed) + project config
vp init --name my-project --domain work
vp init --vault-path ~/my-vault --no-git
```

### Git Version Tracking

When `git_enabled = true` (the default), `vp init` initializes a git
repository in the vault directory. This enables multi-machine sync:

```bash
vp vault pull               # pull vault state from all remotes
vp vault push               # push vault state to all remotes
vp vault sync               # bidirectional sync (pull then push)
```

To disable git tracking, pass `--no-git` during init or set
`git_enabled = false` in your config file.

### Keeping Config Current

As vibe-palace evolves, new settings are added. Check if your config has
missing settings:

```bash
vp check                    # shows "N new setting(s) available" if outdated
```

Reconcile every managed config tier (global, vault, project) in one pass:

```bash
vp config sync --dry-run    # preview drift across all tiers
vp config sync --yes        # apply every action non-interactively
vp config sync --tier global # reconcile a single tier
```

`vp config sync` is the canonical reconcile entry point. It:
- Never changes your existing values (drift fills come in as commented defaults)
- Walks all five reconcilers (global config, vault dir, vault settings, cwd
  project config, vault-project config) by default
- Is idempotent — re-running on a synced tree is a no-op
- Does **not** touch agent files (`CLAUDE.md` etc.) or slash-command shims —
  those are owned by `vp commands upgrade`

> `vp config upgrade` is **deprecated** and now delegates to
> `vp config sync` with the equivalent `--tier` flag. It will be removed
> in the next release. Pass `--legacy` to use the pre-reconciler path
> for one-off cases until you've migrated.

### Context Injection

```bash
vp inject                   # output bootstrap context as JSON (for scripting)
vp check                    # verify installation, config, vault, embedder
```

### Managing Commands

Vibe-palace ships a small catalog of built-in commands (`restart`, `wrap`,
`review-plan`, `cancel-plan`, `capture`) embedded in the `vp` binary. Users
and projects can override or extend the catalog via the 5-tier resolver
(see `doc/COMMANDS-AND-SKILLS.md`).

List every command visible from your current project, with the tier that
provides it:

```bash
vp commands list                      # human-readable table
vp commands list --json               # machine-readable (for scripting)
vp commands list --project myapp      # include project-tier overrides
```

When vibe-palace releases update the embedded templates, `vp commands
upgrade` reconciles them against your vault-level copies (tier 4 —
`{vault}/Templates/commands/`). Project/wing/room overrides are never
touched.

```bash
vp commands upgrade --dry-run         # preview the plan; non-zero exit if work is pending
vp commands upgrade                   # interactive: unified diff + accept/skip per file
vp commands upgrade --overwrite       # accept every change (required in non-TTY)
vp commands upgrade --only restart    # target a single template
```

In interactive mode you'll be prompted per template:

- `a` — accept this change
- `s` — skip it
- `A` — accept this and every remaining change
- `q` — abort without applying further changes

The vault is git-managed. After an upgrade, review with `git -C <vault>
diff` and commit as you prefer — the upgrader never commits on your
behalf. If the vault has uncommitted changes under `Templates/commands/`
or `Projects/*/commands/` when you run upgrade, a warning lists them so
you can stash or commit first.

`vp commands upgrade` also surfaces stale vibe-palace managed blocks in
agent files (CLAUDE.md, AGENTS.md, …). When the block template's sha
changes, the upgrader offers to re-wire each file using the same atomic
path as `vp init`.

Man pages are available for all commands: `man vp`, `man vp-search`,
`man vp-commands`, `man vp-commands-upgrade`, etc. Install with
`make man`.

### Customizing a command template

The command templates shipped inside `vp` are the *floor* — a default.
Your vault is the primary editable surface. This walkthrough shows the
full materialize-edit-reconcile loop.

**1. Materialize on init.** A fresh `vp init` writes every embedded
command into your vault:

```bash
vp init
ls ~/vibe-palace-vault/Templates/commands/
# → capture.md  cancel-plan.md  restart.md  review-plan.md  wrap.md
cat ~/vibe-palace-vault/.vibe-palace/templates.lock
# → one entry per materialized file, keyed by vault-relative path
grep -E '\*\.(bak|new)' ~/vibe-palace-vault/.gitignore
# → *.bak and *.new present
```

`vp init` also scaffolds `<vault>/Projects/<your-slug>/commands/` and
`<vault>/Projects/<your-slug>/skills/` with README stubs explaining
the 5-tier precedence.

**2. Edit freely.** Open a template and change its phrasing:

```bash
$EDITOR ~/vibe-palace-vault/Templates/commands/wrap.md
```

**3. Your edit survives `vp config sync`.** As long as the embedded
default is unchanged, the reconciler sees `vault ≠ lock` and
`embedded == lock` — the "user-edited, binary-stable" row of the
decision table — and reports the file as `Unchanged`:

```bash
vp config sync --dry-run
# → Templates/commands/wrap.md ... Unchanged
```

**4. Simulating a binary bump.** Suppose a future `vp` release ships a
rewritten `wrap.md`. On the next `vp config sync`, both your vault copy
*and* the new embedded default have diverged from the lock SHA — the
"both diverged" row. The reconciler emits a Prompt action:

```
=== TemplateTree:Templates Prompt ===
Templates/commands/wrap.md diverged (user-edited AND embedded bumped)
  embedded_sha=c7f0...
  vault_sha=ae12...
  lock_sha=3b88...
  embedded_relpath=commands/wrap.md
[s]kip / [o]verwrite (writes .bak) / [n]ew-sidecar — uppercase for all remaining items, [q]uit:
```

**5. The three options.** Each answer produces a different vault
state. Uppercase `S`/`O`/`N` applies the choice to every remaining
Prompt row in the same run.

- `s` — vault file unchanged. No `.bak`, no `.new`. Lock entry is
  left as-is, so the same Prompt will fire again next sync.
- `o` — vault file replaced by the new embedded bytes. The previous
  vault content is moved to `wrap.md.bak` (overwriting any prior
  `.bak`). Lock entry updated to the new embedded SHA. After this
  run, the file is back on the "user never edited" track.
- `n` — vault file unchanged. The new embedded bytes are written
  side-by-side to `wrap.md.new` for manual review. Lock entry is left
  as-is. You can diff `wrap.md` against `wrap.md.new` at your leisure,
  pick the parts you want, and delete the `.new` when done.

**6. Per-project override.** To diverge permanently for one project
without touching the vault-level template, create a file at the
project tier:

```bash
cp ~/vibe-palace-vault/Templates/commands/wrap.md \
   ~/vibe-palace-vault/Projects/myapp/commands/wrap.md
$EDITOR ~/vibe-palace-vault/Projects/myapp/commands/wrap.md
```

The project-level file shadows the vault-level `Templates/` copy for
`myapp` only. Other projects continue to resolve `wrap.md` from
`<vault>/Templates/commands/`. `vp config sync` in scaffold mode never
overwrites project-tier overrides — it only ensures the directory and
README stub exist.

**Promoting back to the `vp` source tree.** Vibe-palace cannot automate
promotion because at runtime it does not know where your vibe-palace
source checkout lives. To land a vault-side edit as the new embedded
floor for the next release, manually copy the edited file from
`<vault>/Templates/commands/<name>.md` to
`<vp-repo>/internal/templates/templates/commands/<name>.md` and commit
it like any other source change.

---

## Part 5: Migration

If you are migrating from VibeVault or MemPalace, vibe-palace can import
your existing data. See [doc/MIGRATION.md](MIGRATION.md) for the full
migration guide, architecture details, and risk assessment.

### Quick Start

```bash
# Preview what would be imported (no changes written)
vp migrate vibevault --dry-run
vp migrate mempalace --export-path ~/mempalace-export.json --dry-run

# Run the actual import
vp migrate vibevault
vp migrate mempalace --export-path ~/mempalace-export.json
```

After import, restart the MCP server to rebuild search indexes.

---

## Part 6: Daily Workflow

### Starting a Session

1. Open your editor in a project that has `.vibe-palace.toml`
2. The AI calls `vp_bootstrap_context` (or you prompt it to)
3. It returns: workflow rules, project resume, active tasks, recent sessions,
   KG snapshot, and available commands
4. The AI has full project context — start working

### During Work

The AI transparently calls tools as needed:

- `vp_search` — find relevant past knowledge
- `vp_cmd` / `vp_skill` — execute commands, activate skills
- `vp_palace_status` — browse knowledge structure
- `vp_traverse` — walk the knowledge graph

You don't need to invoke these manually.

### Invoking Commands: the `vpc-` Alias Convention

Every command returned by `vp_bootstrap_context` carries an `alias`
field of the form `vpc-<name>` — mnemonic: *vp command*. Skills use the
sibling `vps-<name>` form. Type either in chat as an unambiguous trigger
and the AI will call `vp_cmd`/`vp_skill` with `name=<name>` and follow
the returned instructions.

Example:

```
you: vpc-restart
AI: (calls vp_cmd name=restart, follows the restart workflow)
```

The canonical lookup key is still the bare command name — `vpc-`/`vps-`
are purely *display and recognition* conveniences so humans and AIs
have a single unambiguous trigger. The bootstrap response includes
`command_invocation` and `post_bootstrap_instructions` strings that
restate this rule and tell the model to announce available commands
and skills to the user; new models see both on every session start.

To discover what is available in the current project, call `vp_cmd`
(or `vp_skill`) with no arguments, or look at the `available_commands`
and `available_skills` arrays in the bootstrap response.

### Browsing Commands in Claude Code's `/` Menu

In addition to the free-form `vpc-<name>` trigger, `vp init` writes one
tiny shim per command into `.claude/commands/vpc-<name>.md`. Claude Code
surfaces these in its slash menu, so typing `/vpc-` in the REPL fuzzy-
filters the full vibe-palace command set without needing to remember
names. Each shim is a three-line delegation to `vp_cmd` — the command
body itself still lives in the vault, so precedence
(embedded → vault → project/wing/room) stays authoritative.

**Recommended: start every Claude Code session with `/vpc-restart`.**
Claude Code does not load `CLAUDE.md` until *after* the human's first
turn, so the `BEFORE … call vp_bootstrap_context` directive in the
vibe-palace managed block fires on turn 2+ at the earliest. A slash
shim, by contrast, is resolved before turn 1 completes — so typing
`/vpc-restart` as the first message is the deterministic way to pull
bootstrap context on session start. In Cursor, Zed, and Copilot the
rules file *is* loaded early, so the managed-block directive does the
job there; `/vpc-restart` is specifically the Claude Code primitive.
See `knowledge.md` for full rationale.

The shim set is regenerated idempotently on each `vp init` and can be
re-synced on demand with:

```bash
vp commands upgrade           # interactive: per-shim accept/skip/accept-all
vp commands upgrade --dry-run # preview drift without writing
vp commands upgrade --only restart  # narrow to a single shim
```

Stale shims (for commands that were renamed or removed from the vault)
are detected automatically but never deleted without explicit consent.
Files under `.claude/commands/` that do not carry the vibe-palace shim
marker are reported as `custom` and left strictly alone.

If you prefer not to commit the shims — they are regeneratable on any
fresh clone — add this line to `.gitignore`:

```gitignore
.claude/commands/vpc-*.md
```

Teams that want everyone to see the same `/vpc-` menu can simply omit
that ignore rule; the shim bodies are deterministic, so checking them in
produces stable diffs.

### Ending a Session

Say "capture session" or "wrap up". The AI calls `vp_capture_session` with:

- **summary** — what was accomplished
- **tag** — implementation, debugging, refactor, exploration, etc.
- **decisions** — key technical decisions made
- **files_changed** — files created or modified
- **open_threads** — unresolved items for next session
- **transcript** (optional) — full session text, chunked and indexed

If a transcript is provided, the capture pipeline:
1. Detects format (plain text, markdown, JSON-RPC chat)
2. Chunks the text (sliding window, 800 chars, 100 overlap)
3. Classifies chunks by room (testing, api, devops, etc.) and hall (facts,
   decisions, discoveries, etc.)
4. Embeds and indexes all chunks for semantic search
5. Extracts entities (file paths, URLs) into the knowledge graph
6. Computes a friction score (0–100)

The next session sees all of this via `vp_bootstrap_context`.

### Reviewing Past Work

- `vp_search_sessions` — find sessions by keyword, date, friction score
- `vp_get_session_detail` — full session content
- `vp_get_friction_trends` — weekly efficiency trends
- `vp_get_effectiveness` — context availability vs session outcomes

---

## Part 7: LLM Provider Notes

### Model-Agnostic Design

Vibe-palace does **not** communicate with any LLM. The MCP server talks to
your editor; the editor talks to the LLM. The same `vp` binary works
regardless of which model you use.

### Anthropic (Claude)

Best experience via Claude Code (native MCP support, session capture built-in).
Also works with Zed or Open Code using a Claude API key.

### Google (Gemini)

Configure a Gemini API key in your editor's provider settings. All `vp_`
tools work identically.

### xAI (Grok)

Configure an xAI API key in your editor's provider settings. All `vp_`
tools work identically.

---

## Part 8: Advanced Configuration

All configuration is optional — defaults work out of the box. Override in
`~/.config/vibe-palace/config.toml` (vault-level) or
`{vault}/Projects/{project}/config.toml` (project-level).

### Embedder Settings

```toml
[embedder]
model = "sentence-transformers/all-MiniLM-L6-v2"  # HuggingFace model name
max_sequence_length = 256                           # max tokens per input
batch_size = 32                                     # texts per batch
```

### Search Settings

```toml
[search]
default_limit = 10              # results per query
structural_boost_wing = 0.12    # score boost for wing match
structural_boost_hall = 0.24    # score boost for hall match
structural_boost_room = 0.34    # score boost for room match
```

### Chunker Settings

```toml
[chunker]
max_chars = 800    # target chunk size in characters
overlap = 100      # overlap between consecutive chunks
```

### Custom Room Keywords

Define project-specific rooms with custom keywords. Project-level rooms
fully replace vault-level rooms:

```toml
[palace.rooms.audio]
keywords = ["wav", "mp3", "codec", "ffmpeg"]

[palace.rooms.networking]
keywords = ["tcp", "udp", "socket", "http"]
```

### Weighted Scoring Overrides

Fine-tune room classification with weighted keyword tiers (high=1.0,
medium=0.6, low=0.3). These merge with the built-in defaults:

```toml
[palace.scoring]
min_score = 0.5              # lower threshold → fewer "general" fallbacks

[palace.scoring.rooms.testing]
high = ["integration test", "e2e test"]
medium = ["spec"]
low = ["check"]
```

### LLM Configuration (for `vp tune` and `vp discover`)

Configure an OpenAI-compatible endpoint for offline classification analysis.
The LLM is never used at runtime — only for `vp tune rooms` and
`vp discover rooms` commands:

```toml
[palace.llm]
endpoint = "https://api.x.ai/v1"
model = "grok-3-mini"
api_key_env = "XAI_API_KEY"
max_tokens = 4096
```

---

## Part 9: Troubleshooting

### "config not found" or "no such file or directory"

Run `vp init` to create the global config and vault:

```bash
vp init
```

On Linux, the config file is created at `~/.config/vibe-palace/config.toml`.
The resolved path respects `XDG_CONFIG_HOME` if set.

### Model download fails

Editors don't show stderr, so a download failure looks like "no tools
available" with no explanation. Run `vp check` from a terminal to see the
actual error:

```bash
vp check
```

The embedder step will show the failure (e.g., `[FAIL] Embedder: load model: download ... bad status code 401`).

Common fixes:
- Check network connectivity to huggingface.co
- If behind auth, set `HF_TOKEN` environment variable
- If behind a proxy, set `HTTPS_PROXY`
- Model is cached at `{vault}/palace/.local/models/` — delete the directory
  to force re-download
- If offline: vp will not start (embedder initialization is required)

### "no tools available" in editor

Run `vp check` from a terminal — it verifies all prerequisites in order:

1. Config file found and parsed
2. Vault directory exists
3. Settings load successfully
4. Embedding model loads

If all checks pass, verify your editor's MCP config points to `"vp"` (not a
full path) and that `which vp` shows the binary in PATH.

### "project not detected"

Create `.vibe-palace.toml` in your project root with `vp init`:

```bash
cd ~/code/myapp
vp init --name myapp
```

The `name` field must be a slug (lowercase, hyphens, max 64 chars).

### vp hangs on startup

First run downloads the ONNX model (~90MB). Wait up to 2 minutes. If it
persists, check stderr for download errors:

```bash
echo '{}' | vp 2>/tmp/vp-errors.log; cat /tmp/vp-errors.log
```

### Search returns no results

Content must be captured first. Run a session capture with a transcript, then
search should find the indexed content. Check that the project slug in your
query matches the one in `.vibe-palace.toml`.
