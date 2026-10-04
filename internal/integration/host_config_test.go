// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// writeHostConfig isolates the host config (testinfra.IsolateEnv) and writes the
// global vault config (global, after a [meta] header) and project's host-local
// config (local); an empty string writes no file. IndexTranscript takes its
// chunk recipe and room scoring from these files (palace.ProjectIndexing),
// never from the storage.Config a harness is built with, so a test that
// varies them must write them here.
func writeHostConfig(t *testing.T, project, global, local string) {
	t.Helper()
	testinfra.IsolateEnv(t)
	write := func(p, body string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if global != "" {
		p, err := storage.VaultConfigFilePath()
		if err != nil {
			t.Fatal(err)
		}
		write(p, "[meta]\nversion_major = 1\n\n"+global)
	}
	if local != "" {
		p, err := storage.HostProjectConfigPath(project)
		if err != nil {
			t.Fatal(err)
		}
		write(p, local)
	}
}
