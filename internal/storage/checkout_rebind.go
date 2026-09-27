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

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/wrapstate"
)

// RebindKind selects what a checkout rebind changes in a project checkout's
// .vibe-palace.toml.
type RebindKind int

const (
	// RebindRename changes [project].name from FromSlug to ToSlug: the project
	// was renamed inside its vault.
	RebindRename RebindKind = iota + 1
	// RebindSplit binds the project to its new vault on this host
	// ([project_vaults], ADR-012) through BindProjectVault: the project moved to
	// another vault under the same slug. The checkout is never written — a
	// committed marker never carries vault_path.
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
	// VaultPath is the vault the project moved to, written into the host's
	// [project_vaults] exactly as given; it must be absolute after ~ expansion
	// (split only).
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
	TomlPath   string `json:"toml_path"`
	TomlChange string `json:"toml_change"`
	BackupPath string `json:"backup_path,omitempty"`
	// BindingChange is the [project_vaults] key a rename moved; Binding is the
	// bind a split made.
	BindingChange string             `json:"binding_change,omitempty"`
	Binding       *BindReport        `json:"binding,omitempty"`
	HostConfig    *RebindFileAction  `json:"host_config,omitempty"`
	Anchors       []RebindFileAction `json:"anchors,omitempty"`
	GrepHits      []RebindGrepHit    `json:"grep_hits,omitempty"`
	GrepTotal     int                `json:"grep_total"`
	GitCommands   []string           `json:"git_commands"`
	Warnings      []string           `json:"warnings,omitempty"`
	DryRun        bool               `json:"dry_run"`
}

// rebindGrepCap bounds the hits a report carries; GrepTotal says how many
// there were.
const rebindGrepCap = 200

