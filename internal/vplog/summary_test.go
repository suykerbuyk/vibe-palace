// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vplog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vp.log")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// TestSummarizeReadsABoundedTailNotTheWholeLog is the performance constraint made
// executable, and it is not a micro-optimization.
//
// Summarize runs on the vp_bootstrap_context path — the hottest call in the system,
// which iteration 190 spent a whole session taking from ~0.4 s to 0.012 s — and the
// log is capped at 8 MiB. Scanning it end to end on every session start would hand
// that win straight back.
//
// So: bury a warning far below the tail window under megabytes of noise, and assert
// Summarize does NOT see it, and SAYS it did not (Truncated). A summary that silently
// covers only part of the log while looking authoritative is the same lie in a new place.
func TestSummarizeReadsABoundedTailNotTheWholeLog(t *testing.T) {
	// One ancient warning, then enough filler to push it well past TailBytes.
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"ancient: buried far above the tail"}`, nowStamp()),
	}
	filler := strings.Repeat("x", 512)
	for len(lines)*560 < TailBytes*3 {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"INFO","msg":"noise %s"}`, nowStamp(), filler))
	}
	lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"recent: inside the tail"}`, nowStamp()))

	s := Summarize(writeLog(t, lines), 24, 20)

	if !s.Truncated {
		t.Fatal("the log is far larger than TailBytes but Truncated is false — " +
			"a partial view is being presented as a complete one")
	}
	joined := ""
	for _, e := range s.RecentWarns {
		joined += e.Msg + "\n"
	}
	if !strings.Contains(joined, "recent: inside the tail") {
		t.Errorf("the tail's own warning was missed:\n%s", joined)
	}
	if strings.Contains(joined, "ancient: buried far above the tail") {
		t.Error("Summarize read past the tail window — it is scanning the whole log, " +
			"which is what the bounded read exists to prevent")
	}
}

// TestSummarizeDropsThePartialFirstLine guards the seam the bounded read creates: a
// mid-file seek almost certainly lands INSIDE a line, and that fragment is not a
// record. Parsing it would at best be ignored and at worst mis-parse.
func TestSummarizeDropsThePartialFirstLine(t *testing.T) {
	var lines []string
	bytes := 0
	for bytes < TailBytes*2 {
		l := fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry"}`, nowStamp())
		lines = append(lines, l)
		bytes += len(l) + 1 // +1 for the newline writeLog adds
	}
	s := Summarize(writeLog(t, lines), 24, 1000)

	if !s.Truncated {
		t.Fatal("test is not exercising a truncated read")
	}
	// Every surviving entry must be a fully-parsed record, not a fragment.
	for _, e := range s.RecentWarns {
		if e.Msg != "cat: entry" || e.Level != "WARN" {
			t.Fatalf("a partial line was parsed as a record: %+v", e)
		}
	}
}

// TestSummarizeUnknownNotHealthyOnMissingLog: the headline invariant, at the level
// that actually implements it.
func TestSummarizeUnknownNotHealthyOnMissingLog(t *testing.T) {
	s := Summarize(filepath.Join(t.TempDir(), "does-not-exist.log"), 24, 20)

	if s.Status == StatusHealthy {
		t.Fatal("a log that does not exist was reported HEALTHY")
	}
	if s.Status != StatusUnknown {
		t.Errorf("status = %q, want %q", s.Status, StatusUnknown)
	}
	if s.Healthy() {
		t.Error("Healthy() is true for an unknown status")
	}
	if s.ScanError == "" {
		t.Error("status is unknown but nothing says why")
	}
}

// TestSummarizeStatusOverAllEntriesNotTheDisplayCap — an ERROR outside the display
// cap must still set the status. Deriving status from the capped list is how the tool
// reported "warnings" while holding an ERROR it had itself counted.
func TestSummarizeStatusOverAllEntriesNotTheDisplayCap(t *testing.T) {
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"ERROR","msg":"boom: it broke"}`, nowStamp()),
	}
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry %d"}`, nowStamp(), i))
	}

	s := Summarize(writeLog(t, lines), 24, 2)

	if len(s.RecentWarns) != 2 {
		t.Fatalf("display cap not applied: %d entries", len(s.RecentWarns))
	}
	for _, e := range s.RecentWarns {
		if e.Level == "ERROR" {
			t.Fatal("test is not exercising the defect: the ERROR is inside the display cap")
		}
	}
	if s.Status != StatusErrors {
		t.Errorf("status = %q, want %q — the ERROR was counted but did not set the status", s.Status, StatusErrors)
	}
}

