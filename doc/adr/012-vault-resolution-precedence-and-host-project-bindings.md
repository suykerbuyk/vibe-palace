# ADR 012: Vault Resolution Precedence and Host-Local Project Bindings

**Status:** Accepted (2026-09-27). **Amended 2026-09-28** — see *Amendment (2026-09-28): older binaries, and the bind surface after the lifecycle commands*; rule 7 is corrected in place, and rules 4 and 6 and the Consequences bind bullet carry dated amendment markers. **Amended 2026-10-23** — see *Amendment (2026-10-23): Option C adopted (per-project MCP routing), delivered fail-safe-first on a startup binding snapshot*; Decision 4 is refined for the MCP routing surface, and §6's "deferred Option C" and the Consequences "born in another vault … parked follow-on" bullet are superseded. **Amended 2026-10-25** — see *Amendment (2026-10-25): Option (b) structural split*; Decision 1's tier-1 source moves to a gitignored `.vibe-palace/local.toml`, and Decision 2's committed-marker advisory becomes a hard refusal (the latter lands in a later unit).
**Deciders:** Project owner
**Context:** Epic `project-relocation-rename-and-vault-split`; decision task
`tracked-vibe-palace-toml-and-per-host-vault-binding` (U3); implementation task
`resolver-per-host-project-vault-tier` (U2a)

## Context

Until this ADR, no document owned how `vp` chooses a vault. The rule lived in a
doc comment (`internal/storage/resolve.go`) and was two tiers: the nearest
`.vibe-palace.toml` walking upward from the working directory, if it set a
top-level `vault_path`, then the global config's `vault_path`.

Splitting a group of projects into their own vault broke that rule. The first
project to move tracks its `.vibe-palace.toml` in its
own repository, shared by every host that has a checkout. The only per-host
lever was `vault_path` in that file, so rebinding the checkout had two outcomes,
both wrong:

- **Commit a host path into a shared repository.** It is false on every host
  whose vault lives elsewhere. `vault-abs-paths` cannot see it, because it scans
  vault documents only.
- **Leave the edit uncommitted.** It is permanent working-tree dirt: every stash,
  checkout or reset silently unbinds the checkout, and a new `git worktree` never
  has it.

The documents also disagreed about the file. The PRD calls it the project's one
committed identity file; `TEMPLATE_POLICY.md` called it host-local and said
nothing commits it.

Rejected alternatives (full reasoning in U3 § Plan):

- **An untracked sibling file** (e.g. `.vibe-palace.local.toml`). It is a second
  project-local file, and it is missing from every new worktree, where the hook
  would then resolve the wrong vault and skip capture.
