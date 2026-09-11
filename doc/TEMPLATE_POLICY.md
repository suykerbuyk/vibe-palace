# Template Write Policy

This document records which code paths may change a vault `Templates/`
file, and the backup policy for the one that removes an override.

## Golden path

No template-byte writer remains. Nothing writes embedded template bytes
over a vault `Templates/` file, and nothing materializes one: the
embedded floor serves every built-in. `templates.Executor`, the writer
such writes once routed through, was deleted with its last callers. Three
paths change `Templates/`, and each only removes a file or adds one
beside it:

- `vp config sync` removes only vp's own mirrors — a byte-identical copy
  of a built-in — never an override. On an unversioned vault,
  `applyMaterialize`'s prune removes the primary with a raw `os.Remove`
  after re-hashing it against the SHAs the plan recorded (no `.bak`). On
  a git vault the prune is `storage.PruneMirrorsVerified`, which removes
  through `vaultfs.Delete` — compare-and-set under the path's lock —
  only after the HEAD and remote-tip checks.
- `vp config sync`'s `resolveTemplatePrompts` writes the `n` answer's
  `.new` sidecar directly, beside the operator's file and never over it.
- `vp commands reset` / `vp skills reset` remove the overrides the
  operator names, on request only, after keeping a content-named backup
  of each (see *Backup mechanics*). They remove through `vaultfs.Delete`,
  compare-and-set on the bytes backed up.

Routing the two raw writes (the unversioned prune's `os.Remove` and the
`.new` sidecar) through the lock funnel is owned by
`template-tree-raw-vault-writes-bypass-the-lock-funnel`.

| Caller | Command surface | Effect on `Templates/` | Backup |
|---|---|---|---|
| `commands.Reset` | `vp commands reset`, `vp skills reset` | Removes each named override | `templates.PreserveBackup`: named by content, never overwritten; none for a byte-identical mirror |
| — | `vp commands upgrade`, `vp skills upgrade` | None: each override is reported as `[keep]` | Not applicable |

`reconcile.TemplateTree.Apply` (`vp config sync`) never writes a
template either, and reports a Create or Update action as an error. Its
Update — the old `o` answer to a diverged-override Prompt, and `--yes` —
overwrote the operator's override with what was then
`BackupPolicyAlways`, and was the first step of a chain in which the next
sync pruned the result and committed the deletion to every host. The
answer and the policy were removed together
(`vault-template-override-is-discarded-by-config-sync`).

## Outside the golden path: `reconcile.applyUpgrade`

`internal/reconcile.applyUpgrade` — the config **schema upgrade** path behind
`vp config sync` / `vp config upgrade` — is a template-adjacent writer that
was never part of the template write path (it never routed through the
since-deleted `templates.Executor`). It does not materialize
template bytes onto disk; it merges the *missing canonical keys* from a template
into a config the user already owns. It therefore has its own `.bak` rule, and
the two halves are deliberately asymmetric:

| Branch | Target | `.bak`? | Rationale |
|---|---|---|---|
| vault (`vaultRoot != ""`) | `{vault}/Projects/<slug>/config.toml` | **No** | `*.bak` is in `storage.CanonicalGitignorePatterns`, so a `.bak` written inside the vault is never committed and never synced — it is host-local litter, not a durable backup. The recoverable pre-image is the committed `config.toml` itself. The write goes through `storage.LockedUpdate` → `atomicfile.Write`, which owns the temp file and the rename; the old fixed-name `config.toml.tmp` is gone too, because a shared sidecar name is exactly what two concurrent upgraders collide on. |
| host-local (`vaultRoot == ""`) | CWD project config, global config | **Yes** | These files live outside the vault. Nothing commits them and nothing syncs them, so the `.bak` is the only pre-image they have. This branch keeps its raw `backup` + temp + rename verbatim and is deliberately **not** routed through `atomicfile` — it must not inherit atomicfile's permission/fsync semantics or the vault surface stamp. |

## What gets materialized at all (override-only, iter 319)

The golden-path table says who may change a `Templates/` file. It does
**not** say what is classified as a template at all. Since iteration 319
`commands.Plan` is **override-only**, and that decision comes first:

