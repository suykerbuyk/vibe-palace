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
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
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
// THE SCOPE OF WHAT THIS PINS, STATED NARROWLY: hook capture does not USE the
// decoded [index] transcripts setting. hook.Run loads the generic vault config
// (for cfg.Enrichment), and that loader can decode the value, but nothing on the
// capture path consumes or branches on it; RunOptions and the archive/session
// options carry no exclusion predicate. Exclusion is an ingest/indexing policy.
//
// WALL-CLOCK FIELDS ARE NOT COMPARED ACROSS ARMS, AND THAT WAS THE FLAKE. The two
// arms run back to back, and three things they write are functions of the clock,
// not of the setting: the manifest's captured_at (time.Now at one-second
// resolution, archive.go) and git_head (the SHA of the fixture's own fresh
// commit, which covers the commit time in seconds), and the session note's
// calendar date (storage.CalendarDay, the process-local zone). Hashing them made
// the test pass only when both arms fell inside one second, so it failed on a
// loaded host whenever the pair straddled a boundary (2 of 30 under -count=30).
// They are masked for the cross-arm equality and each arm is validated on its
// own instead (captureUnder), so masking does not let a wrong value through.
//
// Mutations (each must redden): gate archive.Create on the exclusion predicate
// (the second arm writes no archive); make any UNMASKED manifest field depend on
// the setting (e.g. TurnCount); make git_head or captured_at wrong or
// setting-dependent (the per-arm checks); remove the masks (the forced-boundary
// test below).
func TestCaptureIsUnaffectedByTranscriptExclusion(t *testing.T) {
	on := captureUnder(t, true)
	off := captureUnder(t, false)
	compareArms(t, on, off)
}

// TestCaptureComparisonIgnoresWallClock is the deterministic regression for the
// flake: the two arms are forced across a second boundary, so every wall-clock
// field differs between them for certain. With the masks it passes every time;
// without them it fails every time (the intermittent test above does not).
//
// The sleep is the clock "seam" on purpose. hook.Run does not thread a clock
// into archive.Create or capture.WriteSession (their Now options exist but the
// hook never sets them), so an injected clock would be a production change made
// only for this test.
func TestCaptureComparisonIgnoresWallClock(t *testing.T) {
	on := captureUnder(t, true)
	time.Sleep(1150 * time.Millisecond)
	off := captureUnder(t, false)
	compareArms(t, on, off)
}

// TestCaptureComparisonIgnoresCalendarDay is the deterministic regression for the
// OTHER wall-clock hazard: the session note's calendar date (and the archive
// name it records) comes from storage.CalendarDay, the process-local zone, so two
// arms that cross local midnight would differ. The arms run in zones 26 hours
// apart, so their calendar days differ for certain whatever the real time is.
// Without the note-date mask this fails every time; with it, it passes.
//
// time.Local is process-global, so the zone is restored after each arm and this
// test must not run in parallel (nothing in this package does).
func TestCaptureComparisonIgnoresCalendarDay(t *testing.T) {
	under := func(zone *time.Location, transcripts bool) map[string]string {
		prev := time.Local
		time.Local = zone
		defer func() { time.Local = prev }()
		return captureUnder(t, transcripts)
	}
	on := under(time.FixedZone("UTC+14", 14*3600), true)
	off := under(time.FixedZone("UTC-12", -12*3600), false)
	compareArms(t, on, off)
}

