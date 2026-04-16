// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// AutoSummary generates a deterministic session summary from recent git
// history in the given working directory.
func AutoSummary(cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "log", "--oneline", "-5")
	out, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return "Auto-captured session"
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	trimmed := make([]string, 0, len(lines))
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			trimmed = append(trimmed, t)
		}
	}

	if len(trimmed) == 0 {
		return "Auto-captured session"
	}

	return "Auto-captured session. Recent: " + strings.Join(trimmed, "; ")
}
