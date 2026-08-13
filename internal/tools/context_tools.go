// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/resumezone"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// BootstrapResult is the response from vp_bootstrap_context.
//
// 🔴 DECLARATION ORDER IS WIRE ORDER IS CUT ORDER, AND THAT MAKES THIS FIELD
// LIST A TRANSPORT CONTRACT RATHER THAN A STYLE CHOICE. encoding/json emits
// struct fields in declaration order, and nothing on the response path
// re-serializes through a map (mcplib.NewToolResultJSON marshals this value
// directly; `vp inject` encodes it directly), so whatever is declared last is
// what a host with a fixed inline cap throws away first.
//
// It used to be declared bulk-first: project, workflow, resume, ... and then,
// at the very tail, the directive, the alerts and `budget`. Measured on a live
// Grok pane 2026-08-12: three payloads of 60.3 KB, 53.4 KB and 32.7 KB were
// each cut at exactly 19.5 KB — a FLAT cap, not a ratio — and the model
// received `project`, a whole `workflow`, and a `resume` that stopped
// mid-sentence. Every instrument and every recovery handle was past the cut.
//
// AN INSTRUMENT PLACED AFTER THE PAYLOAD IT MEASURES CANNOT REPORT ITS OWN
// LOSS. So the order below is: identity, then the budget report, then the
// recovery handles (URI + the digest that CAS-verifies what the URI serves),
// then the compact alerts and the directive — and only then the bulk that a cut
// is allowed to land in. A truncated payload now still carries everything the
// agent needs to go and fetch what it lost.
//
// Changing the order of this struct changes what survives truncation. See
// TestBootstrapInstrumentsPrecedeBulk and TestBootstrapTruncatedPrefixIsDetectable.
type BootstrapResult struct {
	Project string `json:"project"`

	// Budget reports what the token shed ladder did. NIL when nothing was shed
	// and the payload fit — the healthy case says nothing, exactly like Health.
	//
	// It exists because the shed loop was a silent instrument. It would set
	// recent_sessions to null, drop the memory index and the command list, run
	// out of things to shed, RETURN OVER BUDGET ANYWAY, and report none of it:
	// no error, no field, no log line. The 204 review had to INFER that a shed
	// had happened from the wording of the directive it got back. A tool that
	// quietly returns less than it was asked for, while reporting success, is
	// the class this epic exists to delete — and it was living inside the shed
	// loop the whole time.
	//
	// 🔴 IT LEADS THE PAYLOAD because it is the report ABOUT the payload. Last,
	// it was the first casualty of a host cut, and its absence then meant two
	// incompatible things at once — "vp shed nothing" and "the report was cut
	// off" — which no agent inside the truncated channel could tell apart. First,
	// absence means exactly one thing again.
	Budget *BootstrapBudget `json:"budget,omitempty"`

	// The recovery handles, ahead of the bulk they point at — a handle that
	// arrives only when the body already fit is not a recovery handle.
	//
	// ResumeSha256 sits WITH them rather than beside Resume: it covers the FULL
	// RAW file, so an agent that pages the body back through ResumeURI needs it
	// to compare-and-set against disk. Stranded on the far side of the bulk it
	// describes, it was reachable only by the sessions that never needed it.
	ResumeURI    string `json:"resume_uri"`
	WorkflowURI  string `json:"workflow_uri"`
	ResumeSha256 string `json:"resume_sha256"`

	// ActiveTaskCount survives the ladder even when ActiveTasks is shed, and now
	// survives a host cut for the same reason: a backlog that leaves no trace
	// reads as "no open tasks".
	ActiveTaskCount int `json:"active_task_count"`

	// VaultStaleness reports the network-free fetch age of the vault view.
	VaultStaleness *VaultStaleness `json:"vault_staleness,omitempty"`

	// Health rides in the payload every session already loads, so a degraded vp
	// reaches every agent on every host WITHOUT the agent having to think to ask.
	// "Who calls vp_health?" was the wrong question: every pull-based answer is a
	// rule in prose, and `vp check` is this project's standing proof that prose
	// reaches nobody — it has an entire check suite that no template invokes.
	//
	// NIL WHEN HEALTHY, and that is deliberate. An always-on "healthy ✅" is the
	// soft signal agents learn to skim past, which is the same reasoning that
	// killed the `partial` capture status. This field appearing AT ALL means
	// something needs looking at.
	Health *vplog.Summary `json:"health,omitempty"`

	// AuditStaleness nags when the vault audit has gone stale — NIL WHEN FRESH, for
	// the same reason Health is. This payload now carries four possible alerts, and
	// four alerts that fire on a healthy vault is how you train a reader to skim all
	// four. Any new bootstrap alert MUST be silent in the healthy case; if a fifth is
	// ever proposed, they need a priority or a cap first.
	AuditStaleness *vaultaudit.Staleness `json:"audit_staleness,omitempty"`

	FrictionTrend *capture.FrictionTrend `json:"friction_trend,omitempty"`

	// The directive carries the alerts in prose for a reader that skims the
	// structured fields, so it rides with them, ahead of the bulk.
	PostBootstrapInstructions string `json:"post_bootstrap_instructions,omitempty"`
	CommandInvocation         string `json:"command_invocation,omitempty"`

	// ── THE BULK. Everything below is large, and everything below is where a
	// host cut is allowed to land: each of these is re-fetchable through a handle
	// declared above (resume_uri, workflow_uri, vp_list_tasks, vp_cmd/vp_skill).
	Workflow          string             `json:"workflow"`
	Resume            string             `json:"resume"`
	ActiveTasks       []storage.TaskMeta `json:"active_tasks"`
	RecentSessions    []sessionSummary   `json:"recent_sessions,omitempty"`
	Memory            []memorySnapshot   `json:"memory,omitempty"`
	KGSnapshot        *storage.KGStats   `json:"kg_snapshot,omitempty"`
	AvailableCommands []commandSummary   `json:"available_commands,omitempty"`
	AvailableSkills   []skillSummary     `json:"available_skills,omitempty"`

	// 🔴 THE TERMINAL SENTINEL. LAST FIELD, NO omitempty, ALWAYS true — all three
	// properties are the mechanism, and each one is load-bearing.
	//
	// LAST, because its job is to be the thing a truncation removes. Reordering
	// the instruments above makes a cut payload RECOVERABLE on a host that
	// announces the cut; it does nothing on a host that truncates SILENTLY, and
	// Claude Code truncates silently. `complete` present ⇒ every byte arrived.
	// `complete` absent ⇒ the transport cut the payload, whatever the host said.
	// Any field moved below this one re-opens the hole.
	//
	// NO omitempty, because ABSENCE IS THE SIGNAL. `bool` with omitempty vanishes
	// when false, which would make "not delivered whole" and "delivered whole"
	// produce the same bytes — the exact absent-vs-cut ambiguity that made a
	// missing `budget` uninterpretable from inside the truncated channel.
	//
	// ALWAYS true, because it asserts nothing about the CONTENT. It is not "the
	// payload is complete" in the sense of un-shed — the shed ladder reports
	// that, honestly, in Budget. It is "this JSON document is the whole document
	// vp emitted". A false value would be unreachable anyway: the only writer is
	// the successful return path, and every failure path returns an error, not a
	// half-populated result.
	//
	// It costs ~18 bytes and is host-independent, which is why it beats asking an
	// agent in prose to remember that it might have been truncated (ADR-006: the
	// agent DERIVES its delivery state instead of being told to recall a rule).
	Complete bool `json:"complete"`
}

