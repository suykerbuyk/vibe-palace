// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// writeCoreFile writes one of the two scoped files for a project.
func writeCoreFile(t *testing.T, vault, project, file, body string) {
	t.Helper()
	dir := filepath.Join(vault, "Projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestVaultAbsPathBreaches_TruePositives pins the host-rooted forms that MUST
// be flagged. Each is a fact about one machine committed into a file synced to
// every machine.
func TestVaultAbsPathBreaches_TruePositives(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"linux home", "Vault lives at /home/johns/vibe-palace-vault today.", "/home/johns/vibe-palace-vault"},
		{"macos users", "See /Users/jane/code/thing for the fixture.", "/Users/jane/code/thing"},
		{"wsl mnt", "Windows side is /mnt/c/Users/johns/vault.", "/mnt/c/Users/johns/vault"},
		{"root home", "Daemon reads /root/.config/vp/config.toml.", "/root/.config/vp/config.toml"},
		{"windows drive", `Checkout is at C:\Users\johns\code.`, `C:\Users\johns\code`},
		{"windows extended", `Long path \\?\C:\very\long\path here.`, `\\?\C:\very\long\path`},
		{"inline code span is not an exemption", "Vault at `/home/johns/vault` right now.", "/home/johns/vault"},
		{"start of line", "/home/johns/x is the root.", "/home/johns/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := vaultAbsPathBreaches("resume.md", []byte(tc.line))
			if len(got) != 1 {
				t.Fatalf("got %d breaches %v, want exactly 1", len(got), got)
			}
			if !strings.HasSuffix(got[0], tc.want) {
				t.Errorf("breach = %q, want it to report %q", got[0], tc.want)
			}
			if !strings.HasPrefix(got[0], "resume.md line 1:") {
				t.Errorf("breach = %q, want the file and 1-indexed line", got[0])
			}
		})
	}
}

// TestVaultAbsPathBreaches_FalsePositives is THE test in this file.
//
// A check that cries wolf gets ignored, and then it is worth less than nothing
// — it converts a real signal into noise an operator learns to skim. Every case
// here is text that appears, or plausibly appears, in a real resume.md or
// workflow.md and must stay silent.
func TestVaultAbsPathBreaches_FalsePositives(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		// Machine-independent absolutes — the whole reason this matches an
		// allowlist of roots instead of "any leading slash".
		{"proc", "The check is `readlink /proc/<pid>/exe` for every running server."},
		{"usr", "Installed under /usr/local/bin on some hosts."},
		{"etc", "Config precedence starts at /etc/vibe-palace/config.toml."},
		{"tmp", "Scratch goes to /tmp/vp-scratch."},
		{"var", "Logs may land in /var/log/vp.log."},

		// Tilde paths are host CONVENTIONS and resolve everywhere.
		{"tilde bin", "Install: `make install` (→ `~/.local/bin/vp`)."},
		{"tilde config", "`vault_path` in `~/.config/vibe-palace/config.toml`."},
		{"tilde claude", "Claude memory sits at `~/.claude/projects/<encoded>/memory/`."},

		// Repo-relative paths have no root at all.
		{"repo relative", "See internal/storage/paths.go:348 for the resolver."},
		{"repo relative with dir", "Edit internal/templates/templates/commands/wrap.md only."},

		// Prose that merely NAMES the concept.
		{"prose home", "Never place a .vibe-palace.toml at $HOME."},
		{"prose root", "The vault root is resolved at startup."},
		{"bare drive letter", "Option C: the drive is remapped."},

		// A URL is not a filesystem path.
		{"url", "Remote is github.com:suykerbuyk/vibe-palace-vault."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vaultAbsPathBreaches("resume.md", []byte(tc.line)); len(got) != 0 {
				t.Errorf("false positive on %q: %v", tc.line, got)
			}
		})
	}
}

