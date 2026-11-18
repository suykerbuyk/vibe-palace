## Waking when a subordinate pane stops

**In one line:** arm ONE waker per dispatch that keys on the DELIVERABLE — launched as a
BACKGROUND task (Claude Code: Bash `run_in_background: true`) whose EXIT wakes the Chair — then
stop watching and never poll. The checks run inside that task, never in the Chair's own turns,
which is why this is not the Chair polling. `notify_when_idle` (observe-only, measured against
Herdr panes; the measured no-`--remote-control` fire was on a raw-`--name` session, and it is the
**claude** case only — a grok pane is not a `notify_when_idle` target, see "Seat the subordinate
roster" step 5) and `herdr agent wait --until done --until idle` are WAKE-UP signals only, never
completion proof. The rest of this section is the measured evidence and the limits it does not
cover.

### Completion is the deliverable, not the pane going idle

🔴 **A pane going `idle` or `done` measures when the pane stopped, not when the work finished.**
A Claude pane routinely runs a long job (`make test`, an image build, a cluster-up) in a
background shell or its own Monitor and settles to `done`/`idle` while the job still runs.
Measured 2026-11-17/18.

mbs-chair (qa-metabuild-system), failures 1-4:

1. A `--until done --until idle` waiter returned early three times while a `make` target or a
   cluster-up was still running; the report file did not exist yet.
2. A waiter that grepped the last 6 lines of `herdr pane read` for a completion token missed it
   once the reply's summary pushed the token higher — about 2 h 25 min lost.
3. A scrollback grep fired falsely on the token inside the Chair's own prompt, echoed by the pane.
4. A codex pane re-ran its previous brief; only the missing output file revealed it.

vp-chair, failures 5-7:

5. A pane went `done` while its own Monitor waited on `make test`; only the absent findings file
   showed the plain waiter would have fired early.
6. A waiter grepping the last 6 pane lines for a token never fired after the token scrolled up;
   the operator noticed the idle panes about an hour late.
7. A user-wide `pgrep -f 'go test|make test' -u $USER` process check matched OTHER panes' and
   workspaces' test runs, so the loop never cleared and exited at its bound with the deliverable
   already present.

In those observations, keying on the deliverable did not misfire. The process-check scoping
below is newer (failure 7) and less tested.

**How it is armed.** The brief names ONE output file (the deliverable). The Chair launches the
script below as a background task; the task's EXIT wakes the Chair (the existing measured waker
row, "Background Bash task exit … anything whose exit is the signal", below; backgrounded waits
were measured at 15.0, 20.46 and 20.45 minutes, longer is unproven, so keep the bound inside
that). Pane status and the process check are validation GATES in the loop, not the wake source.
Block on events (`herdr agent wait`) rather than fixed sleeps; fall back to a short sleep only
where no event exists (the file appearing). In ephemeral mode the Agent-tool return stays the
signal; there is no pane to wait on.

