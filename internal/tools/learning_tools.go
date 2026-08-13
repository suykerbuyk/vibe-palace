// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Vault-wide learning MCP tools (vp_list_learnings, vp_get_learning).
//
// These are read-only JSON-Schema + (de)serialisation wrappers over the
// vault-wide learnings storage layer (storage.Vault.{ListLearnings,GetLearning}).
// Learnings are cross-project and live under {vault}/Knowledge/learnings/<slug>.md.

package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// ---------------------------------------------------------------------------
// vp_list_learnings
// ---------------------------------------------------------------------------

type listLearningsParams struct {
	FilterType string `json:"filter_type,omitempty"`
}

type listLearningsResult struct {
	Learnings []storage.LearningMetadata `json:"learnings"`
}

var listLearningsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"filter_type": {"type": "string", "description": "Optional learning type filter: user, feedback, or reference. Empty (default) returns all; an unknown value returns an empty set."}
	}
}`)

// ListLearningsTool lists the frontmatter index (slug/name/description/type) for
// the vault-wide learnings. Bodies are NOT returned — this is the cheap index;
// use vp_get_learning for bodies.
func ListLearningsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_list_learnings",
		Description: "List the frontmatter index (slug/name/description/type) of the " +
			"vault-wide cross-project learnings. Bodies are NOT returned — this is the " +
			"cheap index; use vp_get_learning for bodies. filter_type optionally narrows " +
			"to one of user, feedback, reference.",
		Schema: listLearningsSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p listLearningsParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			learnings, err := vault.ListLearnings(p.FilterType)
			if err != nil {
				return nil, fmt.Errorf("list learnings: %w", err)
			}
			// ListLearnings guarantees a non-nil slice (so this marshals as []
			// not null), so no nil coercion is needed here.
			return listLearningsResult{Learnings: learnings}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// vp_get_learning
// ---------------------------------------------------------------------------

// learningExcerptCap bounds the rune-safe excerpt returned when the caller drops
// the inline body (include_content=false). It mirrors taskExcerptCap so the
// content-URI idiom is uniform across read-only detail tools.
const learningExcerptCap = 1500

type getLearningParams struct {
	Slug string `json:"slug"`
	// IncludeContent is tri-state: nil or true ⇒ full inline Content; false ⇒
	// drop a large body and return only ContentURI + a bounded Excerpt.
	IncludeContent *bool `json:"include_content,omitempty"`
}

// getLearningResult carries the same content-URI idiom as getTaskResult, and
// carried the same inversion: the handle was declared BELOW the body it exists
// to rescue. Declaration order is wire order is cut order (encoding/json emits
// in declaration order; nothing on the response path re-serializes through a
// map), so a hatch under the bulk arrives only when the bulk already fit.
// Learnings measure 1,237 bytes on the live vault today — comfortably under a
// host's 19,968-byte flat cap — but the body is unbounded in principle and the
// layout is fixed here rather than left as a size coincidence to be discovered
// again by the first long learning. No `complete` sentinel: this task added it
// to the tools MEASURED over the cap, and this one is two orders under it.
type getLearningResult struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	// ContentURI and ContentSize are ALWAYS set. ContentURI addresses the full
	// body for vp_read_resource; ContentSize is its length in bytes.
	ContentURI  string `json:"content_uri"`
	ContentSize int    `json:"content_size"`
	// Excerpt is a rune-safe leading slice of the body, set only when the inline
	// Content was dropped (include_content=false). Bounded by learningExcerptCap,
	// so it rides above Content with the handles rather than with the bulk.
	Excerpt string `json:"excerpt,omitempty"`
	// Content is the full learning body — the only unbounded field, and so the
	// one a host cut is allowed to land in. Omitted when include_content=false.
	Content string `json:"content,omitempty"`
}

var getLearningSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"slug":            {"type": "string", "description": "Learning slug (filename stem)."},
		"include_content": {"type": "boolean", "description": "Include the full inline learning body. Default true. When false, a large body is dropped in favour of content_uri + a short excerpt (fetch the full body via vp_read_resource); a small body that cannot be truncated is still returned inline. content present means the body is complete; excerpt present means it is partial."}
	},
	"required": ["slug"]
}`)

// GetLearningTool reads a single vault-wide learning by slug. It follows the
// vp_get_task content-URI idiom: ContentURI/ContentSize are always set, and the
// tri-state include_content controls whether the body is returned inline or, for
// a large body, dropped in favour of an excerpt + the content URI.
func GetLearningTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_get_learning",
		Description: "Get full learning detail including metadata and content.",
		Schema:      getLearningSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var p getLearningParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("parse params: %w", err)
			}
			if p.Slug == "" {
				return nil, fmt.Errorf("slug is required")
			}
			learning, err := vault.GetLearning(p.Slug)
			if err != nil {
				return nil, fmt.Errorf("get learning: %w", err)
			}

			res := getLearningResult{
				Slug:        learning.Slug,
				Name:        learning.Name,
				Description: learning.Description,
				Type:        learning.Type,
				ContentURI:  mcp.LearningURI(p.Slug),
				ContentSize: len(learning.Content),
			}
			// Tri-state: nil/true keeps the full inline body. false drops the body
			// for a host that truncates large results — but only when it is actually
			// large: a body that already fits within learningExcerptCap cannot
			// truncate, so we still return it inline. Content present ⇒ complete body;
			// Excerpt present ⇒ partial, fetch the rest via ContentURI.
			switch {
			case p.IncludeContent == nil || *p.IncludeContent:
				res.Content = learning.Content
			case len(learning.Content) <= learningExcerptCap:
				res.Content = learning.Content
			default:
				res.Excerpt = runeSafeExcerpt(learning.Content, learningExcerptCap)
			}
			return res, nil
		},
	}
}