- **A vault identity in the tracked file** (e.g. the vault's remote URL). It still
  needs a host-local map from identity to path, so it is this ADR plus a registry,
  plus a commit in every moved repository.
- **A tracked, home-relative `vault_path`** (e.g. `"~/work-vault"`). The hosts'
  vault layouts differ, and it fails open: on a host where the path is missing,
  a read-only `vp status` exits 0 and creates `<path>/palace/.local/vp.log`,
  which leaves a plausible-looking empty vault behind (the iteration-188 class).

## Decision

**1. The vault is resolved in three tiers**, by `storage.ResolveVaultBinding`
(`ResolveVaultPath` is its thin wrapper):

1. The nearest `.vibe-palace.toml` at or above the working directory (the walk
   stops at `$HOME`), if it sets a top-level `vault_path`. Source `cwd:<file>`.
2. The global config's `[project_vaults]` entry for the `[project].name` of
   **that same file**. Source `binding:<configpath>#<slug>`.
3. The global config's `vault_path`. Source `global:<configpath>`.

```toml
# ~/.config/vibe-palace/config.toml
vault_path = "~/vibe-palace-vault"

[project_vaults]
example-project = "~/work-vibe-palace-vault"
```

**2. A tracked `.vibe-palace.toml` is identity only.** It MAY be committed to its
project repository. A committed copy names the project and never sets
`vault_path`: a path is a fact about one host. Each host binds the project to a
vault in its own global config. Tier 1 remains for untracked trees (throwaway
vaults, rehearsal fixtures). `vp check` reports a committed marker that sets
`vault_path`, as an advisory (Info) row.

**3. The binding is keyed on the marker's `[project].name` only**, never on the
git-origin fallback that slug detection also uses. Resolution stays a function
of files, and remote-derived names are known to pick the wrong slug
(`community-sonic`'s remote is `sonic-buildimage`).

**4. Resolution fails closed.** Each of the following refuses, and never falls
through to a lower tier, because the lowest tier is the live vault:

- a found `.vibe-palace.toml` that cannot be read or parsed, or whose
  `vault_path` is not a string (once a committed marker plus a host binding
  decides the vault, one bad commit must not reroute every host's capture);
- tiers 1 and 2 naming different vaults (the refusal names both sources; the
  same root is allowed);
- a tier-2 target that is not an absolute path (after `~` expansion) to an
  existing directory holding `.vibe-palace/vault.toml`;
- a `[project_vaults]` table that is malformed: a key differing from
  `project_vaults` only in case, a key that is not a slug, or a value that is
  not a non-empty string;
- a checkout whose marker names no project while its git-origin slug is bound on
  this host, or while `git` cannot be asked (missing, failed, timed out after
  5 s) on a host that binds anything;
- *(added 2026-09-28)* a tier-2 target that holds none of the project's content
  while this host's default vault records the project's departure or holds
  tracked content for it — the stale binding; see Amendment (2026-09-28) §3.
- *(added 2026-10-23)* for the MCP routing surface specifically, "fails closed" is
  applied to a **startup snapshot** of `[project_vaults]`, not a live per-request
  re-read — see Amendment (2026-10-23) §2. Live CLI resolution (tiers 1–3) is unchanged.

A global config that is present but cannot be read or parsed also fails
resolution closed (it may hide a binding), with its own error
(`ErrHostConfigUnreadable`), and `vp check` still runs its rows and reports it.
An absent config, or a dangling symlink, means "no bindings".

**4a. One marker walk and one reader.** Vault resolution and project detection
find and read the marker through the same function (`project.LocateMarker`:
symlink-resolved walk, bounded at the resolved `$HOME`; name trimmed), so the
vault a session writes and the project it is labelled with cannot come from
different files.

A marker name that is unbound while its git-origin slug is bound is not refused:
the marker is the identity. It resolves tier 3, and `vp status` prints a warning
naming both.

**5. One typed refusal.** Every refusal wraps `storage.ErrVaultBindingRejected`,
and a swallowed `vault_path` wraps it as well as `ErrSwallowedVaultPath`. `vp
hook` falls back to the global vault on any resolution error it does not
recognise, so an untyped refusal would be a silent capture into the live vault
(iteration 210). No refusal wraps `fs.ErrNotExist`, which `vp check` and
`vp skills show` read as "no config at all".

**6. Global-only stays the default; the vault *git* family becomes cwd-aware.** Tier 2 lives in
`ResolveVaultBinding`, never in `ResolveGlobalVaultPath`. The 7 vault file-CRUD commands
(`read/write/edit/delete/move/exists/sha256`), the surface gate (`vaultRoot()`), the host menu
shims, the vault *lifecycle* commands (`vp vault copy`, `vp vault rename`, `vp vault project
delete`, `vp vault init`, `vp vault clone`), and `OpenVaultGlobal` (the `vp hook` fallback) all keep
resolving the global vault (or an explicit `--vault`). **Amended 2026-10-12:** only the vault *git*
family — `vp vault pull/push/sync/commit/tidy/status` — now resolves its target through
`ResolveVaultBinding` (tier 1→2→3) when no `--vault` is given, so `cd`-ing into a project operates on
that project's own vault. `--vault` still overrides; every **mutating/transport** git-family
resolution (`pull/push/sync/commit/tidy`, any source) must first pass `refuseUnlessOwnGitRepo`;
**read-only `status` is exempt (the M1 nested-vault carve-out)** — except an explicit `--vault`
naming a non-own-repo path, which `status` still refuses (pre-existing
`TestVaultFlagRefusesSubfolderOfAVault`) — and a mutating one (`commit`, `tidy`) also the surface
fail-stop. **This changes only the CLI:** the MCP `vp_vault_*` tools resolve
the server's startup-bound vault and are unchanged (per-project MCP routing is the deferred
Option C); the vault git family prints a parity note to stderr when it resolves a non-global source.
*(Superseded 2026-10-23 — Option C is adopted; the MCP `vp_vault_*` surface no longer treats
per-project routing as deferred. See Amendment (2026-10-23).)*
The fresh-host `vp init` marker write (rule 2) is unchanged; `vp init --vault-path` on an existing
host writes a tier-2 `[project_vaults]` binding **when the target is a git vault differing from the
default**; a **no-git / untracked / nested target keeps the existing tier-1 cwd-marker path**
(ADR-012 tier-1-for-untracked).

**7. The global config schema moves to 1.2** for the new table. The version
fields are informational for a minor bump. An older binary silently ignores
`[project_vaults]` and resolves tier 3; ~~for a project that moved away, that
vault's departure record then refuses the write.~~ **(Corrected 2026-09-28 —
see Amendment (2026-09-28) §1: an older binary does not honour departure
records. What stops it writing a moved project back into the global vault is
the MCP surface gate, raised to 8 for exactly this reason. The departure-record
refusal described here holds only in a v8 or later binary.)**

## Consequences

- A running MCP server resolves its vault once, at startup. Adding or changing
  a binding makes `vp_*` writes refuse with `StaleBindingError` until the host
  reloads the server, which is the drift guard working as designed.
- A project born in another vault (never present in the global vault, so it has
  no departure record there) is not protected by rule 7 or rule 4 on a host that
  lacks the binding: the first write creates it in the global vault. The split
  runbook covers this by ordering (bind on each host before that host's first
  session in the checkout). Closing it in code is a parked follow-on.
  *(Closed 2026-10-23 — the fail-safe (U2/U3) converts the silent cross-vault write into a
  loud refusal at the MCP gate and the shared write funnel; see Amendment (2026-10-23) §3.
  Residual coverage limit — a tier-1 cwd-marker-only project with no `[project_vaults]` entry
  — stated there.)*
- Resolution still does not check that a tier-1 or tier-3 root exists. That is a
  whole-binary behaviour change and a parked follow-on.
- *(Amended 2026-09-28 — see Amendment (2026-09-28) §2; this bullet originally
  named `storage.BindProjectVault` and a single `<slug>`.)* A binding is
  written by `vp config bind <slug>... --vault <path> [--checkout
  <dir>...] [--new] [--allow-unlabelled] [--dry-run] [--json]` or the stdio MCP
  tool `vp_config_bind` (both over `storage.BindProjectVaults`, task
  `checkout-rebind-entry-point`), one `[project_vaults]` line per slug, all in
  ONE compare-and-set write. `vp vault clone <url> <path> --bind <project>...`
  is a second writer of `[project_vaults]`: it runs the same bind over the new
  clone, again in one write, and needs the default vault pulled first so it
  holds the moved-to-vault records (`vp vault clone --help`; ADR-013). It
  checks the
  split landed (a moved-to-vault record whose destination equals a remote of the
  target, compared as host/path in any URL spelling; `--allow-unlabelled` binds
  although the record names no destination to check) or, with `--new`, that the
  default vault never held the project. It refuses a target vault that holds
  any departure record for the project. It never re-points an existing
  binding, never writes a checkout, keeps the config's mode and writes through
  a symlinked config, and restores the config (compare-and-set) if any named
  checkout does not then resolve through it. A checkout rebind's split kind
  binds this way; its rename kind ADDS `[project_vaults].<to>` and keeps
  `<from>`, so other checkouts still naming `<from>` stay on the bound vault,
  whose `renamed` departure record refuses and redirects them.

## Amendment (2026-09-28): older binaries, and the bind surface after the lifecycle commands

The resolution tiers are unchanged. Three things this page said, or did not say, were overtaken by
the vault project-lifecycle work, recorded in
`doc/adr/013-vault-project-lifecycle-and-departure-records.md`.

**1. Rule 7 was wrong about older binaries.** It said an older binary that ignores
`[project_vaults]` is still stopped, for a moved project, by the departure record
in the global vault. It is not: a v7 binary from before departure records existed
never reads them, and no v7 binary treats the record as winning over the directory,
so a lagging host can re-create a moved project's tree in the global vault and
publish it on its next push. The protection is the MCP surface gate: the
lifecycle commands raised `MCPSurfaceVersion` from 7 to 8 (`1d80e77`), so once
the vault carries a v8 stamp an older binary is refused every write. The
reasoning is on the 7→8 note above `MCPSurfaceVersion` in
`internal/surface/version.go`. Rollout therefore means installing a v8 binary on
every host, and restarting its AI harnesses, before splitting a vault.

**2. The bind surface grew.** As corrected in place under Consequences:
`vp config bind` and `vp_config_bind` take several slugs in one write and have
`--allow-unlabelled` / `allow_unlabelled` and `--dry-run` / `dry_run` (plus
`--json` on the CLI; the tool spells `--new` as `mode: "new"`); the bind refuses
a target vault that records the project as departed; and `vp vault clone --bind`
is a second entry point that writes `[project_vaults]` through the same
`storage.BindProjectVaults`. Rule 6's list of global-only callers now also names
`vp vault copy` and `vp vault project delete` (`--vault`), and notes that
`vp vault init` and `vp vault clone` take explicit paths. See `vp config bind
--help` and `vp vault clone --help` for the current flags, and
`doc/VAULT-LIFECYCLE.md` for the operator procedure.

**3. Resolution gained a fail-closed rule.** A tier-2 target that holds none of
the project's content, while this host's default vault records the project's
departure or holds tracked content for it, is refused as a stale binding
(`ErrStaleProjectBinding`, which wraps `ErrVaultBindingRejected`, so `vp hook`
captures nothing rather than fall back; `staleProjectBinding` in
`internal/storage/project_vaults.go`, called from the tier-2 block of
`ResolveVaultBinding`, commit `dfcddaf`). This is the binding that outlived an
undone move, or that disagrees with content a host without the binding created
in the default vault. Where git cannot answer, the check fails open (an
undecided target counts as holding the project). A running MCP server reports
it as `StaleBindingError` and refuses mutating tools (ADR-010, Amendment
(2026-09-28)). Rule 4's list carries it as a dated bullet.

## Amendment (2026-10-23): Option C adopted — per-project MCP routing, delivered fail-safe-first on a startup binding snapshot

The resolution tiers (Decision 1) are unchanged. This amendment records that **Option C —
per-project MCP vault routing — is ADOPTED** (operator, 2026-10-22, chaired session), replacing
the "deferred/parked follow-on" language in §6 and Consequences. It is delivered in two phases;
phase 1 has landed.

**The exposure Option C closes.** The MCP server binds ONE vault at process startup and injects it
into every request (`internal/mcp/server.go`); the `project` arg selects only a *subtree* under
that one vault (`internal/tools/task_write_commit.go`), never a vault. A project-keyed mutating MCP
write for a project whose vault ≠ the startup vault would land in the wrong vault, silently — the
latent cross-project hole named in the original Consequences bullet.

**1. Two-phase delivery.**

- **Phase 1 — fail-safe (B), LANDED (iter 500; U1–U3).** A mis-routed project-keyed mutating write
  now REFUSES (fail-closed) instead of landing in the wrong vault. A safe stopping point, strictly
  safer than the prior state.
- **Phase 2 — routing (A), in progress (U4–U6).** Replaces the refusal with a route to the correct
  vault's stack. Gated on an operator RAM/throughput sign-off (given 2026-10-23) and requires an
  in-process shared-model seam — see §4.

**2. Decision 4 ("resolution fails closed") is refined for the MCP routing surface: a STARTUP
SNAPSHOT, not a live re-read.** The `[project_vaults]` routing table is snapshotted ONCE at startup
— the same moment the server binds its vault, when the config is readable by construction — and the
guard and router consult that in-memory snapshot, NEVER a live re-read of `config.toml`.

- Mid-session config corruption → guard holds the snapshot → no brick AND no fail-open window.
- Corrupt at startup → the server cannot resolve its own `vault_path` either → moot.
- Unbuildable snapshot at startup (config changed in the narrow window after the server's own
  startup resolution succeeded, e.g. `vp config bind` mid-startup) → mutating MCP writes fail closed
  (refuse) until a good snapshot builds. The builder NEVER installs an "allow-all empty" snapshot on
  a build error. Reads and ALL CLI commands are unaffected.

This supersedes the fail-closed recommendation for this surface and refines Decision 4; it is
consistent with existing behaviour (`vp_config_bind` already requires a host reload — bindings are
effectively startup-scoped today).

**3. The fail-safe enforces at the layer BOTH CLI and MCP call** (standing rule: a guard only an
agent can trip is not a guard). Two choke points, fail-closed:

- **U2 — MCP dispatch refusal** (`internal/mcp/tools.go`, `gateIfMutating`): a mutating call whose
  `project` resolves to a vault ≠ startup vault is refused. On an unbuilt snapshot, only
  PROJECT-KEYED mutating writes are refused; whole-vault tools are not.
- **U3 — write-funnel backstop** at the two universal write choke points both surfaces call
  (`atomicfile.writeAtomic` hook + `storage.appendUnderLock`): a storage-level `ErrCrossVaultWrite`
  classifies against a process-global snapshot installed EAGERLY (CLI pre-run + MCP bootstrap),
  never lazy-built inside the write lock.
- **Per-write-site lifecycle exemption** (`atomicfile.ForMisboundExempt`): legitimate lifecycle
  cross-vault writes (`vp vault copy/clone/rename` writing a project's content into its newly-bound
  vault) pass an exemption at the shared storage write site — one exemption covers both the CLI
  commands and the `vp_vault_copy` MCP tool. Mirrors `ForDepartureRecord`.
- **Derived-delete sink is ALLOW + WARN:** a pull/sync merge-heal (`dropDerivedPaths` →
  `vaultfs.Delete`) removing a regenerable `palace/<slug>/…` path from the WRONG vault is corrective
  cleanup (it can never touch the project's real vault) — it proceeds and logs a cross-vault
  warning. Distinct from the content write funnel, which still refuses.

**4. Routing (A) requires an in-process shared-model seam (operator, 2026-10-23).** Measured:
per-vault resident cost is ~310 MB (default go backend), and the model is private anonymous heap,
not mmap'd — multi-process routing gets zero OS page-cache sharing, so routing N vaults naively
costs N×310 MB. The vault-stack registry (U4) therefore MUST include a shared model cache keyed on
the embedder fingerprint (backend, model, max-seq), capping cost to ~310 MB flat in N.

**5. Known coverage limit of the fail-safe (B).** The slug-only backstop catches only projects WITH
a `[project_vaults]` entry; a project with only a tier-1 cwd-marker `vault_path` and no global
binding is invisible to it. For the quantum split this is fine — split projects are bound via
`[project_vaults]` (Decision 1, `clone --bind`). Stated as a known limit.

## Amendment (2026-10-25): Option (b) structural split — the tracked marker is identity-only by STRUCTURE, the tier-1 override moves to a gitignored file

> STAGED DELIVERY. The operator chose Option (b) (task
> `adr-012-identity-only-marker-compliance-and-guard`, Decision 2026-10-25, wording
> signed off 2026-10-25); this section records the amended design, delivered across
> units U1–U5 of the reviewed plan. **This change ships U1 (this text) + U2 (resolver
> reads the new file) only.** The hard tracked-marker refusal (U4) and the `vp init`
> writer changes (U5) are separate, later units and are NOT in force yet.

The resolution tiers (Decision 1) are unchanged in NUMBER and ORDER. What changes
is the SOURCE of the tier-1 override and the enforcement of Decision 2. The
2026-09-28 and 2026-10-23 amendments are untouched.

**Why.** Decision 2 made identity-vs-override a CONTENT rule on one file (a tracked
`.vibe-palace.toml` must not carry `vault_path`; an untracked one may). That rule
drifted in both directions on live checkouts (a tracked marker carrying a host
path; an identity marker git-excluded so identity did not travel). A STRUCTURAL
split — identity in the tracked file, the override in a separate gitignored file —
removes the content rule the drift kept violating.

**1. Tier-1 source: `.vibe-palace.toml` → `.vibe-palace/local.toml`.** The Decision 1
tier-1 lever is read from the gitignored `.vibe-palace/local.toml` (top-level
`vault_path`), found by a `$HOME`-bounded walk identical to the identity-marker
walk. Source label `cwd-local:<file>`. It is scoped to throwaway/rehearsal trees;
the normal per-host binding stays the tier-2 global `[project_vaults].<slug>`,
keyed on the tracked marker's `[project].name` (Decision 3, unchanged).

A **deprecation window** (U2) still honors a legacy top-level `vault_path` in
`.vibe-palace.toml`, but ONLY when `.vibe-palace/local.toml` sets none; it is
surfaced as a `Resolution.Warnings` migration nudge, which **both `vp status` and
`vp check` print** (as `vault binding warning:`), never a new error type (a
non-sentinel error would fall through `vp hook` to the live vault). The new
override wins when both are set; **both set and naming different vaults is a
refusal** (`ErrVaultBindingRejected`).

**2. Decision 2: advisory → hard refusal (U4, not yet in force).** A tracked
`.vibe-palace.toml` that carries an active `vault_path` moves from the `vp check`
**advisory (Info)** row to a **hard refusal** at the resolver — the single seam
both the CLI and the MCP startup bind, and the hook (`OpenVaultFromCwd`), pass
through. The tracked predicate FAILS CLOSED: when `vault_path` is non-empty and
git cannot answer (a real failure/timeout/disabled, as distinct from a genuinely
non-git tree), the resolution refuses rather than honoring the path. The `vp check`
row remains as a friendly pre-flight that names the fix. This supersedes Decision
2's last sentence ("`vp check` reports a committed marker that sets `vault_path`,
as an advisory (Info) row"). The enforcement lands in U4; U2 keeps the legacy
advisory behavior for tracked markers until then.

**3. The rejected "untracked sibling file" alternative is refined, not overturned.**
ADR-012's Context rejected an untracked sibling because a fresh `git worktree` /
`git clone` lacks it, so the hook would resolve the wrong vault and skip capture.
That rejection STANDS for using such a file as the *normal* per-host binding
mechanism. Option (b) does not: the normal mechanism is the tier-2 global
`[project_vaults]` binding, keyed on the identity the TRACKED marker carries (which
a worktree/clone does carry). The gitignored `.vibe-palace/local.toml` is scoped
strictly to intentional throwaway trees that are never tier-2-bound — so a fresh
worktree/clone of a real project resolves on identity via tier-2 and never depends
on the override's presence. (A fresh clone on a host with NO binding still falls to
tier 3; that is the pre-existing "bind before first session" hazard in the
Consequences, not introduced here.)

**4. Decision 4 / 4a.** Unchanged in rule. The tier-1 source file is renamed to
`.vibe-palace/local.toml`; the found-but-unreadable / swallowed-`vault_path`
refusals apply to the override file as well as the identity marker. **4a's
one-walk-one-reader intent is preserved by CO-LOCATION:** when an identity marker
is found at directory `D`, the override is read ONLY at `D/.vibe-palace/local.toml`
— never via an independent upward walk that could climb past `D`. So a child
checkout's own identity marker is never bound to a parent tree's throwaway override,
and the vault choice and the project label cannot come from disagreeing files. A
throwaway tree with no identity marker keeps the override's own `$HOME`-bounded
walk. The new-file-vs-legacy disagreement refusal is added to Decision 4's list.

**5. Decision 6 is explicitly UNCHANGED.** This amendment governs the
resolver/capture path only. The 7 vault file-CRUD commands, the surface gate
`vaultRoot()`, the vault lifecycle commands, and the `vp hook` global fallback
stay on the GLOBAL resolver (`ResolveGlobalVaultPath` / `OpenVaultGlobal`), which
never reads the marker or the override. Shipping this split is NOT a license to
point file-CRUD at `ResolveVaultBinding`.
