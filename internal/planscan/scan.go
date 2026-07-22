// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package planscan is a read-only "detect-and-report" reporter for orphaned
// Claude Code plan files. Claude Code drops a plan's markdown under a FLAT
// ~/.claude/plans/ directory with no cwd encoding, so a stray plan carries no
// structural signal about which project it belonged to — the only cwd evidence
// it leaves is the absolute filesystem paths its prose happens to mention.
//
// Scan greps those paths out of each plan body, reduces them to candidate
// directory roots ranked by frequency then depth, and attributes each plan to a
// vibe-palace-managed project (SignalVibeConfig + a matching Projects/<slug> in
// the vault), an unmanaged real directory (a marker walk that found nothing), or
// none (the body had no absolute path at all, so the plan is unattributable).
//
// The package NEVER promotes, deletes, or writes anything: it only reads the
// plans dir, reads the referenced directories' markers via project.DetectSignal,
// and Stats the vault. An absent plans dir is normal (returns an empty Report,
// nil error) — Grok and Zed have no plans dir at all.
package planscan

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/project"
)

// Resolution is the attribution verdict for a single stray plan.
//
// Kind is one of "managed", "unmanaged", or "none":
//   - managed:   a candidate dir carries a .vibe-palace.toml AND its detected
//     slug has a matching <vault>/Projects/<slug> directory. Project holds the
//     slug.
//   - unmanaged: candidate dirs resolve to real directories but none is a
//     vault-managed project (no marker up to $HOME, or a marker with no vault
//     project). CandidateDir holds the top-ranked candidate as evidence.
//   - none:      the plan body contained no absolute path, so it is
//     unattributable. CandidateDir is empty.
//
// Candidates lists every ranked candidate root (best first); CandidateDir is
// its head. When a plan's candidates resolve to more than one distinct owner,
// Ambiguous is set and Candidates carries them all — a multi-root plan is never
// collapsed to a single guessed owner.
type Resolution struct {
	Kind         string   `json:"kind"`
	Project      string   `json:"project,omitempty"`
	CandidateDir string   `json:"candidate_dir,omitempty"`
	Candidates   []string `json:"candidates,omitempty"`
	Ambiguous    bool     `json:"ambiguous,omitempty"`
}

// StrayPlan is one plan file under the plans dir, the absolute paths grepped
// from its body (deduplicated, sorted), and its attribution.
type StrayPlan struct {
	File       string     `json:"file"`
	AbsPaths   []string   `json:"abs_paths"`
	Resolution Resolution `json:"resolution"`
}

// Report is the full read-only scan result: the plans dir that was scanned and
// one StrayPlan per *.md file found under it (sorted by path). An absent plans
// dir yields a Report with the resolved PlansDir and an empty Strays slice.
type Report struct {
	PlansDir string      `json:"plans_dir"`
	Strays   []StrayPlan `json:"strays"`
}

// Scan reads <claudeHome>/plans/*.md and reports attribution for each stray
// plan, resolving managed projects against vaultRoot. It is pure with respect
// to its inputs (pass an explicit claudeHome and vaultRoot) and never mutates
// anything. An absent plans dir returns an empty Report and a nil error.
func Scan(claudeHome, vaultRoot string) (Report, error) {
	plansDir := filepath.Join(claudeHome, "plans")
	report := Report{PlansDir: plansDir, Strays: []StrayPlan{}}

	matches, err := filepath.Glob(filepath.Join(plansDir, "*.md"))
	if err != nil {
		// The only error Glob returns is ErrBadPattern, which our fixed
		// "*.md" cannot produce; treat defensively as "no plans".
		return report, nil
	}
	sort.Strings(matches)

	for _, file := range matches {
		body, rerr := os.ReadFile(file)
		if rerr != nil {
			// Unreadable plan (permissions, vanished mid-scan): report it as
			// unattributable rather than failing the whole scan.
			report.Strays = append(report.Strays, StrayPlan{
				File:       file,
				AbsPaths:   []string{},
				Resolution: Resolution{Kind: "none"},
			})
			continue
		}
		report.Strays = append(report.Strays, classify(file, string(body), vaultRoot))
	}

	return report, nil
}

