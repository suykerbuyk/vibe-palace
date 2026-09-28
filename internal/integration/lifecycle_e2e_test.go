// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package integration

// U7: the end-to-end, two-host proof of the quantum split, driven through the
// REAL vp binary against file:// bare remotes. It replaces the hand-run
// rehearsal harness (/home/johns/vp-split-rehearsal/harness/).
//
// 🔴 IT FOLLOWS THE U8 RUN SHEET LITERALLY
// (/home/johns/vp-scratch-imp3/u8/u8-procedure.md, "Run sheet"). Every step
// the operator runs is a step here, labelled with its sheet label (B3, B4,
// 1-6, and the "If step 5 refuses" branch); there is no step here the sheet
// does not have, except where a comment says why (a host's ordinary pull
// between the operator's steps, and the assertions). Each printed real-run
// line and each printed Undo line is run through `sh -c` exactly as printed,
// and the quantum vault's path holds a space, so a quoting fault in any
// printed line fails the test.
//
// The world:
//   - P: the personal vault, published to a bare repo; every host's clone
//     names that remote "github".
//   - Q: the quantum vault, created on the admin host by `vp vault init` with
//     two empty bare remotes, "origin" and the "github" mirror.
//   - Two hosts, A (the admin) and H, each with its own HOME,
//     XDG_CONFIG_HOME, XDG_CACHE_HOME, XDG_DATA_HOME and CLAUDE_HOME, its own
//     clone of P as its default vault, and checkouts of qms and orch.
//
// A "write through a checkout" is `vp memory harvest` run in that checkout: it
// resolves the project's vault exactly as a session does (ADR-012) and
// commits and pushes what it routes there.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/testinfra"
)

// lcProjects are the two moving projects: qms has both trees, orch is
// Projects-only (harness case P1).
var lcProjects = []string{"qms", "orch"}

// lcQuantumName is the quantum vault's directory on every host. It holds a
// space on purpose: every printed line that names it must quote it.
const lcQuantumName = "quantum vibe palace vault"

// lcPersonalName is the personal vault's directory on every host; it holds a
// space too, for the lines that name it (delete's real-run and Undo lines).
const lcPersonalName = "personal vibe palace vault"

type lcWorld struct {
	root     string
	pBare    string // P's published remote
	qOrigin  string // Q's origin
	qMirror  string // Q's github mirror
	A, H     *lcHost
	original map[string]string // P's moving footprint as seeded: path -> "mode oid"
}

type lcHost struct {
	name      string
	home      string
	claude    string
	personal  string // this host's clone of P: its default vault
	quantum   string // where this host's copy of Q lives, or will
	checkouts map[string]string
}

func lcGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// GIT_EDITOR=false: an editor invocation in the harness's own git fails
	// loudly rather than being accepted silently.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=false",
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func lcWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileURL(p string) string { return "file://" + filepath.ToSlash(p) }

func lcBare(t *testing.T, dir string) string {
	t.Helper()
	lcGit(t, filepath.Dir(dir), "init", "-q", "--bare", "-b", "main", dir)
	// The lifecycle commands fetch blobless and fetch blobs lazily.
	lcGit(t, dir, "config", "uploadpack.allowFilter", "true")
	lcGit(t, dir, "config", "uploadpack.allowAnySHA1InWant", "true")
	return dir
}

func lcPresent(p string) bool { _, err := os.Lstat(p); return err == nil }

