// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// vp_vault_merge folds a declared set of project slugs from a standalone vault
// at an absolute host path INTO the bound vault. It is the inverse direction of
// vp_vault_split and shares that tool's copy helper, its hashed minus-set and
// its non-regular refusal — but not its verify, for a reason worth stating up
// front.
//
// 🔴 SPLIT'S DESTINATION IS EMPTY; MERGE'S IS THE OPERATOR'S LIVE VAULT. Every
// gate split states as "the destination holds only what travelled" is false
// here by construction, because the destination already holds everything the
// operator has. So merge asserts INVARIANCE instead of emptiness: the parts of
// the destination this merge does not add must come out the other side
// identical. R20 names the specific trap — a shared "Knowledge/ is empty"
// helper would fail every merge that ever ran — and the two tools deliberately
// do not share one.
//
// 🔴 THE PRE-MERGE SNAPSHOT LIVES IN THE MANIFEST DIGEST, AND THAT IS WHAT MAKES
// LEAK GATE 1 CHECKABLE AFTER THE FACT. The gate is "destination slug set ==
// pre-merge snapshot ∪ allow-list", and `verify` is a separate call that cannot
// observe the pre-merge state. Asserting it against a snapshot taken after the
// merge would be circular. The way out is that the quantity
//
//	destination slug names  MINUS  the allow-list
//
// is INVARIANT across a correct merge — same-slug is refused, so the allow-list
// is disjoint from the destination beforehand, and a correct merge adds exactly
// the allow-list. So the manifest binds that difference, and the digest carries
// it from plan through apply to verify. An unnamed slug arriving changes the
// difference, changes the digest, and refuses. The same trick binds the
// destination's Knowledge/ and Audits/ file sets.
//
// 🔴 v1 IS DISJOINT SLUGS ONLY, AND SAME-SLUG IS A HARDCODED REFUSAL. It is not
// a flag with one value. Iteration numbers are minted independently in each
// vault (project_dirs.go:114 calls wrapstate.go:148), so folding two histories
// of one slug together would renumber iterations and break every citation that
// names one. Moving work between projects is `first-class-task-migrate-action`,
// a different task with a different shape; this tool points at it and refuses.

// mergeManifestFormat tags the digest preimage. It is distinct from split's:
// the two manifests bind different things, and a digest minted by one must
// never validate against the other.
const mergeManifestFormat = "vp_vault_merge/manifest/1"

// mergeGlobalClasses are the vault-global artifact classes merge can carry, and
// the ONLY ones. Templates is absent by design rather than by oversight: the
// destination has its own override-only tree, the embedded floor serves every
// lookup it does not override, and copying a source corpus over it would shadow
// the binary. There is no include_templates and there will not be one.
var mergeGlobalClasses = []struct {
	class string
	rel   string
	note  string
}{
	{"learnings", "Knowledge/learnings",
		"Learning carries no project field, so no subset of these files is derivable from the slug allow-list."},
	{"audits", "Audits",
		"Audit reports and the accepted-findings baseline name slugs from across the whole vault."},
}

// mergeGlobalReport is one vault-global class: what the source holds, and
// whether any of it travels.
type mergeGlobalReport struct {
	Class    string `json:"class"`
	Path     string `json:"path"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	Included bool   `json:"included"`
	Note     string `json:"note,omitempty"`
}

// mergeManifest is the server-side plan artifact.
//
// Entries are the source bytes that travel. DestSlugsUntouched and
// DestGlobalUntouched are the destination state this merge must NOT disturb —
// they are in the digest precisely so that disturbing any of it refuses at the
// next bind.
type mergeManifest struct {
	Slugs               []string
	IncludeLearnings    bool
	IncludeAudits       bool
	Entries             []splitEntry
	Trees               []splitTreeReport
	Global              []mergeGlobalReport
	Drift               []splitDriftReport
	DestSlugsUntouched  []string
	DestGlobalUntouched []string
	SHA256              string
}

type vaultMergeParams struct {
	Action                   string   `json:"action"`
	Source                   string   `json:"source"`
	Slugs                    []string `json:"slugs"`
	IncludeLearnings         bool     `json:"include_learnings"`
	IncludeAudits            bool     `json:"include_audits"`
	AffirmDestinationRemotes []string `json:"affirm_destination_remotes"`
	ManifestSHA256           string   `json:"manifest_sha256"`
}

var vaultMergeSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "enum": ["plan", "apply", "verify"], "description": "Action. \"plan\" walks the source vault, refuses a same-slug collision, hashes what would travel and returns a manifest digest, writing nothing. \"apply\" re-binds that digest, requires the destination's remotes to match affirm_destination_remotes exactly, and copies the source slug trees into the bound vault; it never mutates the source. \"verify\" proves the destination gained exactly the manifest and disturbed nothing else, writing nothing. There is no purge: merge removes nothing from either vault."},
		"source": {"type": "string", "description": "Absolute host path of the standalone vault to merge FROM. It must resolve outside the bound vault, which is the destination. A relative path is refused: it would resolve against the server's working directory."},
		"slugs": {"type": "array", "items": {"type": "string"}, "description": "Allow-list of project slugs to bring across. Inclusion is an allow-list, never \"everything except\": there is no exclude parameter. A slug absent from the source is a refusal, and a slug already present in the destination is a refusal — v1 merges disjoint slugs only."},
		"include_learnings": {"type": "boolean", "description": "Include the source's Knowledge/learnings/*.md. Default false. Learnings carry no project field, so copy-none is the fail-closed default."},
		"include_audits": {"type": "boolean", "description": "Include the source's Audits/. Default false. Audit reports name slugs from across the whole source vault."},
		"affirm_destination_remotes": {"type": "array", "items": {"type": "string"}, "description": "The destination's git remotes, restated by the caller. apply refuses unless this matches the destination's actual git remote set EXACTLY. Merging a project into a vault that pushes somewhere is a publication decision, so the caller states where it publishes rather than discovering it afterwards. Omit for a destination with no remotes."},
		"manifest_sha256": {"type": "string", "description": "The digest action \"plan\" returned. Required by apply and verify. The manifest stays server-side: both actions re-derive it and refuse unless the digest matches, so neither the source nor the untouched parts of the destination can have changed since the plan was approved."}
	},
	"required": ["action", "source", "slugs"]
}`)

