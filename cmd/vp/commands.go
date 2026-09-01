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
	// mutates(): `vp audit rooms --apply` calls vault.MoveDrawer, relocating a drawer
	// between rooms, so it must FAIL-STOP against a vault written by a newer binary
	// rather than take the warn-only path — same as any other local vault writer.
	reg.Register(mutates(cmdAuditRooms()))
	// mutates(): `vp audit vault --write` writes a report and `--accept` writes the
	// baseline, so it must FAIL-STOP against a vault written by a newer binary rather
	// than take the warn-only path.
	reg.Register(mutates(cmdAuditVault()))
	reg.Register(cmdCommands())
	reg.Register(cmdCommandsList())
	// mutates(): `vp commands upgrade` and `vp skills upgrade` share one writer —
	// commands.applyWithPolicy (internal/commands/upgrade.go) writes
	// Change.VaultPath, a template under the vault's Templates/ tree. Both are
	// therefore vault writers and must FAIL-STOP against a vault written by a
	// newer binary rather than take the warn-only path. They were registered
	// UNWRAPPED until 2026-08-19, which left the command that WRITES template
	// mirrors ungated while `config sync`, the command that prunes them, was
	// gated — the gate's coverage tracked whether someone typed mutates(), not
	// what the command does.
	reg.Register(mutates(cmdCommandsUpgrade()))
	reg.Register(cmdSkills())
	reg.Register(cmdSkillsList())
	reg.Register(cmdSkillsShow())
	// mutates(): same vault writer as `commands upgrade` above.
	reg.Register(mutates(cmdSkillsUpgrade()))
	reg.Register(cmdDiscover())
	reg.Register(mutates(cmdDiscoverRooms()))
	reg.Register(cmdTune())
	reg.Register(mutates(cmdTuneRooms()))
	reg.Register(cmdConfig())
	reg.Register(mutates(cmdConfigUpgrade()))
	reg.Register(mutates(cmdConfigSync()))
	reg.Register(cmdMCP())
	reg.Register(cmdMCPServe())
	reg.Register(cmdMCPInstall(info))
	reg.Register(cmdMCPUninstall())
	reg.Register(cmdCheck(info))
	reg.Register(cmdHook(info))
	reg.Register(cmdHookInstall())
	reg.Register(cmdHookUninstall())
	reg.Register(mutates(cmdInit(info)))
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
	reg.Register(cmdVersion(info))
	// Worktree ops target the PROJECT repo (not the vault), so they carry no
	// vault surface gate and are registered UNWRAPPED.
	reg.Register(cmdWorktree())
	reg.Register(cmdWorktreeCreate())
	reg.Register(cmdWorktreeRemove())
	reg.Register(cmdWorktreeList())
	reg.Register(cmdVault())
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
	reg.Register(cmdVaultStatus())
	reg.Register(cmdVaultRead())
	reg.Register(mutates(cmdVaultWrite()))
	reg.Register(mutates(cmdVaultEdit()))
	reg.Register(mutates(cmdVaultDelete()))
	reg.Register(mutates(cmdVaultMove()))
	reg.Register(cmdVaultExists())
	reg.Register(cmdVaultSha256())
	reg.Register(cmdMemory())
	reg.Register(mutates(cmdMemoryHarvest()))
	reg.Register(mutates(cmdMigrate()))
	reg.Register(mutates(cmdMigrateVibeVault()))
	reg.Register(mutates(cmdMigrateMemPalace()))
	reg.Register(mutates(cmdMigrateKGFilenames()))
	// mutates(): both rewrite iterations.md — the project's narrative history,
	// the one vault file with no second copy — so they must FAIL-STOP against a
	// vault written by a newer binary rather than take the warn-only path.
	reg.Register(mutates(cmdMigrateIterationHeadings()))
	reg.Register(mutates(cmdMigrateIterationsPreamble()))
	reg.Register(mutates(cmdMigrateTaskPreamble()))
	reg.Register(mutates(cmdMigrateTaskStatus()))
	reg.Register(mutates(cmdMigrateTaskHeader()))
	reg.RegisterHelp()
}