// newLCWorld builds P with the moving projects and one that stays, and hosts
// A and H. seedExtra adds files to P's first commit.
func newLCWorld(t *testing.T, seedExtra map[string]string) *lcWorld {
	t.Helper()
	if testing.Short() {
		t.Skip("lifecycle end-to-end: builds the vp binary and drives several vaults; run with make integration")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := testinfra.RetainOnFailure(t, "lifecycle")
	w := &lcWorld{root: root}
	w.pBare = lcBare(t, filepath.Join(root, "p.git"))
	w.qOrigin = lcBare(t, filepath.Join(root, "q-origin.git"))
	w.qMirror = lcBare(t, filepath.Join(root, "q-mirror.git"))

	seed := filepath.Join(root, "p-seed")
	lcGit(t, root, "init", "-q", "-b", "main", seed)
	lcGit(t, seed, "config", "user.email", "seed@example.com")
	lcGit(t, seed, "config", "user.name", "Seed")
	files := map[string]string{
		".vibe-palace/vault.toml":                     fmt.Sprintf("format = %d\n", surface.RequiredDataFormat),
		".gitignore":                                  strings.Join(storage.CanonicalGitignorePatterns, "\n") + "\n",
		"Projects/qms/resume.md":                      "# qms\n",
		"Projects/qms/sessions/2026-09-01-aaaa-01.md": "---\nproject: qms\n---\n# a session\n",
		"Projects/qms/commit-log.anchor":              "9b6da95804608aeb80a7dc3ec22f7a79ba01efc8\n",
		"palace/qms/kg/entities.jsonl":                "{}\n",
		"Projects/orch/resume.md":                     "# orch\n",
		"Projects/stays/resume.md":                    "# stays in P\n",
	}
	for k, v := range seedExtra {
		files[k] = v
	}
	for rel, body := range files {
		lcWrite(t, filepath.Join(seed, rel), body)
	}
	lcGit(t, seed, "add", "-A")
	lcGit(t, seed, "commit", "-q", "-m", "personal vault")
	lcGit(t, seed, "push", "-q", fileURL(w.pBare), "main")
	w.original = footprintListing(t, w.pBare, "main")
	if len(w.original) == 0 {
		t.Fatal("fixture: P's footprint is empty")
	}

	w.A = w.newHost(t, "A")
	w.H = w.newHost(t, "H")
	return w
}

func (w *lcWorld) newHost(t *testing.T, name string) *lcHost {
	t.Helper()
	h := &lcHost{name: name, home: filepath.Join(w.root, "host-"+name), checkouts: map[string]string{}}
	h.claude = filepath.Join(h.home, ".claude")
	h.personal = filepath.Join(h.home, lcPersonalName)
	h.quantum = filepath.Join(h.home, lcQuantumName)
	if err := os.MkdirAll(h.home, 0o755); err != nil {
		t.Fatal(err)
	}
	lcGit(t, h.home, "clone", "-q", "-o", "github", fileURL(w.pBare), h.personal)
	lcWrite(t, filepath.Join(h.home, ".config", "vibe-palace", "config.toml"), "vault_path = \""+h.personal+"\"\n")
	for _, p := range append([]string{"stays"}, lcProjects...) {
		co := filepath.Join(h.home, "code", p)
		lcWrite(t, filepath.Join(co, ".vibe-palace.toml"), "[project]\nname = \""+p+"\"\n")
		h.checkouts[p] = co
	}
	return h
}

func (h *lcHost) env(extra ...string) []string {
	return append(append(os.Environ(),
		"HOME="+h.home, "USERPROFILE="+h.home,
		"XDG_CONFIG_HOME="+filepath.Join(h.home, ".config"),
		"XDG_CACHE_HOME="+filepath.Join(h.home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(h.home, ".local", "share"),
		"CLAUDE_HOME="+h.claude,
		"GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_AUTHOR_NAME=Host "+h.name, "GIT_AUTHOR_EMAIL="+strings.ToLower(h.name)+"@example.com",
		"GIT_COMMITTER_NAME=Host "+h.name, "GIT_COMMITTER_EMAIL="+strings.ToLower(h.name)+"@example.com",
		"VP_SURFACE_GATE="), extra...)
}

// vp runs the real binary as this host, in dir (the host's home if "").
func (h *lcHost) vp(t *testing.T, dir string, args ...string) testinfra.CLIResult {
	t.Helper()
	if dir == "" {
		dir = h.home
	}
	return testinfra.RunCLI(t, h.env(), dir, nil, args...)
}

// sh runs line through `sh -c`, exactly as the operator pastes it, with the
// built vp first on PATH.
func (h *lcHost) sh(t *testing.T, line string) testinfra.CLIResult {
	t.Helper()
	bin := testinfra.BuildVPBinary(t)
	cmd := exec.Command("sh", "-c", line)
	cmd.Dir = h.home
	cmd.Env = h.env("PATH=" + filepath.Dir(bin) + string(os.PathListSeparator) + os.Getenv("PATH"))
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("sh -c %q: %v", line, err)
		}
		code = ee.ExitCode()
	}
	return testinfra.CLIResult{Stdout: so.String(), Stderr: se.String(), ExitCode: code}
}

