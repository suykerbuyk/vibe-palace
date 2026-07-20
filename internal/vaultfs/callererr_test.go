// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
)

// TestEditCallerErrorsAreClassified — the high-frequency edit guards
// (old_string not found / non-empty / occurs N times) are the caller's fault and
// must satisfy errors.As(&apperr.CallerError{}), so the MCP seam counts them as
// friction instead of turning health amber. old_string-not-found on resume.md was
// the single loudest amber-maker in the evidence.
func TestEditCallerErrorsAreClassified(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "f.md"), []byte("hello world hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name       string
		old, new   string
		replaceAll bool
	}{
		{"not found", "absent-text", "x", false},
		{"empty old_string", "", "x", false},
		{"multi occurrence", "hello", "x", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Edit(vault, "f.md", c.old, c.new, c.replaceAll, "")
			if err == nil {
				t.Fatal("expected a caller error, got nil")
			}
			if !errors.As(err, &apperr.CallerError{}) {
				t.Errorf("edit error is not classified as CallerError: %v", err)
			}
		})
	}
}

// TestReadCapErrorsAreClassified — file-exceeds-cap and cap-too-large are the
// caller's own limits, not a fault, and must classify as CallerError.
func TestReadCapErrorsAreClassified(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "big.md"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	// file exceeds the caller's own small cap
	if _, err := Read(vault, "big.md", 3); err == nil {
		t.Fatal("expected exceeds-cap error, got nil")
	} else if !errors.As(err, &apperr.CallerError{}) {
		t.Errorf("exceeds-cap error is not a CallerError: %v", err)
	}

	// cap above the hard 10 MiB limit
	if _, err := Read(vault, "big.md", 11<<20); err == nil {
		t.Fatal("expected cap-too-large error, got nil")
	} else if !errors.As(err, &apperr.CallerError{}) {
		t.Errorf("cap-too-large error is not a CallerError: %v", err)
	}
}
