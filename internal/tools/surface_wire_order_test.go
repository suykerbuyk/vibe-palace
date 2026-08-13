// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// These tests generalise internal/tools/bootstrap_wire_order_test.go from ONE
// tool to the whole surface. They pin a TRANSPORT property, not a behavioural
// one: encoding/json emits struct fields in declaration order, nothing on the
// response path re-serializes through a map (mcp.marshalResult hands the value
// straight to mcplib.NewToolResultJSON, which marshals the struct itself), so
// declaration order IS wire order IS cut order. A host with a fixed inline cap
// keeps a prefix and drops the rest, and the only thing deciding what an agent
// still holds is which fields were declared first.
//
// hostInlineCutSpecimen (19,968 bytes — declared in bootstrap_wire_order_test.go)
// is a MEASURED specimen, not a constant of the system. The tests below cut at
// an offset and assert what survives it; any offset landing inside the bulk
// proves the same property.

// bigBody is a filler body comfortably past the specimen cut, so the cut lands
// INSIDE the bulk field exactly as it did on the live pane. 60 KB is ~3x the
// specimen, which is the real ratio the survey measured for vp_manual (3.3x)
// and not far off vp_get_task's 9.6x.
func bigBody() string {
	return strings.Repeat("a line of body text that nobody has ruled on yet\n", 1250)
}

// wireCase is one result struct under test: a value with an over-cap bulk field,
// the recovery handles that must be on the near side of a cut, and the bulk keys
// that must be on the far side of them.
//
// handles may be empty — several of these tools deliberately get the sentinel
// and NO handle, because there is no URI that addresses their result (see
// ProjectContext) and inventing one is a paging design, not a field move.
type wireCase struct {
	name     string
	value    any
	handles  []string
	bulk     []string
	sentinel bool
}