var runLineRE = regexp.MustCompile(`(?m)^To run it:\n\s+(vp .+)$`)

// dryRunThenPrinted runs a lifecycle command with --dry-run, then the exact
// line it printed, through sh -c. It returns the real run and the line.
func (h *lcHost) dryRunThenPrinted(t *testing.T, args ...string) (testinfra.CLIResult, string) {
	t.Helper()
	dry := h.vp(t, "", append(args, "--dry-run")...).Must(t)
	m := runLineRE.FindStringSubmatch(dry.Stdout)
	if m == nil {
		t.Fatalf("the dry run printed no real-run line:\n%s", dry.Stdout)
	}
	return h.sh(t, m[1]), m[1]
}

// printedUndo returns the lines a run printed under its undo heading.
func printedUndo(t *testing.T, out string) []string {
	t.Helper()
	var lines []string
	in := false
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "undo:"), strings.HasPrefix(l, "Undo ("):
			in = true
		case in && strings.HasPrefix(l, "  "):
			lines = append(lines, strings.TrimSpace(l))
		case in:
			in = false
		}
	}
	if len(lines) == 0 {
		t.Fatalf("no undo lines were printed:\n%s", out)
	}
	return lines
}

// harvest writes one project memory file into this host's native memory dir
// for the checkout, and runs `vp memory harvest` there.
func (h *lcHost) harvest(t *testing.T, project, tag string, extra ...string) testinfra.CLIResult {
	t.Helper()
	co := h.checkouts[project]
	lcWrite(t, filepath.Join(h.claude, "projects", archive.EncodeProjectDir(co), "memory", tag+".md"),
		"---\nname: "+tag+"\ndescription: written through "+h.name+"'s "+project+" checkout\nmetadata:\n  type: project\n---\n\n"+tag+"\n")
	return h.vp(t, co, append([]string{"memory", "harvest"}, extra...)...)
}

// footprintListing is the moving projects' trees at ref of a (bare or not)
// repository: path -> "mode oid", with .surface left out (each vault stamps
// its own).
func footprintListing(t *testing.T, repo, ref string) map[string]string {
	t.Helper()
	args := []string{"ls-tree", "-r", ref, "--"}
	for _, p := range lcProjects {
		args = append(args, "Projects/"+p, "palace/"+p)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ls-tree in %s: %v", repo, err)
	}
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		meta, path, ok := strings.Cut(l, "\t")
		if !ok || filepath.Base(path) == ".surface" {
			continue
		}
		f := strings.Fields(meta)
		m[path] = f[0] + " " + f[2]
	}
	return m
}

// assertFootprint fails unless repo's footprint at main is want, byte for byte.
func assertFootprint(t *testing.T, what, repo string, want map[string]string) {
	t.Helper()
	got := footprintListing(t, repo, "main")
	var diff []string
	for p, v := range want {
		if got[p] != v {
			diff = append(diff, fmt.Sprintf("  want %s %s, got %q", v, p, got[p]))
		}
	}
	for p, v := range got {
		if _, ok := want[p]; !ok {
			diff = append(diff, fmt.Sprintf("  extra %s %s", v, p))
		}
	}
	if len(diff) > 0 {
		sort.Strings(diff)
		t.Errorf("%s: %s's footprint is not as expected:\n%s", what, repo, strings.Join(diff, "\n"))
	}
}

func bareHas(t *testing.T, bare, dir, needle string) bool {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "ls-tree", "-r", "--name-only", "main", "--", dir).Output()
	return err == nil && strings.Contains(string(out), needle)
}

