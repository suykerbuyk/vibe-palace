# ADR 017: vibe-palace Worktrees Live Under `<root>/.wt/<slug>` on a Bare-Slug Branch

**Status:** Accepted (2026-10-21)
**Deciders:** Project owner
**Context:** Task `worktree-discipline-wt-dir-slug-convention-and-lifecycle`, whose
"Implementation plan — scoped and decided 2026-10-21" section records seven operator-confirmed
decisions (chaired session, Chair w5:p1B) and a read-only scoping pass (vp-imp1, 2026-10-21) whose
two pivotal facts the Chair re-verified. Drafted and adversarially reviewed 2026-10-21 (one
revision round). This ADR is U1 of that epic and **records** the seven decisions; it does not
re-litigate them. It is a **move + rename** of an existing mechanism, not greenfield — see "Builds
on / supersedes" below. It amends no earlier ADR.

## Context

Task lifecycle has codified discipline (retire only on the operator's word), but the branches and
worktrees used to implement tasks were free-form: created in ad-hoc host paths
(`~/code/<proj>-u5`, `~/code/wt/<name>`, `.claude/worktrees/agent-<id>/`), with no naming
convention and no cleanup rule, so telling which project/task owns a worktree required
investigation. This session's own hand-rolled `/home/johns/code/vibe-palace-u5` is the live
specimen.

A worktree subsystem already exists and is the thing being changed, not replaced. `vp worktree`
(`internal/worktree/worktree.go`) today places a worktree at a **sibling** of the repo root —
`worktreePath` returns `filepath.Join(filepath.Dir(m.Root), siblingDir, slug)` with
`siblingDir = "wt"` (`worktree.go:103-104`, `:41`) — on branch **`plan/<slug>`**
(`BranchPrefix = "plan/"`, `worktree.go:37`; `branchName` returns `BranchPrefix + slug`,
`worktree.go:99`). `List` discriminates a managed worktree by that **branch prefix**
(`strings.HasPrefix(e.Branch, BranchPrefix)`, `worktree.go:208`; default-only-`plan/*` documented
`worktree.go:191-194`). The sole production **Go** `git worktree add` is `worktree.go:157`
(`runGit(m.Root, "worktree", "add", path, "-b", branch, base)`); it is not the only production
creation path (see decision 7).

**A constraint the decisions below build on:** `Create(slug, base, branch)` validates only `slug`
via `slug.Validate` (`worktree.go:131-143`) and uses a non-empty `branch` argument **unvalidated**;
the CLI exposes an unconstrained `--branch` override (`cmd/vp/cmd_worktree.go:42`, passed at `:74`).
`slug.Validate` requires `^[a-z0-9]+(-[a-z0-9]+)*$`, caps length at 64, and rejects `/`, `\`, `..`
(`internal/slug/slug.go:15`, `:25-37`). So today's validator rejects **both** the epic branch
`<epic>/<task>` (contains `/`) **and** the epic directory `<epic>__<task>` (contains `_`). The
directory key is the only string `Create` may treat as a slug today; the branch is a separate
argument. Two constraints therefore force both names to be **derived**, not passed through `Validate`
unchanged: (a) `<epic>/<task>` cannot be a single directory component — a path component may not
contain `/`; and (b) neither `<epic>__<task>` (contains `_`) nor `<epic>/<task>` (contains `/`) is a
valid slug. The operator's `__`-in-directory / `/`-in-branch mapping (decision 3) is the chosen
boundary encoding within those constraints, not the only legal one — `__` is preferred over a hyphen
because a hyphen is a legal intra-slug separator, so `__` keeps the epic/task boundary distinct from
hyphens inside either name.

Worktrees-under-root is already an established, tooling-supported pattern: `.gitignore:5` ignores
`/.claude/` and the Makefile find-sweep prunes `./.claude/*` among others
(`Makefile:96` — it also prunes `./.git/*` and `./third_party/*`; comment `Makefile:89-91` — the
`.claude` prune "keeps a subagent worktree's checked-out copy of this module out of the count").
That mechanism just lives at `.claude/worktrees/agent-<id>/` (harness-specific, agent-id-named, no
task mapping). So `.wt/` is a convention plus a `.gitignore` line plus find-sweep prunes, not new
machinery.

## Decision

1. **Location (universal): every vibe-palace worktree lives under `<project_root>/.wt/`.** Never a
   sibling of the root, never a free host path. The path alone identifies the owning project,
   because it is *inside* it — **no cross-host-path contamination, no ownership investigation.** A
   corollary the fleet rollout depends on: a worktree is never created in a repo whose `/.wt/`
   ignore line is not already present on the branch the worktree is cut from (an untracked `.wt/`
   would otherwise dirty `git status` and double-count in the fmt gate — the U6 ordering hazard,
   recorded here so U6 implements a decided thing). This is a **move** of today's sibling `wt/`
   (`worktree.go:103-104`) to under-root `.wt/`.

2. **Single-task mapping is by convention; nothing is stored.** The worktree directory name AND its
   branch name are both the task slug. `task → worktree` is `.wt/<slug>`; `worktree → task` is the
   basename. Both directions are derivable; no path or branch field is written into the task body
   ("record the query, never the answer" — a stored value rots). This is a **rename** of today's
   branch scheme: `plan/<slug>` (`worktree.go:37`, `:99`) becomes a bare `<slug>`.

3. **Epics keep two tiers, relocated under `.wt/`.** The integration worktree is `.wt/<epic>` on
   branch `<epic>`; each task worktree is `.wt/<epic>__<task>` on branch `<epic>/<task>`, and
   fast-forwards into the integration branch. (This mapping is the operator's, verbatim from the
   plan of record; why `__`-in-directory / `/`-in-branch is the chosen boundary encoding is recorded
   in Context, not here.)

4. **Discrimination is by PATH, not by branch prefix.** A vibe-palace worktree is one whose parent
   directory is exactly `<root>/.wt`. Concretely, `List`'s default (today "only `plan/*`",
   `worktree.go:191-194`, `:208`) becomes "only children of `<root>/.wt`", tested as
   `filepath.Dir(e.Path) == filepath.Join(root, ".wt")`. Parent-dir **equality** (not ancestry) is
   deliberate: the main checkout and `.claude/worktrees/*` stay out with no branch prefix, and the
   `__` encoding (decision 3) keeps the equality true — a nested `<root>/.wt/<epic>/<task>` would
   fail it, so U2 must not "fix" that by walking ancestors (which would then misclassify an
   unrelated tree under a `.wt`-named dir).

5. **The convention is code-enforced, not prose-enforced.** The Chair and `vpc-execute-plan` MUST
   create worktrees via `vp worktree create`, which owns the **path** and must come to own the
   **branch**. It does not own the branch today: `Create` validates only the directory and takes the
   branch as an unvalidated argument, and the CLI `--branch` override (`cmd_worktree.go:42`, `:74`)
   lets any branch sit on a slug-shaped directory. So, as a decided consequence (not a U2 surprise):
   the branch (`<slug>`, `<epic>`, or `<epic>/<task>`) must be **derived by a helper** from the
   directory key, and the free `--branch` override must be **closed by U2, or explicitly kept with
   `vp worktree reconcile` flagging any directory↔branch mismatch**. A hand-rolled `git worktree
   add` is off-convention and is flagged by `vp worktree reconcile`. ("A guard only an agent can
   trip is not a guard" — a prose-only convention is the anti-pattern this decision closes.)

6. **Cleanup happens in close-out, after the operator's push; wrap only REPORTS orphans.** The flow
   is: implement in the worktree → commit → ff-merge local main → operator reviews local main and
   pushes (accepts) → close-out runs `vp worktree remove`, by tier:
   - single-task: remove `.wt/<slug>` and branch `<slug>`;
   - epic task: remove `.wt/<epic>__<task>` and branch `<epic>/<task>`;
   - epic integration: remove `.wt/<epic>` and branch `<epic>` **only when no `.wt/<epic>__*`
     directory remains and no local branch `<epic>/*` is still checked out**.

   Today's `Remove` safe-deletes `branchName(slug)` = `plan/<slug>` (`worktree.go:167-183`, `:99`),
   so U2 must delete the branch it actually **created** (the derived bare/epic branch), not
   `BranchPrefix+slug`. Removal is housekeeping once work is accepted, not a "done" claim (retire
   stays operator-gated, separately). Wrap enumerates orphaned `.wt/*` (mapped to task / branch /
   merged / pushed / dirty state) as a backstop and **never auto-deletes**.

7. **`.claude/worktrees/` is harness-owned and out of scope.** It is the Claude Code Agent-tool
   `isolation: worktree` area. The only **Go** production `git worktree add` is `worktree.go:157`
   and it targets the sibling path; **no Go call targets `.claude/`**. There IS another production
   creation path: the embedded `epic-orchestrator` skill still instructs a hand-rolled
   `git worktree add ../wt/<epic> -b epic/<epic>` and `git worktree add ../wt/<epic>_<task> -b
   <epic>/<task>` (`internal/templates/templates/skills/epic-orchestrator/SKILL.md:148`, `:151`),
   served by the binary — which U3 replaces (both target `../wt/`, not `.claude/`). Decision 7's
   point stands: `.claude/` is harness-owned; keep its Makefile prune; **add a `.wt` prune beside
   it**, not in place of it.

## Consequences

**What changes:** `internal/worktree` moves `siblingDir`→under-root `.wt`, drops the `plan/` branch
prefix for a derived bare/epic branch, swaps the `List` branch-prefix filter for the parent-dir path
test (decision 4), closes-or-reconciles the `--branch` override (decision 5), and grows epic
two-tier derivation helpers; `vp worktree reconcile` reports off-convention trees and
directory↔branch mismatches; close-out and wrap gain the tiered remove/report steps; this repo's
`.gitignore` and find-sweeps gain `/.wt/`. (These are U2–U5 work items; this ADR records only the
decisions.)

**What stays:** `.claude/worktrees/` and its Makefile `.claude` prune (`Makefile:96`, which also
prunes `./.git/*` and `./third_party/*`); the `vp worktree` subsystem's git-admin model (git admin
data lives in `.git/worktrees/<name>`, not in the ignored checkout); the operator-gated retire.

**Verified facts (settled):**
- `go build/vet/test ./...` and sourceaudit's `packages.Load(cfg, "./...")`
  (`internal/sourceaudit/derived_gate.go:236`) STOP at a nested `go.mod` boundary even with an
  identical module path (reproduced at go 1.27.2; corroborated by `third_party/coder-hnsw` returning
  0 packages from `go list ./...`). **The compile/test gate is unaffected by a live worktree** —
  this part is verified.
- sourceaudit's syntactic walk skips any dot-prefixed directory (`sourceaudit.go:231`), so `.wt/` is
  already excluded there with no change.

**What those facts do NOT entail:** they do not entail "only `find`-based sweeps need a `.wt`
prune." Every walk that is **not** the Go loader and **not** that dot-dir skip sees a live
`.wt/<slug>` (a full checkout) and needs a `.wt` prune. U4's set —
`scripts/check-architecture-citations.sh:77`, the gopls-hint prose at
`epic-orchestrator/references/prompt-implement.md:35-36`, and the resume Quick-Reference `find`
(which prunes `./.git/*` and `./.claude/*` but not `.wt`) — is the **KNOWN set, not a proof of
completeness**.

**UNVERIFIED — to confirm in U2, not settled here:** that `git worktree add .wt/<slug>` succeeds at
an under-root path that `.gitignore` ignores. The scoping pass was read-only; U2 confirms this
empirically before relying on it.

## Builds on / supersedes

This ADR **supersedes the location and branch-naming conventions** of the existing `vp worktree`
subsystem: the sibling path `filepath.Dir(m.Root)/wt/<slug>` (`worktree.go:103-104`, `:41`) and the
`plan/<slug>` branch scheme (`worktree.go:37`, `:99`), together with the `List` branch-prefix
discriminator (`worktree.go:208`). It is a MOVE (sibling → under-root `.wt/`) + RENAME
(`plan/<slug>` → derived bare/epic branch) + path-based discrimination + code-enforced, consistent
adoption, on the same `git worktree add` machinery (`worktree.go:157`) plus the embedded creation
paths U3 replaces (`epic-orchestrator/SKILL.md:148`, `:151`). No prior ADR records this convention,
so there is no ADR status line to mark superseded; this section carries the supersede note. It does
not supersede the harness's `.claude/worktrees/` area (decision 7).

## Alternatives considered

- **Keep the sibling `wt/<slug>` layout, add a naming convention in prose** — rejected: a sibling
  path is outside the project and cannot be identified by location alone (decision 1), and a
  prose-only rule is "a guard only an agent can trip" (decision 5).
- **Store the worktree path/branch in the task body** — rejected: a stored value rots; both
  directions are derivable (decision 2).
- **Keep discriminating by a branch prefix** — rejected: bare slugs carry no prefix, and the
  location discriminates by construction (decision 4).
- **Fold vibe-palace worktrees into `.claude/worktrees/`** — rejected: that area is harness-owned,
  agent-id-named, and carries no task mapping (decision 7).
- **Leave the `--branch` override free** — rejected: it makes "the binary owns the branch" false and
  lets an epic branch land on a mismatched directory; U2 closes it or reconcile flags it
  (decision 5).
