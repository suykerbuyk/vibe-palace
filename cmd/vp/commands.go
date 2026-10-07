// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import "github.com/suykerbuyk/vibe-palace/internal/cli"

// mutates marks a command as vault-mutating so the dispatch pre-run hook
// (surfaceGate) fail-stops it when the vault's MCP surface version exceeds this
// binary's. Keeping the classification in one place here — rather than in each
// constructor — makes the mutating CLI surface auditable at a glance and is the
// CLI analogue of the Mutating flag on MCP tools.
func mutates(c *cli.Command) *cli.Command {
	c.MutatesVault = true
	return c
}

// registerAll registers every command with the registry.
func registerAll(reg *cli.Registry, info cli.BuildInfo) {
	reg.Register(mutates(cmdAbsorb()))
	reg.Register(cmdArchive())
	reg.Register(mutates(cmdArchiveCreate(info)))
	reg.Register(cmdArchiveThreads())
	reg.Register(cmdArchiveList())
	reg.Register(cmdArchiveVerify())
	reg.Register(cmdArchiveExtract())
	reg.Register(cmdArchiveBackfill())
	// mutates(): `vp archive link` rewrites session notes and a manifest. Registered
	// WRAPPED deliberately — the audit-rooms-apply bypass (below) is the mistake this
	// comment exists to keep from being copied.
	reg.Register(mutates(cmdArchiveLink()))
	reg.Register(cmdAudit())
	// mutates(): `vp audit rooms --apply` relabels room metadata in the host-local
	// chunk store (palace.Relabel -> indexstore.Tx.Rewrite; it no longer moves a
	// tracked drawer), so it must FAIL-STOP against a vault written by a newer binary
	// rather than take the warn-only path — same as any other local writer.
	reg.Register(mutates(cmdAuditRooms()))
	// mutates(): `vp audit vault --write` writes a report and `--accept` writes the
	// baseline, so it must FAIL-STOP against a vault written by a newer binary rather
	// than take the warn-only path.
	reg.Register(mutates(cmdAuditVault()))
	// Registered UNWRAPPED, unlike both its siblings above: `vp audit task-files` has
	// no write mode at all — no --apply, no --write, no --accept — so there is nothing
	// for the surface fail-stop to protect. Wrapping it would make a pure reporter
	// refuse to run against a vault written by a newer binary, which is precisely the
	// vault you most want to be able to inspect.
	reg.Register(cmdAuditTaskFiles())
	reg.Register(cmdCommands())
	reg.Register(cmdCommandsList())
	// `vp commands upgrade` and `vp skills upgrade` are registered UNWRAPPED
	// because neither writes the vault any more. Until
	// upgrade-overwrite-resets-vault-template-overrides they did: both reset a
	// vault Templates/ override by writing the embedded bytes over it
	// (commands.applyWithPolicy -> templates.Executor.Write), so they were
	// gated (from 2026-08-19; before that the command that WROTE template
	// mirrors was ungated while `config sync`, which prunes them, was gated).
	// Now `commands upgrade` writes only project-tree files — shims, agent-file
	// blocks, the project .gitignore and hook — and `skills upgrade` only
	// reports. The derived-gate rule (make source-audit) agrees: neither
	// reaches a vault-write sink. Do not re-wrap them to be safe; a gate on a
	// command that writes nothing is exactly the declared-vs-derived divergence
	// that rule reports.
	//
	// The destructive template verbs are the reset commands: they remove a
	// vault Templates/ file (vaultfs.Delete), write its backup (vaultfs.Create)
	// and commit the removal, so they are gated.
	reg.Register(cmdCommandsUpgrade())
	reg.Register(mutates(cmdCommandsReset()))
	reg.Register(cmdSkills())
	reg.Register(cmdSkillsList())
	reg.Register(cmdSkillsShow())
	reg.Register(cmdSkillsUpgrade())
	reg.Register(mutates(cmdSkillsReset()))
	reg.Register(cmdDiscover())
	reg.Register(mutates(cmdDiscoverRooms()))
	reg.Register(cmdDrain())
	// `drain summaries` is now WRAPPED with mutates(): runDrainSummaries
	// (cmd/vp/cmd_drain.go) resolves the project's own [summarization]
	// config and, when it is enabled and resolvable, constructs a real
	// itersummary.IterationSummarizer that writes vault-committed iteration
	// summaries (storage.Vault.WriteIterationSummary) — so this command can
	// genuinely write vault content now, and must get the same
	// version-mismatch fail-stop protection as any other local vault writer
	// (the same protection vp_trigger_summarization_drain's own
	// MutatingToolNames entry already provides on the MCP path). A project
	// with summarization disabled/unresolvable still drains as a documented
	// no-op (see runDrainSummaries's own doc comment) — mutates() is a
	// blanket, config-independent classification, exactly like every other
	// entry in this list.
	reg.Register(mutates(cmdDrainSummaries()))
	// drain archives writes only host-local files under palace/.local/ (the
	// index store, the embed cache, vp.log), never a tracked one, so it is
	// not a mutating command.
	reg.Register(cmdDrainArchives())
	reg.Register(cmdSummarize())
	// `summarize iterations` writes vault-committed iteration summaries
	// (storage.Vault.WriteIterationSummary, via the same
	// itersummary.IterationSummarizer `drain summaries` above now uses) — an
	// operator-triggered, synchronous alternative to the queue-drain path
	// above, for backfill/regeneration. Wrapped with mutates() for the same
	// reason `drain summaries` is: this is a local vault writer and must get
	// the version-mismatch fail-stop protection every other one gets.
	reg.Register(mutates(cmdSummarizeIterations()))
	reg.Register(cmdTune())
	reg.Register(mutates(cmdTuneRooms()))
	reg.Register(cmdConfig())
	reg.Register(mutates(cmdConfigUpgrade()))
	reg.Register(mutates(cmdConfigSync()))
	// mutates(): `vp config bind` writes the host's global config, not the
	// vault, but it decides which vault every later write lands in — gated
	// like vp_config_bind, its MCP twin.
	reg.Register(mutates(cmdConfigBind()))
	reg.Register(cmdMCP())
	reg.Register(cmdMCPServe())
	reg.Register(cmdMCPInstall(info))
	reg.Register(cmdMCPUninstall())
	reg.Register(cmdCheck(info))
	reg.Register(cmdHook(info))
	reg.Register(cmdHookInstall())
	reg.Register(cmdHookUninstall())
	reg.Register(mutates(cmdInit(info)))
	reg.Register(cmdIndex())
	// Bare, not mutates(): `vp index rebuild` writes ONLY host-local state under
	// the git-ignored palace/.local/ (the index store, ledger, embed cache and
	// run-lock holder), never tracked vault content — the same disposition as
	// `vp drain archives` and `vp search`. Its derived-gate divergence is
	// accepted in internal/sourceaudit/baseline.json.
	reg.Register(cmdIndexRebuild())
	reg.Register(cmdInject())
	reg.Register(cmdSearch())
	reg.Register(cmdSessions())
	reg.Register(cmdFriction())
	reg.Register(cmdTrends())
	reg.Register(cmdEffectiveness())
	reg.Register(cmdStatus())
	reg.Register(cmdPlans())
	reg.Register(cmdPlansScan())
	reg.Register(cmdTasks())
	reg.Register(cmdTasksEpics())
	reg.Register(mutates(cmdTasksEdit()))
	// Registered UNWRAPPED (no mutates()), like cmdTasks()/cmdTasksEpics() above
	// and cmdBoard() below: `vp tasks read` opens a throwaway COPY of a task body
	// in the user's editor (or, with no terminal, prints the body to stdout) and
	// never writes the vault. Wrapping it would be worse than redundant —
	// surfaceGate would make it refuse to run against a vault written by a newer
	// binary, and that is precisely the vault you most want to be able to read.
	// Same ruling, same reason, as cmdAuditTaskFiles().
	reg.Register(cmdTasksRead())
	// `vp board` is registered UNWRAPPED (no mutates()), same as cmdTasks()/
	// cmdTasksEpics() immediately above: Board() is a pure read, vp board
	// never writes the vault.
	reg.Register(cmdBoard())
	reg.Register(cmdVersion(info))
	// Worktree ops target the PROJECT repo (not the vault), so they carry no
	// vault surface gate and are registered UNWRAPPED.
	reg.Register(cmdWorktree())
	reg.Register(cmdWorktreeCreate())
	reg.Register(cmdWorktreeRemove())
	reg.Register(cmdWorktreeList())
	reg.Register(cmdVault())
	// NOT mutates(): `vp vault init` writes only a vault that does not exist
	// yet, never the served vault, so the served vault's surface fail-stop
	// does not apply to it (a newer served vault must not block creating a
	// new one). It is a different command from `vp init` (project
	// onboarding), and shares none of its flags or its host-config writes.
	reg.Register(cmdVaultInit())
	// mutates(), unlike vault init: clone --bind writes the host config and so
	// decides which vault every later write lands in, the reason `vp config
	// bind` is gated.
	reg.Register(mutates(cmdVaultClone()))
	// Registered UNWRAPPED (no mutates()) DELIBERATELY: `vault pull` and `vault
	// push` are TRANSPORT, not authorship. Neither writes vault content bearing
	// this binary's schema — pull applies commits authored elsewhere (its only
	// worktree write is the `checkout HEAD --` heal that restores a stale
	// template to its already-committed bytes), and push requires a clean tree
	// and moves existing commits to a remote. The surface gate exists to stop an
	// OLD binary writing OLD-schema data over a vault a NEWER binary wrote, so it
	// belongs at the write; every authoring path below has it (vault
	// write/edit/delete/move).
	//
	// Gating pull would also be self-defeating: the newer .surface stamps that
	// raise the vault's version ARRIVE BY PULL. A host that pulled once would
	// lock itself out of every subsequent pull and could never reach the state —
	// or the fix — that resolves the mismatch.
	//
	// On a MIGRATED vault the pull also writes derived index paths, and nothing
	// else: a merge conflict on drawers or the ingest ledger alone is concluded
	// by deleting them (vaultfs.Delete), and after every merge the derived paths
	// the merged tree tracks are untracked in a commit of the pull's own, which
	// sync then pushes. Bounded by storage.isDerivedPath, gated by the marker
	// read from the merged index or HEAD. Transport too: it authors nothing and
	// stamps nothing (sourceaudit baseline, ungated-vault-writer
	// main.cmdVaultPull / main.cmdVaultSync, ruled 2026-10-04).
	reg.Register(cmdVaultPull())
	reg.Register(cmdVaultPush())
	// `vault sync` is UNWRAPPED for the same reason as pull and push above, and
	// for one sharper one: IT CONTAINS THE PULL.
	//
	// storage.SyncVault (internal/storage/vaultsyncflow.go:80) is TidyScan →
	// TidyVault → Pull (:120) → push. Gating the command therefore gated the
	// pull inside it, so a host whose binary was behind hit EnforceFailStop →
	// ExitSystem on `vp vault sync` — the ordinary way people take updates —
	// while `vault pull` two lines above was deliberately left open precisely so
	// that could not happen. The lockout the pull/push rationale rules against
	// was live in this file: the escape hatch existed and the command people
	// actually run was not it.
	//
	// It records rather than authors. Measured, not assumed: SyncVault's whole
	// file set — vaultsyncflow.go, vaulttidy.go, vaultsync.go — contains ZERO
	// calls to surface.StampForPath or surface.WriteStamp. It stages and commits
	// bytes already on disk and moves commits to a remote; every byte it records
	// was authored by some earlier write that carried the gate itself.
	//
	// Re-derive, never cite:
	//   grep -n 'StampForPath\|WriteStamp' internal/storage/vaultsyncflow.go \
	//     internal/storage/vaulttidy.go internal/storage/vaultsync.go
	reg.Register(cmdVaultSync())
	// `vault commit` and `vault tidy` STAY WRAPPED — and this is a DEFERRAL, not
	// a verdict. The same measurement covers them: neither stamps, so by the
	// transport-not-authorship reading they arguably belong unwrapped beside
	// sync. They are kept gated because they carry no lockout — neither contains
	// a pull, so refusing them strands nobody — and because the boundary they sit
	// on is an ARTIFACT OF GATING AT THE COMMAND LEVEL AT ALL.
	//
	// `move-the-surface-gate-to-the-write-chokepoint` (task, high, parent
	// first-principles) would put the fail-stop on the vault-write primitives, at
	// which point "is this command a mutator?" stops being a question anyone has
	// to answer — including this one. Adjudicating a boundary we intend to delete
	// would be a fix to a fix, which is the pattern that task exists to stop.
	//
	// So: do not unwrap these two as a tidy-up. Either that task lands and the
	// annotation goes away entirely, or it is refused and this becomes a real
	// question again.
	reg.Register(mutates(cmdVaultCommit()))
	reg.Register(mutates(cmdVaultTidy()))
	// mutates(): `vault copy` writes project content into the served vault and
	// commits it, so it fail-stops against a vault written by a newer binary.
	reg.Register(mutates(cmdVaultCopy()))
	// mutates(): `vault rename` moves a project's footprint, rewrites its stored
	// identifiers and commits them, so it fail-stops against a newer-binary vault.
	reg.Register(mutates(cmdVaultRename()))
	reg.Register(cmdVaultStatus())
	reg.Register(cmdVaultRead())
	reg.Register(mutates(cmdVaultWrite()))
	reg.Register(mutates(cmdVaultEdit()))
	reg.Register(mutates(cmdVaultDelete()))
	// `vault project delete` removes projects and publishes the commit: a
	// vault writer, gated like the other vault writers.
	reg.Register(mutates(cmdVaultProject()))
	reg.Register(mutates(cmdVaultMove()))
	reg.Register(cmdVaultExists())
	reg.Register(cmdVaultSha256())
	reg.Register(cmdMemory())
	reg.Register(mutates(cmdMemoryHarvest()))
	reg.Register(mutates(cmdMigrate()))
	reg.Register(mutates(cmdMigrateVibeVault()))
	reg.Register(mutates(cmdMigrateMemPalace()))
	reg.Register(mutates(cmdMigrateKGFilenames()))
	// Read-only: it classifies a vault copy and compares against its HEAD.
	reg.Register(cmdMigrateKGCensus())
	// mutates(): both rewrite iterations.md — the project's narrative history,
	// the one vault file with no second copy — so they must FAIL-STOP against a
	// vault written by a newer binary rather than take the warn-only path.
	reg.Register(mutates(cmdMigrateIterationHeadings()))
	reg.Register(mutates(cmdMigrateIterationsPreamble()))
	reg.Register(mutates(cmdMigrateTaskPreamble()))
	reg.Register(mutates(cmdMigrateTaskStatus()))
	reg.Register(mutates(cmdMigrateTaskHeader()))
	reg.Register(mutates(cmdMigrateTaskHeaderSpacing()))
	reg.Register(mutates(cmdMigrateTaskSections()))
	reg.Register(mutates(cmdMigrateTaskHeaderShape()))
	reg.Register(mutates(cmdMigrateTaskBoardFields()))
	// ONE-SHOT: deleted with cmd_migrate_project_configs.go.
	reg.Register(mutates(cmdMigrateProjectConfigs()))
	reg.Register(mutates(cmdMigrateTaskHeaderBlock()))
	reg.RegisterHelp()
}
