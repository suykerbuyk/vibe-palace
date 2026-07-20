// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package apperr

import (
	"errors"
	"fmt"
	"testing"
)

// TestCallerIsDiscoverableViaErrorsAs — the seam the whole design rests on: a
// wrapped caller error must be found by errors.As(err, &CallerError{}) even after
// further %w wrapping, so the MCP handler can classify it at the boundary.
func TestCallerIsDiscoverableViaErrorsAs(t *testing.T) {
	base := errors.New("old_string not found")
	wrapped := Caller(base)
	if !errors.As(wrapped, &CallerError{}) {
		t.Fatal("Caller() result is not discoverable via errors.As — the classification seam is broken")
	}

	// Survives an upstream %w wrap (the tool layer wraps vaultfs errors).
	up := fmt.Errorf("append iteration: %w", wrapped)
	if !errors.As(up, &CallerError{}) {
		t.Fatal("caller classification did not survive an upstream %w wrap")
	}
	if !IsCaller(up) {
		t.Fatal("IsCaller disagrees with errors.As on a wrapped caller error")
	}
}

// TestCallerPreservesMessageAndUnwrap — the type carries the label, not the text:
// Error() is the underlying message unchanged, and Is() reaches the sentinel.
func TestCallerPreservesMessageAndUnwrap(t *testing.T) {
	sentinel := errors.New("boom")
	err := Caller(sentinel)
	if err.Error() != "boom" {
		t.Errorf("Error() = %q, want %q — wrapping must not mangle the message", err.Error(), "boom")
	}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is cannot reach the wrapped sentinel through CallerError")
	}
}

// TestCallerNilIsNil — safe to apply unconditionally at a return site.
func TestCallerNilIsNil(t *testing.T) {
	if Caller(nil) != nil {
		t.Error("Caller(nil) must be nil so it is safe to wrap a maybe-nil error")
	}
	if IsCaller(nil) {
		t.Error("IsCaller(nil) must be false")
	}
}

// TestPlainErrorIsNotCaller — an unclassified error must NOT read as caller, so
// it defaults to internal/amber at the seam. This is what keeps a partial
// classification honest.
func TestPlainErrorIsNotCaller(t *testing.T) {
	if IsCaller(errors.New("some internal fault")) {
		t.Error("a plain error must not be classified as caller — unclassified defaults to internal")
	}
}
