// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// hostInlineCutSpecimen is a MEASURED specimen, not a constant of the system:
// 19,968 bytes = 19.5 KiB, the flat inline cap a Grok pane applied on
// 2026-08-12 to three separate MCP results of 60.3 KB, 53.4 KB and 32.7 KB. It
// is quoted here as evidence of the CLASS — "some host cuts the payload at some
// fixed offset" — and the tests below must not depend on this particular number
// being right tomorrow. They cut at an offset and assert what survives it; any
// offset landing inside the bulk would prove the same property.
const hostInlineCutSpecimen = 19968

// overBudgetBootstrap builds a payload that is BIGGER than the specimen cut and
// carries a live `budget` block, which is the state the tests below need and
// which a comfortable payload cannot produce (budget is nil when nothing was
// shed and the payload fit).
//
// The resume declares NO pin marker on purpose: that makes it un-sheddable by
// the ladder's resume rung (the server will not guess which half of an
// undeclared resume was safe to drop), so the payload stays far over budget and
// `budget` is emitted with over_budget set. That is not a contrived state — it
// is the quantum-ng specimen the epic measured, a real project whose core alone
// is ~1.95x a real host's cap.
func overBudgetBootstrap(t *testing.T) (BootstrapResult, []byte) {
	t.Helper()
	vault, resolver := testSetup(t)
	// ~60 KB of un-pinned diary, comfortably past the specimen cut so the cut
	// lands inside `resume` exactly as it did on the live pane.
	resume := "# Resume\n\n## Current State\n\n" +
		strings.Repeat("- a line of project diary that nobody has ruled on yet\n", 1100)
	if err := vault.WriteResume("test-proj", resume, ""); err != nil {
		t.Fatal(err)
	}
	tool := BootstrapContextTool(resolver, vault)
	br := bootstrapResult(t, tool, `{"project":"test-proj"}`)

	raw, err := json.Marshal(br)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) <= hostInlineCutSpecimen {
		t.Fatalf("test premise broken: payload is %d bytes, which already fits inside the %d-byte cut — "+
			"nothing is being truncated, so this test would pass without measuring anything",
			len(raw), hostInlineCutSpecimen)
	}
	return br, raw
}

// TestBootstrapTruncatedPrefixIsDetectable is the headline: it reproduces the
// actual defect — a host cutting the payload at a fixed offset — and asserts
// that what reaches the model is now enough to KNOW it was cut and to recover.
//
// 🔴 THE ASSERTION THAT MATTERS IS THE NEGATIVE ONE. `complete` must be ABSENT
// from the prefix. Before the sentinel, a truncated payload and a payload from
// a vp that simply had nothing to report were byte-indistinguishable from
// inside the channel: the agent saw no `budget` and could not tell "vp shed
// nothing" from "the report was cut off". That ambiguity is the whole defect;
// an agent that cannot detect the cut cannot decide to rehydrate.
//
// The positive assertions are the other half: the instruments and the recovery
// handles must be on the near side of the cut, or detecting the truncation only
// tells the agent it is lost without telling it where to look.
func TestBootstrapTruncatedPrefixIsDetectable(t *testing.T) {
	_, raw := overBudgetBootstrap(t)
	prefix := string(raw[:hostInlineCutSpecimen])

	// The prefix really is a cut document, not a smaller whole one. Asserted so
	// a future change that quietly shrinks the payload under the cut cannot turn
	// this test green by removing the truncation it is measuring.
	if json.Valid([]byte(prefix)) {
		t.Fatal("the truncated prefix parses as valid JSON — it was not actually cut mid-document, so this test is measuring nothing")
	}

	if strings.Contains(prefix, `"complete"`) {
		t.Errorf("the %d-byte prefix already carries `complete` — the sentinel is not the last field, so a truncated payload is indistinguishable from a whole one",
			hostInlineCutSpecimen)
	}
	if !strings.Contains(string(raw), `"complete":true`) {
		t.Error("the WHOLE payload carries no `complete`:true — absence would then mean nothing at all")
	}

	// What the agent must still hold after the cut: the report about the
	// payload, the handles that fetch what it lost, and the count that proves a
	// backlog exists.
	for _, key := range []string{
		`"resume_uri"`,
		`"workflow_uri"`,
		`"resume_sha256"`,
		`"active_task_count"`,
		`"post_bootstrap_instructions"`,
	} {
		if !strings.Contains(prefix, key) {
			t.Errorf("%s did not survive the %d-byte cut — an agent in the truncated channel cannot recover what it never received",
				key, hostInlineCutSpecimen)
		}
	}
}