// BootstrapBudget is the shed ladder's own account of itself: what it dropped,
// what the payload cost in the end, and whether it met the budget it was given.
type BootstrapBudget struct {
	MaxTokens int `json:"max_tokens"`
	// EstimatedTokens is measured on the payload AS RETURNED, with this budget
	// field already in place — exact to within the digits of its own value, and
	// on the same rough 4-chars-per-token basis the ladder sheds against. An
	// instrument that reported a number for some other payload than the one it
	// sent would be the very defect being fixed here.
	EstimatedTokens int `json:"estimated_tokens"`
	// OverBudget is true when the payload AS RETURNED exceeds max_tokens after
	// the ladder shed everything it could. The verdict is taken on the FINAL
	// measurement — budget field and post-ladder alerts included — never frozen
	// at whatever the ladder last saw: the frozen verdict was the stale-report
	// defect that let the live vault ship 8060 tokens against a budget of 8000
	// with over=false. It is NEVER silent: it also raises an alert in
	// post_bootstrap_instructions and a WARN in vp.log.
	OverBudget bool `json:"over_budget"`
	// Shed names each rung the ladder had to use, in order, so the caller knows
	// precisely what is missing and can fetch it: "recent_sessions", "memory",
	// "kg_snapshot", "commands+skills", "resume->pinned", "active_tasks",
	// "workflow->excerpt".
	Shed []string `json:"shed,omitempty"`
	// ShedCore names, in shed order, the subset of Shed that ADR-009 classifies
	// as inviolable core rather than re-fetchable context. Most rungs carry a
	// project-agnostic tier (see shedRungTier); the resume rung's tier is derived
	// per bootstrap from that project's own resume (see resumeRungTier).
	// MECHANISM ONLY today: the tier is reported so a caller can see that safety
	// surface — not just context — was dropped, but core rungs still shed
	// exactly like context rungs. Refusing to shed core (fail-loud) is the gated
	// sibling task adr-009-arm-fail-loud-bootstrap, sequenced behind the ADR-008
	// core shrink. Absent when no core rung was shed.
	ShedCore []string `json:"shed_core,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// VaultStaleness reports the network-free fetch AGE of the vault view at
// bootstrap — how long since the last `git fetch` on this host. It never carries
// a "commits behind" count: a behind-count requires a fetch and is deliberately
// out of scope. LastFetched is a pointer (nil + omitempty) so an unknown fetch
// time is omitted rather than serialized as a zero timestamp.
type VaultStaleness struct {
	LastFetched *time.Time `json:"last_fetched,omitempty"`
	AgeHours    float64    `json:"age_hours"`
	Warn        bool       `json:"warn"`
	Message     string     `json:"message,omitempty"`
}

// vaultStaleThreshold is how old the last fetch may be before bootstrap warns.
const vaultStaleThreshold = 24 * time.Hour

// computeVaultStaleness derives the staleness verdict from a network-free fetch
// age. It warns when the vault was last fetched more than vaultStaleThreshold
// ago, or when the fetch age is unknown (never fetched / no tracking ref on this
// host). Kept separate from AssembleBootstrap so the threshold logic is unit
// testable without a git repo.
func computeVaultStaleness(age time.Duration, fetchedAt time.Time, known bool) VaultStaleness {
	if !known {
		return VaultStaleness{
			Warn:    true,
			Message: "vault fetch age unknown — never fetched on this host?",
		}
	}
	vs := VaultStaleness{
		AgeHours: age.Hours(),
	}
	lf := fetchedAt
	vs.LastFetched = &lf
	if age > vaultStaleThreshold {
		vs.Warn = true
		vs.Message = fmt.Sprintf("vault last fetched %.1f days ago — run a pull to refresh context", age.Hours()/24)
	}
	return vs
}

// skillSummary reuses the commandSummary shape; alias semantics differ
// (vps-<name> vs vpc-<name>) but the fields are identical.
type skillSummary = commandSummary

// commandInvocationDirective tells the AI how to interpret a "vpc-<name>" or
// "vps-<name>" alias typed by the user. References agentfile.CommandToolName
// so the block copy and this directive cannot drift.
var commandInvocationDirective = fmt.Sprintf(
	"When the user types `vpc-<name>`, call `%s` with `name=<name>` and follow the returned instructions. `vps-<name>` works the same way via `%s`.",
	agentfile.CommandToolName, agentfile.SkillToolName,
)

// bootstrapExcerptCap bounds the rune-safe excerpt the TOKEN ladder substitutes
// for an oversized workflow (the workflow->excerpt rung). The full body remains
// reachable via the workflow_uri the result always carries.
//
// 🔴 IT IS NO LONGER A BYTE-AXIS CONSTANT. The byte axis — an unconditional
// prefix cut of the resume, applied before the ladder and recorded nowhere — was
// DELETED (see the axis note on AssembleBootstrap). This constant now has ONE
// reader. Do not grow it a second one: the deleted design's core defect was that
// a single number silently governed two mechanisms with different rules.
const bootstrapExcerptCap = 4000

// bootstrapExcerptBanner prefixes an excerpt with a loud, unmissable
// pointer to the full body. The caller MUST read the URI before acting.
func bootstrapExcerptBanner(body, uri string) string {
	return "⚠ excerpt — full content at " + uri + ", read before acting\n\n" +
		runeSafeExcerpt(body, bootstrapExcerptCap)
}

// bootstrapZoneBannerLead is the opening of every pinned-zone banner, and it is
// a CONSTANT because two things read it: the banner builder and the idempotence
// guard on the workflow digest. A reduction that cannot recognize its own output
// can be applied twice, and the second application would band a banner over a
// banner.
const bootstrapZoneBannerLead = "⚠ pinned sections only — the full "

// bootstrapZoneBanner prefixes a PINNED ZONE with the same loud pointer as an
// excerpt — but does NOT truncate. The zone is already a deliberate selection
// (every section its author marked as always-inline); cutting it at
// bootstrapExcerptCap would silently amputate the very sections the marker
// exists to protect, which is the failure the marker was introduced to prevent.
//
// ONE FUNCTION SERVES BOTH REDUCTIONS — the resume's pinned zone and the
// workflow's digest — on purpose. An agent that has learned to read this banner
// on a shed resume must recognize the identical shape on a digested workflow;
// two hand-written strings would have drifted the first time either was edited.
// doc names the document ("resume", "workflow"); rely completes "read it before
// relying on …".
func bootstrapZoneBanner(zone, uri, doc, rely string) string {
	return bootstrapZoneBannerLead + doc + " is at " + uri + ", read it before relying on " + rely + "\n\n" + zone
}

// memoryRecallCap bounds how many memory index entries the bootstrap surfaces.
// Recall is "index now, body on demand via vp_memory_read" — the cap keeps the
// curated index small so it sheds cheaply under a tight token budget.
const memoryRecallCap = 50

// memorySnapshot is a lightweight view of storage.MemoryMeta for the bootstrap
// response. It carries the index metadata only — never the body. Rel is
// included because vp_memory_read is keyed by rel, so the agent needs it to
// fetch a body on demand.
type memorySnapshot struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Rel         string `json:"rel"`
}

// sessionSummary is a lightweight view of SessionMeta for the bootstrap response.
type sessionSummary struct {
	Date      string `json:"date"`
	Iteration int    `json:"iteration"`
	Title     string `json:"title,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Tag       string `json:"tag,omitempty"`
}

type bootstrapParams struct {
	Project   string `json:"project"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Wing      string `json:"wing,omitempty"`
	Room      string `json:"room,omitempty"`
}

// bootstrapSchemaStdio is the stdio MCP schema: project is optional because the
// handler may high-confidence-default from cwd. MUST stay separate from the
// HTTP/explicit schema — sharing one literal made the machine-readable contract
// say optional while HTTP runtime required it (honest-instruments defect).
var bootstrapSchemaStdio = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug. Prefer always passing it. Optional on stdio MCP when the process cwd has a high-confidence signal (.vibe-palace.toml [project].name or git origin remote) AND Projects/<slug>/ exists in the bound vault; never derived from directory basename. Whenever detection is ambiguous — pass explicitly or call vp_list_projects."
		},
		"max_tokens": {
			"type": "integer",
			"description": "Token budget for response. Default: 16000."
		},
		"wing": {
			"type": "string",
			"description": "Wing slug for palace-scoped command discovery."
		},
		"room": {
			"type": "string",
			"description": "Room slug for palace-scoped command discovery (requires wing)."
		}
	}
}`)

