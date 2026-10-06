# Vault lifecycle: new vaults, moving projects, and per-host bindings

This guide covers the commands that create a vault, move projects between
vaults, and bind a project to a vault on one host:

| Command | Runs on | What it does |
|---|---|---|
| `vp vault init` | the host creating the vault | Creates a new, empty vault and publishes it to every remote |
| `vp vault copy` | the **receiving** vault | Copies projects in from another vault's published remote (with `--as <newname>`, copies one project under a new slug) |
| `vp vault rename` | the vault that holds the project | Renames a project from one slug to another in place, in one published commit that writes a `renamed` departure record |
| `vp vault project delete` | the vault the projects **leave** | Deletes projects in one published commit that writes their departure records |
| `vp config bind` | each host | Binds one or more projects to a vault in this host's global config |
| `vp vault clone --bind` | each other host | Clones a published vault and binds projects to it in one step |

The design and its reasoning are in two ADRs:
[ADR-012](adr/012-vault-resolution-precedence-and-host-project-bindings.md)
covers how a host resolves a project's vault, and
[ADR-013](adr/013-vault-project-lifecycle-and-departure-records.md)
covers the lifecycle commands and departure records. `vp <command> --help`
is the authoritative flag reference. This guide describes behaviour as of
v10.2.0.

## How a host finds a project's vault

Every vp entry point (CLI, MCP server, hooks) resolves the vault in three
tiers. The first tier that matches wins
([ADR-012](adr/012-vault-resolution-precedence-and-host-project-bindings.md) § Decision 1):

1. **The `.vibe-palace.toml` marker.** This is the nearest marker at or above
   the working directory (the search stops at `$HOME`), if it sets a
   top-level `vault_path`.
2. **A per-host binding.** This is the `[project_vaults]` entry in the global
   config for the `[project].name` in that same marker.
3. **The global default.** This is `vault_path` in the global config.

```toml
# ~/.config/vibe-palace/config.toml
vault_path = "~/vibe-palace-vault"

[project_vaults]
my-team-project = "~/team-vault"
```

A committed `.vibe-palace.toml` names the project and must not set
`vault_path`. A path belongs to one host, so each host binds the project in
its own global config instead. `vp check` flags a committed marker that sets
one.

Resolution **fails closed**. The following refuse instead of falling through
to the default vault:
- an unreadable marker
- tiers 1 and 2 naming different vaults
- a binding whose target is not an existing vault
- a malformed `[project_vaults]` table

## Departure records

When a project leaves a vault, the vault keeps a small tracked record of
where it went. The record lives at `Audits/departures/<project>.json`, with
one file per project (`internal/departure/departure.go`). It stores the
format tag `vp-departure/1`, the project slug, a `kind` (`moved-to-vault`,
`renamed` or `deleted`), the destination label `to` (empty for `deleted`),
and the date. When `vp vault project delete` writes it, it also carries the
footprint digest of the trees it removed, a per-project `generation`
counter and, for `--moved-to`, the verified `copy_commit`. A `vp vault
rename` record (kind `renamed`, `to` = the new slug) carries the `generation`
counter too.

While a project's departure record exists, that project counts as
**departed** in that vault, whatever `Projects/<project>/` holds:

- **Writes are refused.** This covers `vp vault write`/`edit`/`move`, the MCP
  vault tools, and every storage writer (capture, iterations, memory, tasks,
  knowledge graph). The refusal says the project departed and names where it
  went.
- **Tidy and commit leave it alone.** `vp vault tidy` reports the project as
  "left untouched: departed project", and an explicit `vp vault commit` into
  its tree refuses.