- **No vault copy → `ChangeUnneeded`.** Nothing is written. The embedded floor
  (precedence Tier 5, `internal/context/precedence.go`) already serves the
  resource, and the bytes a write would produce are that floor verbatim. A
  byte-identical vault copy is not a no-op: it is a Tier 4 override that
  shadows the binary, so the next release's `wrap.md` or `restart.md` would be
  silently ignored. `vp config sync` classifies exactly such a mirror as
  reconciler-owned garbage and plans its deletion (ADR-008 Phase 3), so
  writing one puts the two commands into a loop over the same paths.
- **Vault copy differing from embedded → `ChangeOverride`.** A genuine local
  override. The upgrade commands list it as `[keep]` and never change it;
  only `commands.Reset`, behind a reset verb the operator names, removes one.
- **Vault copy matching embedded → `ChangeUnchanged`.** A mirror. The upgrade
  commands skip it; `vp config sync` prunes it; a named reset removes it with
  no backup.

`ChangeUnneeded` is deliberately distinct from `ChangeUnchanged`: "unchanged"
asserts a vault copy was compared and matched, and in the unneeded case there
is no vault copy to have compared. `Plan` has no create case: `ChangeNew` was
deleted with `commands.Apply`, its last consumer.

This applies to **both** surfaces — `vp commands upgrade`, `vp skills
upgrade` and both reset verbs share `commands.Plan`, and skills resolve
through the same five-tier chain with the same embedded floor.

## Backup behaviour (changed 2026-09-11)

Until 2026-09-11 the policy was asymmetric. `vp commands upgrade
--overwrite` reset a vault override to the embedded bytes and kept no
backup (`BackupPolicyNever`); `vp skills upgrade --overwrite` renamed the
file to one sibling `.bak`, which the next reset overwrote
(`BackupPolicyRename`). This section recorded that as a user-visible
inconsistency, and said that changing either side belonged "in a separate
PR with release notes". The change that stopped the upgrade commands
resetting overrides, and added the named reset verbs, is that PR. It
unified both surfaces on a backup that is never overwritten — the first of
the options the old note listed — and removed the policy switch
(`templates.BackupPolicy`) with the writer that took it. Its release note:

> Behaviour change — vault `Templates/` overrides and the upgrade commands.
> Requires `make install` on every host (MCP surface v5).
>
> - `vp commands upgrade` and `vp skills upgrade` no longer reset a vault
>   `Templates/` override of a built-in, in any mode. `--overwrite` now
>   accepts only vp-owned changes: command and skill shims, agent-file
>   managed blocks, the project `.gitignore` and the commit hook. Each
>   override is listed as `[keep]` and left byte-for-byte. Before,
>   `--overwrite` — the documented non-TTY path, and where `vp check`
>   remedies lead — replaced every override with the embedded copy,
>   keeping no backup for commands and one overwritable `.bak` for skills.
> - Resetting an override is an explicit, named operation:
>   `vp commands reset NAME` / `vp skills reset NAME`. `--dry-run`
>   previews it. It removes the file, so the built-in serves it, instead
>   of writing a copy of the built-in into `Templates/`. Afterwards run
>   `vp commands upgrade --overwrite` (or `vp init`) in each project, so
>   its shims stop carrying the removed override's text.
> - Backups are never overwritten, for commands and skills alike. Each is
>   named by its content, `<file>.<first 12 hex of its sha256>.bak`, so
>   resetting identical bytes twice reuses one backup. A different file
>   never takes an existing name. The bare `<file>.bak` is not used, and
>   existing `.bak` files are left untouched. On a git vault backups are
>   gitignored and stay on the host; a vault replicated by a file-sync
>   tool carries them to every host. This replaces the policy this
>   document recorded before (commands: none; skills: one `.bak`,
>   replaced by the next reset).
> - On a git vault that is its own repository, the removal is committed
>   locally, scoped to the removed files, with a message naming the
>   files, the backups and the host; `vp vault sync` publishes it. vp
>   never commits into a repository that encloses the vault.
> - `vp skills upgrade` now only reports.
> - Existing `Projects/<slug>/{commands,skills}/README.md` stubs keep
>   their old wording. vp writes a stub only when it is absent, so delete
>   one to get the new text.
> - `MCPSurfaceVersion` rises from 4 to 5. Once an upgraded host makes a
>   stamped write (a task write or session capture; a reset or
>   `vp commands upgrade` stamps nothing) and a lagging host has pulled
>   it, that lagging host is refused on vault writes (exit 2,
>   `git pull && make install`).

