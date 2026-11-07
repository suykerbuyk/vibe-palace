// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package check

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// codexOrphansRow is this check's row name, spelled once.
const codexOrphansRow = "Codex orphans"

// Codex runs its SessionEnd hook only on an orderly shutdown. A session killed
// by SIGTERM, SIGHUP or SIGKILL leaves its rollout on disk and archives
// nothing, and NOTHING reports the loss: no error, no warning, no vp.log line.
// The vault simply holds one fewer session than the day contained.
//
// This row is the reporter that did not exist. It compares the rollouts under
// CODEX_HOME against the session ids the vault has archived, and names the
// difference.
//
// 🔴 ITS WHOLE DIFFICULTY IS THE FALSE POSITIVE. Most rollouts with no archive
// were never meant to have one: the hook DECLINES a session whose cwd carries
// no project marker, whose marker is unnamed, whose vault binding is refused,
// whose project departed, or whose project the vault has not initialised. A
// sweep that reported those as losses would repeat the exact error this check
// was filed over — a correctly declined capture recorded as a lost one — and
// would be ignored inside a week. So the predicate below is the HOOK'S OWN
// predicate, in the hook's own order, and every decline gets its own labelled
// line.

// rolloutHeaderMaxBytes bounds one line of a rollout's header.
//
// Deliberately 1 MiB, NOT the 16 MiB of archive.inspectCodexRollout or the
// 64 MiB of archive.codexSessionStart: this check reads the FIRST RECORD of
// every rollout on the host, where those read one file a caller already chose.
// Measured on the live corpus, the longest session_meta line is 19,321 bytes
// against rollouts up to 17 MB, so a header past 1 MiB is a malformed file and
// is reported as unreadable rather than buffered.
const rolloutHeaderMaxBytes = 1 << 20

// rolloutHeaderMaxLines bounds how far the scan looks for session_meta.
//
// Every rollout in the live corpus opens with one (measured: 44 of 44), and the
// deepest the scan ever needs to go is 2 — one rollout is a SUBAGENT thread
// whose first record is its own session_meta and whose second is the parent's.
// The budget exists so a rollout with NO session_meta costs 8 lines rather than
// 17 MB.
const rolloutHeaderMaxLines = 8

// errNoCodexSessions reports an absent <CODEX_HOME>/sessions directory, which
// is a Skip (this host does not run Codex) and never a finding.
var errNoCodexSessions = errors.New("check: no Codex sessions directory")

// errPartialProc reports a /proc scan that could read the root but not every
// process, so "no process holds this rollout" is unproven for some of them.
var errPartialProc = errors.New("some processes could not be inspected")

// CheckCodexOrphans reports Codex rollouts on this host that ended without
// reaching the vault. It resolves the three roots it reads and delegates.
//
// Like CheckStaleMCP it inspects THIS MACHINE (CODEX_HOME, /proc) as well as
// the vault: the producer registry hands a check only the vault root, and the
// same $HOME escape hatch host-surfaces uses applies here.
func CheckCodexOrphans(vaultRoot string) []Result {
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return []Result{{Name: codexOrphansRow, Status: Info, Summary: "home directory unavailable: " + err.Error()}}
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return checkCodexOrphansAt(codexHome, vaultRoot, "/proc")
}

// rolloutHeader is what one rollout's first session_meta record tells us.
type rolloutHeader struct {
	Path     string
	Key      string // the session id this rollout is joined on
	KeyField string // "id" or "session_id" -- which field supplied Key
	CWD      string
	Subagent bool
}

// codexSessionMeta is the part of a rollout's session_meta record this check
// reads. Everything else in the payload (base_instructions alone is ~18 KB) is
// decoded and discarded, which is why the scan stops at the first record.
type codexSessionMeta struct {
	Type    string `json:"type"`
	Payload struct {
		ID             string `json:"id"`
		SessionID      string `json:"session_id"`
		CWD            string `json:"cwd"`
		ThreadSource   string `json:"thread_source"`
		ParentThreadID string `json:"parent_thread_id"`
	} `json:"payload"`
}