// vaultMergeReadOnly admits the two actions that write nothing.
//
// It is an ALLOW-LIST of actions proven read-only, never a deny-list of the
// ones known to write — the same shape as vaultSplitReadOnly and for the same
// reason: a fourth action added later is refused by a stale binary until
// someone deliberately names it here, whereas a deny-list would admit it by
// default and the mistake would be silent.
var vaultMergeReadOnly = readOnlyIf(func(p vaultMergeParams) bool {
	return p.Action == "plan" || p.Action == "verify"
})

// VaultMergeTool registers vp_vault_merge.
func VaultMergeTool(vault *storage.Vault) mcp.Tool {
	return mcp.Tool{
		Name:     "vp_vault_merge",
		Mutating: true,
		// plan and verify read and hash; apply writes. See vaultMergeReadOnly.
		ReadOnlyWhen: vaultMergeReadOnly,
		// 🔴 THIS TEXT IS PART OF THE SURFACE AND MUST TRACK THE HANDLER IN BOTH
		// DIRECTIONS — naming a capability the code lacks, and denying one the
		// schema advertises, are the same lie. Nothing pins description text
		// against behaviour, so this is a discipline rather than a check.
		Description: "Merge a declared set of project slugs from a standalone " +
			"vault at an absolute host path INTO the bound vault. Three actions. " +
			"\"plan\" verifies both vaults' data format, refuses any slug already " +
			"present in the destination (v1 merges disjoint slugs only), walks " +
			"the allow-listed palace/<slug> and Projects/<slug> trees in the " +
			"source, refuses any non-regular file it finds there rather than " +
			"skipping or following it, hashes what would travel minus " +
			"{.surface, **/.local, .vp-locks, commit-log.anchor}, records the " +
			"destination state the merge must not disturb, and returns a " +
			"manifest_sha256; it writes nothing. \"apply\" re-binds that digest, " +
			"refuses unless the destination's git remotes match " +
			"affirm_destination_remotes exactly, and copies; it never mutates " +
			"the source and never configures a remote. \"verify\" proves the " +
			"destination gained exactly the manifest — allow-listed slugs " +
			"arrived, no unnamed slug did, Knowledge/ and Audits/ gained files " +
			"only if include_* was set, Templates/ was not written — and writes " +
			"nothing. There is no purge: merge removes nothing from either " +
			"vault. Source Templates/ never travels and there is no " +
			"include_templates. Splitting a vault apart is vp_vault_split.",
		Schema:  vaultMergeSchema,
		Handler: vaultMergeHandler(vault),
	}
}

// vaultMergePlanResult reports the manifest's SHAPE and its digest, never its
// rows — a real merge runs to thousands of files, and a host-truncated row list
// beside an authoritative-looking digest is worse than no row list at all.
type vaultMergePlanResult struct {
	Action              string              `json:"action"`
	Source              string              `json:"source"`
	Destination         string              `json:"destination"`
	Slugs               []string            `json:"slugs"`
	SourceFormat        int                 `json:"source_format"`
	DestFormat          int                 `json:"dest_format"`
	Files               int                 `json:"files"`
	Bytes               int64               `json:"bytes"`
	Trees               []splitTreeReport   `json:"trees"`
	SubtractSet         []string            `json:"subtract_set"`
	VaultGlobal         []mergeGlobalReport `json:"vault_global"`
	Drift               []splitDriftReport  `json:"drift"`
	DestinationRemotes  []string            `json:"destination_remotes"`
	DestSlugsUntouched  []string            `json:"dest_slugs_untouched"`
	DestGlobalUntouched int                 `json:"dest_global_untouched_files"`
	Notes               []string            `json:"notes"`
	ManifestSHA256      string              `json:"manifest_sha256"`
	Complete            bool                `json:"complete"`
}