// RebindCheckout rebinds one project checkout after its project was renamed
// or split away, and reports what it cannot fix.
//
// A rename edits exactly one line of the checkout's .vibe-palace.toml, moves
// the host-local project config and the host's [project_vaults] key, and
// carries the wrapstate anchors. A split changes no byte of the checkout: it
// binds the project to its new vault in the host's global config
// (BindProjectVault, ADR-012) — a committed marker never carries vault_path.
//
// The marker is read by project.ReadMarker, the reader resolution and
// detection share, and must be the file project.FindMarker finds from the
// checkout, so the marker this edits is the marker that binds the vault.
//
// Every precondition is checked before the first write. It never commits in
// the project repository; GitCommands are for the operator to run, because
// trailer rules, hooks and branch policy are theirs.
func RebindCheckout(r CheckoutRebind) (CheckoutRebindReport, error) {
	rep := CheckoutRebindReport{DryRun: r.DryRun}
	if err := rebindValidate(r); err != nil {
		return rep, err
	}
	rep.TomlPath = filepath.Join(r.Checkout, projectFileName)
	old, m, err := rebindReadMarker(rep.TomlPath, r.Checkout)
	if err != nil {
		return rep, err
	}
	anchors, err := rebindAnchorPlan(r)
	if err != nil {
		return rep, err
	}
	if r.Kind == RebindSplit {
		return rebindSplit(r, rep, m, anchors)
	}

	// Rename.
	if err := rebindRenamePreconditions(r, m); err != nil {
		return rep, err
	}
	var next string
	if m.Name == r.ToSlug {
		rep.TomlChange = "already rebound"
	} else {
		next, rep.TomlChange, err = rebindNameLine(string(old), r.FromSlug, r.ToSlug)
		if err != nil {
			return rep, err
		}
		if err := rebindPostcondition(string(old), next, r); err != nil {
			return rep, err
		}
	}
	hc, err := rebindHostConfigPlan(r.FromSlug, r.ToSlug)
	if err != nil {
		return rep, err
	}
	rep.HostConfig = hc
	bm, err := rebindBindingPlan(r)
	if err != nil {
		return rep, err
	}
	rep.BindingChange = bm.change

	// Report-only work: nothing below changes the checkout.
	rep.GrepHits, rep.GrepTotal, rep.Warnings = rebindGrep(r, "")
	rep.Warnings = append(rep.Warnings, rebindQueuedJobs(r.Checkout, r.FromSlug, r.Kind)...)
	rep.GitCommands = rebindGitCommands(r)

	if r.DryRun {
		if rep.HostConfig != nil && rep.HostConfig.Action == "moved" {
			rep.HostConfig.Action = "would-move"
		}
		rep.Anchors = rebindDryAnchors(anchors)
		return rep, nil
	}

	// Writes, in order: the host's [project_vaults].<to> key, the toml, the
	// host-local project config, the anchors. A failure before the anchors
	// undoes what was already written — each undo a compare-and-set against
	// the bytes this call wrote, so another writer's change is never
	// overwritten with an older pre-image.
	var written []string
	var cfgBackup string
	if bm.add {
		if err := casWriteHostConfig(bm.cfgPath, bm.old, []byte(bm.next), &cfgBackup); err != nil {
			return rep, fmt.Errorf("refusing: %v", err)
		}
		written = append(written, fmt.Sprintf("%s (added [%s].%s; pre-image %s)", bm.cfgPath, projectVaultsKey, r.ToSlug, cfgBackup))
	}
	undoCfg := func(cause error) error {
		if !bm.add {
			return cause
		}
		if rerr := restoreHostLocalCAS(bm.cfgPath, []byte(bm.next), bm.old); rerr != nil {
			return fmt.Errorf("%w; RESTORING %s ALSO FAILED: %v — its pre-image is %s", cause, bm.cfgPath, rerr, cfgBackup)
		}
		return fmt.Errorf("%w; %s was restored to its previous bytes", cause, bm.cfgPath)
	}
	if next != "" {
		bak, err := WriteHostLocalWithBackup(rep.TomlPath, old, []byte(next))
		if err != nil {
			return rep, undoCfg(fmt.Errorf("rewrite %s: %w", rep.TomlPath, err))
		}
		rep.BackupPath = bak
		written = append(written, fmt.Sprintf("%s (name %s -> %s; pre-image %s)", rep.TomlPath, r.FromSlug, r.ToSlug, bak))
	}
	undoToml := func(cause error) error {
		if next != "" {
			if rerr := restoreHostLocalCAS(rep.TomlPath, []byte(next), old); rerr != nil {
				cause = fmt.Errorf("%w; restoring %s also failed: %v — its pre-image is %s", cause, rep.TomlPath, rerr, rep.BackupPath)
			}
		}
		return undoCfg(cause)
	}
	movedHost := false
	if rep.HostConfig != nil && rep.HostConfig.Action == "moved" {
		if err := os.Rename(rep.HostConfig.From, rep.HostConfig.To); err != nil {
			return rep, undoToml(fmt.Errorf("move host project config: %w", err))
		}
		movedHost = true
		written = append(written, fmt.Sprintf("%s (moved from %s)", rep.HostConfig.To, rep.HostConfig.From))
	}
	if bm.verify {
		res, err := ResolveVaultBinding(r.Checkout)
		want := "binding:" + bm.cfgPath + "#" + r.ToSlug
		if err != nil || res.Source != want || !sameVaultRoot(res.Path, r.VaultRoot) {
			cause := fmt.Errorf("refusing: after the rename the checkout resolves %q (source %q, err %v), not %s through the binding",
				res.Path, res.Source, err, r.VaultRoot)
			if movedHost {
				if rerr := os.Rename(rep.HostConfig.To, rep.HostConfig.From); rerr != nil {
					cause = fmt.Errorf("%w; moving %s back to %s also failed: %v", cause, rep.HostConfig.To, rep.HostConfig.From, rerr)
				}
			}
			return rep, undoToml(cause)
		}
	}
	copied, err := rebindApplyAnchors(anchors)
	if err != nil {
		// The rename itself is complete and consistent; only the anchor copy
		// failed. Nothing is undone — say exactly what is now on disk.
		written = append(written, copied...)
		return rep, fmt.Errorf("%w; the rename is otherwise complete, and these were written and NOT undone: %s",
			err, strings.Join(written, "; "))
	}
	rep.Anchors = anchors
	return rep, nil
}

// rebindReadMarker reads the checkout root's own marker (no upward walk, so a
// parent project's marker is never edited) through the shared reader, and
// refuses unless it is also the marker the resolver's walk finds.
func rebindReadMarker(tomlPath, checkout string) ([]byte, project.Marker, error) {
	old, err := os.ReadFile(tomlPath)
	if err != nil {
		return nil, project.Marker{}, fmt.Errorf("refusing: %s: %w (the checkout root must hold its own .vibe-palace.toml)", tomlPath, err)
	}
	m, err := project.ReadMarker(tomlPath)
	switch {
	case err != nil:
		return nil, m, fmt.Errorf("refusing: %s: %w", tomlPath, err)
	case m.Name == "":
		return nil, m, fmt.Errorf("refusing: %s names no [project].name", tomlPath)
	case m.NameErr != nil:
		return nil, m, fmt.Errorf("refusing: %s: [project].name %q: %w", tomlPath, m.Name, m.NameErr)
	}
	found, _ := project.FindMarker(checkout)
	resolved, rerr := filepath.EvalSymlinks(tomlPath)
	if rerr != nil || found != filepath.Clean(resolved) {
		return nil, m, fmt.Errorf("refusing: from %s the resolver reads %q, not %s; rebind from the directory whose marker binds the vault",
			checkout, found, tomlPath)
	}
	return old, m, nil
}