// TestVaultAbsPathBreaches_FenceAware pins that a path inside a fenced block is
// a documented sample, not a live assertion. The mdfence scanner is shared with
// resume-caps and resume-refs precisely so an inline code run is never misread
// as an opening fence.
func TestVaultAbsPathBreaches_FenceAware(t *testing.T) {
	body := "Before.\n\n```sh\ncd /home/johns/vibe-palace-vault\n```\n\nAfter.\n"
	if got := vaultAbsPathBreaches("resume.md", []byte(body)); len(got) != 0 {
		t.Errorf("fenced sample was flagged: %v", got)
	}

	// ...but the fence must not swallow the rest of the file.
	body2 := "```sh\ncd /home/a/b\n```\n\nLive: /home/johns/real-vault\n"
	got := vaultAbsPathBreaches("resume.md", []byte(body2))
	if len(got) != 1 {
		t.Fatalf("got %d breaches %v, want 1 (the unfenced one only)", len(got), got)
	}
	if !strings.Contains(got[0], "/home/johns/real-vault") {
		t.Errorf("breach = %q, want the unfenced path", got[0])
	}
	if !strings.Contains(got[0], "line 5") {
		t.Errorf("breach = %q, want line 5", got[0])
	}
}

// TestCheckVaultAbsPaths_MutationAgainstIter188 pins BOTH directions against the
// real bug this check was commissioned for: the exact line iter 188 removed must
// be flagged, and the replacement text that now stands in its place must not be.
//
// One direction alone is worthless. A check that flags the bug but also flags
// the fix teaches operators to ignore it; a check that stays quiet on both never
// had to work at all.
func TestCheckVaultAbsPaths_MutationAgainstIter188(t *testing.T) {
	// The literal line 188 deleted.
	t.Run("flags the bug", func(t *testing.T) {
		vault := t.TempDir()
		writeCoreFile(t, vault, "p", "resume.md",
			"# p\n\n## Current State\n\n- Vault location: /home/johns/vibe-palace-vault (WSL-local ext4)\n")
		r := CheckVaultAbsPaths(storage.NewVault(vault))
		if r.Status != Info {
			t.Fatalf("status = %v (%s), want Info — the 188 bug must be detected", r.Status, r.Summary)
		}
		joined := strings.Join(r.Details, "\n")
		if !strings.Contains(joined, "/home/johns/vibe-palace-vault") {
			t.Errorf("details never name the offending path:\n%s", joined)
		}
	})

	// The text that replaced it: resolve-don't-recall, tilde config paths, and
	// the constraint rather than the path.
	t.Run("does not flag the fix", func(t *testing.T) {
		vault := t.TempDir()
		writeCoreFile(t, vault, "p", "resume.md",
			"# p\n\n## Current State\n\n"+
				"- **RESOLVE, DON'T RECALL:** `vault_path` in `~/.config/vibe-palace/config.toml`\n"+
				"  (per-tree override: `.vibe-palace.toml`); `vp status` prints what the binary resolved.\n"+
				"  Write the **constraint** (POSIX filesystem, never NTFS/exFAT), never the **path**.\n"+
				"- The check is one command: `readlink /proc/<pid>/exe` for every `pgrep -f \"vp mcp\"`.\n")
		r := CheckVaultAbsPaths(storage.NewVault(vault))
		if r.Status != Pass {
			t.Fatalf("status = %v, want Pass — the FIX must not be flagged. details:\n%s",
				r.Status, strings.Join(r.Details, "\n"))
		}
	})
}

// TestCheckVaultAbsPaths_ScansWorkflowMd pins that scope is the ADR-009 core —
// BOTH files — not resume.md alone. workflow.md is bootstrap-inlined and read as
// current truth exactly like resume.md.
func TestCheckVaultAbsPaths_ScansWorkflowMd(t *testing.T) {
	vault := t.TempDir()
	writeCoreFile(t, vault, "p", "resume.md", "# p\n\nClean.\n")
	writeCoreFile(t, vault, "p", "workflow.md", "# p workflow\n\nRun from /home/johns/code/p.\n")

	r := CheckVaultAbsPaths(storage.NewVault(vault))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info — workflow.md is in scope", r.Status)
	}
	joined := strings.Join(r.Details, "\n")
	if !strings.Contains(joined, "workflow.md line 3") {
		t.Errorf("details must attribute the breach to workflow.md:\n%s", joined)
	}
}

