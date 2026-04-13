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
competing knowledge: `vp check` warns whenever an agent-context file
contains non-managed content.

## When to run

- Immediately after `vp init` on a project that already has a populated
  `CLAUDE.md` or other agent-context file.
- Any time `vp check` reports "agent-context file contains content
  outside the managed block."

## Routing

| Heading pattern (case-insensitive, first-token) | Destination |
|---|---|
| `Architecture`, `Design`, `Package layout`, `Import direction`, `Data model`, `Move atomicity` | `doc/architecture.md` |
| `Testing`, `Test strategy`, `Coverage` | `doc/testing.md` |
| `Non-goals`, `Scope`, `Out of scope` | `doc/scope.md` |
| `Commands`, `Build`, `Run`, `Dev loop`, `Make targets` | `workflow.md` § Commands |
| `Workflow`, `When working in this repo`, `Conventions`, `Style`, `Rules` (imperative body) | `workflow.md` § Rules |
| `Overview`, `About`, `Status`, single-word project title | `absorbed/resume-suggestions.md` (manual merge into `resume.md`) |
| `Notation`, `Glossary`, `Vocabulary`, `Rules — quick reference`, `Rules of the game` | `knowledge.md` |
| `Vibe-Palace Integration` (managed block) | **keep in place** |
| Unrecognized | `doc/misc.md` |

Plaintext adapters (`.cursorrules`, `.rules`) have no heading structure.
Their whole-file body routes to `knowledge.md` regardless of content.

### Why resume goes through a scratch file

`resume.md` is curated narrative — absorb never auto-appends to it. All
resume-bound content (preamble, `Status`, `Overview`, project-title
sections) lands in `Projects/{slug}/absorbed/resume-suggestions.md` with
a `TODO: human merge` marker. Review and paste relevant bits into
`resume.md` yourself.

### Why `doc/*.md` gets pointer lines in the scratch file

`doc/` is intentionally **not** loaded by bootstrap — it's read-on-demand
reference material. To keep absorbed `doc/*.md` files discoverable,
absorb queues one-line pointers ("- see `doc/architecture.md` for
architecture reference") into the resume scratch file under a
`## Reference pointers` section. Paste them into `resume.md` in the
same merge pass so agents can find the content.

## Usage

```
vp absorb --dry-run            # print the routing plan, no writes
vp absorb                      # interactive: per-section y/s/q prompt
vp absorb --yes                # accept every proposed route
vp absorb --project my-proj    # override the detected slug
vp absorb --project-root PATH  # override the cwd
vp absorb --no-stage           # skip `git add` on rewritten files
```

`--dry-run` exits with status 1 when any migration is pending, so scripts
can gate on it.

## Safety

- Vault-project existence predicate is `Projects/{slug}/config.toml`.
  Absorb bails with a clear message directing the user to `vp init` when
  it's missing. Bare directory existence is not sufficient because
  `vp init` also creates empty `tasks/done` subdirs.
- Content-hash dedup: every appended block carries an
  `<!-- absorb-hash: ... -->` marker so re-running absorb against the
  same (or byte-identical) input produces no duplicate subheadings.
- Dated subheadings: every append lands under a
  `## From <source> (YYYY-MM-DD)` header so a human can tell what
  arrived when.
- Source rewrites are atomic (tmp + fsync + rename). Backups predate the
  rewrite, so recovery is always possible.
- Unsupported source files (adapter present but `Supported() == false`)
  emit a "recognized but not yet supported" message and are left
  untouched — never silently skipped.

## Drift detection

`vp check` includes an **Agent-file drift** row that warns when any
agent-context file contains non-whitespace content outside the managed
block. Suppress per-file with a `<!-- vibe-palace:allow-local -->`
marker anywhere in the file.
