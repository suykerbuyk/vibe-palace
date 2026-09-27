// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// BindMode says why a project is being bound to a vault other than the host
// default (ADR-012).
type BindMode string

const (
	// BindMoved binds a project that was split out of the host's default
	// vault: the default vault must hold a moved-to-vault departure record for
	// it, and the target must hold it.
	BindMoved BindMode = "moved"
	// BindNew binds a project born in another vault: the default vault must
	// never have held it. The target need not hold it yet (`vp init` follows).
	BindNew BindMode = "new"
)

// BindRequest is one `vp config bind` / `vp_config_bind` call.
type BindRequest struct {
	Slug string
	// VaultPath is written into [project_vaults] exactly as given; it must be
	// absolute after ~ expansion (boundVaultRoot).
	VaultPath string
	Mode      BindMode
	// Checkouts are checkout roots whose resolution the bind must change; each
	// is verified after the write, and a failure restores the config.
	Checkouts []string
	// AllowUnlabelled accepts a moved-to-vault record that names no
	// destination, which otherwise refuses (nothing to match the target
	// against).
	AllowUnlabelled bool
	DryRun          bool
}

// BindCheckout is how one checkout resolves after the bind.
type BindCheckout struct {
	Checkout string `json:"checkout"`
	Resolved string `json:"resolved,omitempty"`
	Source   string `json:"source,omitempty"`
}

// BindReport is everything a bind did or would do.
type BindReport struct {
	ConfigPath string          `json:"config_path"`
	Change     string          `json:"change"`
	BackupPath string          `json:"backup_path,omitempty"`
	Vault      string          `json:"vault"`
	Checkouts  []BindCheckout  `json:"checkouts,omitempty"`
	GrepHits   []RebindGrepHit `json:"grep_hits,omitempty"`
	GrepTotal  int             `json:"grep_total"`
	Warnings   []string        `json:"warnings,omitempty"`
	DryRun     bool            `json:"dry_run"`
}

// bindBeforeWrite runs between computing the new config and the
// compare-and-set re-read; a test replaces it to change the file under the
// writer.
var bindBeforeWrite = func() {}

// bindBeforeVerify runs between the write and its verification; a test
// replaces it to change the file after the bind wrote it.
var bindBeforeVerify = func() {}

