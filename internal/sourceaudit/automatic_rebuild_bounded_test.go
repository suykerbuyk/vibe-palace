// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package sourceaudit

import (
	"slices"
	"strings"
	"testing"
)

// G11's tests. The rule's healthy steady state is zero findings, so a silent
// run over the real tree proves nothing on its own: each fixture below is a
// named break, asserted to FIRE, beside the shapes that must stay clean.

// autoRebuildTools is the cold-search launcher's package half, CLEAN.
const autoRebuildTools = `package tools

func NewRebuildLauncher(root string, launch func(string, []string, string) (int, error)) func(string) {
	return func(project string) {
		args := []string{"index", "rebuild", project, "--vault-root", root, "--no-transcripts"}
		_, _ = launch("", args, "")
	}
}

// CLEAN: the EXPLICIT site must NOT carry the flag, or opting in is
// unreachable. It is not in the allow-list, so the rule never looks at it.
func refreshIndexHandler(root, project string) []string {
	return []string{"index", "rebuild", project, "--vault-root", root}
}
`

// autoRebuildMain is the drain's spawner, CLEAN.
const autoRebuildMain = `package main

func spawnRebuildFor(root string) func(string) {
	return func(project string) {
		args := []string{"index", "rebuild", project, "--vault-root", root, "--no-transcripts"}
		_ = args
	}
}
`

func autoRebuildFindings(t *testing.T, pkgs map[string]string) []string {
	t.Helper()
	findings, err := Run(writeFixtureTree(t, pkgs))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out []string
	for _, f := range findings {
		if f.Kind == KindAutomaticRebuildUnbounded {
			out = append(out, f.Symbol)
		}
	}
	return out
}

func autoRebuildTree(tools, mainPkg string) map[string]string {
	return map[string]string{"tools": tools, "main": mainPkg}
}

// The negative control: both sanctioned sites carry the flag and the explicit
// one does not, so the rule is silent.
func TestAutomaticRebuildBoundedIsSilentOnTheSanctionedShapes(t *testing.T) {
	if got := autoRebuildFindings(t, autoRebuildTree(autoRebuildTools, autoRebuildMain)); len(got) != 0 {
		t.Errorf("the sanctioned shapes produced findings: %v", got)
	}
}

// The break the rule exists for, at site 1.
func TestAutomaticRebuildBoundedFlagsTheColdSearchLauncher(t *testing.T) {
	broken := strings.Replace(autoRebuildTools, `, "--no-transcripts"}`, `}`, 1)
	got := autoRebuildFindings(t, autoRebuildTree(broken, autoRebuildMain))
	if !slices.Contains(got, "tools.NewRebuildLauncher") {
		t.Errorf("a cold-search launcher argv with no --no-transcripts was not flagged: %v", got)
	}
	if slices.Contains(got, "main.spawnRebuildFor") {
		t.Errorf("the untouched drain spawner was flagged too: %v", got)
	}
}

// The break at site 2, independently — so neither site's guard can be
// satisfied by the other's.
func TestAutomaticRebuildBoundedFlagsTheDrainSpawner(t *testing.T) {
	broken := strings.Replace(autoRebuildMain, `, "--no-transcripts"}`, `}`, 1)
	got := autoRebuildFindings(t, autoRebuildTree(autoRebuildTools, broken))
	if !slices.Contains(got, "main.spawnRebuildFor") {
		t.Errorf("a drain-spawner argv with no --no-transcripts was not flagged: %v", got)
	}
	if slices.Contains(got, "tools.NewRebuildLauncher") {
		t.Errorf("the untouched cold-search launcher was flagged too: %v", got)
	}
}

// The ANCHOR: an allow-listed site that no longer exists means the rule is
// guarding a name nothing carries, and a rename would otherwise leave the site
// silently unguarded.
func TestAutomaticRebuildBoundedAnchorFiresWhenASiteIsMissing(t *testing.T) {
	renamed := strings.Replace(autoRebuildMain, "func spawnRebuildFor(", "func spawnTheRebuild(", 1)
	got := autoRebuildFindings(t, autoRebuildTree(autoRebuildTools, renamed))
	if !slices.Contains(got, "sourceaudit.automaticRebuildBounded/ABSENT/main.spawnRebuildFor") {
		t.Errorf("renaming an allow-listed spawn site did not fire the anchor: %v", got)
	}
}

// The EXPLICIT site must not be dragged in: a `vp index rebuild` argv built
// anywhere outside the allow-list is none of this rule's business, because
// forcing the flag there is what makes opting in impossible.
func TestAutomaticRebuildBoundedIgnoresTheExplicitSite(t *testing.T) {
	got := autoRebuildFindings(t, autoRebuildTree(autoRebuildTools, autoRebuildMain))
	for _, f := range got {
		if strings.Contains(f, "refreshIndexHandler") {
			t.Errorf("the explicit vp_refresh_index site was flagged: %v", got)
		}
	}
}