// readRolloutHeader returns the join key, cwd and thread kind of one rollout.
//
// 🔴 THE KEY IS payload.id, FALLING BACK TO payload.session_id, and the field
// that supplied it is recorded. Both halves are load-bearing and neither may be
// dropped:
//
//   - session_id ALONE collapses a subagent rollout into its parent. Measured
//     on the live corpus's one subagent rollout, its first session_meta carries
//     id = its own thread and session_id = the PARENT thread. The parent is
//     archived, so keying on session_id would report the subagent's rollout as
//     captured — a false NEGATIVE, the worst direction for this check.
//   - id ALONE is EMPTY against every Codex fixture in this repository
//     (archive.codexOneRecord, archive.codexRolloutFixture both carry
//     session_id and no id), so a test built on those shapes would pass
//     vacuously against an id-only key.
//
// A rollout with neither field, no session_meta in its first
// rolloutHeaderMaxLines lines, or an unreadable file returns an error and is
// reported as unreadable — never silently skipped, because a skipped rollout is
// a loss this check cannot see.
func readRolloutHeader(path string) (rolloutHeader, error) {
	f, err := os.Open(path)
	if err != nil {
		return rolloutHeader{Path: path}, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), rolloutHeaderMaxBytes)
	for i := 0; i < rolloutHeaderMaxLines && sc.Scan(); i++ {
		line := sc.Bytes()
		var rec codexSessionMeta
		if json.Unmarshal(line, &rec) != nil || rec.Type != "session_meta" {
			continue
		}
		h := rolloutHeader{
			Path:     path,
			CWD:      rec.Payload.CWD,
			Subagent: rec.Payload.ThreadSource == "subagent" || rec.Payload.ParentThreadID != "",
		}
		switch {
		case rec.Payload.ID != "":
			h.Key, h.KeyField = rec.Payload.ID, "id"
		case rec.Payload.SessionID != "":
			h.Key, h.KeyField = rec.Payload.SessionID, "session_id"
		default:
			return rolloutHeader{Path: path}, fmt.Errorf("session_meta carries neither id nor session_id")
		}
		return h, nil
	}
	if err := sc.Err(); err != nil {
		return rolloutHeader{Path: path}, err
	}
	return rolloutHeader{Path: path}, fmt.Errorf("no session_meta record in the first %d lines", rolloutHeaderMaxLines)
}

// listCodexRollouts returns every rollout file under <codexHome>/sessions.
func listCodexRollouts(codexHome string) ([]string, error) {
	root := filepath.Join(codexHome, "sessions")
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, errNoCodexSessions
		}
		return nil, err
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// archivedSessionIDs returns every session id the vault has archived, how many
// manifests could NOT be parsed, and any per-project blindness.
//
// 🔴 THE ENUMERATION IS THE UNION OF LIVE AND DEPARTED SLUGS.
// storage.Vault.ListAllProjects DELETES departed slugs from its result
// "whatever its Projects/ directory still holds" (internal/storage/projects.go)
// -- and a departed slug whose transcripts directory survives is exactly the
// class the departed arm of the predicate is about. Enumerating live projects
// alone would drop those ids and turn their rollouts into reported losses.
//
// 🔴 AN UNREADABLE MANIFEST IS UNKNOWN, NEVER ABSENT. archive.ListEntries
// swallows a file-level ReadManifest error with a continue (one bad file must
// not poison a listing), which is right for a round-trip audit and WRONG for an
// id set: a dropped id is a rollout reported as lost. So the directory is
// counted independently and the difference is surfaced; the caller caps the row
// at Info when it is non-zero, because an id set that cannot state its own
// completeness must not drive a gate.
func archivedSessionIDs(vaultRoot string) (ids map[string]bool, unparsed int, blind []string) {
	ids = map[string]bool{}
	slugs := map[string]bool{}
	if presences, err := storage.NewVault(vaultRoot).ListAllProjects(); err == nil {
		for _, p := range presences {
			slugs[p.Slug] = true
		}
	} else {
		blind = append(blind, fmt.Sprintf("cannot enumerate projects: %v", err))
	}
	for _, rec := range departure.List(vaultRoot) {
		slugs[rec.Slug] = true
	}

	for _, slug := range sortedSlugs(slugs) {
		entries, err := archive.ListEntries(vaultRoot, slug)
		if err != nil {
			blind = append(blind, fmt.Sprintf("%s: cannot list transcripts: %v", slug, err))
			continue
		}
		// 🔴 A MANIFEST THAT PARSES BUT CARRIES NO session_id IS UNKNOWN, NOT
		// ABSENT. archive.ReadManifest validates schema_version and nothing
		// else, so a manifest with an empty or missing session_id is a perfectly
		// good Entry that contributes NO id to this set. Counted as parsed it
		// would raise no unknown line, and its rollout would be reported as a
		// confident orphan — measured, a probe produced exactly that false
		// orphan. It is counted here with the files that would not parse at all.
		idless := 0
		for _, e := range entries {
			if e.Manifest == nil || e.Manifest.SessionID == "" {
				idless++
				continue
			}
			ids[e.Manifest.SessionID] = true
		}
		unparsed += idless
		// The independent count. ListEntries returns only the manifests it
		// PARSED; this counts the manifests that are THERE.
		if n, err := countManifests(vaultRoot, slug); err == nil && n > len(entries) {
			unparsed += n - len(entries)
		}
	}
	return ids, unparsed, blind
}

