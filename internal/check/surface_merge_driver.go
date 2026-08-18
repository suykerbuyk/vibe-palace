// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The vault must not name a merge driver that does not ship.
//
// vp used to install a `vp-surface` merge driver and write
// `*.surface merge=vp-surface` into the vault's `.gitattributes`, to resolve
// conflicts between per-host surface stamps. At 174 the stamps were made
// byte-stable, which deleted the conflict class outright, and the whole
// apparatus went with it: the driver, the `vp vault merge-driver` subcommand,
// its auto-installer, and the `--no-install-merge-driver` opt-out on
// `vault pull` / `vault sync` (doc/TESTING.md records the removal).
//
// What did not go away is the failure mode of putting the attribute back.
// **git emits no diagnostic for an attribute naming an undefined driver.** It
// silently falls back to a text merge, so the damage arrives looking like a
// clean merge — the worst failure shape available, and the same one the iter
// 188 stale-path bug had.
//
// # Why this does not consult git config
//
// The obvious extension is to ask whether a `merge.vp-surface.driver` is
// configured and only fail when it is absent. It is refused, for the reason
// CheckVaultAbsPaths refuses to expand $HOME: a check whose verdict depends on
// WHICH host ran it is the bug class these checks exist to detect. Two facts
// make the config read worthless anyway:
//
//   - **vp ships no driver at all.** Whether some host has an entry is
//     irrelevant to whether the command it names exists — and it does not, in
//     any binary built from this tree. A config entry pointing at a deleted
//     subcommand binds nothing; it is stale config, not a live driver, and
//     reporting it as "defined" would be a false green.
//   - **git config is host-local; this vault is synced to every machine.** Even
//     a genuinely working driver on one box is absent on the others, so an
//     attribute relying on it is broken by construction everywhere else the
//     vault lands.
//
// So the condition "and no merge driver is defined" is constant, not measured,
// and this check never reads — let alone echoes — a host's git configuration.
// Write the CONSTRAINT, never the PATH.
//
// # Scope
//
// Tracked `.gitattributes` files anywhere in the vault tree. `.git/` is skipped
// deliberately: `.git/info/attributes` is host-local and unsynced, so flagging
// it would reintroduce exactly the host-dependence rejected above.
const surfaceMergeDriverName = "vp-surface"

// surfaceMergeDriverHazard is the need-to-know text. It is attached only to a
// failing row, so it costs nothing on the healthy path and is present at the
// only moment a reader needs it.
var surfaceMergeDriverHazard = []string{
	"vp ships NO merge driver. The driver, the `vp vault merge-driver` subcommand, its auto-installer and",
	"the `--no-install-merge-driver` opt-out were all DELETED at 174: `.surface` stamps are byte-stable, so",
	"the conflict class the driver resolved no longer exists.",
	"**git emits no diagnostic for an attribute naming an undefined driver** — it silently falls back to a",
	"TEXT merge, so a corrupted stamp arrives looking like a clean merge.",
	"A `merge.<name>.driver` entry left in some host's git config does not make this safe: it names the",
	"deleted subcommand, so it binds nothing — and git config is host-local while this vault is synced to",
	"every machine, so it could not cover the other hosts even if it did bind.",
	"REMEDY: delete the line from `.gitattributes`. Do not re-add the attribute without re-adding a driver",
	"that actually ships — and a driver that ships is a deliberate design decision, not a wrap-time fix.",
}

// surfaceMergeDriverRef is one offending line.
type surfaceMergeDriverRef struct {
	Rel   string // vault-relative path of the .gitattributes
	Line  int
	Token string // the offending attribute token, e.g. merge=vp-surface
	Patt  string // the pattern the attribute is attached to, e.g. *.surface
}

// CheckSurfaceMergeDriver fails when a `.gitattributes` in the vault names the
// deleted `vp-surface` merge driver.
func CheckSurfaceMergeDriver(v *storage.Vault) Result {
	r := Result{Name: "Surface merge driver"}

	if v == nil || v.Root == "" {
		r.Status = Skip
		r.Summary = "no vault configured"
		return r
	}

	var refs []surfaceMergeDriverRef
	files, dirs := 0, 0

	err := filepath.WalkDir(v.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree must not abort the walk: a partial scan
			// that says so is worth more than no scan at all.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			// .git is host-local (and enormous); see the scope note above.
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			// The root itself is NOT counted. WalkDir always yields it, so
			// counting it would make the dirs>0 guard below true by
			// construction — a guard that cannot fail, which is
			// indistinguishable from no guard at all.
			if path != v.Root {
				dirs++
			}
			return nil
		}
		if d.Name() != ".gitattributes" {
			return nil
		}
		files++
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(v.Root, path)
		if relErr != nil {
			rel = d.Name()
		}
		refs = append(refs, surfaceMergeDriverRefs(filepath.ToSlash(rel), data)...)
		return nil
	})
	if err != nil {
		r.Status = Info
		r.Summary = fmt.Sprintf("scan vault: %v", err)
		return r
	}

	// A walk that stops seeing the vault reports zero findings, which is
	// byte-identical to the healthy state — the 296 shape. The live vault has
	// no .gitattributes at all today, so "0 files" is the CORRECT answer and
	// cannot itself be the tripwire. The SUBDIRECTORY count is: a real vault
	// always has Projects/ under it, so zero means the walk never descended and
	// this row's silence is meaningless.
	if dirs == 0 {
		r.Status = Info
		r.Summary = "walk traversed no directories — verdict withheld"
		r.Details = []string{
			"The vault root resolved to something with no directories in it, so a \"no findings\" verdict",
			"here would be vacuous rather than clean. Nothing was inspected; nothing is claimed.",
		}
		return r
	}

	if len(refs) == 0 {
		r.Status = Pass
		r.Summary = fmt.Sprintf("%s named by none of %d .gitattributes (%d dirs walked)",
			surfaceMergeDriverName, files, dirs)
		return r
	}

	r.Status = Fail
	r.Summary = fmt.Sprintf("%d line(s) in %d .gitattributes name the deleted %s merge driver",
		len(refs), files, surfaceMergeDriverName)
	for _, ref := range refs {
		r.Details = append(r.Details,
			fmt.Sprintf("  %s:%s: pattern %q carries %q", ref.Rel, strconv.Itoa(ref.Line), ref.Patt, ref.Token))
	}
	r.Details = append(r.Details, surfaceMergeDriverHazard...)
	return r
}

// surfaceMergeDriverRefs parses one .gitattributes body and returns the lines
// naming the deleted driver.
//
// git's attribute syntax is `<pattern> <attr>...`, where a macro/driver binding
// is `merge=<name>`. Both the keyed form (`merge=vp-surface`) and a bare
// `vp-surface` token are matched: the bare form is not valid git, but it is
// exactly what a half-remembered re-add looks like, and a tripwire that stays
// silent on the malformed attempt is a tripwire that misses the realistic case.
func surfaceMergeDriverRefs(rel string, data []byte) []surfaceMergeDriverRef {
	var out []surfaceMergeDriverRef
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, tok := range fields[1:] {
			if tok == surfaceMergeDriverName ||
				strings.EqualFold(tok, "merge="+surfaceMergeDriverName) {
				out = append(out, surfaceMergeDriverRef{
					Rel: rel, Line: i + 1, Token: tok, Patt: fields[0],
				})
				break
			}
		}
	}
	return out
}
