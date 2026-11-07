// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 🔴 EVERY TEST HERE DRIVES THE REAL RESOLVERS, AND THAT IS WHY THE FIXTURE
// SETS HOME.
//
// project.DetectSignal and storage.ResolveVaultPath take no root: the marker
// walk is bounded at os.UserHomeDir() and the host config is read from
// XDG_CONFIG_HOME. testutil.RunHermetic (this package's TestMain) points
// XDG_CONFIG_HOME at the checked-in fixture and does NOT set HOME. So a test
// that built a fake home under t.TempDir() and forgot t.Setenv("HOME", ...)
// would walk to the developer's real home and compare against the live host —
// an inert guard that passes whatever the production code does. Every fixture
// below sets BOTH, and every vault path it names differs from the process
// default, so a mutation can change the outcome.

// orphanFixture is a whole fake host: a HOME, an XDG config, a CODEX_HOME, two
// vaults and a /proc.
type orphanFixture struct {
	t          *testing.T
	root       string
	home       string
	xdg        string
	codexHome  string
	vault      string // the vault under audit
	otherVault string
	procRoot   string
}

func newOrphanFixture(t *testing.T) *orphanFixture {
	t.Helper()
	root := t.TempDir()
	f := &orphanFixture{
		t:          t,
		root:       root,
		home:       filepath.Join(root, "home"),
		xdg:        filepath.Join(root, "xdg"),
		vault:      filepath.Join(root, "home", "vault"),
		otherVault: filepath.Join(root, "home", "other-vault"),
		procRoot:   filepath.Join(root, "proc"),
	}
	f.codexHome = filepath.Join(f.home, ".codex")
	for _, d := range []string{f.home, f.vault, f.otherVault, f.procRoot, f.codexHome} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Both vaults carry a manifest: storage refuses to resolve a [project_vaults]
	// target that holds no .vibe-palace/vault.toml, so without this the
	// another-vault row would get a REFUSAL (a different bucket) instead of the
	// other vault, and would pass for the wrong reason.
	for _, v := range []string{f.vault, f.otherVault} {
		if err := os.MkdirAll(filepath.Join(v, ".vibe-palace"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(v, ".vibe-palace", "vault.toml"), []byte("[vault]\nversion = 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", f.home)
	t.Setenv("XDG_CONFIG_HOME", f.xdg)
	f.writeHostConfig(nil)
	return f
}

// writeHostConfig writes the global config. vault_path names the AUDITED vault,
// which is under the fixture home and therefore never the process default —
// without that, a two-vault comparison degenerates to "" == "" and no mutation
// can change it.
func (f *orphanFixture) writeHostConfig(projectVaults map[string]string) {
	f.t.Helper()
	dir := filepath.Join(f.xdg, "vibe-palace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("git_enabled = false\n")
	fmt.Fprintf(&b, "vault_path = %q\n", f.vault)
	b.WriteString("\n[meta]\nversion_major = 1\nversion_minor = 1\n")
	if len(projectVaults) > 0 {
		b.WriteString("\n[project_vaults]\n")
		for k, v := range projectVaults {
			fmt.Fprintf(&b, "%q = %q\n", k, v)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(b.String()), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// checkout creates a marked checkout under the fixture home and returns it.
// An empty slug writes a marker with no [project].name — the step-2 case.
func (f *orphanFixture) checkout(name, slug string) string {
	f.t.Helper()
	dir := filepath.Join(f.home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	body := "[project]\n"
	if slug != "" {
		body += fmt.Sprintf("name = %q\n", slug)
	}
	if err := os.WriteFile(filepath.Join(dir, ".vibe-palace.toml"), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return dir
}

// initProject makes Projects/<slug>/ hold content, which is what
// projectdir.ClassifyProjectDir reads as Initialised.
func (f *orphanFixture) initProject(vaultRoot, slug string) {
	f.t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resume.md"), []byte("# resume\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// manifest writes a transcript manifest carrying sessionID, the right-hand side
// of the join.
func (f *orphanFixture) manifest(vaultRoot, slug, day, sessionID string) {
	f.t.Helper()
	dir := filepath.Join(vaultRoot, "Projects", slug, "transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	body := fmt.Sprintf(`{"schema_version":1,"adapter":"codex-raw","session_id":%q,"captured_at":"2026-11-06T00:00:00Z","project_slug":%q,"source_sha256":"x","source_bytes":1,"compressed_bytes":1,"turn_count":1}`+"\n", sessionID, slug)
	if err := os.WriteFile(filepath.Join(dir, day+"-"+sessionID+".manifest.json"), []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// rollout writes one rollout whose first record is a session_meta carrying the
// given payload fields, and returns its path.
func (f *orphanFixture) rollout(id string, payload map[string]any) string {
	f.t.Helper()
	dir := filepath.Join(f.codexHome, "sessions", "2026", "11", "06")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	rec := map[string]any{"timestamp": "2026-11-06T00:00:00Z", "ordinal": 0, "type": "session_meta", "payload": payload}
	raw, err := json.Marshal(rec)
	if err != nil {
		f.t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-06T00-00-00-"+id+".jsonl")
	body := append(raw, '\n')
	body = append(body, []byte(`{"type":"response_item","payload":{"type":"message","role":"user"}}`+"\n")...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		f.t.Fatal(err)
	}
	return path
}

// holdOpen builds a fake /proc entry whose fd symlink points at path, which is
// how a live Codex session looks to this check.
func (f *orphanFixture) holdOpen(path string) { f.holdOpenTarget(path) }

// holdOpenTarget is holdOpen with the fd's link target spelled out, so a test
// can reproduce exactly what the kernel writes — including the " (deleted)"
// marker it appends for a file that is unlinked but still open.
func (f *orphanFixture) holdOpenTarget(target string) {
	f.t.Helper()
	fdDir := filepath.Join(f.procRoot, "4242", "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(fdDir, "3")); err != nil {
		f.t.Fatal(err)
	}
}

func (f *orphanFixture) run() Result {
	f.t.Helper()
	rows := checkCodexOrphansAt(f.codexHome, f.vault, f.procRoot)
	if len(rows) != 1 {
		f.t.Fatalf("want exactly one row, got %d: %+v", len(rows), rows)
	}
	return rows[0]
}

// orphanKeys returns the session keys the row reported as orphans, read from
// the Details text the operator actually sees.
func orphanKeys(r Result) []string {
	var out []string
	for _, d := range r.Details {
		if strings.HasPrefix(d, "ORPHAN ") {
			out = append(out, strings.Fields(d)[1])
		}
	}
	return out
}

func rowMentions(r Result, want string) bool {
	for _, d := range r.Details {
		if strings.Contains(d, want) {
			return true
		}
	}
	return false
}

// plainOrphan is the whole happy path: a marked, named, this-vault,
// initialised, non-departed checkout whose rollout has no manifest.
func plainOrphan(t *testing.T) (*orphanFixture, string) {
	t.Helper()
	f := newOrphanFixture(t)
	cwd := f.checkout("work", "demo")
	f.initProject(f.vault, "demo")
	f.rollout("aaaa-1111", map[string]any{"id": "aaaa-1111", "session_id": "aaaa-1111", "cwd": cwd})
	return f, cwd
}

// T1: the positive case, asserted on the text the operator reads.
func TestCodexOrphansReportsAnUnarchivedRollout(t *testing.T) {
	f, _ := plainOrphan(t)
	r := f.run()
	if got := orphanKeys(r); len(got) != 1 || got[0] != "aaaa-1111" {
		t.Fatalf("orphan keys = %v, want [aaaa-1111]; row=%+v", got, r)
	}
	if r.Status != Fail {
		t.Fatalf("status = %v, want Fail; row=%+v", r.Status, r)
	}
}

// T2a / T2b / T2c: the join key. 🔴 Both shapes are REQUIRED. Today's rollouts
// carry payload.id; every Codex fixture in this repository
// (archive.codexOneRecord, archive.codexRolloutFixture) carries only
// payload.session_id, so an id-only key would pass vacuously against them.
func TestCodexOrphansJoinKeyPrefersIDAndFallsBackToSessionID(t *testing.T) {
	t.Run("payload.id wins over a differing session_id", func(t *testing.T) {
		f := newOrphanFixture(t)
		cwd := f.checkout("work", "demo")
		f.initProject(f.vault, "demo")
		// The SUBAGENT shape, measured live: id is this thread, session_id is
		// the parent. The parent IS archived. Keying on session_id would report
		// this rollout as captured.
		f.rollout("child-1", map[string]any{"id": "child-1", "session_id": "parent-1", "cwd": cwd})
		f.manifest(f.vault, "demo", "2026-11-06", "parent-1")
		r := f.run()
		if got := orphanKeys(r); len(got) != 1 || got[0] != "child-1" {
			t.Fatalf("orphan keys = %v, want [child-1] — the parent's manifest must not satisfy the child; row=%+v", got, r)
		}
	})
	t.Run("session_id is used when id is absent", func(t *testing.T) {
		f := newOrphanFixture(t)
		cwd := f.checkout("work", "demo")
		f.initProject(f.vault, "demo")
		f.rollout("legacy-1", map[string]any{"session_id": "legacy-1", "cwd": cwd})
		f.manifest(f.vault, "demo", "2026-11-06", "legacy-1")
		if got := orphanKeys(f.run()); len(got) != 0 {
			t.Fatalf("orphan keys = %v, want none — an archived id-less rollout must join on session_id", got)
		}
	})
	t.Run("the row records which field supplied the key", func(t *testing.T) {
		f := newOrphanFixture(t)
		cwd := f.checkout("work", "demo")
		f.initProject(f.vault, "demo")
		f.rollout("withid", map[string]any{"id": "withid", "session_id": "withid", "cwd": cwd})
		f.rollout("noid", map[string]any{"session_id": "noid", "cwd": cwd})
		r := f.run()
		if !rowMentions(r, "key from payload.id") || !rowMentions(r, "key from payload.session_id") {
			t.Fatalf("row does not record key provenance for both shapes: %+v", r.Details)
		}
	})
}

// T3: the id set is VAULT-WIDE. A manifest filed under another project in the
// same vault still satisfies the rollout.
//
// 🔴 The fixture needs TWO projects and the manifest under the OTHER one, or a
// per-project join passes unchanged and the mutation is invisible.
func TestCodexOrphansJoinIsVaultWideAcrossProjects(t *testing.T) {
	f := newOrphanFixture(t)
	cwd := f.checkout("work", "demo")
	f.initProject(f.vault, "demo")
	f.initProject(f.vault, "elsewhere")
	f.rollout("shared-1", map[string]any{"id": "shared-1", "cwd": cwd})
	f.manifest(f.vault, "elsewhere", "2026-11-06", "shared-1")
	if got := orphanKeys(f.run()); len(got) != 0 {
		t.Fatalf("orphan keys = %v, want none — a manifest under another project in this vault still archives the session", got)
	}
}

// T15 / T16: departed and uninitialised. Both need steps 1-3 to PASS first, so
// each fixture carries a valid named marker bound to the audited vault.
func TestCodexOrphansHonoursTheDepartedAndUninitialisedGates(t *testing.T) {
	t.Run("departed", func(t *testing.T) {
		f := newOrphanFixture(t)
		cwd := f.checkout("work", "gonezo")
		f.initProject(f.vault, "gonezo")
		// A REAL departure record, in the vault, at the path departure.List reads.
		dir := filepath.Join(f.vault, "Audits", "departures") // departure.Dir
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		rec := `{"slug":"gonezo","kind":"moved-to-vault","to":"git@example.com:other.git","date":"2026-10-12"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "gonezo.json"), []byte(rec), 0o644); err != nil {
			t.Fatal(err)
		}
		f.rollout("dep-1", map[string]any{"id": "dep-1", "cwd": cwd})
		r := f.run()
		if got := orphanKeys(r); len(got) != 0 {
			t.Fatalf("orphan keys = %v, want none — the hook declines a departed slug; row=%+v", got, r)
		}
		if !rowMentions(r, "departed") {
			t.Fatalf("the decline is not labelled: %+v", r.Details)
		}
	})
	t.Run("uninitialised", func(t *testing.T) {
		f := newOrphanFixture(t)
		cwd := f.checkout("work", "newproj") // no Projects/newproj in the vault
		f.rollout("uninit-1", map[string]any{"id": "uninit-1", "cwd": cwd})
		r := f.run()
		if got := orphanKeys(r); len(got) != 0 {
			t.Fatalf("orphan keys = %v, want none — the hook declines an uninitialised project; row=%+v", got, r)
		}
		if !rowMentions(r, "not initialised") {
			t.Fatalf("the decline is not labelled: %+v", r.Details)
		}
	})
}

// T4 / T5 / T17: the two marker gates. T5 is the row that forbids re-spelling
// DetectSignal as a stat of the cwd — findMarkerUpward climbs to the parent.
func TestCodexOrphansHonoursTheMarkerGates(t *testing.T) {
	t.Run("no marker anywhere", func(t *testing.T) {
		f := newOrphanFixture(t)
		bare := filepath.Join(f.home, "bare")
		if err := os.MkdirAll(bare, 0o755); err != nil {
			t.Fatal(err)
		}
		f.rollout("bare-1", map[string]any{"id": "bare-1", "cwd": bare})
		r := f.run()
		if got := orphanKeys(r); len(got) != 0 {
			t.Fatalf("orphan keys = %v, want none; row=%+v", got, r)
		}
		if !rowMentions(r, "no project marker") {
			t.Fatalf("the decline is not labelled: %+v", r.Details)
		}
	})
	t.Run("marker in a PARENT directory still counts", func(t *testing.T) {
		f := newOrphanFixture(t)
		parent := f.checkout("work", "demo")
		f.initProject(f.vault, "demo")
		sub := filepath.Join(parent, "nested", "deeper")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		f.rollout("nested-1", map[string]any{"id": "nested-1", "cwd": sub})
		if got := orphanKeys(f.run()); len(got) != 1 || got[0] != "nested-1" {
			t.Fatalf("orphan keys = %v, want [nested-1] — the marker walk climbs to the parent", got)
		}
	})
	t.Run("marker with no project name", func(t *testing.T) {
		f := newOrphanFixture(t)
		cwd := f.checkout("work", "") // marker present, [project].name absent
		f.rollout("unnamed-1", map[string]any{"id": "unnamed-1", "cwd": cwd})
		r := f.run()
		if got := orphanKeys(r); len(got) != 0 {
			t.Fatalf("orphan keys = %v, want none; row=%+v", got, r)
		}
		if !rowMentions(r, "marker unreadable, unnamed or invalid") {
			t.Fatalf("the decline is not labelled: %+v", r.Details)
		}
	})
}

// T6: a cwd bound to ANOTHER vault is out of scope, not an orphan.
//
// 🔴 Both vault paths are under the fixture home and differ from the process
// default, so the compare is between two real distinct paths.
func TestCodexOrphansSkipsARolloutBoundToAnotherVault(t *testing.T) {
	f := newOrphanFixture(t)
	f.writeHostConfig(map[string]string{"away": f.otherVault})
	cwd := f.checkout("work", "away")
	f.initProject(f.otherVault, "away")
	f.rollout("away-1", map[string]any{"id": "away-1", "cwd": cwd})
	r := f.run()
	if got := orphanKeys(r); len(got) != 0 {
		t.Fatalf("orphan keys = %v, want none — that session belongs to another vault; row=%+v", got, r)
	}
	if !rowMentions(r, "belongs to another vault") {
		t.Fatalf("the out-of-scope count is not reported: %+v", r.Details)
	}
}

// T14a / T14b: a REFUSED vault binding is its own verdict, never an orphan.
//
// 🔴 THE FIXTURE IS THE WHOLE POINT OF THESE TWO ROWS. "A marker that sets
// vault_path" is NOT enough: storage.ResolveVaultBinding consults
// markerIsTracked only when vault_path is non-empty, and project.IsTrackedMarker
// calls findGitTreeFailClosed FIRST and returns (false, nil) WITHOUT running
// git when no .git exists above the marker. A marker in a plain temp dir is
// therefore untracked, no refusal fires, and the mutation these rows exist to
// catch changes nothing. So each fixture builds a real git repository.
func TestCodexOrphansTreatsARefusedVaultBindingAsDeclined(t *testing.T) {
	// repo builds a checkout that is a real git repo whose marker sets
	// vault_path, and returns it. The marker is NOT added to the index here;
	// each subtest decides which refusal arm it wants.
	repo := func(f *orphanFixture) string {
		t.Helper()
		dir := filepath.Join(f.home, "tracked")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf("vault_path = %q\n\n[project]\nname = \"demo\"\n", f.otherVault)
		if err := os.WriteFile(filepath.Join(dir, ".vibe-palace.toml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		git := exec.Command("git", "init", "--quiet")
		git.Dir = dir
		if out, err := git.CombinedOutput(); err != nil {
			t.Skipf("git init unavailable: %v: %s", err, out)
		}
		return dir
	}

	t.Run("the marker is git-tracked", func(t *testing.T) {
		f := newOrphanFixture(t)
		dir := repo(f)
		f.initProject(f.vault, "demo")
		// 🔴 `git add` alone is sufficient and is the point: IsTrackedMarker
		// asks `git ls-files --error-unmatch`, which is the INDEX view, so no
		// commit and no committer identity are needed.
		add := exec.Command("git", "add", ".vibe-palace.toml")
		add.Dir = dir
		if out, err := add.CombinedOutput(); err != nil {
			t.Fatalf("git add: %v: %s", err, out)
		}
		f.rollout("tracked-1", map[string]any{"id": "tracked-1", "cwd": dir})
		r := f.run()
		if got := orphanKeys(r); len(got) != 0 {
			t.Fatalf("orphan keys = %v, want none — a refused binding is not a loss; row=%+v", got, r)
		}
		if !rowMentions(r, "vault binding refused") {
			t.Fatalf("the refusal is not labelled: %+v", r.Details)
		}
	})

	t.Run("git cannot be asked", func(t *testing.T) {
		f := newOrphanFixture(t)
		dir := repo(f)
		f.initProject(f.vault, "demo")
		f.rollout("nogit-1", map[string]any{"id": "nogit-1", "cwd": dir})
		// A .git exists, so IsTrackedMarker gets past findGitTreeFailClosed and
		// tries to RUN git; with no PATH it cannot, and fails closed. That is a
		// different return in ResolveVaultBinding from the tracked verdict, and
		// a fix that handles one can miss the other.
		t.Setenv("PATH", "")
		r := f.run()
		if got := orphanKeys(r); len(got) != 0 {
			t.Fatalf("orphan keys = %v, want none; row=%+v", got, r)
		}
		if !rowMentions(r, "vault binding refused") {
			t.Fatalf("the refusal is not labelled: %+v", r.Details)
		}
	})
}

// T7: a rollout a live process holds open is not an orphan.
func TestCodexOrphansSkipsASessionThatIsStillOpen(t *testing.T) {
	f, _ := plainOrphan(t)
	rollouts, err := listCodexRollouts(f.codexHome)
	if err != nil || len(rollouts) != 1 {
		t.Fatalf("listCodexRollouts = %v, %v", rollouts, err)
	}
	f.holdOpen(rollouts[0])
	r := f.run()
	if got := orphanKeys(r); len(got) != 0 {
		t.Fatalf("orphan keys = %v, want none — a held descriptor means the session is live; row=%+v", got, r)
	}
	if !rowMentions(r, "still open") {
		t.Fatalf("the live session is not reported: %+v", r.Details)
	}
}

// T8: with no /proc the row still LISTS what it found, says it could not
// qualify it, and is capped at Info.
//
// Skipping the row entirely would hide real losses, which is the silence this
// check exists to end; failing on data it cannot qualify would call a live
// session a loss.
func TestCodexOrphansWithoutProcReportsAndCapsAtInfo(t *testing.T) {
	f, _ := plainOrphan(t)
	rows := checkCodexOrphansAt(f.codexHome, f.vault, filepath.Join(f.root, "no-such-proc"))
	r := rows[0]
	if got := orphanKeys(r); len(got) != 1 {
		t.Fatalf("orphan keys = %v, want the orphan to still be listed; row=%+v", got, r)
	}
	if r.Status != Info {
		t.Fatalf("status = %v, want Info — a row that cannot qualify its data must not gate", r.Status)
	}
	if !rowMentions(r, "liveness incomplete") {
		t.Fatalf("the caveat is missing: %+v", r.Details)
	}
}

// T8b: a manifest that will not parse is UNKNOWN, not absent.
//
// archive.ListEntries skips an unreadable manifest, which silently drops its
// session id from the id set and would turn its rollout into a reported loss.
// The row must notice that it could not read everything and stop gating.
func TestCodexOrphansCapsAtInfoWhenAManifestCannotBeParsed(t *testing.T) {
	f, _ := plainOrphan(t)
	dir := filepath.Join(f.vault, "Projects", "demo", "transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-11-06-broken.manifest.json"), []byte("{garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := f.run()
	if r.Status != Info {
		t.Fatalf("status = %v, want Info — an id set that cannot state its own completeness must not gate", r.Status)
	}
	if !rowMentions(r, "yielded no session id") {
		t.Fatalf("the unknown count is not reported: %+v", r.Details)
	}
	if got := orphanKeys(r); len(got) != 1 {
		t.Fatalf("orphan keys = %v — the finding must still be listed", got)
	}
}

// T9: a vanished cwd whose PARENT still holds a marker is evaluated by the real
// predicate and merely ANNOTATED.
//
// 🔴 findMarkerUpward climbs past a missing leaf, so the hook WOULD have
// captured that session. Routing it into a "cannot determine" bucket would be a
// false negative — the direction this check must never take.
func TestCodexOrphansStillJudgesAVanishedCwdUnderAMarkedParent(t *testing.T) {
	f := newOrphanFixture(t)
	parent := f.checkout("work", "demo")
	f.initProject(f.vault, "demo")
	gone := filepath.Join(parent, "gone")
	f.rollout("gone-1", map[string]any{"id": "gone-1", "cwd": gone})
	r := f.run()
	if got := orphanKeys(r); len(got) != 1 || got[0] != "gone-1" {
		t.Fatalf("orphan keys = %v, want [gone-1] — the marker walk climbs past a missing leaf; row=%+v", got, r)
	}
	if !rowMentions(r, "no longer exists") {
		t.Fatalf("the vanished cwd is not annotated: %+v", r.Details)
	}
}

// T10: the header read is bounded. A first line past the scanner ceiling is
// reported unreadable rather than buffered.
//
// 🔴 THE JUNK LINE'S SIZE IS A LITERAL, NOT rolloutHeaderMaxBytes + 1, AND THAT
// IS THE WHOLE TEST. The first draft sized the fixture from the constant, so
// raising the ceiling to 64 MiB also grew the fixture to 64 MiB + 1 and the
// scanner still overflowed: the mutation harness found this row STILL GREEN
// under the very mutation it exists to catch — a fixture that moves with the
// thing it measures cannot measure it. A fixed 2 MiB is above the 1 MiB
// production ceiling and below any plausible raise, so raising the ceiling
// parses the file, the rollout becomes an ORPHAN verdict, and this goes red.
func TestCodexOrphansBoundsTheHeaderRead(t *testing.T) {
	f := newOrphanFixture(t)
	cwd := f.checkout("work", "demo")
	f.initProject(f.vault, "demo")
	dir := filepath.Join(f.codexHome, "sessions", "2026", "11", "06")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const junkBytes = 2 << 20 // fixed, deliberately NOT derived from rolloutHeaderMaxBytes
	junk := strings.Repeat("x", junkBytes)
	good := fmt.Sprintf(`{"type":"session_meta","payload":{"id":"big-1","cwd":%q}}`, cwd)
	body := junk + "\n" + good + "\n"
	path := filepath.Join(dir, "rollout-2026-11-06T00-00-00-big-1.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := f.run()
	if got := orphanKeys(r); len(got) != 0 {
		t.Fatalf("orphan keys = %v — an unreadable header is not an orphan verdict; row=%+v", got, r)
	}
	if !rowMentions(r, path) {
		t.Fatalf("the unreadable rollout is not named: %+v", r.Details)
	}
	// 🔴 P3: AN INCOMPLETE SCAN MUST NOT RETURN A CONFIDENT VERDICT. Naming the
	// file and not counting it as an orphan is not enough — before this
	// assertion a scan with one unreadable rollout and no candidates returned
	// Pass, exit 0, on a check whose whole purpose is to stop a silent loss.
	if r.Status != Info {
		t.Fatalf("status = %v, want Info — a rollout this scan could not read is a gap, and a gap is not a pass", r.Status)
	}
	if !rowMentions(r, "could not be classified") {
		t.Fatalf("the incomplete scan is not declared: %+v", r.Details)
	}
}

// T18: a subagent thread is not an orphan.
//
// 🔴 MEASURED 2026-11-06 in a throwaway CODEX_HOME, not assumed: a parent
// session spawned one subagent via the collaboration spawn_agent tool. Three
// rollouts were written and the recording hooks fired for exactly TWO session
// ids — both parents. The subagent's rollout (thread_source "subagent",
// parent_thread_id the parent, agent_path /root/probe_agent) got NO SessionStart
// and NO SessionEnd. Codex runs no hook for a subagent thread, so its rollout is
// never archived and its absence is not a loss.
//
// The same run reproduced the join-key divergence independently: the subagent's
// payload.id was its own thread and its payload.session_id was the parent's.
func TestCodexOrphansDoesNotReportASubagentThread(t *testing.T) {
	f := newOrphanFixture(t)
	cwd := f.checkout("work", "demo")
	f.initProject(f.vault, "demo")
	f.rollout("sub-1", map[string]any{
		"id": "sub-1", "session_id": "parent-9", "cwd": cwd,
		"thread_source": "subagent", "parent_thread_id": "parent-9",
	})
	r := f.run()
	if got := orphanKeys(r); len(got) != 0 {
		t.Fatalf("orphan keys = %v, want none — Codex runs no hook for a subagent thread; row=%+v", got, r)
	}
	if !rowMentions(r, "subagent") {
		t.Fatalf("the subagent is not reported on its own line: %+v", r.Details)
	}
}

// T12: the status is a verdict, not a constant.
func TestCodexOrphansStatusFailsOnAnOrphanAndPassesWithout(t *testing.T) {
	f, _ := plainOrphan(t)
	if r := f.run(); r.Status != Fail {
		t.Fatalf("status with an orphan = %v, want Fail", r.Status)
	}
	f.manifest(f.vault, "demo", "2026-11-06", "aaaa-1111")
	if r := f.run(); r.Status != Pass {
		t.Fatalf("status with the session archived = %v, want Pass", r.Status)
	}
}

// orphanFields pulls the three facts the row reports for the one orphan — the
// session key, the project slug and the rollout path — out of its prose line.
// They are what an operator (or this test) needs to build the recovery command.
func orphanFields(t *testing.T, r Result) (key, slug, rollout string) {
	t.Helper()
	for _, d := range r.Details {
		if !strings.HasPrefix(d, "ORPHAN ") {
			continue
		}
		key = strings.Fields(d)[1]
		if _, rest, ok := strings.Cut(d, "project "); ok {
			slug = strings.TrimSuffix(strings.Fields(rest)[0], ",")
		}
		if _, rest, ok := strings.Cut(d, "rollout "); ok {
			rollout = strings.Fields(rest)[0]
		}
		if key == "" || slug == "" || rollout == "" {
			t.Fatalf("the orphan line does not carry all three fields: %q", d)
		}
		return key, slug, rollout
	}
	t.Fatalf("no ORPHAN detail in %+v", r.Details)
	return "", "", ""
}

// T11a (revised): the row prints NO command-shaped line, names every orphan,
// and says where the fields can be read unwrapped.
//
// 🔴 THIS IS THE ROW'S CONTRACT AFTER THE RENDERER RULING. check.writeEntry
// word-wraps any entry line past the row width and inserts REAL newlines, so a
// `vp archive create … \` block with an absolute rollout path reaches the
// terminal with --source split from its argument and the backslash stranded —
// copied out, it does not run. The row therefore carries FACTS as prose, where
// wrapping is harmless, and never a pasteable block.
//
// The assertion is made against the RENDERED output, not against Details, so it
// fails for the reason an operator would experience.
func TestCodexOrphanRowPrintsNoCommandTheRendererWouldMangle(t *testing.T) {
	f, _ := plainOrphan(t)
	r := f.run()

	// (1) No DETAILS line is command-shaped. Details lines are what this check
	// controls; a multi-line `vp archive create … \\` block produces lines that
	// START with vp/--flag or END with a continuation backslash, and those are
	// exactly the lines writeEntry breaks. Prose that merely MENTIONS the
	// command inline is fine — wrapping prose costs nothing.
	for _, d := range r.Details {
		for _, line := range strings.Split(d, "\n") {
			tr := strings.TrimSpace(line)
			if tr == "" {
				continue
			}
			if strings.HasPrefix(tr, "vp archive create") || strings.HasPrefix(tr, "--") ||
				strings.HasPrefix(tr, "-p ") || tr == "\\" || strings.HasSuffix(tr, "\\") {
				t.Fatalf("the row emits a command-shaped line, which this renderer wraps and breaks:\n  %q", tr)
			}
		}
	}

	// (2) 🔴 ASSERTED AGAINST THE REAL RENDERED LINES, NOT A NORMALISED BLOB.
	// The first version of this test flattened the output before asserting, and
	// a reviewer changed only the note's prose prefix: the test still passed
	// while the rendered result split an apparent command across lines. The
	// paste hazard is per-LINE, so the assertion has to be per-line too.
	//
	// The hazards, precisely: a line that ends in a shell continuation, a line
	// that IS one, a line beginning with a value-taking flag torn from its flag,
	// and a line beginning the archive invocation itself. A wrapped prose line
	// that happens to start with "--check" or "--json" is not a hazard — those
	// take no separated value — so the rule names the dangerous tokens rather
	// than banning every dash.
	var buf bytes.Buffer
	PrintRows(&buf, []Result{r})
	rendered := buf.String()
	for _, line := range strings.Split(rendered, "\n") {
		tr := strings.TrimSpace(line)
		if tr == "" {
			continue
		}
		if tr == "\\" || strings.HasSuffix(tr, "\\") {
			t.Fatalf("a rendered line ends in a shell continuation, so the row is handing over a command this renderer has already broken:\n  %q\nfull row:\n%s", tr, rendered)
		}
		for _, flag := range []string{"--source", "--session-id", "--adapter", "-p ", "vp archive create"} {
			if strings.HasPrefix(tr, flag) {
				t.Fatalf("a rendered line begins with %q, so a command has been split across lines:\n  %q\nfull row:\n%s", flag, tr, rendered)
			}
		}
	}
	// The orphan's id survives rendering on one line — ids are short enough that
	// the renderer never splits them.
	foundID := false
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "ORPHAN aaaa-1111") {
			foundID = true
		}
	}
	if !foundID {
		t.Fatalf("no rendered line names the orphan's session id:\n%s", rendered)
	}
	if !strings.Contains(strings.Join(strings.Fields(rendered), " "), "vp check --check codex-orphans --json") {
		t.Fatalf("the row does not say where the fields can be read unwrapped:\n%s", rendered)
	}

	// And the facts an operator needs are all there, machine-readably.
	key, slug, rollout := orphanFields(t, r)
	if key != "aaaa-1111" || slug != "demo" || !strings.HasSuffix(rollout, ".jsonl") {
		t.Fatalf("orphan line fields = %q %q %q", key, slug, rollout)
	}
}

// T13a: the selector entry exists on both sides of the registry.
func TestCodexOrphansSelectorWiring(t *testing.T) {
	if _, ok := Producers["codex-orphans"]; !ok {
		t.Fatal("codex-orphans selector missing from Producers")
	}
	for _, name := range ProducerOrder {
		if name == "codex-orphans" {
			return
		}
	}
	t.Fatal("codex-orphans omitted from ProducerOrder")
}

// T13b: the REGISTERED CLOSURE does the work.
//
// 🔴 T13a and TestProducerOrderCoversProducers both pass against a closure that
// returns []Result{} — they check the NAME, never the scan. This row calls the
// closure the registry actually dispatches and compares it with the direct
// call against the same roots, so a no-op registration goes red here and only
// here.
func TestCodexOrphansProducerClosureDoesTheScan(t *testing.T) {
	f, _ := plainOrphan(t)
	t.Setenv("CODEX_HOME", f.codexHome)

	direct := checkCodexOrphansAt(f.codexHome, f.vault, "/proc")
	viaRegistry := Producers["codex-orphans"](f.vault)

	if len(viaRegistry) != len(direct) {
		t.Fatalf("registry returned %d row(s), direct call %d", len(viaRegistry), len(direct))
	}
	for i := range direct {
		if viaRegistry[i].Name != direct[i].Name || viaRegistry[i].Status != direct[i].Status ||
			viaRegistry[i].Summary != direct[i].Summary ||
			strings.Join(viaRegistry[i].Details, "\x00") != strings.Join(direct[i].Details, "\x00") {
			t.Fatalf("the registered closure did not produce the direct call's finding:\n registry=%+v\n direct=%+v",
				viaRegistry[i], direct[i])
		}
	}
	if len(orphanKeys(viaRegistry[0])) != 1 {
		t.Fatalf("the registered closure reported no orphan, so it is not running the scan: %+v", viaRegistry[0])
	}
}

// realHomeForBuild is the process HOME as it was before any fixture replaced
// it. Captured at package initialisation, which runs before any test body.
var realHomeForBuild = os.Getenv("HOME")

// buildVP builds the real CLI once per test and returns its path.
//
// A real binary is unavoidable here: the behaviour under test is `vp archive
// create`'s VAULT SELECTION, which lives in package main (openProjectVault ->
// os.Getwd) and cannot be reached from this package. Measured, the build costs
// ~0.3 s warm against this repository's build cache, so it is not short-skipped
// — a row that only runs outside `make test` proves nothing about `make test`.
func buildVP(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "vp")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/vp")
	cmd.Dir = repoRoot(t)
	// 🔴 HOME MUST BE THE REAL ONE FOR THE BUILD. The fixture points HOME at a
	// t.TempDir(), and `go build` locates its cache at $HOME/.cache/go-build
	// when GOCACHE is unset -- so building under the fixture home rebuilds the
	// whole module every run. Measured: 24 s and 2m33s of CPU, against 0.3 s
	// with the real cache.
	cmd.Env = append(os.Environ(), "HOME="+realHomeForBuild)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build cmd/vp here: %v: %s", err, out)
	}
	return bin
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test working directory")
		}
		dir = parent
	}
}

func manifestCount(t *testing.T, vaultRoot, slug string) int {
	t.Helper()
	n, err := countManifests(vaultRoot, slug)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// T11b: the emitted command recovers the rollout into the vault the WORKING
// DIRECTORY resolves to — and into no other.
//
// 🔴 BOTH ARMS, AND THE POSITIVE ONE FIRST. An earlier draft asserted only that
// the command does not write into the audited vault when run from elsewhere,
// which a command that dies at exit 127 satisfies trivially — a vacuous
// assertion dressed as a guard. Arm 1 is what proves the command works at all;
// arm 2 is what proves the cwd requirement in its comment is real.
//
// 🔴 The two checkouts must resolve to DIFFERENT vaults, or Getwd and the
// recorded cwd coincide and the location bug is invisible.
func TestCodexOrphanRemedyRecoversIntoTheCwdsVaultAndNoOther(t *testing.T) {
	f := newOrphanFixture(t)
	f.writeHostConfig(map[string]string{"away": f.otherVault})
	here := f.checkout("work", "demo")          // resolves to f.vault (global vault_path)
	elsewhere := f.checkout("awaywork", "away") // resolves to f.otherVault (tier-2 binding)
	f.initProject(f.vault, "demo")
	f.initProject(f.otherVault, "away")
	f.rollout("recover-1", map[string]any{"id": "recover-1", "session_id": "recover-1", "cwd": here})

	// 🔴 THE COMMAND IS BUILT FROM THE FIELDS THE ROW REPORTS, which is the
	// point now that the row no longer prints a block: if those three facts are
	// not enough to recover the session, the row is not actionable.
	key, slug, rollout := orphanFields(t, f.run())
	remedy := "vp archive create --adapter codex-raw --session-id " + key +
		" --source " + rollout + " -p " + slug
	vp := buildVP(t)
	// The emitted text says `vp`; put the built binary first on PATH so the
	// text runs EXACTLY as printed rather than being rewritten by the test.
	binDir := filepath.Dir(vp)
	env := append(os.Environ(),
		"HOME="+f.home,
		"XDG_CONFIG_HOME="+f.xdg,
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	before := manifestCount(t, f.vault, "demo")

	// --- ARM 2 FIRST, so arm 1 cannot be credited with its write. ---
	wrong := exec.Command("/bin/sh", "-c", remedy)
	wrong.Dir = elsewhere
	wrong.Env = env
	wrongOut, wrongErr := wrong.CombinedOutput()
	if n := manifestCount(t, f.vault, "demo"); n != before {
		t.Fatalf("running the remedy from %s wrote into the audited vault (%d -> %d manifests); "+
			"vp archive create takes its vault from os.Getwd(), so the cwd requirement in the emitted comment is load-bearing\noutput: %s",
			elsewhere, before, n, wrongOut)
	}
	t.Logf("arm 2 (wrong cwd): err=%v output=%s", wrongErr, strings.TrimSpace(string(wrongOut)))

	// --- ARM 1: the positive outcome. ---
	right := exec.Command("/bin/sh", "-c", remedy)
	right.Dir = here
	right.Env = env
	if out, err := right.CombinedOutput(); err != nil {
		t.Fatalf("the emitted remedy FAILED from the correct directory: %v\noutput: %s\ncommand:\n%s", err, out, remedy)
	}
	if n := manifestCount(t, f.vault, "demo"); n != before+1 {
		t.Fatalf("the remedy did not archive into the audited vault (%d -> %d manifests)\ncommand:\n%s", before, n, remedy)
	}
	if n := manifestCount(t, f.otherVault, "demo"); n != 0 {
		t.Fatalf("the remedy also wrote %d manifest(s) into the other vault", n)
	}
	// And the check now agrees: the session is archived.
	if got := orphanKeys(f.run()); len(got) != 0 {
		t.Fatalf("after recovery the row still reports %v; the recovery and the join disagree", got)
	}
}

// P1: a rollout whose session_meta carries no cwd is UNKNOWN, never an orphan.
//
// 🔴 THE FIXTURE'S POINT IS THAT THE CHECKER IS STANDING SOMEWHERE REAL.
// project.DetectSignal hands its argument to filepath.Abs, which resolves "" to
// the PROCESS working directory — so before this arm the row attributed a
// cwd-less rollout to whatever project the checker happened to be in and
// reported it as that project's loss. The test therefore chdirs into a marked,
// initialised checkout first: if the empty-cwd arm is removed, the resolvers
// succeed against THAT checkout and the rollout comes back as a confident
// ORPHAN, which is the defect.
func TestCodexOrphansTreatsARolloutWithNoCWDAsUnknown(t *testing.T) {
	f := newOrphanFixture(t)
	cwd := f.checkout("work", "demo")
	f.initProject(f.vault, "demo")
	t.Chdir(cwd)
	f.rollout("nocwd-1", map[string]any{"id": "nocwd-1", "session_id": "nocwd-1"}) // no cwd key

	r := f.run()
	if got := orphanKeys(r); len(got) != 0 {
		t.Fatalf("orphan keys = %v, want none — a rollout with no cwd cannot be attributed to a project; row=%+v", got, r)
	}
	if !rowMentions(r, "carries no cwd") {
		t.Fatalf("the missing cwd is not reported as its own unknown: %+v", r.Details)
	}
	if r.Status != Info {
		t.Fatalf("status = %v, want Info — a scan that could not classify a rollout must not report a confident verdict", r.Status)
	}
}

// P2a: liveness that could not be fully read caps the row at Info.
//
// Only an error on the /proc ROOT used to reach the cap; a per-process fd
// directory this user may not read was silently treated as "that process
// exited", so a LIVE session could be reported as a loss with no caveat at all.
func TestCodexOrphansCapsAtInfoWhenAProcessCannotBeInspected(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read an unreadable directory, so this fixture cannot deny access")
	}
	f, _ := plainOrphan(t)
	fdDir := filepath.Join(f.procRoot, "4242", "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fdDir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fdDir, 0o755) })

	r := f.run()
	if r.Status != Info {
		t.Fatalf("status = %v, want Info — liveness was incomplete, so a live session may be in the list", r.Status)
	}
	if !rowMentions(r, "liveness incomplete") {
		t.Fatalf("the incomplete-liveness caveat is missing: %+v", r.Details)
	}
	if got := orphanKeys(r); len(got) != 1 {
		t.Fatalf("orphan keys = %v — the finding must still be listed, or the row hides losses", got)
	}
}

// P2b: a symlinked CODEX_HOME still matches the fd /proc reports.
//
// 🔴 THE FIXTURE MUST ENUMERATE THROUGH THE ALIAS AND HOLD THE REAL PATH, which
// is what the kernel does: /proc reports the resolved target. A literal prefix
// test then fails to match at all and EVERY live session becomes eligible to be
// reported as a loss.
func TestCodexOrphansMatchesAHeldRolloutThroughASymlinkedCodexHome(t *testing.T) {
	f, _ := plainOrphan(t)
	real, err := listCodexRollouts(f.codexHome)
	if err != nil || len(real) != 1 {
		t.Fatalf("listCodexRollouts = %v, %v", real, err)
	}
	alias := filepath.Join(f.root, "codex-alias")
	if err := os.Symlink(f.codexHome, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// /proc always reports the RESOLVED path; the scan enumerates via the alias.
	f.holdOpen(real[0])

	rows := checkCodexOrphansAt(alias, f.vault, f.procRoot)
	if len(rows) != 1 {
		t.Fatalf("want one row, got %d", len(rows))
	}
	if got := orphanKeys(rows[0]); len(got) != 0 {
		t.Fatalf("orphan keys = %v, want none — the held rollout is the same file reached through a symlink; row=%+v", got, rows[0])
	}
	if !rowMentions(rows[0], "still open") {
		t.Fatalf("the live session was not recognised through the alias: %+v", rows[0].Details)
	}
}

// P4: a manifest that PARSES but carries no session_id is unknown, not absent.
//
// 🔴 DISTINCT FROM T8b, which plants syntactically invalid JSON. This one is a
// valid manifest that ReadManifest accepts — ReadManifest validates
// schema_version and nothing else — so it counts as a parsed Entry, contributes
// no id, and raised no unknown line at all. Its rollout then came back as a
// confident orphan.
func TestCodexOrphansCapsAtInfoWhenAManifestHasNoSessionID(t *testing.T) {
	f, _ := plainOrphan(t)
	dir := filepath.Join(f.vault, "Projects", "demo", "transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"schema_version":1,"adapter":"codex-raw","session_id":"","captured_at":"2026-11-06T00:00:00Z","project_slug":"demo"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "2026-11-06-idless.manifest.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := f.run()
	if r.Status != Info {
		t.Fatalf("status = %v, want Info — an id set that dropped a manifest's id must not gate", r.Status)
	}
	if !rowMentions(r, "yielded no session id") {
		t.Fatalf("the id-less manifest is not reported as unknown: %+v", r.Details)
	}
}

// D1: the archived id set includes DEPARTED slugs' transcripts.
//
// 🔴 THE EARLIER DEPARTED FIXTURE COULD NOT SEE THIS. It planted a departure
// record and pointed the rollout's cwd at that same slug, so the rollout was
// declined at predicate step 4 and never reached the join — deleting the
// departure.List union left the suite green.
//
// Here the rollout's checkout names a LIVE slug, so it passes every predicate
// step and REACHES the join, while its manifest sits under a DEPARTED slug.
// That is the real shape: a project archived under one slug, then moved, with
// its transcripts still on disk under the old name (measured live as
// qa-metabuild-system). storage.Vault.ListAllProjects DELETES departed slugs,
// so without the union the id is invisible and this rollout is a false orphan.
func TestCodexOrphansIdSetIncludesDepartedSlugsTranscripts(t *testing.T) {
	f := newOrphanFixture(t)
	cwd := f.checkout("work", "live-one")
	f.initProject(f.vault, "live-one")

	// The manifest lives under the DEPARTED slug, not under the live one.
	f.manifest(f.vault, "dep-one", "2026-11-06", "moved-1")
	depDir := filepath.Join(f.vault, "Audits", "departures") // departure.Dir
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rec := `{"slug":"dep-one","kind":"moved-to-vault","to":"git@example.com:other.git","date":"2026-10-12"}` + "\n"
	if err := os.WriteFile(filepath.Join(depDir, "dep-one.json"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}

	f.rollout("moved-1", map[string]any{"id": "moved-1", "cwd": cwd})

	r := f.run()
	if got := orphanKeys(r); len(got) != 0 {
		t.Fatalf("orphan keys = %v, want none — the session IS archived, under a departed slug whose "+
			"transcripts are still on disk; row=%+v", got, r)
	}
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass; row=%+v", r.Status, r)
	}
}

// D2: the subagent classifier and the join key, pinned separately.
//
// 🔴 THE EARLIER FIXTURE SET BOTH SIGNALS AND NEVER ARCHIVED THE PARENT, so
// three mutations stayed green: narrowing the classifier to thread_source
// alone, narrowing it to parent_thread_id alone, and — the one that matters —
// keying a subagent on payload.session_id instead of payload.id, which is the
// exact false negative the join-key rule exists to prevent.
func TestCodexOrphansSubagentClassifierAndKey(t *testing.T) {
	t.Run("thread_source alone marks a subagent", func(t *testing.T) {
		f := newOrphanFixture(t)
		cwd := f.checkout("work", "demo")
		f.initProject(f.vault, "demo")
		f.rollout("ts-only", map[string]any{"id": "ts-only", "cwd": cwd, "thread_source": "subagent"})
		r := f.run()
		if got := orphanKeys(r); len(got) != 0 {
			t.Fatalf("orphan keys = %v, want none; row=%+v", got, r)
		}
		if !rowMentions(r, "subagent") {
			t.Fatalf("not reported on the subagent line: %+v", r.Details)
		}
	})

	t.Run("parent_thread_id alone marks a subagent", func(t *testing.T) {
		f := newOrphanFixture(t)
		cwd := f.checkout("work", "demo")
		f.initProject(f.vault, "demo")
		f.rollout("pt-only", map[string]any{"id": "pt-only", "cwd": cwd, "parent_thread_id": "parent-7"})
		r := f.run()
		if got := orphanKeys(r); len(got) != 0 {
			t.Fatalf("orphan keys = %v, want none; row=%+v", got, r)
		}
		if !rowMentions(r, "subagent") {
			t.Fatalf("not reported on the subagent line: %+v", r.Details)
		}
	})

	// 🔴 THE KEY IS ASSERTED AT THE READER, not through the row, because the
	// subagent filter returns BEFORE the join (correct under the ruling) and so
	// a wrong key is invisible in the row's output. The parent IS archived here,
	// which is what makes a session_id key look right while being wrong.
	t.Run("a subagent keys on its own id, not its parent's session_id", func(t *testing.T) {
		f := newOrphanFixture(t)
		cwd := f.checkout("work", "demo")
		f.initProject(f.vault, "demo")
		f.manifest(f.vault, "demo", "2026-11-06", "parent-9") // the PARENT is archived
		path := f.rollout("child-9", map[string]any{
			"id": "child-9", "session_id": "parent-9", "cwd": cwd,
			"thread_source": "subagent", "parent_thread_id": "parent-9",
		})

		h, err := readRolloutHeader(path)
		if err != nil {
			t.Fatal(err)
		}
		if h.Key != "child-9" || h.KeyField != "id" {
			t.Fatalf("header key = %q from payload.%s, want \"child-9\" from payload.id — "+
				"keying a subagent on session_id joins it onto its archived parent, which is the "+
				"false negative the join-key rule exists to prevent", h.Key, h.KeyField)
		}
		if !h.Subagent {
			t.Fatalf("a record with thread_source=subagent and parent_thread_id was not classified as one: %+v", h)
		}
	})
}

// 🔴 INCOMPLETE LIVENESS MUST NOT PIN THIS ROW AT Info FOREVER.
//
// Capping unconditionally was measured to do exactly that on an ordinary host:
// /proc/<pid>/fd is unreadable for every other user's process (220 of them
// here) and for this user's own hardened ones (sd-pam, ssh-agent, teleport), so
// the live row came back Info with zero orphans and COULD NO LONGER FAIL — a
// check that cannot fail, which is the defect this project refuses everywhere
// else. The cap is therefore asymmetric, and provably so: an fd this scan could
// not read can only make it MISS that a rollout is held, never make it believe
// a dead session is live, so the only error it can produce is a FALSE ORPHAN —
// and a scan that names no orphans has made no claim to be wrong about.
//
// This row pins both halves of that asymmetry in one place.
func TestCodexOrphansIncompleteLivenessCapsAClaimButNotACleanScan(t *testing.T) {
	t.Run("with an orphan it caps at Info", func(t *testing.T) {
		f, _ := plainOrphan(t)
		r := checkCodexOrphansAt(f.codexHome, f.vault, filepath.Join(f.root, "no-such-proc"))[0]
		if r.Status != Info {
			t.Fatalf("status = %v, want Info — a named orphan under unreadable liveness may be a live session", r.Status)
		}
	})

	t.Run("with no orphan it still passes, and still says so", func(t *testing.T) {
		f, _ := plainOrphan(t)
		f.manifest(f.vault, "demo", "2026-11-06", "aaaa-1111") // nothing is lost
		r := checkCodexOrphansAt(f.codexHome, f.vault, filepath.Join(f.root, "no-such-proc"))[0]
		if r.Status != Pass {
			t.Fatalf("status = %v, want Pass — incomplete liveness can only produce a false ORPHAN, and there are none; "+
				"capping here makes a row that can never fail", r.Status)
		}
		if !rowMentions(r, "liveness incomplete") {
			t.Fatalf("the caveat must be printed even when the verdict is clean: %+v", r.Details)
		}
	})
}

// A rollout held open after being unlinked is still LIVE, not a loss.
//
// 🔴 THE FIXTURE SPELLS THE KERNEL'S OWN RENDERING. Linux writes
// /proc/<pid>/fd/N as "<path> (deleted)" when the file is unlinked but still
// held. filepath.EvalSymlinks fails on that string, so before the strip the
// stored form kept the marker, the lookup form never had it, the two could
// never be equal, and the row returned Fail naming a session that was still
// running. Same direction as the incomplete-liveness defect: liveness misses a
// live session and the row calls it a loss.
//
// The suffix is appended to the SYMLINK TARGET, not to a real file — that is
// what makes EvalSymlinks fail, and a fixture that pointed at a path ending in
// a literal " (deleted)" file on disk would resolve cleanly and prove nothing.
func TestCodexOrphansSkipsAHeldRolloutMarkedDeleted(t *testing.T) {
	f, _ := plainOrphan(t)
	rollouts, err := listCodexRollouts(f.codexHome)
	if err != nil || len(rollouts) != 1 {
		t.Fatalf("listCodexRollouts = %v, %v", rollouts, err)
	}
	f.holdOpenTarget(rollouts[0] + deletedSuffix)

	r := f.run()
	if got := orphanKeys(r); len(got) != 0 {
		t.Fatalf("orphan keys = %v, want none — the rollout is unlinked but still held, so its session is LIVE; row=%+v", got, r)
	}
	if !rowMentions(r, "still open") {
		t.Fatalf("the live session was not recognised through the kernel's deleted marker: %+v", r.Details)
	}
	if r.Status != Pass {
		t.Fatalf("status = %v, want Pass", r.Status)
	}
}
