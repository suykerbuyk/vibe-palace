# Artifact — the LOC census report

The shape of the document produced by `references/prompt-loc-census.md`. Lives with the other
docs (`docs/loc-census-<date>.md`), dated, because **the number is a snapshot and pretending
otherwise is a lie**.

---

## Structure

```
# <Product> LOC census — how much code does <org> actually own?

**Date** · **Method** (name the tool and its version) · **Repos counted** · **Cleanliness**

## The headline
> ## <N> lines of <org>-authored code
> ## — of which <T> (x%) is TEST and <P> (y%) is product

**Definition, in the same breath:** ...

## The findings that matter more than the total     <- 2-4 of them
## Per repo                                          <- the evidence table
## Language mix
## What was excluded, and how much                   <- OSS / generated / artefacts
## Judgement calls                                   <- its own bucket. Never folded in.
## The trap that caught us                           <- see below
## Limits — read before quoting it
```

---

## Rules

**The definition rides with the number, always.** Not in a footnote — in the same sentence.
Someone will quote the headline into a slide with no context, and the definition is the only
thing standing between them and a wrong claim.

**Give the split, not just the total.** Test vs product; active vs dormant. A single total is
almost always the least useful number you can produce.

**Show what you excluded, with its size.** `176,437 lines of upstream OSS` is *checkable*.
"We excluded vendored code" is not. The reader must be able to challenge any individual call,
which means seeing it.

**Judgement calls get their own bucket and their reasoning.** Three or four paths will be
genuinely arguable — code vendored in from another *internal* repo, headers supplied by a
commercial SDK the org modifies. **Do not silently fold these into either total.** Name them,
state the call, state what it would cost to reverse it (`if X owns those headers, deduct
~2,400`).

**Record the traps you fell into.** Not for confession — because the next census will be
tempted by the same shortcuts, and a rule with a scar on it survives where a rule without one
gets "simplified" away. Every trap in `prompt-loc-census.md` is there because it produced a
wrong number that looked completely reasonable.

**State the limits before someone finds them.** Attribution by provenance, not `git blame`.
Activity measured per repo, not per file. Test/product split by path. Any of these can be
attacked; better that you attacked them first.

---

## Split the report from the recipe

**The deterministic half of a census — the tool invocation and the exclusion list — belongs in
a tracked, executable recipe in the project** (a `make` target, a script), **not in this
document and not in an LLM prompt.**

The exclusion list is a set of *facts* about that specific codebase (which files are Vuetify,
which are lz4, which are generated). Facts belong somewhere that runs the same way every time.
Re-deriving them from a prompt is how you get a different answer next quarter — and
re-derivation is exactly what produced the 835 K phantom-JavaScript error.

So:

| | Where it lives | Why |
|---|---|---|
| the numbers, the exclusion list, the checks | **tracked recipe in the project** (`make loc-census`) | facts, mechanical, must not drift |
| the method, the traps, the definitions | **this skill** | expertise, cross-project |
| the org's own hazards and paths | **the project's agent cartridge** | project truth, one home |
| the report itself | **`docs/loc-census-<date>.md`** | a dated snapshot |

**The test of a good census: a human can re-run it with one command and get the same number,
without an agent in the loop at all.** If they cannot, you have written a story, not a census.
