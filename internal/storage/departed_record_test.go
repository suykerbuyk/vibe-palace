// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// U15 round 2, item 1: under the record-wins rule Audits/departures/ is the
// most powerful path in the vault. No ordinary caller may forge, edit, delete
// or move a record; only the lifecycle commands write one, under the vault's
// live root-lock token.

func TestOrdinaryCallersCannotTouchADepartureRecord(t *testing.T) {
	ops := map[string]func(dir string) error{
		"Write forges keep.json": func(dir string) error {
			_, err := vaultfs.Write(dir, departure.RelPath("keep"), `{"slug":"keep","kind":"deleted"}`, "")
			return err
		},
		"Create forges keep.json": func(dir string) error {
			_, err := vaultfs.Create(dir, departure.RelPath("keep"), `{"slug":"keep","kind":"deleted"}`)
			return err
		},
		"Edit p.json": func(dir string) error {
			_, err := vaultfs.Edit(dir, departure.RelPath("p"), "moved-to-vault", "renamed", false, "")
			return err
		},
		"Delete p.json": func(dir string) error {
			_, err := vaultfs.Delete(dir, departure.RelPath("p"), "")
			return err
		},
		"Move p.json away": func(dir string) error {
			_, err := vaultfs.Move(dir, departure.RelPath("p"), "Notes/p.json")
			return err
		},
		"Move a file onto keep.json": func(dir string) error {
			_, err := vaultfs.Move(dir, "Projects/keep/resume.md", departure.RelPath("keep"))
			return err
		},
		"append writer": func(dir string) error {
			return NewVault(dir).appendUnderLock(filepath.Join(dir, filepath.FromSlash(departure.RelPath("keep"))), []byte("{}\n"))
		},
		"atomicfile.Write with no token": func(dir string) error {
			return atomicfile.Write(dir, filepath.Join(dir, filepath.FromSlash(departure.RelPath("keep"))), []byte("{}\n"))
		},
		"atomicfile.Write with a per-path token": func(dir string) error {
			abs := filepath.Join(dir, filepath.FromSlash(departure.RelPath("keep")))
			h, err := vaultlock.AcquireHeld(dir, abs)
			if err != nil {
				return err
			}
			defer h.Release()
			return atomicfile.Write(dir, abs, []byte("{}\n"), atomicfile.ForDepartureRecord(h))
		},
		"WriteDepartureRecord with a per-path token": func(dir string) error {
			h, err := vaultlock.AcquireHeld(dir, filepath.Join(dir, "x"))
			if err != nil {
				return err
			}
			defer h.Release()
			_, err = vaultfs.WriteDepartureRecord(h, "keep", []byte("{}\n"))
			return err
		},
		"RemoveDepartureRecord with a per-path token": func(dir string) error {
			h, err := vaultlock.AcquireHeld(dir, filepath.Join(dir, "x"))
			if err != nil {
				return err
			}
			defer h.Release()
			return vaultfs.RemoveDepartureRecord(h, "p")
		},
		"RecordDepartureForDelete with no token": func(dir string) error {
			_, _, _, err := NewVault(dir).RecordDepartureForDelete(nil, "keep", departure.Deleted, "", DepartureFacts{})
			return err
		},
	}
	for name, op := range ops {
		dir := departedFixture(t)
		before := readFile(t, dir, departure.RelPath("p"))
		if err := op(dir); err == nil {
			t.Errorf("%s: not refused", name)
		}
		if got := readFile(t, dir, departure.RelPath("p")); got != before {
			t.Errorf("%s: p.json changed", name)
		}
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(departure.RelPath("keep")))); err == nil {
			t.Errorf("%s: keep.json was forged", name)
		}
	}
}

