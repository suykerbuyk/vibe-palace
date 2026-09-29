# ADR 013: Vault Project Lifecycle Commands and Departure Records

**Status:** Accepted (2026-09-28)
**Deciders:** Project owner
**Context:** Epic `supported-project-rename`; design task `lifecycle-commands-design-plan`;
builds on [ADR-012](012-vault-resolution-precedence-and-host-project-bindings.md) (per-host
project bindings). The user guide is [doc/VAULT-LIFECYCLE.md](../VAULT-LIFECYCLE.md); this page
records the decisions, not the procedure.

## Context

ADR-012 let a host bind a project to a vault other than its default one. Nothing supported yet
moved a project's history into such a vault, or removed it from the vault it left. The only tool
was the MCP-only `vp_vault_split` / `vp_vault_merge` pair. Both take another vault's absolute
host path: `vp_vault_split` writes the new vault at that path, and `vp_vault_merge` reads the vault
at that path into the served one. Both need a multi-step, operator-run procedure across plan,
apply and verify calls (plus purge, for split).

The operator rejected that shape on 2026-09-27. Moving a project between vaults is a mechanical,
deterministic data move. It belongs in `vp` as a few simple commands, each with `--dry-run` and
each undoable with plain git. The human only coordinates hosts.

## Decision

**1. A vault's own `vp` is the only writer of that vault.** No process writes a vault it does not
serve. It reads another vault only through that vault's **published git remote**, never through
a host path. That makes four filesystems in a move: vault A, vault B, remote A and remote B.
Ignored, host-local state (the embed cache) is never carried. It is derived again at the
destination. This binds the lifecycle commands below; `vp_vault_split`, which predates it and
writes another vault by host path, is an open question (see below).

**2. A move is copy + delete, and each half runs on its own side.**

- `vp vault copy <project>... --from <remote-url> [--at <sha>]` is **receiver-run**. It reads the
  source through a private blobless snapshot of the source's published remote, pinned to a commit.
  It copies each project's footprint (the tracked content under `Projects/<p>/` and `palace/<p>/`)
  and makes one commit carrying `Vp-Copy-*` trailers. It checks the committed footprint hash
  against the source's.
- `vp vault project delete <project>... (--moved-to <remote-url> | --discard)` is **source-run**.
  With `--moved-to`, it refuses unless the destination's published remote holds a verified copy
  of each project. The footprint must be equal at the copy commit, in that commit's trailer, at
  this vault's HEAD, and at the destination's tip. The delete then removes the footprint in one
  commit that also writes the departure record.
- A delete with no destination requires an explicit `--discard`, so a forgotten `--moved-to`
  cannot destroy the only copy.
- Deleting a project from a vault is `vp vault project delete`, never a hand `rm -rf`.

**3. Two supporting commands create and join vaults.**

- `vp vault init <path> --remote <name>=<url>...` creates a new, empty vault. It records the
  vault's remotes in a tracked `.vibe-palace/remotes.toml` and publishes the first commit to
  every remote. It writes only the vault it creates, and touches no checkout or host config.
- `vp vault clone <url> <path> [--bind <project>...]` clones a published vault and adds every
  remote recorded in `remotes.toml`. With `--bind`, it names the clone this host's **primary**
  vault for those projects, in one compare-and-set write of `[project_vaults]`. It becomes a
  second writer of that table, beside `vp config bind` (ADR-012).
- `vp config bind` takes several slugs, all written in one write.

**4. `init`, `copy` and `delete` publish exactly what they verified.** (`clone` makes no commit
and publishes nothing.)

- **The vault must be up to date first.** It must equal every remote's live tip before the
  command commits.
- **One commit, published unmodified.** The command makes one commit and pushes it unchanged to
  every remote: no rebase, no force, and no `--no-push`.
- **Reading the outcome.** The result is decided by whether each remote's live tip contains the
  commit, not by git's exit code. Only when the **first** remote does not take the commit (and no
  remote holds it) does the run roll back and ask for a new dry run. If a later remote fails, the
  commit stays published where it landed and the run is unfinished (`internal/storage/lifecycle_publish.go`,
  `PublishErrorKind`).
- **Blocking other committers.** A local pending marker blocks every other committer on the host
  while a run is unfinished. Re-running the same command finishes the run ("redo, never rebase").
  A mirror that diverged (holds something else while another remote holds the commit) is an
  operator matter, not something a re-run fixes.
- **Rollback.** A rollback uses `git reset --keep`, never `--hard`, so other sessions'
  uncommitted edits are never discarded.
- **Leftovers.** Delete removes the ignored leftovers that git cannot restore only after every
  remote holds the delete commit.

**5. Dry run → digest → `--expect` is the approval protocol for `vault copy`, `vault project
delete` and `vault clone`.** (`vault init` and `config bind` take `--dry-run` but print no digest.)
`--dry-run` runs the same plan function the real run executes first, and prints:

- every refusal;
- the file set;
- the push targets;
- a digest;
- the exact real-run command line, with `--expect` (and, for copy, `--at`) filled in and
  shell-quoted.

Given `--expect`, the real run refuses on any mismatch; `--expect` is optional, so a hand-typed run
without it re-plans but binds nothing. The digest binds what moves, not whole-vault HEADs, so an
unrelated push between the dry run and the run costs only a `vp vault pull`: the live check that
HEAD equals every remote refuses with "pull or push first", and after the pull the same printed
line succeeds.

