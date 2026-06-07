// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"

	"github.com/suykerbuyk/vibe-palace/internal/mcphost"
)

// CheckMCPHosts reports, for each AI coding host detected on this machine,
// whether the vibe-palace MCP server is registered with it. Hosts that are not
// detected are omitted to keep the report quiet; when none are detected a
// single Skip row explains how to register. This is advisory (Pass/Info/Skip,
// never Fail) — an unregistered host is a suggestion, not a failure.
func CheckMCPHosts() []Result {
	return checkHostRows(mcphost.Registry())
}

// checkHostRows is the host-list-injectable core of CheckMCPHosts, separated so
// tests can drive it with stub hosts instead of the live machine registry.
func checkHostRows(hosts []mcphost.Host) []Result {
	var rows []Result
	for _, h := range hosts {
		if !h.Detected() {
			continue
		}
		r := Result{Name: "MCP host: " + h.Name()}
		installed, err := h.Installed()
		switch {
		case err != nil:
			r.Status = Info
			r.Summary = "status unknown: " + err.Error()
		case installed:
			r.Status = Pass
			r.Summary = "vibe-palace registered"
		default:
			r.Status = Info
			r.Summary = "detected but not registered"
			r.Details = append(r.Details, fmt.Sprintf("  Run `vp mcp install %s` to register.", h.Flag()))
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return []Result{{
			Name:    "MCP hosts",
			Status:  Skip,
			Summary: "no AI coding host detected (Claude/Grok/Zed)",
		}}
	}
	return rows
}
