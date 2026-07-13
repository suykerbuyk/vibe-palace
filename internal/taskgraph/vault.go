// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package taskgraph

import "github.com/suykerbuyk/vibe-palace/internal/storage"

// BuildFromVault loads a project's tasks and derives the graph.
//
// 🔴 THIS IS THE ONE PLACE includeDone IS DECIDED, AND IT MUST STAY TRUE.
//
// Build resolves a dependency by looking its slug up in the node set. Hand it
// only the ACTIVE tasks and every FINISHED dependency vanishes from that set —
// so a task correctly waiting on nothing at all would be reported as depending
// on a task that does not exist. The archive is not optional context here; it is
// what makes "satisfied" distinguishable from "you have a typo".
//
// Every production caller goes through this function. Build stays exported for
// tests, where the whole set is constructed by hand anyway.
func BuildFromVault(v *storage.Vault, project string) (*Graph, error) {
	tasks, err := v.ListTasks(project, true)
	if err != nil {
		return nil, err
	}
	return Build(tasks), nil
}
