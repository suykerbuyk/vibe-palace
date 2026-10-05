// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// The ambient XDG_CONFIG_HOME RunHermetic found before it redirected the
// process, and the fixture directory it redirected to. fixtureXDG is empty in a
// package whose TestMain does not call RunHermetic.
var (
	ambientXDG    string
	ambientXDGSet bool
	fixtureXDG    string
)

// RunHermetic is the whole body of a package's TestMain:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.RunHermetic(m)) }
//
// It points XDG_CONFIG_HOME at the checked-in fixture under
// testdata/xdg, so a test that does not isolate its own XDG reads that fixture
// (git_enabled = true, no vault_path) instead of the developer's real host
// config. Without it, a host whose real config says git_enabled = false fails
// every package that commits to a test vault, and every other host setting
// (embedder, log level, vault_path) leaks into the suite.
//
// It is the package-level counterpart of testinfra.IsolateEnv's per-test
// isolation, and it lives here rather than in testinfra because this package is
// a leaf: internal/storage and internal/tools cannot import testinfra, which
// imports them. The Setenv happens in this package, so the envIsolationBypass
// source-audit rule, which scopes on internal/integration's own files, stays
// at zero findings when that package's TestMain calls this.
//
// The fixture holds two files. vibe-palace/config.toml is the host config the
// suite reads. go/telemetry/mode says "off": the Go toolchain keeps its
// telemetry counters under os.UserConfigDir, so without it every test that
// spawns `go build` (testinfra's CLI builder, the version-stamp test) would
// write counter files into the checked-in fixture. GOTELEMETRY=off in the
// environment does not stop that; only the mode file does.
//
// It also makes git hermetic, so a test commits on a CI runner with no git
// config exactly as it does on a developer's host. GIT_CONFIG_GLOBAL points at
// the fixture's git/config, which holds the suite's one test identity;
// GIT_CONFIG_NOSYSTEM=1 drops the host's system config; and an ambient
// GIT_AUTHOR_*/GIT_COMMITTER_*/EMAIL is unset. So no test inherits the host's
// identity, signing or autocrlf settings: a test that passed only because the
// developer's ~/.gitconfig had a user.email fails here too. A config FILE, not
// identity env vars: a repository's own user.email still wins over it, and a
// test that wants no identity (or a global config of its own) sets
// GIT_CONFIG_GLOBAL itself. A test that writes `git config --global` writes
// into the fixture and fails the hash check below.
//
// The fixture is read-only. RunHermetic hashes the whole fixture tree before
// and after m.Run() and fails the package, naming every added, removed or
// changed entry, if a test wrote into it — the signature of a test that writes
// host config without isolating XDG first.
func RunHermetic(m *testing.M) int {
	ambientXDG, ambientXDGSet = os.LookupEnv("XDG_CONFIG_HOME")

	dir, err := fixtureDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil.RunHermetic: %v\n", err)
		return 2
	}
	before, err := hashTree(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil.RunHermetic: hash fixture %s: %v\n", dir, err)
		return 2
	}
	if err := os.Setenv("XDG_CONFIG_HOME", dir); err != nil {
		fmt.Fprintf(os.Stderr, "testutil.RunHermetic: set XDG_CONFIG_HOME: %v\n", err)
		return 2
	}
	fixtureXDG = dir
	if err := hermeticGit(dir); err != nil {
		fmt.Fprintf(os.Stderr, "testutil.RunHermetic: %v\n", err)
		return 2
	}

	code := m.Run()

	after, err := hashTree(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testutil.RunHermetic: re-hash fixture %s: %v\n", dir, err)
		return failed(code)
	}
	if changes := diffTrees(before, after); len(changes) > 0 {
		fmt.Fprintf(os.Stderr, "testutil.RunHermetic: a test wrote into the read-only host-config fixture %s "+
			"(isolate XDG_CONFIG_HOME with initTestEnv or testinfra.IsolateEnv before writing host config):\n", dir)
		for _, c := range changes {
			fmt.Fprintf(os.Stderr, "  %s\n", c)
		}
		return failed(code)
	}
	return code
}

// UseAmbientConfig restores, for the calling test only, the XDG_CONFIG_HOME
// the process had before RunHermetic redirected it. It is for the live-vault
// canaries, which exist to measure the host's real vault through the host's
// real config. t.Setenv undoes it when the test ends, so no other test sees the
// live vault. An ambient value that was unset is restored as empty, which
// os.UserConfigDir treats the same way. In a package that does not call
// RunHermetic it does nothing.
func UseAmbientConfig(t *testing.T) {
	t.Helper()
	if fixtureXDG == "" {
		return
	}
	value := ""
	if ambientXDGSet {
		value = ambientXDG
	}
	t.Setenv("XDG_CONFIG_HOME", value)
}

// XDGIsFixture reports whether XDG_CONFIG_HOME currently points at the
// RunHermetic fixture. A live-vault canary checks it in its "no vault
// configured" skip branch: finding itself on the fixture means the restore
// was lost, which must fail rather than skip.
func XDGIsFixture() bool {
	return fixtureXDG != "" && os.Getenv("XDG_CONFIG_HOME") == fixtureXDG
}

// gitIdentityEnv is every variable git reads an identity from ahead of its
// config files. hermeticGit unsets them so the fixture identity is the one in
// effect.
var gitIdentityEnv = []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"}

// hermeticGit points git at the fixture's global config and away from the
// host's (see RunHermetic).
func hermeticGit(fixture string) error {
	global := filepath.Join(fixture, "git", "config")
	if _, err := os.Stat(global); err != nil {
		return fmt.Errorf("git identity fixture missing: %w", err)
	}
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": global, "GIT_CONFIG_NOSYSTEM": "1"} {
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set %s: %w", k, err)
		}
	}
	for _, k := range gitIdentityEnv {
		if err := os.Unsetenv(k); err != nil {
			return fmt.Errorf("unset %s: %w", k, err)
		}
	}
	return nil
}

func failed(code int) int {
	if code == 0 {
		return 1
	}
	return code
}

// fixtureDir locates testdata/xdg next to this source file, so no calling
// package resolves a path of its own.
func fixtureDir() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate testutil's source file")
	}
	dir := filepath.Join(filepath.Dir(file), "testdata", "xdg")
	cfg := filepath.Join(dir, "vibe-palace", "config.toml")
	if _, err := os.Stat(cfg); err != nil {
		return "", fmt.Errorf("host-config fixture missing: %w", err)
	}
	return dir, nil
}

// hashTree maps every entry under root, by slash-separated relative path, to
// its mode and (for a regular file) the sha256 of its bytes.
func hashTree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sig := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			sig += " " + hex.EncodeToString(sum[:])
		}
		out[filepath.ToSlash(rel)] = sig
		return nil
	})
	return out, err
}

// diffTrees names every entry added, removed or changed between two hashTree
// results, sorted.
func diffTrees(before, after map[string]string) []string {
	var changes []string
	for rel, sig := range before {
		now, ok := after[rel]
		switch {
		case !ok:
			changes = append(changes, "removed: "+rel)
		case now != sig:
			changes = append(changes, "changed: "+rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			changes = append(changes, "added: "+rel)
		}
	}
	sort.Strings(changes)
	return changes
}
