// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SkillChange is the plan entry for one ClaudeSkill or CursorRule file.
// Mirrors Change, but carries the SkillItem instead of a brief so Apply
// has everything Render needs without a separate re-lookup.
type SkillChange struct {
	Kind    ChangeKind
	Target  TargetKind
	Name    string
	Path    string
	Item    SkillItem // populated for New/Modified/UnchangedChange
	PrevSha string    // sha currently on disk for Modified/Stale
}

// PlanSkills computes the set of skill-shim changes required to bring
// projectRoot's ClaudeSkill or CursorRule directory into alignment with
// items. Pure relative to the filesystem scan — no writes — so callers
// can present it as a dry-run before invoking ApplySkills.
//
// Files whose names start with SkillFilePrefix but that lack our marker
// are reported as CustomChange. Files whose names don't match the prefix
// are ignored (other tools' territory). Stale files (marker present,
// name no longer in items) are reported as Stale.
//
// Prefer PlanSkillsAt when the skills parent is not project-relative
// (user-global plugin package).
func PlanSkills(target TargetKind, items []SkillItem, projectRoot string) ([]SkillChange, error) {
	if target != ClaudeSkill && target != CursorRule && target != GrokSkill {
		return nil, fmt.Errorf("PlanSkills: unsupported target %s", target)
	}
	var rootDir string
	switch target {
	case ClaudeSkill:
		rootDir = filepath.Join(projectRoot, ClaudeSkillsDir)
	case GrokSkill:
		rootDir = filepath.Join(projectRoot, GrokSkillsDir)
	default:
		rootDir = filepath.Join(projectRoot, CursorRulesDir)
	}
	return planSkillsAt(target, items, rootDir, func(name string) string {
		return TargetFile(target, projectRoot, name)
	})
}

// planSkillsAt is the layout-parameterized core shared by PlanSkills and
// PlanSkillsAt. rootDir is the absolute skills/rules parent to scan.
// pathFor names the absolute file path for a skill name under this layout.
func planSkillsAt(target TargetKind, items []SkillItem, rootDir string, pathFor func(name string) string) ([]SkillChange, error) {
	desired := make(map[string]SkillItem, len(items))
	for _, it := range items {
		desired[it.Name] = it
	}

	var changes []SkillChange

	// On-disk set: name -> absolute file path.
	onDisk := map[string]string{}
	// Customs carry neither Name nor Item.
	var customs []string

	entries, err := os.ReadDir(rootDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", rootDir, err)
	}
	for _, ent := range entries {
		switch target {
		case ClaudeSkill, GrokSkill:
			if !ent.IsDir() {
				continue
			}
			dirname := ent.Name()
			if !strings.HasPrefix(dirname, SkillFilePrefix) {
				continue
			}
			name := strings.TrimPrefix(dirname, SkillFilePrefix)
			path := filepath.Join(rootDir, dirname, "SKILL.md")
			scan, err := ScanShim(path)
			if err != nil {
				return nil, fmt.Errorf("scan %s: %w", path, err)
			}
			if !scan.Exists {
				// vps-<name>/ directory with no SKILL.md — treat as
				// custom (user-owned scaffolding) and leave alone.
				continue
			}
			if !scan.HasMarker {
				customs = append(customs, path)
				continue
			}
			onDisk[name] = path
		case CursorRule:
			if ent.IsDir() {
				continue
			}
			fname := ent.Name()
			if !strings.HasPrefix(fname, SkillFilePrefix) || !strings.HasSuffix(fname, ".mdc") {
				continue
			}
			name := strings.TrimSuffix(strings.TrimPrefix(fname, SkillFilePrefix), ".mdc")
			path := filepath.Join(rootDir, fname)
			scan, err := ScanShim(path)
			if err != nil {
				return nil, fmt.Errorf("scan %s: %w", path, err)
			}
			if !scan.HasMarker {
				customs = append(customs, path)
				continue
			}
			onDisk[name] = path
		}
	}

	// New / Modified / Unchanged for every desired skill.
	for _, it := range items {
		path := pathFor(it.Name)
		existing, ok := onDisk[it.Name]
		if !ok {
			changes = append(changes, SkillChange{
				Kind: New, Target: target, Name: it.Name, Path: path, Item: it,
			})
			continue
		}
		scan, err := ScanShim(existing)
		if err != nil {
			return nil, fmt.Errorf("rescan %s: %w", existing, err)
		}
		expected := ExpectedSkillSha(target, it)
		if scan.Sha == expected && scan.Version == skillShimVersion {
			changes = append(changes, SkillChange{
				Kind: UnchangedChange, Target: target, Name: it.Name,
				Path: existing, Item: it, PrevSha: scan.Sha,
			})
		} else {
			changes = append(changes, SkillChange{
				Kind: Modified, Target: target, Name: it.Name,
				Path: existing, Item: it, PrevSha: scan.Sha,
			})
		}
	}

	// Stale: on-disk shim for a name no longer desired.
	for name, path := range onDisk {
		if _, ok := desired[name]; ok {
			continue
		}
		scan, _ := ScanShim(path)
		changes = append(changes, SkillChange{
			Kind: Stale, Target: target, Name: name, Path: path, PrevSha: scan.Sha,
		})
	}

	// Custom files: surface for visibility; never written.
	for _, path := range customs {
		changes = append(changes, SkillChange{Kind: CustomChange, Target: target, Path: path})
	}

	sortSkillChanges(changes)
	return changes, nil
}

