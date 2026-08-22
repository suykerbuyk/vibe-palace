# ADR 010: The Surface Gate Stays at the Dispatch Seam; the Mutating Predicate Becomes Derived

**Status:** Accepted (2026-08-20). Supersedes the placement thesis of the task
`move-the-surface-gate-to-the-write-chokepoint`, which proposed the opposite and
is amended to match. The derivation itself is not yet implemented — this ADR
records the decision and its reasoning, not a landed mechanism.

**Amended 2026-08-20** — see *Amendment: what planning the derivation found*. The
ruling is unchanged; the amendment records a better mechanism than this page
originally described, a prerequisite it did not identify, and a trap in the
naive reading of the Decision.

**Amended 2026-08-22** — see *Amendment: what building the derivation found*.
Two statements on this page turned out to be wrong once the derivation was
built. The Decision still stands.

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

## Amendment (2026-08-20): what planning the derivation found

Planning the derivation surfaced three things this page did not anticipate. The
Decision stands; the path to it changes.

### 1. Derivation alone UNDER-gates — the trap

There are **zero funnel sinks anywhere in the pull / sync / tidy path**. A
faithful reachability derivation therefore computes `Mutating: false` for
`vp_vault_sync` and `vp_vault_tidy`.

Read naively that looks like a bonus: the MCP-pull lockout recorded above
dissolves on its own. It is not a bonus. It dissolves as a **side effect**,
while `push`, `sync` and `commit` quietly lose the gate they have today, and the
deliberate deferral on `vault commit` / `vault tidy` gets settled by accident
rather than by a ruling.

**A derivation that changes what is gated is not a refactor.** Any
implementation must diff derived-versus-declared and require an explicit ruling
on every disagreement, rather than adopting the derived answer because it was
computed.

### 2. The granularity defect is a class of three, and the seam already holds the answer

The Consequence above names `vp_vault_sync`. Two more behave identically —
verified at source:

| tool | read-only condition | site |
|---|---|---|
| `vp_vault_sync` | `action: "pull"` | `internal/tools/system_tools.go:179` |
| `vp_vault_tidy` | `dry_run: true` — `TidyScan` only, writes nothing | `internal/tools/system_tools.go:509` |
| `vp_audit_vault` | `write: false` — *"never persisted"* | `internal/tools/audit_tools.go:90` |

All three are refused whole today.

**A cheaper mechanism than this ADR considered.** `Dispatch` already calls
`validateParams(rt.compiled, params)` at `internal/mcp/tools.go:275` — *before*
`gateIfMutating` at `:279`. Schema-validated parameters are therefore in hand at
the seam, so a `func(params) bool` predicate can read `action` / `dry_run` /
`write` there: still pre-handler, still no side effects, and nothing moves into
a handler.

That is strictly better than the two options this page implied (accept the
lockout, or push the gate inside the handler for those tools). **Accepted
2026-08-20**: the gate becomes param-aware at the seam.

### 3. `Mutating` is one boolean answering two questions — splitting it is a PREREQUISITE

`Mutating` drives both the surface gate (`gateIfMutating`) and the read-only
serve filter (`cmd/vp/cmd_mcp_serve.go:178`, `srv.DeleteTools(...)` when
`!allowWrites`). §4 above notes the flag is non-deletable; that understates it.

Derivation makes the two answers **diverge** — see finding 1, where the derived
answer for `vp_vault_sync` is `false`. Wiring a derived value into
`MutatingToolNames` would therefore strip nothing and **silently expose
`vp_vault_sync` and `vp_vault_tidy` on a bearer-authed read-only HTTP surface.**

The two predicates have opposite failure modes. A false negative on the gate is
an ungated write: bad, bounded, detectable after the fact. A false negative on
the serve filter is a write tool published on a read-only surface: a security
failure, and not detectable after the fact. **The serve filter must be
fail-closed** — a tool is stripped unless affirmatively known read-only — while
the gate may be derived.

Splitting them is worth doing **even if derivation never lands**.

### Ordering, ruled 2026-08-20

1. **Split the two predicates.** Prerequisite; a pure refactor today, since both
   answers currently agree for every tool.
