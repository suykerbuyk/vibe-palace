// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// palaceBackfillDecisionsName is the tool's wire name. Tests and
// MutatingToolNames cite this constant. The mcp.Tool Name field MUST still
// be the string literal (see PalaceBackfillDecisionsTool): sourceaudit
// toolConstructors only sees ast.BasicLit names, so a constant here would
// make the derived gate miss the constructor (CI: constructors 73 vs
// registered 74).
const palaceBackfillDecisionsName = "vp_palace_backfill_decisions"

// palaceBackfillDecisionsSchema is a literal: there is no enum to inject and no
// permissive-type subtlety here, only three scalars.
//
// 🔴 THERE IS NO "required" LIST, and the omission is deliberate rather than an
// oversight. `project` is required only when `all` is false, which a flat
// required list cannot express — and, more importantly, a BARE {} call has to
// reach the handler and come back as a successful no-op dry run. A bare call is
// the probe an agent makes when it first meets a tool; refusing it at
// validation would teach nothing, and admitting it costs nothing because
// `apply` defaults to false. The one spelling that genuinely needs a target —
// a write — is refused in the handler, where both parameters are visible at
// once. See resolvePalaceBackfillProjects.
var palaceBackfillDecisionsSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug to backfill. Required when apply is true, unless all is true."
		},
		"all": {
			"type": "boolean",
			"description": "Walk EVERY project in the vault instead of one. Wins over project, which is then ignored. With apply=true this is an unbounded operator-wide write in a single synchronous call."
		},
		"apply": {
			"type": "boolean",
			"description": "Write the drawers. Defaults to false, which walks and counts but appends nothing."
		}
	}
}`)

// palaceBackfillDecisionsParams is the WIRE shape.
//
// 🔴 Apply is a VALUE bool, not a *bool, and that is the safety property: an
// absent, null or malformed `apply` decodes to false, which is the dry run. A
// pointer would make "absent" a third state that some later branch could read
// as "unspecified, so do the obvious thing" — and the obvious thing on a
// backfill is to write. Absence must mean DON'T.
type palaceBackfillDecisionsParams struct {
	Project string `json:"project"`
	All     bool   `json:"all"`
	Apply   bool   `json:"apply"`
}

// palaceBackfillCounts is the five-number tally, used once per project row and
// once for the run total so the two can never describe different things.
//
// notes_scanned and skipped_unparseable PARTITION the *.md files found in the
// sessions directory: every note is counted in exactly one of them. A note is
// "scanned" once it has been read, parsed, and had its decisions built; it is
// "unparseable" if any of those three failed. Nothing is counted twice and
// nothing is silently dropped, so a run whose two counts do not sum to the
// notes on disk is evidence of a walker bug rather than of a quiet vault.
type palaceBackfillCounts struct {
	NotesScanned       int `json:"notes_scanned"`
	DecisionsFound     int `json:"decisions_found"`
	Appended           int `json:"appended"`
	SkippedDup         int `json:"skipped_dup"`
	SkippedUnparseable int `json:"skipped_unparseable"`
}

func (c *palaceBackfillCounts) add(o palaceBackfillCounts) {
	c.NotesScanned += o.NotesScanned
	c.DecisionsFound += o.DecisionsFound
	c.Appended += o.Appended
	c.SkippedDup += o.SkippedDup
	c.SkippedUnparseable += o.SkippedUnparseable
}

// palaceBackfillProject is one project's row.
//
// 🔴 Error carries the reason THIS project's walk stopped, and its presence is
// the whole reason the run does not abort on it. An `all: true, apply: true`
// run walks projects in order and writes as it goes, so a failure on the fourth
// project happens AFTER the first three have already committed drawers to disk.
// Returning (nil, err) there would hand the operator an error and no record of
// what landed — and the recovery from that is to re-run a mutating tool blind,
// which is the one thing a backfill's report exists to make unnecessary. The
// counts on this row are the PARTIAL tally the walker had reached when it
// failed; those drawers are real and already written, so they are reported
// rather than thrown away.
//
// omitempty, so a clean row stays silent and a reader scanning for trouble sees
// only the rows that have any.
type palaceBackfillProject struct {
	Project string `json:"project"`
	palaceBackfillCounts
	Error string `json:"error,omitempty"`
}

// palaceBackfillDecisionsResult is the run report.
//
// `projects` is always present, even empty, and it is what makes a zero result
// legible: a run that walked NO project (a bare call that named none) and a run
// that walked a project holding no decisions both report zeros, and only the
// row list tells them apart. Reporting the totals alone would collapse "you
// gave me nothing to do" into "there was nothing to do" — the same
// empty-is-indistinguishable-from-absent failure the palace query's `complete`
// sentinel exists to prevent.
//
// 🔴 Complete is the LAST field and carries NO omitempty, for the reason
// spelled out on palaceQueryResult: its ABSENCE means the host truncated the
// document, and a false bool would vanish from the JSON and make "cut" and
// "whole" serialize identically. On a backfill the stakes are the writer's, not
// just the reader's — a truncated report of an apply run leaves an operator
// unable to tell which projects were actually written. Nothing may be declared
// after it; TestPalaceBackfillResultEndsWithComplete pins that on the
// serialized bytes rather than on the declaration order.
type palaceBackfillDecisionsResult struct {
	// Apply echoes the mode back, so a report read out of context cannot be
	// mistaken for the other one. A dry run's `appended` is 0 because nothing
	// was written, which looks exactly like an apply run that found only
	// duplicates.
	Apply    bool                    `json:"apply"`
	Projects []palaceBackfillProject `json:"projects"`
	palaceBackfillCounts
	Complete bool `json:"complete"`
}

// resolvePalaceBackfillProjects turns the two selector parameters into the
// ordered list of projects this run will walk.
//
// It is NOT exported, unlike ResolvePalaceQuery. That one owns a stack of
// defaults with a precedence order — a wing default, a source-type default, a
// room prune and the interaction between them — which a CLI twin would have to
// reimplement from prose. This one owns no default at all: it is a selector
// plus one vault enumeration, and any other caller reaching the same answer
// would call the same vault helper on the way.
//
// # all wins over project
//
// `all` means "every project instead of one", so naming both is not an error,
// it is redundant, and the wider set is the one the caller last asked for.
//
// # Why ListAllProjects and not ListProjects
//
// storage.ListProjects enumerates palace/ — the drawer store — and this walk
// reads Projects/<slug>/sessions. Those two trees are not supersets of each
// other, and the asymmetry is exactly the population this backfill exists for:
// a project whose notes were captured before it was ever drawer-indexed has
// sessions and NO palace/ dir, so ListProjects would skip precisely the
// projects with the most decisions to recover, and would report a clean zero
// while doing it. ListAllProjects returns the union with presence flags;
// InProjects is the half that can possibly hold a session note.
//
// # A missing target on a WRITE is an error; on a dry run it is a no-op
//
// A bare call with neither selector walks nothing and returns an honest empty
// report — see the note on the result type for why an empty `projects` list is
// legible rather than ambiguous. The same call with apply=true is REFUSED:
// silently writing nothing is an acceptable answer to a question, and never an
// acceptable answer to an instruction.
func resolvePalaceBackfillProjects(vault *storage.Vault, p palaceBackfillDecisionsParams) ([]string, error) {
	if p.All {
		presence, err := vault.ListAllProjects()
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		projects := make([]string, 0, len(presence))
		for _, pp := range presence {
			if pp.InProjects {
				projects = append(projects, pp.Slug)
			}
		}
		return projects, nil
	}

	if p.Project != "" {
		return []string{p.Project}, nil
	}

	if p.Apply {
		return nil, fmt.Errorf("project is required unless all=true")
	}
	return nil, nil
}

// backfillProjectDecisions walks one project's session notes and files their
// YAML decisions into capture.DecisionRoom.
//
// # It does not use Vault.ListSessions, on purpose
//
// ListSessions returns (nil, err) on the FIRST note it cannot read or parse,
// discarding everything it had already collected. That is defensible for a
// listing — a partial list presented as a whole is a lie — and it is exactly
// wrong here: a backfill exists to recover history, and one note that a hand
// edit left with a broken delimiter would abort the recovery of every note
// behind it, in a vault where the operator has no way to see which. This walker
// counts a bad note and keeps going, and the count is reported so the operator
// can go and look.
//
// # One append per (project, wing), not one per note
//
// Drawers are accumulated across the WHOLE walk and appended once per wing.
// AppendDrawers rescans and unmarshals the entire room JSONL on every call to
// build its dedup set — measured at 33 ms per call against a 19 MB room — so
// appending per note would be O(notes × drawers already filed). Batching is
// also what makes `skipped_dup` computable: the append reports how many of the
// batch it actually wrote, and the remainder were already on disk.
//
// The map is keyed by wing rather than assuming one, because the wing comes
// back from capture.DecisionDrawersForNote rather than being decided here. It
// holds exactly one entry today (palace.DetectWing(project, "") is the project
// slug for every note of a project), and it costs nothing to be right if that
// ever stops being true.
func backfillProjectDecisions(vault *storage.Vault, project string, apply bool) (palaceBackfillCounts, error) {
	var counts palaceBackfillCounts

	dir, err := vault.SessionDir(project)
	if err != nil {
		return counts, err
	}

	// The notes are FLAT — Projects/<slug>/sessions/YYYY-MM-DD[-<fp>]-NN.md,
	// with no date subdirectory — so one glob is the whole enumeration. Sorted
	// so a run reports its rows in the same order twice, which is what makes
	// two runs diffable.
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return counts, fmt.Errorf("glob sessions: %w", err)
	}
	sort.Strings(matches)

	byWing := make(map[string][]storage.Drawer)
	for _, note := range matches {
		data, err := os.ReadFile(note)
		if err != nil {
			counts.SkippedUnparseable++
			continue
		}
		meta, _, err := storage.ParseFrontmatter(data)
		if err != nil {
			counts.SkippedUnparseable++
			continue
		}

		// 🔴 ONLY the YAML `decisions:` list. Not Summary, not Open Threads,
		// not the body prose, and there is no `## Decisions` markdown
		// fallback to add. A decision drawer is filed because the person
		// writing the note ASSERTED that this sentence is a decision; mining
		// the body would turn that assertion back into a guess, and a guess
		// filed as a decision is indistinguishable from a real one once it is
		// on disk. A note whose decisions list is empty simply has none.
		if len(meta.Decisions) == 0 {
			counts.NotesScanned++
			continue
		}

		wing, ds, err := capture.DecisionDrawersForNote(project, meta.ID, meta.Date, meta.Decisions)
		if err != nil {
			// A malformed `date:` reaches here. It is counted with the notes
			// that would not parse and the walk continues: it is one note's
			// frontmatter that is wrong, not the run. Deliberately NOT a
			// fallback to a wall-clock stamp — that would file the note's
			// decisions under the day of the BACKFILL, which is the exact
			// mis-dating capture.DecisionFiledAt exists to prevent, on the one
			// input nobody is watching.
			counts.SkippedUnparseable++
			continue
		}

		counts.NotesScanned++
		counts.DecisionsFound += len(ds)
		byWing[wing] = append(byWing[wing], ds...)
	}

	if !apply {
		// Everything except the append. `appended` and `skipped_dup` stay 0
		// because the dedup outcome is NOT knowable without writing: it lives
		// in AppendDrawers' scan of the room under the lock it holds, and any
		// answer computed out here would be a guess that a concurrent capture
		// could invalidate between the guess and the write.
		return counts, nil
	}

	// Sorted so the appends happen in a deterministic order across runs.
	wings := make([]string, 0, len(byWing))
	for wing := range byWing {
		wings = append(wings, wing)
	}
	sort.Strings(wings)

	for _, wing := range wings {
		ds := byWing[wing]
		n, err := vault.AppendDrawers(project, wing, capture.DecisionRoom, ds)
		if err != nil {
			return counts, fmt.Errorf("append decisions for %s/%s: %w", project, wing, err)
		}
		counts.Appended += n
		counts.SkippedDup += len(ds) - n
	}
	return counts, nil
}

