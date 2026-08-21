# ADR 010: The Surface Gate Stays at the Dispatch Seam; the Mutating Predicate Becomes Derived

**Status:** Accepted (2026-08-20). Supersedes the placement thesis of the task
`move-the-surface-gate-to-the-write-chokepoint`, which proposed the opposite and
is amended to match. The derivation itself is not yet implemented — this ADR
records the decision and its reasoning, not a landed mechanism.

## Context

The MCP surface gate refuses a vault write when the vault's recorded surface
version exceeds this binary's. On the CLI side it fires from `preRun`
(`cmd/vp/main.go:71`, `surfaceGate`) and selects between `EnforceFailStop` and
`EnforceWarnOnly` on `cmd.MutatesVault` — a boolean set by wrapping a command
constructor in `mutates()` at registration (`cmd/vp/commands.go`). On the MCP
side it fires from `Registry.gateIfMutating` (`internal/mcp/tools.go:309`),
which keys off the `Mutating` flag on the registered tool.

Both predicates are **hand-typed**. `vp commands upgrade` and `vp skills
upgrade` wrote the vault ungated for months because nobody typed `mutates()` on
them. The repair typed the annotation and then added a 447-line source-audit
rule, `ungated-vault-writer`, to catch the next omission — a check whose
subject is *whether a human remembered to type an annotation*.

That is the pattern the `first-principles` epic exists to delete, and PRD §1.11
states the principle directly: *a rule that matters becomes a gate in the tool
it governs; prose is reserved for what genuinely cannot be enforced*. An
annotation is one step above prose — a hand-maintained assertion **about**
behaviour, sitting beside the behaviour, free to disagree with it. It did.

The proposed remedy was to move the fail-stop to the vault-write primitives, so
that an un-annotated command would be covered automatically and the annotation,
the ratchet policing it, and the whole missed-command class would stop existing.
A plan review and an independent adversarial pass found that remedy unsound at
its core, and a subsequent architectural ruling made a different placement
correct for reasons the original framing could not see.

### What the review established

- **`surface.StampForPath` is a post-write hook.** In `internal/atomicfile` the
  stamp runs *after* the rename has committed the file, and three of its four
  non-test callers have no error return in their signatures at all. Its own doc
  says callers must not fail the primary write on a stamp error. A fail-stop
  placed there is a post-hoc alarm, not a gate.
- **The named primitive set was not the chokepoint.** A census found roughly
  thirty in-vault mutations across seventeen packages that reached none of it —
  including `vp config sync`'s prune path and `vp audit vault --accept`, both of
  which are gated *today* at the command level and would have become **ungated**
  under the proposal. The proposal would have reduced coverage.
- **Two of the six named primitives were already redundant**, having been
  migrated to the shared whole-file primitive without the anchor set's comment
  being updated.
- **The stamp write is unguardable by construction.** `internal/surface` cannot
  import `internal/atomicfile`, because `atomicfile` imports `surface`; the
  stamp therefore performs its own temp-write-and-rename outside any candidate
  primitive set. This cannot be closed by any placement list.

Phases 1–3 of the remediation nevertheless landed and are kept (see
*Consequences*): one cross-package write-discipline rule replacing three
package-local pins, an append primitive that owns its stamp, and one removal
sink beneath one policy layer. Those are funnel work, and they retain their
value under this decision for reasons given below.

## Decision

**The surface gate stays at the dispatch seam. The `MutatesVault` / `Mutating`
predicate becomes derived from funnel reachability rather than hand-typed.**

Concretely: `surfaceGate` in `cmd/vp/main.go` and `Registry.gateIfMutating` in
`internal/mcp/tools.go` remain the two enforcement points. What changes is that
the boolean they consult stops being an assertion a human types and becomes a
fact a check derives — *does this command or tool reach a vault-write sink?* —
with the derivation pinned so a divergence fails the build rather than shipping.

The gate does **not** move to `atomicfile.Write`, to the removal sink, or to the
append primitive.

### 1. The write site has no access to intent

This is the decisive local argument, and it is not a matter of taste: two
rulings already accepted in this tree encode intent at the command level.