2. **Make the gate param-aware** at the Dispatch seam, closing all three
   lockouts.
3. **Derive the predicate** last, once it can no longer under-gate — and with
   every derived-versus-declared disagreement ruled on explicitly.

### Method note — do not trust a reachability result that has not asserted its sinks

While measuring, an x/tools CHA call graph **silently dropped a declared sink**:
`storage.appendUnderLock` was absent from the graph despite two live call sites,
4 of 5 sinks found, cause undiagnosed. It was caught only because the count was
printed.

Any derivation must assert **sink presence as a hard error** before trusting
reachability output. A call graph missing a sink reports the commands that reach
it as clean, and reports it in exactly the same shape as a correct result.

## Amendment (2026-08-22): what building the derivation found

The derivation was built (step 3 of the ordering above). It corrected two things
this page asserted.

### 1. "Derived" means PINNED, not GENERATED — because the git ruling collides with it

The Decision says the predicate "becomes a fact a check derives ... with the
derivation pinned so a divergence fails the build." That is ambiguous between
two implementations, and only one of them is safe:

- **Generate** — the derived answer becomes the flag.
- **Pin** — the flag stays hand-declared, and a check fails the build on any
  disagreement that nobody has ruled on in writing.

**Generate is wrong**, for a reason neither this page nor the plan anticipated:
**the git channel is exempt from the funnel** (ruled by verb, 2026-08-20). A
tool or command whose vault writes are git-mediated therefore reaches no funnel
sink and derives as non-mutating — *correctly, by the funnel's own definition*.
`vp_vault_sync`, `vp_vault_tidy` and `vault commit` are exactly that shape, and
generating would have silently ungated all three.

Two rulings that were each right in isolation combine into a hole. Under **pin**
the same disagreement becomes a required written exception — *"derives false
because its writes are git-mediated and git is exempt from the funnel; stays
gated"* — a reviewable artifact instead of a silent behaviour change.

**Implemented as pin.** No command's or tool's effective gate changed:
`cmd/vp/commands.go`, `internal/tools/mutating.go` and the MCP surface golden
were all untouched by the derivation landing.

### 2. `ungated-vault-writer` is KEPT, not superseded — this page predicted wrong

The Consequences above state that the syntactic rule is "superseded rather than
deleted." Measured, that is false: **the two anchor sets do not overlap.**
`surface.WriteFormat` (the real import cycle) and `storage.DeleteDrawer`
(`os.OpenFile(O_RDWR)`) reach no funnel primitive at all and are visible *only*
to the syntactic rule.

A second reason, and not hypothetical: the syntactic rule needs no SSA, so it
survives a toolchain the SSA builder cannot handle — which has already happened
once (x/tools v0.43.0 panics on Go 1.27's stdlib unless scoped to this module's
packages).

The rule's narrowed job is recorded in its own doc comment. It is not left green
and pointless; it covers what the derivation structurally cannot see.

### The derivation independently rediscovered a known defect

`vp_refresh_index` derives **true** on a genuine path to `atomicfile.Write`
while registered `Mutating: false`. That disagreement is recorded under protest:
the declared answer is wrong and undefended, left in place only because flipping
it moves the surface golden.

It is the same defect a human refused to retire on evidence at iteration 339
(`refresh-index-reports-rebuilt-while-writing-nothing`). A machine and a person
reaching it independently, from different directions, is the strongest available
evidence that the mechanism works.

### Cost, and the gate that keeps it honest

Type-checking the module costs ~10 s alone and ~49 s under `-race -cover`, so
the derived-gate check self-skips under `-short`.

**That skip was landed together with the thing that runs it**, because verifying
first showed that *nothing* in CI ran the suite without `-short`: the `test` job
is `-short`, `windows-lock` is `-short` and `-run`-filtered, and `make test-full`
/ `make cover-full` are manual targets no workflow invokes. A bare
`testing.Short()` skip would have left the rule running in no automated context
at all — green, pointless, and the exact failure mode this page refuses for
`ungated-vault-writer`.

So it runs via `make source-audit` and a dedicated `source-audit` CI job on every
push and pull request. **Delete either and the rule runs nowhere.** The syntactic
rule stays ungated and still runs under `-short`.

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
