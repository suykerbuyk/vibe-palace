# ADR 005: LLM-Enriched Session Synthesis

**Status:** Accepted (2026-06-21)
**Deciders:** Project owner
**Context:** Vibe-palace llm-enrichment-synthesis — replacing the weak SessionEnd
auto-summary with a real LLM synthesis

## Context

The hook capture path (`vp hook` on SessionEnd) writes a session note
*deterministically* from `git log` — a list of recent commit subjects stuffed
into the summary field, with no decisions, no open threads, and a guessed tag.
That auto-summary is the floor of the system's memory: it is what
`vp_bootstrap_context` surfaces from "recent sessions" next time, what
`vp_search_sessions` ranks, and what a developer resuming the project reads
first. A git-log dump is a poor stand-in for "what actually happened this
session" — it omits the reasoning, the decisions made, and the work left
unfinished, all of which live in the transcript and never in the commit log.

The agent-authored path (`/wrap`, and the `vp_capture_session` MCP tool driven
by an agent) already produces a good narrative summary, because a model wrote
it. The gap is specifically the **unattended** SessionEnd hook, which has no
agent in the loop and falls back to the heuristic.

Three constraints shape any fix:

- **The raw transcript is multi-MB.** Claude Code JSONL transcripts carry full
  tool outputs and file reads; feeding the whole thing to an LLM is wasteful and
  often exceeds context windows. The hook already reads the transcript once for
  friction analysis.
- **Capture must never fail because enrichment failed.** Auto-capture was a pure
  local operation; adding a network LLM call introduces latency and an external
  dependency to a path that previously could not fail on a network error.
- **An inline-enriched note and an asynchronously-enriched note must be
  indistinguishable.** Enrichment can land at capture time (synchronously) or
  later (drained from a queue); both must converge to the same bytes on disk so
  the file is stable and re-drains are idempotent.

`internal/llm` already had a thin OpenAI-compatible client (`Client`,
`ChatCompletion`) used by the Phase-12 `vp tune`/`vp discover` room-classification
workflows, configured under `[palace.llm]`. That client is OpenAI-shaped only —
it cannot talk to the Anthropic Messages API natively.

## Decision

Add an LLM synthesis pass that, when enabled, replaces the heuristic
summary/decisions/open-threads/tag on the SessionEnd note with a real synthesis
from an LLM — best-effort, opt-in, and byte-stable whether it lands inline or
via an async queue.

### Reuse `internal/llm`, add native Anthropic behind a `Completer`

Rather than fork a second HTTP client, a new `Completer` interface abstracts the
single-shot completion both providers need:

```go
type Completer interface {
    Complete(ctx context.Context, system, user string) (string, error)
    Name() string
}
```

`*llm.Client` gains a `Complete` method that wraps its existing
`ChatCompletion` (system+user messages → first choice). A new native
`anthropic.go` client (`internal/llm/anthropic.go`) satisfies the same interface
against the Anthropic Messages API: `x-api-key` + `anthropic-version: 2023-06-01`
headers, `POST {base}/v1/messages`, `max_tokens` defaulting to 4096, parsing
`content[0].text` from the response. A factory `NewCompleter(provider, cfg)`
returns the Anthropic client for `provider == "anthropic"` and the
OpenAI-compatible `*Client` otherwise. The 429/5xx exponential-backoff loop,
previously buried inside `ChatCompletion`, was extracted into a shared
`retryWithBackoff` helper (`internal/llm/retry.go`) that both clients call, so
there is one backoff implementation, not two. Anthropic has no JSON-response
mode, which is why the parse path tolerates prose-wrapped output (see below).

### Truncated `PromptInput`, not the raw transcript

A dedicated `internal/enrichment` package owns the transcript→LLM boundary.
`ExtractPromptInput([]byte)` parses Claude Code JSONL into a `PromptInput`: it
walks `user`/`assistant` records (handling both the plain-string and
content-block-array content shapes), concatenates the visible text, **caps
UserText and AssistantText at 12000 chars each**, tallies `tool_use`
invocations into `ToolCounts`, counts messages, and collects distinct edited
file paths (from `Write`/`Edit`/`MultiEdit`/`NotebookEdit` tool inputs).
Malformed lines are skipped — extraction is best-effort. The LLM sees this
compact, bounded digest, never the multi-MB transcript.