// ---- The run sheet's steps. ----

// sheetB is B3 and B4 on one host: sync, then every remote reads "in sync".
func (h *lcHost) sheetB(t *testing.T) {
	t.Helper()
	h.vp(t, "", "vault", "sync").Must(t)         // B3
	st := h.vp(t, "", "vault", "status").Must(t) // B4
	for _, r := range strings.Fields(lcGit(t, h.personal, "remote")) {
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(r) + `: in sync$`).MatchString(st.Stdout) {
			t.Fatalf("B4 on %s: remote %s does not read in sync:\n%s", h.name, r, st.Stdout)
		}
	}
}

// sheet1 is step 1: vault init, dry run, then the same line.
func (w *lcWorld) sheet1(t *testing.T) {
	t.Helper()
	args := []string{"vault", "init", w.A.quantum, "--remote", "origin=" + fileURL(w.qOrigin), "--remote", "github=" + fileURL(w.qMirror)}
	w.A.vp(t, "", append(args, "--dry-run")...).Must(t)
	w.A.vp(t, "", args...).Must(t)
}

// sheet2 is step 2: copy, dry run then the printed line. It returns the real
// run's output, whose undo lines the undo branch runs.
func (w *lcWorld) sheet2(t *testing.T) string {
	t.Helper()
	r, _ := w.A.dryRunThenPrinted(t, append(append([]string{"vault", "copy"}, lcProjects...), "--from", fileURL(w.pBare), "--vault", w.A.quantum)...)
	r.Must(t)
	return r.Stdout
}

// sheet3 is step 3: delete in the personal vault, dry run then the printed
// line. It returns the real run's output.
func (w *lcWorld) sheet3(t *testing.T) string {
	t.Helper()
	r, _ := w.A.dryRunThenPrinted(t, append(append([]string{"vault", "project", "delete"}, lcProjects...), "--moved-to", fileURL(w.qOrigin))...)
	r.Must(t)
	return r.Stdout
}

// sheet4 is step 4: bind both projects to Q.
func (w *lcWorld) sheet4(t *testing.T) {
	t.Helper()
	w.A.vp(t, "", append(append([]string{"config", "bind"}, lcProjects...), "--vault", w.A.quantum)...).Must(t)
}

// sheet5 is step 5 on a host: vault pull.
func (h *lcHost) sheet5(t *testing.T) testinfra.CLIResult {
	t.Helper()
	return h.vp(t, "", "vault", "pull")
}

// cloneArgs is step 6's command for host h.
func (w *lcWorld) cloneArgs(h *lcHost) []string {
	return append([]string{"vault", "clone", fileURL(w.qOrigin), h.quantum, "--bind"}, lcProjects...)
}

// sheet6 is step 6 on a host: clone --bind, dry run then the printed line.
func (w *lcWorld) sheet6(t *testing.T, h *lcHost) {
	t.Helper()
	r, _ := h.dryRunThenPrinted(t, w.cloneArgs(h)...)
	r.Must(t)
}

// sheetUndo is the "If step 5 refuses" branch's first bullet: the admin runs
// the undo lines step 2 printed, then the ones step 3 printed. between runs
// after the copy revert, for the tests that look at that state.
func (w *lcWorld) sheetUndo(t *testing.T, copyOut, deleteOut string, between func()) {
	t.Helper()
	for _, l := range printedUndo(t, copyOut) {
		w.A.sh(t, l).Must(t)
	}
	if between != nil {
		between()
	}
	for _, l := range printedUndo(t, deleteOut) {
		w.A.sh(t, l).Must(t)
	}
}

// adminMove is B3/B4 on the admin host and steps 1-4.
func (w *lcWorld) adminMove(t *testing.T) (copyOut, deleteOut string) {
	t.Helper()
	w.A.sheetB(t)
	w.sheet1(t)
	copyOut = w.sheet2(t)
	deleteOut = w.sheet3(t)
	w.sheet4(t)
	return copyOut, deleteOut
}

