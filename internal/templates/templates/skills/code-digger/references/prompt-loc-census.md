# Prompt — LOC census: how much code does this organisation actually own?

**Load this when the question is an INVENTORY question, not an architecture one:** *how much
code do we own? how much of it is alive? how much is test?* It is the same read-only,
evidence-over-names discipline as the rest of `code-digger`, pointed at a different output.

**Paste `references/subagent-harness.md` §hazards into the prompt verbatim first.** Nothing
else reaches a subagent.

---

## The law of this prompt

> **A line count without its definition is not a fact, it is a vibe.**

Every figure you report carries its definition in the same breath, or it is worthless. The
number changes by **40%** depending on choices most people make silently.

---

## The four numbers that are NOT the same number

State which one you are giving. Never give one and let the reader assume another.

| | What it is |
|---|---|
| **Raw tracked lines** | everything, including comments and blanks |
| **Code lines** | `cloc`'s "code" column — comments and blanks removed |
| **Product code** | code minus test code |
| **Everything on disk** | includes build output and untracked junk — **never** use this |

`{{RAW}}` and `{{CODE}}` differ by ~25%. `{{CODE}}` and `{{PRODUCT}}` can differ by **half**.

---

## Definitions you must fix BEFORE counting

**"Owned IP"** = authored by this organisation. **EXCLUDE**, and count each exclusion
separately so the human can challenge it:

1. **Upstream OSS the org merely consumes.**
2. **Generated code** — protobuf/thrift output, gRPC stubs, mocks, **and lockfiles**
   (`package-lock.json`, `Cargo.lock`, `go.sum` — routinely 50 K+ lines on their own).
   Real code; not *authored* IP.
3. **Build artefacts.**

**"Actively maintained"** — **measure it, do not assume it.** Per repo: last commit date,
commits in 90 / 365 days, distinct authors in 365 days. Classify ACTIVE / MAINTAINED /
DORMANT and **report the totals split that way**. A single number that mixes a repo last
touched in 2021 with one touched last week is a confidently-wrong number.

> If the "actively maintained" split turns out **not** to shrink the number — say so plainly.
> A filter that filters nothing is a finding, not a disappointment.

---

## THE THREE TRAPS. All three have produced a wrong answer in practice.

### 1. `git ls-files` lists a submodule as a DIRECTORY, and `cloc` will recurse into it

A gitlink is a single entry in `git ls-files`, and it is a **directory**. Hand that list to
`cloc` and it walks *into* the submodule — swallowing untracked and vendored content that
every exclusion rule you wrote exists to keep out, **and double-counting any sibling repo the
submodule shadows**.

Observed: a first run reported **835,264 lines of JavaScript**; the largest hand-written `.js`
file in that product was **3,345 lines**. The rest was Vuetify and Vue re-entering through a
submodule copy of a repo already counted at the top level.

**Filter to regular files** (`[ -f "$p" ]`) and list submodules explicitly as their own
codebases. **This can move the total by over a million lines.**

### 2. Most vendored OSS is NOT in a directory called `vendor/`

Verify by **evidence**, never by path:
- copyright headers (`grep -il 'copyright' | grep -v <org>`),
- `LICENSE` / `AUTHORS` naming a non-org owner,
- `git remote -v` — a `github.com/<someone-else>` remote excludes; an internal remote includes.

Observed: **61% of vendored OSS sat in ordinary source paths** — `gui/js/`, `container/cfg/`,
`src/fs/tests/utils/`. A directory-name census overcounted owned IP by **~108,000 lines**.

### 3. A DOMAIN word is not a BUILD word

`target/` is a Rust build dir — **and also the SCSI target daemon in a storage company.** An
exclusion rule that fired on the name deleted **13,239 lines of real product C**.

Before excluding any directory by name, **look inside it.** Narrow the rule (`target/debug/`,
`target/release/`) rather than banning the word. The same applies to `build/`, `dist/`, `ext/`,
`external/` — in one workspace, `ext/` held the org's **own** flagship code.

---

## The check that saves you: find the number that CANNOT be true

Do not merely ask "does this look plausible?" — plausible is how a wrong number survives.
Assert the identities that **must** hold:

- **code ≤ raw.** A code-only count larger than a raw count including comments is
  **impossible** — that is how trap 1 was caught.
- No single file may exceed the total of its language.
- Per-repo figures should sum to the total. **If they do not, find out why before reporting.**
  (Legitimate cause: `cloc` counts identical file *content* once, even across repos. A file
  owned twice is still owned once — but you must *know* that is the reason.)

---

## Method

- **Use a real tool: `cloc`, `tokei`, or `scc`.** They handle comment stripping properly.
- **If none is installed, DO NOT hand-roll one and present it as equivalent.** Report raw
  lines and *say so*. A hand-rolled estimate in one census came out **15% low** — and it erred
  in the direction that flattered the product number. Ask for the tool; it is one package.
- **`git ls-files` is the file set.** It lists only tracked files, which excludes build output
  for free.
- Classify test-vs-product by path (`test/`, `*_test.go`, `gtest_*`, `*.feature`, …). Say so —
  test helpers living in product directories will be miscounted, and that is acceptable if
  stated.

---

## Deliverable

The artifact shape is in **`references/artifact-loc-census.md`**. It must carry:

1. **The headline, with its definition in the same breath.**
2. **Per-repo table**: code | comments | blanks | last commit | 90 d | 365 d | authors | class.
3. **Language mix.**
4. **Excluded, and how much** — OSS / generated / artefacts, each with line counts, so the
   human can see the size of what was left out and challenge any call.
5. **Judgement calls in their own bucket**, never silently folded into either total.
6. **Limits**, plainly stated.
7. **Cleanliness proof** — `git status --porcelain` empty for every repo touched.

**Be honest over impressive.** If the owned-and-alive figure is far smaller than the total,
that is the finding. Do not round up. Mark anything inferred `[unverified]`.

---

## The finding that is usually waiting for you

**Count the test code separately, and look at the ratio.** In the workspace this prompt was
written from, **52% of all owned code was test code — more test code than product** — and
**none of it had ever been executed**, because CI compiled it out. The census did not just
size the asset; it sized *the thing nobody was running*.

If the org has a "we don't run our tests" problem, **the LOC census is where its scale becomes
undeniable.** Cross-reference the register.