// vaultMergeApplyResult is the payload for action "apply".
type vaultMergeApplyResult struct {
	Action             string   `json:"action"`
	Source             string   `json:"source"`
	Destination        string   `json:"destination"`
	Slugs              []string `json:"slugs"`
	ManifestSHA256     string   `json:"manifest_sha256"`
	FilesCopied        int      `json:"files_copied"`
	BytesCopied        int64    `json:"bytes_copied"`
	DestinationRemotes []string `json:"destination_remotes"`
	Notes              []string `json:"notes"`
	Complete           bool     `json:"complete"`
}

// vaultMergeVerifyResult is the payload for action "verify".
type vaultMergeVerifyResult struct {
	Action             string   `json:"action"`
	Source             string   `json:"source"`
	Destination        string   `json:"destination"`
	Slugs              []string `json:"slugs"`
	ManifestSHA256     string   `json:"manifest_sha256"`
	FilesVerified      int      `json:"files_verified"`
	BytesVerified      int64    `json:"bytes_verified"`
	DestinationRemotes []string `json:"destination_remotes"`
	GatesChecked       []string `json:"gates_checked"`
	Notes              []string `json:"notes"`
	Complete           bool     `json:"complete"`
}

func vaultMergeHandler(vault *storage.Vault) mcp.HandlerFunc {
	return func(_ context.Context, params json.RawMessage) (any, error) {
		var p vaultMergeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("parse params: %w", err)
		}

		// The handler re-checks the action rather than trusting the enum: a
		// tool whose refusal lives only in a schema silently gains an action
		// the day the schema is widened. There is no `purge` arm, and merge
		// deliberately has nothing to remove.
		switch p.Action {
		case "plan":
			return vaultMergePlan(vault, p)
		case "apply":
			return vaultMergeApply(vault, p)
		case "verify":
			return vaultMergeVerify(vault, p)
		default:
			return nil, apperr.Caller(fmt.Errorf(
				"invalid action %q: expected one of plan, apply, verify", p.Action))
		}
	}
}

// vaultMergePlan walks both vaults and returns a digest. It writes nothing and
// creates nothing.
func vaultMergePlan(vault *storage.Vault, p vaultMergeParams) (*vaultMergePlanResult, error) {
	m, err := buildMergeManifest(vault, p, true)
	if err != nil {
		return nil, err
	}

	sourceFormat, err := surface.ReadFormat(p.Source)
	if err != nil {
		return nil, fmt.Errorf("read source vault format: %w", err)
	}
	destFormat, err := surface.ReadFormat(vault.Root)
	if err != nil {
		return nil, fmt.Errorf("read destination vault format: %w", err)
	}

	// Reported, never enforced here: apply is where the affirmation must match.
	// plan states what the destination's remotes ARE so an operator can affirm
	// them from fact rather than from memory.
	remotes, err := mergeDestinationRemotes(vault.Root)
	if err != nil {
		return nil, err
	}

	var files int
	var bytes int64
	for _, e := range m.Entries {
		files++
		bytes += e.Size
	}

	return &vaultMergePlanResult{
		Action:              "plan",
		Source:              p.Source,
		Destination:         vault.Root,
		Slugs:               m.Slugs,
		SourceFormat:        sourceFormat,
		DestFormat:          destFormat,
		Files:               files,
		Bytes:               bytes,
		Trees:               m.Trees,
		SubtractSet:         splitSubtractSet,
		VaultGlobal:         m.Global,
		Drift:               m.Drift,
		DestinationRemotes:  remotes,
		DestSlugsUntouched:  m.DestSlugsUntouched,
		DestGlobalUntouched: len(m.DestGlobalUntouched),
		Notes:               mergePlanNotes(m, remotes),
		ManifestSHA256:      m.SHA256,
		Complete:            true,
	}, nil
}

// mergePlanNotes are the things an operator must be told rather than left to
// discover, and they are said on every plan.
func mergePlanNotes(m *mergeManifest, remotes []string) []string {
	notes := []string{
		"Git history does not travel. The source's per-commit vault diff for these " +
			"projects stays in the source repository; iterations.md, tasks/done/ and " +
			"commit-log.md travel as files. commit-log.anchor does NOT travel — it names " +
			"a commit that does not exist in the destination.",
		"Writer identity will change. The fingerprint is derived from hostname and " +
			"vault path, so future writes to these projects carry the DESTINATION's " +
			"fingerprint while historical session filenames keep the source's. A merged " +
			"project's session index legitimately spans two fingerprints, and " +
			"`vp check --check writer-identity` reports both.",
		"Source Templates/ does not travel and there is no include_templates. The " +
			"destination keeps its own override-only tree and the embedded floor serves " +
			"the rest.",
		"Merge removes nothing. The source vault is untouched by apply and there is no " +
			"purge action; deleting the source copy is a separate decision an operator " +
			"makes by hand.",
	}
	if len(remotes) > 0 {
		notes = append(notes, fmt.Sprintf(
			"The destination publishes to %d remote(s): %s. Merging these projects in "+
				"makes them publishable there, which is why apply requires them restated "+
				"in affirm_destination_remotes.", len(remotes), strings.Join(remotes, ", ")))
	}
	if len(m.Drift) > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d source slug(s) are present in only one of palace/ and Projects/. plan "+
				"reports them and does not guess a disposition: what should happen to a "+
				"history-without-store or store-without-history slug is owned by the task "+
				"`vault-tree-drift-is-two-problems-with-opposite-defaults`.", len(m.Drift)))
	}
	return notes
}

