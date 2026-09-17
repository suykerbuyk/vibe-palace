package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// gitInitVault makes root a git repo with an identity, so --apply's
// requireVaultGitRepo gate passes and HasUncommittedChanges can answer.
func gitInitVault(t *testing.T, root string) {
	t.Helper()
	if err := storage.GitInit(root); err != nil {
		t.Fatalf("git init: %v", err)
	}
	for _, kv := range [][2]string{
		{"user.name", "sectionstest"},
		{"user.email", "sectionstest@vibe-palace.invalid"},
		{"commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", "-C", root, "config", kv[0], kv[1])
		cmd.Env = storage.SafeGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %s: %s: %v", kv[0], out, err)
		}
	}
}

// gitCommitAll stages and commits everything, so the per-file dirty check sees
// a clean path. A file seeded AFTER this call is untracked, which git reports
// as `??` — that is how the dirty test makes one file dirty and not the other.
func gitCommitAll(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-m", "seed", "--no-gpg-sign"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = storage.SafeGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
}

// ---------------------------------------------------------------------------
// planTaskSections — the pure transform. No vault, no files, no git.
//
// 🔴 THESE FIXTURES ARE THE PRIMARY SIGNAL, NOT A FALLBACK. The live-vault test
// at the bottom of this file skips when no vault is configured, and CI configures
// none — re-derive with:
//
//	grep -rn 'vault_path\|VP_VAULT\|vibe-palace-vault' .github/workflows/
//
// so every branch of the transform must be reachable from here or it is not
// tested at all in the only place that always runs.
// ---------------------------------------------------------------------------

// nineShaped is the anatomy measured on all nine live atlassian-vault files:
// H1 / blank / Status / Priority / Parent [/ Depends] / blank / ### Plan /
// ### Definition of Done. depends is spelled separately because FOUR of the nine
// carry no **Depends:** line, which is why ModTime placement is asserted by RANK
// (last line of the header block) and not "after **Depends:**".
func nineShaped(depends bool) string {
	s := "# A legacy task\n\n**Status:** retired\n**Priority:** medium\n**Parent:** some-epic\n"
	if depends {
		s += "**Depends:** other-task\n"
	}
	return s + "\n### Plan\n\nDo the thing.\n\n### Definition of Done\n\nThe thing is done.\n"
}

func TestPlanTaskSections_PromotesEveryH3(t *testing.T) {
	for _, depends := range []bool{false, true} {
		before := nineShaped(depends)
		if verr := storage.ValidateWholeTaskFile(before); verr == nil {
			t.Fatal("precondition: the fixture must FAIL the validator before repair, or the test is vacuous")
		}

		after, outcome, reason, promos := planTaskSections(before)
		if outcome != sectionsPromote {
			t.Fatalf("depends=%v: outcome = %v, want sectionsPromote (reason %q)", depends, outcome, reason)
		}
		if promos != 2 {
			t.Errorf("depends=%v: promos = %d, want 2", depends, promos)
		}
		if verr := storage.ValidateWholeTaskFile(after); verr != nil {
			t.Errorf("depends=%v: repaired file still fails the validator: %v", depends, verr)
		}

		// EVERY H3 is promoted, not just the first. Promoting only "Plan" would
		// satisfy the validator while leaving "Definition of Done" NESTED under
		// it — a worse file and a different `amend` surface.
		if !strings.Contains(after, "\n## Plan\n") {
			t.Errorf("depends=%v: first H3 was not promoted:\n%s", depends, after)
		}
		if !strings.Contains(after, "\n## Definition of Done\n") {
			t.Errorf("depends=%v: SECOND H3 was left nested under the first:\n%s", depends, after)
		}
		if strings.Contains(after, "###") {
			t.Errorf("depends=%v: an H3 survived the promotion:\n%s", depends, after)
		}
		// Nothing but the heading prefixes moved.
		if got, want := len(before)-len(after), 2; got != want {
			t.Errorf("depends=%v: %d bytes changed, want exactly %d (one '#' per promoted heading)", depends, got, want)
		}
		if !strings.Contains(after, "**Status:** retired") || !strings.Contains(after, "Do the thing.") {
			t.Errorf("depends=%v: the transform disturbed the header or the body:\n%s", depends, after)
		}
	}
}

