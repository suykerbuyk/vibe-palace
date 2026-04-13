// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

// Item is one routing decision: "this slice of this file goes to that
// destination." The planner produces an ordered slice of Items; the writer
// consumes it in the same order.
type Item struct {
	// Source is the adapter for the originating file.
	Source Source
	// SourcePath is the absolute (symlink-resolved) path to the source
	// file on disk.
	SourcePath string
	// DisplayPath is the repo-relative path used in dry-run output.
	DisplayPath string
	// Section is the originating section. Body holds the content that
	// will be written (without the heading line for heading-based
	// sources; the heading is re-emitted in the dated subheading).
	Section Section
	// Dest is the vault destination. Dest.IsZero() means "keep in place
	// in the source file" (only the managed block survives the rewrite —
	// see writer.go for why keep-in-place still means the body is
	// dropped from CLAUDE.md).
	Dest Destination
	// BodyHash is sha256(Section.Body), used by the writer to dedup
	// re-runs against existing dated subheadings.
	BodyHash string
	// Reason populates the dry-run "why" column when the classifier
	// returned DestMisc or an ambiguous result. Empty for clean routes.
	Reason string
}

// Plan is the ordered list of items the writer will apply, plus per-source
// metadata the orchestrator needs for source-file rewriting.
type Plan struct {
	Items []Item
	// Sources is the set of source files that contributed to the plan.
	// Duplicated Items from the same file share a Source entry here.
	Sources []PlannedSource
}

// PlannedSource describes one source file within a plan: the adapter, the
// resolved absolute path, the display path, the original bytes (for
// backup), and whether a managed block was detected on read (so the
// rewriter can preserve it). Unsupported sources appear here with
// Supported=false and zero Items in the plan.
type PlannedSource struct {
	Source        Source
	AbsPath       string
	DisplayPath   string
	Original      []byte
	HadBlock      bool
	Supported     bool
	UsesCRLF      bool
	ContributedAt []int // indexes into Plan.Items; empty when Supported=false
}

// BuildPlan enumerates agent-context files under projectRoot, splits each
// supported file, classifies every section, and returns the full routing
// plan. It does no I/O to the vault — it only reads the source files.
func BuildPlan(projectRoot string) (*Plan, error) {
	targets, _ := agentfile.Detect(projectRoot)
	plan := &Plan{}

	for _, t := range targets {
		src := SourceForDisplayName(t.DisplayName)
		if src == nil {
			continue
		}
		data, err := os.ReadFile(t.Path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", t.Path, err)
		}
		display := t.DisplayName
		if !filepath.IsAbs(display) {
			display = filepath.Join(projectRoot, display)
			rel, rerr := filepath.Rel(projectRoot, display)
			if rerr == nil {
				display = rel
			}
		}
		ps := PlannedSource{
			Source:      src,
			AbsPath:     t.Path,
			DisplayPath: t.DisplayName,
			Original:    data,
			Supported:   src.Supported(),
			UsesCRLF:    containsCRLF(data),
		}
		if _, hasBlock, _ := agentfile.ScanBlock(t.Path); hasBlock {
			ps.HadBlock = true
		}
		if !ps.Supported {
			plan.Sources = append(plan.Sources, ps)
			continue
		}

		sections := Split(data, src.SplitStrategy())
		for _, s := range sections {
			body := strings.TrimRight(s.Body, "\n")
			if strings.TrimSpace(body) == "" {
				continue
			}
			var dest Destination
			if src.SplitStrategy() == SplitWholeFile {
				dest = src.FixedDestination()
			} else {
				dest = Classify(s.Heading, body)
			}
			if dest == DestKeepInPlace {
				// Keep-in-place sections are not written to the vault,
				// but they are also not preserved in the source file —
				// the rewriter replaces the file with just the managed
				// block. We skip adding an Item at all.
				continue
			}
			item := Item{
				Source:      src,
				SourcePath:  t.Path,
				DisplayPath: t.DisplayName,
				Section:     s,
				Dest:        dest,
				BodyHash:    hashBody(body),
			}
			if dest == DestMisc {
				item.Reason = "unrecognized heading — defaults to doc/misc.md"
			}
			ps.ContributedAt = append(ps.ContributedAt, len(plan.Items))
			plan.Items = append(plan.Items, item)
		}
		plan.Sources = append(plan.Sources, ps)
	}
	return plan, nil
}

// hashBody returns the first 16 hex chars of sha256 over the normalized
// body (trimmed of trailing whitespace, LF line endings). 16 chars is
// overkill for collision resistance within a single vault file and keeps
// the dated-subheading marker readable.
func hashBody(body string) string {
	norm := strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])[:16]
}

func containsCRLF(data []byte) bool {
	for i := 0; i+1 < len(data); i++ {
		if data[i] == '\r' && data[i+1] == '\n' {
			return true
		}
	}
	return false
}
