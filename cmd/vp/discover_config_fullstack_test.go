// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// TestRunDiscoverRooms_ApplyPreservesInheritedConfig is the genuine
// full-stack regression test for the WriteScoringConfig absent-field bug:
// it drives the ACTUAL `vp discover rooms --apply` code path
// (runDiscoverRooms, the function cmdDiscoverRooms's Run closure calls) end
// to end against a real global config file and a real "mostly absent"
// per-project override file — the host-local
// <XDG>/vibe-palace/projects/<slug>.toml, the one per-project tier left and
// the file --apply writes — seeded [meta]-only, the shape that broke in
// production when it lived in the vault — with a fake LLM
// HTTP server standing in for the real endpoint so no network call happens.
//
// Unlike TestRunDiscoverRooms_Apply (cmd_discover_test.go), which hands
// runDiscoverRooms a storage.Config built by hand, this test loads cfg via
// the real v.LoadConfig("proj") call cmdDiscoverRooms itself makes
// (cmd_discover.go:72) before invoking the apply path, and then reloads
// after the apply to prove the on-disk host-local file — not just the
// in-memory Config the command already held — still resolves the global
// [palace.llm] block and every other inherited field. This is the level of
// test that would have caught the original bug: a mock-Config-based test
// cannot, because it never round-trips through the actual file on disk.
func TestRunDiscoverRooms_ApplyPreservesInheritedConfig(t *testing.T) {
	// Fake LLM endpoint: an httptest server standing in for the real
	// [palace.llm] endpoint, so runDiscoverRooms's real llm.NewClient/
	// ChatCompletion call hits a local server instead of the network.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": `[
					{"drawer_id": "any", "room": "devops", "keywords": [
						{"keyword": "neural network", "weight": "high"}
					]}
				]`}},
			},
			"usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// Real global config file at the XDG-resolved path, mirroring the
	// actual production setup that broke: [palace.llm] configured only at
	// the global tier, plus other distinctive non-scoring values so any
	// accidental zeroing by WriteScoringConfig is unambiguous.
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	testutil.RequireResolvedUnder(t, configDir, storage.VaultConfigFilePath)
	t.Setenv("VP_FULLSTACK_TEST_API_KEY", "fake-key-value")

	globalVaultPath := filepath.Join(configDir, "global-tier-vault")
	vpDir := filepath.Join(configDir, "vibe-palace")
	if err := os.MkdirAll(vpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	globalConfig := `git_enabled = false
http_port = 9001
vault_path = "` + globalVaultPath + `"

[palace.llm]
endpoint = "` + srv.URL + `"
model = "fullstack-test-model"
api_key_env = "VP_FULLSTACK_TEST_API_KEY"
max_tokens = 2048
`
	if err := os.WriteFile(filepath.Join(vpDir, "config.toml"), []byte(globalConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real vault the discover command actually operates on, and the
	// project's per-project override file. That file is the host-local
	// <XDG>/vibe-palace/projects/proj.toml (the vault's
	// Projects/<slug>/config.toml is no longer read or written), and it starts
	// in the "mostly absent" state: only [meta], every overridable key never
	// written.
	v := storage.NewVault(t.TempDir())
	cfgPath, err := storage.HostProjectConfigPath("proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("[meta]\nversion_major = 1\nversion_minor = 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed "general" drawers with content the mock LLM's proposed keyword
	// ("neural network") captures — same fixture shape already proven to
	// produce a proposal in TestRunDiscoverRooms_Apply.
	for i := range 3 {
		d := storage.Drawer{
			Content:    "neural network transformer training " + string(rune('a'+i)),
			Hall:       "facts",
			SourceType: "manual",
			FiledAt:    "2026-04-10T10:00:00Z",
		}
		if err := v.AppendDrawer("proj", "proj", "general", d); err != nil {
			t.Fatalf("seed drawer: %v", err)
		}
	}

	// Load config exactly the way cmdDiscoverRooms's Run closure does
	// (cmd_discover.go:72) before calling runDiscoverRooms.
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig (pre-apply): %v", err)
	}
	if cfg.PalaceLLM.Endpoint != srv.URL {
		t.Fatalf("precondition failed: cfg.PalaceLLM.Endpoint = %q, want %q (global tier)", cfg.PalaceLLM.Endpoint, srv.URL)
	}

	// Drive the REAL command handler with --apply, exactly as
	// `vp discover rooms --apply` would.
	var buf bytes.Buffer
	code := runDiscoverRooms(v, "proj", cfg, 0, true, false, "", false, &buf)
	if code != cli.ExitOK {
		t.Fatalf("runDiscoverRooms exit code = %d, want %d; output:\n%s", code, cli.ExitOK, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "Applied") {
		t.Fatalf("expected discovery to apply proposals; output:\n%s", out)
	}

	// The assertion that matters: LoadConfig for this project, reloaded
	// from disk after the real apply, still resolves the global
	// [palace.llm] block and every other inherited field correctly — not
	// zeroed by the WriteScoringConfig call that just ran.
	after, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig (post-apply): %v", err)
	}
	if after.PalaceLLM.Endpoint != srv.URL {
		t.Errorf("PalaceLLM.Endpoint = %q, want %q (global tier, post-apply)", after.PalaceLLM.Endpoint, srv.URL)
	}
	if after.PalaceLLM.Model != "fullstack-test-model" {
		t.Errorf("PalaceLLM.Model = %q, want fullstack-test-model (global tier, post-apply)", after.PalaceLLM.Model)
	}
	if after.PalaceLLM.APIKeyEnv != "VP_FULLSTACK_TEST_API_KEY" {
		t.Errorf("PalaceLLM.APIKeyEnv = %q, want VP_FULLSTACK_TEST_API_KEY (global tier, post-apply)", after.PalaceLLM.APIKeyEnv)
	}
	if after.PalaceLLM.MaxTokens != 2048 {
		t.Errorf("PalaceLLM.MaxTokens = %d, want 2048 (global tier, post-apply)", after.PalaceLLM.MaxTokens)
	}
	if enabled, err := storage.HostGitEnabled(); err != nil || enabled {
		t.Errorf("HostGitEnabled = %v, %v; want false (global tier, post-apply)", enabled, err)
	}
	if after.HTTPPort != 9001 {
		t.Errorf("HTTPPort = %d, want 9001 (global tier, post-apply)", after.HTTPPort)
	}
	if after.VaultPath != globalVaultPath {
		t.Errorf("VaultPath = %q, want %q (global tier, post-apply)", after.VaultPath, globalVaultPath)
	}
	if after.PalaceScoringOverrides == nil {
		t.Error("expected scoring overrides to have been applied")
	}

	// Belt-and-suspenders: the host-local override file on disk itself must
	// have gained the scoring block and nothing else — proving the fix, not
	// just that LoadConfig happens to still resolve correctly some other way.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read host-local project config: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "palace.scoring") {
		t.Errorf("host-local project config %s did not receive the applied scoring:\n%s", cfgPath, content)
	}
	// And the retired vault project config was never created.
	vaultCfg := filepath.Join(v.Root, "Projects", "proj", "config.toml")
	if _, err := os.Stat(vaultCfg); !os.IsNotExist(err) {
		t.Errorf("--apply wrote the retired vault project config %s (stat err=%v)", vaultCfg, err)
	}
	for _, forbidden := range []string{"git_enabled", "http_port", "vault_path", "[palace.llm]"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("project config file unexpectedly contains %q after --apply:\n%s", forbidden, content)
		}
	}
}
