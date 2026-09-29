# `vp absorb` — migrating legacy agent-context files into the vault

`claude --init` and hand-authored agent-context files produced before
vibe-palace existed leave repo-local files (`CLAUDE.md`, `AGENTS.md`,
`.cursorrules`, `.rules`, `.github/copilot-instructions.md`) filled with
project knowledge — architecture, workflow rules, domain facts, test
strategy, build commands — that properly belongs in the palace vault.

`vp absorb` is a one-shot migration that:

1. Splits each supported agent-context file into sections.
2. Routes every section to the right vault file under
   `Projects/{slug}/` (flat layout, no `agentctx/` segment).
3. Rewrites each source file down to a preamble comment + its managed
   `vibe-palace:begin/end` block.
4. Writes a timestamped backup of every rewritten file to
   `.vibe-palace/<filename>.bak-<ts>`.

After absorb, a future `claude --init` can't silently re-introduce
competing knowledge: a full `vp check` run reports an Info row whenever an
agent-context file contains non-managed content.

## When to run

- Immediately after `vp init` on a project that already has a populated
  `CLAUDE.md` or other agent-context file.
- Any time `vp check` reports "N file(s) hold content outside the managed
  block" on its **Agent-file drift** row.

## Routing

| Heading pattern (case-insensitive, first-token) | Destination |
|---|---|
| `Architecture`, `Design`, `Package layout`, `Import direction`, `Data model`, `Move atomicity` | `doc/architecture.md` |
| `Testing`, `Test strategy`, `Coverage` | `doc/testing.md` |
| `Non-goals`, `Scope`, `Out of scope` | `doc/scope.md` |
| `Commands`, `Build`, `Run`, `Dev loop`, `Make targets` | `workflow.md` § Commands |
| `Workflow`, `When working in this repo`, `Conventions`, `Style`; also `Rules — …` / `Rules of the game` when the body is imperative (a line opening with `never`, `always`, `must`, `should` …, or `run make` / `run go` … anywhere) | `workflow.md` § Rules |
| `Overview`, `About`, `Status`, and any other unmatched heading with no space, tab, `-`, `—` or `:` (a project title, or a bare `Rules`) | `absorbed/resume-suggestions.md` (manual merge into `resume.md`) |
| `Notation`, `Glossary`, `Vocabulary`, `Rules — quick reference`, `Rules of the game` (non-imperative body) | `knowledge.md` |
| `Vibe-Palace Integration` (managed block) | **keep in place** |
| Any other unmatched heading (one containing a space, tab, `-`, `—` or `:`) | `doc/misc.md` |

Plaintext adapters (`.cursorrules`, `.rules`) have no heading structure.
Their whole-file body routes to `knowledge.md` regardless of content.

### Why resume goes through a scratch file

`resume.md` is curated narrative — absorb never auto-appends to it. All
resume-bound content (preamble, `Status`, `Overview`, project-title
sections) lands in `Projects/{slug}/absorbed/resume-suggestions.md` with
a `TODO: human merge` marker. Review and paste relevant bits into
`resume.md` yourself.

### Why `doc/*.md` gets pointer lines in the scratch file

`doc/` is **not** surfaced by bootstrap, which returns an index rather than
document bodies (the resume itself is fetched through `resume_uri`) — it's
read-on-demand reference material. To keep absorbed `doc/*.md` files discoverable,
absorb queues one-line pointers ("- see `doc/architecture.md` for
architecture reference") into the resume scratch file under a
`## Reference pointers (YYYY-MM-DD)` section. Paste them into `resume.md` in the
same merge pass so agents can find the content.

## Usage

```
vp absorb --dry-run            # print the routing plan, no writes
vp absorb                      # interactive: per-section prompt, [y]es / [s]kip / [A]ccept all / [q]uit
vp absorb --yes                # accept every proposed route
vp absorb --project my-proj    # override the detected slug
vp absorb --project-root PATH  # read source files from PATH instead of the cwd
vp absorb --no-stage           # skip `git add` on rewritten files
```

`--dry-run` exits with status 1 when any migration is pending, so scripts
can gate on it.

`--project-root` moves where absorb detects the slug and reads source files
from; it does not change which vault is written. Absorb resolves the vault
from the process working directory by the ADR-012 tiers (see
`doc/adr/012-vault-resolution-precedence-and-host-project-bindings.md`),
never from `--project-root`. Not every `vp` command does the same:
`vp config sync` resolves from its `--project-root`, and the vault git and
lifecycle commands take `--vault` and otherwise use the global vault.
Run absorb from inside the checkout so both point at the same project.

Absorb writes into the vault's working tree but does not commit it. Commit
the vault afterwards (`vp vault commit --paths … --message …`, or at wrap).

## Safety

- Absorb writes only into an initialised project: `Projects/{slug}/` must
  hold an init-scaffold marker (`commands/README.md` or `skills/README.md`)
  or real history (`resume.md`, `iterations.md`, or any file under
  `sessions/` or `tasks/`), as `storage.ClassifyProjectDir` judges it.
  Absorb bails with a clear message directing the user to `vp init`
  otherwise. Bare directory existence is not sufficient, and neither is a
  lone `Projects/{slug}/config.toml`: that per-project vault config is
  retired (v7.2.0) and is no longer an init marker.
- The slug must also be named by the checkout's `.vibe-palace.toml` or
  already have `Projects/<slug>/` in the vault
  (`project.RequireKnownProject`, checked even under `--dry-run`), and a
  departed project (one whose
  departure record says it moved to another vault, was renamed or was
  deleted; see `doc/VAULT-LIFECYCLE.md`) is refused.
- Content-hash dedup: every appended block carries an
  `<!-- absorb-hash: ... -->` marker so re-running absorb against the
  same (or byte-identical) input produces no duplicate subheadings.
- Dated subheadings, so a human can tell what arrived when:
  - `workflow.md`: under `## Commands` or `## Rules`, then
    `### From <sources> (YYYY-MM-DD)`;
  - other destinations (`knowledge.md`, `doc/*.md`): `## From <sources> (YYYY-MM-DD)`;
  - the resume scratch file: `## From absorb (YYYY-MM-DD)`, one
    `### <heading> (source: <file>)` per section, and pointer lines under
    `## Reference pointers (YYYY-MM-DD)`.
- Each vault append (destination read, dedup and append, and the resume
  scratch append) runs under the path's vault lock (ADR-003), so a
  concurrent write of the same file is not lost.
- Source rewrites are atomic (tmp + fsync + rename). Backups predate the
  rewrite, so recovery is always possible.
- Unsupported source files (adapter present but `Supported() == false`)
  emit a "recognized but not yet supported" message and are left
  untouched — never silently skipped. (All five current adapters are
  supported, so this path does not fire today.)

## Drift detection

A full `vp check` run includes an **Agent-file drift** row (it is not one
of the names `vp check --check` accepts). When any agent-context file
contains non-whitespace content outside the managed block, the row is
Info, not a warning or failure: "N file(s) hold content outside the managed
block", followed by the file names and "Run `vp absorb` to migrate into the
vault, or add `<!-- vibe-palace:allow-local -->` to suppress." Suppress
per-file with a `<!-- vibe-palace:allow-local -->` marker anywhere in the
file.
