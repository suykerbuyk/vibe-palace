// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build !ORT

package embedder

import (
	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"
)

// ORTAvailable reports whether this binary was built with the native
// ONNX-Runtime backend. On the default, zero-CGO build it is false: `-tags ORT`
// is required (see session_ort.go). It is a compile-time constant so a caller
// that gates on it is dead-code-eliminated into either a plain Go binary or an
// ORT-capable one, with no CGO symbols leaking into the default build.
const ORTAvailable = false

// newHugotSession creates the hugot session for the requested backend. On the
// default build only the pure-Go backend exists; a request for BackendORT fails
// loud rather than silently falling back, so a misconfigured default host is
// told exactly what to do instead of quietly embedding with the wrong backend
// (ADR-009 ethos). NewONNXBackend gates on this before any lock or download, so
// in practice this branch is the belt to that suspenders.
func newHugotSession(backend string) (*hugot.Session, error) {
	if backend == BackendORT {
		return nil, errBackendNeedsORTBuild(backend)
	}
	return hugot.NewGoSession()
}

// installTruncation installs maxSeqLen token truncation for the default build:
// there is only the Go tokenizer path. modelPath is unused here (the ORT path
// re-reads tokenizer.json from it); it is part of the shared seam signature.
func installTruncation(pipeline *pipelines.FeatureExtractionPipeline, maxSeqLen int, _ string) (func() error, *truncatingTokenizer, error) {
	return installGoTruncation(pipeline, maxSeqLen)
}
