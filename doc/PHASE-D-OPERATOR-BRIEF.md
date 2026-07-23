# Vibe-Palace Cutover: Where We Are and What We're Doing Next

**Audience:** John (human operator)
**Written:** 2026-05-09 (Phase C complete) · **Revised:** 2026-05-31 (Phase D
closed via retrospective reconciliation 2026-05-30)
**Source plan:** `doc/RESUMPTION-PLAN.md`

> **Historical document (May 2026).** Point-in-time operator briefing for the
> cutover; kept as a record. Claims and counts here describe that moment, not
> the current system.

A complete operator briefing — what vibe-palace is, what we shipped through
Phase C, how Phase D landed, and the single go/no-go decision now in front
of you.

---

## 1. The Big Picture (one paragraph)

You've been using **vibe-vault** for ~232 iterations as your AI-session
memory + context system. It works, but the friction has plateaued: bootstraps
cost ~25K+ tokens, every "wrap" dispatches LLM calls to compose narratives,
the architecture is married to Claude Code, and multi-machine vault-narrative
conflicts keep recurring. Three weeks ago you decided to *resume*
**vibe-palace** — a younger codebase you put down at iter 70 — because
vibe-palace was *designed* for the endpoint that vibe-vault is slowly
drifting toward: a mechanical Go binary that owns the vault, with
MCP/HTTP/CLI all calling the same service layer. **The cutover sprint** moves
your daily session-capture and context-bootstrap from vibe-vault to
vibe-palace, while keeping vibe-vault fully alive as a safety net throughout.
Phases A–D are now complete; the one remaining decision is whether to accept
the Phase D retrospective and run the ~1-hour hook cutover (Phase E).

---

## 2. The Six-Phase Cutover (Phase D closed; Phase E pending your go/no-go)

```
A ── B ── C ── D ── E ── F
✅   ✅   ✅   ✅   ⏸    ⏸
```

| Phase | What it does | Status |
|-------|--------------|--------|
| **A** | HEALTH.md refresh + repoint vibe-palace's vault to the shared `~/obsidian/VibeVault` | ✅ shipped (commit `0008d87`, iter 78) |
| **B** | Make migration tolerant of malformed YAML frontmatter (pre-existing data quality issue) | ✅ shipped (commit `a9a9652`, iter 79) |
| **C** | Run the real migration; verify search/KG/bootstrap_context end-to-end | ✅ shipped (iter 82, migration executed 2026-05-12) |
| **D** | **Run vibe-palace + vibe-vault in parallel; prove vibe-palace works for daily use** | ✅ **CLOSED 2026-05-30 via retrospective reconciliation** (parity-or-better; see §3) |
| **E** | Cut hooks over: remove `vv hook`, install `vp hook`, point IDEs at vibe-palace MCP | ⏸ **pending operator go/no-go** (hook cutover, ~1h) |
| **F** | Quiesce the vibe-vault repo (no deletion; archive label + readonly) | ⏸ blocked on E |

**State at HEAD (iter 84):**

- Phase C migration executed **2026-05-12**: **925 sessions** imported into
  the shared vault, building **28,338 drawers / 20,561 entities / 50,909
  triples**
- Migration-counter-display fix shipped 2026-05-30 (commit `f999329`),
  wiring up the drawer/entity/triple counters in the vibevault path
- `vp_search` and `vp_bootstrap_context` verified end-to-end during Phase C;
  bootstrap returns structured JSON at ~10× structural reduction vs
  vibe-vault's ~107KB markdown blob
- **Phase D retrospective (2026-05-30):** vibe-palace at parity-or-better
  with the authoritative system — **98.0% ground-truth session coverage vs
  vibe-vault's 94.9%, Jaccard 96.9%, ZERO vv-only misses**

---

## 3. How Phase D Landed (closed 2026-05-30 via retrospective reconciliation)

