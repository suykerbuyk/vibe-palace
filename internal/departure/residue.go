// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package departure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/gitenv"
)

// MachineLocalDirNames are the directory names whose subtrees are host-local
// state: a .local component (palace/.local, palace/<slug>/.local, a legacy
// embed cache) or .vp-locks (vaultlock's sidecar directory). This is THE
// definition; storage.MachineLocalDirNames is an alias of it, so the project
// inventory and the residue test below cannot disagree. A caller must not
// modify it.
var MachineLocalDirNames = map[string]bool{".local": true, ".vp-locks": true}

// residueGit, residueTimeout and residueNow are TEST SEAMS: the git binary the
// residue probe runs, how long both of its calls may take together, and the
// clock the warn-once memo reads.
var (
	residueGit     = "git"
	residueTimeout = 5 * time.Second
	residueNow     = time.Now
)

// residueWarnTTL is how long one undecidable warning stands for its slug and
// cause. It is shorter than the health window bootstrap summarizes (24 h,
// healthWindowHours in internal/tools), so a long-lived server keeps the
// alert alive for as long as the slug stays undecidable instead of warning
// once at startup and going quiet.
const residueWarnTTL = 12 * time.Hour

// residueVerdict is what the probe could establish about Projects/<slug>/.
type residueVerdict int

const (
	// residueContent: git would carry something under the tree — it is live.
	residueContent residueVerdict = iota
	// residueOnly: git would carry nothing; what is left is ignored or
	// machine-local residue a pull could not remove.
	residueOnly
	// residueNoRepo: the vault is not its own git repository (no git at all,
	// or nested inside another one). A permanent, legitimate configuration:
	// the probe has no answer and says so silently.
	residueNoRepo
	// residueFault: git could not be asked or its answer cannot be trusted.
	residueFault
)

// OnlyResidue reports whether Projects/<slug>/, which the caller has found
// PRESENT, holds nothing git would carry: no tracked file and no untracked,
// non-ignored file, once machine-local paths are set aside.
//
// 🔴 WHY THIS EXISTS. A pull deletes only tracked files. git leaves ignored
// ones (*.bak, *.new, anything the vault's .gitignore, a nested one,
// info/exclude or core.excludesFile names) and machine-local ones, so the
// directory of a departed project survives on every host that had any. Read
// as "present", that residue used to switch off every departure refusal, and
// the next write re-created the project in tracked history.
//
// It asks git rather than a hand-written ignore matcher, because git is the
// thing that decided what the pull left: this is the pull guard's own rule
// (departure_guard.go asks git what this host would carry).
//
// It no longer decides WHETHER a slug is departed — the record alone does
// (Find, departedpath.RecordExists). It answers what is left under a departed
// tree, for the delete's postcheck and for reports.
//
// FAIL-OPEN: every answer other than a clean, empty listing is false ("not
// only residue", so the slug stays live), exactly as before this probe
// existed. A vault that is not its own repository is silent; a runtime fault
// (git missing, an error, a timeout, any stderr, output of the wrong shape)
// logs one warning per slug and cause per residueWarnTTL.
func OnlyResidue(vaultRoot, slug string) bool {
	v, cause := probeResidue(vaultRoot, "Projects/"+slug+"/")
	switch v {
	case residueOnly:
		return true
	case residueFault:
		warnResidueUndecidable(slug, cause)
	}
	return false
}

// TreeHoldsContent applies OnlyResidue's rule to any tree: whether git would
// carry something under treeRel/ in vaultRoot (a tracked file, or an
// untracked file that is not ignored, machine-local paths set aside). An
// absent, empty or residue-only tree holds nothing. decided is false when git
// cannot answer (the vault is not its own repository, or a fault); a fault
// logs one warning per tree and cause per residueWarnTTL. The caller chooses
// what an undecided answer means.
func TreeHoldsContent(vaultRoot, treeRel string) (holds, decided bool) {
	v, cause := probeResidue(vaultRoot, strings.TrimSuffix(treeRel, "/")+"/")
	switch v {
	case residueContent:
		return true, true
	case residueOnly:
		return false, true
	case residueFault:
		warnResidueUndecidable(treeRel, cause)
	}
	return false, false
}