## Backup mechanics

There is one policy, for every template reset, commands and skills alike,
and deliberately no policy parameter. It lives in
`internal/templates/backup.go`:

- `templates.BackupName(rel, data)` names a backup by its content:
  `<rel>.<first 12 hex of sha256(data)>.bak`, as `internal/archive` names
  its backups. The same bytes always get the same name, so a dry run
  prints exactly the name a real run uses. The name always ends in `.bak`
  (every lister, the stamp resolver and the canonical `.gitignore` skip
  that suffix) and never contains a `:`, so it is valid on Windows.
- `templates.PreserveBackup(vaultRoot, rel, data)` keeps a copy under that
  name and never overwrites anything to do it:
  - nothing at the name: the backup is created through `vaultfs.Create`;
  - a regular file holding exactly `data`: nothing is written, and the
    backup is reported as reused, so resetting the same bytes twice keeps
    one backup;
  - anything else (different bytes, a symlink, a directory):
    `ErrBackupCollision`. The file there is left alone, the error tells
    the operator to move or rename it and rerun, and the reset removes
    nothing.
- `vaultfs.Create` is the locked create-if-absent primitive: `Write`'s
  refusals (`.git`, `.vp-locks`, task files), `ResolveSafePath`, the path
  lock, an `os.Lstat` inside the lock (`ErrExists`), then
  `atomicfile.Write` with fsync, so the backup is durable before the
  removal. Its residual — a non-vp writer inside the lock window — is made
  harmless by content-addressed names.
- The bare `<rel>.bak` is never written or read. Older binaries overwrite
  that fixed name on every reset, so it can never be trusted to hold a
  backup, and an existing one is left for its owner.
- A byte-identical mirror of a built-in needs no backup and gets none.

`commands.Reset` writes every backup before it removes anything, then
removes each file through `vaultfs.Delete`, compare-and-set on the bytes
backed up, so an edit landing after the backup is kept.

Backups inside a git vault are ignored (`*.bak` is in
`storage.CanonicalGitignorePatterns`) and stay on the host that wrote them;
a reset that finds git would not ignore one still writes it, and warns that
`vp config sync` restores the canonical ignore lines. A vault replicated by
a file-sync tool carries backups to every host, which is harmless: older
binaries can only overwrite the fixed `<file>.bak`.

## What lives where

- `internal/templates/backup.go` — `BackupName`, `PreserveBackup`,
  `ErrBackupCollision`, and `HashFile`. The one backup primitive for a
  template reset.
- `internal/templates/embedded.go` — `WalkEmbedded`,
  `EmbeddedSHA`, and the `Resource` value type.
- `internal/reconcile/template_tree.go` — still owns the
  override-only decision table, the silent-adopt pre-pass, and lock-
  file integration. It writes no template bytes; the prune's verified
  `os.Remove` is the raw exception named under *Golden path*.
- `internal/commands/upgrade.go` — `Plan`, the override-only
  classification the upgrade commands and the reset verbs share. It
  writes nothing.
- `internal/commands/reset.go` — `Reset`: every path checked (a symlink
  in any component, or a non-regular file, refuses the whole call with
  `ErrUnsafeResetPath`), every backup written, then the compare-and-set
  removals; and `ShimSourceRemoved`, which drives the shim notice.
- `cmd/vp/template_reset.go` — `vp commands reset` / `vp skills reset`:
  name validation, the git preflight (identity, staged changes), the
  output, and the commit according to the vault's git shape (committed
  only on the vault's own repository, never pushed).
- `internal/vaultfs/write.go` — `Create`, the locked no-clobber create a
  backup goes through.
- `internal/storage/vaultsync.go` — `CommitRemovals`, the reset's own
  locked stage→commit sequence: one local commit carrying exactly the
  removed paths, never pushed. On a stage or commit failure it unstages
  exactly those paths under the vault commit lock, so the removal is
  left unstaged.