// TestSummarizeRecentWarnsIsNewestN — the field is called recent_warns.
func TestSummarizeRecentWarnsIsNewestN(t *testing.T) {
	var lines []string
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry %d"}`, nowStamp(), i))
	}
	s := Summarize(writeLog(t, lines), 24, 3)

	want := []string{"cat: entry 7", "cat: entry 8", "cat: entry 9"}
	if len(s.RecentWarns) != len(want) {
		t.Fatalf("got %d entries, want %d", len(s.RecentWarns), len(want))
	}
	for i, w := range want {
		if s.RecentWarns[i].Msg != w {
			t.Errorf("recent_warns[%d] = %q, want %q — this is the OLDEST-N bug", i, s.RecentWarns[i].Msg, w)
		}
	}
}

// TestSummarizeExcludesCallerFaultFromStatus is the whole point of the task made
// executable: a WARN stamped fault="caller" is a guard that WORKED (the tool
// correctly rejected bad input), so it must NOT move status off healthy — while
// still being COUNTED as caller friction so the information is not lost.
func TestSummarizeExcludesCallerFaultFromStatus(t *testing.T) {
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"mcp.makeHandler: handler error","fault":"caller"}`, nowStamp()),
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"mcp.makeHandler: validation failed","fault":"caller"}`, nowStamp()),
	}
	s := Summarize(writeLog(t, lines), 24, 20)

	if s.Status != StatusHealthy {
		t.Errorf("status = %q, want %q — a caller-fault WARN moved health amber, which is the exact bug this task exists to kill", s.Status, StatusHealthy)
	}
	if s.CallerFriction != 2 {
		t.Errorf("caller_friction = %d, want 2 — a caller error must stay VISIBLE as friction; silencing it is a regression, not a fix", s.CallerFriction)
	}
}

// TestSummarizePlainWarnStillGoesAmber — an unclassified (internal) WARN must
// still set status=warnings. The fix must not turn EVERY warning green; only
// fault="caller" is excluded. A line with no fault field defaults to internal.
func TestSummarizePlainWarnStillGoesAmber(t *testing.T) {
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"capture: archive not linked"}`, nowStamp()),
	}
	s := Summarize(writeLog(t, lines), 24, 20)

	if s.Status != StatusWarnings {
		t.Errorf("status = %q, want %q — an internal WARN must still go amber", s.Status, StatusWarnings)
	}
	if s.CallerFriction != 0 {
		t.Errorf("caller_friction = %d, want 0 — an unstamped WARN is not caller friction", s.CallerFriction)
	}
}

// TestSummarizeOperationalWarnGoesAmber — fault="operational" (the surface-gate
// refusal: vault ahead of binary) is neither caller nor silent. Per the operator
// decision it STAYS amber, distinct from caller friction.
func TestSummarizeOperationalWarnGoesAmber(t *testing.T) {
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"mcp.makeHandler: surface gate refused","fault":"operational"}`, nowStamp()),
	}
	s := Summarize(writeLog(t, lines), 24, 20)

	if s.Status != StatusWarnings {
		t.Errorf("status = %q, want %q — an operational surface-gate refusal should show amber", s.Status, StatusWarnings)
	}
	if s.CallerFriction != 0 {
		t.Errorf("caller_friction = %d, want 0 — operational is not caller friction", s.CallerFriction)
	}
}

