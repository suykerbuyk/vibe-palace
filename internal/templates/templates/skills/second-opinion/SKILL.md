---
name: second-opinion
description: >
  Run an adversarial review of a plan, a diff, a design, or a file by driving a DIFFERENT
  LLM headlessly as a subagent, then reconcile its findings against your own. Triggers on
  "get a second opinion", "have Grok review this", "adversarial review", "red team this
  plan", "what would another model say", "check my review", "cross-model review", or any
  request to have an independent model verify work before it is acted on. Also triggers
  when you have just produced a review, an audit, or a set of findings and the stakes
  justify an independent pass. Use it whenever the failure mode is "the same model
  re-confirming its own reasoning".
---

# Second Opinion — adversarial review by an external model

You are running a **different model** as a subagent, on purpose, because a review by the
same model that authored the work is largely self-confirmation. The external model exists
to disagree with you. Its value is entirely in the divergences.

**The one rule that makes this safe:** the external model's output is a **witness
statement, not a verdict**. Nothing it says enters a register, an escalation, a task, or a
human's inbox until *you* re-derive it from source. This is not caution for its own sake —
in a prior run on this vault an external pass produced two confident findings that named a
function which did not exist; acting on them would have shipped code that failed at
runtime. Treat every returned finding as a lead.

---

## Provider

**Grok is implementation #1 and the only one verified.** Keep the invocation behind the
single `run_external_review` shape below so a second provider is a new config block rather
than a rewrite. Do not build a general provider abstraction until a second CLI actually
exists on the host — there is none today.

Detect before you invoke: `command -v grok`. If absent, say so and stop; do not silently
fall back to reviewing the work yourself and calling it a second opinion.

---

## The verified invocation contract

Everything in this block was **executed** on 2026-07-31 against `grok` 0.2.112
(`~/.local/bin/grok`, model `grok-4.5`) unless explicitly marked otherwise.

```sh
grok --cwd <repo-root> \
     --prompt-file <path-to-prompt> \
     --permission-mode plan \
     --output-format json \
     --json-schema '<schema>'
```

- **`--prompt-file <PATH>`** ✅ verified. Use it, never `-p "<huge string>"` — review
  prompts carry pasted rules and run to kilobytes, and shell quoting will corrupt them.
- **`--json-schema '<schema>'`** ✅ verified. The response JSON gains a
  **`structuredOutput`** key holding the parsed object. Parse that, not `text`.
- **`--output-format json`** ✅ verified. Returns `text`, `structuredOutput`, `stopReason`,
  `num_turns`, `usage`, `total_cost_usd`, `sessionId`.
- **`--cwd <dir>`** ✅ verified — scopes the run to a repo without your shell entering it.

### 🔴 The containment trap — read this before you trust any flag

**`--disallowed-tools Write,Edit,Bash,NotebookEdit` DOES NOT BLOCK WRITES.** Verified by
execution: a run carrying that exact flag was asked to create a file and **created it**.
Whatever those names match, they are not the write path. Never rely on it.

**What actually contains the run — `--permission-mode plan`.** ✅ Verified: the same write
attempt under plan mode returned `stopReason: "Cancelled"`, exit 2, and **no file was
created**. A full 5-turn read-and-review under plan mode completed normally
(`stopReason: "EndTurn"`) with the target repo's `git status` clean afterwards. Plan mode
permits reading and forbids mutation, which is exactly the shape of a review.

**`--sandbox <profile>`** is the strongest primitive and is **fail-closed by design** — it
refuses to start rather than run with deny paths unprotected. It needs `bubblewrap`
(`bwrap`) and profiles declared in `~/.grok/sandbox.toml` or `.grok/sandbox.toml`
(`extends = "workspace"`). ⚠ `bwrap` was **absent** on the host measured, so `--sandbox`
was unusable there and is `[unverified]` end-to-end. Prefer it over plan mode wherever
`bwrap` exists; verify with `command -v bwrap` first and fall back to plan mode.

Valid `--permission-mode` values: `default`, `acceptEdits`, `auto`, `dontAsk`,
`bypassPermissions`, `plan`. **Never pass `bypassPermissions` or `acceptEdits`** for a
review.

### Exit codes and stop reasons

| Signal | Meaning | Action |
|---|---|---|
| `stopReason: EndTurn`, rc 0 | normal completion | parse `structuredOutput` |
| `stopReason: Cancelled`, rc 2 | plan mode blocked an attempted mutation | **report it** — the reviewer tried to write, which a review should never need |
| missing `structuredOutput` | schema not honoured or run died early | do not guess; re-run or fall back to `text` and say you did |

---

## Cost — state it before you spawn a fleet

Measured 2026-07-31, `grok-4.5`:

| Run | Turns | Cost |
|---|---:|---:|
| Trivial one-liner ("say PONG") | 1 | **$0.023** |
| Schema-constrained toy answer | 1 | $0.023 |
| Real review of one 49-line shell script | 5 | **$0.10** |

There is a **~11k input-token floor per invocation** — the external host loads its own
system prompt, its skill list, and its MCP tool schemas before it sees your prompt. So
**one prompt covering five files beats five prompts covering one file each.** Batch by
subject, not by file. Tell the human the expected cost before launching more than a
handful of runs.

---

## Nothing reaches the external model except your prompt

Not your context. Not your findings. Not the repository's agent rules — and this bites in
a way that looks like success.

⚠ **Verify the external host actually loads the repo's instruction file.** On the host
measured, `grok inspect` reported **`Project Instructions (0) └ (none)`** in a repository
that *had* an `AGENTS.md` sitting in its root. An instruction pointer installed months
earlier was inert, and nobody knew, because a review still came back and still looked
plausible. **Run `grok inspect` in the target repo and read the `Project Instructions`
count before you trust any run.** If it is 0, every rule the reviewer needs must be pasted
into the prompt.

