// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// ONE-SHOT. Everything in this file exists for `vp migrate project-slug`
// (cmd/vp/cmd_migrate_project_slug.go), the one-time move of every piece of a
// project's vault history from one slug to another (quantum-ng ->
// qa-metabuild-system, task migrate-quantum-ng-vault-history-to-qa-metabuild-system).
// Delete, in ONE commit:
//
//   - this file, project_slug_migration_test.go,
//     project_slug_migration_guards_test.go, and the two
//     project_slug_migration_owner_*.go build-tagged helpers;
//   - cmd/vp/cmd_migrate_project_slug.go and its _test.go;
//   - the `// ONE-SHOT` registration line in cmd/vp/commands.go;
//   - the "migrate project-slug" wantMutating row in cmd/vp/main_test.go;
//   - the PlanProjectSlugMigration entry in internal/sourceaudit's plannerFuncs;
//   - the whole scripts/oneshot-project-slug/ harness directory.
//
// It lives in package storage, beside MoveTaskToProject, because it needs the
// unexported gitCmd and it is a TYPED WRITER of task locations: it renames task
// files through the F2 sink (vaultfs.RenameNoLock) with its own
// refuse-existing-destination policy, exactly as MoveTaskToProject does, and it
// preserves every task's relative location (tasks/, tasks/done/,
// tasks/cancelled/) byte for byte. vaultfs.Move is not used because it refuses
// task paths at both ends.
//
// # Shape
//
// Three vault commits, then a host-local cache step:
//
//	K0  make room   delete colliding scaffolds, rename colliding target files
//	K1  rename      every tracked source file into free paths, all R100
//	K2  rewrite     the stored identifiers (W1..W10c), exact patterns, counted
//	cache           rename embed-cache vectors (host-local, journalled)
//
// Every commit is asserted AFTER it lands, on `git diff-tree HEAD^ HEAD`, and
// HEAD^ must be the previous step's commit. Only operations git cannot undo
// (ignored .bak moves and every cache operation) are journalled; the journal
// line is fsynced before the operation, and ReplaySlugJournal undoes them.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// slugMigrationAfterOp is a TEST SEAM, nil in production: it is called after
// the n-th journalled operation (1-based) and an error it returns aborts the
// run at that point, leaving the journal exactly as a crash would.
var slugMigrationAfterOp func(n int) error

// slugMigrationBeforeCommit is a TEST SEAM, nil in production: it is called
// just before each K commit with the step name ("K0", "K1", "K2").
var slugMigrationBeforeCommit func(step string) error

// SlugMove is one rename, vault-relative.
type SlugMove struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// SlugCounts is the report the live run must reproduce exactly (--expect). It
// covers TRACKED vault state only: no host-local cache count is in it.
type SlugCounts struct {
	From            string         `json:"from"`
	To              string         `json:"to"`
	K0Delete        []string       `json:"k0_delete"`
	K0Rename        []SlugMove     `json:"k0_rename"`
	K1Tracked       int            `json:"k1_tracked"`
	K1TrackedDigest string         `json:"k1_tracked_digest"`
	K1Untracked     []string       `json:"k1_untracked"`
	Classes         map[string]int `json:"classes"`
}

// SlugPlan is everything the planner derived. It is read-only state.
type SlugPlan struct {
	Root, From, To string
	Counts         SlugCounts
	k0Deletes      []string
	k0Renames      []SlugMove
	k1Moves        []SlugMove // tracked
	bakMoves       []SlugMove // untracked (ignored) files under the source trees
	strayStems     map[string]string
	holds          map[string]string // merged room file (post-K1 path) -> hold file
}

