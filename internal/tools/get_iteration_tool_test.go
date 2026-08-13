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

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

func seedIterations(t *testing.T, vault *storage.Vault, project string, frames ...string) {
	t.Helper()
	path, err := vault.IterationsFile(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# " + project + " — Iteration Narratives\n" + strings.Join(frames, "")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func frame(n int, title, body string) string {
	return "\n---\n" + wrapstate.FormatIterationHeader(n, title) + "\n\n" + strings.TrimSpace(body) + "\n"
}

func callGetIteration(t *testing.T, vault *storage.Vault, params map[string]any) getIterationResult {
	t.Helper()
	tool := GetIterationTool(vault)
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Handler(context.Background(), raw)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	// round-trip through JSON so we exercise the real shape
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var got getIterationResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Complete {
		t.Fatal("complete must be true")
	}
	// complete is last on the wire (doctrine; also pinned by surface_wire_order_test).
	if !strings.HasSuffix(strings.TrimSpace(string(b)), `"complete":true}`) {
		t.Fatalf("complete must be the last JSON key, got tail %q", string(b)[max(0, len(b)-40):])
	}
	return got
}

func TestGetIteration_ByN(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(1, "one", "body-one"),
		frame(2, "two", "body-two"),
	)
	got := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 2})
	if got.Mode != "n" || got.Returned != 1 || got.Entries[0].Body != "body-two" {
		t.Fatalf("%+v", got)
	}
	if got.Entries[0].ContentURI != "vibe-palace://iteration/demo/2" {
		t.Errorf("uri=%s", got.Entries[0].ContentURI)
	}
}

func TestGetIteration_DuplicateN(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(128, "a", "A"),
		frame(128, "b", "B"),
		frame(128, "c", "C"),
	)
	got := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 128})
	if got.Returned != 3 {
		t.Fatalf("returned=%d", got.Returned)
	}
	if got.Entries[0].Title != "a" || got.Entries[2].Title != "c" {
		t.Fatalf("%+v", got.Entries)
	}
	if got.Entries[0].Matches != 3 {
		t.Fatalf("matches=%d", got.Entries[0].Matches)
	}
	if got.Entries[0].MatchIndex == nil || *got.Entries[0].MatchIndex != 0 {
		t.Fatalf("row0 match_index must be present and 0, got %+v", got.Entries[0].MatchIndex)
	}
	if got.Entries[1].MatchIndex == nil || *got.Entries[1].MatchIndex != 1 {
		t.Fatalf("row1 match_index=%v", got.Entries[1].MatchIndex)
	}
	// content_uri must address THIS row, not silently the last duplicate.
	for i, e := range got.Entries {
		wantURI := "vibe-palace://iteration/demo/128/" + itoa(i)
		if e.ContentURI != wantURI {
			t.Errorf("row%d uri=%s want %s", i, e.ContentURI, wantURI)
		}
	}
}