// BindProjectVault binds a project to a vault on THIS host: one
// `[project_vaults].<slug>` line in the global config (ADR-012, tier 2). It
// is the one writer of that table, behind both `vp config bind` and the MCP
// tool vp_config_bind, and the split kind of RebindCheckout.
//
// Every precondition is checked before the first write. It never creates the
// global config, never writes a checkout, never re-points an existing binding
// (a mistaken bind is corrected by hand), and never takes a vaultlock:
// host-local writes take none (WriteHostLocalWithBackup), and `vp config
// upgrade` does not lock either, so a lock here would exclude nothing. The
// write is a compare-and-set on the file's bytes instead, and it is verified
// through ResolveVaultBinding from every named checkout — any failure restores
// the file byte-for-byte.
func BindProjectVault(req BindRequest) (BindReport, error) {
	rep := BindReport{DryRun: req.DryRun}
	refuse := func(format string, a ...any) (BindReport, error) {
		return rep, fmt.Errorf("refusing to bind project %q: %s", req.Slug, fmt.Sprintf(format, a...))
	}
	if err := slug.Validate(req.Slug); err != nil {
		return refuse("%v", err)
	}
	if req.Mode != BindMoved && req.Mode != BindNew {
		return refuse("mode %q is neither %q nor %q", req.Mode, BindMoved, BindNew)
	}

	// ONE read: the bindings the checks below use are parsed from exactly the
	// bytes the splice edits and the compare-and-set compares against, so a
	// concurrent writer cannot slip a binding in between the check and the
	// write.
	bindings, cfgPath, old, err := readProjectVaultsBytes()
	if err != nil {
		return refuse("the host config cannot be used: %v", err)
	}
	rep.ConfigPath = cfgPath
	if cfgPath == "" {
		return refuse("the host config directory cannot be resolved")
	}
	if old == nil {
		return refuse("there is no global config at %s; run `vp init` first — a bind never creates it", cfgPath)
	}
	target, err := boundVaultRoot(cfgPath, req.Slug, req.VaultPath)
	if err != nil {
		return refuse("%v", err)
	}
	rep.Vault = target
	if format, ferr := surface.ReadFormat(target); ferr != nil {
		return refuse("%s is not a readable vault: %v", target, ferr)
	} else if format != surface.RequiredDataFormat {
		return refuse("%s is at data format %d; this binary requires %d", target, format, surface.RequiredDataFormat)
	}
	global, _, err := ResolveGlobalVaultPath()
	if err != nil {
		return refuse("the host default vault cannot be resolved: %v", err)
	}
	if sameVaultRoot(global, target) {
		return refuse("%s is this host's default vault (vault_path); a binding names a DIFFERENT vault", target)
	}
	if _, departed := departure.Find(target, req.Slug); departed {
		return refuse("%s records %q as departed from it; bind the project where it lives now", target, req.Slug)
	}

	switch req.Mode {
	case BindMoved:
		rec, departed := departure.Find(global, req.Slug)
		switch {
		case !departed:
			return refuse("the default vault %s holds no departure record for it: the split has not landed there "+
				"(pull first), or the project never left (use mode %q for a project born elsewhere)", global, BindNew)
		case rec.Malformed != "":
			return refuse("its departure record in %s cannot be read (%s)", global, rec.Malformed)
		case rec.Kind != departure.MovedToVault:
			return refuse("it was %s in %s, not moved to another vault", rec.Kind, global)
		}
		if !vaultHoldsProject(target, req.Slug) {
			return refuse("%s holds neither palace/%s nor Projects/%s", target, req.Slug, req.Slug)
		}
		if rec.To == "" {
			if !req.AllowUnlabelled {
				return refuse("its departure record names no destination, so nothing proves %s is where it went; "+
					"pass allow_unlabelled to bind anyway", target)
			}
			rep.Warnings = append(rep.Warnings, "the departure record names no destination; bound without checking")
		} else {
			want, ok := normaliseRemoteURL(rec.To)
			if !ok {
				return refuse("its departure record names %q, which is not a git URL, so no remote of %s can be matched "+
					"against it; bind it by hand-editing [%s] in %s", rec.To, target, projectVaultsKey, cfgPath)
			}
			urls, uerr := VaultRemoteURLs(target)
			if uerr != nil {
				return refuse("%v", uerr)
			}
			if !slices.ContainsFunc(urls, func(u string) bool { n, ok := normaliseRemoteURL(u); return ok && n == want }) {
				return refuse("it moved to %q, and no remote of %s is that repository (remotes: %v)", rec.To, target, urls)
			}
		}
	case BindNew:
		for _, tree := range ProjectTrees(req.Slug) {
			if _, lerr := os.Lstat(filepath.Join(global, filepath.FromSlash(tree))); lerr == nil {
				return refuse("the default vault %s has %s: it is not a project born elsewhere", global, tree)
			}
		}
		if _, recorded := departure.Read(global, req.Slug); recorded {
			return refuse("the default vault %s records a departure for it; use mode %q", global, BindMoved)
		}
	}

	already := false
	if cur, ok := bindings[req.Slug]; ok {
		curRoot, cerr := boundVaultRoot(cfgPath, req.Slug, cur)
		if cerr != nil || !sameVaultRoot(curRoot, target) {
			return refuse("%s already binds it to %q; re-pointing a binding is refused — edit the config by hand "+
				"if the old binding is wrong", cfgPath, cur)
		}
		already = true
		rep.Change = "already bound"
	}

	for _, c := range req.Checkouts {
		if !filepath.IsAbs(c) {
			return refuse("checkout %q is not an absolute path", c)
		}
		m, merr := project.LocateMarker(c)
		switch {
		case merr != nil:
			return refuse("checkout %s: its marker cannot be read: %v", c, merr)
		case m.Path == "":
			return refuse("checkout %s has no .vibe-palace.toml", c)
		case m.Name != req.Slug:
			return refuse("checkout %s names project %q (%s), not %q", c, m.Name, m.Path, req.Slug)
		}
		hits, total, warns := rebindGrep(CheckoutRebind{Kind: RebindSplit, Checkout: c, FromSlug: req.Slug}, global)
		rep.GrepHits = append(rep.GrepHits, hits...)
		rep.GrepTotal += total
		rep.Warnings = append(rep.Warnings, warns...)
	}

	if already {
		if !req.DryRun {
			if err := verifyBind(&rep, cfgPath, req, target); err != nil {
				return rep, err
			}
		}
		return rep, nil
	}

	next, change, err := spliceProjectVault(string(old), req.Slug, req.VaultPath)
	if err != nil {
		return refuse("%v", err)
	}
	if err := projectVaultsPostcondition(string(old), next, map[string]string{req.Slug: req.VaultPath}); err != nil {
		return refuse("%v", err)
	}
	rep.Change = change
	if req.DryRun {
		return rep, nil
	}

	bindBeforeWrite()
	if err := casWriteHostConfig(cfgPath, old, []byte(next), &rep.BackupPath); err != nil {
		return refuse("%v", err)
	}
	bindBeforeVerify()
	if err := verifyBind(&rep, cfgPath, req, target); err != nil {
		if rerr := restoreHostLocalCAS(cfgPath, []byte(next), old); rerr != nil {
			return rep, fmt.Errorf("%w; RESTORING %s ALSO FAILED: %v — its pre-image is %s", err, cfgPath, rerr, rep.BackupPath)
		}
		return rep, fmt.Errorf("%w; %s was restored to its previous bytes", err, cfgPath)
	}
	rep.Warnings = append(rep.Warnings, "a running MCP server resolved its vault at startup and will refuse writes "+
		"(StaleBindingError) until your AI host reloads it")
	return rep, nil
}

