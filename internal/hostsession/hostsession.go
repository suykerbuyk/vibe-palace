// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package hostsession resolves the live session id of the AI host process
// that spawned this server, by reading the host's own per-process session map.
//
// Claude Code maintains ~/.claude/sessions/<pid>.json — one file per host
// instance, keyed by the host's OS pid, created at process start, rewritten
// synchronously on every session change (including /clear), and deleted on
// exit. The vp MCP server is a DIRECT child of the host process (no
// intermediate shell), so os.Getppid() names the host, and concurrent host
// instances can never collide on a key the way cwd- or env-based schemes do.
//
// The one hole in a pid key is pid REUSE over time: a stale file left by a
// crashed host could name a session that a later, unrelated process with the
// same pid never ran. The file carries the writer's /proc start time
// (procStart) precisely so a reader can tell "the process that wrote this" from
// "a process that merely has the same number"; ClaudeSessionID refuses the file
// on any mismatch. The same-pid session-SWITCH case (a /clear mints a new id in
// the same process, which procStart cannot distinguish) was measured live on
// 2026-07-16: the host rewrites the file within ~54 ms of the /clear, before
// the new session can receive a message, so no freshness heuristic is needed —
// see the capture-note-archive-link-never-closes task for the probe protocol.
//
// The resolver NEVER guesses: every failure — no file, unreadable, malformed,
// pid or start-time mismatch — degrades to "", meaning "cannot be confirmed".
// A wrong id is worse than no id: the archive linker matches on the stamped id
// alone, so a wrong stamp mis-links another session's transcript, while an
// empty one just leaves the note honestly unlinked.
package hostsession

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ProcStartFunc returns /proc/<pid>/stat field 22 (starttime, in clock ticks
// since boot) for pid, or an error. Injected so tests can exercise the
// pid-reuse guard without faking /proc.
type ProcStartFunc func(pid int) (string, error)

// sessionFile is the subset of ~/.claude/sessions/<pid>.json this resolver
// reads. procStart is a JSON STRING in the live format (e.g. "20850305") —
// an int field would make Unmarshal fail on every real file.
type sessionFile struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	ProcStart string `json:"procStart"`
}

// ClaudeSessionID returns the live Claude Code session id for the process
// that spawned the caller (parentPID), or "" when it cannot be CONFIRMED.
// It never guesses: any read, parse, or guard failure returns "".
//
// claudeHome is the host's state directory (see archive.ClaudeHome);
// procStart supplies the live /proc start time for the pid-reuse guard
// (pass ReadProcStart outside tests).
func ClaudeSessionID(parentPID int, claudeHome string, procStart ProcStartFunc) string {
	if parentPID <= 0 || claudeHome == "" || procStart == nil {
		return ""
	}
	path := filepath.Join(claudeHome, "sessions", strconv.Itoa(parentPID)+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var f sessionFile
	if err := json.Unmarshal(b, &f); err != nil {
		return ""
	}
	// The filename key and the body must agree about whose file this is.
	if f.PID != parentPID {
		return ""
	}
	// Pid-reuse guard: the file is only trustworthy if the process it names is
	// the process that is our parent NOW. A reused pid is not the host that
	// wrote the file.
	live, err := procStart(parentPID)
	if err != nil || live == "" || live != f.ProcStart {
		return ""
	}
	return f.SessionID
}

// ReadProcStart returns /proc/<pid>/stat field 22 (starttime) for pid. On
// platforms without /proc the read fails and the resolver degrades to "" —
// an honest unknown, not an error the capture path has to handle.
func ReadProcStart(pid int) (string, error) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	return parseProcStartTime(b)
}

// parseProcStartTime extracts field 22 (starttime) from a /proc/<pid>/stat
// line. Field 2 (comm) is parenthesized and may itself contain spaces and ')'
// — "(tmux: server)" is a real comm — so splitting the whole line on
// whitespace is a bug. Split at the LAST ')': the remainder begins at field 3,
// so field 22 sits at index 19 of the remaining fields.
func parseProcStartTime(stat []byte) (string, error) {
	s := string(stat)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return "", fmt.Errorf("proc stat: no comm terminator")
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 20 {
		return "", fmt.Errorf("proc stat: %d fields after comm, want >= 20", len(fields))
	}
	return fields[19], nil
}