// PlanGrokHub computes the SkillChange for the single /vpc command hub at
// .grok/skills/vpc/SKILL.md. The hub is NOT covered by PlanSkills (its dir
// lacks the vps- prefix, so PlanSkills' prefix filter ignores it and never
// flags it Stale); this dedicated planner owns it instead. The returned
// change rides ApplySkills unchanged: New/Modified flow through WireSkill →
// RenderSkill → renderGrokHub, UnchangedChange and CustomChange are no-ops.
//
// Prefer PlanGrokHubAt when the skills parent is not project-relative.
func PlanGrokHub(projectRoot string) (SkillChange, error) {
	return PlanGrokHubAt(filepath.Join(projectRoot, GrokSkillsDir))
}

// planOneSkillFile classifies a single skill file path (hub or one-off).
func planOneSkillFile(target TargetKind, item SkillItem, path string) (SkillChange, error) {
	scan, err := ScanShim(path)
	if err != nil {
		return SkillChange{}, fmt.Errorf("scan %s: %w", path, err)
	}
	expected := ExpectedSkillSha(target, item)
	switch {
	case !scan.Exists:
		return SkillChange{Kind: New, Target: target, Name: item.Name, Path: path, Item: item}, nil
	case !scan.HasMarker:
		// A marker-less SKILL.md the user owns — surface, never overwrite.
		return SkillChange{Kind: CustomChange, Target: target, Name: item.Name, Path: path}, nil
	case scan.Sha == expected && scan.Version == skillShimVersion:
		return SkillChange{
			Kind: UnchangedChange, Target: target, Name: item.Name,
			Path: path, Item: item, PrevSha: scan.Sha,
		}, nil
	default:
		return SkillChange{
			Kind: Modified, Target: target, Name: item.Name,
			Path: path, Item: item, PrevSha: scan.Sha,
		}, nil
	}
}

// sortSkillChanges orders plan output as New → Modified → Unchanged →
// Stale → Custom; alphabetical by path within each kind.
func sortSkillChanges(changes []SkillChange) {
	order := map[ChangeKind]int{
		New: 0, Modified: 1, UnchangedChange: 2, Stale: 3, CustomChange: 4,
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if order[changes[i].Kind] != order[changes[j].Kind] {
			return order[changes[i].Kind] < order[changes[j].Kind]
		}
		return changes[i].Path < changes[j].Path
	})
}