// verifyBind proves the binding took, through the resolver itself.
func verifyBind(rep *BindReport, cfgPath string, req BindRequest, target string) error {
	wantSource := "binding:" + cfgPath + "#" + req.Slug
	if len(req.Checkouts) == 0 {
		bindings, _, err := readProjectVaults()
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		got, ok := bindings[req.Slug]
		if !ok {
			return fmt.Errorf("verify: %s has no [project_vaults] entry for %q after the write", cfgPath, req.Slug)
		}
		if root, err := boundVaultRoot(cfgPath, req.Slug, got); err != nil || !sameVaultRoot(root, target) {
			return fmt.Errorf("verify: the written entry does not resolve to %s (err %v)", target, err)
		}
		return nil
	}
	rep.Checkouts = rep.Checkouts[:0]
	for _, c := range req.Checkouts {
		res, err := ResolveVaultBinding(c)
		if err != nil {
			return fmt.Errorf("verify: checkout %s does not resolve after the bind: %w", c, err)
		}
		rep.Checkouts = append(rep.Checkouts, BindCheckout{Checkout: c, Resolved: res.Path, Source: res.Source})
		if res.Source != wantSource || !sameVaultRoot(res.Path, target) {
			return fmt.Errorf("verify: checkout %s resolves %s (source %s), not %s through the binding", c, res.Path, res.Source, target)
		}
	}
	return nil
}

