// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package absorb

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// WriteOptions configures a writer pass. All paths are absolute.
type WriteOptions struct {
	// Vault is the vault the writer will append into.
	Vault *storage.Vault
	// Project is the palace-project slug. The writer requires
	// Projects/{Project}/config.toml to exist before writing anything.
	Project string
	// ProjectRoot is the on-disk repo root containing the source files.
	ProjectRoot string
	// Now is the timestamp used for dated subheadings and backup
	// filenames. Zero value means time.Now().
	Now time.Time
	// Stage, when true, runs `git add` on rewritten source files and
	// backups. Silently no-ops when ProjectRoot is not a git repo.
	Stage bool
}

// WriteReport summarizes what the writer did. All counts and lists are
// cumulative across every source file and every destination touched.
type WriteReport struct {
	VaultFilesCreated  []string
	VaultFilesAppended []string
	DuplicateSkipped   []string // destinations where content-hash dedup kicked in
	ResumeScratchPath  string   // set when anything routed to resume scratch
	SourceRewritten    []string
	SourceBackedUp     []string
	UnsupportedSkipped []string
	PointerLines       []string
}

// Apply executes plan against opts.Vault. It is the caller's responsibility
// to validate (e.g. dry-run display) before calling Apply. Apply is
// idempotent under byte-identical re-runs thanks to content-hash dedup on
// every dated-subheading append.
func Apply(plan *Plan, opts WriteOptions) (*WriteReport, error) {
	if err := requireVaultProject(opts.Vault, opts.Project); err != nil {
		return nil, err
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	dateStr := opts.Now.Format("2006-01-02")
	report := &WriteReport{}

	// Pass 1: group items by destination. Preserves item order within
	// each destination; deduplicates identical body hashes within the
	// same run.
	type destKey struct{ path, section string }
	buckets := map[destKey][]Item{}
	var destOrder []destKey
	seenThisRun := map[string]bool{} // per-destination dedup: destKey+hash
	var resumeItems []Item

	for _, it := range plan.Items {
		if it.Dest.IsZero() {
			continue
		}
		if it.Dest.Scratch {
			resumeItems = append(resumeItems, it)
			continue
		}
		k := destKey{path: it.Dest.Path, section: it.Dest.Section}
		seenKey := k.path + "|" + k.section + "|" + it.BodyHash
		if seenThisRun[seenKey] {
			report.DuplicateSkipped = append(report.DuplicateSkipped, k.path)
			continue
		}
		seenThisRun[seenKey] = true
		if _, ok := buckets[k]; !ok {
			destOrder = append(destOrder, k)
		}
		buckets[k] = append(buckets[k], it)
	}

	// Pass 2: for each destination, read existing file, skip items whose
	// body-hash already appears, then append one block containing the
	// remaining items under a dated subheading.
	for _, k := range destOrder {
		items := buckets[k]
		absPath, _, err := resolveDestPath(opts.Vault, opts.Project, k.path)
		if err != nil {
			return nil, err
		}
		// Serialize the whole read-dedup-append against a concurrent absorb or
		// vp_memory_write of the same file: it is a read-modify-write, and
		// unlocked two writers both read the old bytes and the loser's append
		// vanishes. `created` is derived from the LOCKED read (readIfExists
		// returns nil only on not-exist), so it cannot go stale between a
		// pre-lock stat and the write.
		if err := func() error {
			release, lerr := vaultlock.Acquire(opts.Vault.Root, absPath)
			if lerr != nil {
				return fmt.Errorf("absorb: lock %s: %w", absPath, lerr)
			}
			defer release()

			existing, err := readIfExists(absPath)
			if err != nil {
				return err
			}
			created := existing == nil

			var toWrite []Item
			for _, it := range items {
				if containsBodyHash(existing, it.BodyHash) {
					report.DuplicateSkipped = append(report.DuplicateSkipped,
						k.path+"#"+it.BodyHash)
					continue
				}
				toWrite = append(toWrite, it)
			}
			if len(toWrite) == 0 {
				return nil
			}

			body := renderDatedAppend(toWrite, dateStr, k.section, created)
			if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(absPath), err)
			}
			// appendOrCreate routes through atomicfile.Write, which stamps
			// .surface on success — no out-of-band stampVault needed.
			if err := appendOrCreate(opts.Vault.Root, absPath, existing, body, created); err != nil {
				return err
			}
			if created {
				report.VaultFilesCreated = append(report.VaultFilesCreated, k.path)
				// For new doc/*.md files, queue a pointer line for resume.
				if strings.HasPrefix(k.path, "doc/") {
					summary := summarizeDestination(k.path)
					report.PointerLines = append(report.PointerLines,
						fmt.Sprintf("- see `%s` for %s", k.path, summary))
				}
			} else {
				report.VaultFilesAppended = append(report.VaultFilesAppended, k.path)
			}
			return nil
		}(); err != nil {
			return nil, err
		}
	}

	// Pass 3: resume scratch file.
	if len(resumeItems) > 0 || len(report.PointerLines) > 0 {
		scratchPath, err := opts.Vault.AbsorbedFile(opts.Project, "resume-suggestions.md")
		if err != nil {
			return nil, err
		}
		body := renderResumeScratch(resumeItems, report.PointerLines, dateStr)
		if err := appendScratch(opts.Vault, opts.Project, "resume-suggestions.md", body); err != nil {
			return nil, err
		}
		report.ResumeScratchPath = scratchPath
	}

	// Pass 4: rewrite source files and drop backups.
	for _, ps := range plan.Sources {
		if !ps.Supported {
			report.UnsupportedSkipped = append(report.UnsupportedSkipped, ps.DisplayPath)
			continue
		}
		backupRel, err := rewriteSourceFile(ps, opts)
		if err != nil {
			return nil, err
		}
		report.SourceRewritten = append(report.SourceRewritten, ps.DisplayPath)
		if backupRel != "" {
			report.SourceBackedUp = append(report.SourceBackedUp, backupRel)
		}
	}

	// Pass 5: optional git-add.
	if opts.Stage {
		stageRewrittenFiles(opts.ProjectRoot, plan, report)
	}

	return report, nil
}

