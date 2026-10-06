// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// The exact-line rewrite helpers for `vp vault rename` (U9). They were MOVED
// here from the retired one-shot `vp migrate project-slug` engine
// (project_slug_migration.go, deleted in U12), with its K0/make-room machinery
// trimmed off: SlugPlan drops its K0 fields and the `--expect` Counts; the W9
// and W10c (stray-session / held-row) blocks of slugScanClasses are gone (they
// only ran over K0 collision state, which a fresh-target rename never builds);
// and slugMove dropped the one-shot journal. The rewrite classes themselves
// (W1–W8, W10/W10b drawer re-hash) are byte-identical to the one-shot — the
// landed project_rename / copy --as suites are the regression gate. DrawerID /
// Drawer live in drawers.go; SafeGitEnv in git.go.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// SlugMove is one rename, vault-relative.
type SlugMove struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// SlugPlan is the rename's derived move set. It is read-only state.
type SlugPlan struct {
	Root, From, To string
	k1Moves        []SlugMove // tracked
	bakMoves       []SlugMove // untracked (ignored) files under the source trees
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
// It returns the counts and, in apply mode, the vault-relative paths it wrote:
// the rename's expected commit state is built from THOSE, never from dirt.
func slugScanClasses(root, from, to string, p *SlugPlan, apply bool) (map[string]int, []string, error) {
	c := map[string]int{"W1": 0, "W1b": 0, "W2": 0, "W2b": 0, "W3": 0, "W4": 0, "W5": 0, "W6": 0, "W7": 0,
		"W8": 0, "W10": 0, "W10b": 0}
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
		// before the move: the destination exists after iff its source is tracked.
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

	var written []string
	if apply {
		for _, w := range writes {
			abs := filepath.Join(root, filepath.FromSlash(w.rel))
			if err := atomicfile.Write(root, abs, w.data, atomicfile.WithInheritPerm(), atomicfile.WithFsync()); err != nil {
				return nil, nil, fmt.Errorf("rewrite %s: %w", w.rel, err)
			}
			written = append(written, w.rel)
		}
	}
	return c, written, nil
}

// slugAssertClasses refuses when the rewrite pass did not do exactly what the
// counted plan promised: the engine's own answer to "did the rewrite rewrite
// what the plan said it would".
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

// slugMove renames one vault file with the per-move policy: containment,
// regular source, ABSENT destination, and parent creation. (The one-shot's
// journal is gone — rename always moved without one.)
func slugMove(root, srcRel, dstRel string) error {
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
	if err := vaultfs.RenameNoLock(src, dst); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", srcRel, dstRel, err)
	}
	return nil
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
