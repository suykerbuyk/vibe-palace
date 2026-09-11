// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"reflect"
	"strings"
	"testing"
)

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestProvenanceKey(t *testing.T) {
	if got, want := ProvenanceKey([]byte("a\r\nb\r\n")), ProvenanceKey([]byte("a\nb\n")); got != want {
		t.Errorf("CRLF and LF forms keyed apart: %s vs %s", got, want)
	}
	if got := ProvenanceKey([]byte("a\nb\n")); got != sha("a\nb\n") {
		t.Errorf("an LF body is not its plain sha256: %s", got)
	}
	if got := ProvenanceKey([]byte("a\rb\n")); got != sha("a\rb\n") {
		t.Errorf("a lone CR was dropped: %s", got)
	}
	// One pass, pinned: "\r\r\n" becomes "\r\n", never "\n".
	if got := ProvenanceKey([]byte("a\r\r\n")); got != sha("a\r\n") {
		t.Errorf(`"\r\r\n" is not keyed as "\r\n": %s`, got)
	}
	if got := ProvenanceKey(nil); got != sha("") {
		t.Errorf("empty = %s, want sha256(\"\")", got)
	}
}

// withSeams substitutes EmbeddedSHA and ShippedVersion for one test.
func withSeams(t *testing.T, emb map[string]string, shipped map[string]map[string]bool) {
	t.Helper()
	origE, origS := EmbeddedSHA, ShippedVersion
	t.Cleanup(func() { EmbeddedSHA, ShippedVersion = origE, origS })
	EmbeddedSHA = func(rel string) (string, bool) {
		k, ok := emb[rel]
		return k, ok
	}
	ShippedVersion = func(rel, key string) bool { return shipped[rel][key] }
}

func TestClassifyVaultCopy(t *testing.T) {
	const (
		current = "# current wrap\n"
		earlier = "# earlier wrap\n"
		restart = "# current restart\n"
	)
	withSeams(t,
		map[string]string{"commands/wrap.md": sha(current), "commands/restart.md": sha(restart)},
		map[string]map[string]bool{"commands/wrap.md": {sha(earlier): true}})

	cases := []struct {
		name, rel, body string
		want            Provenance
	}{
		{"current", "commands/wrap.md", current, ProvenanceCurrent},
		{"earlier through the seam", "commands/wrap.md", earlier, ProvenanceEarlier},
		{"operator", "commands/wrap.md", "# my wrap\n", ProvenanceOperator},
		{"unknown relpath", "commands/none.md", current, ProvenanceOperator},
		{"empty", "commands/wrap.md", "", ProvenanceOperator},
		{"CRLF copy of current", "commands/wrap.md", strings.ReplaceAll(current, "\n", "\r\n"), ProvenanceCurrent},
		{"CRLF copy of earlier", "commands/wrap.md", strings.ReplaceAll(earlier, "\n", "\r\n"), ProvenanceEarlier},
		{"bytes shipped under another relpath", "commands/restart.md", earlier, ProvenanceOperator},
		{"another built-in's current bytes", "commands/restart.md", current, ProvenanceOperator},
	}
	for _, tc := range cases {
		if got := ClassifyVaultCopy(tc.rel, []byte(tc.body)); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestClassifyVaultCopyCRLFEmbedded: an LF vault copy of an embedded template
// that itself carries CRLF (a build from a CRLF checkout) is still current,
// because realEmbeddedSHA keys the embedded bytes the same way.
func TestClassifyVaultCopyCRLFEmbedded(t *testing.T) {
	crlf := []byte("# built from a CRLF checkout\r\nline\r\n")
	origE := EmbeddedSHA
	t.Cleanup(func() { EmbeddedSHA = origE })
	EmbeddedSHA = func(rel string) (string, bool) { return ProvenanceKey(crlf), rel == "workflow.md" }
	if got := ClassifyVaultCopy("workflow.md", []byte("# built from a CRLF checkout\nline\n")); got != ProvenanceCurrent {
		t.Errorf("got %v, want current", got)
	}
}

// TestClassifyVaultCopyRealCorpus runs the production seams: every embedded
// resource classifies current against itself and in its CRLF form.
func TestClassifyVaultCopyRealCorpus(t *testing.T) {
	rs, err := WalkEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if got := ClassifyVaultCopy(r.RelPath, r.Bytes); got != ProvenanceCurrent {
			t.Errorf("%s: %v", r.RelPath, got)
		}
		if got := ClassifyVaultCopy(r.RelPath, []byte(strings.ReplaceAll(string(r.Bytes), "\n", "\r\n"))); got != ProvenanceCurrent {
			t.Errorf("%s (CRLF): %v", r.RelPath, got)
		}
		if k, _ := EmbeddedSHA(r.RelPath); k != r.SHA256 {
			t.Errorf("%s: EmbeddedSHA %s differs from the raw sha %s — the corpus holds a CR", r.RelPath, k, r.SHA256)
		}
	}
}

func TestProvenanceString(t *testing.T) {
	for p, want := range map[Provenance]string{
		ProvenanceOperator: "operator", ProvenanceCurrent: "current", ProvenanceEarlier: "earlier", Provenance(99): "operator",
	} {
		if got := p.String(); got != want {
			t.Errorf("%d: %q, want %q", p, got, want)
		}
	}
	var zero Provenance
	if zero != ProvenanceOperator {
		t.Error("the zero value must keep the file")
	}
}

func TestShippedVersionParse(t *testing.T) {
	k1, k2 := sha("one"), sha("two")
	src := strings.Join([]string{
		"# a header line",
		"",
		k1 + "  commands/wrap.md\r", // CRLF checkout of the manifest
		"not a row at all",
		"XYZ  commands/wrap.md",
		strings.ToUpper(k2) + "  commands/wrap.md", // keys are lowercase
		k2 + "  ",
		k2 + " commands/single-space.md",
		k2 + "  skills/chair/SKILL.md",
	}, "\n")
	rows := parseShipped(src)
	want := map[string]map[string]bool{
		"commands/wrap.md":      {k1: true},
		"skills/chair/SKILL.md": {k2: true},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %v, want %v", rows, want)
	}

	// The seam answers membership, relpath-scoped, and exposes no map.
	var seam func(string, string) bool = ShippedVersion
	_ = seam
	if !realShippedVersion("commands/restart.md", sha(readFixture(t))) {
		t.Error("the committed earlier fixture is not a row of the real manifest")
	}
	if realShippedVersion("commands/wrap.md", sha(readFixture(t))) {
		t.Error("restart.md's bytes matched under wrap.md: rows are not relpath-scoped")
	}
	if realShippedVersion("commands/restart.md", strings.Repeat("0", 64)) {
		t.Error("a key that is no row matched")
	}
}

func readFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/earlier/commands/restart.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestEarlierFixtureIsAShippedVersion pins the fixture every earlier-version
// test in cmd/vp and internal/integration reads: the version of
// commands/restart.md just before its current one in 1f3bb62's history
// (23bedcc). If the current restart.md ever equals it, or the manifest lost
// it, those tests would pass for the wrong reason.
func TestEarlierFixtureIsAShippedVersion(t *testing.T) {
	if got := ClassifyVaultCopy("commands/restart.md", []byte(readFixture(t))); got != ProvenanceEarlier {
		t.Errorf("fixture classifies %v, want earlier", got)
	}
}
