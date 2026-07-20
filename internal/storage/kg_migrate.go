// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// TripleFilenameMigration reports the outcome (or, in plan-only mode, the
// PREVIEW) of the KG triple-filename migration.
type TripleFilenameMigration struct {
	Projects  int // project triple dirs scanned
	Scanned   int // triple files examined
	Renamed   int // files moved to a fresh flat name (would-rename in plan mode)
	Collapsed int // redundant SAME-body duplicates removed (would-collapse in plan mode)
	AlreadyOK int // files already at their target flat name (idempotent re-run)

	// Collisions holds every detected filename collision: two DISTINCT triples
	// (differing subject/predicate/object) that encode to the same flat name, or
	// a pre-existing/foreign file sitting at a source's flat target with a
	// DIFFERENT body. A non-empty Collisions is a HARD STOP — an apply refuses and
	// mutates nothing, because collapsing a collision would silently delete a
	// distinct triple.
	Collisions []TripleCollision
	// BadFiles holds every triple .json that is unparseable or not a Triple
	// (blank subject/predicate/object). A non-empty BadFiles is a HARD STOP too:
	// the pre-scan refuses so one poison file cannot leave a half-migrated tree.
	BadFiles []BadTripleFile
}

// TripleCollision names both sides of a filename collision so the operator can
// see exactly which two triples fight for one flat name (or which foreign file
// blocks a source's target). Nothing is ever deleted when a collision is found.
type TripleCollision struct {
	NewPath    string // the shared flat target both sides encode/point to
	SourcePath string // the source triple file being migrated
	SourceS    string
	SourceP    string
	SourceO    string
	OtherPath  string // the other file: a second colliding source, or the target on disk
	OtherS     string
	OtherP     string
	OtherO     string
}

// BadTripleFile names a triple .json that failed the pre-scan and why.
type BadTripleFile struct {
	Path   string
	Reason string
}

// HasBlockers reports whether the plan detected anything that must abort an
// apply: a bad file or a real collision. An apply refuses when this is true.
func (m *TripleFilenameMigration) HasBlockers() bool {
	return len(m.BadFiles) > 0 || len(m.Collisions) > 0
}

// scannedTriple is one parsed triple file and its computed flat target.
type scannedTriple struct {
	oldPath string
	newPath string
	t       Triple
}

// projectPlan is the resolved set of mutations for one project's triples dir.
type projectPlan struct {
	project    string
	triplesDir string
	scanned    int
	alreadyOK  int
	// renames: newPath -> the single source to os.Rename into it (target does
	// not yet exist on disk).
	renames map[string]string
	// collapses: newPath -> source paths to os.Remove (a SAME-body duplicate,
	// either of a sibling source that won the rename or of a target already
	// present on disk with a matching body). Never a differing body.
	collapses map[string][]string
}

// PlanTripleFilenameMigration walks every project's triples dir and returns a
// PLAN-ONLY preview: what WOULD be renamed and collapsed, plus every detected
// collision and bad file. It mutates nothing — no rename, remove, prune, stamp,
// or lock — and is safe to run against a live, in-use vault.
//
// It sets the migrator-exempt seam so it can READ format-0 (old-encoding) data
// past the armed data-format gate.
func (v *Vault) PlanTripleFilenameMigration() (TripleFilenameMigration, error) {
	v.SetMigratorExempt(true)
	m, _, err := v.planTripleFilenames()
	return m, err
}

// ApplyTripleFilenameMigration acquires EXCLUSIVE access to the vault (refusing
// rather than blocking or racing if a concurrent holder has it), pre-scans every
// triple file, REFUSES if any file is bad or any collision is detected (mutating
// nothing), then performs the renames/collapses and prunes the emptied nested
// dirs, and finally runs the post-migration verification pass. It does NOT
// advance the data-format stamp — the caller does that only after this returns
// with no error, so a half-migrated vault still reads as format 0.
func (v *Vault) ApplyTripleFilenameMigration() (TripleFilenameMigration, error) {
	v.SetMigratorExempt(true)

	// Demand exclusive access so a concurrent AddTriple / MCP server / agent
	// cannot interleave with the raw rename/remove/prune. vaultlock is a blocking
	// LOCK_EX, so we TRY (non-blocking) and REFUSE if we cannot get it — the live
	// RUN requires exclusive vault access (ADR-003 semantics). One acquire, one
	// release: no double-acquire, no deadlock.
	release, ok, err := vaultlock.TryAcquire(v.Root, v.kgMigrationLockPath())
	if err != nil {
		return TripleFilenameMigration{}, fmt.Errorf("acquire migration lock: %w", err)
	}
	if !ok {
		return TripleFilenameMigration{}, fmt.Errorf(
			"another process holds the KG migration lock; the migration needs exclusive vault access — stop any vp mcp server / concurrent vp run and retry")
	}
	defer release()

	m, plans, err := v.planTripleFilenames()
	if err != nil {
		return m, err
	}

	// PRE-SCAN FAIL-SAFE: refuse on ANY bad file or collision BEFORE touching a
	// single file, so one poison file or one collision can never leave a
	// half-migrated tree.
	if len(m.BadFiles) > 0 {
		return m, fmt.Errorf("pre-scan found %d unmigratable triple file(s); mutated nothing:\n%s",
			len(m.BadFiles), formatBadFiles(m.BadFiles))
	}
	if len(m.Collisions) > 0 {
		return m, fmt.Errorf("pre-scan found %d filename collision(s); mutated nothing (a collapse would DELETE a distinct triple):\n%s",
			len(m.Collisions), formatCollisions(m.Collisions))
	}

	for _, pp := range plans {
		if err := applyProjectPlan(pp); err != nil {
			return m, err
		}
	}

	if err := v.verifyMigration(plans); err != nil {
		return m, fmt.Errorf("post-migration verification FAILED (nothing stamped; `git checkout .` rolls back): %w", err)
	}
	return m, nil
}

