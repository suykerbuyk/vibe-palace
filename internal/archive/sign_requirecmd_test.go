// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import "testing"

// recorder is a testing.TB that records, rather than acts on, Fatalf and Skipf.
type recorder struct {
	testing.TB
	fatal, skip bool
}

func (r *recorder) Helper()               {}
func (r *recorder) Fatalf(string, ...any) { r.fatal = true }
func (r *recorder) Skipf(string, ...any)  { r.skip = true }

const absentTool = "vp-definitely-not-an-installed-tool-g1"

// requireCmd must FAIL under CI and SKIP elsewhere. A skip under CI is the defect
// it exists to prevent: the signed path would run nowhere and the job would stay
// green.
func TestRequireCmd_FailsUnderCISkipsLocally(t *testing.T) {
	t.Run("CI set: a missing tool fails", func(t *testing.T) {
		t.Setenv("CI", "true")
		r := &recorder{TB: t}
		requireCmd(r, absentTool)
		if !r.fatal || r.skip {
			t.Errorf("under CI a missing tool must fail and not skip (fatal=%v skip=%v)", r.fatal, r.skip)
		}
	})
	t.Run("CI unset: a missing tool skips", func(t *testing.T) {
		t.Setenv("CI", "")
		r := &recorder{TB: t}
		requireCmd(r, absentTool)
		if !r.skip || r.fatal {
			t.Errorf("locally a missing tool must skip and not fail (fatal=%v skip=%v)", r.fatal, r.skip)
		}
	})
	t.Run("a present tool does neither", func(t *testing.T) {
		t.Setenv("CI", "true")
		r := &recorder{TB: t}
		requireCmd(r, "go") // the toolchain running this test
		if r.fatal || r.skip {
			t.Errorf("a present tool must pass silently (fatal=%v skip=%v)", r.fatal, r.skip)
		}
	})
}
