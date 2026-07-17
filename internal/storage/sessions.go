// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
	"gopkg.in/yaml.v3"
)

// FrictionBreakdown holds the four capped friction sub-scores that sum to the
// composite FrictionScore. It is stored as a pointer on SessionMeta with
// presence semantics: a nil pointer means friction was never broken down (old
// sessions), while a non-nil pointer with all-zero fields means a real,
// measured frictionless session. Each field is the already-capped sub-score
// (each 0-25), matching the composite computation. Total() reproduces the
// composite (sum, capped at 100).
type FrictionBreakdown struct {
	Corrections  int `yaml:"corrections"`
	Retries      int `yaml:"retries"`
	ErrorDensity int `yaml:"error_density"`
	Rework       int `yaml:"rework"`
}

// Total returns the composite friction score: the sum of the four sub-scores,
// capped at 100. It is the authoritative reconstruction of FrictionScore.
func (b FrictionBreakdown) Total() int {
	return min(b.Corrections+b.Retries+b.ErrorDensity+b.Rework, 100)
}

// SessionMeta is the lightweight metadata parsed from YAML frontmatter.
type SessionMeta struct {
	ID      string `yaml:"session_id"`
	Project string `yaml:"project"`
	Date    string `yaml:"date"`

	// SessionKey is the capture-attempt idempotency key, and SessionKeySource
	// records where it came from ("caller" | "minted"). A capture that resolves
	// an existing note by this key UPDATES it in place instead of minting a new
	// one, so a retry of a failed capture cannot leave a duplicate behind.
	//
	// The key identifies the CAPTURE ATTEMPT, not the session: an agent captures
	// once per WORK UNIT and a session holds several, so keying on the session
	// would make each work-unit capture overwrite the last. The server mints a key
	// when the caller supplies none and hands it back, so a retry can push it
	// again — identity is pushed, never derived.
	//
	// These sit high in the struct ON PURPOSE. yaml.Marshal emits fields in
	// declaration order, so keeping them above the free-text Title/Summary keeps
	// them inside the first few hundred bytes of every note — which is what lets
	// the key scan read a bounded HEAD of each file instead of the whole thing.
	SessionKey       string `yaml:"session_key,omitempty"`
	SessionKeySource string `yaml:"session_key_source,omitempty"`

	// ArchiveSessionID is the HOST session identifier this note was captured from
	// — the harness/transcript session, NOT the capture-attempt key above. It is
	// what makes a note findable from the transcript side, and it is recorded
	// UNCONDITIONALLY: at capture time the archive usually does not exist yet
	// (the hook only archives at SessionEnd/PreCompact), so the note that cannot
	// be linked NOW is precisely the one that must still be findable LATER.
	//
	// Its absence was the whole of capture-note-archive-link-never-closes. Capture
	// had ALWAYS received this value (SessionParams.ArchiveSessionID) and used it
	// to resolve the archive — then discarded it. So a note whose archive did not
	// exist yet kept no record of which session produced it, and nothing could ever
	// close the loop: 105 of 417 manifests vault-wide were stranded, and every
	// agent-written note (implementation 0/61) was divorced from its transcript.
	//
	// It is NOT SessionKey and must never be conflated with it. SessionKey keys the
	// capture ATTEMPT and drives UpsertSessionByKey's resolve-or-mint: reusing the
	// host session ID as the key would make the agent's wrap capture resolve the
	// hook's auto-capture note and REWRITE IT IN PLACE, silently collapsing two
	// notes into one. One session legitimately has several notes; that is why this
	// is a separate field and why the lookup below returns a SLICE.
	//
	// Declared high in the struct for the same reason as SessionKey: yaml.Marshal
	// emits in declaration order, so this stays inside the bounded head window the
	// scan below reads.
	ArchiveSessionID string `yaml:"archive_session_id,omitempty"`

	// ArchiveSessionIDSource records the provenance of ArchiveSessionID:
	// "derived" when the MCP server resolved it from the host's live session
	// map (internal/hostsession), empty when the hook pushed the id it was
	// handed (the hook IS the authority) or when no id was recorded. Lets the
	// vault audit — and a human reading the note — tell a derived link from a
	// pushed one.
	ArchiveSessionIDSource string `yaml:"archive_session_id_source,omitempty"`

	// DELETED at 202: branch, domain, duration_minutes, messages, tokens_in,
	// tokens_out, tool_uses — and needs_indexing, below.
	//
	// All eight were yaml-tagged, omitempty, and NOTHING EVER ASSIGNED THEM. They
	// were the same defect as note_path, sitting in the same struct, and they
	// survived the note_path fix because that fix chased one field instead of asking
	// what else shared its shape.
	//
	// internal/sourceaudit found them mechanically, and the live vault confirmed the
	// verdict before a line was deleted: **0 of 273 session notes carry any of these
	// keys** — never written, not once, in the whole life of the project. Nothing
	// reads them either (the apparent readers are bare-name collisions on other
	// structs). So removing them is a provable no-op on disk, not a migration.
	//
	// Delete, don't "wire up": a field nobody produces and nobody consumes is not a
	// feature waiting to happen, it is a claim the struct was making and never
	// honoring. If session telemetry is wanted later, add it WITH a writer and a
	// reader in the same commit.
	Iteration     int                `yaml:"iteration"`
	Title         string             `yaml:"title,omitempty"`
	Summary       string             `yaml:"summary,omitempty"`
	Tag           string             `yaml:"tag,omitempty"`
	Model         string             `yaml:"model,omitempty"`
	FrictionScore int                `yaml:"friction_score,omitempty"`
	Breakdown     *FrictionBreakdown `yaml:"friction_breakdown,omitempty"`
	Decisions     []string           `yaml:"decisions,omitempty"`
	FilesChanged  []string           `yaml:"files_changed,omitempty"`
	OpenThreads   []string           `yaml:"open_threads,omitempty"`
	NotePath      string             `yaml:"note_path,omitempty"`
	// Archive is the vault-relative path to the transcript manifest
	// associated with this session, if one was archived. Written by
	// vp_capture_session when session_id + adapter resolve to an
	// existing archive. See doc/adr/001-transcript-archive.md.
	Archive string `yaml:"archive,omitempty"`
	// EnrichedBy and EnrichedAt mark a session whose summary/decisions/threads
	// were synthesized by an LLM enrichment pass (set by the enrichment pass or
	// the Step-7 drain). EnrichedBy is the enriching model; EnrichedAt is an
	// RFC3339 timestamp. Both are omitted for plain heuristic captures.
	EnrichedBy string `yaml:"enriched_by,omitempty"`
	EnrichedAt string `yaml:"enriched_at,omitempty"`
}

