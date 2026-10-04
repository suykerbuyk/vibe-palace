// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// assertBornMigrated checks the commit sha in repo holds the migration
// marker, both derived-index ignore lines, and Audits/.surface stamped at
// surface.MCPSurfaceVersion. The check is symbolic, so it holds at 8 now and
// at 9 after the migration child bumps the constant.
func assertBornMigrated(t *testing.T, repo, sha string) {
	t.Helper()
	m, err := surface.ParseVaultManifest([]byte(gitRun(t, repo, "show", sha+":.vibe-palace/vault.toml")))
	if err != nil {
		t.Fatalf("manifest at %s: %v", sha, err)
	}
	if m.AuthoredOnly == "" || m.Format != surface.RequiredDataFormat {
		t.Errorf("manifest at %s = %+v, want the marker beside format %d", sha, m, surface.RequiredDataFormat)
	}
	ignore := "\n" + gitRun(t, repo, "show", sha+":.gitignore") + "\n"
	for _, l := range MigratedVaultGitignorePatterns {
		if !strings.Contains(ignore, "\n"+l+"\n") {
			t.Errorf(".gitignore at %s lacks %q", sha, l)
		}
	}
	stamp := gitRun(t, repo, "show", sha+":Audits/.surface")
	var s surface.Stamp
	if _, err := toml.Decode(stamp, &s); err != nil || s.Surface != surface.MCPSurfaceVersion {
		t.Errorf("Audits/.surface at %s = %q (%v), want surface = %d", sha, stamp, err, surface.MCPSurfaceVersion)
	}
}

// v9 `vault init` is born migrated: the vault's first commit holds the
// marker, the two lines and the surface stamp. Mutant: a stampless or
// unmarked new vault; a literal 9.
func TestInitVault_BornMigrated(t *testing.T) {
	initEnv(t)
	origin, github := twoEmptyRemotes(t)
	path := filepath.Join(t.TempDir(), "v")
	rep, err := InitVault(context.Background(), initReq(path, origin, github))
	if err != nil {
		t.Fatal(err)
	}
	assertBornMigrated(t, path, rep.Commit)
	if migrated, err := VaultMigrated(path); err != nil || !migrated {
		t.Errorf("VaultMigrated = %v, %v", migrated, err)
	}
	if err := surface.CheckCompatible(path); err != nil {
		t.Errorf("the new vault refuses the binary that created it: %v", err)
	}
	if st := gitRun(t, path, "status", "--porcelain"); st != "" {
		t.Errorf("vault not clean: %q", st)
	}
}

// Resumed `vault init` is born migrated, through both existing seams. (a)
// Stopped after the lifecycle marker: the re-run restarts through initFresh.
// (b) Stopped after the commit, with a remote already holding it: the re-run
// publishes the commit initFresh made. Mutant: the writes placed after the
// commit (initPublish), or only on a first attempt.
func TestInitVault_ResumedIsBornMigrated(t *testing.T) {
	t.Run("stopped after the marker", func(t *testing.T) {
		initEnv(t)
		origin, github := twoEmptyRemotes(t)
		path := filepath.Join(t.TempDir(), "v")
		seamInit(t, &initAfterMarker, func() error { return errKilledInit })
		req := initReq(path, origin, github)
		if _, err := InitVault(context.Background(), req); !errors.Is(err, errKilledInit) {
			t.Fatalf("err = %v", err)
		}
		initAfterMarker = func() error { return nil }
		rep, err := InitVault(context.Background(), req)
		if err != nil {
			t.Fatalf("re-run: %v", err)
		}
		assertBornMigrated(t, origin, rep.Commit)
	})
	t.Run("stopped after the commit", func(t *testing.T) {
		initEnv(t)
		origin, github := twoEmptyRemotes(t)
		path := filepath.Join(t.TempDir(), "v")
		seamInit(t, &initAfterCommit, func() error { return errKilledInit })
		req := initReq(path, origin, github)
		if _, err := InitVault(context.Background(), req); !errors.Is(err, errKilledInit) {
			t.Fatalf("err = %v", err)
		}
		initAfterCommit = func() error { return nil }
		rep, err := InitVault(context.Background(), req)
		if err != nil {
			t.Fatalf("re-run: %v", err)
		}
		assertBornMigrated(t, origin, rep.Commit)
		assertBornMigrated(t, github, rep.Commit)
	})
}
