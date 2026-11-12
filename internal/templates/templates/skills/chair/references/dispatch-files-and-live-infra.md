## Every dispatch is two files

The default dispatch contract, both topologies:

- **The Chair writes a brief file** before dispatching: binding constraints first, then
  the read-these list, the ONE question/deliverable, the output path, and a reply token.
- **The subordinate writes a findings/verdict file** as it works, and replies with ONLY
  that path plus a bounded (≤~12-line) summary — never a pasted body.
- **The Chair reads the ARTIFACT file directly, never the pane scrollback.**

This keeps the Chair's context thin and makes the loop scrollback-proof and legible: the
Chair and a reviewer read each other's files. Keep the inline prompt a SHORT POINTER at the
brief file — a short prompt also sidesteps shell-quoting hazards.

`.build/chair/<unit>-brief.md` / `-findings.md` is a fine convention in a single tree.
**Two caveats:** panes in different working trees do not share a relative dir — hand an
ABSOLUTE path both can reach; and parallel subordinates each need a PRIVATE scratch dir,
or they silently clobber each other's files. The durable record is still the vault task
(`amend`); these files are scratch.

---

## Live infrastructure — blast-radius tiers

When the work is a live infrastructure experiment (a VM, a network device, a
shared host, an external system) rather than pure code, calibrate how much
the Chair self-authorizes by tier, roughly:

1. **Disposable / brand-new resource** (a throwaway VM, a scratch file) — the
   Chair may authorize freely once the general unit is agreed.
2. **Scoped, reversible change to a component already under test** (a
   node-only restart, a config toggle with a clean revert path) — self-
   authorize after the initial plan is reviewed; each individual experiment
   still gets its own before/after evidence and a clean revert-if-it-didn't-
   help path.
3. **A shared or already-fragile component** (a peer service, anything that
   took real effort to get stable earlier in the same session) — ask first,
   even under a broad standing authorization, and say plainly that this is a
   different risk category from what came before.
4. **Host-level or platform-wide config** (a setting that affects everything
   sharing that host/bridge/cluster, not just the unit under test) — ask
   first, and have the implementor state the blast radius explicitly before
   touching anything.
5. **A system or repository the operator's org does not own** (pushing to a
   third-party remote, mutating a shared external service) — always ask, and
   have the implementor state the *exact* mechanics before executing, not
   just the intent.

State which tier an action falls in in your own head before authorizing it;
say so to the operator when it's ambiguous which tier applies.

### Gated live execution

When a unit reaches a tier-3+ live mutation, run it as a procedure, not a single authorized
action: read-only preflight → STOP and report the live blast radius → operator go on the
decisive mutation (AskUserQuestion) → execute to the next checkpoint → STOP. The decisive,
partially-irreversible step gets its own stop-the-world gate EVEN under a standing
authorization — a broad "you may proceed" does not pre-authorize the one step that cannot be
cleanly reverted.

---

## Scope before you plan

When a unit is large or vaguely bounded enough that writing a plan directly
feels premature — a "consolidate the whole X subsystem" shape, rather than a
single named defect — dispatch a read-only scoping pass first: read the
whole thing, identify what (if anything) intervening work has already made
stale or redundant, and recommend whether it is one cohesive change or
several independently shippable units with real dependency edges between
them (see "Sequencing dependent units" above). Only write the plan(s) once
that assessment exists. Weigh the scoping pass's own recommendation the same
as any other subordinate's output: read it, but decide independently rather
than adopting it by default just because it arrived first.

---

