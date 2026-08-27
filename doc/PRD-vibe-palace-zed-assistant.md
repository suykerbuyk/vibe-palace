# PRD: Vibe-Palace Zed Assistant (Option 3 — Native Panel + Command Palette)

> **⛔ NOT SHIPPED, NOT STARTED, AND CONTINGENT ON A CAPABILITY THAT RESOLVED NEGATIVE.**
> Banner added 2026-08-27. Everything below it is a 2026-07-12 design document, and nothing
> in it describes software that exists: no Rust extension was written, and `make build`
> produces no `zed-vp` artifact.
>
> **Read "Design locked — ready for implementation", "frozen for implementation", and
> "first-class citizen" below as statements about a DESIGN, never about a shipped
> capability.** They record what a review concluded on 2026-07-12. They are not a status.
>
> **The capability the design rests on resolved to the NEGATIVE.** `zed_extension_api`
> exposes no active-thread id and no thread lifecycle, and Zed has no hook mechanism — so
> the extension cannot publish a session identity or observe a session ending, which is
> what the panel status header and any durable-capture role would have required. The
> upstream posture is to revive PR #52729, not to file a new request. Measurement:
> `zed-pane-capture-parity`, `## Zed host capability research (2026-07-30)`.
>
> **The supported Zed path today is a Claude-shaped ACP agent in the Zed agent panel** —
> not this extension, and not the native pane. See [TUTORIAL § Zed](TUTORIAL.md#zed) and
> [COMMANDS-AND-SKILLS § Durability by host](COMMANDS-AND-SKILLS.md#durability-by-host-claude-vs-hook-less).
>
> **Zed is not a first-class host.** The native pane is still Zed's default and is still
> lossy. Do not cite §1 below as evidence that it is not.

**Version:** 0.2  
**Date:** 2026-07-12  
**Status:** Design locked — ready for implementation  
**Authors:** John Suykerbuyk (operator) + Grok 4.3 (agent)  
**Target:** Zed first (VSCode noted as future expansion)

> This revision incorporates all architectural decisions reached during review.  
> The document is now frozen for implementation. No Rust or Go code should be committed until the baseline scope defined below is complete.

---

## 1. Executive Summary

Vibe-Palace currently reaches Zed only through a thin `context_servers` entry and a prompt-based prefix hack. This PRD defines a native Rust extension (`zed-vp`) that makes Zed a first-class citizen by providing reliable, discoverable actions through Zed’s native UI surfaces.

**Core architectural commitments (locked):**
- All user-initiated actions use **direct MCP tool invocation** (`vp_cmd`, `vp_skill`, etc.) — no text insertion into the chat composer.
- The extension **owns its stdio connection** to `vp mcp`.
- Project detection, capability discovery, and command resolution are delegated to the existing Vibe-Palace MCP surface.
- Installation follows Zed’s standard extension mechanism only.
- v1 delivers a focused baseline; moonshot features are explicitly designed for but deferred.

**Baseline (v1) deliverables:**
- Ellipsis menu actions for high-frequency commands (direct invocation).
- Minimal Assistant panel with status header, “Bootstrap Context” button, and basic dynamic command list.
- Robust failure mode when `vp` is missing or unreachable.
- `make build` produces both `vp` and the extension artifacts.

Moonshot features (rich searchable catalog, active tasks mini-list, one-click Init/Config Sync, automatic bootstrap) remain on the horizon and must not be designed out of reach.

---

## 2. Goals & Non-Goals

### 2.1 Baseline Goals (v1 — Must Ship)

- Native Zed extension in Rust using `zed_extension_api` + GPUI.
- Binary name: `zed-vp`.
- Lives in `vibe-palace/zed-extension/` (monorepo).
- `make build` and `make install` produce all binaries for the product.
- Ellipsis menu entries that perform direct `vp_cmd` / `vp_skill` calls.
- Minimal Assistant panel containing:
  - Status header (project, iteration, vault dirty flag).
  - Prominent “Bootstrap Context” direct-action button.
  - Dynamically discovered list of available commands (via `vp_list_commands`).
- Extension owns and maintains the stdio MCP connection for the life of the Zed instance.
- Visible, helpful failure UX when `vp mcp` cannot be reached.
- Project detection delegated entirely to existing Vibe-Palace logic.
- Dynamic discovery preferred over static contracts (new MCP surface may be added if required).

### 2.2 Moonshot Goals (Explicitly Designed For, Deferred)

- Rich searchable command + skill catalog.
- Active tasks mini-list with priority/status.
- One-click `vp init` and `vp config sync` flows.
- Optional automatic context bootstrap on first message (after model selection).
- Full status indicators and wrap preflight integration.

### 2.3 Non-Goals (v1)

- VSCode implementation.
- Replacement or removal of the existing `AGENTS.md` / `CLAUDE.md` managed block.
- Public marketplace release.
- Support for `vp mcp serve` (HTTP) — stdio only.
- Static hard-coded command lists in the extension manifest.

---

## 3. User Stories (Baseline v1)

1. As a Zed user, when I open the AI pane ellipsis I see reliable “Vibe-Palace: Restart”, “Vibe-Palace: Wrap”, etc. entries that execute immediately via direct tool call.
2. As a new user, I can click “Bootstrap Context” and receive structured context without typing or relying on prompt interpretation.
3. As an operator, I receive clear, actionable guidance when the `vp` binary is missing or the MCP connection fails.
4. As a maintainer, `make build` produces a complete, installable product containing both Go and Rust artifacts.

---

## 4. Success Metrics (Baseline v1)

- Every ellipsis action results in a direct, observable MCP tool call (no prompt-dependent failure path).
- `vp_bootstrap_context` can be invoked successfully from the panel on first try.
- Extension gracefully surfaces a helpful error state when `vp` is not on PATH.
- `make build` completes with both `vp` and extension artifacts present.
- Connection remains stable for the lifetime of a Zed window (target: < 1 reconnection per 4-hour session).

---

## 5. Monorepo Layout & Build Integration

### 5.1 Directory Structure

```
vibe-palace/
├── cmd/vp/
├── internal/
├── zed-extension/                 # NEW
│   ├── Cargo.toml
│   ├── extension.toml
│   ├── src/
│   │   ├── main.rs
│   │   ├── lib.rs
│   │   ├── mcp_client.rs          # owns stdio connection
│   │   ├── actions.rs             # direct tool invocation handlers
│   │   ├── panel.rs               # minimal Assistant panel
│   │   ├── status.rs
│   │   └── ui/
│   └── resources/
├── Makefile
└── ...
```

### 5.2 Binary Output Requirement (Locked)

`make build` **must** produce:
- `vp` (Go)
- `zed-vp` (Rust) — even if the primary runtime artifact is the extension directory

`make install` must make both artifacts available for local installation.

### 5.3 `make build` Contract

```makefile
##@ Zed Extension
.PHONY: zed-vp
zed-vp:
	cd zed-extension && cargo build --release

build: zed-vp   # extends existing Go build
```

---

## 6. Installation & Distribution (Locked)

- The extension is installed via Zed’s standard extension mechanism (manual directory drop or future marketplace).
- `make install-zed` may copy the extension tree into the conventional Zed extensions location for convenience, but **must not** directly edit `settings.json` or any other Zed internal configuration file.
- The extension must be capable of functioning even if the user has never run any `vp mcp install` command.

---

## 7. Zed Extension Architecture (Rust)

### 7.1 Core Commitments

- Language: Rust (edition and MSRV per `zed_extension_api` requirements).
- The extension **owns** the stdio JSON-RPC connection to `vp mcp`.
- All user actions result in direct `tools/call` messages.
- Capability discovery is dynamic (via `vp_list_commands` / `vp_list_skills` or a new aggregated resource if needed).
- Project detection is delegated to the MCP layer.

### 7.2 Baseline v1 Components

1. **MCP Client** — Single long-lived stdio connection owned by the extension.
2. **Action Handlers** — Each ellipsis entry calls the corresponding `vp_cmd` or `vp_skill` directly.
3. **Minimal Assistant Panel**
   - Status header (project slug, iteration, dirty flag).
   - “Bootstrap Context” button → direct `vp_bootstrap_context` call.
   - Dynamically populated list of available high-frequency commands.
4. **Failure Handling** — When `vp` cannot be found or the connection fails, the extension surfaces a clear, actionable error with resolution steps.

### 7.3 Data Flow (Baseline)

```
User clicks ellipsis / panel button
        ↓
zed-vp extension (Rust) — direct tools/call
        ↓
vp mcp (Go)
        ↓
Result returned to extension → rendered in panel or as assistant message
```

---

## 8. Baseline UX Specification (v1)

### 8.1 Ellipsis Menu (high-frequency, direct invocation)

- Vibe-Palace: Restart
- Vibe-Palace: Wrap
- Vibe-Palace: Capture
- Vibe-Palace: Status
- (separator)
- Vibe-Palace: Activate Startup Analyst (example)
- …

### 8.2 Minimal Assistant Panel

**Header:**
```
vibe-palace  | Iter 187  | Vault: clean
```

**Primary action:**
[ Bootstrap Context ]

**Dynamic list:**
Discovered commands (populated from `vp_list_commands` on first open or refresh).

Moonshot elements (searchable catalog, tasks list, Init/Config Sync, automatic bootstrap) are explicitly designed for but not implemented in v1.

---

## 9. MCP Surface Usage

The extension will use existing tools:
- `vp_list_commands`, `vp_list_skills`
- `vp_cmd`, `vp_skill`
- `vp_bootstrap_context`
- `vp_get_project_context` (for header data)
- `vp_list_tasks` (future moonshot)

If dynamic discovery of the full command surface proves insufficient, a new lightweight resource or tool (`vp_cheat_sheet` or equivalent) may be added to the Go MCP surface. This is acceptable.

---

## 10. Risks & Mitigations (Updated)

1. **MCP connection ownership** — Extension owns the pipe (locked). Mitigated by keeping the connection simple and long-lived.
2. **Zed extension API maturity** — Accepted risk. Baseline scope stays within well-supported surfaces.
3. **Missing `vp` binary** — Handled by visible, helpful failure state (locked).
4. **Dynamic discovery** — May require small new MCP surface; explicitly allowed.
5. **Testing** — Focus on Rust-side MCP client tests + golden UI tests. Headless Zed testing is deprioritized for v1.

---

## 11. Implementation Phases (Baseline v1)

**Phase 0** — Design lock (complete)  
**Phase 1** — Skeleton + `make build` contract  
**Phase 2** — MCP client + direct action invocation  
**Phase 3** — Minimal panel + status + Bootstrap button  
**Phase 4** — Dynamic command list + failure UX  
**Phase 5** — Polish, dogfood, documentation

Moonshot features are tracked separately and must not be started until baseline is stable.

---

## 12. VSCode Option (Future)

Deferred. The same direct-invocation + dynamic-discovery principles will apply.

---

## 13. Appendix: Baseline Ellipsis Action List (v1)

Curated high-frequency set (exact list to be confirmed during implementation):
- `vpc-restart`
- `vpc-wrap`
- `vpc-capture`
- `vpc-status`
- One or two representative skills

All other commands remain discoverable via the dynamic list in the panel.

---

**End of Locked PRD.**  
This document now represents the agreed specification for the baseline v1 Zed Assistant extension. Implementation may begin.