// countManifests counts the *.manifest.json files in one project's transcripts
// directory, by the same name rule archive.ListEntries applies, so the two
// numbers are comparable. An absent directory is zero, not an error.
func countManifests(vaultRoot, slug string) (int, error) {
	dirents, err := os.ReadDir(filepath.Join(vaultRoot, "Projects", slug, "transcripts"))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, de := range dirents {
		if strings.HasSuffix(de.Name(), ".manifest.json") {
			n++
		}
	}
	return n, nil
}

// openRollouts returns the rollout paths a live process currently holds open.
//
// 🔴 AN OPEN DESCRIPTOR PROVES A SESSION IS LIVE. IT DOES NOT PROVE THE
// CONVERSE. Codex may close and reopen a rollout, so "not held" means "no
// process holds it right now" -- which this check treats as ELIGIBLE to be an
// orphan, never as proof the session ended. The row's wording says so.
//
// Same mechanism and same degradation as listMCPProcessesFrom: procRoot is a
// parameter so a test drives this scan against a fixture directory rather than
// replacing it with a stub that proves only the formatter.
func openRollouts(procRoot, sessionsDir string) (map[string]bool, error) {
	ents, err := os.ReadDir(procRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNoProc
		}
		return nil, err
	}
	held := map[string]bool{}
	complete := true
	me := os.Geteuid()
	for _, e := range ents {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		// 🔴 ONLY THIS USER'S PROCESSES ARE INSPECTED, AND ONLY THEY COUNT
		// TOWARDS INCOMPLETENESS. On an ordinary Linux host /proc/<pid>/fd is
		// unreadable for every OTHER user's process — measured here: 220 of
		// them against 144 of our own. Counting those as "liveness incomplete"
		// is not caution, it is a permanent unknown that would stop this row
		// ever reaching a verdict. Codex runs as this user, so a rollout under
		// this user's home is held by this user's process or by nobody.
		if st, serr := os.Stat(filepath.Join(procRoot, e.Name())); serr != nil || int(fileUID(st)) != me {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(procRoot, e.Name(), "fd"))
		if err != nil {
			// 🔴 A PROCESS THAT EXITED IS NOT THE SAME AS A PROCESS WE MAY NOT
			// LOOK AT. os.IsNotExist is the race this scan expects and ignores;
			// anything else (EACCES on another user's process, a /proc that is
			// mounted hidepid) means the liveness answer is INCOMPLETE, and an
			// incomplete answer must not be allowed to call a live session a
			// loss. Measured: an unreadable fd directory used to produce a Fail
			// with no caveat at all.
			if !os.IsNotExist(err) {
				complete = false
			}
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(procRoot, e.Name(), "fd", fd.Name()))
			if err != nil {
				if !os.IsNotExist(err) {
					complete = false
				}
				continue
			}
			if underSessionsDir(target, sessionsDir) {
				held[canonicalPath(target)] = true
			}
		}
	}
	if !complete {
		return held, errPartialProc
	}
	return held, nil
}