// TestBootstrapInstrumentsPrecedeBulk pins the wire order by BYTE OFFSET, which
// is the only property the transport actually respects. Field names and
// omitempty flags are irrelevant to a cut; offsets are all it sees.
func TestBootstrapInstrumentsPrecedeBulk(t *testing.T) {
	_, raw := overBudgetBootstrap(t)
	doc := string(raw)

	// First occurrence is the key: every instrument key below is emitted in the
	// header region, ahead of any bulk string that might quote one in prose.
	at := func(key string) int {
		i := strings.Index(doc, key)
		if i < 0 {
			t.Fatalf("%s is absent from the payload entirely", key)
		}
		return i
	}

	for _, instrument := range []string{
		`"resume_uri"`,
		`"workflow_uri"`,
		`"resume_sha256"`,
		`"active_task_count"`,
	} {
		for _, bulk := range []string{`"workflow":`, `"resume":`} {
			if at(instrument) > at(bulk) {
				t.Errorf("%s is at byte %d, AFTER %s at byte %d — a host cut inside the bulk takes the instrument with it",
					instrument, at(instrument), bulk, at(bulk))
			}
		}
	}
}

// TestBootstrapCompleteSentinelAlwaysEmitted covers the two properties that
// make absence readable as a signal: the field is never omitted, and nothing is
// ever declared after it.
func TestBootstrapCompleteSentinelAlwaysEmitted(t *testing.T) {
	// NO omitempty. A zero-value result must still spell the field out: with
	// omitempty a false bool vanishes, and "not delivered whole" would serialize
	// to the same bytes as "delivered whole".
	zero, err := json.Marshal(BootstrapResult{})
	if err != nil {
		t.Fatalf("marshal zero value: %v", err)
	}
	if !strings.Contains(string(zero), `"complete":false`) {
		t.Errorf("a zero-value BootstrapResult marshals to %s without `complete` — the field carries omitempty, so its absence no longer proves truncation", zero)
	}

	// LAST FIELD, structurally. This is the guard against the way this contract
	// will actually be broken: not by deleting the sentinel, but by someone
	// appending a new field to the end of the struct without knowing that the
	// end is load-bearing.
	rt := reflect.TypeFor[BootstrapResult]()
	last := rt.Field(rt.NumField() - 1)
	if tag := last.Tag.Get("json"); tag != "complete" {
		t.Errorf("the last declared field of BootstrapResult is %s (json:%q), not the `complete` sentinel — "+
			"anything declared after the sentinel is serialized after it, and a cut that lands there leaves `complete` visible on a truncated payload",
			last.Name, tag)
	}

	// LAST FIELD, on the wire, on a real payload from each side of the ladder.
	for _, tc := range []struct {
		name   string
		params string
	}{
		{"under budget, nothing shed", `{"project":"test-proj"}`},
		{"shed to the floor", `{"project":"test-proj","max_tokens":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault, resolver := testSetup(t)
			tool := BootstrapContextTool(resolver, vault)
			raw, err := json.Marshal(bootstrapResult(t, tool, tc.params))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.HasSuffix(string(raw), `,"complete":true}`) {
				tail := string(raw)
				if len(tail) > 120 {
					tail = "…" + tail[len(tail)-120:]
				}
				t.Errorf("payload does not END with the sentinel; tail = %s", tail)
			}
		})
	}
}