// planTripleFilenames is the shared scan+resolve engine behind both the plan and
// apply entry points. It walks every project, parses every triple file, computes
// each file's flat target, and resolves the moves into renames, same-body
// collapses, real collisions, and bad files. It mutates nothing.
func (v *Vault) planTripleFilenames() (TripleFilenameMigration, []*projectPlan, error) {
	var m TripleFilenameMigration

	palaceDir := filepath.Join(v.Root, "palace")
	entries, err := os.ReadDir(palaceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return m, nil, nil // no palace tree yet — nothing to migrate
		}
		return m, nil, fmt.Errorf("read palace dir: %w", err)
	}

	var plans []*projectPlan
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		project := e.Name()
		triplesDir, err := v.KGTriplesDir(project)
		if err != nil {
			continue // not a valid project slug — nothing this migration owns
		}
		if _, err := os.Stat(triplesDir); err != nil {
			continue // no triples dir for this project
		}
		m.Projects++

		pp := &projectPlan{
			project:    project,
			triplesDir: triplesDir,
			renames:    map[string]string{},
			collapses:  map[string][]string{},
		}

		// Collect + parse every triple file FIRST (recursively — the old layout
		// nested subjects/objects that carried a "/").
		var scanned []scannedTriple
		walkErr := filepath.WalkDir(triplesDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || filepath.Ext(path) != ".json" {
				return nil
			}
			m.Scanned++
			pp.scanned++
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return fmt.Errorf("read triple %s: %w", path, rerr)
			}
			var t Triple
			if uerr := json.Unmarshal(data, &t); uerr != nil {
				m.BadFiles = append(m.BadFiles, BadTripleFile{Path: path, Reason: "unparseable JSON: " + uerr.Error()})
				return nil
			}
			if strings.TrimSpace(t.Subject) == "" || strings.TrimSpace(t.Predicate) == "" || strings.TrimSpace(t.Object) == "" {
				m.BadFiles = append(m.BadFiles, BadTripleFile{Path: path, Reason: "not a Triple: blank subject/predicate/object"})
				return nil
			}
			newPath, perr := v.KGTriplePath(project, t.Subject, t.Predicate, t.Object)
			if perr != nil {
				m.BadFiles = append(m.BadFiles, BadTripleFile{Path: path, Reason: "cannot encode: " + perr.Error()})
				return nil
			}
			scanned = append(scanned, scannedTriple{oldPath: path, newPath: newPath, t: t})
			return nil
		})
		if walkErr != nil {
			return m, nil, fmt.Errorf("walk %s: %w", triplesDir, walkErr)
		}

		v.resolveProjectMoves(pp, scanned, &m)
		plans = append(plans, pp)
	}

	// Deterministic ordering for stable previews and reproducible apply.
	sort.Slice(plans, func(i, j int) bool { return plans[i].project < plans[j].project })
	sort.Slice(m.Collisions, func(i, j int) bool { return m.Collisions[i].SourcePath < m.Collisions[j].SourcePath })
	sort.Slice(m.BadFiles, func(i, j int) bool { return m.BadFiles[i].Path < m.BadFiles[j].Path })
	return m, plans, nil
}

