// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testutil"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// repairFixture is an initialised vault with ONE orphaned archive: a real zst
// made by Create (so the bytes, the name and the date are exactly what capture
// writes), its manifest then removed, and its mtime set to `age` before now.
type repairFixture struct {
	root, project, session string
	zst, manifest, name    string
	source                 []byte
	mtime                  time.Time
}

func newRepairFixture(t *testing.T, age time.Duration) *repairFixture {
	t.Helper()
	root := t.TempDir()
	return newRepairFixtureIn(t, root, age)
}

func newRepairFixtureIn(t *testing.T, root string, age time.Duration) *repairFixture {
	t.Helper()
	const project, session = "p", "sess-repair-1"
	testutil.InitProject(t, root, project)
	src := []byte(sampleInlineJSONL)
	captured := time.Now().Add(-90 * time.Minute)
	res, err := Create(CreateOptions{
		Adapter: InlineAdapterName, SessionID: session, SourceContent: src,
		VaultRoot: root, ProjectSlug: project, Now: captured, VPVersion: "test",
	})
	if err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	if err := os.Remove(res.ManifestPath); err != nil {
		t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(res.ArchivePath, mt, mt); err != nil {
		t.Fatal(err)
	}
	return &repairFixture{root: root, project: project, session: session, zst: res.ArchivePath,
		manifest: res.ManifestPath, name: strings.TrimSuffix(filepath.Base(res.ArchivePath), zstSuffix), source: src, mtime: mt}
}

func (f *repairFixture) opts() RepairOptions {
	return RepairOptions{
		VaultRoot: f.root, Project: f.project, Archive: f.name,
		Adapter: ClaudeCodeAdapterName, CapturedAt: time.Now().Add(-100 * time.Minute).UTC().Format(time.RFC3339),
		VPVersion: "repair-test",
	}
}

func fileSum(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func panicText(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

func absent(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	return errors.Is(err, os.ErrNotExist)
}

// R2: the happy path, every field, the zst untouched, and the end-to-end claim of
// the done-definition: the next tidy sweeps both halves.
func TestRepairManifestWritesATruthfulManifestBesideTheUntouchedArchive(t *testing.T) {
	f := newRepairFixture(t, time.Hour)
	zstBefore, mtimeBefore := fileSum(t, f.zst), f.mtime
	opts := f.opts()
	opts.CapturedAt = "2026-11-01T05:31:54-07:00" // an offset input: must be stored in UTC
	now := time.Now().UTC().Truncate(time.Second)
	opts.Now = now

	res, err := RepairManifest(opts)
	if err != nil {
		t.Fatalf("RepairManifest: %v", err)
	}
	if res.NothingToDo != "" || res.Manifest == nil {
		t.Fatalf("nothing was written: %+v", res)
	}
	m, err := ReadManifest(f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(f.source)
	zst, _ := os.Stat(f.zst)
	a, _ := LookupAdapter(ClaudeCodeAdapterName)
	turns, model, _ := a.Inspect(writeTemp(t, f.source))
	checks := map[string][2]any{
		"schema_version":    {m.SchemaVersion, ManifestSchemaVersion},
		"adapter":           {m.Adapter, ClaudeCodeAdapterName},
		"adapter_version":   {m.AdapterVersion, a.Version()},
		"session_id":        {m.SessionID, f.session},
		"project_slug":      {m.ProjectSlug, f.project},
		"source_sha256":     {m.SourceSHA256, hex.EncodeToString(sum[:])},
		"source_bytes":      {m.SourceBytes, int64(len(f.source))},
		"compressed_bytes":  {m.CompressedBytes, zst.Size()},
		"turn_count":        {m.TurnCount, turns},
		"model":             {m.Model, model},
		"captured_at (UTC)": {m.CapturedAt, "2026-11-01T12:31:54Z"},
		"git_head empty":    {m.GitHead, ""},
		"hostname empty":    {m.CapturedByHostname, ""},
		"vp_version empty":  {m.VPVersion, ""},
		"note link empty":   {m.VaultRelSessionNote, ""},
	}
	for k, c := range checks {
		if !reflect.DeepEqual(c[0], c[1]) {
			t.Errorf("%s = %v, want %v", k, c[0], c[1])
		}
	}
	if m.Regenerated == nil {
		t.Fatal("the regenerated marker is missing")
	}
	wantBases := map[string]string{"session_id": "filename", "project_slug": "path", "adapter": "operator-supplied",
		"adapter_version": "inspecting version at repair time", "captured_at": "operator-supplied"}
	if !reflect.DeepEqual(m.Regenerated.Bases, wantBases) {
		t.Errorf("bases = %v, want %v", m.Regenerated.Bases, wantBases)
	}
	if !slices.Equal(m.Regenerated.Unrecoverable, []string{"git_head", "captured_by_hostname", "vp_version", "vault_rel_session_note"}) {
		t.Errorf("unrecoverable = %v", m.Regenerated.Unrecoverable)
	}
	if m.Regenerated.At != now.Format(time.RFC3339) || m.Regenerated.ByVPVersion != "repair-test" {
		t.Errorf("regenerated = %+v", m.Regenerated)
	}

	// The archive is untouched: bytes, mtime and name.
	if fileSum(t, f.zst) != zstBefore {
		t.Error("the archive's bytes changed")
	}
	if st, _ := os.Stat(f.zst); !st.ModTime().Equal(mtimeBefore) {
		t.Errorf("the archive's mtime moved: %v -> %v", mtimeBefore, st.ModTime())
	}

	// Verify passes on the repaired pair.
	if vr := VerifyWithOptions(&Entry{ManifestPath: f.manifest, ArchivePath: f.zst, Manifest: m}, VerifyOptions{}); !vr.OK {
		t.Errorf("vp archive verify fails on the repaired pair: %v", vr.Problems)
	}
	// ListEntries sees it.
	ents, err := ListEntries(f.root, f.project)
	if err != nil || len(ents) != 1 || ents[0].Manifest.SessionID != f.session {
		t.Errorf("ListEntries = %v, %v", ents, err)
	}

	// Idempotent: a second run is "nothing to do" and writes nothing.
	before := fileSum(t, f.manifest)
	res2, err := RepairManifest(opts)
	if err != nil || res2.NothingToDo == "" {
		t.Fatalf("second run = %+v, %v; want nothing to do", res2, err)
	}
	if fileSum(t, f.manifest) != before {
		t.Error("the second run rewrote the manifest")
	}
}

func writeTemp(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.jsonl")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The done-definition's last clause: BEFORE the repair the tidy sweep reports the
// lone archive as Orphaned; AFTER it, both halves are swept and the archive keeps
// its original name (and so its date).
func TestRepairManifestMakesTheNextTidySweepBothHalves(t *testing.T) {
	f := newRepairFixture(t, time.Hour)
	gitInitVault(t, f.root)
	zstRel := "Projects/p/transcripts/" + f.name + zstSuffix
	manRel := "Projects/p/transcripts/" + f.name + ".manifest.json"

	scan, err := storage.TidyScan(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(scan.Orphaned, zstRel) {
		t.Fatalf("before the repair the archive is not Orphaned: Orphaned=%v Deferred=%v", scan.Orphaned, scan.Deferred)
	}

	if _, err := RepairManifest(f.opts()); err != nil {
		t.Fatal(err)
	}
	scan, err = storage.TidyScan(f.root)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(scan.Swept, zstRel) || !slices.Contains(scan.Swept, manRel) {
		t.Errorf("after the repair tidy does not sweep both halves: Swept=%v", scan.Swept)
	}
	if slices.Contains(scan.Orphaned, zstRel) || slices.Contains(scan.Deferred, zstRel) {
		t.Errorf("the repaired archive is still Orphaned/Deferred: %v %v", scan.Orphaned, scan.Deferred)
	}
	if !strings.HasPrefix(f.name, f.zstDay()) {
		t.Errorf("the archive name %s does not carry its original day", f.name)
	}
}

func (f *repairFixture) zstDay() string { return f.name[:10] }

func gitInitVault(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "T"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = gitenv.SafeGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// R1: aimed at the ambient vault, repair is refused BEFORE anything is created
// (the vaultlock seat), and the write it uses (WriteManifest) is behind the funnel
// guard on its own. Control: a non-ambient vault gets its manifest and its lock dir.
func TestRepairManifestIsRefusedBeforeCreatingAnythingInTheAmbientVault(t *testing.T) {
	ctl := newRepairFixture(t, time.Hour)
	_ = os.RemoveAll(filepath.Join(ctl.root, ".vp-locks")) // the seeding Create made it; start both arms without one
	if _, err := RepairManifest(ctl.opts()); err != nil {
		t.Fatalf("control: %v", err)
	}
	if absent(t, filepath.Join(ctl.root, ".vp-locks")) || absent(t, ctl.manifest) {
		t.Fatal("control did not create .vp-locks and the manifest: the assertions below cannot discriminate")
	}

	amb := newRepairFixture(t, time.Hour)
	_ = os.RemoveAll(filepath.Join(amb.root, ".vp-locks"))
	t.Cleanup(surface.SetAmbientVaultRoot(amb.root))
	if msg := panicText(func() { _, _ = RepairManifest(amb.opts()) }); !strings.Contains(msg, "CONFIGURED vault") {
		t.Fatalf("repair at the ambient vault was not refused, got %q", msg)
	}
	if !absent(t, amb.manifest) || !absent(t, filepath.Join(amb.root, ".vp-locks")) {
		t.Error("something was created in the ambient vault before the refusal")
	}

	// The write funnel, directly: WriteManifest is the writer repair uses.
	if msg := panicText(func() { _ = WriteManifest(amb.root, amb.manifest, &Manifest{SchemaVersion: 1}) }); !strings.Contains(msg, "CONFIGURED vault") {
		t.Errorf("WriteManifest into the ambient vault was not refused, got %q", msg)
	}
	if !absent(t, amb.manifest) {
		t.Error("WriteManifest wrote into the ambient vault")
	}
}

// R3: a young archive is refused (a capture may still own it), at the boundary
// with an injected clock, using the tidy sweep's own constant.
func TestRepairManifestRefusesAYoungArchiveAtTheTidyGrace(t *testing.T) {
	f := newRepairFixture(t, time.Hour)
	opts := f.opts()
	opts.CapturedAt = f.mtime.Add(-time.Hour).UTC().Format(time.RFC3339)

	opts.Now = f.mtime.Add(storage.TranscriptPairGrace - time.Second)
	if _, err := RepairManifest(opts); !errors.Is(err, ErrArchiveTooYoung) {
		t.Fatalf("one second under the grace: err = %v, want ErrArchiveTooYoung", err)
	}
	if !absent(t, f.manifest) {
		t.Fatal("a manifest was written for a young archive")
	}
	opts.Now = f.mtime.Add(storage.TranscriptPairGrace)
	if _, err := RepairManifest(opts); err != nil {
		t.Fatalf("exactly at the grace (the tidy sweep's >=): %v", err)
	}
}

// R4: a manifest that appears between the pre-check and the lock (a capture that
// was only slow) is never overwritten.
func TestRepairManifestNeverOverwritesAManifestThatAppearsBeforeTheLock(t *testing.T) {
	f := newRepairFixture(t, time.Hour)
	const live = `{"schema_version":1,"adapter":"claude-code","session_id":"sess-repair-1","source_sha256":"x","captured_at":"2026-11-01T00:00:00Z"}` + "\n"
	repairPreLockHook = func() { _ = os.WriteFile(f.manifest, []byte(live), 0o644) }
	t.Cleanup(func() { repairPreLockHook = nil })

	res, err := RepairManifest(f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if res.NothingToDo == "" {
		t.Fatal("repair did not notice the manifest that appeared under the lock")
	}
	if b, _ := os.ReadFile(f.manifest); string(b) != live {
		t.Errorf("the live capture's manifest was overwritten: %q", b)
	}
}

// R5 + R15: the lock key is the one Create takes. (i) the manifest path repair
// derives equals the one Create builds; (ii) holding it on Create's path makes
// repair refuse without waiting; (iii) holding it inside repair makes Create's
// non-blocking posture refuse.
func TestRepairManifestUsesTheSameLockKeyAsCreate(t *testing.T) {
	f := newRepairFixture(t, time.Hour)

	_, mp := manifestPathFor(f.root, f.project, f.name)
	if mp != f.manifest {
		t.Fatalf("repair derives manifest path %s, Create built %s", mp, f.manifest)
	}

	rel, err := vaultlock.Acquire(f.root, f.manifest)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, e := RepairManifest(f.opts()); done <- e }()
	select {
	case e := <-done:
		if !errors.Is(e, ErrManifestLocked) {
			t.Errorf("with the lock held repair returned %v, want ErrManifestLocked", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repair WAITED on a held lock instead of refusing")
	}
	_ = rel()
	if !absent(t, f.manifest) {
		t.Error("a manifest was written while the lock was held elsewhere")
	}

	// (iii) repair holds the same key while it signs: Create's non-blocking posture refuses.
	var createErr error
	signManifest = func(string, SignOptions) error {
		_, createErr = Create(CreateOptions{
			Adapter: InlineAdapterName, SessionID: f.session, SourceContent: f.source,
			VaultRoot: f.root, ProjectSlug: f.project, Now: time.Now(), LockPosture: LockNonBlocking,
		})
		return nil
	}
	t.Cleanup(func() { signManifest = Sign })
	opts := f.opts()
	opts.Sign = SignOptions{Mode: SignModeSSH}
	if _, err := RepairManifest(opts); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(createErr, ErrManifestLocked) {
		t.Errorf("Create while repair held the lock returned %v, want ErrManifestLocked", createErr)
	}
}

// R6, R7, R8, R9: refusals leave nothing behind.
func TestRepairManifestRefusals(t *testing.T) {
	good := func(t *testing.T) (*repairFixture, RepairOptions) {
		f := newRepairFixture(t, time.Hour)
		return f, f.opts()
	}
	t.Run("adapter is required and never guessed", func(t *testing.T) {
		f, o := good(t)
		o.Adapter = ""
		if _, err := RepairManifest(o); !errors.Is(err, ErrRepairAdapterRequired) {
			t.Errorf("err = %v", err)
		}
		if !absent(t, f.manifest) {
			t.Error("a manifest was written")
		}
	})
	t.Run("an unregistered adapter is refused", func(t *testing.T) {
		f, o := good(t)
		o.Adapter = "no-such-adapter"
		if _, err := RepairManifest(o); err == nil {
			t.Error("accepted an unknown adapter")
		}
		if !absent(t, f.manifest) {
			t.Error("a manifest was written")
		}
	})
	t.Run("captured_at is required, parsed, and never in the future", func(t *testing.T) {
		f, o := good(t)
		o.CapturedAt = ""
		if _, err := RepairManifest(o); !errors.Is(err, ErrRepairCapturedAtRequired) {
			t.Errorf("missing: err = %v", err)
		}
		o.CapturedAt = "yesterday"
		if _, err := RepairManifest(o); err == nil || !strings.Contains(err.Error(), "RFC3339") {
			t.Errorf("unparseable: err = %v", err)
		}
		o.CapturedAt = time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
		if _, err := RepairManifest(o); err == nil || !strings.Contains(err.Error(), "future") {
			t.Errorf("future: err = %v", err)
		}
		if !absent(t, f.manifest) {
			t.Error("a manifest was written")
		}
	})
	t.Run("captured_at is the operator's, never the file's mtime", func(t *testing.T) {
		f, o := good(t)
		o.CapturedAt = "2026-01-02T03:04:05Z"
		res, err := RepairManifest(o)
		if err != nil {
			t.Fatal(err)
		}
		if res.Manifest.CapturedAt != "2026-01-02T03:04:05Z" {
			t.Errorf("captured_at = %q: the operator's value was not used verbatim", res.Manifest.CapturedAt)
		}
		if res.Manifest.CapturedAt == f.mtime.UTC().Format(time.RFC3339) {
			t.Error("captured_at equals the file mtime")
		}
		if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "filename day") {
			t.Errorf("a different calendar day than the filename must warn: %v", res.Warnings)
		}
	})
	t.Run("a name that is not YYYY-MM-DD-session is refused", func(t *testing.T) {
		f, o := good(t)
		bad := filepath.Join(filepath.Dir(f.zst), "not-a-dated-name"+zstSuffix)
		if err := os.Rename(f.zst, bad); err != nil {
			t.Fatal(err)
		}
		o.Archive = "not-a-dated-name"
		if _, err := RepairManifest(o); !errors.Is(err, ErrNotRepairable) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("an undecodable archive is refused and nothing is written", func(t *testing.T) {
		f, o := good(t)
		if err := os.WriteFile(f.zst, []byte("this is not zstd"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		_ = os.Chtimes(f.zst, old, old)
		if _, err := RepairManifest(o); err == nil || !strings.Contains(err.Error(), "decompress") {
			t.Errorf("err = %v", err)
		}
		if !absent(t, f.manifest) {
			t.Error("a manifest was written for an undecodable archive")
		}
	})
	t.Run("an archive that does not exist is refused", func(t *testing.T) {
		f, o := good(t)
		o.Archive = "2026-01-01-nope"
		if _, err := RepairManifest(o); !errors.Is(err, ErrNoSuchArchive) {
			t.Errorf("err = %v", err)
		}
		_ = f
	})
	t.Run("a path in a different project than --project is refused", func(t *testing.T) {
		f, o := good(t)
		o.Archive = f.zst
		o.Project = "other"
		if _, err := RepairManifest(o); !errors.Is(err, ErrNotRepairable) {
			t.Errorf("err = %v", err)
		}
	})
}

// R11 / R11b: signing. A configured signature is written; a signing failure rolls
// the manifest (and any partial signature) back so the orphan stays an orphan, and
// a retry succeeds.
func TestRepairManifestSigning(t *testing.T) {
	t.Cleanup(func() { signManifest = Sign })
	f := newRepairFixture(t, time.Hour)
	opts := f.opts()
	opts.Sign = SignOptions{Mode: SignModeSSH}
	zstBefore, mtimeBefore := fileSum(t, f.zst), f.mtime

	signManifest = func(manifestPath string, _ SignOptions) error {
		_ = os.WriteFile(SignaturePath(manifestPath), []byte("partial"), 0o644) // a partial signature
		return errors.New("injected signing failure")
	}
	_, err := RepairManifest(opts)
	if err == nil || !strings.Contains(err.Error(), "injected signing failure") {
		t.Fatalf("err = %v, want the signing error", err)
	}
	if !absent(t, f.manifest) || !absent(t, SignaturePath(f.manifest)) {
		t.Fatal("the failed repair left a manifest or a signature behind")
	}
	if fileSum(t, f.zst) != zstBefore {
		t.Error("the archive's bytes changed")
	}
	if st, _ := os.Stat(f.zst); !st.ModTime().Equal(mtimeBefore) {
		t.Error("the archive's mtime moved")
	}

	signManifest = func(manifestPath string, _ SignOptions) error {
		return os.WriteFile(SignaturePath(manifestPath), []byte("sig"), 0o644)
	}
	if _, err := RepairManifest(opts); err != nil {
		t.Fatalf("the retry after the rollback failed: %v", err)
	}
	if absent(t, f.manifest) || absent(t, SignaturePath(f.manifest)) {
		t.Error("the retry did not write the manifest and its signature")
	}
}

// R13: the listing.
func TestListOrphans(t *testing.T) {
	f := newRepairFixture(t, time.Hour)
	// a second, YOUNG orphan, a complete pair, and a badly named lone archive
	young := newRepairFixtureIn(t, f.root, time.Minute) // same vault, same session id: same name
	_ = young
	dir := filepath.Dir(f.zst)
	if err := os.WriteFile(filepath.Join(dir, "weird"+zstSuffix), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	pair := filepath.Join(dir, "2026-01-01-complete")
	for _, p := range []string{pair + zstSuffix, pair + ".manifest.json"} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	orphans, err := ListOrphans(f.root, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Orphan{}
	for _, o := range orphans {
		byName[o.Name] = o
	}
	if _, ok := byName["2026-01-01-complete"]; ok {
		t.Error("a complete pair was listed as an orphan")
	}
	if o, ok := byName["weird"]; !ok || o.Repairable || o.Reason == "" {
		t.Errorf("a badly named lone archive = %+v", o)
	}
	o, ok := byName[f.name]
	if !ok {
		t.Fatalf("the orphan %s was not listed: %v", f.name, byName)
	}
	// newRepairFixtureIn re-created it young (the second call overwrote the mtime)
	if o.Repairable || !strings.Contains(o.Reason, "younger") {
		t.Errorf("a young orphan = %+v, want not repairable (younger than the grace)", o)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(f.zst, old, old)
	orphans, _ = ListOrphans(f.root, f.project, time.Now())
	for _, o := range orphans {
		if o.Name == f.name && !o.Repairable {
			t.Errorf("an old orphan is not repairable: %+v", o)
		}
	}
}

// R16 (the documented old-hook risk): the CURRENT link path preserves the marker,
// so a drop can only come from an older binary, never from this one.
func TestLinkSessionNotePreservesRegenerated(t *testing.T) {
	f := newRepairFixture(t, time.Hour)
	if _, err := RepairManifest(f.opts()); err != nil {
		t.Fatal(err)
	}
	before, err := ReadManifest(f.manifest)
	if err != nil || before.Regenerated == nil {
		t.Fatalf("seed: %v %+v", err, before)
	}
	for name, link := range map[string]func(string) error{
		"LinkSessionNote":    func(note string) error { return LinkSessionNote(f.root, f.manifest, note) },
		"TryLinkSessionNote": func(note string) error { return TryLinkSessionNote(f.root, f.manifest, note) },
	} {
		note := "Projects/p/sessions/2026-11-01-" + name + ".md"
		if err := link(note); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		after, err := ReadManifest(f.manifest)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after.Regenerated, before.Regenerated) {
			t.Errorf("%s dropped or changed the regenerated marker:\n before %+v\n after  %+v", name, before.Regenerated, after.Regenerated)
		}
		if after.VaultRelSessionNote != note {
			t.Errorf("%s did not set the link: %q", name, after.VaultRelSessionNote)
		}
	}
}

// An ordinary manifest never carries the key, so only a repaired one is marked.
func TestOrdinaryManifestsCarryNoRegeneratedKey(t *testing.T) {
	root := t.TempDir()
	testutil.InitProject(t, root, "p")
	res, err := Create(CreateOptions{Adapter: InlineAdapterName, SessionID: "s-ordinary", SourceContent: []byte(sampleInlineJSONL),
		VaultRoot: root, ProjectSlug: "p", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(res.ManifestPath)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, has := raw["regenerated"]; has {
		t.Errorf("an ordinary manifest carries a regenerated key:\n%s", bytes.TrimSpace(b))
	}
}

// The manifest write goes through the atomicfile funnel: its write observer sees
// the manifest path, and sees nothing else (the zst is never written). A raw
// os.WriteFile of the same bytes would not be observed. (The sourceaudit funnel
// rule cannot see this destination: it arrives through a multi-value call, which
// the rule's syntactic resolver does not track, so this is the guard.)
func TestRepairManifestWritesThroughTheAtomicFunnel(t *testing.T) {
	f := newRepairFixture(t, time.Hour)
	var mu sync.Mutex
	var seen []string
	restore := atomicfile.SetWriteObserver(func(p string) {
		mu.Lock()
		seen = append(seen, p)
		mu.Unlock()
	})
	defer restore()
	if _, err := RepairManifest(f.opts()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || filepath.Clean(seen[0]) != filepath.Clean(f.manifest) {
		t.Errorf("atomicfile observed %v; want exactly the manifest %s (a write that bypasses the funnel, or one that touches the zst)", seen, f.manifest)
	}
}

// F3-R1: a .sig beside an orphan with no manifest is an anomaly: repair refuses
// before writing anything, the .sig stays byte-identical, and the listing says so.
func TestRepairManifestRefusesAnOrphanThatHasASignature(t *testing.T) {
	t.Cleanup(func() { signManifest = Sign })
	f := newRepairFixture(t, time.Hour)
	sig := SignaturePath(f.manifest)
	if err := os.WriteFile(sig, []byte("someone else's signature"), 0o644); err != nil {
		t.Fatal(err)
	}
	sigBefore := fileSum(t, sig)
	signed := false
	signManifest = func(string, SignOptions) error { signed = true; return nil }

	opts := f.opts()
	opts.Sign = SignOptions{Mode: SignModeSSH}
	_, err := RepairManifest(opts)
	if !errors.Is(err, ErrOrphanHasSignature) || !strings.Contains(err.Error(), ".sig") {
		t.Fatalf("err = %v, want ErrOrphanHasSignature naming the .sig", err)
	}
	if signed || !absent(t, f.manifest) {
		t.Error("the refused repair wrote a manifest or signed")
	}
	if fileSum(t, sig) != sigBefore {
		t.Error("the pre-existing .sig was modified")
	}

	orphans, lerr := ListOrphans(f.root, f.project, time.Now())
	if lerr != nil || len(orphans) != 1 || orphans[0].Repairable || !strings.Contains(orphans[0].Reason, ".sig") {
		t.Errorf("listing = %+v, %v; want one non-repairable orphan naming the .sig", orphans, lerr)
	}
}

// F3-R1: a rollback whose removal fails says so (no "removed" claim), names the
// failed removal, and wraps both errors.
func TestRepairManifestSigningRollbackReportsAFailedRemoval(t *testing.T) {
	t.Cleanup(func() { signManifest = Sign; removeForRollback = vaultfs.RemoveNoLock })
	f := newRepairFixture(t, time.Hour)
	opts := f.opts()
	opts.Sign = SignOptions{Mode: SignModeSSH}
	signManifest = func(string, SignOptions) error { return errors.New("injected signing failure") }
	cleanupErr := errors.New("injected cleanup failure")
	removeForRollback = func(p string) error {
		if p == f.manifest {
			return cleanupErr
		}
		return vaultfs.RemoveNoLock(p)
	}
	_, err := RepairManifest(opts)
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"injected signing failure", "rollback failed", "remove manifest", f.manifest} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "was removed") {
		t.Errorf("error claims a removal that failed: %v", err)
	}
	if !errors.Is(err, cleanupErr) {
		t.Error("the cleanup error is not wrapped")
	}
}
