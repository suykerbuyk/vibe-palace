// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"fmt"
	"sort"
	"sync"
)

// Adapter produces the pre-compression source bytes for a single
// archive operation. Implementations are registered by Name and
// looked up from CreateOptions.Adapter.
//
// An Adapter is a per-IDE translator: it knows where transcripts live
// on disk for its IDE, how to read them, and how to turn them into a
// byte stream suitable for hashing + compression. The archive.Create
// pipeline is adapter-agnostic; the Adapter is the only IDE-specific
// seam.
type Adapter interface {
	// Name is the stable string recorded in Manifest.Adapter
	// (e.g., "claude-code", "zed"). Used as the registry key.
	Name() string

	// Version is the adapter implementation version, independent of
	// vp's release. Bump when the on-disk contract with the source
	// IDE changes.
	Version() string

	// ResolveSource returns an absolute path to the bytes that Create
	// will hash and compress. For adapters that read directly from an
	// on-disk file (Claude Code), the path is the real source file.
	// For adapters that synthesize (Zed), the path points at a
	// transient temp file whose lifetime is managed by cleanup.
	//
	// The returned cleanup is always non-nil and safe to call once;
	// callers must invoke it after the source bytes are no longer
	// needed. For persistent-source adapters it is a no-op.
	ResolveSource(opts CreateOptions) (path string, cleanup func(), err error)

	// Inspect reads the resolved source and returns best-effort
	// manifest metadata: user+assistant turn count and the most
	// recent non-empty assistant model string. Errors are non-fatal
	// at the archive level — the source hash still pins the exact
	// bytes — so Create logs but does not abort on Inspect failure.
	Inspect(sourcePath string) (turnCount int, model string, err error)
}

var (
	adaptersMu sync.RWMutex
	adapters   = map[string]Adapter{}
)

// RegisterAdapter adds or replaces an adapter in the global registry.
// Intended to be called from package init functions.
func RegisterAdapter(a Adapter) {
	adaptersMu.Lock()
	defer adaptersMu.Unlock()
	adapters[a.Name()] = a
}

// LookupAdapter returns the adapter registered under name.
func LookupAdapter(name string) (Adapter, error) {
	adaptersMu.RLock()
	defer adaptersMu.RUnlock()
	a, ok := adapters[name]
	if !ok {
		return nil, fmt.Errorf("unknown adapter %q", name)
	}
	return a, nil
}

// RegisteredAdapterNames returns the sorted list of adapter names
// currently in the registry. Useful for CLI help text and diagnostics.
func RegisteredAdapterNames() []string {
	adaptersMu.RLock()
	defer adaptersMu.RUnlock()
	names := make([]string, 0, len(adapters))
	for n := range adapters {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func noopCleanup() {}
