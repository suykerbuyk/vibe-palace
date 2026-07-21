// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// validateSlugs validates all provided slugs, returning the first error found.
func validateSlugs(slugs ...string) error {
	for _, s := range slugs {
		if err := slug.Validate(s); err != nil {
			return err
		}
	}
	return nil
}

// DrawerDir returns the path to a drawer directory:
// {vault}/palace/{project}/drawers/{wing}/{room}
func (v *Vault) DrawerDir(project, wing, room string) (string, error) {
	if err := validateSlugs(project, wing, room); err != nil {
		return "", err
	}
	return filepath.Join(v.Root, "palace", project, "drawers", wing, room), nil
}

// DrawerFile returns the path to a drawer JSONL file:
// {vault}/palace/{project}/drawers/{wing}/{room}/drawers.jsonl
func (v *Vault) DrawerFile(project, wing, room string) (string, error) {
	dir, err := v.DrawerDir(project, wing, room)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "drawers.jsonl"), nil
}

// KGEntitiesFile returns the path to the knowledge graph entities file:
// {vault}/palace/{project}/kg/entities.jsonl
func (v *Vault) KGEntitiesFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "palace", project, "kg", "entities.jsonl"), nil
}

// KGTriplePath returns the path to a knowledge graph triple file:
// {vault}/palace/{project}/kg/triples/{enc_subj}--{enc_pred}--{enc_obj}.json
//
// Each component is encoded by encodeTripleComponent into a FLAT, portable,
// injective on-disk name (slug + content hash). The encoded parts contain no
// "/", "\\", ":", control chars, ".." or "--", so the filename is single-level
// and the "--" delimiter is unambiguous. The RAW subject/predicate/object are
// retained in the JSON body (see Triple), so nothing is lost.
func (v *Vault) KGTriplePath(project, subject, predicate, object string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	// Reject empty RAW components: every encoded component carries a "_<hash>"
	// suffix so it is never itself empty, so emptiness must be checked on the
	// raw input (a blank subject/predicate/object is a meaningless triple).
	for _, c := range []struct{ name, val string }{
		{"subject", subject}, {"predicate", predicate}, {"object", object},
	} {
		if strings.TrimSpace(c.val) == "" {
			return "", fmt.Errorf("%s must not be empty", c.name)
		}
	}
	subj := encodeTripleComponent(subject)
	pred := encodeTripleComponent(predicate)
	obj := encodeTripleComponent(object)
	// Defense-in-depth: the encoder collapses "-" runs so no component can carry
	// the "--" delimiter, but assert it so the filename split stays unambiguous.
	for _, c := range []struct{ name, val string }{
		{"subject", subj}, {"predicate", pred}, {"object", obj},
	} {
		if strings.Contains(c.val, "--") {
			return "", fmt.Errorf("%s %q must not contain \"--\" delimiter", c.name, c.val)
		}
	}
	filename := subj + "--" + pred + "--" + obj + ".json"

	// Containment assertion (defense-in-depth): the flat encoder already strips
	// "/", "\\", ":", control chars and "..", but assert it anyway — this is the
	// traversal fix and mirrors the Contains("..")+Clean+prefix idiom used by
	// DocFile/MemoryFile/AbsorbedFile above. The joined path must be
	// Clean-stable and stay directly under triplesDir (no interior separator,
	// no "..").
	triplesDir := filepath.Join(v.Root, "palace", project, "kg", "triples")
	full := filepath.Join(triplesDir, filename)
	if strings.Contains(filename, "..") || full != filepath.Clean(full) || filepath.Dir(full) != triplesDir {
		return "", fmt.Errorf("triple filename %q escapes triples dir", filename)
	}
	return full, nil
}

// LocalDir returns the path to a project's machine-local directory:
// {vault}/palace/{project}/.local
func (v *Vault) LocalDir(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "palace", project, ".local"), nil
}

