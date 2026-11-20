// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package archive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultfs"
)

// Regenerating a manifest for an orphaned transcript archive.
//
// An orphan is a complete, valid <day>-<session>.jsonl.zst whose
// .manifest.json was never written: Create writes the zst first (an atomic
// rename), then inspects, then writes the manifest, and a capture that died in
// between leaves exactly this. After storage.TranscriptPairGrace the tidy sweep
// reports it as Orphaned and never commits it. The only other repair, `vp
// archive create`, RE-DATES the archive; this one writes the manifest next to
// the EXISTING zst and never opens the zst for writing, so the name, and with it
// the date, is unchanged and the next tidy sweeps both halves.
//
// What the manifest can and cannot say is the design (doc/adr/001 amendment):
// the hash, sizes, model and turn count are derived from the archive's bytes;
// session id and project slug are read from the filename and path (and the
// manifest says so); the adapter and capture time are the OPERATOR's claims and
// are required, never guessed (claude-code and inline archives are byte-for-byte
// the same shape and differ in authenticity, and a file's mtime can be rewritten
// by a copy); git_head, captured_by_hostname, vp_version and the session-note
// link are unrecoverable and left empty. The Regenerated marker records all of
// it.
//
// ALL enforcement lives here, once, for the CLI and the MCP tool alike.

// Typed refusals, so callers and tests can tell them apart.
var (
	ErrRepairAdapterRequired    = errors.New("archive repair: --adapter is required (it is an authenticity claim and is never guessed)")
	ErrRepairCapturedAtRequired = errors.New("archive repair: --captured-at is required (an RFC3339 time the operator vouches for; it is never inferred from the file)")
	ErrNoSuchArchive            = errors.New("archive repair: no such archive")
	ErrNotRepairable            = errors.New("archive repair: not a repairable archive name or location")
	ErrArchiveTooYoung          = errors.New("archive repair: the archive is younger than the pair grace; a capture may still own it")
	// ErrOrphanHasSignature: a .sig beside an orphan that has no manifest is an
	// anomaly (a signature of what?). It is surfaced, never repaired over, so the
	// signing rollback can only ever remove what this call wrote.
	ErrOrphanHasSignature = errors.New("archive repair: a signature file already exists beside this archive but no manifest does; " +
		"that is an anomaly to investigate (a .sig of what?), so nothing was written; move the .sig aside once you know what it is")
)

// unrecoverableFields are the manifest fields only the capturing process could
// have supplied. They stay empty on a regenerated manifest and are named in it.
var unrecoverableFields = []string{"git_head", "captured_by_hostname", "vp_version", "vault_rel_session_note"}

// Test seams, nil/default in production.
var (
	// repairPreLockHook runs after the unlocked pre-check and before the lock is
	// taken, so a test can make a manifest appear in exactly that window.
	repairPreLockHook func()
	// signManifest is Sign; a test replaces it to inject a signing failure.
	signManifest = Sign
	// removeForRollback is vaultfs.RemoveNoLock; a test replaces it to inject a
	// cleanup failure.
	removeForRollback = vaultfs.RemoveNoLock
)

// RepairOptions selects one orphan and carries the operator's claims.
type RepairOptions struct {
	Context   context.Context
	VaultRoot string
	// Project is required when Archive is a bare name; otherwise it is checked
	// against the path.
	Project string
	// Archive is the zst: an absolute path, a vault-relative path, or a bare
	// YYYY-MM-DD-<session>[.jsonl.zst] name with Project.
	Archive string
	// Adapter and CapturedAt are the operator's claims; both are required.
	Adapter    string
	CapturedAt string
	VPVersion  string
	Sign       SignOptions
	// Now is the clock for the grace check and the marker; zero means time.Now.
	Now time.Time
}