// bootstrapSchemaExplicit is the HTTP serve schema: project is required in the
// machine-readable contract, matching the handler. Used only by
// BootstrapContextToolExplicit / WithRequireExplicitProject.
var bootstrapSchemaExplicit = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug. Required on this transport (multiplexed HTTP serve does not default project from the server process cwd). Call vp_list_projects if unknown."
		},
		"max_tokens": {
			"type": "integer",
			"description": "Token budget for response. Default: 16000."
		},
		"wing": {
			"type": "string",
			"description": "Wing slug for palace-scoped command discovery."
		},
		"room": {
			"type": "string",
			"description": "Room slug for palace-scoped command discovery (requires wing)."
		}
	},
	"required": ["project"]
}`)

// BootstrapContextTool returns the MCP tool definition for vp_bootstrap_context.
//
// There is ONE reduction path and this description states it exactly. The old
// `slim` param advertised a second one; it is gone (see AssembleBootstrap).
//
// Cwd-based project defaulting is enabled (stdio MCP). Use
// BootstrapContextToolExplicit when the transport multiplexes clients over one
// process cwd (HTTP serve) and must keep project required.
func BootstrapContextTool(resolver *vpctx.Resolver, vault *storage.Vault) mcp.Tool {
	return bootstrapContextTool(resolver, vault, true)
}

// BootstrapContextToolExplicit is vp_bootstrap_context with no cwd defaulting —
// project must be supplied on every call. Used by `vp mcp serve` (HTTP).
// Its schema retains required:["project"] so schema-driven clients cannot omit
// it and only learn the requirement from a runtime error string.
func BootstrapContextToolExplicit(resolver *vpctx.Resolver, vault *storage.Vault) mcp.Tool {
	return bootstrapContextTool(resolver, vault, false)
}

func bootstrapContextTool(resolver *vpctx.Resolver, vault *storage.Vault, allowCwdDefault bool) mcp.Tool {
	schema := bootstrapSchemaStdio
	if !allowCwdDefault {
		schema = bootstrapSchemaExplicit
	}
	return mcp.Tool{
		Name:        "vp_bootstrap_context",
		Description: "Single-call context restoration: workflow + resume + tasks + recent sessions + KG snapshot + available commands + available skills + post-bootstrap capability-announcement directive. Sheds context to fit max_tokens and REPORTS WHAT IT SHED in `budget.shed` — `shed_core` (or a `⚠ pinned sections only` banner) means the resume itself was reduced; `budget.shed` naming only optional rungs (recent_sessions, memory, kg_snapshot) is benign, so do NOT re-fetch and do NOT report it as truncation. A shed resume arrives as its `<!-- vp:pin -->` sections only, behind a banner — read `resume_uri` for the full body. `workflow->digest` means the same thing for the workflow: the project marked its always-inline rules, the rest is at `workflow_uri`, and that reduction is UNCONDITIONAL (it answers a host inline cap, not max_tokens), so it is not a sign the budget was tight. A workflow that declares no pin zone arrives WHOLE. A shed task list leaves `active_task_count` — call vp_list_tasks for it. `budget` reports only vp's own shedding; whether every byte ARRIVED is a separate question that only `complete` answers. The payload ends with `complete: true`; if you do not see it, your HOST truncated the result and the inline body is untrustworthy whatever `budget` says — rehydrate via `resume_uri` / `workflow_uri` before acting on it. An absent `budget` means nothing was reduced ONLY when `complete` is present; both absent is a cut, not a clean run.",
		Schema:      schema,
		Handler:     bootstrapHandler(resolver, vault, allowCwdDefault),
	}
}

// AssembleBootstrap builds context restoration payload.
// Used by both the MCP tool handler and the CLI inject command.
// When wing/room are provided, palace-scoped commands are included in discovery.
//
// 🔴 THERE IS ONE REDUCTION AXIS. The byte axis — `slim` — was DELETED, and the
// deletion is the point: a second, differently-ruled path is what made this
// tool report success for work it did not do.
//
// What it was: an unconditional 4,000-byte prefix cut of the resume, applied
// BEFORE the ladder, ignoring `<!-- vp:pin -->` entirely, defaulting ON for the
// HTTP transport, and recorded in NO field — so a payload that had dropped most
// of a resume could return `budget: null`. Worse than unrecorded: because the
// cut made the payload fit, the ladder then shed nothing, so the byte axis
// ERASED the honest `shed_core` report the token axis produces.
//
// Why it went rather than being fixed: it was built for "a host that truncates
// large inline results", and that bound was never measured. The only refusal
// ever observed was ~62,463 characters (iteration 257) — 15x the cap that cited
// it — and the payload was under that bound both with the cut and without it.
// The cut bought nothing and cost the pin markers their meaning on the one
// transport a non-Claude host is likeliest to use. The remedy for the project
// that actually failed was editing its resume, which is what fixed it in 257.
//
// The ladder below remains: graduated, last-resort, pin-aware, and reported.
//
// 🔴 CORRECTED at 209: this comment used to promise that the shed loop "NEVER
// excerpts" resume/workflow. That was true, and it was the bug — with 96% of a
// real payload sitting in resume+workflow+tasks, a ladder forbidden to touch
// any of the three ran out of rungs and returned 2.4x over budget in silence.
// See shedToBudget.
// DefaultBootstrapMaxTokens is the session-start payload budget when a caller
// names none. It is ONE constant because it used to be four literals (here, the
// tool schema, `vp inject`, and the live canary), which is how a budget drifts.
//
// 🔴 RAISED 8000 -> 12000 at iteration 260, from measurement, not taste. The old
// value was chosen by guess and was BELOW THE FLOOR: the inviolable core alone —
// resume.md 18,577 B + workflow.md 13,482 B = 32,059 B ≈ 8,015 tokens — exceeded
// it before a single task, the directive, or any JSON overhead. That made ADR-009
// ("the core is delivered whole or fail loud") and the budget MUTUALLY
// UNSATISFIABLE, and every session resolved the contradiction by amputating
// something: first resume's un-pinned zone (the 2026-07-21 incident where a Grok
// session repeated a disproven diagnosis it could not see corrected), then, once
// the resume was pinned correctly, the operating contract itself.
//
// The budget must therefore exceed the core, by definition.
//
// SIZED FROM MEASUREMENT, and the first attempt was wrong: 12,000 was chosen
// against an estimate of ~9,950 tokens everything-inline. The real figure is
// 11,721 (live canary), which left 279 tokens of headroom — not headroom, a
// cliff. 16,000 is 11,721 × ~1.35, and that 35% is itself derived: workflow.md
// grew 9,427 → 13,486 B (43%) across roughly fifty iterations while nobody
// measured it, so a budget with less slack than one growth cycle just schedules
// the next crisis.
//
// A CEILING COSTS NOTHING UNTIL CONTENT GROWS INTO IT. Raising this does not add
// a token to today's payload — the payload is what it is; the budget only decides
// whether the ladder starts amputating. That asymmetry is why a generous ceiling
// is the right default and a tight one is a false economy: the downside of too
// high is a few tokens per session IF content grows, and the downside of too low
// is a silently degraded agent. At 16,000 the payload is ~1.6% of a 1M context
// window and ~8% of a 200K one.
//
// The levers that keep this honest are the core-floor check
// (internal/check/core_floor.go) and the LiveVault canary — NOT shrinking the
// charter to fit a number, which inverts the dependency: the contract sets the
// budget, not the reverse.
const DefaultBootstrapMaxTokens = 16000

func AssembleBootstrap(resolver *vpctx.Resolver, vault *storage.Vault, project string, maxTokens int, wing, room string) BootstrapResult {
	if maxTokens == 0 {
		maxTokens = DefaultBootstrapMaxTokens
	}

	// Complete is set HERE, at assembly, not just before the return: the shed
	// ladder measures the payload it is about to send (est() marshals `result`),
	// and a sentinel attached after the last measurement would be bytes the
	// budget never counted — the same under-measurement that let the live vault
	// ship 8060 tokens against a budget of 8000 with over=false.
	result := BootstrapResult{
		Project:     project,
		ResumeURI:   mcp.ResumeURI(project),
		WorkflowURI: mcp.WorkflowURI(project),
		Complete:    true,
	}

	// Workflow — graceful on error.
	if wf, _, err := resolver.Resolve("workflow", project); err == nil {
		result.Workflow = wf
	}

	// The full body is held aside for the same reason fullResume is: the tier
	// derivation below must read the WHOLE document to find its markers, and the
	// digest just below may replace the inline copy with a subset of it.
	fullWorkflow := result.Workflow

	// 🔴 THE DIGEST RUNS HERE, ABOVE THE LADDER, NOT INSIDE IT. See
	// digestWorkflowToPinnedZone: the constraint it answers is a host inline cap,
	// which no token budget can see. A workflow declaring no pin zone is left
	// WHOLE and this is a no-op.
	workflowDigested := digestWorkflowToPinnedZone(&result)

	// Resume — graceful on error. The digest comes from the same read as the body
	// and covers the FULL resume, so a caller that pages the body back through
	// resume_uri can still compare-and-set against disk. It is empty when no
	// project-tier resume.md exists (vault/embedded fallback) — the writer reads
	// that as "assert absent".
	if resume, _, sha, err := resolver.ResolveDigest("resume", project); err == nil {
		result.Resume = resume
		result.ResumeSha256 = sha
	}

	// The full body is held aside for the token ladder's resume rung, which needs
	// the WHOLE document to find its pin markers.
	fullResume := result.Resume

	// Active tasks — graceful on error.
	//
	// Iceboxed tasks are dropped: bootstrap carries what the project INTENDS to
	// do, not everything it KNOWS. An agent that opens a session to a list where
	// the critical work sits beside a dozen deliberately-unscheduled
	// found-in-passing items has to re-derive the difference every time, and this
	// payload is already over its own token budget besides.
	if tasks, err := vault.ListTasks(project, false); err == nil {
		result.ActiveTasks = storage.DropIcebox(tasks)
		// The COUNT survives the ladder even when the list itself is shed, so a
		// caller that gets a shed payload still knows the backlog exists and how
		// big it is. A shed list that leaves no trace reads as "no open tasks".
		result.ActiveTaskCount = len(result.ActiveTasks)
	}

	// Recent sessions (last 5, most-recent-first) — graceful on error.
	if sessions, err := vault.ListSessions(project, "", "", 0); err == nil {
		// Compute the friction trend from the FULL history BEFORE the 5-session
		// trim — computing after the trim would leave the 30d/90d windows
		// meaningless. GetFrictionWindows always returns one window per request,
		// so guard on real data (any window covering at least one session) rather
		// than len(Windows): a zero-session project attaches nothing, while a
		// frictionless-but-active project (RecentAvg 0, direction "stable")
		// legitimately attaches. The omitempty pointer means nil = not attached.
		if trend := capture.ComputeFrictionTrend(sessions, time.Now()); frictionTrendHasData(trend) {
			result.FrictionTrend = &trend
		}
		if len(sessions) > 5 {
			sessions = sessions[len(sessions)-5:]
		}
		// Reverse for most-recent-first.
		for i, j := 0, len(sessions)-1; i < j; i, j = i+1, j-1 {
			sessions[i], sessions[j] = sessions[j], sessions[i]
		}
		for _, s := range sessions {
			result.RecentSessions = append(result.RecentSessions, sessionSummary{
				Date:      s.Date,
				Iteration: s.Iteration,
				Title:     s.Title,
				Summary:   s.Summary,
				Tag:       s.Tag,
			})
		}
	}

	// KG snapshot — Phase 7 may not exist yet, graceful.
	if stats, err := vault.KGStats(project); err == nil {
		result.KGSnapshot = &stats
	}

	// Memory index (capped) — bodies fetched on demand via vp_memory_read.
	// Graceful: a missing dir or read error must never hard-fail bootstrap.
	if mems, err := vault.ListMemories(project, memoryRecallCap); err == nil {
		for _, m := range mems {
			result.Memory = append(result.Memory, memorySnapshot{
				Name:        m.Name,
				Description: m.Description,
				Type:        m.Type,
				Rel:         m.Rel,
			})
		}
	}

	// Available commands for discovery (palace-scoped when wing/room provided).
	if cmds, err := resolver.ListResourcesScoped("command", project, wing, room); err == nil {
		for _, cmd := range cmds {
			cs := commandSummary{Name: cmd.Name, Alias: commandAlias(cmd.Name), Source: cmd.Source}
			if content, _, err := resolver.ResolveScoped(fmt.Sprintf("command:%s", cmd.Name), project, wing, room); err == nil {
				cs.Brief = extractBrief(content, 60)
			}
			result.AvailableCommands = append(result.AvailableCommands, cs)
		}
		if len(result.AvailableCommands) > 0 {
			result.CommandInvocation = commandInvocationDirective
		}
	}

	// Available skills for discovery (palace-scoped when wing/room provided).
	if skills, err := resolver.ListResourcesScoped("skill", project, wing, room); err == nil {
		for _, sk := range skills {
			ss := skillSummary{Name: sk.Name, Alias: commands.SkillAlias(sk.Name), Source: sk.Source}
			if content, _, err := resolver.ResolveScoped(fmt.Sprintf("skill:%s", sk.Name), project, wing, room); err == nil {
				ss.Brief = extractBrief(content, 60)
			}
			result.AvailableSkills = append(result.AvailableSkills, ss)
		}
	}

	// PostBootstrapInstructions tells the model to announce capabilities after
	// the bootstrap summary. Populated server-side and excluded from truncation
	// so the directive fires even when the command list is shed — a degraded
	// "run vp_cmd to list commands" is still better than silent capability.
	result.PostBootstrapInstructions = renderPostBootstrapInstructions(result.AvailableCommands, result.AvailableSkills)

	// ALERTS ARE COLLECTED SEPARATELY FROM THE DIRECTIVE, and that separation is a
	// bug fix, not bookkeeping.
	//
	// The friction, staleness and health warnings used to be appended straight onto
	// PostBootstrapInstructions — and the token-budget truncation below RE-RENDERS
	// that field with a blind assignment when it sheds the command list, which
	// silently DISCARDED every warning appended before it. So the payload dropped its
	// alerts exactly when it was too big, i.e. on a busy project, which is precisely
	// when they matter. The alerts are the highest-value thing in this payload and
	// they were the first thing thrown away.
	//
	// Keeping them in their own slice and composing at the end makes that
	// unrepresentable: re-rendering the directive can no longer drop them.
	var alerts []string

	// Surface the proactive friction nudge in the directive itself. The directive
	// is excluded from the token-shed truncation below, so appending the trend's
	// actionable Message here guarantees the agent sees (and acts on) it even when
	// the friction_trend field would otherwise be missed in a large payload.
	if result.FrictionTrend != nil && result.FrictionTrend.Warn && result.FrictionTrend.Message != "" {
		alerts = append(alerts, result.FrictionTrend.Message)
	}

	// Vault-staleness warning — NETWORK-FREE. VaultFetchAge reads only local git
	// plumbing + os.Stat (no fetch, no ls-remote, no GetRemoteStatus), so it is
	// safe on the session-start critical path and on offline hosts. Best-effort:
	// a stale/unknown fetch age never fails the bootstrap. When it warns, mirror
	// the friction pattern above and surface a human-visible line in the directive
	// in addition to the structured vault_staleness field.
	age, fetchedAt, known := storage.VaultFetchAge(vault.Root)
	vs := computeVaultStaleness(age, fetchedAt, known)
	result.VaultStaleness = &vs
	if vs.Warn && vs.Message != "" {
		alerts = append(alerts, vs.Message)
	}

	// Health — PUSHED, not pulled, and SILENT WHEN HEALTHY.
	//
	// vp_health existed for a long time and NOTHING EVER CALLED IT: not a template,
	// not a command, not a skill. It was itself a member of the very class it was
	// built to detect — capability built, nothing invokes it. Riding in the payload
	// every session already loads fixes that structurally, instead of asking agents
	// to remember a rule. ("Who calls vp_health?" was the wrong question: every
	// pull-based answer is a rule in prose, and `vp check` is the standing proof
	// that prose reaches nobody.)
	//
	// Summarize reads a BOUNDED TAIL (vplog.TailBytes), never the whole log: this is
	// the hottest path in the system — 190 spent a session taking the handshake from
	// ~0.4 s to 0.012 s — and the log is capped at 8 MiB. It also never errors: a
	// health probe that fails is itself a health signal, so it degrades to status
	// "unknown" rather than failing the bootstrap.
	h := vplog.Summarize(vaultLogPath(vault), healthWindowHours, healthDisplayLimit)
	if !h.Healthy() {
		result.Health = &h
		alerts = append(alerts, healthMessage(h))
	}
	// Caller friction is a SEPARATE, non-amber signal: guards that correctly
	// rejected bad input. It is surfaced distinctly from health.status and only
	// when the count is non-zero — silent in the healthy case, per the standing
	// operator rule and the payload's ~110-token headroom.
	if msg := callerFrictionMessage(h); msg != "" {
		alerts = append(alerts, msg)
	}

	// Audit staleness — PUSHED, not pulled, and SILENT WHEN FRESH.
	//
	// The operator arrived at this independently of the vp_health work ("it's easy
	// to forget to run QA-like tools such as vault-audit… perhaps on startup, if an
	// audit has not been run for 50 or so iterations, it suggests one be performed"),
	// which is the strongest evidence it is right. "Agents and humans forget to call
	// the QA tool" is EXACTLY the vp_health problem, and the answer is already proved:
	// do not write a rule in prose asking someone to remember — have the SERVER
	// notice and say so. `vp check` is this project's standing proof that prose
	// reaches nobody.
	//
	// It is essentially FREE (see vaultaudit.CheckStaleness): churn is a glob of
	// session-note filenames with zero file reads, and the only read is a bounded
	// 512-byte head of the newest report's frontmatter. The AUDIT may be slow; the
	// STALENESS CHECK may not — this is the hottest path in the system.
	//
	// 🔴 THIS IS THE FOURTH ALERT ON THIS PAYLOAD, and it could only be added once
	// the payload could actually deliver three (bootstrap-payload-exceeds-its-own-
	// token-budget, landed at 209). Adding a fourth alert to a vehicle returning
	// 2.4x over budget would not have been a feature; it would have been a fourth
	// thing nobody reads, reporting success while being silently truncated away.
	if as := vaultaudit.CheckStaleness(vault, time.Now()); as.Warn {
		result.AuditStaleness = &as
		alerts = append(alerts, as.Message)
	}

	// Compose a PROVISIONAL directive so the ladder below measures a payload the
	// same shape as the one that will be returned. The final compose happens
	// after the ladder, because the ladder can itself raise an alert.
	result.PostBootstrapInstructions = composeDirective(result.PostBootstrapInstructions, alerts)

	budget := shedToBudget(&result, maxTokens, fullResume, workflowDigested)
	// ADR-009 tier report, derived from Shed so the two can never drift: which
	// of the shed rungs were inviolable core. Reported even though the shed
	// itself still happened — see shedRungTier for why reporting is (for now)
	// the whole mechanism.
	//
	// Both per-project tiers are DERIVED HERE, from fullResume and fullWorkflow —
	// the same whole documents the reductions themselves read. They are
	// per-project verdicts, not constants: see resumeRungTier / workflowRungTier.
	budget.ShedCore = coreShed(budget.Shed, derivedTiers{
		Resume:   resumeRungTier(fullResume),
		Workflow: workflowRungTier(fullWorkflow),
	})

	if shedTasks(budget) {
		alerts = append(alerts, fmt.Sprintf("⚠ The active task list (%d open) was shed to fit the token budget — call `vp_list_tasks` for it.", result.ActiveTaskCount))
	}

	// FINAL compose. RE-COMPOSE, DO NOT RE-ASSIGN: this used to be a blind
	// assignment inside the shed loop, which threw away the friction / staleness
	// / health alerts appended above — dropping the payload's most important
	// content precisely when the payload was too big to fit, which is when a
	// project is busiest. Rendering from the POST-ladder command list also means
	// the examples can no longer point at aliases that were just shed.
	baseDirective := renderPostBootstrapInstructions(result.AvailableCommands, result.AvailableSkills)
	result.PostBootstrapInstructions = composeDirective(baseDirective, alerts)

	result.Budget = budget
	// Measure the payload AS RETURNED, budget field included. Reporting a size
	// for some other payload than the one being sent is the defect, not the fix.
	budget.EstimatedTokens = measuredTokens(&result, budget.MaxTokens)

	// 🔴 THE VERDICT IS TAKEN HERE, ON THE FINAL MEASUREMENT — never inside the
	// ladder. The ladder's last look predates this budget attach and the alert
	// composition above, and a verdict frozen there described a payload other
	// than the one being sent: the live vault shipped 8060 tokens against its
	// 8000 budget with over=false exactly that way.
	//
	// AN UNMEETABLE BUDGET IS NEVER SILENT. It says so in the payload (Budget),
	// in the directive the agent actually reads (alerts), and in vp.log — which
	// vp_health reads, so the next session opens on it too. A max_tokens the
	// tool cannot honor and does not mention is a budget that is a lie, and
	// that lie is what let this payload run 2.4x over on the transport every
	// agent uses, for as long as it has existed.
	if budget.EstimatedTokens > budget.MaxTokens {
		budget.OverBudget = true
		if budget.Reason == "" {
			budget.Reason = "⚠ bootstrap payload is over its own token budget after shedding everything sheddable — read the resume via resume_uri and treat this payload as incomplete"
		} else {
			budget.Reason = "⚠ bootstrap payload is over its own token budget: " + budget.Reason
		}
		alerts = append(alerts, budget.Reason)
		slog.Warn("bootstrap: payload exceeds max_tokens after shedding everything sheddable",
			"project", project, "max_tokens", budget.MaxTokens,
			"estimated_tokens", budget.EstimatedTokens, "shed", strings.Join(budget.Shed, ","))
		result.PostBootstrapInstructions = composeDirective(baseDirective, alerts)
		// The alert itself grew the payload; re-measure so the reported number
		// still describes the payload being sent. Growth is monotonic here, so
		// the verdict cannot flip back under.
		budget.EstimatedTokens = measuredTokens(&result, budget.MaxTokens)
	} else {
		// A reason recorded on the way down is only worth reporting if the
		// payload actually ended up over budget. Fitting anyway is not a defect
		// — and the report must describe the payload without the dropped reason.
		budget.Reason = ""
		budget.EstimatedTokens = measuredTokens(&result, budget.MaxTokens)
	}

	// Silent when healthy: nothing shed and inside budget ⇒ no field at all.
	if len(budget.Shed) == 0 && !budget.OverBudget {
		result.Budget = nil
	}

	return result
}

// dropRung removes a rung from the shed list — used by the give-back pass when
// a shed turns out to have been unnecessary. The report must name what is
// actually missing from the payload, not what the descent considered dropping.
func dropRung(xs []string, drop string) []string {
	out := xs[:0]
	for _, x := range xs {
		if x != drop {
			out = append(out, x)
		}
	}
	return out
}

// shedResumeToPinnedZone replaces the resume body with the sections its author
// marked resumezone.ResumePinMarker, behind a banner pointing at resume_uri. Idempotent:
// once the resume has been reduced it will not be reduced again.
//
// 🔴 THE BYTE-AXIS SKIP GUARD IS GONE. It read "skip when slim already excerpted
// the resume — that excerpt is SMALLER than the pinned zone", and that premise
// was FALSE for six of the eight resumes in the live vault (measured 2026-07-27;
// rezbldr's pinned zone was 3,457 bytes UNDER the 4,000-byte excerpt). It guarded
// a path that no longer exists; both went together.
//
// resume_sha256 is deliberately NOT touched: it covers the FULL RAW file, and a
// caller that pages the whole body back through resume_uri needs the digest to
// still match disk, or every compare-and-set it makes will fail.
func shedResumeToPinnedZone(result *BootstrapResult, b *BootstrapBudget, fullResume string, est func() int) int {
	if fullResume == "" || sliceHasRung(b.Shed, shedResumePinned) {
		return est()
	}
	zone, declared := resumezone.PinnedZone(fullResume)
	if !declared {
		// NO PIN MARKER ⇒ NOT SHEDDABLE. The server will not guess which half of
		// an undeclared resume was safe to drop; guessing wrong drops the
		// behavioral notes — the ones that stop an agent corrupting the vault —
		// silently. Stay inline and say so, loudly, in the budget report.
		b.Reason = "resume declares no " + resumezone.ResumePinMarker + " zone, so it cannot be shed — see the pinned-zone marker in the resume template"
		return est()
	}
	if len(zone) >= len(result.Resume) {
		return est() // a resume that pins everything sheds nothing
	}
	result.Resume = bootstrapZoneBanner(zone, result.ResumeURI, "resume", "project state")
	b.Shed = append(b.Shed, shedResumePinned)
	return est()
}

// digestWorkflowToPinnedZone replaces the inline workflow with the sections its
// author marked resumezone.ResumePinMarker, behind the same banner
// shedResumeToPinnedZone puts on a shed resume, pointing at workflow_uri.
// It reports whether the digest was applied.
//
// 🔴 IT IS NOT A LADDER RUNG, AND THAT IS THE WHOLE POINT. It runs on every
// bootstrap, at any budget, because the constraint it answers is NOT vp's token
// budget — it is a HOST'S INLINE CAP, which vp cannot measure and the ladder
// never sees. The epic measured a 61,747-byte payload on which the ladder shed
// NOTHING and reported `budget: absent` while two thirds of the bytes never
// reached the model. A reduction gated on the budget would have fired on exactly
// none of those runs.
//
// 🔴 ERR DOWNWARD — A WORKFLOW THAT DECLARES NO PIN ZONE IS DELIVERED WHOLE,
// exactly as before this function existed: no digest, no banner, no rung. Nine
// of the ten projects in the live vault carry no markers, and a default that
// guessed which half of an unruled contract was safe to drop would silently
// degrade nine projects to improve one. Same rule as PinnedZone's `declared`
// half, for the same reason: degrading to "too big, and here is why" is safe;
// degrading to "quietly smaller" is not.
//
// 🔴 THE SIZE GUARD IS ALSO THE IDEMPOTENCE GUARD, and that is worth saying out
// loud because a reader will otherwise add a second one. A workflow that pins
// everything digests nothing — the zone is not smaller than the body. Re-running
// this on its own OUTPUT is the same case: the zone of a zone is that zone (the
// banner becomes preamble, which is unconditionally kept, and every surviving
// section is pinned by construction), so the second pass measures equal and
// declines. A banner-prefix check on top of that would be unreachable code
// asserting a property the guard above already has.
func digestWorkflowToPinnedZone(result *BootstrapResult) bool {
	if result.Workflow == "" {
		return false
	}
	zone, declared := resumezone.PinnedZone(result.Workflow)
	if !declared {
		return false
	}
	if len(zone) >= len(result.Workflow) {
		return false
	}
	result.Workflow = bootstrapZoneBanner(zone, result.WorkflowURI, "workflow", "the project's rules")
	return true
}

func sliceHasRung(xs []string, want string) bool {
	return slices.Contains(xs, want)
}

// shedTasks reports whether the ladder had to drop the active task list.
func shedTasks(b *BootstrapBudget) bool {
	return slices.Contains(b.Shed, shedActiveTasks)
}

// The rungs of the ladder, named so the caller knows exactly what is missing
// from the payload it received and can go and fetch it.
const (
	shedRecentSessions = "recent_sessions"
	shedMemory         = "memory"
	shedKGSnapshot     = "kg_snapshot"
	shedCommands       = "commands+skills"
	shedResumePinned   = "resume->pinned"
	shedActiveTasks    = "active_tasks"
	shedWorkflowDigest = "workflow->digest"
	shedWorkflow       = "workflow->excerpt"
)

// 🔴 THE TWO WORKFLOW REDUCTIONS, RECONCILED — they are MUTUALLY EXCLUSIVE by
// construction, not two competing answers to one question.
//
// workflow->digest is a DELIBERATE SELECTION: the sections the author declared
// always-inline, whole. It applies to a workflow that DECLARES a pin zone, it
// applies unconditionally (see digestWorkflowToPinnedZone), and it is never
// excerpted afterwards — cutting a pinned zone at bootstrapExcerptCap would
// amputate the very sections the marker exists to protect, which is precisely
// why bootstrapZoneBanner does not truncate.
//
// workflow->excerpt is a BLIND PREFIX CUT, and it survives for exactly one case:
// a workflow that declares NOTHING. There is no selection to honour there, so
// under real budget pressure the ladder's last resort is still the first
// bootstrapExcerptCap bytes behind a URI — which is what nine of the ten live
// projects get today, unchanged. Deleting the rung would have taken that last
// resort away from every unmarked project to tidy up a name.
//
// So the digest sits ABOVE the excerpt in the ladder rather than replacing it:
// strictly better where it applies, and where it does not apply nothing moved.
// shedToBudget skips the excerpt rung whenever the digest fired.

// shedTier is a rung's ADR-009 classification
// (doc/adr/009-inviolable-core-delivered-whole-or-fail-loud.md): core rungs
// carry the operating contract or active project state; context rungs carry
// re-fetchable context.
type shedTier string

const (
	shedTierCore    shedTier = "core"
	shedTierContext shedTier = "context"
)

// shedRungTier classifies, per ADR-009, every ladder rung whose tier is a
// genuine CONSTANT — true of every project, in every vault, on every run.
//
// MECHANISM ONLY (task enforce-adr-009-inviolable-bootstrap-core): the
// classification exists and is REPORTED (BootstrapBudget.ShedCore), but it does
// not change shed behavior — the ladder still sheds core rungs exactly as
// before when it reaches them. Refusing to shed core (the fail-loud arm) is the
// gated sibling task adr-009-arm-fail-loud-bootstrap, sequenced behind the
// ADR-008 core shrink: arming today would halt every bootstrap, because the
// live core alone exceeds the default budget.
//
// active_tasks is deliberately CONTEXT even though ADR-009 puts "the current
// task" in the core: the list sheds to a surviving ActiveTaskCount plus an
// explicit vp_list_tasks alert, so nothing is silently lost and one cheap call
// recovers it — and the CURRENT task belongs in resume's pinned active-state,
// not in a whole-backlog dump whose promotion would grow a core floor that must
// shrink.
//
// shedWorkflow (the blind excerpt) IS static core, and that is not an oversight
// beside its derived sibling: it can only ever fire on a workflow that declared
// NOTHING (see the reconciliation note on the rung constants), and an unruled
// contract is core by the same err-downward rule that makes an unruled resume
// core. There is no document state that could make it context.
//
// 🔴 shedResumePinned AND shedWorkflowDigest ARE DELIBERATELY ABSENT FROM THIS
// MAP. Their tier is not a
// property of the ladder at all — it is a property of ONE project's resume.md,
// which the ladder reads at run time. It is derived per bootstrap by
// resumeRungTier, and read (with every rung here) through rungTier. Adding a
// static entry back would re-commit the iteration-260 mistake documented on that
// function. Every rung whose tier IS project-agnostic belongs right here, static
// and readable at a glance; deriving the whole map would hide six constants to
// express one variable.
var shedRungTier = map[string]shedTier{
	shedRecentSessions: shedTierContext,
	shedMemory:         shedTierContext,
	shedKGSnapshot:     shedTierContext,
	shedCommands:       shedTierContext,
	shedActiveTasks:    shedTierContext,
	shedWorkflow:       shedTierCore,
}

// resumeRungTier derives the ADR-009 tier of the resume->pinned rung for ONE
// bootstrap, from THAT project's resume body.
//
// THE RULE: shedding the resume is a CORE loss unless every un-pinned section of
// that resume has been positively declared disposable. A section carrying
// neither marker is LIVE STATE (see resumezone.UndeclaredLiveSections) — nobody
// ruled on it, the ladder drops it anyway, and announcing that drop is the
// entire job of budget.shed_core.
//
// 🔴 IT REPLACES A CONSTANT THAT WAS ALWAYS A CLAIM ABOUT ONE DOCUMENT.
// Iteration 260 hard-coded this rung shedTierContext and said why outright: on
// vibe-palace's resume, every live-state and live-hazard section had just been
// pinned, so the un-pinned remainder was a navigation table and losing it cost a
// lookup, not a rule. That was true — OF THAT ONE FILE, ON THAT DAY. Written
// down as a project-agnostic constant, it made one project's editorial state the
// reported tier for every project in every vault. The moment the pin-coverage
// check could measure it (iteration 262) it was false for 8 of 8 projects in the
// live vault: every one of them had undeclared live sections, and the ones whose
// core is over the floor were having them shed with shed_core reporting nothing.
//
// The old comment even carried its own remedy — "if a future resume ever un-pins
// live state again, this must go back to core" — addressed to nobody, checked by
// nothing. A rule with no reader is not a rule. This function is the reader.
//
// 🔴 IT ERRS DOWNWARD, ALWAYS. Any resume this cannot rule on reports CORE: no
// resume resolved at all, a body with no H2 sections, or a body that declares no
// pin zone. Those are not "no core content", they are NO ANSWER, and absence is
// not a value. The cost of being wrong that way is a loud report on a rung that,
// for a resume with no declared pin zone, shedResumeToPinnedZone cannot even
// fire; the cost of being wrong the other way is silence over dropped live
// state, which is the failure class this whole epic exists to delete.
//
// It reads the resume through the SAME two resumezone functions the ladder and
// check.CheckPinCoverage use, so "declares a pin zone" and "undeclared live
// section" cannot come to mean one thing in the reducer and another in the
// report about what was reduced.
func resumeRungTier(fullResume string) shedTier {
	// The zone string is discarded on purpose: only `declared` is wanted, and it
	// is the very verdict shedResumeToPinnedZone gates the shed itself on.
	if _, declared := resumezone.PinnedZone(fullResume); !declared {
		return shedTierCore
	}
	if len(resumezone.UndeclaredLiveSections(fullResume)) > 0 {
		return shedTierCore
	}
	return shedTierContext
}

// workflowRungTier derives the ADR-009 tier of the workflow->digest rung for ONE
// bootstrap, from THAT project's workflow body. It is resumeRungTier's rule,
// applied to the other document, through the SAME two resumezone functions — so
// "declares a pin zone" and "undeclared live section" cannot come to mean one
// thing in the reducer and another in the report about what was reduced.
//
// THE RULE: digesting the workflow is a CORE loss unless every un-pinned section
// has been positively declared disposable. A section carrying neither marker is
// an un-ruled rule: nobody said whether an agent can act without it, the digest
// drops it anyway, and announcing that drop is the entire job of shed_core.
//
// 🔴 IT IS DERIVED, NOT A CONSTANT, BECAUSE THE CONSTANT WAS MEASURED WRONG
// ONCE ALREADY. Iteration 262 falsified exactly this shape of hard-coding on the
// resume rung: 260 wrote down one project's editorial state as a project-agnostic
// tier, and the moment pin coverage could measure it, it was false for 8 of 8
// projects in the live vault. The workflow is the SAME kind of claim about the
// same kind of file, so it gets the same treatment on day one instead of after
// its own incident.
//
// 🔴 IT ERRS DOWNWARD, ALWAYS. No workflow resolved, a body with no H2 sections,
// a body declaring no pin zone: all report CORE. Those are NO ANSWER, not "no
// core content", and absence is not a value (ADR-006). Being wrong that way costs
// a loud report on a rung that, for an undeclared workflow, cannot even fire —
// digestWorkflowToPinnedZone leaves such a document whole. Being wrong the other
// way is silence over a dropped correctness rule.
func workflowRungTier(fullWorkflow string) shedTier {
	if _, declared := resumezone.PinnedZone(fullWorkflow); !declared {
		return shedTierCore
	}
	if len(resumezone.UndeclaredLiveSections(fullWorkflow)) > 0 {
		return shedTierCore
	}
	return shedTierContext
}

// derivedTiers carries the per-bootstrap tier verdicts for the rungs whose tier
// is a property of ONE project's documents rather than of the ladder.
//
// It is a struct rather than two positional arguments so a caller cannot swap
// them silently, and its ZERO VALUE IS SAFE BY CONSTRUCTION: an unset field is
// "", which rungTier reads as core. A caller that has measured neither document
// asserts nothing about either.
type derivedTiers struct {
	Resume   shedTier
	Workflow shedTier
}

// rungTier is the ONE reader of a rung's ADR-009 tier: the static map for the
// rungs whose tier is project-agnostic, and the per-bootstrap verdicts in
// derived — from resumeRungTier and workflowRungTier — for the two rungs whose
// tier is not.
//
// EVERYTHING IT CANNOT ANSWER IS CORE. An unknown rung, and an unset/unknown
// derived verdict, both report core rather than falling through to the zero
// value. The bare map lookup this replaces did the opposite: a missing key
// yielded "" and compared unequal to shedTierCore, so a rung nobody classified
// would shed out of shed_core in complete silence. Same rule as resumeRungTier —
// the direction of a wrong guess is chosen, not left to a zero value.
func rungTier(rung string, derived derivedTiers) shedTier {
	switch rung {
	case shedResumePinned:
		if derived.Resume == shedTierContext {
			return shedTierContext
		}
		return shedTierCore
	case shedWorkflowDigest:
		if derived.Workflow == shedTierContext {
			return shedTierContext
		}
		return shedTierCore
	}
	if t, ok := shedRungTier[rung]; ok {
		return t
	}
	return shedTierCore
}

// coreShed filters shed down to the rungs classified core, preserving shed
// order. Derived from Shed at report time so the two lists can never drift.
//
// derived carries this bootstrap's verdicts on the resume rung (resumeRungTier)
// and the workflow digest rung (workflowRungTier). It is a required argument
// rather than a defaulted one precisely because those tiers are per-project
// facts: a caller that has not measured the documents has no business asserting
// either rung is safe, and rungTier reads an unset value as core.
func coreShed(shed []string, derived derivedTiers) []string {
	var out []string
	for _, r := range shed {
		if rungTier(r, derived) == shedTierCore {
			out = append(out, r)
		}
	}
	return out
}

// shedToBudget is the ONLY reduction path. It sheds, in order, until the payload
// fits — and reports what it did. (It used to be one of two: a byte-axis `slim`
// cut ran before it, by different rules and recorded nowhere. That path was
// deleted — see AssembleBootstrap.)
//
// THE ORDER IS THE DESIGN: least correctness-critical and cheapest to re-fetch
// goes first.
//
//  1. sessions, memory, KG, commands — cheap context, re-fetchable, no rule lives in them.
//  5. resume down to its PINNED ZONE — the diary is a narrative and the full body is one
//     vp_read_resource away; /vpc-restart Step 2 already instructs the agent to fetch it.
//     The behavioral notes are marked resumezone.ResumePinMarker and CANNOT be shed by this rung.
//  6. active_tasks — one cheap vp_list_tasks call away, and ActiveTaskCount survives, so
//     nothing is silently lost. Note it drops the WHOLE list rather than truncating titles:
//     a truncated title is a title that misleads, and the title is what every agent reads
//     first (the 205 set_meta lesson).
//  7. workflow — LAST, because it is the behavioral contract. Losing it is a correctness
//     risk, not an inconvenience.
//
// The four original rungs alone COULD NOT REACH THE BUDGET on any real vault:
// this project's live payload measured 78,055 bytes ≈ 19.5k tokens against a
// default of 8,000, of which resume+workflow+tasks were 96% — and the old loop
// could touch NONE of those three. It shed everything it was allowed to touch,
// came up 2.4x short, and returned anyway without a word.
// workflowDigested says the caller already reduced the workflow to its pinned
// zone above the ladder. It has two consequences and both are load-bearing: the
// rung is REPORTED (a payload whose contract is now a digest must not return
// `budget: absent`, which reads as "nothing was reduced" — the exact instrument
// dishonesty this epic exists to delete), and the excerpt rung is DISABLED, so a
// deliberate selection is never re-cut by a blind prefix.
func shedToBudget(result *BootstrapResult, maxTokens int, fullResume string, workflowDigested bool) *BootstrapBudget {
	b := &BootstrapBudget{MaxTokens: maxTokens}

	// FIRST in shed order because it happened first — before the descent, and
	// regardless of whether a descent was needed at all.
	if workflowDigested {
		b.Shed = append(b.Shed, shedWorkflowDigest)
	}

	// The estimate is rough (4 chars ≈ 1 token) and deliberately so: an exact
	// tokenizer would tie this hot path to a model's vocabulary. It only has to
	// be honest about which side of the line it is on — which is why it measures
	// WITH the budget report attached: the returned payload carries b, and a
	// ladder that measures a payload without the field it later attaches
	// under-reports. That under-measurement, frozen into the verdict, is how the
	// live vault shipped 8060 tokens against a budget of 8000 with over=false.
	// The final verdict is still re-taken by the caller after the post-ladder
	// alerts are composed (see AssembleBootstrap).
	est := func() int {
		result.Budget = b
		return measuredTokens(result, maxTokens)
	}
	tokens := est()
	if tokens <= maxTokens {
		b.EstimatedTokens = tokens
		return b
	}

	// Snapshot every sheddable field before the descent, so the give-back pass
	// below can put back whatever the descent turned out not to need.
	before := *result

	// ── THE DESCENT: shed least-valuable first, until it fits ──
	if len(result.RecentSessions) > 0 {
		result.RecentSessions = nil
		b.Shed = append(b.Shed, shedRecentSessions)
		tokens = est()
	}
	if tokens > maxTokens && len(result.Memory) > 0 {
		result.Memory = nil
		b.Shed = append(b.Shed, shedMemory)
		tokens = est()
	}
	if tokens > maxTokens && result.KGSnapshot != nil {
		result.KGSnapshot = nil
		b.Shed = append(b.Shed, shedKGSnapshot)
		tokens = est()
	}
	if tokens > maxTokens && (len(result.AvailableCommands) > 0 || len(result.AvailableSkills) > 0) {
		result.AvailableCommands = nil
		result.AvailableSkills = nil
		result.CommandInvocation = ""
		b.Shed = append(b.Shed, shedCommands)
		tokens = est()
	}
	if tokens > maxTokens {
		tokens = shedResumeToPinnedZone(result, b, fullResume, est)
	}
	if tokens > maxTokens && len(result.ActiveTasks) > 0 {
		result.ActiveTasks = nil
		b.Shed = append(b.Shed, shedActiveTasks)
		tokens = est()
	}
	// NOT REACHABLE ON A DIGESTED WORKFLOW — see the reconciliation note on the
	// rung constants. The digest is a deliberate selection of whole sections;
	// excerpting it would amputate pinned rules mid-sentence, which is the exact
	// failure the marker was introduced to prevent.
	if tokens > maxTokens && !workflowDigested && len(result.Workflow) > bootstrapExcerptCap {
		result.Workflow = bootstrapExcerptBanner(result.Workflow, result.WorkflowURI)
		b.Shed = append(b.Shed, shedWorkflow)
		tokens = est()
	}

	// ── 🔴 THE GIVE-BACK: put back everything the descent did not actually need ──
	//
	// A greedy descent OVER-SHEDS, and the first version of this ladder proved it
	// on the live vault: it dropped recent_sessions, the KG snapshot AND the
	// command list — and then had to shed the resume and the task list anyway.
	// Those three were destroyed for nothing. Shedding the resume alone would have
	// kept all of them.
	//
	// The descent cannot know that, because it cannot see the future. So it does
	// not have to: walk the shed rungs back in order of DESCENDING VALUE, restore
	// each one that still fits, and keep the rest shed. The most valuable thing we
	// gave up is the first thing offered back.
	//
	// This is the same rule the workflow rung used to enforce by hand — "only pay a
	// cost that buys something" — generalized to every rung, which is why the
	// hand-rolled workflow special case is gone.
	for _, g := range []struct {
		rung    string
		restore func()
	}{
		{shedWorkflow, func() { result.Workflow = before.Workflow }},
		{shedActiveTasks, func() { result.ActiveTasks = before.ActiveTasks }},
		{shedResumePinned, func() { result.Resume = before.Resume }},
		{shedCommands, func() {
			result.AvailableCommands = before.AvailableCommands
			result.AvailableSkills = before.AvailableSkills
			result.CommandInvocation = before.CommandInvocation
		}},
		{shedKGSnapshot, func() { result.KGSnapshot = before.KGSnapshot }},
		{shedMemory, func() { result.Memory = before.Memory }},
		{shedRecentSessions, func() { result.RecentSessions = before.RecentSessions }},
	} {
		if !sliceHasRung(b.Shed, g.rung) {
			continue
		}
		undo := *result
		g.restore()
		if est() <= maxTokens {
			b.Shed = dropRung(b.Shed, g.rung)
			tokens = est()
			continue
		}
		*result = undo // it did not fit after all; stay shed
	}

	// 🔴 THE CONTRACT IS NOT SACRIFICED TO A BUDGET THAT WAS MISSED ANYWAY.
	//
	// The give-back above restores a rung only when restoring it FITS. That is the
	// right rule for re-fetchable context, and the wrong rule for the workflow: if
	// the payload is over budget even after the full descent, then shedding the
	// contract bought NOTHING, and an agent without the contract does not know the
	// rules it is breaking. Being over budget WITH the rules beats being over
	// budget without them. (The cheap rungs stay shed — a smaller payload still
	// lowers the odds a host truncates the tail, where the alerts live.)
	//
	// This restore is the un-armed enforcement of the ADR-009 core tier, and it
	// covers workflow->excerpt AND NOTHING ELSE. workflow->excerpt is the one
	// rung shedRungTier classifies core unconditionally, and it is a PREFIX CUT
	// that can sever the contract mid-rule.
	//
	// 🔴 resume->pinned IS NOT PUT BACK HERE, and since its tier became derived
	// (resumeRungTier) that is a KNOWING omission, not the old claim that its
	// shed is harmless. This comment used to assert that "shedRungTier classifies
	// two rungs core" and that resume->pinned "DELIVERS its core whole when it
	// sheds (the pinned zone survives by construction)" — both stopped being true.
	// The tier is now per project, and on a project whose resume leaves live
	// sections undeclared the rung IS core and its shed IS a real loss: the
	// undeclared sections do not survive, by construction.
	//
	// It stays un-restored anyway, deliberately. Restoring it would change the
	// payload every agent receives, and turning the tier report into a refusal is
	// the gated sibling task adr-009-arm-fail-loud-bootstrap. Today the loss is
	// ANNOUNCED (budget.shed_core names the rung) and never silent, and the
	// remedy is not in this function: rule on the named sections — `vp check
	// --check pin-coverage` lists them by heading.
	//
	// Since ADR-008 what that contract IS has shrunk: the
	// doctrine is served on demand (vp_get_doctrine), and the inline workflow
	// carries the project-specific patterns plus the minimal bootstrap-contract
	// paragraph that points a fresh host at the doctrine surface. At the
	// embedded floor the thin workflow sits under bootstrapExcerptCap, so this
	// rung cannot even trigger — the guard remains for fat vault overrides.
	// Refusing to shed core outright (fail-loud) stays the gated sibling task
	// adr-009-arm-fail-loud-bootstrap.
	if tokens > maxTokens && sliceHasRung(b.Shed, shedWorkflow) {
		result.Workflow = before.Workflow
		b.Shed = dropRung(b.Shed, shedWorkflow)
		tokens = est()
	}

	b.EstimatedTokens = tokens
	// NO VERDICT IS TAKEN HERE. The ladder cannot see the post-ladder alerts
	// (over-budget reason, shed-tasks pointer) that AssembleBootstrap composes
	// into the directive after it returns, so any over/under call made at this
	// point describes a payload other than the one being sent — the stale-report
	// defect. OverBudget and Reason formatting are decided on the final
	// measurement in AssembleBootstrap.
	return b
}