// sessionKeyScanBytes bounds how much of a note the key scan reads. Frontmatter
// sits at the top of the file and session_key is declared high in SessionMeta, so
// the key is always well inside this window. The bound is the whole point: notes
// are ~20 KB of narrative each and grow forever, so a whole-file scan of the
// directory would cost megabytes of I/O on EVERY capture, inside an exclusive
// lock every other writer queues behind. A head read makes it cents.
const sessionKeyScanBytes = 4096

// frontmatterFieldFromHead returns the value of a top-level frontmatter field,
// reading only the first sessionKeyScanBytes of the file. It returns "" for a
// note that does not carry the field (every note written before the field
// existed), for an unreadable file, and for anything that is not frontmatter — a
// miss is never an error, because a caller's fallback is simply to mint a new
// note, or to skip a note it cannot associate.
//
// It deliberately does NOT yaml.Unmarshal: the head is a TRUNCATED document, so a
// real parse would fail on exactly the large notes we are trying not to read.
//
// ONE definition, parameterized by field name — session_key and archive_session_id
// are read by the same scanner on purpose. A second hand-rolled copy of "parse the
// frontmatter head" is how two readers of one file format drift apart (see mdfence,
// whose naive fence check had been copied three times before one definition
// replaced them).
//
// field is matched as a top-level key only: the "name:" prefix must start the line,
// so a nested or body line of the same name cannot match.
func frontmatterFieldFromHead(path, field string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	buf := make([]byte, sessionKeyScanBytes)
	n, err := f.Read(buf)
	if n == 0 || (err != nil && err != io.EOF) {
		return ""
	}
	head := string(buf[:n])
	if !strings.HasPrefix(head, "---\n") {
		return ""
	}
	front := head[4:]
	// Stop at the closing delimiter when it is inside the window, so a key-shaped
	// line in the BODY can never be mistaken for frontmatter.
	if end := strings.Index(front, "\n---\n"); end >= 0 {
		front = front[:end]
	}
	for line := range strings.SplitSeq(front, "\n") {
		rest, ok := strings.CutPrefix(line, field+":")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(rest), `"'`)
	}
	return ""
}

