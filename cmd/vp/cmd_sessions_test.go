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
	code := runSessions(v, "test-proj", 10, false, &buf)
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
	code := runSessions(v, "test-proj", 10, false, &buf)
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
	for i := 0; i < 5; i++ {
		v.WriteSession("test-proj", storage.SessionMeta{
			Date: "2026-04-01", Title: "Session",
		}, "body")
	}

	var buf bytes.Buffer
	code := runSessions(v, "test-proj", 2, false, &buf)
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
	code := runSessions(v, "test-proj", 10, true, &buf)
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
	runSessions(v, "test-proj", 10, false, &buf)
	out := buf.String()
	if !strings.Contains(out, "...") {
		t.Error("expected truncation with ellipsis")
	}
}