// resolveProjectMoves groups the scanned files by flat target and classifies
// each group as already-OK, a rename, a same-body collapse, or a hard collision.
// It records collision/collapse/rename intents into pp and the running counts
// into m. It reads (never writes) any pre-existing target on disk.
func (v *Vault) resolveProjectMoves(pp *projectPlan, scanned []scannedTriple, m *TripleFilenameMigration) {
	// Group the true MOVES (newPath != oldPath) by their shared flat target.
	groups := map[string][]scannedTriple{}
	for _, s := range scanned {
		if s.newPath == s.oldPath {
			pp.alreadyOK++
			m.AlreadyOK++
			continue
		}
		groups[s.newPath] = append(groups[s.newPath], s)
	}

	newPaths := make([]string, 0, len(groups))
	for np := range groups {
		newPaths = append(newPaths, np)
	}
	sort.Strings(newPaths)

	for _, np := range newPaths {
		srcs := groups[np]
		sort.Slice(srcs, func(i, j int) bool { return srcs[i].oldPath < srcs[j].oldPath })

		// (1) Two DISTINCT triples that encode to one flat name is a real
		// collision — refuse, never collapse.
		base := srcs[0]
		for _, s := range srcs[1:] {
			if !sameSPO(base.t, s.t) {
				m.Collisions = append(m.Collisions, collision(np, base, s.oldPath, s.t))
			}
		}
		if hasCollisionAt(m.Collisions, np) {
			continue // do not plan any mutation for a colliding target
		}

		// (2) Is a file already sitting at the flat target on disk?
		if tgt, exists, terr := readTargetBody(np); exists {
			if terr != nil || !sameSPO(base.t, tgt) {
				// Foreign / corrupt / divergent body at the target — a collision.
				var os, op, oo string
				if terr == nil {
					os, op, oo = tgt.Subject, tgt.Predicate, tgt.Object
				}
				m.Collisions = append(m.Collisions, TripleCollision{
					NewPath: np, SourcePath: base.oldPath,
					SourceS: base.t.Subject, SourceP: base.t.Predicate, SourceO: base.t.Object,
					OtherPath: np, OtherS: os, OtherP: op, OtherO: oo,
				})
				continue
			}
			// Target already correct (same S/P/O): every source is a redundant
			// duplicate — collapse them all, rename none.
			for _, s := range srcs {
				pp.collapses[np] = append(pp.collapses[np], s.oldPath)
				m.Collapsed++
			}
			continue
		}

		// (3) No target on disk: rename ONE source into the flat name, collapse
		// the rest (they are SAME-body duplicates, verified above).
		pp.renames[np] = base.oldPath
		m.Renamed++
		for _, s := range srcs[1:] {
			pp.collapses[np] = append(pp.collapses[np], s.oldPath)
			m.Collapsed++
		}
	}
}

func sameSPO(a, b Triple) bool {
	return a.Subject == b.Subject && a.Predicate == b.Predicate && a.Object == b.Object
}

func collision(np string, base scannedTriple, otherPath string, other Triple) TripleCollision {
	return TripleCollision{
		NewPath: np, SourcePath: base.oldPath,
		SourceS: base.t.Subject, SourceP: base.t.Predicate, SourceO: base.t.Object,
		OtherPath: otherPath, OtherS: other.Subject, OtherP: other.Predicate, OtherO: other.Object,
	}
}

func hasCollisionAt(cs []TripleCollision, np string) bool {
	for _, c := range cs {
		if c.NewPath == np {
			return true
		}
	}
	return false
}

// readTargetBody reads and parses the triple already present at path. exists is
// false when no file is there; when exists is true, err reports a read/parse
// failure (treated as a divergent/corrupt target by the caller).
func readTargetBody(path string) (t Triple, exists bool, err error) {
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return Triple{}, false, nil
		}
		return Triple{}, true, rerr
	}
	if uerr := json.Unmarshal(data, &t); uerr != nil {
		return Triple{}, true, uerr
	}
	return t, true, nil
}

// applyProjectPlan performs one project's resolved renames and collapses. Order
// is: renames first (fresh flat targets), then collapses (redundant sources),
// so a collapse never removes a file another group still needs to read.
func applyProjectPlan(pp *projectPlan) error {
	renTargets := make([]string, 0, len(pp.renames))
	for np := range pp.renames {
		renTargets = append(renTargets, np)
	}
	sort.Strings(renTargets)
	for _, np := range renTargets {
		src := pp.renames[np]
		if err := os.Rename(src, np); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", src, np, err)
		}
	}

	colTargets := make([]string, 0, len(pp.collapses))
	for np := range pp.collapses {
		colTargets = append(colTargets, np)
	}
	sort.Strings(colTargets)
	for _, np := range colTargets {
		for _, src := range pp.collapses[np] {
			if err := os.Remove(src); err != nil {
				return fmt.Errorf("remove redundant %s: %w", src, err)
			}
		}
	}

	pruneEmptyDirs(pp.triplesDir)
	return nil
}