- **`vp hook`'s capture must never fail-stop.** `internal/surface/gate.go`
  documents `EnforceWarnOnly` as existing *"so an out-of-date binary can never
  block a capture or a read"*, and the accepted source-audit baseline states the
  trade plainly: a capture that fail-stops is a capture that is **lost**, and
  losing history to protect a schema is the wrong trade.
- **`vp mcp` must have no startup gate.** The server stays up and the
  remediation surfaces in-band as a tool error payload, so that
  `vp_bootstrap_context` remains reachable exactly when an agent needs to
  discover what is wrong. Writes are gated per tool, not per process.

`atomicfile.Write` cannot distinguish a capture from an upgrade command. A gate
placed there must therefore either **violate both rulings**, or accept an
intent flag passed by each caller — which is the annotation again, relocated
from 26 command registrations to roughly 30 write call sites. That is more
hand-typed assertions, not fewer, distributed more widely and reviewed less.

The command and tool registration seam is where intent is *already* expressed,
because it is the only layer that knows what the caller is trying to do.

### 2. The dispatch seam is the API boundary the product is heading toward

The target architecture is settled: **the server owns the vault and clients are
thin.** That reframes the problem the gate exists to solve.

Today every host writes the vault directly, which is precisely why a stale
binary can corrupt a newer vault. Behind a Web API there is exactly one writer —
the server — and its binary always matches its own vault by construction. The
binary-versus-vault skew that motivates the gate **largely dissolves**, and what
remains is client-versus-server protocol compatibility: a version negotiated
once per connection, not a filesystem stamp scanned per write.

The dispatch seam is already that boundary, and its **ordering** is already
right: `gateIfMutating` runs **before** `rt.tool.Handler` — a per-request
refusal with no side effects, which is what an HTTP API must do. Its
**granularity** is not right yet, and that is a defect this decision inherits
rather than removes; see *The gate is per-tool, and one tool multiplexes* in
Consequences. `vp mcp serve` already carries the shape: bearer auth, a
per-request vault in the request context, and `MutatingToolNames` used as the
authorization predicate for read-only mode.

A write-site gate would be built deep inside the one component that survives the
transition, guarding against a skew that can no longer occur, and would surface
failures *after* side effects had begun — the worst available API shape.

### 3. Derivation is now cheap, because the funnel is real

The original objection to keeping the predicate was that a hand-maintained
anchor set cannot see interface dispatch: `vp config sync` reaches its writer
through `[]reconcile.Reconciler`, and no syntactic model recovers the receiver.
That objection was correct when the anchor set was six hand-listed entries
declaring themselves *"a floor, not a census"*.

It is much weaker now. Phases 1–3 consolidated the write paths into a small
number of real sinks, so reachability has few roots and they correspond to
actual behaviour. Where syntactic reachability still falls short, a type-checked
call graph closes it at measured cost: `golang.org/x/tools` is already present
in `go.sum` transitively, adds no downloaded module, and — scoped to this
module's packages — leaves the shipped binary byte-identical. `internal/sourceaudit`
is not linked into `cmd/vp` at all, so this is a test-time cost only.

### 4. The MCP `Mutating` flag is not deletable

`Mutating` has three consumers, not one: the surface gate, the surface golden
pin, and — critically — the read-only filter in `vp mcp serve`, which strips
mutating tools from the served set unless `--allow-writes` is passed. It is a
**security boundary**. Any future work that "deletes the annotation" must
exclude it explicitly.

## Consequences

- **`mutates()` and `Mutating` survive, but stop being assertions.** Derived and
  pinned, the predicate can no longer silently disagree with behaviour. The
  epic's objection was to hand-maintenance, not to the predicate's existence.
- **`ungated-vault-writer` is superseded rather than deleted.** Its job — find
  commands that write without being gated — becomes the derivation itself. It
  should not be left green and pointless beside its replacement.
- **The funnel work keeps its value under a different justification.** Routing
  every mutation through a small set of primitives is no longer needed *so the
  gate has one place to go*; it is needed because **one auditable server-side
  write path** is what makes a hosted vault tractable — transactional behaviour,
  audit logging, quota, and any non-POSIX backend all depend on it. Remaining
  routing phases should be motivated that way, or descoped honestly.
