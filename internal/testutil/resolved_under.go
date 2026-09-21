// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testutil

import (
	"path/filepath"
	"strings"
	"testing"
)

// RequireResolvedUnder fails the test unless resolve returns a path inside
// root. Pass storage.VaultConfigFilePath as resolve and the directory the test
// pointed XDG_CONFIG_HOME at as root: the assertion then proves the test cannot
// read or write the developer's real host config, on every machine, whether or
// not a file exists there. (resolve is an argument because this package cannot
// import storage: storage's own tests import this package.)
//
// A resolve error FAILS the test and never skips it. The error is reachable
// only through a broken isolation fixture — neither XDG_CONFIG_HOME nor HOME
// set — which is exactly what this helper exists to detect, and a skip reports
// as success.
func RequireResolvedUnder(t testing.TB, root string, resolve func() (string, error)) {
	t.Helper()
	got, err := resolve()
	if err != nil {
		t.Fatalf("host config path is unresolvable (%v): the test's XDG isolation is broken", err)
	}
	rel, err := filepath.Rel(root, got)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		t.Fatalf("host config resolves to %s, outside the test's own root %s: the test is not isolated from the real host config", got, root)
	}
}