// buildMergeManifest is the whole of plan: validate, refuse, walk, hash.
//
// It is separate from the handler so tests can assert on the ROWS, and it is
// re-run by apply and verify so the digest bind is a real TOCTOU guard rather
// than a checksum of the request.
//
// 🔴 beforeCopy SELECTS THE REFUSALS THAT ONLY MAKE SENSE BEFORE THE COPY, and
// getting this wrong makes verify structurally impossible to pass. Two of the
// checks here — the same-slug collision and the would-overwrite collision — say
// "the destination must not ALREADY contain what this merge would add". That is
// exactly true at plan and apply, and exactly FALSE at verify, where a correct
// merge has just put every one of those files there on purpose. So plan and
// apply pass true and verify passes false.
//
// Nothing else differs, and in particular the DIGEST does not: the two
// collision checks either refuse or say nothing, and neither contributes a byte
// to the manifest. That is what lets one derivation serve a bind across all
// three actions.
//
// The safety those checks provide is not lost at verify. A slug that failed to
// arrive is caught by verify's own "every allow-listed slug arrived" assertion,
// and a destination file that was overwritten rather than added is caught by
// DestGlobalUntouched, which is recomputed and compared through the digest.
func buildMergeManifest(vault *storage.Vault, p vaultMergeParams, beforeCopy bool) (*mergeManifest, error) {
	dest := vault.Root
	if dest == "" {
		return nil, fmt.Errorf("vault root is empty: no destination vault is bound")
	}
	if len(p.Slugs) == 0 {
		return nil, apperr.Caller(fmt.Errorf(
			"slugs must name at least one project: inclusion is an allow-list, and an " +
				"empty one selects nothing rather than everything"))
	}
	if err := mergeCheckSource(dest, p.Source); err != nil {
		return nil, err
	}

	// 🔴 BOTH VAULTS' DATA FORMAT IS CHECKED BEFORE ANY INVENTORY, and therefore
	// before any copy. The format gate is a READ gate on knowledge-graph
	// accessors, so nothing about copying a format-0 tree fails on its own —
	// which is exactly the danger. Merge's source is an arbitrary host path and
	// is the likelier of the two to be an old, unmigrated vault; folding its
	// old-encoding triple files into a destination stamped format 1 produces a
	// vault that reports itself current while QueryEntity silently undercounts.
	for _, v := range []struct {
		label string
		root  string
	}{{"source", p.Source}, {"destination", dest}} {
		format, err := surface.ReadFormat(v.root)
		if err != nil {
			return nil, fmt.Errorf("read %s vault format: %w", v.label, err)
		}
		if format != surface.RequiredDataFormat {
			return nil, fmt.Errorf(
				"%s vault is at data format %d, required %d: merge refuses to fold "+
					"old-encoding data into a vault that would report itself current",
				v.label, format, surface.RequiredDataFormat)
		}
	}

	slugs, err := normalizeSplitSlugs(p.Slugs)
	if err != nil {
		return nil, err
	}

	sourceVault := storage.NewVault(p.Source)
	sourcePresence, err := sourceVault.ListAllProjects()
	if err != nil {
		return nil, fmt.Errorf("enumerate source vault projects: %w", err)
	}
	sourceKnown := make(map[string]bool, len(sourcePresence))
	for _, pr := range sourcePresence {
		sourceKnown[pr.Slug] = true
	}

	var unknown []string
	for _, s := range slugs {
		if !sourceKnown[s] {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		return nil, apperr.Caller(fmt.Errorf(
			"unknown project slug(s) %s: not present in palace/ or Projects/ of the source vault at %s",
			strings.Join(unknown, ", "), p.Source))
	}

	// 🔴 SAME-SLUG IS A HARDCODED REFUSAL, NOT A POLICY WITH A DEFAULT. It is
	// checked by tree membership rather than by ListAllProjects, for the same
	// reason leak gate 1 is: ListAllProjects filters to valid slug directories
	// and would miss a collision with anything else sitting under that name.
	destNames, err := mergeDestTreeNames(dest)
	if err != nil {
		return nil, err
	}
	var collide []string
	if beforeCopy {
		for _, s := range slugs {
			if destNames[s] {
				collide = append(collide, s)
			}
		}
	}
	if len(collide) > 0 {
		return nil, apperr.Caller(fmt.Errorf(
			"slug(s) %s already exist in the destination vault: v1 merges DISJOINT slugs "+
				"only, and folding two histories of one slug together is refused rather "+
				"than resolved. Iteration numbers are minted independently in each vault, "+
				"so a union would renumber iterations and break every citation naming one. "+
				"Moving work between projects is the task `first-class-task-migrate-action`, "+
				"which merge points at and does not absorb",
			strings.Join(collide, ", ")))
	}

	m := &mergeManifest{
		Slugs:            slugs,
		IncludeLearnings: p.IncludeLearnings,
		IncludeAudits:    p.IncludeAudits,
	}

	// Source slug trees, through split's walker: same Lstat-first contract, same
	// non-regular refusal, same subtract set. Sharing it is deliberate — a
	// second walker would be a second definition of what travels, and the day
	// the two disagree is the day one of them is wrong and nothing says so.
	for _, s := range slugs {
		for _, tree := range []struct {
			label string
			dir   string
		}{
			{"palace/" + s, filepath.Join(p.Source, "palace", s)},
			{"Projects/" + s, filepath.Join(p.Source, "Projects", s)},
		} {
			report, entries, err := walkSplitTree(p.Source, tree.dir)
			if err != nil {
				return nil, err
			}
			report.Tree = tree.label
			m.Trees = append(m.Trees, report)
			m.Entries = append(m.Entries, entries...)
		}
	}

	// Vault-global classes. Reported always; hashed only when included.
	for _, c := range mergeGlobalClasses {
		included := (c.class == "learnings" && p.IncludeLearnings) ||
			(c.class == "audits" && p.IncludeAudits)
		entries, err := mergeWalkClass(p.Source, c.rel, included)
		if err != nil {
			return nil, err
		}
		report := mergeGlobalReport{Class: c.class, Path: c.rel, Included: included, Note: c.note}
		for _, e := range entries {
			report.Files++
			report.Bytes += e.Size
		}
		m.Global = append(m.Global, report)
		if included {
			m.Entries = append(m.Entries, entries...)
		}
	}

	// A vault-global file that would land on an existing destination file is a
	// refusal, not an overwrite. Slug collisions are caught above; this is the
	// same disjointness rule for the artifacts that do not partition by slug.
	var clobber []string
	if beforeCopy {
		for _, e := range m.Entries {
			if _, err := os.Lstat(filepath.Join(dest, filepath.FromSlash(e.Path))); err == nil {
				clobber = append(clobber, e.Path)
			}
		}
	}
	if len(clobber) > 0 {
		sort.Strings(clobber)
		if len(clobber) > 8 {
			clobber = append(clobber[:8], fmt.Sprintf("… and %d more", len(clobber)-8))
		}
		return nil, apperr.Caller(fmt.Errorf(
			"merge would overwrite existing destination file(s): %s. Merge adds; it never "+
				"replaces", strings.Join(clobber, ", ")))
	}

	// Source drift is REPORTED, never resolved.
	for _, pr := range sourcePresence {
		if pr.Complete() {
			continue
		}
		m.Drift = append(m.Drift, splitDriftReport{
			Slug:       pr.Slug,
			InPalace:   pr.InPalace,
			InProjects: pr.InProjects,
			Requested:  containsMergeSlug(slugs, pr.Slug),
		})
	}

	// 🔴 THE INVARIANT HALF: the destination state this merge must not disturb.
	//
	// Both quantities are computed as "what is there now, minus what this merge
	// adds", which makes them identical before and after a correct apply — and
	// different after an incorrect one. Putting them in the digest is what lets
	// `verify`, which cannot see the pre-merge vault, still assert leak gate 1's
	// equality without asking the question in a circle.
	requested := make(map[string]bool, len(slugs))
	for _, s := range slugs {
		requested[s] = true
	}
	for name := range destNames {
		if !requested[name] {
			m.DestSlugsUntouched = append(m.DestSlugsUntouched, name)
		}
	}
	sort.Strings(m.DestSlugsUntouched)

	adding := make(map[string]bool, len(m.Entries))
	for _, e := range m.Entries {
		adding[e.Path] = true
	}
	for _, c := range mergeGlobalClasses {
		paths, err := mergeClassPaths(dest, c.rel)
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if !adding[path] {
				m.DestGlobalUntouched = append(m.DestGlobalUntouched, path)
			}
		}
	}
	sort.Strings(m.DestGlobalUntouched)

	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	m.SHA256 = mergeManifestDigest(m)
	return m, nil
}

