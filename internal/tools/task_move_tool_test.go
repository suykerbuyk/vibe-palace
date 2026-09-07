// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/apperr"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/taskgraph"
)

// vp_manage_task action=move — the whole arm: the refuse-only core, the
// provenance it records at BOTH ends, and the surface that now advertises it.
//
// 🔴 The order the arm performs and these tests measure is RENAME → DESTINATION
// PROVENANCE → SOURCE TOMBSTONE. Position, not presence, is the contract at both
// ends, so the assertions below locate the provenance rather than merely find
// its text: a section that landed in the wrong file, or a tombstone that landed
// in the active directory, would satisfy a contains-check while being exactly
// the defect the ordering exists to prevent.
//
// The storage-level rules — dangling edges, occupied destination, archived
// source, byte-identical relocation by the rename itself — are pinned beside the
// writer in internal/storage/task_move_test.go, which stays a pure-rename test
// because MoveTaskToProject stays a pure rename. What is pinned HERE is
// everything that only exists at this layer: the destination-project gate, that
// a refusal scaffolds nothing, the two-ended provenance, and the golden surface.

// moveTaskVault builds a two-project vault: `moving` active in src-proj, and an
// anchor task in dst-proj so that Projects/dst-proj/ exists for the gate.
func moveTaskVault(t *testing.T) *storage.Vault {
	t.Helper()
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("src-proj", storage.TaskSpec{
		Slug: "moving", Title: "Moving", Content: "Body.\n", Priority: "medium",
	}); err != nil {
		t.Fatalf("CreateTask source: %v", err)
	}
	if err := vault.CreateTask("dst-proj", storage.TaskSpec{
		Slug: "anchor", Title: "Anchor", Content: "Body.\n", Priority: "medium",
	}); err != nil {
		t.Fatalf("CreateTask dest anchor: %v", err)
	}
	return vault
}

// TestManageTaskMoveRelocatesThroughTheHandler drives the arm an agent would
// call and measures the vault, not the return value: one copy, in the
// destination's ACTIVE directory, byte-identical to the original APART FROM the
// appended provenance section.
//
// 🔴 THIS ASSERTION WAS DELIBERATELY LOOSENED, ONCE, AND ONLY THIS FAR. In the
// refuse-only phase it read `string(after) != string(before)` — the move wrote
// no content, so ANY difference was a defect. The arm now appends one provenance
// section, so that exact equality is legitimately false. It is replaced by a
// PREFIX check plus an assertion that the ONLY thing following the prefix is the
// provenance section: everything the move did not add is still byte-identical,
// including the trailing newline the original body ended on. A bare
// strings.Contains(after, before) would have been the lazy relaxation and would
// pass on a re-rendered header, a re-flowed body, or content spliced in ahead of
// the original text.
func TestManageTaskMoveRelocatesThroughTheHandler(t *testing.T) {
	vault := moveTaskVault(t)
	srcPath, _ := vault.TaskFile("src-proj", "moving")
	before, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}

	raw, err := callManageTask(t, vault, manageTaskParams{
		Project: "src-proj", Action: "move", Task: "moving", ToProject: "dst-proj",
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	res, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", raw)
	}
	if res["status"] != "moved" || res["to_project"] != "dst-proj" || res["from_project"] != "src-proj" {
		t.Errorf("result does not describe the move it performed: %#v", res)
	}

	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Errorf("source copy still present, stat err = %v", err)
	}
	destPath, _ := vault.TaskFile("dst-proj", "moving")
	after, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if !strings.HasPrefix(string(after), string(before)) {
		t.Fatalf("the move rewrote text it did not add — the original bytes are no longer the file's prefix.\n before: %q\n  after: %q", before, after)
	}
	added := strings.TrimPrefix(string(after), string(before))
	if !strings.HasPrefix(added, "\n## Moved from src-proj\n") {
		t.Errorf("the only thing the move may append is the provenance section under its own H2; got %q", added)
	}
	if rest := strings.TrimPrefix(added, "\n## Moved from src-proj\n"); strings.Contains(rest, "\n## ") {
		t.Errorf("the move appended more than one section: %q", added)
	}
}

