// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import _ "embed"

// vaultProjectTemplate is the template the retired per-project vault config was
// written from. Nothing in the binary writes or embeds it any longer; it is
// embedded HERE, test-only, because LoadConfig still decodes that file and its
// round-trip tests seed from the real shipped bytes rather than a fixture. It
// goes, with config/vault_project_template.toml, when that decode does (task
// move-per-project-config-out-of-the-shared-vault).
//
//go:embed config/vault_project_template.toml
var vaultProjectTemplate string

// VaultProjectTemplateContent returns the embedded retired template.
func VaultProjectTemplateContent() string { return vaultProjectTemplate }
