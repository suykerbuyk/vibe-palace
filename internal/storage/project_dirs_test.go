// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
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
// the whole-file writer on the same path.
//
// The premise changed when WriteResume became compare-and-set: it can no longer
// blindly reset the file, so the old assertion ("interleaved resets may happen;
// only prove no line is torn") no longer describes the contract. The contract is
// now stronger, and this test asserts the strong form: a WriteResume racer does
// the read→compose→CAS-write an LLM does, and every call of EITHER kind that
// returns nil must be honored by the final file. A racer whose base went stale
// is REFUSED (ErrShaConflict) and, having promised nothing, may vanish — but a
// nil return is a promise, and no promise may be lost.
func TestEditResumeInterlocksWithWriteResume(t *testing.T) {
	v := testVault(t)
	path := seedResume(t, v, "proj", "# Resume\n")
	const n = 16

	var mu sync.Mutex
	var landed []string // markers whose call returned nil

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			marker := fmt.Sprintf("edit-%02d", i)
			if err := v.EditResume("proj", func(s string) (string, error) {
				return s + marker + "\n", nil
			}); err != nil {
				t.Errorf("EditResume %d: %v", i, err)
				return
			}
			mu.Lock()
			landed = append(landed, marker)
			mu.Unlock()
		})
		wg.Go(func() {
			marker := fmt.Sprintf("write-%02d", i)
			// The stale-read shape: read outside the lock, compose, then CAS.
			base, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("read base %d: %v", i, err)
				return
			}
			err = v.WriteResume("proj", string(base)+marker+"\n", sha256Hex(string(base)))
			if err != nil {
				if !errors.Is(err, vaultfs.ErrShaConflict) {
					t.Errorf("WriteResume %d: unexpected error: %v", i, err)
				}
				return // refused on a stale base: nothing was promised
			}
			mu.Lock()
			landed = append(landed, marker)
			mu.Unlock()
		})
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read resume: %v", err)
	}
	content := string(data)

	for line := range strings.SplitSeq(strings.TrimRight(content, "\n"), "\n") {
		if line == "" {
			continue
		}
		if line != "# Resume" && !strings.HasPrefix(line, "edit-") && !strings.HasPrefix(line, "write-") {
			t.Fatalf("torn or interleaved line %q in final file", line)
		}
	}
	for _, marker := range landed {
		if got := strings.Count(content, marker); got != 1 {
			t.Errorf("marker %q appears %d times, want 1 (lost update)", marker, got)
		}
	}
	if len(landed) < n {
		t.Errorf("only %d of at least %d calls landed; the EditResume half must never be refused", len(landed), n)
	}
}