func TestGetIteration_GapNotFound(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo", frame(1, "one", "x"))
	tool := GetIterationTool(vault)
	_, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"demo","n":111}`))
	if err == nil {
		t.Fatal("expected not-found")
	}
}

func TestGetIteration_RecentByteBudget(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	// three bodies: 100, 100, 100 bytes — budget 250 admits two newest
	b100 := strings.Repeat("x", 100)
	seedIterations(t, vault, "demo",
		frame(1, "old", b100),
		frame(2, "mid", b100),
		frame(3, "new", b100),
	)
	// Budget counts marshalled rows. ~100 B bodies become ~250+ B rows; 600 admits two.
	got := callGetIteration(t, vault, map[string]any{
		"project": "demo", "recent": true, "max_bytes": 600,
	})
	if got.Returned != 2 || got.BytesInlined != 200 {
		t.Fatalf("returned=%d inlined=%d %+v", got.Returned, got.BytesInlined, got)
	}
	if !got.MoreAvailable {
		t.Fatal("more_available want true")
	}
	// file order: mid then new
	if got.Entries[0].N != 2 || got.Entries[1].N != 3 {
		t.Fatalf("order %+v", got.Entries)
	}
	// Archive extent spans the whole file, not the returned window (2..3).
	if got.OldestN != 1 || got.NewestN != 3 {
		t.Fatalf("archive extent oldest=%d newest=%d want 1..3", got.OldestN, got.NewestN)
	}
	for _, e := range got.Entries {
		if e.Body == "" {
			t.Fatalf("expected inlined body: %+v", e)
		}
	}
}

func TestGetIteration_RecentOversizeNewestManifestOnly(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	big := strings.Repeat("Z", 500)
	seedIterations(t, vault, "demo",
		frame(1, "old", "small"),
		frame(2, "huge", big),
	)
	got := callGetIteration(t, vault, map[string]any{
		"project": "demo", "recent": true, "max_bytes": 100,
	})
	if got.Returned != 1 || got.BytesInlined != 0 {
		t.Fatalf("%+v", got)
	}
	if got.Entries[0].Body != "" || got.Entries[0].N != 2 {
		t.Fatalf("want manifest for newest: %+v", got.Entries[0])
	}
	if !got.Entries[0].BodyDeferred {
		t.Fatal("body_deferred must mark the deferred newest")
	}
	// Older entry 1 exists beyond the window.
	if !got.MoreAvailable {
		t.Fatal("more_available: older entry exists")
	}
	if got.OldestN != 1 || got.NewestN != 2 {
		t.Fatalf("archive extent oldest=%d newest=%d", got.OldestN, got.NewestN)
	}
}

// Single-entry oversize: body deferred, more_available MUST be false.
func TestGetIteration_SingleOversizeMoreAvailableFalse(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	big := strings.Repeat("Z", 500)
	seedIterations(t, vault, "demo", frame(7, "only", big))
	got := callGetIteration(t, vault, map[string]any{
		"project": "demo", "recent": true, "max_bytes": 100,
	})
	if got.Returned != 1 || got.BytesInlined != 0 {
		t.Fatalf("%+v", got)
	}
	if !got.Entries[0].BodyDeferred || got.Entries[0].Body != "" {
		t.Fatalf("want deferred empty body: %+v", got.Entries[0])
	}
	if got.MoreAvailable {
		t.Fatal("single-entry archive: more_available must be false (nothing older)")
	}
	if got.OldestN != 7 || got.NewestN != 7 {
		t.Fatalf("extent %d..%d", got.OldestN, got.NewestN)
	}
}

// n-mode on a middle iteration reports ARCHIVE extent, not an echo of n.
func TestGetIteration_NModeArchiveExtent(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(1, "first", "a"),
		frame(5, "mid", "middle-body"),
		frame(12, "last", "z"),
	)
	got := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 5})
	if got.Returned != 1 || got.Entries[0].Body != "middle-body" {
		t.Fatalf("%+v", got)
	}
	if got.OldestN != 1 || got.NewestN != 12 {
		t.Fatalf("archive extent must be 1..12, got oldest=%d newest=%d (must not echo n=5)",
			got.OldestN, got.NewestN)
	}
	if got.MoreAvailable {
		t.Fatal("all matches for n=5 returned; more_available false")
	}
}

func TestGetIteration_MaxBytesClamp(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo", frame(1, "a", "x"))
	tool := GetIterationTool(vault)
	_, err := tool.Handler(context.Background(), json.RawMessage(
		`{"project":"demo","recent":true,"max_bytes":999999}`))
	if err == nil {
		t.Fatal("expected clamp reject")
	}
}

func TestGetIteration_RejectBothNeither(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	tool := GetIterationTool(vault)
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"project":"demo"}`)); err == nil {
		t.Fatal("neither")
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(
		`{"project":"demo","n":1,"recent":true}`)); err == nil {
		t.Fatal("both")
	}
}

func TestGetIteration_DefaultReadCapUnchanged(t *testing.T) {
	// Pin the vaultfs constants so this task cannot raise them silently.
	// Imported via the same values the package documents.
	if HostInlineCapBytes != 19968 {
		t.Fatalf("HostInlineCapBytes=%d", HostInlineCapBytes)
	}
	if MaxGetIterationMaxBytes >= HostInlineCapBytes {
		t.Fatalf("MaxGetIterationMaxBytes (%d) must be strictly < HostInlineCapBytes (%d)",
			MaxGetIterationMaxBytes, HostInlineCapBytes)
	}
	if MaxGetIterationMaxBytes+getIterationEnvelopeReserve != HostInlineCapBytes {
		t.Fatalf("envelope reserve invariant broken: max=%d reserve=%d host=%d",
			MaxGetIterationMaxBytes, getIterationEnvelopeReserve, HostInlineCapBytes)
	}
}

