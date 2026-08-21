// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package mcp

import (
	"bytes"
	"encoding/json"
	"sort"
)

// jsonNull is the wire form of a JSON null. It is checked explicitly below
// because encoding/json accepts it into ANY struct and leaves every field at
// its zero value — so a predicate written as "dry_run is false ⇒ writes" would
// be handed a payload that decodes cleanly and says nothing at all.
var jsonNull = []byte("null")

// readOnlyInvocation reports whether THIS invocation of a Mutating tool is
// affirmatively known to write nothing, and may therefore pass the surface gate
// (Tool.ReadOnlyWhen carries the full rationale).
//
// 🔴 EVERY RETURN HERE EXCEPT THE LAST IS FALSE, AND THAT IS THE DESIGN. This
// function owns the three ways the QUESTION can be unanswerable — no predicate
// was declared, there are no params to read, or the params are JSON null — and
// each one gates. It never asks whether the tool looks harmless; it asks
// whether something affirmatively proved this call writes nothing, and treats
// "cannot tell" as "writes".
//
// The asymmetry is the same one that makes the read-only serve filter an
// allow-list: a false negative here is an ungated write — bad, bounded, and
// detectable afterwards through stamps and the audit ratchet — while a false
// POSITIVE is a stale binary writing a vault only a newer one can safely write,
// which is the exact corruption the surface gate exists to prevent. So the
// cheap wrong answer is "gate a read", and it is the one this function is built
// to give.
//
// A predicate that panics is not caught here: makeHandler recovers panics for
// the whole dispatch, and swallowing one at the gate would convert a broken
// predicate into a silently-admitted write.
func readOnlyInvocation(t Tool, params json.RawMessage) bool {
	if t.ReadOnlyWhen == nil {
		return false
	}
	trimmed := bytes.TrimSpace(params)
	if len(trimmed) == 0 || bytes.Equal(trimmed, jsonNull) {
		return false
	}
	return t.ReadOnlyWhen(params)
}

// ParamAwareToolNames returns, sorted, the names of registered tools carrying a
// ReadOnlyWhen predicate — the tools whose gate verdict depends on the
// invocation rather than on the registration.
//
// It exists so the set can be PINNED from the package that declares it
// (internal/tools cannot see a registered Tool's func field, and ToolInfo
// deliberately does not carry it: ToolInfo is a wire payload — vp_manual serves
// it — and a predicate is server-side machinery, not part of the callable
// surface). A tool that grows or loses a predicate is a change in what a stale
// binary will admit, and must fail a test rather than ship quietly.
func (r *Registry) ParamAwareToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.tools))
	for name, rt := range r.tools {
		if rt.tool.ReadOnlyWhen != nil {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
