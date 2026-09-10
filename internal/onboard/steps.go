// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package onboard

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/hook"
	"github.com/suykerbuyk/vibe-palace/internal/plugin"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/reconcile"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// stepTable is the canonical onboarding sequence, in the order `vp init` has
// always run it. Steps returns a clone; this var is the single definition and
// the golden test pins its Side / ReadsHostGlobal / Needs tagging so that
// adding, retagging or dropping a prerequisite is a reviewed diff rather than a
// silent change of what a remote surface is allowed to do.
var stepTable = []Step{
	{
		Name: "cwd-project",
		Side: SideWorkingTree,
		Run:  stepCwdProject,
	},
	{
		Name: "vault-project",
		Side: SideVault,
		Run:  stepVaultProject,
	},
	{
		// Needs is not decoration. Without it, a run whose config.toml write
		// just failed would go on to scaffold Projects/<slug>/{commands,skills}
		// into a project that does not exist.
		Name:  "project-scaffold",
		Side:  SideVault,
		Needs: []string{"vault-project"},
		Run:   stepProjectScaffold,
	},
	{
		Name: "agent-wiring",
		Side: SideWorkingTree,
		Run:  stepAgentWiring,
	},
	{
		// SideWorkingTree because that is where the shims land — but
		// ReadsHostGlobal because WHICH shims land is decided by
		// plugin.ClaudeUserCommandsHealthy() and shims.GrokUserCommandsHealthy(),
		// both of which read the RUNNING host's home. Over MCP that would
		// consult the server operator's surface to decide the caller's files.
		Name:            "command-shims",
		Side:            SideWorkingTree,
		ReadsHostGlobal: true,
		Run:             stepCommandShims,
	},
	{
		// The only SideHostGlobal step, and the reason the side exists:
		// hook.Install rewrites ~/.claude/settings.json of whatever machine the
		// process runs on. Its old CLI signature — initHookWiring(_ string,
		// projectReady bool) — already ignored the project root entirely, which
		// is the proof rather than the claim.
		Name:            "hook-wiring",
		Side:            SideHostGlobal,
		ReadsHostGlobal: true,
		Run:             stepHookWiring,
	},
	{
		Name: "project-gitignore",
		Side: SideWorkingTree,
		Run:  stepProjectGitignore,
	},
	{
		Name: "git-post-commit-hook",
		Side: SideWorkingTree,
		Run:  stepGitPostCommitHook,
	},
}

// errOrFirst returns err.Error() if err is non-nil, else the first error from
// errs. Used to surface a single message from a reconciler Apply result that
// may report failures via either return value.
//
// DUPLICATED from cmd/vp/cmd_init.go rather than exported from there: initGlobal
// still needs it and stays in cmd/vp (package main cannot be imported), and
// promoting a four-line string helper into a shared package to avoid one copy
// buys a dependency edge for nothing.
func errOrFirst(err error, errs []error) string {
	if err != nil {
		return err.Error()
	}
	if len(errs) > 0 {
		return errs[0].Error()
	}
	return ""
}

