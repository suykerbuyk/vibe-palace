# Herdr — Load the Session's Herdr Skill

Herdr is a terminal workspace manager for coding agents. The installed binary
carries its own operating instructions; this command just fetches them, so you
do not have to type "run `herdr --skill` and treat its output as a skill".

This is a thin loader. It is not a Herdr manual, and it does not describe what
Herdr can do — the fetched text does that.

## Step 1: Gate on the Herdr Environment

In your own shell, run:

```sh
test "${HERDR_ENV:-}" = 1
```

If that exits non-zero, this session is not running inside a Herdr pane. Say so
plainly and **STOP**. Do not load the skill, and do not fall back to anything —
driving a Herdr session you do not live in means controlling panes that belong
to somebody else.

Only after the gate passes are `HERDR_PANE_ID`, `HERDR_TAB_ID`,
`HERDR_WORKSPACE_ID`, and `HERDR_SOCKET_PATH` meaningful. They are context for
the skill, not the gate — do not substitute one of them for the check above.

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
