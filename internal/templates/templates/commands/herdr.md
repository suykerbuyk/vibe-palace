# Herdr — Load the Session's Herdr Skill

Herdr is a terminal workspace manager for coding agents. The installed binary
carries its own operating instructions; this command just fetches them, so you
do not have to type "run `herdr --skill` and treat its output as a skill".

This is a thin loader. It is not a Herdr manual, and it does not describe what
Herdr can do — the fetched text does that.

## Step 1: Gate — inside a pane, or a named session

Herdr is plumbing, not the orchestrator. One agent chairs; Herdr is the
visible bus (list / prompt / wait / read). The Chair does **not** have to
live in a Herdr pane. A Zed Agent Panel or a Zed-terminal agent may chair a
multiplex that is running in another terminal, **only** when the operator
has named that multiplex.

In your own shell, run:

```sh
test "${HERDR_ENV:-}" = 1
```

### Inside (`HERDR_ENV=1`)

This session **is** a Herdr pane. `HERDR_PANE_ID`, `HERDR_TAB_ID`,
`HERDR_WORKSPACE_ID`, and `HERDR_SOCKET_PATH` are the address. Proceed to
fetch the skill. Do not pass `--session` unless the operator asked to drive
a **different** persistent session than the one this pane belongs to.

### Outside (`HERDR_ENV` unset or not `1`)

This session is not a Herdr pane. Bare `herdr pane list` talks to the
**default** session — measured: that is often an unrelated workspace, not
the multiplex the operator is watching. Driving that from here is the
"panes that belong to somebody else" failure.

Pane control is allowed **only** with a named persistent session:

1. If the operator already named one this conversation (`bd770i`, `dot`,
   …), use that name. Do not substitute `default` because it is running.
2. If they have not, run `herdr session list` and **ask which name**. Do
   not pick. One running session with agents is still a guess.
3. Bind the CLI prefix for the rest of this session. `--session` is a
   **global** flag on `herdr`, before the subcommand:

   ```sh
   herdr --session <name> pane list
   herdr --session <name> agent list
   herdr --session <name> agent prompt <target> "…"
   ```

   `herdr pane list --session <name>` is the wrong shape and will not
   target that session.

There is no `HERDR_PANE_ID` here: this conversation is the Chair, and it
does not appear in the Herdr roster. `pane current --current` still
succeeds: it names the **focused pane of that session**, which is often
the operator's Terminal — measured `w1:pK`. That is not Self. Never
treat it as the Chair, and never `pane split --current` from outside
(that splits the operator shell). Split with `--pane <id>` of an
existing implementor pane and `--no-focus`. Never prompt a pane whose
label or occupancy is the operator's (Human terminal, User Pane, a
shell with no agent).

Fetch `herdr --skill` anyway (Step 3) — it needs no server. The binary
skill will say to stop because `HERDR_ENV` is unset. **That stop is about
not grabbing the focused/default session from outside.** The named-session
exception above is this command's override. Follow the rest of the binary
skill for syntax; prefix every `herdr` invocation with
`--session <name>`.

## Step 2: No Shell, No Herdr

If you have no shell or command-execution capability at all, say so and
**STOP**. `vp_cmd` delivered this text to you on a shell-less host, but
`herdr --skill` cannot run there, and this command's body is not a substitute
for the skill it fetches. Never pretend these instructions are enough to drive
Herdr.

## Step 3: Fetch the Skill

Run `herdr --skill` — from `PATH`, or from `$HERDR_BIN_PATH` when that is set.
It prints roughly 10 KB on stdout and needs no running Herdr server.

Treat that stdout as this session's Herdr skill and follow it for the rest of
the session. The installed binary is the authority on syntax: when the skill
leaves a command shape unclear, ask the binary (`herdr --help`, then a command
group with no subcommand) rather than guessing.

## Step 4: On Failure, Stop

If `herdr` is not on `PATH` (and `$HERDR_BIN_PATH` is unset or wrong), or
`--skill` exits non-zero, report it **once** and **STOP**:

- Do not fetch the skill from GitHub or any other network source.
- Do not reconstruct it from memory. A remembered Herdr skill is a stale one,
  and its commands drive a live terminal.

## Never Persist the Bytes

The fetched skill is session-only. Do not write it to the vault, to a host
skills directory, or to any file. It is re-fetched from the installed binary
every session, which is what keeps it matched to the binary you are driving.