// classify attributes a single plan body. It is the deterministic core of the
// scan: grep abs paths, rank candidate dirs, resolve each candidate's owner,
// and fold the results into a Resolution.
func classify(file, body, vaultRoot string) StrayPlan {
	occurrences := extractAbsPaths(body)
	absPaths := dedupSorted(occurrences)

	sp := StrayPlan{File: file, AbsPaths: absPaths}
	if len(occurrences) == 0 {
		sp.Resolution = Resolution{Kind: "none"}
		return sp
	}

	candidates := rankCandidateDirs(occurrences)
	if len(candidates) == 0 {
		sp.Resolution = Resolution{Kind: "none"}
		return sp
	}

	// Resolve every candidate's owner, preserving rank order.
	type owned struct {
		dir     string
		kind    string // "managed" | "unmanaged"
		project string // slug when managed
		ownerID string // distinct-owner key: "m:"+slug or "u:"+dir
	}
	resolved := make([]owned, 0, len(candidates))
	ownerSet := make(map[string]struct{})
	for _, dir := range candidates {
		kind, slug := resolveCandidate(dir, vaultRoot)
		id := "u:" + dir
		if kind == "managed" {
			id = "m:" + slug
		}
		resolved = append(resolved, owned{dir: dir, kind: kind, project: slug, ownerID: id})
		ownerSet[id] = struct{}{}
	}

	best := resolved[0]
	res := Resolution{
		Kind:         best.kind,
		Project:      best.project,
		CandidateDir: best.dir,
		Candidates:   candidates,
		Ambiguous:    len(ownerSet) > 1,
	}
	sp.Resolution = res
	return sp
}

// resolveCandidate reports a single candidate dir's owner. It gates on
// SignalVibeConfig FIRST (DetectProject falls back to git/basename for ANY
// dir, so it must not be trusted on its own), then confirms the detected slug
// has a real <vault>/Projects/<slug> directory before calling it managed.
// Anything else — no marker, or a marker with no matching vault project — is
// unmanaged.
func resolveCandidate(dir, vaultRoot string) (kind, proj string) {
	if project.DetectSignal(dir) != project.SignalVibeConfig {
		return "unmanaged", ""
	}
	slug, err := project.DetectProject(dir)
	if err != nil || slug == "" {
		return "unmanaged", ""
	}
	if vaultRoot == "" {
		return "unmanaged", ""
	}
	projectDir := filepath.Join(vaultRoot, "Projects", slug)
	if fi, err := os.Stat(projectDir); err == nil && fi.IsDir() {
		return "managed", slug
	}
	return "unmanaged", ""
}

// absPathRe matches an absolute Unix path in prose. The path must be preceded
// by the start of input or a boundary rune (whitespace, quote, backtick, or an
// opening bracket) so mid-token slashes — most importantly the "//" in a URL
// like https://host/x — are not mistaken for paths. The path body runs until a
// closing boundary rune. Trailing sentence punctuation is trimmed afterward.
var absPathRe = regexp.MustCompile("(?:^|[\\s(\\[<\"'`])(/[^\\s)\\]>\"'`]+)")

// extractAbsPaths greps every absolute path occurrence from body (WITH
// repetition — the counts drive frequency ranking). Trailing sentence
// punctuation and Markdown trailing colons are trimmed; single "/" and other
// degenerate matches are dropped.
func extractAbsPaths(body string) []string {
	ms := absPathRe.FindAllStringSubmatch(body, -1)
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		p := strings.TrimRight(m[1], ".,;:!?")
		if len(p) < 2 { // "/" alone, or empty after trim
			continue
		}
		out = append(out, filepath.Clean(p))
	}
	return out
}