// stepCwdProject writes <project>/.vibe-palace.toml via the CwdProject
// reconciler, then PARSES the result.
//
// # Why the parse is not a nicety
//
// storage.PresentKeys (internal/storage/configupgrade.go) is a LINE SCANNER,
// not a TOML parse. Given a .vibe-palace.toml that is not valid TOML it finds
// no keys at all, so every canonical key reads as missing, storage.UpgradeConfig
// appends the template blocks to the malformed text, and
// CwdProjectReconciler.Apply reports Updated over a file that is still invalid.
// storage.ResolveVaultPath then fails at toml.DecodeFile and the vault never
// opens — a run that reported success end to end, followed by a project that
// cannot resolve its own vault.
//
// So a clean Apply is not proof the file parses. Failure is an outcome, never
// an absence: on a decode error this returns Fail naming the file and the
// VERBATIM parse error, and never Pass.
//
// This is a DISTINCT failure from OpenVault failing. Both exist and both are
// reachable in the same run; collapsing them would send the operator to repair
// the vault when the thing they can actually fix is a file in their own repo.
//
// The operator's original bytes survive either way: an untouched file is
// untouched, and the host-local branch of reconcile.applyUpgrade writes
// <path>.bak before it appends (ruling 5 dropped .bak only on the VAULT
// branch, where the committed config is the pre-image).
func stepCwdProject(ctx context.Context, req Request) []Outcome {
	configPath := filepath.Join(req.ProjectDir, project.ConfigFileName)

	// Read the signal BEFORE the write. Once .vibe-palace.toml exists,
	// DetectSignal reports SignalVibeConfig, and the row would then cite the
	// file this step just created as the evidence for creating it.
	signal := project.DetectSignal(req.ProjectDir)

	cw := reconcile.NewCwdProject(req.ProjectDir, reconcile.CwdProjectSeed{
		Name:              req.Slug,
		Domain:            req.Domain,
		Tags:              req.Tags,
		VaultPathOverride: req.VaultPathOverride,
	}.WithCreate())
	plan, err := cw.Plan(ctx)
	if err != nil {
		return []Outcome{{Status: Fail, Summary: err.Error()}}
	}
	rep, err := cw.Apply(ctx, plan)
	if err != nil || len(rep.Errors) > 0 {
		return []Outcome{{Status: Fail, Summary: errOrFirst(err, rep.Errors)}}
	}

	if perr := decodeTOMLFile(configPath); perr != nil {
		return []Outcome{{
			Status:  Fail,
			Summary: configPath + " is not valid TOML: " + perr.Error(),
			Details: []string{
				"vp cannot resolve this project's vault until the file parses — fix it by hand and re-run `vp init`.",
				"Your bytes are preserved: the file is untouched, or its pre-image is beside it as " +
					configPath + ".bak if this run appended the missing schema blocks.",
			},
		}}
	}

	switch {
	case rep.Created > 0:
		return []Outcome{{
			Created: true,
			Status:  Pass,
			Summary: fmt.Sprintf("created %s (%s, %s detected)", configPath, req.Slug, signal),
		}}
	case rep.Updated > 0:
		return []Outcome{{
			Status:  Pass,
			Summary: fmt.Sprintf("updated %s (%s, %s detected)", configPath, req.Slug, signal),
		}}
	default:
		return []Outcome{{
			Status:  Pass,
			Summary: fmt.Sprintf("%s (%s, %s detected)", configPath, req.Slug, signal),
		}}
	}
}

// decodeTOMLFile parses path as TOML and returns the parse error verbatim.
// The decoded value is discarded: the question is only "does this parse".
func decodeTOMLFile(path string) error {
	var probe map[string]any
	_, err := toml.DecodeFile(path, &probe)
	return err
}

// stepVaultProject writes {vault}/Projects/{slug}/config.toml plus the
// tasks/{done,cancelled} directories.
//
// It is its OWN row now. It used to be a Details line appended to the Project
// config row, which meant a vault-project failure rendered as a footnote under
// a [pass] — and, worse, was invisible to any caller reading rows rather than
// prose. A step that a surface can be forbidden from running needs an identity
// of its own regardless.
func stepVaultProject(ctx context.Context, req Request) []Outcome {
	vault, err := req.OpenVault()
	if err != nil {
		return []Outcome{{Status: Fail, Summary: "open vault: " + err.Error()}}
	}
	vp := reconcile.NewVaultProject(vault, req.Slug)
	plan, perr := vp.Plan(ctx)
	if perr != nil {
		slog.Error("vault-project plan", "project", req.Slug, "err", perr)
		return []Outcome{{Status: Fail, Summary: "vault-project config write failed: " + perr.Error()}}
	}
	rep, aerr := vp.Apply(ctx, plan)
	if aerr != nil {
		slog.Error("vault-project apply", "project", req.Slug, "err", aerr)
		return []Outcome{{Status: Fail, Summary: "vault-project config write failed: " + aerr.Error()}}
	}
	if len(rep.Errors) > 0 {
		for _, e := range rep.Errors {
			slog.Error("vault-project apply error", "project", req.Slug, "err", e)
		}
		return []Outcome{{Status: Fail, Summary: "vault-project config write failed: " + rep.Errors[0].Error()}}
	}
	summary := "Projects/" + req.Slug + "/config.toml"
	if cfgPath, cerr := vault.ProjectConfigFile(req.Slug); cerr == nil {
		summary = cfgPath
	}
	return []Outcome{{Status: Pass, Summary: summary, Created: rep.Created > 0}}
}

