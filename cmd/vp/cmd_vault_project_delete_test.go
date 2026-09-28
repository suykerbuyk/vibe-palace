// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The CLI is thin over storage: the dry run prints the plan as JSON and
// writes nothing, the real run publishes, and a request with neither
// --moved-to nor --discard refuses.
func TestVaultProjectDeleteCLI(t *testing.T) {
	setupVaultWithOrigin(t)
	dir, bare := newRepoWithOrigin(t)
	mkfile(t, dir, "Projects/p/resume.md", "p\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-m", "p")
	gitRun(t, dir, "push", "origin", "main")
	head := gitHead(t, dir)

	if _, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultProject(), "delete", "p", "--vault", dir); code != cli.ExitUser ||
		!strings.Contains(stderr, "--discard") {
		t.Fatalf("neither flag: exit %d, stderr %s", code, stderr)
	}
	if _, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultProject(), "remove", "p"); code != cli.ExitUser ||
		!strings.Contains(stderr, "unknown subcommand") {
		t.Fatalf("unknown subcommand: exit %d, stderr %s", code, stderr)
	}

	stdout, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultProject(), "delete", "p", "--discard", "--vault", dir, "--dry-run", "--json")
	if code != cli.ExitOK {
		t.Fatalf("dry run: exit %d\n%s", code, stderr)
	}
	var plan storage.DeletePlan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("dry run JSON: %v\n%s", err, stdout)
	}
	if plan.Mode != storage.DeleteFull || plan.Kind != "deleted" || !strings.Contains(plan.Command, "--expect "+plan.Digest) {
		t.Fatalf("plan = %+v", plan)
	}
	if gitHead(t, dir) != head {
		t.Fatal("the dry run committed")
	}

	if _, stderr, code := runVaultCmdCapturingBoth(t, cmdVaultProject(), "delete", "p", "--discard", "--vault", dir, "--expect", plan.Digest); code != cli.ExitOK {
		t.Fatalf("real run: exit %d\n%s", code, stderr)
	}
	if got := gitRun(t, bare, "rev-parse", "main"); got != gitHead(t, dir) || got == head {
		t.Fatalf("the delete was not published: remote %s, HEAD %s", got, gitHead(t, dir))
	}
}
