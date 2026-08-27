// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/mcp"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// vp_vault_split moves a declared set of project slugs out of the bound vault
// and into a NEW standalone vault, so a mixed vault can be separated into
// publishable and proprietary halves. The design lives in the task
// `split-and-merge-standalone-vaults`; this file is SLICE 1 of it and
// implements exactly one action: `plan`.
//
// 🔴 THE SCHEMA ADVERTISES ONLY WHAT IS BUILT. The full design names four
// actions (plan / apply / verify / purge) and a sibling tool vp_vault_merge.
// None of the other three are here, so none of them appear in the action enum.
// A tool that lists an action it cannot perform is the honest-instruments
// defect in its purest form — the surface reporting a capability the code does
// not have — and it would also let a caller reach a `manifest_sha256` parameter
// nothing reads. Widening the enum is slice 2's job, and it moves
// schema_sha256, which is a golden regeneration and NOT an MCPSurfaceVersion
// bump.
//
// 🔴 REGISTERED Mutating: true THOUGH SLICE 1 WRITES NOTHING, refined to
// read-only per invocation by vaultSplitReadOnly. The declaration describes the
// TOOL, which will write as soon as `apply` lands; the predicate describes the
// CALL. Declaring it non-mutating now and remembering to flip it later is the
// order that gets forgotten, and the failure is silent — an ungated writer.
// Because the flag is forward-looking, the derived-gate analysis will report
// mcp.vp_vault_split as "declared gated, reaches NO funnel sink" until `apply`
// lands. That divergence is the same shape as the accepted mcp.vp_vault_sync
// entry and is expected here; it is a chair/operator ruling, not something this
// file silences.

// splitSubtractSet names the paths that are hashed by NOBODY and copied by
// NOBODY: {.surface, **/.local, .vp-locks, commit-log.anchor}.
//
// 🔴 THE MINUS-SET AND THE DO-NOT-COPY LIST ARE THE SAME SET, and that identity
// is the invariant. Leaving a path in the manifest while skipping its copy
// fails verify; copying it to satisfy the manifest lands bytes that are false
// in the destination. So there is one predicate, splitSubtracted, and both
// halves read it.
//
// Each entry is here for its own reason, not because it is gitignored — the
// canonical gitignore set (git.go) is a DIFFERENT set and is deliberately not
// consulted:
//
//   - .surface   — a stamp about the binary that last wrote THIS vault. A
//     destination inherits nothing from it and must stamp itself on first write.
//   - **/.local  — machine-local state: the vault-wide palace/.local and, the
//     one the gitignore line does NOT reach, the per-project
//     palace/{p}/.local/embed-cache. Caches rebuild; they never travel.
//   - .vp-locks  — host-local advisory write locks.
//   - commit-log.anchor — the SHA of a source-vault commit. A fresh destination
//     has no such commit, so the value is a dangling reference on arrival.
//     commit-log.md itself DOES travel; only the anchor is false there.
var splitSubtractSet = []string{".surface", "**/.local", ".vp-locks", "commit-log.anchor"}

// splitPrunedDirs are the subtract-set entries that are DIRECTORIES, and are
// pruned rather than merely skipped.
//
// The distinction matters for the non-regular refusal below. Pruning means the
// walk never descends, so a symlink inside palace/{p}/.local cannot refuse a
// plan — which is correct: that tree is machine-local and is not part of the
// split at all, so its contents are not this tool's business. Everything else
// under an allow-listed tree IS scanned, and a non-regular file there refuses,
// even when its own path would later be subtracted from the hash.
var splitPrunedDirs = map[string]bool{".local": true, ".vp-locks": true}

// splitSubtracted reports whether a vault-relative (slash-separated) path is in
// the subtract set — hashed by nobody, copied by nobody.
func splitSubtracted(rel string) bool {
	base := splitPathBase(rel)
	if base == ".surface" || base == "commit-log.anchor" {
		return true
	}
	for _, comp := range strings.Split(rel, "/") {
		if splitPrunedDirs[comp] {
			return true
		}
	}
	return false
}

