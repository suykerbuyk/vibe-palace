// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import "strings"

// JSONReport is the stable schema emitted by `vp check --json`. Bump
// JSONReport.Version when adding or removing fields; additive changes in
// nested structs (e.g. a new optional field on JSONCheck) do not.
//
// The shape mirrors vibe-vault's `vv check --json` so tooling can consume
// both, adapted to vibe-palace's status set (no Warn — see JSONSummary) and
// binary metadata (no schema version — see JSONBinaryInfo).
type JSONReport struct {
	Version  int            `json:"version"`
	Binary   JSONBinaryInfo `json:"binary"`
	Checks   []JSONCheck    `json:"checks"`
	Summary  JSONSummary    `json:"summary"`
	ExitCode int            `json:"exit_code"`
}

// JSONBinaryInfo records surface-version and build metadata for the binary
// running the check. The caller populates it because internal/check does not
// depend on internal/surface or internal/mcp for these values.
//
// vibe-vault carries a "schema" field (its vault-context frontmatter schema
// version). vibe-palace has no equivalent context-schema counter, so that
// field is omitted here rather than fabricated.
type JSONBinaryInfo struct {
	Surface int    `json:"surface"`
	Tools   int    `json:"tools"`
	Commit  string `json:"commit"`
}

// JSONCheck is the JSON projection of a single Result. Status is the
// lowercased status string ("pass" / "fail" / "skip" / "info"). Detail folds
// the human Summary together with any remediation Details into one line.
type JSONCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// JSONSummary tallies the report by status. vibe-palace has no Warn status
// (unlike vibe-vault); skip and info are tallied in its place.
type JSONSummary struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
	Info int `json:"info"`
}

// statusName returns the lowercase JSON status string for a Status.
func statusName(s Status) string {
	switch s {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	case Skip:
		return "skip"
	case Info:
		return "info"
	default:
		return "unknown"
	}
}

// ToJSON converts a slice of check Results to the v1 JSON schema. The caller
// supplies the binary metadata block since those values come from outside the
// check package.
//
// ExitCode is set to 1 when any check failed, 0 otherwise.
func ToJSON(results []Result, binary JSONBinaryInfo) JSONReport {
	out := JSONReport{Version: 1, Binary: binary}
	out.Checks = make([]JSONCheck, 0, len(results))
	for _, res := range results {
		detail := res.Summary
		if len(res.Details) > 0 {
			joined := strings.Join(res.Details, " ")
			if detail == "" {
				detail = joined
			} else {
				detail = detail + " — " + joined
			}
		}
		out.Checks = append(out.Checks, JSONCheck{
			Name:   res.Name,
			Status: statusName(res.Status),
			Detail: detail,
		})
		switch res.Status {
		case Pass:
			out.Summary.Pass++
		case Fail:
			out.Summary.Fail++
		case Skip:
			out.Summary.Skip++
		case Info:
			out.Summary.Info++
		}
	}
	if out.Summary.Fail > 0 {
		out.ExitCode = 1
	}
	return out
}