func surfaceWireCases() []wireCase {
	body := bigBody()
	return []wireCase{
		{
			name: "vp_get_task",
			value: getTaskResult{
				Meta:        storage.TaskMeta{Slug: "s", Title: "t"},
				ContentURI:  "vibe-palace://task/p/s",
				ContentSize: len(body),
				Content:     body,
				Complete:    true,
			},
			handles:  []string{`"content_uri"`, `"content_size"`},
			bulk:     []string{`"content":`},
			sentinel: true,
		},
		{
			name: "vp_get_learning",
			value: getLearningResult{
				Slug:        "s",
				ContentURI:  "vibe-palace://learning/s",
				ContentSize: len(body),
				Content:     body,
			},
			handles: []string{`"content_uri"`, `"content_size"`},
			bulk:    []string{`"content":`},
			// No sentinel: measured 1,237 bytes, two orders under the cap. This
			// task added the sentinel to the tools measured OVER it.
			sentinel: false,
		},
		{
			name: "vp_get_resume",
			value: resumeResult{
				ResumeURI:     "vibe-palace://resume/p",
				resolveResult: resolveResult{Project: "p", Source: "project", Sha256: "deadbeef", Content: body},
				Complete:      true,
			},
			// sha256 rides with the handle, not with the body: an agent paging
			// the body back through resume_uri needs the digest to CAS against.
			handles:  []string{`"resume_uri"`, `"sha256"`},
			bulk:     []string{`"content":`},
			sentinel: true,
		},
		{
			name: "vp_get_doctrine",
			value: doctrineResult{
				DoctrineURI:   "vibe-palace://doctrine/p",
				resolveResult: resolveResult{Project: "p", Source: "embedded", Sha256: "deadbeef", Content: body},
			},
			handles: []string{`"doctrine_uri"`, `"sha256"`},
			bulk:    []string{`"content":`},
			// No sentinel: doctrineResult NESTS inside ManualResult, and a
			// sentinel mid-document survives the cut it exists to detect.
			sentinel: false,
		},
		{
			name: "vp_get_command",
			value: getResourceResult{
				Name:       "wrap",
				ContentURI: "vibe-palace://command/p/wrap",
				Source:     "project",
				Content:    body,
				Complete:   true,
			},
			handles:  []string{`"content_uri"`},
			bulk:     []string{`"content":`},
			sentinel: true,
		},
		{
			name: "vp_get_session_detail",
			value: sessionDetailResult{
				SessionID:  "2026-08-04-01",
				SessionURI: "vibe-palace://session/p/2026-08-04-01",
				Project:    "p",
				Body:       body,
				Complete:   true,
			},
			handles:  []string{`"session_uri"`},
			bulk:     []string{`"body":`},
			sentinel: true,
		},
		{
			name: "vp_read_resource",
			value: readResourceResult{
				URI:       "vibe-palace://task/p/s",
				MIMEType:  "text/markdown",
				TotalSize: len(body),
				Length:    len(body),
				EOF:       true,
				Content:   body,
				Complete:  true,
			},
			handles:  []string{`"uri"`, `"offset"`, `"length"`, `"total_size"`, `"eof"`},
			bulk:     []string{`"content":`},
			sentinel: true,
		},
		{
			name: "vp_manual",
			value: ManualResult{
				Doctrine: doctrineResult{
					DoctrineURI:   "vibe-palace://doctrine/p",
					resolveResult: resolveResult{Project: "p", Content: body},
				},
				ServerInstructions: "x",
				Complete:           true,
			},
			handles:  []string{`"doctrine_uri"`},
			bulk:     []string{`"server_instructions"`},
			sentinel: true,
		},
		{
			name:     "vp_get_project_context",
			value:    ProjectContext{Project: "p", Summary: body, Complete: true},
			bulk:     []string{`"summary":`},
			sentinel: true,
		},
		{
			name:     "vp_get_knowledge",
			value:    knowledgeResult{Triples: bigTriples(), Complete: true},
			bulk:     []string{`"triples":`},
			sentinel: true,
		},
		{
			name:     "vp_kg_query / vp_kg_timeline",
			value:    kgTripleListResult{Triples: bigTriples(), Complete: true},
			bulk:     []string{`"triples":`},
			sentinel: true,
		},
		{
			name:     "vp_vault_list",
			value:    vaultListResult{Entries: bigEntries(), Complete: true},
			bulk:     []string{`"entries":`},
			sentinel: true,
		},
		{
			name: "vp_get_iteration (result)",
			value: getIterationResult{
				Project:       "p",
				Mode:          "n",
				NewestN:       1,
				OldestN:       1,
				Returned:      1,
				BytesInlined:  len(body),
				MoreAvailable: false,
				MaxBytes:      DefaultGetIterationMaxBytes,
				Entries: []iterationEntryRow{{
					N:          1,
					Title:      "t",
					Bytes:      len(body),
					ContentURI: "vibe-palace://iteration/p/1",
					Header:     "## Iteration 1 — t",
					Body:       body,
				}},
				Complete: true,
			},
			// Wrapper identity + the per-row handle must precede bulk body text.
			handles:  []string{`"project"`, `"mode"`, `"content_uri"`, `"n"`, `"title"`, `"bytes"`},
			bulk:     []string{`"body":`, `"header":`},
			sentinel: true,
		},
		{
			// Nested row alone — doctrine applies inside the entries array too.
			name: "vp_get_iteration (entry row inlined)",
			value: iterationEntryRow{
				N:          1,
				Title:      "t",
				Bytes:      len(body),
				ContentURI: "vibe-palace://iteration/p/1",
				Header:     "## Iteration 1 — t",
				Body:       body,
			},
			handles:  []string{`"content_uri"`, `"n"`, `"title"`, `"bytes"`},
			bulk:     []string{`"body":`, `"header":`},
			sentinel: false,
		},
	}
}

func bigTriples() []storage.Triple {
	out := make([]storage.Triple, 400)
	for i := range out {
		out[i] = storage.Triple{
			Subject:   "a-subject-entity-with-a-realistically-long-name",
			Predicate: "depends_on_in_some_documented_way",
			Object:    "an-object-entity-with-a-realistically-long-name",
		}
	}
	return out
}

func bigEntries() []vaultfs.Entry {
	out := make([]vaultfs.Entry, 500)
	for i := range out {
		out[i] = vaultfs.Entry{
			Name:   "a-file-with-a-realistically-long-name-in-a-big-directory.md",
			Type:   "file",
			Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}
	}
	return out
}