// WireSkill ensures path holds a current shim for the (target, item) pair.
// Semantics mirror Wire for the command shim:
//
//   - File absent              -> write rendered, return Added.
//   - File present, no marker  -> leave untouched, return Custom.
//   - File present, marker sha matches expected -> Unchanged (no write).
//   - File present, marker sha mismatches or marker malformed -> rewrite,
//     return Updated with PrevSha set to whatever sha we extracted.
//
// Writes are atomic (tmp + fsync + rename) so a crash mid-write cannot
// leave a half-written shim visible to the editor's file watcher.
func WireSkill(path string, target TargetKind, item SkillItem) (Result, error) {
	rendered := RenderSkill(target, item)
	expected := ExpectedSkillSha(target, item)

	scan, err := ScanShim(path)
	if err != nil {
		return Result{}, fmt.Errorf("scan %s: %w", path, err)
	}

	switch {
	case !scan.Exists:
		if err := atomicWrite(path, []byte(rendered)); err != nil {
			return Result{}, err
		}
		return Result{Kind: Added}, nil

	case !scan.HasMarker:
		return Result{Kind: Custom}, nil

	case scan.Sha == expected && scan.Version == skillShimVersion:
		return Result{Kind: Unchanged, PrevSha: scan.Sha}, nil

	default:
		if err := atomicWrite(path, []byte(rendered)); err != nil {
			return Result{}, err
		}
		return Result{Kind: Updated, PrevSha: scan.Sha}, nil
	}
}

// SkillOutcome pairs a SkillChange with its Result for Apply-style
// reporting. Kept separate from Outcome so callers can distinguish
// command-shim and skill-shim reports in the CLI surface.
type SkillOutcome struct {
	Change  SkillChange
	Result  Result
	Removed bool
	Err     error
}

// ApplySkills executes a skill-shim plan. Never touches Custom files.
// Stale files are removed iff opts.AllowStaleRemoval is true. Unchanged
// entries incur no IO.
//
// Returns the first IO error encountered alongside the Report
// accumulated up to that point.
func ApplySkills(changes []SkillChange, opts ApplyOptions) (Report, []SkillOutcome, error) {
	var (
		rep      Report
		outcomes = make([]SkillOutcome, 0, len(changes))
	)
	for _, ch := range changes {
		out := SkillOutcome{Change: ch}
		switch ch.Kind {
		case New, Modified:
			res, err := WireSkill(ch.Path, ch.Target, ch.Item)
			out.Result = res
			out.Err = err
			if err != nil {
				outcomes = append(outcomes, out)
				rep.Outcomes = nil // skill outcomes live in the returned slice
				return rep, outcomes, fmt.Errorf("wire %s: %w", ch.Path, err)
			}
			switch res.Kind {
			case Added:
				rep.Added++
			case Updated:
				rep.Updated++
			case Unchanged:
				rep.Unchanged++
			case Custom:
				rep.Custom++
			}
		case UnchangedChange:
			out.Result = Result{Kind: Unchanged, PrevSha: ch.PrevSha}
			rep.Unchanged++
		case Stale:
			if !opts.AllowStaleRemoval {
				rep.Stale++
				out.Result = Result{Kind: Unchanged, PrevSha: ch.PrevSha}
				break
			}
			removed, err := Remove(ch.Path)
			out.Err = err
			out.Removed = removed
			if err != nil {
				outcomes = append(outcomes, out)
				return rep, outcomes, fmt.Errorf("remove %s: %w", ch.Path, err)
			}
			if removed {
				rep.Removed++
				// ClaudeSkill/GrokSkill: try to prune the now-empty parent
				// vps-<name>/ directory so the scan set shrinks. Best
				// effort — if the dir still has references/ siblings
				// the remove fails and we leave it alone.
				if ch.Target == ClaudeSkill || ch.Target == GrokSkill {
					_ = os.Remove(filepath.Dir(ch.Path))
				}
			} else {
				rep.Custom++
			}
		case CustomChange:
			rep.Custom++
			out.Result = Result{Kind: Custom}
		}
		outcomes = append(outcomes, out)
	}
	return rep, outcomes, nil
}