// PalaceBackfillDecisionsTool returns the MCP tool for
// vp_palace_backfill_decisions.
//
// It takes a *storage.Vault and nothing else — the shape every palace tool
// uses. The walk is a glob, a YAML parse and an append; there is no
// search.Engine to thread and no embedder to load, so it registers on the
// nil-engine path too.
func PalaceBackfillDecisionsTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_palace_backfill_decisions",
		Description: "Walk a project's historical session notes and file the YAML `decisions:` entries of each one as palace decision drawers, so decisions recorded before the live capture path filed them become answerable by vp_palace_query. " +
			"Only the frontmatter `decisions:` list is read — never the summary, the open threads, or the body prose. " +
			"Each drawer is stamped with its own note's calendar day, not the day of the backfill, and re-running is a no-op: the append dedups on content. " +
			"`apply` DEFAULTS TO FALSE, so a bare call is a DRY RUN — it walks, parses and counts, appends nothing, and reports `found` while leaving `appended` and `skipped_dup` at 0, because the duplicate outcome cannot be known without writing. Pass apply=true to write. " +
			"`all: true` together with `apply: true` is an UNBOUNDED OPERATOR-WIDE WRITE: every project in the vault, in one synchronous call, with no date bound and no per-project confirmation. There is no way to stop it partway. " +
			"A note that cannot be read, cannot be parsed, or carries a malformed `date:` is counted under `skipped_unparseable` and the walk continues; it never aborts the run. " +
			"This tool is STDIO-ONLY. It is mutating, so a default `vp mcp serve` strips it from the surface entirely unless --allow-writes is passed. " +
			"A binary whose MCP surface is behind the vault's stamp REFUSES EVERY CALL, the dry run included — the gate is per-tool and this tool is declared mutating, so there is no read-only spelling of it. " +
			"The result ENDS with `complete`: if you do not see `complete: true`, your host truncated it and the project rows are a PREFIX — do not read a missing project as one that was not written.",
		Schema:   palaceBackfillDecisionsSchema,
		Handler:  palaceBackfillDecisionsHandler(vault),
		Mutating: true,
		// 🔴 NO ReadOnlyWhen, deliberately. A dry run really does write
		// nothing, so a refinement admitting apply=false would type-check and
		// look correct — and it would be wrong for this tool. The surface gate
		// asks whether a binary that does not understand the vault's format
		// may proceed, and the dry run's whole product is a COUNT the operator
		// then acts on by re-running with apply=true. A stale binary that
		// mis-parses notes reports a wrong count confidently, and the operator
		// authorizes the write from it. Refusing both spellings keeps the
		// advice and the write on the same side of the gate.
	}
}

func palaceBackfillDecisionsHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p palaceBackfillDecisionsParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}

		projects, err := resolvePalaceBackfillProjects(vault, p)
		if err != nil {
			return nil, err
		}

		// make(...) not var: a nil slice serializes as "projects": null, which
		// a client reads as a missing field rather than as "I walked nothing"
		// — and "I walked nothing" is precisely what a bare call must say.
		result := palaceBackfillDecisionsResult{
			Apply:    p.Apply,
			Projects: make([]palaceBackfillProject, 0, len(projects)),
			Complete: true,
		}
		// A project that fails does NOT end the run. Each project is an
		// independent walk against its own sessions directory and its own room
		// file, so a failure in one says nothing about the next — and under
		// apply the earlier ones have already written. The error goes on that
		// project's row and the walk continues; backfillProjectDecisions
		// returns the counts it reached alongside its error, so the partial
		// tally is added to the run total rather than discarded.
		for _, project := range projects {
			counts, err := backfillProjectDecisions(vault, project, p.Apply)
			row := palaceBackfillProject{
				Project:              project,
				palaceBackfillCounts: counts,
			}
			if err != nil {
				row.Error = err.Error()
			}
			result.Projects = append(result.Projects, row)
			result.add(counts)
		}

		// complete:true even when a row carries an error. The sentinel answers
		// "did this document arrive whole", not "did every project succeed" —
		// dropping it on a partial failure would tell the operator the report
		// was TRUNCATED, which is the one reading that makes the per-project
		// errors below it unsafe to trust.
		return result, nil
	}
}
