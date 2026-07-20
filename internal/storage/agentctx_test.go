// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"strings"
	"testing"
)

func TestWriteResume(t *testing.T) {
	vault := bornCurrentVault(t, t.TempDir())

	// No resume.md yet: the empty guard is the assert-absent (first-write) case.
	if err := vault.WriteResume("myproj", "# Resume\nSome content", ""); err != nil {
		t.Fatalf("WriteResume: %v", err)
	}

	path, _ := vault.ResumeFile("myproj")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "# Resume\nSome content" {
		t.Errorf("got %q", string(data))
	}
}

func TestWriteResumeOverwrite(t *testing.T) {
	vault := bornCurrentVault(t, t.TempDir())

	if err := vault.WriteResume("myproj", "v1", ""); err != nil {
		t.Fatalf("WriteResume v1: %v", err)
	}
	// The overwrite must present v1's digest: atomicfile writes verbatim, so the
	// bytes handed to WriteResume are exactly the bytes now on disk.
	if err := vault.WriteResume("myproj", "v2", sha256Hex("v1")); err != nil {
		t.Fatalf("WriteResume v2: %v", err)
	}

	path, _ := vault.ResumeFile("myproj")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "v2" {
		t.Errorf("overwrite failed: got %q, want %q", string(data), "v2")
	}
}

func TestWriteResumeInvalidSlug(t *testing.T) {
	vault := bornCurrentVault(t, t.TempDir())
	if err := vault.WriteResume("INVALID", "content", ""); err == nil {
		t.Fatal("expected error for invalid slug")
	}
}

func TestAppendIterationOwned(t *testing.T) {
	vault := bornCurrentVault(t, t.TempDir())

	// Fresh vault: the server mints iteration 1 and composes the header itself.
	n, derived, err := vault.AppendIterationOwned("myproj", "first work", "body", nil)
	if err != nil {
		t.Fatalf("AppendIterationOwned: %v", err)
	}
	if n != 1 || derived != 1 {
		t.Fatalf("n=%d derived=%d, want 1,1", n, derived)
	}

	path, _ := vault.IterationsFile("myproj")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "## Iteration 1 — first work") {
		t.Errorf("missing composed header: %q", string(data))
	}
}

func TestAppendIterationMultiple(t *testing.T) {
	vault := bornCurrentVault(t, t.TempDir())

	if n, _, err := vault.AppendIterationOwned("myproj", "First", "body", nil); err != nil {
		t.Fatalf("AppendIterationOwned 1: %v", err)
	} else if n != 1 {
		t.Fatalf("first n=%d, want 1", n)
	}
	if n, _, err := vault.AppendIterationOwned("myproj", "Second", "body", nil); err != nil {
		t.Fatalf("AppendIterationOwned 2: %v", err)
	} else if n != 2 {
		t.Fatalf("second n=%d, want 2", n)
	}

	path, _ := vault.IterationsFile("myproj")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "First") || !strings.Contains(content, "Second") {
		t.Errorf("missing iteration entries: %q", content)
	}
	if strings.Count(content, "---") < 2 {
		t.Errorf("expected at least 2 separators, got %d", strings.Count(content, "---"))
	}
}

func TestAppendIterationInvalidSlug(t *testing.T) {
	vault := bornCurrentVault(t, t.TempDir())
	if _, _, err := vault.AppendIterationOwned("INVALID", "title", "content", nil); err == nil {
		t.Fatal("expected error for invalid slug")
	}
}

func TestListTriplesEmpty(t *testing.T) {
	vault := bornCurrentVault(t, t.TempDir())
	triples, err := vault.ListTriples("myproj")
	if err != nil {
		t.Fatalf("ListTriples: %v", err)
	}
	if triples != nil {
		t.Errorf("expected nil for empty KG, got %v", triples)
	}
}

func TestListTriplesPopulated(t *testing.T) {
	vault := bornCurrentVault(t, t.TempDir())

	t1 := Triple{Subject: "Go", Predicate: "uses", Object: "modules", Confidence: 1.0}
	t2 := Triple{Subject: "Rust", Predicate: "uses", Object: "crates", Confidence: 0.9}

	if err := vault.AddTriple("myproj", t1); err != nil {
		t.Fatalf("AddTriple t1: %v", err)
	}
	if err := vault.AddTriple("myproj", t2); err != nil {
		t.Fatalf("AddTriple t2: %v", err)
	}

	triples, err := vault.ListTriples("myproj")
	if err != nil {
		t.Fatalf("ListTriples: %v", err)
	}
	if len(triples) != 2 {
		t.Fatalf("got %d triples, want 2", len(triples))
	}

	subjects := make(map[string]bool)
	for _, tr := range triples {
		subjects[tr.Subject] = true
	}
	if !subjects["Go"] || !subjects["Rust"] {
		t.Errorf("unexpected subjects: %v", subjects)
	}
}
