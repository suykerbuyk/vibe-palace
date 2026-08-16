// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/mdfence"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The vault is synced to every machine and lives somewhere different on each,
// so a host-rooted absolute path committed into a synced document is a fact
// about the ONE host that wrote it and is false everywhere else from the moment
// it lands.
//
// This check exists because of iter 188: resume.md carried
// `Vault location: /home/johns/vibe-palace-vault`, true only on the operator's
// previous WSL host. An EMPTY DIRECTORY sits at that path on the current
// machine, so the stale answer looked plausible instead of failing loudly —
// the worst failure shape available. The first fix attempt was more prose (a
// "don't do that" note); the operator rejected it, because every rule prose can
// state is already in the injected context of the agent that broke it.
//
// Like every check in this file's neighbourhood: prevention is unachievable
// in-process (no typed write path is left to gate, and any agent holding Bash
// writes the file directly), so this is DETECTION, reported as Info — never
// Fail — and it never writes, moves, or repairs anything.
//
// # Scope: resume.md + workflow.md only
//
// Those two ARE the ADR-009 inviolable core, and they are the only synced docs
// read as CURRENT TRUTH rather than as history. iterations.md and tasks/ are
// deliberately EXCLUDED: they legitimately quote host paths as specimens of the
// mistake — the task that commissioned this check quotes the 188 path twice,
// once in a blockquote where fence-awareness would not save it. A check that
// fires on the record of a bug being fixed is a check that gets ignored.
//
// # Why an allowlist of roots, and not a denylist of exemptions
//
// Only prefixes that are definitionally host-rooted are matched. Everything
// else is silent by construction — `/proc/<pid>/exe`, `/usr`, `/etc`, `/tmp`,
// `/var` and every other FHS location never match because they are not in the
// set, so there is no exemption list to maintain and no way for a new
// machine-independent path to start firing when someone forgets to add it.
// resume.md legitimately cites `/proc/<pid>/exe` in its stale-server note; that
// citation is not special-cased anywhere, it simply never matches.
//
// Tilde paths (`~/.local/bin/vp`, `~/.config/vibe-palace/config.toml`) are host
// CONVENTIONS, not host FACTS — they resolve correctly on every host — and are
// likewise not in the match set. Repo-relative paths (`internal/check/resume.go`)
// have no leading root at all.
//
// # No os.UserHomeDir(), deliberately
//
// The obvious extension is to also flag the literal expansion of the running
// host's $HOME. It is refused: a check whose verdict depends on WHICH host ran
// it is the exact bug class this check exists to detect, and it would report a
// finding on one machine and silence on another for byte-identical content. The
// expanded-home case is already covered structurally — `/home/<user>` on Linux,
// `/Users/<user>` on macOS, `/mnt/<drive>` under WSL.
var vaultAbsPathPatterns = []*regexp.Regexp{
	// POSIX user-rooted: /home/<user>/…, /Users/<user>/…, /mnt/<drive>/…,
	// /root/…. The leading group keeps the match from firing mid-path (so
	// `/mnt/c/home/x` is reported once, as the /mnt/ path it is) and from
	// firing on a tilde-relative token.
	regexp.MustCompile(`(^|[^A-Za-z0-9_./\\~-])(/(?:home|Users|mnt|root)/[^\s\x60'"()\[\]]*)`),

	// Windows drive-letter roots: C:\…. Requires the `:\` pair, so a bare
	// `C:` in prose never matches.
	regexp.MustCompile(`(^|[^A-Za-z0-9_\\])([A-Za-z]:\\[^\s\x60'"()\[\]]*)`),

	// Windows extended-length prefix: \\?\…
	regexp.MustCompile(`(^|[^A-Za-z0-9_])(\\\\\?\\[^\s\x60'"()\[\]]*)`),
}

// vaultAbsPathScope is the set of per-project filenames scanned. See the scope
// note above: this is the ADR-009 core, not the whole project directory.
var vaultAbsPathScope = []string{"resume.md", "workflow.md"}

