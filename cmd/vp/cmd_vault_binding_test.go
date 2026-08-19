// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// runVaultCmdCapturingBoth drives a vp vault subcommand and returns its stdout
// and stderr separately. Both are needed: the binding line is deliberately on
// stderr (stdout carries file bytes for `vp vault read`), and --json carries the
// same facts in band on stdout.
func runVaultCmdCapturingBoth(t *testing.T, cmd *cli.Command, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr

	code = cmd.Run(args)

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	so, _ := io.ReadAll(rOut)
	se, _ := io.ReadAll(rErr)
	return string(so), string(se), code
}

// overrideTreeAt writes a .vibe-palace.toml binding vault_path to a DIFFERENT
// vault than the global config, and chdirs into it. This is the exact shape of
// the 2026-08-16 incident: the cwd-aware family follows this file, the vault
// file-CRUD family deliberately does not, and until now neither said so.
func overrideTreeAt(t *testing.T) string {
	t.Helper()
	scratchVault := t.TempDir()
	tree := t.TempDir()
	body := "vault_path = \"" + scratchVault + "\"\n\n[project]\nname = \"scratchproj\"\n"
	if err := os.WriteFile(filepath.Join(tree, ".vibe-palace.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write override marker: %v", err)
	}
	t.Chdir(tree)
	return scratchVault
}

// TestVaultWriteNamesTheGlobalVaultItWroteTo is the acceptance gate for
// vault-file-ops-and-cwd-tools-resolve-different-vaults-silently.
//
// The incident: from a tree whose .vibe-palace.toml pointed at a scratch vault,
// `vp vault write` returned a clean {"bytes":118,"sha256":"0720cd65…"} against
// the PRODUCTION vault. The result was accurate about the bytes and silent
// about the subject, so it took a `find` to discover where the file went.
//
// Resolving globally is deliberate and stays (TestOpenVaultGlobal_IgnoresCwd-
// Override pins it). What changes is that the result now names its subject.
func TestVaultWriteNamesTheGlobalVaultItWroteTo(t *testing.T) {
	globalVault := setupTestVaultEnv(t)
	scratchVault := overrideTreeAt(t)

	// Control: the fixture must actually create the divergence, or this test
	// would pass against a machine where both families agree — measuring
	// nothing. The two vaults must be different directories.
	if globalVault == scratchVault {
		t.Fatalf("fixture is degenerate: global and cwd vault are both %q", globalVault)
	}

	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultWrite(),
		"Projects/scratchproj/resume.md", "--content", "# p\n", "--json")
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d\nstdout:%s\nstderr:%s", code, stdout, stderr)
	}

	var res vaultfs.WriteResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("decode JSON: %v\nstdout:%s", err, stdout)
	}
	if res.VaultPath != globalVault {
		t.Errorf("vault_path = %q, want the GLOBAL vault it actually wrote to (%q)", res.VaultPath, globalVault)
	}
	if res.VaultPath == scratchVault {
		t.Errorf("vault_path names the cwd-override vault %q — resolution changed; this family must stay global", scratchVault)
	}
	if !strings.HasPrefix(res.VaultPathSource, "global:") {
		t.Errorf("vault_path_source = %q, want a global:<configpath> source for this family", res.VaultPathSource)
	}
	if res.VaultPathSource == vaultfs.SourceUnknown {
		t.Errorf("vault_path_source is %q — the command resolved a source and did not pass it", vaultfs.SourceUnknown)
	}

	// The file really is in the global vault, not the scratch one: the claim
	// the output makes has to be true, not merely present.
	if _, err := os.Stat(filepath.Join(globalVault, "Projects/scratchproj/resume.md")); err != nil {
		t.Errorf("write did not land in the vault the result names: %v", err)
	}
}

// TestVaultOpsReportBindingOnStderr pins the human surface for the whole
// family. Stderr, not stdout, because `vp vault read` writes file bytes to
// stdout — a binding line there would corrupt every redirect.
func TestVaultOpsReportBindingOnStderr(t *testing.T) {
	globalVault := setupTestVaultEnv(t)
	overrideTreeAt(t)

	if _, err := vaultfs.Write(globalVault, "notes.md", "hello\n", ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cases := []struct {
		name string
		cmd  *cli.Command
		args []string
	}{
		{"read", cmdVaultRead(), []string{"notes.md"}},
		{"write", cmdVaultWrite(), []string{"w.md", "--content", "x"}},
		{"edit", cmdVaultEdit(), []string{"notes.md", "--old", "hello", "--new", "bye"}},
		{"exists", cmdVaultExists(), []string{"notes.md"}},
		{"sha256", cmdVaultSha256(), []string{"notes.md"}},
		{"move", cmdVaultMove(), []string{"notes.md", "moved.md"}},
		{"delete", cmdVaultDelete(), []string{"moved.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := runVaultCmdCapturingBoth(t, tc.cmd, tc.args...)
			if code != cli.ExitOK {
				t.Fatalf("exit code = %d\nstderr:%s", code, stderr)
			}
			want := "vault_path = " + globalVault
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr must name the vault as %q; got:\n%s", want, stderr)
			}
			if !strings.Contains(stderr, "vault_path source = global:") {
				t.Errorf("stderr must name the source; got:\n%s", stderr)
			}
			if strings.Contains(stdout, "vault_path = ") {
				t.Errorf("the binding line must NOT be on stdout — it would corrupt `vp vault read` redirects; got:\n%s", stdout)
			}
		})
	}
}

// TestVaultfsStampsThePathItWasHanded pins the by-construction property: the
// path in a result is the root vaultfs actually operated against, so it cannot
// drift from the bytes it describes. Source is unknown until a caller that
// knows how it resolved says so.
func TestVaultfsStampsThePathItWasHanded(t *testing.T) {
	root := t.TempDir()
	res, err := vaultfs.Write(root, "a.md", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.VaultPath != root {
		t.Errorf("VaultPath = %q, want the root it was handed (%q)", res.VaultPath, root)
	}
	if res.VaultPathSource != vaultfs.SourceUnknown {
		t.Errorf("VaultPathSource = %q, want %q — vaultfs cannot know how its caller resolved the root, and must not guess", res.VaultPathSource, vaultfs.SourceUnknown)
	}
}