var slugSessionName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}-[0-9A-Za-z]+)-(\d+)\.md$`)

func slugMsg(step int, from, to string) string {
	names := []string{"0/2 make room", "1/2 rename", "2/2 identifier rewrite"}
	return fmt.Sprintf("migrate: %s -> %s (%s)", from, to, names[step])
}

// slugGitOut runs git in root and returns STDOUT only, so warnings on stderr
// can never be parsed as porcelain.
func slugGitOut(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func splitNUL(b []byte) []string {
	var out []string
	for s := range strings.SplitSeq(string(b), "\x00") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func slugTracked(root string, dirs ...string) ([]string, error) {
	args := append([]string{"-c", "core.quotepath=off", "ls-files", "-z", "--"}, dirs...)
	out, err := slugGitOut(root, args...)
	if err != nil {
		return nil, err
	}
	l := splitNUL(out)
	sort.Strings(l)
	return l, nil
}

// slugMapPath maps a source-tree path to its destination path, "" when rel is
// not under a source tree.
func slugMapPath(rel, from, to string) string {
	switch {
	case strings.HasPrefix(rel, "palace/"+from+"/drawers/"+from+"/"):
		return "palace/" + to + "/drawers/" + to + "/" + strings.TrimPrefix(rel, "palace/"+from+"/drawers/"+from+"/")
	case strings.HasPrefix(rel, "palace/"+from+"/"):
		return "palace/" + to + "/" + strings.TrimPrefix(rel, "palace/"+from+"/")
	case strings.HasPrefix(rel, "Projects/"+from+"/"):
		return "Projects/" + to + "/" + strings.TrimPrefix(rel, "Projects/"+from+"/")
	}
	return ""
}

func slugExists(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

// PlanProjectSlugMigration derives the whole migration and writes nothing.
// The source audit's plannerNoWrite rule names it.
func PlanProjectSlugMigration(root, from, to string) (*SlugPlan, error) {
	if err := slug.Validate(from); err != nil {
		return nil, fmt.Errorf("--from: %w", err)
	}
	if err := slug.Validate(to); err != nil {
		return nil, fmt.Errorf("--to: %w", err)
	}
	if from == to {
		return nil, fmt.Errorf("--from and --to are both %q", from)
	}
	p := &SlugPlan{Root: root, From: from, To: to, strayStems: map[string]string{}, holds: map[string]string{}}
	src, err := slugTracked(root, "Projects/"+from, "palace/"+from)
	if err != nil {
		return nil, err
	}
	if len(src) == 0 {
		return nil, fmt.Errorf("nothing tracked under Projects/%s or palace/%s", from, from)
	}
	dstTracked, err := slugTracked(root, "Projects/"+to, "palace/"+to)
	if err != nil {
		return nil, err
	}
	target := map[string]bool{}
	for _, r := range dstTracked {
		target[r] = true
	}
	tracked := map[string]bool{}
	for _, r := range src {
		tracked[r] = true
	}

	// Session numbering per <date>-<fp> prefix, across BOTH trees.
	maxNN := map[string]int{}
	for _, r := range append(append([]string{}, src...), dstTracked...) {
		if !strings.Contains(r, "/sessions/") {
			continue
		}
		if m := slugSessionName.FindStringSubmatch(path.Base(r)); m != nil {
			n, _ := strconv.Atoi(m[2])
			if n > maxNN[m[1]] {
				maxNN[m[1]] = n
			}
		}
	}

	droppedSrc := map[string]bool{}
	for _, r := range src {
		dst := slugMapPath(r, from, to)
		if !target[dst] && !slugExists(root, dst) {
			continue
		}
		switch {
		case path.Base(r) == ".surface":
			p.k0Deletes = append(p.k0Deletes, dst)
		case r == "Projects/"+from+"/commands/README.md" || r == "Projects/"+from+"/skills/README.md":
			if err := slugOnlyReadme(root, path.Dir(r)); err != nil {
				return nil, err
			}
			p.k0Deletes = append(p.k0Deletes, r)
			droppedSrc[r] = true
		case strings.HasPrefix(dst, "Projects/"+to+"/sessions/"):
			m := slugSessionName.FindStringSubmatch(path.Base(dst))
			if m == nil {
				return nil, fmt.Errorf("colliding session %s has an unparseable name", dst)
			}
			maxNN[m[1]]++
			newName := fmt.Sprintf("%s-%02d.md", m[1], maxNN[m[1]])
			p.k0Renames = append(p.k0Renames, SlugMove{Src: dst, Dst: "Projects/" + to + "/sessions/" + newName})
			p.strayStems[strings.TrimSuffix(path.Base(dst), ".md")] = strings.TrimSuffix(newName, ".md")
		case strings.HasPrefix(dst, "palace/"+to+"/drawers/"+to+"/") && path.Base(dst) == "drawers.jsonl":
			room := path.Base(path.Dir(dst))
			hold := "palace/" + to + "/premerge-stray-" + room + ".jsonl"
			p.k0Renames = append(p.k0Renames, SlugMove{Src: dst, Dst: hold})
			p.holds[dst] = hold
		default:
			return nil, fmt.Errorf("unexpected collision: %s would land on existing %s; refusing", r, dst)
		}
	}
	for _, r := range src {
		if droppedSrc[r] {
			continue
		}
		p.k1Moves = append(p.k1Moves, SlugMove{Src: r, Dst: slugMapPath(r, from, to)})
	}
	for _, mv := range p.k0Renames {
		if target[mv.Dst] || slugExists(root, mv.Dst) {
			return nil, fmt.Errorf("K0 destination %s already exists; refusing", mv.Dst)
		}
	}

	// Untracked files under the source trees (the ignored .bak backups).
	for _, dir := range []string{"Projects/" + from, "palace/" + from} {
		base := filepath.Join(root, filepath.FromSlash(dir))
		err := filepath.WalkDir(base, func(pth string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				if d.Name() == ".local" {
					return fs.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, pth)
			rel = filepath.ToSlash(rel)
			if tracked[rel] {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("%s is not a regular file", rel)
			}
			p.bakMoves = append(p.bakMoves, SlugMove{Src: rel, Dst: slugMapPath(rel, from, to)})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(p.bakMoves, func(i, j int) bool { return p.bakMoves[i].Src < p.bakMoves[j].Src })
	for _, mv := range append(append([]SlugMove{}, p.k1Moves...), p.bakMoves...) {
		collides := false
		for _, k := range p.k0Deletes {
			if k == mv.Dst {
				collides = true
			}
		}
		if _, held := p.holds[mv.Dst]; held {
			collides = true
		}
		for _, r := range p.k0Renames {
			if r.Src == mv.Dst {
				collides = true
			}
		}
		if !collides && (target[mv.Dst] || slugExists(root, mv.Dst)) {
			return nil, fmt.Errorf("destination %s already exists; refusing", mv.Dst)
		}
	}

	classes, _, err := slugScanClasses(root, from, to, p, false)
	if err != nil {
		return nil, err
	}
	sort.Strings(p.k0Deletes)
	sort.Slice(p.k0Renames, func(i, j int) bool { return p.k0Renames[i].Src < p.k0Renames[j].Src })
	h := sha256.New()
	for _, mv := range p.k1Moves {
		fmt.Fprintf(h, "%s\t%s\n", mv.Src, mv.Dst)
	}
	var unt []string
	for _, mv := range p.bakMoves {
		unt = append(unt, mv.Src)
	}
	p.Counts = SlugCounts{
		From: from, To: to,
		K0Delete: append([]string{}, p.k0Deletes...), K0Rename: append([]SlugMove{}, p.k0Renames...),
		K1Tracked: len(p.k1Moves), K1TrackedDigest: hex.EncodeToString(h.Sum(nil)),
		K1Untracked: unt, Classes: classes,
	}
	if p.Counts.K0Delete == nil {
		p.Counts.K0Delete = []string{}
	}
	if p.Counts.K0Rename == nil {
		p.Counts.K0Rename = []SlugMove{}
	}
	if p.Counts.K1Untracked == nil {
		p.Counts.K1Untracked = []string{}
	}
	return p, nil
}

func slugOnlyReadme(root, dir string) error {
	ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(dir)))
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.Name() != "README.md" {
			return fmt.Errorf("%s holds %s besides README.md; refusing to delete its scaffold", dir, e.Name())
		}
	}
	return nil
}

// JSON renders the counts deterministically, one trailing newline.
func (c SlugCounts) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// ---------------------------------------------------------------------------
// The rewrite classes.
//
// slugScanClasses counts every class. With apply=false it reads the SOURCE
// paths (before K1) and writes nothing; with apply=true it reads and rewrites
// the DESTINATION paths (after K1) through atomicfile and returns the counts it
// applied, which the caller asserts equal to the plan's.

type slugRewrite struct {
	rel  string
	data []byte
}

func slugFrontmatter(lines []string) (end int) {
	if len(lines) == 0 || lines[0] != "---" {
		return 0
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return i
		}
	}
	return 0
}

// slugScanClasses counts (and with apply=true performs) every rewrite class.
// It returns the counts and, in apply mode, the vault-relative paths it wrote
// and removed: K2's expected state is built from THOSE, never from the dirt it
// is about to commit.
func slugScanClasses(root, from, to string, p *SlugPlan, apply bool) (map[string]int, []string, error) {
	c := map[string]int{"W1": 0, "W1b": 0, "W2": 0, "W2b": 0, "W3": 0, "W4": 0, "W5": 0, "W6": 0, "W7": 0,
		"W8": 0, "W9": 0, "W10": 0, "W10b": 0, "W10c": 0}
	var writes []slugRewrite
	// at maps a source path to where it lives in the current phase.
	at := func(srcRel string) string {
		if apply {
			return slugMapPath(srcRel, from, to)
		}
		return srcRel
	}
	read := func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) }
	existsAfter := func(dstRel string) bool {
		if apply {
			return slugExists(root, dstRel)
		}
		// before K1: the destination exists after K1 iff its source is tracked.
		for _, mv := range p.k1Moves {
			if mv.Dst == dstRel {
				return true
			}
		}
		return false
	}

	for _, mv := range p.k1Moves {
		srcRel := mv.Src
		rel := at(srcRel)
		isSession := strings.HasPrefix(srcRel, "Projects/"+from+"/sessions/") && strings.HasSuffix(srcRel, ".md") && !strings.Contains(strings.TrimPrefix(srcRel, "Projects/"+from+"/sessions/"), "/")
		isNote := strings.HasPrefix(srcRel, "Projects/"+from+"/notes/") && strings.HasSuffix(srcRel, ".md")
		isManifest := strings.HasPrefix(srcRel, "Projects/"+from+"/transcripts/") && strings.HasSuffix(srcRel, ".manifest.json")
		isResume := srcRel == "Projects/"+from+"/resume.md"
		isWorkflow := srcRel == "Projects/"+from+"/workflow.md"
		isRoom := strings.HasPrefix(srcRel, "palace/"+from+"/drawers/"+from+"/") && path.Base(srcRel) == "drawers.jsonl"
		if !(isSession || isNote || isManifest || isResume || isWorkflow || isRoom) {
			continue
		}
		data, err := read(rel)
		if err != nil {
			return nil, nil, err
		}
		var out []byte
		changed := false
		switch {
		case isSession || isNote || isResume:
			lines := strings.Split(string(data), "\n")
			end := slugFrontmatter(lines)
			stem := strings.TrimSuffix(path.Base(srcRel), ".md")
			for i := 1; i < end; i++ {
				l := lines[i]
				switch {
				case l == "project: "+from:
					lines[i] = "project: " + to
					if isSession {
						c["W1"]++
					} else if isNote {
						c["W1b"]++
					} else {
						c["W6"]++
					}
					changed = true
				case isSession && l == "note_path: Projects/"+from+"/sessions/"+stem+".md":
					lines[i] = "note_path: Projects/" + to + "/sessions/" + stem + ".md"
					c["W2"]++
					changed = true
				case isSession && strings.HasPrefix(l, "archive: Projects/"+from+"/transcripts/") && strings.HasSuffix(l, ".manifest.json"):
					name := strings.TrimPrefix(l, "archive: Projects/"+from+"/transcripts/")
					if strings.Contains(name, "/") || !existsAfter("Projects/"+to+"/transcripts/"+name) {
						return nil, nil, fmt.Errorf("%s: archive target %s does not exist", srcRel, name)
					}
					lines[i] = "archive: Projects/" + to + "/transcripts/" + name
					c["W3"]++
					changed = true
				case isNote && strings.HasPrefix(l, "tracked_by: Projects/"+from+"/tasks/") && strings.HasSuffix(l, ".md"):
					name := strings.TrimPrefix(l, "tracked_by: Projects/"+from+"/")
					if !existsAfter("Projects/" + to + "/" + name) {
						return nil, nil, fmt.Errorf("%s: tracked_by target %s does not exist", srcRel, name)
					}
					lines[i] = "tracked_by: Projects/" + to + "/" + name
					c["W2b"]++
					changed = true
				}
			}
			if isResume {
				for i, l := range lines {
					if l == "# "+from+" — Working Context" {
						lines[i] = "# " + to + " — Working Context"
						c["W6"]++
						changed = true
					}
				}
			}
			out = []byte(strings.Join(lines, "\n"))
		case isWorkflow:
			lines := strings.Split(string(data), "\n")
			for i, l := range lines {
				if l == "# "+from+" — Workflow" {
					lines[i] = "# " + to + " — Workflow"
					c["W7"]++
					changed = true
				}
			}
			out = []byte(strings.Join(lines, "\n"))
		case isManifest:
			lines := strings.Split(string(data), "\n")
			ps := 0
			for i, l := range lines {
				if l == `  "project_slug": "`+from+`",` {
					lines[i] = `  "project_slug": "` + to + `",`
					ps++
					changed = true
					continue
				}
				pre := `  "vault_rel_session_note": "Projects/` + from + `/sessions/`
				if rest, ok := strings.CutPrefix(l, pre); ok {
					comma := strings.HasSuffix(rest, `",`)
					name := strings.TrimSuffix(strings.TrimSuffix(rest, ","), `"`)
					if strings.Contains(name, "/") || !strings.HasSuffix(name, ".md") || !existsAfter("Projects/"+to+"/sessions/"+name) {
						return nil, nil, fmt.Errorf("%s: vault_rel_session_note target %s does not exist", srcRel, name)
					}
					nl := `  "vault_rel_session_note": "Projects/` + to + `/sessions/` + name + `"`
					if comma {
						nl += ","
					}
					lines[i] = nl
					c["W5"]++
					changed = true
				}
			}
			// Exactly one. Zero used to be tolerated (imp2-N3): it would mean
			// a manifest the rewrite silently did nothing to, and the census
			// counts one per manifest (416 of 416 today).
			if ps != 1 {
				return nil, nil, fmt.Errorf("%s: project_slug appears %d times, want exactly 1", srcRel, ps)
			}
			c["W4"] += ps
			out = []byte(strings.Join(lines, "\n"))
		case isRoom:
			var err error
			var n, nb int
			out, n, nb, err = slugRehashRoom(data, from, to, srcRel)
			if err != nil {
				return nil, nil, err
			}
			c["W10"] += n
			c["W10b"] += nb
			changed = n > 0
		}
		if apply && changed {
			writes = append(writes, slugRewrite{rel: rel, data: out})
		}
	}

	// W9: the renamed target sessions (after K0 they live under their new name).
	for oldStem, newStem := range p.strayStems {
		rel := "Projects/" + to + "/sessions/" + oldStem + ".md"
		if apply {
			rel = "Projects/" + to + "/sessions/" + newStem + ".md"
		}
		data, err := read(rel)
		if err != nil {
			return nil, nil, err
		}
		lines := strings.Split(string(data), "\n")
		end := slugFrontmatter(lines)
		oldNN, _ := strconv.Atoi(oldStem[strings.LastIndex(oldStem, "-")+1:])
		newNN, _ := strconv.Atoi(newStem[strings.LastIndex(newStem, "-")+1:])
		for i := 1; i < end; i++ {
			switch lines[i] {
			case "session_id: " + oldStem:
				lines[i] = "session_id: " + newStem
				c["W9"]++
			case "iteration: " + strconv.Itoa(oldNN):
				lines[i] = "iteration: " + strconv.Itoa(newNN)
				c["W9"]++
			case "note_path: Projects/" + to + "/sessions/" + oldStem + ".md":
				lines[i] = "note_path: Projects/" + to + "/sessions/" + newStem + ".md"
				c["W9"]++
			}
		}
		if apply {
			writes = append(writes, slugRewrite{rel: rel, data: []byte(strings.Join(lines, "\n"))})
		}
	}

	// W8: Audits/baseline.json accepted[] entries.
	if data, err := read("Audits/baseline.json"); err == nil {
		out, n, err := slugRewriteBaseline(data, from, to)
		if err != nil {
			return nil, nil, err
		}
		c["W8"] = n
		if apply && n > 0 {
			writes = append(writes, slugRewrite{rel: "Audits/baseline.json", data: out})
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, err
	}

	// W10c: the held stray rows, appended to the merged rooms.
	for merged, hold := range p.holds {
		holdData, err := read(hold)
		if apply {
			if err != nil {
				return nil, nil, err
			}
		} else {
			// before K0 the hold file is still at its original (target) path.
			holdData, err = read(merged)
			if err != nil {
				return nil, nil, err
			}
		}
		rows, n, err := slugHeldRows(holdData, to, p.strayStems)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", hold, err)
		}
		c["W10c"] += n
		if apply {
			var base []byte
			for i := range writes {
				if writes[i].rel == merged {
					base = writes[i].data
				}
			}
			if base == nil {
				if base, err = read(merged); err != nil {
					return nil, nil, err
				}
			}
			ids := map[string]bool{}
			for l := range strings.SplitSeq(string(base), "\n") {
				var d Drawer
				if json.Unmarshal([]byte(l), &d) == nil {
					ids[d.ID] = true
				}
			}
			for _, r := range rows {
				var d Drawer
				_ = json.Unmarshal([]byte(r), &d)
				if ids[d.ID] {
					return nil, nil, fmt.Errorf("held row %s collides with an id already in %s", d.ID, merged)
				}
			}
			if len(base) > 0 && !bytes.HasSuffix(base, []byte("\n")) {
				base = append(base, '\n')
			}
			for _, r := range rows {
				base = append(base, []byte(r+"\n")...)
			}
			found := false
			for i := range writes {
				if writes[i].rel == merged {
					writes[i].data = base
					found = true
				}
			}
			if !found {
				writes = append(writes, slugRewrite{rel: merged, data: base})
			}
		}
	}

	var written []string
	if apply {
		for _, w := range writes {
			abs := filepath.Join(root, filepath.FromSlash(w.rel))
			if err := atomicfile.Write(root, abs, w.data, atomicfile.WithInheritPerm(), atomicfile.WithFsync()); err != nil {
				return nil, nil, fmt.Errorf("rewrite %s: %w", w.rel, err)
			}
			written = append(written, w.rel)
		}
		for _, hold := range p.holds {
			if err := vaultfs.RemoveNoLock(filepath.Join(root, filepath.FromSlash(hold))); err != nil {
				return nil, nil, fmt.Errorf("remove %s: %w", hold, err)
			}
		}
	}
	return c, written, nil
}

