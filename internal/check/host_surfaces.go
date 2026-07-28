// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/plugin"
	"github.com/suykerbuyk/vibe-palace/internal/shims"
)

// CheckHostSurfaces reports whether user-global slash-command surfaces are
// present (Phase 5). Claude health probes the **cache** copy Claude Code loads,
// not the marketplace source alone (H2). One row: Pass when every detected host
// is healthy, Info when some are missing, Skip when neither host is present.
func CheckHostSurfaces() []Result {
	var details []string
	var missing []string
	var okHosts []string

	if plugin.Detected() {
		root := plugin.ClaudeOperativePluginRoot()
		if root != "" {
			okHosts = append(okHosts, "claude")
			details = append(details, "  claude: ok (cache) "+filepath.Join(root, shims.PluginCommandsRel))
		} else {
			missing = append(missing, "claude")
			details = append(details,
				"  claude: missing cache command shims — run `vp mcp install --claude-plugin`",
				"  (marketplace source alone is not enough; Claude loads the cache copy)",
			)
		}
	}

	if grokHomePresent() {
		if shims.GrokUserCommandsHealthy() {
			okHosts = append(okHosts, "grok")
			details = append(details, "  grok: ok "+filepath.Join(shims.GrokUserPluginDir(), shims.PluginCommandsRel))
		} else {
			missing = append(missing, "grok")
			details = append(details,
				"  grok: missing command shims — run `vp mcp install --grok`",
			)
		}
	}

	if len(okHosts) == 0 && len(missing) == 0 {
		return []Result{{
			Name:    "Host surfaces",
			Status:  Skip,
			Summary: "no Claude/Grok host detected",
		}}
	}

	r := Result{Name: "Host surfaces", Details: details}
	switch {
	case len(missing) == 0:
		r.Status = Pass
		r.Summary = "user-global vpc-* present (" + strings.Join(okHosts, ", ") + ")"
	case len(okHosts) == 0:
		r.Status = Info
		r.Summary = "user-global vpc-* missing (" + strings.Join(missing, ", ") + ")"
	default:
		r.Status = Info
		r.Summary = "partial: ok=" + strings.Join(okHosts, ",") + " missing=" + strings.Join(missing, ",")
	}
	return []Result{r}
}

func grokHomePresent() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	fi, err := os.Stat(filepath.Join(home, ".grok"))
	return err == nil && fi.IsDir()
}
