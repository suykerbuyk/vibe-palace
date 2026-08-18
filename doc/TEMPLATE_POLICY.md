# Template Write Policy

This document captures the centralized `.bak` (backup) policy for
every code path that materializes embedded template bytes onto disk.

## Golden path

Every template-write in the project routes through
`internal/templates.Executor.Write`. The caller passes a
`WriteOptions{Backup: BackupPolicy}` value; the Executor owns the
atomic write, directory creation, and (conditional) `.bak` emission.

| Caller | Command surface | Policy | Rationale |
|---|---|---|---|
| `reconcile.TemplateTree.Apply` (Update action) | `vp init` materialize | `BackupPolicyAlways` | The reconciler's Update path only fires after the decision table classifies a file as safely auto-upgradeable (vault matches lock, embedded bumped). Preserving the pre-existing bytes as `.bak` is cheap insurance against a corrupted lock entry mis-classifying a user edit as safe to overwrite. |
| `reconcile.TemplateTree.Apply` (Create action) | `vp init` materialize | `BackupPolicyNever` | Fresh materialization — nothing to preserve. |
| `commands.Apply` | `vp commands upgrade` | `BackupPolicyNever` | See asymmetry note below. |
| `commands.ApplyWithBackup` | `vp skills upgrade` | `BackupPolicyRename` | See asymmetry note below. |

## What gets materialized at all (override-only, iter 319)

The table above says how a write is backed up. It does **not** say that a
write happens. Since iteration 319 `commands.Plan` is **override-only**, and
that decision comes first:

- **No vault copy → `ChangeUnneeded`.** Nothing is written. The embedded floor
  (precedence Tier 5, `internal/context/precedence.go`) already serves the
  resource, and the bytes a write would produce are that floor verbatim. A
  byte-identical vault copy is not a no-op: it is a Tier 4 override that
  shadows the binary, so the next release's `wrap.md` or `restart.md` would be
  silently ignored. `vp config sync` classifies exactly such a mirror as
  reconciler-owned garbage and plans its deletion (ADR-008 Phase 3), so
  writing one puts the two commands into a loop over the same paths.
- **Vault copy differing from embedded → `ChangeUpdated`.** A genuine local
  override. This is the only case that reaches `Apply`, and the table above is
  what governs its `.bak`.
- **Vault copy matching embedded → `ChangeUnchanged`.** Skipped, as before.

`ChangeUnneeded` is deliberately distinct from `ChangeUnchanged`: "unchanged"
asserts a vault copy was compared and matched, and in the unneeded case there
is no vault copy to have compared. `Plan` therefore never emits `ChangeNew`;
the constant remains only because `Apply`/`ApplyWithBackup` accept a
caller-built `[]Change` and still owe a create path the correct policy (no
prior bytes, so never a `.bak` regardless of the caller's choice).

This applies to **both** surfaces — `vp commands upgrade` and `vp skills
upgrade` share `commands.Plan`, and skills resolve through the same five-tier
chain with the same embedded floor.

## The commands-vs-skills asymmetry

**User-visible inconsistency, preserved for now:** `vp commands
upgrade` never emits `.bak`; `vp skills upgrade` always does. The
asymmetry predates this refactor. Reasons it was left in place:

1. Skills are typically longer, more user-customized artifacts
   (multi-file directories, often edited in place); command
   templates are usually shorter and less touched. The historical
   choice was to be more defensive with skills.
2. Changing either side is a user-facing behavior change and belongs
   in a separate PR with release notes, not a refactor that
   advertises no behavior change.

After this refactor, the policy is controlled by a single switch
(`templates.BackupPolicy` passed through `WriteOptions`) rather than
two divergent writer implementations. A follow-up PR should:

- Either **unify** on `BackupPolicyAlways` for both surfaces (safer
  default), or
- Expose an `--backup / --no-backup` CLI flag so the user picks per
  invocation, or
- Ship the "commands = never, skills = always" choice as an
  **explicitly documented** user-facing guarantee.

**Follow-up tracking:** Item not yet logged as a Top-10 task; flag
here so a future sprint picks it up.

## Backup mechanics

Three policies are defined in `internal/templates/executor.go`:

- `BackupPolicyNever` — overwrite atomically; no `.bak` on disk.
- `BackupPolicyAlways` — read-then-write the `.bak` sibling before
  the atomic rename of the primary. The primary stays readable
  throughout the write window (no interval where `dst` is missing).
- `BackupPolicyRename` — rename the existing target to `.bak`, then
  atomic-write the new bytes. There is a brief window where the
  primary does not exist. Matches the legacy `commands.ApplyWithBackup`
  byte-for-byte so the skills-upgrade golden-path tests pin.

`Always` and `Rename` have the same steady-state output (user-edited
bytes end up in `.bak`, new bytes in the primary). They differ only
under partial-failure: `Always` leaves the primary intact if the
atomic rename of the new bytes fails; `Rename` leaves the caller
with a `.bak` and a missing primary.

A single-generation `.bak` is considered adequate. Users who need
multi-generation backups should snapshot externally (`git`,
Time Machine, etc.) before invoking `vp * upgrade`.

## What lives where

- `internal/templates/executor.go` — `Executor.Write`, `HashFile`,
  and `Classify` helpers. This is the single template-write primitive.
- `internal/templates/embedded.go` — `WalkEmbedded`,
  `EmbeddedSHA`, and the `Resource` value type. Unchanged by this
  refactor.
- `internal/reconcile/template_tree.go` — still owns the
  materialize decision table, the silent-adopt pre-pass, and lock-
  file integration. Delegates every individual write to
  `templates.Executor.Write`.
- `internal/commands/upgrade.go` — `Plan`, `Apply`, and
  `ApplyWithBackup` collapse into a shared `applyWithPolicy` helper
  that picks the `BackupPolicy` and delegates to
  `templates.Executor.Write`.