// slugAssertClasses refuses when the rewrite pass did not do exactly what the
// counted plan promised: it is the tool's own answer to "did K2 rewrite what
// the report said it would".
func slugAssertClasses(applied, planned map[string]int) error {
	for k, v := range planned {
		if applied[k] != v {
			return fmt.Errorf("class %s rewrote %d, the plan counted %d", k, applied[k], v)
		}
	}
	for k, v := range applied {
		if _, ok := planned[k]; !ok && v != 0 {
			return fmt.Errorf("class %s rewrote %d, the plan did not count it", k, v)
		}
	}
	return nil
}

// slugRehashRoom rewrites every row's id from DrawerID(from, content) to
// DrawerID(to, content), and a decision source_ref's trailing id with it. Bytes
// outside those two substrings are untouched.
func slugRehashRoom(data []byte, from, to, rel string) ([]byte, int, int, error) {
	lines := strings.Split(string(data), "\n")
	n, nb := 0, 0
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var d Drawer
		if err := json.Unmarshal([]byte(l), &d); err != nil {
			return nil, 0, 0, fmt.Errorf("%s line %d: %w", rel, i+1, err)
		}
		if d.ID != DrawerID(from, d.Content) {
			return nil, 0, 0, fmt.Errorf("%s line %d: id %s is not DrawerID(%q, content)", rel, i+1, d.ID, from)
		}
		nid := DrawerID(to, d.Content)
		idTok := `"id":"` + d.ID + `"`
		if strings.Count(l, idTok) != 1 {
			return nil, 0, 0, fmt.Errorf("%s line %d: id token not found exactly once", rel, i+1)
		}
		l = strings.Replace(l, idTok, `"id":"`+nid+`"`, 1)
		n++
		if strings.HasPrefix(d.SourceRef, "session/") && strings.HasSuffix(d.SourceRef, "/"+d.ID) && strings.Contains(d.SourceRef, "#decision/") {
			refTok := "/" + d.ID + `"`
			if strings.Count(l, refTok) != 1 {
				return nil, 0, 0, fmt.Errorf("%s line %d: source_ref id token not found exactly once", rel, i+1)
			}
			l = strings.Replace(l, refTok, "/"+nid+`"`, 1)
			nb++
		}
		lines[i] = l
	}
	return []byte(strings.Join(lines, "\n")), n, nb, nil
}

// slugHeldRows returns the held rows with their source_ref session renamed.
func slugHeldRows(data []byte, to string, stems map[string]string) ([]string, int, error) {
	var rows []string
	for i, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var d Drawer
		if err := json.Unmarshal([]byte(l), &d); err != nil {
			return nil, 0, fmt.Errorf("line %d: %w", i+1, err)
		}
		if d.ID != DrawerID(to, d.Content) {
			return nil, 0, fmt.Errorf("line %d: id %s is not DrawerID(%q, content)", i+1, d.ID, to)
		}
		for oldStem, newStem := range stems {
			tok := `"session/` + oldStem + `#`
			l = strings.Replace(l, tok, `"session/`+newStem+`#`, 1)
		}
		rows = append(rows, l)
	}
	return rows, len(rows), nil
}

func slugRewriteBaseline(data []byte, from, to string) ([]byte, int, error) {
	var doc struct {
		Dimensions map[string]struct {
			Accepted []string `json:"accepted"`
		} `json:"dimensions"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, 0, fmt.Errorf("Audits/baseline.json: %w", err)
	}
	out := string(data)
	n := 0
	for _, v := range doc.Dimensions["archive-roundtrip"].Accepted {
		if !strings.HasPrefix(v, "Projects/"+from+"/transcripts/") {
			continue
		}
		oldTok, _ := json.Marshal(v)
		newTok, _ := json.Marshal("Projects/" + to + "/" + strings.TrimPrefix(v, "Projects/"+from+"/"))
		if strings.Count(out, string(oldTok)) != 1 {
			return nil, 0, fmt.Errorf("Audits/baseline.json: accepted entry %s is not a unique token", v)
		}
		out = strings.Replace(out, string(oldTok), string(newTok), 1)
		n++
	}
	return []byte(out), n, nil
}

// ---------------------------------------------------------------------------
// LIVE classification and the process guard (M3).

// ClassifySlugVault reports whether root is the LIVE vault. It is LIVE when it
// has any git remote, or is the same inode as the vault_path in
// home/.config/vibe-palace/config.toml. Every uncertainty is LIVE (fail-closed):
// an unreadable config, a missing vault_path, a vault_path that does not stat,
// or a failing `git remote`.
func ClassifySlugVault(root, home string) (bool, string) {
	remotes, err := ListRemotes(root)
	if err != nil {
		return true, "git remote failed (fail-closed)"
	}
	cfgPath := filepath.Join(home, ".config", "vibe-palace", "config.toml")
	detail := func(same string) string {
		return fmt.Sprintf("remotes=%d, samefile-as-configured=%s", len(remotes), same)
	}
	if len(remotes) > 0 {
		return true, detail("n/a")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return true, detail("unknown: " + cfgPath + " unreadable (fail-closed)")
	}
	var cfg struct {
		VaultPath string `toml:"vault_path"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil || cfg.VaultPath == "" {
		return true, detail("unknown: no vault_path (fail-closed)")
	}
	vi, err := os.Stat(cfg.VaultPath)
	if err != nil {
		return true, detail("unknown: vault_path does not stat (fail-closed)")
	}
	ri, err := os.Stat(root)
	if err != nil {
		return true, detail("unknown: root does not stat (fail-closed)")
	}
	if os.SameFile(vi, ri) {
		return true, detail("true")
	}
	return false, detail("false")
}

// slugWindowChairArgv reports whether a claude process's own argv proves it is
// the ONE sanctioned window Chair: hooks disabled and no vibe-palace MCP path.
// It must carry BOTH
//
//   - exactly one --settings whose value is an inline JSON object with
//     "disableAllHooks": true (a settings FILE path cannot be verified from the
//     command line, so it does not qualify), and
//   - --strict-mcp-config together with at least one --mcp-config, every value
//     of which is an inline JSON object whose mcpServers names no server whose
//     command is vp and no field mentioning vibe-palace.
//
// argv comes from /proc/<pid>/cmdline, split on NUL, so no shell quoting is
// involved. Anything unparseable is NOT exempt (fail-closed).
func slugWindowChairArgv(argv []string) (bool, string) {
	var settings, mcps []string
	strict := false
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--strict-mcp-config":
			strict = true
		case a == "--settings" && i+1 < len(argv):
			settings = append(settings, argv[i+1])
			i++
		case strings.HasPrefix(a, "--settings="):
			settings = append(settings, strings.TrimPrefix(a, "--settings="))
		case a == "--mcp-config":
			for i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
				mcps = append(mcps, argv[i+1])
				i++
			}
		case strings.HasPrefix(a, "--mcp-config="):
			mcps = append(mcps, strings.TrimPrefix(a, "--mcp-config="))
		}
	}
	if len(settings) != 1 {
		return false, fmt.Sprintf("%d --settings values, want exactly 1", len(settings))
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(settings[0]), &st); err != nil {
		return false, "--settings is not inline JSON"
	}
	if v, ok := st["disableAllHooks"].(bool); !ok || !v {
		return false, "--settings lacks \"disableAllHooks\": true"
	}
	if !strict {
		return false, "no --strict-mcp-config"
	}
	if len(mcps) == 0 {
		return false, "no --mcp-config"
	}
	for _, m := range mcps {
		var cfg struct {
			MCPServers map[string]map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(m), &cfg); err != nil {
			return false, "--mcp-config is not inline JSON"
		}
		for name, srv := range cfg.MCPServers {
			raw, _ := json.Marshal(srv)
			cmd, _ := srv["command"].(string)
			b := filepath.Base(cmd)
			if b == "vp" || b == "vp.exe" || strings.Contains(name, "vibe-palace") || strings.Contains(string(raw), "vibe-palace") {
				return false, "--mcp-config names a vibe-palace server"
			}
		}
	}
	return true, ""
}

// slugBinaryIdent is a binary's identity: its size, and its sha256 computed
// lazily (hashing a 40 MB binary for every process on the host would be
// wasteful, and a size mismatch already rules a candidate out).
type slugBinaryIdent struct {
	path string
	size int64
	sum  string
}

func slugIdentOf(path string) (slugBinaryIdent, error) {
	st, err := os.Stat(path)
	if err != nil {
		return slugBinaryIdent{}, err
	}
	return slugBinaryIdent{path: path, size: st.Size()}, nil
}

func (b *slugBinaryIdent) sha() (string, error) {
	if b.sum == "" {
		s, err := slugSha(b.path)
		if err != nil {
			return "", err
		}
		b.sum = s
	}
	return b.sum, nil
}

// sameBinary reports whether exe is the SAME binary as b: the same file
// (device+inode), or a copy with identical size and sha256. A renamed or
// copied binary is the case name matching misses.
func (b *slugBinaryIdent) sameBinary(exe string) bool {
	if b.path == "" || exe == "" {
		return false
	}
	if a, err := os.Stat(b.path); err == nil {
		if c, err := os.Stat(exe); err == nil {
			if os.SameFile(a, c) {
				return true
			}
			if a.Size() != c.Size() {
				return false
			}
		} else {
			return false
		}
	}
	want, err := b.sha()
	if err != nil {
		return false
	}
	got, err := slugSha(exe)
	if err != nil {
		return false
	}
	return got == want
}

// slugClaudeIdents returns the Claude Code binaries under the native install
// directory, so one copied elsewhere and renamed is still recognised by its
// bytes. It is NOT every install: an npm install ships a JS entry point run by
// node, which the `claude-code/cli` argv rule catches instead, and its bytes
// are not in this set.
func slugClaudeIdents(home string) []slugBinaryIdent {
	var out []slugBinaryIdent
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return out
	}
	vers := filepath.Join(home, ".local", "share", "claude", "versions")
	ents, err := os.ReadDir(vers)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if id, err := slugIdentOf(filepath.Join(vers, e.Name())); err == nil && id.size > 0 {
			out = append(out, id)
		}
	}
	return out
}

