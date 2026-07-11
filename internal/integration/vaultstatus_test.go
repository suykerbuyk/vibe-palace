// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// vsGit runs an isolated git command against dir and returns its trimmed
// combined output, failing the test on error. The environment pins
// GIT_CONFIG_GLOBAL/SYSTEM to /dev/null so the developer's real git config never
// leaks into the hermetic fixture, and suppresses any credential prompt.
func vsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_EDITOR=true",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return string(out)
}

// vsNewVaultWithOrigin stands up a real vault git repo with a per-repo identity,
// a seed commit on main, and a bare "origin" remote that the seed has been
// pushed to (so the vault starts in-sync). It returns the vault root and the
// bare origin path.
func vsNewVaultWithOrigin(t *testing.T) (root, bare string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root = t.TempDir()
	bare = t.TempDir()

	vsGit(t, bare, "init", "--bare", "-b", "main")

	vsGit(t, root, "init", "-b", "main")
	vsGit(t, root, "config", "user.email", "test@example.com")
	vsGit(t, root, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vsGit(t, root, "add", "-A")
	vsGit(t, root, "commit", "-m", "seed")
	vsGit(t, root, "remote", "add", "origin", bare)
	vsGit(t, root, "push", "-u", "origin", "main")
	return root, bare
}

// vsAdvanceOrigin pushes a new commit to the bare origin from a throwaway clone,
// so the vault becomes "behind" without ever touching the vault's own tree.
func vsAdvanceOrigin(t *testing.T, bare string) {
	t.Helper()
	clone := t.TempDir()
	vsGit(t, ".", "clone", bare, clone)
	vsGit(t, clone, "config", "user.email", "other@example.com")
	vsGit(t, clone, "config", "user.name", "Other User")
	if err := os.WriteFile(filepath.Join(clone, "remote-change.txt"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vsGit(t, clone, "add", "-A")
	vsGit(t, clone, "commit", "-m", "advance origin")
	vsGit(t, clone, "push", "origin", "main")
}

// vsToolReport invokes the MCP vp_vault_status tool end-to-end (params marshalled
// as the real client would send them) and decodes the result back through JSON
// into the shared StatusReport schema — proving the tool and the CLI builder
// share one wire contract.
func vsToolReport(t *testing.T, vault *storage.Vault, refresh bool) storage.StatusReport {
	t.Helper()
	tool := tools.VaultStatusTool(vault)
	if tool.Name != "vp_vault_status" {
		t.Fatalf("tool name = %q, want vp_vault_status", tool.Name)
	}
	if tool.Mutating {
		t.Fatal("vp_vault_status must be non-mutating")
	}
	params, _ := json.Marshal(map[string]any{"refresh": refresh})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler(refresh=%v): %v", refresh, err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	var report storage.StatusReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("decode StatusReport: %v\n%s", err, raw)
	}
	return report
}

// originRemote returns the RemoteStatusJSON for "origin", failing if absent.
func originRemote(t *testing.T, report storage.StatusReport) storage.RemoteStatusJSON {
	t.Helper()
	for _, r := range report.Remotes {
		if r.Remote == "origin" {
			return r
		}
	}
	t.Fatalf("no origin remote in report: %+v", report.Remotes)
	return storage.RemoteStatusJSON{}
}

// TestIntegrationVaultStatus proves the read-only vault-status feature through
// the full stack against REAL git: the storage.BuildStatusReport builder and the
// MCP vp_vault_status tool agree on per-remote ahead/behind/diverged flags and on
// the working-tree dirt classification, with a bare file:// origin and fully
// isolated git config (no network).
func TestIntegrationVaultStatus(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Belt-and-suspenders: also pin the in-process git subprocesses launched by
	// BuildStatusReport so they never read the developer's real git config.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")

	t.Run("in_sync", func(t *testing.T) {
		root, _ := vsNewVaultWithOrigin(t)
		vault := storage.NewVault(root)

		report, err := storage.BuildStatusReport(root, true)
		if err != nil {
			t.Fatalf("BuildStatusReport: %v", err)
		}
		if report.Version != 1 {
			t.Errorf("Version = %d, want 1", report.Version)
		}
		if report.Branch != "main" {
			t.Errorf("Branch = %q, want main", report.Branch)
		}
		o := originRemote(t, report)
		if o.Ahead != 0 || o.Unpushed {
			t.Errorf("in-sync: Ahead=%d Unpushed=%v, want 0/false", o.Ahead, o.Unpushed)
		}
		if o.Behind != 0 || !o.BehindKnown {
			t.Errorf("in-sync: Behind=%d BehindKnown=%v, want 0/true", o.Behind, o.BehindKnown)
		}
		if o.Diverged {
			t.Error("in-sync: Diverged should be false")
		}
		if !o.Reachable {
			t.Error("in-sync: Reachable should be true after a successful fetch")
		}

		// The MCP tool must report the same in-sync state.
		to := originRemote(t, vsToolReport(t, vault, true))
		if to.Ahead != 0 || to.Behind != 0 || !to.BehindKnown || to.Diverged {
			t.Errorf("tool in-sync mismatch: %+v", to)
		}
	})

	t.Run("ahead_unpushed", func(t *testing.T) {
		root, _ := vsNewVaultWithOrigin(t)
		vault := storage.NewVault(root)

		// Local commit that is never pushed.
		if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("local\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		vsGit(t, root, "add", "-A")
		vsGit(t, root, "commit", "-m", "local only")

		o := originRemote(t, mustReport(t, root, true))
		if o.Ahead < 1 || !o.Unpushed {
			t.Errorf("Ahead=%d Unpushed=%v, want >=1 / true", o.Ahead, o.Unpushed)
		}
		if o.Behind != 0 || !o.BehindKnown {
			t.Errorf("Behind=%d BehindKnown=%v, want 0/true (origin unchanged)", o.Behind, o.BehindKnown)
		}
		if o.Diverged {
			t.Error("ahead-only must not be Diverged")
		}

		to := originRemote(t, vsToolReport(t, vault, true))
		if to.Ahead < 1 || !to.Unpushed || to.Diverged {
			t.Errorf("tool ahead mismatch: %+v", to)
		}
	})

	t.Run("behind", func(t *testing.T) {
		root, bare := vsNewVaultWithOrigin(t)
		vault := storage.NewVault(root)
		vsAdvanceOrigin(t, bare)

		// With fetch the behind count is real.
		o := originRemote(t, mustReport(t, root, true))
		if o.Behind < 1 || !o.BehindKnown {
			t.Errorf("Behind=%d BehindKnown=%v, want >=1 / true", o.Behind, o.BehindKnown)
		}
		if o.Ahead != 0 {
			t.Errorf("Ahead=%d, want 0 (vault tree untouched)", o.Ahead)
		}
		if o.Diverged {
			t.Error("behind-only must not be Diverged")
		}

		to := originRemote(t, vsToolReport(t, vault, true))
		if to.Behind < 1 || !to.BehindKnown || to.Diverged {
			t.Errorf("tool behind mismatch: %+v", to)
		}
	})

	t.Run("diverged", func(t *testing.T) {
		root, bare := vsNewVaultWithOrigin(t)
		vault := storage.NewVault(root)

		// Advance both sides: local commit AND remote commit.
		if err := os.WriteFile(filepath.Join(root, "local.txt"), []byte("local\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		vsGit(t, root, "add", "-A")
		vsGit(t, root, "commit", "-m", "local divergent")
		vsAdvanceOrigin(t, bare)

		o := originRemote(t, mustReport(t, root, true))
		if !o.Diverged {
			t.Errorf("expected Diverged, got %+v", o)
		}
		if o.Ahead < 1 || o.Behind < 1 || !o.BehindKnown {
			t.Errorf("diverged flags wrong: Ahead=%d Behind=%d BehindKnown=%v", o.Ahead, o.Behind, o.BehindKnown)
		}

		to := originRemote(t, vsToolReport(t, vault, true))
		if !to.Diverged {
			t.Errorf("tool should report Diverged: %+v", to)
		}
	})

	t.Run("no_fetch_leaves_behind_unknown", func(t *testing.T) {
		root, bare := vsNewVaultWithOrigin(t)
		vault := storage.NewVault(root)
		vsAdvanceOrigin(t, bare)

		// fetch=false: the H1 contract — a stale cached ref must NEVER be reported
		// as a known "behind 0". BehindKnown stays false even though origin moved.
		o := originRemote(t, mustReport(t, root, false))
		if o.BehindKnown {
			t.Error("fetch=false must leave BehindKnown=false")
		}
		if o.Behind != 0 {
			t.Errorf("fetch=false: Behind=%d, want 0 (cached, not real)", o.Behind)
		}

		to := originRemote(t, vsToolReport(t, vault, false))
		if to.BehindKnown {
			t.Errorf("tool refresh=false must leave BehindKnown=false: %+v", to)
		}
	})

	t.Run("dirt_classification", func(t *testing.T) {
		root, _ := vsNewVaultWithOrigin(t)

		// A capture-artifact-shaped file (session) is swept; an ordinary
		// non-artifact dirty file (resume) is reported but never swept.
		writeFile(t, filepath.Join(root, "Projects/vibe-palace/sessions/2026-06-17.md"), "session\n")
		writeFile(t, filepath.Join(root, "Projects/vibe-palace/resume.md"), "resume\n")

		report := mustReport(t, root, false)
		if !containsPath(report.Dirt.Swept, "Projects/vibe-palace/sessions/2026-06-17.md") {
			t.Errorf("session artifact should be Swept, got Swept=%v", report.Dirt.Swept)
		}
		if !containsPath(report.Dirt.Reported, "Projects/vibe-palace/resume.md") {
			t.Errorf("resume.md should be Reported, got Reported=%v", report.Dirt.Reported)
		}
		if containsPath(report.Dirt.Swept, "Projects/vibe-palace/resume.md") {
			t.Errorf("resume.md must never be Swept, got Swept=%v", report.Dirt.Swept)
		}
	})
}

// TestIntegrationVaultStatusSections drives the vp_vault_status sections selector
// through the full MCP stack against REAL git: it proves the selector zeroes the
// unselected section (present-but-empty on the wire, never an absent key), keeps
// Version/Branch/VaultPath regardless, that both-sections equals the default byte
// output, and that an unknown section name is a clean tool error.
func TestIntegrationVaultStatusSections(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, _ := vsNewVaultWithOrigin(t)
	// Make the tree dirty so both sections carry real content by default.
	writeFile(t, filepath.Join(root, "Projects/vibe-palace/resume.md"), "resume\n")
	vault := storage.NewVault(root)
	tool := tools.VaultStatusTool(vault)

	call := func(t *testing.T, params map[string]any) ([]byte, storage.StatusReport) {
		t.Helper()
		in, _ := json.Marshal(params)
		res, err := tool.Handler(context.Background(), in)
		if err != nil {
			t.Fatalf("handler(%v): %v", params, err)
		}
		out, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var report storage.StatusReport
		if err := json.Unmarshal(out, &report); err != nil {
			t.Fatalf("decode StatusReport: %v\n%s", err, out)
		}
		return out, report
	}
	hasKey := func(t *testing.T, raw []byte, k string) bool {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("decode keys: %v\n%s", err, raw)
		}
		_, ok := m[k]
		return ok
	}

	t.Run("sync_only", func(t *testing.T) {
		raw, report := call(t, map[string]any{"sections": []string{"sync"}})
		if !hasKey(t, raw, "dirt") {
			t.Errorf("dirt key must remain present, got %s", raw)
		}
		if report.Dirt.Reported != nil || report.Dirt.Swept != nil {
			t.Errorf("Dirt must be zeroed for sync-only, got %+v", report.Dirt)
		}
		if len(report.Remotes) == 0 {
			t.Errorf("Remotes must be populated for sync-only, got %+v", report.Remotes)
		}
		if report.Version != 1 || report.Branch != "main" {
			t.Errorf("Version/Branch must persist, got version=%d branch=%q", report.Version, report.Branch)
		}
	})

	t.Run("dirt_only", func(t *testing.T) {
		raw, report := call(t, map[string]any{"sections": []string{"dirt"}})
		if !hasKey(t, raw, "remotes") {
			t.Errorf("remotes key must remain present, got %s", raw)
		}
		if report.Remotes != nil {
			t.Errorf("Remotes must be nil for dirt-only, got %+v", report.Remotes)
		}
		if !containsPath(report.Dirt.Reported, "Projects/vibe-palace/resume.md") {
			t.Errorf("Dirt must be populated for dirt-only, got %+v", report.Dirt)
		}
	})

	t.Run("both_equal_default", func(t *testing.T) {
		rawBoth, _ := call(t, map[string]any{"sections": []string{"sync", "dirt"}})
		rawDefault, _ := call(t, map[string]any{})
		if string(rawBoth) != string(rawDefault) {
			t.Errorf("both-sections must equal default\n both=%s\n def =%s", rawBoth, rawDefault)
		}
	})

	t.Run("unknown_section_errors", func(t *testing.T) {
		in, _ := json.Marshal(map[string]any{"sections": []string{"bogus"}})
		if _, err := tool.Handler(context.Background(), in); err == nil {
			t.Fatal("expected error for unknown section, got nil")
		}
	})
}

// mustReport is a tiny BuildStatusReport wrapper that fails the test on error.
func mustReport(t *testing.T, root string, fetch bool) storage.StatusReport {
	t.Helper()
	report, err := storage.BuildStatusReport(root, fetch)
	if err != nil {
		t.Fatalf("BuildStatusReport(fetch=%v): %v", fetch, err)
	}
	return report
}

func containsPath(paths []string, want string) bool {
	return slices.Contains(paths, want)
}
