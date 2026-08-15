// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

func TestRunSessionsEmpty(t *testing.T) {
	v := testVault(t)
	var buf bytes.Buffer
	code := runSessions(v, "test-proj", sessionsQuery{limit: 10, asJSON: false}, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	if !strings.Contains(buf.String(), "No sessions found") {
		t.Errorf("expected no sessions message: %s", buf.String())
	}
}

func TestRunSessionsWithData(t *testing.T) {
	v := testVault(t)
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "First", Tag: "impl", FrictionScore: 25,
	}, "body1")
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-02", Title: "Second", Tag: "debug",
	}, "body2")
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-03", Title: "Third",
	}, "body3")

	var buf bytes.Buffer
	code := runSessions(v, "test-proj", sessionsQuery{limit: 10, asJSON: false}, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	out := buf.String()
	if !strings.Contains(out, "DATE") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "First") {
		t.Error("missing session 1")
	}
	if !strings.Contains(out, "Second") {
		t.Error("missing session 2")
	}
	if !strings.Contains(out, "25") {
		t.Error("missing friction score")
	}
}

func TestRunSessionsLimit(t *testing.T) {
	v := testVault(t)
	for range 5 {
		v.WriteSession("test-proj", storage.SessionMeta{
			Date: "2026-04-01", Title: "Session",
		}, "body")
	}

	var buf bytes.Buffer
	code := runSessions(v, "test-proj", sessionsQuery{limit: 2, asJSON: false}, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}
	// Should only show last 2 sessions.
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	// 1 header + 2 data lines = 3
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (header + 2 data), got %d", len(lines))
	}
}

func TestRunSessionsJSON(t *testing.T) {
	v := testVault(t)
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "Test", Tag: "impl",
	}, "body")

	var buf bytes.Buffer
	code := runSessions(v, "test-proj", sessionsQuery{limit: 10, asJSON: true}, &buf)
	if code != cli.ExitOK {
		t.Errorf("exit code = %d", code)
	}

	var sessions []storage.SessionMeta
	if err := json.Unmarshal(buf.Bytes(), &sessions); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session, got %d", len(sessions))
	}
}

func TestRunSessionsTitleTruncation(t *testing.T) {
	v := testVault(t)
	longTitle := strings.Repeat("A", 60)
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: longTitle,
	}, "body")

	var buf bytes.Buffer
	runSessions(v, "test-proj", sessionsQuery{limit: 10, asJSON: false}, &buf)
	out := buf.String()
	if !strings.Contains(out, "...") {
		t.Error("expected truncation with ellipsis")
	}
}

// --- host attribution readers -------------------------------------------------
//
// `vp sessions --hosts` is the answer to the question the host-parity epic is
// named for. Before it, SessionMeta.Host had one consumer (an auto-inline
// decision on the MCP path) and no reader at all, so "distinguishable in the
// vault by inspection" meant opening frontmatter by hand.

// hostSessionsVault stands up a project whose sessions span all four attribution
// states that occur in the live vault: two named hosts, an explicit unknown, and
// notes carrying no claim at all.
func hostSessionsVault(t *testing.T) *storage.Vault {
	t.Helper()
	v := testVault(t)
	write := func(date, title, host, source, entrypoint string) {
		v.WriteSession("test-proj", storage.SessionMeta{
			Date: date, Title: title,
			Host: host, HostSource: source, Entrypoint: entrypoint,
		}, "body")
	}
	write("2026-04-01", "claude one", storage.HostClaudeCode, storage.HostSourceDerived, "cli")
	write("2026-04-02", "claude two", storage.HostClaudeCode, storage.HostSourceDerived, "cli")
	write("2026-04-03", "claude acp", storage.HostClaudeCode, storage.HostSourceDerived, storage.EntrypointUnknown)
	write("2026-04-04", "grok one", storage.HostGrok, storage.HostSourceDerived, storage.EntrypointUnknown)
	write("2026-04-05", "looked and failed", storage.HostUnknown, storage.HostSourceUnknown, storage.EntrypointUnknown)
	write("2026-04-06", "never looked", "", "", "")
	return v
}

