// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import "github.com/suykerbuyk/vibe-palace/internal/cli"

// registerAll registers every command with the registry.
func registerAll(reg *cli.Registry, info cli.BuildInfo) {
	reg.Register(cmdAbsorb())
	reg.Register(cmdAudit())
	reg.Register(cmdAuditRooms())
	reg.Register(cmdCommands())
	reg.Register(cmdCommandsList())
	reg.Register(cmdCommandsUpgrade())
	reg.Register(cmdDiscover())
	reg.Register(cmdDiscoverRooms())
	reg.Register(cmdTune())
	reg.Register(cmdTuneRooms())
	reg.Register(cmdConfig())
	reg.Register(cmdConfigUpgrade())
	reg.Register(cmdMCP())
	reg.Register(cmdCheck(info))
	reg.Register(cmdInit(info))
	reg.Register(cmdInject())
	reg.Register(cmdSearch())
	reg.Register(cmdServe())
	reg.Register(cmdSessions())
	reg.Register(cmdStatus())
	reg.Register(cmdTasks())
	reg.Register(cmdVersion(info))
	reg.Register(cmdVault())
	reg.Register(cmdVaultPull())
	reg.Register(cmdVaultPush())
	reg.Register(cmdVaultSync())
	reg.Register(cmdMigrate())
	reg.Register(cmdMigrateVibeVault())
	reg.Register(cmdMigrateMemPalace())
	reg.RegisterHelp()
}
