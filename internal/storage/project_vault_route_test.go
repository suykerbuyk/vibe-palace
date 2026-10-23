// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func builtRouteSnap(entries map[string]snapEntry) ProjectVaultSnapshot {
	return ProjectVaultSnapshot{entries: entries, built: true}
}

func TestRouteForRequest(t *testing.T) {
	S := t.TempDir() // startup vault
	R := t.TempDir() // a bound-elsewhere vault
	snap := builtRouteSnap(map[string]snapEntry{
		"qa":   {root: R},
		"qa2":  {root: R},
		"home": {root: S},
		"bad":  {broken: true, reason: "will not resolve"},
	})

	// FLAG2 lock: a NO-SLUG request returns (startup, nil) BEFORE the snapshot is
	// consulted — proven by passing an UNBUILT snapshot and still getting (S, nil).
	var unbuilt ProjectVaultSnapshot
	if root, err := RouteForRequest(json.RawMessage(`{}`), S, unbuilt); err != nil || root != S {
		t.Fatalf("no-slug must return (startup,nil) even under an unbuilt snapshot; got (%q,%v)", root, err)
	}

	// Single-root / allowed cases.
	for _, tc := range []struct {
		name, params, want string
	}{
		{"BoundHere", `{"project":"home"}`, S},
		{"Unbound", `{"project":"brand-new"}`, S},
		{"BoundElsewhere", `{"project":"qa"}`, R},
		{"SameVaultMove", `{"project":"qa","to_project":"qa2"}`, R},
	} {
		root, err := RouteForRequest(json.RawMessage(tc.params), S, snap)
		if err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
			continue
		}
		if root != tc.want {
			t.Errorf("%s: root = %q, want %q", tc.name, root, tc.want)
		}
	}

	// Broken binding → RouteBrokenOrUnbuilt.
	if _, err := RouteForRequest(json.RawMessage(`{"project":"bad"}`), S, snap); !isRouteKind(err, RouteBrokenOrUnbuilt) {
		t.Errorf("broken binding must be RouteBrokenOrUnbuilt, got %v", err)
	}

	// Unbuilt snapshot with a slug present → RouteBrokenOrUnbuilt (fail-closed).
	if _, err := RouteForRequest(json.RawMessage(`{"project":"qa"}`), S, unbuilt); !isRouteKind(err, RouteBrokenOrUnbuilt) {
		t.Errorf("unbuilt snapshot with a slug must be RouteBrokenOrUnbuilt, got %v", err)
	}

	// Cross-vault move (home→S, qa→R) → RouteTwoRoots naming BOTH roots.
	_, err := RouteForRequest(json.RawMessage(`{"project":"home","to_project":"qa"}`), S, snap)
	var cve *CrossVaultRouteError
	if !errors.As(err, &cve) || cve.Kind != RouteTwoRoots {
		t.Fatalf("cross-vault move must be RouteTwoRoots, got %v", err)
	}
	if !(cve.RootA == S && cve.RootB == R) {
		t.Fatalf("RouteTwoRoots must name both roots (%q / %q), got RootA=%q RootB=%q", S, R, cve.RootA, cve.RootB)
	}
	if msg := cve.Error(); !(strings.Contains(msg, S) && strings.Contains(msg, R)) {
		t.Fatalf("RouteTwoRoots message must name both roots: %q", msg)
	}
}

func isRouteKind(err error, k CrossVaultRouteKind) bool {
	var cve *CrossVaultRouteError
	return errors.As(err, &cve) && cve.Kind == k
}
