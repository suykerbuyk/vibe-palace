// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package context

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/skills"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/templates"
)

// SkillDir is the directory-form view of a skill: the parsed SKILL.md
// frontmatter plus body, plus the deduplicated set of reference file
// basenames (without ".md") discovered across all 5 precedence tiers.
// Root is the absolute filesystem path (or embedded "templates/"-prefixed
// path, signalled by an empty Root when the source is embedded) of the
// directory that provided SKILL.md. Higher tiers supply SKILL.md;
// references fall through per-file (see ResolveSkillSection).
type SkillDir struct {
	Frontmatter    skills.SkillFrontmatter
	SkillMDBody    []byte
	ReferenceNames []string
	Root           string
}

// ResourceInfo describes a named resource and which precedence level provided it.
type ResourceInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"` // "room", "wing", "project", "vault", or "embedded"
}

// Resolver resolves resources using up to 5-tier precedence:
// Room > Wing > Project > Vault > Embedded.
// When wing and room are empty, the 3-tier subset (Project > Vault > Embedded) is used.
type Resolver struct {
	defaults  embed.FS
	vaultRoot string
}

// NewResolver creates a Resolver with the compiled-in defaults and vault root path.
func NewResolver(vaultRoot string) *Resolver {
	return &Resolver{
		defaults:  templates.FS(),
		vaultRoot: vaultRoot,
	}
}

// Resolve returns (content, source, error) for a given resource identifier
// using 3-tier precedence (Project > Vault > Embedded).
// Source is one of: "project", "vault", "embedded".
//
// Resource identifiers:
//
//	"workflow"        → workflow.md
//	"resume"          → resume.md
//	"command:{name}"  → commands/{name}.md
//	"skill:{name}"    → skills/{name}.md
func (r *Resolver) Resolve(resource, project string) (string, string, error) {
	return r.ResolveScoped(resource, project, "", "")
}

// ResolveScoped returns (content, source, error) using 5-tier precedence:
// Room > Wing > Project > Vault > Embedded.
// When wing and room are empty, only the 3-tier subset is checked.
func (r *Resolver) ResolveScoped(resource, project, wing, room string) (string, string, error) {
	if err := validateScope(wing, room); err != nil {
		return "", "", err
	}

	resType, name, dir, err := parseResource(resource)
	if err != nil {
		return "", "", err
	}

	// For non-command/skill resources (workflow, resume), fall through to path-based resolution.
	if dir == "" {
		return r.resolveByPath(resType+".md", project, wing, room)
	}

	// Directory-form skills: resolve skills/<name>/SKILL.md per the
	// vps-skill-artifacts-cross-ide contract. No flat-file fallback —
	// the embedded corpus ships only directory-form skills, and any
	// vault/project override must follow the same layout.
	if resType == "skill" {
		rel := path.Join(dir, name, "SKILL.md")
		return r.resolveAtRel(rel, project, wing, room, resource)
	}

	filename := name + ".md"

	// Tier 1 — Room (only if both wing and room are set).
	if wing != "" && room != "" && project != "" {
		roomPath := filepath.Join(r.vaultRoot, "Projects", project, dir, wing, room, filename)
		if data, err := os.ReadFile(roomPath); err == nil {
			return r.expandScoped(string(data), project, wing, room), "room", nil
		}
	}

	// Tier 2 — Wing (only if wing is set).
	if wing != "" && project != "" {
		wingPath := filepath.Join(r.vaultRoot, "Projects", project, dir, wing, ".wing", filename)
		if data, err := os.ReadFile(wingPath); err == nil {
			return r.expandScoped(string(data), project, wing, room), "wing", nil
		}
	}

	// Tier 3 — Project.
	if project != "" {
		projPath := filepath.Join(r.vaultRoot, "Projects", project, dir, filename)
		if data, err := os.ReadFile(projPath); err == nil {
			return r.expandScoped(string(data), project, wing, room), "project", nil
		}
	}

	// Tier 4 — Vault template.
	vaultPath := filepath.Join(r.vaultRoot, "Templates", dir, filename)
	if data, err := os.ReadFile(vaultPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "vault", nil
	}

	// Tier 5 — Embedded default.
	embedPath := path.Join("templates", dir, filename)
	if data, err := fs.ReadFile(r.defaults, embedPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "embedded", nil
	}

	return "", "", fmt.Errorf("resource %q not found at any precedence level", resource)
}