// stepProjectScaffold creates Projects/<slug>/{commands,skills}/ with README
// stubs. Best-effort: a scaffold failure is an Info row, never a Fail, because
// the project config it hangs off already landed and the operator can re-run.
func stepProjectScaffold(ctx context.Context, req Request) []Outcome {
	vault, err := req.OpenVault()
	if err != nil {
		return []Outcome{{Status: Fail, Summary: "open vault: " + err.Error()}}
	}
	tt := reconcile.NewTemplateTree(vault.Root, "Projects/"+req.Slug, reconcile.TemplateTreeSeed{
		Mode: reconcile.TemplateModeScaffold,
	})
	plan, err := tt.Plan(ctx)
	if err != nil {
		slog.Error("project scaffold plan", "project", req.Slug, "err", err)
		return []Outcome{{Status: Info, Summary: fmt.Sprintf("scaffold plan failed: %v", err)}}
	}
	rep, err := tt.Apply(ctx, plan)
	switch {
	case err != nil:
		slog.Error("project scaffold apply", "project", req.Slug, "err", err)
		return []Outcome{{Status: Info, Summary: fmt.Sprintf("scaffold apply failed: %v", err)}}
	case len(rep.Errors) > 0:
		slog.Error("project scaffold apply error", "project", req.Slug, "err", rep.Errors[0])
		return []Outcome{{Status: Info, Summary: fmt.Sprintf("scaffold apply error: %v", rep.Errors[0])}}
	}
	// The row must not out-claim the report. "scaffolded" was printed
	// unconditionally, so a converged re-init — every action Unchanged,
	// rep.Created == 0 — announced work it had not done. While the marker gate
	// existed that row was suppressed on a re-init and the wording was only
	// ever seen when it was true; deleting the gate made the claim print on
	// EVERY run, which is the "report more than you did" defect this package
	// exists to eliminate.
	//
	// The shape is its siblings': stepCwdProject separates created from
	// updated from unchanged, and stepCommandShims downgrades a
	// nothing-happened run to Info.
	switch {
	case rep.Created > 0:
		return []Outcome{{
			Status:  Pass,
			Created: true,
			Summary: fmt.Sprintf("scaffolded Projects/%s/{commands,skills}/", req.Slug),
		}}
	case rep.Updated > 0:
		return []Outcome{{
			Status:  Pass,
			Summary: fmt.Sprintf("updated Projects/%s/{commands,skills}/", req.Slug),
		}}
	default:
		return []Outcome{{
			Status:  Info,
			Summary: fmt.Sprintf("Projects/%s/{commands,skills}/ already present — nothing to scaffold", req.Slug),
		}}
	}
}

// stepAgentWiring detects agent instruction files under the project root,
// writes the vibe-palace managed block into each, and returns one row per
// (canonical) target plus one row per deliberate skip.
func stepAgentWiring(_ context.Context, req Request) []Outcome {
	projectRoot := req.ProjectDir

	// Guarantee AGENTS.md *exists* so the WireAll pass below wires it and
	// reports it. AGENTS.md is a host-local vp-managed bootstrap shim
	// (gitignored, like CLAUDE.md) and the cross-host behavioral baseline;
	// WireAll only wires *pre-existing* agent files, so a fresh project would
	// otherwise never get one. We create an EMPTY file (not the managed block
	// itself) so WireAll remains the sole wirer and honestly reports "block
	// added" on a fresh init rather than "unchanged". Non-fatal: a failure must
	// not abort init — log and let WireAll proceed.
	agentsPath := filepath.Join(projectRoot, "AGENTS.md")
	if _, err := os.Stat(agentsPath); os.IsNotExist(err) {
		if werr := os.WriteFile(agentsPath, []byte{}, 0o644); werr != nil {
			slog.Error("create AGENTS.md baseline", "err", werr)
		}
	}

	// Snapshot legacy-content flags before WireAll rewrites files, so the
	// Init summary can suggest `vp absorb` when pre-existing content needs
	// migration. Detect runs once here, and WireAll re-runs Detect internally
	// — cheap (a few os.Stat calls) and keeps the orchestrator's surface
	// focused on wiring rather than reporting.
	preTargets, _ := agentfile.Detect(projectRoot)
	driftSet := make(map[string]bool, len(preTargets))
	for _, t := range preTargets {
		if data, err := os.ReadFile(t.Path); err == nil && hasLegacyContent(data) {
			driftSet[t.DisplayName] = true
		}
	}

	outcomes, skips, err := agentfile.WireAll(projectRoot)
	if err != nil {
		return []Outcome{{Status: Fail, Summary: err.Error()}}
	}

	var rows []Outcome
	if len(outcomes) == 0 {
		rows = append(rows, Outcome{
			Status:  Skip,
			Summary: "no agent file found; create CLAUDE.md/AGENTS.md and re-run `vp init`",
		})
	}

	var driftFiles []string
	for _, oc := range outcomes {
		t := oc.Target
		display := t.DisplayName
		if len(t.Aliases) > 0 {
			display += " (→ " + strings.Join(t.Aliases, ", ") + ")"
		}
		if driftSet[t.DisplayName] {
			driftFiles = append(driftFiles, t.DisplayName)
		}
		if oc.Err != nil {
			rows = append(rows, Outcome{Status: Fail, Summary: display + ": " + oc.Err.Error()})
			continue
		}
		switch oc.Result.Kind {
		case agentfile.Added:
			rows = append(rows, Outcome{Status: Pass, Created: true, Summary: display + " — block added"})
		case agentfile.Updated:
			summary := display + " — block updated"
			if oc.Result.PrevSha != "" {
				summary += " (was " + oc.Result.PrevSha + ")"
			}
			rows = append(rows, Outcome{Status: Pass, Summary: summary})
		case agentfile.Unchanged:
			rows = append(rows, Outcome{Status: Info, Summary: display + " — block unchanged"})
		}
	}

	for _, s := range skips {
		rows = append(rows, Outcome{Status: Skip, Summary: s.DisplayName + " — " + s.Reason})
	}
	if len(driftFiles) > 0 {
		rows = append(rows, Outcome{
			Status:  Info,
			Summary: "legacy content detected in " + strings.Join(driftFiles, ", "),
			Details: []string{"run `vp absorb` to migrate existing content into the vault"},
		})
	}
	return rows
}