// measuredTokens estimates the token cost of the payload exactly as it stands,
// on the same rough 4-chars-per-token basis the ladder sheds against. An
// unmarshalable payload reports over budget so the failure is loud — "I have no
// information" is not "nothing is wrong".
func measuredTokens(result *BootstrapResult, maxTokens int) int {
	raw, err := json.Marshal(result)
	if err != nil {
		return maxTokens + 1
	}
	return len(raw) / 4
}

// composeDirective joins the capability directive with any alerts (friction,
// vault staleness, health). Alerts come LAST so they are the final thing the model
// reads, and they are never folded into the directive string itself — see the
// truncation path, which re-renders the directive and would otherwise erase them.
func composeDirective(directive string, alerts []string) string {
	if len(alerts) == 0 {
		return directive
	}
	joined := strings.Join(alerts, " ")
	if directive == "" {
		return joined
	}
	return directive + " " + joined
}

// renderPostBootstrapInstructions returns a short directive telling the model
// to announce the available commands and skills after the bootstrap summary.
// Includes up to two live examples drawn from cmds (or a degraded fallback
// when nothing was enumerated) so the directive stays accurate without
// per-project hand-editing.
func renderPostBootstrapInstructions(cmds []commandSummary, _ []skillSummary) string {
	const base = "After presenting this bootstrap summary, tell the user in one or two lines which commands and skills are now available and how to invoke them (`vpc-<name>`, `vps-<name>`)."
	examples := make([]string, 0, 2)
	for i := 0; i < len(cmds) && len(examples) < 2; i++ {
		if cmds[i].Alias != "" {
			examples = append(examples, "`"+cmds[i].Alias+"`")
		}
	}
	if len(examples) == 0 {
		return base + " If no examples survived truncation, call `" + agentfile.CommandToolName + "` or `" + agentfile.SkillToolName + "` with no arguments to list them."
	}
	return base + " Examples from this project: " + joinExamples(examples) + "."
}

