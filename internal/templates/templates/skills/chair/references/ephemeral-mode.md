# Ephemeral-agent mode

Everything in this section applies when Setup resolved to ephemeral-agent
mode — a long-running Chair process (tmux, a plain terminal, anywhere without
Herdr) dispatching subordinates directly through the Agent tool, with no
persistent pane, no roster, and nothing visible to the operator except what
the Chair reports.

## Dispatch

Use the Agent tool directly. A `fork` inherits the Chair's own conversation
context and is the right choice for research or verification work that
doesn't need a fresh perspective; any other `subagent_type` starts with zero
context and needs a fully self-contained prompt — brief it like a colleague
who just walked in, including the project's environment-hazards material if
one exists, pasted in rather than referenced.

There is no persistent name to assign and no roster to discover — each
dispatch is its own call, and "the roster" is just whatever is currently
in flight. Track units by what you dispatched them for, not by an identity
that persists across turns.

## No composer, no `/clear`, no pane state

None of Herdr mode's pane-management concerns apply here:

- **No composer drafts to guard against** — a subagent's response is its
  final word for that call; there is no lingering unsent suggestion to weigh.
- **No `/clear` equivalent needed** — an ephemeral subagent holds no state
  once it returns. If you want a fresh take on the same objective, dispatch a
  new subagent; there is nothing to reset.
- **No pane to glance at** — the operator has no visibility into subordinate
  work except through the Chair's own running commentary. Report progress and
  findings more deliberately than Herdr mode requires, since there is no pane
  for the operator to check independently.

## Review cadence

The same discipline as Herdr mode: review a subordinate's returned diff or
findings against the artifacts (re-run the check yourself where practical),
not against its own summary. Plan review, then code review, each bounded
by the Convergence rule in the core SKILL.md — one full pass plus one delta pass per half, a
third only if the delta pass finds a BLOCKER, and the third is the last — before anything lands. Accept,
send-back-with-named-defects, or escalate — the same three outcomes, just
without a pane's `idle`/`blocked` state to read; the subagent's return *is*
the signal that a turn is complete.

## Parallel work

The Agent tool's own parallel-dispatch mechanics apply directly — independent
sub-work can be sent in one message with multiple tool-use blocks. The same
rule as Herdr mode still governs: don't let two pieces of parallel work dirty
the same files: sequence, or bind them to disjoint paths. When units have
real dependencies, see "Sequencing dependent units" in the generic core
above — land the foundation, merge it, and only then fan out its dependents;
don't fan out a whole dependency graph in one pass just because the Agent
tool makes it easy to.

## Long-running dispatches

A long-running command (a full test suite, an integration run) dispatched with
`run_in_background: true` wakes the Chair on exit, and that is the intended
shape rather than a risk to be minimised — see "Learning that a subordinate
finished" above for the measurement (15.0, 20.46 and 20.45 minutes, none
reaped) and, just as importantly, for what it does not cover. **This section no
longer advises shortening the window**; that advice predated the measurement,
and each shortening buys another wake for nothing.

A backgrounded run can still be killed. The reported memory pressure often has
nothing to do with the host's actual free memory (check `free` or equivalent
before assuming otherwise), so do not conclude the command itself is broken.
Retry once at the same shape. If it dies again, change *what* is running rather
than how long you are willing to wait for it: serialize what was parallel (a
test runner's `-p 1` or equivalent), or run it in the foreground under a
bounded timeout.

---