// underSessionsDir reports whether an fd target names a file inside the Codex
// sessions tree, comparing CANONICAL paths.
//
// 🔴 A LITERAL PREFIX TEST IS WRONG HERE. CODEX_HOME may be (or sit under) a
// symlink: the walk then enumerates rollouts as …/alias/sessions/… while /proc
// reports the fd as …/real/sessions/…, the prefix does not match, and every
// live session is eligible to be reported as a loss. Measured on a symlinked
// home before this was canonicalised.
func underSessionsDir(target, sessionsDir string) bool {
	return strings.HasPrefix(canonicalPath(target), canonicalPath(sessionsDir)+string(os.PathSeparator))
}

// canonicalPath returns a path in the one form both sides of the liveness
// lookup can be compared in: the kernel's " (deleted)" marker removed, symlinks
// resolved when they can be, and a cleaned absolute path when they cannot.
//
// 🔴 THE SUFFIX IS STRIPPED FIRST, BEFORE EvalSymlinks AND BEFORE THE FALLBACK.
// When a file is unlinked while still open, Linux renders /proc/<pid>/fd/N as
// "<path> (deleted)". EvalSymlinks then fails on that string, the fallback
// cleaned it WITH the marker still attached, and the stored form could never
// equal the lookup form — which never carries the marker. A rollout Codex had
// unlinked but was still holding came back as a confident orphan: liveness
// missing a live session and the row calling it a loss, the same direction as
// the incomplete-liveness defect above.
//
// deletedSuffix is stale_mcp.go's constant, reused deliberately. That scanner
// has treated this marker as load-bearing since it was written, and a second
// copy of the same string in the same package is how the two drift apart.
func canonicalPath(p string) string {
	p = strings.TrimSuffix(p, deletedSuffix)
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

// fileUID returns a FileInfo's owning uid, or a sentinel that can never equal a
// real euid when the platform does not expose one.
func fileUID(fi os.FileInfo) uint32 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Uid
	}
	// No ownership available (non-Unix): inspect the process rather than skip
	// every one of them, which would silently disable liveness entirely.
	return uint32(os.Geteuid())
}

// sameVaultRoot reports whether two vault paths name the same directory.
//
// Clean/Abs first, symlinks only if that disagrees -- the shape
// mcphost.sameExecutable uses, and for the same reason: the configured path
// carries a trailing slash ("/home/x/vault/") while the resolver returns it
// cleaned, and a raw string compare would call those two different vaults.
func sameVaultRoot(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if filepath.Clean(absA) == filepath.Clean(absB) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(absA)
	rb, errB := filepath.EvalSymlinks(absB)
	return errA == nil && errB == nil && ra == rb
}

// orphan is one rollout this check reports as unreachable from the vault.
type orphan struct {
	header rolloutHeader
	slug   string
	gone   bool // the recorded cwd no longer exists
}

// orphanLine renders ONE orphan as a single prose line: the session key, the
// project, which payload field supplied the key, and the rollout path.
//
// 🔴 IT IS DELIBERATELY NOT A PASTEABLE COMMAND BLOCK, and that is a ruling, not
// an oversight. check.writeEntry word-wraps any entry line past the row width,
// inserting REAL newlines. A multi-line `vp archive create … \` block with an
// absolute rollout path (~110 characters) therefore reaches the terminal with
// --source separated from its argument and the continuation backslash stranded
// on a line of its own: copied out, it does not run. Measured on this host
// before the ruling. A single-line form breaks the same way, for the same
// reason.
//
// So the row reports the FACTS an operator needs — id, project, key provenance,
// rollout path — as prose, where wrapping is harmless, and names the command
// shape once in codexOrphanRecoveryNote. Teaching writeEntry to leave
// command-shaped entries verbatim is the right general fix and is filed
// separately (check-row-renderer-mangles-command-shaped-entries); it changes a
// primitive every check row uses and does not belong in this unit.
func orphanLine(o orphan) string {
	line := "ORPHAN " + o.header.Key + " — project " + o.slug +
		", key from payload." + o.header.KeyField + ", rollout " + o.header.Path
	if o.gone {
		line += " (its recorded cwd " + o.header.CWD + " no longer exists)"
	}
	return line
}

