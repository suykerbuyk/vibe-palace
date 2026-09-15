# ADR 008: The Agent Instruction-Manual Lives in the Binary, Not in Vault Files

**Status:** Accepted (2026-07-21) — Phase 1 implemented 2026-07-22
(`29437ff`: doctrine embedded in the binary and served via `vp_get_doctrine` /
`vibe-palace://doctrine/<project>`; embedded `workflow.md` thinned to project
patterns + doctrine pointer). **Phase 1 live-vault rollout COMPLETED 2026-07-25
(iteration 257)**: Edit A/B applied under compare-and-set, cutting the pinned
inviolable core 13,271 → 3,527 bytes (73%) and the live canary to 7,457/8,000.
Phase 1 did **not** make the core FIT, and was never able to — the full resume
still exceeds the whole token budget on its own, so `resume->pinned` keeps
shedding and `adr-009-arm-fail-loud-bootstrap` stays gated. Phases 2–4 pending
(see `doc/rollout/adr-008-phase1-live-vault-edits.md`, now a record rather than
a plan).

**Amended 2026-09-11** — see *Amendment (2026-09-11): Templates/ provenance is a frozen shipped-version manifest*.

**Deciders:** Project owner
**Context:** `workflow.md` is the largest un-sheddable item in the bootstrap payload, it has drifted from its embedded floor, and it is riddled with host-specific assumptions — three symptoms of one cause.

## Context

Vibe-Palace is a **host-agnostic** system: its MCP has been driven from Claude Code,
Grok Builder, and the agent inside Zed, and it must work for any future host (VSCode,
others) that can host an MCP. The standing axiom is that **the MCP is the center of every
managed project space** — it must exist, and it **air-gaps the vault implementation**
behind accessor tools so no agent ever touches the filesystem or knows the layout.

Two documents are read into every agent's context at session start: `resume.md` and
`workflow.md`. Each is doing **two jobs at once**:

