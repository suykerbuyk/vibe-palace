// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Diagnostic-suite tool (vp_check). A read-only probe that runs the SAME named,
// cheap checks `vp check --check NAME[,NAME...]` runs, over the SAME registry
// (check.Producers), so an agent on any host — Claude, Grok, Zed — reaches the
// diagnostics without shelling out to the binary.
//
// This replaces the per-check wrapper pattern. vp_check_resume_refs was one
// hand-written tool for one check; growing that pattern means a second registry
// of check names that diverges from the CLI's the first time a check is added to
// one side only. All verdict logic stays in internal/check; this layer only
// marshals []check.Result into a stable, self-describing JSON envelope.
//
// It never calls check.Run, so it never constructs an embedder: check.Run
// reaches CheckEmbedder → embedder.NewONNX on a healthy install, which costs
// tens of seconds and a ~90MB download on a cold cache. The selector path is
// the whole point.
//
// The vault is the one bound at REGISTRATION. Nothing here consults the process
// working directory: `vp mcp` is long-lived and its cwd is the host's launch
// directory, not the project the agent is working in.

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// CheckRow is one diagnostic row: the projection of a single check.Result.
// Details stays an ARRAY — check.ToJSON folds Summary and Details into one
// string, which is lossy for a check whose remediation spans several lines,
// and check.Status is an int, so the raw Result cannot be marshalled either.
type CheckRow struct {
	Name    string   `json:"name"`              // human check name, e.g. "Resume refs"
	Status  string   `json:"status"`            // "pass" | "info" | "skip" | "fail"
	Summary string   `json:"summary"`           // one-line verdict
	Details []string `json:"details,omitempty"` // per-breach lines, verbatim
}

// CheckSuiteResult is the JSON output of vp_check. Status is an ADVISORY
// worst-of roll-up (see checkAggregateStatus); consumers key off Checks.
type CheckSuiteResult struct {
	Status  string     `json:"status"`  // advisory roll-up: "pass" | "info" | "skip" | "fail"
	Summary string     `json:"summary"` // one-line tally
	Checks  []CheckRow `json:"checks"`  // one row per check, in the order they ran
}

// checkSchema is built from check.ProducerOrder so the enum of accepted
// selector names cannot drift from the registry the CLI dispatches. A
// hand-written enum would be the third copy of the concept this change exists
// to collapse.
var checkSchema = buildCheckSchema()

func buildCheckSchema() json.RawMessage {
	enum, err := json.Marshal(check.ProducerOrder)
	if err != nil { // unreachable: []string always marshals
		panic("vp_check schema: marshal ProducerOrder: " + err.Error())
	}
	names := strings.Join(check.ProducerOrder, ", ")
	return json.RawMessage(fmt.Sprintf(`{
	"type": "object",
	"properties": {
		"checks": {
			"type": "array",
			"items": {"type": "string", "enum": %s},
			"description": "Names of the checks to run. OPTIONAL — omit it (or pass []) to run ALL of them, in declared order: %s. An unknown name is an error rather than a silent skip."
		}
	}
}`, enum, names))
}

// checkStatusString maps a check.Status to the lower-cased verdict string used
// in the tool's JSON output. Unlike the per-check wrappers this covers all four
// states: the selector set spans checks that can Fail (surface) and checks that
// never do (the advisory scans).
func checkStatusString(s check.Status) string {
	switch s {
	case check.Fail:
		return "fail"
	case check.Info:
		return "info"
	case check.Skip:
		return "skip"
	default:
		return "pass"
	}
}

// checkStatusRank orders the verdicts for the advisory roll-up: a Fail
// outranks a finding, a finding outranks a check that could not run, and a
// check that could not run outranks a clean pass.
func checkStatusRank(s check.Status) int {
	switch s {
	case check.Fail:
		return 3
	case check.Info:
		return 2
	case check.Skip:
		return 1
	default:
		return 0
	}
}

// checkAggregateStatus rolls the rows up worst-of. This is ADVISORY ONLY: the
// underlying checks disagree about an absent vault (with Root == "" surface
// returns Info while resume-refs returns Skip; with a root set but vanished
// surface returns Fail while resume-refs returns Pass), so a single verdict
// cannot be authoritative for a set of independent scans. Consumers key off the
// per-check rows.
func checkAggregateStatus(results []check.Result) check.Status {
	worst := check.Pass
	for _, r := range results {
		if checkStatusRank(r.Status) > checkStatusRank(worst) {
			worst = r.Status
		}
	}
	return worst
}

// checkSummaryLine renders the deterministic one-line tally.
func checkSummaryLine(results []check.Result) string {
	var pass, info, skip, fail int
	for _, r := range results {
		switch r.Status {
		case check.Fail:
			fail++
		case check.Info:
			info++
		case check.Skip:
			skip++
		default:
			pass++
		}
	}
	return fmt.Sprintf("%d checks: %d pass, %d info, %d skip, %d fail",
		len(results), pass, info, skip, fail)
}

// CheckTool exposes the named, embedder-free diagnostic checks as a
// non-mutating MCP tool.
//
// The constructor stashes the vault pointer and static metadata ONLY. It must
// never dereference the vault: registeredToolCount (cmd/vp/cmd_check.go) and
// cmd/vp/tool_surface_golden_test.go both register the entire tool set against
// storage.NewVault("") purely to count tools, and that works only because no
// constructor touches the vault at registration time.
func CheckTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name: "vp_check",
		Description: "Read-only diagnostics: run the named vibe-palace health checks — the same " +
			"set `vp check --check` runs, over the same registry — host-agnostically. " +
			"Selectable: " + strings.Join(check.ProducerOrder, ", ") + ". " +
			"The `checks` argument is OPTIONAL: omit it to run ALL of them, in that " +
			"declared order. Returns {status, summary, checks[]} where each row is " +
			"{name, status (pass|info|skip|fail), summary, details[]}; details carries " +
			"the verbatim per-breach lines. The top-level status is an ADVISORY " +
			"worst-of roll-up — the checks disagree about an absent vault, so KEY OFF " +
			"THE PER-CHECK ROWS, not the aggregate. Fast: this never loads the embedder " +
			"and never builds the tool registry. It scans the vault this server was " +
			"started against, not the caller's working directory. Never writes vault " +
			"data — the only file it ever creates is vault-filesystem's transient, " +
			"self-deleting probe.",
		Schema: checkSchema,
		Handler: func(_ context.Context, params json.RawMessage) (any, error) {
			var args struct {
				Checks []string `json:"checks"`
			}
			if err := unmarshalParams(params, &args); err != nil {
				return nil, err
			}

			// Only the registration-bound root. Joining an empty selection
			// yields "", which check.RunSelected reads as "every producer".
			results, err := check.RunSelected(vault.Root, strings.Join(args.Checks, ","))
			if err != nil {
				return nil, err
			}

			rows := make([]CheckRow, 0, len(results))
			for _, r := range results {
				rows = append(rows, CheckRow{
					Name:    r.Name,
					Status:  checkStatusString(r.Status),
					Summary: r.Summary,
					Details: r.Details,
				})
			}

			return CheckSuiteResult{
				Status:  checkStatusString(checkAggregateStatus(results)),
				Summary: checkSummaryLine(results),
				Checks:  rows,
			}, nil
		},
	}
}