// dedupSorted returns the unique paths in sorted order for stable reporting.
func dedupSorted(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// rankCandidateDirs reduces path occurrences to candidate directory roots and
// ranks them: an existing file's parent dir (or an existing/inferred dir
// itself) is the candidate, tallied by how often the plan mentions it.
// Ancestor/descendant chains are collapsed FIRST via collapseLineages — a path
// and any segment-descendant of it are one lineage (one owner), folded into the
// shallowest path with the counts summed — so a plan naming both a dir and a
// file beneath it yields a single directory candidate, never two. Order is
// frequency desc, then depth (path segment count) desc — a deeper, more
// specific dir is the better cwd guess on a tie — then lexical asc for a stable
// deterministic order.
func rankCandidateDirs(occurrences []string) []string {
	raw := make(map[string]int)
	for _, p := range occurrences {
		raw[dirOf(p)]++
	}
	counts := collapseLineages(raw)
	dirs := make([]string, 0, len(counts))
	for d := range counts {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if counts[dirs[i]] != counts[dirs[j]] {
			return counts[dirs[i]] > counts[dirs[j]]
		}
		di, dj := depth(dirs[i]), depth(dirs[j])
		if di != dj {
			return di > dj
		}
		return dirs[i] < dirs[j]
	})
	return dirs
}

// collapseLineages folds ancestor/descendant candidate chains into a single
// canonical root. Two candidates are the same lineage (same owner) when one is
// a segment-prefix of the other — `/a/b` is a segment-prefix of `/a/b/c` but
// NOT of `/a/bc`. Descendants are collapsed into the shallowest ancestor and
// their counts summed onto it, so the remaining candidates are mutually
// non-lineage: neither is a segment-prefix of another. This is pure string
// lineage — we deliberately never stat, because referenced paths are often
// "files to create" that do not exist yet.
func collapseLineages(counts map[string]int) map[string]int {
	dirs := make([]string, 0, len(counts))
	for d := range counts {
		dirs = append(dirs, d)
	}
	// Shallowest first so an ancestor is always chosen as the root before any
	// of its descendants are visited.
	sort.Slice(dirs, func(i, j int) bool {
		di, dj := depth(dirs[i]), depth(dirs[j])
		if di != dj {
			return di < dj
		}
		return dirs[i] < dirs[j]
	})
	collapsed := make(map[string]int, len(dirs))
	roots := make([]string, 0, len(dirs))
	for _, d := range dirs {
		owner := ""
		for _, r := range roots {
			if isSegmentPrefix(r, d) {
				owner = r
				break
			}
		}
		if owner == "" {
			roots = append(roots, d)
			owner = d
		}
		collapsed[owner] += counts[d]
	}
	return collapsed
}

// isSegmentPrefix reports whether ancestor is equal to p or an ancestor of p on
// a path-segment boundary: `/a/b` is a segment-prefix of `/a/b` and `/a/b/c`,
// but not of `/a/bc`. Comparison is pure string lineage over slash-normalized
// paths (both are already filepath.Clean'd absolute paths).
func isSegmentPrefix(ancestor, p string) bool {
	if ancestor == p {
		return true
	}
	a := filepath.ToSlash(ancestor)
	b := filepath.ToSlash(p)
	if !strings.HasPrefix(b, a) {
		return false
	}
	if strings.HasSuffix(a, "/") { // root "/" is an ancestor of everything
		return true
	}
	return len(b) > len(a) && b[len(a)] == '/'
}

// dirOf reduces a referenced path to its candidate directory: an existing
// directory is itself; an existing regular file contributes its parent dir; a
// non-existent path (referenced but not on this host) is taken as-is, so the
// marker walk still gets a chance to attribute it via an on-disk ancestor.
func dirOf(p string) string {
	if fi, err := os.Stat(p); err == nil {
		if fi.IsDir() {
			return p
		}
		return filepath.Dir(p)
	}
	return p
}

// depth counts path segments below root, used as the ranking tie-break.
func depth(p string) int {
	return strings.Count(strings.Trim(filepath.ToSlash(p), "/"), "/") + 1
}