// codexOrphanRecoveryNote says what to do, in prose, and names no flags.
//
// 🔴 IT CARRIES NO COMMAND TEMPLATE AT ALL, deliberately. An earlier version
// spelled out `vp archive create --adapter codex-raw --session-id <id> --source
// <rollout> -p <project>`; a reviewer then changed only the sentence around it
// and every test still passed while the RENDERED row split that command across
// lines. A note that contains no pasteable command cannot be mangled into a
// broken one, and TestCodexOrphanRowPrintsNoCommandTheRendererWouldMangle
// asserts that against the real rendered lines.
//
// The working-directory sentence is load-bearing and is the reason this note
// exists at all: the archiver takes its vault from os.Getwd() via
// openProjectVault, never from a flag, so the same invocation run from the
// wrong checkout writes a correct archive into the WRONG vault — which the next
// run of this check would then read as captured.
const codexOrphanRecoveryNote = "Each ORPHAN line above carries the three inputs needed to re-archive that " +
	"session: its id, its project, and its rollout path. Re-archive with the codex-raw adapter, " +
	"and run it from a checkout that resolves to this vault — the archiver takes the vault from " +
	"the working directory and not from a flag, so the same command run elsewhere files the " +
	"transcript in the wrong vault. For the same fields unwrapped, run this check with JSON output: " +
	"vp check --check codex-orphans --json"