// TestSurfaceTruncatedPrefixIsDetectable is the headline, and it is the same
// assertion TestBootstrapTruncatedPrefixIsDetectable makes, applied to every
// other result struct on the surface that carries a handle or a sentinel.
//
// 🔴 THE ASSERTION THAT MATTERS IS THE NEGATIVE ONE. `complete` must be ABSENT
// from the prefix. Before the sentinel, a truncated result and a whole one were
// byte-indistinguishable from inside the channel, and an agent that cannot
// detect the cut never decides to reach for the handle — which is why hoisting
// the handle alone would not have been enough.
//
// The positive assertions are the other half: the handles must be on the near
// side of the cut, or detecting the truncation only tells the agent it is lost
// without telling it where to look. Measured 2026-08-12, vp_get_task returned
// 192,060 bytes with content_uri at byte 191,956 — 172 KB past the cut.
func TestSurfaceTruncatedPrefixIsDetectable(t *testing.T) {
	for _, tc := range surfaceWireCases() {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if len(raw) <= hostInlineCutSpecimen {
				t.Fatalf("test premise broken: payload is %d bytes, which already fits inside the %d-byte cut — "+
					"nothing is being truncated, so this test would pass without measuring anything",
					len(raw), hostInlineCutSpecimen)
			}
			prefix := string(raw[:hostInlineCutSpecimen])

			// The prefix really is a cut document, not a smaller whole one.
			// Guards against a future shrink turning this green by removing the
			// truncation it measures.
			if json.Valid([]byte(prefix)) {
				t.Fatal("the truncated prefix parses as valid JSON — it was not actually cut mid-document, so this test is measuring nothing")
			}

			for _, h := range tc.handles {
				if !strings.Contains(prefix, h) {
					t.Errorf("%s did not survive the %d-byte cut — an agent in the truncated channel cannot recover what it never received",
						h, hostInlineCutSpecimen)
				}
			}

			if !tc.sentinel {
				if strings.Contains(string(raw), `"complete"`) {
					t.Errorf("this case declares no sentinel but the payload carries one: %s", tailOf(string(raw)))
				}
				return
			}
			if strings.Contains(prefix, `"complete"`) {
				t.Errorf("the %d-byte prefix already carries `complete` — the sentinel is not the last field, so a truncated payload is indistinguishable from a whole one",
					hostInlineCutSpecimen)
			}
			if !strings.HasSuffix(string(raw), `,"complete":true}`) {
				t.Errorf("the whole payload does not END with the sentinel; tail = %s", tailOf(string(raw)))
			}
		})
	}
}

// TestSurfaceHandlesPrecedeBulk pins the order by BYTE OFFSET, the only property
// a transport cut actually respects. Field names and omitempty flags are
// invisible to a cut; offsets are all it sees.
func TestSurfaceHandlesPrecedeBulk(t *testing.T) {
	for _, tc := range surfaceWireCases() {
		if len(tc.handles) == 0 {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			doc := string(raw)
			at := func(key string) int {
				i := strings.Index(doc, key)
				if i < 0 {
					t.Fatalf("%s is absent from the payload entirely", key)
				}
				return i
			}
			for _, h := range tc.handles {
				for _, b := range tc.bulk {
					if at(h) > at(b) {
						t.Errorf("%s is at byte %d, AFTER %s at byte %d — a host cut inside the bulk takes the handle with it",
							h, at(h), b, at(b))
					}
				}
			}
		})
	}
}

// TestSurfaceCompleteSentinelIsStructurallyLast covers the two properties that
// make ABSENCE readable as a signal: the field is never omitted, and nothing is
// ever declared after it.
//
// The reflection assertion is the guard against the way this contract will
// actually be broken — not by deleting a sentinel, but by someone appending a
// field to the end of a struct without knowing that the end is load-bearing.
func TestSurfaceCompleteSentinelIsStructurallyLast(t *testing.T) {
	for _, tc := range []struct {
		name string
		zero any
	}{
		{"getTaskResult", getTaskResult{}},
		{"resumeResult", resumeResult{}},
		{"getResourceResult", getResourceResult{}},
		{"sessionDetailResult", sessionDetailResult{}},
		{"readResourceResult", readResourceResult{}},
		{"ManualResult", ManualResult{}},
		{"ProjectContext", ProjectContext{}},
		{"knowledgeResult", knowledgeResult{}},
		{"kgTripleListResult", kgTripleListResult{}},
		{"vaultListResult", vaultListResult{}},
		{"getIterationResult", getIterationResult{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// LAST FIELD, structurally.
			rt := reflect.TypeOf(tc.zero)
			last := rt.Field(rt.NumField() - 1)
			if tag := last.Tag.Get("json"); tag != "complete" {
				t.Errorf("the last declared field of %s is %s (json:%q), not the `complete` sentinel — "+
					"anything declared after the sentinel is serialized after it, and a cut that lands there leaves `complete` visible on a truncated payload",
					tc.name, last.Name, tag)
			}

			// NO omitempty. With it, a false bool vanishes and "not delivered
			// whole" serializes to the same bytes as "delivered whole".
			raw, err := json.Marshal(tc.zero)
			if err != nil {
				t.Fatalf("marshal zero value: %v", err)
			}
			if !strings.Contains(string(raw), `"complete":false`) {
				t.Errorf("a zero-value %s marshals to %s without `complete`:false — the field carries omitempty, so its absence no longer proves truncation",
					tc.name, tailOf(string(raw)))
			}
		})
	}
}

