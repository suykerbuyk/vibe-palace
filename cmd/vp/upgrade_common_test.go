// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/commands"
)

// newPromptReader makes a *bufio.Reader over a string (one call site,
// keeps the tests concise).
func newPromptReader(s string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(s))
}

// identity maps every change to its own Name, collapsing runUpgradePrompt
// to per-change prompting (the commands path's behavior).
func identityGroup(c commands.Change) string { return c.Name }

func changes(names ...string) []commands.Change {
	out := make([]commands.Change, 0, len(names))
	for _, n := range names {
		out = append(out, commands.Change{
			Name:            n,
			Kind:            commands.ChangeNew,
			EmbeddedContent: "body-of-" + n,
		})
	}
	return out
}

func TestRunUpgradePrompt_IdentityPerChange(t *testing.T) {
	plan := changes("a", "b", "c")
	var out, errb bytes.Buffer
	res := runUpgradePrompt(plan, UpgradePromptOpts{
		GroupBy: identityGroup,
		RenderHeader: func(w io.Writer, _ string, g []commands.Change) {
			_ = w
		},
		RenderBody: func(w io.Writer, _ string, g []commands.Change) {},
		Reader:     newPromptReader("a\ns\na\n"),
		Stdout:     &out,
		Stderr:     &errb,
	})
	if res.AcceptedCount != 2 || res.SkippedCount != 1 {
		t.Errorf("counts: accepted=%d skipped=%d, want 2/1", res.AcceptedCount, res.SkippedCount)
	}
	if len(res.Accepted) != 2 {
		t.Errorf("accepted len=%d, want 2", len(res.Accepted))
	}
	if res.Accepted[0].Name != "a" || res.Accepted[1].Name != "c" {
		t.Errorf("accepted order: got %+v", res.Accepted)
	}
}

func TestRunUpgradePrompt_AcceptAllShortCircuits(t *testing.T) {
	plan := changes("a", "b", "c")
	var out, errb bytes.Buffer
	res := runUpgradePrompt(plan, UpgradePromptOpts{
		GroupBy:      identityGroup,
		RenderHeader: func(w io.Writer, _ string, _ []commands.Change) {},
		RenderBody:   func(w io.Writer, _ string, _ []commands.Change) {},
		Reader:       newPromptReader("A\n"),
		Stdout:       &out,
		Stderr:       &errb,
	})
	if res.AcceptedCount != 3 || res.SkippedCount != 0 {
		t.Errorf("A choice: accepted=%d skipped=%d, want 3/0", res.AcceptedCount, res.SkippedCount)
	}
	if !res.AcceptAll {
		t.Errorf("AcceptAll flag not set")
	}
	// Every name appears in stdout as "[accept] …" after the A choice.
	for _, n := range []string{"b", "c"} {
		if !strings.Contains(out.String(), "[accept] "+n) {
			t.Errorf("output missing [accept] %s:\n%s", n, out.String())
		}
	}
}

func TestRunUpgradePrompt_Quit(t *testing.T) {
	plan := changes("a", "b", "c")
	res := runUpgradePrompt(plan, UpgradePromptOpts{
		GroupBy:      identityGroup,
		RenderHeader: func(w io.Writer, _ string, _ []commands.Change) {},
		RenderBody:   func(w io.Writer, _ string, _ []commands.Change) {},
		Reader:       newPromptReader("a\nq\n"),
		Stdout:       &bytes.Buffer{},
		Stderr:       &bytes.Buffer{},
	})
	if !res.Quit {
		t.Errorf("Quit not set")
	}
	if res.AcceptedCount != 1 {
		t.Errorf("accepted=%d, want 1", res.AcceptedCount)
	}
}

func TestRunUpgradePrompt_EOFCountsAsSkip(t *testing.T) {
	plan := changes("a", "b")
	res := runUpgradePrompt(plan, UpgradePromptOpts{
		GroupBy:      identityGroup,
		RenderHeader: func(w io.Writer, _ string, _ []commands.Change) {},
		RenderBody:   func(w io.Writer, _ string, _ []commands.Change) {},
		Reader:       newPromptReader(""), // immediate EOF
		Stdout:       &bytes.Buffer{},
		Stderr:       &bytes.Buffer{},
	})
	if res.SkippedCount != 2 {
		t.Errorf("EOF: skipped=%d, want 2", res.SkippedCount)
	}
}

// TestRunUpgradePrompt_GroupingCollapsesSkillFiles proves that when a
// GroupBy collapses multiple files to one group, a single "a" response
// accepts every member. 6-file startup-analyst fans out into one group.
func TestRunUpgradePrompt_GroupingCollapsesSkillFiles(t *testing.T) {
	names := []string{
		"startup-analyst/SKILL.md",
		"startup-analyst/references/capex-opex.md",
		"startup-analyst/references/competitive-landscape.md",
		"startup-analyst/references/funding-sources.md",
		"startup-analyst/references/reality-validation.md",
		"startup-analyst/references/strategic-partnerships.md",
	}
	plan := changes(names...)
	res := runUpgradePrompt(plan, UpgradePromptOpts{
		GroupBy:      skillGroupID,
		RenderHeader: func(w io.Writer, _ string, _ []commands.Change) {},
		RenderBody:   func(w io.Writer, _ string, _ []commands.Change) {},
		Reader:       newPromptReader("a\n"),
		Stdout:       &bytes.Buffer{},
		Stderr:       &bytes.Buffer{},
	})
	if res.AcceptedCount != 6 {
		t.Errorf("accepted=%d, want 6", res.AcceptedCount)
	}
	if len(res.Accepted) != 6 {
		t.Errorf("Accepted len=%d, want 6", len(res.Accepted))
	}
	// Order must be preserved relative to the input plan.
	for i, n := range names {
		if res.Accepted[i].Name != n {
			t.Errorf("Accepted[%d].Name = %q, want %q", i, res.Accepted[i].Name, n)
		}
	}
}

func TestRunUpgradePrompt_GranularFansOut(t *testing.T) {
	names := []string{
		"startup-analyst/SKILL.md",
		"startup-analyst/references/capex-opex.md",
	}
	plan := changes(names...)
	res := runUpgradePrompt(plan, UpgradePromptOpts{
		GroupBy:      identityGroup, // --granular
		RenderHeader: func(w io.Writer, _ string, _ []commands.Change) {},
		RenderBody:   func(w io.Writer, _ string, _ []commands.Change) {},
		Reader:       newPromptReader("a\ns\n"),
		Stdout:       &bytes.Buffer{},
		Stderr:       &bytes.Buffer{},
	})
	if res.AcceptedCount != 1 || res.SkippedCount != 1 {
		t.Errorf("granular: accepted=%d skipped=%d, want 1/1",
			res.AcceptedCount, res.SkippedCount)
	}
}
