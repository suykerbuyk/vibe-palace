## Process flow (extended session)

### 0. Seat, then propose, then wait

After discovery, propose a **prioritized next-work list** with reasons (risk,
proof, what it unblocks). The operator chooses. Do not start a unit —
especially gated destructive work, retire, or commit — on the Chair's own
initiative.

### 1. Bind, then ask

Every prompt to an implementor leads with **binding constraints**, then the
ask. Name what they must not do (retire, commit, start the next unit, gated
destructive work, `git add -A`, revert another pane's files). **One open unit
per implementor** in the sense of not colliding on the same files — a single
implementor working many sequential steps of one deep investigation is not a
violation of this, so long as nothing else is dirtying the same paths at the
same time.

### 2. Keep them working

1. Arm a waker at dispatch rather than watching — "Learning that a
   subordinate finished" above. A finished pane reports `done` or `idle` (both
   occur in the live population); `blocked` is an approval or question UI.
2. On **idle/done**: read output, review against the artifacts (not their
   recap). Then: **accept**, **send-back** (named defects, same unit),
   **hold**, or **escalate** to the operator.
3. On **blocked**: classify the question. Answer it if it's in the Chair's
   seat; escalate if it's the operator's.
4. If a pane's context is stale, rediscover and re-bind constraints.

### 3. Close a unit without lying about git

Do not start the next unit inside an uncommitted wrap. Close-out is the
project's **wrap** command, timed by the project workflow's commit pattern.
Dispatch stage and wrap the way the rest of this skill dispatches commands —
`vp_cmd` with `name="stage"` / `name="wrap"` (the host surfaces these as the
`vpc-`-prefixed shims); do not hand-type a slash alias. The default pattern,
which the wrap and stage commands already implement, is:

- **Direct commit to the integration branch the project workflow names
  (default `main`).** Run wrap BEFORE the commit. Wrap is the whole
  preparation — the hard gate (clean compile, unit and integration tests
  pass), author `commit.msg`, vault ingest, path-scoped `git add`. **Do not
  run stage first.** Then commit from the authored message:
  `git commit -F commit.msg && rm commit.msg`.
- **Feature branch.** For each intermediate commit, run stage — the light
  gate, author `commit.msg`, path-scoped `git add`; **stage does not touch
  the vault** — then the same `git commit -F commit.msg && rm commit.msg`.
  Run wrap ONCE, AFTER the workflow's post-merge steps: by default the
  `--ff-only` merge to the integration branch, then the push the operator
  still must authorize, then feature-branch deletion. On that clean project
  tree, wrap's `commit.msg` and staging steps self-skip; its git-derived
  commit-log archive and vault sync still run. Wrapping before those steps
  records transient state into the vault narrative.

If the project workflow names a different commit pattern, follow the workflow.

**The commit is always `git commit -F commit.msg && rm commit.msg`, never a
bare `git commit -m` — a standing commit grant does not waive that.** Two
separate reasons, not one chain:

- `-m` bypasses the authored `commit.msg`, so the project's commit-message
  tooling never ran. A `-m` message is also not the file the post-commit
  reaper compares, so the hook does not reap unless the `-m` text happens to
  equal a leftover `commit.msg`. The `&& rm` is what consumes `commit.msg` on
  the hand-run path; the hook only backstops a forgotten `rm`.
- Skipping wrap leaves no vault mirror of the unit until some later wrap
  happens to run. Running `-m` does not itself skip wrap — these are two
  independent omissions; do not treat fixing one as fixing the other.

Do not add `Co-Authored-By` trailers, author lines, or any other AI authorship
marker, in the commit message or in code. The project's stage and wrap
commands already enforce this; a standing commit grant does not waive it. If
the project workflow states a stricter attribution rule, follow that too.

**Who runs what.** Wrap of a bound unit is the implementor's — dispatch the
implementor through the project's wrap command for their own unit; run wrap
from the Chair's own context only when the unit is Chair-bound. The commit
itself is run by whoever holds the commit grant — the Chair under an explicit
standing delegation, otherwise the operator; an implementor never commits
(Roles table). On the feature-branch path, an implementor may run the stage
step for their own unit (Roles table permits path-scoped staging), but the
commit itself stays with the grant-holder. Before starting the next unit,
confirm the vault mirror actually landed (ingest, commit-log, sync), not merely
that a wrap was reported.

If the operator says the git operations are done, believe the tree
(`git status`, `git log -1`), not the chat.

---

