// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedResume plants an initial resume.md for project and returns its path. It
// writes through the os directly rather than WriteResume so the surface stamp
// for the project dir is left unwritten — the stamp is memoized per process, so
// a seed that stamped would mask whether EditResume stamps.
func seedResume(t *testing.T, v *Vault, project, content string) string {
	t.Helper()
	path, err := v.ResumeFile(project)
	if err != nil {
		t.Fatalf("ResumeFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write resume: %v", err)
	}
	return path
}

// TestEditResume covers the read→modify→write contract: the mutator sees the
// on-disk content, its return value lands on disk, and a mutator error aborts
// the edit without touching the file.
func TestEditResume(t *testing.T) {
	errBoom := errors.New("boom")

	tests := []struct {
		name    string
		seed    string // "" means do not create resume.md
		project string
		mutate  func(string) (string, error)
		wantErr string // substring; "" means success expected
		want    string // expected on-disk content afterwards ("" when no file expected)
	}{
		{
			name:    "append",
			seed:    "# Resume\n",
			project: "proj",
			mutate:  func(s string) (string, error) { return s + "added\n", nil },
			want:    "# Resume\nadded\n",
		},
		{
			name:    "identity",
			seed:    "# Resume\n",
			project: "proj",
			mutate:  func(s string) (string, error) { return s, nil },
			want:    "# Resume\n",
		},
		{
			name:    "truncate to empty",
			seed:    "# Resume\n",
			project: "proj",
			mutate:  func(string) (string, error) { return "", nil },
			want:    "",
		},
		{
			name:    "mutate error aborts write",
			seed:    "# Resume\n",
			project: "proj",
			mutate:  func(string) (string, error) { return "REPLACED", errBoom },
			wantErr: "boom",
			want:    "# Resume\n",
		},
		{
			name:    "missing resume",
			project: "proj",
			mutate:  func(s string) (string, error) { return s + "x", nil },
			wantErr: `resume.md not found for project "proj"`,
		},
		{
			name:    "invalid project slug",
			project: "../escape",
			mutate:  func(s string) (string, error) { return s, nil },
			wantErr: "project:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := testVault(t)
			var path string
			if tt.seed != "" {
				path = seedResume(t, v, tt.project, tt.seed)
			}

			err := v.EditResume(tt.project, tt.mutate)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("EditResume() error = %v, want nil", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("EditResume() = nil, want error containing %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("EditResume() error = %q, want it to contain %q", err, tt.wantErr)
			}

			if path == "" {
				return
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read resume: %v", readErr)
			}
			if string(data) != tt.want {
				t.Errorf("resume content = %q, want %q", data, tt.want)
			}
		})
	}
}

// TestEditResumeMissingErrorMatchesLegacyContract pins the exact error string the
// vp_thread_*/vp_carried_* tools (and their tests) depend on when resume.md does
// not exist. It must stay verbatim.
func TestEditResumeMissingErrorMatchesLegacyContract(t *testing.T) {
	v := testVault(t)
	err := v.EditResume("proj", func(s string) (string, error) { return s, nil })
	if err == nil {
		t.Fatal("EditResume() on missing resume = nil, want error")
	}
	if got, want := err.Error(), `resume.md not found for project "proj"`; got != want {
		t.Errorf("error = %q, want exactly %q", got, want)
	}
}

// TestEditResumeMutateSeesCurrentContent proves the mutator is fed the
// authoritative bytes read under the lock, not anything the caller supplied.
func TestEditResumeMutateSeesCurrentContent(t *testing.T) {
	v := testVault(t)
	seedResume(t, v, "proj", "on-disk\n")

	var seen string
	if err := v.EditResume("proj", func(s string) (string, error) {
		seen = s
		return s, nil
	}); err != nil {
		t.Fatalf("EditResume: %v", err)
	}
	if seen != "on-disk\n" {
		t.Errorf("mutate saw %q, want %q", seen, "on-disk\n")
	}
}