func TestPlanTaskSections_FencedH3IsNotPromoted(t *testing.T) {
	// The only H3 outside a fence is "Plan"; the fenced one is sample text.
	before := "# T\n\n**Status:** retired\n**Priority:** medium\n\n### Plan\n\n" +
		"```markdown\n### Not a heading\n```\n\nbody\n"

	after, outcome, reason, promos := planTaskSections(before)
	if outcome != sectionsPromote {
		t.Fatalf("outcome = %v, want sectionsPromote (reason %q)", outcome, reason)
	}
	if promos != 1 {
		t.Errorf("promos = %d, want 1 — the fenced ### must not be counted", promos)
	}
	if !strings.Contains(after, "\n## Plan\n") {
		t.Errorf("the real H3 was not promoted:\n%s", after)
	}
	if !strings.Contains(after, "\n### Not a heading\n") {
		t.Errorf("the FENCED ### was promoted; a line-prefix scan was used instead of mdfence:\n%s", after)
	}
}

// TestPlanTaskSections_Refusals covers every shape the command must refuse
// rather than guess at. Each asserts the CLASS; the reason text is checked only
// for the substring that identifies which rule fired.
func TestPlanTaskSections_Refusals(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    taskSectionsOutcome
		reason  string
	}{{
		// 🔴 The error for an unterminated fence is the FENCE message, not
		// "missing title" and not "missing section": unbalancedFence returns at
		// tasks.go:2477-2478, BEFORE the OutsideFences scan, so those later arms
		// are unreachable for this input.
		// 🔴 THE H3 SITS AFTER THE FENCE OPENS, AND THAT IS THE POINT.
		// mdfence.OutsideFences treats the tail of a half-open fence as fenced
		// and drops it, so without the balance check this file reads as having
		// NO H3 and is misreported as the BOLD class — a broken file
		// masquerading as one with nothing to promote, which the post-condition
		// backstop never sees because no transform is attempted. An earlier
		// version of this fixture put the H3 BEFORE the fence and stayed GREEN
		// when the balance check was deleted.
		name:    "unterminated fence swallowing the only H3",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n```go\nnever closed\n\n### Plan\n\nbody\n",
		want:    sectionsRefused,
		reason:  "unterminated code fence",
	}, {
		name:    "unterminated fence after the H3",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n### Plan\n\n```go\nnever closed\n",
		want:    sectionsRefused,
		reason:  "unterminated code fence",
	}, {
		name:    "missing H1",
		content: "**Status:** retired\n**Priority:** medium\n\n### Plan\n\nbody\n",
		want:    sectionsRefused,
		reason:  "0 \"# \" H1 title line(s)",
	}, {
		name:    "two H1s",
		content: "# T\n\n# Second\n\n**Status:** retired\n**Priority:** medium\n\n### Plan\n\nbody\n",
		want:    sectionsRefused,
		reason:  "2 \"# \" H1 title line(s)",
	}, {
		name:    "empty H3",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n###\n\nbody\n",
		want:    sectionsRefused,
		reason:  "empty H3 heading",
	}, {
		name:    "H4 present",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n### Plan\n\n#### Detail\n\nbody\n",
		want:    sectionsRefused,
		reason:  "H4 heading present",
	}, {
		name:    "indented H3",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n   ### Plan\n\nbody\n",
		want:    sectionsRefused,
		reason:  "indented H3 heading",
	}, {
		name:    "duplicate H3 titles",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n### Plan\n\na\n\n### Plan\n\nb\n",
		want:    sectionsRefused,
		reason:  "two H3 headings both titled",
	}, {
		// No H2 and no H3 — the BOLD pseudo-heading class, a separate unit.
		name:    "no H3 at all",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n**Plan Details**\n\nbody\n",
		want:    sectionsNoH3,
		reason:  "no \"## \" H2 and no \"### \" H3",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			after, outcome, reason, _ := planTaskSections(tc.content)
			if outcome != tc.want {
				t.Fatalf("outcome = %v, want %v (reason %q)", outcome, tc.want, reason)
			}
			if !strings.Contains(reason, tc.reason) {
				t.Errorf("reason = %q, want it to contain %q", reason, tc.reason)
			}
			if after != "" {
				t.Errorf("a refused/skipped file must yield no content, got %d bytes", len(after))
			}
		})
	}
}

