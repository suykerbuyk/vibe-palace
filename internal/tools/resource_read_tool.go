// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	vpctx "github.com/suykerbuyk/vibe-palace/internal/context"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// defaultReadResourceLimit is the page size used when the caller omits limit
// (or passes a non-positive value). It is a byte budget, not a rune count.
const defaultReadResourceLimit = 16000

type readResourceParams struct {
	URI    string `json:"uri"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"` // default 16000
}

// readResourceResult is the layout the rest of the surface was hoisted to
// match: every instrument and address first, the unbounded body last. It was
// already correct — this is where the pattern came from — and it earns the
// sentinel too, because `limit` has no upper clamp, so the designated RECOVERY
// tool can itself be made to overflow a host cap (measured 191,927 bytes at
// limit=10000000 on the live vault, against a 19,968-byte flat cap; the 16,000
// default emits 16,558 with only ~3.4 KB of headroom). Clamping `limit` is a
// separate decision and is filed separately.
type readResourceResult struct {
	URI       string `json:"uri"`
	MIMEType  string `json:"mime_type"`
	Offset    int    `json:"offset"`
	Length    int    `json:"length"`
	TotalSize int    `json:"total_size"`
	EOF       bool   `json:"eof"`
	Content   string `json:"content"`

	// 🔴 THE TERMINAL SENTINEL — last field, no omitempty, always true on a
	// successful return. It answers a DIFFERENT question from `eof`, and
	// confusing the two is the trap: `eof` is about the RESOURCE (you have
	// reached its end), `complete` is about this DOCUMENT (every byte of this
	// page reached you). A page can be eof=true and still arrive cut in half,
	// and without the sentinel the caller would reassemble a body that is
	// silently short and then compare-and-set against it. Absence is the
	// signal — re-request this same offset with a smaller limit. No omitempty
	// because a false bool would vanish and make "cut" and "whole" serialize
	// identically. Anything declared after this field re-opens the hole.
	Complete bool `json:"complete"`
}

// alignRuneBoundary backs an index off to the nearest UTF-8 rune boundary at or
// below it, so a page never starts or ends mid-rune. An index already on a
// boundary (or == len(s)) is returned unchanged. This is the load-bearing
// guard against the U+FFFD rewrite in encoding/json: a byte slice that splits a
// multibyte rune (this vault is saturated with 3-byte em-dashes) would corrupt
// the tail of one page and the head of the next, so reassembled pages would no
// longer be byte-identical to the source.
func alignRuneBoundary(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	if i < 0 {
		return 0
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// runeSafeExcerpt returns the leading prefix of s up to at most maxBytes bytes
// without ever splitting a UTF-8 rune. When a newline falls within the capped
// region it cuts at the last newline (a cleaner markdown boundary); otherwise
// it cuts at the nearest rune boundary at or below maxBytes. Same U+FFFD hazard
// as the pager: a naive byte slice would corrupt a trailing em-dash.
func runeSafeExcerpt(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	end := alignRuneBoundary(s, maxBytes)
	if nl := strings.LastIndexByte(s[:end], '\n'); nl > 0 {
		return s[:nl]
	}
	return s[:end]
}

var readResourceSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"uri":    {"type": "string", "description": "vibe-palace:// resource URI to page (e.g. vibe-palace://resume/myproject)."},
		"offset": {"type": "integer", "description": "Byte offset to start reading from. Default 0. Advance by the returned offset+length, NEVER by the requested limit."},
		"limit":  {"type": "integer", "description": "Maximum bytes to read this page. Default 16000. The actual bytes emitted (length) may be slightly less when the cut is backed off a UTF-8 rune boundary."}
	},
	"required": ["uri"]
}`)

// ReadResourceTool is the generic paginated fallback reader for any
// vibe-palace:// resource body. It is the guaranteed floor for hosts that
// truncate large inline tool results: pages are rune-safe (never split a
// multibyte rune) and the caller walks the body by advancing offset to the
// returned offset+length until eof.
//
// It is deliberately STATELESS: each call re-resolves the full body via
// ResolveURI (one vault read, or one precedence walk for resume/workflow). That
// is O(body) per page, so paging a large body in many small windows is O(n²) in
// bytes — an accepted tradeoff because (a) the native resources/read channel is
// the primary path for hosts that support it, (b) this tool is the fallback for
// hosts that truncate, and (c) the default ~16 KB page fetches most bodies in a
// single call, so the quadratic walk is the rare worst case, not the norm.
// A cache is intentionally avoided to keep the read path free of cross-call
// state and its invalidation/concurrency hazards.
func ReadResourceTool(resolver *vpctx.Resolver, vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_read_resource",
		Description: "Page any vibe-palace:// resource body rune-safely. Returns " +
			"{uri, mime_type, offset, length, total_size, eof, content, complete}. The " +
			"guaranteed fallback when a primary tool's inline body is truncated " +
			"by the host. IMPORTANT: advance offset by the returned offset+length, " +
			"NOT by the requested limit — pages are backed off UTF-8 rune " +
			"boundaries so length may be slightly under limit. Read until eof. " +
			"`eof` is about the RESOURCE, `complete` is about this PAGE: if you do not see " +
			"`complete: true` your host truncated the page itself — re-request the same offset with a smaller limit.",
		Schema:  readResourceSchema,
		Handler: readResourceHandler(resolver, vault),
	}
}

// readResourceHandler closes over the resolver/vault captured at registration —
// it MUST NOT read them from ctx, since the streamable-HTTP transport injects no
// contextFunc (same trap as the resource providers in resources.go).
func readResourceHandler(resolver *vpctx.Resolver, vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p readResourceParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.URI == "" {
			return nil, fmt.Errorf("uri is required")
		}

		// ResolveURI yields a distinct, self-describing error for an
		// unknown-scheme / unknown-type / malformed URI (a caller bug) versus a
		// content-not-found error bubbled up from the backing vault/resolver.
		content, mime, err := ResolveURI(p.URI, resolver, vault)
		if err != nil {
			return nil, fmt.Errorf("read resource %q: %w", p.URI, err)
		}

		limit := p.Limit
		if limit <= 0 {
			limit = defaultReadResourceLimit
		}

		// alignRuneBoundary already clamps i<0 → 0 and i>=len → len, so it
		// subsumes both the start clamp and the end-vs-len guard — no need to
		// re-derive those bounds here. Clamp limit to the bytes remaining first
		// so offset+limit can never overflow int for a hostile large limit.
		offset := alignRuneBoundary(content, p.Offset)
		if remaining := len(content) - offset; limit > remaining {
			limit = remaining
		}
		end := alignRuneBoundary(content, offset+limit)
		// Degenerate case: a single rune larger than limit backs off to
		// end <= offset and would stall the pager. Advance forward to include
		// the whole rune so progress is always made.
		if end <= offset && offset < len(content) {
			_, size := utf8.DecodeRuneInString(content[offset:])
			end = min(offset+size, len(content))
		}

		return readResourceResult{
			URI:       p.URI,
			MIMEType:  mime,
			Offset:    offset,
			Length:    end - offset,
			TotalSize: len(content),
			EOF:       end == len(content),
			Content:   content[offset:end],
			Complete:  true,
		}, nil
	}
}
