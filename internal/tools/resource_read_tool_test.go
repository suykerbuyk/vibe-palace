// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// TestRuneSafeExcerpt exercises runeSafeExcerpt directly across its three
// branches: under-cap passthrough, the newline cut, and the load-bearing
// no-newline rune-boundary cut (which must never split a multibyte rune).
func TestRuneSafeExcerpt(t *testing.T) {
	// Under cap → returned whole, unchanged.
	if got := runeSafeExcerpt("short", 1500); got != "short" {
		t.Errorf("under-cap: got %q, want %q", got, "short")
	}
	// maxBytes <= 0 → returned whole.
	if got := runeSafeExcerpt("abc", 0); got != "abc" {
		t.Errorf("maxBytes=0: got %q, want %q", got, "abc")
	}
	// No newline within the cap, em-dash straddling the cap → must cut on a
	// rune boundary (never mid-rune) and stay valid UTF-8.
	noNL := strings.Repeat("a", 9) + "—" + strings.Repeat("b", 20) // em-dash starts at byte 9
	got := runeSafeExcerpt(noNL, 10)                               // cap lands inside the 3-byte em-dash
	if !utf8.ValidString(got) {
		t.Fatalf("no-newline excerpt not valid UTF-8: %q", got)
	}
	if got != strings.Repeat("a", 9) {
		t.Errorf("no-newline excerpt = %q, want the 9 a's (em-dash backed off)", got)
	}
	// Newline within the cap → cut at the last newline.
	if got := runeSafeExcerpt("line1\nline2\ntail", 13); got != "line1\nline2" {
		t.Errorf("newline cut = %q, want %q", got, "line1\nline2")
	}
}

// readResourceCall is a small helper that drives the vp_read_resource handler
// and returns the typed result.
func readResourceCall(t *testing.T, resolver *vpctx.Resolver, vault *storage.Vault, uri string, offset, limit int) readResourceResult {
	t.Helper()
	tool := ReadResourceTool(resolver, vault)
	params, _ := json.Marshal(readResourceParams{URI: uri, Offset: offset, Limit: limit})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	rr, ok := res.(readResourceResult)
	if !ok {
		t.Fatalf("result type = %T, want readResourceResult", res)
	}
	return rr
}

func TestReadResourceBasic(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", "my-task", "My Task", "hello world body", "high"); err != nil {
		t.Fatal(err)
	}
	_, body, err := vault.GetTask("test-proj", "my-task")
	if err != nil {
		t.Fatal(err)
	}

	uri := mcp.TaskURI("test-proj", "my-task")
	rr := readResourceCall(t, resolver, vault, uri, 0, 0) // limit 0 → default

	if rr.URI != uri {
		t.Errorf("URI = %q, want %q", rr.URI, uri)
	}
	if rr.MIMEType != "text/markdown" {
		t.Errorf("MIMEType = %q, want text/markdown", rr.MIMEType)
	}
	if rr.TotalSize != len(body) {
		t.Errorf("TotalSize = %d, want %d", rr.TotalSize, len(body))
	}
	if !rr.EOF {
		t.Error("expected EOF for full read with default limit")
	}
	if rr.Content != body {
		t.Errorf("Content mismatch:\n got %q\nwant %q", rr.Content, body)
	}
	if rr.Offset != 0 || rr.Length != len(body) {
		t.Errorf("Offset/Length = %d/%d, want 0/%d", rr.Offset, rr.Length, len(body))
	}
}

func TestReadResourceOffsetPastEnd(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", "my-task", "T", "short", "low"); err != nil {
		t.Fatal(err)
	}
	uri := mcp.TaskURI("test-proj", "my-task")
	_, body, _ := vault.GetTask("test-proj", "my-task")

	rr := readResourceCall(t, resolver, vault, uri, len(body)+9999, 100)
	if rr.Offset != len(body) {
		t.Errorf("Offset = %d, want clamped to %d", rr.Offset, len(body))
	}
	if rr.Length != 0 {
		t.Errorf("Length = %d, want 0 past end", rr.Length)
	}
	if !rr.EOF {
		t.Error("expected EOF when offset past end")
	}
	if rr.Content != "" {
		t.Errorf("Content = %q, want empty", rr.Content)
	}
}

func TestReadResourceLimitLargerThanContent(t *testing.T) {
	vault, resolver := testSetup(t)
	if err := vault.CreateTask("test-proj", "my-task", "T", "body content here", "low"); err != nil {
		t.Fatal(err)
	}
	uri := mcp.TaskURI("test-proj", "my-task")
	_, body, _ := vault.GetTask("test-proj", "my-task")

	rr := readResourceCall(t, resolver, vault, uri, 0, 100000)
	if rr.Content != body || !rr.EOF {
		t.Errorf("expected whole body with EOF, got len=%d eof=%v", rr.Length, rr.EOF)
	}
}

