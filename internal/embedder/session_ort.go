// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

//go:build ORT

package embedder

import (
	"os"
	"strings"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/options"
)

// ORTAvailable is true in a binary built with -tags ORT (CGO_ENABLED=1): the
// native ONNX-Runtime backend is compiled in and selectable at runtime by
// backend=ort. See session_go.go for the default build's false.
const ORTAvailable = true

// newHugotSession creates the hugot session for the requested backend. This
// -tags ORT binary can serve BOTH backends at runtime — the pure-Go session is
// always compiled (hugot_go.go is untagged), which is what lets the ORT test
// binary A/B the two backends in one process for the parity check.
//
// For the ORT backend, VP_ONNX_LIBRARY_PATH (if set) names the DIRECTORY holding
// libonnxruntime.so; hugot's WithOnnxLibraryPath joins the filename itself and
// requires a directory, not a file. Unset, hugot falls back to its platform
// default (/usr/lib/libonnxruntime.so on Linux) via SetSharedLibraryPath.
func newHugotSession(backend string) (*hugot.Session, error) {
	if backend != BackendORT {
		return hugot.NewGoSession()
	}
	if dir := strings.TrimSpace(os.Getenv(EnvONNXLibraryPath)); dir != "" {
		return hugot.NewORTSession(options.WithOnnxLibraryPath(dir))
	}
	return hugot.NewORTSession()
}
