// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// These pin the top layer of LoadConfig: the host-local per-project file at
// <XDG>/vibe-palace/projects/<slug>.toml, which outranks the host global config
// and may set only the scoring keys. The vault's Projects/<slug>/config.toml is
// no longer read at all.
//
// Every test isolates XDG_CONFIG_HOME, so none of them reads or writes the
// host's real config directory.

// isolateHostConfig redirects every root os.UserConfigDir consults —
// XDG_CONFIG_HOME (Linux), HOME (macOS: ~/Library/Application Support) and
// APPDATA (Windows) — under one fresh temp dir, and REQUIRES the host config to
// resolve inside it before any test writes there. Setting XDG_CONFIG_HOME alone
// isolates Linux only; on another OS a write to VaultConfigFilePath() would
// land in the developer's real config directory. RequireResolvedUnder fails
// (never skips) if the resolution escapes, so such a machine reds instead of
// being written to. It returns the XDG_CONFIG_HOME directory.
func isolateHostConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	xdg := filepath.Join(root, "xdg")
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	testutil.RequireResolvedUnder(t, root, VaultConfigFilePath)
	return xdg
}

// hostLocalEnv isolates XDG, seeds the host global config, and returns the
// vault plus the host-local path for project. Nothing here touches the real
// user config directory: isolateHostConfig proves the path first.
func hostLocalEnv(t *testing.T, project, globalConfig string) (*Vault, string) {
	t.Helper()
	isolateHostConfig(t)
	globalPath, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(globalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalPath, []byte(globalConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	hostPath, err := HostProjectConfigPath(project)
	if err != nil {
		t.Fatal(err)
	}
	return testVault(t), hostPath
}

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The host-local room wins over the global one.
func TestLoadConfigHostLocalRoomWins(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "[palace.scoring.rooms.general]\nhigh = [\"global-high\"]\n")
	writeFileAt(t, hostPath, "[palace.scoring.rooms.general]\nhigh = [\"host-high\"]\n")

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.PalaceScoringOverrides["general"].High; len(got) != 1 || got[0] != "host-high" {
		t.Errorf("general.high = %v, want [host-high]", got)
	}
}

// A host-local room replaces the whole room, not tier by tier: naming only
// `high` leaves Medium and Low empty, over a global room that set all three.
func TestLoadConfigHostLocalReplacesWholeRoom(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj",
		"[palace.scoring.rooms.general]\nhigh = [\"global-high\"]\nmedium = [\"global-medium\"]\nlow = [\"global-low\"]\n")
	writeFileAt(t, hostPath, "[palace.scoring.rooms.general]\nhigh = [\"host-high\"]\n")

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	room := cfg.PalaceScoringOverrides["general"]
	if len(room.High) != 1 || room.High[0] != "host-high" {
		t.Errorf("high = %v, want [host-high]", room.High)
	}
	if len(room.Medium) != 0 || len(room.Low) != 0 {
		t.Errorf("medium=%v low=%v, want both empty: a host-local room replaces the room, it does not patch tiers",
			room.Medium, room.Low)
	}
}

// A room the host-local file does not name is left to the layers below.
func TestLoadConfigHostLocalLeavesOtherRoomsAlone(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj",
		"[palace.scoring.rooms.general]\nhigh = [\"global-general\"]\n\n[palace.scoring.rooms.other]\nhigh = [\"global-other\"]\n")
	writeFileAt(t, hostPath, "[palace.scoring.rooms.general]\nhigh = [\"host-general\"]\n")

	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.PalaceScoringOverrides["other"].High; len(got) != 1 || got[0] != "global-other" {
		t.Errorf("other.high = %v, want [global-other]: an unnamed room is not touched", got)
	}
}