// assertMoved: both Q remotes hold exactly want, and P holds neither project.
func (w *lcWorld) assertMoved(t *testing.T, what string, want map[string]string) {
	t.Helper()
	assertFootprint(t, what, w.qOrigin, want)
	assertFootprint(t, what, w.qMirror, want)
	assertFootprint(t, what, w.pBare, map[string]string{})
}

// 1. Happy path, with the rows that belong to it: init publishes both
// remotes, the move is byte-identical, the second host's departed embed cache
// is swept by its pull, the raw write into the moved tree refuses (U15), and
// clone --bind makes the second host's write land in Q (U14b).
func TestIntegrationLifecycleHappyPath(t *testing.T) {
	w := newLCWorld(t, nil)
	w.H.sheetB(t) // B3, B4 on H; the admin's are in adminMove.
	// H has searched qms before: a host-local embed cache for it.
	cache := filepath.Join(w.H.personal, "palace", ".local", "embed-cache", "qms", "d1.vec")
	keep := filepath.Join(w.H.personal, "palace", ".local", "embed-cache", "stays", "d1.vec")
	lcWrite(t, cache, "vectors\n")
	lcWrite(t, keep, "vectors\n")

	w.adminMove(t)

	qHead := lcGit(t, w.A.quantum, "rev-parse", "HEAD~1") // the init commit, under the copy
	for _, b := range []string{w.qOrigin, w.qMirror} {
		if got := lcGit(t, b, "rev-parse", "main~1"); got != qHead {
			t.Errorf("%s: main~1 is %s, want init's commit %s", b, got, qHead)
		}
	}
	w.assertMoved(t, "after the move", w.original)
	for _, p := range lcProjects {
		if !bareHas(t, w.pBare, "Audits/departures", p+".json") {
			t.Errorf("P's remote has no departure record for %s", p)
		}
	}
	if !bareHas(t, w.pBare, "Projects/stays", "resume.md") {
		t.Error("the project that stays left P")
	}

	w.A.harvest(t, "qms", "admin-note").Must(t)
	for _, b := range []string{w.qOrigin, w.qMirror} {
		if !bareHas(t, b, "Projects/qms", "admin-note") {
			t.Errorf("A's write did not reach %s", b)
		}
	}

	w.H.sheet5(t).Must(t) // 5
	if lcPresent(filepath.Join(w.H.personal, "Projects/qms/resume.md")) {
		t.Error("H's pull left qms's tracked files in P")
	}
	// hostC: the pull swept the departed project's cache, and only that.
	if lcPresent(cache) || !lcPresent(keep) {
		t.Errorf("H's pull: qms cache present=%v (want gone), stays cache present=%v (want kept)", lcPresent(cache), lcPresent(keep))
	}

	t.Run("raw_write_into_departed_tree_refuses", func(t *testing.T) {
		r := w.H.vp(t, "", "vault", "write", "Projects/qms/stale.md", "--content", "stale")
		if r.ExitCode == 0 || lcPresent(filepath.Join(w.H.personal, "Projects/qms/stale.md")) {
			t.Fatalf("a raw write into the moved tree was accepted; exit %d\n%s\n%s", r.ExitCode, r.Stdout, r.Stderr)
		}
		// Refused for the right reason: qms departed, and the refusal names
		// where it went (the departure record's label).
		if !strings.Contains(r.Stderr, "departed") || !strings.Contains(r.Stderr, fileURL(w.qOrigin)) {
			t.Fatalf("the raw write must be refused as a departed project, naming its label %s; got:\n%s", fileURL(w.qOrigin), r.Stderr)
		}
	})

	t.Run("clone_bind", func(t *testing.T) {
		w.sheet6(t, w.H) // 6
		if urls := lcGit(t, w.H.quantum, "remote", "-v"); !strings.Contains(urls, fileURL(w.qMirror)) {
			t.Errorf("H's clone lacks the github mirror:\n%s", urls)
		}
		w.H.harvest(t, "qms", "host-h-note").Must(t)
		for _, b := range []string{w.qOrigin, w.qMirror} {
			if !bareHas(t, b, "Projects/qms", "host-h-note") {
				t.Errorf("H's write did not reach %s", b)
			}
		}
		if bareHas(t, w.pBare, "Projects/qms", "host-h-note") {
			t.Error("H's write reached P")
		}
	})
}

