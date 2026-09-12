// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

// Go port of the retired test/e2e/walkthrough/ bash tier — a single,
// deliberately singular, narrated case mirroring doc/TUTORIAL.md Part 2
// ("Project Setup"). If this test and Part 2 disagree, one of them is wrong.
//
// One test function, not one-per-step: the old runtime "exactly one case
// file" structural check (walkthrough/run.sh's glob-and-count-*.sh guard) has
// no direct Go analogue — Go compiles files, it doesn't glob case scripts at
// runtime — so that invariant is enforced by convention (this doc comment)
// rather than by a runtime check. Low-severity property loss, noted in the
// migration plan.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// TestIntegrationE2EWalkthroughHappyPath is the ONLY test in this file. Its
// stdout IS the documentation artifact: every banner and every captured
// `vp` invocation's stdout is written directly to os.Stdout (never t.Log),
// because Go's testing package buffers t.Log/t.Logf per-test and only
// flushes it on failure or under -v — a plain `go test` pass run would
// otherwise silently swallow the transcript, breaking the "the transcript IS
// the documentation" property the retired bash harness guaranteed via
// `tee`+`cat`.
func TestIntegrationE2EWalkthroughHappyPath(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the real vp binary; skipped under -short")
	}
	out := os.Stdout

	env := testinfra.IsolateEnv(t)
	caseDir := testinfra.RetainOnFailure(t, "walkthrough")

	// ---------------------------------------------------------------------
	// STEP 1 — create a project directory (mirrors TUTORIAL Part 2:
	// "Initialize a Project"). Nothing vibe-palace-specific yet; this is
	// just the user landing in a project they want to memorialize.
	// ---------------------------------------------------------------------
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "=== STEP 1: create project directory ===")
	fmt.Fprintln(out, "(mirrors TUTORIAL Part 2 — Initialize a Project)")
	projA := filepath.Join(caseDir, "proj-a")
	if err := os.MkdirAll(projA, 0o755); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(out, "cwd: %s\n", projA)

	// ---------------------------------------------------------------------
	// STEP 2 — LOAD-BEARING `git init`. Not decorative.
	//
	// `vp init` only writes .vibe-palace.toml when project.DetectSignal(dir)
	// is non-None. With no .git (and no recognized manifest like go.mod /
	// package.json / Cargo.toml), step 3's per-project assertion would
	// silently pass because the config would NEVER be written. Commit
	// 92338b1 hardened the positional-arg validator against exactly this
	// class of silent regression; this step LOCKS that guarantee in at e2e
	// scope.
	// ---------------------------------------------------------------------
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "=== STEP 2: git init (LOAD-BEARING) ===")
	fmt.Fprintln(out, "git init writes .git/ which is the project signal vp init detects —")
	fmt.Fprintln(out, "without it, vp init would NOT write .vibe-palace.toml (silent")
	fmt.Fprintln(out, "regression risk, see commit 92338b1).")
	gitInit(t, projA)
	requireDirExists(t, filepath.Join(projA, ".git"))

	// ---------------------------------------------------------------------
	// STEP 3 — `vp init` with defaults. Asserts all three configuration
	// artifacts land where the tutorial promises: global config, default
	// vault, and per-project stub.
	// ---------------------------------------------------------------------
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "=== STEP 3: vp init (default config + default vault) ===")
	fmt.Fprintln(out, "(mirrors TUTORIAL Part 2 — Initialize a Project / Understanding the Vault)")
	r := testinfra.RunCLI(t, env.Environ(), projA, nil, "init")
	r.Must(t)
	fmt.Fprint(out, r.Stdout)

	requireFileExists(t, filepath.Join(env.Home, ".config", "vibe-palace", "config.toml"))
	requireDirExists(t, filepath.Join(env.Home, "vibe-palace-vault"))
	requireFileExists(t, filepath.Join(projA, ".vibe-palace.toml"))

	// ---------------------------------------------------------------------
	// STEP 4 — hydrate the project space and inspect it.
	//
	// End-users normally trigger capture through their editor's MCP client
	// (vp_capture_session); we call the inlined seedDrawer helper here to
	// produce the same on-disk artifact without an MCP roundtrip or a
	// subprocess. Then `vp status` and `vp inject` demonstrate that the
	// bootstrap JSON the AI sees contains a non-empty .project.
	// ---------------------------------------------------------------------
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "=== STEP 4: hydrate + inspect ===")
	fmt.Fprintln(out, "(End-users normally trigger capture through their editor's MCP client")
	fmt.Fprintln(out, " (vp_capture_session); this port calls the inlined seed-drawer helper")
	fmt.Fprintln(out, " directly to produce the same on-disk artifact without an MCP roundtrip.)")
	seedDrawer(t, env.Home, "proj-a", "facts", "Walkthrough seeds a fact drawer for demo purposes.")

	fmt.Fprintln(out, "--- vp status ---")
	statusRes := testinfra.RunCLI(t, env.Environ(), projA, nil, "status")
	statusRes.Must(t)
	fmt.Fprint(out, statusRes.Stdout)

	fmt.Fprintln(out, "--- vp inject ---")
	injectRes := testinfra.RunCLI(t, env.Environ(), projA, nil, "inject")
	injectRes.Must(t)
	// Show a readable slice of the bootstrap JSON rather than the whole blob.
	preview := injectRes.Stdout
	if len(preview) > 400 {
		preview = preview[:400]
	}
	fmt.Fprint(out, preview)
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "… (bootstrap JSON truncated for transcript readability)")

	// Shape check: bootstrap JSON must carry a non-empty .project. stdout
	// from `vp inject` is pure JSON.
	var bootstrap struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal([]byte(injectRes.Stdout), &bootstrap); err != nil {
		t.Fatalf("vp inject stdout is not valid JSON: %v\n%s", err, injectRes.Stdout)
	}
	if bootstrap.Project == "" {
		t.Fatalf("vp inject bootstrap JSON has empty .project:\n%s", injectRes.Stdout)
	}

	// ---------------------------------------------------------------------
	// STEP 5 — attach a SECOND project to the SAME vault. `vp init
	// --vault-path` must be idempotent at the vault level: proj-b gets its
	// own .vibe-palace.toml but the shared vault .git/HEAD must not move.
	// ---------------------------------------------------------------------
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "=== STEP 5: second project, same vault ===")
	fmt.Fprintln(out, "(mirrors TUTORIAL Part 2 — Understanding the Vault: one vault, many projects)")
	vaultHeadPath := filepath.Join(env.Home, "vibe-palace-vault", ".git", "HEAD")
	before, err := os.ReadFile(vaultHeadPath)
	if err != nil {
		t.Fatal(err)
	}

	projB := filepath.Join(caseDir, "proj-b")
	if err := os.MkdirAll(projB, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, projB)
	r5 := testinfra.RunCLI(t, env.Environ(), projB, nil, "init", "--vault-path", filepath.Join(env.Home, "vibe-palace-vault"))
	r5.Must(t)
	fmt.Fprint(out, r5.Stdout)
	requireFileExists(t, filepath.Join(projB, ".vibe-palace.toml"))

	after, err := os.ReadFile(vaultHeadPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("vault .git/HEAD changed — proj-b must not reinitialize the vault (before=%q after=%q)", before, after)
	}
	fmt.Fprintf(out, "vault .git/HEAD unchanged (%s) — proj-b attached, not reinitialized.\n", after)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "=== walkthrough: all steps passed ===")
}