// resolveByPath handles non-command/skill resources (workflow, resume) which
// don't participate in wing/room scoping.
func (r *Resolver) resolveByPath(relPath, project, wing, room string) (string, string, error) {
	if project != "" {
		projPath := filepath.Join(r.vaultRoot, "Projects", project, relPath)
		if data, err := os.ReadFile(projPath); err == nil {
			return r.expandScoped(string(data), project, wing, room), "project", nil
		}
	}
	vaultPath := filepath.Join(r.vaultRoot, "Templates", relPath)
	if data, err := os.ReadFile(vaultPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "vault", nil
	}
	embedPath := path.Join("templates", relPath)
	if data, err := fs.ReadFile(r.defaults, embedPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "embedded", nil
	}
	return "", "", fmt.Errorf("resource %q not found at any precedence level", relPath)
}

// ListResources returns deduplicated resource names using 3-tier precedence.
func (r *Resolver) ListResources(resourceType, project string) ([]ResourceInfo, error) {
	return r.ListResourcesScoped(resourceType, project, "", "")
}

// ListResourcesScoped returns deduplicated resource names merged across up to
// 5 precedence tiers: Room > Wing > Project > Vault > Embedded.
// Higher-precedence sources shadow lower when names collide.
func (r *Resolver) ListResourcesScoped(resourceType, project, wing, room string) ([]ResourceInfo, error) {
	if err := validateScope(wing, room); err != nil {
		return nil, err
	}

	var dir string
	switch resourceType {
	case "command":
		dir = "commands"
	case "skill":
		dir = "skills"
	default:
		return nil, fmt.Errorf("unsupported resource type for listing: %q", resourceType)
	}

	seen := make(map[string]bool)
	var result []ResourceInfo

	addTier := func(names []string, source string) {
		for _, name := range names {
			if !seen[name] {
				seen[name] = true
				result = append(result, ResourceInfo{Name: name, Source: source})
			}
		}
	}

	if resourceType == "skill" {
		// Directory-form enumeration: one entry per skill directory that
		// contains a SKILL.md, not per .md file. Higher tiers shadow
		// lower tiers by name.
		listDirs := func(root string) []string {
			entries, err := os.ReadDir(root)
			if err != nil {
				return nil
			}
			var names []string
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if _, err := os.Stat(filepath.Join(root, e.Name(), "SKILL.md")); err == nil {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			return names
		}

		if wing != "" && room != "" && project != "" {
			addTier(listDirs(filepath.Join(r.vaultRoot, "Projects", project, dir, wing, room)), "room")
		}
		if wing != "" && project != "" {
			addTier(listDirs(filepath.Join(r.vaultRoot, "Projects", project, dir, wing, ".wing")), "wing")
		}
		if project != "" {
			addTier(listDirs(filepath.Join(r.vaultRoot, "Projects", project, dir)), "project")
		}
		addTier(listDirs(filepath.Join(r.vaultRoot, "Templates", dir)), "vault")

		// Tier 5 — Embedded defaults.
		if entries, err := fs.ReadDir(r.defaults, path.Join("templates", dir)); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if _, err := fs.ReadFile(r.defaults, path.Join("templates", dir, e.Name(), "SKILL.md")); err != nil {
					continue
				}
				if !seen[e.Name()] {
					seen[e.Name()] = true
					result = append(result, ResourceInfo{Name: e.Name(), Source: "embedded"})
				}
			}
		}

		sort.Slice(result, func(i, j int) bool {
			return result[i].Name < result[j].Name
		})
		return result, nil
	}

	// Tier 1 — Room.
	if wing != "" && room != "" && project != "" {
		roomDir := filepath.Join(r.vaultRoot, "Projects", project, dir, wing, room)
		addTier(listMDFiles(roomDir), "room")
	}

	// Tier 2 — Wing.
	if wing != "" && project != "" {
		wingDir := filepath.Join(r.vaultRoot, "Projects", project, dir, wing, ".wing")
		addTier(listMDFiles(wingDir), "wing")
	}

	// Tier 3 — Project.
	if project != "" {
		projDir := filepath.Join(r.vaultRoot, "Projects", project, dir)
		addTier(listMDFiles(projDir), "project")
	}

	// Tier 4 — Vault templates.
	vaultDir := filepath.Join(r.vaultRoot, "Templates", dir)
	addTier(listMDFiles(vaultDir), "vault")

	// Tier 5 — Embedded defaults.
	embedDir := path.Join("templates", dir)
	if entries, err := fs.ReadDir(r.defaults, embedDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if !seen[name] {
				seen[name] = true
				result = append(result, ResourceInfo{Name: name, Source: "embedded"})
			}
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result, nil
}

