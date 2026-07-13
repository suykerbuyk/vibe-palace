// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
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
			"description": "Token budget for response. Default: 8000."
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
		Description: "Single-call context restoration: workflow + resume + tasks + recent sessions + KG snapshot + available commands + available skills + post-bootstrap capability-announcement directive.",
		Schema:      bootstrapSchema,
		Handler:     bootstrapHandler(resolver, vault, def),
	}
}

// AssembleBootstrap builds context restoration payload.
// Used by both the MCP tool handler and the CLI inject command.
// When wing/room are provided, palace-scoped commands are included in discovery.
//
// slim is the BYTE-axis control, distinct from the maxTokens TOKEN-axis shed
// loop below. When slim is true, resume (and oversized workflow) are replaced
// by banner-led, rune-safe excerpts pointing at their always-present URIs. When
// slim is false, resume/workflow stay fully inline no matter what — the
// token-budget shed loop NEVER excerpts them; it only sheds
// sessions→memory→KG→commands+skills. The two axes are deliberately separate.
func AssembleBootstrap(resolver *vpctx.Resolver, vault *storage.Vault, project string, maxTokens int, wing, room string, slim bool) BootstrapResult {
	if maxTokens == 0 {
		maxTokens = 8000
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

	// Byte-axis slim: excerpt resume behind a banner+URI ONLY when it actually
	// exceeds the cap — a resume that already fits inline (the common embedded
	// default, well under bootstrapExcerptCap) is returned whole and must NOT be
	// mislabeled as a truncated excerpt, or the agent wastes a vp_read_resource
	// round-trip on content it already holds. Workflow stays inline (behavioral
	// contract, smaller file) unless its own size busts the budget. This is the
	// ONLY place resume/workflow are ever excerpted — the token shed loop further
	// down leaves them untouched.
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
	if h := vplog.Summarize(vaultLogPath(vault), healthWindowHours, healthDisplayLimit); !h.Healthy() {
		result.Health = &h
		alerts = append(alerts, healthMessage(h))
	}

	result.PostBootstrapInstructions = composeDirective(result.PostBootstrapInstructions, alerts)

	// Token budget truncation (TOKEN axis — independent of the byte-axis slim
	// above): rough estimate 4 chars per token. Shed order: sessions → memory →
	// KG → commands+skills (as a pair). This loop NEVER touches resume/workflow:
	// under slim=false they stay fully inline even when over maxTokens.
	raw, err := json.Marshal(result)
	if err == nil {
		estimatedTokens := len(raw) / 4
		for estimatedTokens > maxTokens && len(result.RecentSessions) > 0 {
			result.RecentSessions = result.RecentSessions[:len(result.RecentSessions)-1]
			raw, _ = json.Marshal(result)
			estimatedTokens = len(raw) / 4
		}
		for estimatedTokens > maxTokens && len(result.Memory) > 0 {
			result.Memory = result.Memory[:len(result.Memory)-1]
			raw, _ = json.Marshal(result)
			estimatedTokens = len(raw) / 4
		}
		if estimatedTokens > maxTokens && result.KGSnapshot != nil {
			result.KGSnapshot = nil
			raw, _ = json.Marshal(result)
			estimatedTokens = len(raw) / 4
		}
		if estimatedTokens > maxTokens && (len(result.AvailableCommands) > 0 || len(result.AvailableSkills) > 0) {
			result.AvailableCommands = nil
			result.AvailableSkills = nil
			result.CommandInvocation = ""
			// PostBootstrapInstructions deliberately survives, but the examples
			// rendered pre-truncation point at aliases that just got shed.
			// Re-render so the directive degrades to "call vp_cmd / vp_skill
			// to list them" instead of dangling stale references.
			//
			// RE-COMPOSE, DO NOT RE-ASSIGN. This used to be a blind assignment, which
			// threw away the friction / staleness / health alerts appended above —
			// dropping the payload's most important content precisely when the payload
			// was too big to fit, which is when a project is busiest.
			result.PostBootstrapInstructions = composeDirective(renderPostBootstrapInstructions(nil, nil), alerts)
		}
	}

	return result
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
		return AssembleBootstrap(resolver, vault, p.Project, p.MaxTokens, p.Wing, p.Room, slim), nil
	}
}