// RepairResult reports one repair.
type RepairResult struct {
	ManifestPath string    `json:"manifest_path"`
	ArchivePath  string    `json:"archive_path"`
	Manifest     *Manifest `json:"manifest,omitempty"`
	// NothingToDo is set, with the reason, when a manifest already exists; no file
	// is written.
	NothingToDo string   `json:"nothing_to_do,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Orphan is one lone archive found by ListOrphans.
type Orphan struct {
	Project      string    `json:"project"`
	Name         string    `json:"name"`
	ArchivePath  string    `json:"archive_path"`
	ManifestPath string    `json:"manifest_path"`
	SessionID    string    `json:"session_id,omitempty"`
	FilenameDay  string    `json:"filename_day,omitempty"`
	ModTime      time.Time `json:"mtime"`
	AgeSeconds   int64     `json:"age_seconds"`
	// Repairable is true when the name parses and the archive is past the grace.
	Repairable bool   `json:"repairable"`
	Reason     string `json:"reason,omitempty"` // why not, when not repairable
}

const zstSuffix = ".jsonl.zst"

// parseArchiveName splits <YYYY-MM-DD>-<session id> and validates both halves.
func parseArchiveName(base string) (day, session string, ok bool) {
	if len(base) <= len("2006-01-02-") || base[len("2006-01-02")] != '-' {
		return "", "", false
	}
	day, session = base[:len("2006-01-02")], base[len("2006-01-02")+1:]
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return "", "", false
	}
	if session == "" || strings.ContainsAny(session, `/\`) {
		return "", "", false
	}
	return day, session, true
}

// manifestPathFor builds the manifest path exactly as Create does, from the
// vault root, project and name, so both writers lock the SAME key.
func manifestPathFor(vaultRoot, project, base string) (archivePath, manifestPath string) {
	dir := filepath.Join(vaultRoot, "Projects", project, "transcripts")
	return filepath.Join(dir, base+zstSuffix), filepath.Join(dir, base+".manifest.json")
}

func sigBesideOrphan(manifestPath string) bool {
	_, err := os.Lstat(SignaturePath(manifestPath))
	return err == nil
}

// ListOrphans returns every lone .jsonl.zst (no sibling manifest) under
// Projects/<project>/transcripts, or under every project when project is empty.
// It reads the filesystem only (independent of git state) and writes nothing.
func ListOrphans(vaultRoot, project string, now time.Time) ([]Orphan, error) {
	if now.IsZero() {
		now = time.Now()
	}
	var projects []string
	if project != "" {
		projects = []string{project}
	} else {
		ents, err := os.ReadDir(filepath.Join(vaultRoot, "Projects"))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return []Orphan{}, nil
			}
			return nil, fmt.Errorf("read Projects: %w", err)
		}
		for _, e := range ents {
			if e.IsDir() {
				projects = append(projects, e.Name())
			}
		}
	}
	out := []Orphan{}
	for _, p := range projects {
		dir := filepath.Join(vaultRoot, "Projects", p, "transcripts")
		ents, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", dir, err)
		}
		for _, e := range ents {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, zstSuffix) {
				continue
			}
			base := strings.TrimSuffix(name, zstSuffix)
			ap, mp := manifestPathFor(vaultRoot, p, base)
			if _, err := os.Stat(mp); err == nil {
				continue // a complete pair
			}
			st, err := os.Stat(ap)
			if err != nil {
				continue
			}
			o := Orphan{Project: p, Name: base, ArchivePath: ap, ManifestPath: mp, ModTime: st.ModTime(),
				AgeSeconds: int64(now.Sub(st.ModTime()).Seconds())}
			day, session, ok := parseArchiveName(base)
			switch {
			case !ok:
				o.Reason = "the name is not YYYY-MM-DD-<session id>"
			case sigBesideOrphan(mp):
				o.SessionID, o.FilenameDay = session, day
				o.Reason = "a .sig exists beside this archive but no manifest: an anomaly to investigate; repair refuses it"
			case now.Sub(st.ModTime()) < storage.TranscriptPairGrace:
				o.SessionID, o.FilenameDay = session, day
				o.Reason = fmt.Sprintf("younger than %s: a capture may still own it", storage.TranscriptPairGrace)
			default:
				o.SessionID, o.FilenameDay = session, day
				o.Repairable = true
			}
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ArchivePath < out[j].ArchivePath })
	return out, nil
}

// resolveRepairTarget turns the operator's argument into (project, base name),
// then rebuilds the paths from the vault root so the lock key matches Create's.
func resolveRepairTarget(vaultRoot, project, arg string) (proj, base string, err error) {
	if arg == "" {
		return "", "", fmt.Errorf("%w: no archive given", ErrNotRepairable)
	}
	bare := !strings.ContainsAny(arg, `/\`)
	if bare {
		if project == "" {
			return "", "", fmt.Errorf("%w: a bare name needs --project", ErrNotRepairable)
		}
		return project, strings.TrimSuffix(arg, zstSuffix), nil
	}
	abs := arg
	if !filepath.IsAbs(arg) {
		abs = filepath.Join(vaultRoot, filepath.FromSlash(arg))
	}
	rel, rerr := filepath.Rel(vaultRoot, abs)
	if rerr != nil || strings.HasPrefix(rel, "..") {
		return "", "", fmt.Errorf("%w: %s is outside the vault", ErrNotRepairable, arg)
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) != 4 || parts[0] != "Projects" || parts[2] != "transcripts" || !strings.HasSuffix(parts[3], zstSuffix) {
		return "", "", fmt.Errorf("%w: %s is not Projects/<project>/transcripts/<name>%s", ErrNotRepairable, arg, zstSuffix)
	}
	if project != "" && project != parts[1] {
		return "", "", fmt.Errorf("%w: the path is in project %q, not %q", ErrNotRepairable, parts[1], project)
	}
	return parts[1], strings.TrimSuffix(parts[3], zstSuffix), nil
}

// RepairManifest writes the manifest of ONE orphaned archive; see the file
// comment. The steps, and the failure each owns:
//
//  1. validate the claims and the name; nothing is touched;
//  2. an unlocked pre-check: the zst exists, no manifest exists (else
//     NothingToDo), the zst is at least storage.TranscriptPairGrace old (else
//     ErrArchiveTooYoung), using the same constant and arithmetic as the tidy
//     sweep;
//  3. the manifest lock Create takes, NON-blocking (a held lock means a live
//     capture owns the pair): ErrManifestLocked;
//  4. under the lock, the same checks again, because a slow capture may have
//     finished: an existing manifest is NEVER overwritten;
//  5. one streaming pass through the zst (hash, byte count, and a temp copy
//     OUTSIDE the vault for the adapter's Inspect), then the manifest through
//     WriteManifest (atomic, through the write funnel), then Sign if configured;
//     a signing failure removes the manifest again, under the lock, so the
//     orphan stays an orphan and a retry is possible.
//
// The zst is never opened for writing. Not fixed here: a later LinkSessionNote
// rewrites the manifest without re-signing it (task
// link-session-note-rewrites-a-signed-manifest-without-re-signing).
func RepairManifest(opts RepairOptions) (*RepairResult, error) {
	if opts.VaultRoot == "" {
		return nil, fmt.Errorf("vault root is required")
	}
	if opts.Adapter == "" {
		return nil, ErrRepairAdapterRequired
	}
	a, err := LookupAdapter(opts.Adapter)
	if err != nil {
		return nil, err
	}
	if opts.CapturedAt == "" {
		return nil, ErrRepairCapturedAtRequired
	}
	capturedAt, err := time.Parse(time.RFC3339, opts.CapturedAt)
	if err != nil {
		return nil, fmt.Errorf("archive repair: --captured-at %q is not RFC3339: %w", opts.CapturedAt, err)
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	if capturedAt.After(now) {
		return nil, fmt.Errorf("archive repair: --captured-at %s is in the future", opts.CapturedAt)
	}
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}

	proj, base, err := resolveRepairTarget(opts.VaultRoot, opts.Project, opts.Archive)
	if err != nil {
		return nil, err
	}
	day, session, ok := parseArchiveName(base)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not YYYY-MM-DD-<session id>", ErrNotRepairable, base)
	}
	zstPath, manifestPath := manifestPathFor(opts.VaultRoot, proj, base)
	res := &RepairResult{ManifestPath: manifestPath, ArchivePath: zstPath}
	if d := storage.NewVault(opts.VaultRoot).CalendarDay(capturedAt); d != day {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"--captured-at falls on %s in the vault's zone but the archive's filename day is %s; recorded as given", d, day))
	}

	// check is the pre-check and the re-check: one definition, run twice.
	check := func() (nothing string, err error) {
		st, serr := os.Stat(zstPath)
		if serr != nil {
			if errors.Is(serr, fs.ErrNotExist) {
				return "", fmt.Errorf("%w: %s", ErrNoSuchArchive, zstPath)
			}
			return "", fmt.Errorf("archive repair: stat archive: %w", serr)
		}
		if _, merr := os.Stat(manifestPath); merr == nil {
			return "a manifest already exists for this archive; nothing was written", nil
		}
		if _, lerr := os.Lstat(SignaturePath(manifestPath)); lerr == nil {
			return "", fmt.Errorf("%w: %s", ErrOrphanHasSignature, SignaturePath(manifestPath))
		}
		if age := now.Sub(st.ModTime()); age < storage.TranscriptPairGrace {
			return "", fmt.Errorf("%w (age %s, grace %s)", ErrArchiveTooYoung, age.Round(time.Second), storage.TranscriptPairGrace)
		}
		return "", nil
	}
	if nothing, cerr := check(); cerr != nil {
		return nil, cerr
	} else if nothing != "" {
		res.NothingToDo = nothing
		return res, nil
	}

	if repairPreLockHook != nil {
		repairPreLockHook()
	}
	release, lerr := lockManifest(opts.VaultRoot, manifestPath, LockNonBlocking)
	if lerr != nil {
		return nil, lerr
	}
	defer release()

	if nothing, cerr := check(); cerr != nil {
		return nil, cerr
	} else if nothing != "" {
		res.NothingToDo = nothing
		return res, nil
	}

	sum, srcBytes, tmp, err := decompressForRepair(ctx, zstPath)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	st, err := os.Stat(zstPath)
	if err != nil {
		return nil, fmt.Errorf("archive repair: stat archive: %w", err)
	}

	turnCount, model, inspectErr := inspectWithin(ctx, a, tmp)
	if inspectErr != nil {
		slog.Warn("archive: adapter Inspect failed during repair; manifest turn_count and model are unset",
			"adapter", opts.Adapter, "session", session, "err", inspectErr)
		res.Warnings = append(res.Warnings, "the adapter could not inspect the transcript; turn_count and model are unset")
	}

	hostname, _ := os.Hostname()
	m := &Manifest{
		SchemaVersion:   ManifestSchemaVersion,
		Adapter:         opts.Adapter,
		AdapterVersion:  a.Version(),
		SessionID:       session,
		Model:           model,
		TurnCount:       turnCount,
		SourceSHA256:    sum,
		SourceBytes:     srcBytes,
		CompressedBytes: st.Size(),
		ProjectSlug:     proj,
		CapturedAt:      capturedAt.UTC().Format(time.RFC3339),
		Regenerated: &Regenerated{
			At:          now.UTC().Format(time.RFC3339),
			ByHostname:  hostname,
			ByVPVersion: opts.VPVersion,
			Bases: map[string]string{
				"session_id":      "filename",
				"project_slug":    "path",
				"adapter":         "operator-supplied",
				"adapter_version": "inspecting version at repair time",
				"captured_at":     "operator-supplied",
			},
			Unrecoverable: append([]string(nil), unrecoverableFields...),
		},
	}
	if err := WriteManifest(opts.VaultRoot, manifestPath, m); err != nil {
		return nil, err
	}
	if opts.Sign.Enabled() {
		if serr := signManifest(manifestPath, opts.Sign); serr != nil {
			// Roll back under the same lock so the orphan stays an orphan and a
			// retry is possible. The check above guarantees no .sig existed
			// before this call, so everything removed here was written by it.
			// Each removal failure is reported; "removed" is claimed only when true.
			var rerrs []error
			if rerr := removeForRollback(manifestPath); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				rerrs = append(rerrs, fmt.Errorf("remove manifest %s: %w", manifestPath, rerr))
			}
			if rerr := removeForRollback(SignaturePath(manifestPath)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				rerrs = append(rerrs, fmt.Errorf("remove signature %s: %w", SignaturePath(manifestPath), rerr))
			}
			if len(rerrs) > 0 {
				return nil, fmt.Errorf("sign manifest: %w; AND the rollback failed, so the repaired manifest may still be present (%w); remove it by hand before retrying",
					serr, errors.Join(rerrs...))
			}
			return nil, fmt.Errorf("sign manifest: %w (the repaired manifest was removed; retry once signing works)", serr)
		}
	}
	res.Manifest = m
	return res, nil
}

// decompressForRepair streams the zst once: it hashes the decompressed bytes,
// counts them, and copies them to a temp file OUTSIDE the vault for the
// adapter's Inspect. A zst that does not decode is an error and nothing is left
// behind. The caller removes the returned temp file.
func decompressForRepair(ctx context.Context, zstPath string) (sum string, n int64, tmpPath string, err error) {
	f, err := os.Open(zstPath)
	if err != nil {
		return "", 0, "", fmt.Errorf("archive repair: open archive: %w", err)
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return "", 0, "", fmt.Errorf("archive repair: init zstd reader: %w", err)
	}
	defer dec.Close()
	tmp, err := os.CreateTemp("", "vp-repair-*.jsonl")
	if err != nil {
		return "", 0, "", fmt.Errorf("archive repair: temp file: %w", err)
	}
	h := sha256.New()
	n, err = io.Copy(io.MultiWriter(h, tmp), &contextReader{ctx: ctx, r: dec})
	cerr := tmp.Close()
	if err != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		if err == nil {
			err = cerr
		}
		return "", 0, "", fmt.Errorf("archive repair: the archive does not decompress cleanly: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, tmp.Name(), nil
}

// SignOptionsForProject reads the project's [archive] signing configuration, the
// one place the CLI and the MCP tool both get it from.
func SignOptionsForProject(vaultRoot, project string) (SignOptions, error) {
	cfg, err := storage.NewVault(vaultRoot).LoadConfig(project)
	if err != nil {
		return SignOptions{}, fmt.Errorf("load archive config: %w", err)
	}
	return SignOptions{Mode: cfg.Archive.SignMode, Key: cfg.Archive.SignKey, Namespace: cfg.Archive.SignNamespace}, nil
}
