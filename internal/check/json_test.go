// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToJSONShape(t *testing.T) {
	results := []Result{
		{Name: "Config", Status: Pass, Summary: "ok"},
		{Name: "Vault", Status: Skip},
		{Name: "Project", Status: Info, Summary: "myapp"},
		{
			Name:    "Surface",
			Status:  Fail,
			Summary: "binary v1 < vault v2 at /vault/Projects/p",
			Details: []string{"Upgrade: git pull", "Override: VP_SURFACE_GATE=warn"},
		},
	}
	binary := JSONBinaryInfo{Surface: 1, Tools: 42, Commit: "abc123"}

	rep := ToJSON(results, binary)

	if rep.Version != 1 {
		t.Errorf("version = %d, want 1", rep.Version)
	}
	if rep.Binary != binary {
		t.Errorf("binary = %+v, want %+v", rep.Binary, binary)
	}
	if len(rep.Checks) != len(results) {
		t.Fatalf("checks len = %d, want %d", len(rep.Checks), len(results))
	}

	// Status strings are lowercased to pass/fail/skip/info.
	wantStatus := []string{"pass", "skip", "info", "fail"}
	for i, c := range rep.Checks {
		if c.Status != wantStatus[i] {
			t.Errorf("check[%d].status = %q, want %q", i, c.Status, wantStatus[i])
		}
	}

	// Summary tallies every status bucket.
	if rep.Summary.Pass != 1 || rep.Summary.Fail != 1 || rep.Summary.Skip != 1 || rep.Summary.Info != 1 {
		t.Errorf("summary = %+v, want one of each", rep.Summary)
	}

	// A Fail forces exit_code 1.
	if rep.ExitCode != 1 {
		t.Errorf("exit_code = %d, want 1", rep.ExitCode)
	}

	// Surface detail folds Summary + Details onto one line.
	surfaceDetail := rep.Checks[3].Detail
	if !strings.Contains(surfaceDetail, "binary v1 < vault v2") || !strings.Contains(surfaceDetail, "Upgrade: git pull") {
		t.Errorf("surface detail did not fold summary+details: %q", surfaceDetail)
	}

	// The report marshals to stable JSON with the expected field names.
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, field := range []string{
		`"version"`, `"binary"`, `"surface"`, `"tools"`, `"commit"`,
		`"checks"`, `"name"`, `"status"`, `"detail"`,
		`"summary"`, `"pass"`, `"fail"`, `"skip"`, `"info"`, `"exit_code"`,
	} {
		if !strings.Contains(string(b), field) {
			t.Errorf("marshaled JSON missing field %s:\n%s", field, b)
		}
	}
	// vibe-palace omits the vibe-vault "schema" field — it must not appear.
	if strings.Contains(string(b), `"schema"`) {
		t.Errorf("unexpected schema field in JSON:\n%s", b)
	}
}

func TestToJSONExitCodeZeroWhenNoFail(t *testing.T) {
	rep := ToJSON([]Result{
		{Name: "Config", Status: Pass},
		{Name: "Surface", Status: Info, Summary: "warn-ish"},
	}, JSONBinaryInfo{Surface: 1})
	if rep.ExitCode != 0 {
		t.Errorf("exit_code = %d, want 0", rep.ExitCode)
	}
}

func TestStatusName(t *testing.T) {
	cases := map[Status]string{Pass: "pass", Fail: "fail", Skip: "skip", Info: "info"}
	for s, want := range cases {
		if got := statusName(s); got != want {
			t.Errorf("statusName(%v) = %q, want %q", s, got, want)
		}
	}
	if got := statusName(Status(99)); got != "unknown" {
		t.Errorf("statusName(unknown) = %q, want unknown", got)
	}
}