// requireVaultProject enforces the "Projects/{slug}/config.toml must
// exist" predicate from the plan (§3A). Bare directory existence is not
// sufficient because `vp init` also creates empty subdirs.
func requireVaultProject(v *storage.Vault, project string) error {
	cfg, err := v.ProjectConfigFile(project)
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfg); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("vault project %q not initialized (%s missing) — run `vp init` first",
				project, cfg)
		}
		return fmt.Errorf("stat %s: %w", cfg, err)
	}
	return nil
}

func resolveDestPath(v *storage.Vault, project, rel string) (path string, fresh bool, err error) {
	switch {
	case rel == "workflow.md":
		path, err = v.WorkflowFile(project)
	case rel == "knowledge.md":
		path, err = v.KnowledgeFile(project)
	// Deliberately NO "resume.md" case. Every resume-bound item is classified
	// DestResumeScratch (Scratch: true) and Apply diverts it to
	// absorbed/resume-suggestions.md for human merge before resolveDestPath is
	// ever reached. A resume.md case here would be a trap, not a feature:
	// absorb's atomicWrite takes no vaultlock and carries no expected-sha, so
	// wiring it up would blind-overwrite resume.md outside both the advisory
	// lock and the WriteResume compare-and-set (see doc/adr/003).
	case strings.HasPrefix(rel, "doc/"):
		path, err = v.DocFile(project, strings.TrimPrefix(rel, "doc/"))
	default:
		err = fmt.Errorf("unknown destination %q", rel)
	}
	if err != nil {
		return "", false, err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return path, true, nil
		}
		return "", false, statErr
	}
	return path, false, nil
}

func readIfExists(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// bodyHashMarker tags every appended block with its sha so re-runs can
// see the content is already present and skip.
const bodyHashPrefix = "<!-- absorb-hash: "
const bodyHashSuffix = " -->"

func containsBodyHash(existing []byte, hash string) bool {
	if len(existing) == 0 || hash == "" {
		return false
	}
	needle := []byte(bodyHashPrefix + hash + bodyHashSuffix)
	return bytes.Contains(existing, needle)
}

// renderDatedAppend turns a slice of Items into one markdown block under a
// dated subheading. If k.section is non-empty, the block emits a nested
// heading per originating heading so multi-file files stay navigable.
// When created is true, we also emit the starter header so the file looks
// the same whether or not it pre-existed.
func renderDatedAppend(items []Item, date, section string, created bool) string {
	var b strings.Builder
	if created {
		b.WriteString(starterHeader(items[0].Dest.Path))
	}
	// One dated subheading groups everything appended on the same run
	// for this destination+section.
	label := fmt.Sprintf("From %s (%s)", distinctSources(items), date)
	if section != "" {
		// workflow.md groups content under Commands/Rules section
		// subheadings; the dated header appears inside that section.
		fmt.Fprintf(&b, "\n## %s\n\n", section)
		fmt.Fprintf(&b, "### %s\n\n", label)
	} else {
		fmt.Fprintf(&b, "\n## %s\n\n", label)
	}
	b.WriteString("<!-- TODO: human merge — review and fold into canonical sections -->\n\n")
	for _, it := range items {
		// Preserve the originating heading when one existed.
		if it.Section.Heading != "" {
			fmt.Fprintf(&b, "%s %s\n\n", strings.Repeat("#", clampHeadingLevel(it.Section.Level+1)),
				it.Section.Heading)
		}
		b.WriteString(strings.TrimRight(it.Section.Body, "\n"))
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "%s%s%s\n\n", bodyHashPrefix, it.BodyHash, bodyHashSuffix)
	}
	return b.String()
}

