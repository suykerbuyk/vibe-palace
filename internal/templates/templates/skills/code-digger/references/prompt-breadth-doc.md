# Breadth doc — subagent prompt template

The **breadth** tier. Produces the onboarding map: what a repo *is*, how to build
it, how to test it, what it depends on, and where the bodies are buried. One per
**repo** (unlike the deep-dive, which is one per **binary**).

Its second job is to **brief its own deep-dive.** A breadth doc that ends without
a usable seed forces the orchestrator to re-read the repo to write the focus
questions, which defeats the tiering.

## Slots

| Token | Source |
|---|---|
| `{{PROJECT}}` | cartridge §0 |
| `{{DOMAIN_LENS}}` | cartridge §1 |
| `{{ENVIRONMENT_HAZARDS}}` | cartridge §2 — **paste verbatim** |
| `{{WRITE_SCOPE}}` | cartridge §3 |
| `{{KNOWN_CONTEXT}}` | `resume.md` |
| `{{ISSUE_ID_RANGE}}` | the register's high-water mark |
| `{{REPO}}` / `{{PATH}}` / `{{SHA}}` | |
| `{{DELIVERABLE}}` | e.g. `docs/repos/<repo>.md` |

---

Everything below the line is the prompt.

---

You are producing the **onboarding document** for the repo `{{REPO}}` at
`{{PATH}}`, SHA `{{SHA}}` — part of a documentation programme mapping {{PROJECT}}.

{{DOMAIN_LENS}}

The reader is a competent engineer who has never seen this repo and needs to be
productive in it. Not a tutorial: a **map**, with the traps marked.

## Hard rules — violating these breaks the workspace

{{ENVIRONMENT_HAZARDS}}

**Write scope:** {{WRITE_SCOPE}}

## Established context

{{KNOWN_CONTEXT}}

A starting hypothesis, not scripture. **If the code refutes it, say so and cite
the refutation.**

## Deliverable

Write `{{DELIVERABLE}}` — typically 1–3 pages. Every non-obvious claim cites
`path:line` relative to the workspace root.

### Required sections

1. **What it is** — one paragraph a newcomer could repeat back. What it produces
   (binaries? a library? IDL? charts?), who consumes it, and where it sits in the
   product line. **If it ships no binary, say so** — that decides whether it gets a
   deep-dive at all.
2. **Layout and entry points** — the directory structure that matters and the
   `main()`s. Not a `tree` dump: the parts a newcomer must find first.
3. **Build** — how it is actually built, and **by what**. Name the build file that
   governs (`qb.yaml` / `Makefile` / `CMakeLists.txt` / `Cargo.toml`) and, when
   they disagree, **which one CI actually runs**. That disagreement is where the
   findings live. Every command is `[unverified]`.
4. **Test** — what test suites exist, how many, how they are invoked, and — the
   question that matters — **are they actually run by CI?** Count them by *running*
   the count (e.g. `grep -rl 'testing\.[TB]' | wc -l`) and cite the command — never
   eyeball a number you then report. The same discipline covers **every** count you
   state in this doc — CI `jobs:` keys, tools, subcommands: run the count, show the
   command and its integer (e.g. `yq '.jobs | keys | length' ci.yml`). A suite that
   exists and never runs is a finding, not a feature. And when a suite exists but CI never
   runs it, do not stop at the coverage gap: **the un-run tests are the authors' own spec.**
   Skim them for the invariants they assert (a vendor/identity string, a status code, an
   on-disk layout) and flag any the code visibly contradicts — that contradiction is a
   high-confidence finding the codebase will not otherwise confess.
5. **Deploy** — how it ships: image, chart, DaemonSet, systemd unit, nothing at
   all. If it is not deployed anywhere you can find, **say that explicitly** — it
   changes the severity of everything else in the repo.
6. **Dependencies** — what it needs, at what version, and **from which copy**
   (vendored? submodule? sibling working tree? registry tag?). Cite the file that
   pins it. Never reason about this from the general rule; check the build file.
7. **Gotchas** — the traps. Fossils, half-migrations, config that lies, defaults
   that differ between the Makefile and CI, a flag that silently changes
   correctness. This section is usually the most valuable one in the doc. Two traps
   to hunt for *actively*, because neither appears in a diff: **(a) the defect no
   comment points at** — do not let your findings all trace back to a `TODO` or an
   author's admission; reason about correctness from the code itself. **(b)
   duplicated implementations of one responsibility** — the same write/parse/auth
   logic living in two places, or a shared primitive that some callers
   re-implement privately instead of using; a change to one copy silently leaves the
   others wrong. Name each copy's `path:line` and flag it for the register. **(c) on-disk
   names that are not portable** — for any filename, directory key, or ID the code writes,
   check what characters the components can contain against NTFS/exFAT reserved chars
   (`: \ / * ? " < > |`) and case-collisions; **read the encoder** rather than trusting the
   word "encoded". A store that cannot exist on a shipped OS is a finding, not a footnote.
8. **Provenance** — the SHA, and the files you actually read, as `path:line`.

### 9. Deep-dive seed  *(required — this is the handoff)*

End the doc with a section titled exactly `## 9. Deep-dive seed`, containing:

- **2–4 focus questions** the architecture deep-dive must answer. Good ones are
  specific, answerable from source, and load-bearing — *"where is a write
  acknowledged, and is it durable at that point?"*, not *"how does it work?"*
- **File anchors** for each — the `path:line` a deep-diver should start from.
- **Open unknowns** — what you could not determine, and what it would take to
  determine it.

The orchestrator lifts this section verbatim into the deep-dive's `{{FOCUS}}`
slot. Write it for that consumer. **A vague seed produces a vague deep-dive.**

## Return value

Your final message is consumed programmatically, not read by a human. Return:

1. A 3–5 sentence summary of what this repo is and what it ships.
2. `## SHIPS BINARIES` — the list of deployable binaries/products (which become
   deep-dive candidates), or "none".
3. `## SEED` — the deep-dive seed from §9, verbatim.
4. `## CORRECTIONS` — anything in the established context the code refuted, or
   "none".
5. `## NEW ISSUES` — findings for the register: proposed severity
   (Critical/High/Medium/Low), one-line title, 2–4 sentences of evidence citing
   `path:line`. **Do not assign final IDs** — the register runs {{ISSUE_ID_RANGE}}.
   Or "none".