// slugAgentScan lists every process in procDir that the guard refuses: any vp
// process other than self, any grok client, and any claude process except ONE
// sanctioned window Chair (see slugWindowChairArgv). A second exempt-looking
// claude process is refused too.
//
// Identity, not names (code review round 1, C5). A process is matched by what
// its /proc/<pid>/exe actually IS — the same file as, or a byte-identical copy
// of, this tool's own binary or an installed Claude Code — as well as by comm
// and argv. So `cp $(command -v vp) /tmp/zzz && /tmp/zzz mcp` is caught, and so
// is a Claude Code copied out of its versions directory.
//
// Fail-closed cases: a process of OUR OWN uid whose exe cannot be resolved is
// refused, because our processes' exe is always readable and something that
// hides it is exactly what this guard is for. A process of another uid (root's,
// typically) keeps being judged on comm and argv, which are always readable —
// refusing there would refuse on every host, since PID 1 is never ours.
// home is "" in production, which reads the running user's home; the tests
// point it at a fixture.
// slugProcFloor is the self-visibility floor (code review round 2, D2). A
// /proc that cannot show the scanner its own process, or that lists a bare
// handful of entries, is not the host's process table: a PID namespace, a
// chroot with a fresh /proc, or hidepid all produce "nothing to refuse" while
// agents run. A guard that cannot see itself cannot clear the host.
//
// The count is a floor for a HOST (this one runs ~270 processes), not a proof
// of anything: a PID namespace holding 8 processes including ours would pass
// it. The two checks belong together — do not "simplify" this into the count
// alone — and the runbook never runs the tool inside a container.
const slugProcFloor = 8

func slugAgentScan(procDir string, self int, home string) ([]string, error) {
	return slugAgentScanAs(procDir, self, home, os.Getuid())
}

// slugAgentScanAs takes the uid explicitly so tests can build a process that
// is NOT ours, which is the shape the "nothing readable" branch exists for
// (imp3 R2-S2). Production always passes os.Getuid().
func slugAgentScanAs(procDir string, self int, home string, uid int) ([]string, error) {
	ents, err := os.ReadDir(procDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", procDir, err)
	}
	seen, sawSelf := 0, false
	for _, e := range ents {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			seen++
			if pid == self {
				sawSelf = true
			}
		}
	}
	if !sawSelf {
		return nil, fmt.Errorf("%s does not list this process (pid %d): it is not this host's process table", procDir, self)
	}
	if seen < slugProcFloor {
		return nil, fmt.Errorf("%s lists only %d processes, fewer than the floor of %d: it is not this host's process table", procDir, seen, slugProcFloor)
	}
	me, _ := slugIdentOf(slugSelfExe())
	claudes := slugClaudeIdents(home)
	var out []string
	exempt := 0
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		dir := filepath.Join(procDir, e.Name())
		st, err := os.Stat(dir)
		if err != nil {
			// The process exited between ReadDir and now. A vanished process
			// cannot be running during the migration, and treating its empty
			// reads as "nothing readable" would refuse on any busy host.
			continue
		}
		commB, _ := os.ReadFile(filepath.Join(dir, "comm"))
		comm := strings.TrimSpace(string(commB))
		cmdB, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		argv := strings.Split(strings.TrimRight(string(cmdB), "\x00"), "\x00")
		exe, exeErr := os.Readlink(filepath.Join(dir, "exe"))
		ours := slugOwnedByUs(st, uid)
		base := func(i int) string {
			if i < len(argv) {
				return filepath.Base(argv[i])
			}
			return ""
		}
		why := ""
		// argv[1] is exactly "mcp" or "hook": the shape a vp that was copied
		// AND edited (so identity misses it) is still invoked in. Measured on
		// this host, that matched `vp mcp` four times and nothing else, so it
		// costs no false refusal here (code review round 2, D5).
		subcmd := len(argv) > 1 && (argv[1] == "mcp" || argv[1] == "hook")
		isVP := comm == "vp" || base(0) == "vp" || filepath.Base(exe) == "vp" || filepath.Base(exe) == "vp.exe" || me.sameBinary(exe) || subcmd
		// NOTE: the `claude-code/cli` test is a substring of the WHOLE command
		// line, so a shell or editor whose arguments merely contain that
		// literal is classified as claude and refused. That is fail-closed and
		// deliberate, but it is why nothing in the runbook quotes the literal.
		isClaude := comm == "claude" || base(0) == "claude" || base(1) == "claude" ||
			strings.Contains(string(cmdB), "claude-code/cli") || strings.Contains(exe, "/claude/versions/")
		for i := range claudes {
			if claudes[i].sameBinary(exe) {
				isClaude = true
			}
		}
		switch {
		case len(commB) == 0 && len(cmdB) == 0 && slugProcStillThere(dir):
			// Nothing readable at all: it cannot be cleared, so it is refused.
			// This is the hidepid / hardened-container shape, and it is the
			// ONLY blanket fail-closed case. An unreadable `exe` is NOT one:
			// /proc/<pid>/exe is unreadable for any non-dumpable process even
			// to its own uid, and this host has five of them in an ordinary
			// session (systemd --user, (sd-pam), two ssh-agents and the
			// operator's own sshd-session). Refusing those would refuse on a
			// perfectly frozen host, with nothing the operator could kill
			// (code review round 2, D1). An unreadable exe on a process that
			// says `systemd --user` is not evidence of hiding; it is normal.
			why = "process with no readable comm or cmdline (fail-closed)"
		case comm == "grok" || strings.Contains(exe, "/.grok/downloads/") || strings.HasPrefix(comm, "grok"):
			why = "grok client"
		case isClaude:
			ok, reason := slugWindowChairArgv(argv)
			if len(cmdB) == 0 {
				ok, reason = false, "cmdline unreadable (fail-closed)"
			}
			// The exemption is the one place an unreadable exe still fails
			// closed: a process that already looks like Claude Code must be
			// fully identifiable before it is let through.
			if ok && exeErr != nil && ours {
				ok, reason = false, "exe cannot be resolved (fail-closed)"
			}
			switch {
			case ok && exempt == 0:
				exempt++
			case ok:
				why = "a second window-Chair claude process"
			default:
				why = "claude (not the window Chair: " + reason + ")"
			}
		case isVP:
			why = "vp process"
			if subcmd && comm != "vp" && base(0) != "vp" {
				// It is not called vp: say which rule fired, or the operator
				// spends the window wondering why `docker mcp` is "vp".
				why = "argv[1] is a vp subcommand (" + argv[1] + ")"
			}
		}
		if why != "" {
			out = append(out, fmt.Sprintf("pid %d %s (%s)", pid, why, strings.Join(argv, " ")))
		}
	}
	sort.Strings(out)
	return out, nil
}

// slugProcStillThere distinguishes a HIDDEN process from one that simply
// exited between the directory listing and the reads a few lines later. A
// genuinely hidden process still has a readable `stat`; a vanished one has
// nothing at all, and refusing it would stop the run for no cause the operator
// could act on — before K1, that means rolling back a committed K0 for nothing
// (code review round 3, imp3 R3-S1).
func slugProcStillThere(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "stat"))
	return err == nil
}