func clampHeadingLevel(l int) int {
	if l < 1 {
		return 3
	}
	if l > 6 {
		return 6
	}
	return l
}

func distinctSources(items []Item) string {
	seen := map[string]bool{}
	var out []string
	for _, it := range items {
		if !seen[it.DisplayPath] {
			seen[it.DisplayPath] = true
			out = append(out, it.DisplayPath)
		}
	}
	return strings.Join(out, ", ")
}

// starterHeader returns a short file-top header for a newly-created vault
// file. Headings only — no fabricated content.
func starterHeader(path string) string {
	switch {
	case path == "workflow.md":
		return "# Workflow\n\n_Workflow rules, build commands, and conventions for this project._\n"
	case path == "knowledge.md":
		return "# Knowledge\n\n_Domain facts, glossary, and stable references._\n"
	case strings.HasPrefix(path, "doc/"):
		name := strings.TrimSuffix(filepath.Base(path), ".md")
		title := titleCase(strings.ReplaceAll(name, "-", " "))
		return fmt.Sprintf("# %s\n\n_%s reference material; not loaded by bootstrap, consult on demand._\n",
			title, title)
	}
	return ""
}

func summarizeDestination(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".md")
	return strings.ReplaceAll(base, "-", " ") + " reference"
}

// appendOrCreate writes body to an existing file (append) or creates a new
// one seeded with body. Uses a tmp + rename for atomicity.
func appendOrCreate(vaultRoot, path string, existing []byte, body string, created bool) error {
	var out []byte
	if created {
		out = []byte(body)
	} else {
		out = make([]byte, 0, len(existing)+len(body)+1)
		out = append(out, existing...)
		if len(existing) > 0 && existing[len(existing)-1] != '\n' {
			out = append(out, '\n')
		}
		out = append(out, []byte(body)...)
	}
	// WithFsync + WithInheritPerm preserve the deleted private atomicWrite's
	// behavior (it fsynced and chmod-inherited an existing target); a NEW file
	// shifts 0600 -> the atomicfile 0o644 default — a deliberate, recorded delta.
	// A non-empty vaultRoot makes the .surface stamp structural here.
	return atomicfile.Write(vaultRoot, path, out, atomicfile.WithFsync(), atomicfile.WithInheritPerm())
}

func appendScratch(v *storage.Vault, project, rel, body string) error {
	path, err := v.AbsorbedFile(project, rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Lock the read-then-append so a concurrent scratch append cannot clobber it.
	release, err := vaultlock.Acquire(v.Root, path)
	if err != nil {
		return fmt.Errorf("absorb: lock %s: %w", path, err)
	}
	defer release()
	existing, err := readIfExists(path)
	if err != nil {
		return err
	}
	out := make([]byte, 0, len(existing)+len(body)+1)
	out = append(out, existing...)
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, []byte(body)...)
	return atomicfile.Write(v.Root, path, out, atomicfile.WithFsync(), atomicfile.WithInheritPerm())
}

