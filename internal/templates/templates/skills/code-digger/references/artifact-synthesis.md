# The synthesis document

The final tier, and **the actual deliverable.** Everything before it — the breadth
docs, the deep-dives, the register — is raw material. This is the document someone
reads to understand *the product as one system*.

Write it **last**, and write it yourself. It is the one artifact that cannot be
delegated: its value is entirely in the connections between components, and no
subagent has ever seen more than one.

## What it is not

- Not a summary of the other docs. If it can be assembled by concatenating
  abstracts, it is worthless — the reader could do that.
- Not an index. There is a `README.md` for that.
- Not exhaustive. It should be **short relative to its inputs** (the origin run:
  ~350 lines synthesising ~14,000).

## What it is

The answers to the questions that **no single component's doc can answer**, because
each one lives in the seams:

1. **What the product line actually is** — in the vocabulary a customer or an
   executive would use, then in the vocabulary of the repos. Most readers cannot
   map one to the other, and that is the first thing they need.
2. **How a byte moves through the product, end to end.** The single most valuable
   section. Follow one unit of real data from the client's `PUT` to the physical
   medium and back out again, naming every component it touches and every format it
   is transformed into. *No component doc contains this, because no component sees
   it.*
3. **How the product is built and shipped.** The build graph, the registry, the
   charts, what CI does and does not do. Where "what CI runs" and "what the
   Makefile does" diverge — because that divergence is where the shipped artifact
   stops matching the source you just read.
4. **The load-bearing seams.** The interfaces where two components make
   *incompatible assumptions*, or where one's guarantee is weaker than the other's
   requirement. These are only visible from above, and they are where the next
   outage comes from.
5. **The security posture as one picture.** Individually: an unauthenticated
   service, a weak credential, a root container. Together: a chain. The register
   lists them as separate findings; only here can you draw the line through them.
6. **Cross-cutting themes worth a human's hour.** The patterns that recur across
   components — a whole product line that never runs its tests, a family of services
   that all default to no auth. A theme is worth more than the sum of its findings
   because it points at a **process**, not a bug.
7. **What is deliberately not here.** The parts of the product that live outside
   what you could see, and what you know about them. An honest boundary is worth
   more than a confident map with a fabricated edge.
8. **How to navigate from here.** Where to go next, for which question.

## Discipline

- **Every claim traces to a component doc or to source.** The synthesis is where
  fluency is most tempting and most dangerous: it is the document most likely to be
  quoted, and the one furthest from the code. If a sentence cannot be traced, cut it.
- **The byte's journey is the spine.** If you get one section right, make it that
  one. Everything else in the document is scaffolding around it.
- **Name what you could not establish.** A synthesis that admits "we could not
  determine whether anything deploys this component, and here is what one sentence
  from a human would settle" is *more* useful than one that quietly picks an answer.
- **Do not smooth over the contradictions between components.** They are the
  findings. Two implementations of the same tape operation, one correct and one not,
  is the most interesting sentence in the document — not an embarrassment to be
  tidied into a generality.

## The test it must pass

> **Hand it to an engineer who has never seen the codebase. Can they now say, in
> their own words, what the product does, how data flows through it, and where they
> would look first if it lost a customer's data?**

If yes, the run succeeded — whatever else it produced.