// 🔴 THE LOAD-BEARING TEST. An explicit "unknown" and an absent key are DIFFERENT
// FACTS — "a writer looked and could not tell" versus "no claim was ever
// recorded" — and any reader that merges them lets a caller infer a host from an
// absence. That inference is the rule this task had to withdraw after the vault
// falsified it, so it must not reappear inside the tool that reports the data.
func TestHostMixKeepsUnknownAndNoClaimApart(t *testing.T) {
	v := hostSessionsVault(t)

	var buf bytes.Buffer
	if code := runSessions(v, "test-proj", sessionsQuery{limit: 100, hostMix: true, asJSON: true}, &buf); code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}

	var rows []hostCount
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	got := map[string]int{}
	for _, r := range rows {
		got[r.Host] = r.Sessions
	}
	for host, want := range map[string]int{
		storage.HostClaudeCode: 3,
		storage.HostGrok:       1,
		storage.HostUnknown:    1,
		hostNoClaim:            1,
	} {
		if got[host] != want {
			t.Errorf("host %q = %d sessions, want %d (rows: %v)", host, got[host], want, got)
		}
	}
	if _, ok := got[storage.HostUnknown]; !ok {
		t.Error("the explicit unknown was folded into another bucket")
	}
	if _, ok := got[hostNoClaim]; !ok {
		t.Error("the no-claim notes were folded into another bucket")
	}
}

// The entrypoint breakdown is what separates a Claude Code CLI session from
// Claude Code reached some other way — the ACP pane does not inherit
// CLAUDE_CODE_ENTRYPOINT. Without it, --hosts answers "which application" but
// not the question host-parity actually asks.
func TestHostMixBreaksDownByEntrypoint(t *testing.T) {
	v := hostSessionsVault(t)

	rows := hostMix(mustSessions(t, v))
	var claude *hostCount
	for i := range rows {
		if rows[i].Host == storage.HostClaudeCode {
			claude = &rows[i]
		}
	}
	if claude == nil {
		t.Fatal("no claude-code row")
	}
	if claude.Entrypoints["cli"] != 2 {
		t.Errorf("claude-code cli entrypoints = %d, want 2", claude.Entrypoints["cli"])
	}
	if claude.Entrypoints[storage.EntrypointUnknown] != 1 {
		t.Errorf("claude-code unknown entrypoints = %d, want 1 — the CLI/non-CLI split is the discrimination this field exists for",
			claude.Entrypoints[storage.EntrypointUnknown])
	}
}

// Most frequent first, ties broken by name, so a human diffing two runs sees
// data changing rather than map iteration order.
func TestHostMixIsOrderedAndStable(t *testing.T) {
	v := hostSessionsVault(t)
	sessions := mustSessions(t, v)

	first := hostMix(sessions)
	if len(first) == 0 {
		t.Fatal("no rows")
	}
	if first[0].Host != storage.HostClaudeCode {
		t.Errorf("first row = %q, want the most frequent host %q", first[0].Host, storage.HostClaudeCode)
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].Sessions < first[i].Sessions {
			t.Errorf("rows not ordered by descending count: %v", first)
		}
		if first[i-1].Sessions == first[i].Sessions && first[i-1].Host > first[i].Host {
			t.Errorf("ties not broken by name: %q before %q", first[i-1].Host, first[i].Host)
		}
	}
	for range 8 {
		if second := hostMix(sessions); !sameOrder(first, second) {
			t.Fatalf("host mix order is not stable across runs:\n%v\n%v", first, second)
		}
	}
}

// The table must SAY what an absence means, every time it counts one. A bare
// number in a column invites exactly the inference this task had to withdraw.
func TestHostMixTableExplainsTheNoClaimBucket(t *testing.T) {
	v := hostSessionsVault(t)

	var buf bytes.Buffer
	runSessions(v, "test-proj", sessionsQuery{limit: 100, hostMix: true}, &buf)
	out := buf.String()

	if !strings.Contains(out, "is not a host") {
		t.Errorf("no-claim caveat missing from the report:\n%s", out)
	}
	if !strings.Contains(out, "not evidence about which host ran") {
		t.Errorf("report does not say what an absence fails to prove:\n%s", out)
	}
}

// ...and must NOT print the caveat when nothing was counted into that bucket:
// a warning that fires unconditionally is a warning nobody reads.
func TestHostMixOmitsTheCaveatWhenEveryNoteMakesAClaim(t *testing.T) {
	v := testVault(t)
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "attributed",
		Host: storage.HostClaudeCode, HostSource: storage.HostSourceDerived, Entrypoint: "cli",
	}, "body")

	var buf bytes.Buffer
	runSessions(v, "test-proj", sessionsQuery{limit: 100, hostMix: true}, &buf)
	if strings.Contains(buf.String(), "is not a host") {
		t.Errorf("caveat printed with no no-claim sessions to explain:\n%s", buf.String())
	}
}