- **Pulls protect local work.** `vp vault pull` removes the departed
  project's tracked files and its host-local embedding cache. If this host
  has **unpushed** work under the project, the pull refuses with "refusing to
  bring … onto this host's work", then prints the steps to carry that work
  across. See [If a host's pull refuses](#if-a-hosts-pull-refuses).
- **Only two writers write records**: `vp vault project delete` and
  `vp vault rename`. Ordinary writes (the raw vault file tools and every
  storage writer) cannot create, edit or delete anything under
  `Audits/departures/`.

Only a `git revert` of the delete commit restores a departed project. A
`vp init` re-scaffold does not.

## The common rules of the lifecycle commands

- **Dry run first, then paste the printed line.** `vault copy`,
  `vault rename`, `vault project delete` and `vault clone` print a plan, a
  digest, and a line under `To run it:`. That line carries `--expect
  <digest>`, so the real run refuses if anything changed since the plan. Every
  printed line is shell-quoted and can be pasted as printed. `vault init` and
  `config bind` take `--dry-run` too, but print no digest.
- **They publish one commit, unmodified, to every remote.** This applies to
  `init`, `copy`, `rename` and `delete`, and `copy` and `rename` have no
  `--no-push`.
  - **If the first remote refuses** the commit, the run rolls back. `init`
    also removes the new directory if no remote took the commit.
  - **If a later remote fails** (unreachable, or a mirror that diverged), the
    commit stays on the remotes that took it. A pending marker
    (`.git/vp-lifecycle-pending`) then makes every other vp commit and pull
    on that vault refuse. Fix the cause and re-run **the same command**; do
    not start a new dry run.
- **They print their undo, and the undo blocks differ.** Run each block
  only as printed, and only the block of the step you are undoing.
  - `copy` prints `undo:` and `delete` prints
    `Undo (restores everything but the leftovers):`. Each is a
    `git -C <vault> revert --no-edit <commit>` line followed by one push
    line per remote.
  - `init` and `clone` also print an undo block, but theirs **removes the
    new directory** (`rm -rf <path>`). Init's also names the branch to
    delete on each git host, and clone's restores `config.toml.bak` when
    `--bind` wrote the config.
- **They read other vaults only through git.** `copy` reads the source vault
  only through its **published remote URL** (`--from`), never a host path.
  `delete --moved-to` checks the destination the same way.
- **They act on this host's default vault unless you pass `--vault`.** The
  working directory does not matter. Without `--vault <path>`, `copy`,
  `delete`, `vault pull`, `vault sync` and `vault status` act on the global
  `vault_path`. With it, they act on the named vault (the top level of its
  own git repository).

## The commands

### `vp vault init`: create a new vault

```bash
vp vault init ~/team-vault \
  --remote origin=git@git.example.com:team/vault.git \
  --remote mirror=git@github.com:me/team-vault.git --dry-run
# then the same line without --dry-run
```

`init` requires three things:
- `<path>` must not exist, and must not be inside a vault or a git work tree.
- Every `--remote` must be reachable and **empty**. A non-empty remote
  refuses, and nothing is published.
- The first `--remote` becomes the upstream.

On success, `init` records the remotes in the tracked
`.vibe-palace/remotes.toml`, makes one commit on `main`, and pushes it to
every remote. It is not `vp init`: it onboards no project, touches no
checkout, and writes no host config.

### `vp vault copy`: bring projects into this vault

It acts on the **receiving** vault: your default vault, or the one
`--vault` names:

```bash
vp vault copy proj-a proj-b --from git@github.com:me/vibe-palace-vault.git \
  --vault ~/team-vault --dry-run
# then paste the "To run it:" line
```

`copy` reads the source through a private blobless snapshot of its remote:
the commit history and the directory listings, without file contents. It
then fetches the contents of the projects' files in one request.
Each project's footprint is its trees under `Projects/<project>/` and
`palace/<project>/`. `copy` copies each footprint and makes **one** commit
with `Vp-Copy-*` trailers. It then checks that the committed footprint
hashes equal the source's, and publishes that commit to every remote.

What that costs, for a project of a few thousand files:
- **The dry run downloads the projects' files too**, and so does the real
  run, each into a snapshot of its own that it removes afterwards. The
  download comes before the destination is checked, so a copy that the
  receiving vault then refuses (it already holds the project, say) has
  downloaded the files first.
- **A re-run after a partial publish downloads them again** before it
  finishes the publish, and cannot finish while the source remote is
  unreachable.
- **A commit in the receiving vault waits, with no message, while a copy's
  push runs.** The copy holds that vault's lock while it commits and until
  every remote has the commit. On a slow link that can be minutes.
- The fetches, the commit and the push each have a 30-minute limit.

A vault that holds a departure record for a project refuses a copy of that
project ("a project cannot be copied back over its own departure in v1").

`--at <sha>` pins the source commit; the dry run prints it. Undo is a
`git revert` of the copy commit plus one push line per remote, and the run
prints them.

**`--as <newname>`** copies a **single** named project in under a new slug:
`copy` brings it in, then renames it in this vault, so the project lands at
`<newname>` without first colliding with any `<project>` the vault already
holds. It requires exactly one project (you cannot copy a batch under one
name). `--expect <digest>`, with `--as`, binds the copy half of the plan.

### `vp vault rename`: rename a project in place

It acts on the vault that holds the project — your default vault, or the one
`--vault` names — and renames one project's slug without it leaving:

```bash
vp vault rename old-name new-name --dry-run
# then paste the "To run it:" line
```

`rename` moves the project's footprint (`Projects/<old>/` to
`Projects/<new>/` and `palace/<old>/` to `palace/<new>/`), rewrites every
stored identifier that named the old slug, and writes a `renamed` departure
record for the old slug. It makes **one** commit with `Vp-Rename-*` trailers,
checks that nothing still names the old slug, and publishes exactly that
commit to every remote — or refuses and rolls back. The target must be a
**fresh** slug: a rename onto a project the vault already holds is refused up
front. There is no `--no-push`; with a remote, the command refuses unless
HEAD is already at every remote tip and then publishes immediately. With no
remote it commits locally.