// The procedure's ordering rule: step 6 before step 5 refuses with "pull
// first" and writes nothing; after step 5 the same clone succeeds.
func TestIntegrationLifecycleCloneBeforePullRefuses(t *testing.T) {
	w := newLCWorld(t, nil)
	w.H.sheetB(t)
	w.adminMove(t)
	cfg := filepath.Join(w.H.home, ".config", "vibe-palace", "config.toml")
	cfgBefore, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{append(w.cloneArgs(w.H), "--dry-run"), w.cloneArgs(w.H)} {
		r := w.H.vp(t, "", args...)
		if r.ExitCode == 0 || !strings.Contains(r.Stdout+r.Stderr, "pull first") {
			t.Fatalf("a clone before the pull must refuse with \"pull first\"; exit %d\n%s\n%s", r.ExitCode, r.Stdout, r.Stderr)
		}
	}
	if lcPresent(w.H.quantum) {
		t.Fatal("a refused clone left its vault behind")
	}
	if after, _ := os.ReadFile(cfg); string(after) != string(cfgBefore) {
		t.Fatal("a refused clone changed H's config")
	}
	w.H.sheet5(t).Must(t) // 5
	w.sheet6(t, w.H)      // 6
	w.H.harvest(t, "qms", "after-pull").Must(t)
	if !bareHas(t, w.qOrigin, "Projects/qms", "after-pull") {
		t.Fatal("after the pull, H's clone is not the vault its write lands in")
	}
}

// P8 + bind: a vault whose remotes match none of the departure record's label
// is refused, whatever it holds.
func TestIntegrationLifecycleBindRefusesALabelMismatch(t *testing.T) {
	w := newLCWorld(t, nil)
	w.H.sheetB(t)
	w.adminMove(t)
	w.H.sheet5(t).Must(t)
	other := filepath.Join(w.H.home, "other-vault")
	lcGit(t, w.H.home, "clone", "-q", fileURL(w.qOrigin), other)
	lcGit(t, other, "remote", "set-url", "origin", "git@example.invalid:someone/else.git")
	r := w.H.vp(t, "", "config", "bind", "qms", "--vault", other)
	if r.ExitCode == 0 || !strings.Contains(r.Stderr, "no remote of") {
		t.Fatalf("a label mismatch must refuse; exit %d\n%s", r.ExitCode, r.Stderr)
	}
}

// Step 1 into a remote that is not empty refuses, and publishes nothing.
func TestIntegrationLifecycleInitRefusesANonEmptyRemote(t *testing.T) {
	w := newLCWorld(t, nil)
	lcGit(t, w.A.personal, "push", "-q", fileURL(w.qMirror), "HEAD:refs/heads/main")
	args := []string{"vault", "init", w.A.quantum, "--remote", "origin=" + fileURL(w.qOrigin), "--remote", "github=" + fileURL(w.qMirror)}
	for _, a := range [][]string{append(args, "--dry-run"), args} {
		r := w.A.vp(t, "", a...)
		if r.ExitCode == 0 || !strings.Contains(r.Stdout+r.Stderr, "not empty") {
			t.Fatalf("init into a non-empty remote must refuse; exit %d\n%s\n%s", r.ExitCode, r.Stdout, r.Stderr)
		}
	}
	if lcPresent(w.A.quantum) {
		t.Fatal("a refused init left its vault behind")
	}
	if out, _ := exec.Command("git", "--git-dir", w.qOrigin, "rev-parse", "--verify", "-q", "main").Output(); len(out) != 0 {
		t.Fatal("a refused init published to origin")
	}
}

