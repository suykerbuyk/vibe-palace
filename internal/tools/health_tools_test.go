// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vplog"
)

// TestHealthToolUnknownWhenItCannotReadTheLog is the inversion of the test that
// used to live here.
//
// The old TestHealthToolHealthy asserted `status == "healthy"` against a vault with
// NO LOG FILE — it ENCODED THE BUG AS INTENDED BEHAVIOUR and went green on it. That
// is the entire disease this task exists to cure, sitting in the test suite of the
// tool built to detect it: a tool that cannot READ the log reporting that the system
// is FINE.
//
// A missing log is the normal state on a fresh host, and the PERMANENT state for any
// process that never initialized the logger. "I have no information" is not "nothing
// is wrong."
func TestHealthToolUnknownWhenItCannotReadTheLog(t *testing.T) {
	vault := storage.NewVault(t.TempDir()) // no log file at all
	hr := runHealth(t, vault, healthParams{})

	if hr.Status == "healthy" {
		t.Fatal("vp_health reported HEALTHY for a log it cannot even read — " +
			"it is asserting a fact it does not have")
	}
	if hr.Status != vplog.StatusUnknown {
		t.Errorf("status = %q, want %q", hr.Status, vplog.StatusUnknown)
	}
	if hr.ScanError == "" {
		t.Error("status is unknown but scan_error does not say why")
	}
}

// TestHealthToolHealthyOnlyWithACleanLog pins the other side: healthy means a log
// was actually READ and had nothing to report.
func TestHealthToolHealthyOnlyWithACleanLog(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)
	writeHealthLog(t, vault, []string{
		fmt.Sprintf(`{"time":"%s","level":"INFO","msg":"all is well"}`, now),
	})

	hr := runHealth(t, vault, healthParams{})
	if hr.Status != vplog.StatusHealthy {
		t.Errorf("status = %q, want healthy for a readable log with no warnings", hr.Status)
	}
	if hr.ScanError != "" {
		t.Errorf("scan_error = %q, want empty", hr.ScanError)
	}
}

func TestHealthToolWarnings(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entries := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"entity extraction: add entity failed","err":"disk full"}`, now),
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"entity extraction: duplicate entity skipped","entity":"test"}`, now),
		fmt.Sprintf(`{"time":"%s","level":"INFO","msg":"normal operation"}`, now),
	}

	logPath := filepath.Join(logDir, "vp.log")
	var content string
	for _, e := range entries {
		content += e + "\n"
	}
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(healthParams{})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(vplog.Summary)
	if hr.Status != "warnings" {
		t.Errorf("status = %q, want warnings", hr.Status)
	}
	if len(hr.RecentWarns) != 2 {
		t.Errorf("recent warns = %d, want 2", len(hr.RecentWarns))
	}
	if hr.WarnCounts["entity extraction"] != 2 {
		t.Errorf("warn count for entity extraction = %d, want 2", hr.WarnCounts["entity extraction"])
	}
}

func TestHealthToolErrors(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	entry := fmt.Sprintf(`{"time":"%s","level":"ERROR","msg":"critical failure"}`, now)
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(entry+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(healthParams{})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(vplog.Summary)
	if hr.Status != "errors" {
		t.Errorf("status = %q, want errors", hr.Status)
	}
}

func TestHealthToolMalformedLines(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	content := "this is not json\n" +
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"valid warn"}`, now) + "\n" +
		"{broken json\n"
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(healthParams{})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(vplog.Summary)
	if len(hr.RecentWarns) != 1 {
		t.Errorf("recent warns = %d, want 1 (malformed lines skipped)", len(hr.RecentWarns))
	}
}

func TestHealthToolOldEntries(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	entry := fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"old warning"}`, old)
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(entry+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(healthParams{})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hr := result.(vplog.Summary)
	if hr.Status != "healthy" {
		t.Errorf("status = %q, want healthy (old entries excluded)", hr.Status)
	}
	if len(hr.RecentWarns) != 0 {
		t.Errorf("recent warns = %d, want 0", len(hr.RecentWarns))
	}
}