// The privileged entry point works with the root token, and the lifecycle
// writers — the split purge's and the delete's — still write and roll back.
func TestTheLifecycleCommandsStillWriteRecords(t *testing.T) {
	dir := departedFixture(t)
	h, err := vaultlock.AcquireHeld(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := vaultfs.WriteDepartureRecord(h, "other", []byte(`{"format":"vp-departure/1","slug":"other","kind":"deleted","date":"2026-09-28"}`+"\n"))
	if err != nil || !created {
		t.Fatalf("WriteDepartureRecord under the root token: %v, created %v", err, created)
	}
	if err := vaultfs.RemoveDepartureRecord(h, "other"); err != nil {
		t.Fatalf("RemoveDepartureRecord under the root token: %v", err)
	}
	_ = h.Release()
	writeFile(t, dir, "Projects/gone/resume.md", "x\n")
	if _, _, err := NewVault(dir).RecordDepartureForPurge("gone", departure.MovedToVault, "q"); err != nil {
		t.Fatalf("the split purge's writer: %v", err)
	}
}

// The commit guard's recovery never removes a live project's tree under a
// forged record, and never commits a record's removal.
func TestPendingRecordAdviceIsSafeForAForgedOrRemovedRecord(t *testing.T) {
	cmdOf := func(msg, label string) string {
		m := regexp.MustCompile(`(?m)^  - ` + regexp.QuoteMeta(label) + `: (.*)$`).FindStringSubmatch(msg)
		if m == nil {
			return ""
		}
		return m[1]
	}
	t.Run("forged over a live project", func(t *testing.T) {
		dir := departedFixture(t)
		writeFile(t, dir, departure.RelPath("keep"), `{"format":"vp-departure/1","slug":"keep","kind":"deleted","date":"2026-09-28"}`+"\n")
		writeFile(t, dir, "Projects/keep/resume.md", "an uncommitted edit to the live project\n")
		err := refuseOnPendingDepartures(dir)
		if err == nil {
			t.Fatal("premise: the forged record is pending")
		}
		msg := err.Error()
		if strings.Contains(msg, " rm -r") || !strings.HasPrefix(cmdOf(msg, "finish it"), "re-run the command") {
			t.Fatalf("the advice would remove the live tree:\n%s", msg)
		}
		undo := cmdOf(msg, "or undo it")
		if out, err := exec.Command("sh", "-c", undo).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", undo, err, out)
		}
		if got := readFile(t, dir, "Projects/keep/resume.md"); got != "an uncommitted edit to the live project\n" {
			t.Fatalf("undo lost the live project's uncommitted edit: %q", got)
		}
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(departure.RelPath("keep")))); err == nil {
			t.Fatal("undo left the forged record")
		}
	})
	t.Run("a committed record removed", func(t *testing.T) {
		dir := departedFixture(t)
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(departure.RelPath("p")))); err != nil {
			t.Fatal(err)
		}
		err := refuseOnPendingDepartures(dir)
		if err == nil {
			t.Fatal("premise: the removal is pending")
		}
		msg := err.Error()
		if cmdOf(msg, "finish it") != "" || !strings.Contains(msg, "committing a record's removal reopens") {
			t.Fatalf("the advice must restore, never commit the removal:\n%s", msg)
		}
		restore := cmdOf(msg, "restore it")
		if out, err := exec.Command("sh", "-c", restore).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", restore, err, out)
		}
		if _, found := departure.Find(dir, "p"); !found {
			t.Fatal("restore did not bring the record back")
		}
	})
}

// The sync names a stray under a departed tree and says to remove it, and a
// swept artifact there is carried as LeftDeparted.
func TestSyncOverADepartedStray(t *testing.T) {
	b, bare := departureFixture(t)
	departOnRemote(t, bare, departure.MovedToVault, "git@example.invalid:q/v.git", nil)
	gitRun(t, b, "pull", "-q", "origin", "main")
	writeFile(t, b, "Projects/old/sessions/2026-09-27-aaaa-01.md", "a stale session\n")
	res, err := SyncVault(b, []string{"origin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.LeftDeparted) != 1 || !strings.Contains(res.LeftDeparted[0], "departed project old") {
		t.Fatalf("LeftDeparted = %q", res.LeftDeparted)
	}
	writeFile(t, b, "Projects/old/notes.md", "a stale note\n")
	_, err = SyncVault(b, []string{"origin"})
	if err == nil || !strings.Contains(err.Error(), "remove it") || !strings.Contains(err.Error(), "project old departed") {
		t.Fatalf("err = %v", err)
	}
}