- **The gate is per-tool, and one tool multiplexes actions — so the recovery
  path is currently locked out.** `vp_vault_sync` is in `MutatingToolNames`
  (`internal/tools/mutating.go:48`), the gate fires at
  `internal/mcp/tools.go:279` before `rt.tool.Handler` at `:283`, and the
  `switch p.Action` that would distinguish `pull` from `push` sits inside that
  handler at `internal/tools/system_tools.go:179`. So `action: "pull"` is
  refused identically to `action: "push"`, and a host whose binary is behind
  the vault cannot pull over MCP at all — while the CLI equivalents are
  registered bare (`cmd/vp/commands.go:107,108,130`) and can. `restart.md`
  Step 1 uses the MCP call, so the operation that would deliver the fixed
  binary is the operation being refused.

  **This is a requirement on the derivation, not a footnote.** A derivation
  that stays per-tool reproduces the lockout *exactly*: `vp_vault_sync`
  genuinely reaches a write sink on two of its three actions, so the derived
  answer would be `Mutating: true` and would be correct, and the recovery path
  would stay broken. The derivation must therefore be action-granular where a
  tool multiplexes actions, or the gate must move inside the handler for those
  tools specifically. This is the one place where "derive the predicate" is not
  sufficient on its own.

  It does have an end-of-life: under server-owns-vault the client does not pull
  at all, so the lockout dissolves with the transition. That is not a reason to
  defer it. It is live for the whole local-model lifetime, it sits on the
  restart path every agent runs, and it fails in the direction that prevents
  its own repair. Note also that the rejected placements are worse on this
  axis, not better — a write-site gate would refuse the pull's writes too, with
  side effects already begun.

- **The wrong-vault defect is not fixed by this decision.** `surfaceGate`
  resolves the *global* vault while a command may write a project-overridden
  one, so a redirected project is gated against a vault it is not writing.
  Under server-owns-vault this becomes a per-request binding concern — a
  tenant-isolation question — and should be fixed there rather than by moving
  the gate.
- **No coverage is lost.** `vp config sync --prune` and `vp audit vault --accept`
  keep the gate they have today, which the rejected placement would have removed.
- **The per-write cost never arrives.** `CheckCompatible` is roughly 510 µs per
  call, unmemoized, and scales with vault *breadth* rather than write volume; a
  per-write gate measured 37× wall-clock and about 930× per write on a real
  vault. Keeping the gate per-command and per-request avoids both that cost and
  the memoization-staleness hazard it would have forced.
- **Multi-write atomicity is preserved.** A refused tool today has done nothing.
  A write-layer gate would have left earlier files committed when the refusal
  arrived partway through a multi-write tool.

## Alternatives considered

**Move the gate to the write primitives** (the original thesis). Rejected on
§1 and §2: it cannot see intent, it would have violated two accepted rulings or
re-created the annotation more widely, it would have *reduced* command coverage
as scoped, and it builds machinery the client-server transition makes redundant.

**Gate at the vault handle** (`storage.NewVault`, with intent attached at
construction). Genuinely attractive locally: all three `OpenVault*` variants
funnel through one constructor, intent attaches once at a boundary that knows
it, the handle knows its own root — which fixes the wrong-vault defect — and one
check per handle removes the performance problem. Rejected as the *gate*
decision because a dozen of the whole-file sink's callers hold no vault handle,
so coverage would be partial until those paths are refactored, and because under
server-owns-vault the per-request handle is better motivated as **tenant
isolation** than as version skew. The shape is right; the justification belongs
to a different decision, at the time multi-tenancy is actually built.

**Hand-model the interface-dispatch table** — enumerate `reconcile.Reconciler`'s
implementers so the existing rule can follow `Apply`. Refused before it was
written: it adds a *second* hand-maintained table beside the anchor set, and
both become dead code the moment derivation lands.

## Related

- `doc/adr/003-vault-write-locking.md` — the per-path lock and CAS contract the
  funnel primitives sit on; read before touching any writer.
- `doc/adr/006-derive-dont-ask.md` — the same principle applied to session
  state. This ADR is that ladder pointed at the mutating predicate: the server
  can compute the answer, so it must not ask a human to type it.
- `doc/PRD-vibe-palace.md` §1.11 — a rule that matters becomes a gate in the
  tool it governs.
- Task `move-the-surface-gate-to-the-write-chokepoint` — the plan this decision
  overrides, kept for its census and its review findings.