// TestManageTaskMoveUnknownDestinationProjectScaffoldsNothing is the gate.
//
// 🔴 THE ERROR ALONE IS NOT THE ASSERTION. An implementation that created
// Projects/<typo>/tasks/ and only then failed would satisfy an error-only check
// while leaving exactly the phantom-project junk the gate exists to prevent, so
// the ABSENCE of the tree is asserted too.
func TestManageTaskMoveUnknownDestinationProjectScaffoldsNothing(t *testing.T) {
	vault := moveTaskVault(t)

	_, err := callManageTask(t, vault, manageTaskParams{
		Project: "src-proj", Action: "move", Task: "moving", ToProject: "dts-proj",
	})
	if err == nil {
		t.Fatal("expected a refusal for an unknown destination project")
	}
	if !apperr.IsCaller(err) {
		t.Errorf("unknown destination project must be CALLER fault (it is a typo), got %#v", err)
	}
	if !strings.Contains(err.Error(), "dts-proj") {
		t.Errorf("refusal does not name the slug it refused: %v", err)
	}

	phantom := filepath.Join(vault.Root, "Projects", "dts-proj")
	if _, statErr := os.Stat(phantom); !os.IsNotExist(statErr) {
		t.Errorf("the refused move scaffolded %s (stat err = %v) — the gate must run BEFORE anything creates a directory", phantom, statErr)
	}

	// And the task never left home.
	srcPath, _ := vault.TaskFile("src-proj", "moving")
	if _, statErr := os.Stat(srcPath); statErr != nil {
		t.Errorf("refused move disturbed the source task: %v", statErr)
	}
}

// TestManageTaskMoveRequiresToProject: the schema now requires `to_project` for
// action=move (an allOf if/then, the same shape create and retire use), but that
// runs in validateParams BEFORE the handler. This drives the handler directly,
// which is the path every Dispatch-free caller takes, so the handler's own check
// is what it measures — belt and braces, exactly as retire's approval check is.
func TestManageTaskMoveRequiresToProject(t *testing.T) {
	vault := moveTaskVault(t)
	_, err := callManageTask(t, vault, manageTaskParams{
		Project: "src-proj", Action: "move", Task: "moving",
	})
	if err == nil {
		t.Fatal("expected a refusal when to_project is absent")
	}
	if !apperr.IsCaller(err) {
		t.Errorf("a missing required parameter is caller fault, got %#v", err)
	}
	if !strings.Contains(err.Error(), "to_project") {
		t.Errorf("refusal does not name the missing parameter: %v", err)
	}
}

// TestManageTaskMoveSurfacesTheDanglingEdgeRefusal proves the storage refusal
// reaches the agent intact — including the name of the action that owns the fix.
// A wrapper that flattened it to "move failed" would leave the caller with a no
// and no next move.
func TestManageTaskMoveSurfacesTheDanglingEdgeRefusal(t *testing.T) {
	vault := storage.NewVault(t.TempDir())
	if err := vault.CreateTask("src-proj", storage.TaskSpec{
		Slug: "child", Title: "Child", Content: "Body.\n", Priority: "medium", Parent: "an-epic",
	}); err != nil {
		t.Fatalf("CreateTask source: %v", err)
	}
	if err := vault.CreateTask("dst-proj", storage.TaskSpec{
		Slug: "anchor", Title: "Anchor", Content: "Body.\n", Priority: "medium",
	}); err != nil {
		t.Fatalf("CreateTask dest anchor: %v", err)
	}

	_, err := callManageTask(t, vault, manageTaskParams{
		Project: "src-proj", Action: "move", Task: "child", ToProject: "dst-proj",
	})
	if err == nil {
		t.Fatal("expected a refusal for a parent that does not resolve in the destination")
	}
	if !strings.Contains(err.Error(), "set_relations") {
		t.Errorf("refusal does not name set_relations as the owner of the fix: %v", err)
	}
	if !strings.Contains(err.Error(), "an-epic") {
		t.Errorf("refusal does not name the unresolved edge: %v", err)
	}
	destPath, _ := vault.TaskFile("dst-proj", "child")
	if _, statErr := os.Stat(destPath); !os.IsNotExist(statErr) {
		t.Errorf("refused move still created %s", destPath)
	}
}