// writeHealthLog writes the given raw log lines to the vault's vp.log and
// returns the HealthTool ready to invoke.
func writeHealthLog(t *testing.T, vault *storage.Vault, lines []string) {
	t.Helper()
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	logPath := filepath.Join(logDir, "vp.log")
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func runHealth(t *testing.T, vault *storage.Vault, params any) vplog.Summary {
	t.Helper()
	tool := HealthTool(vault)
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return result.(vplog.Summary)
}

// TestHealthToolDefaultBehavior confirms that omitting hours/limit reproduces
// today's contract exactly: a 24h window and a 20-entry recent_warns cap.
func TestHealthToolDefaultBehavior(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)

	var lines []string
	// 25 in-window warns (> the 20 cap) to prove the default cap.
	for i := range 25 {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry %d"}`, now, i))
	}
	// An entry outside the default 24h window must be excluded.
	lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: stale"}`, old))
	writeHealthLog(t, vault, lines)

	// projectParams marshals to only {"project":...} — the true "no params" case.
	hr := runHealth(t, vault, healthParams{})

	if len(hr.RecentWarns) != 20 {
		t.Errorf("recent warns = %d, want 20 (default cap)", len(hr.RecentWarns))
	}
	// warn_counts tallies every in-window entry (25), independent of the cap;
	// the 48h-old entry is excluded by the default 24h window.
	if hr.WarnCounts["cat"] != 25 {
		t.Errorf("warn count cat = %d, want 25 (all in-window, stale excluded)", hr.WarnCounts["cat"])
	}
}

// TestHealthToolLimitCapsOnlyRecentWarns is THE key nuance: limit caps the
// recent_warns list but warn_counts still tallies every in-window entry.
func TestHealthToolLimitCapsOnlyRecentWarns(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)

	var lines []string
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry %d"}`, now, i))
	}
	writeHealthLog(t, vault, lines)

	hr := runHealth(t, vault, healthParams{Limit: 3})

	if len(hr.RecentWarns) != 3 {
		t.Errorf("recent warns = %d, want 3 (capped by limit)", len(hr.RecentWarns))
	}
	if hr.WarnCounts["cat"] != 10 {
		t.Errorf("warn count cat = %d, want 10 (limit must NOT cap warn_counts)", hr.WarnCounts["cat"])
	}

	// WHICH three. The old version of this test asserted only the LENGTH — a test
	// named for recency that never tested recency, and it went green on the bug: the
	// implementation appended while `len(RecentWarns) < limit` as it scanned FORWARD
	// through an append-only file, so it kept the OLDEST three in a field called
	// recent_warns. Entries 0,1,2 passing for "recent" is exactly the failure.
	want := []string{"cat: entry 7", "cat: entry 8", "cat: entry 9"}
	for i, w := range want {
		if hr.RecentWarns[i].Msg != w {
			t.Errorf("recent_warns[%d] = %q, want %q — recent_warns is holding the OLDEST entries, not the newest",
				i, hr.RecentWarns[i].Msg, w)
		}
	}
}

// TestHealthStatusSeesErrorsBeyondTheDisplayCap is the defect that fixing the tail
// alone does NOT fix, and it had no test at all.
//
// status used to be computed by looping over RecentWarns — the CAPPED display list.
// So an ERROR outside the cap was tallied into warn_counts and then never set
// status: "errors". The tool would report "warnings" while holding an ERROR it had
// counted itself — contradicting itself inside its own payload.
//
// Here the single ERROR is the OLDEST entry and the cap is small, so it falls outside
// the display list in either direction. status must still be "errors".
func TestHealthStatusSeesErrorsBeyondTheDisplayCap(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)

	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"ERROR","msg":"capture: session capture failed"}`, now),
	}
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: entry %d"}`, now, i))
	}
	writeHealthLog(t, vault, lines)

	hr := runHealth(t, vault, healthParams{Limit: 3})

	// The ERROR is deliberately NOT in the displayed list...
	for _, w := range hr.RecentWarns {
		if w.Level == "ERROR" {
			t.Fatal("test is not exercising the defect: the ERROR landed inside the display cap")
		}
	}
	// ...and status must find it anyway.
	if hr.Status != vplog.StatusErrors {
		t.Errorf("status = %q, want %q — an ERROR outside the display cap was counted but did not set the status",
			hr.Status, vplog.StatusErrors)
	}
}

