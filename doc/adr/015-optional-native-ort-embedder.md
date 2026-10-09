# ADR 015: An Optional, Build-Tag-Gated Native ONNX-Runtime Embedder; the Default Stays Zero-CGO

**Status:** Proposed (2026-10-08)
**Deciders:** Project owner
**Context:** Task `optional-native-ort-cgo-embedder-off-by-default`, including its plan review
(vpimp3, 2026-10-06 — blockers B1/B2, should-fix S1/S2) and its Phase 0 spike results
(vpimp3, 2026-10-06). This ADR records the Phase 1 decision; it is a DRAFT and is not yet accepted.

This ADR **qualifies, and does not supersede,** the zero-CGO identity stated in:
- PRD §1.6 ("A pure-Go ONNX embedder (`hugot`, no CGO)"; "No Python. No Docker. No pip. No
  node_modules. Download, run, done.");
- PRD Decision D4 (the embedder row);
- PRD ~line 2092 ("The default must remain zero-dependency");
- the resume "What This Project Is" pin ("single Go binary with zero CGO dependencies").

It **amends no earlier ADR's decision.** It references:
- **ADR-014** (the search index is compiled per host from vault artifacts), whose decision 3
  reserved the embed-cache-fingerprint backend field for "Phase 11" — this ADR is where that field
  arrives;
- `evaluate-hugot-v0-7-8-upgrade`, whose embedding-parity re-verification machinery is reused;
- `hnsw-parameters-from-real-vector-recall-and-production-wiring` (the #2 HNSW grid), which is
  deferred specifically because a full real re-embed is multi-day at the pure-Go rate — a working
  Phase 1 ORT build makes that grid corpus re-embeddable in minutes, so this task unblocks it.

## Decision

> vibe-palace's released binary remains a single, static, zero-CGO Go binary built pure-Go across
> the full goreleaser matrix; the pure-Go hugot backend stays the default and the only backend the
> project ships and supports for ordinary use. Separately, the embedder gains a build-tag-gated
> (`-tags ort`, `CGO_ENABLED=1`) native ONNX-Runtime backend, selected at runtime by
> `[embedder].backend` / `VP_EMBEDDER_BACKEND`, intended as a host-native power/measurement path
> for bulk re-embedding (target 10–30 min for ~50k vectors vs. 7–11 days). It links
> `libtokenizers.a` at build time and dlopen's `libonnxruntime.so` at runtime; it is never
> cross-compiled and never part of a release artifact or a gating CI job. The embed-cache
> fingerprint records the backend, so ORT-produced and Go-produced vectors never share a cache or
> an index. Promoting ORT to the production embedding regime for all hosts is explicitly out of
> scope of this ADR and requires a separate decision (it implies a project-wide re-embed and a
> bundling/install story).

**Tag casing (B1).** The decision statement above is kept verbatim because the Chair marked it
load-bearing; its "`-tags ort`" is prose describing intent. The actual build tag the project
compiles with is the **UPPERCASE `ORT`**. Go build constraints are case-sensitive and hugot gates
on uppercase `ORT`: a lowercase `-tags ort` sets `cgo` but neither `ORT` nor `ALL`, so hugot
compiles its *disabled* `NewORTSession` stub and the native path is dead at runtime with no build
error. The project's own session files therefore use `//go:build ORT` / `//go:build !ORT`, and
every build and every test command passes `-tags ORT`. Runtime values (`backend=ort`,
`VP_EMBEDDER_BACKEND=ort`) are lowercase and independent of the tag's casing.

## How it is built (Phase 1)

- **Session seam.** `internal/embedder/session_go.go` (`//go:build !ORT`) and `session_ort.go`
  (`//go:build ORT`) provide `newHugotSession(backend)`; `NewONNX`/`NewONNXBackend` call it instead
  of `hugot.NewGoSession()` directly, keeping the download/lock/`Embed`/`EmbedBatch` machinery
  backend-agnostic. `hugot.NewGoSession` is untagged, so an `-tags ORT` binary can serve **both**
  backends at runtime — which is what lets the ORT test binary A/B the two for the parity check.
- **Fail-loud (ADR-009 ethos).** `ORTAvailable` is a build-tagged constant (false on the default
  build). `NewONNXBackend` rejects `backend=ort` on a non-ORT binary *before* any model-cache lock
  or download, with a message naming `make build-ort`. A default binary therefore never silently
  embeds with the wrong backend.
- **Rust-tokenizer truncation (mandatory).** The Go-tokenizer truncation guard
  (`onnx.go`'s `GoTokenizer`) has no counterpart on the ORT/rust path, and Phase 0 proved a raw long
  input crashes the un-wrapped Go path while the rust path only truncated at the model's
  512-position limit, not vp's `max_seq_len`. The ORT path (`truncate_ort.go`) enables the rust
  tokenizer's own Hugging Face truncation at `max_length = max_seq_len`, which replicates vp's
  behaviour=2 rule token-for-token (`[CLS]` + first `max_seq_len-2` content tokens + `[SEP]`); the
  long-input probe that crashed in Phase 0 now embeds successfully and matches the Go regime.

## Embed-cache fingerprint (B2 — the anti-mixing safety net)

`embedder.Fingerprint` compiles into **both** binaries, and the embed cache marks a project stale on
any fingerprint-string change (ADR-014 decision 3). So:

- **The go/default regime keeps today's string byte-identical.** It emits exactly
  `vp-embed behaviour=2 model=… max_seq_len=…`, with **no** backend field. Appending `backend=go`
  would mismatch every existing production cache and force every host to re-embed once at the live
  pure-Go rate — the multi-day re-embed this task exists to avoid, triggered by a routine upgrade of
  the DEFAULT zero-CGO binary. A regression test pins the go string byte-for-byte. On a default
  binary `ResolveBackend` can never yield `ort`, so a default host always produces the unchanged go
  string regardless of config.
- **Only the ORT regime appends ` backend=ort`,** giving ORT vectors their own fingerprint
  namespace so they can never be accepted into a Go cache or vice versa.

**This is the safety net, not isolation (S1).** The embed cache is one directory per project and the
regime lives in the sidecar content, not the path. The fingerprint guarantees "never both regimes in
one index"; it does NOT create a second coexisting cache. The Phase 1 ORT measurement run must
therefore write to a **separate physical index-store / cache path** (a throwaway project clone, an
alternate `.local` root, or a scratch dir) — never the production per-project dir, which a
`vp index rebuild` would otherwise re-embed over, discarding the production Go vectors.

## Scope (Phase 1 only) and what is deferred to Phase 2

This ADR is **Proposed**, not accepted. Phase 1 lands only the measurement build: the session seam,
the rust-tokenizer truncation, the fingerprint backend field, env/config selection, a `make
build-ort` target, and `-tags ORT` tests. It does **not** touch the production embedding regime, any
live vault cache, the PRD, or the resume.

On acceptance (Phase 2, a separate decision), the following PRD/resume sentences are edited — **not
done here**:
- resume "What This Project Is" pin: zero-CGO applies to the released binary; an optional `-tags ORT`
  build adds a native backend.
- PRD §1.6: true for the default, with a carve-out for the optional ORT build.
- PRD Decision D4 row: note the optional backend, default unchanged.
- PRD ~line 2092: add a pointer to this ADR.

Phase 2 also owns the real question — whether ORT becomes the production regime for all hosts, which
entails a project-wide forced re-embed and a bundling/install story for the native libs — gated on
Phase 1's measured value and an explicit operator ruling.
