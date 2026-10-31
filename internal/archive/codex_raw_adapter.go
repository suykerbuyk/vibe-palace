// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"fmt"
	"os"
)

const CodexRawAdapterName = "codex-raw"
const CodexRawAdapterVersion = "1.0.0"

// codexRawAdapter archives the exact file Codex supplied without interpreting
// its private rollout format as another host's transcript schema.
type codexRawAdapter struct{}

func (codexRawAdapter) Name() string    { return CodexRawAdapterName }
func (codexRawAdapter) Version() string { return CodexRawAdapterVersion }

func (codexRawAdapter) ResolveSource(opts CreateOptions) (string, func(), error) {
	if opts.SourcePath == "" {
		return "", noopCleanup, fmt.Errorf("codex-raw: transcript source path is required")
	}
	info, err := os.Stat(opts.SourcePath)
	if err != nil {
		return "", noopCleanup, fmt.Errorf("codex-raw: stat transcript: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return "", noopCleanup, fmt.Errorf("codex-raw: transcript must be a non-empty regular file")
	}
	return opts.SourcePath, noopCleanup, nil
}

func (codexRawAdapter) Inspect(string) (int, string, error) {
	return 0, "", fmt.Errorf("codex-raw: transcript format is not interpreted; turn count and model are unknown")
}

func init() { RegisterAdapter(codexRawAdapter{}) }
