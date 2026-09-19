// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// WriteScoringConfig is the VAULT-path writer, and after R2 of task
// move-per-project-config-out-of-the-shared-vault it exists only here, in a
// test file.
//
// 🔴 IT IS A TEST SHIM ON PURPOSE. Production writes the host-local file
// (WriteHostScoringConfig); nothing in production writes scoring into the vault
// any more, so keeping an exported vault writer would be a surface with no
// caller — the shape the `uninvoked` source-audit rule reports. The splice
// semantics it proves are real and worth keeping pinned, and they belong to
// writeScoringConfigAt, which both destinations share: these tests drive that
// core against a vault path, exactly as the deleted method did.
//
// metaKind is empty here: the vault project file's [meta] comes from its
// template, and seeding one would change what the round-trip tests assert.
func (v *Vault) WriteScoringConfig(project string, rooms map[string]ScoringRoomOverride, minScore float64) error {
	cfgPath, err := v.ProjectConfigFile(project)
	if err != nil {
		return err
	}
	return writeScoringConfigAt(cfgPath, v.Root, v.Root, "", rooms, minScore)
}