func TestIterationResource_LastMatch(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(5, "first", "BODY-FIRST"),
		frame(5, "second", "BODY-SECOND"),
	)
	body, _, err := ResolveURI("vibe-palace://iteration/demo/5", nil, vault)
	if err != nil {
		t.Fatal(err)
	}
	if body != "BODY-SECOND" {
		t.Fatalf("got %q", body)
	}
}

func TestAppendGetRoundTrip(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	// Use the real writer
	_, _, err := vault.AppendIterationOwned("demo", "rt", "round-trip body\nline2", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 1})
	if got.Entries[0].Body != "round-trip body\nline2" {
		t.Fatalf("body=%q", got.Entries[0].Body)
	}
}

// TestGetIteration_WireSizeAtMaxBudget is the ratchet for the marshalled-row
// budget. MANY SMALL entries maximise per-row overhead (the shape that blew
// rezbldr/quantum-ng past HostInlineCapBytes when the budget counted bare
// bodies). Break the fill to use len(Body) again and this goes red.
func TestGetIteration_WireSizeAtMaxBudget(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	var frames []string
	// 40 × ~450 B bodies ≈ rezbldr-like many-small density.
	body := strings.Repeat("w", 450)
	for i := 1; i <= 40; i++ {
		frames = append(frames, frame(i, "t"+itoa(i), body))
	}
	seedIterations(t, vault, "demo", frames...)

	got := callGetIteration(t, vault, map[string]any{
		"project": "demo", "recent": true, "max_bytes": MaxGetIterationMaxBytes,
	})
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) > HostInlineCapBytes {
		t.Fatalf("wire size %d exceeds HostInlineCapBytes %d (returned=%d bytes_inlined=%d); "+
			"budget must count marshalled rows + envelope, not bare bodies",
			len(wire), HostInlineCapBytes, got.Returned, got.BytesInlined)
	}
	if got.Returned < 2 {
		t.Fatalf("expected multiple rows under max budget, got returned=%d", got.Returned)
	}
	if !got.MoreAvailable {
		t.Fatal("40 entries should not all fit; more_available want true")
	}
	// Every inlined body is whole — no silent truncation.
	for _, e := range got.Entries {
		if e.Body != "" && e.Body != body {
			t.Fatalf("inlined body was altered (truncated?): len=%d", len(e.Body))
		}
		if e.Body == "" && e.ContentURI == "" {
			t.Fatalf("manifest row missing content_uri: %+v", e)
		}
	}
}

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// TestGetIteration_NModeFillAndManifest applies the same whole-row budget to
// n-mode matches. Two large same-N bodies must not both force a host breach:
// the second becomes a manifest handle when the first fills the budget.
func TestGetIteration_NModeFillAndManifest(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	big := strings.Repeat("B", 8000)
	seedIterations(t, vault, "demo",
		frame(128, "first", big),
		frame(128, "second", big),
		frame(128, "third", big),
	)
	// Budget admits roughly one marshalled ~8k body row; remainder → manifests.
	got := callGetIteration(t, vault, map[string]any{
		"project": "demo", "n": 128, "max_bytes": 9000,
	})
	if got.Returned != 3 {
		t.Fatalf("want all 3 matches as rows (inline or manifest), got returned=%d %+v",
			got.Returned, got.Entries)
	}
	inlined := 0
	for _, e := range got.Entries {
		if e.Body != "" {
			inlined++
			if e.Body != big {
				t.Fatal("body truncated")
			}
		} else if e.ContentURI == "" {
			t.Fatalf("manifest missing uri: %+v", e)
		}
	}
	if inlined < 1 || inlined >= 3 {
		t.Fatalf("want some but not all inlined under 9000 budget, inlined=%d", inlined)
	}
	if got.MoreAvailable {
		t.Fatal("all matches returned as rows; more_available should be false")
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) > HostInlineCapBytes {
		t.Fatalf("n-mode wire %d > host cap %d", len(wire), HostInlineCapBytes)
	}
}