// rebindSplit binds the checkout's project to the vault it moved to. The
// checkout itself is not written.
func rebindSplit(r CheckoutRebind, rep CheckoutRebindReport, m project.Marker, anchors []RebindFileAction) (CheckoutRebindReport, error) {
	if m.Name != r.FromSlug {
		return rep, fmt.Errorf("refusing: the checkout names project %q, not %q", m.Name, r.FromSlug)
	}
	br, err := BindProjectVault(BindRequest{
		Slug: r.FromSlug, VaultPath: r.VaultPath, Mode: BindMoved,
		Checkouts: []string{r.Checkout}, DryRun: r.DryRun,
	})
	rep.Binding = &br
	if err != nil {
		return rep, err
	}
	rep.TomlChange = "unchanged (bound in " + br.ConfigPath + ")"
	rep.GrepHits, rep.GrepTotal, rep.Warnings = br.GrepHits, br.GrepTotal, br.Warnings
	if r.DryRun {
		rep.Anchors = rebindDryAnchors(anchors)
		return rep, nil
	}
	if _, err := rebindApplyAnchors(anchors); err != nil {
		return rep, fmt.Errorf("%w; the bind itself is complete (%s)", err, br.ConfigPath)
	}
	rep.Anchors = anchors
	return rep, nil
}

// rebindBinding is a rename's planned [project_vaults] change.
type rebindBinding struct {
	// add is true when <to> must be added; verify is true whenever <to> is
	// bound after the rename (added now, or already), so the renamed checkout
	// must resolve through it.
	add     bool
	verify  bool
	cfgPath string
	old     []byte
	next    string
	change  string
}

// rebindBindingPlan plans ADDING [project_vaults].<to> with <from>'s value, so
// the renamed checkout keeps resolving the vault it was bound to.
//
// 🔴 <from> IS NEVER REMOVED. Every other checkout on this host that still
// names <from> — another worktree, a second clone — resolves through that key;
// removing it would drop them to the global vault, where their first capture
// would create Projects/<from> in the live vault. They keep resolving the bound
// vault, whose `renamed` departure record then refuses <from> and redirects to
// <to>. For the same reason a crash between this write and the toml write
// strands nothing.
//
// A bound <from> must point at the vault the rename landed in, and an existing
// <to> must agree with it; anything else refuses before any write. The
// bindings are parsed from the same bytes the splice edits and the
// compare-and-set compares against.
func rebindBindingPlan(r CheckoutRebind) (rebindBinding, error) {
	bindings, cfgPath, old, err := readProjectVaultsBytes()
	if err != nil {
		return rebindBinding{}, fmt.Errorf("refusing: the host config cannot be used: %w", err)
	}
	fromVal, fromOK := bindings[r.FromSlug]
	toVal, toOK := bindings[r.ToSlug]
	if !fromOK {
		if toOK {
			root, berr := boundVaultRoot(cfgPath, r.ToSlug, toVal)
			if berr != nil || !sameVaultRoot(root, r.VaultRoot) {
				return rebindBinding{}, fmt.Errorf("refusing: %s binds %q to %q, not to %s where the rename landed", cfgPath, r.ToSlug, toVal, r.VaultRoot)
			}
			return rebindBinding{verify: true, cfgPath: cfgPath}, nil
		}
		return rebindBinding{}, nil
	}
	fromRoot, err := boundVaultRoot(cfgPath, r.FromSlug, fromVal)
	if err != nil {
		return rebindBinding{}, fmt.Errorf("refusing: %w", err)
	}
	if !sameVaultRoot(fromRoot, r.VaultRoot) {
		return rebindBinding{}, fmt.Errorf("refusing: %s binds %q to %s, but the rename landed in %s", cfgPath, r.FromSlug, fromRoot, r.VaultRoot)
	}
	if toOK {
		if toRoot, berr := boundVaultRoot(cfgPath, r.ToSlug, toVal); berr != nil || !sameVaultRoot(toRoot, fromRoot) {
			return rebindBinding{}, fmt.Errorf("refusing: %s already binds %q to %q, which is not %s; merge the two by hand", cfgPath, r.ToSlug, toVal, fromRoot)
		}
		return rebindBinding{verify: true, cfgPath: cfgPath}, nil
	}
	next, _, err := spliceProjectVault(string(old), r.ToSlug, fromVal)
	if err != nil {
		return rebindBinding{}, fmt.Errorf("refusing: %w", err)
	}
	if err := projectVaultsPostcondition(string(old), next, map[string]string{r.ToSlug: fromVal}); err != nil {
		return rebindBinding{}, fmt.Errorf("refusing: %w", err)
	}
	return rebindBinding{
		add: true, verify: true, cfgPath: cfgPath, old: old, next: next,
		change: fmt.Sprintf("[%s] + %s = %q (%s kept, for checkouts that still name it)", projectVaultsKey, r.ToSlug, fromVal, r.FromSlug),
	}, nil
}

