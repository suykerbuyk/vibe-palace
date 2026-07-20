# Architecture deep-dive — subagent prompt template

The **depth** tier. Produces the reference a staff engineer consults before
changing a component: how it actually works, where the data goes, what it
guarantees, and what it merely hopes.

Presupposes a **breadth doc** for the same component (see `prompt-breadth-doc.md`),
which establishes *what it is*. This tier establishes *how it works*.

**Launch one subagent per binary/product** — not per repo. A repo that ships three
binaries gets three deep-dives; a repo that ships none gets a breadth doc and no
deep-dive.

## Slots

Fill every `{{...}}` before dispatching. `{{ENVIRONMENT_HAZARDS}}` and
`{{KNOWN_CONTEXT}}` are the two that decide whether the output is any good.

| Token | Source | Meaning |
|---|---|---|
| `{{PROJECT}}` | cartridge §0 | what the workspace is |
| `{{DOMAIN_LENS}}` | cartridge §1 | the business's centre of gravity — what makes a fact *interesting* |
| `{{ENVIRONMENT_HAZARDS}}` | cartridge §2 | **paste verbatim, never summarise** |
| `{{WRITE_SCOPE}}` | cartridge §3 | the one directory the agent may write |
| `{{KNOWN_CONTEXT}}` | **`resume.md`** | established facts *and corrections* earned so far |
| `{{ISSUE_ID_RANGE}}` | the register | the current high-water mark, so the agent doesn't invent IDs |
| `{{COMPONENT}}` | | binary/product name |
| `{{PATH}}` | | absolute path to its repo |
| `{{SHA}}` | | short SHA the doc is written against |
| `{{ONE_LINER}}` | breadth doc | what it is, in one line |
| `{{BREADTH_DOC}}` | | path to the tier-1 doc |
| `{{FOCUS}}` | breadth doc's `DEEP-DIVE SEED` | 2–4 questions this dive must answer |
| `{{DELIVERABLE}}` | | output path, e.g. `docs/architecture/<component>.md` |

`{{KNOWN_CONTEXT}}` is why late deep-dives are sharper than early ones. The method
does not improve across a run; the *state* does. Hydrate it from `resume.md` every
single dispatch, or every worker starts from zero and re-learns what the last one
already paid for.

---

Everything below the line is the prompt.

---

You are producing the **architecture document** for `{{COMPONENT}}` —
{{ONE_LINER}} — at `{{PATH}}`, SHA `{{SHA}}`.

This is part of a documentation programme mapping {{PROJECT}}.

{{DOMAIN_LENS}}

Read `{{BREADTH_DOC}}` first — it is the onboarding-level summary of this
component and your starting point. **Do not repeat it. Go deeper than it
everywhere.**

## Hard rules — violating these breaks the workspace

{{ENVIRONMENT_HAZARDS}}

**Write scope:** {{WRITE_SCOPE}}

## Established context

{{KNOWN_CONTEXT}}

Treat the above as a **starting hypothesis, not scripture.** If the code refutes
it, **say so loudly and cite the refutation** — a correction is worth more than a
confirmation, and this programme has already had several confidently-repeated
claims turn out to be false.

## Focus questions

This deep dive must answer:

{{FOCUS}}

## Deliverable

Write `{{DELIVERABLE}}`.

Length is whatever the truth requires — typically 4–10 pages. This is the
reference a staff engineer consults before changing the component. Depth over
breadth; **evidence over fluency**. Every non-obvious claim cites `path:line`
relative to the workspace root.

### Required sections

1. **Purpose and boundaries** — what this component is responsible for, and
   explicitly what it is *not*. Where it sits in the product line.
2. **External interfaces** — every way the outside world touches it: network
   protocols and ports, APIs (REST/gRPC/thrift/S3), CLI surface, config files,
   environment variables, signals, on-disk formats it reads or writes. For each:
   the wire/serialisation format and the file that defines it.
3. **Internal decomposition** — the real module/package/subsystem structure and
   what each part owns. Not a directory listing: an explanation of the **seams**.