// vaultAbsPathBreaches returns one human-readable line per host-rooted absolute
// path committed into the body, each carrying the 1-indexed source line and the
// matched path. An empty slice means the body is clean.
//
// It is fence-aware: a path documented inside a Markdown code fence is a sample
// — a command someone is meant to read, not a pointer the document is asserting
// — and is never flagged. Fence classification uses the shared mdfence.Scanner,
// the one definition in this codebase, so an inline code run is never misread as
// an opening fence. Inline backticks are NOT an exemption: a path in a code span
// mid-sentence is still the document asserting where something lives.
func vaultAbsPathBreaches(file string, body []byte) []string {
	var out []string
	var fences mdfence.Scanner
	lineNo := 0
	for line := range strings.SplitSeq(string(body), "\n") {
		lineNo++
		// Advance the scanner on EVERY line so fence state stays correct;
		// only lines outside a fence (Text) can carry a live assertion.
		if fences.Step(line) != mdfence.Text {
			continue
		}
		clean := strings.TrimSuffix(line, "\r")
		for _, re := range vaultAbsPathPatterns {
			for _, m := range re.FindAllStringSubmatch(clean, -1) {
				path := strings.TrimRight(m[2], ".,;:!?")
				if path == "" {
					continue
				}
				out = append(out, fmt.Sprintf("%s line %d: %s", file, lineNo, path))
			}
		}
	}
	return out
}

// CheckVaultAbsPaths scans every project under <vault>/Projects/ and reports the
// ones whose resume.md or workflow.md commits a host-rooted absolute path.
// Because those files are synced to every machine, such a path is a fact about
// the host that wrote it and is false — often silently, plausibly false —
// everywhere else.
//
// It is strictly READ-ONLY and advisory: Pass when every scanned file is clean,
// Info when one or more carry a host-rooted path. Never Fail — rewriting a
// pointer is a wrap-time judgement call, and the remedy ("resolve, don't
// recall") is a human decision about what the document meant to say. Skip when
// no vault root is configured.
//
// Absence is never a violation: a missing Projects/ dir, a project with neither
// file, and a clean file all report nothing.
func CheckVaultAbsPaths(v *storage.Vault) Result {
	r := Result{Name: "Vault abs paths"}

	if v.Root == "" {
		r.Status = Skip
		r.Summary = "no vault configured"
		return r
	}

	projectsDir := filepath.Join(v.Root, "Projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			r.Status = Pass
			r.Summary = "no Projects/ directory"
			return r
		}
		r.Status = Info
		r.Summary = fmt.Sprintf("scan Projects/: %v", err)
		return r
	}

	scanned := 0
	var violations []resumeViolation
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		var breaches []string
		seen := false
		for _, file := range vaultAbsPathScope {
			data, rerr := os.ReadFile(filepath.Join(projectsDir, name, file))
			if rerr != nil {
				// A project carrying only one of the two files is normal.
				continue
			}
			seen = true
			breaches = append(breaches, vaultAbsPathBreaches(file, data)...)
		}
		if !seen {
			continue
		}
		scanned++
		if len(breaches) > 0 {
			violations = append(violations, resumeViolation{Project: name, Breaches: breaches})
		}
	}

	if len(violations) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%d project core free of host-rooted paths", scanned)
		return r
	}

	sort.Slice(violations, func(i, j int) bool { return violations[i].Project < violations[j].Project })
	r.Status = Info
	r.Summary = fmt.Sprintf("%d of %d project core carry host-rooted paths", len(violations), scanned)
	for _, viol := range violations {
		r.Details = append(r.Details,
			fmt.Sprintf("  %s: %s", viol.Project, strings.Join(viol.Breaches, "; ")))
	}
	r.Details = append(r.Details,
		"resume.md and workflow.md are synced to every machine, so an absolute host path in one is a fact",
		"about the host that wrote it and is false everywhere else — and a stale path that still EXISTS",
		"as an empty directory reads as plausible instead of failing loudly (the iter 188 bug).",
		"RESOLVE, DON'T RECALL: `vp check` prints the resolved vault_path AND its source; `vault_path` lives in",
		"the global config (per-tree override: .vibe-palace.toml). Write the CONSTRAINT (POSIX filesystem,",
		"never NTFS/exFAT), never the PATH. Scope is the ADR-009 core only — iterations.md and tasks/ may",
		"quote host paths as specimens and are deliberately not scanned.")
	return r
}