// TestEditResumeEnsuresProjectDir asserts EditResume creates the project
// directory before reading, mirroring WriteResume. Without it, a project whose
// directory has never been materialized would fail on a stat of a missing parent
// rather than on the resume.md-not-found contract.
func TestEditResumeEnsuresProjectDir(t *testing.T) {
	v := testVault(t)
	dir, err := v.ProjectDir("proj")
	if err != nil {
		t.Fatalf("ProjectDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("precondition: project dir already exists (%v)", err)
	}

	if err := v.EditResume("proj", func(s string) (string, error) { return s, nil }); err == nil {
		t.Fatal("EditResume() on missing resume = nil, want not-found error")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("project dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", dir)
	}
}

// TestEditResumeStampsSurface asserts the real vault root reaches
// atomicfile.Write. Passing "" would silently skip surface.StampForPath, and the
// regression would only show up later as a missed surface bump.
func TestEditResumeStampsSurface(t *testing.T) {
	v := testVault(t)
	seedResume(t, v, "proj", "# Resume\n")
	stamp := filepath.Join(v.Root, "Projects", "proj", ".surface")

	if err := v.EditResume("proj", func(s string) (string, error) { return s + "x\n", nil }); err != nil {
		t.Fatalf("EditResume: %v", err)
	}
	data, err := os.ReadFile(stamp)
	if err != nil {
		t.Fatalf("surface stamp not written: %v", err)
	}
	if !strings.Contains(string(data), "surface =") {
		t.Errorf("stamp content = %q, want a surface record", data)
	}
}

// TestEditResumeMutateErrorReleasesLock is the deadlock guard. vaultlock.Acquire
// is a blocking LOCK_EX with no timeout, so a lock leaked on the mutate-error
// path would hang every subsequent writer of resume.md forever, not return an
// error. The second EditResume must therefore complete promptly.
func TestEditResumeMutateErrorReleasesLock(t *testing.T) {
	v := testVault(t)
	path := seedResume(t, v, "proj", "# Resume\n")

	boom := errors.New("boom")
	if err := v.EditResume("proj", func(string) (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("EditResume() error = %v, want %v", err, boom)
	}

	done := make(chan error, 1)
	go func() {
		done <- v.EditResume("proj", func(s string) (string, error) { return s + "second\n", nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second EditResume: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second EditResume blocked: the mutate-error path leaked the vaultlock")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read resume: %v", err)
	}
	if string(data) != "# Resume\nsecond\n" {
		t.Errorf("resume content = %q, want %q", data, "# Resume\nsecond\n")
	}
}

// TestEditResumeConcurrentEditsAllSurvive is the lost-update acceptance test: N
// goroutines each append a distinct marker through EditResume, and every marker
// must appear exactly once. A read-modify-write that does not hold the per-path
// lock across the whole cycle silently drops most of them. Run under -race.
func TestEditResumeConcurrentEditsAllSurvive(t *testing.T) {
	v := testVault(t)
	path := seedResume(t, v, "proj", "# Resume\n")
	const n = 32

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			err := v.EditResume("proj", func(s string) (string, error) {
				return s + fmt.Sprintf("marker-%03d-end\n", i), nil
			})
			if err != nil {
				t.Errorf("EditResume %d: %v", i, err)
			}
		})
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read resume: %v", err)
	}
	content := string(data)
	for i := range n {
		marker := fmt.Sprintf("marker-%03d-end", i)
		if got := strings.Count(content, marker); got != 1 {
			t.Errorf("marker %q appears %d times, want 1 (lost update)", marker, got)
		}
	}
}

// TestEditResumeInterlocksWithWriteResume proves EditResume serializes against
// the blind whole-file writer on the same path: interleaved WriteResume calls may
// reset the file, but every EditResume that returns nil must have appended to a
// file that was never torn mid-write. The invariant checked here is that the
// final file is always one of the well-formed states (never a partial line).
func TestEditResumeInterlocksWithWriteResume(t *testing.T) {
	v := testVault(t)
	path := seedResume(t, v, "proj", "# Resume\n")
	const n = 16

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if err := v.EditResume("proj", func(s string) (string, error) {
				return s + fmt.Sprintf("edit-%02d\n", i), nil
			}); err != nil {
				t.Errorf("EditResume %d: %v", i, err)
			}
		})
		wg.Go(func() {
			if err := v.WriteResume("proj", "# Resume\n"); err != nil {
				t.Errorf("WriteResume %d: %v", i, err)
			}
		})
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read resume: %v", err)
	}
	for line := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		if line != "# Resume" && !strings.HasPrefix(line, "edit-") {
			t.Fatalf("torn or interleaved line %q in final file", line)
		}
	}
}
