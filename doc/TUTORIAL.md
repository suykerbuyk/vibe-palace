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

[pass] Global config:
       /home/you/.config/vibe-palace/config.toml
[pass] Vault:
       /home/you/vibe-palace-vault
       git repository initialized
[pass] Project config:
       created /home/you/code/myapp/.vibe-palace.toml (myapp, go.mod detected)
[pass] Vault project:
       /home/you/vibe-palace-vault/Projects/myapp/config.toml
[pass] Project templates:
       scaffolded Projects/myapp/{commands,skills}/
[pass] Agent wiring:
       AGENTS.md — block added
[skip] Agent wiring:
       .github/copilot-instructions.md — .github/ not present; create the dir to
         wire
[pass] Slash-command shims (project):
       added 22, updated 0 (commands +15 skills +7)
[pass] Hook wiring:
       vp hook installed
[pass] Project .gitignore:
       host-local vp artifacts ignored
[skip] Git commit.msg hook:
       /home/you/code/myapp is not a git repository — no hook to install
[info] Upgrade policy:
       `vp init` is additive: it never removes a stale shim, and it never
         writes, prunes or reconciles vault Templates/.
       - Stale shims (.claude/commands/vpc-*.md): `vp commands upgrade` removes
         them. It never touches vault Templates/.
       - Command overrides (vault Templates/commands/): nothing resets one
         unless you name it. `vp commands reset NAME` removes it, and a backup
         is kept.
       - Skill overrides (vault Templates/skills/): nothing resets one unless
         you name it. `vp skills reset NAME` removes it, and a backup is kept.

Summary: 10 ok, 2 skip. Re-run `vp init` anytime — it is idempotent.
```

Every onboarding step gets its own row, including the ones that had none
before: the vault-side `Vault project` and `Project templates` writes, the
project `.gitignore`, and the `commit.msg` git hook. A row is never omitted
because a step succeeded quietly — a missing row would be indistinguishable
from a step that never ran.

There is no `Templates` row. `vp init` never writes, prunes or reconciles
your vault's `Templates/` directory — a fresh install does not even create
it. Every built-in command and skill is served from the copy compiled into
the `vp` binary; `Templates/` holds only files you write yourself (see
[Customizing a command template](#customizing-a-command-template)). It does
*read* it: a vault-wide command or skill you add there is resolved into this
project's `vpc-*`/`vps-*` shims like any built-in.

The closing `Upgrade policy` row is an advisory, not a step: it states what
`vp init` deliberately does **not** do. Onboarding is additive: it never
removes a stale shim — `vp commands upgrade` does — and never writes,
prunes or reconciles `Templates/`. Neither upgrade command changes a
`Templates/` file either. Only a reset you name removes an override of a
built-in: `vp commands reset NAME` for `Templates/commands/` and
`vp skills reset NAME` for `Templates/skills/`, each keeping a backup
first (see [Resetting an override](#resetting-an-override)).

The `Project config` row reports which signal marked the directory as a
project: `.git`, `.vibe-palace.toml`, or one of the supported ecosystem
manifests (`go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`,
`pom.xml`). Directories without any of these — including your `$HOME`
and the filesystem root — are force-skipped with a clear reason.

Re-running `vp init` in the same directory is safe: nothing is
overwritten, and no row claims work it did not do.

It is **not** a no-op, and the rows say which is which. Every step runs
again and reconciles; a step that found its artifact already in place says
so — `Project templates` reports `[info]` with `… already present — nothing
to scaffold` —
while a step that reconciled a file in place still reports `[pass]` with a
summary that describes the state it left, not work performed:

```
vp init — vibe-palace 0.1.0-dev

[info] Global config:
       /home/you/.config/vibe-palace/config.toml (already exists, skipped)
[info] Vault:
       already configured
[pass] Project config:
       /home/you/code/myapp/.vibe-palace.toml (myapp, .vibe-palace.toml
         detected)
[pass] Vault project:
       /home/you/vibe-palace-vault/Projects/myapp/config.toml
[info] Project templates:
       Projects/myapp/{commands,skills}/ already present — nothing to scaffold
[info] Agent wiring:
       AGENTS.md — block unchanged
[skip] Agent wiring:
       .github/copilot-instructions.md — .github/ not present; create the dir to
         wire
[info] Slash-command shims (project):
       added 0, updated 0 (commands +0 skills +0)
[info] Hook wiring:
       vp hook already installed
[pass] Project .gitignore:
       host-local vp artifacts ignored
[skip] Git commit.msg hook:
       /home/you/code/myapp is not a git repository — no hook to install

Summary: 10 ok, 2 skip. Re-run `vp init` anytime — it is idempotent.
```

(The `Upgrade policy` advisory prints on this run too; it is elided above.)

An earlier release short-circuited the project steps entirely once
`.vibe-palace.toml` existed, which is why a project first onboarded by
something other than `vp init` could stay half-scaffolded forever. The
steps run every time now.

### Verify Installation

Run the built-in diagnostic to verify everything works:

```bash
vp check
```

Expected output:

```
vp check — vibe-palace installation diagnostic (0.1.0-dev)

[pass] Config:
       /home/you/.config/vibe-palace/config.toml
       vault_path = /home/you/vibe-palace-vault
[pass] Vault:
       /home/you/vibe-palace-vault (exists)
[pass] Settings:
       model=sentence-transformers/all-MiniLM-L6-v2  search_limit=10
[pass] Embedder:
       ONNX loaded, 384 dimensions
[pass] Git:
       remotes: github, vault
[pass] Config Staleness:
       config is up to date
[info] Project:
       my-project (from .vibe-palace.toml)
[pass] Surface:
       binary v1 >= vault max

All checks passed.
```

The **Surface** row is the closing line: it mirrors the runtime MCP
write-gate, comparing this binary's MCP tool-surface version against the
highest `.surface` stamp recorded in the vault. It reports `[pass]` when the
binary is current, and `[FAIL]` (with upgrade remediation) when the vault was
written by a newer binary than the one you are running.

For scripting, `vp check --json` emits a stable machine-readable report
(`{version, binary, checks, summary, exit_code}`) and exits non-zero when any
check fails:

```bash
vp check --json    # exit_code 1 on any [FAIL], 0 otherwise
```

For fast preflights, `--check <name>` runs only the named check(s)
(comma-separated) via selective execution — skipping the expensive embedder
load and tool-registry build. The surface compatibility gate needs none of
that, so a surface-only preflight is near-instant even on a cold model cache:

```bash
vp check --check surface --json    # only the Surface check; no model load
```

The session restart/wrap flows use this to confirm vault write-compatibility
without paying for the full 33-row report or the ~90MB model download. An
unknown check name exits non-zero with an `unknown check` diagnostic.

Selectable check names:

| Name | Reports |
|------|---------|
| `surface` | Binary-vs-vault MCP surface compatibility (the runtime write-gate verdict) |
| `resume-caps` | Any project whose `resume.md` has outgrown its size / row caps |

### Resume caps

`resume.md` is a gateway, not an archive — every byte is paid for at session
start by `vp_bootstrap_context`. The wrap flow prunes it to three caps, and
`vp check` warns when a project has drifted past them:

- total size over **25 KB**
- `## Project History` over **15** data rows
- `## Completed Plans` over **12** data rows

```bash
vp check --check resume-caps
```

```
[info] Resume caps:
       2 of 6 resume.md over cap
         rezbldr: Project History 18 rows (cap 15)
         vibe-palace: 64.8 KB (cap 25 KB); Completed Plans 13 rows (cap 12)
       Caps: 25 KB total, Project History 15 rows, Completed Plans 12 rows.
       resume.md is a gateway, not an archive — prune at the next wrap
         (/vpc-wrap Step 3); the full record already lives in iterations.md and
         tasks/done/.
```

The row is **advisory** — `[info]`, never `[FAIL]`, so it never changes the
exit code. The check is strictly read-only: it never edits, trims or "fixes"
`resume.md`. Pruning happens during `/vpc-wrap` Step 3, where the judgement
about *what* to cut belongs. A project with no `resume.md`, or a resume with no
such section, is silent — absence is not a violation.

The first run downloads `all-MiniLM-L6-v2` (~90MB) from HuggingFace for
semantic search. The model is cached at `{vault}/palace/.local/models/` —
subsequent runs use the cache.