Phase D was **NOT** a coding sprint — no features to ship, no commits
required. It was the dogfooding gate: prove vibe-palace can stand on its own
*before* Phase E cuts hooks over and vibe-vault stops capturing new sessions.

**How it closed.** Rather than open a fresh forward 7-day window, the
operator chose **retrospective reconciliation** against 18 days of already-
existing parallel-capture data (both `vv hook` and `vp_capture_session` had
been running side-by-side since the Phase C migration). With that much real
parallel data on hand, a forward window would have re-derived evidence we
already possessed. The reconciliation compared vibe-palace's capture set
against vibe-vault — the authoritative system — over that period.

**The result: parity-or-better.**

| Metric | vibe-palace | vibe-vault | Verdict |
|--------|-------------|------------|---------|
| Ground-truth session coverage | **98.0%** | 94.9% | vibe-palace ahead |
| Jaccard overlap | **96.9%** | — | near-total agreement |
| vv-only misses (sessions vibe-vault caught that vibe-palace lost) | **0** | — | zero data loss |

Findings are recorded in
`agentctx/dogfood-log.md §Retrospective Reconciliation`.

### Acceptance criteria — how each was settled

The five ACs that gated Phase E:

- **AC1 (restart context useful)** — satisfied; bootstrap parity established
  across the parallel-capture period.
- **AC2 (search beats grep+scroll on real "what did we decide" queries)** —
  **forward-only, carried.** Real search-friction wins cannot be
  retrospectively reconstructed from capture data; this AC is carried, not
  blocking.
- **AC3 (captures land cleanly)** — **satisfied from hard data:** zero
  vv-only misses means every session vibe-vault caught, vibe-palace also
  caught cleanly.
- **AC4 (auto-capture ≥80% of sessions)** — **satisfied from hard data:**
  98.0% ground-truth coverage clears the 80% bar with margin.
- **AC5 (wraps cost fewer tokens)** — **split.** The *structural* reduction
  (~10× smaller bootstrap payload) is satisfied. The *per-wrap token median*
  is **forward-only, carried** — it cannot be reconstructed from
  retrospective capture data.

**Net:** AC3, AC4, and AC5-structural are satisfied from hard retrospective
data. AC2 and the AC5 per-wrap token median are **forward-only** — not
retrospectively reconstructable, so they are **carried, not blocking** the
advance to Phase E.

---

## 4. The One Decision In Front of You

Phase D is closed. There is no ongoing dogfood journaling to maintain. The
single open item is a **go/no-go on advancing to Phase E**:

**Option A — Accept the retrospective and advance to Phase E now.**
The hard-data ACs (AC3, AC4, AC5-structural) are satisfied with margin —
98.0% coverage and zero vv-only misses are strong parity evidence. AC2 and
the AC5 per-wrap token median are forward-only and would be carried as
follow-up. Phase E is the ~1-hour hook cutover (see §7).

**Option B — Hold for a short forward window.**
If you want positive confirmation of the two forward-only ACs (real
search-friction wins and the per-wrap token median) *before* cutover rather
than carrying them, hold for a brief forward dogfood window and collect that
evidence first. The tradeoff: it delays Phase E to re-confirm criteria the
retrospective could not, by construction, address.

Either way vibe-vault keeps running as the safety net until Phase E
actually executes. **No data is at risk while this decision is open.**

---

## 5. What I (the AI) Will Do

| When | What |
|------|------|
| On your go (Option A) | Run the Phase E hook cutover steps in §7, one at a time, verifying each before the next |
| On your hold (Option B) | Re-open the dogfood-log scaffold and start tallying the two forward-only ACs |
| Each `/restart` | Bootstrap from `vp_bootstrap_context` |
| Each `/wrap` | Run mechanically; report context% delta as I see it |
| Either path | Keep AC2 + the AC5 per-wrap median tracked as carried follow-up until confirmed |

I will **not**: spontaneously edit code, commit on your behalf, or advance
the phase clock past Phase D without your sign-off on the §4 decision.

---