4. **Data path** — trace a unit of data end to end: ingest → transform →
   placement → durability → read back. Where does it buffer, where does it fsync,
   where does it replicate or erasure-code, **where does it acknowledge**? What is
   the unit (object, block, chunk, shard, record)? Cite the code that does each
   step. *This is the section that finds the bugs.*
5. **State machines** — every explicit or implicit state machine: request
   lifecycle, node/cluster membership, device lifecycle, job/task states,
   reconciliation loops. Name the states **from the source** (enums, constants) and
   cite them.
6. **Concurrency and scheduling** — threads, goroutines, async runtimes, worker
   pools, locks, queues. What runs in parallel, and what serialises.
7. **Failure modes and recovery** — crash, partial write, node loss, disk loss,
   network partition, mid-write interruption. What is durable at each point, what
   is lost, what replays. **Be specific about what the code actually guarantees
   versus what it merely hopes.**
8. **Performance characteristics** — hot paths, buffer/chunk sizes, batching,
   caching, known bottlenecks, tunables that trade latency for throughput. Cite the
   constants.
9. **Configuration surface** — the knobs that matter, their defaults, and what
   breaks when they are wrong.
10. **Observability** — logs, metrics (name the metric families), traces, health
    endpoints. What would you actually look at at 3am?
11. **Security posture** — authn/authz, secrets handling, TLS, the privilege level
    of the process/container. **Note what is absent.** An unauthenticated data
    plane is a finding, not a footnote.
12. **Evolution and technical debt** — dead code, fossils, TODO/FIXME clusters,
    half-migrations, vendored drift. What would bite a newcomer?
13. **Provenance and unknowns** — the SHA, the files you actually read as
    `path:line`, and **an explicit list of what remains unknown**. An honest
    unknowns list is worth more than a confident guess. Anything you could not
    determine without running the code goes **here**, not into a plausible-sounding
    paragraph.

### Required diagrams (Mermaid)

Diagrams are a **deliverable, not decoration.** Minimum:

- **Context / component** (`flowchart`) — this component and its neighbours, with
  the protocol on each edge.
- **Data pipeline** (`flowchart`) — the data path from §4: where data is buffered,
  transformed, made durable, and acknowledged. Annotate edges with unit and format.
- **At least one state machine** (`stateDiagram-v2`) — from §5, states named
  exactly as the source names them.
- **At least one sequence diagram** (`sequenceDiagram`) — the single most important
  operation end to end (a write, a restore, a reconcile).
- **Deployment topology** (`flowchart`) — pods/processes/nodes/hardware and what
  runs where, *if it is deployed at all*.

Add more where they earn their place. Prefer one accurate diagram over three
speculative ones — and **if a diagram encodes a guess, say so in the caption.**

**Mermaid must parse.** The rules that avoid the common breakages:

- Wrap any label containing spaces, punctuation, `/`, `:`, `(`, or `-` in double
  quotes: `A["S3 PUT (multipart)"]`.
- Never use the bare word `end` as a node id — it terminates a block. Use `done`
  or `End_`.
- Give every node a short alphanumeric id; put the prose in the label.
- One diagram per fenced ```mermaid block.

## Return value

Your final message is **consumed programmatically, not read by a human.** Return
exactly:

1. A 5–10 sentence summary: what this component does, how its data path works, and
   the single most important thing learned.
2. `## ANSWERS TO FOCUS QUESTIONS` — each focus question answered in 1–3 sentences
   with a `path:line` citation, or explicitly marked **UNRESOLVED**.
3. `## CORRECTIONS` — anything in the established context or the breadth doc that
   the code **refuted**. If none, say "none". *Do not soften a refutation into a
   nuance; the register depends on these.*
4. `## NEW ISSUES` — findings for the issue register, each with a proposed severity
   (Critical/High/Medium/Low), a one-line title, and 2–4 sentences of evidence
   citing `path:line`. **Do not assign final IDs** — the register already runs
   {{ISSUE_ID_RANGE}}. If none, say "none".
5. `## UNKNOWNS` — what could not be determined without running the code.
