// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package shims

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/agentfile"
)

func TestFilename(t *testing.T) {
	if got := Filename("restart"); got != "vpc-restart.md" {
		t.Fatalf("Filename: got %q, want vpc-restart.md", got)
	}
}

func TestRenderDeterministic(t *testing.T) {
	a := Render("restart", "Reload vault and resume work", "", "")
	b := Render("restart", "Reload vault and resume work", "", "")
	if a != b {
		t.Errorf("Render not deterministic:\na=%q\nb=%q", a, b)
	}
}

func TestRenderContainsRequiredElements(t *testing.T) {
	out := Render("wrap", "Wrap up current work", "", "")
	checks := []string{
		"---\n",                               // frontmatter open
		"description: Vibe-palace command — ", // brief surfaces
		"Wrap up current work",                // user-provided brief
		"argument-hint:",                      // arg hint key present
		"<!-- vibe-palace:shim v=1 sha=",      // opening marker
		" -->",                                // marker closes
		agentfile.CommandToolName,             // delegates to canonical tool
		"name=\"wrap\"",                       // name arg to the tool
		"/vpc-wrap",                           // alias mentioned for $ARGUMENTS
		"$ARGUMENTS",                          // argument-forwarding instruction
		shimCloseDelim,                        // closing marker
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("Render missing %q in:\n%s", want, out)
		}
	}
	// Global command: no project param in the body.
	if strings.Contains(out, "project=") {
		t.Errorf("global Render leaked a project param:\n%s", out)
	}
}

func TestRenderHandlesEmptyBrief(t *testing.T) {
	out := Render("doctor", "", "", "")
	// No " — " suffix when brief is empty.
	if strings.Contains(out, "Vibe-palace command — ") {
		t.Errorf("empty brief produced dash separator:\n%s", out)
	}
	if !strings.Contains(out, "description: Vibe-palace command\n") {
		t.Errorf("plain description missing for empty brief:\n%s", out)
	}
}

func TestRenderSanitizesMultilineBrief(t *testing.T) {
	out := Render("x", "line one\nline two\r\nline three", "", "")
	// Frontmatter must stay single-line — no raw CR or LF inside the
	// description value before the closing "---".
	fmEnd := strings.Index(out, "---\n\n")
	if fmEnd == -1 {
		t.Fatalf("frontmatter not closed:\n%s", out)
	}
	descLineStart := strings.Index(out, "description:")
	if descLineStart == -1 || descLineStart >= fmEnd {
		t.Fatalf("description line missing in frontmatter:\n%s", out)
	}
	descLineEnd := strings.IndexByte(out[descLineStart:], '\n')
	descLine := out[descLineStart : descLineStart+descLineEnd]
	if strings.ContainsAny(descLine, "\r") {
		t.Errorf("description contains CR: %q", descLine)
	}
	// All three segments must survive on the single description line.
	for _, seg := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(descLine, seg) {
			t.Errorf("segment %q missing from description: %q", seg, descLine)
		}
	}
}

// TestRenderProjectScoped verifies a project-scoped command renders the
// project="<slug>" param so the MCP server resolves the project tier.
func TestRenderProjectScoped(t *testing.T) {
	out := Render("res_build", "Generate a targeted resume", "rezbldrvault", "")
	for _, want := range []string{
		"name=\"res_build\"",
		"project=\"rezbldrvault\"",
		", then follow the returned instructions verbatim",
		"/vpc-res_build",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("project Render missing %q in:\n%s", want, out)
		}
	}
}

// TestRenderArgHint verifies a specific argument-hint passes through into the
// shim frontmatter, replacing the generic fallback.
func TestRenderArgHint(t *testing.T) {
	out := Render("res_build", "brief", "rezbldrvault", "[job file path] [--cover-letter]")
	if !strings.Contains(out, "argument-hint: [job file path] [--cover-letter]\n") {
		t.Errorf("specific argument-hint not emitted:\n%s", out)
	}
	if strings.Contains(out, "[args forwarded to the vibe-palace command]") {
		t.Errorf("generic argument-hint fallback leaked despite a specific hint:\n%s", out)
	}
}

