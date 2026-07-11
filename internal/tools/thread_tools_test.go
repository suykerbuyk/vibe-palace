// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// writeResumeFixture writes resume.md for project under the vault and returns
// its absolute path.
func writeResumeFixture(t *testing.T, vault *storage.Vault, project, content string) string {
	t.Helper()
	abs, err := vault.ResumeFile(project)
	if err != nil {
		t.Fatalf("ResumeFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write resume: %v", err)
	}
	return abs
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

const threadFixture = `# Resume

## Open Threads

### alpha

alpha body

### beta

beta body

## Project History

history
`

func TestThreadInsert_Bottom(t *testing.T) {
	vault := newVaultRoot(t)
	abs := writeResumeFixture(t, vault, "proj", threadFixture)

	tool := ThreadInsertTool(vault)
	if tool.Name != "vp_thread_insert" {
		t.Fatalf("name = %q", tool.Name)
	}
	p, _ := json.Marshal(map[string]any{
		"project":  "proj",
		"position": map[string]any{"mode": "bottom"},
		"slug":     "gamma",
		"body":     "gamma body",
	})
	if _, err := tool.Handler(context.Background(), p); err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := readFile(t, abs)
	if !strings.Contains(got, "### gamma") {
		t.Error("gamma not inserted")
	}
	if !strings.Contains(got, "history") {
		t.Error("project history clobbered")
	}
}

func TestThreadInsert_DuplicateSlug(t *testing.T) {
	vault := newVaultRoot(t)
	writeResumeFixture(t, vault, "proj", threadFixture)
	tool := ThreadInsertTool(vault)
	p, _ := json.Marshal(map[string]any{
		"project":  "proj",
		"position": map[string]any{"mode": "top"},
		"slug":     "alpha",
		"body":     "dup",
	})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected already-exists error")
	}
}

func TestThreadInsert_MissingProject(t *testing.T) {
	vault := newVaultRoot(t)
	tool := ThreadInsertTool(vault)
	p, _ := json.Marshal(map[string]any{
		"position": map[string]any{"mode": "top"},
		"slug":     "x",
		"body":     "y",
	})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected project-required error")
	}
}

func TestThreadReplace_Basic(t *testing.T) {
	vault := newVaultRoot(t)
	abs := writeResumeFixture(t, vault, "proj", threadFixture)
	tool := ThreadReplaceTool(vault)
	p, _ := json.Marshal(map[string]any{
		"project": "proj",
		"slug":    "alpha",
		"body":    "replaced alpha body",
	})
	if _, err := tool.Handler(context.Background(), p); err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := readFile(t, abs)
	if !strings.Contains(got, "replaced alpha body") {
		t.Error("replacement missing")
	}
	if strings.Contains(got, "\nalpha body\n") {
		t.Error("old body still present")
	}
}

func TestThreadReplace_ReservedSlug(t *testing.T) {
	vault := newVaultRoot(t)
	writeResumeFixture(t, vault, "proj", threadFixture)
	tool := ThreadReplaceTool(vault)
	p, _ := json.Marshal(map[string]any{
		"project": "proj",
		"slug":    "Carried forward",
		"body":    "nope",
	})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected reserved-slug rejection")
	}
}

// TestThreadReplace_AmbiguousHardError verifies the multi-match slug case is a
// hard error that leaves the file untouched.
func TestThreadReplace_AmbiguousHardError(t *testing.T) {
	vault := newVaultRoot(t)
	dup := "# R\n\n## Open Threads\n\n### dup\n\nbody1\n\n### dup\n\nbody2\n"
	abs := writeResumeFixture(t, vault, "proj", dup)
	tool := ThreadReplaceTool(vault)
	p, _ := json.Marshal(map[string]any{"project": "proj", "slug": "dup", "body": "new"})
	_, err := tool.Handler(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguous hard error, got %v", err)
	}
	if got := readFile(t, abs); got != dup {
		t.Error("file should be unchanged on ambiguous error")
	}
}

func TestThreadRemove_Basic(t *testing.T) {
	vault := newVaultRoot(t)
	abs := writeResumeFixture(t, vault, "proj", threadFixture)
	tool := ThreadRemoveTool(vault)
	p, _ := json.Marshal(map[string]any{"project": "proj", "slug": "alpha"})
	if _, err := tool.Handler(context.Background(), p); err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := readFile(t, abs)
	if strings.Contains(got, "### alpha") {
		t.Error("alpha not removed")
	}
	if !strings.Contains(got, "### beta") {
		t.Error("beta should survive")
	}
}

