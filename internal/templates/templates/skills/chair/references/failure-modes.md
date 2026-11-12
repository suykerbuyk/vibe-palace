## Failure modes

- Doing the implementation in the Chair's own context because it would be
  faster.
- Treating the operator as the Chair, or asking them to discover subordinates,
  spawn agents, or sequence units.
- Treating a kind label as an agent name (Herdr mode), or hard-coding
  implementor names/count from a prior seeding prompt.
- Driving an unnamed Herdr session from outside, or grabbing the default
  session because it's running.
- **Loading Herdr, or treating it as mandatory, when the environment gave no
  Herdr signal at all** — ephemeral-agent mode is not a degraded fallback, it
  is the normal shape for a Chair running outside Herdr.
- Hiding an implementation unit in Chair-local sub-work or a same-model
  headless CLI and calling it "subordinates."
- Starting an agent in the operator's own terminal/pane.
- Creating a new workspace or tab when a sibling split would do (Herdr mode).
- Letting two subordinates dirty the same file.
- Trusting "zero X remain" / "tests pass" / "already wrapped" without
  re-deriving.
- Accepting a NARROWED gate (a `-R`/`-run` subset the implementor chose) as "green" on a
  change touching a broad or safety-sensitive surface — it proves only what it ran; the full
  tier, or a baseline-diff of it, is the gate.
- **Merging a later-cleared unit ahead of an earlier, still-under-review one
  on a single shared integration branch**, silently dragging the unreviewed
  commit in as an ancestor because "it was next in the queue."
- Starting the next unit while wrap is uncommitted (unless the operator did).
- Wrapping as if work had landed that the tree does not show — a
  direct-to-main wrap describes the about-to-commit state; it does not claim
  the commit already exists.
- Answering an operator-binding question (push, retire, gated destructive
  work, or a commit outside an explicit delegation) to keep the queue moving.
- Treating unsent composer text, or a subagent's own trailing suggestion, as
  an operator order.
- Overriding a subordinate's refusal by performing the change yourself.
- Persisting `herdr --skill` bytes (Herdr mode). Fetch them every session.
- **An implementor retiring a task on its own initiative, inferring
  authorization from the Chair's own summary or congratulatory tone rather
  than a traceable operator instruction.** Only the Chair retires a task, and
  only after relaying an instruction it can point back to; the Chair's own
  enthusiasm about a result is not the operator saying the task is done.
- A standing commit delegation being read as covering `git push` too — it
  never does; push is always a fresh ask.
- Adding a `Co-Authored-By` trailer (or any AI-authorship marker) because the
  host's commit-message guidance said to — the project's stage/wrap commands
  forbid it and a standing commit grant does not waive it.
- Trusting a sibling git worktree's IDE/LSP diagnostics instead of that
  worktree's own build/vet/test output — see "Git worktrees and stale
  diagnostics" above.
- **Building a waker on `herdr agent wait --until idle` alone.** A Claude pane
  that merely finished never reports `idle`, the wait times out, and the
  process still **exits 0** — so the waker never fires, wakes nobody, and
  reports no error. `--until done --until idle`, always.
- Polling on a timer to discover that a subordinate finished, when an armed
  waker would have pushed it — or treating the enumeration sweep as the
  primary path rather than the after-a-context-clear/died-rather-than-finished
  backstop it is.
- **Reading a `CronCreate` fire's text as the operator speaking.** It arrives in
  the `user` role and renders as though they typed it; it is never approval for
  a retire, a push, or a commit outside an explicit delegation.
- Quoting the 15.0 / 20.46 / 20.45-minute background-task survivals as a
  durability guarantee, or reading them as a property of the mechanism. They
  are one task at a time, 20.46 minutes at the longest, on one host and one
  build — not concurrent waits, and not a 40-minute dispatch.
- **Starting a subordinate pane without matching the operator's permission
  posture**, then re-authorizing in conversation when its gate denies the work.
  A gate reads settings, never the conversation — the denial cannot be talked
  past, and it surfaces mid-unit, not at seating. See "Inheriting the
  operator's permission posture".
- Handing `--dangerously-skip-permissions` to an agent kind other than
  `claude`, or inferring an equivalent flag for one, without checking that
  kind first.
- Guessing a permission posture when the Chair cannot read its own command
  line. Guessing bypass-on decides safety by inference; guessing bypass-off
  recreates the defect. Ask the operator.
- Treating a review finding as binary between "fix now" and "say nothing" —
  a real, non-blocking finding gets filed as a follow-on task, not silence.