// sessionKeyFromHead returns the session_key recorded in a note's frontmatter.
func sessionKeyFromHead(path string) string {
	return frontmatterFieldFromHead(path, "session_key")
}

// parseSessionStem splits a session filename stem back into the coordinates that
// resolve it: "YYYY-MM-DD-<fp>-NN" (host-scoped) or "YYYY-MM-DD-NN" (legacy).
func parseSessionStem(stem string) (date, fp string, iteration int, ok bool) {
	parts := strings.Split(stem, "-")
	if len(parts) < 4 {
		return "", "", 0, false
	}
	date = strings.Join(parts[:3], "-")
	if !datePattern.MatchString(date) {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || n < 1 {
		return "", "", 0, false
	}
	if len(parts) >= 5 {
		fp = parts[3]
	}
	return date, fp, n, true
}

// findSessionByKey scans a project's session notes for one carrying sessionKey.
// Callers MUST hold the sessions-directory lock: this is a read that a concurrent
// capture could invalidate, and its whole purpose is to decide whether to mint.
func (v *Vault) findSessionByKey(project, sessionKey string) (SessionRef, bool, error) {
	dir, err := v.SessionDir(project)
	if err != nil {
		return SessionRef{}, false, err
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return SessionRef{}, false, fmt.Errorf("glob sessions: %w", err)
	}
	for _, m := range matches {
		if sessionKeyFromHead(m) != sessionKey {
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(m), ".md")
		date, fp, iteration, ok := parseSessionStem(stem)
		if !ok {
			continue
		}
		rel, relErr := v.SessionRelPath(project, date, fp, iteration)
		if relErr != nil {
			continue
		}
		return SessionRef{
			ID:          stem,
			NotePath:    rel,
			Date:        date,
			Fingerprint: fp,
			Iteration:   iteration,
		}, true, nil
	}
	return SessionRef{}, false, nil
}

// MergeSessionFunc reconciles a capture against the note it is updating. It
// receives the note as it exists on disk and the metadata the new capture wants
// to write, and returns the metadata and body to persist.
type MergeSessionFunc func(old SessionMeta, oldBody string, next SessionMeta) (SessionMeta, string)

// UpsertSessionByKey is the ONE critical section of a capture: it resolves
// sessionKey, and then either rewrites the note that key already names or mints a
// brand new one — under a single lock, so the two cannot race.
//
// The lock closes a hole that PREDATES idempotency. NextIteration globs the
// directory and takes max(NN)+1, and it did so OUTSIDE any lock: the lock was
// taken later, on the already-decided path. So two concurrent captures on one
// host+date both computed the same N+1, both locked the same note path, and the
// second CLOBBERED the first — today, with no keys involved. Resolving a key in
// front of an unlocked mint would have inherited that race; holding one lock over
// resolve + mint + write removes it instead.
//
// LOCK ORDER — DIRECTORY BEFORE FILE, NEVER THE REVERSE. This takes the lock on
// the sessions DIRECTORY, and the writers it calls (WriteSessionRef,
// RewriteSession) take the lock on the NOTE PATH. vaultlock keys on a sha256 of
// the canonical path, so those are different lock files and the nesting is safe —
// but vaultlock.Acquire is a blocking LOCK_EX with no LOCK_NB and no timeout, so
// a caller that ever inverts this order gets a PERMANENT HANG, not an error.
//
// Returns the note's identity and whether an existing note was updated (true) or
// a new one minted (false). An empty sessionKey always mints.
func (v *Vault) UpsertSessionByKey(project, sessionKey string, meta SessionMeta, body string, merge MergeSessionFunc) (SessionRef, bool, error) {
	dir, err := v.SessionDir(project)
	if err != nil {
		return SessionRef{}, false, err
	}
	if err := EnsureDir(dir); err != nil {
		return SessionRef{}, false, fmt.Errorf("ensure sessions dir: %w", err)
	}

	release, err := vaultlock.Acquire(v.Root, dir)
	if err != nil {
		return SessionRef{}, false, fmt.Errorf("storage: lock sessions dir: %w", err)
	}
	defer func() { _ = release() }()

	if sessionKey != "" {
		ref, found, ferr := v.findSessionByKey(project, sessionKey)
		if ferr != nil {
			return SessionRef{}, false, ferr
		}
		if found {
			// READ-MERGE-REWRITE, never rewrite-blind. RewriteSession marshals
			// EXACTLY what it is handed and every field is omitempty, so handing it
			// the new capture's meta alone would silently delete everything the note
			// already carried that this capture did not recompute: archive:,
			// friction_breakdown (whose nil is load-bearing — it means "never
			// scored"), and the LLM-synthesized enriched_by/enriched_at narrative the
			// async drain may have written since. This is the same read-then-apply
			// shape the enrichment drain already uses; it is preservation, not
			// convenience.
			oldMeta, oldBody, rerr := v.ReadSession(project, ref.Date, ref.Fingerprint, ref.Iteration)
			if rerr != nil {
				return SessionRef{}, false, fmt.Errorf("read session for update: %w", rerr)
			}
			finalMeta, finalBody := merge(oldMeta, oldBody, meta)
			// Source the coordinates from the note we ACTUALLY READ. RewriteSession
			// never checks that its target exists, so a wrong iteration here would
			// overwrite a DIFFERENT note and stamp it with that note's ID — producing
			// a perfectly well-formed, self-consistent, wrong file.
			if err := v.RewriteSession(project, ref.Date, ref.Fingerprint, ref.Iteration, finalMeta, finalBody); err != nil {
				return SessionRef{}, false, err
			}
			return ref, true, nil
		}
	}

	ref, err := v.WriteSessionRef(project, meta, body)
	return ref, false, err
}

// TagAutoCapture marks the note the hook writes unattended at the first Stop — a
// git-log summary of recent commits, not a narrative of the session.
//
// It lives here, as ONE definition, because the canonical-note rule below turns on
// this exact string matching what the hook actually writes. Two string literals in
// two packages would let them drift, and the failure would be SILENT AND INVERTED:
// the transcript's back-link would quietly point at the throwaway stub instead of
// the note carrying the session's decisions — which is the very defect this whole
// change exists to fix.
const TagAutoCapture = "auto-capture"

// ArchiveLinkResult reports what LinkArchiveToSessions did.
type ArchiveLinkResult struct {
	// Updated names every note whose archive: field now points at the manifest.
	// Empty when the notes already carried it — the operation is idempotent, and
	// "nothing to do" is reported as nothing updated, never as a failure.
	Updated []SessionRef

	// Canonical is the note the MANIFEST should point back at, and Found reports
	// whether any note for this session exists at all. A session with no notes is
	// an ordinary, expected outcome (the hook archives before it captures), NOT an
	// error: Found is false and the caller simply does not write a back-link.
	Canonical SessionRef
	Found     bool
}

// LinkArchiveToSessions closes the note <-> transcript loop for one host session:
// it stamps archiveRel onto the archive: field of EVERY note captured from that
// session, and reports which of them the manifest should point back at.
//
// THIS IS THE FIX for capture-note-archive-link-never-closes. Capture can only
// link a note to an archive that ALREADY EXISTS, and at capture time it usually
// does not: the hook archives at SessionEnd, while the auto-capture note is
// written at the first Stop ~90 s in, and the agent's wrap note lands somewhere in
// between. The claim sentinel then guarantees capture never runs again, so the
// archive that appears at SessionEnd was never linked to anything. The loop must
// therefore be closed from the ARCHIVE side, after archive.Create — which is
// exactly the "future hook run can call LinkSessionNote" recovery path that
// session.go's comment has promised since it was written and that nothing ever
// built.
//
// It returns a SLICE because one session legitimately produces several notes (the
// hook's auto-capture stub plus the agent's wrap note): every one of them gets the
// forward link, so the transcript is reachable from any note of that session.
//
// CANONICAL-NOTE RULE (deterministic, and NOT order-dependent on hook arrival):
// the manifest points back at the note a human would actually want to land on —
// the one that is not the auto-capture stub, latest by (date, iteration) if there
// is more than one. Only when the stub is the session's ONLY note does the
// manifest point at it. Ordering by note coordinates rather than by mtime or by
// arrival keeps a re-run, a PreCompact, and a SessionEnd all converging on the
// same answer.
//
// Idempotent by construction: a note already carrying archiveRel is left untouched
// (not rewritten), so PreCompact-then-SessionEnd converges instead of thrashing.
//
// LOCK ORDER — DIRECTORY BEFORE FILE. This holds the sessions-directory lock and
// calls RewriteSession, which takes the NOTE-PATH lock. That nesting is the same
// one UpsertSessionByKey documents and is safe; INVERTING it is a permanent hang,
// because vaultlock.Acquire is a blocking LOCK_EX with no LOCK_NB and no timeout.
func (v *Vault) LinkArchiveToSessions(project, archiveSessionID, archiveRel string) (ArchiveLinkResult, error) {
	if archiveSessionID == "" {
		return ArchiveLinkResult{}, fmt.Errorf("archive session id is empty")
	}
	if archiveRel == "" {
		return ArchiveLinkResult{}, fmt.Errorf("archive path is empty")
	}

	dir, err := v.SessionDir(project)
	if err != nil {
		return ArchiveLinkResult{}, err
	}

	release, err := vaultlock.Acquire(v.Root, dir)
	if err != nil {
		return ArchiveLinkResult{}, fmt.Errorf("storage: lock sessions dir: %w", err)
	}
	defer func() { _ = release() }()

	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return ArchiveLinkResult{}, fmt.Errorf("glob sessions: %w", err)
	}

	var res ArchiveLinkResult
	var canonicalMeta SessionMeta

	for _, m := range matches {
		if frontmatterFieldFromHead(m, "archive_session_id") != archiveSessionID {
			continue
		}
		stem := strings.TrimSuffix(filepath.Base(m), ".md")
		date, fp, iteration, ok := parseSessionStem(stem)
		if !ok {
			continue
		}
		rel, relErr := v.SessionRelPath(project, date, fp, iteration)
		if relErr != nil {
			continue
		}
		meta, body, rerr := v.ReadSession(project, date, fp, iteration)
		if rerr != nil {
			return ArchiveLinkResult{}, fmt.Errorf("read session %s: %w", stem, rerr)
		}

		ref := SessionRef{ID: stem, NotePath: rel, Date: date, Fingerprint: fp, Iteration: iteration}

		// Forward link (note -> transcript). Skip the write when it is already
		// correct: re-marshalling an unchanged note would churn the vault's git
		// history on every SessionEnd for no gain.
		if meta.Archive != archiveRel {
			meta.Archive = archiveRel
			if werr := v.RewriteSession(project, date, fp, iteration, meta, body); werr != nil {
				return ArchiveLinkResult{}, fmt.Errorf("link session %s: %w", stem, werr)
			}
			res.Updated = append(res.Updated, ref)
		}

		if !res.Found || preferCanonical(canonicalMeta, res.Canonical, meta, ref) {
			res.Canonical, canonicalMeta = ref, meta
		}
		res.Found = true
	}

	return res, nil
}