// TestManageTaskMoveCommitsBothHalvesOfTheRename is the commit-seam test, and it
// exists because THE SEAM IS PER-PROJECT WHILE THIS WRITE DIRTIES TWO PROJECTS.
// commitTaskWrite probes one project's three task paths, so a single call would
// record the destination creation and leave the source deletion as dirt (or the
// reverse) — and an uncommitted task file makes vp_vault_sync REFUSE, which
// wedges sessions, drawers and KG triples along with it.
//
// It measures the worktree and HEAD rather than the return value: asserting the
// receipt alone would pass on a commit that recorded half a rename.
func TestManageTaskMoveCommitsBothHalvesOfTheRename(t *testing.T) {
	vault := newGitBackedTestVault(t)
	if err := vault.CreateTask("other-proj", storage.TaskSpec{
		Slug: "anchor", Title: "Anchor", Content: seedTaskBody(), Priority: "medium",
	}); err != nil {
		t.Fatalf("CreateTask dest anchor: %v", err)
	}
	gitVaultRun(t, vault.Root, "add", "-A")
	gitVaultRun(t, vault.Root, "commit", "-m", "seed destination project")
	assertVaultClean(t, vault.Root)

	if _, err := callManageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "move", Task: "seed-task", ToProject: "other-proj",
	}); err != nil {
		t.Fatalf("move: %v", err)
	}

	if dirt := genuineDirtOf(t, vault.Root); len(dirt) != 0 {
		t.Errorf("move left genuine dirt: %v", dirt)
	}
	assertVaultClean(t, vault.Root)

	tree := gitVaultRun(t, vault.Root, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(tree, "Projects/other-proj/tasks/seed-task.md") {
		t.Errorf("moved task missing from HEAD; tree:\n%s", tree)
	}
	if strings.Contains(tree, "Projects/test-proj/tasks/seed-task.md") {
		t.Errorf("move committed the destination but not the source deletion; tree:\n%s", tree)
	}
}

// TestManageTaskMoveWritesDestinationProvenanceUnderItsOwnH2 asserts POSITION,
// not presence.
//
// A provenance note is only useful if a reader can find it and a later writer
// can address it, and both of those depend on it being a SECTION: amend is keyed
// on H2 heading text, so prose appended without a heading of its own would be
// swallowed into whatever section happened to be last and would be unreachable
// by name forever. So the test locates the H2, and reads the section's own
// bounds — from its heading to the next H2 or EOF — before looking for the facts
// inside it.
func TestManageTaskMoveWritesDestinationProvenanceUnderItsOwnH2(t *testing.T) {
	vault := moveTaskVault(t)

	if _, err := callManageTask(t, vault, manageTaskParams{
		Project: "src-proj", Action: "move", Task: "moving", ToProject: "dst-proj",
	}); err != nil {
		t.Fatalf("move: %v", err)
	}

	destPath, _ := vault.TaskFile("dst-proj", "moving")
	body := string(mustReadToolFile(t, destPath))

	const heading = "## Moved from src-proj"
	idx := strings.Index(body, "\n"+heading+"\n")
	if idx < 0 {
		t.Fatalf("no %q section in the moved file:\n%s", heading, body)
	}

	// The section is the LAST thing in the file, which is what "appended" means,
	// and it does not sit above the task's own content.
	section := body[idx+len("\n"+heading+"\n"):]
	if strings.Contains(section, "\n## ") {
		t.Errorf("the provenance section is not the last section — something follows it:\n%s", section)
	}
	if strings.Index(body, "## Context") > idx {
		t.Error("the provenance section landed ABOVE the task's own content; it is a record of the move, not a preamble")
	}

	// And it records the three facts: the SOURCE project, the DAY, and the
	// commit the move was made against (here: honestly, that there is none).
	for _, want := range []string{
		"src-proj",
		storage.CalendarDay(time.Now()),
	} {
		if !strings.Contains(section, want) {
			t.Errorf("provenance section does not record %q:\n%s", want, section)
		}
	}
	if !strings.Contains(section, "no commit to record it against") {
		t.Errorf("a non-git vault must SAY it has no commit rather than omit the fact or invent one:\n%s", section)
	}
}

