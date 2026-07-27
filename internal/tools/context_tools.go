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
	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// BootstrapResult is the response from vp_bootstrap_context.
type BootstrapResult struct {
	Project                   string                 `json:"project"`
	Workflow                  string                 `json:"workflow"`
	Resume                    string                 `json:"resume"`
	ResumeSha256              string                 `json:"resume_sha256"`
	WorkflowURI               string                 `json:"workflow_uri"`
	ResumeURI                 string                 `json:"resume_uri"`
	ActiveTasks               []storage.TaskMeta     `json:"active_tasks"`
	ActiveTaskCount           int                    `json:"active_task_count"`
	RecentSessions            []sessionSummary       `json:"recent_sessions,omitempty"`
	KGSnapshot                *storage.KGStats       `json:"kg_snapshot,omitempty"`
	Memory                    []memorySnapshot       `json:"memory,omitempty"`
	AvailableCommands         []commandSummary       `json:"available_commands,omitempty"`
	AvailableSkills           []skillSummary         `json:"available_skills,omitempty"`
	CommandInvocation         string                 `json:"command_invocation,omitempty"`
	PostBootstrapInstructions string                 `json:"post_bootstrap_instructions,omitempty"`
	FrictionTrend             *capture.FrictionTrend `json:"friction_trend,omitempty"`
	VaultStaleness            *VaultStaleness        `json:"vault_staleness,omitempty"`

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
	Budget *BootstrapBudget `json:"budget,omitempty"`

	// ShimsSynced records host shim files this session materialized or rewrote
	// to match the vault's command/skill set — NIL WHEN NOTHING CHANGED. Its
	// presence means the vault had gained (or altered) a capability and this
	// bootstrap wired it into the host so it is typeable, closing the drift that
	// used to require a manual `vp commands upgrade` the human was never told to
	// run.
	//
	// It is deliberately NOT one of the four attention-competing alerts the
	// AuditStaleness comment above caps (Health, AuditStaleness, VaultStaleness,
	// Budget). It is an action-less positive receipt, not a warning: it needs no
	// human response, and it is silent in the steady state (a no-op reconcile is
	// Empty()), so it never trains a reader to skim. It stays a pure data field
	// and is never folded into post_bootstrap_instructions, keeping the alert
	// budget untouched.
	ShimsSynced *shims.ReconcileReport `json:"shims_synced,omitempty"`
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
	// as inviolable core rather than re-fetchable context (see shedRungTier).
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

// bootstrapExcerptCap bounds the rune-safe excerpt substituted for resume (and,
// when oversized, workflow) on the slim byte-axis path. The full body remains
// reachable via the resume_uri / workflow_uri the result always carries.
const bootstrapExcerptCap = 4000

// bootstrapWorkflowInlineCap is the size above which even workflow — normally
// kept inline because it is the behavioral contract and the smaller file — is
// excerpted on the slim path so its bytes cannot bust the channel budget.
const bootstrapWorkflowInlineCap = 24000

// bootstrapExcerptBanner prefixes a slim excerpt with a loud, unmissable
// pointer to the full body. The caller MUST read the URI before acting.
func bootstrapExcerptBanner(body, uri string) string {
	return "⚠ excerpt — full content at " + uri + ", read before acting\n\n" +
		runeSafeExcerpt(body, bootstrapExcerptCap)
}

// bootstrapZoneBanner prefixes the PINNED ZONE of a resume with the same loud
// pointer — but does NOT truncate. The zone is already a deliberate selection
// (every section its author marked as always-inline); cutting it at
// bootstrapExcerptCap would silently amputate the very sections the marker
// exists to protect, which is the failure the marker was introduced to prevent.
func bootstrapZoneBanner(zone, uri string) string {
	return "⚠ pinned sections only — the full resume is at " + uri + ", read it before relying on project state\n\n" + zone
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
	// Slim is tri-state on the byte axis (distinct from the max_tokens token
	// axis): nil ⇒ the per-transport default; true ⇒ excerpt resume (and
	// oversized workflow) behind a banner+URI; false ⇒ full inline bodies.
	Slim *bool `json:"slim,omitempty"`
}

var bootstrapSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug. Required."
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
		},
		"slim": {
			"type": "boolean",
			"description": "Drop the large inline resume body for a banner-led excerpt plus resume_uri (fetch the full body via vp_read_resource). Defaults per transport when omitted."
		}
	},
	"required": ["project"]
}`)

// BootstrapContextTool returns the MCP tool definition for vp_bootstrap_context.
// slimDefault seeds the effective-slim fallback used when a request omits the
// `slim` param; it is variadic only so the many existing constructor call sites
// (and stdio) keep compiling with the false default — the serve path threads
// true via RegisterAll's WithBootstrapSlimDefault option.
func BootstrapContextTool(resolver *vpctx.Resolver, vault *storage.Vault, slimDefault ...bool) mcp.Tool {
	def := false
	if len(slimDefault) > 0 {
		def = slimDefault[0]
	}
	return mcp.Tool{
		Name:        "vp_bootstrap_context",
		Description: "Single-call context restoration: workflow + resume + tasks + recent sessions + KG snapshot + available commands + available skills + post-bootstrap capability-announcement directive. Sheds context to fit max_tokens and REPORTS WHAT IT SHED in `budget.shed` (absent when nothing was shed). A shed resume arrives as its pinned sections only, behind a banner — read `resume_uri` for the full body. A shed task list leaves `active_task_count` — call vp_list_tasks for it.",
		Schema:      bootstrapSchema,
		Handler:     bootstrapHandler(resolver, vault, def),
	}
}

// AssembleBootstrap builds context restoration payload.
// Used by both the MCP tool handler and the CLI inject command.
// When wing/room are provided, palace-scoped commands are included in discovery.
//
// slim is the BYTE-axis control, distinct from the maxTokens TOKEN-axis shed
// ladder below. When slim is true, resume (and oversized workflow) are replaced
// by banner-led, rune-safe excerpts pointing at their always-present URIs.
//
// The two axes remain deliberately separate — slim is an unconditional prefix
// cut for a byte-constrained transport, while the ladder is a graduated,
// last-resort reduction that reaches resume only after the cheap context is
// gone, and reaches only the sections the resume did not pin.
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
// (internal/check/workflow.go) and the LiveVault canary — NOT shrinking the
// charter to fit a number, which inverts the dependency: the contract sets the
// budget, not the reverse.
const DefaultBootstrapMaxTokens = 16000

func AssembleBootstrap(resolver *vpctx.Resolver, vault *storage.Vault, project string, maxTokens int, wing, room string, slim bool) BootstrapResult {
	if maxTokens == 0 {
		maxTokens = DefaultBootstrapMaxTokens
	}

	result := BootstrapResult{
		Project:     project,
		ResumeURI:   mcp.ResumeURI(project),
		WorkflowURI: mcp.WorkflowURI(project),
	}

	// Workflow — graceful on error.
	if wf, _, err := resolver.Resolve("workflow", project); err == nil {
		result.Workflow = wf
	}

	// Resume — graceful on error. The digest comes from the same read as the
	// body and covers the FULL resume, so it is captured HERE, before the slim
	// excerpting below: a sha of the excerpt would collide with nothing on disk
	// and would fail every compare-and-set made by a caller that paged the full
	// body back through resume_uri. It is empty when no project-tier resume.md
	// exists (vault/embedded fallback) — the writer reads that as "assert absent".
	if resume, _, sha, err := resolver.ResolveDigest("resume", project); err == nil {
		result.Resume = resume
		result.ResumeSha256 = sha
	}

	// The full body is held aside for the token ladder's resume rung, which
	// needs the WHOLE document to find its pin markers — the slim excerpt below
	// is a prefix cut and would hide every section after the first 4 KB.
	fullResume := result.Resume

	// Byte-axis slim: excerpt resume behind a banner+URI ONLY when it actually
	// exceeds the cap — a resume that already fits inline (the common embedded
	// default, well under bootstrapExcerptCap) is returned whole and must NOT be
	// mislabeled as a truncated excerpt, or the agent wastes a vp_read_resource
	// round-trip on content it already holds. Workflow stays inline (behavioral
	// contract, smaller file) unless its own size busts the budget.
	//
	// This is the BYTE axis only. The token ladder (shedToBudget) can also reduce
	// resume and workflow, but by different rules and only as a last resort.
	if slim {
		if len(result.Resume) > bootstrapExcerptCap {
			result.Resume = bootstrapExcerptBanner(result.Resume, result.ResumeURI)
		}
		if len(result.Workflow) > bootstrapWorkflowInlineCap {
			result.Workflow = bootstrapExcerptBanner(result.Workflow, result.WorkflowURI)
		}
	}

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

	budget := shedToBudget(&result, maxTokens, fullResume, slim)
	// ADR-009 tier report, derived from Shed so the two can never drift: which
	// of the shed rungs were inviolable core. Reported even though the shed
	// itself still happened — see shedRungTier for why reporting is (for now)
	// the whole mechanism.
	budget.ShedCore = coreShed(budget.Shed)

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
// Skipped when slim already excerpted the resume — that excerpt is SMALLER than
// the pinned zone, so re-expanding to the zone would GROW the payload the ladder
// is trying to shrink.
//
// resume_sha256 is deliberately NOT touched: it covers the FULL RAW file, and a
// caller that pages the whole body back through resume_uri needs the digest to
// still match disk, or every compare-and-set it makes will fail.
func shedResumeToPinnedZone(result *BootstrapResult, b *BootstrapBudget, fullResume string, slim bool, est func() int) int {
	if fullResume == "" || sliceHasRung(b.Shed, shedResumePinned) {
		return est()
	}
	if slim && len(fullResume) > bootstrapExcerptCap {
		return est() // already reduced on the byte axis
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
	result.Resume = bootstrapZoneBanner(zone, result.ResumeURI)
	b.Shed = append(b.Shed, shedResumePinned)
	return est()
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
	shedWorkflow       = "workflow->excerpt"
)

// shedTier is a rung's ADR-009 classification
// (doc/adr/009-inviolable-core-delivered-whole-or-fail-loud.md): core rungs
// carry the operating contract or active project state; context rungs carry
// re-fetchable context.
type shedTier string

const (
	shedTierCore    shedTier = "core"
	shedTierContext shedTier = "context"
)

// shedRungTier classifies every ladder rung, structurally, per ADR-009.
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
// 🔴 shedResumePinned WAS core until iteration 260, and the reclassification is
// a statement about the ARTIFACT, not a relaxation of ADR-009.
//
// ADR-009 classified this rung core for a concrete reason it states outright:
// "resume->pinned drops resume's un-pinned zone, which today still carries Open
// Threads and active Known Issues — live project state and live hazards." That
// was true, and measurably so: only three IDENTITY sections carried the pin
// marker, while Current State, Open Threads and Known Issues — 11,375 B, 41% of
// the file — sat un-pinned and were therefore the first thing spent.
//
// Iteration 260 re-drew that boundary: every live-state and live-hazard section
// is now pinned, and the three hand-maintained history indexes were deleted
// outright (they were stale summaries of iterations.md and tasks/done|cancelled,
// both reachable by tool). What remains un-pinned is the Reference Documents
// navigation table. Shedding it costs a lookup, not a rule.
//
// So the rung is now genuinely context: the zone it reduces TO is the core. A
// rung whose classification no longer matches what it drops is an alarm that
// cries wolf, and this project has spent enough on instruments nobody believes.
// If a future resume ever un-pins live state again, this must go back to core —
// the tier follows the artifact.
var shedRungTier = map[string]shedTier{
	shedRecentSessions: shedTierContext,
	shedMemory:         shedTierContext,
	shedKGSnapshot:     shedTierContext,
	shedCommands:       shedTierContext,
	shedResumePinned:   shedTierContext,
	shedActiveTasks:    shedTierContext,
	shedWorkflow:       shedTierCore,
}

// coreShed filters shed down to the rungs classified core, preserving shed
// order. Derived from Shed at report time so the two lists can never drift.
func coreShed(shed []string) []string {
	var out []string
	for _, r := range shed {
		if shedRungTier[r] == shedTierCore {
			out = append(out, r)
		}
	}
	return out
}

// shedToBudget is the TOKEN-axis ladder (independent of the byte-axis slim
// control, which has already run by the time we get here). It sheds, in order,
// until the payload fits — and reports what it did.
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
func shedToBudget(result *BootstrapResult, maxTokens int, fullResume string, slim bool) *BootstrapBudget {
	b := &BootstrapBudget{MaxTokens: maxTokens}

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
		tokens = shedResumeToPinnedZone(result, b, fullResume, slim, est)
	}
	if tokens > maxTokens && len(result.ActiveTasks) > 0 {
		result.ActiveTasks = nil
		b.Shed = append(b.Shed, shedActiveTasks)
		tokens = est()
	}
	if tokens > maxTokens && len(result.Workflow) > bootstrapExcerptCap {
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
	// This restore is the un-armed enforcement of the ADR-009 core tier for the
	// ONE core rung whose shed leaves core content incomplete. shedRungTier
	// classifies two rungs core: resume->pinned DELIVERS its core whole when it
	// sheds (the pinned zone survives by construction), so there is nothing to
	// un-shed; workflow->excerpt is a prefix cut that can sever the contract, so
	// it alone is put back. Since ADR-008 what that contract IS has shrunk: the
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

func bootstrapHandler(resolver *vpctx.Resolver, vault *storage.Vault, slimDefault bool) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p bootstrapParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		// Validate the project up front: the result advertises resume_uri /
		// workflow_uri built from it, and those URIs are later re-validated by
		// ResolveURI / vp_read_resource. Reject a non-slug project here so we
		// never hand out an URI the read path will refuse.
		if err := slug.Validate(p.Project); err != nil {
			return nil, fmt.Errorf("invalid project %q: %w", p.Project, err)
		}
		// Resolve the tri-state slim: explicit param wins, else the
		// per-transport default seeded at registration.
		slim := slimDefault
		if p.Slim != nil {
			slim = *p.Slim
		}
		result := AssembleBootstrap(resolver, vault, p.Project, p.MaxTokens, p.Wing, p.Room, slim)

		// Session-start shim self-heal: bring this host's slash-command and
		// skill shims into line with the vault's command set, so a command
		// added to the vault becomes typeable WITHOUT the human remembering to
		// run `vp commands upgrade`. Best-effort and additive; runs only over
		// the MCP handler (not the shared AssembleBootstrap, so `vp inject`
		// stays read-only).
		if rep := reconcileHostShims(resolver, p.Project); !rep.Empty() {
			result.ShimsSynced = &rep
		}
		return result, nil
	}
}

// reconcileHostShims runs the additive shim self-heal for the bootstrap's
// project, but ONLY when the MCP server's working directory actually resolves
// to that same project. A server started outside the project tree would
// otherwise scatter shim files into the wrong directory; when the guard
// declines, `vp init` / `vp commands upgrade` remain the fallback for that
// host. Returns an Empty() report whenever it declines or nothing drifted.
func reconcileHostShims(resolver *vpctx.Resolver, projectSlug string) shims.ReconcileReport {
	cwd, err := os.Getwd()
	if err != nil {
		return shims.ReconcileReport{}
	}
	// The server's CWD must resolve to the very project being bootstrapped, or
	// we are looking at the wrong tree. project.DetectProject uses the same
	// signals that named the project in the first place, so agreement is a
	// reliable "this is the right root" check.
	if slug, _ := project.DetectProject(cwd); slug != projectSlug {
		return shims.ReconcileReport{}
	}
	return shims.Reconcile(cwd, resolver, projectSlug)
}
