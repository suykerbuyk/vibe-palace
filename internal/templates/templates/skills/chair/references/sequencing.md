## Sequencing dependent units

When several units have real dependencies between them — not just "listed
together" but one's output feeding the next one's input — order the work
accordingly rather than fanning everything out at once. Land and integrate
the foundational unit first; only fan out the units that depend on it once it
has actually **merged**, not once it has merely been implemented, since a
downstream plan may need to be written against the foundation's real,
reviewed shape rather than a guess at what it will look like. A review often
finds a claimed dependency is false, or a claimed independence isn't — trust
the review stage's own file/API-surface findings to settle which units are
genuinely independent, not the original grouping.

**A single shared integration branch can only advance in its own commit
order.** If several units are staged onto one branch and merged to the main
line incrementally as each clears review, the main line can only ever advance
through that branch's history in the order the commits actually landed on
it — merging a later-cleared unit necessarily drags along every earlier
commit on that branch as its ancestor, reviewed or not. Either sequence the
review order to match the integration order actually wanted, or give
independent units separate branches if they truly need to land out of order.

---