1. carrying **project-specific state** (this project's stack, gotchas, task index), and
2. doubling as the **Vibe-Palace instruction manual** — how the tools work, the
   pair-programming contract, the seven-action task model, the vault-accessor rules, and
   narratives of how the tooling evolved.

The second job does not belong in vault markdown that must be kept in lockstep with binary
releases. An investigation (four parallel read-only probes, 2026-07-21) established:

- **The served `workflow.md` is overwhelmingly generic doctrine — but not 100%.**
  Section-by-section, most of it is generic; a line-level triage (not a bulk move) is
  required, because the file also carries genuinely project-specific content (the
  wrap-timing rule, project data-workflow notes, project statistics) and one piece of
  operator-absolute-path junk to DELETE outright. Additional genuinely project-specific
  workflow content is misfiled in `resume.md`'s pinned *Behavioral Notes*.
- **It has drifted from its own embedded floor.** The embedded scaffold
  `internal/templates/templates/workflow.md` is 4.7 KB; the served vault copy
  `Projects/vibe-palace/workflow.md` has grown to **12.5 KB — 2.6×** — and the resolver
  serves the bloated vault copy *over* the lean embedded one.
- **That bloat is the token-budget breach.** The live bootstrap payload rides the edge of
  its own 8,000-token default; the ~7.8 KB of vault doctrine bloat *is* the overage. (A
  shed-ladder measurement bug — the payload was measured without the `Budget` field
  attached, so `over=false` could be reported for an over-budget payload — was part of the
  original finding; its fix is owned by `enforce-adr-009-inviolable-bootstrap-core`, not
  this work, and the post-attach re-measure has since landed.)
- **The doctrine is host-coupled.** It names `~/.claude/plans/`, `CLAUDE.md`,
  `.claude/settings.json`, `.claude/worktrees/`, and one command computes an absolute
  vault path out of `~/.config/vibe-palace/config.toml` — direct violations of the
  host-agnostic and air-gap axioms.
- **Instruction propagation is a `config sync` tax with no project payoff.** Of the
  reconcilers `vp config sync` runs (six fixed instances plus one `TemplateTree`-scaffold
  per vault project), exactly one — Templates/Materialize — exists solely to copy the
  embedded instruction corpus onto disk and track it in `templates.lock`. That churn (and
  its conflict class) is entirely about updating *instructions*, not project data.

These are not four problems. They are one: **the instruction manual is stored as
vault files, so it drifts, bloats the budget, couples to a host, and taxes every sync.**

This is the natural terminus of **ADR-006 ("Derive, Don't Ask")**: for a rule you would
write in prose and ship in a vault file, the deeper move is to serve it from the binary
that implements it, so it cannot drift from the code it describes.

## Decision

**The Vibe-Palace instruction manual is owned by the binary and served by the MCP. Vault
files carry only project-specific state.**

### 1. Two resources, not one — because the resolver replaces, it does not merge

The precedence resolver (`internal/context/precedence.go`, Project › Vault › Embedded,
most-specific-wins) **replaces** a lower tier with a higher one; it does not append. So a
project's own patterns cannot live in the same `workflow` resource as the doctrine — a
vault `workflow.md` *shadows* the embedded doctrine entirely. Therefore:

- **`doctrine`** — the generic Vibe-Palace manual (pair-programming, investigation-first,
  the seven-action task model, core principles, the vault-accessor/air-gap rules). Its
  **single source of truth is the embedded binary.** It remains 3-tier-overridable (a
  project may fork it as a glide path while perfecting a behavior), but the **tested
  embedded copy is the floor and the preferred source** for all vault-wide base behavior.
- **`workflow`** — thin, genuinely **project-specific** workflow patterns only. Vault/project
  tier. For vibe-palace this receives the *Behavioral Notes* currently misfiled in
  `resume.md`.

### 2. Doctrine is served ON DEMAND, off the bootstrap budget

The 8,000-token budget is a property of the **bootstrap payload**. Doctrine is no longer
inlined into that payload; it is fetched from the MCP by the session-start (restart) flow
via a dedicated surface. This keeps the bootstrap payload minimal (thin project `workflow`
+ resume index), resolves the budget breach structurally rather than by raising the number,
and matches the existing inline-vs-on-demand split already used for the resume
(`resume` excerpt + `resume_uri`). The on-demand plumbing already exists for workflow
(`WorkflowURI`, `vp_get_workflow`, the served `vibe-palace://workflow/{project}` resource).

### 3. `resume.md` becomes a thin project task index

Its pinned `## Task Management` block is generic doctrine and moves into the embedded
`doctrine`. Its `## Project-Specific Behavioral Notes` — the real project-specific workflow
— moves into the thin project `workflow.md`. What remains is project state: what this
project is, data workflow, quick reference, and the task/history index.

### 4. The manual is host-agnostic and air-gapped

Doctrine describes **MCP tools and capability classes**, never a host's filesystem. Every
Claude-ism is genericized (a "host context file", not `CLAUDE.md`; a "host-local plan/scratch
area", not `~/.claude/plans/`). Instructions never tell an agent to read/write/glob the
filesystem or compute a vault path; all vault access is through accessor tools
(`vp_vault_*`, `vp_get_*`, typed writers). The `tasks-*.md` commands — which defer subtree,
role, and count derivation to the server and explicitly forbid filesystem scans — are the
model every command matches.

### 5. Capabilities are self-described from the runtime registry

The MCP `tools/list` handshake already hands every host each tool's name, description, and
input schema, so a hand-written tool-syntax inventory is redundant and forbidden (it can
only drift). A read-only `vp_manual` / `vp_capabilities` tool self-describes from
`Registry.List()` — tools + the vpc-/vps- dispatch convention + the doctrine — so a fresh
host learns everything in-band with **zero hand-authored inventory**. The existing
tool-surface golden test doubles as the guarantee that this dynamic manual stays truthful.

## Consequences

**Positive**
- The token budget is handed back (~8,063 → ~5,960 tokens), structurally, not by raising
  the ceiling. Resolves the `bootstrap-payload-exceeds-its-own-token-budget` canary.
- Instruction-propagation `config sync` churn collapses: the Templates reconciler only
  materializes files a project has actually **overridden**, not the whole corpus.
- The doctrine cannot drift from the binary, because it *is* the binary's content.
- Vibe-Palace stops being implicitly Claude-Code-shaped; a new MCP host works with no
  host-specific document to hand-maintain.
- Single source of truth for base behavior; the glide-path override seam is preserved.

**Costs / risks**
- The bootstrap "un-sheddable workflow contract" guard (the conditional restore inside
  `shedToBudget`, deleted at 310) assumed the behavioral rules physically live in the inlined workflow
  body. When doctrine moves to the on-demand surface, that guard's semantics move with it:
  what the guard protects is the thin workflow's minimal bootstrap-contract paragraph (the
  pointer at `vp_get_doctrine`), riding the ADR-009 core-tier classification
  (`shedRungTier`) that `enforce-adr-009-inviolable-bootstrap-core` landed — the ladder is
  not re-partitioned by this work. At the embedded floor the thin workflow sits under the
  excerpt cap, so the rung cannot even fire; the guard remains for fat vault overrides.
- New surface area: an interim read-only `vp_get_doctrine` tool (Phase 1) and a
  `vp_manual`/`vp_capabilities` tool (Phase 2). Adding a read-only tool does not by itself
  require an `MCPSurfaceVersion` bump, but the tool-surface golden must be regenerated.
  (A `vp_knowledge_write` typed writer was originally proposed here and is **DROPPED**: it
  was premised on `vp_get_knowledge` reading `knowledge.md`, but that tool returns the
  knowledge GRAPH — a different subsystem. The `cancel-plan.md` raw-file-tools violation is
  fixed with the existing `vp_vault_read`/`vp_vault_edit` on the vault-relative
  `Projects/<p>/knowledge.md`; no new mutating tool, no surface-bump assessment.)
- Migration must be atomic-by-construction (the corpus is `go:embed`'d, so binary and
  doctrine ship together) and honor the surface-version gate for any change to what/where
  instruction files are written.
- A separate, real bug surfaced: the shed ladder measured the payload *without* the
  `Budget` field it attaches afterward, so it could report `over=false` while shipping over
  budget. That measurement fix is **owned by `enforce-adr-009-inviolable-bootstrap-core`**
  (operator-ratified, 2026-07-22), not by this work — one bug, one owner. The same
  boundary applies to the shed ladder itself: this work touches no ladder partitioning; it
  ships the thin workflow whose minimal bootstrap-contract paragraph the ladder's existing
  core-tier guard protects.

**Neutral**
- The host slash-command shims are already thin `vp_cmd`/`vp_skill` pointers reconciled at
  bootstrap; this refactor does not need to touch them.

## Relationship to prior decisions

- **Extends ADR-006 (Derive, Don't Ask).** Serving doctrine from the binary is the ultimate
  "don't ship a rule in a file the reader might not read and the writer must keep in sync —
  let the server hand it over." Doctrine-in-the-binary is derive-don't-ask applied to the
  instruction manual itself.
- **Subsumes the planned "ADR-008 workflow shrink"** originally earmarked by
  `workflow-md-is-the-new-binding-constraint-on-the-payload`: shrinking `workflow.md` is a
  consequence of relocating the doctrine, not a separate pruning exercise. (2026-07-22
  triage: that task itself was NARROWED, not subsumed — it survives as the
  bootstrap-margin instruments (canary MARGIN assertion + advisory workflow-caps check)
  and now **depends on** this work.)
- **Reinforces ADR-003 (vault-write locking) and the air-gap posture**: all vault access
  stays behind locked accessor tools; the audit's air-gap violations are closed, not added.

## Staged rollout

Doctrine-first, then the capability service, then the sync reduction, then the air-gap
cleanup. Tracked and reviewed as the task
`mcp-served-doctrine-and-thin-project-workflow` (see its plan for phase detail). No code
lands until that plan clears `/vpc-review-plan`.

## Related

- ADR-006 (Derive, Don't Ask); ADR-003 (vault-write locking); ADR-009 (inviolable core —
  the est()/OverBudget measurement fix and the shed-ladder tiering are owned by
  `enforce-adr-009-inviolable-bootstrap-core`, which this implementation depends on).
- Tasks: `mcp-served-doctrine-and-thin-project-workflow` (the implementation);
  `bootstrap-payload-exceeds-its-own-token-budget` (subsumed/closed by this work);
  `workflow-md-is-the-new-binding-constraint-on-the-payload` (narrowed 2026-07-22 to the
  bootstrap-margin instruments; depends on this work — NOT subsumed).
- Investigation: four read-only probes, 2026-07-21 (bootstrap/budget, templates/precedence,
  content-split/host-audit, MCP registry/discovery).

## Amendment (2026-09-11): Templates/ provenance is a frozen shipped-version manifest

Recorded by `template-provenance-manifest-retires-the-host-local-lock`, under the
operator's decisions of 2026-09-11. It follows
`vault-template-override-is-discarded-by-config-sync` (nothing vp does destroys a
`Templates/` override) and `upgrade-overwrite-resets-vault-template-overrides` (no
upgrade resets one; a named reset removes one). Nothing above is changed; this
records what the override-only model (Design B) now rests on.

1. **Provenance is decided by the binary alone, from a frozen manifest.** A vault
   `Templates/` copy is vp's when its bytes, keyed by sha256 after one CRLF→LF pass,
   equal the embedded copy this binary serves (matched live) or a row of
   `internal/templates/shipped.txt` for the same relpath. The manifest holds every
   version of every built-in a vp binary could ever have written into a vault: every
   version reachable from `1f3bb62` — the last commit before `a4179a5` removed the last
   writer — plus the two rows of tag `pre-rebase-501c96e`, published on the `github`
   remote. It is frozen: never regenerated, pinned by a content-hash test, and changed
   only by a deliberate, reviewed edit. Classification is relpath-scoped, and HEAD's and
   each remote tip's copy are compared as git would check them out (clean/smudge filters
   applied, a filter that cannot run an error). Residuals, all failing toward keep:
   versions only in rebased-away commits, fixups or dirty builds before the boundary;
   mirrors written by vibe-vault binaries; a built-in later removed from the corpus
   (never consulted); a remote tip read with the worktree's attributes; a copy of a
   post-boundary version, which no vp can have written.
2. **`templates.lock` is retired.** No vp from this release reads or writes it; the
   silent-adopt pre-pass, the lock-keyed decision table and the lock-entry retry of a
   failed prune commit are gone (a pending removal is now read from git). On a vault
   that is its own git repository, a `.vibe-palace/templates.lock` that is untracked and
   not ignored is removed by `vp config sync`, because it is host-local dirt that makes
   `vp vault sync` refuse. A tracked or ignored lock, and any lock on a non-git or
   nested vault, is left.
3. **New policy: an earlier shipped version is pruned on every host, with no backup.**
   On a host with no lock this used to be a keep-or-`.new` prompt. The bytes are
   vp-shipped and recoverable from vibe-palace's git history, so a backup would be a
   second copy of public bytes left as ignored litter; the prune row and commit name the
   version matched. Restoring a pruned earlier version into `Templates/` is undone by
   the next sync: a deliberate pin belongs at the project tier, or in an edited copy. A
   vault-tier pin of an unedited historical version is impossible by design.
4. **The operator's commit grant is widened (2026-09-11).** The prune commit of
   `vp config sync` carried only the paths it pruned. It may now also carry a tracked
   removal already pending in the worktree whose committed copy classifies as vp-shipped
   — on a vault that is its own repository only — and the commit message says, in a
   paragraph of its own, that vp found these removals pending and committed them because
   the committed copy of each is vp-shipped bytes. A pending removal of operator content
   is never committed.
5. **`vp config sync` never modifies operator content.** It writes no template, asks no
   question (the keep/`.new` prompt and its sidecar are gone), and removes only bytes it
   can prove it shipped; a path reached through a symlink is never followed or pruned.
6. **The project-tier steer is reversed.** Vault `Templates/` is again a supported
   vault-wide override tier, beside the per-project `Projects/<slug>/` tier: no vp
   reconciler or upgrade command changes an override in either. "Safe" means safe from
   vp's reconcilers and upgrade commands; `vp_vault_write` / `vp_vault_edit` /
   `vp_vault_move` / `vp_vault_delete` and `vp vault commit --paths .` are direct edits
   and reach any tier. An override still shadows the built-in for every project and
   misses binary-contract changes, which is why `template-drift` reports each one.
7. **The MCP surface stays at 5, decided by hazard.** This Decision's letter ("any
   change to what/where instruction files are written") reads as "bump", and an
   independent review read it that way. The operator ruled by hazard: a v5 binary
   removes only vp-built bytes, every state this release leaves is one v5 produces and
   reads, the remaining hazards (earlier-version and CRLF pruning) are this release's
   own policy, which no gate can stop, and removals stamp nothing. The reasoning is
   recorded in `internal/surface/version.go`.

## Amendment (2026-09-14): tag `pre-rebase-501c96e` deleted; rows pinned by blob hash alone

On operator instruction, tag `pre-rebase-501c96e` was deleted from the `github`
remote (and from host `12a23ab8`'s clone) — see task
`docs-and-shipped-manifest-cite-a-deleted-tag`. It was the only ref keeping
commit `501c96e` reachable from that remote; the commit is not an ancestor of
`main`. The manifest's two `# extra:` rows (blobs `17505a1e135686913bb14a1e1845fac6b4c89e25`
and `3924d6ca85fa89d5b298dd57191112bddafa8427`) are unaffected and still count
as vp's — they are pinned by blob hash alone in `internal/templates/shipped.txt`,
not by the tag's continued existence — but they are no longer re-derivable or
recoverable from the published `github` history. The Amendment (2026-09-11)
paragraph above still describes the manifest's design correctly; only the
tag's current availability has changed.