// 2. Undo in the sheet's order with H already bound (by clone --bind), then
// the redo. H's pulls between the operator's steps are its ordinary syncs,
// not sheet steps: they are what lets H see each revert.
func TestIntegrationLifecycleUndoThenRedo(t *testing.T) {
	w := newLCWorld(t, nil)
	w.H.sheetB(t)
	copyOut, deleteOut := w.adminMove(t)
	w.H.sheet5(t).Must(t) // 5
	w.sheet6(t, w.H)      // 6

	hPull := func() {
		w.H.vp(t, "", "vault", "pull").Must(t)
		w.H.vp(t, "", "vault", "pull", "--vault", w.H.quantum).Must(t)
	}
	w.sheetUndo(t, copyOut, deleteOut, func() {
		assertFootprint(t, "after the copy revert", w.qOrigin, map[string]string{})
		assertFootprint(t, "after the copy revert", w.qMirror, map[string]string{})
		hPull()
		if r := w.H.harvest(t, "qms", "during-undo"); r.ExitCode == 0 || !strings.Contains(r.Stderr, "stale project binding") {
			t.Fatalf("after the copy revert H's write must refuse as stale; exit %d\n%s", r.ExitCode, r.Stderr)
		}
	})
	assertFootprint(t, "after both reverts", w.pBare, w.original)
	hPull()
	if r := w.H.harvest(t, "qms", "during-undo-2"); r.ExitCode == 0 || !strings.Contains(r.Stderr, "stale project binding") {
		t.Fatalf("after both reverts H's write must still refuse as stale; exit %d\n%s", r.ExitCode, r.Stderr)
	}

	// The redo: "the admin then runs vp vault pull in the personal vault and
	// repeats steps 2-3". H has done 5-6 already.
	w.A.vp(t, "", "vault", "pull").Must(t)
	w.sheet2(t)
	w.sheet3(t)
	w.assertMoved(t, "after the redo", w.original)
	hPull()
	w.H.harvest(t, "qms", "after-redo").Must(t)
	if !bareHas(t, w.qOrigin, "Projects/qms", "after-redo") {
		t.Error("after the redo H's write did not reach Q")
	}
}

// 3. F3: H's unpushed work under qms survives the undo and redo, and H then
// finishes the procedure. H is the host whose B3/B4 did not happen: its work
// stays unpushed.
func TestIntegrationLifecycleStrandedWorkSurvives(t *testing.T) {
	w := newLCWorld(t, nil)
	w.H.harvest(t, "qms", "stranded", "--no-push").Must(t)
	strandedFP := footprintListing(t, w.H.personal, "HEAD")
	copyOut, deleteOut := w.adminMove(t)

	if pull := w.H.sheet5(t); pull.ExitCode == 0 || !strings.Contains(pull.Stdout+pull.Stderr, "refusing to bring") { // 5 refuses
		t.Fatalf("H's pull must refuse under F3; exit %d\n%s\n%s", pull.ExitCode, pull.Stdout, pull.Stderr)
	}

	// "If step 5 refuses": the undo; the stranded host pulls and syncs; the
	// admin pulls and repeats steps 2-3; the host then does 5-6.
	w.sheetUndo(t, copyOut, deleteOut, func() {
		assertFootprint(t, "after the copy revert", w.qOrigin, map[string]string{})
		assertFootprint(t, "after the copy revert", w.qMirror, map[string]string{})
	})
	assertFootprint(t, "after both reverts", w.pBare, w.original)
	w.H.vp(t, "", "vault", "pull").Must(t)
	w.H.vp(t, "", "vault", "sync").Must(t)
	w.A.vp(t, "", "vault", "pull").Must(t)
	w.sheet2(t)
	w.sheet3(t)

	if len(strandedFP) <= len(w.original) {
		t.Fatalf("fixture: the stranded commit added nothing under the footprint")
	}
	w.assertMoved(t, "after the redo (the original plus exactly the stranded note)", strandedFP)

	w.H.sheet5(t).Must(t) // 5
	w.sheet6(t, w.H)      // 6
	w.H.harvest(t, "qms", "after-recovery").Must(t)
	if !bareHas(t, w.qOrigin, "Projects/qms", "after-recovery") {
		t.Fatal("the recovered host's write did not reach Q")
	}
}