// TestManageTaskMoveProvenanceRecordsTheCommitItWasMadeAgainst is the git half of
// the fact above: when there IS a vault commit, the note names it. A note that
// only ever said "no commit to record" would pass the non-git test forever while
// recording nothing on a real vault.
func TestManageTaskMoveProvenanceRecordsTheCommitItWasMadeAgainst(t *testing.T) {
	vault := newGitBackedTestVault(t)
	if err := vault.CreateTask("other-proj", storage.TaskSpec{
		Slug: "anchor", Title: "Anchor", Content: seedTaskBody(), Priority: "medium",
	}); err != nil {
		t.Fatalf("CreateTask dest anchor: %v", err)
	}
	gitVaultRun(t, vault.Root, "add", "-A")
	gitVaultRun(t, vault.Root, "commit", "-m", "seed destination project")

	head := gitVaultRun(t, vault.Root, "rev-parse", "HEAD")

	if _, err := callManageTask(t, vault, manageTaskParams{
		Project: "test-proj", Action: "move", Task: "seed-task", ToProject: "other-proj",
	}); err != nil {
		t.Fatalf("move: %v", err)
	}

	destPath, _ := vault.TaskFile("other-proj", "seed-task")
	if body := string(mustReadToolFile(t, destPath)); !strings.Contains(body, head) {
		t.Errorf("provenance does not name the commit %s the move was made against:\n%s", head, body)
	}
	tombPath := filepath.Join(vault.Root, "Projects", "test-proj", "tasks", "cancelled", "seed-task.md")
	if body := string(mustReadToolFile(t, tombPath)); !strings.Contains(body, head) {
		t.Errorf("tombstone does not name the commit %s the move was made against:\n%s", head, body)
	}
}