// compareArms is the cross-arm assertion, shared by both tests above.
func compareArms(t *testing.T, on, off map[string]string) {
	t.Helper()
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
// captureSessionID is the payload session id; the archive stem carries it.
const captureSessionID = "test-session"

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

	start := time.Now().UTC()
	res, err := Run(context.Background(), Payload{
		SessionID:      captureSessionID,
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
	end := time.Now().UTC()
	if res == nil {
		t.Fatalf("hook.Run with transcripts=%v returned no result", transcripts)
	}
	fixtureHead := strings.TrimSpace(gitOutput(t, cwd, "rev-parse", "HEAD"))

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
		for _, p := range []string{vaultRoot, cwd, home} {
			if strings.Contains(string(data), p) {
				t.Errorf("transcripts=%v: %s embeds the absolute path %q; normalise() replaces paths, so a path in captured output would be masked, not pinned", transcripts, rel, p)
			}
		}
		norm := normalise(data, vaultRoot, cwd, home)
		switch {
		case strings.HasSuffix(rel, ".manifest.json"):
			norm = maskManifestClock(t, transcripts, data, norm, vaultRoot, fixtureHead, start, end)
		case strings.HasPrefix(filepath.ToSlash(rel), "sessions/") && strings.HasSuffix(rel, ".md"):
			norm = maskNoteDate(t, transcripts, rel, data, norm, vaultRoot, start, end)
		}
		sum := sha256.Sum256(norm)
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

var (
	manifestClockRe = regexp.MustCompile(`"(captured_at|git_head)": "[^"]*"`)
	noteDateRe      = regexp.MustCompile(`(?m)^date: "(\d{4}-\d{2}-\d{2})"$`)
	noteFileRe      = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})-[0-9a-f]{8}-\d{2}\.md$`)
	noteSessionIDRe = regexp.MustCompile(`(?m)^session_id: (\S+)$`)
	notePathRe      = regexp.MustCompile(`(?m)^note_path: (\S+)$`)
	// noteArchiveLinkRe captures the link without its suffix, and its day.
	noteArchiveLinkRe = regexp.MustCompile(`(?m)^archive: (\S*/transcripts/(\d{4}-\d{2}-\d{2})-\S+?)\.manifest\.json$`)
	noteArchiveRe     = regexp.MustCompile(`(?m)^(archive: .*/transcripts/)\d{4}-\d{2}-\d{2}-`)
)

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return string(out)
}

// maskManifestClock validates THIS arm's manifest clock fields independently and
// then masks them, so the cross-arm hash is about everything else.
//
//   - git_head must equal this arm's own fixture checkout HEAD. A shape check
//     (40 hex) would accept a well-formed but wrong value, which is what the
//     mask would otherwise hide.
//   - captured_at must parse as UTC RFC3339 and lie in [start, end] recorded
//     around this arm's hook.Run. The field has one-second resolution, so the
//     lower bound is start truncated to the second.
//
// raw is the unnormalised manifest; norm is what is returned, masked.
func maskManifestClock(t *testing.T, transcripts bool, raw, norm []byte, vaultRoot, fixtureHead string, start, end time.Time) []byte {
	t.Helper()
	var m struct {
		CapturedAt string `json:"captured_at"`
		GitHead    string `json:"git_head"`
		NotePath   string `json:"vault_rel_session_note"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("transcripts=%v: manifest is not JSON: %v", transcripts, err)
	}
	if m.GitHead != fixtureHead {
		t.Errorf("transcripts=%v: manifest git_head = %q, want this arm's fixture HEAD %q", transcripts, m.GitHead, fixtureHead)
	}
	at, err := time.Parse(time.RFC3339, m.CapturedAt)
	if err != nil {
		t.Fatalf("transcripts=%v: captured_at %q is not RFC3339: %v", transcripts, m.CapturedAt, err)
	}
	if !strings.HasSuffix(m.CapturedAt, "Z") {
		t.Errorf("transcripts=%v: captured_at %q is not UTC", transcripts, m.CapturedAt)
	}
	if lo := start.Truncate(time.Second); at.Before(lo) || at.After(end) {
		t.Errorf("transcripts=%v: captured_at %s lies outside this arm's capture interval [%s, %s]",
			transcripts, at.Format(time.RFC3339), lo.Format(time.RFC3339), end.Format(time.RFC3339))
	}
	// vault_rel_session_note is rewritten to <note-id> by normalise, so it is
	// pinned here: it must name a session note that exists in THIS arm's vault.
	if nm := noteFileRe.FindStringSubmatch(filepath.Base(m.NotePath)); nm == nil || filepath.ToSlash(filepath.Dir(m.NotePath)) != "Projects/test-project/sessions" {
		t.Errorf("transcripts=%v: manifest vault_rel_session_note %q is not a Projects/test-project/sessions/<day>-<id>-<nn>.md path", transcripts, m.NotePath)
	} else if _, err := os.Stat(filepath.Join(vaultRoot, filepath.FromSlash(m.NotePath))); err != nil {
		t.Errorf("transcripts=%v: manifest vault_rel_session_note %q does not exist in this arm's vault: %v", transcripts, m.NotePath, err)
	}
	return []byte(manifestClockRe.ReplaceAllString(string(norm), `"$1": "<volatile>"`))
}