// TestHealthToolHoursWindow confirms hours widens/narrows the window for BOTH
// warn_counts and recent_warns.
func TestHealthToolHoursWindow(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	recent := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	mid := time.Now().UTC().Add(-10 * time.Hour).Format(time.RFC3339)
	lines := []string{
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: recent"}`, recent),
		fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: mid"}`, mid),
	}
	writeHealthLog(t, vault, lines)

	// Narrow 5h window: only the 1h-old entry qualifies.
	narrow := runHealth(t, vault, healthParams{Hours: 5})
	if len(narrow.RecentWarns) != 1 || narrow.WarnCounts["cat"] != 1 {
		t.Errorf("narrow(5h): recent=%d counts=%d, want 1/1", len(narrow.RecentWarns), narrow.WarnCounts["cat"])
	}

	// Wide 48h window: both entries qualify.
	wide := runHealth(t, vault, healthParams{Hours: 48})
	if len(wide.RecentWarns) != 2 || wide.WarnCounts["cat"] != 2 {
		t.Errorf("wide(48h): recent=%d counts=%d, want 2/2", len(wide.RecentWarns), wide.WarnCounts["cat"])
	}
}

// TestHealthToolClamping exercises the clamp bounds for hours and limit.
func TestHealthToolClamping(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)
	// An entry 30 days minus a margin old, inside the 720h ceiling but well
	// outside a 24h default — used to prove hours:99999 clamps to 720 (not
	// unbounded, and not the default).
	within720 := time.Now().UTC().Add(-700 * time.Hour).Format(time.RFC3339)
	beyond720 := time.Now().UTC().Add(-800 * time.Hour).Format(time.RFC3339)

	t.Run("hours negative -> 24 default", func(t *testing.T) {
		vault := storage.NewVault(t.TempDir())
		old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
		writeHealthLog(t, vault, []string{
			fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: fresh"}`, now),
			fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: old"}`, old),
		})
		hr := runHealth(t, vault, healthParams{Hours: -5})
		if hr.WarnCounts["cat"] != 1 {
			t.Errorf("hours:-5 -> counts=%d, want 1 (24h default excludes 48h-old)", hr.WarnCounts["cat"])
		}
	})

	t.Run("hours huge -> 720 ceiling", func(t *testing.T) {
		vault := storage.NewVault(t.TempDir())
		writeHealthLog(t, vault, []string{
			fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: within720"}`, within720),
			fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: beyond720"}`, beyond720),
		})
		hr := runHealth(t, vault, healthParams{Hours: 99999})
		if hr.WarnCounts["cat"] != 1 {
			t.Errorf("hours:99999 -> counts=%d, want 1 (720h ceiling includes 700h, excludes 800h)", hr.WarnCounts["cat"])
		}
	})

	t.Run("limit zero -> 20 default", func(t *testing.T) {
		vault := storage.NewVault(t.TempDir())
		var lines []string
		for i := range 25 {
			lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: e%d"}`, now, i))
		}
		writeHealthLog(t, vault, lines)
		hr := runHealth(t, vault, healthParams{Limit: 0})
		if len(hr.RecentWarns) != 20 {
			t.Errorf("limit:0 -> recent=%d, want 20 (default)", len(hr.RecentWarns))
		}
	})

	t.Run("limit huge -> 1000 ceiling", func(t *testing.T) {
		vault := storage.NewVault(t.TempDir())
		var lines []string
		for i := range 25 {
			lines = append(lines, fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"cat: e%d"}`, now, i))
		}
		writeHealthLog(t, vault, lines)
		hr := runHealth(t, vault, healthParams{Limit: 99999})
		// 25 entries all fit under the 1000 ceiling: none dropped.
		if len(hr.RecentWarns) != 25 {
			t.Errorf("limit:99999 -> recent=%d, want 25 (all fit under 1000 ceiling)", len(hr.RecentWarns))
		}
	})
}

// TestCallerFrictionMessageSilentAtZero — the bootstrap friction line MUST be
// empty at count 0, so it costs nothing in the healthy case (the ~110-token
// headroom rule), and MUST render terse when the count is positive.
func TestCallerFrictionMessageSilentAtZero(t *testing.T) {
	if msg := callerFrictionMessage(vplog.Summary{CallerFriction: 0}); msg != "" {
		t.Errorf("callerFrictionMessage at 0 = %q, want empty — the line must be silent in the healthy case", msg)
	}
	msg := callerFrictionMessage(vplog.Summary{CallerFriction: 3})
	if msg == "" {
		t.Fatal("callerFrictionMessage at 3 is empty — a real friction count must surface")
	}
	if !strings.Contains(msg, "3") {
		t.Errorf("friction line %q does not carry the count", msg)
	}
}

// TestHealthToolAttributesWarningsToTheirProject drives the REAL vp_health
// handler and reconstructs the iteration-263 question — "which project dominates
// this window?" — from the tool's own output.
//
// That question is why this exists. vp_health is the health surface an agent
// actually reaches; the raw log at palace/.local/vp.log is not reachable through
// any tool an agent is likely to call. Summarize was already unmarshalling every
// line into map[string]any and copying out only time/level/msg/fault, so the
// project attribute sat in that map and was dropped — and the 263 attribution
// work had to be done with `jq` over the raw log instead.
//
// Asserting through Handler rather than Summarize is deliberate: a summary that
// carries the field proves nothing if the tool that hands it to an agent does
// not. Mutation: stop copying the field in Summarize and this fails.
func TestHealthToolAttributesWarningsToTheirProject(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	logDir := filepath.Join(vault.Root, "palace", ".local")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var content string
	add := func(line string) { content += line + "\n" }
	for i := 0; i < 5; i++ {
		add(fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"search: index rebuild failed","project":"noisy-proj","fault":"internal"}`, now))
	}
	add(fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"search: index rebuild failed","project":"quiet-proj","fault":"internal"}`, now))
	// Most lines carry no project; the field must stay empty on those rather
	// than being filled in with a neighbour's attribution.
	add(fmt.Sprintf(`{"time":"%s","level":"WARN","msg":"mcp.makeHandler: handler error","fault":"internal"}`, now))

	if err := os.WriteFile(filepath.Join(logDir, "vp.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := HealthTool(vault)
	params, _ := json.Marshal(healthParams{Limit: 1000})
	result, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hr := result.(vplog.Summary)

	byProject := map[string]int{}
	unattributed := 0
	for _, e := range hr.RecentWarns {
		if e.Project == "" {
			unattributed++
			continue
		}
		byProject[e.Project]++
	}
	if byProject["noisy-proj"] != 5 {
		t.Errorf("noisy-proj = %d, want 5 — vp_health cannot say WHICH project is failing, which is the gap that forced iteration 263 around this tool; byProject=%v", byProject["noisy-proj"], byProject)
	}
	if byProject["quiet-proj"] != 1 {
		t.Errorf("quiet-proj = %d, want 1; byProject=%v", byProject["quiet-proj"], byProject)
	}
	if unattributed != 1 {
		t.Errorf("unattributed = %d, want 1 — a line with no project attribute must stay empty", unattributed)
	}

	// The field must survive the tool's own JSON encoding, which is what a
	// client actually receives.
	blob, err := json.Marshal(hr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"project":"noisy-proj"`) {
		t.Errorf("encoded vp_health result does not carry the project attribution: %s", blob)
	}
}
