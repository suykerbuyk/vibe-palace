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
		// No H2 and no H3, but a whole-line BOLD pseudo-heading. This fixture
		// asserted sectionsNoH3 while the class was deferred to a separate unit;
		// the file comment reserved the class by name and this command now
		// implements it, so the expectation moves from "skipped" to "promoted".
		name:    "bold pseudo-heading, no H3",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n**Plan Details**\n\nbody\n",
		want:    sectionsPromoteBold,
	}, {
		// A bold line with a value after it is a FIELD, not a heading.
		name:    "no H3 and no bold pseudo-heading",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\nplain prose only\n",
		want:    sectionsNoH3,
		reason:  "no bold pseudo-heading to promote",
	}, {
		// 🔴 THE THEMATIC BREAK, AND THIS ROW GUARDS A PANIC RATHER THAN A
		// MIS-PROMOTION. "***" satisfies both HasPrefix("**") and HasSuffix("**")
		// because the two OVERLAP, so only the length bound stops inner from being
		// sliced as t[2:1] — "slice bounds out of range [2:1]", demonstrated. A
		// crash part-way through a vault-wide run leaves whatever was already
		// written in place.
		name:    "*** thematic break does not panic or promote",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n***\n\nbody\n",
		want:    sectionsNoH3,
		reason:  "no bold pseudo-heading to promote",
	}, {
		// 🔴 THE SIBLING THE LENGTH BOUND DOES NOT REACH. "*****" is length 5, so it
		// clears len(t) >= 4; its inner is a single "*", so it clears the
		// empty-inner guard AND the nested-marker guard — and it promotes to the
		// heading "## *". Only "a line of nothing but asterisks is a thematic
		// break" closes it, and that closes the whole family at once.
		name:    "***** does not promote to a one-asterisk heading",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n*****\n\nbody\n",
		want:    sectionsNoH3,
		reason:  "no bold pseudo-heading to promote",
	}, {
		name:    "a longer asterisk run is still a thematic break",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n*******\n\nbody\n",
		want:    sectionsNoH3,
		reason:  "no bold pseudo-heading to promote",
	}, {
		// "****" is length 4 — the first length the slice can express — and yields
		// an EMPTY inner, which must be rejected rather than promoted to "## ".
		name:    "**** yields an empty heading and is not promoted",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n****\n\nbody\n",
		want:    sectionsNoH3,
		reason:  "no bold pseudo-heading to promote",
	}, {
		// 🔴 TWO BOLD RUNS IN ONE SENTENCE IS PROSE, NOT A HEADING. Without the
		// nested-marker guard this is promoted to the mangled heading
		// "## Note** and **Warning".
		name:    "a sentence with two bold runs is not a heading",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n**Note** and **Warning**\n\nbody\n",
		want:    sectionsNoH3,
		reason:  "no bold pseudo-heading to promote",
	}, {
		// 🔴 ONE TRAILING SPACE WALKED PAST THE COLON GUARD. The label test must
		// run on the TRIMMED inner text: "**x: **" is the same label shape as
		// "**x:**", and an untrimmed test promotes it to "## x: ".
		name:    "a colon label with a trailing space is still not a heading",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n**Acceptance criteria: **\n\n- a\n",
		want:    sectionsNoH3,
		reason:  "no bold pseudo-heading to promote",
	}, {
		// 🔴 A trailing colon INSIDE the bold makes it a label for the list
		// beneath it, not a section title. This shape occurs in the corpus, and a
		// colon-tolerant predicate silently restructures a file nobody asked to
		// restructure.
		name:    "bold label with a trailing colon is NOT a heading",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n\n**Acceptance criteria:**\n\n- a\n- b\n",
		want:    sectionsNoH3,
		reason:  "no bold pseudo-heading to promote",
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
			// A promoting outcome MUST carry content; every other outcome must
			// carry none, so a refusal can never smuggle bytes toward the writer.
			if tc.want == sectionsPromoteBold {
				if after == "" {
					t.Error("a promoting outcome must yield the transformed content")
				}
				if verr := storage.ValidateWholeTaskFile(after); verr != nil {
					t.Errorf("the promoted content does not validate: %v", verr)
				}
			} else if after != "" {
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

// ---------------------------------------------------------------------------
// Send-back fixes. Each test below was added because a break against the
// behaviour it describes previously left the suite GREEN.
// ---------------------------------------------------------------------------

// TestPlanTaskSections_RefusalsCiteATrueCause is the ORDERING test.
//
// 🔴 A refusal may only name a shape that actually blocks the promotion. An
// earlier version returned from inside the scan loop, before the `h2 > 0`
// escape, so a file with plenty of H2s — never a candidate for this command —
// was refused citing an H4 or a duplicate H3 that blocked nothing. On the live
// corpus five of six refusals named a false cause that way; their real defect
// was a missing **Status:** or a doubled **Priority:**. This output is B2's
// input, so a false cause misdirects the next unit.
//
// Break: move the `if h2 > 0` escape back below the scan loop, or return the
// refusals from inside the loop again. This test fails.
func TestPlanTaskSections_RefusalsCiteATrueCause(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{{
		// Has H2s AND an H4. The H4 blocks nothing: the file was never a
		// candidate, because it already has an addressable section.
		name: "H2s present alongside an H4",
		content: "# T\n\n**Priority:** medium\n\n## Context\n\nbody\n\n" +
			"## Plan\n\n#### Deep\n\nmore\n",
	}, {
		// Has H2s AND two same-named H3s. Same reasoning.
		name: "H2s present alongside duplicate H3s",
		content: "# T\n\n**Priority:** medium\n\n## Context\n\n### Files\n\na\n\n" +
			"## Plan\n\n### Files\n\nb\n",
	}, {
		// The live shape: two **Priority:** lines, plus an H4.
		name: "two Priority lines alongside an H4",
		content: "# T\n\n**Status:** retired\n**Priority:** medium\n**Priority:** high\n\n" +
			"## Context\n\nbody\n\n#### Deep\n\nmore\n",
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verr := storage.ValidateWholeTaskFile(tc.content)
			if verr == nil {
				t.Fatal("precondition: the fixture must FAIL the validator, or there is no cause to get right")
			}
			_, outcome, reason, _ := planTaskSections(tc.content)
			if outcome != sectionsNoWork {
				t.Fatalf("outcome = %v, want sectionsNoWork — this file already has an H2, so it is "+
					"not this command's defect and must not be refused (reason given: %q)", outcome, reason)
			}
			// The reason must be the VALIDATOR's own first-failure message, not
			// a shape this command guessed at.
			if reason != verr.Error() {
				t.Errorf("reason = %q, want the validator's true cause %q", reason, verr.Error())
			}
			for _, false_ := range []string{"heading present", "two H3 headings", "empty H", "indented"} {
				if strings.Contains(reason, false_) {
					t.Errorf("reason cites a shape that blocks nothing (%q): %q", false_, reason)
				}
			}
		})
	}
}

// TestMigrateTaskSections_OtherDefectRosterIsPrinted pins that a malformed file
// which is NOT this command's defect still reaches the operator — and B2.
//
// Break: delete the `if reason != ""` print in the sectionsNoWork arm. This
// test fails.
func TestMigrateTaskSections_OtherDefectRosterIsPrinted(t *testing.T) {
	root := t.TempDir()
	// Has an H2, so not ours; missing **Status:**, so still malformed.
	seedArchivedTask(t, root, "p", "done", "othersick",
		"# T\n\n**Priority:** medium\n\n## Context\n\nbody\n")

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.OtherDefect != 1 {
		t.Errorf("OtherDefect = %d, want 1", sum.OtherDefect)
	}
	s := out.String()
	if !strings.Contains(s, "not this command's defect") {
		t.Errorf("the other-defect roster was not printed:\n%s", s)
	}
	if !strings.Contains(s, "missing Status") {
		t.Errorf("the printed reason is not the validator's true cause:\n%s", s)
	}
}

// TestPlanTaskSections_FrontmatterHeadingIsSkipped pins the frontmatter span.
//
// 🔴 DELIBERATE DEVIATION FROM THE SPEC, ASSERTED SO IT CANNOT DRIFT SILENTLY.
// The spec's transform table lists a heading inside frontmatter under REFUSE;
// this command SKIPS the span and promotes the real headings around it. The
// rationale is on planTaskSections. This test is what makes the deviation a
// decision rather than an accident, and it is the only coverage the span had —
// deleting the tracking left the suite green before.
//
// Break: delete the inFrontmatter block in planTaskSections. This test fails.
func TestPlanTaskSections_FrontmatterHeadingIsSkipped(t *testing.T) {
	before := "---\n### not a section\ntitle: x\n---\n\n# T\n\n**Status:** retired\n" +
		"**Priority:** medium\n\n### Plan\n\nbody\n"

	after, outcome, reason, promos := planTaskSections(before)
	if outcome != sectionsPromote {
		t.Fatalf("outcome = %v, want sectionsPromote (reason %q)", outcome, reason)
	}
	if promos != 1 {
		t.Errorf("promos = %d, want 1 — the frontmatter heading must not be counted", promos)
	}
	if !strings.Contains(after, "\n### not a section\n") {
		t.Errorf("a heading inside frontmatter was promoted into a fake section:\n%s", after)
	}
	if !strings.Contains(after, "\n## Plan\n") {
		t.Errorf("the real H3 was not promoted:\n%s", after)
	}
}

// TestMigrateTaskSections_ShadowedSlugIsRefusedInREPORTMode is the send-back's
// first finding.
//
// 🔴 THE SHADOW GUARD MUST RUN IN BOTH MODES. Nesting it inside `if apply` — as
// cmd_migrate_task_header.go:317/:370/:409 do — makes the report print FIX,
// count the file as fixable and tell the operator to "re-run with --apply", for
// a file apply categorically refuses. The report would be promising something
// the write refuses, which is the exact lie the plan-first shape exists to
// prevent. The pre-existing shadow test passes apply=true and cannot see this.
//
// Break: move the taskHeaderShadowed call back inside `if apply {`. This test
// fails.
func TestMigrateTaskSections_ShadowedSlugIsRefusedInREPORTMode(t *testing.T) {
	root := t.TempDir()
	activeBody := "# A legacy task\n\n**Status:** retired\n**Priority:** medium\n**Parent:** some-epic\n" +
		"**Depends:** other-task\n\n## Context\n\nThe REAL active file.\n"
	seedArchivedTask(t, root, "p", "", "shadowed", activeBody)
	seedArchivedTask(t, root, "p", "done", "shadowed", nineShaped(true))

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if sum.Fix != 0 {
		t.Errorf("Fix = %d, want 0 — report counted a file apply refuses as fixable", sum.Fix)
	}
	if strings.Contains(s, "FIX   p/shadowed") {
		t.Errorf("report printed FIX for a shadowed slug apply will refuse:\n%s", s)
	}
	if sum.Failed != 1 {
		t.Errorf("Failed = %d, want 1 — a shadowed slug is a vault defect in either mode", sum.Failed)
	}
	if !strings.Contains(s, "an ACTIVE task of the same slug exists") {
		t.Errorf("report did not surface the shadow refusal:\n%s", s)
	}
	if strings.Contains(s, "Re-run with --apply") {
		t.Errorf("report told the operator to apply a run with nothing appliable:\n%s", s)
	}
}

// TestMigrateTaskSections_RollbackBannerListsEveryPathSeparately is the
// send-back's fourth finding.
//
// 🔴 IT NEEDS MORE THAN ONE APPLIED PATH, AND A TRACKED .surface. The earlier
// banner tests asserted over a population of size 1 — one task file, and a
// stamp that GitPathIsTracked dropped because it had never been committed. Two
// breaks survived that: joining every path into ONE quoted argument still
// contained the asserted substring, and deleting the GitPathIsTracked gate
// changed nothing. So the precise failure the banner exists to prevent — an
// untracked path making `git checkout --` a pathspec error that restores
// NOTHING — was unreachable from any test.
//
// Break: join the paths into one %q, or delete the GitPathIsTracked gate in
// taskSectionsRecordWrite. This test fails either way.
func TestMigrateTaskSections_RollbackBannerListsEveryPathSeparately(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	seedArchivedTask(t, root, "p", "done", "alpha", nineShaped(true))
	seedArchivedTask(t, root, "p", "done", "beta", nineShaped(false))
	// Pre-create and COMMIT the stamp so GitPathIsTracked says true and the
	// stamp reaches the rollback list. Without this it is untracked, correctly
	// dropped, and the population falls back to one path per project.
	if err := os.WriteFile(filepath.Join(root, "Projects", "p", ".surface"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCommitAll(t, root)

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 2 {
		t.Fatalf("Applied = %d, want 2:\n%s", sum.Applied, out.String())
	}
	want := []string{
		"Projects/p/tasks/done/alpha.md",
		"Projects/p/tasks/done/beta.md",
		"Projects/p/.surface",
	}
	for _, w := range want {
		if !slicesContains(sum.AppliedPaths, w) {
			t.Errorf("AppliedPaths %v is missing %q", sum.AppliedPaths, w)
		}
	}
	if len(sum.AppliedPaths) != 3 {
		t.Errorf("AppliedPaths = %v, want exactly 3 (two task files and the tracked stamp)", sum.AppliedPaths)
	}

	s := out.String()
	// EACH path separately quoted. A list joined into one quoted value keeps the
	// continuation indentation inside the argument and git receives one bogus
	// pathspec instead of three real ones.
	for _, w := range want {
		if !strings.Contains(s, `"`+w+`"`) {
			t.Errorf("banner does not quote %q on its own:\n%s", w, s)
		}
	}
	if strings.Contains(s, `"Projects/p/tasks/done/alpha.md Projects/`) {
		t.Errorf("banner joined several paths into ONE quoted argument:\n%s", s)
	}
	if !strings.Contains(s, "Do NOT use `git checkout .`") {
		t.Errorf("the scoped-rollback warning is missing:\n%s", s)
	}
}

// TestMigrateTaskSections_UntrackedStampIsNotInTheRollbackList is the other half
// of the same guard: a stamp git has never seen must be OMITTED, because one
// unknown pathspec makes `git checkout --` restore none of the task files
// either, while looking to the operator like the undo worked.
//
// Break: delete the GitPathIsTracked check in taskSectionsRecordWrite. This
// test fails.
func TestMigrateTaskSections_UntrackedStampIsNotInTheRollbackList(t *testing.T) {
	root := t.TempDir()
	gitInitVault(t, root)
	seedArchivedTask(t, root, "p", "done", "alpha", nineShaped(true))
	gitCommitAll(t, root) // no .surface committed: the writer creates it untracked

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", true, &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Applied != 1 {
		t.Fatalf("Applied = %d, want 1", sum.Applied)
	}
	for _, p := range sum.AppliedPaths {
		if strings.HasSuffix(p, ".surface") {
			t.Errorf("an UNTRACKED stamp reached the rollback list (%q); "+
				"`git checkout --` would fail for every path, restoring nothing", p)
		}
	}
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The write seam. Send-back finding three.
//
// 🔴 THE TWO SEAMS ARE INDISTINGUISHABLE FROM THIS COMMAND'S CALL SITE, AND
// THAT IS A FINDING RATHER THAN A TEST GAP. planTaskSections rewrites heading
// prefixes on lines the header block does not contain, so `after` carries a
// BYTE-IDENTICAL header to the file it was derived from; refuseHeaderChange has
// nothing to compare unequal. The one path where the writer could resolve to a
// DIFFERENT file than the one read is the shadow case, and the shadow guard now
// refuses that in both modes before any write. So no behavioural fixture can
// separate OverwriteTaskFile from OverwriteTaskFileRewritingHeader here, and
// swapping them leaves every behavioural test green — verified, not assumed.
//
// The decision is therefore pinned two ways instead: the DEPENDENCY it rests on
// (that the strict seam really does refuse what the permissive one allows), and
// the CALL ITSELF, by source shape. Neither is a behavioural test and neither
// pretends to be.
// ---------------------------------------------------------------------------

// TestTaskSectionsStrictSeamContract pins the property the strict seam is chosen
// FOR. If storage ever stopped refusing header changes through OverwriteTaskFile,
// the rationale in this command's header comment would be false and nothing else
// in this package would notice.
func TestTaskSectionsStrictSeamContract(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Projects", "p", "tasks", "done")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# T\n\n**Status:** retired\n**Priority:** medium\n\n## Context\n\nbody\n"
	write := func() {
		if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	headerChanged := "# T\n\n**Status:** done\n**Priority:** medium\n\n## Context\n\nbody\n"
	v := storage.NewVault(root)

	write()
	if err := v.OverwriteTaskFile("p", "x", headerChanged); err == nil {
		t.Error("the STRICT seam accepted a header-changing body; this command's seam rationale is void")
	}

	write()
	if err := v.OverwriteTaskFileRewritingHeader("p", "x", headerChanged); err != nil {
		t.Errorf("the PERMISSIVE seam refused a header-changing body (%v); the two seams no longer "+
			"differ, so choosing between them buys nothing", err)
	}

	// And the property this command actually relies on: a body that leaves the
	// header alone passes the STRICT seam, which is why the strict policy is free.
	write()
	bodyOnly := "# T\n\n**Status:** retired\n**Priority:** medium\n\n## Context\n\nCHANGED body\n"
	if err := v.OverwriteTaskFile("p", "x", bodyOnly); err != nil {
		t.Errorf("the STRICT seam refused a body-only change (%v); this command could not use it", err)
	}
}

// TestTaskSectionsUsesTheStrictSeam pins the CALL, by source shape, because no
// behavioural fixture can — see the block comment above.
//
// Break: change vault.OverwriteTaskFile to vault.OverwriteTaskFileRewritingHeader
// in the walk. This test fails; every behavioural test stays green.
func TestTaskSectionsUsesTheStrictSeam(t *testing.T) {
	src, err := os.ReadFile("cmd_migrate_task_sections.go")
	if err != nil {
		t.Fatalf("read own source: %v", err)
	}
	body := string(src)
	// Strip the file's doc comment, which discusses the permissive wrapper by
	// name, so this asserts over CODE rather than prose.
	if i := strings.Index(body, "\nvar migrateTaskSectionsFlags"); i > 0 {
		body = body[i:]
	}
	if !strings.Contains(body, "vault.OverwriteTaskFile(slug, taskSlug, after)") {
		t.Error("the walk no longer calls the STRICT seam vault.OverwriteTaskFile")
	}
	if strings.Contains(body, "OverwriteTaskFileRewritingHeader(") {
		t.Error("the walk calls the PERMISSIVE seam OverwriteTaskFileRewritingHeader; " +
			"this command writes archived files through the strict writer deliberately, and no " +
			"behavioural test can catch the swap because the transform never touches header bytes")
	}
}

// TestTaskSectionsPopulationMatchesTheDetector is the population differential
// the spec names.
//
// It cannot be written against the `vp audit task-files` SUBCOMMAND, which has
// no --vault flag and would read the configured (live) vault. It is written
// against the reporter that subcommand calls, runTaskFileValidityReport, which
// does take a root — so the two enumerations are compared in-process over a
// temp vault, with no live-vault access at all.
//
// 🔴 WHAT THIS PROVES AND DOES NOT. Both walks call the same predicate, so
// agreement says nothing about the RULES. It says the two ENUMERATIONS agree:
// which projects, which directories, which files. That is the divergence worth
// catching, and it is what cmd_audit_task_files.go:40-50 says the differential
// is for.
//
// Break: remove "cancelled" from taskSectionsDirs. This test fails.
func TestTaskSectionsPopulationMatchesTheDetector(t *testing.T) {
	root := t.TempDir()
	// Two projects, both archive directories, and a mix of classes.
	seedArchivedTask(t, root, "alpha", "done", "promote-me", nineShaped(true))
	seedArchivedTask(t, root, "alpha", "cancelled", "promote-me-too", nineShaped(false))
	seedArchivedTask(t, root, "beta", "done", "already-fine",
		"# T\n\n**Status:** retired\n**Priority:** medium\n\n## Context\n\nbody\n")
	seedArchivedTask(t, root, "beta", "cancelled", "other-defect",
		"# T\n\n**Priority:** medium\n\n## Context\n\nbody\n")
	// Active files must appear in NEITHER walk.
	seedArchivedTask(t, root, "alpha", "", "active-legacy", nineShaped(false))

	rep, err := runTaskFileValidityReport(root, "")
	if err != nil {
		t.Fatal(err)
	}
	detector := map[string]string{}
	for _, f := range rep.Failures {
		detector[f.Rel] = f.Reason
	}

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}

	if len(detector) == 0 || sum.Scanned == 0 {
		t.Fatal("precondition: both walks must find files, or agreement is vacuous")
	}
	if sum.Scanned != rep.Scanned {
		t.Errorf("this command scanned %d file(s), the detector scanned %d — the two ENUMERATIONS "+
			"disagree about which files exist", sum.Scanned, rep.Scanned)
	}

	// Every file this command would promote must be one the detector calls
	// invalid, with the missing-section reason specifically.
	var promoted int
	for _, d := range sum.Decisions {
		rel := "Projects/" + d.Project + "/tasks/" + d.Sub + "/" + d.Slug + ".md"
		switch d.Outcome {
		case sectionsPromote:
			promoted++
			reason, ok := detector[rel]
			if !ok {
				t.Errorf("%s: this command would promote a file the detector calls VALID", rel)
				continue
			}
			if !strings.Contains(reason, "missing section") {
				t.Errorf("%s: selected a file whose defect is %q, not the missing section", rel, reason)
			}
		case sectionsNoWork:
			if d.Reason == "" && detector[rel] != "" {
				t.Errorf("%s: this command considers it clean, the detector reports %q", rel, detector[rel])
			}
		}
	}
	if promoted != 2 {
		t.Errorf("promoted = %d, want 2 (one in done/, one in cancelled/)", promoted)
	}
	for rel := range detector {
		if strings.Contains(rel, "/tasks/active-legacy.md") {
			t.Errorf("the detector reported an ACTIVE file: %s", rel)
		}
	}
}

// TestMigrateTaskSections_ArchivedPairReasonNamesTheRealWinner pins the
// STRUCTURED refusal reason, which nothing pinned before and which was therefore
// free to be false.
//
// 🔴 THE PRINTED LINE AND THE STORED REASON ARE DIFFERENT STRINGS AND ONLY ONE OF
// THEM WAS TESTED. The two shadow tests above assert the printed output. The
// Decision.Reason a caller stores was asserted nowhere, so when the shared guard
// was widened to cover a done/+cancelled/ pair with no active twin, this call
// site kept a hardcoded "an ACTIVE task of the same slug exists" — a correct line
// printed beside a false structured cause. That is the class 930bde9 closed in
// this same file, reopened underneath it by a change in another command.
//
// Break: replace the taskHeaderShadowReason call with the old literal. This test
// fails; every other test in this file stays green, which is the point.
func TestMigrateTaskSections_ArchivedPairReasonNamesTheRealWinner(t *testing.T) {
	root := t.TempDir()
	// done/ and cancelled/ hold the same slug and there is NO active twin, so the
	// old active-only guard passed this through entirely.
	seedArchivedTask(t, root, "p", "done", "shadowed", nineShaped(false))
	seedArchivedTask(t, root, "p", "cancelled", "shadowed", nineShaped(true))

	var out bytes.Buffer
	sum, err := runTaskSectionsMigration(root, "", false, &out)
	if err != nil {
		t.Fatal(err)
	}

	var got string
	var found bool
	for _, d := range sum.Decisions {
		if d.Slug == "shadowed" && d.Sub == "cancelled" {
			got, found = d.Reason, true
		}
	}
	if !found {
		t.Fatalf("no decision recorded for the cancelled/ copy; out:\n%s", out.String())
	}
	want := "the same slug also exists in tasks/done/; the writer resolves done before cancelled"
	if got != want {
		t.Errorf("structured reason is wrong.\n got: %q\nwant: %q", got, want)
	}
	// And the ACTIVE case must keep its own wording, not be flattened into one
	// generic sentence — the two causes are different and the operator acts on
	// them differently.
	root2 := t.TempDir()
	seedArchivedTask(t, root2, "p", "", "shadowed", nineShaped(false))
	seedArchivedTask(t, root2, "p", "done", "shadowed", nineShaped(true))
	var out2 bytes.Buffer
	sum2, err := runTaskSectionsMigration(root2, "", false, &out2)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range sum2.Decisions {
		if d.Slug == "shadowed" && d.Sub == "done" {
			if d.Reason != "an ACTIVE task of the same slug exists; the writer resolves active first" {
				t.Errorf("the active-case reason changed: %q", d.Reason)
			}
		}
	}
}

// The three tests below exist because three breaks against the bold detector left
// the whole suite GREEN. Each names the break it was written for.

// TestPlanTaskSections_UnterminatedBoldIsNotPromoted.
//
// Break: drop the HasSuffix(t, "**") half of the predicate, so it matches a bold
// PREFIX rather than a whole-line bold run.
//
// The other fixtures cannot see that break: "**Status:** retired" survives it
// because the inner text still contains "**" and is excluded by the second guard.
// A line that OPENS bold and never closes it has no second "**" to be caught by,
// so under the break it is promoted — and because the inner text is computed by
// slicing two characters off the end, the heading it produces is the line with its
// last two characters SILENTLY TRUNCATED.
func TestPlanTaskSections_UnterminatedBoldIsNotPromoted(t *testing.T) {
	const content = "# T\n\n**Status:** retired\n**Priority:** medium\n\n**unterminated bold prose\n\nbody\n"
	after, outcome, reason, _ := planTaskSections(content)
	if outcome == sectionsPromoteBold {
		t.Fatalf("an unterminated bold run was promoted into a heading, truncating the line:\n%s", after)
	}
	if outcome != sectionsNoH3 {
		t.Errorf("outcome = %v, want sectionsNoH3 (reason %q)", outcome, reason)
	}
}

// TestPlanTaskSections_FencedBoldIsNotPromoted.
//
// Break: scan raw lines instead of mdfence.OutsideFences.
//
// A bold line inside a code fence is SAMPLE TEXT. Promoting it rewrites the
// sample, and — because the promoted line is no longer bold-shaped — the sample
// silently stops demonstrating what it was written to demonstrate. No other
// fixture carries a fenced bold line, so nothing else sees this break.
func TestPlanTaskSections_FencedBoldIsNotPromoted(t *testing.T) {
	const content = "# T\n\n**Status:** retired\n**Priority:** medium\n\n```\n**Plan Details**\n```\n\nbody\n"
	after, outcome, reason, _ := planTaskSections(content)
	if outcome == sectionsPromoteBold {
		t.Fatalf("a bold line INSIDE a code fence was promoted; the scan is not fence-aware:\n%s", after)
	}
	if outcome != sectionsNoH3 {
		t.Errorf("outcome = %v, want sectionsNoH3 (reason %q)", outcome, reason)
	}
}

// TestPlanTaskSections_BoldPromotionThatDoesNotValidateIsRefused.
//
// Break: remove the ValidateWholeTaskFile post-condition from the bold arm.
//
// Every other bold fixture validates after promotion, so removing the gate changes
// nothing for them. This file has a promotable bold pseudo-heading AND a second
// defect the promotion does not touch, so promoting it does not make it valid. The
// writer would refuse it later, but the REPORT would already have printed FIX and
// told the operator to re-run with --apply — promising something apply refuses,
// which is the exact lie plan-first exists to prevent.
func TestPlanTaskSections_BoldPromotionThatDoesNotValidateIsRefused(t *testing.T) {
	const content = "# T\n\n**Status:** retired\n**Priority:** medium\n**Priority:** high\n\n**Plan Details**\n\nbody\n"
	if storage.ValidateWholeTaskFile(content) == nil {
		t.Fatal("precondition: the fixture must FAIL the validator")
	}
	after, outcome, reason, _ := planTaskSections(content)
	if outcome == sectionsPromoteBold {
		t.Fatalf("a promotion that does not make the file valid was reported as a FIX:\n%s", after)
	}
	if outcome != sectionsRefused {
		t.Errorf("outcome = %v, want sectionsRefused (reason %q)", outcome, reason)
	}
	if after != "" {
		t.Error("a refused file must yield no content")
	}
}

// ---------------------------------------------------------------------------
// The bold promotion must not reach a region the scan beside it treats as
// non-existent.
// ---------------------------------------------------------------------------

// TestBoldPromotionNeverReachesAnInertRegion is a WRITE-side pin, not a refusal
// one. boldPseudoHeadingLines read mdfence.OutsideFences while the scan that
// decides the file has no sections skipped YAML frontmatter and HTML comments,
// so the promotion manufactured a "## " heading inside a region that scan treats
// as invisible. The rewritten file then VALIDATES — the validator counts "## "
// lines the same way — while remaining unaddressable by amend, which is the one
// thing the missing-section rule exists to guarantee. Validity is not
// correctness.
//
// Break: restore `for _, l := range mdfence.OutsideFences(content)` in
// boldPseudoHeadingLines. Both subtests fail, and they fail on the WRITTEN
// BYTES, not on a counter.
func TestBoldPromotionNeverReachesAnInertRegion(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{
			"inside an HTML comment",
			"# T\n\n**Status:** open\n**Priority:** high\n\n<!--\n**Design notes**\n-->\n\nbody\n",
		},
		{
			"inside YAML frontmatter",
			"---\n**Design notes**\n---\n# T\n\n**Status:** open\n**Priority:** high\n\nbody\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Positive first: the fixture must really be a missing-section file,
			// or every assertion below is vacuous.
			verr := storage.ValidateWholeTaskFile(tc.content)
			if verr == nil {
				t.Fatal("fixture validates clean, so this test cannot see the defect it exists for")
			}
			if !strings.Contains(verr.Error(), "missing section") {
				t.Fatalf("fixture fails for the wrong reason, so it never reaches the bold arm: %v", verr)
			}

			if got := boldPseudoHeadingLines(tc.content); len(got) != 0 {
				t.Errorf("boldPseudoHeadingLines = %v — a bold run inside an inert region was selected for promotion", got)
			}
			after, outcome, reason, promos := planTaskSections(tc.content)
			if outcome == sectionsPromoteBold {
				t.Fatalf("🔴 PROMOTED INSIDE AN INERT REGION — the file now validates while staying unaddressable:\n%q", after)
			}
			if after != "" {
				t.Errorf("after = %q, want empty — nothing may be rewritten here", after)
			}
			if promos != 0 {
				t.Errorf("promos = %d, want 0", promos)
			}
			if outcome != sectionsNoH3 {
				t.Errorf("outcome = %v, want sectionsNoH3 (%v); reason = %q", outcome, sectionsNoH3, reason)
			}
		})
	}
}

// TestBoldPromotionStillPromotesALiveBoldHeading is the over-correction guard.
// The bound above must remove the inert regions and nothing else: the live
// corpus specimen (Projects/vibe-palace/tasks/done/grok-vpc-skill.md, a plain
// "**Plan Details**" line) must still promote.
//
// Break: make outsideInertRegions return nil. This test fails; the two above
// pass, which is why a negative-only pair is not enough.
func TestBoldPromotionStillPromotesALiveBoldHeading(t *testing.T) {
	const content = "# T\n\n**Status:** open\n**Priority:** high\n\n**Plan Details**\n\nbody\n"
	if got := boldPseudoHeadingLines(content); len(got) != 1 || got[0] != 6 {
		t.Fatalf("boldPseudoHeadingLines = %v, want [6] — the bound removed a live bold heading", got)
	}
	after, outcome, reason, promos := planTaskSections(content)
	if outcome != sectionsPromoteBold {
		t.Fatalf("outcome = %v, want sectionsPromoteBold; reason = %q", outcome, reason)
	}
	if promos != 1 {
		t.Errorf("promos = %d, want 1", promos)
	}
	const want = "# T\n\n**Status:** open\n**Priority:** high\n\n## Plan Details\n\nbody\n"
	if after != want {
		t.Errorf("after =\n%q\nwant\n%q", after, want)
	}
}
