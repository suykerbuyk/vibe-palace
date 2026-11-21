// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"strings"
	"testing"
)

// L11: a rename of a project that holds a signed transcript manifest is refused
// at planning time. The rename rewrites the manifest's project_slug and note link
// and moves the .sig unchanged, which would leave the signature covering bytes
// that no longer exist.
//
// The check is best-effort (it scans once and does not serialize with the
// per-manifest locks archive.Create and the link functions take); that limit is
// documented at renameSignedManifestRefusals and in doc/VAULT-LIFECYCLE.md.
func TestRename_RefusesAProjectHoldingASignedManifest(t *testing.T) {
	f := newRenFix(t, func(o *renOpts) {})

	// Control: with no signature the same project plans cleanly, so the refusal
	// below is attributable to the .sig and to nothing else in the fixture.
	if _, err := PlanRename(f.req()); err != nil {
		t.Fatalf("control: the fixture project must plan cleanly without a signature: %v", err)
	}

	writeFile(t, f.V, "Projects/old/transcripts/x.manifest.json.sig", "-----BEGIN SSH SIGNATURE-----\nfake\n-----END SSH SIGNATURE-----\n")
	_, err := ApplyRename(f.req())
	assertRefused(t, err, "Projects/old/transcripts/x.manifest.json.sig is a manifest signature")
	if !strings.Contains(err.Error(), "best-effort") {
		t.Errorf("the refusal must say it is a best-effort check: %v", err)
	}
	if !renPresent(f.V, "Projects/old/resume.md") || renPresent(f.V, "Projects/new/resume.md") {
		t.Error("the project was moved despite the refusal")
	}
	if _, perr := PlanRename(f.req()); perr == nil {
		t.Error("the dry-run plan did not report the signed-project refusal")
	}
}