// frictionTrendHasData reports whether a computed trend covers at least one
// session in any of its windows. Used to decide whether to attach the trend to
// the bootstrap result: a zero-session project produces all-empty windows and
// must not attach a misleading empty trend.
func frictionTrendHasData(t capture.FrictionTrend) bool {
	for _, w := range t.Windows {
		if w.SessionCount > 0 {
			return true
		}
	}
	return false
}

func joinExamples(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	default:
		return xs[0] + ", " + xs[1]
	}
}

func bootstrapHandler(resolver *vpctx.Resolver, vault *storage.Vault, allowCwdDefault bool) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p bootstrapParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		projectSlug, err := resolveBootstrapProject(p.Project, vault, allowCwdDefault)
		if err != nil {
			return nil, err
		}
		// Validate the project up front: the result advertises resume_uri /
		// workflow_uri built from it, and those URIs are later re-validated by
		// ResolveURI / vp_read_resource. Reject a non-slug project here so we
		// never hand out an URI the read path will refuse.
		if err := slug.Validate(projectSlug); err != nil {
			return nil, fmt.Errorf("invalid project %q: %w", projectSlug, err)
		}
		result := AssembleBootstrap(resolver, vault, projectSlug, p.MaxTokens, p.Wing, p.Room)

		// Phase 4a / D4: bootstrap no longer writes project (or HOME) shims.
		// Host surfaces refresh only via `vp mcp install` / user install paths.
		return result, nil
	}
}