// hasLegacyContent reports whether data contains any non-whitespace bytes
// outside the managed vibe-palace block. Used to suggest `vp absorb`.
func hasLegacyContent(data []byte) bool {
	start, end := agentfile.FindBlock(data)
	var outside []byte
	if start < 0 {
		outside = data
	} else {
		outside = append([]byte{}, data[:start]...)
		if end <= len(data) {
			outside = append(outside, data[end:]...)
		}
	}
	for _, b := range outside {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return true
		}
	}
	return false
}

// stepCommandShims emits one .claude/commands/vpc-<name>.md shim per
// vibe-palace command into the project root, surfacing the command set in
// Claude Code's `/` slash menu. Additive-by-default: stale shims (commands that
// no longer exist) are reported but not deleted — `vp commands upgrade` handles
// removal with explicit user consent.
//
// Per-host skip when that host's user-global surface is healthy (C1 — never OR
// hosts together). Project-scoped commands remain MCP-only under a global
// surface (D6). Those two probes read the RUNNING host's home, which is why the
// step is tagged ReadsHostGlobal.
func stepCommandShims(_ context.Context, req Request) []Outcome {
	projectRoot := req.ProjectDir

	claudeOK := plugin.ClaudeUserCommandsHealthy()
	grokOK := shims.GrokUserCommandsHealthy()
	opts := shims.ReconcileOptions{
		SkipClaude: claudeOK,
		SkipGrok:   grokOK,
	}
	if claudeOK {
		opts.ClaudeSkipReason = "Claude user-global cache has vpc-* (vp mcp install --claude-plugin); project .claude/commands not re-emitted — bare /vpc-* may require vibe-palace: prefix or project shims"
	}
	if grokOK {
		opts.GrokSkipReason = "Grok user-global plugin has vpc-* (vp mcp install --grok); project .grok/plugins not re-emitted"
	}

	var rows []Outcome
	if claudeOK {
		rows = append(rows, Outcome{
			Name:    "Slash-command shims (Claude)",
			Status:  Info,
			Summary: "skipped — user-global Claude surface healthy",
			Details: []string{"  " + opts.ClaudeSkipReason, "  Project-scoped commands: MCP-only under global menu (D6)"},
		})
	}
	if grokOK {
		rows = append(rows, Outcome{
			Name:    "Slash-command shims (Grok)",
			Status:  Info,
			Summary: "skipped — user-global Grok surface healthy",
			Details: []string{"  " + opts.GrokSkipReason, "  Project-scoped commands: MCP-only under global menu (D6)"},
		})
	}

	// Still need project emit for any host that is not globally healthy
	// (and Cursor when present — always project-local).
	needProject := !claudeOK || (!grokOK && shims.GrokPresent(projectRoot)) || shims.CursorPresent(projectRoot)
	if !needProject {
		if len(rows) == 0 {
			// Unreachable by construction — !needProject implies claudeOK,
			// which appended a row above. Kept anyway because Run refuses a
			// step that accounts for nothing, and turning a shim no-op into a
			// hard `vp init` failure would be a bad way to learn that this
			// implication stopped holding.
			rows = append(rows, Outcome{
				Status:  Info,
				Summary: "nothing to emit — every host's user-global surface is healthy",
			})
		}
		return rows
	}

	// req.OpenVault, not a cwd-derived resolution: the shims belong to
	// req.ProjectDir, so the vault whose command surface they mirror must be
	// the one THAT directory resolves to.
	vault, err := req.OpenVault()
	if err != nil {
		return append(rows, Outcome{Status: Skip, Summary: "skipped — open vault: " + err.Error()})
	}
	resolver := vpctx.NewResolver(vault.Root)
	slug, _ := project.DetectProject(projectRoot)

	rep := shims.Reconcile(projectRoot, resolver, slug, opts)
	if len(rep.Errors) > 0 {
		// Read-only target dirs and similar stay Info/Skip-ish: Reconcile
		// records them as Errors; surface as Info so a deliberately read-only
		// .claude/ does not look like a hard Fail (M5).
		return append(rows, Outcome{
			Name:    "Slash-command shims (project)",
			Status:  Info,
			Summary: "project emit incomplete: " + strings.Join(rep.Errors, "; "),
		})
	}
	summary := fmt.Sprintf(
		"added %d, updated %d (commands +%d skills +%d)",
		len(rep.CommandsAdded)+len(rep.SkillsAdded),
		len(rep.CommandsUpdated)+len(rep.SkillsUpdated),
		len(rep.CommandsAdded), len(rep.SkillsAdded),
	)
	status := Pass
	if rep.Empty() {
		status = Info
	}
	return append(rows, Outcome{
		Name:    "Slash-command shims (project)",
		Status:  status,
		Created: len(rep.CommandsAdded)+len(rep.SkillsAdded) > 0,
		Summary: summary,
	})
}

