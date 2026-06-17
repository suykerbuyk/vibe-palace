// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIntegration_VaultTidySweep proves the full `vault tidy` stack
// (CLI → storage.TidyVault → git) against a REAL `vp` binary and a REAL git
// vault: it commits ONLY classified capture artifacts and leaves all other dirt
// untracked (reported, never `git add -A`). The motivating H2 case is asserted:
// a stray, untracked `Projects/p/.surface` is reported, NOT swept.
func TestIntegration_VaultTidySweep(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary against a git vault; skipped under -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	bin := buildVPBinary(t)

	// Isolated HOME + XDG so the child `vp` resolves our temp vault via the
	// global config, exactly as a real install would.
	home := t.TempDir()
	xdg := filepath.Join(home, ".config")
	if err := os.MkdirAll(filepath.Join(xdg, "vibe-palace"), 0o755); err != nil {
		t.Fatal(err)
	}
	vaultRoot := filepath.Join(home, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `vault_path = "` + vaultRoot + `"` + "\ngit_enabled = true\n"
	if err := os.WriteFile(filepath.Join(xdg, "vibe-palace", "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	// Real git vault.
	run(t, vaultRoot, "git", "init", "-q")
	run(t, vaultRoot, "git", "config", "user.email", "test@example.com")
	run(t, vaultRoot, "git", "config", "user.name", "Test")

	// Seed a committed .surface so its later modification shows as TRACKED (" M").
	writeFile(t, filepath.Join(vaultRoot, "Projects/demo/.surface"), "v1\n")
	run(t, vaultRoot, "git", "add", "Projects/demo/.surface")
	run(t, vaultRoot, "git", "commit", "-q", "-m", "seed surface")

	// --- Swept: capture artifacts ---
	// Tracked modification of .surface (hook stamp churn).
	writeFile(t, filepath.Join(vaultRoot, "Projects/demo/.surface"), "v2\n")
	// Untracked capture artifacts.
	writeFile(t, filepath.Join(vaultRoot, "Projects/demo/sessions/2026-06-17.md"), "session\n")
	writeFile(t, filepath.Join(vaultRoot, "Projects/demo/transcripts/x.manifest.json"), "{}\n")
	writeFile(t, filepath.Join(vaultRoot, "Projects/demo/transcripts/x.jsonl.zst"), "zst\n")
	writeFile(t, filepath.Join(vaultRoot, "palace/demo/kg/entities.jsonl"), "{}\n")
	// DEEP triples path (nests under a source-derived subpath incl. .claude).
	writeFile(t, filepath.Join(vaultRoot, "palace/demo/kg/triples/.claude/plans/foo.md--m--1.json"), "{}\n")
	// DEEP drawers path.
	writeFile(t, filepath.Join(vaultRoot, "palace/demo/drawers/demo/api/drawers.jsonl"), "{}\n")

	// --- Reported: non-artifact dirt, must never be staged ---
	writeFile(t, filepath.Join(vaultRoot, "Projects/demo/resume.md"), "resume\n")
	writeFile(t, filepath.Join(vaultRoot, "Projects/p/config.toml"), "stray\n")
	// H2: an untracked .surface under a stray scaffold must be REPORTED, not swept.
	writeFile(t, filepath.Join(vaultRoot, "Projects/p/.surface"), "stray-stamp\n")

	childEnv := append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+xdg)

	sweptArtifacts := []string{
		"Projects/demo/.surface",
		"Projects/demo/sessions/2026-06-17.md",
		"Projects/demo/transcripts/x.manifest.json",
		"Projects/demo/transcripts/x.jsonl.zst",
		"palace/demo/kg/entities.jsonl",
		"palace/demo/kg/triples/.claude/plans/foo.md--m--1.json",
		"palace/demo/drawers/demo/api/drawers.jsonl",
	}
	reportedDirt := []string{
		"Projects/demo/resume.md",
		"Projects/p/config.toml",
		"Projects/p/.surface",
	}

	// 1) Dry-run: classifies, commits nothing.
	dry := runVPTidy(t, bin, home, childEnv, "vault", "tidy", "--dry-run")
	sweep, report := splitTidyOutput(t, dry)
	for _, p := range sweptArtifacts {
		if !strings.Contains(sweep, p) {
			t.Errorf("dry-run: %q should be in the sweep list\n--- output ---\n%s", p, dry)
		}
	}
	for _, p := range reportedDirt {
		if !strings.Contains(report, p) {
			t.Errorf("dry-run: %q should be in the reported list\n--- output ---\n%s", p, dry)
		}
		if strings.Contains(sweep, p) {
			t.Errorf("dry-run: %q must NOT be swept (H2/never-add-A)\n--- output ---\n%s", p, dry)
		}
	}

	// Dry-run created no commit: HEAD unchanged from the seed.
	if headSubject(t, vaultRoot) != "seed surface" {
		t.Fatalf("dry-run must not commit; HEAD subject = %q", headSubject(t, vaultRoot))
	}

	// 2) Real sweep, local-only (no remotes configured anyway).
	runVPTidy(t, bin, home, childEnv, "vault", "tidy", "--no-push")

	// A tidy commit now sits at HEAD.
	if got := headSubject(t, vaultRoot); !strings.HasPrefix(got, "vault tidy:") {
		t.Fatalf("expected a tidy commit at HEAD, got subject %q", got)
	}

	// Every artifact is committed → absent from the dirty tree.
	porcelain := gitPorcelain(t, vaultRoot)
	for _, p := range sweptArtifacts {
		if strings.Contains(porcelain, p) {
			t.Errorf("artifact %q should be committed (clean), still dirty:\n%s", p, porcelain)
		}
	}
	// Non-artifact dirt is untouched → still present in the dirty tree.
	for _, p := range reportedDirt {
		if !strings.Contains(porcelain, p) {
			t.Errorf("reported dirt %q must remain uncommitted/untracked:\n%s", p, porcelain)
		}
	}
}

// runVPTidy runs the vp binary with the given env and cwd, failing on a non-zero
// exit. Returns combined stdout+stderr.
func runVPTidy(t *testing.T, bin, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vp %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// splitTidyOutput splits dry-run output into the "Would sweep" and "Reported"
// halves so each can be asserted independently.
func splitTidyOutput(t *testing.T, out string) (sweep, report string) {
	t.Helper()
	idx := strings.Index(out, "Reported")
	if idx < 0 {
		t.Fatalf("dry-run output missing a Reported section:\n%s", out)
	}
	return out[:idx], out[idx:]
}

func headSubject(t *testing.T, vault string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", vault, "log", "-1", "--pretty=%s").Output()
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func gitPorcelain(t *testing.T, vault string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", vault, "status", "--porcelain", "-uall").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	return string(out)
}