// min_score is in the allow-list, and absent must not read as zero.
func TestLoadConfigHostLocalMinScore(t *testing.T) {
	t.Run("host-local value wins", func(t *testing.T) {
		v, hostPath := hostLocalEnv(t, "proj", "[palace.scoring]\nmin_score = 4.5\n")
		writeFileAt(t, hostPath, "[palace.scoring]\nmin_score = 9.5\n")
		cfg, err := v.LoadConfig("proj")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.PalaceMinScore != 9.5 {
			t.Errorf("min_score = %v, want 9.5", cfg.PalaceMinScore)
		}
	})
	t.Run("absent leaves the global value alone", func(t *testing.T) {
		v, hostPath := hostLocalEnv(t, "proj", "[palace.scoring]\nmin_score = 4.5\n")
		// The host-local file exists and sets a room, but names no min_score.
		writeFileAt(t, hostPath, "[palace.scoring.rooms.general]\nhigh = [\"host-high\"]\n")
		cfg, err := v.LoadConfig("proj")
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.PalaceMinScore != 4.5 {
			t.Errorf("min_score = %v, want 4.5: an absent key must not resolve as 0", cfg.PalaceMinScore)
		}
	})
}

// 🔴 THE ALLOW-LIST. A host-local file carrying vault-wide or machine-wide keys
// changes none of them, and each is reported once as ignored.
func TestLoadConfigHostLocalCannotSetNonProjectKeys(t *testing.T) {
	const global = `
http_port = 9001
log_level = "warn"
vault_path = "/global-vault-path"

[embedder]
model = "global-model"
`
	v, hostPath := hostLocalEnv(t, "proj", global)

	before, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig (no host-local file): %v", err)
	}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	writeFileAt(t, hostPath, `
http_port = 1234
log_level = "debug"
vault_path = "/host-local-path"

[embedder]
model = "host-local-model"

[palace.scoring.rooms.general]
high = ["host-high"]
`)
	after, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig (with host-local file): %v", err)
	}

	if after.HTTPPort != before.HTTPPort || after.HTTPPort != 9001 {
		t.Errorf("HTTPPort = %d, want %d unchanged", after.HTTPPort, before.HTTPPort)
	}
	if after.LogLevel != before.LogLevel || after.LogLevel != "warn" {
		t.Errorf("LogLevel = %q, want %q unchanged", after.LogLevel, before.LogLevel)
	}
	if after.VaultPath != before.VaultPath || after.VaultPath != "/global-vault-path" {
		t.Errorf("VaultPath = %q, want %q unchanged", after.VaultPath, before.VaultPath)
	}
	if after.EmbedderModel != before.EmbedderModel || after.EmbedderModel != "global-model" {
		t.Errorf("EmbedderModel = %q, want %q unchanged", after.EmbedderModel, before.EmbedderModel)
	}
	// The one key it MAY set still lands, so this is not passing by the file
	// being ignored wholesale.
	if got := after.PalaceScoringOverrides["general"].High; len(got) != 1 || got[0] != "host-high" {
		t.Fatalf("general.high = %v, want [host-high]: the allow-listed key must still apply", got)
	}

	logged := buf.String()
	for _, key := range []string{"http_port", "log_level", "vault_path", "embedder.model"} {
		if !strings.Contains(logged, key) {
			t.Errorf("no ignored-key warning naming %q:\n%s", key, logged)
		}
	}
	if !strings.Contains(logged, "not a per-project key") || !strings.Contains(logged, hostPath) {
		t.Errorf("warning does not carry the message and the path:\n%s", logged)
	}
}

// The [meta] block the writer lays down is decoded, so it is not reported as an
// ignored key — and it does not gate anything yet.
func TestLoadConfigHostLocalMetaIsNotReportedAsIgnored(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	writeFileAt(t, hostPath, "[meta]\nversion_major = 1\nversion_minor = 1\nkind = \"host-project\"\n\n[palace.scoring.rooms.general]\nhigh = [\"host-high\"]\n")
	if _, err := v.LoadConfig("proj"); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if strings.Contains(buf.String(), "not a per-project key") {
		t.Errorf("the file's own [meta] block was reported as an ignored key:\n%s", buf.String())
	}
}