func TestReadResourceUnknownURI(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := ReadResourceTool(resolver, vault)

	// Missing scheme → caller-bug error.
	params, _ := json.Marshal(readResourceParams{URI: "http://nope"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for non-vibe-palace URI")
	}

	// Unknown type → caller-bug error.
	params, _ = json.Marshal(readResourceParams{URI: mcp.ResourceScheme + "bogus/test-proj"})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for unknown resource type")
	}

	// Empty URI.
	params, _ = json.Marshal(readResourceParams{URI: ""})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected error for empty URI")
	}
}

func TestReadResourceContentNotFound(t *testing.T) {
	vault, resolver := testSetup(t)
	tool := ReadResourceTool(resolver, vault)
	// Well-formed URI, but the task does not exist → bubbled not-found error.
	params, _ := json.Marshal(readResourceParams{URI: mcp.TaskURI("test-proj", "ghost")})
	if _, err := tool.Handler(context.Background(), params); err == nil {
		t.Fatal("expected content-not-found error")
	}
}

// TestReadResourceMultibyteBoundary is the critical correctness test: a body
// saturated with 3-byte em-dashes is paged with a tiny limit so the requested
// page edge repeatedly lands mid-rune. Every page must be valid UTF-8, and the
// concatenation of all pages — walking by the RETURNED offset+length, never by
// the requested limit — must be byte-identical to the source.
func TestReadResourceMultibyteBoundary(t *testing.T) {
	vault, resolver := testSetup(t)
	// Build a body where em-dashes (— = 3 bytes, U+2014) straddle page edges.
	var sb strings.Builder
	for range 200 {
		sb.WriteString("a—b—c—") // mix of 1- and 3-byte runes
	}
	body := sb.String()
	if err := vault.CreateTask("test-proj", "emdash", "Em", body, "low"); err != nil {
		t.Fatal(err)
	}
	uri := mcp.TaskURI("test-proj", "emdash")
	_, stored, _ := vault.GetTask("test-proj", "emdash")

	for _, limit := range []int{1, 2, 3, 4, 5, 7, 16, 100} {
		var reassembled strings.Builder
		offset := 0
		pages := 0
		for {
			pages++
			if pages > len(stored)+10 {
				t.Fatalf("limit=%d: pager did not terminate (no progress)", limit)
			}
			rr := readResourceCall(t, resolver, vault, uri, offset, limit)
			if !utf8.ValidString(rr.Content) {
				t.Fatalf("limit=%d offset=%d: page is not valid UTF-8", limit, offset)
			}
			if rr.Offset != offset {
				t.Fatalf("limit=%d: returned Offset %d != requested %d", limit, rr.Offset, offset)
			}
			reassembled.WriteString(rr.Content)
			next := rr.Offset + rr.Length
			if rr.EOF {
				break
			}
			if next <= offset {
				t.Fatalf("limit=%d offset=%d: no forward progress (length=%d)", limit, offset, rr.Length)
			}
			offset = next
		}
		if reassembled.String() != stored {
			t.Fatalf("limit=%d: reassembled body NOT byte-identical to source", limit)
		}
	}
}

// TestReadResourceSingleRuneLargerThanLimit pins the degenerate-progress guard:
// a 3-byte rune with limit 1 or 2 would back off to end<=offset; the handler
// must instead advance forward to emit the whole rune so the pager never stalls.
func TestReadResourceSingleRuneLargerThanLimit(t *testing.T) {
	vault, resolver := testSetup(t)
	body := "—tail"
	if err := vault.CreateTask("test-proj", "lead", "L", body, "low"); err != nil {
		t.Fatal(err)
	}
	uri := mcp.TaskURI("test-proj", "lead")

	// CreateTask prepends a title + status/priority header, so the em-dash is
	// not at offset 0. Locate its real byte offset and page from exactly there
	// so the limit lands strictly inside the 3-byte rune (the degenerate case).
	_, stored, err := vault.GetTask("test-proj", "lead")
	if err != nil {
		t.Fatal(err)
	}
	dashOff := strings.Index(stored, "—")
	if dashOff < 0 {
		t.Fatalf("em-dash not found in stored body %q", stored)
	}

	for _, limit := range []int{1, 2} {
		rr := readResourceCall(t, resolver, vault, uri, dashOff, limit)
		if rr.Length != 3 {
			t.Fatalf("limit=%d: Length = %d, want 3 (whole em-dash)", limit, rr.Length)
		}
		if !utf8.ValidString(rr.Content) || rr.Content != "—" {
			t.Fatalf("limit=%d: Content = %q, want %q", limit, rr.Content, "—")
		}
	}
}
