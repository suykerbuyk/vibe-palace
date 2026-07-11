// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var healthSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"project": {
			"type": "string",
			"description": "Project slug."
		},
		"hours": {
			"type": "integer",
			"description": "Look-back window in hours (default 24, max 720). Widens the window for BOTH warn_counts and recent_warns."
		},
		"limit": {
			"type": "integer",
			"description": "Max entries in the recent_warns list (default 20, max 1000). Caps ONLY recent_warns — warn_counts still tallies every WARN/ERROR in the window."
		}
	},
	"required": ["project"]
}`)

// healthParams carries the vp_health input. project stays required; hours and
// limit are optional scalar tuning knobs (see healthSchema for semantics).
type healthParams struct {
	Project string `json:"project"`
	Hours   int    `json:"hours,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// HealthResult is the structured response from vp_health.
//
// ScanError is set when the log scan aborted before EOF (bufio.Scanner caps a
// line at 64 KiB, so one oversized entry ends the loop). The counts below are
// then a PREFIX of the log, not the whole of it — without this field a truncated
// scan is indistinguishable from a clean one, and the health report would
// under-count warnings while looking authoritative.
type HealthResult struct {
	Status      string         `json:"status"`
	RecentWarns []LogEntry     `json:"recent_warns,omitempty"`
	WarnCounts  map[string]int `json:"warn_counts,omitempty"`
	LogPath     string         `json:"log_path"`
	LogSize     int64          `json:"log_size_bytes"`
	ScanError   string         `json:"scan_error,omitempty"`
}

// LogEntry represents a parsed log line.
type LogEntry struct {
	Time  string `json:"time"`
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

// HealthTool returns the MCP tool for vp_health.
func HealthTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:        "vp_health",
		Description: "Runtime health: recent warnings/errors from the vp log file.",
		Schema:      healthSchema,
		Handler:     healthHandler(vault),
	}
}

func healthHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p healthParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}
		if p.Project == "" {
			return nil, fmt.Errorf("project is required")
		}

		hours := p.Hours
		if hours <= 0 {
			hours = 24
		}
		if hours > 720 {
			hours = 720
		}

		limit := p.Limit
		if limit <= 0 {
			limit = 20
		}
		if limit > 1000 {
			limit = 1000
		}

		logPath := vault.VaultLocalDir() + "/vp.log"
		result := HealthResult{
			Status:     "healthy",
			WarnCounts: map[string]int{},
			LogPath:    logPath,
		}

		info, err := os.Stat(logPath)
		if err != nil {
			// Missing log = healthy.
			return result, nil
		}
		result.LogSize = info.Size()

		f, err := os.Open(logPath)
		if err != nil {
			return result, nil
		}
		defer f.Close()

		cutoff := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339)

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var entry map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue // tolerate malformed lines
			}

			level, _ := entry["level"].(string)
			if level != "WARN" && level != "ERROR" {
				continue
			}

			ts, _ := entry["time"].(string)
			if ts != "" && ts < cutoff {
				continue
			}

			msg, _ := entry["msg"].(string)
			le := LogEntry{Time: ts, Level: level, Msg: msg}

			if len(result.RecentWarns) < limit {
				result.RecentWarns = append(result.RecentWarns, le)
			}

			category := categorizeMsg(msg)
			result.WarnCounts[category]++
		}

		// A scan that stops short (an oversized line) leaves the tallies above
		// covering only a prefix of the log. Report the partial results — the
		// tool tolerates a bad log rather than failing the caller — but say so,
		// so the numbers are not read as complete.
		if err := scanner.Err(); err != nil {
			result.ScanError = err.Error()
		}

		if len(result.RecentWarns) > 0 {
			hasError := false
			for _, w := range result.RecentWarns {
				if w.Level == "ERROR" {
					hasError = true
					break
				}
			}
			if hasError {
				result.Status = "errors"
			} else {
				result.Status = "warnings"
			}
		}

		return result, nil
	}
}

// categorizeMsg extracts a category prefix from a log message.
func categorizeMsg(msg string) string {
	if idx := strings.Index(msg, ":"); idx > 0 && idx < 40 {
		return strings.TrimSpace(msg[:idx])
	}
	if len(msg) > 40 {
		return msg[:40]
	}
	return msg
}
