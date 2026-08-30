// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func denverOrSkip(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skipf("no tzdata for America/Denver on this host: %v", err)
	}
	return loc
}

// measuredEvening is the instant the whole finding was measured on:
// 2026-08-12 22:57 America/Denver == 2026-08-13 04:57 UTC.
func measuredEvening(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 8, 12, 22, 57, 0, 0, denverOrSkip(t))
}

func TestCalendarDay_IgnoresTheInstantsOwnZone(t *testing.T) {
	v := NewVault(t.TempDir())
	instant := time.Date(2026, 8, 13, 4, 57, 0, 0, time.UTC)

	zones := []*time.Location{time.UTC, time.FixedZone("minus6", -6*3600), time.FixedZone("plus14", 14*3600)}
	first := v.CalendarDay(instant.In(zones[0]))
	for _, z := range zones[1:] {
		if got := v.CalendarDay(instant.In(z)); got != first {
			t.Errorf("CalendarDay(same instant, zone %s) = %q, want %q — the SAME instant "+
				"must yield the SAME local calendar day regardless of which zone the caller's "+
				"time.Time is wearing", z, got, first)
		}
	}
}

func TestCalendarDay_IgnoresClockToml(t *testing.T) {
	v := NewVault(t.TempDir())
	evening := measuredEvening(t)
	without := v.CalendarDay(evening)
	if err := os.WriteFile(filepath.Join(v.Root, "clock.toml"), []byte("timezone = \"America/Denver\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	with := v.CalendarDay(evening)
	if with != without {
		t.Errorf("CalendarDay with clock.toml = %q, without = %q — the file must not be an authority", with, without)
	}
}

func TestParseCalendarDay_UsesProcessLocal(t *testing.T) {
	v := NewVault(t.TempDir())
	got, err := v.ParseCalendarDay("2026-08-12")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 8, 12, 0, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("ParseCalendarDay = %s, want %s", got, want)
	}
}

// TestCalendarDay_FollowsProcessTZ is the load-bearing pin that in-process tests
// cannot make: time.Local is fixed for the test process, and a UTC CI host cannot
// tell UTC Format from Local Format. A child with TZ=America/Denver must stamp
// the measured evening as 2026-08-12; a child with TZ=UTC must stamp 2026-08-13.
func TestCalendarDay_FollowsProcessTZ(t *testing.T) {
	if os.Getenv("VP_CLOCK_CHILD") == "1" {
		evening := time.Date(2026, 8, 12, 22, 57, 0, 0, time.FixedZone("MDT", -6*3600))
		os.Stdout.WriteString("CALENDAR_DAY=" + CalendarDay(evening) + "\n")
		os.Exit(0)
	}

	run := func(tz, want string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestCalendarDay_FollowsProcessTZ$", "-test.v=false")
		cmd.Env = append(os.Environ(), "VP_CLOCK_CHILD=1", "TZ="+tz)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child TZ=%s: %v\n%s", tz, err, out)
		}
		got := ""
		for _, line := range strings.Split(string(out), "\n") {
			if rest, ok := strings.CutPrefix(line, "CALENDAR_DAY="); ok {
				got = rest
				break
			}
		}
		if got != want {
			t.Errorf("TZ=%s CalendarDay = %q, want %q\n%s", tz, got, want, out)
		}
	}

	run("UTC", "2026-08-13")
	if _, err := time.LoadLocation("America/Denver"); err != nil {
		t.Logf("skipping America/Denver child: %v", err)
		return
	}
	run("America/Denver", "2026-08-12")
}
