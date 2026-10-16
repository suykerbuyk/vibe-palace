// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package ingest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// TestIngestImportBoundary pins the U4 B3 import boundary: internal/ingest must
// import neither internal/tools (the home of pidAlive) nor syscall. The B3
// holder-liveness check reaches ingest only through the injected
// Deps.HolderAlive seam, wired in cmd/vp, so the process-spawn and OS-signal
// machinery stays out of this library. A future edit that reaches for
// tools.pidAlive or syscall.Kill directly fails here.
func TestIngestImportBoundary(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	if len(pkgs) == 0 {
		t.Fatal("no non-test package files parsed; the guard would be vacuous")
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatalf("%s: bad import %q", name, imp.Path.Value)
				}
				if path == "syscall" {
					t.Errorf("%s imports %q — the U4 B3 liveness check must stay behind Deps.HolderAlive (cmd/vp)", name, path)
				}
				if strings.HasSuffix(path, "/internal/tools") {
					t.Errorf("%s imports %q — internal/ingest must not import internal/tools", name, path)
				}
			}
		}
	}
}
