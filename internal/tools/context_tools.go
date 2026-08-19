// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/search"
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

	// ActiveTaskCount is the whole open backlog, and it sits here — ahead of the
	// bulk, next to the handles — because a backlog that leaves no trace reads as
	// "no open tasks". head_of_queue carries at most headOfQueueN rows; this is
	// how a reader knows how much it is not seeing, and vp_list_tasks is where
	// the rest lives.
	ActiveTaskCount int `json:"active_task_count"`

	// Ranking reports how the rows below were ordered and what they were ordered
	// against. It is the one instrument that is NEVER silent — see RankingReport
	// for why that exception is earned.
	Ranking *RankingReport `json:"ranking,omitempty"`

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
	//
	// 🔴 IT CARRIES THE VERDICT, NOT THE RECORDS. `recent_warns` is cleared on
	// the copy that ships here; vp_health serves the full tail. See the clearing
	// site in assembleBootstrap for why — in short, an unbounded record list is
	// the one thing that must not sit in the region a host preview keeps.
	Health *vplog.Summary `json:"health,omitempty"`

	// AuditStaleness nags when the vault audit has gone stale — NIL WHEN FRESH, for
	// the same reason Health is.
	//
	// 🔴 THE RULE IS "SILENT WHEN HEALTHY", NOT A HEADCOUNT. Alerts that fire on a
	// healthy vault are how you train a reader to skim ALL of them — the same
	// reasoning that killed the `partial` capture status. So any new bootstrap
	// alert MUST be silent in the healthy case; one that cannot be needs a
	// priority order or a cap on the set first.
	//
	// 📏 RECORD THE GREP, NEVER THE COUNT. This comment used to assert "four
	// possible alerts" and the AuditStaleness site below used to call itself "the
	// fourth" — both were stale, and they were stale in the one comment whose job
	// is to gate additions. Caller friction was appended later and never counted,
	// and the ladder raises two more. Derive it, do not recall it:
	//
	//	grep -n "alerts = append" internal/tools/context_tools.go
	//
	// Note the set is two CLASSES, and only the first is about the vault:
	// CONDITION alerts (friction trend, vault staleness, health, caller friction,
	// audit staleness) report something an operator may need to act on; DELIVERY
	// alerts (task list shed, budget reason) report this payload's own reduction
	// and are raised by the ladder after the condition alerts are composed.
	//
	// A capability announcement is NEITHER. "Feature X is available" is not a
	// warning, nothing is wrong, and nobody must act — it belongs in the
	// capability directive (renderPostBootstrapInstructions), which shares this
	// field but not this channel. Do not spend an alert on one.
	AuditStaleness *vaultaudit.Staleness `json:"audit_staleness,omitempty"`

	FrictionTrend *capture.FrictionTrend `json:"friction_trend,omitempty"`

	// The directive carries the alerts in prose for a reader that skims the
	// structured fields, so it rides with them, ahead of the bulk.
	//
	// 🔴 IT MUST STAY LAST IN THIS BLOCK. It is the only variable-length
	// instrument here, so it is the only one that should absorb a host cut —
	// everything above it is bounded, and a cut that lands in a bounded field
	// destroys a whole instrument instead of a sentence tail.
	//
	// `command_invocation` used to follow it and was DELETED: a constant string,
	// identical on every call for every project forever, restating the vpc-/vps-
	// dispatch rule that mcp.ServerInstructions already delivers at initialize
	// (before any tool call) and internal/agentfile writes into the project
	// context file. Three copies of one sentence, and the per-call copy was the
	// one a preview ate — measured on a live restart, where it was lost whole
	// while the two that survive cost the payload nothing. Do not re-add it here;
	// if the rule needs to change, it changes in mcp.ServerInstructions.
	PostBootstrapInstructions string `json:"post_bootstrap_instructions,omitempty"`

	// ── THE INDEX. Everything below is a list of ROWS, never a document body:
	// each row names what it is and carries the handle that fetches it. This is
	// PRD §1.9 — session start returns an index plus what is relevant to what
	// comes next, and bulk is retrieved, never pushed.
	//
	// 🔴 NO DOCUMENT BODY IS INLINED HERE, UNCONDITIONALLY. `workflow` and
	// `resume` used to lead this block as whole files — 37,060 B of a 68,977 B
	// live payload — and are now reachable only through WorkflowURI / ResumeURI,
	// with ResumeSha256 to compare-and-set what the URI serves. `restart.md`
	// fetches both on every restart; it is not a recovery path any more, it is
	// THE path. That is safe now and was not before first-principles Phase 1,
	// which moved the rules those documents carried into check producers and tool
	// guards: a rule an agent never fetches is a rule it breaks, so lazy delivery
	// had to wait for enforcement to stop living in prose.
	//
	// HeadOfQueue carries no omitempty deliberately: it is the first key of this
	// block on the wire, so a reader (and the wire-order tests) can find the
	// boundary between the instruments and the index without depending on which
	// optional lists a given project happens to populate.
	HeadOfQueue       []headOfQueueRow `json:"head_of_queue"`
	RecentSessions    []sessionSummary `json:"recent_sessions,omitempty"`
	Memory            []memorySnapshot `json:"memory,omitempty"`
	KGSnapshot        *storage.KGStats `json:"kg_snapshot,omitempty"`
	AvailableCommands []commandSummary `json:"available_commands,omitempty"`
	AvailableSkills   []skillSummary   `json:"available_skills,omitempty"`

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