// preferCanonical reports whether candidate (nextMeta, next) should displace the
// incumbent (curMeta, cur) as the note a transcript points back at. A real note
// always beats the auto-capture stub; between two of the same kind, the later
// (date, iteration) wins. See LinkArchiveToSessions for why this must not depend
// on filesystem mtime or on the order the hook happened to see events in.
func preferCanonical(curMeta SessionMeta, cur SessionRef, nextMeta SessionMeta, next SessionRef) bool {
	curStub := curMeta.Tag == TagAutoCapture
	nextStub := nextMeta.Tag == TagAutoCapture
	if curStub != nextStub {
		return curStub // a non-stub displaces a stub, never the reverse
	}
	if next.Date != cur.Date {
		return next.Date > cur.Date
	}
	return next.Iteration > cur.Iteration
}

// SessionRef identifies a session note that was just written: its ID plus the
// coordinates that resolve it again. It exists so a caller never has to
// re-derive where the note went by parsing the ID back apart — the writer that
// placed the file is the only thing entitled to say where it landed, and it now
// simply says so. NotePath is vault-relative (see SessionRelPath).
type SessionRef struct {
	ID          string
	NotePath    string
	Date        string
	Fingerprint string
	Iteration   int
}

// WriteSession writes a session markdown file with YAML frontmatter.
// It auto-increments the iteration number and returns the session ID.
// It is a thin wrapper over WriteSessionRef, which additionally reports where
// the note landed; prefer that one when you need the note's path.
func (v *Vault) WriteSession(project string, meta SessionMeta, body string) (string, error) {
	ref, err := v.WriteSessionRef(project, meta, body)
	if err != nil {
		return "", err
	}
	return ref.ID, nil
}

