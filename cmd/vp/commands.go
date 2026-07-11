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
	reg.Register(cmdArchiveList())
	reg.Register(cmdArchiveVerify())
	reg.Register(cmdArchiveExtract())
	reg.Register(cmdAudit())
	reg.Register(cmdAuditRooms())
	reg.Register(cmdCommands())
	reg.Register(cmdCommandsList())
	reg.Register(cmdCommandsUpgrade())
	reg.Register(cmdSkills())
	reg.Register(cmdSkillsList())
	reg.Register(cmdSkillsShow())
	reg.Register(cmdSkillsUpgrade())
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
	reg.Register(cmdTasks())
	reg.Register(cmdVersion(info))
	reg.Register(cmdVault())
	reg.Register(cmdVaultPull())
	reg.Register(cmdVaultPush())
	reg.Register(mutates(cmdVaultSync()))
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
	reg.RegisterHelp()
}
