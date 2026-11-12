# Herdr mode

Everything in this section applies **only** when Setup resolved to Herdr mode.
Skip it entirely in ephemeral-agent mode — there is no roster, no pane, no
composer box to manage there.

Herdr organizes terminals into workspaces, tabs, and panes, recognizes agents
running inside panes, and exposes the session through the `herdr` CLI. The
installed binary is the authority for exact command syntax — `herdr --help`,
then a bare command group, when a shape is unclear.

## Dynamic discovery

Re-run discovery at seating, after every split / close / `agent start` / pane
recycle, and whenever a target looks stale. Live JSON is the roster. Memory is
not — **never** hardcode implementor names or count from a prior session, a
seeding prompt, or a label you remember. A session-kickoff instruction that
names "Implementor1/2/3" is describing what happened to be true last time, not
a guarantee; discover the live roster regardless of what any prompt claims.

Inside a Herdr pane:

```sh
herdr pane list --workspace "$HERDR_WORKSPACE_ID"
herdr agent list
herdr pane current --current
```

Outside Chair (named session; `--session` before the subcommand):

```sh
herdr --session <name> pane list
herdr --session <name> agent list
herdr session list
```

Do not run `pane current --current` from outside as if it were Self — it
names the focused pane of that session, often the operator Terminal. Do not
`pane split --current` from outside. Do not run bare `herdr pane list` from
outside — that is the default session.

Classify every pane in the targeted session's workspace:

| Class | How you tell | What the Chair may do |
|---|---|---|
| **Self (Chair)** | Inside: `pane_id` equals `$HERDR_PANE_ID`. Outside: this conversation is not in the roster | Drive Herdr from here. Do not prompt yourself. |
| **Implementor** | `agent` is set (claude, grok, …) and it is not this pane | Prompt, wait, read, review. |
| **Operator shell** | no agent, or labels such as Human terminal / User Pane | Never prompt. Never start an agent there. |

Prefer a **pane id** (`w1:p1`); a **unique live agent name** also works. A kind label
(`claude`, `grok`) is not a target. Pane **labels** (`Implementor`) are display
names; they are not agent names. Agent names must match `[a-z][a-z0-9_-]{0,31}`
and be unique among live agents. If a live agent has no unique name (the
`agent` field is only a kind), rename it after discovery —
`herdr agent rename <pane-id> implementor1` — so prompts can target a name.

**The Chair self-rosters — the operator need not hand over pane identities.** Naming the
roster is the Chair's job, not the operator's. On discovery, rename each unnamed subordinate
pane to the convention `<project-prefix>-<role[N]>` (e.g. `vp-imp1`, `vp-imp2`, `vp-adv-grok`),
deriving the prefix and role from the pane's existing label or terminal title where one is
present. A seeding prompt's claimed names are not trusted for this — the live roster is
discovered and named here, then stated once (see "State the seating once"). A pane already
named `<project-prefix>-<role>-<datestamp>` (seated via "Seat the subordinate roster") is
already rostered — leave its name as-is; do not rename it down to `<project-prefix>-<role>`.
So the operator does not have to tell the Chair which pane is which; creating the panes is enough.

`idle` means ready for input and seen in the focused UI. `done` is the same
idle after unseen background work. CLI reads do not mark a pane seen.
`blocked` is an approval or question UI. `unknown` does not prove completion.
A Claude pane finishing a turn goes `working` -> `done` without passing through
`idle`, so a wait must name **both** — see "Waiting on long-running work".

## Visibility — why panes exist

The operator sees Herdr panes, not the Chair's hidden tool tree.

- **Do the unit in an implementor pane** whenever the operator should be able
  to watch it: implementation, lab measurement, nested sub-work that *is* the
  unit.
- **Use the Chair's own tools** for review, git status, vault, staging, wrap,
  Herdr control, and re-deriving an implementor's claim from source.
- **Chair-local sub-work** is allowed only when it is not the unit the
  operator came to watch: read-only investigation, reviewing a plan the
  operator authorized, extracting a remainder from a large task body.
  Findings they return are witness statements — re-derive from source before
  recording or dispatching. Do not implement in them.
- **Do not** satisfy "use subordinates" by doing the implementation unit in
  Chair-local sub-work and summarizing it. That hides the work.