// TestSummarizeCallerFaultDoesNotMaskARealError — a caller WARN alongside a
// genuine internal ERROR must not swallow the error: status is still errors, and
// the caller line is still counted.
func TestSummarizeCallerFaultDoesNotMaskARealError(t *testing.T) {
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"mcp.makeHandler: handler error","fault":"caller"}`, nowStamp()),
		fmt.Sprintf(`{"time":"%s","level":"ERROR","msg":"boom: it broke"}`, nowStamp()),
	}
	s := Summarize(writeLog(t, lines), 24, 20)

	if s.Status != StatusErrors {
		t.Errorf("status = %q, want %q — a caller line must not mask a real error", s.Status, StatusErrors)
	}
	if s.CallerFriction != 1 {
		t.Errorf("caller_friction = %d, want 1", s.CallerFriction)
	}
	if e := s.RecentWarns; len(e) > 0 && e[0].Fault != FaultCaller {
		t.Errorf("caller line surfaced in recent_warns without its Fault field: %+v", e[0])
	}
}

// TestSummarizeCarriesTheProjectAttribution is the acceptance gate for
// vp-health-drops-the-project-attribute-it-parses.
//
// Summarize already unmarshals every line into map[string]any, so `project` was
// sitting in that map and being dropped while time/level/msg/fault were copied
// out. The cost was paid once already: the iteration-263 attribution work — WHICH
// project was responsible for a run of warnings — could not be done through
// vp_health at all and was done with `jq` over the raw log, which no agent-facing
// tool reaches.
//
// So this does not assert a struct tag. It reconstructs the 263 QUESTION —
// "which project dominates this window?" — and answers it from Summarize output
// alone. Mutation: stop copying the field and this fails.
func TestSummarizeCarriesTheProjectAttribution(t *testing.T) {
	ts := nowStamp()
	var lines []string
	// 19 of 21 from one project, the shape of the 263 finding.
	for i := 0; i < 19; i++ {
		lines = append(lines, fmt.Sprintf(
			`{"time":%q,"level":"WARN","msg":"bootstrap: payload over budget","project":"noisy-proj","fault":"internal"}`, ts))
	}
	for i := 0; i < 2; i++ {
		lines = append(lines, fmt.Sprintf(
			`{"time":%q,"level":"WARN","msg":"bootstrap: payload over budget","project":"quiet-proj","fault":"internal"}`, ts))
	}
	// A line with no project at all: most log lines carry none, and the field
	// must stay empty rather than inventing an attribution.
	lines = append(lines, fmt.Sprintf(
		`{"time":%q,"level":"WARN","msg":"mcp.makeHandler: handler error","fault":"internal"}`, ts))

	s := Summarize(writeLog(t, lines), 24, 1000)

	if len(s.RecentWarns) != 22 {
		t.Fatalf("RecentWarns = %d entries, want 22", len(s.RecentWarns))
	}

	// The 263 question, answered from the summary and nothing else.
	byProject := map[string]int{}
	unattributed := 0
	for _, e := range s.RecentWarns {
		if e.Project == "" {
			unattributed++
			continue
		}
		byProject[e.Project]++
	}
	if byProject["noisy-proj"] != 19 {
		t.Errorf("noisy-proj = %d, want 19 — vp_health cannot attribute warnings to a project, which is what forced iteration 263 to jq the raw log; byProject=%v", byProject["noisy-proj"], byProject)
	}
	if byProject["quiet-proj"] != 2 {
		t.Errorf("quiet-proj = %d, want 2; byProject=%v", byProject["quiet-proj"], byProject)
	}
	if unattributed != 1 {
		t.Errorf("unattributed = %d, want 1 — a line carrying no project must stay empty, never be given one", unattributed)
	}
}

// TestSummarizeDoesNotResurrectTheRationingAttributes pins the allow-list's
// BOUNDARY, which is the half a "does it surface project?" test cannot see.
//
// The task named four attributes to consider: project, max_tokens,
// estimated_tokens and shed. Only project is live. The other three belonged to
// the rationing machinery deleted in Phase 2 — no emitter can produce them
// again — but the log is APPEND-ONLY and still carries them on old lines, so
// deriving an allow-list from the log rather than from the emitters would
// resurrect three ghosts. Entry must not grow to mirror them.
func TestSummarizeDoesNotResurrectTheRationingAttributes(t *testing.T) {
	ts := nowStamp()
	// A pre-Phase-2 fossil line, exactly as such lines still appear in the log.
	line := fmt.Sprintf(`{"time":%q,"level":"WARN","msg":"bootstrap: payload exceeds max_tokens after shedding everything sheddable","project":"fossil-proj","max_tokens":25000,"estimated_tokens":31000,"shed":true}`, ts)

	s := Summarize(writeLog(t, []string{line}), 24, 20)
	if len(s.RecentWarns) != 1 {
		t.Fatalf("RecentWarns = %d, want 1", len(s.RecentWarns))
	}
	e := s.RecentWarns[0]
	if e.Project != "fossil-proj" {
		t.Errorf("Project = %q, want fossil-proj — the live attribute must survive even on a fossil line", e.Project)
	}

	// The Entry that reaches an agent must carry the allow-listed field and
	// nothing else from the deleted machinery. Marshalling is the honest check:
	// it is exactly what the tool hands out.
	//
	// Assert on the KEY SET, not on a substring of the blob. This fossil's own
	// message is "payload exceeds max_tokens after shedding everything
	// sheddable", so a substring check matches the MESSAGE and reports a field
	// that is not there — a test failing for a reason unrelated to its claim.
	blob, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	for _, ghost := range []string{"max_tokens", "estimated_tokens", "shed"} {
		if _, present := got[ghost]; present {
			t.Errorf("Entry carries a %q FIELD — that attribute belongs to machinery deleted in Phase 2 and only survives on old log lines; the allow-list is derived from live EMITTERS, not from the log: %s", ghost, blob)
		}
	}
	// And the allow-list really is closed: only the four documented keys plus
	// project may appear, so a later "just one more attribute" edit is caught.
	allowed := map[string]bool{"time": true, "level": true, "msg": true, "fault": true, "project": true}
	for k := range got {
		if !allowed[k] {
			t.Errorf("Entry grew an un-allow-listed field %q — Summarize holds EVERY attribute in its map[string]any, so growth here is free and turns the health summary into a log viewer: %s", k, blob)
		}
	}
}