// The filter selects on host, including the two states that are not host names.
func TestRunSessionsHostFilter(t *testing.T) {
	v := hostSessionsVault(t)

	for _, tc := range []struct {
		host string
		want int
	}{
		{storage.HostClaudeCode, 3},
		{storage.HostGrok, 1},
		{storage.HostUnknown, 1},
		{hostNoClaim, 1},
		{"CLAUDE-CODE", 3}, // case-insensitive
		{"zed", 0},
	} {
		var buf bytes.Buffer
		code := runSessions(v, "test-proj", sessionsQuery{limit: 100, host: tc.host, asJSON: true}, &buf)
		if code != cli.ExitOK {
			t.Fatalf("--host %s: exit code = %d", tc.host, code)
		}
		var sessions []storage.SessionMeta
		if err := json.Unmarshal(buf.Bytes(), &sessions); err != nil {
			t.Fatalf("--host %s: invalid JSON: %v", tc.host, err)
		}
		if len(sessions) != tc.want {
			t.Errorf("--host %s returned %d sessions, want %d", tc.host, len(sessions), tc.want)
		}
	}
}

// The filter must run BEFORE the --last window, or "the last 2 Grok sessions"
// silently means "whichever of the last 2 sessions were Grok" — which is zero
// here, and the empty answer looks identical to "Grok never ran".
func TestRunSessionsHostFilterPrecedesTheLimit(t *testing.T) {
	v := testVault(t)
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "grok early",
		Host: storage.HostGrok, HostSource: storage.HostSourceDerived,
	}, "body")
	for i := range 5 {
		v.WriteSession("test-proj", storage.SessionMeta{
			Date: "2026-04-0" + string(rune('2'+i)), Title: "claude later",
			Host: storage.HostClaudeCode, HostSource: storage.HostSourceDerived,
		}, "body")
	}

	var buf bytes.Buffer
	code := runSessions(v, "test-proj", sessionsQuery{limit: 2, host: storage.HostGrok, asJSON: true}, &buf)
	if code != cli.ExitOK {
		t.Fatalf("exit code = %d", code)
	}
	var sessions []storage.SessionMeta
	if err := json.Unmarshal(buf.Bytes(), &sessions); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want the 1 Grok session — the limit was applied before the filter", len(sessions))
	}
}

// A filtered-empty result must not read as "no sessions exist".
func TestRunSessionsHostFilterEmptyIsDistinct(t *testing.T) {
	v := hostSessionsVault(t)

	var buf bytes.Buffer
	runSessions(v, "test-proj", sessionsQuery{limit: 100, host: "zed"}, &buf)
	out := buf.String()
	if !strings.Contains(out, `attributed to "zed"`) {
		t.Errorf("empty filter result does not name the filter:\n%s", out)
	}
	if strings.Contains(out, "No sessions found") {
		t.Errorf("filtered-empty reported as project-empty:\n%s", out)
	}
}

// The default listing carries the host, so the field is visible without asking
// for it. Nothing read it at all before this change.
func TestRunSessionsListingShowsHost(t *testing.T) {
	v := hostSessionsVault(t)

	var buf bytes.Buffer
	runSessions(v, "test-proj", sessionsQuery{limit: 100}, &buf)
	out := buf.String()
	for _, want := range []string{"HOST", storage.HostClaudeCode, storage.HostGrok, hostNoClaim} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q:\n%s", want, out)
		}
	}
}

func mustSessions(t *testing.T, v *storage.Vault) []storage.SessionMeta {
	t.Helper()
	sessions, err := v.ListSessions("test-proj", "", "", 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	return sessions
}

func sameOrder(a, b []hostCount) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Host != b[i].Host || a[i].Sessions != b[i].Sessions {
			return false
		}
	}
	return true
}

// A host name wider than the header must not shove the remaining columns out of
// alignment. Found by running --hosts against the live vault, which already
// holds a 22-character clientInfo name from the MCP path — the hook's own
// vocabulary is closed and short, so fixtures alone would never have shown it.
func TestHostMixColumnFitsTheWidestHost(t *testing.T) {
	const wide = "grok-shell-vibe-palace"
	v := testVault(t)
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-01", Title: "wide", Host: wide, HostSource: storage.HostSourceDeclared,
	}, "body")
	v.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-04-02", Title: "narrow", Host: storage.HostGrok, HostSource: storage.HostSourceDerived,
	}, "body")

	var buf bytes.Buffer
	runSessions(v, "test-proj", sessionsQuery{limit: 100, hostMix: true}, &buf)

	var starts []int
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		if i := strings.Index(line, "SESSIONS"); i >= 0 {
			starts = append(starts, i)
			continue
		}
		// The count column begins at the first digit after the host name.
		if strings.HasPrefix(line, wide) || strings.HasPrefix(line, storage.HostGrok) {
			starts = append(starts, strings.IndexAny(line, "0123456789"))
		}
	}
	if len(starts) != 3 {
		t.Fatalf("expected header + 2 rows, got %d columns: %q", len(starts), buf.String())
	}
	for i := 1; i < len(starts); i++ {
		if starts[i] != starts[0] {
			t.Errorf("count column misaligned: header at %d, row at %d\n%s", starts[0], starts[i], buf.String())
		}
	}
}
