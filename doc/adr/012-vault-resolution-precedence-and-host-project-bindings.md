# ADR 012: Vault Resolution Precedence and Host-Local Project Bindings

**Status:** Accepted (2026-09-27). **Amended 2026-09-28** — see *Amendment (2026-09-28): older binaries, and the bind surface after the lifecycle commands*; rule 7 is corrected in place, and rules 4 and 6 and the Consequences bind bullet carry dated amendment markers.
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

**6. Machine-wide callers stay global-only.** Tier 2 lives inside
`ResolveVaultBinding`, never in `ResolveGlobalVaultPath`. So the host menu shims
and the vault git command family (`vp vault pull/push/sync/commit/tidy/status`,
which take `--vault`) keep resolving the global vault, as before. *(Amended
2026-09-28, see Amendment (2026-09-28) §2:)* So do the
lifecycle commands `vp vault copy` and `vp vault project delete`, which act on
the configured global vault or on `--vault`; `vp vault init <path>` and
`vp vault clone <url> <path>` take an explicit path and resolve nothing
(ADR-013).

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
