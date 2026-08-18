// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// ReadCommitLogAnchor returns the SHA stored in the project's
// commit-log.anchor, or "" when the anchor file does not exist yet — the
// canonical "never archived" signal that the archive-at-wrap step seeds from
// the repo's oldest root commit. Only a true I/O error propagates.
func (v *Vault) ReadCommitLogAnchor(project string) (string, error) {
	path, err := v.CommitLogAnchorFile(project)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read commit-log anchor: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// archivedSHAs returns the set of commit SHAs already recorded in an
// already-read commit-log.md body, by scanning for the "## commit <sha>"
// headers ArchiveCommitBodies writes.
//
// It anchors on a line START so a "## commit <sha>" quoted inside an archived
// commit MESSAGE — this project's own messages discuss commits routinely —
// cannot be mistaken for a real entry. Everything after the SHA token on the
// header line is ignored, so a future header that grows a suffix still matches.
func archivedSHAs(body string) map[string]struct{} {
	seen := map[string]struct{}{}
	for line := range strings.SplitSeq(body, "\n") {
		rest, ok := strings.CutPrefix(line, "## commit ")
		if !ok {
			continue
		}
		sha, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if sha != "" {
			seen[sha] = struct{}{}
		}
	}
	return seen
}

// ArchiveCommitBodies appends each commit's full message to the project's
// commit-log.md append-log and advances the last-archived anchor to newAnchor,
// both under a SINGLE hold of the commit-log.md path lock so a concurrent
// archive-at-wrap cannot interleave an append with an anchor advance and lose
// commits. It returns (appended, skipped): entries written, and entries the log
// already carried.
//
// 🔴 The append is idempotent on CONTENT, not on position. commit-log.md is a
// SHARED vault artifact; the anchor that guards it is HOST-LOCAL state. So a
// second host's walk legitimately yields a SHA this file already carries — its
// own anchor knew nothing about the first host's archive — and appending it
// double-records a commit in what the project calls its permanent history.
// Position-based appending made every commit that crossed between two hosts a
// duplicate (iter 281). Re-reading the log inside the lock and skipping SHAs it
// already holds is what makes the walk safe to run from ANY anchor.
//
// Empty commits appends nothing but STILL advances the anchor to newAnchor:
// the wrap that finds no new commits (a re-run, a coherency-only tree) writes
// the anchor forward to HEAD so the next wrap's <anchor>..HEAD is empty too —
// the idempotence the archive relies on. A blank newAnchor is a caller bug and
// is refused rather than clobbering a good anchor with nothing.
//
// The full message body is written verbatim under a "## commit <sha>" header so
// the file stays readable AND `git log -p` over it recovers every archived
// message. The git walk itself lives in internal/wrapstate; this method only
// writes vault artifacts, keeping git out of the storage layer.
func (v *Vault) ArchiveCommitBodies(project string, commits []wrapstate.CommitInfo, newAnchor string) (int, int, error) {
	if strings.TrimSpace(newAnchor) == "" {
		return 0, 0, fmt.Errorf("archive commit bodies: refusing to write an empty anchor")
	}
	logPath, err := v.CommitLogFile(project)
	if err != nil {
		return 0, 0, err
	}
	anchorPath, err := v.CommitLogAnchorFile(project)
	if err != nil {
		return 0, 0, err
	}
	if err := EnsureDir(filepath.Dir(logPath)); err != nil {
		return 0, 0, fmt.Errorf("ensure project dir: %w", err)
	}

	// One lock over both the append and the anchor advance: the anchor is the
	// commitment that "commit-log.md is current through this SHA", so it must
	// not move independently of the append it describes.
	release, err := vaultlock.Acquire(v.Root, logPath)
	if err != nil {
		return 0, 0, fmt.Errorf("lock commit-log: %w", err)
	}
	defer release()

	appended, skipped := 0, 0
	if len(commits) > 0 {
		// Read the existing log INSIDE the lock. Reading it before acquiring
		// would let a concurrent archive land an entry between the read and
		// the append, and this dedup would not see it.
		existing, err := os.ReadFile(logPath)
		if err != nil && !os.IsNotExist(err) {
			return 0, 0, fmt.Errorf("read commit-log: %w", err)
		}
		seen := archivedSHAs(string(existing))

		var b strings.Builder
		for _, c := range commits {
			if _, dup := seen[c.SHA]; dup {
				skipped++
				continue
			}
			// Guard the batch against itself too: a walk that yields the same
			// SHA twice must still write one entry.
			seen[c.SHA] = struct{}{}
			body := strings.TrimRight(c.Body, "\n")
			if body == "" {
				// A commit with an empty body is still a real commit; record
				// the header so the SHA is not silently dropped.
				fmt.Fprintf(&b, "\n## commit %s\n", c.SHA)
			} else {
				fmt.Fprintf(&b, "\n## commit %s\n\n%s\n", c.SHA, body)
			}
			appended++
		}

		if appended > 0 {
			f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err != nil {
				return 0, 0, fmt.Errorf("open commit-log: %w", err)
			}
			if _, err := f.WriteString(b.String()); err != nil {
				f.Close()
				return 0, 0, fmt.Errorf("append commit-log: %w", err)
			}
			if err := f.Close(); err != nil {
				return 0, 0, fmt.Errorf("close commit-log: %w", err)
			}
			v.stamp(logPath)
		}
	}

	// Advance the anchor last: if the append failed we never get here, so the
	// anchor never claims coverage the log does not have.
	if err := atomicfile.Write(v.Root, anchorPath, []byte(newAnchor+"\n")); err != nil {
		return 0, 0, fmt.Errorf("write commit-log anchor: %w", err)
	}
	return appended, skipped, nil
}