// slugSelfExe is a TEST SEAM: the binary this process is running, which the
// guard compares candidate processes against.
var slugSelfExe = func() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// SlugGuard is M3: on a LIVE vault it refuses while any agent or other vp
// process exists. A non-LIVE (rehearsal) vault is exempt. Where /proc cannot
// be enumerated it refuses unless attest is set.
func SlugGuard(live bool, procDir string, attest bool) error {
	if !live {
		return nil
	}
	if runtime.GOOS != "linux" || procDir == "" {
		if attest {
			return nil
		}
		return fmt.Errorf("refusing: LIVE vault and processes cannot be enumerated on %s", runtime.GOOS)
	}
	off, err := slugAgentScan(procDir, os.Getpid(), "")
	if err != nil {
		return fmt.Errorf("refusing: LIVE vault and %w", err)
	}
	if len(off) > 0 {
		return fmt.Errorf("refusing: LIVE vault and %s is running", strings.Join(off, "; "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// The free-space precondition (E3).

// slugFreeSpace is a TEST SEAM: the free space on the filesystem holding a
// path. Tests point it at a tiny number to prove the refusal fires.
var slugFreeSpace = slugFreeBytes

// slugSameDevice is a TEST SEAM: whether two paths share a filesystem. Tests
// force it false to exercise the journal-side budget, which on this host is
// zero because $W and the vault are both on /home.
var slugSameDevice = sameDevice

// slugSpaceSlack is what the run needs beyond the bytes it rewrites: git
// objects for three commits, the index, and the atomicfile temporaries.
const slugSpaceSlack = 64 << 20

// slugMovedBytes is the size of every file the migration moves. It is a
// deliberate OVER-estimate of what the run writes: K2 rewrites a subset of
// those files (about 35 MiB of the 178 MiB moved on the live vault), atomicfile
// writes one temporary at a time, and git's new blobs are of the same order as
// the rewritten bytes. Doubling the moved size therefore demands roughly 5x
// what the run needs, in the safe direction, and stays a single cheap stat
// walk rather than a second pass over the rewrite classes.
//
// A file it cannot stat is an error, not a zero: a requirement that quietly
// shrank because a tree was unreadable is not a measurement (imp3 R4-N1).
func slugMovedBytes(root string, p *SlugPlan) (int64, error) {
	var n int64
	for _, mv := range p.k1Moves {
		st, err := os.Stat(filepath.Join(root, filepath.FromSlash(mv.Src)))
		if err != nil {
			return 0, fmt.Errorf("cannot measure %s for the free-space check: %w", mv.Src, err)
		}
		n += st.Size()
	}
	return n, nil
}

// slugCheckSpace refuses up front when the filesystem cannot hold what the run
// is about to write. A migration that meets ENOSPC halfway through is the one
// failure the journal has never been rehearsed against: git may have committed
// K0 and K1, the cache may be half renamed, and the operator is left deciding
// whether a partial write is worse than a rollback that also cannot write.
// Refusing before the first byte costs nothing and removes that state.
//
// It reports what it computed either way, so "there was room" is a measurement
// in the log rather than an assumption.
func slugCheckSpace(what, path string, need int64, log io.Writer) error {
	free, ok := slugFreeSpace(path)
	if !ok {
		slugLog(log, "space: %s needs %s on %s; free space could not be measured on this platform, continuing\n",
			what, slugBytes(need), path)
		return nil
	}
	slugLog(log, "space: %s needs %s on %s, %s free\n", what, slugBytes(need), path, slugBytes(int64(free)))
	if int64(free) < need {
		return fmt.Errorf("refusing: %s needs %s free on %s and there is %s; free space or move the run",
			what, slugBytes(need), path, slugBytes(int64(free)))
	}
	return nil
}

func slugBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/float64(1<<20))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// ---------------------------------------------------------------------------
// The journal.

// SlugJournal records the operations git cannot undo. Each line is fsynced
// BEFORE its operation runs.
type SlugJournal struct {
	path string
	f    *os.File
	n    int
}

func slugJournalAux(journal string) string {
	return strings.TrimSuffix(journal, ".tsv") + ".d"
}

// slugVaultIdent identifies the vault a journal belongs to: its resolved path.
// A replay compares it with os.SameFile where the path still exists, so a bind
// mount or a symlink cannot make one vault look like another, and a COPY of
// the same shape (the rehearsal's) is never mistaken for the original. It is
// deliberately portable: the Windows host replays its own cache journal.
func slugVaultIdent(root string) (string, error) {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	return real, nil
}

// slugSameVault reports whether a journal header names this vault.
func slugSameVault(header, root string) bool {
	ident, err := slugVaultIdent(root)
	if err == nil && header == ident {
		return true
	}
	a, err1 := os.Stat(header)
	b, err2 := os.Stat(root)
	return err1 == nil && err2 == nil && os.SameFile(a, b)
}

// OpenSlugJournal creates the journal for one vault. It refuses a path that
// already exists and is non-empty, so every attempt starts a fresh journal,
// and writes the vault identity as the first line.
func OpenSlugJournal(journalPath, root string) (*SlugJournal, error) {
	if journalPath == "" {
		return nil, fmt.Errorf("--journal is required")
	}
	if st, err := os.Stat(journalPath); err == nil && st.Size() > 0 {
		return nil, fmt.Errorf("refusing: journal %s already exists and is non-empty (archive it first)", journalPath)
	}
	if err := os.MkdirAll(filepath.Join(slugJournalAux(journalPath), "deleted"), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if err := slugFsyncDir(filepath.Dir(journalPath)); err != nil {
		f.Close()
		return nil, err
	}
	ident, err := slugVaultIdent(root)
	if err != nil {
		f.Close()
		return nil, err
	}
	j := &SlugJournal{path: journalPath, f: f}
	if err := j.record("#vault", ident, ""); err != nil {
		f.Close()
		return nil, err
	}
	return j, nil
}

func (j *SlugJournal) record(op, a, b string) error {
	if _, err := fmt.Fprintf(j.f, "%s\t%s\t%s\n", op, a, b); err != nil {
		return err
	}
	return j.f.Sync()
}

func (j *SlugJournal) afterOp() error {
	j.n++
	if slugMigrationAfterOp != nil {
		return slugMigrationAfterOp(j.n)
	}
	return nil
}

// Close closes the journal file.
func (j *SlugJournal) Close() error {
	if j == nil || j.f == nil {
		return nil
	}
	return j.f.Close()
}

func slugFsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}

// slugMove renames one vault file with the per-move policy: containment,
// regular source, ABSENT destination, parent creation, journal (when j is
// non-nil) before the rename, and a destination-directory fsync after it.
func slugMove(root, srcRel, dstRel string, j *SlugJournal) error {
	src, err := vaultfs.ResolveSafePath(root, srcRel)
	if err != nil {
		return err
	}
	dst, err := vaultfs.ResolveSafePath(root, dstRel)
	if err != nil {
		return err
	}
	st, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("source %s: %w", srcRel, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("source %s is not a regular file", srcRel)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("refusing: destination %s already exists", dstRel)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("destination %s: %w", dstRel, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if j != nil {
		if err := j.record("mv", srcRel, dstRel); err != nil {
			return err
		}
	}
	if err := vaultfs.RenameNoLock(src, dst); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", srcRel, dstRel, err)
	}
	if j != nil {
		if err := slugFsyncDir(filepath.Dir(dst)); err != nil {
			return err
		}
		return j.afterOp()
	}
	return nil
}

// slugRemoveVec moves a vault file into the journal's deleted/ directory, so
// the rollback can restore it.
func slugRemoveVec(root, rel string, j *SlugJournal) error {
	abs, err := vaultfs.ResolveSafePath(root, rel)
	if err != nil {
		return err
	}
	saved := filepath.Join(slugJournalAux(j.path), "deleted", strings.ReplaceAll(rel, "/", "__"))
	if _, err := os.Lstat(saved); err == nil {
		return fmt.Errorf("refusing: saved copy %s already exists", saved)
	}
	if err := j.record("rm-vec", rel, saved); err != nil {
		return err
	}
	if err := vaultfs.RenameNoLock(abs, saved); err != nil {
		if !errors.Is(err, syscall.EXDEV) {
			return err
		}
		data, rerr := os.ReadFile(abs)
		if rerr != nil {
			return rerr
		}
		if werr := slugWriteOutside(saved, data); werr != nil {
			return werr
		}
		if err := vaultfs.RemoveNoLock(abs); err != nil {
			return err
		}
	}
	if err := slugFsyncDir(filepath.Dir(saved)); err != nil {
		return err
	}
	return j.afterOp()
}

// slugWriteJournalled creates a file under the gitignored palace/.local/ and
// journals it first, so a replay removes it. git cannot undo these: they are
// ignored, so `reset --hard` leaves them in place. The cache marker is the
// one that matters — an unremoved marker makes the retry skip the whole cache
// step and leave a stray vector on a live key.
func slugWriteJournalled(root, rel string, data []byte, j *SlugJournal) error {
	abs, err := vaultfs.ResolveSafePath(root, rel)
	if err != nil {
		return err
	}
	if err := j.record("mk", rel, ""); err != nil {
		return err
	}
	if err := atomicfile.Write(root, abs, data, atomicfile.WithFsync()); err != nil {
		return err
	}
	return j.afterOp()
}

// slugWriteOutside writes a file OUTSIDE the vault (journal side), fsynced.
func slugWriteOutside(p string, data []byte) error {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// SlugReplayResult is the per-state count of a replay, plus any warning the
// run must shout without failing: a rollback that did the right thing has to
// EXIT 0, or the `set -eu` block around it stops before restoring the kill
// switch (code review round 2, D4).
type SlugReplayResult struct {
	Lines, Done, PreviouslyReplayed, NotDone, Conflict int
	Warnings                                           []string
}

func (r SlugReplayResult) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "lines=%d done=%d previously-replayed=%d not-done=%d conflict=%d",
		r.Lines, r.Done, r.PreviouslyReplayed, r.NotDone, r.Conflict)
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "\nWARNING: %s", w)
	}
	return b.String()
}

// It undoes the journalled operations in reverse and is re-entrant: every
// reverted line index is recorded (fsynced) in <journal>.replayed and skipped
// by a re-run. A missing or empty journal is done=0, exit 0. It fails on any
// conflict or on more than one not-done line.
// ReplaySlugJournal undoes a journal against the vault it was written for.
// forceRoot overrides the vault-identity refusal, for the one case the
// operator can legitimately assert: the vault was moved after the journal was
// written (imp2 round-2 NIT 2). It is recorded as a warning on the result.
func ReplaySlugJournal(root, journalPath string, forceRoot bool) (SlugReplayResult, error) {
	var res SlugReplayResult
	data, err := os.ReadFile(journalPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return res, nil
		}
		return res, err
	}
	var lines []string
	header := ""
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		t := sc.Text()
		if t == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(t, "#vault\t"); ok {
			header = strings.TrimSuffix(rest, "\t")
			continue
		}
		lines = append(lines, t)
	}
	// The journal names the vault it was written for. A replay against any
	// other vault would "restore" that one and leave the intended vault
	// untouched, reporting success either way.
	if header == "" {
		return res, fmt.Errorf("refusing: journal %s has no vault header; it was not written by this tool", journalPath)
	}
	forced := ""
	if !slugSameVault(header, root) {
		if !forceRoot {
			return res, fmt.Errorf("refusing: journal %s belongs to vault %q, not %q (--force-root overrides this)", journalPath, header, root)
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"--force-root: replaying a journal written for %q against %q", header, root))
		forced = fmt.Sprintf("#force-root\t%s\t%s", header, root)
	}
	res.Lines = len(lines)
	// The .replayed file is bound to the journal's exact bytes. A stale one
	// left beside a NEW journal at the same path would otherwise skip every
	// line and report success, which is the loudest possible silent failure.
	replayedPath := journalPath + ".replayed"
	want := fmt.Sprintf("#journal\t%d\t%x", len(data), sha256.Sum256(data))
	replayed := map[int]bool{}
	stale := false
	if b, err := os.ReadFile(replayedPath); err == nil {
		rl := strings.Split(string(b), "\n")
		if len(rl) == 0 || strings.TrimSpace(rl[0]) != want {
			stale = true
		} else {
			for _, t := range rl[1:] {
				if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
					replayed[n] = true
				}
			}
		}
	}
	if stale {
		// Not a refusal, and not a failure afterwards either. The right answer
		// is to replay by state, which is idempotent; the wrong answer is to
		// do that work and then exit non-zero, which aborts the rollback block
		// between the replay and the kill-switch restore (D4). So: discard the
		// stale file, replay, and carry a WARNING on the result.
		if err := os.Remove(replayedPath); err != nil {
			return res, err
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%s did not belong to this journal and was discarded; the journal was replayed by state", replayedPath))
	}
	rf, err := os.OpenFile(replayedPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return res, err
	}
	defer rf.Close()
	if st, err := rf.Stat(); err == nil && st.Size() == 0 {
		if _, err := fmt.Fprintln(rf, want); err != nil {
			return res, err
		}
	}
	if forced != "" {
		// The override belongs where the next reader looks, not only in a
		// warning that scrolls past (imp2 round-3 NIT 3). Non-index lines are
		// ignored when the file is read back.
		if _, err := fmt.Fprintln(rf, forced); err != nil {
			return res, err
		}
	}
	mark := func(i int) error {
		if _, err := fmt.Fprintf(rf, "%d\n", i); err != nil {
			return err
		}
		return rf.Sync()
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }
	var conflicts []string
	for i, line := range slices.Backward(lines) {
		if replayed[i] {
			res.PreviouslyReplayed++
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			return res, fmt.Errorf("journal line %d is malformed", i+1)
		}
		switch f[0] {
		case "mv":
			src, err := vaultfs.ResolveSafePath(root, f[1])
			if err != nil {
				return res, err
			}
			dst, err := vaultfs.ResolveSafePath(root, f[2])
			if err != nil {
				return res, err
			}
			se, de := exists(src), exists(dst)
			switch {
			case de && !se:
				if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
					return res, err
				}
				if err := vaultfs.RenameNoLock(dst, src); err != nil {
					return res, err
				}
				res.Done++
				if err := mark(i); err != nil {
					return res, err
				}
			case se && !de:
				res.NotDone++
			default:
				res.Conflict++
				conflicts = append(conflicts, fmt.Sprintf("line %d mv %s (src present=%v, dst present=%v)", i+1, f[1], se, de))
			}
		case "rm-vec":
			orig, err := vaultfs.ResolveSafePath(root, f[1])
			if err != nil {
				return res, err
			}
			saved := f[2]
			// The saved copy must live in THIS journal's deleted/ directory:
			// a journal line is not a licence to move an arbitrary path.
			deleted, derr := filepath.Abs(filepath.Join(slugJournalAux(journalPath), "deleted"))
			if derr != nil {
				return res, derr
			}
			absSaved, derr := filepath.Abs(saved)
			if derr != nil {
				return res, derr
			}
			if absSaved != deleted && !strings.HasPrefix(absSaved, deleted+string(os.PathSeparator)) {
				return res, fmt.Errorf("journal line %d: saved copy %s is outside %s", i+1, saved, deleted)
			}
			oe, se := exists(orig), exists(saved)
			switch {
			case !oe && se:
				if err := os.MkdirAll(filepath.Dir(orig), 0o755); err != nil {
					return res, err
				}
				if err := vaultfs.RenameNoLock(saved, orig); err != nil {
					if !errors.Is(err, syscall.EXDEV) {
						return res, err
					}
					b, rerr := os.ReadFile(saved)
					if rerr != nil {
						return res, rerr
					}
					if werr := atomicfile.Write(root, orig, b, atomicfile.WithFsync()); werr != nil {
						return res, werr
					}
				}
				res.Done++
				if err := mark(i); err != nil {
					return res, err
				}
			case oe && se:
				// The crash hit between the copy and the removal (the EXDEV
				// path): the original is intact, so the saved copy goes.
				if err := os.Remove(saved); err != nil {
					return res, err
				}
				res.NotDone++
			case oe && !se:
				res.NotDone++
			default:
				res.Conflict++
				conflicts = append(conflicts, fmt.Sprintf("line %d rm-vec %s (both absent)", i+1, f[1]))
			}
		case "mk":
			// A `mk` line removes a file and keeps no copy, so it may name
			// ONLY the two files slugWriteJournalled writes, under the
			// gitignored cache directory. A corrupted or hand-edited line
			// cannot turn the replay into a delete of tracked content
			// (imp3 R2-N3).
			if bn := path.Base(f[1]); !strings.HasPrefix(f[1], "palace/.local/embed-cache/") ||
				(bn != slugCacheMarker && bn != slugStrayList) {
				return res, fmt.Errorf("journal line %d: mk names %s, which this tool never creates", i+1, f[1])
			}
			target, err := vaultfs.ResolveSafePath(root, f[1])
			if err != nil {
				return res, err
			}
			if exists(target) {
				if err := vaultfs.RemoveNoLock(target); err != nil {
					return res, err
				}
			}
			res.Done++
			if err := mark(i); err != nil {
				return res, err
			}
		default:
			return res, fmt.Errorf("journal line %d has unknown op %q", i+1, f[0])
		}
	}
	if res.Lines > 0 && res.Done == 0 && res.PreviouslyReplayed == res.Lines {
		return res, fmt.Errorf("refusing to report success: every one of the %d journal lines was already marked replayed, so this rollback did nothing; archive %s and its .replayed sibling if this is a new attempt", res.Lines, journalPath)
	}
	if res.Conflict > 0 {
		return res, fmt.Errorf("replay conflicts: %s", strings.Join(conflicts, "; "))
	}
	if res.NotDone > 1 {
		return res, fmt.Errorf("replay found %d not-done lines; at most 1 (the interrupted last line) is allowed", res.NotDone)
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// Apply.

// SlugApplyOptions configures ApplyProjectSlugMigration.
type SlugApplyOptions struct {
	Root, From, To string
	ExpectHead     string
	Expect         []byte // the counts.json bytes the fresh report must equal
	JournalPath    string
	ProcDir        string // "/proc" in production
	Home           string // os.UserHomeDir() in production
	Log            io.Writer
}

// SlugApplyResult reports what the apply did.
type SlugApplyResult struct {
	AlreadyApplied bool
	Live           bool
	K              [3]string
	Cache          SlugCacheResult
}

// SlugApplied reports whether root's TREE is in the migrated state: both
// source trees absent, the destination resume names the new slug, and no old
// session or manifest identifier remains. Commit messages are not consulted,
// so a reverted vault is not "applied".
func SlugApplied(root, from, to string) (bool, error) {
	for _, d := range []string{"Projects/" + from, "palace/" + from} {
		if slugExists(root, d) {
			return false, nil
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "Projects", to, "resume.md"))
	if err != nil {
		return false, nil
	}
	lines := strings.Split(string(data), "\n")
	end := slugFrontmatter(lines)
	ok := false
	for i := 1; i < end; i++ {
		if lines[i] == "project: "+to {
			ok = true
		}
	}
	if !ok {
		return false, nil
	}
	sessions, _ := filepath.Glob(filepath.Join(root, "Projects", to, "sessions", "*.md"))
	for _, s := range sessions {
		b, err := os.ReadFile(s)
		if err != nil {
			return false, err
		}
		if bytes.Contains(b, []byte("\nproject: "+from+"\n")) {
			return false, nil
		}
	}
	return true, nil
}

// slugDirt returns every dirty path (tracked changes and untracked files,
// ignored files excluded), one entry per file.
func slugDirt(root string) ([]string, error) {
	out, err := slugGitOut(root, "-c", "core.quotepath=off", "status", "--porcelain=v1", "-z", "-uall", "--no-renames")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range splitNUL(out) {
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
	}
	sort.Strings(paths)
	return paths, nil
}

func slugHead(root string) (string, error) {
	out, err := slugGitOut(root, "rev-parse", "HEAD")
	return strings.TrimSpace(string(out)), err
}

// SlugPreCommitCheck is M4 before a commit: HEAD must equal want, and every
// dirty path must belong to allowed (a nil allowed means the tree must be clean).
func SlugPreCommitCheck(root, wantHead string, allowed map[string]bool) error {
	head, err := slugHead(root)
	if err != nil {
		return err
	}
	if head != wantHead {
		return fmt.Errorf("refusing: HEAD moved from %s to %s", wantHead, head)
	}
	dirt, err := slugDirt(root)
	if err != nil {
		return err
	}
	var foreign []string
	for _, p := range dirt {
		if !allowed[p] {
			foreign = append(foreign, p)
		}
	}
	if len(foreign) > 0 {
		return fmt.Errorf("refusing: foreign dirt %s", strings.Join(foreign, ", "))
	}
	return nil
}

// SlugNameStatus is one line of `git diff-tree -M --name-status`.
type SlugNameStatus struct {
	Status   string // "D", "M", "A", "R100", ...
	Old, New string // New is "" except for renames
}

func slugDiffTree(root string) ([]SlugNameStatus, error) {
	out, err := slugGitOut(root, "-c", "core.quotepath=off", "-c", "diff.renameLimit=0", "diff-tree", "-r", "-M", "--name-status", "-z", "--no-commit-id", "HEAD^", "HEAD")
	if err != nil {
		return nil, err
	}
	f := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	var res []SlugNameStatus
	for i := 0; i < len(f); {
		if f[i] == "" {
			i++
			continue
		}
		st := f[i]
		if strings.HasPrefix(st, "R") || strings.HasPrefix(st, "C") {
			if i+2 >= len(f) {
				return nil, fmt.Errorf("truncated diff-tree output")
			}
			res = append(res, SlugNameStatus{Status: st, Old: f[i+1], New: f[i+2]})
			i += 3
			continue
		}
		res = append(res, SlugNameStatus{Status: st, Old: f[i+1]})
		i += 2
	}
	return res, nil
}

// SlugPostCommitCheck is M4 after a commit: HEAD^ must be prev, the tree must
// be clean, and the commit's name-status must equal want as a multiset of
// (status, old, new) — renames compared as the SETS of old and new paths, so
// git's pairing of identical blobs does not matter. extraOK admits additional
// lines (the .surface stamps K2 may touch).
func SlugPostCommitCheck(root, prev string, want []SlugNameStatus, extraOK func(SlugNameStatus) bool) error {
	parent, err := slugGitOut(root, "rev-parse", "HEAD^")
	if err != nil {
		return err
	}
	if p := strings.TrimSpace(string(parent)); p != prev {
		return fmt.Errorf("HEAD^ is %s, expected %s: a foreign commit slipped in", p, prev)
	}
	dirt, err := slugDirt(root)
	if err != nil {
		return err
	}
	if len(dirt) > 0 {
		return fmt.Errorf("dirt left after the commit: %s", strings.Join(dirt, ", "))
	}
	got, err := slugDiffTree(root)
	if err != nil {
		return err
	}
	key := func(s SlugNameStatus) string {
		if strings.HasPrefix(s.Status, "R") {
			return "R\x00old\x00" + s.Old
		}
		return s.Status + "\x00" + s.Old
	}
	keyNew := func(s SlugNameStatus) string { return "R\x00new\x00" + s.New }
	need := map[string]int{}
	for _, w := range want {
		need[key(w)]++
		if strings.HasPrefix(w.Status, "R") {
			need[keyNew(w)]++
		}
	}
	var bad []string
	for _, g := range got {
		if strings.HasPrefix(g.Status, "R") && g.Status != "R100" {
			bad = append(bad, fmt.Sprintf("%s %s -> %s (not R100)", g.Status, g.Old, g.New))
			continue
		}
		k := key(g)
		if need[k] > 0 && (!strings.HasPrefix(g.Status, "R") || need[keyNew(g)] > 0) {
			need[k]--
			if strings.HasPrefix(g.Status, "R") {
				need[keyNew(g)]--
			}
			continue
		}
		if extraOK != nil && extraOK(g) {
			continue
		}
		bad = append(bad, fmt.Sprintf("unexpected %s %s %s", g.Status, g.Old, g.New))
	}
	for k, n := range need {
		if n > 0 {
			bad = append(bad, "missing "+strings.ReplaceAll(k, "\x00", " "))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("commit name-status mismatch: %s", strings.Join(bad, "; "))
	}
	return nil
}

func slugLog(w io.Writer, format string, a ...any) {
	if w != nil {
		fmt.Fprintf(w, format, a...)
	}
}

// ApplyProjectSlugMigration runs K0, K1, K2 and the cache step. It never pushes.
func ApplyProjectSlugMigration(o SlugApplyOptions) (*SlugApplyResult, error) {
	res := &SlugApplyResult{}
	root, from, to := o.Root, o.From, o.To
	if done, err := SlugApplied(root, from, to); err != nil {
		return nil, err
	} else if done {
		// "Already applied" must not be the answer to a typo. The source slug
		// has to have existed in this vault's history; otherwise the tree
		// merely looks migrated because the source never existed at all.
		ever, err := slugGitOut(root, "rev-list", "-1", "HEAD", "--", "Projects/"+from+"/", "palace/"+from+"/")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(ever)) == "" {
			return nil, fmt.Errorf("refusing: %q has no history in this vault, so there is nothing to migrate (check --from)", from)
		}
		res.AlreadyApplied = true
		return res, nil
	}
	live, detail := ClassifySlugVault(root, o.Home)
	res.Live = live
	slugLog(o.Log, "vault=%s LIVE=%v (%s)\n", root, live, detail)

	head, err := slugHead(root)
	if err != nil {
		return nil, err
	}
	if o.ExpectHead == "" || head != o.ExpectHead {
		return nil, fmt.Errorf("refusing: HEAD is %s, --expect-head is %q", head, o.ExpectHead)
	}
	if err := RefuseIfNestedVaultGit(root, "migrate"); err != nil {
		return nil, err
	}
	plan, err := PlanProjectSlugMigration(root, from, to)
	if err != nil {
		return nil, err
	}
	fresh, err := plan.Counts.JSON()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(fresh, o.Expect) {
		return nil, fmt.Errorf("refusing: the fresh report differs from --expect")
	}
	remotes, err := ListRemotes(root)
	if err != nil {
		return nil, fmt.Errorf("refusing: list remotes: %w", err)
	}
	if len(remotes) == 0 {
		slugLog(o.Log, "no remotes: remote fetch and origin check skipped\n")
	}
	for _, r := range remotes {
		branch, err := currentBranch(root)
		if err != nil {
			return nil, err
		}
		if _, err := gitCmd(root, 60*time.Second, "fetch", "-q", r); err != nil {
			return nil, fmt.Errorf("refusing: fetch %s failed (offline is a refusal): %w", r, err)
		}
		tip, err := slugGitOut(root, "rev-parse", r+"/"+branch)
		if err != nil {
			return nil, fmt.Errorf("refusing: %s/%s does not resolve: %w", r, branch, err)
		}
		if strings.TrimSpace(string(tip)) != head {
			return nil, fmt.Errorf("refusing: %s/%s is %s, HEAD is %s (ahead or behind)", r, branch, strings.TrimSpace(string(tip)), head)
		}
	}
	if err := SlugGuard(live, o.ProcDir, false); err != nil {
		return nil, err
	}
	moved, err := slugMovedBytes(root, plan)
	if err != nil {
		return nil, err
	}
	if err := slugCheckSpace("the migration", root, 2*moved+slugSpaceSlack, o.Log); err != nil {
		return nil, err
	}
	// The apply runs the cache step in process, and that step's rm-vec saves
	// land beside the journal. RunSlugCachePhase budgeted this and the apply
	// did not, which made the path RB7 actually runs the weaker of the two
	// (imp2 round-4 SHOULD-FIX 1).
	if need := slugJournalSideBytes(root, from, o.JournalPath); need > 0 {
		if err := slugCheckSpace("the migration's saved copies", filepath.Dir(o.JournalPath), need, o.Log); err != nil {
			return nil, err
		}
	}
	if err := SlugPreCommitCheck(root, head, nil); err != nil {
		return nil, err
	}
	j, err := OpenSlugJournal(o.JournalPath, root)
	if err != nil {
		return nil, err
	}
	defer j.Close()

	lock := func() (func() error, error) {
		rel, ok, err := vaultlock.TryAcquire(root, root)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("refusing: the vault root lock is held by another process")
		}
		return rel, nil
	}
	commit := func(step int, paths []string, want []SlugNameStatus, extra func(SlugNameStatus) bool, prev string) (string, error) {
		name := fmt.Sprintf("K%d", step)
		if slugMigrationBeforeCommit != nil {
			if err := slugMigrationBeforeCommit(name); err != nil {
				return "", err
			}
		}
		if err := SlugGuard(live, o.ProcDir, false); err != nil {
			return "", err
		}
		allowed := map[string]bool{}
		for _, p := range paths {
			allowed[p] = true
		}
		if err := SlugPreCommitCheck(root, prev, allowed); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		r, err := CommitAndPushPaths(root, slugMsg(step, from, to), paths, false)
		if err != nil {
			return "", fmt.Errorf("%s: commit: %w", name, err)
		}
		if r == nil || r.CommitSHA == "" {
			return "", fmt.Errorf("%s: nothing was committed", name)
		}
		if err := SlugPostCommitCheck(root, prev, want, extra); err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		sha, err := slugHead(root)
		if err != nil {
			return "", err
		}
		slugLog(o.Log, "%s %s ok\n", name, sha)
		return sha, nil
	}

	// K0.
	release, err := lock()
	if err != nil {
		return nil, err
	}
	var k0Paths []string
	var k0Want []SlugNameStatus
	for _, d := range plan.k0Deletes {
		if err := vaultfs.RemoveNoLock(filepath.Join(root, filepath.FromSlash(d))); err != nil {
			release()
			return nil, fmt.Errorf("K0: remove %s: %w", d, err)
		}
		k0Paths = append(k0Paths, d)
		k0Want = append(k0Want, SlugNameStatus{Status: "D", Old: d})
	}
	for _, mv := range plan.k0Renames {
		if err := slugMove(root, mv.Src, mv.Dst, nil); err != nil {
			release()
			return nil, fmt.Errorf("K0: %w", err)
		}
		k0Paths = append(k0Paths, mv.Src, mv.Dst)
		k0Want = append(k0Want, SlugNameStatus{Status: "R100", Old: mv.Src, New: mv.Dst})
	}
	release()
	prev := head
	if len(k0Paths) > 0 {
		if res.K[0], err = commit(0, k0Paths, k0Want, nil, prev); err != nil {
			return res, err
		}
		prev = res.K[0]
	}

	// K1: the ignored .bak files first (journalled), then the tracked files.
	// The guard runs again HERE, before this phase writes anything: argv
	// cannot bind what a session does later, so every writing phase re-asks
	// (code review round 1, C8).
	if err := SlugGuard(live, o.ProcDir, false); err != nil {
		return res, err
	}
	if release, err = lock(); err != nil {
		return res, err
	}
	for _, mv := range plan.bakMoves {
		if err := slugMove(root, mv.Src, mv.Dst, j); err != nil {
			release()
			return res, fmt.Errorf("K1: %w", err)
		}
	}
	var k1Paths []string
	var k1Want []SlugNameStatus
	for _, mv := range plan.k1Moves {
		if err := slugMove(root, mv.Src, mv.Dst, nil); err != nil {
			release()
			return res, fmt.Errorf("K1: %w", err)
		}
		k1Paths = append(k1Paths, mv.Src, mv.Dst)
		k1Want = append(k1Want, SlugNameStatus{Status: "R100", Old: mv.Src, New: mv.Dst})
	}
	for _, d := range []string{"Projects/" + from, "palace/" + from} {
		if err := slugRemoveEmptyTree(root, d); err != nil {
			release()
			return res, fmt.Errorf("K1: %w", err)
		}
	}
	release()
	if res.K[1], err = commit(1, k1Paths, k1Want, nil, prev); err != nil {
		return res, err
	}
	prev = res.K[1]

	// K2.
	if err := SlugGuard(live, o.ProcDir, false); err != nil {
		return res, err
	}
	if slugMigrationBeforeCommit != nil {
		// TEST SEAM: the point a concurrent writer would have to hit for its
		// edit to be adopted into K2 (code review round 1, C4).
		if err := slugMigrationBeforeCommit("K2-scan"); err != nil {
			return res, err
		}
	}
	if release, err = lock(); err != nil {
		return res, err
	}
	applied, wrote, err := slugScanClasses(root, from, to, plan, true)
	release()
	if err != nil {
		return res, fmt.Errorf("K2: %w", err)
	}
	if err := slugAssertClasses(applied, plan.Counts.Classes); err != nil {
		return res, fmt.Errorf("K2: %w", err)
	}
	// K2's expected state is what the rewrite pass itself wrote and removed,
	// NOT the dirt found afterwards. Deriving it from the dirt would adopt any
	// concurrent edit to a tracked file into the commit and call it expected.
	var k2Paths []string
	var k2Want []SlugNameStatus
	for _, w := range wrote {
		k2Paths = append(k2Paths, w)
		k2Want = append(k2Want, SlugNameStatus{Status: "M", Old: w})
	}
	for _, h := range plan.holds {
		k2Paths = append(k2Paths, h)
		k2Want = append(k2Want, SlugNameStatus{Status: "D", Old: h})
	}
	// Writing through atomicfile stamps the enclosing tree's `.surface`. Those
	// stamps are the ONE thing allowed beyond the rewrite pass's own paths:
	// they are machine-written, and stampOK below keeps them out of the
	// asserted name-status. Every other dirty path is foreign and refused.
	dirt, err := slugDirt(root)
	if err != nil {
		return res, err
	}
	for _, d := range dirt {
		if path.Base(d) == ".surface" {
			k2Paths = append(k2Paths, d)
		}
	}
	stampOK := func(s SlugNameStatus) bool {
		return (s.Status == "M" || s.Status == "A") && path.Base(s.Old) == ".surface"
	}
	if res.K[2], err = commit(2, k2Paths, k2Want, stampOK, prev); err != nil {
		return res, err
	}
	if n, err := slugGitOut(root, "rev-list", "--count", head+"..HEAD"); err != nil {
		return res, err
	} else if want := strconv.Itoa(countNonEmpty(res.K[:])); strings.TrimSpace(string(n)) != want {
		return res, fmt.Errorf("rev-list %s..HEAD is %s, want %s", head, strings.TrimSpace(string(n)), want)
	}

	// Cache.
	if err := SlugGuard(live, o.ProcDir, false); err != nil {
		return res, err
	}
	cr, err := slugCacheStep(root, from, to, j)
	res.Cache = cr
	if err != nil {
		return res, fmt.Errorf("cache: %w", err)
	}
	slugLog(o.Log, "cache: %s\n", cr)
	return res, nil
}