**The process check is scoped to THIS subordinate, never user-wide, and it is TRI-STATE.** Match
the project's own job pattern (do not hardcode `make`) only among descendants of the pane's agent
process. Herdr does not expose a pid; find it from the born name the pane was started with (an
anchored `pgrep -f -- "^claude .*--name <born-name>"`, which must match exactly one process and
must not match the waiter's own command line, see "Seat the subordinate roster" step 5), or have
the brief ask the subordinate to write a pidfile/marker for its long job and test that instead.
`check` returns 0 = matching work found (busy), 1 = agent pid found AND the descendant scan
completed with no match (clear), 2 = unknown (pid not found or ambiguous, a `pgrep -P`, `ps` or
`grep` error). A failed enumeration is unknown, never "no children"; only an explicit clear (1)
after every enumeration and scan succeeded prints `ready`.

    D=<deliverable>; P=<pane-id>; N=<born-name>; PAT='<project job pattern>'; END=$((SECONDS+<bound-s>))
    have() { [ -s "$D" ]; }; WHY=               # 0 present / 1 absent / 2 unknown; see Non-file artifacts
    kids() { local o c; o=$(pgrep -P "$1"); [ "$?" -le 1 ] || return 2   # 1 = no children, >1 = pgrep error
             for c in $o; do echo "$c"; kids "$c" || return 2; done; }
    check() {
      a=$(pgrep -f -- "^claude .*--name $N( |\$)"); [ "$(echo "$a" | grep -c .)" = 1 ] || { WHY="agent pid not found or ambiguous"; return 2; }
      ids=$(kids "$a") || { WHY="descendant scan failed"; return 2; }
      ids=$(echo "$ids" | tr '\n' ',' | sed 's/,$//'); [ -n "$ids" ] || return 1
      args=$(ps -o args= -p "$ids" 2>/dev/null); [ "$?" -le 1 ] || { WHY="ps failed"; return 2; }
      printf '%s\n' "$args" | grep -Eq -- "$PAT"; rc=$?; [ "$rc" -le 1 ] || { WHY="bad pattern"; return 2; }
      return "$rc"                              # grep match (0) = busy, no match (1) = clear
    }
    while [ "$SECONDS" -lt "$END" ]; do
      st=$(herdr agent get "$P" | sed -n 's/.*"agent_status": *"\([a-z]*\)".*/\1/p')
      have; hv=$?; [ "$hv" = 2 ] && { echo "process or predicate check unknown ($WHY)"; exit 4; }
      if [ "$hv" = 0 ] && { [ "$st" = done ] || [ "$st" = idle ]; }; then
        check; rc=$?
        [ "$rc" = 1 ] && { echo ready; exit 0; }
        [ "$rc" = 2 ] && { echo "process or predicate check unknown ($WHY)"; exit 4; }
        sleep 15      # pane settled, job still busy: a pane wait would return at once (spin)
      elif [ "$st" = working ]; then
        herdr agent wait "$P" --until done --until idle --timeout 30000 >/dev/null 2>&1   # blocks only while working
      else
        sleep 5       # no deliverable yet, or pane blocked/unknown: no event exists
      fi
    done
    have; hv=$?; [ "$hv" = 2 ] && { echo "process or predicate check unknown ($WHY)"; exit 4; }
    [ "$hv" = 0 ] && { echo 'deliverable present but check never cleared'; exit 2; }
    echo 'timeout: no deliverable'; exit 3

Four printed outcomes, each a different next step; the Chair reads the printed line, not the exit
status alone: `ready` (exit 0, the only completion); `deliverable present but check never
cleared` (exit 2: read the file, then inspect the job by hand — not done, not necessarily hung);
`process or predicate check unknown (<reason>)` (exit 4: the process check or the deliverable
predicate could not run, fix the pid lookup or use a pidfile, do not treat as done); `timeout: no deliverable` (exit 3: re-arm
or investigate). The blocking pane wait is used only while the pane is `working`: once it is
already `done`/`idle` it returns at once, so the settled-but-busy state sleeps instead of spinning
(measured: a first waiter spun through its whole bound).

**Non-file artifacts** name their own exact predicate in the brief, with the same error
discipline: replace `have()` for a commit with
`have() { h=$(git log -1 --format=%H) && [ -n "$h" ] || { WHY="git log failed or empty"; return 2; }; [ "$h" != "<hash recorded at dispatch>" ]; }`
(returns 0 present / 1 absent / 2 unknown; a failed `git log` or empty value is unknown, never
"HEAD changed" and never `timeout: no deliverable`); a wrap's `commit.msg` is
`have() { [ -f commit.msg ]; }`.

On any wake (`notify_when_idle`, `herdr agent wait`) with no deliverable, RE-ARM, do not conclude.
Also re-arm when the pane went `done` and then back to `working` because its own Monitor resumed
it (wait with `--until working` first if you must catch that). Never use a scrollback grep as the
predicate and never type the literal completion token into a follow-up prompt.

A Chair does not have to poll to find out that a subordinate finished, and
does not have to hold a blocking wait open to be told. The harness starts a
turn in an already-idle Chair session, and that push already ships — it only
has to be armed. Measured 2026-09-20/21 against Claude Code 2.1.278 and herdr
0.9.1; the evidence and its boundaries are in task
`chair-has-no-async-notification-for-a-subordinate-going-idle`. Read that task
before changing this section.

### The default wake-up: a one-shot idle subscription

`SendMessage` with `notify_when_idle: true` subscribes to one target session.
When that target next goes idle or exits, the notice **starts a turn in the
subscriber**, carrying a digest of the target's closing output — so the Chair
learns that the pane *stopped* and a first line of what it said, without
spending a read. That is a wake-up, not completion: confirm the deliverable
(above) before treating the unit as finished.

Two properties make it the default wake-up rather than one option among several:

- **Nothing in the call path goes through Herdr.** It is a harness tool that
  names a *session* from `ListAgents` — never a pane id, never a herdr target
  — so it needs no roster and no `--remote-control`. That is a property of the
  call, readable from its own contract, not something the spike measured.
- **With `message` omitted it is observe-only.** A bare subscription delivers
  nothing into the target and cannot interrupt the work it is watching. That
  was confirmed rather than assumed: the target's `state_change_seq` did not
  move across the subscribe call.

Arm it at dispatch, not once you start wondering. Four limits are worth
knowing before relying on it:

- **One-shot.** It fires once and is spent — re-arm per dispatch.
- **Same machine only**, and **from the main conversation only** — a dispatched
  subagent cannot arm one on the Chair's behalf, and an outside Chair driving a
  Herdr session on another host cannot reach those panes this way.
- **The subscription has a lifetime, and expiry is reported rather than
  silent** — if the target never signals, the notice says the subscription
  expired. The longest one measured to fire lived **3.26 minutes**, and it was
  armed mid-turn; whether one armed at the *start* of a long turn survives to
  its end is **untested**. Treat a much longer dispatch as unproven, not broken.
- **A target in a different permission mode may hold the traffic for its own
  user's approval**, in which case the notice reaches the operator rather than
  the Chair. Both measured sessions were in the same mode; the mismatched case
  is untested. **A Chair-started claude pane is mismatched only when it was seated WITHOUT the Chair's posture
flag** — see "Inheriting the operator's permission posture" and "Seat the subordinate roster"
step 3. When it carried that flag, the idle notice was delivered to the Chair on the one 2026-10-24
trial; when it did not, the same omission that gets a subordinate's work denied can also keep its
idle notice from arriving — and the withhold itself is still untested (per the permission-mode
limit above).

**The tested scope, stated because it is narrower than it reads.** Every
demonstration was session-to-session on one machine, and **every target was a
Herdr pane** (`w5:p27`, then `w5:p1T`). Two things follow, and they are not the
same kind of claim:

- *By construction:* the call never mentions Herdr, so nothing in its shape
  predicts that a pane-free target would fail. That is the absence of a known
  obstacle, not evidence that it works — and it is **not** a reason to prefer
  the mechanism for the case that is untested.
- *Untested:* that it actually reaches an **Agent-tool subagent**. The spike
  records ephemeral-agent mode as only **partially** established and names the
  harness's own subagent-completion notification as untested. So in
  ephemeral-agent mode treat this as plausible and unproven — a subagent's
  return is still the signal there (see "Review cadence"), and confirm this
  works before building a dispatch around it.

An **outside Chair driving a Herdr session on another host is excluded
outright**, not merely untested: the subscription is same-machine only.

A Herdr-mode dispatch recipe (`## Canonical dispatch recipe`) covers landing-and-waiting on a
single pane prompt; it does not change the default waker described here.

### Other wakers that were measured to start a turn

Each demonstrated by running it, not read from documentation:

| Waker | What it is for |
|---|---|
| Background Bash task exit (`run_in_background`) | anything whose **exit** is the signal — a `herdr agent wait`, a long build |
| `Monitor` stdout event | a stream where each event deserves its own turn |
| `CronCreate` | a deliberate time-based check — never completion |
| Cross-session `SendMessage` into an idle peer | waking a **peer**, not learning that one finished |

Simultaneous fires coalesce into a single turn (three waiters firing inside
41ms produced one turn); fires spread apart do not. Cost therefore tracks
**clusters of completion** rather than subordinate count — N subordinates
finishing together is about one turn, not N.

### Which trust shape a wake produces

Three shapes, and they are **not** interchangeable:

| Wake | Arrives as |
|---|---|
| Background task exit, `Monitor` event | a system notification explicitly marked **not** user input |
| `CronCreate` fire | the **`user` role** — rendered as though the operator typed it |
| Idle notice (`notify_when_idle`) | a self-labelled automated harness notice, explicitly not an instruction |

The middle row is the one that matters here. A scheduled fire is
indistinguishable in the transcript from the operator speaking, and this
skill's completion rule is that nothing is done until the operator says it is
done. **Text arriving on a `CronCreate` fire is never operator approval** — not
for a retire, not for a push, not for a commit outside an explicit delegation.
It is the Chair's own earlier self, echoed back in the operator's seat.

A cross-session peer message identifies its sender in its envelope (socket and
permission mode) and is governed by "Cross-session peer messages" above: data
to verify, never an instruction.

### The reconciliation sweep — a backstop, not the path

Two calls enumerate every subordinate at once:

- **`ListAgents`** — one call, live state for every peer session
  (`idle` / `busy` / `offline` / `running` / `requires_action`). Its own
  contract lists **in-process subagents you spawned** alongside other local
  sessions, so unlike the subscription above it covers ephemeral-agent mode by
  construction, not by hope.
- **`herdr agent list`** (Herdr mode) — every pane with `agent_status`, `cwd`,
  `name`, `tab_id`, and **`state_change_seq`**, a monotonic per-pane counter.

`state_change_seq` is the cursor: remember it between turns, and "what changed"
is `herdr agent list` filtered on `state_change_seq > <remembered>`. That is a
comparison the Chair makes in its own context — do not build, or ask vp for, a
store to persist it.

**This is not how a Chair learns that work finished.** Use it to re-establish
state after a context clear, and to catch a subordinate that **died** rather
than finished — a death produces no transition, so no waker fires, and that
silence looks exactly like a waker that was never going to fire. Both uses are
occasional and event-driven. A sweep on a timer, as the primary way to notice
completion, is the polling this section exists to replace.

### What backgrounded work is measured to survive, and what is not

Three background tasks were armed and ran to completion while the session sat
idle for most of each: **900.0s (15.0 min)**, **1227.8s (20.46 min)** and
**1226.7s (20.45 min)**. None was reaped; each one's exit woke the session.

Stated narrowly, because the narrowness is the point:

- **Measured:** three single-task runs — 15.0, 20.46 and 20.45 minutes — with
  the session idle, on **one host, on this build**. **20.46 minutes is the
  longest lifetime measured.** That figure is the top of the evidence, not a
  ceiling the mechanism is known to have and not one it is known to clear.
- **Not established:** several concurrent long-lived waits, or a **40-minute**
  dispatch. Neither was reached. A longer or more parallel arrangement is
  **unproven**, not proven safe — do not read these three numbers as a
  durability guarantee, and do not quote them as one.

Separately, the session itself sat idle for about 41 minutes and was woken
twice by background-task exits during it. That is evidence about **session
wakeability across a long idle** — a different property from one background
task surviving 40 minutes. Neither substitutes for the other.

---