// TestPlanTaskSections_H3InsideHTMLCommentIsNotPromoted pins that a heading
// mdfence cannot see — it recognises only ` and ~ as delimiters — is not
// promoted into a fake section.
func TestPlanTaskSections_H3InsideHTMLCommentIsNotPromoted(t *testing.T) {
	before := "# T\n\n**Status:** retired\n**Priority:** medium\n\n<!--\n### Commented out\n-->\n\n### Plan\n\nbody\n"
	after, outcome, reason, promos := planTaskSections(before)
	if outcome != sectionsPromote {
		t.Fatalf("outcome = %v, want sectionsPromote (reason %q)", outcome, reason)
	}
	if promos != 1 {
		t.Errorf("promos = %d, want 1 — the commented-out ### must not be counted", promos)
	}
	if !strings.Contains(after, "\n### Commented out\n") {
		t.Errorf("a ### inside an HTML comment was promoted into a fake section:\n%s", after)
	}
}

// TestPlanTaskSections_AlreadyHasH2IsSkipped is the zero-H2 PRECONDITION's own
// test.
//
// 🔴 IT NEEDS A FILE THAT IS INVALID FOR AN UNRELATED REASON, and that is the
// whole point. A file with an H2 that already VALIDATES returns at the
// early-exit on the first line of planTaskSections and never reaches the h2 > 0
// branch — so it passes whether the precondition exists or not. The first
// version of this test did exactly that and stayed GREEN when the precondition
// was deleted. The fixture below has an H2, an H3, and no **Priority:** line, so
// it reaches the branch under test.
//
// Break: change `if h2 > 0` to `if false && h2 > 0`. This test fails.
func TestPlanTaskSections_AlreadyHasH2IsSkipped(t *testing.T) {
	t.Run("valid file returns at the early exit", func(t *testing.T) {
		before := "# T\n\n**Status:** retired\n**Priority:** medium\n\n## Context\n\nbody\n\n### Sub\n\nmore\n"
		if verr := storage.ValidateWholeTaskFile(before); verr != nil {
			t.Fatalf("precondition: this fixture must already VALIDATE: %v", verr)
		}
		_, outcome, _, _ := planTaskSections(before)
		if outcome != sectionsNoWork {
			t.Errorf("outcome = %v, want sectionsNoWork", outcome)
		}
	})

	t.Run("invalid file with an H2 is still not ours", func(t *testing.T) {
		// H2 present, H3 present, but no **Priority:** — invalid for a reason
		// this command does not repair.
		before := "# T\n\n**Status:** retired\n\n## Context\n\nbody\n\n### Sub\n\nmore\n"
		if verr := storage.ValidateWholeTaskFile(before); verr == nil {
			t.Fatal("precondition: this fixture must FAIL the validator, or the branch is not reached")
		}
		after, outcome, reason, _ := planTaskSections(before)
		if outcome != sectionsNoWork {
			t.Fatalf("outcome = %v, want sectionsNoWork — a file that already has an H2 is not ours "+
				"to restructure, whatever else is wrong with it (reason %q)", outcome, reason)
		}
		if after != "" {
			t.Error("a skipped file must yield no content")
		}
	})
}

// TestPlanTaskSections_PostConditionRefusesAnUnfixableFile pins the real safety
// property: the shape checks give a readable reason, but what makes a bad write
// impossible is that the TRANSFORMED bytes must pass the validator. Here the
// promotion succeeds structurally and the file is still invalid (no Priority),
// so it must be refused rather than written.
func TestPlanTaskSections_PostConditionRefusesAnUnfixableFile(t *testing.T) {
	before := "# T\n\n**Status:** retired\n\n### Plan\n\nbody\n"
	after, outcome, reason, _ := planTaskSections(before)
	if outcome != sectionsRefused {
		t.Fatalf("outcome = %v, want sectionsRefused — promoting does not make this file valid", outcome)
	}
	if !strings.Contains(reason, "promoting would not make the file valid") {
		t.Errorf("reason = %q, want the post-condition refusal", reason)
	}
	if after != "" {
		t.Error("a refused file must yield no content")
	}
}

// ---------------------------------------------------------------------------
// The walk, against a temp vault.
// ---------------------------------------------------------------------------