func countNonEmpty(s []string) int {
	n := 0
	for _, x := range s {
		if x != "" {
			n++
		}
	}
	return n
}

// slugRemoveEmptyTree removes dir and its (now empty) subdirectories,
// deepest first, refusing if any file remains.
func slugRemoveEmptyTree(root, rel string) error {
	base := filepath.Join(root, filepath.FromSlash(rel))
	var dirs []string
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			return fmt.Errorf("%s still holds %s", rel, p)
		}
		dirs = append(dirs, p)
		return nil
	})
	if err != nil {
		return err
	}
	for _, d := range slices.Backward(dirs) {
		if err := vaultfs.RemoveNoLock(d); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The cache step.

// SlugCacheResult counts the cache operations.
type SlugCacheResult struct {
	NothingToDo     bool
	StrayDeleted    int
	Renamed         int
	OrphansSaved    int
	DuplicatesSaved int
	StrayList       string
}

func (r SlugCacheResult) String() string {
	if r.NothingToDo {
		return "nothing to do (marker present)"
	}
	return fmt.Sprintf("stray-deleted %d (list %s), renamed %d, orphans-saved %d, duplicates-saved %d",
		r.StrayDeleted, r.StrayList, r.Renamed, r.OrphansSaved, r.DuplicatesSaved)
}

const slugCacheMarker = ".project-slug-cache-done"
const slugStrayList = ".project-slug-stray.list"

func slugCacheRel(slugName string) string { return "palace/.local/embed-cache/" + slugName }

// slugStrayStems finds the K0 session renames (old stem -> new stem) from the
// K0 commit in history, so a host that only pulled can derive them.
func slugStrayStems(root, from, to string) (map[string]string, error) {
	// The subject must be EQUAL, not merely contain the K0 message: a
	// `Revert "…(0/2 make room)"` contains it and would invert the stems.
	out, err := slugGitOut(root, "log", "-F", "--grep="+slugMsg(0, from, to), "--format=%H%x00%s", "-n", "50")
	if err != nil {
		return nil, err
	}
	k0 := ""
	for line := range strings.SplitSeq(string(out), "\n") {
		f := strings.SplitN(strings.TrimSpace(line), "\x00", 2)
		if len(f) == 2 && f[1] == slugMsg(0, from, to) {
			k0 = f[0]
			break
		}
	}
	stems := map[string]string{}
	if k0 == "" {
		return stems, nil
	}
	nsOut, err := slugGitOut(root, "-c", "core.quotepath=off", "diff-tree", "-r", "-M", "--name-status", "-z", "--no-commit-id", k0+"^", k0)
	if err != nil {
		return nil, err
	}
	f := strings.Split(strings.TrimRight(string(nsOut), "\x00"), "\x00")
	for i := 0; i < len(f); {
		if f[i] == "" {
			i++
			continue
		}
		if strings.HasPrefix(f[i], "R") && i+2 < len(f) {
			o, n := f[i+1], f[i+2]
			pre := "Projects/" + to + "/sessions/"
			if strings.HasPrefix(o, pre) && strings.HasPrefix(n, pre) {
				stems[strings.TrimSuffix(path.Base(o), ".md")] = strings.TrimSuffix(path.Base(n), ".md")
			}
			i += 3
			continue
		}
		i += 2
	}
	return stems, nil
}

func slugSha(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}

// RunSlugCachePhase is `--phase cache`: the per-host cache step.
func RunSlugCachePhase(root, from, to, journalPath, procDir, home string, attest bool, log io.Writer) (SlugCacheResult, error) {
	done, err := SlugApplied(root, from, to)
	if err != nil {
		return SlugCacheResult{}, err
	}
	if !done {
		return SlugCacheResult{}, fmt.Errorf("refusing: the vault tree is not in the migrated state (pull K0..K2 first)")
	}
	live, detail := ClassifySlugVault(root, home)
	slugLog(log, "vault=%s LIVE=%v (%s)\n", root, live, detail)
	if err := SlugGuard(live, procDir, attest); err != nil {
		return SlugCacheResult{}, err
	}
	if err := slugRefuseStaleMarker(root, from, to); err != nil {
		return SlugCacheResult{}, err
	}
	if slugExists(root, slugCacheRel(to)+"/"+slugCacheMarker) {
		return SlugCacheResult{NothingToDo: true}, nil
	}
	// The cache step renames within the vault, so the vault needs only slack.
	// The journal's deleted/ copies are renames too when the journal shares the
	// vault's filesystem, and full copies when it does not.
	if err := slugCheckSpace("the cache step", root, slugSpaceSlack, log); err != nil {
		return SlugCacheResult{}, err
	}
	if need := slugJournalSideBytes(root, from, journalPath); need > 0 {
		if err := slugCheckSpace("the cache step's saved copies", filepath.Dir(journalPath), need, log); err != nil {
			return SlugCacheResult{}, err
		}
	}
	j, err := OpenSlugJournal(journalPath, root)
	if err != nil {
		return SlugCacheResult{}, err
	}
	defer j.Close()
	return slugCacheStep(root, from, to, j)
}

// slugRefuseStaleMarker catches the one state the marker cannot describe: the
// "done" marker is present while the SOURCE cache directory still exists, so
// the step plainly did not finish (or was rolled back without removing the
// marker). Skipping there would leave a stray vector on a live key.
func slugRefuseStaleMarker(root, from, to string) error {
	if slugExists(root, slugCacheRel(to)+"/"+slugCacheMarker) && slugExists(root, slugCacheRel(from)) {
		return fmt.Errorf("refusing: %s says the cache step is done, but %s still exists; remove the marker only by replaying the journal that created it",
			slugCacheRel(to)+"/"+slugCacheMarker, slugCacheRel(from))
	}
	return nil
}

// slugJournalSideBytes is what the journal directory must hold: nothing when
// it shares the vault's filesystem (rm-vec renames), and the whole source
// cache when it does not (rm-vec copies).
func slugJournalSideBytes(root, from, journalPath string) int64 {
	if slugSameDevice(root, filepath.Dir(journalPath)) {
		return 0
	}
	var n int64
	dir := filepath.Join(root, filepath.FromSlash(slugCacheRel(from)))
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n + slugSpaceSlack
}

// sameDevice reports whether two paths sit on one filesystem. It answers false
// when it cannot tell, which is the conservative direction: the caller then
// budgets for a copy that may turn out to be a rename.
func sameDevice(a, b string) bool {
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return false
	}
	da, db := slugDeviceOf(sa), slugDeviceOf(sb)
	return da != 0 && da == db
}