After the tracked commit publishes, the rename runs its **host-local index
step**: it renames this host's index store `index/<old>/` to `index/<new>/`
and re-labels it. The embed cache is rebuilt, not carried (`<new>` re-embeds
lazily); chunks, ledger and baseline survive. A crash in this window leaves a
rename-pending marker that the next run clears.

What `rename` deliberately does **not** touch:
- the project checkout's `[project].name` and any host binding — the
  departed-slug alert names those fixes;
- the knowledge-graph defaults, which are unchanged;
- cross-project links, which are not rewritten;
- the `files_changed` history.

Undo is a `git revert` of the rename commit plus a push; to also move this
host's index store back, run `vp vault rename --undo` after the revert (old
back, new gone). `--expect <digest>` refuses unless the plan still digests to
the value the dry run printed.

### `vp vault project delete`: remove projects from the vault they leave

Once the copy is published, run it against the **source** vault. That is
your default vault when the source is your default; otherwise pass
`--vault <path>`:

```bash
vp vault project delete proj-a proj-b \
  --moved-to git@git.example.com:team/vault.git --dry-run
# then paste the "To run it:" line
```

The two modes are:
- `--moved-to <url>` is refused unless the destination's published remote
  holds a verified copy of each project. The footprint must match at the
  copy commit, in its trailer, at this vault's `HEAD`, and at the
  destination tip.
- `--discard` deletes with no destination and writes a `deleted` record.

In both modes the vault must equal every remote's live tip. If another host
pushed after your dry run, the real run refuses with "pull or push first"
and writes nothing. Pull, then run the same printed line again. If that
other push changed a moving project, the rerun refuses because the
footprints no longer match. In that case, run the copy's undo lines and
redo from the copy.

`delete` writes one commit that removes the trees and adds the departure
records, and publishes it to every remote without rebasing. The ignored
leftovers, which `git revert` cannot restore, are removed only once every
remote holds the commit, and the dry run lists them. If a run stopped
partway, running the same command again finishes it.

### `vp config bind`: bind projects to a vault on this host

```bash
vp config bind proj-a proj-b --vault ~/team-vault --checkout ~/code/proj-a --dry-run
```

`bind` writes one `[project_vaults].<slug>` line per slug in the global
config, all in **one** write.

It refuses in these cases:
- **For a moved project:** this host's default vault must record the
  project as moved (if it does not, pull first). The target vault must
  hold the project. One of the target vault's remotes must equal the
  recorded destination; `--allow-unlabelled` accepts a record that names no
  destination.
- **For a project born in the target vault:** such a project has no
  departure record, so pass `--new`.
- **Always:** it refuses a target vault that holds a departure record for
  the project. It never re-points an existing binding, never writes a
  checkout, and never creates the global config.

Each `--checkout` is re-resolved after the write to prove the binding took
effect. Any failure restores the file.

**Reload your AI host afterwards:** a running MCP server resolved its vault
at startup.

### `vp vault clone --bind`: join a vault on another host

```bash
vp vault clone git@git.example.com:team/vault.git ~/team-vault --bind proj-a proj-b --dry-run
# then paste the "To run it:" line
```

`clone` adds and fetches every remote the vault records. The `<url>` must
be one of them, and it refuses if a mirror is unreachable or has diverged.
The clone must be at this binary's data format.