func sortedSlugs(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// checkCodexOrphansAt is the whole check against three explicit roots, so every
// test drives the real walker, reader, resolvers and join rather than a stub.
//
// 🔴 THE PREDICATE BELOW IS THE HOOK'S PREDICATE, IN THE HOOK'S ORDER, and the
// order is load-bearing rather than cosmetic: steps 4 and 5 need the vault root
// and the marker's project name, which only exist after steps 2 and 3.
// Evaluation stops at the first step that declines, and EVERY decline gets its
// own labelled line instead of being folded into the orphan count.
//
//  1. project.DetectSignal                     codexhook.HandleSessionEnd
//  2. project.LocateMarker + a usable name     codexhook.HandleSessionEnd
//  3. storage.ResolveVaultPath == this vault   codexhook.HandleSessionEnd
//  4. !project.Departed                        hook.GateCapture, SkipDeparted
//  5. ClassifyProjectDir().Initialised()       hook.GateCapture, SkipUninitialised
//  6. the session-id join
//  7. liveness
//
// 🔴 STEP 3'S ERROR ARM IS ITS OWN BUCKET. storage.ResolveVaultPath returns
// ("", "", err) when a binding is REFUSED, so a bare "the path differs from the
// vault we audit" test reads a refusal as a MATCH and drops the rollout into
// the orphan bucket -- the opposite of the hook, which captures nothing and
// returns the error. An earlier draft of this check did exactly that.
func checkCodexOrphansAt(codexHome, vaultRoot, procRoot string) []Result {
	r := Result{Name: codexOrphansRow}
	if vaultRoot == "" {
		return []Result{{Name: codexOrphansRow, Status: Skip, Summary: "no vault configured"}}
	}
	rollouts, err := listCodexRollouts(codexHome)
	if err != nil {
		if errors.Is(err, errNoCodexSessions) {
			return []Result{{Name: codexOrphansRow, Status: Skip, Summary: "no Codex sessions directory on this host"}}
		}
		return []Result{{Name: codexOrphansRow, Status: Info, Summary: fmt.Sprintf("scan: %v", err)}}
	}

	archived, unparsed, blind := archivedSessionIDs(vaultRoot)

	held, herr := openRollouts(procRoot, filepath.Join(codexHome, "sessions"))
	livenessBlind := herr != nil

	var orphans []orphan
	counts := map[string]int{}
	var unreadable []string
	var unknownCWD []string

	for _, path := range rollouts {
		h, err := readRolloutHeader(path)
		if err != nil {
			unreadable = append(unreadable, fmt.Sprintf("  %s: %v", path, err))
			continue
		}

		// Step 0 -- a header with no cwd is UNKNOWN, and this arm must come
		// before every resolver below.
		//
		// 🔴 AN ABSENT VALUE MUST NOT REACH A RESOLVER. project.DetectSignal
		// hands its argument to filepath.Abs, which resolves "" against the
		// CHECKER's process working directory — so a cwd-less rollout used to be
		// attributed to whatever project the operator happened to be standing in
		// and reported as its orphan. The hook refuses an empty cwd outright
		// (internal/codexhook/session_end.go), and this is the same defect class
		// as the resolver-error arm in step 3: nothing absent may come back from
		// a resolver as a confident answer.
		if strings.TrimSpace(h.CWD) == "" {
			unknownCWD = append(unknownCWD, "  "+h.Path+": session_meta carries no cwd, so this rollout cannot be attributed to a project")
			continue
		}
		// Step 1 -- the opt-in marker gate.
		if project.DetectSignal(h.CWD) != project.SignalVibeConfig {
			counts["no project marker — correctly declined"]++
			continue
		}
		// Step 2 -- the marker must be readable and usable.
		marker, merr := project.LocateMarker(h.CWD)
		if merr != nil || marker.Path == "" || marker.Name == "" || marker.NameErr != nil {
			counts["marker unreadable, unnamed or invalid — correctly declined"]++
			continue
		}
		// Step 3 -- resolution, with the refusal as its own verdict.
		bound, _, rerr := storage.ResolveVaultPath(h.CWD)
		if rerr != nil {
			counts["vault binding refused — correctly declined"]++
			continue
		}
		if !sameVaultRoot(bound, vaultRoot) {
			counts["belongs to another vault — out of scope"]++
			continue
		}
		// Step 4 -- the stale-checkout gate.
		if _, gone := project.Departed(vaultRoot, marker.Name); gone {
			counts["project departed this vault — correctly declined"]++
			continue
		}
		// Step 5 -- the uninitialised-project gate.
		if state, serr := storage.ClassifyProjectDir(vaultRoot, marker.Name); serr != nil || !state.Initialised() {
			counts["project not initialised in this vault — correctly declined"]++
			continue
		}
		// A subagent thread is reported on its own line and never counted as a
		// loss: MEASURED 2026-11-06, Codex runs no hook for one. See
		// codexOrphanSubagentNote for the measurement.
		if h.Subagent {
			counts["subagent thread — reported separately, see below"]++
			continue
		}
		// Step 6 -- the join.
		if archived[h.Key] {
			counts["archived"]++
			continue
		}
		// Step 7 -- liveness, last, because a live session is not a loss.
		//
		// 🔴 BOTH SIDES OF THIS LOOKUP ARE CANONICAL. openRollouts stores the
		// resolved target /proc reports; the walk enumerates whatever path
		// CODEX_HOME spells. Canonicalising only one side is the symlinked-home
		// bug with an extra step: the map is populated correctly and never hit.
		if held[canonicalPath(h.Path)] {
			counts["session still open (a process holds the rollout)"]++
			continue
		}
		_, statErr := os.Stat(h.CWD)
		orphans = append(orphans, orphan{header: h, slug: marker.Name, gone: statErr != nil})
	}

	r.Summary = fmt.Sprintf("%d Codex rollout(s) scanned; %d with no archive in this vault", len(rollouts), len(orphans))
	for _, k := range sortedCountKeys(counts) {
		r.Details = append(r.Details, fmt.Sprintf("  %s: %d", k, counts[k]))
	}
	for _, o := range orphans {
		r.Details = append(r.Details, orphanLine(o))
	}
	if len(orphans) > 0 {
		r.Details = append(r.Details, "vault: "+vaultRoot)
		r.Details = append(r.Details, codexOrphanRecoveryNote)
	}
	r.Details = append(r.Details, unreadable...)
	r.Details = append(r.Details, unknownCWD...)

	// 🔴 UNKNOWNS CAP THE ROW AT Info. A row that cannot qualify its own data
	// must never fail a build: liveness we could not read would report a LIVE
	// session as a loss, and a manifest we could not parse drops its id and
	// turns its rollout into one. Both still LIST what they found -- skipping
	// the row entirely would hide real losses, which is the failure this check
	// exists to end.
	//
	// 🔴 AN INCOMPLETE SCAN IS ALSO AN UNKNOWN. A rollout whose header could not
	// be read, and a rollout carrying no cwd, both mean this run does not know
	// what it did not look at — and a check whose purpose is to stop a silent
	// loss must not return a confident green over a gap. Measured before this
	// cap: one invalid-JSON rollout and no candidates returned Pass, exit 0.
	//
	// 🔴 INCOMPLETE LIVENESS CAPS A CLAIM, NOT A CLEAN SCAN, and the asymmetry
	// is provable rather than a convenience. An fd this scan could not read can
	// only ever make it MISS that a rollout is held — it can never make it
	// believe a dead session is live. So the only error incomplete liveness can
	// produce is a FALSE ORPHAN, and where there are no orphans there is no
	// claim to be wrong about. Capping unconditionally was measured to pin this
	// row at Info forever on an ordinary host (three of this user's own
	// processes are hardened against /proc/<pid>/fd), which would make a check
	// that cannot fail — the defect this project refuses everywhere else. The
	// caveat line is printed either way.
	switch {
	case livenessBlind && len(orphans) > 0:
		r.Status = Info
	case unparsed > 0 || len(blind) > 0:
		r.Status = Info
	case len(unreadable) > 0 || len(unknownCWD) > 0:
		r.Status = Info
		r.Details = append(r.Details, fmt.Sprintf(
			"%d rollout(s) could not be classified (unreadable header, or no cwd) — this scan is incomplete, "+
				"so a loss may be missing from the list above", len(unreadable)+len(unknownCWD)))
	case len(orphans) > 0:
		r.Status = Fail
	default:
		r.Status = Pass
	}
	if livenessBlind {
		r.Details = append(r.Details, "liveness incomplete ("+herr.Error()+") — this can only hide that a listed rollout is still open, never invent one, so it caps a report that names orphans and leaves a clean scan alone")
	}
	if unparsed > 0 {
		r.Details = append(r.Details, fmt.Sprintf(
			"%d manifest(s) in this vault yielded no session id (unparseable, or parsed with an empty session_id) "+
				"— those ids are unknown, so an orphan above may be false", unparsed))
	}
	for _, b := range blind {
		r.Details = append(r.Details, "  blind: "+b)
	}
	if counts["subagent thread — reported separately, see below"] > 0 {
		r.Details = append(r.Details, codexOrphanSubagentNote)
	}
	r.Details = append(r.Details, codexOrphanLivenessNote)
	return []Result{r}
}

// codexOrphanSubagentNote states what the subagent line does and does not mean.
// codexOrphanSubagentNote states what the subagent line means, and names the
// measurement rather than asserting the behaviour.
//
// MEASURED 2026-11-06 in a throwaway CODEX_HOME whose hooks.json ran a
// recording command instead of vp: one session spawned one subagent via the
// collaboration spawn_agent tool, THREE rollouts were written, and the hooks
// fired for exactly TWO session ids -- both user threads. The subagent's
// rollout received no SessionStart and no SessionEnd.
const codexOrphanSubagentNote = "A subagent rollout is a thread Codex spawned inside another session. " +
	"Measured 2026-11-06: Codex runs NO SessionStart and NO SessionEnd for one, so its rollout is never " +
	"archived and its absence is not a loss. It is listed rather than hidden so the scanned count stays honest."

// codexOrphanLivenessNote keeps the row from overclaiming what it observed.
const codexOrphanLivenessNote = "An open file descriptor proves a session is LIVE; nothing proves the converse. " +
	"A rollout no process holds is eligible to be an orphan, not proof that its session ended."

func sortedCountKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
