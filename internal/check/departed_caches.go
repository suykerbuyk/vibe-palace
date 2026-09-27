// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// CheckDepartedCaches reports projects that moved to another vault but whose
// host-local embed cache is still on THIS host — the state a pull by a binary
// older than the departed-cache pass leaves behind.
//
// It calls storage.Vault.DepartedCaches with act=false: the SAME predicate the
// sweep deletes by, so the row can never disagree with what a pull would do.
// Report-only, like palace-local-only: Info or Pass, never Fail, and it never
// writes. When a guard stops the judgement (a merge in progress, Projects/
// unreadable, an empty listing) the row says "undecidable" and names the guard;
// it never reports that as zero.
//
// It is part of the full `vp check` suite only, not a vp_check selector: a
// selector would widen vp_check's schema enum.
func CheckDepartedCaches(v *storage.Vault) Result {
	r := Result{Name: "Departed caches"}
	res := v.DepartedCaches(false)
	switch {
	case res.Undecidable != "":
		r.Status = Info
		r.Summary = "undecidable: " + res.Undecidable
		r.Details = res.Errors
	case len(res.Departed) == 0:
		r.Status = Pass
		r.Summary = "none"
	default:
		r.Status = Info
		r.Summary = fmt.Sprintf("%d project(s) moved to another vault still have an embed cache on this host: %s",
			len(res.Departed), strings.Join(res.Departed, ", "))
		r.Details = append([]string{"`vp vault pull` removes them, and so does the first cache read of any process " +
			"that searches (the only way to remove them with git_enabled = false); no search serves them meanwhile"}, res.Errors...)
	}
	return r
}
