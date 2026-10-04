// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
)

// KGCensusRow is one project's tracked KG, classified by origin.
type KGCensusRow struct {
	Project           string `json:"project"`
	TriplesAuthored   int    `json:"triples_authored"`
	TriplesExtracted  int    `json:"triples_extracted"`
	TriplesRule3      int    `json:"triples_rule3"` // authored by ClassifyTriple rule 3 (X5), counted in TriplesAuthored
	TriplesInGit      int    `json:"triples_in_git"`
	EntitiesAuthored  int    `json:"entities_authored"`
	EntitiesExtracted int    `json:"entities_extracted"`
	EntitiesInGit     int    `json:"entities_in_git"`
}

// Matches reports whether the classified totals equal the independent counts
// of the same copy's HEAD exactly. There is no tolerance and no unclassified
// bucket.
func (r KGCensusRow) Matches() bool {
	return r.TriplesAuthored+r.TriplesExtracted == r.TriplesInGit &&
		r.EntitiesAuthored+r.EntitiesExtracted == r.EntitiesInGit
}

// KGCensus classifies every tracked KG triple and entity line of a vault COPY
// with ClassifyTriple and ClassifyEntityLine, per project, and counts the same
// files independently from the copy's HEAD:
//
//   - triples: the files `git ls-tree -r --name-only HEAD -- palace/<p>/kg/triples/` names;
//   - entities: the non-empty lines of `git cat-file -p HEAD:palace/<p>/kg/entities.jsonl`.
//
// It is read-only, and is meant for a remote-stripped copy, never the live
// vault: the working tree it classifies must equal HEAD, which a running
// writer would break. A file it cannot parse is an error, not a skip, because
// the census has no unclassified bucket.
func KGCensus(root string) ([]KGCensusRow, error) {
	if _, err := os.Lstat(filepath.Join(root, ".git")); err != nil {
		return nil, fmt.Errorf("%s is not a git repository: the census compares against HEAD", root)
	}
	ents, err := os.ReadDir(filepath.Join(root, "palace"))
	if err != nil {
		return nil, fmt.Errorf("read palace dir: %w", err)
	}
	var rows []KGCensusRow
	for _, e := range ents {
		p := e.Name()
		if !e.IsDir() || slug.Validate(p) != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "palace", p, "kg")); err != nil {
			continue
		}
		row, err := censusProject(root, p)
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", p, err)
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b KGCensusRow) int { return strings.Compare(a.Project, b.Project) })
	return rows, nil
}

func censusProject(root, p string) (KGCensusRow, error) {
	row := KGCensusRow{Project: p}
	files, err := filepath.Glob(filepath.Join(root, "palace", p, "kg", "triples", "*.json"))
	if err != nil {
		return row, err
	}
	for _, f := range files {
		t, err := readTripleFile(f)
		if err != nil {
			return row, err
		}
		if ClassifyTriple(t) == OriginAuthored {
			row.TriplesAuthored++
			if t.Origin == "" && t.ExtractedAt != "" && t.ValidTo != "" {
				row.TriplesRule3++
			}
		} else {
			row.TriplesExtracted++
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "palace", p, "kg", "entities.jsonl")); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 64*1024), maxEntityLine)
		for n := 1; sc.Scan(); n++ {
			if len(bytes.TrimSpace(sc.Bytes())) == 0 {
				continue
			}
			var en Entity
			if err := json.Unmarshal(sc.Bytes(), &en); err != nil {
				return row, fmt.Errorf("entities.jsonl line %d: %w", n, err)
			}
			if ClassifyEntityLine(en) == OriginAuthored {
				row.EntitiesAuthored++
			} else {
				row.EntitiesExtracted++
			}
		}
		if err := sc.Err(); err != nil {
			return row, fmt.Errorf("scan entities.jsonl: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return row, err
	}

	out, err := censusGit(root, "ls-tree", "-r", "--name-only", "-z", "HEAD", "--", "palace/"+p+"/kg/triples/")
	if err != nil {
		return row, err
	}
	for name := range bytes.SplitSeq(out, []byte{0}) {
		if bytes.HasSuffix(name, []byte(".json")) {
			row.TriplesInGit++
		}
	}
	rel := "palace/" + p + "/kg/entities.jsonl"
	if listed, err := censusGit(root, "ls-tree", "--name-only", "HEAD", "--", rel); err != nil {
		return row, err
	} else if len(bytes.TrimSpace(listed)) > 0 {
		blob, err := censusGit(root, "cat-file", "-p", "HEAD:"+rel)
		if err != nil {
			return row, err
		}
		for line := range bytes.SplitSeq(blob, []byte{'\n'}) {
			if len(bytes.TrimSpace(line)) > 0 {
				row.EntitiesInGit++
			}
		}
	}
	return row, nil
}

// censusGit runs one read-only git command in the copy.
func censusGit(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	cmd.Env = SafeGitEnv("GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