- When dispatching to an implementor, tell them to use *their own* sub-work
  for parallel investigation. If the project has an environment-hazards
  document (a cartridge, a gotchas file), tell them to paste it **verbatim**
  into every downstream prompt they write. A downstream agent that does not
  receive those rules will lie with empty greps.

`second-opinion`'s headless CLI is a **witness**, not a seating. Use it only
when the reviewer model is *different* from the Chair.

## Creating panes and launching agents

Default to a sibling pane in the current tab and the current working
directory. Do not create a workspace, tab, worktree, or different cwd unless
the operator asked for that topology.

**When to spawn:** the operator asked for another implementor or more
parallel capacity; discovery found fewer implementors than the agreed unit
needs and the operator already authorized parallel work; the Chair needs a
*different model* for an independent pass on complex or high-blast-radius work.

**When not to spawn:** the Chair would be implementing "because it would be
faster" (spawn an implementor instead, or ask); a Human terminal / User Pane
is empty and tempting (leave it); the tree is dirty with another pane's unit
(sequence, or bind disjoint paths).

**How:**

1. Inspect geometry (`herdr pane layout --pane <id>`). Split a wide pane
   right, a narrow/tall pane down.
2. `herdr pane split --current --direction <right|down> --cwd "$PWD" --no-focus`
   (outside Chair: `--pane <id>` instead of `--current`).
3. Read the new pane id from the JSON (`.result.pane.pane_id`).
4. Optionally `herdr pane rename <pane-id> ImplementorN` so the operator can
   see it.
5. `herdr agent start <name> --kind <kind> --pane <pane-id>` once the pane is
   at an interactive prompt. Name: unique, `[a-z][a-z0-9_-]{0,31}`. Kind:
   whatever the operator named; otherwise reuse the last successful
   implementor kind this session; if neither applies — a fresh session with
   no prior kind and none named — default to **the Chair's own kind**. If
   the operator asked specifically for a *different* model for independence,
   pick a kind other than the Chair's own instead of defaulting.
   Native args go after `--`, and **which ones you pass is a decision, not a
   fixed list** — see "Inheriting the operator's permission posture" below.
   For `--kind claude`, append `-- --prompt-suggestions false` to suppress
   Claude Code's recap banner and unsent predicted-next-prompt clutter in the
   subordinate's own pane — set at process launch, so it never touches the
   global setting and never affects the Chair's own pane. Operator-confirmed
   over many sessions of manual use; not yet confirmed for any other agent
   kind, so do not extend it to `grok` or others without checking first.
6. Bootstrap it with `herdr agent prompt <name> "…" --wait` (binding
   constraints first; the project's own restart command if it needs full
   context).
7. Rediscover; state the new roster once.

### Seat the subordinate roster

When the operator asks for the standard team in one go — "seat imp1, imp2 and a
grok reviewer", or a role list like `imp1,imp2,review:grok` — do not hand-launch
the panes one at a time. Seat them in a single pass: for EACH role, run the
per-pane steps in the "How:" list above, named at `agent start` so there is no
post-hoc `agent rename`.