// verifyMigration proves the migration preserved the data. Per project it
// asserts: (a) KGStats.TripleCount == scanned − collapsed (count preserved,
// dups removed); (b) every remaining triple file parses (KGStats/ListTriples
// re-read them all); (c) no nested subdirectory remains under triplesDir; and
// (d) a bounded spot-check that sampled triples are found by BOTH the
// subject-side and object-side query. Any failure is a loud error.
func (v *Vault) verifyMigration(plans []*projectPlan) error {
	for _, pp := range plans {
		// (c) fully flat: no directory may remain below triplesDir.
		var nested []string
		_ = filepath.WalkDir(pp.triplesDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && path != pp.triplesDir {
				nested = append(nested, path)
			}
			return nil
		})
		if len(nested) > 0 {
			return fmt.Errorf("project %q: %d nested subdir(s) remain under triples dir (e.g. %s)", pp.project, len(nested), nested[0])
		}

		// (a)+(b) count preserved and every file parses.
		stats, err := v.KGStats(pp.project)
		if err != nil {
			return fmt.Errorf("project %q: KGStats: %w", pp.project, err)
		}
		want := pp.scanned - collapsedCount(pp)
		if stats.TripleCount != want {
			return fmt.Errorf("project %q: post-migration triple count = %d, want %d (scanned %d − collapsed %d)",
				pp.project, stats.TripleCount, want, pp.scanned, collapsedCount(pp))
		}

		// (d) both-side queryability spot-check on a bounded sample.
		triples, err := v.ListTriples(pp.project)
		if err != nil {
			return fmt.Errorf("project %q: ListTriples: %w", pp.project, err)
		}
		if err := v.spotCheckBothSides(pp.project, triples); err != nil {
			return err
		}
	}
	return nil
}

func collapsedCount(pp *projectPlan) int {
	n := 0
	for _, srcs := range pp.collapses {
		n += len(srcs)
	}
	return n
}

// spotCheckBothSides samples up to bothSideSampleCap triples (strided across the
// list so the sample is not clustered) and asserts each is returned by both the
// subject-side ("out") and object-side ("in") query. A flat, correctly-named
// tree makes this hold by construction; the check catches any regression that
// silently breaks it.
func (v *Vault) spotCheckBothSides(project string, triples []Triple) error {
	const bothSideSampleCap = 64
	n := len(triples)
	if n == 0 {
		return nil
	}
	stride := 1
	if n > bothSideSampleCap {
		stride = n / bothSideSampleCap
	}
	for i := 0; i < n; i += stride {
		t := triples[i]
		out, err := v.QueryEntity(project, t.Subject, "", "out")
		if err != nil {
			return fmt.Errorf("project %q: subject-side query for %q: %w", project, t.Subject, err)
		}
		if !containsTriple(out, t) {
			return fmt.Errorf("project %q: triple (%s -%s-> %s) NOT found subject-side", project, t.Subject, t.Predicate, t.Object)
		}
		in, err := v.QueryEntity(project, t.Object, "", "in")
		if err != nil {
			return fmt.Errorf("project %q: object-side query for %q: %w", project, t.Object, err)
		}
		if !containsTriple(in, t) {
			return fmt.Errorf("project %q: triple (%s -%s-> %s) NOT found object-side", project, t.Subject, t.Predicate, t.Object)
		}
	}
	return nil
}

func containsTriple(ts []Triple, want Triple) bool {
	for _, t := range ts {
		if sameSPO(t, want) {
			return true
		}
	}
	return false
}

// kgMigrationLockPath is the stable sentinel key the migration locks to demand
// exclusive access. It need not exist on disk — vaultlock hashes it to a sidecar
// under .vp-locks/. It is a fixed, dedicated key so two migration runs contend
// on the same lock (the second refuses).
func (v *Vault) kgMigrationLockPath() string {
	return filepath.Join(v.Root, "palace", ".kg-filename-migration")
}

func formatBadFiles(bad []BadTripleFile) string {
	var b strings.Builder
	for _, f := range bad {
		fmt.Fprintf(&b, "  %s\n    %s\n", f.Path, f.Reason)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatCollisions(cs []TripleCollision) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "  target %s\n    source %s => (%s, %s, %s)\n    other  %s => (%s, %s, %s)\n",
			c.NewPath, c.SourcePath, c.SourceS, c.SourceP, c.SourceO,
			c.OtherPath, c.OtherS, c.OtherP, c.OtherO)
	}
	return strings.TrimRight(b.String(), "\n")
}

// pruneEmptyDirs removes empty directories strictly below root (never root
// itself), deepest-first. Best-effort: any error (incl. a non-empty dir) is
// ignored, since os.Remove only succeeds on an empty directory.
func pruneEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	for _, dir := range slices.Backward(dirs) {
		_ = os.Remove(dir)
	}
}
