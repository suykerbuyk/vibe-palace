// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import "time"

// CalendarDay returns the calendar day the WRITER stamps onto a vault
// artifact, as YYYY-MM-DD, derived from an instant the caller supplies.
//
// 🔴 THE WRITER OWNS THE CLOCK. A client-supplied date is a SECOND clock, and a
// second clock is the defect this replaced. Clients do not get to backdate,
// reorder, or choose the calendar day of a transaction: arrival order at the
// writer IS order. (ADR-006 derive-don't-ask.)
//
// TWO FACTS, DELIBERATELY UNGLUED:
//
//   - the INSTANT is the writer's clock in UTC RFC3339 — already what logs and
//     archives record as CapturedAt / EnrichedAt, and not what this function is
//     about;
//   - the CALENDAR DAY is that instant in the WRITER PROCESS's local timezone
//     (`time.Local` / the OS zone). A laptop that travels Denver → Detroit and
//     whose OS adjusts will file Detroit's lived day; when it comes home, Denver
//     again. There is no vault-side timezone file.
//
// Option 2b (`clock.toml`, a vault-owned IANA zone, default UTC) was withdrawn
// 2026-08-30: the operator's lived day is the OS timezone of the machine that
// wrote, not a home-zone policy that would not travel. The 2026-08-19 ruling
// that clients never send a date is unchanged.
//
// The location is NOT a parameter. A Location argument would let MCP, CLI, and
// capture pass different zones, which is the two-site bug this helper exists to
// make impossible. Every stamper calls this (or Vault.CalendarDay, which is the
// same function).
//
// `now` is a parameter rather than a clock read here so a test can pin an
// instant, and so one logical operation cannot disagree with itself by reading
// the clock twice across midnight.
//
// time.Local is resolved from the process environment. A long-lived `vp mcp
// serve` started before an OS timezone change can keep the old zone until it is
// restarted — that is a property of the process, not a second clock.
func CalendarDay(now time.Time) string {
	return now.In(time.Local).Format("2006-01-02")
}

// CalendarLocation is the timezone CalendarDay uses: the process-local OS zone.
func CalendarLocation() *time.Location { return time.Local }

// ParseCalendarDay parses a YYYY-MM-DD stamp as midnight in the process-local
// zone. time.Parse("2006-01-02") anchors at UTC midnight and disagrees with a
// local stamp for several hours every evening.
func ParseCalendarDay(date string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", date, time.Local)
}

// CalendarDay is Vault's stamp of CalendarDay. The receiver is unused: the zone
// is the process, not the vault. The method exists so capture, archive, and
// audit share one call site on the writer they already hold.
func (v *Vault) CalendarDay(now time.Time) string { return CalendarDay(now) }

// CalendarLocation is Vault's stamp of CalendarLocation. See CalendarDay.
func (v *Vault) CalendarLocation() *time.Location { return CalendarLocation() }

// ParseCalendarDay is Vault's stamp of ParseCalendarDay. See CalendarDay.
func (v *Vault) ParseCalendarDay(date string) (time.Time, error) {
	return ParseCalendarDay(date)
}