// probeResidue runs the two git calls behind OnlyResidue and TreeHoldsContent
// for the tree prefix (a vault-relative path ending in "/"), and classifies
// the answer. cause is set for residueFault only.
func probeResidue(vaultRoot, prefix string) (residueVerdict, string) {
	ctx, cancel := context.WithTimeout(context.Background(), residueTimeout)
	defer cancel()

	// 🔴 ONLY THE VAULT'S OWN REPOSITORY ANSWERS. `git -C <vault>` walks up to
	// an enclosing repository when the vault is not its own top level, and that
	// repository's index says nothing about the vault.
	top, errOut, err := residueGitRun(ctx, vaultRoot, "rev-parse", "--show-toplevel")
	switch {
	case ctx.Err() != nil:
		return residueFault, "git rev-parse timed out"
	case err != nil:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.Contains(string(errOut), "not a git repository") {
			return residueNoRepo, ""
		}
		return residueFault, "git rev-parse: " + faultDetail(err, errOut)
	case len(bytes.TrimSpace(errOut)) > 0:
		return residueFault, "git rev-parse wrote to stderr: " + firstLine(errOut)
	}
	if !SameResolvedDir(strings.TrimSpace(string(top)), vaultRoot) {
		return residueNoRepo, ""
	}

	out, errOut, err := residueGitRun(ctx, vaultRoot,
		"-c", "core.quotepath=off", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", prefix)
	switch {
	case ctx.Err() != nil:
		return residueFault, "git ls-files timed out"
	case err != nil:
		return residueFault, "git ls-files: " + faultDetail(err, errOut)
	case len(errOut) > 0:
		// Exit 0 with stderr is not an answer. Measured: an unreadable
		// subdirectory prints "warning: could not open directory" and lists
		// NOTHING under it, and a broken core.fsmonitor prints "fatal:" and
		// still exits 0. Either would read real content as residue.
		return residueFault, "git ls-files wrote to stderr: " + firstLine(errOut)
	}

	if len(out) == 0 {
		return residueOnly, ""
	}
	if out[len(out)-1] != 0 {
		return residueFault, "git ls-files output is not NUL-terminated"
	}
	content := false
	for f := range bytes.SplitSeq(out[:len(out)-1], []byte{0}) {
		p := string(f)
		// -z output is NUL-separated and unquoted, so a newline inside a field
		// is a legitimate filename, not a shape fault.
		if p == "" || !strings.HasPrefix(p, prefix) {
			return residueFault, fmt.Sprintf("git ls-files listed %q, not a path under %s", p, prefix)
		}
		if !machineLocalPath(p) {
			content = true
		}
	}
	if content {
		return residueContent, ""
	}
	return residueOnly, ""
}

// machineLocalPath reports whether any component of a slash path is a
// machine-local directory name.
func machineLocalPath(p string) bool {
	for comp := range strings.SplitSeq(p, "/") {
		if MachineLocalDirNames[comp] {
			return true
		}
	}
	return false
}

// residueGitRun runs one read-only git command in dir under ctx and returns
// stdout and stderr separately.
func residueGitRun(ctx context.Context, dir string, args ...string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, residueGit, append([]string{"-C", dir}, args...)...)
	cmd.Env = residueGitEnv()
	cmd.WaitDelay = time.Second
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	return o.Bytes(), e.Bytes(), err
}

// residueGitEnv is the probe's git environment. gitenv.SafeGitEnv removes the
// repo-location variables and passes tracing through, so tracing is silenced
// here: stderr is a verdict signal for this probe, and a user tracing git
// would otherwise make every residue-only departed slug undecidable forever.
// Stripping GIT_TRACE* is not enough on its own — trace2 targets can also come
// from git CONFIG (trace2.normalTarget/perfTarget/eventTarget), which `-c`
// does not override — so the three trace2 variables are then set to 0.
func residueGitEnv() []string {
	base := gitenv.SafeGitEnv()
	env := make([]string, 0, len(base)+6)
	for _, kv := range base {
		if strings.HasPrefix(kv, "GIT_TRACE") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"GIT_TRACE2=0", "GIT_TRACE2_PERF=0", "GIT_TRACE2_EVENT=0",
		// probeResidue recognises a non-git vault by git's ENGLISH "not a git
		// repository"; a translated message would read as a runtime fault.
		// LC_ALL=C also makes gettext ignore LANGUAGE. Appended last, so it
		// wins over an inherited LC_ALL (os/exec keeps the last duplicate).
		// Same reason as storage's listing probes (projects.go).
		"LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
}

// faultDetail renders a failed git call for a warning: the exit status and the
// first line of what it said.
func faultDetail(err error, stderr []byte) string {
	if line := firstLine(stderr); line != "" {
		return err.Error() + ": " + line
	}
	return err.Error()
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// residueWarned memoizes warnResidueUndecidable by slug and cause: the time
// each warning was last logged.
var residueWarned sync.Map

// warnResidueUndecidable logs that a departed slug's residue could not be
// judged, at most once per slug and cause per residueWarnTTL. The message's
// text before its first colon is the category vplog.Summarize counts and
// bootstrap's health alert prints. It carries no fault=caller: nothing the
// caller did is wrong; this host's git is.
func warnResidueUndecidable(slug, cause string) {
	key := slug + "\x00" + cause
	now := residueNow()
	if last, ok := residueWarned.Load(key); ok && now.Sub(last.(time.Time)) < residueWarnTTL {
		return
	}
	residueWarned.Store(key, now)
	slog.Warn("departure residue undecidable: cannot tell whether Projects/"+slug+
		"/ holds only ignored residue, so the departed project is treated as still here",
		"project", slug, "cause", cause)
}

// SameResolvedDir reports whether a and b are the same directory once both
// are symlink-resolved (git reports a resolved top level; a vault path may
// not be). Unresolvable means "not the same", so a probe built on it fails
// open.
func SameResolvedDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && filepath.Clean(ra) == filepath.Clean(rb)
}