// TestWriteResumeCAS is the compare-and-set contract of the whole-file resume
// writer: a matching guard writes, a mismatched guard is REFUSED with the file
// untouched, an empty guard means "assert absent", and the refusal carries the
// actual current digest so the caller can retry without a second read.
func TestWriteResumeCAS(t *testing.T) {
	t.Run("matching sha writes", func(t *testing.T) {
		v := testVault(t)
		path := seedResume(t, v, "proj", "# Resume\nv1\n")
		if err := v.WriteResume("proj", "# Resume\nv2\n", sha256Hex("# Resume\nv1\n")); err != nil {
			t.Fatalf("WriteResume: %v", err)
		}
		if got := readResumeFile(t, path); got != "# Resume\nv2\n" {
			t.Errorf("content = %q, want the replacement", got)
		}
	})

	t.Run("mismatched sha is refused and the file is untouched", func(t *testing.T) {
		v := testVault(t)
		const seeded = "# Resume\nv1\n"
		path := seedResume(t, v, "proj", seeded)

		err := v.WriteResume("proj", "# CLOBBER\n", sha256Hex("something else entirely"))
		if err == nil {
			t.Fatal("WriteResume with a mismatched sha succeeded; want ErrShaConflict")
		}
		if !errors.Is(err, vaultfs.ErrShaConflict) {
			t.Fatalf("error = %v, want errors.Is(err, vaultfs.ErrShaConflict)", err)
		}
		// A refused write that already corrupted the file is worse than useless.
		if got := readResumeFile(t, path); got != seeded {
			t.Errorf("refused write mutated the file: got %q, want %q", got, seeded)
		}
	})

	t.Run("empty sha with the file absent creates it", func(t *testing.T) {
		v := testVault(t)
		if err := v.WriteResume("proj", "# Resume\nfirst\n", ""); err != nil {
			t.Fatalf("first-write with empty guard: %v", err)
		}
		path, err := v.ResumeFile("proj")
		if err != nil {
			t.Fatalf("ResumeFile: %v", err)
		}
		if got := readResumeFile(t, path); got != "# Resume\nfirst\n" {
			t.Errorf("content = %q", got)
		}
	})

	t.Run("empty sha with the file present is a conflict", func(t *testing.T) {
		v := testVault(t)
		const seeded = "# Resume\nalready here\n"
		path := seedResume(t, v, "proj", seeded)

		err := v.WriteResume("proj", "# CLOBBER\n", "")
		if err == nil {
			t.Fatal("empty guard against an existing resume succeeded; the blind-overwrite path must not exist")
		}
		if !errors.Is(err, vaultfs.ErrShaConflict) {
			t.Fatalf("error = %v, want errors.Is(err, vaultfs.ErrShaConflict)", err)
		}
		if got := readResumeFile(t, path); got != seeded {
			t.Errorf("refused write mutated the file: got %q", got)
		}
	})

	t.Run("the refusal carries the actual current sha", func(t *testing.T) {
		v := testVault(t)
		const seeded = "# Resume\nv1\n"
		seedResume(t, v, "proj", seeded)
		want := sha256Hex(seeded)

		err := v.WriteResume("proj", "# CLOBBER\n", sha256Hex("stale"))
		if err == nil {
			t.Fatal("want a conflict")
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry the current sha %s", err, want)
		}
		var conflict *ResumeConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("error %v is not a *ResumeConflictError", err)
		}
		if conflict.Current != want {
			t.Errorf("conflict.Current = %s, want %s", conflict.Current, want)
		}
	})
}

// TestWriteResumeRefusesStaleRead pins the exact production bug: an agent reads
// the resume, spends minutes composing a full-body rewrite, and in the meantime a
// second writer lands an edit. The first writer's blind overwrite used to
// silently revert it. Under compare-and-set the stale write must be REFUSED and
// the second writer's content must survive.
func TestWriteResumeRefusesStaleRead(t *testing.T) {
	v := testVault(t)
	path := seedResume(t, v, "proj", "# Resume\n\n## Open Threads\n")

	// Writer A reads and captures the digest of what it read.
	base := readResumeFile(t, path)
	staleSha := sha256Hex(base)

	// Writer B lands an edit in the meantime.
	if err := v.EditResume("proj", func(s string) (string, error) {
		return s + "\n### from-writer-b\n", nil
	}); err != nil {
		t.Fatalf("writer B EditResume: %v", err)
	}

	// Writer A now submits its full-body rewrite composed from the stale read.
	err := v.WriteResume("proj", base+"\n### from-writer-a\n", staleSha)
	if err == nil {
		t.Fatal("the stale full-body write was accepted; writer B's edit would have been silently reverted")
	}
	if !errors.Is(err, vaultfs.ErrShaConflict) {
		t.Fatalf("error = %v, want errors.Is(err, vaultfs.ErrShaConflict)", err)
	}

	final := readResumeFile(t, path)
	if !strings.Contains(final, "### from-writer-b") {
		t.Errorf("writer B's edit was lost:\n%s", final)
	}
	if strings.Contains(final, "### from-writer-a") {
		t.Errorf("the refused write landed anyway:\n%s", final)
	}
}

// readResumeFile reads path or fails the test.
func readResumeFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// sha256Hex is the digest of content in the same form WriteResume compares
// against: the hex sha256 of the exact on-disk bytes. atomicfile writes verbatim,
// so hashing the bytes a test handed to WriteResume and hashing the resulting
// file are the same thing.
func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