// 4. The e4 race: another host pushes to P between the delete's dry run and
// its real run. The real run refuses before writing anything; the admin pulls
// and the same printed line succeeds (the digest binds the footprint).
func TestIntegrationLifecycleRaceBetweenDryRunAndRun(t *testing.T) {
	w := newLCWorld(t, nil)
	w.H.sheetB(t)
	w.A.sheetB(t)
	w.sheet1(t)
	w.sheet2(t)
	dry := w.A.vp(t, "", append(append([]string{"vault", "project", "delete"}, lcProjects...), "--moved-to", fileURL(w.qOrigin), "--dry-run")...).Must(t)
	m := runLineRE.FindStringSubmatch(dry.Stdout)
	if m == nil {
		t.Fatalf("no real-run line:\n%s", dry.Stdout)
	}
	w.H.harvest(t, "stays", "unrelated").Must(t)
	headBefore := lcGit(t, w.A.personal, "rev-parse", "HEAD")

	r := w.A.sh(t, m[1])
	if r.ExitCode == 0 || !strings.Contains(r.Stderr, "pull or push first") {
		t.Fatalf("want the before-commit refusal; exit %d\n%s", r.ExitCode, r.Stderr)
	}
	if lcGit(t, w.A.personal, "rev-parse", "HEAD") != headBefore || !bareHas(t, w.pBare, "Projects/qms", "resume.md") {
		t.Fatal("a refused delete moved HEAD or reached P's remote")
	}
	w.A.vp(t, "", "vault", "pull").Must(t)
	w.A.sh(t, m[1]).Must(t)
	assertFootprint(t, "after the redone delete", w.pBare, map[string]string{})
}

// 6. vp check reports no new finding in P or Q after the move (harness Q6).
// P carries a retired Projects/qms/config.toml, which `vp check` reports as
// info: the copy must not carry it into Q.
func TestIntegrationLifecycleNoNewCheckFinding(t *testing.T) {
	w := newLCWorld(t, map[string]string{"Projects/qms/config.toml": "[palace.scoring]\n"})
	beforeP := lcCheckFindings(t, w.A, w.A.personal)
	if _, ok := beforeP["Vault project config"]; !ok {
		t.Fatalf("fixture: P must report the Vault project config row before the move, got %v", beforeP)
	}
	w.A.sheetB(t)
	w.sheet1(t)
	beforeQ := lcCheckFindings(t, w.A, w.A.quantum)
	w.sheet2(t)
	w.sheet3(t)
	w.sheet4(t)
	for vault, before := range map[string]map[string]string{w.A.personal: beforeP, w.A.quantum: beforeQ} {
		for row, detail := range lcCheckFindings(t, w.A, vault) {
			if _, ok := before[row]; !ok {
				t.Errorf("vp check in %s reports a new row %q: %s", vault, row, detail)
			}
		}
	}
}

// lcCheckFindings runs `vp check --json` against vault, through a config of
// its own (the host's config is never rewritten), and returns the rows that
// report something (fail or info): name to status and detail.
func lcCheckFindings(t *testing.T, h *lcHost, vault string) map[string]string {
	t.Helper()
	xdg := filepath.Join(t.TempDir(), "config")
	lcWrite(t, filepath.Join(xdg, "vibe-palace", "config.toml"), "vault_path = \""+vault+"\"\n")
	r := testinfra.RunCLI(t, h.env("XDG_CONFIG_HOME="+xdg), h.home, nil, "check", "--json")
	var rep struct {
		Checks []struct {
			Name, Status, Detail string
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(r.Stdout), &rep); err != nil || len(rep.Checks) == 0 {
		t.Fatalf("vp check --json: %v\n%s\n%s", err, r.Stdout, r.Stderr)
	}
	out := map[string]string{}
	for _, c := range rep.Checks {
		if c.Status == "fail" || c.Status == "info" {
			out[c.Name] = c.Status + ": " + c.Detail
		}
	}
	return out
}