// sessionSummary is one row of the session INDEX: enough to decide whether a
// session is worth reading, plus the handle that reads it.
//
// It carries no Summary. The bodies were the second-largest field in the live
// payload — 17,200 B of 68,977 B, five whole narratives — and every one of them
// is one vp_read_resource away behind URI. rankSessions still reads the summary
// to score relevance; that is server-side parsing (PRD §1.9), and the text it
// scored stays where it lives.
type sessionSummary struct {
	Date      string `json:"date"`
	Iteration int    `json:"iteration"`
	Title     string `json:"title,omitempty"`
	Tag       string `json:"tag,omitempty"`
	URI       string `json:"uri,omitempty"`
}

type bootstrapParams struct {
	Project string `json:"project"`
	Wing    string `json:"wing,omitempty"`
	Room    string `json:"room,omitempty"`
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
func BootstrapContextTool(resolver *vpctx.Resolver, vault *storage.Vault, engine *search.Engine) mcp.Tool {
	return bootstrapContextTool(resolver, vault, engine, true)
}

// BootstrapContextToolExplicit is vp_bootstrap_context with no cwd defaulting —
// project must be supplied on every call. Used by `vp mcp serve` (HTTP).
// Its schema retains required:["project"] so schema-driven clients cannot omit
// it and only learn the requirement from a runtime error string.
func BootstrapContextToolExplicit(resolver *vpctx.Resolver, vault *storage.Vault, engine *search.Engine) mcp.Tool {
	return bootstrapContextTool(resolver, vault, engine, false)
}

func bootstrapContextTool(resolver *vpctx.Resolver, vault *storage.Vault, engine *search.Engine, allowCwdDefault bool) mcp.Tool {
	schema := bootstrapSchemaStdio
	if !allowCwdDefault {
		schema = bootstrapSchemaExplicit
	}
	return mcp.Tool{
		Name:        "vp_bootstrap_context",
		Description: "Single-call context restoration, delivered as an INDEX: head of queue + session index + memory index + KG snapshot + available commands + available skills + post-bootstrap capability-announcement directive. NO DOCUMENT BODY IS INLINED — `resume` and `workflow` are NOT in this payload and never arrive by waiting; FETCH them with vp_read_resource via `resume_uri` and `workflow_uri` on every restart, and `resume_sha256` covers the FULL RAW resume so a caller can compare-and-set against disk. `head_of_queue` is what comes NEXT, derived from the task graph (unblocked, in-progress first, then priority, then dependency order), each row carrying the URI of its task body; `active_task_count` is the whole open backlog, so a count larger than the rows means more work exists — call vp_list_tasks for it. `recent_sessions` are index rows, not narratives: read one via its `uri`. `ranking` states which ranker ordered those rows (structural or semantic), the head-of-queue slug it ranked against, how many candidates it chose from, and fallback_reason when semantic could not run without blocking. The payload LEADS with its instruments (health, vault staleness, friction, ranking, alerts) because those are what a host preview keeps, and it ENDS with `complete: true`: if you do not see that field, your HOST truncated the result.",
		Schema:      schema,
		Handler:     bootstrapHandler(resolver, vault, engine, allowCwdDefault),
	}
}

// AssembleBootstrap builds context restoration payload.
// Used by both the MCP tool handler and the CLI inject command.
// When wing/room are provided, palace-scoped commands are included in discovery.
//
// AssembleBootstrap is the entry point for callers that carry NO transport
// knowledge — `vp inject` and the integration harnesses. It delegates with the
// Herdr announcement gated OFF, which is the only honest default: HERDR_ENV
// describes whichever process happens to be running, and outside the
// per-client stdio MCP server that process is not the agent's pane. See
// assembleBootstrap for why the bit cannot be derived here.
func AssembleBootstrap(resolver *vpctx.Resolver, vault *storage.Vault, project string, wing, room string) BootstrapResult {
	// Inject/CLI path: no search engine → structural ranker with fallback_reason.
	// MCP RegisterAll wires the engine through BootstrapContextTool instead.
	return assembleBootstrap(resolver, vault, project, wing, room, nil, false)
}

// assembleBootstrap is AssembleBootstrap plus the one fact this payload cannot
// derive for itself: whether it is being assembled by the per-client stdio MCP
// server (stdioMCP true) or by the multiplexed HTTP one `vp mcp serve` stands
// up (false).
//
// 🔴 THE BIT IS PASSED IN BECAUSE NOTHING IN HERE CAN OBSERVE IT. Both
// transports run the same binary, the same registry and the same handler; the
// only place the difference is already recorded is registerOptions'
// requireExplicitProject, threaded down as bootstrapHandler's allowCwdDefault
// — the same flag, for the same reason: on stdio the process is spawned per
// client so its environment IS the caller's, while on serve the process
// environment belongs to whoever started the server, days ago, possibly on
// another machine. Sniffing os.Args or stdin's file type would be guessing at a
// fact the caller already knows for certain, so the caller states it.
func assembleBootstrap(resolver *vpctx.Resolver, vault *storage.Vault, project string, wing, room string, engine *search.Engine, stdioMCP bool) BootstrapResult {

	// The Herdr line is built ONCE, here, into a local that is threaded into
	// BOTH renderPostBootstrapInstructions calls below.
	//
	// 🔴 IT IS A LOCAL, NOT AN APPEND TO result.PostBootstrapInstructions. That
	// field is composed twice — the provisional compose before the shed ladder
	// and the FINAL compose after it, which REBUILDS the directive from scratch
	// — so anything appended to the field itself is erased by the rebuild. That
	// is not hypothetical: it is exactly how the friction / staleness / health
	// alerts were lost before they were moved into their own slice (see the
	// ALERTS ARE COLLECTED SEPARATELY note below). A value the rebuild consumes
	// as an INPUT cannot lose that fight.
	//
	// It is deliberately NOT an alert either. Nothing is wrong and nobody must
	// act — see the AuditStaleness field comment: a capability announcement
	// belongs in the capability directive, and spending an alert on one trains
	// the reader to skim the alerts that do matter.
	herdrLine := herdrAnnouncement(stdioMCP)

	// Complete is set HERE, at assembly, not just before the return, so no path
	// through this function can reach a successful result without it.
	result := BootstrapResult{
		Project:     project,
		ResumeURI:   mcp.ResumeURI(project),
		WorkflowURI: mcp.WorkflowURI(project),
		Complete:    true,
	}

	// The resume DIGEST, without the resume. ResolveDigest reads the file to hash
	// it and hands back both; only the hash is kept, because the body's route is
	// ResumeURI and the hash is what lets a caller that pages it back
	// compare-and-set against disk. It is empty when no project-tier resume.md
	// exists (vault/embedded fallback) — the writer reads that as "assert absent".
	if _, _, sha, err := resolver.ResolveDigest("resume", project); err == nil {
		result.ResumeSha256 = sha
	}

	// The open backlog COUNT — graceful on error.
	//
	// Iceboxed tasks are dropped: bootstrap carries what the project INTENDS to
	// do, not everything it KNOWS. An agent that opens a session to a list where
	// the critical work sits beside a dozen deliberately-unscheduled
	// found-in-passing items has to re-derive the difference every time.
	// storage.DropIcebox is the ONE definition of that filter, shared with
	// vp tasks and vp_list_tasks.
	if tasks, err := vault.ListTasks(project, false); err == nil {
		result.ActiveTaskCount = len(storage.DropIcebox(tasks))
	}

	// Head of queue — what comes NEXT, derived from the task graph rather than
	// asked for or guessed from recency (PRD §1.9). It is also the query the
	// session index is ranked against, so it is built before the sessions.
	result.HeadOfQueue = deriveHeadOfQueue(vault, project, headOfQueueN)
	terms := headOfQueueTerms(result.HeadOfQueue)
	ranking := RankingReport{Ranker: rankerStructural}
	if len(result.HeadOfQueue) > 0 {
		ranking.RankedAgainst = result.HeadOfQueue[0].Slug
	}

	// The session INDEX, ranked against the head of queue — graceful on error.
	if sessions, err := vault.ListSessions(project, "", "", 0); err == nil {
		// Compute the friction trend from the FULL history BEFORE the index trim
		// — computing after it would leave the 30d/90d windows meaningless.
		// GetFrictionWindows always returns one window per request, so guard on
		// real data (any window covering at least one session) rather than
		// len(Windows): a zero-session project attaches nothing, while a
		// frictionless-but-active project (RecentAvg 0, direction "stable")
		// legitimately attaches. The omitempty pointer means nil = not attached.
		if trend := capture.ComputeFrictionTrend(sessions, time.Now()); frictionTrendHasData(trend) {
			result.FrictionTrend = &trend
		}
		rows, report := rankSessionIndex(project, sessions, terms, headOfQueueN, engine)
		result.RecentSessions = rows
		ranking.Ranker = report.Ranker
		ranking.Candidates = report.Candidates
		ranking.Returned = report.Returned
		ranking.FallbackReason = report.FallbackReason
	}

	// The ranking report is attached UNCONDITIONALLY, including when the session
	// listing failed and it has zero of zero to report. Attaching it only on the
	// success path would make "the ranker found nothing" and "the ranker never
	// ran" the same bytes — the absent-versus-empty ambiguity that made a missing
	// `budget` uninterpretable from inside a truncated channel (286).
	result.Ranking = &ranking

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
	result.PostBootstrapInstructions = renderPostBootstrapInstructions(result.AvailableCommands, result.AvailableSkills, herdrLine)

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
		// 🔴 THE ALERT CARRIES THE VERDICT; vp_health CARRIES THE RECORDS.
		// RecentWarns is dropped from the bootstrap copy — and ONLY from this
		// copy, on a local value, so vp_health and the CLI still serve the full
		// tail from the same vplog.Summarize.
		//
		// It was the single largest instrument in the payload: 609 B of the
		// 2,214 B header on a live restart, 28% of everything ahead of the bulk,
		// spent on five log records whose only differing content was a timestamp
		// and a fault label — the message string repeated verbatim five times.
		// Its tally already rides in WarnCounts, and healthMessage below prints
		// that tally AND names vp_health as the reader. So the records were the
		// one part of this field nothing else could reach, in the one region of
		// the payload that must survive a host preview.
		//
		// That inversion is the point: a bounded ALERT belongs in the instrument
		// block, an unbounded RECORD belongs behind the reader the alert names.
		// Carrying the reader's payload in the alert's slot is what pushed the
		// block past a host preview and cost it the fields declared after it.
		// Re-adding a record list here re-opens that.
		h.RecentWarns = nil
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
	// 🔴 THIS ALERT COULD ONLY BE ADDED ONCE THE PAYLOAD COULD ACTUALLY DELIVER THE
	// ONES ALREADY ON IT (bootstrap-payload-exceeds-its-own-token-budget, landed at
	// 209). Adding an alert to a vehicle returning 2.4x over budget would not have
	// been a feature; it would have been one more thing nobody reads, reporting
	// success while being silently truncated away. That is the bar for the next one
	// too — the constraint is delivery and silence-when-healthy, not a headcount.
	//
	// This comment used to open "THIS IS THE FOURTH ALERT ON THIS PAYLOAD" while
	// sitting at the FIFTH append site. See AuditStaleness's field comment: record
	// the grep, never the count.
	if as := vaultaudit.CheckStaleness(vault, time.Now()); as.Warn {
		result.AuditStaleness = &as
		alerts = append(alerts, as.Message)
	}

	// FINAL compose. RE-COMPOSE, DO NOT RE-ASSIGN: this used to be a blind
	// assignment inside the shed loop, which threw away the friction / staleness
	// / health alerts appended above — dropping the payload's most important
	// content precisely when the payload was too big to fit, which is when a
	// project is busiest. Rendering from the POST-ladder command list also means
	// the examples can no longer point at aliases that were just shed.
	baseDirective := renderPostBootstrapInstructions(result.AvailableCommands, result.AvailableSkills, herdrLine)
	result.PostBootstrapInstructions = composeDirective(baseDirective, alerts)

	return result
}

// composeDirective joins any alerts (friction, vault staleness, health) with the
// capability directive. They are never folded into the directive string itself —
// see the truncation path, which re-renders the directive and would otherwise
// erase them.
//
// 🔴 ALERTS COME FIRST, AND THIS ORDER IS A DELIVERY CONTRACT, NOT A READING
// PREFERENCE. post_bootstrap_instructions is the LAST field before the bulk and
// the only variable-length instrument in the payload, which makes it the field a
// host preview is designed to cut into. Whatever sits at its tail is what the
// cut destroys.
//
// It used to read `directive + " " + alerts`, on the reasoning that alerts last
// are "the final thing the model reads". That is true only of a payload that
// arrives whole. Measured on a live /vpc-restart 2026-08-16: a 2 KB host preview
// ended inside this field at `…(guards working, not fa` — the entire capability
// announcement survived intact and the caller-friction alert died mid-word. The
// payload spent its surviving bytes telling the agent which commands exist and
// lost the one sentence saying something was wrong.
//
// So the order is inverted: alerts, then the announcement. A cut now lands on
// the capability announcement, which is the correct casualty — it is not an
// alert, nothing is wrong, nobody must act on it, and every host already gets
// the same dispatch rule from mcp.ServerInstructions at initialize. The alerts
// are the reason this field is in the surviving region at all.
//
// This also means an alert must never be appended to the base directive by its
// renderer: doing so puts it back on the far side of the cut.
func composeDirective(directive string, alerts []string) string {
	if len(alerts) == 0 {
		return directive
	}
	joined := strings.Join(alerts, " ")
	if directive == "" {
		return joined
	}
	return joined + " " + directive
}

// renderPostBootstrapInstructions returns a short directive telling the model
// to announce the available commands and skills after the bootstrap summary.
// Includes up to two live examples drawn from cmds (or a degraded fallback
// when nothing was enumerated) so the directive stays accurate without
// per-project hand-editing.
//
// herdrLine is appended verbatim: it is FINISHED TEXT, already built and
// already gated by herdrAnnouncement, so this function knows nothing about
// Herdr and carries no third state to get wrong. Empty means SILENT, and the
// returned string is then byte-for-byte what it was before the announcement
// existed — which TestPostBootstrapDirectiveUnchangedOutsideHerdr pins.
//
// The skills parameter is still deliberately unused (issue 177 — the directive
// names skills generically and draws examples only from commands); this is not
// the change that fixes it.
func renderPostBootstrapInstructions(cmds []commandSummary, _ []skillSummary, herdrLine string) string {
	const base = "After presenting this bootstrap summary, tell the user in one or two lines which commands and skills are now available and how to invoke them (`vpc-<name>`, `vps-<name>`)."
	examples := make([]string, 0, 2)
	for i := 0; i < len(cmds) && len(examples) < 2; i++ {
		if cmds[i].Alias != "" {
			examples = append(examples, "`"+cmds[i].Alias+"`")
		}
	}
	if len(examples) == 0 {
		return base + " If no examples survived truncation, call `" + agentfile.CommandToolName + "` or `" + agentfile.SkillToolName + "` with no arguments to list them." + herdrLine
	}
	return base + " Examples from this project: " + joinExamples(examples) + "." + herdrLine
}

// herdrAnnouncement returns the finished one-sentence Herdr capability line, or
// "" when there is nothing to announce.
//
// 🔴 IT RETURNS THE SENTENCE, NOT THE PANE. Threading the pane id out to the
// renderer would make one string carry two facts — announce-or-stay-silent, and
// which pane — and the empty string is already spoken for by the silent case.
// That forces a sentinel for "in a pane, but Herdr exported no HERDR_PANE_ID",
// and a magic value in a plain string parameter is a trap: passing
// os.Getenv("HERDR_PANE_ID") straight in reads perfectly and silently suppresses
// the announcement for exactly that case. Returning finished text keeps the only
// distinction the parameter carries — empty or not — the one a string can state
// honestly, and leaves renderPostBootstrapInstructions knowing nothing about
// Herdr at all.
//
// The line rides the capability directive rather than the alerts slice on
// purpose: this is "a capability is available here", not "something needs
// looking at". The directive is also excluded from the token-shed ladder, so
// every byte here is guaranteed-delivered weight — which is why it is one
// sentence and not a paragraph.
//
// 🔴 HERDR_ENV IS ONLY EVIDENCE ON THE STDIO MCP PATH, which is what stdioMCP
// gates. A stdio server is spawned per client by the agent's own host, so its
// environment IS the agent's environment and HERDR_ENV=1 really does mean the
// agent lives in that pane. `vp mcp serve` inherits the environment of whoever
// started it — a shell that may itself have sat in a Herdr pane hours earlier,
// on another host entirely — so the same variable there describes a pane no
// connecting agent can see, and telling that agent to drive it would point it
// at somebody else's terminal. Note this is the same reasoning that makes serve
// refuse to default `project` from the process cwd (resolveBootstrapProject):
// on a multiplexed transport, process-scoped facts are not client-scoped facts.
//
// The gate is HERDR_ENV alone. HERDR_PANE_ID is context, never the gate — a
// missing id downgrades the sentence, it does not suppress it, exactly as the
// vpc-herdr command body specifies. An id is never invented: a fabricated one is
// indistinguishable from a measured one and would be refused by herdr (ADR-006).
func herdrAnnouncement(stdioMCP bool) string {
	if !stdioMCP || os.Getenv("HERDR_ENV") != "1" {
		return ""
	}
	if id := strings.TrimSpace(os.Getenv("HERDR_PANE_ID")); id != "" {
		return " This session is running inside Herdr pane " + id + ": `vpc-herdr` loads Herdr pane and agent control on demand."
	}
	return " This session is running inside a Herdr pane: `vpc-herdr` loads Herdr pane and agent control on demand."
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

func bootstrapHandler(resolver *vpctx.Resolver, vault *storage.Vault, engine *search.Engine, allowCwdDefault bool) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p bootstrapParams
		// Malformed params are the caller's JSON, not our state.
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, apperr.Caller(err)
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
			return nil, apperr.Caller(fmt.Errorf("invalid project %q: %w", projectSlug, err))
		}
		// allowCwdDefault is the transport bit, not just a defaulting knob: it is
		// true exactly on stdio and false exactly on `vp mcp serve`, and both
		// process-scoped facts this handler reads — the cwd and the Herdr
		// environment — are only client-scoped on the first of those. Passing it
		// through as stdioMCP names the bit for what it is at the seam that
		// needs it; see herdrAnnouncement.
		result := assembleBootstrap(resolver, vault, projectSlug, p.Wing, p.Room, engine, allowCwdDefault)

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
		return "", apperr.Caller(fmt.Errorf("project is required: this transport does not default project from cwd (multiplexed HTTP serve) — pass project explicitly or call vp_list_projects"))
	}
	cwd, err := os.Getwd()
	if err != nil {
		// DELIBERATELY NOT apperr.Caller, unlike its three siblings: os.Getwd
		// fails when the server process's own working directory has been removed
		// or become unreadable. That is an I/O fault in vp's environment, not a
		// caller supplying bad input, and amber is the correct health signal for
		// it — classifying it caller would hide a genuinely broken process
		// behind the friction counter.
		return "", fmt.Errorf("project is required: cannot resolve cwd for defaulting (%v) — pass project explicitly or call vp_list_projects", err)
	}
	detected, err := project.DetectProjectHighConfidence(cwd)
	if err != nil {
		return "", apperr.Caller(fmt.Errorf("project is required: %w", err))
	}
	if !vaultProjectDirExists(vault, detected) {
		return "", apperr.Caller(fmt.Errorf("project is required: detected %q from cwd but Projects/%s/ is absent from the vault — pass project explicitly, run vp init, or call vp_list_projects", detected, detected))
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