// rebindDryAnchors reports the anchor plan as it would run.
func rebindDryAnchors(anchors []RebindFileAction) []RebindFileAction {
	for i := range anchors {
		if anchors[i].Action == "copied" {
			anchors[i].Action = "would-copy"
		}
	}
	return anchors
}

// rebindApplyAnchors copies the planned anchors, returning the ones it copied
// (so a failure part-way can say exactly what is on disk).
func rebindApplyAnchors(anchors []RebindFileAction) ([]string, error) {
	var copied []string
	for _, a := range anchors {
		if a.Action != "copied" {
			continue
		}
		data, err := os.ReadFile(a.From)
		if err != nil {
			return copied, fmt.Errorf("read anchor: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(a.To), 0o755); err != nil {
			return copied, fmt.Errorf("create anchor dir: %w", err)
		}
		if err := os.WriteFile(a.To, data, 0o644); err != nil {
			return copied, fmt.Errorf("write anchor: %w", err)
		}
		copied = append(copied, a.To+" (anchor)")
	}
	return copied, nil
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

func rebindRenamePreconditions(r CheckoutRebind, m project.Marker) error {
	if m.Name != r.FromSlug && m.Name != r.ToSlug {
		return fmt.Errorf("refusing: the checkout names project %q, neither %q nor %q", m.Name, r.FromSlug, r.ToSlug)
	}
	if st, err := os.Stat(filepath.Join(r.VaultRoot, "Projects", r.ToSlug)); err != nil || !st.IsDir() {
		return fmt.Errorf("refusing: %s has no Projects/%s: rebind the checkout only after the vault rename has landed, "+
			"or its marker names a project the vault does not hold", r.VaultRoot, r.ToSlug)
	}
	if _, err := os.Lstat(filepath.Join(r.VaultRoot, "Projects", r.FromSlug)); err == nil {
		// A directory that survived a pull holding only ignored residue is a
		// LANDED rename, not a pending one: departure.Find says which.
		if _, departed := departure.Find(r.VaultRoot, r.FromSlug); !departed {
			return fmt.Errorf("refusing: %s still has Projects/%s: the vault rename has not landed", r.VaultRoot, r.FromSlug)
		}
	}
	return nil
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

// rebindPostcondition refuses, before any write, unless a rename's new text
// differs from the old in exactly [project].name, with the intended value.
func rebindPostcondition(old, next string, r CheckoutRebind) error {
	var a, b map[string]any
	if _, err := toml.Decode(old, &a); err != nil {
		return fmt.Errorf("refusing: parse original: %w", err)
	}
	if _, err := toml.Decode(next, &b); err != nil {
		return fmt.Errorf("refusing: the edit would not parse: %w", err)
	}
	pa, _ := a["project"].(map[string]any)
	pb, _ := b["project"].(map[string]any)
	if pa == nil || pb == nil {
		return fmt.Errorf("refusing: no [project] table")
	}
	// Read the name the way project.ReadMarker does — from the generic decode —
	// so a wrong-typed key elsewhere in [project] cannot fail a rename.
	if name, _ := pb["name"].(string); name != r.ToSlug {
		return fmt.Errorf("refusing: the edit would name %v, not %q", pb["name"], r.ToSlug)
	}
	delete(pa, "name")
	delete(pb, "name")
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

// rebindGitCommands are printed for the operator and never run. Only a rename
// has any: a split changes nothing in the repository, and rebindSplit never
// asks for them.
func rebindGitCommands(r CheckoutRebind) []string {
	msg := fmt.Sprintf("Rebind this checkout to vibe-palace project %s", r.ToSlug)
	return []string{
		fmt.Sprintf("git -C %s add -- .vibe-palace.toml", r.Checkout),
		fmt.Sprintf("git -C %s commit -m %q", r.Checkout, msg),
		fmt.Sprintf("git -C %s push    # after the vault side is pushed", r.Checkout),
	}
}
