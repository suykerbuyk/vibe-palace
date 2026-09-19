// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
)

// These are real races, run many times. On the fixed code a move and any
// writer of its source are serialised on the source path's lock, so the
// properties asserted here hold in EVERY interleaving: timing can only decide
// whether a broken lock is caught, never whether correct code passes. Measured
// on 19fd24c, before the source lock, a retire racing a move duplicated the task
// in 241–309 of 400 iterations with both calls returning nil.

// taskLocations reports which of the four places a task p/x can be in.
func taskLocations(v *Vault) []string {
	var at []string
	for _, rel := range []string{
		"Projects/p/tasks/x.md", "Projects/p/tasks/done/x.md",
		"Projects/p/tasks/cancelled/x.md", "Projects/q/tasks/x.md",
	} {
		if !taskFileAbsent(v, rel) {
			at = append(at, rel)
		}
	}
	return at
}

// checkRaceLoser asserts a race loser's error is one of the refusals the design
// allows, and never the two that would mean the lock is not holding.
func checkRaceLoser(t *testing.T, i int, err error) {
	t.Helper()
	var te *taskSlugTakenError
	msg := err.Error()
	switch {
	case errors.As(err, &te):
		t.Fatalf("iteration %d: a race loser got the slug-taken refusal: %v", i, err)
	case strings.Contains(msg, "failed, so the task is still in"):
		t.Fatalf("iteration %d: a move renamed a source that was already gone: %v", i, err)
	case strings.Contains(msg, "a concurrent operation"):
		// The under-lock losers: new wording, caller friction.
		if !apperr.IsCaller(err) {
			t.Fatalf("iteration %d: an under-lock race loser is not classified as caller friction: %v", i, err)
		}
	case strings.Contains(msg, "not found (may already be"),
		strings.Contains(msg, "it is archived at"),
		strings.Contains(msg, "not found in the active tasks of project"):
		// The pre-lock refusals: the winner finished before this call started.
	default:
		t.Fatalf("iteration %d: unexpected error from the race loser: %v", i, err)
	}
}

// A retire or cancel racing a move of the same task: the task ends up in
// exactly one place, and exactly one call succeeds.
func TestRetireRacingMoveNeverDuplicates(t *testing.T) {
	const iterations = 400
	for _, c := range []struct {
		name    string
		archive func(v *Vault) error
	}{
		{"retire", func(v *Vault) error { return v.RetireTask("p", "x") }},
		{"cancel", func(v *Vault) error { return v.CancelTask("p", "x", "") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			for i := 0; i < iterations; i++ {
				v := testVault(t)
				seedTaskRaw(t, v, "p", "", "x", "TASK")
				start := make(chan struct{})
				archived, moved := make(chan error, 1), make(chan error, 1)
				go func() { <-start; archived <- c.archive(v) }()
				go func() { <-start; moved <- v.MoveTaskToProject("p", "x", "q") }()
				close(start)
				aerr, merr := <-archived, <-moved
				if at := taskLocations(v); len(at) != 1 {
					t.Fatalf("iteration %d: the task is in %d places %v (%s err=%v, move err=%v)", i, len(at), at, c.name, aerr, merr)
				}
				if (aerr == nil) == (merr == nil) {
					t.Fatalf("iteration %d: want exactly one success; %s err=%v, move err=%v", i, c.name, aerr, merr)
				}
				if aerr != nil {
					checkRaceLoser(t, i, aerr)
				} else {
					checkRaceLoser(t, i, merr)
				}
			}
		})
	}
}

// The class the filing missed: any writer of the source re-created it. An amend
// or a status change racing a move leaves the task in exactly one place. Both
// may legitimately succeed (write, then move), so only the location is asserted.
func TestSourceWriterRacingMoveNeverDuplicates(t *testing.T) {
	const iterations = 400
	for _, c := range []struct {
		name  string
		write func(v *Vault) error
	}{
		{"amend", func(v *Vault) error {
			_, err := v.AmendTask("p", "x", "Context", "amended while a move ran")
			return err
		}},
		{"status", func(v *Vault) error { return v.UpdateTaskStatus("p", "x", StatusInProgress) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			for i := 0; i < iterations; i++ {
				v := testVault(t)
				seedTaskRaw(t, v, "p", "", "x", "TASK")
				start := make(chan struct{})
				wrote, moved := make(chan error, 1), make(chan error, 1)
				go func() { <-start; wrote <- c.write(v) }()
				go func() { <-start; moved <- v.MoveTaskToProject("p", "x", "q") }()
				close(start)
				werr, merr := <-wrote, <-moved
				if at := taskLocations(v); len(at) != 1 {
					t.Fatalf("iteration %d: the task is in %d places %v (%s err=%v, move err=%v)", i, len(at), at, c.name, werr, merr)
				}
			}
		})
	}
}

// Opposing moves of one slug, p→q and q→p, both acquire the same two locks. With
// an active x in both projects each passes its unlocked checks, takes the pair,
// and refuses on the taken slug — so acquisition is exercised every time. A pair
// that acquired in argument order would deadlock here.
func TestOpposingMovesDoNotDeadlock(t *testing.T) {
	const iterations = 200
	deadline := time.Now().Add(30 * time.Second)
	for i := 0; i < iterations; i++ {
		if time.Now().After(deadline) {
			t.Fatalf("opposing moves exceeded the 30 s ceiling at iteration %d", i)
		}
		v := testVault(t)
		seedTaskRaw(t, v, "p", "", "x", "IN-P")
		seedTaskRaw(t, v, "q", "", "x", "IN-Q")
		start := make(chan struct{})
		done := make(chan error, 2)
		go func() { <-start; done <- v.MoveTaskToProject("p", "x", "q") }()
		go func() { <-start; done <- v.MoveTaskToProject("q", "x", "p") }()
		close(start)
		for j := 0; j < 2; j++ {
			select {
			case err := <-done:
				if err == nil {
					t.Fatalf("iteration %d: a move onto an active twin succeeded", i)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("iteration %d: deadlock suspected: opposing moves did not complete within 5s", i)
			}
		}
	}
}
