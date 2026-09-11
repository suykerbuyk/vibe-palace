// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"regexp"
	"strings"
	"testing"
)

// frozenManifestSHA256 is the sha256 of shipped.txt as frozen at the 1f3bb62
// last-writer boundary. The manifest is never regenerated: a change to it is a
// deliberate, reviewed edit that updates this constant in the same commit.
const frozenManifestSHA256 = "b0acf9d64dc9f5596f346e07d59ef56c0992a2020cfb12fec5f4ddfcc434c831"

// The two rows that come from the tag pre-rebase-501c96e, whose commit is not
// an ancestor of 1f3bb62, and the git blob each was derived from.
var tagRows = map[string]string{
	"3a237999814fd03956a59518ae505f55b170daa11c774c4074348562b35a1c47  commands/capture.md": "17505a1e135686913bb14a1e1845fac6b4c89e25",
	"2f13399366aef10ebc04d2ecd2eba3344557435637ba15faa924a3e2cf5aa8ea  commands/wrap.md":    "3924d6ca85fa89d5b298dd57191112bddafa8427",
}

func readManifest(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("shipped.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestShippedManifestIsFrozen pins the file's content, on disk and embedded.
func TestShippedManifestIsFrozen(t *testing.T) {
	disk := readManifest(t)
	for name, body := range map[string]string{"shipped.txt on disk": disk, "the embedded shipped.txt": shippedManifest} {
		sum := sha256.Sum256([]byte(body))
		if got := hex.EncodeToString(sum[:]); got != frozenManifestSHA256 {
			t.Errorf("%s hashes to %s, want %s.\n"+
				"shipped.txt is FROZEN at the 1f3bb62 last-writer boundary: after it no vp binary "+
				"writes a template into a vault, so no later version can be a row. A change must be a "+
				"deliberate, reviewed edit that updates frozenManifestSHA256 in the same commit; verify "+
				"the rows with the derivation command in the file's header (task "+
				"template-provenance-manifest-retires-the-host-local-lock, Plan §2). A template edit "+
				"needs no change here.", name, got, frozenManifestSHA256)
		}
	}
}

var manifestRow = regexp.MustCompile(`^[0-9a-f]{64}  \S.*\.md$`)

func TestShippedManifestWellFormed(t *testing.T) {
	disk := readManifest(t)
	if strings.Contains(disk, "\r") {
		t.Fatal("shipped.txt holds a CR: it must be checked out LF (.gitattributes)")
	}
	lines := strings.Split(strings.TrimSuffix(disk, "\n"), "\n")
	var header []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") {
			break
		}
		header = append(header, l)
	}
	head := strings.Join(header, "\n")
	for _, must := range []string{"FROZEN", "1f3bb62", "git rev-list --full-history", "pre-rebase-501c96e", "set -eu -o pipefail"} {
		if !strings.Contains(head, must) {
			t.Errorf("the header does not name %q", must)
		}
	}

	type row struct{ key, rel string }
	var rows []row
	extras := 0
	for i := len(header); i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "# extra:") {
			extras++
			if i+1 >= len(lines) {
				t.Fatalf("line %d: an # extra: annotation ends the file", i+1)
			}
			oid, ok := tagRows[lines[i+1]]
			if !ok {
				t.Errorf("line %d: # extra: precedes %q, which is not a tag row", i+1, lines[i+1])
			} else if !strings.Contains(l, "pre-rebase-501c96e") || !strings.HasSuffix(l, "blob "+oid) {
				t.Errorf("line %d: the annotation does not name the tag and blob %s: %q", i+1, oid, l)
			}
			continue
		}
		if !manifestRow.MatchString(l) {
			t.Errorf("line %d is not a row: %q", i+1, l)
			continue
		}
		key, rel, _ := strings.Cut(l, "  ")
		if path.Clean(rel) != rel || strings.HasPrefix(rel, "/") || strings.Contains(rel, "..") {
			t.Errorf("line %d: relpath %q is not clean and relative", i+1, rel)
		}
		if _, isTag := tagRows[l]; isTag && !strings.HasPrefix(lines[i-1], "# extra:") {
			t.Errorf("line %d: the tag row has no # extra: annotation directly before it", i+1)
		}
		rows = append(rows, row{key, rel})
	}
	if extras != 2 {
		t.Errorf("%d # extra: annotations, want 2", extras)
	}
	for i := 1; i < len(rows); i++ {
		a, b := rows[i-1], rows[i]
		if a.rel > b.rel || (a.rel == b.rel && a.key >= b.key) {
			t.Errorf("rows out of (relpath, key) order or duplicated at %v -> %v", a, b)
		}
	}
	// Every row parses: the parser's skip of a malformed line is unreachable
	// for the real file.
	parsed := parseShipped(disk)
	n := 0
	for _, keys := range parsed {
		n += len(keys)
	}
	if n != len(rows) {
		t.Errorf("parsed %d rows of %d", n, len(rows))
	}
}