// maskNoteDate masks the session note's calendar date and the date inside its
// archive link, and ONLY in the session note. Both come from
// storage.CalendarDay in the process-local zone, so two arms that cross local
// midnight would otherwise differ. The date must be present and a real date, so
// the mask cannot hide a note that lost it.
//
// The note is YAML front matter and the date is a quoted string: `date: "YYYY-MM-DD"`.
//
// 🔴 THE ARCHIVE LINK IS VALIDATED PER ARM BEFORE ITS DATE IS MASKED. A masked
// component that nothing else checks would hide a real defect in the link (a
// review mutated the excluded arm's link date to 1999-12-31 and the test still
// passed). So, for THIS arm:
//
//   - the `archive:` link must name a manifest that exists in THIS arm's vault,
//     with a .jsonl.zst of the same stem beside it (the archive this arm produced);
//   - the link's calendar day must be the arm's actual capture day, derived the way
//     production derives it (storage.CalendarDay, in the zone in force for the arm)
//     for the capture interval [start, end]. Either end of the interval is accepted
//     so a capture that itself crosses local midnight is not a false failure.
//
// raw is the unnormalised note; norm is what is returned, masked.
func maskNoteDate(t *testing.T, transcripts bool, rel string, raw, norm []byte, vaultRoot string, start, end time.Time) []byte {
	t.Helper()
	m := noteDateRe.FindSubmatch(norm)
	if m == nil {
		t.Fatalf("transcripts=%v: session note has no `date:` line", transcripts)
	}
	if _, err := time.Parse("2006-01-02", string(m[1])); err != nil {
		t.Fatalf("transcripts=%v: session note date %q is not a valid date: %v", transcripts, m[1], err)
	}
	d1, d2 := storage.CalendarDay(start), storage.CalendarDay(end)
	if day := string(m[1]); day != d1 && day != d2 {
		t.Errorf("transcripts=%v: session note date %q is not this arm's capture day (%s..%s in the zone in force)", transcripts, day, d1, d2)
	}

	// The note's own identity is rewritten to <note-id> by normalise (and its
	// file name stripped by stableName), so it is pinned here: the file name has
	// the id shape and this arm's capture day, and the note's session_id and
	// note_path agree with the file it lives in.
	base := filepath.Base(rel)
	if nm := noteFileRe.FindStringSubmatch(base); nm == nil {
		t.Errorf("transcripts=%v: session note file name %q is not <day>-<id>-<nn>.md", transcripts, base)
	} else if nm[1] != d1 && nm[1] != d2 {
		t.Errorf("transcripts=%v: session note file name day %q is not this arm's capture day (%s..%s)", transcripts, nm[1], d1, d2)
	}
	id := strings.TrimSuffix(base, ".md")
	if sm := noteSessionIDRe.FindSubmatch(raw); sm == nil || string(sm[1]) != id {
		t.Errorf("transcripts=%v: note session_id %q does not match its file name %q", transcripts, sm, id)
	}
	if pm, want := notePathRe.FindSubmatch(raw), "Projects/test-project/sessions/"+base; pm == nil || string(pm[1]) != want {
		t.Errorf("transcripts=%v: note note_path %q does not match its location %q", transcripts, pm, want)
	}

	am := noteArchiveLinkRe.FindSubmatch(raw)
	if am == nil {
		t.Fatalf("transcripts=%v: session note has no `archive:` link to a transcripts/<day>-<stem>.manifest.json", transcripts)
	}
	stemPath := string(am[1]) // vault-relative, without the .manifest.json suffix
	linkDay := string(am[2])
	// The archive stem is <day>-<session id>; stableName strips both from the file
	// names, so they are pinned here against the payload's session id.
	if want := linkDay + "-" + captureSessionID; filepath.Base(stemPath) != want {
		t.Errorf("transcripts=%v: the note's archive stem %q is not %q", transcripts, filepath.Base(stemPath), want)
	}
	for _, suffix := range []string{".manifest.json", ".jsonl.zst"} {
		if _, err := os.Stat(filepath.Join(vaultRoot, filepath.FromSlash(stemPath+suffix))); err != nil {
			t.Errorf("transcripts=%v: the note's archive link %q does not resolve to an archive THIS arm produced (%s missing): %v",
				transcripts, stemPath+".manifest.json", suffix, err)
		}
	}
	if linkDay != d1 && linkDay != d2 {
		t.Errorf("transcripts=%v: the note's archive link day %q is not this arm's capture day (%s..%s in the zone in force)",
			transcripts, linkDay, d1, d2)
	}

	norm = noteArchiveRe.ReplaceAll(norm, []byte("${1}<date>-"))
	return noteDateRe.ReplaceAll(norm, []byte(`date: "<date>"`))
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