// The vault's Projects/<slug>/config.toml is no longer a config layer: a
// scoring override left there changes nothing.
func TestVaultProjectConfigNoLongerApplies(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")
	writeFileAt(t, filepath.Join(v.Root, "Projects", "proj", "config.toml"),
		"[palace.scoring.rooms.general]\nhigh = [\"vault-high\"]\n")
	if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
		t.Fatalf("the host-local file must not exist for this test (stat err=%v)", err)
	}
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.PalaceScoringOverrides["general"].High; len(got) != 0 {
		t.Errorf("general.high = %v, want []: the vault's per-project config is no longer read", got)
	}
}

// The path: beside the global config, under projects/, and refusing a bad slug.
func TestHostProjectConfigPath(t *testing.T) {
	xdg := isolateHostConfig(t)

	got, err := HostProjectConfigPath("proj")
	if err != nil {
		t.Fatalf("HostProjectConfigPath: %v", err)
	}
	if want := filepath.Join(xdg, "vibe-palace", "projects", "proj.toml"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	// 🔴 DERIVED FROM THE GLOBAL CONFIG'S OWN RESOLUTION, not from a second
	// os.UserConfigDir call: the two tiers share one directory, so one
	// redirection moves both.
	globalPath, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if gotDir, wantDir := filepath.Dir(filepath.Dir(got)), filepath.Dir(globalPath); gotDir != wantDir {
		t.Errorf("host-local dir %q is not the global config's dir %q", gotDir, wantDir)
	}

	if _, err := HostProjectConfigPath("Not A Slug"); err == nil {
		t.Error("an invalid slug returned no error")
	}
}

// A host-local file that is not valid TOML is an error, not a silent skip.
func TestLoadConfigHostLocalMalformedIsAnError(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")
	writeFileAt(t, hostPath, "[palace.scoring\n")
	if _, err := v.LoadConfig("proj"); err == nil {
		t.Fatal("a malformed host-local file decoded without error")
	}
}

// 🔴 LoadConfig AND ProjectConfigSources MUST AGREE ABOUT WHAT EXISTS.
//
// A dangling symlink at the host-local path is a file that is there and cannot
// be read. If the reporter used os.Stat it would follow the link, get ENOENT,
// and report "no per-project config" for a project whose every command fails on
// that same file — the diagnostic denying the problem it exists to explain.
func TestProjectConfigSourcesAgreesWithLoadConfigOnADanglingSymlink(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone.toml"), hostPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, loadErr := v.LoadConfig("proj")
	srcs, srcErr := v.ProjectConfigSources("proj")
	if srcErr != nil {
		t.Fatalf("ProjectConfigSources: %v", srcErr)
	}

	// LoadConfig cannot read it, so the reporter must not call it absent.
	if loadErr == nil {
		t.Fatal("LoadConfig read a dangling symlink without error; this test no longer probes what it claims")
	}
	if len(srcs) != 1 || srcs[0] != hostPath {
		t.Errorf("ProjectConfigSources = %v, want [%s]: LoadConfig fails on this file (%v), so the reporter must name it, not report none",
			srcs, hostPath, loadErr)
	}
}

// The vault's Projects/<slug>/config.toml is not read, so the reporter never
// names it — even when it is present — whether or not a host-local file is.
func TestProjectConfigSourcesNeverListsTheVaultFile(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")
	writeFileAt(t, filepath.Join(v.Root, "Projects", "proj", "config.toml"),
		"[palace.scoring.rooms.general]\nhigh = [\"vault-high\"]\n")

	srcs, err := v.ProjectConfigSources("proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 0 {
		t.Errorf("ProjectConfigSources = %v with only a vault file present, want none", srcs)
	}

	writeFileAt(t, hostPath, "[palace.scoring.rooms.general]\nhigh = [\"h\"]\n")

	srcs, err = v.ProjectConfigSources("proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 || srcs[0] != hostPath {
		t.Errorf("ProjectConfigSources = %v, want [%s] only", srcs, hostPath)
	}
}
