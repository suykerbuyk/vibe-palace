// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"fmt"
	"os"
)

// InlineAdapterName is the manifest.adapter value for caller-supplied
// content. The name records the MECHANISM — bytes handed to Create
// in-memory — never a host: an inline transcript is the agent's own
// rendition of the session, not host ground truth, and the host that
// produced it is recorded separately by capture provenance. Naming a
// host here would launder a self-report into an authenticity claim.
const InlineAdapterName = "inline"

// InlineAdapterVersion is the adapter implementation version,
// independent of vp's release. Bump when the contract with callers
// changes (e.g., accepted content shape, encoding expectations).
const InlineAdapterVersion = "1.0.0"

// inlineAdapter implements Adapter for transcripts that exist only in
// memory: hook-less hosts have no on-disk transcript for us to read,
// so the caller supplies the bytes via CreateOptions.SourceContent.
//
// ResolveSource writes those bytes verbatim to a temp file — the same
// synthesize-to-temp-file shape as the Zed adapter — so the rest of
// the Create pipeline (hash, compress, inspect) stays byte-oriented
// and adapter-agnostic. The adapter is correct-or-absent: it never
// guesses a source, so empty SourceContent is a hard error rather
// than a fallback to some on-disk location.
type inlineAdapter struct{}

func (inlineAdapter) Name() string    { return InlineAdapterName }
func (inlineAdapter) Version() string { return InlineAdapterVersion }

func (inlineAdapter) ResolveSource(opts CreateOptions) (string, func(), error) {
	if len(opts.SourceContent) == 0 {
		return "", noopCleanup, fmt.Errorf("inline: no transcript content supplied (set SourceContent)")
	}

	f, err := os.CreateTemp("", "vp-archive-inline-*.jsonl")
	if err != nil {
		return "", noopCleanup, fmt.Errorf("inline: temp file: %w", err)
	}
	tmpPath := f.Name()
	cleanup := func() { os.Remove(tmpPath) }

	if _, err := f.Write(opts.SourceContent); err != nil {
		f.Close()
		cleanup()
		return "", noopCleanup, fmt.Errorf("inline: write temp: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", noopCleanup, fmt.Errorf("inline: close temp: %w", err)
	}
	return tmpPath, cleanup, nil
}

func (inlineAdapter) Inspect(sourcePath string) (int, string, error) {
	// Callers are expected to supply Claude-shape JSONL, so reuse the
	// Claude inspector. Best-effort by contract: Create discards
	// Inspect errors, and the source hash still pins the exact bytes
	// even when the content turns out to be some other shape.
	return InspectClaudeJSONL(sourcePath)
}

func init() {
	RegisterAdapter(inlineAdapter{})
}
