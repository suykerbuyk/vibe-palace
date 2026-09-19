// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testutil

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestFixtureDirResolvesTheCheckedInConfig pins the fixture RunHermetic points
// every hermetic package at: it exists next to this package's source, enables
// git, carries a [meta] block at the current schema version, and names no
// vault.
func TestFixtureDirResolvesTheCheckedInConfig(t *testing.T) {
	dir, err := fixtureDir()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("fixtureDir = %q, want an absolute path", dir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "vibe-palace", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{"git_enabled = true", "[meta]", "version_major = 1", "version_minor = 1"} {
		if !strings.Contains(body, want) {
			t.Errorf("fixture config lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "\nvault_path") {
		t.Errorf("fixture config must not name a vault_path:\n%s", body)
	}
	mode, err := os.ReadFile(filepath.Join(dir, "go", "telemetry", "mode"))
	if err != nil {
		t.Fatalf("the fixture must switch Go telemetry off, or every `go build` a test spawns writes into it: %v", err)
	}
	if strings.TrimSpace(string(mode)) != "off" {
		t.Errorf("go/telemetry/mode = %q, want off", mode)
	}
}

// TestHashTreeReportsEveryKindOfChange is the fixture write guard's detector:
// an added, a removed and a changed file must each be named.
func TestHashTreeReportsEveryKindOfChange(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vibe-palace/config.toml", "git_enabled = true\n")
	write("vibe-palace/keep.toml", "x\n")
	write("vibe-palace/gone.toml", "y\n")

	before, err := hashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := diffTrees(before, before); len(got) != 0 {
		t.Fatalf("an unchanged tree reported changes: %v", got)
	}

	write("vibe-palace/config.toml", "git_enabled = false\n")
	if err := os.Remove(filepath.Join(root, "vibe-palace", "gone.toml")); err != nil {
		t.Fatal(err)
	}
	write("vibe-palace/projects/x.toml", "z\n")

	after, err := hashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	got := diffTrees(before, after)
	want := []string{
		"added: vibe-palace/projects",
		"added: vibe-palace/projects/x.toml",
		"changed: vibe-palace/config.toml",
		"removed: vibe-palace/gone.toml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diffTrees = %v\nwant       %v", got, want)
	}
}

// swapHermeticState installs a redirect state for one test and restores the
// package's real one afterwards.
func swapHermeticState(t *testing.T, fixture, ambient string, ambientSet bool) {
	t.Helper()
	oldFixture, oldAmbient, oldSet := fixtureXDG, ambientXDG, ambientXDGSet
	fixtureXDG, ambientXDG, ambientXDGSet = fixture, ambient, ambientSet
	t.Cleanup(func() { fixtureXDG, ambientXDG, ambientXDGSet = oldFixture, oldAmbient, oldSet })
}

// TestUseAmbientConfigRestoresForOneTestOnly: the canary sees the ambient
// value, and the fixture comes back when the canary's test ends.
func TestUseAmbientConfigRestoresForOneTestOnly(t *testing.T) {
	fixture := t.TempDir()
	ambient := t.TempDir()
	swapHermeticState(t, fixture, ambient, true)
	t.Setenv("XDG_CONFIG_HOME", fixture)

	if !XDGIsFixture() {
		t.Fatal("XDGIsFixture = false while XDG_CONFIG_HOME is the fixture")
	}
	t.Run("canary", func(t *testing.T) {
		UseAmbientConfig(t)
		if got := os.Getenv("XDG_CONFIG_HOME"); got != ambient {
			t.Errorf("inside the canary XDG_CONFIG_HOME = %q, want the ambient %q", got, ambient)
		}
		if XDGIsFixture() {
			t.Error("XDGIsFixture = true after UseAmbientConfig")
		}
	})
	if got := os.Getenv("XDG_CONFIG_HOME"); got != fixture {
		t.Errorf("after the canary XDG_CONFIG_HOME = %q, want the fixture %q back", got, fixture)
	}
}

// TestUseAmbientConfigRestoresAnUnsetAmbientAsEmpty: an ambient XDG that was
// unset comes back as empty, which os.UserConfigDir treats as unset.
func TestUseAmbientConfigRestoresAnUnsetAmbientAsEmpty(t *testing.T) {
	fixture := t.TempDir()
	swapHermeticState(t, fixture, "", false)
	t.Setenv("XDG_CONFIG_HOME", fixture)

	t.Run("canary", func(t *testing.T) {
		UseAmbientConfig(t)
		if got := os.Getenv("XDG_CONFIG_HOME"); got != "" {
			t.Errorf("XDG_CONFIG_HOME = %q, want empty", got)
		}
	})
}

// TestHermeticHelpersAreInertWithoutRunHermetic: in a package that never
// redirected, neither helper touches the environment.
func TestHermeticHelpersAreInertWithoutRunHermetic(t *testing.T) {
	swapHermeticState(t, "", "", false)
	t.Setenv("XDG_CONFIG_HOME", "/somewhere")
	t.Run("canary", func(t *testing.T) {
		UseAmbientConfig(t)
		if got := os.Getenv("XDG_CONFIG_HOME"); got != "/somewhere" {
			t.Errorf("XDG_CONFIG_HOME = %q, want it untouched", got)
		}
	})
	if XDGIsFixture() {
		t.Error("XDGIsFixture = true with no redirect in place")
	}
}

// TestFixtureReadsEnabledWithoutAMetaWarning: every hermetic package reads the
// fixture through storage.HostGitEnabled (and LoadConfig's host layer). Without
// a current [meta] block, checkConfigVersion would log "config has no [meta]
// block; run 'vp config upgrade'" against a checked-in test fixture on every
// run.
func TestFixtureReadsEnabledWithoutAMetaWarning(t *testing.T) {
	dir, err := fixtureDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	enabled, err := storage.HostGitEnabled()
	if err != nil || !enabled {
		t.Fatalf("HostGitEnabled on the fixture = %v, %v; want true, nil", enabled, err)
	}
	if strings.Contains(buf.String(), "[meta]") {
		t.Errorf("reading the fixture logged a [meta] warning:\n%s", buf.String())
	}
}
