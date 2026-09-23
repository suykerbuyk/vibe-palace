// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// RebindKind selects what a checkout rebind changes in a project checkout's
// .vibe-palace.toml.
type RebindKind int

const (
	// RebindRename changes [project].name from FromSlug to ToSlug: the project
	// was renamed inside its vault.
	RebindRename RebindKind = iota + 1
	// RebindSplit sets the top-level vault_path: the project moved to another
	// vault under the same slug.
	RebindSplit
)

// CheckoutRebind describes one rebind of ONE checkout.
//
// 🔴 ONLY THE NEW CHECKOUT IS EVER WRITTEN. The quantum-ng migration flipped
// the old tree too (runbook RB15), so a session started there would have
// written into the new slug. AnchorSource is the only other checkout this
// type can name, and it is opened read-only.
//
// 🔴 NOTHING HERE IS PERSISTED. No path in this struct is written to any state
// file, journal or config: the old checkout's path went stale the moment it
// was quarantined, and anything keyed on it would have gone stale with it.
type CheckoutRebind struct {
	Kind RebindKind
	// Checkout is the NEW checkout's root, absolute. It must hold
	// .vibe-palace.toml itself; no upward walk, so a parent project's marker
	// is never edited.
	Checkout string
	// FromSlug is the slug the checkout names now. For a split it is also the
	// slug it keeps.
	FromSlug string
	// ToSlug is the new slug (rename only).
	ToSlug string
	// VaultPath is the new top-level vault_path exactly as it should be
	// written; "~/…" is allowed and kept (split only).
	VaultPath string
	// VaultRoot is the vault that must already hold the project under its new
	// name (rename only). A split checks the vault VaultPath resolves to.
	VaultRoot string
	// AnchorSource is an optional second checkout to copy the wrapstate
	// anchors FROM, read-only and at call time only.
	AnchorSource string
	// OldDirBase is an extra `git grep` pattern, the old checkout directory's
	// base name. It defaults to base(AnchorSource) when that is set.
	OldDirBase string
	// DryRun computes and reports everything and writes nothing.
	DryRun bool
}

// RebindFileAction reports what happened to one host-local file.
type RebindFileAction struct {
	From   string `json:"from,omitempty"`
	To     string `json:"to"`
	Action string `json:"action"` // moved, copied, absent, already-present, kept-different, would-move, would-copy
}

// RebindGrepHit is one line of the checkout that still names the old slug,
// directory or vault. It is reported, never fixed.
type RebindGrepHit struct {
	Pattern string `json:"pattern"`
	File    string `json:"file"`
	Line    string `json:"line"`
	Text    string `json:"text"`
}

// CheckoutRebindReport is everything a rebind did or would do.
type CheckoutRebindReport struct {
	TomlPath    string             `json:"toml_path"`
	TomlChange  string             `json:"toml_change"`
	BackupPath  string             `json:"backup_path,omitempty"`
	HostConfig  *RebindFileAction  `json:"host_config,omitempty"`
	Anchors     []RebindFileAction `json:"anchors,omitempty"`
	GrepHits    []RebindGrepHit    `json:"grep_hits,omitempty"`
	GrepTotal   int                `json:"grep_total"`
	GitCommands []string           `json:"git_commands"`
	Warnings    []string           `json:"warnings,omitempty"`
	DryRun      bool               `json:"dry_run"`
}

// rebindGrepCap bounds the hits a report carries; GrepTotal says how many
// there were.
const rebindGrepCap = 200

