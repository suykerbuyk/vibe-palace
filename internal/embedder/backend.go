// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"fmt"
	"os"
	"strings"
)

// Backend names the embedding backend a binary can use. They are runtime
// VALUES, not build tags: their casing is lowercase and independent of the
// UPPERCASE `ORT` build tag that decides which session seam is compiled in
// (session_go.go vs session_ort.go). See ADR-015.
const (
	// BackendGo is the pure-Go hugot backend — the default, the only backend
	// the released zero-CGO binary ships, and the one every host uses for
	// ordinary work.
	BackendGo = "go"
	// BackendORT is the native ONNX-Runtime (cgo) backend, reachable only in a
	// binary built with `-tags ORT` (CGO_ENABLED=1). It is a host-native
	// power/measurement path for bulk re-embedding; it is never part of a
	// release artifact or a gating CI job.
	BackendORT = "ort"
)

const (
	// EnvBackend overrides [embedder].backend at runtime (go|ort).
	EnvBackend = "VP_EMBEDDER_BACKEND"
	// EnvONNXLibraryPath points the ORT backend at the DIRECTORY holding
	// libonnxruntime.so (hugot joins the filename itself). Honored only by the
	// ORT session seam; ignored on a default build.
	EnvONNXLibraryPath = "VP_ONNX_LIBRARY_PATH"
)

// normalizeBackend lowercases and trims a configured backend name, mapping the
// empty string to BackendGo so an absent config key resolves to the default.
func normalizeBackend(b string) string {
	b = strings.ToLower(strings.TrimSpace(b))
	if b == "" {
		return BackendGo
	}
	return b
}

// RequestedBackend is the backend the operator asked for: the configured value
// with VP_EMBEDDER_BACKEND taking precedence. It does NOT mask an unsupported
// request — a caller that constructs an embedder passes this to NewONNXBackend,
// which fails loud if "ort" is asked of a binary built without -tags ORT. Use
// ResolveBackend, not this, for the embed-cache fingerprint.
func RequestedBackend(configured string) string {
	b := normalizeBackend(configured)
	if env := strings.TrimSpace(os.Getenv(EnvBackend)); env != "" {
		b = normalizeBackend(env)
	}
	return b
}

// ResolveBackend is the backend that will ACTUALLY produce vectors on this
// binary, which is what the embed-cache fingerprint must record.
//
// 🔴 ON A DEFAULT (non-ORT) BINARY IT ALWAYS RETURNS "go", EVEN WHEN THE CONFIG
// OR ENV ASKS FOR "ort". A binary built without -tags ORT cannot produce ORT
// vectors (NewONNXBackend fails loud before it embeds), so every vector such a
// host reads or writes is a pure-Go vector. Masking "ort" to "go" here keeps
// the fingerprint string byte-identical to today's on every default host (B2):
// a routine upgrade of the zero-CGO binary never changes the regime string and
// so never forces a multi-day re-embed. Only an -tags ORT binary
// (ORTAvailable == true) can resolve to "ort" and give its vectors the distinct
// `backend=ort` fingerprint that keeps them out of the pure-Go index.
func ResolveBackend(configured string) string {
	b := RequestedBackend(configured)
	if b == BackendORT && !ORTAvailable {
		return BackendGo
	}
	return b
}

// errBackendNeedsORTBuild is the single fail-loud message for asking a binary
// built without -tags ORT to use the ORT backend. Shared so the text reads
// identically from NewONNXBackend's early gate and from the default session
// seam.
func errBackendNeedsORTBuild(backend string) error {
	return fmt.Errorf("embedder: backend %q requires a binary built with -tags ORT (CGO_ENABLED=1); this is the default zero-CGO build — rebuild with `make build-ort`, or set %s=go", backend, EnvBackend)
}