func TestContentHashInputsAffectHash(t *testing.T) {
	base := contentHash("restart", "brief", "", "")
	cases := map[string]string{
		"different name":    contentHash("wrap", "brief", "", ""),
		"different brief":   contentHash("restart", "other", "", ""),
		"added project":     contentHash("restart", "brief", "proj", ""),
		"added arghint":     contentHash("restart", "brief", "", "[x]"),
		"different project": contentHash("restart", "brief", "proj2", ""),
	}
	for label, got := range cases {
		if got == base {
			t.Errorf("%s did not change hash (got %s == base %s)", label, got, base)
		}
		if len(got) != 7 {
			t.Errorf("%s: hash len = %d, want 7", label, len(got))
		}
	}
}

// TestContentHashGlobalStable guards the no-churn property: an empty
// project/argHint must hash identically to the pre-feature two-input form, so
// existing global shims are never needlessly rewritten on upgrade.
func TestContentHashGlobalStable(t *testing.T) {
	withEmpties := contentHash("restart", "Reload and resume", "", "")
	// The pre-feature hash inputs, recomputed inline.
	if got := contentHash("restart", "Reload and resume", "", ""); got != withEmpties {
		t.Fatalf("non-deterministic: %s vs %s", got, withEmpties)
	}
	// Sanity: a global shim's hash must NOT equal the same name+brief once a
	// project is folded in (proves conditional inclusion actually fires).
	if withEmpties == contentHash("restart", "Reload and resume", "proj", "") {
		t.Error("global and project hashes collided")
	}
}

func TestExpectedShaMatchesRendered(t *testing.T) {
	name, brief := "restart", "Reload and resume"
	expected := ExpectedSha(name, brief, "", "")
	rendered := Render(name, brief, "", "")
	if !strings.Contains(rendered, "sha="+expected+" ") {
		t.Errorf("rendered sha does not match ExpectedSha (%s):\n%s", expected, rendered)
	}
}

func TestExpectedShaMatchesRenderedProject(t *testing.T) {
	name, brief, proj, hint := "res_build", "brief", "rezbldrvault", "[x]"
	expected := ExpectedSha(name, brief, proj, hint)
	rendered := Render(name, brief, proj, hint)
	if !strings.Contains(rendered, "sha="+expected+" ") {
		t.Errorf("project rendered sha does not match ExpectedSha (%s):\n%s", expected, rendered)
	}
}

func TestScanShimMissingFile(t *testing.T) {
	res, err := ScanShim(filepath.Join(t.TempDir(), "nope.md"))
	if err != nil {
		t.Fatalf("ScanShim on missing: %v", err)
	}
	if res.Exists || res.HasMarker {
		t.Errorf("missing file reported present: %+v", res)
	}
}

func TestScanShimNoMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpc-custom.md")
	if err := os.WriteFile(path, []byte("# I wrote this myself\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ScanShim(path)
	if err != nil {
		t.Fatalf("ScanShim: %v", err)
	}
	if !res.Exists {
		t.Error("Exists = false, want true")
	}
	if res.HasMarker {
		t.Error("HasMarker = true on a file without our marker")
	}
}

func TestScanShimExtractsShaAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpc-restart.md")
	if err := os.WriteFile(path, []byte(Render("restart", "do the thing", "", "")), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ScanShim(path)
	if err != nil {
		t.Fatalf("ScanShim: %v", err)
	}
	if !res.HasMarker {
		t.Fatal("HasMarker = false")
	}
	if res.Version != shimVersion {
		t.Errorf("Version = %d, want %d", res.Version, shimVersion)
	}
	if res.Sha != ExpectedSha("restart", "do the thing", "", "") {
		t.Errorf("Sha = %q, want %q", res.Sha, ExpectedSha("restart", "do the thing", "", ""))
	}
}

func TestScanShimIgnoresCloseMarkerOnly(t *testing.T) {
	// A file that somehow has only the close marker (truncation, manual
	// edit) must not be read as "marker present" — that would cause us to
	// treat it as our own and potentially clobber it without extracting
	// a sha. Treat it as custom.
	path := filepath.Join(t.TempDir(), "vpc-broken.md")
	if err := os.WriteFile(path, []byte("garbage\n"+shimCloseDelim+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ScanShim(path)
	if err != nil {
		t.Fatalf("ScanShim: %v", err)
	}
	if res.HasMarker {
		t.Error("close-only file classified as ours")
	}
}