So the prompt must carry, inline and verbatim:

1. **The environment hazards.** If the project keeps a canonical agent rules file, paste
   the hazards section **verbatim** — do not summarise it. A search tool that silently
   returns zero hits inside ignored directories will produce a confident, wrong "no
   consumers found", and the reviewer has no way to know.
2. **Read-only scope.** State it in the prompt as well as enforcing it by flag. Belt and
   braces, since one of the two belts turned out to be decorative.
3. **The artifact under review** — or an exact path to it. Prefer a path plus "read it
   yourself" over pasting, so the reviewer works from the real file, not your excerpt.
4. **What you already believe — only when you want confirmation.** See below; usually you
   want the opposite.

---

## Blind vs. informed: pick deliberately, and say which you used

**Blind (default).** Give the reviewer the artifact and the question, and **none of your
conclusions**. Ask *"what is wrong with this?"* and *"how would you do this?"* — never
*"is my finding X correct?"*. A model shown a conclusion tends to find support for it.
Blind divergence is real signal; informed agreement is close to worthless.

**Informed (use sparingly).** Hand over your findings and ask the reviewer to **refute
them**, one at a time, with instructions to default to "refuted" when uncertain. Use this
only for a small number of high-stakes claims already destined for a human, and grade the
result as "survived an attempt at refutation", never as "confirmed".

Run blind first. Only go informed for the findings that survive.

---

## Reconciliation — the step that is the actual deliverable

Never paste the external model's output at the human. Produce a reconciliation:

| Class | Meaning | What you do |
|---|---|---|
| **CONFIRMED** | It found something you also found, independently | Raises confidence. Say "independently corroborated", and say it was blind. |
| **NEW** | It found something you missed | **Re-derive from source before recording.** This is the payload. |
| **REFUTED** | It asserts something you can disprove at source | Say so explicitly, with the evidence. Do not quietly drop it. |
| **UNRESOLVED** | Neither of you can settle it from the tree | Becomes a question for a human or a one-command experiment. |

**Every CONFIRMED and NEW item gets hand-verified before it goes anywhere.** Open the file,
read the lines, check the symbol exists. An external finding that names a function, a flag,
or a line number is a claim about the tree, and claims about the tree are cheap to check
and expensive to get wrong.

Report the counts. "4 confirmed, 2 new, 1 refuted" tells the human how much the run bought;
"Grok agreed" tells them nothing.

---

## The harness

```sh
# run_external_review <repo-root> <prompt-file> <schema-file> [out-file]
# Returns 0 with structuredOutput on stdout. Provider seam: swap this body per provider.
run_external_review() {
    local root=$1 prompt=$2 schema=$3 out=${4:-/dev/stdout}
    command -v grok >/dev/null || { echo "no external reviewer installed" >&2; return 127; }

    local mode=plan
    # Prefer the fail-closed sandbox when bubblewrap can enforce it.
    if command -v bwrap >/dev/null && [ -f "$root/.grok/sandbox.toml" ]; then
        grok --cwd "$root" --prompt-file "$prompt" --sandbox review \
             --output-format json --json-schema "$(cat "$schema")" > "$out"
        return
    fi

    grok --cwd "$root" --prompt-file "$prompt" --permission-mode "$mode" \
         --output-format json --json-schema "$(cat "$schema")" > "$out"
}
```

Parse with `structuredOutput`, and **check `stopReason` first**:

```sh
python3 -c '
import json,sys
d=json.load(sys.stdin)
if d.get("stopReason")=="Cancelled":
    sys.exit("reviewer attempted a mutation and was blocked — investigate the prompt")
out=d.get("structuredOutput")
if out is None:
    sys.exit("no structuredOutput — schema not honoured; do not guess")
print(json.dumps(out,indent=2))
' < response.json
```

A findings schema that has produced clean output:

```json
{"type":"object","required":["findings"],
 "properties":{"findings":{"type":"array","items":{
   "type":"object","required":["severity","file","line","claim"],
   "properties":{
     "severity":{"type":"string","enum":["critical","high","medium","low"]},
     "file":{"type":"string"},
     "line":{"type":"integer"},
     "claim":{"type":"string"},
     "evidence":{"type":"string"}}}}}}
```

Require `file` and `line`. A finding that cannot name a location is a finding you cannot
check, and one you cannot check is one you must not record.

---

## Verify the tree afterwards

Plan mode is verified to block writes, but verification is cheap and the failure is silent:

```sh
git -C <repo-root> status --porcelain     # expect empty
```

If the project has a stronger contamination gate of its own, run that instead. Report the
result — "reviewer ran read-only, tree clean" is part of the deliverable.

---

## Failure modes seen in practice

1. **A flag that reads like enforcement and isn't.** `--disallowed-tools` above. Test any
   containment flag by asking the reviewer to violate it and checking the filesystem.
2. **Silent loss of the rules.** `Project Instructions (0)` while the file exists. The
   review still returns, still reads well, and is uninformed.
3. **Confident non-existent symbols.** External findings citing functions that are not in
   the tree. Hand-verification catches these; nothing else does.
4. **Asking a leading question and banking the agreement.** If the prompt names your
   conclusion, the answer is not independent evidence.
5. **Per-invocation token floor eaten by fan-out.** Many small runs cost far more than one
   batched run for the same coverage.
6. **Treating a second opinion as a tiebreak.** Two models agreeing on an unverified claim
   is two guesses, not a measurement. Source is the tiebreak.