func renderResumeScratch(items []Item, pointerLines []string, date string) string {
	var b strings.Builder
	if len(items) > 0 {
		fmt.Fprintf(&b, "\n## From absorb (%s)\n\n", date)
		b.WriteString("<!-- TODO: human merge into resume.md; absorb does not auto-edit resume.md -->\n\n")
		for _, it := range items {
			if it.Section.Heading != "" {
				fmt.Fprintf(&b, "### %s (source: %s)\n\n", it.Section.Heading, it.DisplayPath)
			} else {
				fmt.Fprintf(&b, "### Preamble (source: %s)\n\n", it.DisplayPath)
			}
			b.WriteString(strings.TrimRight(it.Section.Body, "\n"))
			b.WriteString("\n\n")
			fmt.Fprintf(&b, "%s%s%s\n\n", bodyHashPrefix, it.BodyHash, bodyHashSuffix)
		}
	}
	if len(pointerLines) > 0 {
		fmt.Fprintf(&b, "\n## Reference pointers (%s)\n\n", date)
		b.WriteString("<!-- paste one or more of these into resume.md so `doc/*` stays discoverable -->\n\n")
		for _, p := range pointerLines {
			b.WriteString(p)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// rewriteSourceFile replaces the source with preamble + managed block,
// writing a backup first. Returns the repo-relative backup path when one
// was written.
func rewriteSourceFile(ps PlannedSource, opts WriteOptions) (string, error) {
	// Backup.
	backupDir := filepath.Join(opts.ProjectRoot, ".vibe-palace")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir backup dir: %w", err)
	}
	ts := opts.Now.Format("20060102-150405")
	backupName := filepath.Base(ps.AbsPath) + ".bak-" + ts
	backupPath := filepath.Join(backupDir, backupName)
	if err := os.WriteFile(backupPath, ps.Original, 0o644); err != nil {
		return "", fmt.Errorf("write backup %s: %w", backupPath, err)
	}

	// Rewrite: preamble + managed block (preserved if present, or freshly
	// added via agentfile.Wire below). The simplest implementation: emit
	// preamble first, then let agentfile.Wire append the managed block
	// idempotently. We write preamble + any pre-existing block (if any),
	// then call Wire to refresh the block to the current hash.
	var out bytes.Buffer
	out.WriteString(ps.Source.Preamble())
	// Preserve an existing managed block if the original had one (Wire
	// will update its hash if needed). Otherwise, leave the file as just
	// the preamble and let Wire append a fresh block.
	if ps.HadBlock {
		start, end := agentfile.FindBlock(ps.Original)
		if start >= 0 {
			out.WriteString("\n")
			out.Write(ps.Original[start:end])
			out.WriteString("\n")
		}
	}
	data := out.Bytes()
	if ps.UsesCRLF {
		data = bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
	}
	// A project-repo source file, NOT a vault file: pass vaultRoot="" so no
	// .surface stamp is attempted, but keep fsync + inherit-perm to match the
	// deleted private atomicWrite's behavior on this path.
	if err := atomicfile.Write("", ps.AbsPath, data, atomicfile.WithFsync(), atomicfile.WithInheritPerm()); err != nil {
		return "", err
	}
	// Wire to ensure a managed block exists with the current hash. Route
	// through WireAll with an explicit Target override so all three wiring
	// call sites share one orchestrator even though we only want this one
	// file touched.
	outcomes, _, err := agentfile.WireAll(opts.ProjectRoot,
		agentfile.WithTargets(agentfile.Target{Path: ps.AbsPath, DisplayName: ps.DisplayPath}))
	if err != nil {
		return "", fmt.Errorf("wire managed block into %s: %w", ps.AbsPath, err)
	}
	for _, oc := range outcomes {
		if oc.Err != nil {
			return "", fmt.Errorf("wire managed block into %s: %w", ps.AbsPath, oc.Err)
		}
	}

	rel, err := filepath.Rel(opts.ProjectRoot, backupPath)
	if err != nil {
		rel = backupPath
	}
	return rel, nil
}

// stageRewrittenFiles runs `git add` on each rewritten source file and
// backup. No-op when the project has no .git directory (e.g. a directory
// that isn't yet a git repo — checkers01 as of 2026-04-12 is this case).
func stageRewrittenFiles(projectRoot string, plan *Plan, report *WriteReport) {
	if _, err := os.Stat(filepath.Join(projectRoot, ".git")); err != nil {
		return
	}
	args := []string{"-C", projectRoot, "add", "--"}
	for _, ps := range plan.Sources {
		if !ps.Supported {
			continue
		}
		args = append(args, ps.DisplayPath)
	}
	for _, b := range report.SourceBackedUp {
		args = append(args, b)
	}
	cmd := exec.Command("git", args...)
	// Best-effort — if git is absent or the add fails, it's not fatal.
	_ = cmd.Run()
}

// titleCase capitalizes the first rune of each whitespace-separated word.
// Ascii-only — project directory names don't use unicode punctuation, so
// this avoids a dependency on golang.org/x/text/cases.
func titleCase(s string) string {
	out := []byte(s)
	up := true
	for i := range out {
		if out[i] == ' ' || out[i] == '\t' {
			up = true
			continue
		}
		if up && out[i] >= 'a' && out[i] <= 'z' {
			out[i] = out[i] - 'a' + 'A'
		}
		up = false
	}
	return string(out)
}
