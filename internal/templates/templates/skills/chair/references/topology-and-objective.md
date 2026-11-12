### 2. Detect subordinate topology

Check, in order:

1. **`test "${HERDR_ENV:-}" = 1`** — this process is itself inside a
   Herdr-managed pane. → **Herdr mode**, seated as an in-pane Chair. Self in
   the roster is `$HERDR_PANE_ID`.
2. **Not inside a pane, but the operator has named a persistent Herdr session**
   this conversation (e.g. "drive the `bd770i` session"), and `herdr` is on
   `PATH` or `$HERDR_BIN_PATH` — → **Herdr mode**, seated as an outside Chair
   (a Zed Agent Panel, a plain terminal, or another host) driving that named
   session with `herdr --session <name> ...` prefixed on every call.
3. **Neither signal present** — no pane, no named session, `herdr` absent or
   simply never invoked — → **Ephemeral-agent mode**. This is the normal case
   for a long-running Chair process outside Herdr (a tmux pane, a plain
   terminal session): dispatch and manage subordinates with the Agent tool
   directly. Do not go looking for Herdr, do not ask the operator to set it up
   unless they asked for panes specifically.

Only when topology resolves to Herdr mode do you load `herdr --skill`: call
`vp_cmd` with `name="herdr"` and follow its instructions (which is itself the
gate described above — inside a pane or a named session, never a bare/default
session driven from outside). **Do not load Herdr, and do not treat it as a
dependency, in ephemeral-agent mode.** A Chair running in a tmux pane, no
Herdr in sight, is a fully valid and common way to run this skill — it needs
none of Herdr's mechanics.

### 3. Which objective this session is chairing

The task, epic, or objective to drive is a **per-invocation parameter, never
skill content** — supply it as an argument (`/vps-chair <task-slug>`) or state
it in your first message. If none is given, default to the project's own
`head_of_queue` (from `vp_bootstrap_context`) and say so, rather than asking —
the operator can redirect if that guess is wrong.

**A chair session is kicked off by a single command: `/vps-chair <objective>`.** The skill
self-bootstraps — per Setup §1, if bootstrap/restart has not run this session the Chair's first
action is `vp_cmd name="restart"`, so a separate `/vpc-restart` is not needed. Do NOT hand the
Chair a roster or paste the review loop into a kickoff prompt: the Chair discovers and names the
live roster itself (see "Dynamic discovery") and the loop is skill content, so a multi-paragraph
seeding prompt is unnecessary. `<objective>` is the ordinary parameter of this section — absent,
the Chair defaults to `head_of_queue` and says so.

