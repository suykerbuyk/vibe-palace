# Vibe-Palace Cutover: Where We Are and What We're Doing Next

**Audience:** John (human operator)
**Written:** 2026-05-09 (Phase C complete, Phase D clock starts today)
**Source plan:** `doc/RESUMPTION-PLAN.md`

A complete operator briefing — what vibe-palace is, what we just shipped,
what Phase D is trying to prove, and what you specifically need to do over
the next ~week.

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
MCP/HTTP/CLI all calling the same service layer. **We are now in the cutover
sprint** that moves your daily session-capture and context-bootstrap from
vibe-vault to vibe-palace, while keeping vibe-vault fully alive as a safety
net throughout.

---

## 2. The Six-Phase Cutover (you are entering Phase D)

```
A ── B ── C ── D ── E ── F
✅   ✅   ✅   ▶️   ⏸    ⏸
```

| Phase | What it does | Status |
|-------|--------------|--------|
| **A** | HEALTH.md refresh + repoint vibe-palace's vault to the shared `~/obsidian/VibeVault` | ✅ shipped (commit `0008d87`, iter 78) |
| **B** | Make migration tolerant of malformed YAML frontmatter (pre-existing data quality issue) | ✅ shipped (commit `a9a9652`, iter 79) |
| **C** | Run the real migration; verify search/KG/bootstrap_context end-to-end | ✅ shipped today (iter 82 wrap + verified just now) |
| **D** | **Run vibe-palace + vibe-vault in parallel for ≥7 days; prove vibe-palace works for daily use** | ▶️ **clock started today (2026-05-09)** |
| **E** | Cut hooks over: remove `vv hook`, install `vp hook`, point IDEs at vibe-palace MCP | ⏸ blocked on D |
| **F** | Quiesce the vibe-vault repo (no deletion; archive label + readonly) | ⏸ blocked on E |

**Today's rough state at HEAD:**

- Real migration completed: 36 projects, 207 new sessions imported into the
  shared vault
- Vibe-vault knowledge graph in vibe-palace: **3,899 entities / 11,070
  triples**
- `vp_search` smoke queries returned strong hits (top scores 0.70–0.76)
- `vp_bootstrap_context` returns ~11KB structured JSON vs vibe-vault's
  ~107KB markdown blob — **~10× structural reduction**

---

## 3. What Phase D Is Actually Trying to Prove

Phase D is **NOT** a coding sprint. There are no features to ship and no
commits required. It is a **dogfooding period** — you use vibe-palace
alongside vibe-vault for a week, and we collect evidence that vibe-palace
can stand on its own. This evidence is required because once we cut hooks
over in Phase E, vibe-vault stops capturing new sessions. We need to know
vibe-palace handles real day-to-day work *before* that cutover.

The 5 acceptance criteria below are what we're measuring. Each must hold for
**≥7 consecutive days** before Phase E proceeds.

### AC1 — Restart context is *useful*

After every `/restart`, ask yourself: **did the bootstrapped context contain
everything I needed to continue work, or did I have to fetch additional
files?**

- ✅ tag `bootstrap-ok` when it had everything
- ❌ tag `bootstrap-gap: <what was missing>` when something was missing
- **Pass condition:** ≥80% of restarts tagged `bootstrap-ok`, no recurring
  gap pattern

### AC2 — Search beats grep+scroll for "what did we decide" questions

When a question arises naturally during work like *"what did we decide about
X?"* or *"where did we land on Y?"*, run
`vp search -p <project> "<query>"` **first**, before grepping iterations.md
or scrolling.

- Record query + top-hit score + whether the relevant decision actually
  surfaced
- **Pass condition:** ≥3 distinct real queries return relevant results in
  top-5 hits during the window

> **Important:** Phase C's three smoke queries (wrap-model-tiering / Grok
> provider / session-source-interface) **do not count** — those were
> contrived. AC2 needs queries that emerged from real friction.

### AC3 — Captures land cleanly

Every session capture (manual `/wrap` or auto via hook) must succeed without
schema/storage errors.

- **Pass condition:** zero capture failures over the window. One transient
  error is acceptable IF root cause is found and a regression test added.

### AC4 — Auto-capture catches ≥80% of sessions

The IDE hook (`vp hook`) is supposed to fire on every Claude Code
`SessionEnd` / `Stop` / `PreCompact` event and on equivalent Zed events,
automatically capturing the session without you doing anything.

- **End-of-day count:** how many Claude Code + Zed sessions ended today, vs
  how many `vp` claim sentinels exist in `.vibe-palace/claimed-*`
  directories?
- **Pass condition:** aggregate over the week, claimed/total ≥ 0.80

### AC5 — Wrapping costs fewer tokens than vibe-vault did

Vibe-vault's median wrap (per iter 167 reference) burned ~28K tokens of
session budget — the bootstrap pulled ~107KB of context, and `/wrap` itself
dispatched LLM calls to compose narratives. Vibe-palace's wrap should be
measurably cheaper because most of the work is mechanical.

- **Two tracks:**
  - **Structural (one-time):** ✅ already pass — 9.6× reduction at natural
    payload size, up to 20× under tight budget
  - **Per-wrap journal:** for each `/wrap` you do, note Claude Code's
    context-meter delta (% before → % after) so we can compute approximate
    token burn (200K window × Δ%)