// RebindCheckout rebinds one project checkout after its project was renamed
// or split away: it edits exactly one line of the checkout's
// .vibe-palace.toml, carries the host-local bindings, and reports what it
// cannot fix.
//
// Every precondition is checked before the first write. It never commits in
// the project repository; GitCommands are for the operator to run, because
// trailer rules, hooks and branch policy are theirs.
func RebindCheckout(r CheckoutRebind) (CheckoutRebindReport, error) {
	rep := CheckoutRebindReport{DryRun: r.DryRun}
	if err := rebindValidate(r); err != nil {
		return rep, err
	}
	rep.TomlPath = filepath.Join(r.Checkout, ".vibe-palace.toml")
	old, err := os.ReadFile(rep.TomlPath)
	if err != nil {
		return rep, fmt.Errorf("refusing: %s: %w (the checkout root must hold its own .vibe-palace.toml)", rep.TomlPath, err)
	}
	pf, err := project.ParseProjectFile(rep.TomlPath)
	if err != nil {
		return rep, fmt.Errorf("refusing: %w", err)
	}
	if pf.Project.Name == "" {
		return rep, fmt.Errorf("refusing: %s names no [project].name", rep.TomlPath)
	}

	// The vault the checkout resolves to BEFORE the edit, for the grep of a
	// split: code that hard-codes the old vault's path.
	oldVault, _, _ := ResolveVaultPath(r.Checkout)

	var next string
	switch r.Kind {
	case RebindRename:
		if err := rebindRenamePreconditions(r, pf); err != nil {
			return rep, err
		}
		if pf.Project.Name == r.ToSlug {
			rep.TomlChange = "already rebound"
		} else {
			next, rep.TomlChange, err = rebindNameLine(string(old), r.FromSlug, r.ToSlug)
			if err != nil {
				return rep, err
			}
		}
		hc, err := rebindHostConfigPlan(r.FromSlug, r.ToSlug)
		if err != nil {
			return rep, err
		}
		rep.HostConfig = hc
	case RebindSplit:
		want, err := rebindSplitPreconditions(r, pf)
		if err != nil {
			return rep, err
		}
		if cur := strings.TrimSpace(pf.VaultPath); cur != "" && rebindSamePath(cur, want) {
			rep.TomlChange = "already rebound"
		} else {
			next, rep.TomlChange, err = rebindVaultPathLine(string(old), r.VaultPath)
			if err != nil {
				return rep, err
			}
		}
	}
	if next != "" {
		if err := rebindPostcondition(string(old), next, r); err != nil {
			return rep, err
		}
	}
	anchors, err := rebindAnchorPlan(r)
	if err != nil {
		return rep, err
	}

	// Report-only work: nothing below changes the checkout.
	rep.GrepHits, rep.GrepTotal, rep.Warnings = rebindGrep(r, oldVault)
	rep.Warnings = append(rep.Warnings, rebindQueuedJobs(r.Checkout, r.FromSlug, r.Kind)...)
	rep.GitCommands = rebindGitCommands(r)

	if r.DryRun {
		if rep.HostConfig != nil && rep.HostConfig.Action == "moved" {
			rep.HostConfig.Action = "would-move"
		}
		for i := range anchors {
			if anchors[i].Action == "copied" {
				anchors[i].Action = "would-copy"
			}
		}
		rep.Anchors = anchors
		return rep, nil
	}

	// Writes, in order: the toml, the host-local config, the anchors.
	if next != "" {
		bak, err := WriteHostLocalWithBackup(rep.TomlPath, old, []byte(next))
		if err != nil {
			return rep, fmt.Errorf("rewrite %s: %w", rep.TomlPath, err)
		}
		rep.BackupPath = bak
		if r.Kind == RebindSplit {
			got, src, err := ResolveVaultPath(r.Checkout)
			if err != nil || !rebindSamePath(got, r.VaultPath) || !strings.HasPrefix(src, "cwd:") {
				// Restore the pre-image: a rebind that does not resolve is
				// worse than none.
				_ = os.WriteFile(rep.TomlPath, old, 0o644)
				return rep, fmt.Errorf("refusing: after the edit the checkout resolves the vault %q (source %s, err %v), not %q; the original file was restored",
					got, src, err, r.VaultPath)
			}
		}
	}
	if rep.HostConfig != nil && rep.HostConfig.Action == "moved" {
		if err := os.Rename(rep.HostConfig.From, rep.HostConfig.To); err != nil {
			return rep, fmt.Errorf("move host project config: %w", err)
		}
	}
	for _, a := range anchors {
		if a.Action != "copied" {
			continue
		}
		data, err := os.ReadFile(a.From)
		if err != nil {
			return rep, fmt.Errorf("read anchor: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(a.To), 0o755); err != nil {
			return rep, fmt.Errorf("create anchor dir: %w", err)
		}
		if err := os.WriteFile(a.To, data, 0o644); err != nil {
			return rep, fmt.Errorf("write anchor: %w", err)
		}
	}
	rep.Anchors = anchors
	return rep, nil
}

func rebindValidate(r CheckoutRebind) error {
	if !filepath.IsAbs(r.Checkout) {
		return fmt.Errorf("refusing: checkout %q is not an absolute path", r.Checkout)
	}
	if r.FromSlug == "" {
		return fmt.Errorf("refusing: FromSlug is required")
	}
	switch r.Kind {
	case RebindRename:
		if r.ToSlug == "" || r.ToSlug == r.FromSlug || r.VaultPath != "" {
			return fmt.Errorf("refusing: a rename needs a ToSlug different from FromSlug and no VaultPath")
		}
		if !filepath.IsAbs(r.VaultRoot) {
			return fmt.Errorf("refusing: a rename needs the absolute VaultRoot that holds Projects/%s", r.ToSlug)
		}
	case RebindSplit:
		if r.VaultPath == "" || r.ToSlug != "" {
			return fmt.Errorf("refusing: a split needs a VaultPath and no ToSlug")
		}
	default:
		return fmt.Errorf("refusing: unknown rebind kind %d", r.Kind)
	}
	if r.AnchorSource != "" {
		if !filepath.IsAbs(r.AnchorSource) {
			return fmt.Errorf("refusing: anchor source %q is not an absolute path", r.AnchorSource)
		}
		if rebindSamePath(r.AnchorSource, r.Checkout) {
			return fmt.Errorf("refusing: the anchor source is the checkout itself")
		}
	}
	return nil
}

func rebindRenamePreconditions(r CheckoutRebind, pf project.ProjectFile) error {
	if pf.Project.Name != r.FromSlug && pf.Project.Name != r.ToSlug {
		return fmt.Errorf("refusing: the checkout names project %q, neither %q nor %q", pf.Project.Name, r.FromSlug, r.ToSlug)
	}
	if st, err := os.Stat(filepath.Join(r.VaultRoot, "Projects", r.ToSlug)); err != nil || !st.IsDir() {
		return fmt.Errorf("refusing: %s has no Projects/%s: rebind the checkout only after the vault rename has landed, "+
			"or its marker names a project the vault does not hold", r.VaultRoot, r.ToSlug)
	}
	if _, err := os.Lstat(filepath.Join(r.VaultRoot, "Projects", r.FromSlug)); err == nil {
		return fmt.Errorf("refusing: %s still has Projects/%s: the vault rename has not landed", r.VaultRoot, r.FromSlug)
	}
	return nil
}

// rebindSplitPreconditions returns the absolute vault VaultPath resolves to,
// after proving it is a vault of the required data format that holds the slug.
func rebindSplitPreconditions(r CheckoutRebind, pf project.ProjectFile) (string, error) {
	if pf.Project.Name != r.FromSlug {
		return "", fmt.Errorf("refusing: the checkout names project %q, not %q", pf.Project.Name, r.FromSlug)
	}
	want, err := rebindExpand(r.VaultPath)
	if err != nil {
		return "", fmt.Errorf("refusing: vault_path %q: %w", r.VaultPath, err)
	}
	format, err := surface.ReadFormat(want)
	if err != nil {
		return "", fmt.Errorf("refusing: %s is not a readable vault: %w", want, err)
	}
	if format != surface.RequiredDataFormat {
		return "", fmt.Errorf("refusing: %s is at data format %d, required %d", want, format, surface.RequiredDataFormat)
	}
	held := false
	for _, tree := range ProjectTrees(r.FromSlug) {
		if st, err := os.Stat(filepath.Join(want, filepath.FromSlash(tree))); err == nil && st.IsDir() {
			held = true
		}
	}
	if !held {
		return "", fmt.Errorf("refusing: %s holds neither palace/%s nor Projects/%s: point the checkout only at a vault that holds the project",
			want, r.FromSlug, r.FromSlug)
	}
	return want, nil
}

func rebindExpand(p string) (string, error) {
	e, err := expandTilde(p)
	if err != nil {
		return "", err
	}
	return filepath.Abs(e)
}

func rebindSamePath(a, b string) bool {
	ea, err1 := rebindExpand(a)
	eb, err2 := rebindExpand(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return samePath(ea, eb)
}

// rebindAssignment matches a line that is exactly `key = "value"`, modulo
// surrounding whitespace, and returns its indentation. Anything else (an
// inline comment, single quotes, escapes) is not matched, and the caller
// refuses rather than guesses.
func rebindAssignment(line, key string) (indent, value string, ok bool) {
	trimmed := trimLeftSpace(line)
	indent = line[:len(line)-len(trimmed)]
	k, isKey := parseKeyAssignment(trimmed, false)
	if !isKey || k != key {
		return "", "", false
	}
	rest := trimSpace(trimLeftSpace(trimmed[len(key):]))
	if !strings.HasPrefix(rest, "=") {
		return "", "", false
	}
	rest = trimSpace(rest[1:])
	if len(rest) < 2 || rest[0] != '"' || rest[len(rest)-1] != '"' || strings.ContainsAny(rest[1:len(rest)-1], "\"\\#") {
		return "", "", false
	}
	return indent, rest[1 : len(rest)-1], true
}

func rebindQuote(v string) string { return fmt.Sprintf("%q", v) }

// rebindNameLine rewrites the one active `name = "<from>"` line inside
// [project]. Every other byte is kept.
func rebindNameLine(text, from, to string) (string, string, error) {
	lines := strings.Split(text, "\n")
	idx := -1
	for _, sr := range FindSectionRanges(text) {
		if sr.Name != "project" {
			continue
		}
		for i := sr.StartLine + 1; i < sr.EndLine && i < len(lines); i++ {
			if k, ok := parseKeyAssignment(trimLeftSpace(lines[i]), false); ok && k == "name" {
				if idx >= 0 {
					return "", "", fmt.Errorf("refusing: [project] has more than one name line")
				}
				idx = i
			}
		}
	}
	if idx < 0 {
		return "", "", fmt.Errorf("refusing: no active name line inside [project]")
	}
	indent, val, ok := rebindAssignment(lines[idx], "name")
	if !ok || val != from {
		return "", "", fmt.Errorf("refusing: line %d %q is not exactly name = %q; edit it by hand", idx+1, lines[idx], from)
	}
	oldLine := lines[idx]
	lines[idx] = indent + "name = " + rebindQuote(to)
	return strings.Join(lines, "\n"), "- " + oldLine + "\n+ " + lines[idx], nil
}

// rebindVaultPathLine sets the TOP-LEVEL vault_path. A vault_path under any
// table refuses: it is already swallowed, and the operator must fix the file.
func rebindVaultPathLine(text, value string) (string, string, error) {
	lines := strings.Split(text, "\n")
	ranges := FindSectionRanges(text)
	topEnd := len(lines)
	for _, sr := range ranges {
		if sr.Name == "" {
			topEnd = sr.EndLine
			continue
		}
		for i := sr.StartLine + 1; i < sr.EndLine && i < len(lines); i++ {
			if k, ok := parseKeyAssignment(trimLeftSpace(lines[i]), false); ok && k == "vault_path" {
				return "", "", fmt.Errorf("refusing: line %d sets vault_path inside [%s], where TOML scopes it to that table "+
					"and it overrides nothing; move or remove it by hand", i+1, sr.Name)
			}
		}
	}
	newLine := "vault_path = " + rebindQuote(value)
	active, commented := -1, -1
	for i := 0; i < topEnd && i < len(lines); i++ {
		t := trimLeftSpace(lines[i])
		if k, ok := parseKeyAssignment(t, false); ok && k == "vault_path" {
			if active >= 0 {
				return "", "", fmt.Errorf("refusing: more than one top-level vault_path line")
			}
			active = i
		} else if k, ok := parseKeyAssignment(t, true); ok && k == "vault_path" && commented < 0 {
			commented = i
		}
	}
	switch {
	case active >= 0:
		indent, _, ok := rebindAssignment(lines[active], "vault_path")
		if !ok {
			return "", "", fmt.Errorf("refusing: line %d %q is not a plain vault_path = \"…\"; edit it by hand", active+1, lines[active])
		}
		oldLine := lines[active]
		lines[active] = indent + newLine
		return strings.Join(lines, "\n"), "- " + oldLine + "\n+ " + lines[active], nil
	case commented >= 0:
		// Keep the template's commented example and set the key under it.
		lines = append(lines[:commented+1], append([]string{newLine}, lines[commented+1:]...)...)
	default:
		// Top-level keys must precede the first table header.
		lines = append(lines[:topEnd], append([]string{newLine, ""}, lines[topEnd:]...)...)
	}
	return strings.Join(lines, "\n"), "+ " + newLine, nil
}

// rebindPostcondition refuses, before any write, unless the new text differs
// from the old in exactly the one key the rebind sets, with the intended value.
func rebindPostcondition(old, next string, r CheckoutRebind) error {
	var a, b map[string]any
	if _, err := toml.Decode(old, &a); err != nil {
		return fmt.Errorf("refusing: parse original: %w", err)
	}
	if _, err := toml.Decode(next, &b); err != nil {
		return fmt.Errorf("refusing: the edit would not parse: %w", err)
	}
	var pf project.ProjectFile
	if _, err := toml.Decode(next, &pf); err != nil {
		return fmt.Errorf("refusing: the edit would not parse: %w", err)
	}
	switch r.Kind {
	case RebindRename:
		if pf.Project.Name != r.ToSlug {
			return fmt.Errorf("refusing: the edit would name %q, not %q", pf.Project.Name, r.ToSlug)
		}
		pa, _ := a["project"].(map[string]any)
		pb, _ := b["project"].(map[string]any)
		if pa == nil || pb == nil {
			return fmt.Errorf("refusing: no [project] table")
		}
		delete(pa, "name")
		delete(pb, "name")
	case RebindSplit:
		if pf.VaultPath != r.VaultPath {
			return fmt.Errorf("refusing: the edit would set vault_path %q, not %q", pf.VaultPath, r.VaultPath)
		}
		delete(a, "vault_path")
		delete(b, "vault_path")
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("refusing: the edit would change more than the one key it sets")
	}
	return nil
}

// rebindHostConfigPlan decides the host-local projects/<from>.toml move. A
// conflicting destination refuses here, before the toml is touched.
func rebindHostConfigPlan(from, to string) (*RebindFileAction, error) {
	src, err := HostProjectConfigPath(from)
	if err != nil {
		return nil, err
	}
	dst, err := HostProjectConfigPath(to)
	if err != nil {
		return nil, err
	}
	a := &RebindFileAction{From: src, To: dst}
	sb, serr := os.ReadFile(src)
	db, derr := os.ReadFile(dst)
	switch {
	case errors.Is(serr, os.ErrNotExist):
		a.Action = "absent"
	case serr != nil:
		return nil, fmt.Errorf("read %s: %w", src, serr)
	case errors.Is(derr, os.ErrNotExist):
		a.Action = "moved"
	case derr != nil:
		return nil, fmt.Errorf("read %s: %w", dst, derr)
	case bytes.Equal(sb, db):
		a.Action = "already-present"
	default:
		return nil, fmt.Errorf("refusing: host project config %s already exists with different content than %s; "+
			"merge the two by hand, then re-run", dst, src)
	}
	return a, nil
}

// rebindAnchorPlan decides which wrapstate anchors to copy from AnchorSource.
// The per-session claimed-* sentinels are never copied.
func rebindAnchorPlan(r CheckoutRebind) ([]RebindFileAction, error) {
	if r.AnchorSource == "" {
		return nil, nil
	}
	var out []RebindFileAction
	for _, name := range []string{wrapstate.AnchorFile, wrapstate.SnapshotFile} {
		src := filepath.Join(r.AnchorSource, wrapstate.AnchorDir, name)
		dst := filepath.Join(r.Checkout, wrapstate.AnchorDir, name)
		a := RebindFileAction{From: src, To: dst}
		sb, serr := os.ReadFile(src)
		db, derr := os.ReadFile(dst)
		switch {
		case errors.Is(serr, os.ErrNotExist):
			a.Action = "absent"
		case serr != nil:
			return nil, fmt.Errorf("read %s: %w", src, serr)
		case errors.Is(derr, os.ErrNotExist):
			a.Action = "copied"
		case derr != nil:
			return nil, fmt.Errorf("read %s: %w", dst, derr)
		case bytes.Equal(sb, db):
			a.Action = "already-present"
		default:
			a.Action = "kept-different"
		}
		out = append(out, a)
	}
	return out, nil
}

// rebindGrep reports, and never fixes, lines of the checkout that still name
// the old slug, the old checkout directory, or the old vault.
func rebindGrep(r CheckoutRebind, oldVault string) ([]RebindGrepHit, int, []string) {
	var patterns []string
	if r.Kind == RebindRename {
		patterns = append(patterns, r.FromSlug)
	}
	dirBase := r.OldDirBase
	if dirBase == "" && r.AnchorSource != "" {
		dirBase = filepath.Base(r.AnchorSource)
	}
	if dirBase != "" && dirBase != r.FromSlug {
		patterns = append(patterns, dirBase)
	}
	if r.Kind == RebindSplit && oldVault != "" {
		patterns = append(patterns, oldVault)
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(oldVault, home+string(filepath.Separator)) {
			patterns = append(patterns, "~"+strings.TrimPrefix(oldVault, home))
		}
	}
	if len(patterns) == 0 {
		return nil, 0, nil
	}
	if state, _ := InspectVaultGit(r.Checkout); state != VaultGitOK && state != VaultGitNested {
		return nil, 0, []string{"the checkout is not a git work tree; the reference grep was skipped"}
	}
	var hits []RebindGrepHit
	total := 0
	for _, pat := range patterns {
		cmd := exec.Command("git", "-C", r.Checkout, "grep", "-n", "-I", "-F", "--untracked", "-e", pat)
		cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0")
		out, err := cmd.Output()
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			continue // no match
		}
		if err != nil {
			return hits, total, []string{fmt.Sprintf("git grep %q failed: %v", pat, err)}
		}
		for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			parts := strings.SplitN(l, ":", 3)
			if len(parts) != 3 {
				continue
			}
			total++
			if len(hits) < rebindGrepCap {
				hits = append(hits, RebindGrepHit{Pattern: pat, File: parts[0], Line: parts[1], Text: parts[2]})
			}
		}
	}
	return hits, total, nil
}