// TestSurfaceHandlersEmitHandleAndSentinel closes the gap the struct-layout
// tests above cannot: a perfectly ordered struct whose handler never POPULATES
// the handle emits `"content_uri":""`, which is a layout that passes every
// offset assertion and helps nobody. Three of these URIs (resume, session,
// command) existed in internal/mcp, had registered resource templates, were
// served by vp_read_resource — and were minted by no handler at all, which is
// exactly the shape of bug this asserts against.
func TestSurfaceHandlersEmitHandleAndSentinel(t *testing.T) {
	vault, resolver := testSetup(t)
	ctx := context.Background()

	if err := vault.WriteResume("test-proj", "# Resume\nbody\n", ""); err != nil {
		t.Fatal(err)
	}
	if err := vault.CreateTask("test-proj", storage.TaskSpec{
		Slug: "a-task", Title: "a task", Content: "Some body text for the task.\n", Priority: "high",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("vp_get_resume", func(t *testing.T) {
		res := handlerResult(t, ctx, GetResumeTool(resolver), `{"project":"test-proj"}`).(resumeResult)
		if res.ResumeURI != "vibe-palace://resume/test-proj" {
			t.Errorf("resume_uri = %q", res.ResumeURI)
		}
		if !res.Complete {
			t.Error("complete is false on a successful return")
		}
	})

	t.Run("vp_get_task", func(t *testing.T) {
		res := handlerResult(t, ctx, GetTaskTool(vault), `{"project":"test-proj","task":"a-task"}`).(getTaskResult)
		if res.ContentURI != "vibe-palace://task/test-proj/a-task" {
			t.Errorf("content_uri = %q", res.ContentURI)
		}
		if !res.Complete {
			t.Error("complete is false on a successful return")
		}
	})

	t.Run("vp_get_session_detail", func(t *testing.T) {
		id := seedSession(t, vault, "test-proj", storage.SessionMeta{Date: "2026-04-01", Title: "t"}, "\n## Summary\nbody\n")
		res := handlerResult(t, ctx, GetSessionDetailTool(vault),
			`{"project":"test-proj","session_id":"`+id+`"}`).(sessionDetailResult)
		if want := "vibe-palace://session/test-proj/" + id; res.SessionURI != want {
			t.Errorf("session_uri = %q, want %q", res.SessionURI, want)
		}
		if !res.Complete {
			t.Error("complete is false on a successful return")
		}
	})

	t.Run("vp_get_doctrine", func(t *testing.T) {
		res := handlerResult(t, ctx, GetDoctrineTool(resolver), `{"project":"test-proj"}`).(doctrineResult)
		if res.DoctrineURI != "vibe-palace://doctrine/test-proj" {
			t.Errorf("doctrine_uri = %q", res.DoctrineURI)
		}
	})

	t.Run("vp_get_command emits a project-scoped URI", func(t *testing.T) {
		res := handlerResult(t, ctx, GetCommandTool(resolver), `{"project":"test-proj","name":"restart"}`).(getResourceResult)
		if res.ContentURI != "vibe-palace://command/test-proj/restart" {
			t.Errorf("content_uri = %q", res.ContentURI)
		}
		if !res.Complete {
			t.Error("complete is false on a successful return")
		}
	})

	// A wing/room-scoped call resolves a DIFFERENT body than the URI template
	// (which carries only {project}/{name} and re-resolves unscoped), so the
	// handle is withheld rather than allowed to point at the wrong document.
	// A missing hatch is visible; a lying one is not.
	t.Run("vp_get_command withholds the URI when scoped", func(t *testing.T) {
		if got := resourceContentURI("command", getResourceParams{Project: "p", Name: "n", Wing: "w"}); got != "" {
			t.Errorf("content_uri = %q for a wing-scoped lookup, want empty", got)
		}
		if got := resourceContentURI("command", getResourceParams{Name: "n"}); got != "" {
			t.Errorf("content_uri = %q with no project, want empty", got)
		}
	})
}

func handlerResult(t *testing.T, ctx context.Context, tool mcp.Tool, params string) any {
	t.Helper()
	res, err := tool.Handler(ctx, json.RawMessage(params))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	return res
}

func tailOf(s string) string {
	if len(s) > 140 {
		return "…" + s[len(s)-140:]
	}
	return s
}