- **Pass condition:** structural ≥3× reduction (already met); median
  per-wrap burn ≤20K tokens over the window

---

## 4. What YOU Need to Do (the only operational ask)

Vibe-palace is already running automatically. The five things you actively
own during Phase D:

### (a) Keep working normally

Use Claude Code, Zed, your terminal — same as always. The hook is installed,
sessions auto-capture, vibe-vault keeps its own copy as safety net. **Do
not** uninstall `vv hook` until Phase E.

### (b) Journal in `agentctx/dogfood-log.md`

Every `/restart`, `/wrap`, real "what did we decide" search query, or
capture failure → append a row to the appropriate section of
`~/obsidian/VibeVault/Projects/vibe-palace/agentctx/dogfood-log.md`. The
scaffold is already written; the templates are filled in. **You can ask me
to log entries for you** — just tell me "log a bootstrap-ok for this
session" and I'll edit the file.

### (c) Tally hook coverage at end of day

Once a day (~30 seconds): count Claude Code transcripts created today
(`ls ~/.claude/projects/*/...jsonl`), count Zed transcripts (path TBD — see
below), count `claimed-*` sentinels for active repos. Append a row to the
§Hook-Coverage table.

### (d) Confirm the Zed transcript path on Day 1

The dogfood-log has an action item: figure out where Zed stores its
conversation transcripts so we can tally AC4 properly. Likely candidates
listed in the doc; first time you use Zed today or tomorrow, check which
one is actually populated.

### (e) Flag any capture failures immediately

If `vp_capture_session` returns an error, or if a session you expected to
be captured isn't in the vault — tell me right away. AC3 only tolerates
failures that have a found root cause + regression test. Silent failures
kill the cutover.

---

## 5. What I (the AI) Will Do

| When | What |
|------|------|
| Each `/restart` | Bootstrap from `vp_bootstrap_context`, prompt you for a 1-line journal note |
| Each `/wrap` | Run mechanically; report context% delta as I see it; offer to write the dogfood-log entry |
| Mid-week | Look at the AC tally and either confirm we're tracking to advance Phase E, or surface gaps |
| End of window | Tally the 7 days; if all green, propose retiring the Phase D task and starting Phase E |
| If anything fails AC | Investigate, propose root cause + fix, get your approval before any code change |

I will **not**: spontaneously edit code, commit on your behalf, advance the
phase clock without your sign-off, or skip the journal entries that produce
evidence.

---

## 6. What Could Go Wrong (and what we do about it)

| Scenario | Response |
|----------|----------|
| AC1 fails: bootstrap consistently missing something | Identify the recurring gap → file a `vp_bootstrap_context` enrichment task → land it → restart the 7-day clock |
| AC2 fails: searches don't surface relevant decisions | Likely an embedding-quality or chunking issue; investigate before cutover (could indicate the migration didn't index everything correctly) |
| AC3 fails: any capture error | Find the root cause and add regression test before retrying. Cutover is gated on data integrity. |
| AC4 fails: <80% hook coverage | The auto-capture-ide-hooks task is shipped but field-validation still pending sign-off — failure here means investigate hook installation, not a cutover blocker |
| AC5 fails: per-wrap burn doesn't drop | Surprising — structural reduction is already 9.6×. Would mean something in the wrap workflow is undoing the structural gain. Investigate. |
| **Window drags >14 days without all green** | **Stop and review.** That signals a design assumption is wrong, not just a small gap. |

In all failure modes: vibe-vault keeps running. No data is at risk. Worst
case is "Phase E is delayed."

---

## 7. After Phase D — What Phases E and F Look Like

### Phase E (≈ 1 hour, when ACs are green)

1. Edit `~/.claude/settings.json` → remove the `vv hook` entries
2. Run `vp hook install` → installs the `vp hook` entries (already done in
   dogfooding; this just becomes the only one)
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
| `agentctx/dogfood-log.md` | The week's working journal — AC evidence accumulates here |
| `agentctx/tasks/migration-counter-display-fix.md` | Small follow-up bug found during Phase C; not blocking |
| `doc/HEALTH.md` | Project health snapshot (refreshed in Phase A) |
| `doc/ARCHITECTURE.md` | Service-layer / storage / KG architecture |
| `doc/PRD-vibe-palace.md` | Full product requirements |

---

## 9. TL;DR

> **Where we are:** Phases A/B/C done. Vibe-palace is now ingesting your
> shared vault and answering search/KG/bootstrap queries. The infrastructure
> works.
>
> **What we're doing next:** Use vibe-palace as your daily driver alongside
> vibe-vault for ≥7 days. Journal what works and what doesn't in
> `agentctx/dogfood-log.md`. Five acceptance criteria need to hold for the
> whole week.
>
> **What you do:** Work normally. Tell me about restarts, search hits, and
> any capture failures. Do an end-of-day count of sessions vs claim
> sentinels.
>
> **What success looks like:** On 2026-05-16 or later, the dogfood-log
> shows ✅ across all 5 ACs for ≥7 consecutive days. We retire the Phase D
> task and run the ~1-hour Phase E hook cutover.
>
> **The whole point:** the AI session-memory system that you've been
> building friction against for ~50 vibe-vault iterations finally has a
> clean, decoupled, mechanical foundation underneath it.