// rebindQueuedJobs warns about summarization and enrichment jobs in the
// checkout that still name the old slug. They are not rewritten.
func rebindQueuedJobs(checkout, from string, kind RebindKind) []string {
	if kind != RebindRename {
		return nil
	}
	var out []string
	for _, q := range []string{"summarization-queue", "enrichment-queue"} {
		dir := filepath.Join(checkout, wrapstate.AnchorDir, q)
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		n := 0
		for _, e := range ents {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			var job struct {
				Project string `json:"project"`
			}
			if json.Unmarshal(data, &job) == nil && job.Project == from {
				n++
			}
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("%d queued job(s) in %s still name project %q; drain or move them before the next session", n, dir, from))
		}
	}
	sort.Strings(out)
	return out
}

// rebindGitCommands are printed for the operator and never run.
func rebindGitCommands(r CheckoutRebind) []string {
	msg := fmt.Sprintf("Rebind this checkout to vibe-palace project %s", r.ToSlug)
	if r.Kind == RebindSplit {
		msg = fmt.Sprintf("Rebind this checkout to the vibe-palace vault at %s", r.VaultPath)
	}
	return []string{
		fmt.Sprintf("git -C %s add -- .vibe-palace.toml", r.Checkout),
		fmt.Sprintf("git -C %s commit -m %q", r.Checkout, msg),
		fmt.Sprintf("git -C %s push    # after the vault side is pushed", r.Checkout),
	}
}