With `--bind`, `clone` also binds the listed projects in one write. It
checks three things first:
- This host's default vault must record each project as moved to one of
  the clone's remotes. If it refuses with "pull first", run `vp vault pull`
  in the default vault and retry.
- The clone must hold each project.
- The clone must record none of them as departed.

`clone` makes no commit and no push in any vault.

## Worked example: move two projects into a new team vault

This is the sequence proven end to end by
`internal/integration/lifecycle_e2e_test.go`. That test runs the real
binary across two hosts, with vault paths that contain spaces, and runs each
printed line exactly as printed. Host **A** does the move. Host **H** is
every other host that works on the projects.

**Before you start:** every host that uses these vaults must run a v8
binary (`vp version --surface` prints `surface: 8` or later). After
installing it, restart every AI host on that host: a running older
`vp mcp` is not replaced by the install. The first v8-stamped write raises
the vault's surface floor for every host.

**On every host:** run `vp vault sync`, then
`vp vault status`. Each remote must read `in sync`. A host with unpushed
work under a moving project will hit the refusal in the last section. Stop
sessions on the moving projects on each host until that host has finished
step 6.

**On host A:**

1. `vp vault init ~/team-vault --remote origin=<team-url> [--remote mirror=<url>]`.
   Run it with `--dry-run` first, then without.
2. `vp vault copy proj-a proj-b --from <source-vault-remote> --vault ~/team-vault --dry-run`,
   then paste the printed line. **Keep the `undo:` lines this copy prints**
   (not step 1's, which remove the new vault).
3. `vp vault project delete proj-a proj-b --moved-to <team-url> --dry-run`,
   against the source vault (add `--vault <path>` if the source is not your
   default vault), then paste the printed line. **Keep the `Undo` lines it
   prints.**
4. `vp config bind proj-a proj-b --vault ~/team-vault`, then reload the AI
   host.

**On each host H:**

5. `vp vault pull` (it acts on the default vault). This brings in the
   departure records and removes the moved trees.
6. `vp vault clone <team-url> ~/team-vault --bind proj-a proj-b --dry-run`,
   then paste the printed line, then reload the AI host.

From then on, a session in any checkout of `proj-a` or `proj-b` reads and
writes the team vault on both hosts.

### Undo

On host A, run the `undo:` lines that step 2 printed, **then** the `Undo`
lines that step 3 printed. Each set is a `git revert` plus one push line per
remote. Run them in that order.

Undo is plain git, so it applies only while the destination still holds
exactly what the copy put there. `git revert` does not check this for you.
Before undoing, confirm that this prints nothing:

```bash
git -C ~/team-vault log --oneline <copy-commit>..origin/main -- Projects/proj-a palace/proj-a
```

Repeat it for each project. Once anyone has written to the project in the
destination, plain-git undo no longer applies. In v1 a project cannot be
copied back into a vault that holds its departure record (`vp vault copy`
refuses), so moving it back is not supported by the commands as of v8.2.0.

After the undo, every host, A included, pulls **both** vaults:
`vp vault pull` and `vp vault pull --vault ~/team-vault`. Once the
team-vault pull brings in the copy revert, any write through the old
binding refuses with `stale project binding`, because vp will not write to
a vault that no longer holds the project. To clear the binding, remove the
project's `[project_vaults]` line (or restore `config.toml.bak`). If you
removed the line, bind again after a redo. If you left it, it works again
once the redo lands and the host has pulled both vaults.

To redo the move, A runs `vp vault pull` in the source vault and repeats
steps 2–3.

### If a host's pull refuses

If step 5 refuses with "refusing to bring … onto this host's work", host H
had unpushed work under a moving project. Nothing was merged, and HEAD and
the working tree are unchanged.

The supported recovery is to **undo and redo the move**. This is the path
the end-to-end test proves, and the one
[ADR-013](adr/013-vault-project-lifecycle-and-departure-records.md) specifies:
1. A runs the undo lines (see [Undo](#undo)).
2. H runs `vp vault pull`, then `vp vault sync`, to publish its work.
3. A runs `vp vault pull` and repeats steps 2–3. The copy now includes H's
   work.
4. H does steps 5–6.

The refusal also prints a manual route for one host: keep the work on a
`hold/departed-<project>-<date>` branch, carry it to the destination vault,
reset, and pull again. The end-to-end test does not cover that route, and
its "bind this host first" step refuses (`pull first`) until the host's
default vault holds the departure, which happens only after the reset. Use
undo-and-redo.