func TestThreadRemove_ReservedSlug(t *testing.T) {
	vault := newVaultRoot(t)
	writeResumeFixture(t, vault, "proj", threadFixture)
	tool := ThreadRemoveTool(vault)
	p, _ := json.Marshal(map[string]any{"project": "proj", "slug": "Carried forward"})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected reserved-slug rejection")
	}
}

func TestThreadRemove_AmbiguousHardError(t *testing.T) {
	vault := newVaultRoot(t)
	dup := "# R\n\n## Open Threads\n\n### dup\n\nbody1\n\n### dup\n\nbody2\n"
	abs := writeResumeFixture(t, vault, "proj", dup)
	tool := ThreadRemoveTool(vault)
	p, _ := json.Marshal(map[string]any{"project": "proj", "slug": "dup"})
	_, err := tool.Handler(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("want ambiguous hard error, got %v", err)
	}
	if got := readFile(t, abs); got != dup {
		t.Error("file should be unchanged on ambiguous error")
	}
}

// TestThreadInsert_ConcurrentInsertsAllSurvive is the acceptance test for
// vp_thread_insert's read-modify-write. The handler reads resume.md, applies the
// insert in memory via mdutil, and writes the whole file back through
// atomicfile. atomicfile makes each write atomic but does nothing about lost
// updates: two handlers that read the same base concurrently and write back in
// sequence silently discard the earlier insert. mcp-go's stdio transport
// dispatches tool calls on a worker pool, so concurrent vp_thread_insert calls
// are reachable in production.
//
// N goroutines each insert a DISTINCT slug. Every insert whose handler returned
// nil error MUST appear exactly once in the final on-disk file. Note the race
// detector cannot see this bug — a file-level lost update is not a memory data
// race — so the final file content is the only detector.
func TestThreadInsert_ConcurrentInsertsAllSurvive(t *testing.T) {
	const n = 32
	vault := newVaultRoot(t)
	abs := writeResumeFixture(t, vault, "proj", threadFixture)

	// Zero-pad the slug indices to a fixed width so no slug is a substring of
	// another (thread-1 would otherwise match thread-10..19, breaking both the
	// insert and the occurrence counting below).
	slug := func(i int) string { return fmt.Sprintf("thread-%03d", i) }

	tool := ThreadInsertTool(vault)
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Go(func() {
			p, _ := json.Marshal(map[string]any{
				"project":  "proj",
				"position": map[string]any{"mode": "bottom"},
				"slug":     slug(i),
				"body":     "body " + slug(i),
			})
			_, errs[i] = tool.Handler(context.Background(), p)
		})
	}
	wg.Wait()

	final := readFile(t, abs)
	lost := 0
	for i, err := range errs {
		if err != nil {
			t.Logf("insert %s returned error (not counted as lost): %v", slug(i), err)
			continue
		}
		// The handler reported success, so the slug must be in the file, exactly
		// once: missing means a concurrent writer clobbered it (lost update),
		// duplicated means the section was written twice.
		switch got := strings.Count(final, "### "+slug(i)); got {
		case 1:
			// survived
		case 0:
			lost++
			t.Errorf("lost update: %s reported success but is missing from final content", slug(i))
		default:
			t.Errorf("duplicate: %s appears %d times in final content", slug(i), got)
		}
	}
	if lost > 0 {
		t.Errorf("%d of %d concurrent inserts were lost", lost, n)
	}
	if !strings.Contains(final, "### alpha") || !strings.Contains(final, "history") {
		t.Error("pre-existing content clobbered")
	}
}

func TestThreadInsert_NoResume(t *testing.T) {
	vault := newVaultRoot(t)
	tool := ThreadInsertTool(vault)
	p, _ := json.Marshal(map[string]any{
		"project":  "proj",
		"position": map[string]any{"mode": "top"},
		"slug":     "x",
		"body":     "y",
	})
	if _, err := tool.Handler(context.Background(), p); err == nil {
		t.Fatal("expected resume-not-found error")
	}
}