// TestCheckVaultAbsPaths_OutOfScopeFilesAreNotScanned pins the exclusion that
// keeps this check usable: iterations.md and task files quote host paths as
// specimens of the mistake, and flagging the record of a bug being fixed is how
// a check earns a reputation for crying wolf.
func TestCheckVaultAbsPaths_OutOfScopeFilesAreNotScanned(t *testing.T) {
	vault := t.TempDir()
	writeCoreFile(t, vault, "p", "resume.md", "# p\n\nClean.\n")
	writeCoreFile(t, vault, "p", "iterations.md",
		"## Iteration 188\n\nRemoved `Vault location: /home/johns/vibe-palace-vault`.\n")
	dir := filepath.Join(vault, "Projects", "p", "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t.md"),
		[]byte("The specimen was /home/johns/vibe-palace-vault.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := CheckVaultAbsPaths(storage.NewVault(vault))
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass — only resume.md and workflow.md are in scope. details:\n%s",
			r.Status, strings.Join(r.Details, "\n"))
	}
}

func TestCheckVaultAbsPaths_EmptyVaultRootSkips(t *testing.T) {
	r := CheckVaultAbsPaths(storage.NewVault(""))
	if r.Status != Skip {
		t.Errorf("status = %v, want Skip on an empty vault root", r.Status)
	}
	if r.Summary != "no vault configured" {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestCheckVaultAbsPaths_NoProjectsDir(t *testing.T) {
	r := CheckVaultAbsPaths(storage.NewVault(t.TempDir()))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass", r.Status)
	}
	if r.Summary != "no Projects/ directory" {
		t.Errorf("summary = %q", r.Summary)
	}
}

func TestCheckVaultAbsPaths_MissingCoreFilesAreNotScanned(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "Projects", "bare"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := CheckVaultAbsPaths(storage.NewVault(vault))
	if r.Status != Pass {
		t.Errorf("status = %v, want Pass (a project with neither file is not a violation)", r.Status)
	}
	if r.Summary != "0 project core free of host-rooted paths" {
		t.Errorf("summary = %q, want the project uncounted", r.Summary)
	}
}

// TestCheckVaultAbsPaths_ReportsPerProjectSorted pins deterministic output: the
// details are read by agents and diffed by humans, so project order cannot
// depend on directory iteration.
func TestCheckVaultAbsPaths_ReportsPerProjectSorted(t *testing.T) {
	vault := t.TempDir()
	writeCoreFile(t, vault, "zeta", "resume.md", "# z\n\n/home/a/z\n")
	writeCoreFile(t, vault, "alpha", "resume.md", "# a\n\n/home/a/a\n")

	r := CheckVaultAbsPaths(storage.NewVault(vault))
	if r.Status != Info {
		t.Fatalf("status = %v, want Info", r.Status)
	}
	if r.Summary != "2 of 2 project core carry host-rooted paths" {
		t.Errorf("summary = %q", r.Summary)
	}
	alphaAt := strings.Index(strings.Join(r.Details, "\n"), "alpha")
	zetaAt := strings.Index(strings.Join(r.Details, "\n"), "zeta")
	if alphaAt < 0 || zetaAt < 0 || alphaAt > zetaAt {
		t.Errorf("projects must be sorted; alpha at %d, zeta at %d", alphaAt, zetaAt)
	}
}

// TestCheckVaultAbsPaths_NeverWrites pins the read-only contract. This check is
// advisory detection; repairing a path is a human judgement call about what the
// document meant to say.
func TestCheckVaultAbsPaths_NeverWrites(t *testing.T) {
	vault := t.TempDir()
	writeCoreFile(t, vault, "p", "resume.md", "# p\n\n/home/johns/x\n")
	path := filepath.Join(vault, "Projects", "p", "resume.md")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	_ = CheckVaultAbsPaths(storage.NewVault(vault))

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("CheckVaultAbsPaths modified the file it scanned")
	}
}