// splitPathBase is filepath.Base for an already-slash-separated vault-relative
// path. filepath.Base is separator-dependent, and every path in the manifest is
// canonically slash-separated so the digest is identical on every platform.
func splitPathBase(rel string) string {
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

// splitEntry is one file that will travel: its vault-relative slash path, the
// sha256 of its content, and its size.
type splitEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// splitTreeReport is the per-tree shape of the manifest: how many files travel
// from palace/{slug} or Projects/{slug}, how many bytes, and how many paths the
// subtract set removed.
type splitTreeReport struct {
	Tree       string `json:"tree"`
	Present    bool   `json:"present"`
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	Subtracted int    `json:"subtracted"`
}

// splitGlobalReport is one vault-global artifact class, with its size, and
// whether it travels. Vault-global artifacts do not partition by slug — they
// are the leak surface — so plan reports every class explicitly rather than
// letting an operator infer what was left behind.
type splitGlobalReport struct {
	Class      string `json:"class"`
	Path       string `json:"path"`
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	NonRegular int    `json:"non_regular"`
	Included   bool   `json:"included"`
	Note       string `json:"note,omitempty"`
}

// splitDriftReport is one slug present in exactly one of the two trees.
type splitDriftReport struct {
	Slug       string `json:"slug"`
	InPalace   bool   `json:"in_palace"`
	InProjects bool   `json:"in_projects"`
	Requested  bool   `json:"requested"`
}

// splitManifest is the server-side plan artifact. Entries are the bytes that
// travel; ManifestSHA256 binds both them and the REQUEST that selected them.
type splitManifest struct {
	Slugs            []string
	IncludeLearnings bool
	IncludeAudits    bool
	Entries          []splitEntry
	Trees            []splitTreeReport
	Global           []splitGlobalReport
	Drift            []splitDriftReport
	SHA256           string
}

// splitManifestFormat tags the digest preimage. A change to how the manifest is
// canonicalised must change every digest it produces, or an old plan's
// manifest_sha256 would silently validate against a new plan's bytes.
const splitManifestFormat = "vp_vault_split/manifest/1"

type vaultSplitParams struct {
	Action           string   `json:"action"`
	Slugs            []string `json:"slugs"`
	Destination      string   `json:"destination"`
	IncludeLearnings bool     `json:"include_learnings"`
	IncludeAudits    bool     `json:"include_audits"`
}

var vaultSplitSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "enum": ["plan"], "description": "Action. Only \"plan\" exists today: it reads the source vault and returns a manifest digest, writing nothing. apply/verify/purge are designed but not implemented, and are deliberately absent from this enum rather than present and refused."},
		"slugs": {"type": "array", "items": {"type": "string"}, "description": "Allow-list of project slugs to split out. Inclusion is an allow-list, never \"everything except\": there is no exclude parameter and there will not be one. An unknown slug is a refusal, not an empty selection."},
		"destination": {"type": "string", "description": "Absolute host path of the new standalone vault. It must not resolve inside the bound vault. plan does not create it; it validates it here so a later apply cannot start against a destination that was always going to be refused."},
		"include_learnings": {"type": "boolean", "description": "Include Knowledge/learnings/*.md in the manifest. Default false. Learnings carry no project field, so copy-none is the fail-closed default."},
		"include_audits": {"type": "boolean", "description": "Include Audits/ in the manifest. Default false. Audit reports name slugs across the whole vault."}
	},
	"required": ["action", "slugs", "destination"]
}`)

// vaultSplitReadOnly admits action:"plan", which reads the source vault and
// returns a digest.
//
// It is written against the same params struct the handler decodes, so a field
// rename breaks the compile rather than quietly flipping the answer, and it
// names the ONE value it can prove writes nothing. Today that value is the only
// one the schema admits, which makes the predicate look redundant — it is not.
// It is the half that must already be in place when `apply` widens the enum,
// because the order "add the writing action, then remember the predicate" is
// the order in which the predicate is forgotten, and forgetting it here fails
// in the safe direction (a refused plan) while forgetting it there fails in the
// unsafe one.
var vaultSplitReadOnly = readOnlyIf(func(p vaultSplitParams) bool {
	return p.Action == "plan"
})

// VaultSplitTool registers vp_vault_split.
func VaultSplitTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_split",
		Mutating: true,
		// action:"plan" reads and hashes; it creates nothing — see
		// vaultSplitReadOnly for why the predicate exists before it is needed.
		ReadOnlyWhen: vaultSplitReadOnly,
		Description: "Plan the split of a declared set of project slugs out of " +
			"the bound vault into a new standalone vault. ONLY action \"plan\" is " +
			"implemented: it verifies the source vault's data format, walks the " +
			"allow-listed palace/<slug> and Projects/<slug> trees, refuses any " +
			"non-regular file it finds there rather than skipping or following " +
			"it, hashes what would travel minus {.surface, **/.local, .vp-locks, " +
			"commit-log.anchor}, reports every vault-global artifact left behind " +
			"and every slug present in only one of the two trees, and returns a " +
			"manifest_sha256. It writes nothing, creates no destination, and " +
			"never mutates the source. Inclusion is an allow-list: an unknown " +
			"slug is a refusal and there is no exclude parameter.",
		Schema:  vaultSplitSchema,
		Handler: vaultSplitHandler(vault),
	}
}

// vaultSplitPlanResult is the payload. It reports the manifest's SHAPE and its
// digest, never its rows.
//
// 🔴 THE PER-FILE LIST IS DELIBERATELY NOT IN THE PAYLOAD. A real vault's split
// runs to thousands of files; emitting them would produce a result the host
// truncates, and a truncated manifest is worse than none because the digest
// beside it still looks authoritative. The rows are a server-side artifact:
// `apply` re-hashes the source and binds by manifest_sha256, so no caller ever
// needs to carry them. What a human needs to rule on the plan — per-tree file
// and byte counts, what the subtract set removed, which vault-global classes
// stay behind, which slugs are drift — is bounded and is all here.
type vaultSplitPlanResult struct {
	Action         string              `json:"action"`
	Destination    string              `json:"destination"`
	Slugs          []string            `json:"slugs"`
	SourceFormat   int                 `json:"source_format"`
	Files          int                 `json:"files"`
	Bytes          int64               `json:"bytes"`
	Trees          []splitTreeReport   `json:"trees"`
	SubtractSet    []string            `json:"subtract_set"`
	VaultGlobal    []splitGlobalReport `json:"vault_global"`
	Drift          []splitDriftReport  `json:"drift"`
	Notes          []string            `json:"notes"`
	ManifestSHA256 string              `json:"manifest_sha256"`
	Complete       bool                `json:"complete"`
}

func vaultSplitHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p vaultSplitParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}

		// The enum admits only "plan", but the handler re-checks rather than
		// trusting it: the schema is one validator away from the handler, and a
		// tool whose refusal depends on a schema it does not read is a tool that
		// silently gains an action the day the schema is widened.
		if p.Action != "plan" {
			return nil, apperr.Caller(fmt.Errorf(
				"invalid action %q: only %q is implemented (apply, verify and purge are designed but not built)",
				p.Action, "plan"))
		}

		m, err := buildSplitManifest(vault, p)
		if err != nil {
			return nil, err
		}

		format, err := surface.ReadFormat(vault.Root)
		if err != nil {
			return nil, fmt.Errorf("read source vault format: %w", err)
		}

		var files int
		var bytes int64
		for _, e := range m.Entries {
			files++
			bytes += e.Size
		}

		return &vaultSplitPlanResult{
			Action:         "plan",
			Destination:    p.Destination,
			Slugs:          m.Slugs,
			SourceFormat:   format,
			Files:          files,
			Bytes:          bytes,
			Trees:          m.Trees,
			SubtractSet:    splitSubtractSet,
			VaultGlobal:    m.Global,
			Drift:          m.Drift,
			Notes:          splitPlanNotes(m),
			ManifestSHA256: m.SHA256,
			Complete:       true,
		}, nil
	}
}

// splitPlanNotes are the things an operator must be told rather than left to
// discover, and they are said on every plan.
func splitPlanNotes(m *splitManifest) []string {
	notes := []string{
		"Git history does not travel. The destination is a fresh repository; the " +
			"per-commit vault diff for these projects stays in the source. " +
			"iterations.md, tasks/done/ and commit-log.md travel as files.",
		"Writer identity will change. The fingerprint is derived from hostname and " +
			"vault path, so future writes in the destination carry a NEW one while " +
			"historical session filenames keep the source fingerprint. A split " +
			"project's session index legitimately spans two fingerprints; " +
			"`vp check --check writer-identity` reports both, and that is correct.",
		"Templates do not travel and there is no include_templates. A fresh vault's " +
			"Templates/ is empty by design and the embedded floor serves every " +
			"lookup; copying the corpus onto the destination would shadow the binary.",
	}
	if len(m.Drift) > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d slug(s) are present in only one of palace/ and Projects/. plan reports "+
				"them and does not guess a disposition: what should happen to a "+
				"history-without-store or store-without-history slug is owned by the task "+
				"`vault-tree-drift-is-two-problems-with-opposite-defaults`.", len(m.Drift)))
	}
	return notes
}

// buildSplitManifest is the whole of plan: validate, refuse, walk, hash.
//
// It is separate from the handler so tests can assert on the ROWS — that
// commit-log.anchor is absent from the hashed inventory is a claim about
// entries, and the payload deliberately does not carry them.
func buildSplitManifest(vault *storage.Vault, p vaultSplitParams) (*splitManifest, error) {
	root := vault.Root
	if root == "" {
		return nil, fmt.Errorf("vault root is empty: no source vault is bound")
	}

	if len(p.Slugs) == 0 {
		return nil, apperr.Caller(fmt.Errorf(
			"slugs must name at least one project: inclusion is an allow-list, and an " +
				"empty one selects nothing rather than everything"))
	}
	if p.Destination == "" {
		return nil, apperr.Caller(fmt.Errorf("destination is required"))
	}

	// The destination is validated at PLAN time even though plan creates
	// nothing. A destination that resolves inside the vault was never going to
	// be legal, and finding that out at apply — after an operator has read a
	// manifest and approved it — wastes the approval.
	if err := vaultfs.RefuseDestinationInsideVault(root, p.Destination); err != nil {
		return nil, apperr.Caller(err)
	}

	// 🔴 SOURCE FORMAT IS CHECKED BEFORE ANY INVENTORY, not before any copy.
	// The format gate is a READ gate on KG accessors: a format-0 vault holds
	// triple files in the old encoding. Hashing them here and stamping the
	// destination format 1 later would produce a destination that looks current
	// while QueryEntity silently undercounts. Refusing at plan means apply
	// never starts on such a tree.
	format, err := surface.ReadFormat(root)
	if err != nil {
		return nil, fmt.Errorf("read source vault format: %w", err)
	}
	if format != surface.RequiredDataFormat {
		return nil, fmt.Errorf(
			"source vault is at data format %d, required %d: split refuses to copy "+
				"old-encoding data into a destination that would be stamped current",
			format, surface.RequiredDataFormat)
	}

	// Deduplicate and sort the request so the digest does not depend on the
	// order the caller happened to type.
	slugs, err := normalizeSplitSlugs(p.Slugs)
	if err != nil {
		return nil, err
	}

	presence, err := vault.ListAllProjects()
	if err != nil {
		return nil, fmt.Errorf("enumerate vault projects: %w", err)
	}
	known := make(map[string]storage.ProjectPresence, len(presence))
	for _, pr := range presence {
		known[pr.Slug] = pr
	}

	// 🔴 AN UNKNOWN SLUG IS A REFUSAL, never an empty selection. A typo that
	// silently planned zero files would produce a manifest whose digest is
	// perfectly valid and whose content is nothing — and the operator would
	// discover it after apply, on a destination that is missing a project.
	requested := make(map[string]bool, len(slugs))
	var unknown []string
	for _, s := range slugs {
		requested[s] = true
		if _, ok := known[s]; !ok {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		return nil, apperr.Caller(fmt.Errorf(
			"unknown project slug(s) %s: not present in palace/ or Projects/ of the bound vault",
			strings.Join(unknown, ", ")))
	}

	m := &splitManifest{
		Slugs:            slugs,
		IncludeLearnings: p.IncludeLearnings,
		IncludeAudits:    p.IncludeAudits,
	}

	for _, s := range slugs {
		projectDir, err := vault.ProjectDir(s)
		if err != nil {
			return nil, apperr.Caller(fmt.Errorf("project dir: %w", err))
		}
		// paths.go composes palace/{slug}/... per artifact and exports no
		// whole-tree helper, so the store root is joined here. The slug is
		// already validated, by normalizeSplitSlugs and again by ProjectDir.
		palaceDir := filepath.Join(root, "palace", s)

		for _, tree := range []struct {
			label string
			dir   string
		}{
			{"palace/" + s, palaceDir},
			{"Projects/" + s, projectDir},
		} {
			report, entries, err := walkSplitTree(root, tree.dir)
			if err != nil {
				return nil, err
			}
			report.Tree = tree.label
			m.Trees = append(m.Trees, report)
			m.Entries = append(m.Entries, entries...)
		}
	}

	global, globalEntries, err := walkSplitGlobal(root, p)
	if err != nil {
		return nil, err
	}
	m.Global = global
	m.Entries = append(m.Entries, globalEntries...)

	// Drift is REPORTED, never resolved. A slug in exactly one tree is the
	// common case in a real vault, not an edge case, and what should happen to
	// one is another task's ruling.
	for _, pr := range presence {
		if pr.Complete() {
			continue
		}
		m.Drift = append(m.Drift, splitDriftReport{
			Slug:       pr.Slug,
			InPalace:   pr.InPalace,
			InProjects: pr.InProjects,
			Requested:  requested[pr.Slug],
		})
	}

	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	m.SHA256 = splitManifestDigest(m)
	return m, nil
}

// normalizeSplitSlugs validates, deduplicates and sorts the requested slugs.
func normalizeSplitSlugs(in []string) ([]string, error) {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if err := slug.Validate(s); err != nil {
			return nil, apperr.Caller(fmt.Errorf("slug: %w", err))
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// walkSplitTree inventories one allow-listed tree.
//
// 🔴 Lstat FIRST, AND A NON-REGULAR FILE REFUSES. Not skip, not follow.
//
//   - FOLLOW is a leak. A symlink under an allow-listed slug can point at a
//     project that is not in the allow-list, or outside the vault entirely.
//     Reading through it files another project's bytes under this slug's name,
//     and the destination-side membership gate only ever sees the allow-listed
//     slug — so the leak passes every check downstream of here. This is why
//     migrate.copyTree/copyOne are not called: they os.Stat and os.ReadFile,
//     which both follow.
//   - SKIP is a silent hole. The path would be absent from the manifest and
//     absent from the destination, and nothing downstream would ever say so.
//
// filepath.WalkDir does not descend through a symlinked directory, but a
// symlinked FILE is still handed over as a DirEntry — so the refusal has to be
// made here, per entry, and not inferred from the walk's own behaviour.
func walkSplitTree(root, dir string) (splitTreeReport, []splitEntry, error) {
	var report splitTreeReport
	var entries []splitEntry

	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// A slug present in only one tree is drift, not an error. It is
			// reported as such; the missing side simply contributes nothing.
			return report, nil, nil
		}
		return report, nil, fmt.Errorf("stat %s: %w", vaultRel(root, dir), err)
	}
	if !info.IsDir() {
		return report, nil, fmt.Errorf(
			"%s is not a directory (mode %s): an allow-listed project tree must be a real directory",
			vaultRel(root, dir), info.Mode().Type())
	}
	report.Present = true

	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := vaultRel(root, p)

		// Machine-local trees are pruned, not scanned. They never travel, so
		// their contents are not this tool's business — and scanning them would
		// let a symlink in a cache directory refuse an otherwise clean plan.
		if d.IsDir() {
			if splitPrunedDirs[d.Name()] {
				report.Subtracted++
				return fs.SkipDir
			}
			return nil
		}

		st, lerr := os.Lstat(p)
		if lerr != nil {
			return fmt.Errorf("lstat %s: %w", rel, lerr)
		}
		if st.Mode().Type() != 0 {
			return fmt.Errorf(
				"%s is not a regular file (mode %s): split refuses a tree containing "+
					"symlinks, devices or sockets rather than skipping them (a silent hole "+
					"in the manifest) or following them (bytes from outside the allow-list "+
					"filed under an allow-listed slug)",
				rel, st.Mode().Type())
		}

		// The subtract set is applied AFTER the non-regular refusal, on purpose.
		// A symlink named .surface is still a symlink in an allow-listed tree,
		// and the reason to refuse it has nothing to do with whether its path
		// would later be hashed.
		if splitSubtracted(rel) {
			report.Subtracted++
			return nil
		}

		sum, size, herr := hashFile(p)
		if herr != nil {
			return fmt.Errorf("hash %s: %w", rel, herr)
		}
		entries = append(entries, splitEntry{Path: rel, SHA256: sum, Size: size})
		report.Files++
		report.Bytes += size
		return nil
	})
	if err != nil {
		return report, nil, err
	}
	return report, entries, nil
}

// walkSplitGlobal reports every vault-global artifact class and hashes the ones
// the caller affirmatively included.
//
// Vault-global artifacts are the leak surface precisely because they do NOT
// partition by slug: a learning carries no project field at all, and an audit
// report names slugs from across the whole vault. Both therefore default to
// copy-none, and plan reports what stays behind rather than leaving an operator
// to infer it from silence.
func walkSplitGlobal(root string, p vaultSplitParams) ([]splitGlobalReport, []splitEntry, error) {
	classes := []struct {
		class    string
		rel      string
		included bool
		note     string
	}{
		{"learnings", "Knowledge/learnings", p.IncludeLearnings,
			"Learning carries no project field, so no subset of these files is derivable from the slug allow-list."},
		{"audits", "Audits", p.IncludeAudits,
			"Audit reports and the accepted-findings baseline name slugs from across the whole vault."},
		{"templates", "Templates", false,
			"Never travels and has no flag: a fresh vault's Templates/ is empty and the embedded floor serves. Copying the corpus would shadow the binary."},
	}

	var reports []splitGlobalReport
	var entries []splitEntry
	for _, c := range classes {
		report := splitGlobalReport{Class: c.class, Path: c.rel, Included: c.included, Note: c.note}
		dir := filepath.Join(root, filepath.FromSlash(c.rel))

		err := filepath.WalkDir(dir, func(fp string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && fp == dir {
					return fs.SkipAll
				}
				return err
			}
			if d.IsDir() {
				if splitPrunedDirs[d.Name()] {
					return fs.SkipDir
				}
				return nil
			}
			rel := vaultRel(root, fp)
			st, lerr := os.Lstat(fp)
			if lerr != nil {
				return fmt.Errorf("lstat %s: %w", rel, lerr)
			}
			if st.Mode().Type() != 0 {
				// The refusal applies to allow-listed trees. A class the caller
				// did not include is not one, so a non-regular file there is
				// counted and reported rather than failing a plan over bytes
				// that are staying put.
				report.NonRegular++
				if c.included {
					return fmt.Errorf(
						"%s is not a regular file (mode %s): %s is included in this split, so "+
							"split refuses it rather than skipping or following it",
						rel, st.Mode().Type(), c.rel)
				}
				return nil
			}
			if splitSubtracted(rel) {
				return nil
			}
			report.Files++
			report.Bytes += st.Size()
			if c.included {
				sum, size, herr := hashFile(fp)
				if herr != nil {
					return fmt.Errorf("hash %s: %w", rel, herr)
				}
				entries = append(entries, splitEntry{Path: rel, SHA256: sum, Size: size})
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, nil, err
		}
		reports = append(reports, report)
	}
	return reports, entries, nil
}

// vaultRel renders a host path as a vault-relative, slash-separated path. Every
// manifest row and every error message uses it, so neither leaks the host's
// absolute vault location and neither varies by platform separator.
func vaultRel(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}

// hashFile returns the sha256 and size of a regular file. It streams rather
// than reading whole: a transcript archive is large, and a plan over a real
// vault opens thousands of them.
func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// splitManifestDigest canonicalises a manifest and returns its sha256.
//
// It binds the REQUEST as well as the bytes — the slug allow-list and both
// include flags — not only the entry rows. Two requests that happen to select
// identical bytes today are still different splits, and a later apply that
// re-hashes the source must not accept a digest minted for a different
// allow-list.
//
// The preimage is newline-delimited with NUL field separators so no path can
// forge a row boundary, and it opens with a format tag so a change to this
// encoding changes every digest it produces.
func splitManifestDigest(m *splitManifest) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", splitManifestFormat)
	fmt.Fprintf(h, "slugs\x00%s\n", strings.Join(m.Slugs, "\x00"))
	fmt.Fprintf(h, "include_learnings\x00%t\n", m.IncludeLearnings)
	fmt.Fprintf(h, "include_audits\x00%t\n", m.IncludeAudits)
	for _, e := range m.Entries {
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", e.Path, e.SHA256, strconv.FormatInt(e.Size, 10))
	}
	return hex.EncodeToString(h.Sum(nil))
}
