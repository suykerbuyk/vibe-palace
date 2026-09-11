# Template Write Policy

This document captures the centralized `.bak` (backup) policy for
every code path that writes embedded template bytes onto disk.

## Golden path

Every write of embedded template bytes over a vault template routes
through `internal/templates.Executor.Write`. The caller passes a
`WriteOptions{Backup: BackupPolicy}` value; the Executor owns the
atomic write, directory creation, and (conditional) `.bak` emission.

Two template-adjacent writes do **not**, and are named here so the
sentence above stays true. On an unversioned vault, `applyMaterialize`'s
prune removes the primary with a raw `os.Remove` after re-hashing it
against the SHAs the plan recorded (no `.bak`). And `vp config sync`'s
`resolveTemplatePrompts` writes the `n` answer's `.new` sidecar
directly. (On a git vault the prune is `storage.PruneMirrorsVerified`,
which removes through `vaultfs.Delete` — compare-and-set under the
path's lock — only after the HEAD and remote-tip checks.) Routing them through the lock funnel is owned by
`template-tree-raw-vault-writes-bypass-the-lock-funnel`.

| Caller | Command surface | Policy | Rationale |
|---|---|---|---|
| `commands.Apply` | `vp commands upgrade` | `BackupPolicyNever` | See asymmetry note below. |
| `commands.ApplyWithBackup` | `vp skills upgrade` | `BackupPolicyRename` | See asymmetry note below. |

`reconcile.TemplateTree.Apply` (`vp config sync`) is **not** a caller any
more: it never writes a template, and reports a Create or Update action
as an error. Its Update — the old `o` answer to a diverged-override
Prompt, and `--yes` — overwrote the operator's override with
`BackupPolicyAlways`, and was the first step of a chain in which the next
sync pruned the result and committed the deletion to every host. The
answer and the policy were removed together
(`vault-template-override-is-discarded-by-config-sync`).

## Outside the golden path: `reconcile.applyUpgrade`

`internal/reconcile.applyUpgrade` — the config **schema upgrade** path behind
`vp config sync` / `vp config upgrade` — is the one template-adjacent writer
that never routed through `templates.Executor.Write`. It does not materialize
template bytes onto disk; it merges the *missing canonical keys* from a template
into a config the user already owns. It therefore has its own `.bak` rule, and
the two halves are deliberately asymmetric:

| Branch | Target | `.bak`? | Rationale |
|---|---|---|---|
| vault (`vaultRoot != ""`) | `{vault}/Projects/<slug>/config.toml` | **No** | `*.bak` is in `storage.CanonicalGitignorePatterns`, so a `.bak` written inside the vault is never committed and never synced — it is host-local litter, not a durable backup. The recoverable pre-image is the committed `config.toml` itself. The write goes through `storage.LockedUpdate` → `atomicfile.Write`, which owns the temp file and the rename; the old fixed-name `config.toml.tmp` is gone too, because a shared sidecar name is exactly what two concurrent upgraders collide on. |
| host-local (`vaultRoot == ""`) | CWD project config, global config | **Yes** | These files live outside the vault. Nothing commits them and nothing syncs them, so the `.bak` is the only pre-image they have. This branch keeps its raw `backup` + temp + rename verbatim and is deliberately **not** routed through `atomicfile` — it must not inherit atomicfile's permission/fsync semantics or the vault surface stamp. |

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

- Either **unify** on a backup that is never overwritten for both
  surfaces (the direction `upgrade-overwrite-resets-vault-template-overrides`
  records; `BackupPolicyAlways`, the old candidate, is gone), or
- Expose an `--backup / --no-backup` CLI flag so the user picks per
  invocation, or
- Ship the "commands = never, skills = always" choice as an
  **explicitly documented** user-facing guarantee.

**Follow-up tracking:** Item not yet logged as a Top-10 task; flag
here so a future sprint picks it up.

## Backup mechanics

Two policies are defined in `internal/templates/executor.go`:

- `BackupPolicyNever` — overwrite atomically; no `.bak` on disk.
- `BackupPolicyRename` — rename the existing target to `.bak`, then
  atomic-write the new bytes. There is a brief window where the
  primary does not exist, and a failed write leaves a `.bak` and no
  primary. Matches the legacy `commands.ApplyWithBackup` byte-for-byte
  so the skills-upgrade golden-path tests pin.

A single-generation `.bak` is considered adequate. Users who need
multi-generation backups should snapshot externally (`git`,
Time Machine, etc.) before invoking `vp * upgrade`.

## What lives where

- `internal/templates/executor.go` — `Executor.Write`, `HashFile`,
  and `Classify` helpers. This is the single template-write primitive
  (see the raw exceptions named under *Golden path*).
- `internal/templates/embedded.go` — `WalkEmbedded`,
  `EmbeddedSHA`, and the `Resource` value type. Unchanged by this
  refactor.
- `internal/reconcile/template_tree.go` — still owns the
  override-only decision table, the silent-adopt pre-pass, and lock-
  file integration. It writes no template bytes; the prune's verified
  `os.Remove` is the raw exception named under *Golden path*.
- `internal/commands/upgrade.go` — `Plan`, `Apply`, and
  `ApplyWithBackup` collapse into a shared `applyWithPolicy` helper
  that picks the `BackupPolicy` and delegates to
  `templates.Executor.Write`.
