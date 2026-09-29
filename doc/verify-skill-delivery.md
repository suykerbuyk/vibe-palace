# Manual Verification: Skill Delivery Across IDEs

This is a step-by-step script that proves vibe-palace delivers the
`vps-startup-analyst` skill end-to-end across Claude Code, Cursor,
Grok Build, and at least one Zed-based surface. Run it after any change to the skill
shim pipeline, the managed-block template, the resolver, or `vp init` —
and record the outcome in the **Recorded results** section at the bottom.
Results are recorded per host, per run.

Persistent failures (reproducible across attempts, against a current
model) block retirement of the feature under test. Transient failures
(model outage, network, IDE-version regressions outside our code) are
logged with repro notes but do not block.

---

## Prerequisites

Before running any section:

1. `vp` is built and on `PATH`:
   ```bash
   make install
   which vp            # → ~/.local/bin/vp (or wherever PREFIX points)
   vp version
   ```
2. Pick a real project directory (not `$HOME`, not a scratch dir). The
   directory must be a detectable project — has `.git/`, a manifest
   (`go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`, `pom.xml`),
   or an existing `.vibe-palace.toml`.
3. Register MCP with each host under test. The primary route is
   `vp mcp install --claude-plugin`, `--grok` and/or `--zed`
   ([One-command registration](TUTORIAL.md#one-command-registration-recommended)).
   `doc/TUTORIAL.md` Part 3 has the per-host sections (Claude Code,
   agent-file wiring, Zed, Grok Build, Grok web, Vim / Neovim, Open Code);
   it has no Cursor section, and vp registers no MCP server with Cursor.
4. Run `vp init` in the project. Confirm the status table reports:
   - `[pass] Project config` row
   - `[pass] Agent wiring` (one or more of CLAUDE.md / AGENTS.md /
     `.cursorrules` / `.rules` / `.github/copilot-instructions.md`)
   - a `Slash-command shims (project)` row for the project shims it wrote,
     and/or `[info] Slash-command shims (Claude)` / `(Grok)` —
     `skipped — user-global … surface healthy` for a host whose
     user-global plugin already carries the shims
5. Confirm the skill shim exists on disk. Where it lives depends on
   step 4:
   ```bash
   # Claude plugin host (the Claude row said "skipped"): no project
   # .claude/skills shim is written; the plugin cache carries it.
   ls ~/.claude/plugins/cache/vibe-palace-local/vibe-palace/*/skills/vps-startup-analyst/SKILL.md
   # Project-shim host (no user-global Claude plugin):
   ls .claude/skills/vps-startup-analyst/SKILL.md
   # Grok project shims (Grok present, no user-global Grok plugin):
   ls .grok/skills/vps-startup-analyst/SKILL.md
   # or, with the Grok user plugin:
   ls ~/.grok/plugins/vibe-palace/skills/vps-startup-analyst/SKILL.md
   # Cursor, only if .cursor/ exists:
   ls .cursor/rules/vps-startup-analyst.mdc
   ```
   If the expected one is missing, stop and investigate before
   continuing — the managed-block / MCP paths all assume the on-disk
   shim is present and non-stale.

---

## Verification matrix

Fill **Last verified / Model / Result** in the Recorded results section
at the bottom, not here. This table is the running summary of what's
been proven.

| IDE                          | Delivery mechanism                                   | Trigger                   | Last verified | Model / version | Result |
|------------------------------|------------------------------------------------------|---------------------------|---------------|-----------------|--------|
| Claude Code, plugin          | Plugin-cache `skills/vps-startup-analyst/SKILL.md` (native SKILL.md primitive) | `/vibe-palace:vps-startup-analyst` or `vps-startup-analyst` | |  |        |
| Claude Code, project shims   | `.claude/skills/vps-startup-analyst/SKILL.md` (native SKILL.md primitive) | `/vps-startup-analyst` or `vps-startup-analyst` | | |        |
| Cursor                       | `.cursor/rules/vps-startup-analyst.mdc` (Rules picker) | rule pick or `vps-startup-analyst` | |          |                 |        |
| Zed + Claude (MCP)           | Managed-block trigger → `vp_skill` MCP call          | `vps-startup-analyst`     |               |                 |        |
| Zed + Gemini or Copilot Chat | Managed-block trigger + user-paste fallback          | `vps-startup-analyst`     |               |                 |        |
| Grok Build                   | `.grok/skills/vps-startup-analyst/SKILL.md` or the `~/.grok/plugins/vibe-palace` user plugin | `/vps-startup-analyst` (possibly namespaced under the user plugin) or `vps-startup-analyst` | | |        |

---

## 1. Claude Code — native SKILL.md primitive

**Invoke it this way:**

1. Open the project directory in Claude Code.
2. Start a fresh session.
3. As the first user message, type:
   ```
   vps-startup-analyst
   ```
   (`/vps-startup-analyst` works too; on a plugin host the slash form
   is typically the namespaced `/vibe-palace:vps-startup-analyst`.
   Claude never adopts the persona
   unasked: the shim sets `disable-model-invocation: true`, so the
   skill is absent from the model's skill listing.)

**Expected response shape:**

- Claude acknowledges the persona by name ("I'm acting as the startup
  analyst…" or "Startup Analyst persona adopted…").
- Claude lists the persona's objectives as returned by `vp_skill`
  (the shim itself is only a delegation) (viability lens,
  adversarial review, capex/opex split, partnerships, etc.).
- Claude names at least one of the reference files available for
  on-demand fetch: `capex-opex`, `competitive-landscape`,
  `funding-sources`, `reality-validation`, `strategic-partnerships`.
- Claude stays in that posture for the remainder of the session
  (second and third user turns continue to feel like the analyst).

**Common failure modes:**

- `SKILL.md` missing → on a plugin host, re-run
  `vp mcp install --claude-plugin` (it refreshes the cache copy) and
  restart Claude Code; on a project-shim host, run `vp init` again and
  confirm the project's `.claude/skills/` is not gitignored away.
- Frontmatter lacks the `Vibe-palace skill — …` label or
  `disable-model-invocation: true` → the shim predates the label
  change; re-render it via `vp commands upgrade` (and
  `vp mcp install --claude-plugin` for the plugin copy). Skill shims are rendered by its
  `PlanSkills`/`ApplySkills` half — `vp skills upgrade` only reports
  vault `Templates/skills/` overrides and never touches a shim.
  Resetting a vault skill override is `vp skills reset NAME`.
- `sha=` marker in the shim doesn't match the current template —
  stale shim. `vp commands upgrade` will flag it; accept the rewrite.
- Claude ignores the trigger and answers as a generic assistant →
  the managed block in `CLAUDE.md` / `AGENTS.md` was not loaded
  (Claude Code only reads it after turn 1; use `/vpc-restart` as
  turn 1 to force bootstrap, then retry `vps-startup-analyst`).
- `vp_skill` tool not registered (MCP list shows 0 tools) → re-run
  `vp mcp install --claude-plugin` and restart Claude Code; for a manual
  registration see TUTORIAL Part 3, "Claude Code" (`.mcp.json` /
  `~/.claude.json` not pointing at `vp mcp`).

---

## 2. Cursor — native rule file

**Prerequisite check:** `vp init` only emits `.cursor/rules/vps-*.mdc`
when the project already has `.cursor/` or `.cursor/rules/`. If
neither exists, create `.cursor/rules/` (empty) and re-run `vp init`.

**Invoke it this way:**

1. Open the project in Cursor.
2. Open the Rules panel (Cmd/Ctrl-Shift-P → "Rules" or the sidebar
   rules picker).
3. Confirm `vps-startup-analyst` appears in the list and its
   description is the one-line `Vibe-palace skill — …` label derived
   from the `SKILL.md` description.
4. Either select the rule to attach it to the current chat, or type
   `vps-startup-analyst` in the chat to trigger via the managed
   block.

**Expected response shape:**

- Rules picker shows `vps-startup-analyst` with its
  `Vibe-palace skill — …` label.
- Selecting / invoking the rule adopts the persona as in §1.
- If MCP is wired, `vp_skill` is called and the full persona frame
  arrives. If MCP is not wired, the rule's fallback tells the agent
  to run `vp skills show startup-analyst` from the project directory
  and adopt the printed persona. Run from the project directory, that
  command serves the same tier `vp_skill` would (project override,
  then vault override, then the built-in), and it prints each
  reference with `--section <ref>`. If the agent cannot run `vp`, it
  asks the user to run the command and paste the output.

**Common failure modes:**

- `.cursor/` does not exist → no rule is rendered: `vp init` has no
  Cursor row, and its `Slash-command shims (project)` row counts no
  Cursor rule. Create the directory and re-run `vp init`.
- Stale `sha=` in the shim → `vp commands upgrade` will re-render.
- MCP not configured in Cursor → the persona is adopted through the
  CLI fallback (`vp skills show <name>`), and reference fetches work
  through `vp skills show <name> --section <ref>`. If `vp` is not on
  the agent's `PATH` (a remote or devcontainer workspace), the agent
  asks the user to run the command and paste its output.
- Rule shows up but doesn't engage → confirm the managed block is
  present in `AGENTS.md`, `.cursorrules` and/or `.rules`; Cursor reads
  these in addition to the `.cursor/rules/` picker.

---

## 3. Zed + Claude (MCP) — trigger-phrase path

**Prerequisites:**

- Zed with Claude in the pane under test: a Claude-shaped ACP agent
  (e.g. `claude-acp`) in the agent panel, or the native pane with Claude
  as the provider. Record which pane in the results.
- `vp` registered as a Zed `context_servers` entry. `vp mcp install --zed`
  writes it ([TUTORIAL: Zed](TUTORIAL.md#zed)):
  ```json
  {
    "context_servers": {
      "vibe-palace": { "command": "vp", "args": ["mcp"] }
    }
  }
  ```
- One of the agent files (`.rules`, `AGENTS.md`, `CLAUDE.md`)
  contains the current vibe-palace managed block.

**Invoke it this way:**

1. Open the project in Zed.
2. Open a new chat with Claude as the provider.
3. Type:
   ```
   vps-startup-analyst
   ```

**Expected response shape:**

- The model recognizes `vps-<name>` as a trigger (instruction lives
  in the managed block the agent file was seeded with).
- The model calls the `vp_skill` MCP tool with `name=startup-analyst`.
- The returned persona frame is adopted for the rest of the session
  (same shape as §1).
- Reference names are enumerated; fetching a reference via
  `vp_get_skill_section` works when the conversation asks about it.

**Common failure modes:**

- `vp_skill` not present in Zed's MCP tool list → `context_servers`
  config wrong or `vp` not on `PATH` (Zed resolves `command`
  through `$PATH`). Re-run `vp mcp install --zed`.
- Model ignores `vps-<name>` → agent file missing the managed
  block, or the block's `sha=` is stale. Run `vp init` or
  `vp commands upgrade` (the latter re-wires agent files too).
- Persona adopted for one turn then forgotten → the model is
  treating `vp_skill` output as a one-shot command; re-read the
  managed-block lifetime contract in TUTORIAL Part 6 ("Skills:
  personas that span a whole session") and confirm the model
  version used understands standing-instruction framing.

---

## 4. Zed + Gemini or Copilot Chat — user-paste fallback

This section covers any AI-coding surface where MCP may not be wired
up. The contract is weaker: the managed-block trigger makes the
model *aware* that `vps-<name>` means something, and a paste of the
persona finishes the activation. The persona is what
`vp skills show <name>` prints from the project directory — not the
`.claude/skills/vps-<name>/SKILL.md` file, which is only a short
delegation to `vp_skill`.

**Invoke it this way (Zed + Gemini variant):**

1. Open the project in Zed with Gemini as the provider.
2. In chat, type:
   ```
   vps-startup-analyst
   ```
3. If the model says it can't find the skill or can't call a tool,
   run `vp skills show startup-analyst` in the project directory,
   paste its output into the chat and ask the model to adopt the
   persona.

**Invoke it this way (Copilot Chat variant):**

1. Open the project in the Copilot Chat surface (VS Code, JetBrains,
   etc.).
2. Type `vps-startup-analyst`.
3. Paste the output of `vp skills show startup-analyst` (run in the
   project directory) if the model doesn't adopt on the trigger
   alone.

**Expected response shape:**

- At minimum: the model acknowledges the `vps-<name>` trigger ("You
  want me to adopt the startup-analyst skill…") even before any
  paste — that's the managed-block awareness working.
- After paste: persona is adopted, objectives are listed, the
  conversation proceeds in-character.
- Reference fetches are out of scope — the user will paste
  reference content if a sub-topic comes up.

**Common failure modes:**

- Model does not recognize the trigger at all → the agent file in
  the project does not contain the vibe-palace managed block.
  Depending on the provider, `.rules` (Zed) / `.github/copilot-
  instructions.md` (Copilot) / `CLAUDE.md` (general) must exist and
  carry the block.
- Model refuses to adopt a persona from pasted text → provider-
  level safety policy; document the refusal and move on (this is
  not a vibe-palace defect).
- Model adopts the persona for one turn only → expected on
  providers without standing-instruction context. Paste the
  `vp skills show startup-analyst` output again if persistence is
  needed.

---

## 5. Grok Build — skill shim

**Invoke it this way:**

1. Open the project in Grok Build after `vp mcp install --grok` (or
   with project `.grok/` shims from `vp init`; see Prerequisite 5).
2. Start a fresh session and type `vps-startup-analyst` (or
   `/vps-startup-analyst`, possibly namespaced under the user plugin).

**Expected response shape:** as in §1. The Grok shim carries the
`Vibe-palace skill — …` label but no `disable-model-invocation` key
(Grok is not known to honour it), so the label is the only guard
against unasked adoption.

**Common failure modes:**

- Shim missing → re-run `vp mcp install --grok` (user plugin) or
  `vp commands upgrade` (project shims).
- MCP not wired → the shim's fallback runs
  `vp skills show startup-analyst`, as in §2.

---

## Recorded results

One row per verification run. Copy the template block, fill it in
after running, and commit the update. Keep historical rows — the
record is the audit trail.

### Template

```
- IDE:              <Claude Code plugin | Claude Code project shims | Cursor | Grok Build | Zed+Claude (ACP or native pane) | Zed+Gemini | Copilot>
  Date:             YYYY-MM-DD
  Model / version:  <model id / host version>
  Result:           <PASS | FAIL>
  Notes:            <blockers, workarounds, transient vs persistent>
```

### Runs

- IDE:              Claude Code plugin
  Date:
  Model / version:
  Result:
  Notes:

- IDE:              Claude Code project shims
  Date:
  Model / version:
  Result:
  Notes:

- IDE:              Cursor
  Date:
  Model / version:
  Result:
  Notes:

- IDE:              Zed + Claude (ACP or native pane)
  Date:
  Model / version:
  Result:
  Notes:

- IDE:              Zed + Gemini (or Copilot Chat)
  Date:
  Model / version:
  Result:
  Notes:

- IDE:              Grok Build
  Date:
  Model / version:
  Result:
  Notes:

### Failure classification

- **Persistent:** reproducible across ≥2 attempts, same IDE + model
  version, no obvious environmental cause. Blocks retirement of the
  feature under test — open an issue, link the recorded-run row.
- **Transient:** one-off (model outage, network, IDE-version
  regression outside vibe-palace's code). Record with repro notes;
  re-run next cycle. Does not block.