func seedArchivedTask(t *testing.T, root, project, sub, slug, content string) string {
	t.Helper()
	dir := filepath.Join(root, "Projects", project, "tasks", sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMigrateTaskSections_ReportWritesNothing(t *testing.T) {
	root := t.TempDir()
	path := seedArchivedTask(t, root, "p", "done", "legacy", nineShaped(true))

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Fix != 1 {
		t.Errorf("Fix = %d, want 1", sum.Fix)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — report mode must not write", sum.Applied)
	}
	got, _ := os.ReadFile(path)
	if string(got) != nineShaped(true) {
		t.Error("report mode modified the file on disk")
	}
	if !strings.Contains(out.String(), "REPORT ONLY") {
		t.Errorf("report mode did not say so:\n%s", out.String())
	}
}

// TestMigrateTaskSections_ActiveFilesAreNeverTouched is the DISJOINTNESS guard.
//
// 🔴 A missing H2 on an ACTIVE file belongs to `vp migrate task-preamble`'s
// PreambleSkippedNoH2 class. Two surfaces repairing the same byte is exactly
// what that idiom exists to prevent.
//
// Break: add "" to taskSectionsDirs. This test fails.
func TestMigrateTaskSections_ActiveFilesAreNeverTouched(t *testing.T) {
	root := t.TempDir()
	active := seedArchivedTask(t, root, "p", "", "activelegacy", nineShaped(false))

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Scanned != 0 {
		t.Errorf("Scanned = %d, want 0 — the active directory must not be walked", sum.Scanned)
	}
	if sum.Fix != 0 {
		t.Fatalf("Fix = %d, want 0 — an ACTIVE file was selected; this collides with task-preamble", sum.Fix)
	}
	got, _ := os.ReadFile(active)
	if string(got) != nineShaped(false) {
		t.Error("an active task file was modified")
	}
}

// TestMigrateTaskSections_WalksBothArchiveDirs pins the walk's COVERAGE. A file
// in cancelled/ is as archived as one in done/, and dropping either from
// taskSectionsDirs silently shrinks the population with no other symptom.
//
// Break: remove "cancelled" (or "done") from taskSectionsDirs. This test fails.
func TestMigrateTaskSections_WalksBothArchiveDirs(t *testing.T) {
	root := t.TempDir()
	seedArchivedTask(t, root, "p", "done", "a", nineShaped(true))
	seedArchivedTask(t, root, "p", "cancelled", "b", nineShaped(false))

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Scanned != 2 {
		t.Errorf("Scanned = %d, want 2 — both done/ and cancelled/ must be walked", sum.Scanned)
	}
	if sum.Fix != 2 {
		t.Fatalf("Fix = %d, want 2; a whole archive directory is missing from the walk:\n%s", sum.Fix, out.String())
	}
}

func TestMigrateTaskSections_ApplyPromotesAndValidates(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	path := seedArchivedTask(t, root, "p", "done", "legacy", nineShaped(true))
	gitCommitAll(t, root)

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 1 {
		t.Fatalf("Applied = %d, want 1:\n%s", sum.Applied, out.String())
	}
	got, _ := os.ReadFile(path)
	if verr := storage.ValidateWholeTaskFile(string(got)); verr != nil {
		t.Errorf("the written file does not validate: %v\n%s", verr, got)
	}
	if !strings.Contains(string(got), "\n## Plan\n") || !strings.Contains(string(got), "\n## Definition of Done\n") {
		t.Errorf("headings were not promoted on disk:\n%s", got)
	}

	// ModTime is inserted as the LAST line of the header block — by RANK
	// (headerFieldOrder puts ModTime 5th and the insertion point breaks before
	// the first field of rank >= 5), not "after **Depends:**".
	stamp := modTimeStampFor(t)
	if !strings.Contains(string(got), stamp) {
		t.Errorf("the write did not restamp ModTime (%s):\n%s", stamp, got)
	}
	lines := strings.Split(string(got), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "**ModTime:**") {
			if i == 0 || !strings.HasPrefix(lines[i-1], "**") {
				t.Errorf("ModTime is not inside the header block, line %d", i+1)
			}
			if strings.TrimSpace(lines[i+1]) != "" {
				t.Errorf("ModTime is not the LAST header line; next line is %q", lines[i+1])
			}
		}
	}
}

// TestMigrateTaskSections_ModTimeLandsLastForBothHeaderShapes covers the four of
// nine files that carry NO **Depends:** line. Asserting "after **Depends:**"
// would pass on five files and silently mean nothing on the other four.
func TestMigrateTaskSections_ModTimeLandsLastForBothHeaderShapes(t *testing.T) {
	for _, depends := range []bool{false, true} {
		root := t.TempDir()
		gitInitVault(t, root)
		path := seedArchivedTask(t, root, "p", "done", "legacy", nineShaped(depends))
		gitCommitAll(t, root)

		var out bytes.Buffer
		if _, err := runTaskSectionsMigration(root, "", true, &out); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		lines := strings.Split(string(got), "\n")
		last := ""
		for _, l := range lines {
			if strings.HasPrefix(l, "**") {
				last = l
			} else if last != "" {
				break
			}
		}
		if !strings.HasPrefix(last, "**ModTime:**") {
			t.Errorf("depends=%v: last header line is %q, want the ModTime line", depends, last)
		}
	}
}

// TestMigrateTaskSections_SecondRunSelectsNothing pins SELECTOR convergence.
//
// 🔴 THIS DOES NOT TEST THE WRITER'S BYTE-IDENTICAL SHORT-CIRCUIT, and must not
// be described as if it did. After run 1 the file HAS H2s, so it fails the
// zero-H2 precondition and is filtered out — `Applied == 0` proves the writer
// was never CALLED, which is the opposite of proving the short-circuit fired.
// The transform can never emit its own input anyway: promotion deletes one byte
// per heading, so output != input for every admissible file. The seam's
// short-circuit is pinned where it belongs, at
// internal/storage/tasks_test.go:TestOverwriteNoOpDoesNotRestampModTime.
func TestMigrateTaskSections_SecondRunSelectsNothing(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	path := seedArchivedTask(t, root, "p", "done", "legacy", nineShaped(true))
	gitCommitAll(t, root)

	var out bytes.Buffer
	if _, err := runTaskSectionsMigration(root, "", true, &out); err != nil {
		t.Fatal(err)
	}
	afterRun1, _ := os.ReadFile(path)

	out.Reset()
	sum2, err := runTaskSectionsMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum2.Fix != 0 || sum2.Applied != 0 {
		t.Errorf("second run: Fix = %d, Applied = %d, want 0 and 0", sum2.Fix, sum2.Applied)
	}
	afterRun2, _ := os.ReadFile(path)
	if !bytes.Equal(afterRun1, afterRun2) {
		t.Error("the second run changed the file")
	}
}

// TestMigrateTaskSections_DirtyFileIsSkippedNotOverwritten pins the per-file
// dirty guard. This is a WHOLE-FILE overwrite into a vault other sessions write
// concurrently, and git holds the only copy of uncommitted work.
//
// Break: delete the HasUncommittedChanges block. This test fails.
func TestMigrateTaskSections_DirtyFileIsSkippedNotOverwritten(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	seedArchivedTask(t, root, "p", "done", "committed", nineShaped(true))
	gitCommitAll(t, root)
	// Now add a second file that git has never seen: untracked counts as dirty.
	dirty := seedArchivedTask(t, root, "p", "done", "uncommitted", nineShaped(false))

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Dirty != 1 {
		t.Errorf("Dirty = %d, want 1:\n%s", sum.Dirty, out.String())
	}
	if sum.Applied != 1 {
		t.Errorf("Applied = %d, want 1 — the CLEAN file must still be repaired", sum.Applied)
	}
	got, _ := os.ReadFile(dirty)
	if string(got) != nineShaped(false) {
		t.Error("a file with uncommitted changes was overwritten")
	}
	// A dirty file is not a failure: it heals the moment the operator commits.
	if sum.Failed != 0 {
		t.Errorf("Failed = %d, want 0 — dirty must not flip the exit code", sum.Failed)
	}
}

// TestMigrateTaskSections_ShadowedSlugIsRefused pins the shadow guard.
//
// 🔴 Vault.resolveTaskFile searches ACTIVE FIRST, so writing an archived slug
// that also exists under tasks/ silently rewrites the ACTIVE file.
// `vp migrate task-header-spacing` omits this guard and a reviewer reproduced it
// destroying an active task with an archived body.
//
// 🔴 THE ACTIVE FILE'S HEADER MUST MATCH THE ARCHIVED ONE, AND THAT IS WHY THIS
// TEST IS BUILT THE WAY IT IS. The first version gave the active file a
// different Status/Priority, so when the guard was deleted the STRICT seam
// (headerMustMatch) refused the write on its own and the test stayed GREEN —
// passing for a reason that had nothing to do with the guard. With identical
// headers the seam permits the write, and the guard is the only thing standing
// between this command and a destroyed active task.
//
// Break: change `if taskHeaderShadowed(...)` to `if false && taskHeaderShadowed(...)`.
// This test fails.
func TestMigrateTaskSections_ShadowedSlugIsRefused(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	// Byte-identical header block to nineShaped(true), so refuseHeaderChange has
	// nothing to object to and cannot stand in for the guard.
	activeBody := "# A legacy task\n\n**Status:** retired\n**Priority:** medium\n**Parent:** some-epic\n" +
		"**Depends:** other-task\n\n## Context\n\nThe REAL active file.\n"
	active := seedArchivedTask(t, root, "p", "", "shadowed", activeBody)
	archived := seedArchivedTask(t, root, "p", "done", "shadowed", nineShaped(true))
	gitCommitAll(t, root)

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 0 {
		t.Errorf("Applied = %d, want 0 — a shadowed slug must never be written", sum.Applied)
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — a shadowed slug is a vault defect needing a human", sum.Failed)
	}
	if got, _ := os.ReadFile(active); string(got) != activeBody {
		t.Fatalf("🔴 THE ACTIVE FILE WAS REWRITTEN WITH THE ARCHIVED BODY:\n%s", got)
	}
	// The archived file is also left alone: the run refused, it did not redirect.
	if got, _ := os.ReadFile(archived); string(got) != nineShaped(true) {
		t.Errorf("the archived file was modified despite the refusal:\n%s", got)
	}
	if !strings.Contains(out.String(), "an ACTIVE task of the same slug exists") {
		t.Errorf("the refusal was not reported:\n%s", out.String())
	}
}

// TestMigrateTaskSections_RollbackBannerIsScoped pins that the undo names exact
// paths. A bare `git checkout .` in a vault holding every project would revert
// other sessions' in-flight work.
func TestMigrateTaskSections_RollbackBannerIsScoped(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	seedArchivedTask(t, root, "p", "done", "legacy", nineShaped(true))
	gitCommitAll(t, root)

	var out bytes.Buffer
	if _, err := runTaskSectionsMigration(root, "", true, &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "Do NOT use `git checkout .`") {
		t.Errorf("the scoped-rollback warning is missing:\n%s", s)
	}
	if !strings.Contains(s, "Projects/p/tasks/done/legacy.md") {
		t.Errorf("the banner does not name the exact path written:\n%s", s)
	}
}

func TestMigrateTaskSections_ApplyRequiresGitRepo(t *testing.T) {
	root := t.TempDir()
	seedArchivedTask(t, root, "p", "done", "legacy", nineShaped(true))

	var out bytes.Buffer
	_, err := runTaskSectionsMigration(root, "", true, &out)
	if err == nil {
		t.Fatal("--apply against a non-git vault must be refused")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("err = %v, want the git-repo refusal", err)
	}
}

// ---------------------------------------------------------------------------
// Live vault. Read-only, and it never writes.
// ---------------------------------------------------------------------------

// TestMigrateTaskSections_LiveArchivedCorpus runs the transform in memory over
// the real archived corpus.
//
// Fixtures agree with whoever wrote them; this project has been bitten three
// times by bugs that passed a green suite and surfaced only against real files.
//
// 🔴 It SKIPS without a vault, and a skip is acceptable. What is NOT acceptable
// is PASSING over an empty selection, so the population precondition t.Fatals.
func TestMigrateTaskSections_LiveArchivedCorpus(t *testing.T) {
	root := os.Getenv("VP_LIVE_VAULT")
	if root == "" {
		t.Skip("no live vault configured for this test; set VP_LIVE_VAULT to the vault root")
	}
	if _, err := os.Stat(root); err != nil {
		t.Skipf("configured vault is not present on this host: %v", err)
	}

	var selected, promoted int
	for _, sub := range taskSectionsDirs {
		matches, _ := filepath.Glob(filepath.Join(root, "Projects", "*", "tasks", sub, "*.md"))
		for _, path := range matches {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			before := string(data)
			after, outcome, _, _ := planTaskSections(before)
			if outcome != sectionsPromote {
				continue
			}
			selected++
			if verr := storage.ValidateWholeTaskFile(before); verr == nil {
				t.Errorf("%s: selected a file that ALREADY validates", path)
				continue
			}
			if verr := storage.ValidateWholeTaskFile(after); verr != nil {
				t.Errorf("%s: still fails after the transform: %v", path, verr)
				continue
			}
			promoted++
		}
	}

	if selected == 0 {
		t.Fatal("precondition: the live corpus must produce at least one selected file, " +
			"or this test passes vacuously and proves nothing")
	}
	t.Logf("live corpus: %d file(s) selected, %d repaired to validity", selected, promoted)
}