// stepHookWiring ensures vp hook entries are installed in
// ~/.claude/settings.json, replacing any legacy vv hook entries. It touches
// only the RUNNING host — it never reads req.ProjectDir.
func stepHookWiring(_ context.Context, _ Request) []Outcome {
	status, err := hook.Status()
	if err != nil {
		return []Outcome{{Status: Skip, Summary: "could not check hook status: " + err.Error()}}
	}
	if status.Installed && !status.LegacyPresent && !status.Stale {
		return []Outcome{{Status: Info, Summary: "vp hook already installed"}}
	}
	changed, err := hook.Install()
	if err != nil {
		return []Outcome{{Status: Fail, Summary: "hook install: " + err.Error()}}
	}
	msg := "vp hook installed"
	if status.LegacyPresent {
		msg += " (vv hook preserved — both will fire)"
	}
	if !changed {
		return []Outcome{{Status: Info, Summary: msg}}
	}
	return []Outcome{{Status: Pass, Created: true, Summary: msg}}
}

// stepProjectGitignore reconciles the project repo-root .gitignore so the
// host-local AI artifacts vp writes into the project tree (CLAUDE.md,
// commit.msg, .claude/, .grok/, .vibe-palace/) are never committed. It is
// append-only and idempotent. Failures are non-fatal — they surface as an Info
// row and are logged, mirroring how the vault .gitignore reconcile is treated,
// so a gitignore hiccup never aborts onboarding.
func stepProjectGitignore(_ context.Context, req Request) []Outcome {
	if err := storage.ReconcileProjectGitignore(req.ProjectDir); err != nil {
		slog.Error("project gitignore reconcile error", "err", err)
		return []Outcome{{Status: Info, Summary: "could not reconcile .gitignore: " + err.Error()}}
	}
	return []Outcome{{Status: Pass, Summary: "host-local vp artifacts ignored"}}
}

// stepGitPostCommitHook installs the post-commit hook that deletes the
// project-root commit.msg once the commit that consumed it has landed. It
// mirrors stepProjectGitignore's posture exactly — writes into the project
// tree, idempotent, non-fatal, one row.
//
// This is a GIT hook and has nothing to do with stepHookWiring above, which
// wires AI-host session hooks into ~/.claude/settings.json. The two
// vocabularies are deliberately kept apart; see internal/storage/githook.go.
//
// A refusal (a foreign post-commit hook, or a repo with core.hooksPath set) is
// an Info row naming the reason, never a Fail: neither is repairable here and
// both are the human's call.
func stepGitPostCommitHook(_ context.Context, req Request) []Outcome {
	rep := storage.InstallPostCommitHook(req.ProjectDir)
	switch rep.Status {
	case storage.HookInstalled:
		return []Outcome{{Status: Pass, Created: true, Summary: rep.Detail}}
	case storage.HookCurrent:
		return []Outcome{{Status: Pass, Summary: rep.Detail}}
	case storage.HookNoRepo:
		return []Outcome{{Status: Skip, Summary: rep.Detail}}
	default:
		slog.Warn("post-commit hook not installed", "reason", rep.Detail)
		return []Outcome{{Status: Info, Summary: rep.Detail}}
	}
}