// VaultLocalDir returns the path to the vault-wide machine-local directory:
// {vault}/palace/.local
func (v *Vault) VaultLocalDir() string {
	return filepath.Join(v.Root, "palace", ".local")
}

// EnsureDir creates the directory tree at path if it does not exist.
// Uses os.MkdirAll with 0755 permissions. Idempotent.
func EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

// KGTriplesDir returns the path to the knowledge graph triples directory:
// {vault}/palace/{project}/kg/triples
func (v *Vault) KGTriplesDir(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "palace", project, "kg", "triples"), nil
}

// SessionDir returns the path to a project's sessions directory:
// {vault}/Projects/{project}/sessions
func (v *Vault) SessionDir(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "sessions"), nil
}

// datePattern matches YYYY-MM-DD date strings.
var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// SessionStem returns the host-qualified session identity stem
// "<date>-<fp>-<NN>", or legacy "<date>-<NN>" when fp=="". Single source of
// truth for session filename and meta.ID construction.
func SessionStem(date, fp string, iteration int) string {
	if fp != "" {
		return fmt.Sprintf("%s-%s-%02d", date, fp, iteration)
	}
	return fmt.Sprintf("%s-%02d", date, iteration)
}

// SessionGlobPrefix returns the glob-ready prefix for one host's sessions on a
// date: "<date>-<fp>-" (or legacy "<date>-" when fp=="").
func SessionGlobPrefix(date, fp string) string {
	if fp != "" {
		return date + "-" + fp + "-"
	}
	return date + "-"
}

// SessionRelPath returns the VAULT-RELATIVE, slash-separated path to a session
// markdown file. When fp (the writer fingerprint, see surface.WriterFingerprint)
// is non-empty the host-scoped layout is used:
//
//	Projects/{project}/sessions/YYYY-MM-DD-<fp>-NN.md
//
// When fp is empty the legacy host-agnostic layout is used, so pre-existing
// notes written before fingerprinting remain readable:
//
//	Projects/{project}/sessions/YYYY-MM-DD-NN.md
//
// This is the ONE definition of where a session note lives; SessionFile is the
// absolute form of it, and note_path in the frontmatter is this string verbatim.
// It is vault-relative because a session note is read on machines other than the
// one that wrote it: the vault syncs everywhere, one project lives at different
// absolute paths on different hosts (and in different subtrees on one host), and
// note_path is persisted — so an absolute path here is a fact about the writing
// host and a lie everywhere else. It is also exactly the form vp_vault_read
// takes, so relative is the useful answer, not merely the safe one.
func (v *Vault) SessionRelPath(project, date, fp string, iteration int) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	if !datePattern.MatchString(date) {
		return "", fmt.Errorf("date %q must be in YYYY-MM-DD format", date)
	}
	if iteration < 1 {
		return "", fmt.Errorf("iteration must be >= 1, got %d", iteration)
	}
	filename := SessionStem(date, fp, iteration) + ".md"
	// path.Join, not filepath.Join: this is a vault-relative slash path on every
	// OS, not a host path. On Windows filepath.Join would emit backslashes and
	// bake a host convention into a synced document.
	return "Projects/" + project + "/sessions/" + filename, nil
}

// SessionFile returns the ABSOLUTE path to a session markdown file, derived from
// SessionRelPath so the layout has exactly one definition. See SessionRelPath.
func (v *Vault) SessionFile(project, date, fp string, iteration int) (string, error) {
	rel, err := v.SessionRelPath(project, date, fp, iteration)
	if err != nil {
		return "", err
	}
	return filepath.Join(v.Root, filepath.FromSlash(rel)), nil
}

// TasksDir returns the path to a project's tasks directory:
// {vault}/Projects/{project}/tasks
func (v *Vault) TasksDir(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "tasks"), nil
}