// WriteSessionRef writes a session markdown file with YAML frontmatter,
// auto-incrementing the iteration number, and returns the full identity of the
// note it wrote — ID, vault-relative path, and the (date, fp, iteration)
// coordinates that resolve it.
func (v *Vault) WriteSessionRef(project string, meta SessionMeta, body string) (SessionRef, error) {
	if err := slug.Validate(project); err != nil {
		return SessionRef{}, fmt.Errorf("project: %w", err)
	}
	if !datePattern.MatchString(meta.Date) {
		return SessionRef{}, fmt.Errorf("date %q must be in YYYY-MM-DD format", meta.Date)
	}

	// Fingerprint this writer (host+vault) so two offline hosts minting the
	// same (date, iteration) produce distinct filenames AND distinct meta.IDs,
	// avoiding git add/add conflicts and ambiguous session identity on sync.
	fp := surface.WriterFingerprint(v.Root)

	iteration, err := v.NextIteration(project, meta.Date, fp)
	if err != nil {
		return SessionRef{}, err
	}

	meta.Iteration = iteration
	meta.Project = project
	meta.ID = SessionStem(meta.Date, fp, iteration)

	// note_path is an identity coordinate, so the writer that decides where the
	// note goes is the only thing entitled to state where it went. It was yaml-
	// tagged and never assigned, so omitempty dropped the key from every note
	// ever written and every read-back unmarshalled a field that was not there:
	// vp_capture_session had reported note_path: "" for the entire life of the
	// project while returning status ok.
	rel, err := v.SessionRelPath(project, meta.Date, fp, iteration)
	if err != nil {
		return SessionRef{}, err
	}
	meta.NotePath = rel

	path, err := v.SessionFile(project, meta.Date, fp, iteration)
	if err != nil {
		return SessionRef{}, err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return SessionRef{}, fmt.Errorf("ensure sessions dir: %w", err)
	}

	data, err := marshalSessionFile(meta, body)
	if err != nil {
		return SessionRef{}, err
	}

	if err := v.lockedWrite(path, data); err != nil {
		return SessionRef{}, fmt.Errorf("write session file: %w", err)
	}
	return SessionRef{
		ID:          meta.ID,
		NotePath:    rel,
		Date:        meta.Date,
		Fingerprint: fp,
		Iteration:   iteration,
	}, nil
}

