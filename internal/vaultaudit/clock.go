// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package vaultaudit

import "time"

// WriterCalendarDay returns the calendar day the WRITER stamps onto a vault
// artifact, as YYYY-MM-DD, derived from an instant the caller supplies.
//
// 🔴 THE WRITER OWNS THE CLOCK. A client-supplied date is a SECOND clock, and a
// second clock is the defect this replaced. Hosts run with the wrong timezone, a
// drifted RTC, an inherited TZ=UTC, or sit in a different country from the vault —
// "the agent knows the session date" is only ever that process's time.Now(). So
// clients do not get to backdate, reorder, or choose the calendar day of a
// transaction: arrival order at the writer IS order. (Operator ruling 2026-08-19;
// ADR-006 derive-don't-ask; PRD §1.8, the vault sits beside the server.)
//
// TWO FACTS, DELIBERATELY UNGLUED:
//
//   - the INSTANT is the writer's clock in UTC — already what logs and archives
//     record, and not what this function is about;
//   - the CALENDAR DAY is derived from that instant in ONE timezone owned by the
//     vault, so a VPS whose machine timezone is UTC still files the operator's
//     lived day once that timezone is configured.
//
// Today that timezone is UTC. That is option 2b's stated DEFAULT, not a silent
// retreat to option 2a: when the vault timezone config lands, this function grows a
// *time.Location (read from the vault config) and every caller follows it without
// moving. That is the entire reason it exists as ONE function — the bug being fixed
// was two independent time.Now().Format("2006-01-02") sites, in the MCP handler and
// the CLI, which disagreed because nothing held them together.
//
// `now` is a parameter rather than a clock read here for two reasons: a test can pin
// an instant without a clock seam, and a single logical operation cannot disagree
// with itself by reading the clock twice across midnight.
func WriterCalendarDay(now time.Time) string {
	return now.UTC().Format("2006-01-02")
}