// TaskFile returns the path to a task markdown file:
// {vault}/Projects/{project}/tasks/{slug}.md
func (v *Vault) TaskFile(project, slug string) (string, error) {
	if err := validateSlugs(project, slug); err != nil {
		return "", err
	}
	return filepath.Join(v.Root, "Projects", project, "tasks", slug+".md"), nil
}

// TaskDoneDir returns the path to a project's completed tasks directory:
// {vault}/Projects/{project}/tasks/done
func (v *Vault) TaskDoneDir(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "tasks", "done"), nil
}

// TaskCancelledDir returns the path to a project's cancelled tasks directory:
// {vault}/Projects/{project}/tasks/cancelled
func (v *Vault) TaskCancelledDir(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "tasks", "cancelled"), nil
}

// ProjectConfigFile returns the path to a project's config file:
// {vault}/Projects/{project}/config.toml
func (v *Vault) ProjectConfigFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "config.toml"), nil
}

// ResumeFile returns the path to a project's resume file:
// {vault}/Projects/{project}/resume.md
func (v *Vault) ResumeFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "resume.md"), nil
}

// ProjectDir returns the path to a project's root directory:
// {vault}/Projects/{project}
func (v *Vault) ProjectDir(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project), nil
}

// CommitMsgFile returns the path to a project's vault commit-message file:
// {vault}/Projects/{project}/commit.msg
func (v *Vault) CommitMsgFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "commit.msg"), nil
}

// CommitLogFile returns the path to a project's vault commit-log append-log:
// {vault}/Projects/{project}/commit-log.md. Unlike commit.msg (a
// single-overwrite mirror of the LATEST message), commit-log.md is the
// permanent, append-only history — each landed commit's full message is
// appended, so `git log -p` over it recovers every message ever archived.
func (v *Vault) CommitLogFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "commit-log.md"), nil
}

// CommitLogAnchorFile returns the path to a project's vault-resident
// last-archived anchor: {vault}/Projects/{project}/commit-log.anchor. It holds
// the SHA of the commit through which commit-log.md is up to date. It lives in
// the VAULT (not .vibe-palace/) so vp_vault_sync commits it each wrap without
// re-dirtying a coherency-only feature-branch project tree.
func (v *Vault) CommitLogAnchorFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "commit-log.anchor"), nil
}

// IterationsFile returns the path to a project's iterations file:
// {vault}/Projects/{project}/iterations.md
func (v *Vault) IterationsFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "iterations.md"), nil
}

// WorkflowFile returns the path to a project's workflow file:
// {vault}/Projects/{project}/workflow.md
func (v *Vault) WorkflowFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "workflow.md"), nil
}

// KnowledgeFile returns the path to a project's knowledge file:
// {vault}/Projects/{project}/knowledge.md
func (v *Vault) KnowledgeFile(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "knowledge.md"), nil
}

// DocFile returns the path to a project-scoped doc file:
// {vault}/Projects/{project}/doc/{rel}. rel must be a simple relative
// filename like "architecture.md" — no traversal, no absolute paths.
func (v *Vault) DocFile(project, rel string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	if rel == "" {
		return "", fmt.Errorf("doc filename must not be empty")
	}
	if strings.Contains(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("doc filename %q must be a relative path without traversal", rel)
	}
	cleaned := filepath.Clean(rel)
	if strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("doc filename %q escapes project dir", rel)
	}
	return filepath.Join(v.Root, "Projects", project, "doc", cleaned), nil
}

// MemoryDir returns the path to a project's memory directory:
// {vault}/Projects/{project}/memory
func (v *Vault) MemoryDir(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	return filepath.Join(v.Root, "Projects", project, "memory"), nil
}

