A reviewer's job is to **re-derive claims from source**, not evaluate the
implementor's own summary of them. "I checked X" is not verification; running
the check yourself and quoting the actual output is. This applies whether the
reviewer is another implementor, a Chair-local pass, or a headless
`second-opinion` model.

**Two kinds of reviewer conflict — and only one is the Chair's to settle.** A contradiction
between two different-model reviewers of equal standing is a signal to act, not to pick the more
confident one or to run another round hoping they converge. Which action depends on what the
conflict rests on:
- **On a verifiable FACT** (settleable by reading source): the Chair re-derives the one pivotal
  fact from source itself. Isolate the single claim the verdict turns on, open the decisive
  source, settle it, and only then issue a revision brief.
- **On an ARCHITECTURAL / design judgment** — an approach choice, a tradeoff, anything touching
  project architecture, doctrine, or ADR/PRD scope — that no re-derivation of a fact can settle:
  this is OPERATOR-BINDING. The Chair does not decide it unilaterally; it escalates to the
  operator with both positions and a recommendation (per the Mandate's "Ask the operator" list,
  which already names architectural decisions, doctrine, ADR and PRD, and the "pause for the
  operator" rule).

The test: can the disagreement be settled by re-deriving a fact from source? If yes, the Chair
settles it; if it rests on an architectural/design judgment, it is the operator's call — escalate.

**The strongest form of re-deriving a claim is trying to break it.** Revert
the fix and confirm the test now fails; delete a table entry and confirm a
completeness gate catches it; comment out a guard and confirm the regression
it exists to prevent reappears. A check that has never been shown capable of
failing has not been shown to work — reading the code is necessary but not
sufficient when the artifact under review is itself a test or a gate.

**A green from a gate the implementor NARROWED proves only what it ran.** A subset
(`ctest -R …`, `go test -run …`, a single package) that passes has not exercised the paths
it excluded. For anything that could regress a broad or safety-sensitive surface, the Chair
requires the FULL gate — or a baseline-diff of the full gate — before declaring green, the
same way it re-derives a claim instead of trusting the summary of one.

**Narrowing a claim where it is DEFINED does not narrow it where it is USED.**
A claim that gets properly qualified in the section that defines it routinely
survives unqualified in every section that merely *relies* on it — and the
confident copy usually sits inside a recommendation, which is the half a
skimming reader obeys. A narrowing edit is therefore not finished at the
definition site: **grep the document for the claim and its paraphrases, and
report the list of sites, not just the one you fixed.** Same shape as "a
deletion is derived, never reported" and "a fix scoped to one branch can be
undone by integration" — the fix you made is not the fix that shipped until you
have gone looking for the copies.