// EmbeddedContent returns the raw embedded (tier-5) content for a resource,
// without placeholder expansion. Returns an error if no embedded copy exists.
func (r *Resolver) EmbeddedContent(resource string) (string, error) {
	relPath, err := resourceToPath(resource)
	if err != nil {
		return "", err
	}
	data, err := fs.ReadFile(r.defaults, path.Join("templates", relPath))
	if err != nil {
		return "", fmt.Errorf("no embedded copy for %q: %w", resource, err)
	}
	return string(data), nil
}

// VaultContent returns the raw vault (tier-4) content for a resource, without
// placeholder expansion. Returns (content, true, nil) if present, ("", false,
// nil) if absent, and ("", false, err) only on read errors other than NotExist.
func (r *Resolver) VaultContent(resource string) (string, bool, error) {
	relPath, err := resourceToPath(resource)
	if err != nil {
		return "", false, err
	}
	p := filepath.Join(r.vaultRoot, "Templates", relPath)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

// VaultPath returns the filesystem path where the vault (tier-4) copy of the
// given resource would live. The file may or may not exist.
func (r *Resolver) VaultPath(resource string) (string, error) {
	relPath, err := resourceToPath(resource)
	if err != nil {
		return "", err
	}
	return filepath.Join(r.vaultRoot, "Templates", relPath), nil
}

// VaultRoot returns the vault root this resolver was configured with.
func (r *Resolver) VaultRoot() string {
	return r.vaultRoot
}

// ListEmbedded returns the names of embedded resources of the given type.
//
// For "command" the result is one entry per embedded commands/*.md file,
// without the ".md" suffix.
//
// For "skill" the result is one entry per file inside every
// skills/<name>/ directory, using the nested form
// "<name>/<relpath-within-the-skill-dir>" — i.e. "startup-analyst/SKILL.md"
// and "startup-analyst/references/capex-opex.md". This per-file fan-out is
// what commands.Plan consumes for per-file diffs. Callers that want the
// directory-level surface (e.g. `vp skills list`) use ListResourcesScoped
// which continues to return skill *names*.
func (r *Resolver) ListEmbedded(resourceType string) ([]string, error) {
	var dir string
	switch resourceType {
	case "command":
		dir = "commands"
	case "skill":
		dir = "skills"
	default:
		return nil, fmt.Errorf("unsupported resource type for listing: %q", resourceType)
	}
	entries, err := fs.ReadDir(r.defaults, path.Join("templates", dir))
	if err != nil {
		return nil, nil
	}
	var names []string
	if resourceType == "skill" {
		// Walk every skill directory and emit one entry per markdown
		// file inside (SKILL.md + references/*.md).
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillRoot := path.Join("templates", dir, e.Name())
			walkErr := fs.WalkDir(r.defaults, skillRoot, func(p string, d fs.DirEntry, werr error) error {
				if werr != nil {
					return werr
				}
				if d.IsDir() {
					return nil
				}
				if !strings.HasSuffix(p, ".md") {
					return nil
				}
				rel := strings.TrimPrefix(p, skillRoot+"/")
				names = append(names, path.Join(e.Name(), rel))
				return nil
			})
			if walkErr != nil {
				return nil, fmt.Errorf("walk embedded skill %s: %w", e.Name(), walkErr)
			}
		}
		sort.Strings(names)
		return names, nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names, nil
}

// expandScoped replaces template placeholders including wing/room.
func (r *Resolver) expandScoped(content, project, wing, room string) string {
	s := strings.ReplaceAll(content, "{{PROJECT}}", project)
	s = strings.ReplaceAll(s, "{{WING}}", wing)
	s = strings.ReplaceAll(s, "{{ROOM}}", room)
	s = strings.ReplaceAll(s, "{{DATE}}", time.Now().Format("2006-01-02"))
	return s
}

// parseResource extracts resource type, name, and directory from a resource identifier.
// For "command:foo" → ("command", "foo", "commands", nil).
// For "workflow" → ("workflow", "", "", nil) — dir is empty for non-scoped resources.
func parseResource(resource string) (resType, name, dir string, err error) {
	switch {
	case resource == "workflow":
		return "workflow", "", "", nil
	case resource == "resume":
		return "resume", "", "", nil
	case strings.HasPrefix(resource, "command:"):
		name = strings.TrimPrefix(resource, "command:")
		if err := validateResourceName(name); err != nil {
			return "", "", "", err
		}
		return "command", name, "commands", nil
	case strings.HasPrefix(resource, "skill:"):
		rest := strings.TrimPrefix(resource, "skill:")
		// Accept either the directory form ("skill:<name>") which resolves
		// to skills/<name>/SKILL.md, or the nested per-file form
		// ("skill:<name>/<relpath>") which addresses any markdown file
		// under skills/<name>/. Only the first segment is subject to
		// validateResourceName; the rest is a cleaned relative path
		// guarded against traversal.
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			if err := validateResourceName(rest); err != nil {
				return "", "", "", err
			}
			return "skill", rest, "skills", nil
		}
		head := rest[:slash]
		tail := rest[slash+1:]
		if err := validateResourceName(head); err != nil {
			return "", "", "", err
		}
		if err := validateSkillRelPath(tail); err != nil {
			return "", "", "", err
		}
		// Encode the composite name as "<skill>/<relpath>" so downstream
		// callers can round-trip through resourceToPath. Name retains
		// the slash so callers that only look at `name` (e.g. commands.Plan
		// for the Change.Name field) surface a meaningful identifier.
		return "skill", head + "/" + tail, "skills", nil
	default:
		return "", "", "", fmt.Errorf("unknown resource type: %q", resource)
	}
}