// MemoryFile returns the path to a file under a project's memory directory:
// {vault}/Projects/{project}/memory/{rel}. rel must be a simple relative path
// like "pref-foo.md" — no traversal, no absolute paths, no .git segment, and no
// NTFS/exFAT-illegal name.
//
// Path safety and cross-platform portability are delegated to vaultfs, the same
// validators that gate the vp_vault_* surface: ValidateRelPath rejects empty,
// absolute, control-byte, traversal, and unportable-name inputs (reserved chars,
// Windows device names, trailing dot/space), and IsRefusedWritePath rejects a
// ".git"/".vp-locks" segment. Sharing them keeps memory writes held to exactly the
// portability contract the audit reports on — a Claude/Grok/Zed host that harvests
// a natively-named memory file can no longer smuggle a Windows-illegal name into
// the synced vault.
func (v *Vault) MemoryFile(project, rel string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	if err := vaultfs.ValidateRelPath(rel); err != nil {
		return "", fmt.Errorf("memory filename %q: %w", rel, err)
	}
	if vaultfs.IsRefusedWritePath(rel) {
		return "", fmt.Errorf("%w: memory filename %q must not contain a .git or .vp-locks segment",
			vaultfs.ErrRefusedPath, rel)
	}
	cleaned := filepath.Clean(rel)
	return filepath.Join(v.Root, "Projects", project, "memory", cleaned), nil
}

// AbsorbedFile returns the path to a file under a project's absorbed/
// scratch directory. Used by `vp absorb` for resume-suggestions handoffs.
// rel follows the same rules as DocFile.
func (v *Vault) AbsorbedFile(project, rel string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", fmt.Errorf("project: %w", err)
	}
	if rel == "" {
		return "", fmt.Errorf("absorbed filename must not be empty")
	}
	if strings.Contains(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("absorbed filename %q must be a relative path without traversal", rel)
	}
	cleaned := filepath.Clean(rel)
	if strings.HasPrefix(cleaned, "..") {
		return "", fmt.Errorf("absorbed filename %q escapes project dir", rel)
	}
	return filepath.Join(v.Root, "Projects", project, "absorbed", cleaned), nil
}

// encodeTripleComponent produces a FLAT, portable, injective on-disk name for a
// single triple component (subject, predicate, or object). It is the sole
// encoder behind KGTriplePath and the glob patterns used by
// QueryEntity/KGStats/ListTriples.
//
// Shape: slug(raw) + "_" + hex(sha256(raw))[:8].
//   - slug lowercases raw and maps every rune outside [a-z0-9._-] to "_",
//     collapsing runs of "_" and "-" and trimming them. This guarantees the
//     result carries no "/", "\\", ":", control character or path separator,
//     and — because "-" runs collapse — no "--" triple delimiter.
//   - the 8-char content hash makes the whole name INJECTIVE despite slug
//     collisions: two distinct raw strings that slug identically still differ in
//     the hash suffix, so distinct triples never share a filename.
//
// Because every component ends in "_<hash>", it can never be a bare ".." or an
// NTFS-reserved bareword (CON/PRN/AUX/NUL/COM1-9/LPT1-9), and the name is FLAT
// (no interior "/"), which keeps the single-level triple globs correct. The RAW
// subject/predicate/object stay in the JSON body (Triple), so reversibility by
// filename is deliberately traded away — the raw is always recoverable in-file.
func encodeTripleComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	slug := b.String()
	// Collapse runs of "_", "-" and "." to a single char, then trim those from
	// the ends. Collapsing "-" kills any "--" (the triple delimiter); collapsing
	// "." kills any ".." (the traversal token) while still allowing a single "."
	// (e.g. "main.rs"). This lossiness is safe: the content hash below preserves
	// injectivity regardless.
	for strings.Contains(slug, "__") {
		slug = strings.ReplaceAll(slug, "__", "_")
	}
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	for strings.Contains(slug, "..") {
		slug = strings.ReplaceAll(slug, "..", ".")
	}
	slug = strings.Trim(slug, "._-")

	sum := sha256.Sum256([]byte(s))
	return slug + "_" + hex.EncodeToString(sum[:])[:8]
}