// mergeCheckSource validates the source path: absolute, outside the bound
// vault, and an actual directory.
//
// 🔴 RefuseDestinationInsideVault IS USED ON THE SOURCE HERE, AND THE NAME READS
// BACKWARDS FOR A REASON. The predicate is "this operator-typed host path
// resolves OUTSIDE the vault this server owns", which is exactly the question
// merge needs to ask about its source: a source that resolved inside the bound
// vault would make the tool copy the destination onto itself.
func mergeCheckSource(destRoot, source string) error {
	if strings.TrimSpace(source) == "" {
		return apperr.Caller(fmt.Errorf("source is required"))
	}
	if !filepath.IsAbs(source) {
		return apperr.Caller(fmt.Errorf(
			"source %q is not an absolute path: it would resolve against the server's "+
				"working directory, which the caller cannot see", source))
	}
	if err := vaultfs.RefuseDestinationInsideVault(destRoot, source); err != nil {
		return apperr.Caller(fmt.Errorf(
			"source must be a separate vault: %w", err))
	}
	info, err := os.Stat(source)
	if err != nil {
		return apperr.Caller(fmt.Errorf("source vault %q is not readable: %w", source, err))
	}
	if !info.IsDir() {
		return apperr.Caller(fmt.Errorf("source %q is not a directory", source))
	}
	return nil
}