Parse each entry as `role[:kind]`. `kind` defaults exactly as in How step 5 (the
operator's named kind, else the last implementor kind this session, else the
Chair's own); `review:grok` pins the kind for that one pane. Detect your own
permission posture **once, up front** by reading your own command line — see
"Inheriting the operator's permission posture" below — not once per role.

**Build each born name before you start the pane, and keep it legal.** The name is
`<project-prefix>-<role>-$(date "+%Y%d%H%M%S")` (prefix e.g. `vp`), passed to the
existing `agent start <NAME>` positional so the pane is born named — no
`agent rename` afterward (see the reconciliation note at the end).
- 🔴 **It must match `[a-z][a-z0-9_-]{0,31}`** (How step 5 and "Dynamic
  discovery") — 32 characters total. A long role overflows: e.g.
  `vp-adverserial-review-grok-<12-digit stamp>` is 39. **Shorten the role**
  (`adv-grok`) or refuse that entry BEFORE `agent start`.
- 🔴 **The stamp is second-resolution, so a fast loop can emit the SAME name
  twice** — the uniqueness violation the name rule forbids. Capture a FRESH stamp
  each iteration AND confirm the name is not already in `herdr agent list` before
  each `agent start`; on a collision, wait for the second to tick (or suffix) and
  re-check.
- `+%Y%d%H%M%S` omits the month (year, day-of-month, hour, minute, second), so the
  stamp repeats on the same day-of-month next month. That is the operator's format
  — do not change it here; it differentiates resume history within a month, which
  is its purpose.

Then, per role, in order:

1. **Re-inspect geometry before every split** (`herdr pane layout`, How step 1) —
   each split changes the layout, so a direction chosen once for the whole list
   makes a row of thin panes. `herdr pane split --current --direction <right|down>
   --cwd "$PWD" --no-focus` — wide pane right, narrow/tall pane down. Outside
   Chair: `--pane <id>`, and prefix every call with `herdr --session <name>`
   exactly as How step 2.
2. Read the new pane id from the JSON (`.result.pane.pane_id`).
3. `herdr agent start <born-name> --kind <kind> --pane <pane-id> [-- <native args>]`.
   - Native args are **kind-conditional, exactly as How step 5 and the posture
     rule**: for `--kind claude`, `-- --prompt-suggestions false`; append
     `--dangerously-skip-permissions` ONLY for `--kind claude` AND only when your
     own command line carries it (the posture self-detection). Pass neither to a
     non-claude kind without checking that kind first.
   - 🔴 **Never pass `--remote-control` to a subordinate.** Per the operator's
     verbatim convention it is the CHAIR launch flag only (the specimen in
     "Inheriting the operator's permission posture" is corrected to match). This is
     a convention match, not a claim about wakers — arm the waker explicitly in
     step 5.
4. Bootstrap it as in How step 6: `herdr agent prompt <born-name> "…" --wait`,
   binding constraints first, then the project's restart command if it needs full
   context.
5. **Arm a waker for this subordinate** (see "Learning that a subordinate
   finished", whose limits — same-machine, main-conversation-only, one-shot, and
   a measured fire lifetime — all still apply here), kind-conditionally:
   - **claude:** the pane appears in ListAgents by its born name, because
     `agent start <born-name>` passes `--name <born-name>` through to claude
     (ps-confirmed on seated agent-start panes). The born name is therefore ONE
     identity across both subsystems: the same string targets a `notify_when_idle`
     subscription (harness) AND `herdr agent wait <born-name>` (herdr), with no
     pane-to-session resolution step. `notify_when_idle` fires end to end even on a
     pane carrying no `--remote-control` — measured 2026-10-24 (task
     `measure-notify-when-idle-on-no-remote-control-subordinate`) on a RAW `--name`
     session (`vp-imp1`), NOT on an agent-start pane; it is inferred to cover an
     agent-start pane only because that path sets the same `--name` identity, and a
     self-owned agent-start fire is a trivial future re-confirm. Use it as the
     default waker, with a backgrounded
     `herdr agent wait <born-name> --until done --until idle` as the equivalent
     alternative.
   - **grok:** the pane does NOT appear in ListAgents (it is not a Claude session),
     so `notify_when_idle` cannot target it — a backgrounded
     `herdr agent wait <born-name> --until done --until idle` (it names the herdr
     pane) is mandatory. For any OTHER non-claude kind, do not assume ListAgents
     visibility: check that kind before using `notify_when_idle`, and otherwise use
     `herdr agent wait`.
   On posture: a Chair-started claude pane is permission-matched only when step 3
   passed the Chair's own posture flag. On the one inherited-posture trial
   (2026-10-24) the idle notice was delivered to the Chair; the mismatched-mode
   withhold case is still untested (see "Learning that a subordinate finished", the
   permission-mode limit). If this pane's mode is not the Chair's — the operator
   chose not to inherit, or it is a pane this Chair did not start — also arm
   `herdr agent wait <born-name> --until done --until idle`.

After the loop, **rediscover the live roster and state it once** — never treat the
role list you were handed as the resulting roster (see "Dynamic discovery" and
"The Chair self-rosters"). A pane this block already named
`<prefix>-<role>-<datestamp>` is already rostered: leave its name as-is — do NOT
rename it down to `<prefix>-<role>` (that would drop its resume-history stamp and
can collide with a live `<prefix>-<role>`). If posture detection failed, **ask
before seating any pane** — do not guess in either direction (posture section).

### Inheriting the operator's permission posture

🔴 **A pane the Chair starts does not inherit the operator's permission mode.
It comes up in the default one.** The subordinate's own gate then denies the
work the Chair just authorized — and the Chair's authorization cannot clear it,
because a permission gate evaluates tool calls against *settings* and never
reads the conversation the authorization happened in. An auto-mode classifier
does the deciding, so the denials are inconsistent rather than uniform, which
is why this surfaces **mid-unit rather than at seating** — measured in this
workspace: a real unit stalled three rounds in.

**How to tell a pane apart:** `interactive_ready: true` appears in
`herdr agent list` **only** on panes a Chair started this way; it is absent on
panes Herdr merely detected running a shell-launched agent. That field is the
discriminator for which launch path produced a pane, not a readiness signal to
wait on.

**Detect your own posture by reading your own command line** — not a sibling's,
and not by asking Herdr. Walk up from a shell you spawn until you find the
agent process:

```sh
pid=$PPID; hop=0
while [ "$hop" -lt 8 ]; do
  args=$(ps -o args= -p "$pid" 2>/dev/null) || break
  [ -n "$args" ] || break
  case "$args" in *claude*) printf '%s\n' "$args"; break;; esac
  pid=$(ps -o ppid= -p "$pid" 2>/dev/null | tr -d ' ')
  [ -n "$pid" ] && [ "$pid" -gt 1 ] 2>/dev/null || break
  hop=$((hop+1))
done
```

Two details are load-bearing. **Bound the loop; do not hardcode `$PPID`** — the
agent is often the direct parent, but a wrapper can sit between. **Use
`ps -o args=`, not `/proc/<pid>/cmdline`** — `/proc` is Linux-only and `ps`
works on macOS too.

**The rule is not "always pass the flag."** Flags expressing the operator's
**standing posture for the session** inherit to subordinates; flags that are
**per-pane ergonomics** do not. Two specimens from one real session make the
line concrete:

| | Command line |
|---|---|
| **Chair** | `claude --dangerously-skip-permissions --remote-control vp-chair --name vp-chair-…` |
| **Subordinate** | `claude --dangerously-skip-permissions --name vp-imp1-… --prompt-suggestions=false` |

The operator never passes `--prompt-suggestions` to a Chair and always passes
it to a subordinate, so it is ergonomics and does not inherit. They pass
`--dangerously-skip-permissions` to both, so it is posture and does.

`--remote-control` is the operator's chair-launch flag; per the operator's
convention it is not passed to a subordinate. That is a convention match, not a
statement about wakers: see "Seat the subordinate roster" step 5 for how a seated
subordinate's completion is actually watched.

**Kind-conditional, exactly like `--prompt-suggestions` above:
`--dangerously-skip-permissions` is CLAUDE CODE ONLY.** Pass it only for
`--kind claude`, and only when your own command line carries it. Do not write
or infer an equivalent for `grok` or any other kind without checking that kind
first.

**When detection fails, ask — do not guess in either direction.** An outside
Chair, a Zed panel, or any Chair that is not a readable agent process cannot
inspect its own args. Say so and ask the operator which posture to launch
with. Guessing bypass-on makes a safety decision by inference; guessing
bypass-off recreates the defect above.

## Canonical dispatch recipe

This is a Herdr-mode DISPATCH convenience — how to land a prompt on a pane and wait for that
one dispatch to settle. It does NOT change the default completion waker: the `notify_when_idle`
subscription remains the default (for a **claude** pane; a grok pane is not a `notify_when_idle`
target — see "Seat the subordinate roster" step 5) (see "Learning that a subordinate finished" and
the Herdr-mode preference in "Waiting on long-running work" below). Reach here when you want to land-and-wait
on a single pane dispatch from inside the harness.

Dispatch (in-pane Chair) — background this call; its settle wakes you:

    herdr agent prompt <name> "$(cat <brief-file>)" --wait --timeout <ms>

Outside Chair driving a NAMED session — prefix every call with `herdr --session <name>`:

    herdr --session <name> agent prompt <name> "$(cat <brief-file>)" --wait --timeout <ms>

A `--session`-less recipe drives the DEFAULT session — a listed failure mode. An outside Chair
MUST use the prefix and must never run this against a bare/default session.

Same measured limits as any backgrounded wait: the longest PROVEN is 20.46 minutes, one task at
a time; several concurrent long waits, and a 40-minute dispatch, are UNPROVEN (see "Learning
that a subordinate finished"). Do not quote the survivals as a ceiling the mechanism is known to
clear, and do not background many in parallel as if proven.

FALLBACK (pane-status predicate, or when the above is unavailable):

    herdr agent get <name>            # confirm agent_status=working → the prompt landed
    herdr agent wait <name> --until done --until idle --timeout <ms>   # background it

A short confirming `--wait` returns `{"error":"timeout"}` exit 1 for real work — EXPECTED, it
only means work outlived the short window; `get` then confirms it landed. `--until done --until
idle` names BOTH statuses, always (idle alone never matches a finished Claude pane — see
"Waiting on long-running work").

Two hazards:

- **`--wait` / `--until done` return on the FIRST settled state.** Some kinds (Grok observed)
  transiently settle to `done` mid-work, so a bare first-`done` can fire early. Prefer a
  STABILITY wait where it matters — `done`/`idle` AND `state_change_seq` unchanged across a
  short window. Kind-dependent (not reproduced on 3 Grok runs with `--until done --until idle`);
  treat as safe hardening, not a universal bug.
- **A prompt to a BUSY pane QUEUES in the composer; it does not interrupt.** To redirect a
  runaway pane, kill/break what it is blocked on FIRST, then prompt.

## Waiting on long-running work

**Prefer the idle subscription** described in "Learning that a subordinate
finished" above: it costs the target nothing, ties up no backgrounded process,
and **a Herdr pane is precisely the target it was measured against** (the
**claude** case — a grok Herdr pane was measured ABSENT from ListAgents, so it is
not a `notify_when_idle` target, see "Seat the subordinate roster" step 5). Reach for
`herdr agent wait` when you specifically want a pane-status predicate rather
than "that session went idle."

### `--until idle` alone never matches a Claude pane

**A Claude Code agent pane does not enter herdr's `idle` status on finishing a
turn. It transitions `working` -> `done`.** Measured on two panes at a
1-second poll, with no `idle` sample anywhere across either transition.

So `herdr agent wait <target> --until idle` never matches a pane that has
merely finished. It runs to its `--timeout` and returns
`{"error":{"code":"timeout","message":"timed out waiting for agent status"}}`
— demonstrated against a pane that had been finished for roughly three
minutes.

**The correct invocation is `--until done --until idle`.** The flag is
repeatable, and **both statuses are required**: `done` is what a finished turn
produces, and `idle` is what live panes settle into afterwards. A single census
showed five panes reading `idle` and three reading `done` at the same moment,
so neither value alone covers the population.

🔴 **The failure is silent in the direction that reads as success.** The wait
process **exits 0** — the JSON error body goes to stdout, not the exit code. A
waker built on `--until idle` therefore never fires, wakes nobody, and reports
no error to anything checking `$?`. A Chair would conclude that push does not
work and fall back to polling, with every available signal agreeing. Same shape
as `gofmt -l` and the deprecated three-arg `git merge-tree`: a reporter
mistaken for a gate.

### If a backgrounded wait does die

Backgrounding a wait is not the fragile move this section used to describe.
Three background tasks ran 15.0, 20.46 and 20.45 minutes without being reaped;
the measurement, and the limits it does not reach, are in "Learning that a
subordinate finished" above. **The former advice to prefer ever-shorter windows
is withdrawn** — it reasoned from a ceiling the measurement did not find, and
shortening a window only multiplies the number of wakes. What is still unproven
is several concurrent long waits, or a 40-minute one; treat those as untested
rather than safe.

A backgrounded wait can still be lost. When one is:

1. Check the pane's actual status first (`herdr agent get <name>`) rather than
   assuming the prompt failed — if it shows `working`, the dispatch landed fine
   and only the wait was interrupted.
2. Re-issue a plain wait (not a re-prompt), to avoid risking duplicate
   instructions landing on the same pane.
3. Re-arm at the same length, or switch to an idle subscription. Do not climb a
   ladder of shorter and shorter windows.

## Clearing subordinate context

Implementor panes are context-expendable **in general** — clear or restart
them once a unit is recorded to the vault, rather than letting context balloon
"just in case." But weigh this against the actual shape of the work: a pane
that is the single continuous thread through one long, evolving investigation
benefits from staying loaded — restarting it mid-thread costs real
understanding that the vault task record alone doesn't replace while the
investigation is still active. Clear a pane when it's switching to an
unrelated objective; let it run long when it's the one deep in a single
still-evolving thread, and rely on the vault task as the safety net either
way.