func slugCacheStep(root, from, to string, j *SlugJournal) (SlugCacheResult, error) {
	res := SlugCacheResult{}
	toRel, fromRel := slugCacheRel(to), slugCacheRel(from)
	if err := slugRefuseStaleMarker(root, from, to); err != nil {
		return res, err
	}
	if slugExists(root, toRel+"/"+slugCacheMarker) {
		return SlugCacheResult{NothingToDo: true}, nil
	}
	listRel := toRel + "/" + slugStrayList
	res.StrayList = listRel
	type strayEntry struct{ name, sha string }
	var stray []strayEntry
	if b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(listRel))); err == nil {
		for l := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
			f := strings.Fields(l)
			if len(f) == 2 {
				stray = append(stray, strayEntry{name: f[1], sha: f[0]})
			}
		}
	} else {
		stems, err := slugStrayStems(root, from, to)
		if err != nil {
			return res, err
		}
		ents, _ := os.ReadDir(filepath.Join(root, filepath.FromSlash(toRel)))
		for _, e := range ents {
			for o, n := range stems {
				if strings.HasPrefix(e.Name(), "note."+to+"."+o+".") || strings.HasPrefix(e.Name(), "note."+to+"."+n+".") {
					s, err := slugSha(filepath.Join(root, filepath.FromSlash(toRel), e.Name()))
					if err != nil {
						return res, err
					}
					stray = append(stray, strayEntry{name: e.Name(), sha: s})
				}
			}
		}
		sort.Slice(stray, func(a, b int) bool { return stray[a].name < stray[b].name })
		var buf strings.Builder
		for _, s := range stray {
			fmt.Fprintf(&buf, "%s %s\n", s.sha, s.name)
		}
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(toRel)), 0o755); err != nil {
			return res, err
		}
		if err := slugWriteJournalled(root, listRel, []byte(buf.String()), j); err != nil {
			return res, err
		}
	}
	// The journal-side copy is written on EVERY run that has a list, not only
	// on the run that computed it: a resumed run with a new --journal would
	// otherwise leave RB10 with no file to read.
	{
		var buf strings.Builder
		for _, e := range stray {
			fmt.Fprintf(&buf, "%s %s\n", e.sha, e.name)
		}
		cp := filepath.Join(slugJournalAux(j.path), "stray.list")
		if !slugExistsAbs(cp) {
			if err := slugWriteOutside(cp, []byte(buf.String())); err != nil {
				return res, err
			}
		}
	}
	// 1. delete exactly the listed stray vectors whose sha still matches.
	for _, s := range stray {
		rel := toRel + "/" + s.name
		abs := filepath.Join(root, filepath.FromSlash(rel))
		got, err := slugSha(abs)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return res, err
		}
		if got != s.sha {
			continue
		}
		if err := slugRemoveVec(root, rel, j); err != nil {
			return res, err
		}
		res.StrayDeleted++
	}
	// 2. drawer vectors, from the rewritten rows.
	rooms, _ := filepath.Glob(filepath.Join(root, "palace", to, "drawers", to, "*", "drawers.jsonl"))
	sort.Strings(rooms)
	for _, room := range rooms {
		b, err := os.ReadFile(room)
		if err != nil {
			return res, err
		}
		for l := range strings.SplitSeq(string(b), "\n") {
			var d Drawer
			if strings.TrimSpace(l) == "" || json.Unmarshal([]byte(l), &d) != nil {
				continue
			}
			old := DrawerID(from, d.Content)
			if err := slugCacheMove(root, fromRel+"/"+old+".vec", toRel+"/"+d.ID+".vec", j, &res); err != nil {
				return res, err
			}
		}
	}
	// 3. note./iter. vectors, and 4. orphans, then remove the directory.
	ents, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(fromRel)))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	for _, e := range ents {
		name := e.Name()
		switch {
		case strings.HasPrefix(name, "note."+from+"."):
			err = slugCacheMove(root, fromRel+"/"+name, toRel+"/note."+to+"."+strings.TrimPrefix(name, "note."+from+"."), j, &res)
		case strings.HasPrefix(name, "iter."+from+"."):
			err = slugCacheMove(root, fromRel+"/"+name, toRel+"/iter."+to+"."+strings.TrimPrefix(name, "iter."+from+"."), j, &res)
		default:
			continue
		}
		if err != nil {
			return res, err
		}
	}
	ents, _ = os.ReadDir(filepath.Join(root, filepath.FromSlash(fromRel)))
	for _, e := range ents {
		if err := slugRemoveVec(root, fromRel+"/"+e.Name(), j); err != nil {
			return res, err
		}
		res.OrphansSaved++
	}
	if slugExists(root, fromRel) {
		if err := vaultfs.RemoveNoLock(filepath.Join(root, filepath.FromSlash(fromRel))); err != nil {
			return res, err
		}
	}
	listSha, _ := slugSha(filepath.Join(root, filepath.FromSlash(listRel)))
	if err := slugWriteJournalled(root, toRel+"/"+slugCacheMarker, []byte(listSha+"\n"), j); err != nil {
		return res, err
	}
	return res, nil
}

func slugExistsAbs(p string) bool { _, err := os.Lstat(p); return err == nil }

// slugCacheMove renames one cache file when its source exists. An existing
// destination with identical bytes is a duplicate: the source is saved (rm-vec)
// instead. A destination with different bytes is a refusal.
func slugCacheMove(root, srcRel, dstRel string, j *SlugJournal, res *SlugCacheResult) error {
	src := filepath.Join(root, filepath.FromSlash(srcRel))
	if !slugExistsAbs(src) {
		return nil
	}
	dst := filepath.Join(root, filepath.FromSlash(dstRel))
	if slugExistsAbs(dst) {
		a, err := slugSha(src)
		if err != nil {
			return err
		}
		b, err := slugSha(dst)
		if err != nil {
			return err
		}
		if a != b {
			return fmt.Errorf("refusing: cache destination %s exists with different bytes", dstRel)
		}
		res.DuplicatesSaved++
		return slugRemoveVec(root, srcRel, j)
	}
	if err := slugMove(root, srcRel, dstRel, j); err != nil {
		return err
	}
	res.Renamed++
	return nil
}