// marshalSessionFile assembles the on-disk session file bytes: the YAML
// frontmatter (marshaled from meta) framed by "---\n…---\n", followed by the
// body with exactly one trailing newline. WriteSession and RewriteSession
// share this helper so both writers produce byte-identical framing for the
// same (meta, body) pair.
func marshalSessionFile(meta SessionMeta, body string) ([]byte, error) {
	yamlBytes, err := yaml.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("marshal session meta: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	buf.WriteString("---\n")
	if body != "" {
		buf.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}

// RewriteSession overwrites an EXISTING session file in place at the fixed
// (project, date, iteration) path. Unlike WriteSession it does NOT allocate a
// new iteration — the iteration is the caller's responsibility — so it is
// idempotent: re-running it with the same (meta, body) yields byte-identical
// bytes. It is used by the Step-7 enrichment drain to rewrite a previously
// captured note with synthesized summary/decisions/threads. It shares only the
// marshalSessionFile framing helper with WriteSession and serializes against a
// concurrent WriteSession via the same per-path advisory lock (lockedWrite).
func (v *Vault) RewriteSession(project, date, fp string, iteration int, meta SessionMeta, body string) error {
	if err := slug.Validate(project); err != nil {
		return fmt.Errorf("project: %w", err)
	}
	if !datePattern.MatchString(date) {
		return fmt.Errorf("date %q must be in YYYY-MM-DD format", date)
	}

	path, err := v.SessionFile(project, date, fp, iteration)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("ensure sessions dir: %w", err)
	}

	// Defensively pin the identity fields so a rewrite cannot drift the
	// note's own coordinates away from its path. The ID carries the
	// fingerprint when present so it matches the host-scoped filename.
	// note_path is pinned here too — it is a coordinate like the rest, and
	// pinning it backfills every note written before WriteSession set it
	// (the drain rewrites from a ReadSession whose note_path is empty for
	// those, and an empty field would otherwise be written straight back).
	meta.Project = project
	meta.Date = date
	meta.Iteration = iteration
	meta.ID = SessionStem(date, fp, iteration)

	rel, err := v.SessionRelPath(project, date, fp, iteration)
	if err != nil {
		return err
	}
	meta.NotePath = rel

	data, err := marshalSessionFile(meta, body)
	if err != nil {
		return err
	}

	if err := v.lockedWrite(path, data); err != nil {
		return fmt.Errorf("rewrite session file: %w", err)
	}
	return nil
}

// ReadSession reads a session file and returns its metadata and body. fp is
// the writer fingerprint that scoped the file at write time (empty for legacy
// host-agnostic notes). Because this resolves arbitrary — possibly other-host
// — sessions (e.g. via the MCP session resource), fp must be supplied by the
// caller, not recomputed for the local host.
func (v *Vault) ReadSession(project, date, fp string, iteration int) (SessionMeta, string, error) {
	path, err := v.SessionFile(project, date, fp, iteration)
	if err != nil {
		return SessionMeta{}, "", err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return SessionMeta{}, "", fmt.Errorf("read session file: %w", err)
	}

	return ParseFrontmatter(data)
}

// ListSessions returns session metadata filtered by date range and limited
// to the specified count. Pass empty strings for dateFrom/dateTo to skip
// date filtering. Pass 0 for limit to return all matches.
func (v *Vault) ListSessions(project, dateFrom, dateTo string, limit int) ([]SessionMeta, error) {
	dir, err := v.SessionDir(project)
	if err != nil {
		return nil, err
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob sessions: %w", err)
	}
	sort.Strings(matches)

	var result []SessionMeta
	for _, m := range matches {
		base := filepath.Base(m)
		// Filename format: YYYY-MM-DD-NN.md — date is first 10 chars.
		if len(base) < 10 {
			continue
		}
		date := base[:10]
		if dateFrom != "" && date < dateFrom {
			continue
		}
		if dateTo != "" && date > dateTo {
			continue
		}

		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read session %s: %w", m, err)
		}
		meta, _, err := ParseFrontmatter(data)
		if err != nil {
			return nil, fmt.Errorf("parse session %s: %w", m, err)
		}
		result = append(result, meta)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// SearchSessions returns sessions matching a text query and/or minimum
// friction score. The query matches against title, summary, and decisions
// (case-insensitive). Pass empty query and 0 minFriction to match all.
func (v *Vault) SearchSessions(query, project string, minFriction, limit int) ([]SessionMeta, error) {
	all, err := v.ListSessions(project, "", "", 0)
	if err != nil {
		return nil, err
	}

	lowerQuery := strings.ToLower(query)
	var result []SessionMeta
	for _, meta := range all {
		if minFriction > 0 && meta.FrictionScore < minFriction {
			continue
		}
		if query != "" && !sessionMatchesQuery(meta, lowerQuery) {
			continue
		}
		result = append(result, meta)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result, nil
}

// NextIteration returns the next available iteration number for a
// project+date combination (1-based). It is max(NN)+1 over the matching files
// — not count+1 — so a gap in the iteration set (e.g. 01 and 03 present, 02
// deleted) never re-mints an existing iteration and overwrites its note. The
// scan is scoped to the writer fingerprint fp so two offline hosts each
// allocate iterations independently (both may legitimately start at 01 for the
// same date). When fp is empty the legacy host-agnostic glob is used.
func (v *Vault) NextIteration(project, date, fp string) (int, error) {
	dir, err := v.SessionDir(project)
	if err != nil {
		return 0, err
	}

	pattern := filepath.Join(dir, SessionGlobPrefix(date, fp)+"*.md")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, fmt.Errorf("glob iterations: %w", err)
	}
	next := 0
	for _, m := range matches {
		if it := iterationFromSessionFilename(m); it > next {
			next = it
		}
	}
	return next + 1, nil
}

// iterationFromSessionFilename extracts the trailing NN iteration from a session
// filename's base (the last hyphen segment of the stem). It returns 0 for names
// with too few segments to carry an iteration, so a malformed match cannot
// inflate NextIteration. Kept storage-local — storage must not import capture.
func iterationFromSessionFilename(name string) int {
	base := strings.TrimSuffix(filepath.Base(name), ".md")
	parts := strings.Split(base, "-")
	if len(parts) < 4 {
		return 0
	}
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}

// ParseFrontmatter splits a markdown file with YAML frontmatter into
// metadata and body. The frontmatter is delimited by "---" on its own lines.
func ParseFrontmatter(data []byte) (SessionMeta, string, error) {
	content := string(data)

	if !strings.HasPrefix(content, "---\n") {
		return SessionMeta{}, "", fmt.Errorf("missing opening frontmatter delimiter")
	}

	// Find the closing "---" after the opening one.
	rest := content[4:] // skip opening "---\n"
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		// Try ending with "---" at EOF (no trailing newline after delimiter).
		if strings.HasSuffix(rest, "\n---") {
			idx = len(rest) - 4
		} else {
			return SessionMeta{}, "", fmt.Errorf("missing closing frontmatter delimiter")
		}
	}

	yamlStr := rest[:idx]
	body := ""
	closingEnd := idx + 5 // len("\n---\n")
	if closingEnd <= len(rest) {
		body = rest[closingEnd:]
	}

	var meta SessionMeta
	if err := yaml.Unmarshal([]byte(yamlStr), &meta); err != nil {
		return SessionMeta{}, "", fmt.Errorf("parse frontmatter YAML: %w", err)
	}
	return meta, body, nil
}

// sessionMatchesQuery checks if any searchable field contains the query.
func sessionMatchesQuery(meta SessionMeta, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(meta.Title), lowerQuery) {
		return true
	}
	if strings.Contains(strings.ToLower(meta.Summary), lowerQuery) {
		return true
	}
	for _, d := range meta.Decisions {
		if strings.Contains(strings.ToLower(d), lowerQuery) {
			return true
		}
	}
	return false
}
