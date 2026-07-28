// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Selector registry — the single, shared source of the named, cheap checks.
//
// This registry used to live unexported in cmd/vp (package main), which made it
// unreachable from internal/tools by any spelling of an import: the MCP surface
// could only expose a check by hand-writing a second wrapper per check, and a
// second wrapper is a second registry of check names that diverges the first
// time a check is added to one side only. It lives here so `vp check --check`
// and the vp_check MCP tool dispatch the SAME map.
//
// RunSelected is deliberately PURE: it never consults the process working
// directory. The CLI resolves the vault from cwd and passes the root in; the
// MCP tool passes the vault bound at registration. `vp mcp` is long-lived and
// its cwd is the host's launch directory, not the project the agent is working
// in, so re-resolving here would reintroduce the wrong-vault / silent-skip class
// the bound-vault pattern already solved.
//
// Nothing here reaches check.Run, and nothing here loads the embedder — that is
// the entire reason the selector path exists.

package check

import (
	"fmt"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Producers maps a selector name to a light, dependency-light producer.
// Designed to grow one cheap check at a time. The string arg is the resolved
// vault path (cheap to compute; "" when unresolved — CheckSurface and
// CheckVaultFilesystem tolerate it on their own terms, every other producer is
// wrapped in a guard that degrades to Skip).
//
// Adding an entry here MUST also add it to ProducerOrder; TestProducerOrderCoversProducers
// fails the build otherwise.
var Producers = map[string]func(vaultRoot string) []Result{
	"surface": func(vaultRoot string) []Result { return []Result{CheckSurface(vaultRoot)} },
	// CheckVaultFilesystem takes the root directly, like CheckSurface, and
	// already guards the empty root itself (vaultfs.go) by returning the same
	// Skip/"no vault configured" row the wrapped producers below synthesize —
	// so no wrapper guard is needed here, and the degradation contract still
	// holds.
	"vault-filesystem": func(vaultRoot string) []Result { return []Result{CheckVaultFilesystem(vaultRoot)} },
	"stray-scaffolds": func(vaultRoot string) []Result {
		if vaultRoot == "" {
			return []Result{{Name: "Stray scaffolds", Status: Skip, Summary: "no vault configured"}}
		}
		return []Result{CheckStrayScaffolds(storage.NewVault(vaultRoot))}
	},
	"resume-caps": func(vaultRoot string) []Result {
		if vaultRoot == "" {
			return []Result{{Name: "Resume caps", Status: Skip, Summary: "no vault configured"}}
		}
		return []Result{CheckResumeCaps(storage.NewVault(vaultRoot))}
	},
	"resume-refs": func(vaultRoot string) []Result {
		if vaultRoot == "" {
			return []Result{{Name: "Resume refs", Status: Skip, Summary: "no vault configured"}}
		}
		return []Result{CheckResumeRefs(storage.NewVault(vaultRoot))}
	},
	"core-floor": func(vaultRoot string) []Result {
		if vaultRoot == "" {
			return []Result{{Name: "Core floor", Status: Skip, Summary: "no vault configured"}}
		}
		return []Result{CheckCoreFloor(storage.NewVault(vaultRoot))}
	},
	"pin-coverage": func(vaultRoot string) []Result {
		if vaultRoot == "" {
			return []Result{{Name: "Pin coverage", Status: Skip, Summary: "no vault configured"}}
		}
		return []Result{CheckPinCoverage(storage.NewVault(vaultRoot))}
	},
}

// ProducerOrder is the DECLARED order the producers run in when the caller does
// not name them — cheapest and most structural first, so a reader sees the
// surface verdict before the per-project advisories.
//
// This is a correctness requirement, not presentation polish. Go randomizes map
// iteration, and the default-all path below is the first iteration of Producers
// anywhere in the tree (the CLI has always walked the caller's comma-list, so
// order was caller-specified). Without a declared order the MCP checks[] array
// and the CLI table would reorder between identical runs, which makes agent
// narration irreproducible and output-comparing tests flap.
//
// The vault-wide, structural checks come first — the filesystem probe and the
// scaffold scan describe the vault ITSELF — and the per-project resume
// advisories follow. That is also the relative order `vp check`'s full suite
// emits them in (cmd/vp/cmd_check.go, gatherCheckResults), so a reader
// comparing an MCP `checks[]` array against the CLI table sees one sequence
// rather than two arbitrary ones.
//
// `surface` is the one deliberate divergence: it leads here (cheapest, and the
// verdict everything else is read under) but the full suite emits it LAST, as
// the closing line of the report. That is precisely why gatherCheckResults
// cannot just dispatch this slice wholesale — doing so would reorder `vp
// check`'s long-standing output.
var ProducerOrder = []string{
	"surface",
	"vault-filesystem",
	"stray-scaffolds",
	"resume-caps",
	"resume-refs",
	"core-floor",
	"pin-coverage",
}

// RunSelected runs the named checks against the supplied vault root and returns
// their results in order.
//
// filter is a comma-separated list of Producers keys. An empty (or all-blank)
// filter selects EVERY producer, in ProducerOrder — agents and templates omit
// optional arguments constantly, so "omitted" must mean "the whole cheap suite",
// never a user-facing error on the happy path. An explicitly supplied list that
// names nothing runnable (e.g. ",,") keeps the CLI's long-standing
// "no checks selected" error. An unknown name is always an error.
//
// vaultRoot is passed in, never resolved here — see the package comment.
func RunSelected(vaultRoot string, filter string) ([]Result, error) {
	if strings.TrimSpace(filter) == "" {
		results := make([]Result, 0, len(ProducerOrder))
		for _, name := range ProducerOrder {
			results = append(results, Producers[name](vaultRoot)...)
		}
		return results, nil
	}

	var results []Result
	for raw := range strings.SplitSeq(filter, ",") {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		prod, ok := Producers[name]
		if !ok {
			return nil, fmt.Errorf("unknown check: %s", name)
		}
		results = append(results, prod(vaultRoot)...)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no checks selected")
	}
	return results, nil
}