// resourceToPath converts a resource identifier to a relative file path.
// Skills resolve to their directory-form SKILL.md; commands and
// workflow/resume keep the flat-file layout.
func resourceToPath(resource string) (string, error) {
	resType, name, dir, err := parseResource(resource)
	if err != nil {
		return "", err
	}
	if dir == "" {
		return resType + ".md", nil
	}
	if resType == "skill" {
		// Nested form: name carries "<skill>/<relpath>" — resolve to
		// skills/<skill>/<relpath> directly. Directory form (no slash)
		// continues to resolve to skills/<name>/SKILL.md.
		if strings.Contains(name, "/") {
			return filepath.Join(dir, filepath.FromSlash(name)), nil
		}
		return filepath.Join(dir, name, "SKILL.md"), nil
	}
	return filepath.Join(dir, name+".md"), nil
}

// validateSkillRelPath rejects skill-reference tail paths that could
// escape the skill directory via traversal. Accepts forward-slash
// separators only (the embedded FS uses them regardless of host OS).
func validateSkillRelPath(rel string) error {
	if rel == "" {
		return fmt.Errorf("skill relative path must not be empty")
	}
	if strings.Contains(rel, `\`) {
		return fmt.Errorf("skill relative path must use forward slashes: %q", rel)
	}
	if strings.HasPrefix(rel, "/") {
		return fmt.Errorf("skill relative path must not be absolute: %q", rel)
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("skill relative path must not contain empty or '..' segments: %q", rel)
		}
	}
	return nil
}

// validateScope checks that wing/room parameters are valid.
func validateScope(wing, room string) error {
	if room != "" && wing == "" {
		return fmt.Errorf("room %q requires a wing to be specified", room)
	}
	if wing != "" {
		if err := slug.Validate(wing); err != nil {
			return fmt.Errorf("invalid wing: %w", err)
		}
	}
	if room != "" {
		if err := slug.Validate(room); err != nil {
			return fmt.Errorf("invalid room: %w", err)
		}
	}
	return nil
}

// validateResourceName rejects names that could cause path traversal.
func validateResourceName(name string) error {
	if name == "" {
		return fmt.Errorf("resource name must not be empty")
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("resource name must not contain path separators: %q", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("resource name must not contain '..': %q", name)
	}
	return nil
}

// resolveAtRel walks the 5-tier precedence chain for an already-resolved
// templates-root-relative path (forward slashes). Used by directory-form
// skill resolution where the filename includes subdirectories. The
// returned source is the same room/wing/project/vault/embedded label
// used elsewhere; resource is the original resource identifier used
// only to format the "not found" error.
func (r *Resolver) resolveAtRel(rel, project, wing, room, resource string) (string, string, error) {
	// rel is "skills/<name>/SKILL.md". For wing/room tiers the
	// directory-as-override-unit slots in between "skills/" and
	// "<name>/…". We splice by hand so the layout matches command
	// scoping: skills/<wing>/<room>/<name>/SKILL.md, etc.
	splitScoped := func(prefix ...string) string {
		// rel begins with "skills/"; inject scope segments right after.
		const head = "skills/"
		if !strings.HasPrefix(rel, head) {
			// Shouldn't happen for current callers, but fall back to
			// the raw join so future uses don't silently misresolve.
			segs := append([]string{}, prefix...)
			return filepath.Join(append(segs, filepath.FromSlash(rel))...)
		}
		tail := strings.TrimPrefix(rel, head)
		segs := []string{"skills"}
		segs = append(segs, prefix...)
		segs = append(segs, filepath.FromSlash(tail))
		return filepath.Join(segs...)
	}

	if wing != "" && room != "" && project != "" {
		p := filepath.Join(r.vaultRoot, "Projects", project, splitScoped(wing, room))
		if data, err := os.ReadFile(p); err == nil {
			return r.expandScoped(string(data), project, wing, room), "room", nil
		}
	}
	if wing != "" && project != "" {
		p := filepath.Join(r.vaultRoot, "Projects", project, splitScoped(wing, ".wing"))
		if data, err := os.ReadFile(p); err == nil {
			return r.expandScoped(string(data), project, wing, room), "wing", nil
		}
	}
	if project != "" {
		projPath := filepath.Join(r.vaultRoot, "Projects", project, filepath.FromSlash(rel))
		if data, err := os.ReadFile(projPath); err == nil {
			return r.expandScoped(string(data), project, wing, room), "project", nil
		}
	}
	vaultPath := filepath.Join(r.vaultRoot, "Templates", filepath.FromSlash(rel))
	if data, err := os.ReadFile(vaultPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "vault", nil
	}
	embedPath := path.Join("templates", rel)
	if data, err := fs.ReadFile(r.defaults, embedPath); err == nil {
		return r.expandScoped(string(data), project, wing, room), "embedded", nil
	}
	return "", "", fmt.Errorf("resource %q not found at any precedence level", resource)
}

// ResolveSkillDir resolves a directory-form skill by name. The SKILL.md
// comes from the highest tier that has it; ReferenceNames is the
// deduplicated union of reference basenames found in every tier that
// supplies the skill directory (override tiers shadow lower tiers for
// the SKILL.md entry point, but references still fall through
// per-file via ResolveSkillSection). Lifetime defaults to "postural"
// when the frontmatter omits it.
func (r *Resolver) ResolveSkillDir(name, project, wing, room string) (SkillDir, string, error) {
	if err := validateScope(wing, room); err != nil {
		return SkillDir{}, "", err
	}
	if err := validateResourceName(name); err != nil {
		return SkillDir{}, "", err
	}

	type tier struct {
		source string
		root   string // absolute path ("" means embedded)
	}
	var tiers []tier
	if wing != "" && room != "" && project != "" {
		tiers = append(tiers, tier{"room", filepath.Join(r.vaultRoot, "Projects", project, "skills", wing, room, name)})
	}
	if wing != "" && project != "" {
		tiers = append(tiers, tier{"wing", filepath.Join(r.vaultRoot, "Projects", project, "skills", wing, ".wing", name)})
	}
	if project != "" {
		tiers = append(tiers, tier{"project", filepath.Join(r.vaultRoot, "Projects", project, "skills", name)})
	}
	tiers = append(tiers, tier{"vault", filepath.Join(r.vaultRoot, "Templates", "skills", name)})
	tiers = append(tiers, tier{"embedded", ""})

	var (
		body        []byte
		primary     string
		primaryRoot string
		found       bool
	)
	refSet := map[string]struct{}{}

	for _, t := range tiers {
		var skillMD []byte
		var refNames []string
		if t.source == "embedded" {
			data, err := fs.ReadFile(r.defaults, path.Join("templates", "skills", name, "SKILL.md"))
			if err != nil {
				continue
			}
			skillMD = data
			// enumerate embedded references
			entries, err := fs.ReadDir(r.defaults, path.Join("templates", "skills", name, "references"))
			if err == nil {
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
						refNames = append(refNames, strings.TrimSuffix(e.Name(), ".md"))
					}
				}
			}
		} else {
			data, err := os.ReadFile(filepath.Join(t.root, "SKILL.md"))
			if err != nil {
				// even if SKILL.md is absent at this tier, references
				// here still participate in fallthrough only when a
				// higher tier supplied the dir; skip tiers that don't
				// have the skill directory at all.
				if _, statErr := os.Stat(t.root); statErr != nil {
					continue
				}
				// directory exists but no SKILL.md — still collect refs
			} else {
				skillMD = data
			}
			refs, err := os.ReadDir(filepath.Join(t.root, "references"))
			if err == nil {
				for _, e := range refs {
					if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
						refNames = append(refNames, strings.TrimSuffix(e.Name(), ".md"))
					}
				}
			}
		}

		if !found && skillMD != nil {
			body = skillMD
			primary = t.source
			primaryRoot = t.root
			found = true
		}
		for _, n := range refNames {
			refSet[n] = struct{}{}
		}
	}

	if !found {
		return SkillDir{}, "", fmt.Errorf("skill %q not found at any precedence level", name)
	}

	fm, mdBody, err := skills.Parse(body)
	if err != nil && err != skills.ErrNoFrontmatter {
		return SkillDir{}, primary, fmt.Errorf("parse SKILL.md for %q: %w", name, err)
	}
	// If frontmatter absent, body == original input; keep that.
	if err == skills.ErrNoFrontmatter {
		mdBody = body
	}

	refList := make([]string, 0, len(refSet))
	for n := range refSet {
		refList = append(refList, n)
	}
	sort.Strings(refList)

	return SkillDir{
		Frontmatter:    fm,
		SkillMDBody:    mdBody,
		ReferenceNames: refList,
		Root:           primaryRoot,
	}, primary, nil
}

// ResolveSkillSection performs a per-file 5-tier walk for
// skills/<name>/references/<section>.md. Fallthrough is independent of
// which tier supplied SKILL.md: a project override of SKILL.md that
// omits a given reference will transparently fall back to vault or
// embedded for that reference.
func (r *Resolver) ResolveSkillSection(name, section, project, wing, room string) ([]byte, string, error) {
	if err := validateScope(wing, room); err != nil {
		return nil, "", err
	}
	if err := validateResourceName(name); err != nil {
		return nil, "", err
	}
	if err := validateResourceName(section); err != nil {
		return nil, "", err
	}
	rel := path.Join("skills", name, "references", section+".md")
	if wing != "" && room != "" && project != "" {
		p := filepath.Join(r.vaultRoot, "Projects", project, "skills", wing, room, name, "references", section+".md")
		if data, err := os.ReadFile(p); err == nil {
			return data, "room", nil
		}
	}
	if wing != "" && project != "" {
		p := filepath.Join(r.vaultRoot, "Projects", project, "skills", wing, ".wing", name, "references", section+".md")
		if data, err := os.ReadFile(p); err == nil {
			return data, "wing", nil
		}
	}
	if project != "" {
		p := filepath.Join(r.vaultRoot, "Projects", project, "skills", name, "references", section+".md")
		if data, err := os.ReadFile(p); err == nil {
			return data, "project", nil
		}
	}
	vp := filepath.Join(r.vaultRoot, "Templates", "skills", name, "references", section+".md")
	if data, err := os.ReadFile(vp); err == nil {
		return data, "vault", nil
	}
	if data, err := fs.ReadFile(r.defaults, path.Join("templates", rel)); err == nil {
		return data, "embedded", nil
	}
	return nil, "", fmt.Errorf("skill section %q/%q not found at any precedence level", name, section)
}

// listMDFiles returns sorted basenames (without .md) of markdown files in a
// directory. Returns nil if the directory does not exist. README.md is skipped:
// every scaffolded commands/ tier carries a README.md override-guide stub (see
// internal/templates/readme_stub.go) which is documentation, not a command —
// listing it would surface a bogus "README" command (and slash-command shim).
func listMDFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(names)
	return names
}