`Generate`/`Enricher.Enrich` call the `Completer`, then tolerantly parse the
reply: strip a leading/trailing ```json fence, `json.Unmarshal`, and on a parse
failure perform **exactly one** corrective reprompt ("respond with valid JSON
only") before giving up. The returned `tag` is validated against the canonical
7-tag set (`implementation`, `debugging`, `refactor`, `exploration`, `review`,
`docs`, `planning`); any other value normalizes to empty.

### Vault-editable, raw-loaded system prompt

`LoadSystemPrompt(vaultPath)` resolves the synthesis system prompt with the
precedence:

1. `<vault>/Templates/enrichment.md` (raw file read), else
2. the embedded `internal/templates/templates/enrichment.md`, else
3. an in-package `defaultSystemPrompt` const (so the function never returns "").

The prompt is loaded **raw** — deliberately *not* through the context
`Resolver`, which would run `{{DATE}}`/`{{PROJECT}}` token expansion. A system
prompt must stay deterministic; injecting the date would leak nondeterminism
into the synthesis and bust fixture tests. Shipping `enrichment.md` as an
embedded template means it is materialized into `<vault>/Templates/` on
`vp init` and participates in the normal three-SHA reconcile, so a user can edit
the synthesis prompt in the vault and have it survive binary upgrades — while the
loader still bypasses the Resolver to keep the read raw.

### Dedicated `[enrichment]` config block, additive minor bump

Enrichment gets its own top-level `[enrichment]` TOML block
(`internal/storage/config.go`) resolved into `storage.EnrichmentConfig`, rather
than overloading `[palace.llm]` (which has its own provider/model/budget for room
tuning and should not be coupled to session synthesis):

```toml
[enrichment]
enabled = false
provider = "xai"
model = "grok-3-mini"
api_key_env = "XAI_API_KEY"
base_url = "https://api.x.ai/v1"
max_tokens = 4096
timeout_seconds = 30
```

`enabled` defaults to **false** — the feature is opt-in. The API key is never
stored in config: `api_key_env` names the environment variable, resolved at
runtime via `os.Getenv`. Adding the block is purely additive, so the schema
**minor** version bumped 1.0 → 1.1 (`CurrentVersionMinor = 1`); an absent
`[enrichment]` block decodes to the zero value and the version check only rejects
a higher *major*, so older and newer configs interoperate.

### Synchronous pass with an async queue fallback

When the SessionParams carry an enricher and a transcript, `WriteSession` runs
the synthesis **synchronously** before building the note: on success the
heuristic summary/decisions/threads/tag are overwritten and
`SessionMeta.EnrichedBy`/`EnrichedAt` are set. The call is bounded by the
configured `timeout_seconds`. If the LLM errors or times out — or simply isn't
reached in the budget — capture does **not** fail: the note is written with the
plain heuristic summary, and the already-extracted `PromptInput` is enqueued for
later. This keeps the latency- and failure-sensitive capture path robust while
still eventually producing a good note.

### Host-local CWD queue, not the vault

Enqueued jobs are written to `<CWD>/.vibe-palace/enrichment-queue/<id>.json` —
**host-local, next to the project, not inside the vault.** Putting the queue in
the vault would make in-flight scratch files show up as uncommitted dirt to
`vp vault tidy` / `/wrap` preflight (the queue is transient machine state, not
content of record), and would risk session-ID collisions across projects that
share a vault. The host-local `<CWD>/.vibe-palace/` directory is already covered
by the canonical project `.gitignore` patterns, so the queue never leaks into
the project repo either.

### Byte-identical drain via the shared body/marshal helpers

`DrainEnrichmentQueue` processes queued jobs. Each item is **claimed via an
atomic rename to `<item>.processing`** so a concurrent hook drain and any other
drain cannot double-process the same job (a `.processing` file is invisible to
the `*.json` glob). It retries transient enrichment failures by renaming the
claim back to `.json`; a corrupt item is removed rather than retried forever.
On success it rewrites the note **in place** via `storage.RewriteSession` — a
fixed-path overwrite that does **not** increment the iteration and shares the
`marshalSessionFile` framing helper with `WriteSession`.

Crucially, the drain reconstructs `SessionParams` (setting `EnrichedBy`) and
calls the **same** `buildSessionBody`, which wraps an enriched body in a
`<!-- enriched -->…<!-- /enriched -->` fence plus an `*enriched by <model>*`
footer. Because both the inline path and the drain path go through one body
builder and one marshaller, an inline-enriched note and a drained note converge
to **byte-identical** bodies. `EnrichedAt` is preserved when already set, so
re-draining an already-enriched note is a no-op rewrite (idempotent).

### Drain in the hook, not in bootstrap

The drain runs in the SessionEnd hook (`internal/hook/hook.go`), which already
does the heavy SessionEnd work (archive, harvest, friction). It is deliberately
**not** wired into `vp_bootstrap_context`: bootstrap is latency-sensitive
read-assembly at session start and must not block on a network LLM call. The
hook builds the enricher from config via the shared
`capture.NewEnricherFromConfig(cfg, vaultRoot)` and drains the queue when
`[enrichment]` is enabled.

### Opt-in defaults: off for `/wrap` and MCP, on for the enabled hook

Enrichment is driven by the presence of `SessionParams.Enricher`, set per entry
point:

- **SessionEnd hook** — builds the enricher from config; enrichment runs (and
  the queue drains) when `[enrichment].enabled` is true. This is the path the
  feature exists for.
- **`vp_capture_session` MCP tool** — gains an opt-in `enrich` bool, default
  **false**. Agent-authored `/wrap` and MCP capture notes are already good
  narratives written by a model in the loop; re-synthesizing them adds latency
  and cost for no gain, so the MCP path opts in only when the caller asks.

## Consequences

**Positive:**

- The unattended SessionEnd note becomes a real summary/decisions/open-threads/
  tag synthesis instead of a git-log dump, so bootstrap, search, and a resuming
  developer all read better memory.
- Capture is still robust: an LLM error/timeout downgrades to the plain note and
  enqueues for later; capture never fails on a network fault.
- Inline and drained enrichment converge to byte-identical files through one
  shared body builder and marshaller, so notes are stable and re-drains are
  idempotent.
- The queue is host-local, so it never pollutes the vault, never trips tidy/wrap
  preflight, and never leaks into the project repo.
- A single `Completer` abstraction reuses the existing OpenAI-compatible client
  and adds native Anthropic with one shared backoff helper — no second HTTP
  stack, no new external SDK.
- The synthesis prompt is vault-editable and reconciles cleanly, yet stays
  deterministic because it is loaded raw.

**Negative / trade-offs:**

- The SessionEnd hook path now has a synchronous network call (bounded by
  `timeout_seconds`); a slow provider adds latency to hook completion before the
  fallback enqueue kicks in.
- A second, queue-based code path exists alongside the inline path; both must
  stay coherent (mitigated by sharing `buildSessionBody`/`marshalSessionFile`,
  and covered by a byte-identical-convergence test).
- The host-local queue lives outside the vault, so unprocessed jobs do not sync
  across machines — they are drained on the host that produced them.
- Enrichment depends on an external LLM and an API key in the environment; it is
  off by default and a missing key warns and skips rather than failing.

## Alternatives considered

- **Feed the raw transcript to the LLM.** Rejected: multi-MB JSONL is wasteful
  and blows context windows. `ExtractPromptInput` sends a bounded, truncated
  digest instead.
- **Put the queue in the vault.** Rejected: transient queue scratch would be
  reported as dirt by `vp vault tidy` and `/wrap` preflight, and risks session-ID
  collisions across projects sharing a vault. The host-local `<CWD>/.vibe-palace/`
  queue is already gitignored and project-local.
- **A positional `WriteSession` enricher parameter / a bool flag.** Rejected: a
  fifth positional arg would churn every caller and test, and a bool flag carries
  no dependency. Instead `SessionParams` gained a dependency-carrying
  `*enrichment.Enricher` field — a nil field means "disabled," and the same
  struct already threads every other capture input.
- **Reuse `[palace.llm]` for enrichment.** Rejected: room-tuning and session
  synthesis are independent concerns with independent provider/model/budget; a
  dedicated `[enrichment]` block keeps them decoupled.
- **Load the system prompt through the context `Resolver`.** Rejected: the
  Resolver expands `{{DATE}}`/`{{PROJECT}}` tokens, injecting nondeterminism into
  a prompt that must stay stable. The loader bypasses it and reads raw, while the
  template still materializes and reconciles like every other embedded template.
- **Run the drain in `vp_bootstrap_context`.** Rejected: bootstrap is
  latency-sensitive session-start read-assembly and must not block on a network
  LLM call. The drain runs in the already-heavy SessionEnd hook.
- **Always enrich the MCP/`/wrap` path.** Rejected: agent-authored notes are
  already good; enrichment there is wasted latency and cost. The MCP path is
  opt-in via the `enrich` bool (default false).

## References

- Provider abstraction: `internal/llm/completer.go` (`Completer`,
  `NewCompleter`), `internal/llm/anthropic.go` (native Anthropic),
  `internal/llm/retry.go` (`retryWithBackoff`)
- Enrichment engine: `internal/enrichment/` — `extract.go` (`ExtractPromptInput`),
  `client.go` (`Generate`/`Enricher`, tolerant parse + reprompt, tag validation),
  `template.go` (`LoadSystemPrompt`), `prompt.go`, `types.go`
- Capture integration: `internal/capture/session.go`
  (`SessionParams.Enricher`/`EnrichedBy`, `buildSessionBody` fence),
  `internal/capture/enrichqueue.go` (`EnqueueEnrichment`,
  `DrainEnrichmentQueue`), `internal/capture/enricher_config.go`
  (`NewEnricherFromConfig`)
- Storage: `internal/storage/sessions.go` (`RewriteSession`,
  `marshalSessionFile`, `EnrichedBy`/`EnrichedAt`),
  `internal/storage/config.go` (`EnrichmentConfig`, minor bump),
  `internal/storage/config/defaults.toml` (commented `[enrichment]` stanza)
- Entry points: `internal/hook/hook.go` (SessionEnd enricher + drain),
  `internal/tools/session_tools.go` (`enrich` param)
- Embedded prompt: `internal/templates/templates/enrichment.md`
- Prior LLM client / config: ADR — Phase 12 adaptive room classification,
  `doc/ARCHITECTURE.md` §Adaptive Room Classification