## 6. Residual Risk (what's still open and how it's covered)

Phase D's hard-data ACs passed, so the failure modes that gated it no longer
apply. What remains is the carried/forward-only work and the cutover itself:

| Item | Status / coverage |
|------|-------------------|
| AC2 — real search-friction wins | Forward-only, **carried.** Not retrospectively reconstructable. If a real "what did we decide" query later surfaces poorly, that points at embedding/chunking quality — investigate then, not a cutover blocker on the retrospective. |
| AC5 per-wrap token median | Forward-only, **carried.** Structural reduction (~10×) already holds; the per-wrap median is confirmed only by running real wraps post-cutover. |
| Phase E hook cutover | The ~1h mechanical change in §7. `vp hook` already runs in parallel, so cutover mostly removes `vv hook` and repoints config. |
| Data safety | vibe-vault keeps running until Phase E executes. Zero vv-only misses in the retrospective; no data is at risk. |

In all cases the worst case is "Phase E is delayed" — never data loss.

---

## 7. After Phase D — What Phases E and F Look Like

### Phase E (≈ 1 hour, on your go-ahead)

1. Edit `~/.claude/settings.json` → remove the `vv hook` entries
2. Run `vp hook install` → installs the `vp hook` entries (already running in
   parallel; this just becomes the only one)
3. Verify next session captures only via `vp` paths
4. Update `~/code/CLAUDE.md` and per-project `CLAUDE.md` files: replace
   `vv_bootstrap_context` calls with `vp_bootstrap_context`
5. Point Zed and Claude Code MCP configs at `vp` as the primary server

### Phase F (asynchronous, no time pressure)

1. The vibe-vault repo at `~/code/vibe-vault` stays where it is — no
   deletion, ever
2. The historical `Projects/vibe-vault/agentctx/` and `iterations.md` files
   in the shared vault stay readable by vibe-palace
3. Add a top-of-file note to vibe-vault's resume.md: *"Project archived
   2026-MM-DD; new work continues at ~/code/vibe-palace."*
4. Optionally, archive vibe-vault on GitHub after a 30-day quiescence
   period

---

## 8. Reference Pointers

| File | Purpose |
|------|---------|
| `doc/RESUMPTION-PLAN.md` | The full 6-phase cutover plan (this is the source of truth) |
| `agentctx/tasks/phase-d-parallel-operation.md` | Phase D plan as a tracked task |
| `agentctx/dogfood-log.md` | Working journal; **§Retrospective Reconciliation** holds the Phase D closure findings |
| `agentctx/tasks/migration-counter-display-fix.md` | Small follow-up bug found during Phase C; not blocking |
| `doc/HEALTH.md` | Project health snapshot (refreshed in Phase A) |
| `doc/ARCHITECTURE.md` | Service-layer / storage / KG architecture |
| `doc/PRD-vibe-palace.md` | Full product requirements |

---

## 9. TL;DR

> **Where we are:** Phases A/B/C/D done. Phase D **closed 2026-05-30 via
> retrospective reconciliation** against 18 days of parallel-capture data —
> vibe-palace is at parity-or-better with vibe-vault: **98.0% vs 94.9%
> ground-truth session coverage, Jaccard 96.9%, zero vv-only misses.**
> AC3/AC4/AC5-structural are satisfied from hard data; AC2 and the AC5
> per-wrap token median are forward-only and **carried, not blocking**.
>
> **The one decision:** accept the retrospective and run the ~1-hour Phase E
> hook cutover now (Option A), or hold for a short forward window to confirm
> the two carried ACs first (Option B). See §4.
>
> **What's at risk:** nothing — vibe-vault keeps running as the safety net
> until Phase E actually executes.
>
> **The whole point:** the AI session-memory system that you've been
> building friction against for ~50 vibe-vault iterations finally has a
> clean, decoupled, mechanical foundation underneath it — and the data now
> shows it captures your work as well as or better than the system it
> replaces.