If the embedder step fails, see [Troubleshooting](#model-download-fails).

Other useful commands:

```bash
vp version             # print version, commit, build date; DIRTY if built from uncommitted source
vp version --surface   # print the binary's MCP tool-surface version (e.g. "surface: 1")
vp help                # show all commands
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

> **Executable version.** For a machine-verified walkthrough of the
> steps in this chapter, run `make walkthrough-e2e` from the repo
> root. The harness prints an annotated transcript of the same
> operations against a sandboxed `HOME`; if it drifts from this
> chapter, one of the two is wrong.

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

### One-command registration (recommended)

`vp mcp install` registers the vibe-palace MCP server with a host for you,
idempotently, backing up any file it edits. The *same* server (`vp mcp`,
JSON-RPC over stdio) backs every host — only the registration differs, so the
flag picks which host's config to write:

```bash
vp mcp install --claude-plugin   # Claude Code (local plugin/marketplace)
vp mcp install --grok            # Grok Build (xAI) via `grok mcp add`
vp mcp install --zed             # Zed editor (context_servers entry)
vp mcp install --grok --zed      # register with several hosts at once
```

Each installer also ensures `AGENTS.md` carries the managed behavioral block
(the cross-host baseline that teaches `vp_bootstrap_context` and the
`vpc-*`/`vps-*` triggers). Reverse any of them with
`vp mcp uninstall --<host>`. Check what's registered on this machine with
`vp check` — it prints one `MCP host: <name>` row per detected host.

The manual per-host instructions below remain valid if you prefer to wire the
config yourself, or for hosts vp does not yet write automatically (Neovim,
Cursor, OpenCode).

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
with its full tool surface available (the authoritative tool list is
`internal/mcp/tool_surface.golden.json` in the source tree).

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

**The supported Zed path is a Claude-shaped ACP agent in the agent panel — not the
native pane.** Two facts have to reach you here, because neither is discoverable:

1. **Configure a Claude-shaped ACP agent** under Zed's `agent_servers` (e.g.
   `claude-acp`). It inherits Claude Code's hook path — not because it is ACP, but
   because `claude-agent-acp` loads `settingSources: ["user","project","local"]`.
   ACP itself supplies nothing, and `gemini` configured the same way and ended the
   same way produced **nothing**. Configure the Claude-shaped agent specifically.
2. **Archive the thread when you are done.** Archiving is what ends the ACP session and
   fires `SessionEnd`, which flushes the `.zst` transcript, closes the note↔manifest
   link (manifest prefers the wrap note over the Stop stub), and scores friction on
   wrap notes that were never scored. Idle time does **not** do it (measured: 12m43s
   undisturbed, no archive), and neither `restart` nor `exit` does it. **Stop** writes
   the attributed session note earlier, with no archive and no friction yet. Leave
   threads open and the archive **and** the wrap-note friction score stay deferred —
   while the Stop note makes it look like capture worked.

The mechanism behind fact 1 is that `claude-agent-acp` loads
`settingSources: ["user","project","local"]`, which brings `~/.claude/settings.json` —
and its `vp hook` entries — into scope. That is an **upstream implementation detail of a
third-party package**, not a contract vibe-palace controls; treat a future change to it
as a regression risk.

**The native pane remains Zed's default and is not the supported path.** It reaches
vibe-palace over MCP only, so it has no `SessionEnd` hook: a capture there is durable
only if the agent passes `transcript`, and a capture with no transcript now **fails
loud** rather than reporting success for an archive that will never exist.

The MCP registration below is still what wires `vp` into Zed, and it is needed for both
panes.

The one-command path is `vp mcp install --zed`. It surgically adds the
`context_servers` entry below to `~/.config/zed/settings.json` using a
comment-preserving JWCC editor (tailscale/hujson), so your existing comments
and settings survive; the pre-edit file is saved to `settings.json.vp.bak`.
Restart Zed (or reload settings) to activate.

To wire it by hand instead, add to `~/.config/zed/settings.json` (or
`.zed/settings.json` in your project):

```json
{
  "context_servers": {
    "vibe-palace": {
      "command": "vp",
      "args": ["mcp"]
    }
  }
}
```

No `env` block is needed: Zed launches the server with its own inherited
environment, so provider keys (e.g. `XAI_API_KEY`) flow through from the shell
that started Zed. Provider configuration is Zed's concern, not vibe-palace's.

**The MCP tools reach any provider Zed supports; DURABILITY DOES NOT.** Those are
separate questions, and reading the first as the second is the mistake this section
exists to prevent. `vp_cmd`, `vp_skill` and `vp_capture_session` are callable from any
Zed provider that can call tools. Whether the session survives depends on the hook path,
which only a **Claude-shaped ACP agent** inherits — the `gemini` negative control ended
identically and produced nothing durable. Do not generalize from "the tools work" to
"the session is captured".

### Grok Build (xAI)

The one-command path is `vp mcp install --grok`. It shells out to the official
`grok mcp add` CLI (so Grok owns its own `~/.grok/config.toml` schema and vp
never has to track it), registering:

```toml
[mcp_servers.vibe-palace]
command = "vp"
args = ["mcp"]

[mcp_servers.vibe-palace.env]
XAI_API_KEY = "${XAI_API_KEY}"   # expanded by Grok at server-spawn time
# ...ANTHROPIC_API_KEY / OPENAI_API_KEY / GOOGLE_API_KEY likewise
```

Requires `grok` on your PATH. Verify with `grok mcp doctor` or `grok mcp list`
(it should show `vibe-palace: vp mcp`). Reverse with `vp mcp uninstall --grok`
(which runs `grok mcp remove vibe-palace`). Grok Build also reads `AGENTS.md`
natively, so the managed block gives it the same behavioral contract as Claude
Code.

**Durability is MCP-first, and the hook path is not closed to Grok.** Session notes,
transcript archives, and memory commits normally go through MCP tools — typically
`/vpc-wrap` → `vp_capture_session` (+ `vp_vault_sync`) — so treat MCP capture as the
mechanism you rely on: if the agent skips wrap, nothing else is guaranteed to run.

**Corrected 2026-08-27 — Grok is not structurally hook-less.** This paragraph used to
say Grok has no Claude-style SessionEnd `vp hook`. `vp hook` accepts Grok's own wire
dialect (`internal/hook`: Grok sends `sessionId`, Claude Code sends `session_id`, and the
spelling is what names the host), and Grok's hook wiring lives in
`~/.claude/settings.json`. So a Grok session **can** reach the hook path when that wiring
is present. What remains true is that vibe-palace does not assume it: the MCP capture path
still treats grok as hook-less for the inline-archive default below.

| Concern | Grok / hook-less | Claude Code |
|---------|------------------|-------------|
| Session note | `vp_capture_session` (via wrap/capture) | Same MCP path **and** `vp hook` on SessionEnd |
| Transcript archive | **Inline archive by default** when handshake-derived host is grok/xai/zed and `transcript` is non-empty. Omit the transcript on one of those hosts and the capture **fails loud** — no hook will archive it later, so the note would otherwise be born permanently archive-less | SessionEnd archives the host JSONL; `archive_transcript` is a no-op |
| AI memory | `vp_memory_*` writes + wrap commits `Projects/<slug>/memory/` | Same tools + SessionEnd harvest of Claude native memory |
| Enrichment-queue drain | Not automatic (hook-only) | `DrainEnrichmentQueue` on SessionEnd |

Shims under `.grok/plugins/vibe-palace/commands/` and `.grok/skills/` are **host
UX** onto `vp_cmd` / `vp_skill` — not a second durability mechanism. Details:
[Ending a Session](#ending-a-session), [ARCHITECTURE inline archive](ARCHITECTURE.md#inline-transcript-archive-on-hook-less-hosts),
and [COMMANDS durability-by-host](COMMANDS-AND-SKILLS.md#durability-by-host-claude-vs-hook-less).

### Connect Grok web (or the xAI API) to vibe-palace

Everything above wires vibe-palace into a **local** host that can spawn `vp mcp`
over stdio. To reach vibe-palace from a *remote* client — Grok on the web, or the
xAI API's remote-MCP feature — you instead run the dedicated HTTP transport,
`vp mcp serve`, and expose it through a tunnel.

**1. Start the remote MCP server.**

```bash
VP_MCP_BEARER_TOKEN=$(openssl rand -hex 32) vp mcp serve
```

- Binds `127.0.0.1:7423` by default (override with `--addr` / `--port`; the port
  falls back to `http_port` in config).
- **Read-only by default** — the 20 vault-mutating tools are stripped from the
  surface, so a remote client can read and search but cannot write the vault.
  Pass `--allow-writes` to expose them too (see the security note below).
- The bearer token is read from the environment variable named by
  `--bearer-token-env` (default `VP_MCP_BEARER_TOKEN`). With it set, every request
  must carry `Authorization: Bearer <token>`. If the variable is **unset**, the
  server runs UNAUTHENTICATED and prints a loud warning — only acceptable behind
  a tunnel or network ACL you fully control.
- The server speaks the MCP protocol over Streamable HTTP at the **root path**
  (`/`). There is no in-binary TLS.

**2. Expose it with a tunnel.**

Use any tunnel that gives you a public HTTPS URL and terminates TLS for you.
For example, with Cloudflare Tunnel:

```bash
cloudflared tunnel --url http://127.0.0.1:7423
```

This prints a public `https://<random>.trycloudflare.com` URL. ngrok
(`ngrok http 7423`) or `tailscale funnel 7423` work equally well. Because the
server serves MCP at `/`, the URL you hand to clients is the tunnel root —
`https://<public-host>/` — with no extra path suffix.

**3a. Register with Grok web.** Go to [grok.com](https://grok.com) →
**Connectors** → **Bring Your Own MCP** (Custom), and paste:

- **URL:** `https://<public-host>/`
- **Authorization:** the bearer token value (sent as `Bearer <token>`).

**3b. Register with the Grok CLI** against the public endpoint:

```bash
grok mcp add vibe-palace-remote \
  --url https://<public-host>/ \
  --type streamable_http
```

`streamable_http` is the transport token Grok uses for a Streamable-HTTP MCP
server (verify the accepted values with `grok mcp add --help` for your version).
Confirm with `grok mcp doctor vibe-palace-remote`.

**4. xAI API (remote MCP).** When calling the xAI API directly, reference the
server as a remote-MCP tool with the public URL and the bearer header:

```json
{
  "type": "mcp",
  "server_label": "vibe-palace",
  "server_url": "https://<public-host>/",
  "authorization": "Bearer <token>"
}
```

**Security note.** The bearer token lives **only** in the server's environment —
it is never written to vibe-palace config (only the env-var *name* is
configurable). Read-only is the default; `--allow-writes` exposes vault mutation
(create / edit / delete / move, resume and task edits) over the network, which is
a meaningful escalation — only enable it for a tunnel and token you trust, and
prefer a freshly generated, high-entropy token.

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
vp tasks                    # list active tasks, grouped by epic, with priority and status
vp tasks --done             # include completed and cancelled tasks
vp tasks --epic <slug>      # just the subtree under an epic (or story), re-rooted
vp tasks --standalone       # only the tasks that belong to no epic
vp tasks epics              # roll-up of every epic: open/total, priority, status
vp tasks edit <slug>        # open a task file in $EDITOR
```

The `--epic`, `--standalone`, and `epics` views are epic-aware: the epic tree
and each task's role (epic / story / task) are **derived server-side** from the
`Parent` links, never re-asked (see `doc/adr/006-derive-dont-ask.md`). The same
derived views are reachable from the `/vpc-tasks-epics`, `/vpc-tasks-epic`,
`/vpc-tasks-standalone`, and `/vpc-tasks-read` slash commands, which call
`vp_list_tasks` (now accepting `epic`, `standalone`, and `epics_only`) and
`vp_get_task`. There is no `vp tasks read` CLI command — reading a raw task
body is `/vpc-tasks-read` (or `vp_get_task`) and `vp tasks edit` opens it for
editing.

### Search

```bash
vp search "authentication"  # semantic search across palace content
```

The project — `--project/-p`, or the one detected from the current directory —
must exist in the vault: a `Projects/<slug>/` directory or a palace store. An
unknown or malformed project exits 1 with a message naming how it was resolved,
before the embedding model loads. (Until 2026-09-10 it printed
`No results found.` and exited 0.) Neither a symlinked `Projects/<slug>` nor a
symlinked `palace/<slug>` counts, matching what cross-project search
enumerates — both trees refuse a symlinked project directory for the same
reason (see `search.ProjectExists`). MCP `vp_search` applies the identical
existence check before any embedder work: an unknown project is now a tool
error, not `[]`, so the two surfaces no longer disagree.

### Friction Analytics

Every captured session carries a friction score (0–100, higher = rougher).
Three read-only commands turn that history into actionable signal. All of them
auto-detect the project from the current directory (override with `--project/-p`)
and accept `--json` for machine-readable output. They each scan the project's
sessions once and compute pure analytics over them.

```bash
vp friction                 # recent-week average + top-friction triage
vp friction --top 5         # triage the 5 highest-friction sessions
vp trends                   # rolling 7/30/90-day windows + correction density + model regressions
vp effectiveness            # outcome (100 - friction) WITH vs WITHOUT context
```

**`vp friction`** reports the last 7 days' average friction and a top-N triage
table (default 10, set with `--top/-n`) of the highest-friction sessions worth
reviewing:

```
Recent week (7d): 97 sessions, avg friction 18.3

Top-friction triage:
DATE         ITER FRIC  TITLE
2026-04-16   1    100   Auto-captured session...
```

With `--json`:

```json
{
  "recent_week": {"days": 7, "session_count": 97, "avg_friction": 18.3},
  "top": [
    {"id": "...", "date": "2026-04-16", "iteration": 1, "title": "Auto-captured session...", "friction_score": 100}
  ]
}
```

**`vp trends`** shows rolling 7/30/90-day friction windows, a correction-density
series over time, and friction deltas across model-change boundaries. Sessions
captured before friction breakdowns were recorded are excluded from the
correction-density series and counted explicitly (never treated as a measured
zero); model regressions are labeled with model coverage:

```
Friction windows:
WINDOW SESSIONS  AVG_FRIC
7d     97        18.3
30d    152       20.8
90d    161       21.9

Correction density:
  (161 sessions predate breakdown capture — excluded)

Model regressions:
  claude-opus-4-8[1m] → claude-opus-4-8: 0.0 → 0.0 (delta +0.0, 3→2 sessions)
  model coverage: 8 of 161 sessions
```

These rolling N-day windows are complementary to the ISO-calendar-week buckets
reported by `vp_get_friction_trends` — the windows answer "how rough was the
last week/month/quarter," the buckets answer "which calendar week was rough."

With `--json`:

```json
{
  "windows": [{"days": 7, "session_count": 97, "avg_friction": 18.3}],
  "correction_density": {"points": [{"date": "2026-04-16", "iteration": 1, "corrections": 12}], "missing_count": 161},
  "model_regressions": {
    "regressions": [{"from_model": "claude-opus-4-8[1m]", "to_model": "claude-opus-4-8", "from_avg_friction": 0.0, "to_avg_friction": 0.0, "delta": 0.0, "from_sessions": 3, "to_sessions": 2}],
    "modeled_count": 8,
    "unmodeled_count": 153
  }
}
```

**`vp effectiveness`** measures outcome (defined as `100 - friction`) for
sessions captured **with** context versus **without** it. "Context" is a binary
with/without split — a session has context if it recorded decisions or
files-changed — not a measure of context size:

```
Effectiveness (outcome = 100 - friction), with vs without context:

Overall: 161 sessions, avg outcome 78.1
  with context:    140 sessions, avg outcome 79.4
  without context: 21 sessions, avg outcome 69.2
  delta (with − without): +10.2

WEEK         SESSIONS  OUTCOME  WITH_CTX NO_CTX
2026-04-13   12        77.5     78.0     70.0
```

With `--json` it emits the full `EffectivenessResult`: a `project`, a per-week
`weeks` array (`week_start`, `session_count`, `avg_outcome`, `with_context`,
`without_context`, `avg_context_outcome`, `avg_no_context_outcome`), and an
`overall` object adding a `total_sessions` count and the `delta` between the
with- and without-context averages.

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
vp vault sync               # tidy capture artifacts, then pull and push
```

To disable git tracking, pass `--no-git` during init or set
`git_enabled = false` in your config file.

### Zero manual git in the vault

You should never have to run raw `git` inside the vault. Session capture and
the hooks constantly write machine-generated artifacts — session summaries,
transcript archives, knowledge-graph data, drawers, and `.surface` stamps —
and these are committed for you automatically:

- **`/restart`** heals leftover capture residue (from the previous session's
  hooks, a crash, or another machine) right after it pulls, so the vault is
  clean before context loads.
- **`/wrap`** sweeps the session's own capture artifacts after it syncs your
  narrative notes.
- **`vp vault sync`** itself now tidies by default: a plain `vp vault sync`
  commits the sweepable capture artifacts, then pulls and pushes, so you no
  longer have to run `vp vault tidy` first to clear capture churn. It still
  refuses up front — before any network — if the tree carries genuine
  non-artifact dirt (pending user memory does not count and never blocks). Pass
  `--no-tidy` for the old raw pull+push that refuses on *any* uncommitted change.

Both commit *only* classified capture artifacts and **report** everything else —
`git add -A` is never used, so a stray edit or an accidental project scaffold is
flagged for you instead of being silently committed.

To see what a sweep would do at any time, preview it without committing:

```bash
vp vault tidy --dry-run     # show what would be Swept vs. Reported
vp vault tidy --no-push     # commit the swept artifacts locally only
vp vault tidy               # commit swept artifacts and push to all remotes
```

The dry-run prints two counted lists — the artifacts it *would* sweep and the
dirt it leaves for your eyes — and commits nothing. When no remotes are
configured, a real run downgrades to a local-only commit instead of failing.

### Checking vault sync state

To see how the vault stands against its remotes without changing anything,
ask for a status report:

```bash
vp vault status             # fetch and report sync state for all remotes
vp vault status --no-fetch  # fast cached report (behind counts unknown)
vp vault status --json      # the versioned report as JSON
```

It is strictly read-only — it never commits, pushes, or touches the working
tree. Per remote it reports whether you are *ahead* (local commits not yet
pushed → `unpushed`), *behind*, *diverged*, and whether the remote is
*reachable*, plus the working-tree dirt (the same Swept vs. Reported split that
`vp vault tidy` uses). By default it runs a bounded `git fetch` so behind counts
are real; `--no-fetch` skips the network and reports behind as unknown.

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
- Tops up the vault `.gitignore` with any canonical line it lacks, shown
  as `[Update] Vault: top up vault .gitignore (+N canonical line(s))`
- Reconciles vault `Templates/` override-only — prunes byte-identical
  mirrors of built-ins, including any an older release left behind (see
  [Upgrading skills](#upgrading-skills) for what each command does to
  `Templates/`)
- Is idempotent — re-running on a synced tree is a no-op
- Does **not** touch agent files (`CLAUDE.md` etc.) or slash-command shims —
  those are owned by `vp commands upgrade`

> `vp config upgrade` is now a thin alias for `vp config sync` — it
> translates `--cwd` / `--project` into the equivalent `--tier` flag and
> delegates. The legacy pre-reconciler TOML-parsing path has been
> removed (byte-identical parity with the reconciler was verified before
> deletion; see `TestUpgradeAliasParity`). Prefer `vp config sync`
> directly for new scripts.

### Context Injection

```bash
vp inject                   # output bootstrap context as JSON (for scripting)
vp check                    # verify installation, config, vault, embedder
```

### Managing Commands

Vibe-palace ships a small catalog of built-in commands (`restart`, `wrap`,
`review-plan`, `cancel-plan`, `capture`, `execute-plan`, `license`,
`makefile`) embedded in the `vp` binary. Users
and projects can override or extend the catalog via the 5-tier resolver
(see `doc/COMMANDS-AND-SKILLS.md`).

List every command visible from your current project, with the tier that
provides it:

```bash
vp commands list                      # human-readable table
vp commands list --json               # machine-readable (for scripting)
vp commands list --project myapp      # include project-tier overrides
```

`vp commands upgrade` brings this project's vp-owned files up to date:
the `.claude/commands/vpc-*.md` shims (and the Grok twins), the `vps-*`
skill shims, the managed blocks in agent files, the project `.gitignore`
and the commit hook. It also compares each embedded command with your
vault-level copy (tier 4 — `{vault}/Templates/commands/`), where one
exists, but only to report it: it never writes or removes a
`Templates/` file, in any mode. A command with no vault copy is reported
`unneeded`. A vault copy that differs from the built-in is an override,
and it is listed as kept, byte for byte:

```
[keep] Templates/commands/wrap.md — override of a built-in (vault 6b8e659, embedded 4c91c8b); vp commands upgrade never resets one — to remove it: vp commands reset wrap
```

A vault copy that is an *earlier* version vibe-palace shipped of the
built-in is not an override: it is vp's bytes, which the next
`vp config sync` prunes, and until then it shadows the current built-in.
It is listed as stale, not kept:

```
[stale] Templates/commands/restart.md — an earlier shipped version of the built-in; vp config sync prunes it
```

An override is not pending work. The shim drift it can cause is: an
override that changes a command's brief (its first paragraph) makes that
command's shim in each project stale, and refreshing the shim is work
the upgrade offers. Removing an override is a separate, named operation
— see [Resetting an override](#resetting-an-override).
Project/wing/room overrides are never touched.

```bash
vp commands upgrade --dry-run         # preview the plan; non-zero exit if shim or agent-file work is pending
vp commands upgrade                   # interactive: accept/skip per shim and agent-file change
vp commands upgrade --overwrite       # accept every shim and agent-file change (required in non-TTY); never resets a Templates/ override
vp commands upgrade --only restart    # consider only the restart command's shims and vault copy
```

In interactive mode you'll be prompted per change:

- `a` — accept this change
- `s` — skip it
- `A` — accept this and every remaining change
- `q` — abort without applying further changes

Without a terminal and without `--overwrite`, the `[keep]` lines print
first. Then, if shim or agent-file work is pending, the command refuses
with `Re-run with --overwrite to accept every shim and agent-file change
(it never resets a Templates/ override), or --dry-run to preview.` and
exits 1; otherwise it prints `Nothing to do` and exits 0. In a
`--dry-run`, an override's row reads `override  wrap  (vault 6b8e659,
embedded 4c91c8b; kept — vp commands reset wrap removes it)` and the
summary starts `N override(s) kept, N stale cop(y/ies) pending a prune
by vp config sync`. A stale copy is never counted as kept.

The upgrade commands commit nothing: they write no vault file. The
reset verbs are the ones that commit, locally, on a vault that is its
own git repository.

`vp commands upgrade` also surfaces stale vibe-palace managed blocks in
agent files (CLAUDE.md, AGENTS.md, …). When the block template's sha
changes, the upgrader offers to re-wire each file using the same atomic
path as `vp init`.

### Upgrading skills

Directory-form skills (SKILL.md + `references/*.md`) have their own
report, `vp skills upgrade`. It compares each built-in skill file with
your vault-level copy (`{vault}/Templates/skills/`), where one exists,
and only reports: it never writes or removes a `Templates/` file, never
reads stdin, and exits 0 in every mode. The skill *shims* in each
project are refreshed by `vp commands upgrade`.

```bash
vp skills upgrade --dry-run          # show the comparison as a plan table (grouped per skill)
vp skills upgrade                    # list the vault's skill overrides, one line per skill
vp skills upgrade --overwrite        # nothing to accept: prints a note and reports as usual
vp skills upgrade --only startup-analyst   # scope to a single skill
vp skills upgrade --granular         # list per file instead of per skill
```

Each skill with an override gets one `[keep]` line naming the files
that differ:

```
[keep] Templates/skills/chair/ — override of a built-in (1 file(s) differ: SKILL.md); vp skills upgrade never resets one — to remove it: vp skills reset chair
```

A skill file that is an earlier shipped version gets its own
`[stale] Templates/skills/<skill>/<file> — an earlier shipped version of
the built-in; vp config sync prunes it` line and is not counted as kept.
With `--granular` there is one line per file. `--overwrite` is accepted
for compatibility and prints `--overwrite: nothing to accept — vp skills
upgrade never resets an override; use vp skills reset NAME`. Removing a
skill override is `vp skills reset NAME` — see
[Resetting an override](#resetting-an-override).

What each command does to your vault's `Templates/` directory:

- **`vp init`** never writes, prunes or reconciles it, and a fresh
  install does not create the directory. Its shim step does read it, so
  a vault-wide command or skill you add there gets a shim in each
  project you initialise.
- **`vp config sync`** runs the override-only reconcile. It decides
  whether a `Templates/` copy is vp's by its bytes alone — no lock file,
  no host-local state — and never writes a template or prompts about
  one. A copy identical, line endings aside, to the current built-in or
  to an earlier version vibe-palace shipped (the frozen
  `internal/templates/shipped.txt`) is pruned, with no `.bak`, after a
  re-read that keeps it if it changed since the plan. Anything else is
  your override, and is kept. On a git vault nothing is removed until
  HEAD's copy, and each remote's, is checked to be vp's too; the removal
  is then committed. When HEAD's copy is yours it is restored in place
  instead, and a prune git cannot verify is deferred. On a vault that is
  its own repository it also commits a removal of vp-shipped bytes
  already pending in the worktree, and removes a leftover
  `.vibe-palace/templates.lock` that is untracked and not ignored. Files
  with names no built-in uses are never touched.
- **`vp commands upgrade` / `vp skills upgrade`** compare embedded vs
  vault and only report: each override of a built-in is listed as
  `[keep]` with the reset that removes it, and each earlier shipped
  version as `[stale]`. They never write or remove a
  `Templates/` file, in any mode.
- **`vp commands reset NAME...` / `vp skills reset NAME...`** remove the
  overrides you name, so the built-in serves them again, after keeping a
  content-named backup of each (vp-shipped bytes need none). They never write embedded bytes into
  `Templates/`. On a vault that is its own git repository the removal is
  committed locally. See [Resetting an override](#resetting-an-override).

A first `vp init` onto an older vault that still holds byte-identical
mirrors leaves them where they are. The `template-drift` check (in
`vp check`, and the restart/wrap selector calls) reports each one as
drift pending a prune — an `[info]`, not an error — until you run
`vp config sync`. It reports every override of a built-in as `[info]`
too, so an override that shadows the binary's copy stays visible.

Man pages are available for all commands: `man vp`, `man vp-search`,
`man vp-commands`, `man vp-commands-upgrade`, `man vp-commands-reset`,
`man vp-skills`, `man vp-skills-upgrade`, `man vp-skills-reset`, etc.
Install with `make man`.

### Resetting an override

The upgrade commands never remove a vault `Templates/` override of a
built-in. To remove one, name it:

```bash
vp commands reset wrap                    # remove Templates/commands/wrap.md
vp commands reset wrap restart --dry-run  # print each diff and backup name; write nothing
vp skills reset chair                     # every built-in file under Templates/skills/chair/
vp skills reset chair/references/x.md     # one file of a skill
```

The names are the consent. There is no prompt, and a reset behaves the
same in a terminal and in a pipe. Every name is checked before anything
is written: one unknown name refuses the whole invocation (exit 1) and
nothing changes. A vault-wide command or skill with a new name is not an
override of a built-in, so there is nothing to reset it to; it is yours
to delete. A name with no vault override prints `nothing to reset: wrap
has no vault override; the built-in already serves it`, or names the
project-tier override that serves it for the current project instead.

`--dry-run` prints each file's diff (vault → embedded) and the exact
backup name it would get, and writes nothing. It runs the same checks as
the real reset — the symlink refusal, the git identity and staged-change
preflights — so it refuses where the real run would, with the same exit
status, and it says `would commit …` for a removal an earlier run left
uncommitted.

A reset **removes** the file, so the embedded built-in serves it again.
It never writes the embedded bytes into `Templates/`. Before removing a
file it keeps a backup named by the file's content,
`<file>.<first 12 hex of its sha256>.bak`:

```
reset Templates/commands/wrap.md: removed your override; the built-in now serves it (backup: Templates/commands/wrap.md.3f9a0c1d2e4b.bak)
```

- A backup is created with a locked create that never replaces a file.
  When a backup holding the same bytes is already there, it is reused
  and nothing new is written, so resetting identical bytes twice keeps
  one backup.
- When a file with different bytes already has that name (an edited
  backup, say), the reset refuses and removes nothing. Move or rename
  that backup and run the reset again.
- The bare `<file>.bak` is never written, and `.bak` files an older
  binary left are never touched.
- vp-shipped bytes are removed with no backup: a mirror of the built-in
  (line endings aside) reads `(identical to the built-in; no backup
  needed)`, and a copy of an earlier shipped version reads `(a copy of
  an earlier shipped version of commands/restart.md; no backup needed —
  recoverable from vibe-palace history)`. `--dry-run` says the same, and
  names no backup for them.
- On a git vault backups are gitignored (`*.bak` is a canonical ignore
  line) and stay on the host that made them. A vault replicated by a
  file-sync tool carries them to every host; that is harmless, since
  older binaries only ever write the fixed `<file>.bak` name.

The reset works in two phases. First every path is checked and every
backup written; a failure there stops the reset with nothing removed.
Then each file is removed, but only if it still holds the bytes just
backed up: a file edited in between is kept, and the reset exits 2. A
removal that fails leaves that file and its backup in place and exits 2;
running the same reset again finishes it and reuses the backup.

A path reached through a symlink anywhere — the file,
`Templates/skills/<skill>`, `Templates/commands` — is refused before
anything is written (exit 1): vp does not follow or remove a link. On
Windows a path whose letter case or short (8.3) name differs from the
disk is refused too, as a fail-safe. Files you added to a skill
directory that are not built-in files are left in place and listed.
Project, wing and room overrides are never touched.

After it removes a command file or a `SKILL.md`, the reset prints:

```
Shims in each project may still carry the removed override's text (command brief / skill description). Run `vp commands upgrade --overwrite` (or `vp init`) in each project.
```

Shims are per project and a reset is vault-wide, so do that in every
project.

**Committing the removal.** What happens next depends on the vault's git
shape:

- **A vault that is its own git repository.** The removal of each
  tracked file is committed locally, never pushed, with the subject
  `chore(templates): operator reset of N vault Templates/ file(s)`.
  The body names the invocation, each path and its backup, and the
  host, and says that the committed copy of each file is in the commit's
  parent and the working-tree bytes at reset time are in the backup.
  `vp vault sync` publishes the commit. Before anything is written the
  reset checks that git has an identity (when a tracked file would be
  committed) and that the index holds no staged change for any target —
  a commit would take staged bytes no backup holds. Either problem
  refuses the reset with nothing changed.
- **The commit fails** (a pre-commit hook refuses it, say). The removal
  is left unstaged, and the reset prints `removed, not committed` with
  the manual commit and restore commands and exits 2. Running the same
  reset again finishes the commit — also when the deletion is left
  staged (after `git rm`, or when unstaging it failed): a staged
  deletion holds no bytes. The finishing commit names the backup the
  first run wrote. If a commit lands but something else puts a file back
  in HEAD meanwhile, the reset says which commit landed and names only
  the files left.
- **A vault nested in another repository** (a project or dotfiles
  repo). vp never commits it. The file is removed and backed up, and the
  output says the deletion is left in that repository's working tree for
  its owner to commit. Do not run `vp vault sync` there until
  `vault-sync-pushes-the-enclosing-repo-of-a-nested-vault` lands.
- **Git not on PATH**, though the vault has a `.git`. The reset happens
  and warns `not committed`; exit 0.
- **An unreadable repository.** The reset is refused before any write;
  exit 2.
- **Not a git vault.** The removal and the backup are the whole reset.

The reset does not fetch. If a remote holds a newer override of the same
file, it meets your commit at the next pull as a modify/delete conflict,
which is left for you to resolve.

The next `vp config sync` never restores a reset file: it treats the
path as gone. If the reset's commit failed and left the removal pending,
that sync leaves an override's removal for the reset to finish (its row
names `vp commands reset NAME`), and commits a removal whose committed
copy is vp-shipped bytes itself.

### Customizing a command template

The command templates shipped inside `vp` are the *floor* — a default.
You override one in the vault, at one of two tiers: `Templates/` for
every project, or `Projects/<slug>/` for one. This walkthrough shows
where a default comes from, both tiers, and how `vp config sync` tells
your override from a copy of vp's own bytes.

**1. Nothing is materialized.** A fresh `vp init` writes no template
into your vault — the resolver serves every command and skill from the
copy compiled into `vp`:

```bash
vp init
ls ~/vibe-palace-vault/Templates
# → ls: cannot access '…/Templates': No such file or directory
vp commands list
# → every built-in reports tier `embedded`
```

`vp init` does scaffold `<vault>/Projects/<your-slug>/commands/` and
`<vault>/Projects/<your-slug>/skills/`, each with a README stub
explaining the 5-tier precedence.

Three ways to fetch a default to start an override from:

- the `vp_get_command` MCP tool, for a command;
- `vp skills show <name> --no-project` (add `--section NAME` for one
  reference), for a skill. `--no-project` matters once an override
  exists: run from inside the project, `vp skills show` serves the
  project's own copy by default, and `--no-project` shows the vault or
  built-in copy instead;
- the source file under `internal/templates/templates/` in a
  vibe-palace checkout.

**2. Override for one project.** Put your copy at the project tier:

```bash
$EDITOR ~/vibe-palace-vault/Projects/myapp/commands/wrap.md
vp commands list --project myapp
# → wrap … project
```

The project-level file shadows the built-in (and any vault-wide
override) for `myapp` only; other projects keep resolving `wrap` as
before.

**3. Override for every project.** Put your copy under the vault-wide
tier, `<vault>/Templates/commands/` or `<vault>/Templates/skills/`. A
file with a new name — `Templates/commands/mycmd.md` — adds a command
to every project (`vp commands list` shows it at tier `vault`); a file
with a built-in's name overrides that built-in everywhere.

No vp reconciler and no upgrade command changes an override in either
tier. "Safe" means exactly that — safe from vp's reconcilers and
upgrade commands; `vp_vault_write` / `vp_vault_edit` / `vp_vault_move`
/ `vp_vault_delete` and `vp vault commit --paths .` are direct edits and
reach any tier.

**Edit the copy before you sync.** `vp config sync` decides whether a
`Templates/` copy is vp's by its bytes alone: a copy identical, line
endings aside, to the current built-in or to a version in the frozen
`internal/templates/shipped.txt` — every version reachable from
`1f3bb62`, plus the two rows of tag `pre-rebase-501c96e` — is vp's, not
an override, and is pruned. Here a
vault holds an edited `wrap.md`, an unedited copy of `capture.md`, and
an old copy of `restart.md` a past release left behind (rows for the
templates it does not hold, and the paths, trimmed):

```
$ vp config sync --tier vault
Plan:
  [Delete] TemplateTree:Templates: prune Templates/commands/capture.md (byte-identical, line endings aside, to the current embedded copy) (…)
    - embedded_relpath=commands/capture.md
    - provenance=current
    - vault_sha=b693b49ba8bec5d7a66f5810828c394c63412667056946d146216cb6f6358951
    - key=b693b49ba8bec5d7a66f5810828c394c63412667056946d146216cb6f6358951
  [Delete] TemplateTree:Templates: prune Templates/commands/restart.md (matched an earlier shipped version of commands/restart.md; recoverable from vibe-palace history) (…)
    - embedded_relpath=commands/restart.md
    - provenance=earlier
    - vault_sha=6b06dcccc57f1debde3c1cec5ea3c719ba792a4538e0f58d38a4a29c7780ebe3
    - key=6b06dcccc57f1debde3c1cec5ea3c719ba792a4538e0f58d38a4a29c7780ebe3
  [Unchanged] TemplateTree:Templates: Templates/commands/wrap.md operator override of a built-in (kept) (…)
  TemplateTree:Templates: pruned Templates/commands/restart.md (earlier shipped version of commands/restart.md; no backup)
Summary: created=0 updated=0 unchanged=42 skipped=0 pruned=2
```

There is no prompt and nothing to answer: `wrap.md` is yours and is
kept, on every host, whether or not `--yes` is given. The two pruned
copies were vp's bytes — the current built-in and an earlier version of
one — so no backup is kept. The next sync prints `Nothing to do — all
tiers in sync.`

An override shadows the built-in for every project, and it misses
changes the binary depends on — `commands/wrap.md` supplies the
`expected_sha256` that `vp_update_resume` demands, for example. So the
`template-drift` check lists each one, as `[info]`, every time it runs:

```
[info] Template drift:
       1 override(s) of built-ins kept (of 41)
         Templates:Templates/commands/wrap.md: operator override of a built-in
           (kept; shadows embedded 4c91c8b4097d)
```

To drop an override, name it: `vp commands reset wrap` (or
`vp skills reset NAME`) removes it, keeps a backup, and commits the
removal on a vault that is its own repository (see
[Resetting an override](#resetting-an-override)). Do not use
`vp vault delete` for it.

**4. On a git vault.** Nothing is removed until HEAD's copy, and each
remote's, is checked to be vp's too; each copy is read as git would
check it out, so a vault whose `Templates/` passes through git-crypt,
LFS or any other filter is compared like with like. If something reset
a *committed* override to vp's bytes, the next `vp config sync` finds
your content in HEAD and restores it in place
(`restored Templates/commands/wrap.md from HEAD`) instead of removing or
committing anything. If git cannot answer — no identity, an unreadable
index, a filter that cannot run — the file is kept, a
`[Skip] … prune deferred` row says why, and the command exits 2. If a
remote holds an override you have not pulled, or a remote cannot be
reached, the prune is deferred. A vault inside another repository (a
project or dotfiles repo) never has that repository committed to or
pushed: a tracked copy there is kept. On a vault that is its own
repository, a removal of vp-shipped bytes you (or a failed commit) left
pending in the worktree is committed with the prune, in a paragraph of
its own that says vp found it pending; a pending removal of your own
content is never committed — its row names
`vp commands reset NAME` and the `git checkout HEAD -- <path>` that
restores it.

A path reached through a symlink — `Templates/`, `Templates/commands`,
or the file — is never followed or pruned; its row says it is kept.

There is no overwrite answer and no prompt. Until 2026-09-10 an `o`
answer (and `--yes`) replaced your file with the embedded copy, the next
sync pruned the result, and the prune's commit pushed the deletion to
every host. Until this release a host whose `.vibe-palace/templates.lock`
did not record an override prompted on it at every interactive sync.

**Rollout of the fix (surface v4).** The binary that stops this loss
raises `MCPSurfaceVersion` to 4, so an older binary on another host is
refused — exit 2, `action: cd ~/code/vibe-palace && git pull && make
install` — instead of running the old chain against a vault it shares.
Run `make install` on **every** host, then restart every AI harness on
that host: a `vp mcp` still running the old binary is refused
mid-session once the host's own hooks stamp v4. A host starts being
refused when it pulls a vault an upgraded host has written to (any task
write stamps `Projects/<p>/.surface`). Two windows remain: a lagging
host that runs `vp config sync` before it pulls the stamp, and
`VP_SURFACE_GATE=warn`. Never use that escape hatch for `vp config sync`,
`vp commands reset` / `vp skills reset`, or — on a host still running a v4
or older binary — the upgrade commands.

**Rollout of the upgrade-reset fix (surface v5).** The binary whose upgrade
commands never reset a `Templates/` override (and which adds
`vp commands reset` / `vp skills reset`) raises `MCPSurfaceVersion` to 5,
so a v4 binary on another host is refused — exit 2, `action: cd
~/code/vibe-palace && git pull && make install` — instead of running its
`vp commands upgrade --overwrite`, which replaces every override a v5 host
keeps with the embedded bytes (no backup for commands, one overwritable
`.bak` for skills). Run `make install` on **every** host — a host still on
v3 goes straight to v5 — then restart every AI harness on that host: a
`vp mcp` still running the v4 binary is refused once the host's own hooks
stamp v5. A reset itself stamps nothing (a removal and a `.bak` carry no
stamp), so a host starts being refused when it pulls a vault an upgraded
host has made a stamped write to — a task write or a session capture
stamps `Projects/<p>/.surface`. Two windows remain: a lagging host that
runs `vp commands upgrade --overwrite` before it pulls the stamp, and
`VP_SURFACE_GATE=warn`. Never use that escape hatch for `vp config sync`,
the reset verbs, or — on a v4 host — the upgrade commands.

**Recovering an override an older binary already deleted.**

1. Install the fixed binary on every host first.
2. List deletions under `Templates/`:
   `git -C <vault> log --format='%h %ad %s' --date=short --diff-filter=D -- Templates/`.
   vp's prune commits have the subject
   `chore(templates): prune vault mirrors superseded by the embedded floor`.
3. For each path: `git -C <vault> show <sha>^:<path>`. If it differs
   from what `vp_get_command` / `vp skills show --no-project` serves
   (`--no-project`, so a project-tier override is not what you compare
   against), restore it with
   `git -C <vault> checkout <sha>^ -- <path>` and commit. When in doubt,
   restore. An unneeded restore of the current embedded copy, or of any
   version in `shipped.txt` (every version reachable from `1f3bb62`, plus
   the two rows of tag `pre-rebase-501c96e`), is vp's bytes, and the next
   sync prunes it.
4. A non-git vault, or an override never committed: after one sync by
   an older binary, `<path>.bak` holds your override — rename it back.
   After two, the `.bak` holds embedded bytes and the override cannot be
   recovered from vp's side.
5. Leftovers older binaries wrote stay until you remove them — this
   release never touches them, except as noted:
   - `*.new` and `*.new.bak` beside a template (an old `n` answer's
     review copy). Diff it against your file if you like, then delete
     it: `find <vault>/Templates \( -name '*.new' -o -name '*.new.bak' \) -print`,
     then `rm` what it lists.
   - `.bak` files, whatever they hold (an old prune's copy of embedded
     bytes, an old overwrite's copy of your override, a reset's
     content-named backup). `vp config sync` never touches a `*.bak`;
     read one before you delete it.
   - `.vibe-palace/templates.lock`. No vp from this release reads it.
     On a vault that is its own git repository, `vp config sync` removes
     it when it is untracked and not ignored (the `--dry-run` row reads
     `remove the retired .vibe-palace/templates.lock (untracked and not
     ignored; …)`), because it makes `vp vault sync` refuse. A tracked
     or ignored lock, and any lock on a non-git or nested vault, is left
     — a host still running an older binary may share it — and
     `template-drift` reports it; delete it once every host runs this
     release.

**Recovering a copy the prune removed.** `vp config sync` removes, with
no backup, only bytes vibe-palace itself shipped: the current built-in
or an earlier version from the frozen `internal/templates/shipped.txt`.
Every such version is in vibe-palace's git history, so you can get it
back from a vibe-palace checkout. The prune row's `key=` detail is the
sha256 of the removed bytes with CRLF line endings folded to LF:

```bash
git -C <vibe-palace> fetch --tags     # two shipped versions live only under a tag
git -C <vibe-palace> log --all --format=%H -- \
    internal/templates/templates/commands/restart.md internal/context/templates/commands/restart.md
# for each commit c, until the sum equals the row's key=:
git -C <vibe-palace> show <c>:internal/templates/templates/commands/restart.md | tr -d '\r' | sha256sum
```

(Use the `internal/context/templates/` path for a commit before the
directory moved; `git show` says which exists.) Restoring the bytes
into `Templates/` defeats itself: they are still vp's bytes, and the
next sync prunes them again. To keep an old version deliberately, edit
it, or put it at the project tier (`Projects/<slug>/commands/`); a
vault-tier pin of an unedited shipped version is impossible by design.


**Changed in this release: vault `Templates/` overrides and the upgrade
commands.** Requires `make install` on every host (MCP surface v5).

- `vp commands upgrade` and `vp skills upgrade` no longer reset a vault
  `Templates/` override of a built-in, in any mode. `--overwrite` now
  accepts only vp-owned changes: command and skill shims, agent-file
  managed blocks, the project `.gitignore` and the commit hook. Each
  override is listed as `[keep]` and left byte-for-byte. Before,
  `--overwrite` — the documented non-TTY path, and where `vp check`
  remedies lead — replaced every override with the embedded copy,
  keeping no backup for commands and one overwritable `.bak` for skills.
- Resetting an override is an explicit, named operation:
  `vp commands reset NAME` / `vp skills reset NAME`. `--dry-run`
  previews it. It removes the file, so the built-in serves it, instead
  of writing a copy of the built-in into `Templates/`. Afterwards run
  `vp commands upgrade --overwrite` (or `vp init`) in each project, so
  its shims stop carrying the removed override's text.
- Backups are never overwritten, for commands and skills alike. Each is
  named by its content, `<file>.<first 12 hex of its sha256>.bak`, so
  resetting identical bytes twice reuses one backup. A different file
  never takes an existing name. The bare `<file>.bak` is not used, and
  existing `.bak` files are left untouched. On a git vault backups are
  gitignored and stay on the host; a vault replicated by a file-sync
  tool carries them to every host. This replaces the policy recorded in
  `doc/TEMPLATE_POLICY.md` (commands: none; skills: one `.bak`, replaced
  by the next reset).
- On a git vault that is its own repository, the removal is committed
  locally, scoped to the removed files, with a message naming the
  files, the backups and the host; `vp vault sync` publishes it. vp
  never commits into a repository that encloses the vault.
- `vp skills upgrade` now only reports.
- Existing `Projects/<slug>/{commands,skills}/README.md` stubs keep
  their old wording. vp writes a stub only when it is absent, so delete
  one to get the new text.
- `MCPSurfaceVersion` rises from 4 to 5. Once an upgraded host makes a
  stamped write (a task write or session capture; a reset or
  `vp commands upgrade` stamps nothing) and a lagging host has pulled
  it, that lagging host is refused on vault writes (exit 2,
  `git pull && make install`), so an older binary cannot reset overrides
  this release keeps. Run `make install` on every host — hosts still on
  v3 go straight to v5 — then restart every AI harness on it. See
  *Rollout of the upgrade-reset fix (surface v5)* above.

**Changed in this release: how `vp config sync` decides a `Templates/`
copy is vp's.** No surface bump: `MCPSurfaceVersion` stays 5, and
hosts on the previous release keep working against the same vault.

- Provenance is decided by the binary alone. A `Templates/` copy
  identical, line endings aside, to the current built-in or to a version
  in the frozen `internal/templates/shipped.txt` (every version reachable
  from `1f3bb62`, plus the two rows of tag `pre-rebase-501c96e`) is vp's;
  anything else is an
  override, kept. Before, a host decided from its own
  `.vibe-palace/templates.lock`, so a host without the lock prompted on
  every override at every interactive sync and could not recognise a
  stale copy an old release left.
- **New policy:** a copy of an *earlier* shipped version is pruned on
  every host, with no prompt and **no backup** — its bytes are in
  vibe-palace's history (see *Recovering a copy the prune removed*). The
  prune row and the prune commit name the version it matched.
- No prompt, no lock, no `.new` sidecar: the keep/`.new` prompt is gone,
  and `templates.lock` is neither read nor written. On a vault that is
  its own git repository an untracked, un-ignored leftover lock is
  removed, which unblocks `vp vault sync`.
- A removal of vp-shipped bytes already pending in the worktree (a
  failed prune commit, a failed reset commit, a hand deletion) is
  committed with the next prune, and the commit says vp found it
  pending. A pending removal of your own content is never committed.
- On a vault whose `Templates/` passes through a clean/smudge filter
  (git-crypt, LFS), the committed copy is compared as git checks it out;
  a filter that cannot run defers the prune instead of restoring
  filtered bytes over your file.
- A CRLF copy of vp's bytes (a `core.autocrlf=true` checkout) is vp's.
- `vp commands upgrade` / `vp skills upgrade` list an earlier shipped
  version as `[stale]`, not `[keep]`, and a reset removes one without a
  backup.
- A `Templates/` path reached through a symlink is kept and never
  followed, where the previous release pruned through it.
- Vault `Templates/` is again a supported vault-wide override tier.
- The Templates step no longer rewrites the vault `.gitignore` behind
  your back. Before, it silently appended any missing canonical line;
  now only the Vault step tops it up, as an `Update` you accept, so an
  interactive `vp config sync` on a vault whose `.gitignore` lacks a
  canonical line asks until you accept it (`--yes` accepts it).

**Promoting back to the `vp` source tree.** Vibe-palace cannot automate
promotion because at runtime it does not know where your vibe-palace
source checkout lives. To land an override as the new embedded floor
for the next release, copy your override — from
`<vault>/Projects/<slug>/commands/<name>.md` or
`<vault>/Templates/commands/<name>.md` — to
`<vp-repo>/internal/templates/templates/commands/<name>.md` and commit
it like any other source change.

---

## Part 5: Migration

If you are migrating from VibeVault or MemPalace, vibe-palace can import
your existing data. See [doc/MIGRATION.md](MIGRATION.md) for the full
migration guide, architecture details, and risk assessment.

### Quick Start

```bash
# Preview what would be imported (no changes written, no model download)
vp migrate vibevault --dry-run
vp migrate mempalace --export-path ~/mempalace-export.json --dry-run

# Run the actual import (in place: source and destination are your configured vault)
vp migrate vibevault
vp migrate mempalace --export-path ~/mempalace-export.json
```

`--vault-path` names the **source** vault to read sessions from; the
**destination** for all writes is always your configured `vault_path`. To
import from a different vault than you write to, point `--vault-path` at the
source and confirm with `--yes`:

```bash
vp migrate vibevault --vault-path ~/obsidian/VibeVault --yes
```

Each run prints a `Source` / `Destination` / `Same vault` banner before
scanning; a real cross-vault import needs `--yes` (or an interactive `[y/N]`
confirmation). After import, restart the MCP server to rebuild search indexes.

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

### Skills: personas that span a whole session

#### Cross-IDE compatibility matrix

`vps-<name>` delivery works across every MCP-capable coding surface,
but the *mechanism* differs by IDE. This table summarizes what each
surface ships and how skills are invoked there. For step-by-step
verification, see `doc/verify-skill-delivery.md`.

| IDE                         | Mechanism                                                   | Auto-invokes? | Notes                                                                 |
|-----------------------------|-------------------------------------------------------------|---------------|-----------------------------------------------------------------------|
| Claude Code                 | Native SKILL.md primitive (`.claude/skills/vps-<name>/SKILL.md`) | Yes       | Auto-loaded by Claude Code's skill picker; shim is a short delegation to `vp_skill`, with a `vp skills show <name>` fallback for when `vp_skill` cannot be loaded. |
| Cursor                      | Native rule file (`.cursor/rules/vps-<name>.mdc`)           | Pick from Rules panel | Only emitted when `.cursor/` or `.cursor/rules/` exists in the project. vp registers no MCP server with Cursor; without one, the rule's fallback runs `vp skills show <name>` from the project directory. |
| Zed + Claude                | Trigger phrase (managed block) + `vp_skill` MCP             | Yes, on trigger | Model recognizes `vps-<name>` via the agent-file managed block and calls `vp_skill`. |
| Zed + Gemini / Copilot Chat | Trigger phrase (managed block) + user-paste fallback        | Partial       | Awareness works from the managed block; user pastes `vp skills show <name>` output if MCP isn't wired. |
| Any MCP-capable host        | Trigger phrase + `vp_skill` MCP                             | Yes, on trigger | Works anywhere `vp mcp` can be registered. Provider-level tool-use policy applies. |

`.cursor/rules/vps-*.mdc` is vp-managed, depends on your vault and your
vp version, and is rewritten by `vp init`; commit it only if every
contributor shares a vault and a vp version.

**This table is about skill DELIVERY, not durability.** A row saying skills reach a
surface says nothing about whether a session captured there survives. In particular the
two Zed rows describe `vp_skill` invocation only: durable capture on Zed depends on
running a **Claude-shaped ACP agent** (see [§ Zed](#zed)), and the native pane — whichever
provider is selected in it — does not inherit that. Do not read either Zed row as "Zed
captures sessions".

Commands and skills look alike on the wire (`vpc-` vs `vps-`, both
trigger an MCP tool call) but they have very different **lifetimes**.

- **Commands are one-shot.** `vpc-wrap` says "do this one task, then
  come back to your normal posture." The model calls `vp_cmd`, follows
  the returned workflow, finishes, and drops back to the default
  assistant stance.
- **Skills are postural.** `vps-startup-analyst` says "adopt this
  persona and its objectives as a STANDING instruction for the rest of
  the session." The model calls `vp_skill`, merges the returned persona
  into its working-self, and keeps behaving that way turn after turn
  until it is told otherwise.

That standing-instruction stance is what lets one `vps-` call
substitute for pasting a long persona preamble into every message. It
also means you need explicit controls to *leave* that posture — which
is the rest of the contract embedded in the managed block:

- **`vps-clear`** — drops *all* currently active skill personas and
  returns the assistant to its default posture. Type it verbatim.
- **`vps-replace:<other>`** — a **model-parsed prefix**, not a tool
  parameter. The assistant recognizes the `replace:` prefix itself,
  strips it, drops every prior persona, and then calls `vp_skill` with
  `name=<other>` so the *new* persona arrives on a clean slate. Useful
  when you want to swap `vps-startup-analyst` for `vps-code-reviewer`
  without keeping the first one resident.
- **Stacking.** Multiple bare `vps-<name>` invocations *stack
  additively* — `vps-startup-analyst` followed by `vps-pricing-expert`
  gives you both personas active at once. Stacking is how you compose;
  `vps-replace:` is how you pivot.
- **Session boundary.** A new session starts with no active personas.
  Skills do *not* persist across sessions because the managed block is
  re-read on every session start and the model's working memory is
  reset; the block only teaches the model how to respond *within* the
  session it is currently running in.

The canonical copy of this contract lives in the managed block that
`vp init` wires into your project's `CLAUDE.md` / `AGENTS.md` /
`.cursorrules` / `.rules` / `.github/copilot-instructions.md`. When the
schema changes, `vp init` detects the stale block via its content hash
and rewrites it in place; user-authored content outside the delimited
region is preserved byte-for-byte.

### Fetching skill references on demand

Directory-form skills ship a lightweight `SKILL.md` plus any number of
siblings under `references/`. `vp_skill` only inlines the SKILL.md body
into its activation frame — the references are listed by name and
fetched on demand so the context window does not swell with material
the current conversation may not need. Sample flow inside a Claude or
Cursor session:

1. Type `vps-startup-analyst` (or call `vp_skill` with
   `name=startup-analyst`). The returned frame ends with:

   ```
   References (fetch on demand via vp_get_skill_section):
     - capex-opex
     - competitive-landscape
     - funding-sources
     - reality-validation
     - strategic-partnerships
   ```

2. When a sub-topic actually comes up — say the user asks about capex
   vs. opex split — the assistant calls
   `vp_get_skill_section(name="startup-analyst", section="capex-opex")`
   and merges that body into its working context.

From the CLI the same content is reachable via `vp skills show
<name>` (SKILL.md + references list) and `vp skills show <name>
--section <ref>` (just the reference body). Run from a project
directory, `vp skills show` resolves that project's tier by default,
exactly as `vp_skill` does, so the two serve the same body there;
`--project SLUG` picks another project and `--no-project` skips the
project tier. With no vault configured at all it prints the built-in
skill. (`vp skills list` does not default the project; pass
`--project`.) Every persona shim's MCP-less fallback runs this command.
Reference resolution is
per-file: a project may override SKILL.md without having to clone
every reference — the resolver transparently falls back to vault or
embedded copies for anything the project did not override.

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

`vp commands upgrade` refreshes three things in one pass: the command
shims (`.claude/commands/vpc-*.md`), the agent-file managed blocks, and
the per-project **skill** shims (`.claude/skills/vps-*/SKILL.md` and,
when a `.cursor/` layout is present, `.cursor/rules/vps-*.mdc`). Skill
shims previously refreshed only at `vp init`; now bumping a skill's
version, adding a new `vps-*` skill, or removing one propagates on
upgrade too — stale skill shims are offered for removal with the same
accept/skip/accept-all prompts, and `--dry-run` previews skill-shim
drift alongside command drift.

**Grok (xAI) shims.** When Grok Build is detected — the project has a
`.grok/` directory, `~/.grok/` exists, or the `grok` CLI is on `PATH` —
`vp init` and `vp commands upgrade` emit:
- Native slash-command shims under `.grok/plugins/vibe-palace/commands/`
  (one `vpc-<name>.md` per command). This uses Grok's project plugin
  `commands/` surface so `/vpc-restart` etc. appear as first-class slash
  commands in the Grok TUI (same naming as the Claude shims).
- Skill shims under `.grok/skills/` (per-persona `vps-<name>/` + the
  `/vpc` command hub skill that provides a single-argument dispatcher and
  detailed usage notes, including the `vp_get_task` + resource paging
  discipline for task commands).
`.grok/` is gitignored and host-local just like `.claude/`.
User-global shims refresh only when `vp mcp install --grok` /
`--claude-plugin` is re-run; re-run it after upgrading vp. When an
upgrade re-renders many shims, `vp commands upgrade` asks once per shim;
answer `A` once, or pass `--overwrite`.

Stale shims (for commands that were renamed or removed from the vault)
are detected automatically but never deleted without explicit consent.
Files under `.claude/commands/` that do not carry the vibe-palace shim
marker are reported as `custom` and left strictly alone.

The shims are regeneratable on any fresh clone, so they are treated as
host-local by default: `vp init` (and `vp commands upgrade`) reconcile
the project repo-root `.gitignore` to ignore `/.claude/` along with the
other vp-written artifacts (`/CLAUDE.md`, `/commit.msg`, `/.grok/`,
`/.vibe-palace/`). The reconcile is append-only and idempotent — it
never rewrites or reorders your existing `.gitignore` lines — and
`vp check` flags an advisory when a canonical entry is missing.

Teams that instead want everyone to see the same `/vpc-` menu checked
in can commit the shim bodies (they are deterministic, so diffs are
stable); since the reconciler ignores all of `/.claude/`, narrow the
ignore yourself (e.g. drop `/.claude/` and add only the paths you don't
want tracked) and keep the shim files force-added.

### Ending a Session

Say "capture session" or "wrap up" (or type `vpc-wrap`). The AI follows the
wrap/capture command and calls `vp_capture_session` with:

- **summary** — what was accomplished
- **tag** — implementation, debugging, refactor, exploration, etc.
- **decisions** — key technical decisions made
- **files_changed** — files created or modified
- **open_threads** — unresolved items for next session
- **transcript** — full session text when the host can supply it (chunked,
  indexed, friction-scored). **Required** for a durable archive on a
  handshake-derived hook-less host, and enforced there: omitting it makes the
  capture fail loud rather than report success for an archive that will never exist
- **archive_transcript** — templates pass `true` for model-facing clarity;
  on handshake-derived grok/xai/zed the server **auto-archives** a non-empty
  transcript even if this flag is omitted; on Claude Code the flag is a
  **no-op** (SessionEnd owns the authoritative archive)

If a transcript is provided, the capture pipeline:
1. Detects format (plain text, markdown, JSON-RPC chat)
2. Chunks the text (sliding window, 800 chars, 100 overlap)
3. Classifies chunks by room (testing, api, devops, etc.) and hall (facts,
   decisions, discoveries, etc.)
4. Embeds and indexes all chunks for semantic search
5. Extracts entities (file paths, URLs) into the knowledge graph
6. Computes a friction score (0–100)
7. On hook-less hosts (default) or when forced: creates a born-linked inline
   transcript archive under `Projects/<slug>/transcripts/`

**Memory.** Host-agnostic notes live at `Projects/<slug>/memory/` via
`vp_memory_write` / `read` / `list` / `delete`. Bootstrap returns a memory
*index* (not bodies). Wrap includes that directory in its vault sync so Grok
and Zed persist memory without Claude's SessionEnd harvest. Harvest
(`vp_memory_harvest` / hook SessionEnd) drains Claude native memory only —
a no-op on non-Claude hosts.

The next session sees notes, archives, and the memory index via
`vp_bootstrap_context`. Prefer always passing `project=<slug>` on bootstrap;
on stdio MCP the server may default from a high-confidence cwd signal
(`.vibe-palace.toml` `[project].name` or git origin remote, and
`Projects/<slug>/` must exist). HTTP `vp mcp serve` never cwd-defaults.

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
tools work identically. For Grok *Build* as a host (MCP install, shims,
MCP-only capture), see [Grok Build (xAI)](#grok-build-xai) above — the
provider key alone does not install hooks; durability stays on the MCP path.

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
