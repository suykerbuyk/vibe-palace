// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// TestMain runs this package hermetically: XDG_CONFIG_HOME points at the
// checked-in host-config fixture, never the developer's real host config.
func TestMain(m *testing.M) { os.Exit(testutil.RunHermetic(m)) }

// TestIsolateEnvCannotResolveOutsideFixture is the negative control the
// task's Verification section asks for: a test that sets no environment of
// its own beyond calling IsolateEnv must not be able to resolve anything
// under the real host HOME/XDG_CONFIG_HOME — proving the fixture actually
// isolates rather than merely appearing to.
//
// os/user.Current().HomeDir reads the passwd/NSS database directly, ignoring
// $HOME, so it is independent ground truth that t.Setenv cannot taint. This
// assumes a cgo-backed build, true for this repo's actual test job today
// per .github/workflows/ci.yml's `go test -short -race -cover ./...` (no
// CGO_ENABLED=0), unlike the separate binary-build job.
func TestIsolateEnvCannotResolveOutsideFixture(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skip("cannot determine the real host home via os/user; this negative control needs independent ground truth")
	}
	realHome := u.HomeDir

	env := IsolateEnv(t)

	if env.Home == realHome {
		t.Fatalf("IsolateEnv's Home %q collided with the real host home", env.Home)
	}

	if got, herr := os.UserHomeDir(); herr != nil || got != env.Home {
		t.Fatalf("os.UserHomeDir() = %q, %v; want %q (the isolated Home, not the real host home)", got, herr, env.Home)
	}

	// The global config file path is where every un-isolated caller would
	// look; it must be rooted under the isolated XDG_CONFIG_HOME and must
	// never mention the real host home.
	cfgPath, cerr := storage.VaultConfigFilePath()
	if cerr != nil {
		t.Fatalf("VaultConfigFilePath: %v", cerr)
	}
	if !strings.HasPrefix(cfgPath, env.XDGConfigHome) {
		t.Fatalf("VaultConfigFilePath = %q, want a path under the isolated XDG_CONFIG_HOME %q", cfgPath, env.XDGConfigHome)
	}
	if strings.Contains(cfgPath, realHome) {
		t.Fatalf("VaultConfigFilePath leaked the real host home: %q", cfgPath)
	}

	// No global config was written, so resolving from a fresh cwd under the
	// isolated Home must fail (or, if it ever resolves, resolve strictly
	// inside the isolated tree) — never fall through to anything under the
	// real host home.
	cwd := filepath.Join(env.Home, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	path, source, rerr := storage.ResolveVaultPath(cwd)
	switch {
	case rerr != nil:
		if strings.Contains(rerr.Error(), realHome) {
			t.Fatalf("ResolveVaultPath error leaked the real host home: %v", rerr)
		}
	default:
		if !strings.HasPrefix(path, env.Home) {
			t.Fatalf("ResolveVaultPath resolved %q (source %q) outside the isolated fixture", path, source)
		}
	}
}