// mergeDestTreeNames reads the destination's palace/ and Projects/ directory
// entries and returns the union of the names it finds.
//
// 🔴 ReadDir, NOT ListAllProjects. ListAllProjects keeps only directories whose
// names pass slug.Validate and silently drops files, symlinks and invalid-slug
// directories (projects.go:103-126) — the right filter for a DRIFT report,
// the wrong one for a collision or leak assertion, because it applies the same
// slug filter the copy path already applied and would report clean by
// construction. palace/.local is skipped: it is vault-wide machine-local state,
// not a project.
func mergeDestTreeNames(dest string) (map[string]bool, error) {
	names := map[string]bool{}
	for _, tree := range []string{"palace", "Projects"} {
		ents, err := os.ReadDir(filepath.Join(dest, tree))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read destination %s/: %w", tree, err)
		}
		for _, e := range ents {
			if tree == "palace" && e.Name() == ".local" {
				continue
			}
			names[e.Name()] = true
		}
	}
	return names, nil
}

// mergeWalkClass inventories one vault-global class in the SOURCE, applying the
// same Lstat-first, refuse-non-regular, subtract-set rules as a slug tree.
//
// A non-regular file refuses only when the class is INCLUDED. A class staying
// behind is not part of this merge, so a symlink in it is not this tool's
// business — the same distinction split draws, for the same reason.
func mergeWalkClass(root, rel string, included bool) ([]splitEntry, error) {
	dir := filepath.Join(root, filepath.FromSlash(rel))
	var entries []splitEntry
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == dir {
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
		vrel := vaultRel(root, p)
		st, lerr := os.Lstat(p)
		if lerr != nil {
			return fmt.Errorf("lstat %s: %w", vrel, lerr)
		}
		if st.Mode().Type() != 0 {
			if !included {
				return nil
			}
			return fmt.Errorf(
				"%s is not a regular file (mode %s): %s is included in this merge, so "+
					"merge refuses it rather than skipping or following it",
				vrel, st.Mode().Type(), rel)
		}
		if splitSubtracted(vrel) {
			return nil
		}
		sum, size, herr := hashFile(p)
		if herr != nil {
			return fmt.Errorf("hash %s: %w", vrel, herr)
		}
		entries = append(entries, splitEntry{Path: vrel, SHA256: sum, Size: size})
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return entries, nil
}

// mergeClassPaths lists the vault-relative paths of one vault-global class,
// without hashing.
//
// 🔴 PATHS, NOT CONTENT, AND THAT IS THE CONTRACT. The gate this feeds is "the
// destination's Knowledge/ and Audits/ FILE SET is unchanged". Hashing content
// here would additionally bind the destination's own learnings against edits
// that have nothing to do with the merge, and the bind would break for reasons
// no operator would connect to the merge they are running. Content protection
// comes from the overwrite refusal in buildMergeManifest instead.
//
// It is deliberately tolerant of non-regular entries: this walks the OPERATOR's
// live vault, and unrelated dirt there must not refuse a merge that does not
// touch it. It records such an entry rather than refusing, so the set-difference
// gate still sees it.
func mergeClassPaths(root, rel string) ([]string, error) {
	dir := filepath.Join(root, filepath.FromSlash(rel))
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == dir {
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
		vrel := vaultRel(root, p)
		if splitSubtracted(vrel) {
			return nil
		}
		paths = append(paths, vrel)
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return paths, nil
}

// mergeDestinationRemotes reads the destination's git remotes.
//
// 🔴 AN ERROR IS A HARD REFUSAL, NOT "no remotes". ListRemotes shells out to
// `git remote` (vaultsync.go:651-666) and returns (nil, err) when the path is
// not a repository at all. Treating that as an empty set would let the
// affirmation gate PASS on a destination whose repository is broken or absent —
// precisely the case the gate exists to catch. `UNVERIFIED` in the CLI is an
// ahead-count, a different question, and is not a precedent for softening this.
func mergeDestinationRemotes(dest string) ([]string, error) {
	remotes, err := storage.ListRemotes(dest)
	if err != nil {
		return nil, fmt.Errorf(
			"list destination remotes: %w (this is a refusal, not an empty remote set: "+
				"a destination that cannot answer `git remote` is not a repository)", err)
	}
	return remotes, nil
}

// mergeBindManifest is the shared precondition of apply and verify.
func mergeBindManifest(vault *storage.Vault, p vaultMergeParams, beforeCopy bool) (*mergeManifest, error) {
	if strings.TrimSpace(p.ManifestSHA256) == "" {
		return nil, apperr.Caller(fmt.Errorf(
			"manifest_sha256 is required for action %q: run action \"plan\" first and pass "+
				"the digest it returns. The manifest itself is server-side; the digest is "+
				"the whole of what binds this call to the plan an operator approved",
			p.Action))
	}
	m, err := buildMergeManifest(vault, p, beforeCopy)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(m.SHA256, strings.TrimSpace(p.ManifestSHA256)) {
		return nil, apperr.Caller(fmt.Errorf(
			"manifest_sha256 mismatch: this merge now hashes to %s, not the %s the call "+
				"named. Either the source changed, or the destination gained state this "+
				"merge does not account for. Re-run action \"plan\"",
			m.SHA256, strings.TrimSpace(p.ManifestSHA256)))
	}
	return m, nil
}

// mergeAffirmRemotes refuses unless the destination's remotes are EXACTLY the
// affirmed set.
//
// Merging a project into a vault that pushes somewhere is a publication
// decision, and it is one the caller must make with the remote list in front of
// them rather than discover afterwards. Exactly, not "at least": a remote the
// caller did not name is a destination they were not thinking about.
func mergeAffirmRemotes(dest string, affirmed []string) ([]string, error) {
	actual, err := mergeDestinationRemotes(dest)
	if err != nil {
		return nil, err
	}
	want := append([]string(nil), affirmed...)
	got := append([]string(nil), actual...)
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(want, "\x00") != strings.Join(got, "\x00") {
		return nil, apperr.Caller(fmt.Errorf(
			"destination remotes are %s, but affirm_destination_remotes named %s: merging "+
				"projects into this vault makes them publishable wherever it pushes, so "+
				"apply refuses until the caller restates the remote set exactly",
			mergeRemoteList(actual), mergeRemoteList(affirmed)))
	}
	return actual, nil
}

func mergeRemoteList(r []string) string {
	if len(r) == 0 {
		return "(none)"
	}
	return strings.Join(r, ", ")
}

// vaultMergeApply copies the manifest from the source into the bound vault. It
// writes nothing to the source and configures no remotes.
func vaultMergeApply(vault *storage.Vault, p vaultMergeParams) (*vaultMergeApplyResult, error) {
	// Cheap, total refusals first: the bind on its own shape, then the source
	// path, then the destination's publication surface. Only then the expensive
	// walk, and only then any copy.
	if strings.TrimSpace(p.ManifestSHA256) == "" {
		return nil, apperr.Caller(fmt.Errorf(
			"manifest_sha256 is required for action %q: run action \"plan\" first and pass "+
				"the digest it returns", p.Action))
	}
	if err := mergeCheckSource(vault.Root, p.Source); err != nil {
		return nil, err
	}
	remotes, err := mergeAffirmRemotes(vault.Root, p.AffirmDestinationRemotes)
	if err != nil {
		return nil, err
	}

	m, err := mergeBindManifest(vault, p, true)
	if err != nil {
		return nil, err
	}

	var files int
	var bytes int64
	for _, e := range m.Entries {
		// splitCopyEntry is the shared helper: Lstat first, refuse non-regular,
		// hash while streaming and compare to the manifest row, write through
		// atomicfile.WriteStream with vaultRoot = the destination so the
		// destination stamps itself.
		if err := splitCopyEntry(p.Source, vault.Root, e); err != nil {
			return nil, err
		}
		files++
		bytes += e.Size
	}

	return &vaultMergeApplyResult{
		Action:             "apply",
		Source:             p.Source,
		Destination:        vault.Root,
		Slugs:              m.Slugs,
		ManifestSHA256:     m.SHA256,
		FilesCopied:        files,
		BytesCopied:        bytes,
		DestinationRemotes: remotes,
		Notes: append(mergePlanNotes(m, remotes),
			"The source vault is untouched. There is no purge action on merge: removing "+
				"the source copy is a separate decision an operator makes by hand.",
			"No remote was configured and nothing was committed.",
		),
		Complete: true,
	}, nil
}

// vaultMergeVerify proves the destination gained exactly the manifest and
// disturbed nothing else. It writes nothing.
func vaultMergeVerify(vault *storage.Vault, p vaultMergeParams) (*vaultMergeVerifyResult, error) {
	if err := mergeCheckSource(vault.Root, p.Source); err != nil {
		return nil, err
	}

	// 🔴 THE BIND IS ITSELF LEAK GATE 1's EQUALITY. buildMergeManifest recomputes
	// DestSlugsUntouched as "destination slug names minus the allow-list" and
	// DestGlobalUntouched as "destination Knowledge/Audits paths minus what this
	// merge adds". Both are invariant across a CORRECT merge, so a matching
	// digest here is the assertion that the destination slug set is exactly the
	// pre-merge snapshot ∪ the allow-list, and that Knowledge/ and Audits/
	// gained nothing beyond the manifest. An unnamed slug arriving, or an
	// unexpected learning appearing, changes the recomputed value and refuses.
	m, err := mergeBindManifest(vault, p, false)
	if err != nil {
		return nil, err
	}

	dest := vault.Root
	var problems []string

	// Everything asked for actually arrived, as a directory.
	for _, s := range m.Slugs {
		var present bool
		for _, tree := range []string{"palace", "Projects"} {
			info, err := os.Lstat(filepath.Join(dest, tree, s))
			if err != nil {
				continue
			}
			if !info.IsDir() {
				problems = append(problems, fmt.Sprintf(
					"%s/%s is not a directory (mode %s)", tree, s, info.Mode().Type()))
				continue
			}
			present = true
		}
		if !present {
			problems = append(problems, fmt.Sprintf(
				"allow-listed slug %q is not present in the destination", s))
		}
	}

	// Structural half of leak gate 1: every name under the destination's project
	// trees is a directory with a valid slug name. The digest bind proves the
	// SET is right; this proves each member is shaped like a project.
	for _, tree := range []string{"palace", "Projects"} {
		ents, err := os.ReadDir(filepath.Join(dest, tree))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			problems = append(problems, fmt.Sprintf("read destination %s/: %v", tree, err))
			continue
		}
		for _, e := range ents {
			if tree == "palace" && e.Name() == ".local" {
				continue
			}
			if !e.IsDir() {
				problems = append(problems, fmt.Sprintf(
					"%s/%s is not a directory (type %s): a project tree holds slug "+
						"directories and nothing else", tree, e.Name(), e.Type()))
				continue
			}
			if err := slug.Validate(e.Name()); err != nil {
				problems = append(problems, fmt.Sprintf(
					"%s/%s is not a valid slug: %v", tree, e.Name(), err))
			}
		}
	}

	// Every manifest row landed, with the right bytes.
	var files int
	var bytes int64
	for _, e := range m.Entries {
		abs := filepath.Join(dest, filepath.FromSlash(e.Path))
		st, err := os.Lstat(abs)
		if err != nil {
			problems = append(problems, fmt.Sprintf("missing from destination: %s", e.Path))
			continue
		}
		if st.Mode().Type() != 0 {
			problems = append(problems, fmt.Sprintf(
				"%s is not a regular file in the destination (mode %s)", e.Path, st.Mode().Type()))
			continue
		}
		sum, size, err := hashFile(abs)
		if err != nil {
			problems = append(problems, fmt.Sprintf("hash %s: %v", e.Path, err))
			continue
		}
		if sum != e.SHA256 {
			problems = append(problems, fmt.Sprintf(
				"content differs: %s (destination %s, manifest %s)", e.Path, sum, e.SHA256))
			continue
		}
		files++
		bytes += size
	}

	// 🔴 TEMPLATES IS ASSERTED BY WHAT MERGE NEVER WRITES, not by comparing two
	// trees. The destination's Templates/ is its own override tree and the
	// source's is a different vault's; there is no correct comparison between
	// them. What IS assertable is that no row of this manifest targets
	// Templates/ at all.
	for _, e := range m.Entries {
		if strings.HasPrefix(e.Path, "Templates/") {
			problems = append(problems, fmt.Sprintf(
				"manifest row %s targets Templates/, which merge never writes", e.Path))
		}
	}

	remotes, err := mergeAffirmRemotes(dest, p.AffirmDestinationRemotes)
	if err != nil {
		return nil, err
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("merge verification failed:\n  - %s",
			strings.Join(problems, "\n  - "))
	}

	return &vaultMergeVerifyResult{
		Action:             "verify",
		Source:             p.Source,
		Destination:        dest,
		Slugs:              m.Slugs,
		ManifestSHA256:     m.SHA256,
		FilesVerified:      files,
		BytesVerified:      bytes,
		DestinationRemotes: remotes,
		GatesChecked: []string{
			"manifest bind (source re-hashed, and the untouched destination state recomputed)",
			"leak gate 1: destination slug set equals the pre-merge snapshot union the allow-list, carried by the digest",
			"leak gate 1: every project-tree entry is a directory with a valid slug name",
			"every allow-listed slug arrived",
			"every manifest row present with matching content",
			"Knowledge/ and Audits/ gained nothing beyond the manifest, by set difference in the digest",
			"no manifest row targets Templates/",
			"destination remotes match the affirmed set (ListRemotes error is a refusal)",
		},
		Notes: []string{
			"Body-text mentions of other projects and host-rooted paths inside merged " +
				"documents are advisory and never fail verify. A slug named in prose is " +
				"not a slug that travelled.",
			"The source vault is unchanged. Merge has no purge action.",
		},
		Complete: true,
	}, nil
}

func containsMergeSlug(slugs []string, want string) bool {
	for _, s := range slugs {
		if s == want {
			return true
		}
	}
	return false
}

// mergeManifestDigest canonicalises a manifest and returns its sha256.
//
// The preimage binds four things, and the last two are what make merge's verify
// possible at all:
//
//  1. the REQUEST — the slug allow-list and both include flags
//  2. the source ENTRIES that travel, by path, hash and size
//  3. DestSlugsUntouched — destination project names outside the allow-list
//  4. DestGlobalUntouched — destination Knowledge/Audits paths this merge does
//     not add
//
// Fields are NUL-separated and rows newline-delimited so no path can forge a
// row boundary, and the preimage opens with a format tag distinct from split's
// so a digest minted by one tool can never validate against the other.
func mergeManifestDigest(m *mergeManifest) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", mergeManifestFormat)
	fmt.Fprintf(h, "slugs\x00%s\n", strings.Join(m.Slugs, "\x00"))
	fmt.Fprintf(h, "include_learnings\x00%t\n", m.IncludeLearnings)
	fmt.Fprintf(h, "include_audits\x00%t\n", m.IncludeAudits)
	for _, e := range m.Entries {
		fmt.Fprintf(h, "entry\x00%s\x00%s\x00%s\n", e.Path, e.SHA256, strconv.FormatInt(e.Size, 10))
	}
	for _, s := range m.DestSlugsUntouched {
		fmt.Fprintf(h, "dest_slug\x00%s\n", s)
	}
	for _, p := range m.DestGlobalUntouched {
		fmt.Fprintf(h, "dest_global\x00%s\n", p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