// resolveBootstrapProject returns an explicit project slug, or — when
// allowCwdDefault is set and the argument is empty — a high-confidence
// cwd-derived slug that already has Projects/<slug>/ in the bound vault.
// Basename fallback is never used. HTTP serve passes allowCwdDefault=false so
// multiplexed clients cannot inherit the server process cwd.
func resolveBootstrapProject(explicit string, vault *storage.Vault, allowCwdDefault bool) (string, error) {
	if s := strings.TrimSpace(explicit); s != "" {
		return s, nil
	}
	if !allowCwdDefault {
		return "", fmt.Errorf("project is required: this transport does not default project from cwd (multiplexed HTTP serve) — pass project explicitly or call vp_list_projects")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("project is required: cannot resolve cwd for defaulting (%v) — pass project explicitly or call vp_list_projects", err)
	}
	detected, err := project.DetectProjectHighConfidence(cwd)
	if err != nil {
		return "", fmt.Errorf("project is required: %w", err)
	}
	if !vaultProjectDirExists(vault, detected) {
		return "", fmt.Errorf("project is required: detected %q from cwd but Projects/%s/ is absent from the vault — pass project explicitly, run vp init, or call vp_list_projects", detected, detected)
	}
	return detected, nil
}

// vaultProjectDirExists reports whether Projects/<slug>/ is a directory in the
// bound vault. Used as the second gate on cwd-defaulted bootstrap so a
// high-confidence detect that names a phantom slug fails loud instead of
// returning a successful empty-ish payload (AssembleBootstrap is graceful on
// missing project trees).
func vaultProjectDirExists(vault *storage.Vault, projectSlug string) bool {
	if vault == nil {
		return false
	}
	dir, err := vault.ProjectDir(projectSlug)
	if err != nil {
		return false
	}
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}
