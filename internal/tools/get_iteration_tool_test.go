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
	// complete must be last key in marshaled object
	if !strings.HasSuffix(strings.TrimSpace(string(b)), `"complete":true}`) &&
		!strings.Contains(string(b), `"complete":true`) {
		t.Fatalf("complete missing in %s", b)
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
	if got.Entries[0].Matches != 3 || got.Entries[1].MatchIndex != 1 {
		t.Fatalf("match meta %+v", got.Entries)
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
	if got.OldestN != 2 || got.NewestN != 3 {
		t.Fatalf("oldest=%d newest=%d", got.OldestN, got.NewestN)
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
	if !got.MoreAvailable {
		t.Fatal("more_available")
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
