// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// This file is package templates_test (external) on purpose: internal/check
// imports internal/templates, so an in-package test cannot import check
// without a cycle. The external test package can, and that import is the
// entire value of this file — it is the only assertion in the tree that
// compares what the shipped templates ASK FOR against what the registry
// actually SERVES.
package templates_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/check"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// selectorCall matches the vp_check invocation the three delivery templates
// carry, and captures the bracketed name list.
var selectorCall = regexp.MustCompile(`\{"checks": \[([^\]]*)\]\}`)

// TestSelectorLiteralNamesLiveProducers pins the half of the delivery contract
// that a literal-against-literal assertion structurally cannot reach.
//
// TestEmbeddedCommands_VaultHygieneChecks already pins that all three templates
// carry the SAME selector call, byte-identical. That guards divergence between
// the delivery sites — but all three can agree perfectly on a name that no
// longer exists. `vp_check` errors on an unknown name rather than skipping it
// ("an unknown name is an error rather than a silent skip", selector.go), so a
// check renamed or deleted in Go turns every restart, wrap and epic rollout
// into a hard tool error, and nothing in the tree notices until an agent runs
// one. That is this project's signature bug with the arrow reversed: not a
// capability nothing invokes, but an invocation of a capability that is gone.
//
// So this compares the templates against check.Producers, the live map — never
// against another copy of the list.
func TestSelectorLiteralNamesLiveProducers(t *testing.T) {
	resources, err := templates.WalkEmbedded()
	if err != nil {
		t.Fatalf("walk embedded: %v", err)
	}

	sites := map[string]string{
		"commands/restart.md":               "",
		"commands/wrap.md":                  "",
		"skills/epic-orchestrator/SKILL.md": "",
	}
	for _, r := range resources {
		if _, want := sites[r.RelPath]; want {
			sites[r.RelPath] = string(r.Bytes)
		}
	}

	for rel, body := range sites {
		if body == "" {
			t.Fatalf("embedded resource %q missing or empty", rel)
		}
		m := selectorCall.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%s: no vp_check selector call found", rel)
			continue
		}
		names := strings.Split(m[1], ",")
		if len(names) == 0 {
			t.Errorf("%s: vp_check called with an empty selector list — that "+
				"selects the WHOLE registry, which is the default this call "+
				"exists to avoid", rel)
			continue
		}
		for _, raw := range names {
			name := strings.Trim(strings.TrimSpace(raw), `"`)
			if name == "" {
				continue
			}
			if _, ok := check.Producers[name]; !ok {
				t.Errorf("%s: selector names %q, which is not a check.Producers "+
					"key — vp_check errors on an unknown name, so this template "+
					"hard-fails for every agent that runs it", rel, name)
			}
		}
	}
}

// TestProducerOrderIsTotal pins that every registry key is reachable by the
// default-all path. check's own TestProducerOrderCoversProducers asserts the
// same invariant from inside the package; this one exists because the templates
// depend on it from outside: a producer missing from ProducerOrder is invisible
// to a caller that omits the argument, which is what every non-vibe-palace
// project's restart does.
func TestProducerOrderIsTotal(t *testing.T) {
	if len(check.ProducerOrder) != len(check.Producers) {
		t.Errorf("ProducerOrder has %d entries, Producers has %d — the default-all "+
			"path silently skips the difference",
			len(check.ProducerOrder), len(check.Producers))
	}
	seen := make(map[string]bool, len(check.ProducerOrder))
	for _, n := range check.ProducerOrder {
		if seen[n] {
			t.Errorf("ProducerOrder lists %q twice — it would run twice", n)
		}
		seen[n] = true
		if _, ok := check.Producers[n]; !ok {
			t.Errorf("ProducerOrder names %q, absent from Producers — the "+
				"default-all path would panic on a nil func", n)
		}
	}
}
