// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bufio"
	"fmt"
	"io"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/commands"
)

// UpgradePromptOpts configures runUpgradePrompt. It lets the commands-
// and skills-upgrade paths share the accept/skip/accept-all/quit loop
// without leaking the extra surfaces (agent blocks, shims) that only
// the commands path has.
type UpgradePromptOpts struct {
	// GroupBy maps a change to the group identifier it shares with its
	// siblings. All changes in a group receive a single prompt; accept/
	// skip applies to every member. An identity GroupBy (returning the
	// change's own Name) restores per-change prompting. Ordering within
	// the input slice is preserved, and a group is prompted when its
	// first member is encountered.
	GroupBy func(commands.Change) string

	// RenderHeader prints the per-group header (e.g. "=== startup-analyst
	// (6 files) ===" for skills, or "=== restart (updated) ===" for
	// commands). Called once per group, before RenderBody.
	RenderHeader func(w io.Writer, groupID string, group []commands.Change)

	// RenderBody prints the per-group body (diff or "new file" summary).
	// Called once per group, after RenderHeader and before the prompt.
	RenderBody func(w io.Writer, groupID string, group []commands.Change)

	// AcceptAll forces every group to be accepted without prompting.
	// Used to implement --overwrite.
	AcceptAll bool

	// Reader supplies prompt input. Callers that chain multiple prompt
	// loops pass the same *bufio.Reader across calls so line buffering
	// does not drop bytes between phases.
	Reader *bufio.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// UpgradePromptResult bundles the accepted changes and aggregate counts
// so callers can thread them into Apply + the final status line without
// recomputing.
type UpgradePromptResult struct {
	Accepted      []commands.Change
	AcceptedCount int
	SkippedCount  int
	// AcceptAll reports whether the user escalated to accept-all during
	// the loop; callers that chain additional prompt surfaces (commands
	// path chains blocks + shims) honor this to skip those prompts.
	AcceptAll bool
	// Quit is true when the user chose "q" — caller should apply what
	// has been accepted so far and bail.
	Quit bool
}

// runUpgradePrompt drives the per-group accept/skip/accept-all/quit
// loop. It never reads the filesystem and never calls Apply; callers
// own both sides.
//
// Grouping algorithm (stable):
//
//   - Iterate plan in order. Skip Unchanged entries.
//   - Maintain a map of already-prompted group IDs. The first time a
//     new group ID is seen, render its header + body, prompt, and
//     record the choice. Subsequent members of the same group inherit
//     the recorded choice without re-prompting.
//
// Identity GroupBy (returning a unique ID per change) collapses to
// per-change prompting — that's what the commands path uses so output
// stays byte-identical to the pre-refactor implementation.
func runUpgradePrompt(plan []commands.Change, opts UpgradePromptOpts) UpgradePromptResult {
	res := UpgradePromptResult{
		Accepted:  make([]commands.Change, 0, len(plan)),
		AcceptAll: opts.AcceptAll,
	}
	reader := opts.Reader

	// Group order is preserved: slice of IDs in first-seen order, plus
	// a map to the member slice.
	type groupEntry struct {
		id     string
		member []commands.Change
	}
	var groups []groupEntry
	idx := map[string]int{}
	for _, c := range plan {
		if c.Kind == commands.ChangeUnchanged {
			continue
		}
		id := opts.GroupBy(c)
		if i, ok := idx[id]; ok {
			groups[i].member = append(groups[i].member, c)
			continue
		}
		idx[id] = len(groups)
		groups = append(groups, groupEntry{id: id, member: []commands.Change{c}})
	}

	for _, g := range groups {
		if res.AcceptAll {
			res.Accepted = append(res.Accepted, g.member...)
			res.AcceptedCount += len(g.member)
			for _, c := range g.member {
				fmt.Fprintf(opts.Stdout, "[accept] %s (%s)\n", c.Name, c.Kind)
			}
			continue
		}
		opts.RenderHeader(opts.Stdout, g.id, g.member)
		opts.RenderBody(opts.Stdout, g.id, g.member)

		choice, err := cli.PromptChoice(opts.Stdout, reader)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "prompt: %v\n", err)
			res.Quit = true
			return res
		}
		switch choice {
		case "a":
			res.Accepted = append(res.Accepted, g.member...)
			res.AcceptedCount += len(g.member)
		case "A":
			res.Accepted = append(res.Accepted, g.member...)
			res.AcceptedCount += len(g.member)
			res.AcceptAll = true
		case "s":
			res.SkippedCount += len(g.member)
		case "q":
			res.Quit = true
			return res
		}
	}
	return res
}