func TestGetIteration_NModeFirstOversizeThenManifests(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	huge := strings.Repeat("H", 20000) // larger than MaxGetIterationMaxBytes as a body row
	small := "tiny"
	seedIterations(t, vault, "demo",
		frame(9, "huge", huge),
		frame(9, "a", small),
		frame(9, "b", small),
	)
	got := callGetIteration(t, vault, map[string]any{
		"project": "demo", "n": 9, "max_bytes": MaxGetIterationMaxBytes,
	})
	// First is manifest (cannot inline); subsequent small ones may inline or manifest.
	if got.Returned < 1 {
		t.Fatal("empty")
	}
	if got.Entries[0].Body != "" {
		t.Fatalf("first should be manifest handle, got body len %d", len(got.Entries[0].Body))
	}
	wire, _ := json.Marshal(got)
	if len(wire) > HostInlineCapBytes {
		t.Fatalf("wire %d > cap", len(wire))
	}
}

// TestIterationURI_MatchIndexByteIdentity pins the duplicate-N contract:
// content_uri on row i must resolve to row i's body, not the last match.
func TestIterationURI_MatchIndexByteIdentity(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	seedIterations(t, vault, "demo",
		frame(128, "a", "BODY-A"),
		frame(128, "b", "BODY-B"),
		frame(128, "c", "BODY-C"),
		frame(128, "d", "BODY-D"),
	)
	got := callGetIteration(t, vault, map[string]any{"project": "demo", "n": 128})
	if got.Returned != 4 {
		t.Fatalf("returned=%d", got.Returned)
	}
	for i, e := range got.Entries {
		body, _, err := ResolveURI(e.ContentURI, nil, vault)
		if err != nil {
			t.Fatalf("row%d ResolveURI(%s): %v", i, e.ContentURI, err)
		}
		if body != e.Body {
			t.Fatalf("row%d URI %s resolved to %q, want row body %q", i, e.ContentURI, body, e.Body)
		}
	}
	// Bare form still means last match.
	last, _, err := ResolveURI(mcp.IterationURI("demo", 128), nil, vault)
	if err != nil || last != "BODY-D" {
		t.Fatalf("bare URI last-match: got %q err %v", last, err)
	}
}

// TestGetIteration_DefaultBudgetWireCap pins the DEFAULT max_bytes path on a
// many-small-entry fixture. Per-row overhead is what blows the host cap; entry
// COUNT is the variable. Round-one body-only budgeting produced 33 KB wire at
// default on 100×120 B bodies — this must stay ≤ HostInlineCapBytes.
func TestGetIteration_DefaultBudgetWireCap(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	var frames []string
	body := strings.Repeat("x", 120)
	for i := 1; i <= 100; i++ {
		frames = append(frames, frame(i, "t"+itoa(i), body))
	}
	seedIterations(t, vault, "demo", frames...)
	// Omit max_bytes → DefaultGetIterationMaxBytes.
	got := callGetIteration(t, vault, map[string]any{"project": "demo", "recent": true})
	if got.MaxBytes != DefaultGetIterationMaxBytes {
		t.Fatalf("max_bytes defaulted to %d, want %d", got.MaxBytes, DefaultGetIterationMaxBytes)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) > HostInlineCapBytes {
		t.Fatalf("default-budget wire %d exceeds host cap %d (returned=%d inlined=%d)",
			len(wire), HostInlineCapBytes, got.Returned, got.BytesInlined)
	}
}

func TestIterationResource_MissingFileNoAbsPath(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	_, _, err := ResolveURI(mcp.IterationURI("never-wrapped", 1), nil, vault)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, string(os.PathSeparator)+"Projects"+string(os.PathSeparator)) ||
		strings.Contains(msg, vault.Root) {
		t.Fatalf("absolute vault path leaked over the wire: %q", msg)
	}
	if !strings.Contains(msg, "no iterations.md") {
		t.Fatalf("want clean message, got %q", msg)
	}
}

func TestIterationEntryRow_DeferredFieldsPrecedeBody(t *testing.T) {
	row := iterationEntryRow{
		N: 1, Title: "t", Bytes: 99,
		ContentURI:   "vibe-palace://iteration/p/1",
		BodyDeferred: true,
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	uri := strings.Index(s, `"content_uri"`)
	def := strings.Index(s, `"body_deferred"`)
	if uri < 0 || def < 0 {
		t.Fatalf("missing handle fields: %s", s)
	}
	if def < uri {
		t.Fatalf("body_deferred before content_uri unexpected order: %s", s)
	}
	// body/header must not appear (empty + omitempty), so handles are the whole payload.
	if strings.Contains(s, `"body"`) || strings.Contains(s, `"header"`) {
		t.Fatalf("deferred row must not emit empty bulk fields: %s", s)
	}
}
