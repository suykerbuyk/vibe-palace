// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// hostInlineCutSpecimen is a MEASURED specimen, not a constant of the system:
// 19,968 bytes = 19.5 KiB, the flat inline cap a Grok pane applied on
// 2026-08-12 to three separate MCP results of 60.3 KB, 53.4 KB and 32.7 KB. It
// is quoted as evidence of the CLASS — "some host cuts the payload at some fixed
// offset" — and no test may depend on this particular number being right
// tomorrow.
//
// The BOOTSTRAP tests below no longer use it: Phase 3 stopped inlining document
// bodies, so an honest bootstrap payload does not reach 19.5 KB and cutting one
// there would truncate nothing (see cutBootstrapAtBulk). It stays here for the
// other surface results in surface_wire_order_test.go, which still do — a
// vp_get_task result was measured at 192,060 B with its handle 172 KB past the
// cut.
const hostInlineCutSpecimen = 19968

// firstBulkKey is where the instrument block ends and the index begins.
// head_of_queue is declared first in that block and carries no omitempty, so it
// is present on every payload including an empty project's — which is what makes
// it a usable boundary marker rather than one that moves with the vault.
const firstBulkKey = `"head_of_queue":`

// cutBootstrapAtBulk builds a real payload and truncates it exactly where the
// index begins, returning the whole document and the surviving prefix.
//
// 🔴 THE CUT IS DERIVED FROM THE PAYLOAD, NOT FROM A HOST'S NUMBER, and that is
// a Phase 3 change rather than a stylistic one. This helper used to fabricate a
// ~60 KB un-pinned resume so the payload would exceed 19,968 B — the flat inline
// cap a Grok pane applied on 2026-08-12 to three MCP results of 60.3 KB, 53.4 KB
// and 32.7 KB, quoted here as evidence of the CLASS: some host keeps only a
// fixed prefix. Phase 3 stopped inlining document bodies, so no honest payload
// reaches that size any more and the fabrication would have had to grow into a
// lie about what this tool returns.
//
// Cutting at the bulk boundary proves the same property the fixed offset did —
// what survives when a host keeps only a prefix — and it keeps proving it on
// every project, at every size, without depending on any host's number being
// right tomorrow.
func cutBootstrapAtBulk(t *testing.T) (whole string, prefix string) {
	t.Helper()
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", storage.TaskSpec{
		Slug: "the-next-thing", Title: "The next thing", Content: "body", Priority: "high",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.WriteSession("test-proj", storage.SessionMeta{
		Date: "2026-08-18", Title: "a session", Summary: "the next thing, worked on", Tag: "implementation",
	}, "body"); err != nil {
		t.Fatal(err)
	}

	tool := BootstrapContextTool(resolver, vault)
	raw, err := json.Marshal(bootstrapResult(t, tool, `{"project":"test-proj"}`))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	doc := string(raw)

	head, _, found := strings.Cut(doc, firstBulkKey)
	if !found {
		t.Fatalf("no %s key in the payload — the instrument/index boundary this cuts at does not exist, "+
			"so nothing below measures anything; payload: %.400s", firstBulkKey, doc)
	}
	return doc, head
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
	whole, prefix := cutBootstrapAtBulk(t)

	// The prefix really is a cut document, not a smaller whole one. Asserted so
	// a change that moves the boundary cannot turn this test green by leaving
	// nothing truncated.
	if json.Valid([]byte(prefix)) {
		t.Fatal("the truncated prefix parses as valid JSON — it was not actually cut mid-document, so this test is measuring nothing")
	}
	if len(prefix) >= len(whole) {
		t.Fatalf("the cut removed nothing: prefix %d B of a %d B payload", len(prefix), len(whole))
	}

	if strings.Contains(prefix, `"complete"`) {
		t.Errorf("the %d-byte prefix already carries `complete` — the sentinel is not the last field, so a truncated payload is indistinguishable from a whole one",
			len(prefix))
	}
	if !strings.Contains(whole, `"complete":true`) {
		t.Error("the WHOLE payload carries no `complete`:true — absence would then mean nothing at all")
	}

	// What the agent must still hold after the cut: the handles that fetch every
	// body this payload deliberately does not carry, the count that proves a
	// backlog exists, the report on how the rows below were ordered, and the
	// directive with its alerts.
	for _, key := range []string{
		`"resume_uri"`,
		`"workflow_uri"`,
		`"resume_sha256"`,
		`"active_task_count"`,
		`"ranking"`,
		`"post_bootstrap_instructions"`,
	} {
		if !strings.Contains(prefix, key) {
			t.Errorf("%s did not survive the cut at %d B — an agent in the truncated channel cannot recover what it never received",
				key, len(prefix))
		}
	}
}

// TestBootstrapInstrumentsPrecedeBulk pins the wire order by BYTE OFFSET, which
// is the only property the transport actually respects. Field names and
// omitempty flags are irrelevant to a cut; offsets are all it sees.
func TestBootstrapInstrumentsPrecedeBulk(t *testing.T) {
	doc, _ := cutBootstrapAtBulk(t)

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
		`"ranking"`,
	} {
		for _, bulk := range []string{firstBulkKey, `"recent_sessions":`} {
			if at(instrument) > at(bulk) {
				t.Errorf("%s is at byte %d, AFTER %s at byte %d — a host cut inside the index takes the instrument with it",
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

	// LAST FIELD, on the wire, on a real payload — from an empty project, whose
	// optional index lists are all omitted, and from a populated one, whose are
	// not. Those are the two shapes the tail can differ between; the deleted
	// `max_tokens` case that used to sit here named a parameter this binary no
	// longer has, and an unknown JSON key is ignored, so it exercised the SAME
	// path as the case above it while reading as a second one.
	for _, tc := range []struct {
		name    string
		prepare func(*testing.T, *storage.Vault)
	}{
		{"empty project, optional lists omitted", func(*testing.T, *storage.Vault) {}},
		{"populated project, index lists present", func(t *testing.T, v *storage.Vault) {
			if err := v.CreateTask("test-proj", storage.TaskSpec{
				Slug: "a-task", Title: "A task", Content: "body", Priority: "high",
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := v.WriteSession("test-proj", storage.SessionMeta{
				Date: "2026-08-18", Title: "a session", Summary: "work", Tag: "implementation",
			}, "body"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault, resolver := testSetup(t)
			tc.prepare(t, vault)
			tool := BootstrapContextTool(resolver, vault)
			raw, err := json.Marshal(bootstrapResult(t, tool, `{"project":"test-proj"}`))
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
