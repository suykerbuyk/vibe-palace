// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package hook

// G8 — the capture-path spine. Excluding the transcript tier from INDEXING
// must change nothing about CAPTURE: the archive, its manifest and the session
// note are written exactly as before, and only ingest's admission points read
// the setting.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/testutil"
)

// G8 — TestCaptureIsUnaffectedByTranscriptExclusion.
//
// Q1 PATH: wiring on the capture side — it runs hook.Run, the production
// SessionEnd body, against two REAL host configs that differ only in
// [index] transcripts, and compares what landed in the vault byte for byte.
//
// Q2 VALUE: the two runs use OPPOSITE values (true and false). With one value
// only, a gate added on the predicate would behave identically in both arms
// and the comparison would be vacuous.
//
// Q3 PRESENCE: 🔴 an archive WAS created in the first run, asserted.
// GateCapture skips on a stale checkout, a claimed session or a departed
// project, and a Stop event never archives — in any of those, "byte-identical"
// passes trivially in BOTH arms.
//
// Mutation: gate archive.Create on the exclusion predicate (read the config
// and skip the archive when transcripts are excluded). The second arm then
// writes no archive and the comparison reds.
func TestCaptureIsUnaffectedByTranscriptExclusion(t *testing.T) {
	on := captureUnder(t, true)
	off := captureUnder(t, false)

	// Q3: the unmutated run really did archive something.
	if len(on) == 0 {
		t.Fatal("PRESENCE: the capture run wrote NOTHING into the vault. GateCapture skips on a " +
			"stale checkout, a claimed session or a departed project, and a Stop event never " +
			"archives; in any of those a byte-identical comparison passes in both arms and proves " +
			"nothing")
	}
	if !hasArchive(on) {
		t.Fatalf("PRESENCE: the capture run created no archive, only %v", keysOf(on))
	}

	if len(off) != len(on) {
		t.Fatalf("capture wrote %d file(s) with transcripts EXCLUDED and %d with them included.\n"+
			"  included: %v\n  excluded: %v\n"+
			"Excluding the transcript tier is an INDEXING decision: archives are still captured "+
			"exactly as before and are simply never ingested. No file on the capture path may read "+
			"the setting", len(off), len(on), keysOf(on), keysOf(off))
	}
	for name, sum := range on {
		got, ok := off[name]
		if !ok {
			t.Fatalf("capture with transcripts excluded did not write %q at all", name)
		}
		if got != sum {
			t.Fatalf("capture wrote a DIFFERENT %q with transcripts excluded (%s vs %s)", name, got, sum)
		}
	}
}

// captureUnder runs the production SessionEnd capture against a host config
// whose [index] transcripts is `transcripts`, and returns every vault file it
// produced, keyed by a stable name and valued by its content hash.
//
// The names drop the session-id and timestamp components the two runs cannot
// share, so the comparison is about CONTENT and SHAPE, not about two runs
// having picked the same random path.
func captureUnder(t *testing.T, transcripts bool) map[string]string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, ".config"))
	cfgDir := filepath.Join(home, ".config", "vibe-palace")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[index]\ntranscripts = " + map[bool]string{true: "true", false: "false"}[transcripts] + "\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	vaultRoot := t.TempDir()
	testutil.InitProject(t, vaultRoot, "test-project")
	cwd := t.TempDir()
	writeVibeMarker(t, cwd)
	claimDir := filepath.Join(cwd, ".vibe-palace")
	if err := os.MkdirAll(claimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(fakeTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	initGitRepo(t, cwd, "initial commit")

	res, err := Run(context.Background(), Payload{
		SessionID:      "test-session",
		TranscriptPath: transcriptPath,
		CWD:            cwd,
		HookEventName:  "SessionEnd",
	}, RunOptions{
		VaultRoot:   vaultRoot,
		ProjectSlug: "test-project",
		VPVersion:   "test-0.1",
		ClaimDir:    claimDir,
	})
	if err != nil {
		t.Fatalf("hook.Run with transcripts=%v: %v", transcripts, err)
	}
	if res == nil {
		t.Fatalf("hook.Run with transcripts=%v returned no result", transcripts)
	}

	out := map[string]string{}
	root := filepath.Join(vaultRoot, "Projects", "test-project")
	if err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(normalise(data, vaultRoot, cwd, home))
		out[stableName(rel)] = hex.EncodeToString(sum[:])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// stableName strips the date and random components two independent runs cannot
// share, keeping the directory and the file's kind.
func stableName(rel string) string {
	dir, base := filepath.Split(filepath.ToSlash(rel))
	ext := ""
	for _, e := range []string{".jsonl.zst", ".manifest.json", ".md", ".json"} {
		if strings.HasSuffix(base, e) {
			ext = e
			break
		}
	}
	return dir + "*" + ext
}

// normalise removes what two independent runs in different temp directories
// cannot share — the absolute paths, and the session-note id derived from
// them — so the hash is about CONTENT. Nothing here is config-derived, so a
// capture that read [index] transcripts would still show up as a difference.
var noteIDRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}-[0-9a-f]{8}-\d{2}`)

func normalise(data []byte, vaultRoot, cwd, home string) []byte {
	s := string(data)
	for _, p := range []string{vaultRoot, cwd, home} {
		s = strings.ReplaceAll(s, p, "<path>")
	}
	return []byte(noteIDRe.ReplaceAllString(s, "<note-id>"))
}

func hasArchive(m map[string]string) bool {
	for k := range m {
		if strings.HasSuffix(k, ".jsonl.zst") {
			return true
		}
	}
	return false
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