Every lifecycle command prints its own undo lines, pasteable as printed. Only copy's and delete's
are a `git revert` plus one push per remote. Init's remove the new directory (`rm -rf <path>`) and
name the branch to delete on each git host; clone's remove the clone and, with `--bind`, restore
the host config backup. `config bind` prints the backup path instead.

**6. Departure records are the tracked statement that a project left a vault.**

- **Location.** There is one file per slug, at `Audits/departures/<slug>.json` (`internal/departure`).
  It sits under `Audits/` so that no project enumerator walks it, and per slug so that two hosts
  recording different slugs never conflict.
- **Contents.** The format is `vp-departure/1`. The kinds are `renamed`, `moved-to-vault` and
  `deleted`, with the optional fields `generation`, `copy_commit` and `footprint`.
- **Generation.** `generation` is one more than the highest generation in this vault's record
  history for the slug, and at least one more than the destination's own record for it
  (`internal/storage/departures.go`), so it rises across a move and its undo-and-redo.
- **The record wins over the directory.** While a record exists (of any kind, readable or not),
  the project is departed, whatever `Projects/<p>/` holds. Every write funnel refuses writes into
  a departed project (`internal/departedpath`), and so does every mutating MCP tool at the
  dispatch seam.
- **Only reviewed writers write a record.** A write under `Audits/departures/` needs the vault's
  live root-lock token (`atomicfile.ForDepartureRecord`, through
  `vaultfs.WriteDepartureRecord`/`RemoveDepartureRecord`). The raw file tools cannot forge or
  remove one, and the `departure-record-writer` source-audit rule pins every caller to a reviewed
  baseline entry. As of v8.2.0 those callers are `vp vault project delete` and, until it is
  retired, `vp_vault_split`'s purge (`internal/tools/vault_split_apply.go`).

**7. `commit-log.anchor` travels with its project.** It names a commit in the project repository,
not the vault, so a copy carries it.

**8. The upgrade is enforced by the surface gate: MCP surface 7 → 8, data format stays 2.**

- **Why the surface moved.** A v7 binary does not honour departure records. It misreads kind
  `deleted`, and its raw file tools can re-create a moved project's tree or tamper with a record.
  So the protection against a lagging host is the MCP surface-version gate, not the record
  itself. This corrects ADR-012 rule 7.
- **Why the data format did not.** Nothing already on disk needs migrating, and record fields are
  optional.
- **Release tag.** The release is v8.2.0 (ADR-011's `v<surface>.<format>.<build>`). The reasoning
  is recorded at the 7→8 bump in `internal/surface/version.go`.

**9. MCP exposure.** `vp_vault_copy` and `vp_vault_project_delete` take the CLI parameters plus
`action: "plan" | "apply"`, and act only on the vault their server serves.

- `vp_vault_project_delete` and `vp_config_bind` are stdio-only (`StdioOnlyToolNames`,
  `internal/tools/register.go`).
- `vp_vault_copy` is a mutating tool, so `vp mcp serve` exposes it only with `--allow-writes`.
- `vault init` and `vault clone` have no MCP surface.

## Consequences

- **Host coordination.** On the admin host a project move is `vp vault copy`, `vp vault project
  delete` and `vp config bind` (after `vp vault init` when the destination is new). On every other
  host it is `vp vault pull`, **then** `vp vault clone … --bind`: clone refuses until the default
  vault records the departure (see [doc/VAULT-LIFECYCLE.md](../VAULT-LIFECYCLE.md)).
  A host that pulls the delete is told where the project went, and its writes are refused until
  it binds.
- **Undoing a move.** It is plain git: revert the copy in the destination first, then revert the
  delete in the source, and push each. This works while the destination's footprint still equals
  the copy's. After that, plain-git undo no longer applies, and in v1 a project cannot be copied
  back into a vault that holds its departure record (`vp vault copy` refuses,
  `internal/storage/lifecycle_copy.go`).
- **Undoing after hosts have bound.** A host still bound to a vault that no longer holds the
  project is refused with a stale-binding error (ADR-012 tier 2).
- **Stranded work.** A host whose pull refuses because it holds unpushed work in a moved project
  recovers by undo-and-redo: revert both halves, push the stranded work, then re-run copy and
  delete. That is the supported recovery. The pull refusal also prints a manual one-host carry
  route (hold branch, carry the work to the destination, reset, pull again), but there is no graft
  command.
- **The gate.** It now refuses a departed project and a stale binding as well as an incompatible
  surface (ADR-010).
- **Every host must run v8 before the first move.** The first v8 stamped write raises the floor
  vault-wide.

## Open questions

- **Does copy + delete supersede `vp_vault_split` / `vp_vault_merge`?** Both are still in the MCP
  tool surface (`internal/mcp/tool_surface.golden.json`) and have no CLI verb. Both reach another
  vault by host path (split writes one, merge reads one), which Decision 1 forbids for new work.
  - The design plan records an intent to retire them after the first real move.
  - As of v8.2.0 they are not retired. Whether copy + delete covers every case they serve, and
    when to remove them, is **undecided**.
- **`vp vault rename`** (a rename inside one vault that writes a `renamed` record) is designed
  but not built as of v8.2.0. Its `--no-push` policy is not yet ruled.