// vaultHoldsProject reports whether vault has either of slug's trees.
func vaultHoldsProject(vault, slug string) bool {
	for _, tree := range ProjectTrees(slug) {
		if st, err := os.Stat(filepath.Join(vault, filepath.FromSlash(tree))); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// casWriteHostConfig replaces a host-local config only if it still holds the
// bytes the caller computed from (a compare-and-set on bytes), keeping a .bak.
func casWriteHostConfig(path string, old, next []byte, backup *string) error {
	cur, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("re-read %s: %v", path, err)
	}
	if !bytes.Equal(cur, old) {
		return fmt.Errorf("%s changed while the write was being prepared; nothing was written — re-run", path)
	}
	bak, err := WriteHostLocalWithBackup(path, old, next)
	if err != nil {
		return fmt.Errorf("write %s: %v", path, err)
	}
	*backup = bak
	return nil
}

// spliceProjectVault ADDS [project_vaults].<key> = "<value>" and changes no
// other byte: it inserts the line after the last key of an existing
// [project_vaults] section, else appends the table at the END of the file —
// where its header cannot swallow top-level keys.
//
// It never re-points: a line already naming key refuses, whatever its value,
// so a binding that appeared since the caller's checks cannot be overwritten
// by the splice itself. A table written without a [project_vaults] header (the
// dotted `project_vaults.x = …` form, or an inline table) refuses too:
// appending a header for a table already defined is invalid TOML.
func spliceProjectVault(text, key, value string) (string, string, error) {
	lines := strings.Split(text, "\n")
	newLine := key + " = " + rebindQuote(value)
	for _, sr := range FindSectionRanges(text) {
		if sr.Name != projectVaultsKey {
			continue
		}
		insertAt := sr.StartLine + 1
		for i := sr.StartLine + 1; i < sr.EndLine && i < len(lines); i++ {
			k, ok := parseKeyAssignment(trimLeftSpace(lines[i]), false)
			if !ok {
				continue
			}
			insertAt = i + 1
			if k == key {
				return "", "", fmt.Errorf("line %d %q already binds %s; re-pointing a binding is refused — edit it by hand", i+1, lines[i], key)
			}
		}
		lines = slices.Insert(lines, insertAt, newLine)
		return strings.Join(lines, "\n"), "+ " + newLine, nil
	}
	var top map[string]any
	if _, err := toml.Decode(text, &top); err == nil {
		if _, defined := top[projectVaultsKey]; defined {
			return "", "", fmt.Errorf("[%s] is written without a [%s] header (dotted keys or an inline table); "+
				"a header cannot be added to it — edit it by hand", projectVaultsKey, projectVaultsKey)
		}
	}
	out := text
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += "\n[" + projectVaultsKey + "]\n" + newLine + "\n"
	return out, "+ [" + projectVaultsKey + "]\n+ " + newLine, nil
}

// projectVaultsPostcondition refuses, before any write, unless next differs
// from old in exactly the [project_vaults] keys named in want: a key mapped to
// a value must hold it, a key mapped to "" must be absent. Every other key of
// the whole document must decode identically.
func projectVaultsPostcondition(old, next string, want map[string]string) error {
	var a, b map[string]any
	if _, err := toml.Decode(old, &a); err != nil {
		return fmt.Errorf("parse the current config: %w", err)
	}
	if _, err := toml.Decode(next, &b); err != nil {
		return fmt.Errorf("the edit would not parse: %w", err)
	}
	ta, _ := a[projectVaultsKey].(map[string]any)
	tb, _ := b[projectVaultsKey].(map[string]any)
	for k, v := range want {
		got, present := tb[k]
		switch {
		case v == "" && present:
			return fmt.Errorf("the edit would leave [%s].%s", projectVaultsKey, k)
		case v != "" && got != v:
			return fmt.Errorf("the edit would set [%s].%s to %v, not %q", projectVaultsKey, k, got, v)
		}
		delete(ta, k)
		delete(tb, k)
	}
	if len(ta) == 0 {
		delete(a, projectVaultsKey)
	}
	if len(tb) == 0 {
		delete(b, projectVaultsKey)
	}
	if !reflect.DeepEqual(a, b) {
		return errors.New("the edit would change more than the binding it sets")
	}
	return nil
}
