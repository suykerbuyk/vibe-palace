# Vibe-Palace Tutorial

This guide walks you through installing vibe-palace, connecting it to your
editor, and using it in daily development.

---

## Part 1: Installation

### Prerequisites

- **Go 1.25+** — check with `go version`
- **`~/.local/bin` in PATH** — or set a custom `PREFIX` when installing

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

### Create the Vault Config

Vibe-palace requires a configuration file with the vault path. Without it,
`vp` will exit immediately with: `vp: read config ... no such file or directory`

Create `~/.config/vibe-palace/config.toml`:

```toml
vault_path = "/home/you/your-vault"
```

**Important:**
- `vault_path` can be absolute or use `~/` (tilde is expanded to your home directory)
- The directory is created automatically on first use
- This is the only required configuration — all other settings have defaults

### Verify Installation

Run the built-in diagnostic to verify everything works:

```bash
vp check
```

Expected output:

```
vp check — vibe-palace installation diagnostic (0.1.0-dev)

[pass] Config:    /home/you/.config/vibe-palace/config.toml
                  vault_path = /home/you/your-vault
[pass] Vault:     /home/you/your-vault (exists)
[pass] Settings:  model=sentence-transformers/all-MiniLM-L6-v2  search_limit=10
[pass] Embedder:  ONNX loaded, 384 dimensions
[info] Project:   my-project (from .vibe-palace.toml)

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

Create `.vibe-palace.toml` in your project root:

```toml
[project]
name = "my-project"
domain = "personal"
tags = ["go", "api"]
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
        └── agentctx/
            └── tasks/                                   # task plans
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
      "command": "vp"
    }
  }
}
```

Verify: start Claude Code and check the MCP server list shows `vibe-palace`
with 38 tools available.

**Note:** Vibe-palace replaces CLAUDE.md-based context injection —
`vp_bootstrap_context` delivers workflow, resume, tasks, and sessions via MCP.

### Zed

Add to `~/.config/zed/settings.json` or `.zed/settings.json` in your project:

```json
{
  "context_servers": {
    "vibe-palace": {
      "command": { "path": "vp", "args": [] }
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
      "args": []
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
      "command": ["vp"],
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
vp init                     # create .vibe-palace.toml in the current directory
vp init --name my-project --domain work
```

### Vault Sync

For multi-machine setups where the vault is a git repository:

```bash
vp vault pull               # pull vault state from remote
vp vault push               # push vault state to remote
vp vault sync               # bidirectional sync
```

### Context Injection

```bash
vp inject                   # output bootstrap context as JSON (for scripting)
vp check                    # verify installation, config, vault, embedder
```

Man pages are available for all commands: `man vp`, `man vp-search`, etc.
Install with `make man`.

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
- `vp_get_command` — retrieve workflow instructions
- `vp_palace_status` — browse knowledge structure
- `vp_traverse` — walk the knowledge graph

You don't need to invoke these manually.

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
`{vault}/Projects/{project}/agentctx/config.toml` (project-level).

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

### "vp: read config ... no such file or directory"

Create `~/.config/vibe-palace/config.toml` with `vault_path` set to an
absolute path.

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

Create `.vibe-palace.toml` in your project root with a valid `[project]`
section. The `name` field must be a slug (lowercase, hyphens, max 64 chars).

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
