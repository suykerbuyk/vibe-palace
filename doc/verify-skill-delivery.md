# Manual Verification: Skill Delivery Across IDEs

This is a step-by-step script that proves vibe-palace delivers the
`vps-startup-analyst` skill end-to-end across Claude Code, Cursor, and
at least one Zed-based surface. Run it after any change to the skill
shim pipeline, the managed-block template, the resolver, or `vp init` —
and record the outcome in the **Recorded results** table at the bottom.

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
3. Run `vp init` in the project. Confirm the status table reports:
   - `[pass] Project config` row
   - `[pass] Agent wiring` (one or more of CLAUDE.md / AGENTS.md /
     `.cursorrules` / `.rules` / `.github/copilot-instructions.md`)
   - `[pass]` rows for any shim surfaces the project opts into
     (`.claude/commands/`, `.claude/skills/`, `.cursor/rules/`)
4. MCP wiring per IDE — see `doc/TUTORIAL.md` Part 3 for
   editor-by-editor config snippets (Claude Code, Cursor, Zed, Vim,
   Open Code). There is no separate `MCP-SETUP.md`; Part 3 is the
   canonical source.
5. Confirm the skill exists on disk after `vp init`:
   ```bash
   ls .claude/skills/vps-startup-analyst/SKILL.md          # Claude Code
   ls .cursor/rules/vps-startup-analyst.mdc                # only if .cursor/ is opted-in
   ```
   If either is missing when expected, stop and investigate before
   continuing — the managed-block / MCP paths all assume the on-disk
   shim is present and non-stale.

---

## Verification matrix

Fill **Last verified / Model / Result** in the Recorded results section
at the bottom, not here. This table is the running summary of what's
been proven.

| IDE                          | Delivery mechanism                                   | Trigger                   | Last verified | Model / version | Result |
|------------------------------|------------------------------------------------------|---------------------------|---------------|-----------------|--------|
| Claude Code                  | `.claude/skills/vps-startup-analyst/SKILL.md` (native SKILL.md primitive) | `vps-startup-analyst`     |               |                 |        |
| Cursor                       | `.cursor/rules/vps-startup-analyst.mdc` (Rules picker) | rule pick or `vps-startup-analyst` | |          |                 |        |
| Zed + Claude (MCP)           | Managed-block trigger → `vp_skill` MCP call          | `vps-startup-analyst`     |               |                 |        |
| Zed + Gemini or Copilot Chat | Managed-block trigger + user-paste fallback          | `vps-startup-analyst`     |               |                 |        |

---

## 1. Claude Code — native SKILL.md primitive

**Invoke it this way:**

1. Open the project directory in Claude Code.
2. Start a fresh session.
3. As the first user message, type:
   ```
   vps-startup-analyst
   ```

**Expected response shape:**

- Claude acknowledges the persona by name ("I'm acting as the startup
  analyst…" or "Startup Analyst persona adopted…").
- Claude lists the persona's objectives as declared in
  `.claude/skills/vps-startup-analyst/SKILL.md` (viability lens,
  adversarial review, capex/opex split, partnerships, etc.).
- Claude names at least one of the reference files available for
  on-demand fetch: `capex-opex`, `competitive-landscape`,
  `funding-sources`, `reality-validation`, `strategic-partnerships`.
- Claude stays in that posture for the remainder of the session
  (second and third user turns continue to feel like the analyst).

**Common failure modes:**

- `SKILL.md` missing → run `vp init` again; confirm the project's
  `.claude/skills/` is not gitignored away.
- Frontmatter `description:` field empty → Claude Code's skill
  picker won't surface the skill; re-render via
  `vp skills upgrade --only startup-analyst`.
- `sha=` marker in the shim doesn't match the current template —
  stale shim. `vp skills upgrade` will flag it; accept the rewrite.
- Claude ignores the trigger and answers as a generic assistant →
  the managed block in `CLAUDE.md` / `AGENTS.md` was not loaded
  (Claude Code only reads it after turn 1; use `/vpc-restart` as
  turn 1 to force bootstrap, then retry `vps-startup-analyst`).
- `vp_skill` tool not registered (MCP list shows 0 tools) → see
  TUTORIAL Part 3, "Claude Code"; `.mcp.json` / `~/.claude.json`
  not pointing at `vp mcp`.

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
   description matches `SKILL.md` frontmatter.
4. Either select the rule to attach it to the current chat, or type
   `vps-startup-analyst` in the chat to trigger via the managed
   block.

**Expected response shape:**

- Rules picker shows `vps-startup-analyst` with the description
  from the SKILL.md frontmatter.
- Selecting / invoking the rule adopts the persona as in §1.
- If MCP is wired, `vp_skill` is called and the full persona frame
  arrives. If MCP is not wired, the `.mdc` body alone carries
  enough guidance to adopt the persona (with a vault-path fallback
  hint for fetching references).

**Common failure modes:**

- `.cursor/rules/` does not exist → shim not rendered (`vp init`
  reports `[skip] Cursor shims: .cursor/ not present`). Create the
  directory and re-run `vp init`.
- Stale `sha=` in the shim → `vp skills upgrade` will re-render.
- MCP not configured in Cursor → rule body still has the skill
  description and trigger instructions; persona should still
  adopt, but reference fetches will fail (user must paste).
- Rule shows up but doesn't engage → confirm the managed block is
  present in `.cursorrules` and/or `.rules`; Cursor reads these in
  addition to the `.cursor/rules/` picker.

---

## 3. Zed + Claude (MCP) — trigger-phrase path

**Prerequisites:**

- Zed configured with Claude as the provider.
- `vp` registered as a Zed `context_servers` entry per TUTORIAL
  Part 3, "Zed":
  ```json
  {
    "context_servers": {
      "vibe-palace": { "command": { "path": "vp", "args": ["mcp"] } }
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
  config wrong or `vp` not on `PATH` (Zed resolves `command.path`
  through `$PATH`).
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
model *aware* that `vps-<name>` means something, and a paste of
SKILL.md finishes the activation.

**Invoke it this way (Zed + Gemini variant):**

1. Open the project in Zed with Gemini as the provider.
2. In chat, type:
   ```
   vps-startup-analyst
   ```
3. If the model says it can't find the skill or can't call a tool,
   paste the body of `.claude/skills/vps-startup-analyst/SKILL.md`
   into the chat and ask the model to adopt the persona.

**Invoke it this way (Copilot Chat variant):**

1. Open the project in the Copilot Chat surface (VS Code, JetBrains,
   etc.).
2. Type `vps-startup-analyst`.
3. Paste `SKILL.md` contents if the model doesn't adopt on the
   trigger alone.

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
  providers without standing-instruction context. Paste SKILL.md
  again if persistence is needed.

---

## Recorded results

One row per verification run. Copy the template block, fill it in
after running, and commit the update. Keep historical rows — the
record is the audit trail.

### Template

```
- IDE:              <Claude Code | Cursor | Zed+Claude | Zed+Gemini | Copilot>
  Date:             YYYY-MM-DD
  Model / version:  <e.g. claude-opus-4-6 / Claude Code 1.12.0>
  Result:           <PASS | FAIL>
  Notes:            <blockers, workarounds, transient vs persistent>
```

### Runs

- IDE:              Claude Code
  Date:
  Model / version:
  Result:
  Notes:

- IDE:              Cursor
  Date:
  Model / version:
  Result:
  Notes:

- IDE:              Zed + Claude (MCP)
  Date:
  Model / version:
  Result:
  Notes:

- IDE:              Zed + Gemini (or Copilot Chat)
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