// TestManageTaskMoveFilesTheSourceTombstoneInCancelled asserts POSITION, because
// position IS the contract here. tasks/cancelled/ is the directory this project
// already treats as the home of "why something is not here"; the same bytes
// sitting in the ACTIVE directory would instead assert that the task is still
// live work in the source project, which is precisely false.
func TestManageTaskMoveFilesTheSourceTombstoneInCancelled(t *testing.T) {
	vault := moveTaskVault(t)

	res, err := callManageTask(t, vault, manageTaskParams{
		Project: "src-proj", Action: "move", Task: "moving", ToProject: "dst-proj",
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	m, _ := res.(map[string]any)
	if m["tombstone"] != "written" {
		t.Errorf("result does not report a written tombstone: %#v", m)
	}

	activePath, _ := vault.TaskFile("src-proj", "moving")
	if _, statErr := os.Stat(activePath); !os.IsNotExist(statErr) {
		t.Errorf("the tombstone is in the source project's ACTIVE directory (%s) — it would read as live work", activePath)
	}
	donePath := filepath.Join(vault.Root, "Projects", "src-proj", "tasks", "done", "moving.md")
	if _, statErr := os.Stat(donePath); !os.IsNotExist(statErr) {
		t.Errorf("the tombstone landed in done/ (%s); a moved task was not finished", donePath)
	}

	tombPath := filepath.Join(vault.Root, "Projects", "src-proj", "tasks", "cancelled", "moving.md")
	body := string(mustReadToolFile(t, tombPath))

	// It is a real record, not a stub: it clears the same substantive-content
	// floor a hand-written create must clear, and it names the destination, the
	// destination path and the day.
	if len(body) < minTaskContentBytes {
		t.Errorf("tombstone body is %d bytes, under the %d-byte floor create enforces — a record nobody can act on", len(body), minTaskContentBytes)
	}
	for _, want := range []string{
		"dst-proj",
		"Projects/dst-proj/tasks/moving.md",
		storage.CalendarDay(time.Now()),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("tombstone does not record %q:\n%s", want, body)
		}
	}

	// CancelTask stamped it, so the file states its own terminal status rather
	// than relying on the reader noticing which directory it is in.
	meta, _, err := vault.GetTask("src-proj", "moving")
	if err != nil {
		t.Fatalf("GetTask(tombstone): %v", err)
	}
	if meta.Status != storage.StatusCancelled {
		t.Errorf("tombstone status = %q, want %q", meta.Status, storage.StatusCancelled)
	}
	if !meta.Done {
		t.Error("tombstone does not read as archived")
	}
}

// TestManageTaskMoveLeavesNoPROBLEMSInEitherProject is the acceptance criterion,
// and it is the one that catches a move that relocated the file and left a
// dangling edge behind it.
//
// It exercises the same derivation `vp tasks` prints under its PROBLEMS heading:
// taskgraph.BuildFromVault over ListTasks(project, true), then HasProblems() —
// cycles, dangling parent/depends references, and stale parents. Asserting on the
// CLI's rendered text would measure the printer; asserting on the graph measures
// the data the printer describes.
func TestManageTaskMoveLeavesNoPROBLEMSInEitherProject(t *testing.T) {
	vault := moveTaskVault(t)

	// A sibling left behind in the source, so the source project is not merely
	// empty after the move. It names nothing that leaves, so nothing it holds
	// can dangle — which is what makes a PROBLEM here a defect of the MOVE.
	if err := vault.CreateTask("src-proj", storage.TaskSpec{
		Slug: "staying", Title: "Staying", Content: "Body.\n", Priority: "low",
	}); err != nil {
		t.Fatalf("CreateTask sibling: %v", err)
	}

	if _, err := callManageTask(t, vault, manageTaskParams{
		Project: "src-proj", Action: "move", Task: "moving", ToProject: "dst-proj",
	}); err != nil {
		t.Fatalf("move: %v", err)
	}

	for _, proj := range []string{"src-proj", "dst-proj"} {
		g, err := taskgraph.BuildFromVault(vault, proj)
		if err != nil {
			t.Fatalf("BuildFromVault(%s): %v", proj, err)
		}
		if g.HasProblems() {
			t.Errorf("project %q reports PROBLEMS after a clean move: cycles=%v dangling=%v stale_parents=%v",
				proj, g.Cycles, g.Dangling, g.StaleParents)
		}
	}

	// The tombstone is a RECORD, not a node of the plan: it carries no edges of
	// its own, so it cannot introduce structure the work took with it.
	tomb, _, err := vault.GetTask("src-proj", "moving")
	if err != nil {
		t.Fatalf("GetTask(tombstone): %v", err)
	}
	if tomb.Parent != "" || len(tomb.Depends) != 0 {
		t.Errorf("tombstone carries edges (parent=%q depends=%v); a record must not assert structure that moved away", tomb.Parent, tomb.Depends)
	}
}

// TestManageTaskMoveTombstoneCatchesEdgesLeftBehind covers the tombstone's
// passive structural value, which is the reason cancelled/ is the right home
// rather than a note filed elsewhere: resolveTaskFile searches cancelled/ too, so
// a task still sitting in the source project that names the moved slug as its
// parent resolves to the record instead of dangling.
//
// The finding it produces is a STALE PARENT — "active, but its epic is finished"
// — and that is the honest report: the parent genuinely is not live work in this
// project any more. What it must never be is `dangling parent: ... (no such
// task)`, which would tell the reader the slug never existed.
func TestManageTaskMoveTombstoneCatchesEdgesLeftBehind(t *testing.T) {
	vault := moveTaskVault(t)
	if err := vault.CreateTask("src-proj", storage.TaskSpec{
		Slug: "left-child", Title: "Left child", Content: "Body.\n", Priority: "low", Parent: "moving",
	}); err != nil {
		t.Fatalf("CreateTask child: %v", err)
	}

	if _, err := callManageTask(t, vault, manageTaskParams{
		Project: "src-proj", Action: "move", Task: "moving", ToProject: "dst-proj",
	}); err != nil {
		t.Fatalf("move: %v", err)
	}

	g, err := taskgraph.BuildFromVault(vault, "src-proj")
	if err != nil {
		t.Fatalf("BuildFromVault: %v", err)
	}
	for _, d := range g.Dangling {
		if d.To == "moving" {
			t.Errorf("the left-behind parent edge DANGLES (%s -> %s): the tombstone should have caught it", d.From, d.To)
		}
	}
	found := false
	for _, s := range g.StaleParents {
		if s.From == "left-child" && s.To == "moving" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a STALE PARENT finding for left-child -> moving, got stale=%v dangling=%v", g.StaleParents, g.Dangling)
	}
}

// mustReadToolFile is the read-or-fail this file uses. Every assertion here is
// about bytes in the vault, so a read error is a test failure, not a value to
// carry forward.
func mustReadToolFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// ---------------------------------------------------------------------------
// The surface — written LAST, and pinned here at the three things that had to
// be true of it.
// ---------------------------------------------------------------------------

// manageTaskGoldenPath is the build-time tool-surface manifest, relative to this
// package. It is generated from cmd/vp (the only package that can build the full
// tool set without an import cycle) and lives beside the Registry it pins.
const manageTaskGoldenPath = "../mcp/tool_surface.golden.json"

// The two values as they stood at HEAD, before `move` reached the surface.
// They are canned ON PURPOSE and they are the only canned strings in this file:
// the assertion is "this changed" and "this did not", and a BEFORE value cannot
// be re-derived from the tree that now carries the AFTER. Everything else here
// is measured against the live registry and the live golden.
const (
	manageTaskSchemaSHAAtHEAD = "4fb44b92cffff09f17e7b4561eb3344e38aaeb734cf9c0748eebea37cef65832"
	surfaceVersionAtHEAD      = 3
)

// goldenToolSurface is the subset of the manifest these assertions read.
type goldenToolSurface struct {
	SurfaceVersion int `json:"surface_version"`
	Tools          []struct {
		Name         string `json:"name"`
		Mutating     bool   `json:"mutating"`
		SchemaSHA256 string `json:"schema_sha256"`
	} `json:"tools"`
}

// readGoldenToolSurface loads the manifest and returns it with the vp_manage_task
// row already picked out.
func readGoldenToolSurface(t *testing.T) (goldenToolSurface, struct {
	Name         string `json:"name"`
	Mutating     bool   `json:"mutating"`
	SchemaSHA256 string `json:"schema_sha256"`
}) {
	t.Helper()
	var g goldenToolSurface
	if err := json.Unmarshal(mustReadToolFile(t, manageTaskGoldenPath), &g); err != nil {
		t.Fatalf("parse golden %s: %v", manageTaskGoldenPath, err)
	}
	for _, tool := range g.Tools {
		if tool.Name == "vp_manage_task" {
			return g, tool
		}
	}
	t.Fatalf("vp_manage_task is absent from the golden %s", manageTaskGoldenPath)
	panic("unreachable")
}

// liveManageTaskSchemaSHA reproduces the golden generator's canonicalization:
// the raw schema is round-tripped through encoding/json so whitespace and key
// order are normalized, then hashed. Recomputing it here rather than reading the
// golden twice is what makes the next assertion an agreement between two
// independent derivations instead of a tautology.
func liveManageTaskSchemaSHA(t *testing.T) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(manageTaskSchema, &v); err != nil {
		t.Fatalf("manageTaskSchema is not valid JSON: %v", err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-marshal schema: %v", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestManageTaskMoveSurfaceSchemaSHAMoved is assertion one: putting `move` on
// the surface CHANGED vp_manage_task's schema hash, and the golden carries the
// new value.
//
// Both halves matter. "Changed from HEAD" alone would pass on a golden nobody
// regenerated if the hash happened to differ for another reason; "golden equals
// live" alone would pass on a schema nobody edited. Together they say the schema
// moved AND the golden was regenerated to match.
func TestManageTaskMoveSurfaceSchemaSHAMoved(t *testing.T) {
	_, tool := readGoldenToolSurface(t)

	if tool.SchemaSHA256 == manageTaskSchemaSHAAtHEAD {
		t.Errorf("vp_manage_task schema_sha256 is unchanged from HEAD (%s) — `move`, `to_project` and the new "+
			"if/then are supposed to have moved it; the golden was not regenerated", manageTaskSchemaSHAAtHEAD)
	}
	if live := liveManageTaskSchemaSHA(t); tool.SchemaSHA256 != live {
		t.Errorf("golden schema_sha256 = %s, live = %s — regenerate with:\n"+
			"  go test ./cmd/vp -run TestToolSurfaceGolden -update-golden", tool.SchemaSHA256, live)
	}
}

// TestManageTaskMoveSurfaceStaysMutating is assertion two, and it is the one
// that keeps the DISPATCH SEAM correct. vp_manage_task was already Mutating and
// must stay so: the read-only gate refuses a mutating tool by that flag, and a
// `move` arm reachable through a tool that had drifted to non-mutating would be
// a vault write escaping the gate entirely. Because the flag does not change,
// mcp.MutatingToolNames needs no edit — and this asserts that non-edit rather
// than assuming it.
func TestManageTaskMoveSurfaceStaysMutating(t *testing.T) {
	_, tool := readGoldenToolSurface(t)
	if !tool.Mutating {
		t.Error("vp_manage_task is no longer mutating in the golden — a task writer that is not gated as a writer")
	}
	if live := ManageTaskTool(storage.NewVault(t.TempDir())); !live.Mutating {
		t.Error("ManageTaskTool no longer declares Mutating; the surface and the registry disagree")
	}
}

// TestManageTaskMoveDoesNotBumpTheSurfaceVersion is assertion three, and it
// records a RULING, not a preference (iteration 344).
//
// A schema_sha256 move is not a compatibility break. The trigger for bumping
// MCPSurfaceVersion is a change in WHAT GETS WRITTEN INTO THE VAULT, and what
// this change writes is a provenance H2 plus a cancelled task — ordinary task
// markdown that an older binary reads fine. Bumping anyway would gate every host
// running the previous binary out of a vault for no incompatibility at all,
// which is the false-alarm side of the same honest-instruments rule that kept
// `move` off the surface until it worked.
func TestManageTaskMoveDoesNotBumpTheSurfaceVersion(t *testing.T) {
	g, _ := readGoldenToolSurface(t)
	if g.SurfaceVersion != surfaceVersionAtHEAD {
		t.Errorf("golden surface_version = %d, want %d unchanged: a schema_sha256 move is not a compatibility "+
			"break (ruled at iteration 344). The trigger for a bump is a change in what gets WRITTEN INTO the "+
			"vault, and a provenance H2 plus a tombstone is ordinary task markdown", g.SurfaceVersion, surfaceVersionAtHEAD)
	}
	if surface.MCPSurfaceVersion != surfaceVersionAtHEAD {
		t.Errorf("surface.MCPSurfaceVersion = %d, want %d unchanged — see above; this change must not touch "+
			"internal/surface/version.go at all", surface.MCPSurfaceVersion, surfaceVersionAtHEAD)
	}
}
