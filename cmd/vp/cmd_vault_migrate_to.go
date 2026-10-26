// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/departure"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

// cmd_vault_migrate_to.go is `vp vault migrate-to <dest>`: a CLI-only, staged
// convenience wrapper over `vault copy`, `vault project delete` and `config
// bind` (design migrate-to-design-v3.md). It invents no mechanism — every
// mutation is an existing storage call — and the hard stop between the copy and
// the partially-irreversible delete is a CLI gate (separate `--copy` / `--delete`
// invocations, refused together), NOT a storage helper that runs both.
//
// v1 scope (ratified): run from inside the project's checkout; the source must be
// this host's DEFAULT vault (resolver source prefix `global:`); no MCP surface.

var vaultMigrateToFlags = []cli.FlagDef{
	{Name: "--copy", Help: "Stage 2: perform the copy into <dest>, then dry-run the delete and HARD STOP"},
	{Name: "--delete", Help: "Stage 3: preflight the bind, perform the delete, then bind; print the fleet follow-up"},
	{Name: "--expect-copy", Arg: "DIGEST", Help: "Refuse the copy unless its plan still digests to DIGEST (the plan prints it)"},
	{Name: "--expect-delete", Arg: "DIGEST", Help: "Refuse the delete unless its plan still digests to DIGEST (--copy prints it)"},
	{Name: "--from-remote", Arg: "NAME", Help: "Source published remote to read (default: the first in the source's push order)"},
	{Name: "--to-remote", Arg: "NAME", Help: "Destination published remote to publish/verify against (default: the first in the dest's push order)"},
	{Name: "--checkout", Arg: "DIR", Help: "Checkout to bind-verify (default: the current directory)"},
	{Name: "--allow-unlabelled", Help: "Passthrough to config bind: bind although the departure record names no destination"},
	{Name: "--json", Help: "Print the plan or result as JSON"},
}

// cmdVaultMigrateTo wires `vp vault migrate-to`. Registered mutates(): it writes
// vault content (copy into dest, delete from source) and the host config (bind).
func cmdVaultMigrateTo() *cli.Command {
	var c *cli.Command
	c = &cli.Command{
		Name: "vault migrate-to",
		Synopsis: "vp vault migrate-to <dest-vault> [--copy | --delete] [--expect-copy <d>] [--expect-delete <d>] " +
			"[--from-remote <n>] [--to-remote <n>] [--checkout <dir>] [--allow-unlabelled] [--json]",
		Description: "Move the project of the CURRENT checkout into another vault, as a staged wrapper over " +
			"`vault copy`, `vault project delete` and `config bind`. The bare verb PLANS only (writes no git tip). " +
			"`--copy` copies into <dest> (pushing to every configured remote), then prints the delete plan and STOPS " +
			"before the partially-irreversible delete. `--delete` preflights the bind, deletes from the source, and " +
			"binds this host to <dest> in the global [project_vaults] (never writing vault_path into the tracked marker). " +
			"`--copy --delete` together is refused. v1: run inside the checkout; the source must be this host's default " +
			"vault; destination creation is out of scope (use `vp vault init` / `vp vault clone`).",
		Flags: vaultMigrateToFlags,
		Examples: []cli.Example{
			{Cmd: "cd ~/code/my-project && vp vault migrate-to ~/quantum-vibe-palace-vault", Comment: "Plan the move; it prints the --copy line to run next"},
		},
		Run: func(args []string) int {
			return runVaultMigrateTo(c, args, os.Stdout, os.Stderr)
		},
	}
	return c
}

// migrateContext is everything the three stages resolve once, from the checkout.
type migrateContext struct {
	cwd           string
	source        string // the resolved (default) source vault root
	slug          string
	dest          string // cleaned absolute destination vault root
	checkout      string // the tree config bind verifies (--checkout or cwd)
	srcRemoteURL  string // --from for the copy
	destRemoteURL string // --moved-to for the delete AND the URL bind matches
	srcOrder      []string
	destOrder     []string
}

func runVaultMigrateTo(c *cli.Command, args []string, out, errOut io.Writer) int {
	const name = "vp vault migrate-to"
	fv, err := cli.ParseFlags(vaultMigrateToFlags, args)
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", name, err)
		return cli.ExitUser
	}
	doCopy, doDelete := fv.Bool("--copy"), fv.Bool("--delete")
	// The hard stop lives here: the copy and the partially-irreversible delete
	// are separate, deliberate invocations and can never be one.
	if doCopy && doDelete {
		fmt.Fprintf(errOut, "%s: --copy and --delete cannot be combined; run --copy, review the delete plan, then run --delete\n", name)
		return cli.ExitUser
	}
	if len(fv.Args()) != 1 {
		fmt.Fprintf(errOut, "%s: name exactly one destination vault\n", name)
		return cli.ExitUser
	}

	mctx, done, code := resolveMigrateContext(name, fv, fv.Args()[0], out, errOut)
	if done || code != cli.ExitOK {
		return code
	}

	switch {
	case doCopy:
		return migrateStageCopy(name, mctx, fv, out, errOut)
	case doDelete:
		return migrateStageDelete(name, mctx, fv, out, errOut)
	default:
		return migrateStagePlan(name, mctx, fv, out, errOut)
	}
}

// resolveMigrateContext runs the common resolution (steps 1–4). done=true with
// ExitOK is the "already bound" no-op.
func resolveMigrateContext(name string, fv *cli.FlagValues, destArg string, out, errOut io.Writer) (*migrateContext, bool, int) {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", name, err)
		return nil, false, cli.ExitSystem
	}
	res, err := storage.ResolveVaultBinding(cwd)
	if err != nil {
		fmt.Fprintf(errOut, "%s: cannot resolve this checkout's vault: %v\n", name, err)
		return nil, false, cli.ExitUser
	}

	dest, err := cleanAbsVaultArg(destArg)
	if err != nil {
		fmt.Fprintf(errOut, "%s: destination %q: %v\n", name, destArg, err)
		return nil, false, cli.ExitUser
	}
	// No-op: already bound to <dest>. Checked BEFORE the default-source refusal so
	// a fully-migrated checkout (now resolving `binding:` to dest) is reported
	// done, not refused — this is what makes a completed run idempotent.
	if storage.SameVaultRoot(res.Path, dest) {
		fmt.Fprintf(out, "already bound to %s; nothing to do\n", dest)
		return nil, true, cli.ExitOK
	}

	// v1 scope: default-source-only. Any source that is not the host default
	// vault (tier-1 `cwd:`/`cwd-local:` override, or a tier-2 `binding:`) breaks
	// config bind's moved-mode preconditions (it checks the departure record in,
	// and refuses re-pointing away from, the DEFAULT vault).
	if !strings.HasPrefix(res.Source, "global:") {
		fmt.Fprintf(errOut, "%s: this checkout resolves its vault via %q, not the host's default vault.\n"+
			"  migrate-to v1 only moves a project OUT of the default vault. A tier-2 binding or a throwaway\n"+
			"  .vibe-palace/local.toml override (e.g. one already naming %s) resolves non-default and is refused.\n"+
			"  Move it by hand with `vp vault copy` / `vp vault project delete` / `vp config bind`, or remove the override.\n",
			name, res.Source, destArg)
		return nil, false, cli.ExitUser
	}
	m, err := project.LocateMarker(cwd)
	if err != nil || m.Path == "" {
		fmt.Fprintf(errOut, "%s: this checkout has no readable .vibe-palace.toml identity marker (%v)\n", name, err)
		return nil, false, cli.ExitUser
	}
	if m.Name == "" || m.NameErr != nil {
		fmt.Fprintf(errOut, "%s: the marker %s names no valid project ([project].name); set it before migrating\n", name, m.Path)
		return nil, false, cli.ExitUser
	}

	// Destination must already be a vault at the required data format. Creation
	// is out of scope: an absent directory or one with no vault.toml reads as
	// format 0 and is "not a vault".
	if format, ferr := surface.ReadFormat(dest); ferr != nil || format == 0 {
		detail := "it has no .vibe-palace/vault.toml"
		if ferr != nil {
			detail = ferr.Error()
		}
		fmt.Fprintf(errOut, "%s: %s is not a destination vault (%s).\n"+
			"  Creating it is out of scope; make it first with `vp vault init` or `vp vault clone`, then re-run.\n",
			name, dest, detail)
		return nil, false, cli.ExitUser
	} else if format != surface.RequiredDataFormat {
		fmt.Fprintf(errOut, "%s: %s is at data format %d; this binary requires %d\n", name, dest, format, surface.RequiredDataFormat)
		return nil, false, cli.ExitUser
	}

	mctx := &migrateContext{cwd: cwd, source: res.Path, slug: m.Name, dest: dest}
	checkout := cwd
	if co := fv.Get("--checkout"); co != "" {
		abs, aerr := cleanAbsPath(co)
		if aerr != nil {
			fmt.Fprintf(errOut, "%s: --checkout %q: %v\n", name, co, aerr)
			return nil, false, cli.ExitUser
		}
		checkout = abs
	}
	mctx.checkout = checkout

	// Discovery: full push blast radius (every configured remote), then the
	// source/dest published-remote URLs to feed copy/delete.
	if mctx.srcOrder, err = storage.PublishOrder(res.Path); err != nil {
		fmt.Fprintf(errOut, "%s: read the source vault's push order: %v\n", name, err)
		return nil, false, cli.ExitUser
	}
	if mctx.destOrder, err = storage.PublishOrder(dest); err != nil {
		fmt.Fprintf(errOut, "%s: read the destination vault's push order: %v\n", name, err)
		return nil, false, cli.ExitUser
	}
	if len(mctx.srcOrder) == 0 {
		fmt.Fprintf(errOut, "%s: the source vault %s has no configured git remote; a published-remote move needs one\n", name, res.Path)
		return nil, false, cli.ExitUser
	}
	if len(mctx.destOrder) == 0 {
		fmt.Fprintf(errOut, "%s: the destination vault %s has no configured git remote; a published-remote move needs one\n", name, dest)
		return nil, false, cli.ExitUser
	}
	if mctx.srcRemoteURL, err = pickRemoteURL(res.Path, mctx.srcOrder, fv.Get("--from-remote")); err != nil {
		fmt.Fprintf(errOut, "%s: choose the source remote: %v\n", name, err)
		return nil, false, cli.ExitUser
	}
	if mctx.destRemoteURL, err = pickRemoteURL(dest, mctx.destOrder, fv.Get("--to-remote")); err != nil {
		fmt.Fprintf(errOut, "%s: choose the destination remote: %v\n", name, err)
		return nil, false, cli.ExitUser
	}
	return mctx, false, cli.ExitOK
}

// pickRemoteURL chooses a published-remote URL for a vault. With chosen!="" the
// name must be in the push order; otherwise the first push-order name with a
// usable URL is taken. The recorded remotes.toml spelling wins (so copy's redo
// line stays byte-stable); a configured-but-unrecorded name falls back to its
// live URL (the empty-remotes.toml edge).
func pickRemoteURL(root string, order []string, chosen string) (string, error) {
	recorded, err := storage.ReadRecordedRemotes(root)
	if err != nil {
		return "", err
	}
	recURL := map[string]string{}
	for _, r := range recorded {
		recURL[r.Name] = r.URL
	}
	urlFor := func(nm string) (string, error) {
		if u, ok := recURL[nm]; ok && u != "" {
			return u, nil
		}
		return storage.ConfiguredRemoteURL(root, nm)
	}
	if chosen != "" {
		found := false
		for _, nm := range order {
			if nm == chosen {
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("remote %q is not in the vault's push order %v", chosen, order)
		}
		return urlFor(chosen)
	}
	var lastErr error
	for _, nm := range order {
		if u, err := urlFor(nm); err == nil && u != "" {
			return u, nil
		} else if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no usable remote URL among %v", order)
}

// --- Stage A: PLAN ---------------------------------------------------------

func migrateStagePlan(name string, m *migrateContext, fv *cli.FlagValues, out, errOut io.Writer) int {
	plan, err := storage.PlanCopy(storage.CopyRequest{Vault: m.dest, Projects: []string{m.slug}, From: m.srcRemoteURL})
	if err != nil {
		fmt.Fprintf(errOut, "%s: plan the copy: %v\n", name, err)
		if errors.Is(err, storage.ErrCopyRefused) {
			return cli.ExitUser
		}
		return cli.ExitSystem
	}
	if fv.Bool("--json") {
		if jerr := jsonEncode(out, map[string]any{
			"stage": "plan", "project": m.slug, "source": m.source, "destination": m.dest,
			"copy_from": m.srcRemoteURL, "moved_to": m.destRemoteURL,
			"dest_push_targets": m.destOrder, "source_push_targets": m.srcOrder,
			"copy_plan": plan,
			"next":      migrateNextLine(m.dest, "--copy", "--expect-copy", plan.Digest),
		}); jerr != nil {
			fmt.Fprintf(errOut, "%s: %v\n", name, jerr)
			return cli.ExitSystem
		}
		if len(plan.Refusals) > 0 {
			return cli.ExitUser
		}
		return cli.ExitOK
	}
	fmt.Fprintf(out, "migrate-to PLAN (nothing is written to any git tip)\n")
	fmt.Fprintf(out, "project:     %s\nsource:      %s\ndestination: %s\n", m.slug, m.source, m.dest)
	fmt.Fprintf(out, "copy --from: %s\n", m.srcRemoteURL)
	printBlastRadius(out, m)
	fmt.Fprintf(out, "copy: %d file(s), %d bytes\n", len(plan.Files), plan.Bytes)
	if len(plan.Refusals) > 0 {
		fmt.Fprintf(out, "\nthe copy would be REFUSED (%d):\n", len(plan.Refusals))
		for _, r := range plan.Refusals {
			fmt.Fprintf(out, "  - %s\n", r)
		}
		return cli.ExitUser
	}
	fmt.Fprintf(out, "copy digest: %s\n", plan.Digest)
	fmt.Fprintf(out, "\nTo perform the copy (reversible by git revert) and see the delete plan:\n  %s\n",
		migrateNextLine(m.dest, "--copy", "--expect-copy", plan.Digest))
	return cli.ExitOK
}

// --- Stage B: COPY, then plan the delete and HARD STOP ---------------------

func migrateStageCopy(name string, m *migrateContext, fv *cli.FlagValues, out, errOut io.Writer) int {
	asJSON := fv.Bool("--json")
	if code := enforceSurfaceOnRoot(m.dest); code != cli.ExitOK {
		return code
	}
	// Resume: skip the copy only when the dest already holds a verified copy of
	// the CURRENT source HEAD (a false probe — including source moved — falls
	// through to ApplyCopy, whose own redo/short-circuit handles it).
	present, commit, err := storage.VerifiedCopyAt(m.source, m.destRemoteURL, m.slug)
	if err != nil {
		fmt.Fprintf(errOut, "%s: probe the destination for an existing copy: %v\n", name, err)
		return cli.ExitUser
	}
	var copyResult *storage.CopyResult
	if present {
		if !asJSON {
			fmt.Fprintf(out, "copy already present at %s (commit %s); skipping the copy\n", m.dest, commit)
		}
	} else {
		res, cerr := storage.ApplyCopy(storage.CopyRequest{
			Vault: m.dest, Projects: []string{m.slug}, From: m.srcRemoteURL, Expect: fv.Get("--expect-copy"),
		})
		if cerr != nil {
			fmt.Fprintf(errOut, "%s: copy: %v\n", name, cerr)
			var pending *storage.LifecyclePendingError
			if errors.Is(cerr, storage.ErrCopyRefused) || errors.As(cerr, &pending) {
				return cli.ExitUser
			}
			return cli.ExitSystem
		}
		copyResult = res
		if !asJSON {
			if res.Commit != "" {
				fmt.Fprintf(out, "copied and published %s\n", res.Commit)
			}
			for _, l := range res.Undo {
				fmt.Fprintf(out, "undo: %s\n", l)
			}
		}
	}
	// In-process post-step the copy CLI runs (baseline_add): never fatal. Run it on
	// BOTH paths — including the probe-skip resume — since a crash after publish but
	// before the add would otherwise never retry it. It is idempotent.
	for _, w := range tools.AddIncomingArchivesToBaseline(context.Background(), storage.NewVault(m.dest), []string{m.slug}) {
		fmt.Fprintf(errOut, "%s: warning: %s\n", name, w)
	}
	if !asJSON {
		printBlastRadius(out, m)
	}

	// Plan the delete and HARD STOP.
	dplan, derr := storage.PlanDelete(m.source, storage.DeleteRequest{Projects: []string{m.slug}, MovedTo: m.destRemoteURL})
	if derr != nil {
		fmt.Fprintf(errOut, "%s: plan the delete: %v\n", name, derr)
		return cli.ExitUser
	}
	if asJSON {
		if jerr := jsonEncode(out, map[string]any{
			"stage": "copy", "project": m.slug, "copy_skipped": present, "copy_commit": commit,
			"copy_result": copyResult, "delete_plan": dplan, "moved_to": m.destRemoteURL,
			"hard_stop": true, "next": migrateNextLine(m.dest, "--delete", "--expect-delete", dplan.Digest),
		}); jerr != nil {
			fmt.Fprintf(errOut, "%s: %v\n", name, jerr)
			return cli.ExitSystem
		}
		return cli.ExitOK
	}
	fmt.Fprintf(out, "\n--- DELETE PLAN (source %s) ---\n", m.source)
	fmt.Fprintf(out, "moved-to (written as the departure 'To'): %s\n", m.destRemoteURL)
	for _, pp := range dplan.Projects {
		fmt.Fprintf(out, "project %s: %d tracked file(s)\n", pp.Project, len(pp.Tracked))
	}
	fmt.Fprintf(out, "NOT restorable by git revert (%d):\n", len(dplan.Leftovers))
	for _, l := range dplan.Leftovers {
		fmt.Fprintf(out, "  %s (%s, %d bytes)\n", l.Path, l.Class, l.Size)
	}
	for _, s := range dplan.IndexStores {
		fmt.Fprintf(out, "  host-local index store (removed after the delete, NOT restorable by git revert): %s\n", s)
	}
	fmt.Fprintf(out, "delete push targets: %v\ndelete digest: %s\n", dplan.Remotes, dplan.Digest)
	fmt.Fprintf(out, "\n🔴 HARD STOP — the copy is done and reversible (git revert + push).\n")
	fmt.Fprintf(out, "   The next step DELETES %s from %s, publishes to every remote above, and is PARTIALLY\n", m.slug, m.source)
	fmt.Fprintf(out, "   IRREVERSIBLE: its leftovers and the host-local index store cannot be restored by git revert\n")
	fmt.Fprintf(out, "   once every remote holds the commit.\n")
	fmt.Fprintf(out, "\nTo delete from the source and bind this host to the destination:\n  %s\n",
		migrateNextLine(m.dest, "--delete", "--expect-delete", dplan.Digest))
	return cli.ExitOK
}

// --- Stage C: preflight bind, delete, bind, fleet --------------------------

func migrateStageDelete(name string, m *migrateContext, fv *cli.FlagValues, out, errOut io.Writer) int {
	asJSON := fv.Bool("--json")
	if code := enforceSurfaceOnRoot(m.source); code != cli.ExitOK {
		return code
	}
	if code := migrateDeletePreflight(name, m, errOut); code != cli.ExitOK {
		return code
	}

	// Step 12: always call the idempotent ApplyDelete (it finishes an unfinished
	// run; a fully-departed clean project returns ErrNothingLeft, which we treat
	// as "already done"). Never a hand-rolled skip.
	res, derr := storage.ApplyDelete(m.source, storage.DeleteRequest{
		Projects: []string{m.slug}, MovedTo: m.destRemoteURL, Expect: fv.Get("--expect-delete"),
	})
	deleteAlreadyDone := false
	switch {
	case derr == nil:
		if !asJSON {
			if res.Commit != "" {
				fmt.Fprintf(out, "deleted and published %s\n", res.Commit)
			}
			fmt.Fprintf(out, "leftovers removed: %d file(s), %d dir(s)\n", res.FilesRemoved, res.DirsRemoved)
			for _, l := range res.Undo {
				fmt.Fprintf(out, "undo (restores everything but the leftovers): %s\n", l)
			}
		}
	case errors.Is(derr, storage.ErrNothingLeft):
		// The primitive WRAPS the sentinel ("refusing: … %w", errNothingLeft), so
		// this MUST stay errors.Is, never ==. If the primitive ever switched to %v,
		// this match would silently fail and a successful delete would return
		// ExitUser instead of proceeding to bind.
		deleteAlreadyDone = true
		if !asJSON {
			fmt.Fprintf(out, "the delete already landed (%s fully departed, nothing left); continuing\n", m.slug)
		}
	default:
		fmt.Fprintf(errOut, "%s: delete: %v\n", name, derr)
		return cli.ExitUser
	}

	// In-process post-step the delete CLI runs: remove the host-local index store
	// (idempotent; not git-revert-able).
	removal := indexstore.RemoveGoneProjects(context.Background(), storage.NewVault(m.source), []string{m.slug}, indexstore.LifecycleRemovalTimeout)
	if !asJSON {
		for _, r := range removal {
			if r.Error != "" {
				fmt.Fprintf(out, "index store %s: kept (%s); the next index sweep retries it\n", r.Path, r.Error)
				continue
			}
			fmt.Fprintf(out, "index store %s: %s\n", r.Path, r.Outcome)
		}
	}

	// Step 13: bind this host to <dest> in the global [project_vaults]. Never
	// writes vault_path into the tracked marker.
	rep, berr := storage.BindProjectVaults(storage.BindVaultsRequest{
		Slugs: []string{m.slug}, VaultPath: m.dest, Mode: storage.BindMoved,
		Checkouts: []string{m.checkout}, AllowUnlabelled: fv.Bool("--allow-unlabelled"),
	})
	if berr != nil {
		fmt.Fprintf(errOut, "%s: the delete landed but the bind failed: %v\n", name, berr)
		fmt.Fprintf(errOut, "  Re-run `%s` once the cause is cleared; it re-runs the idempotent delete and retries the bind.\n",
			migrateNextLine(m.dest, "--delete", "--expect-delete", ""))
		return cli.ExitUser
	}
	if asJSON {
		if jerr := jsonEncode(out, map[string]any{
			"stage": "delete", "project": m.slug, "delete_already_done": deleteAlreadyDone,
			"delete_result": res, "index_removal": removal, "bind": rep,
			"fleet": map[string]any{"pull_first": true, "dest_remote": m.destRemoteURL, "dest": m.dest},
		}); jerr != nil {
			fmt.Fprintf(errOut, "%s: %v\n", name, jerr)
			return cli.ExitSystem
		}
		return cli.ExitOK
	}
	fmt.Fprintf(out, "bound %s -> %s (config %s)\n", m.slug, rep.Vault, rep.ConfigPath)
	if rep.BackupPath != "" {
		fmt.Fprintf(out, "config backup: %s\n", rep.BackupPath)
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	printFleetFollowup(out, m)
	return cli.ExitOK
}

// migrateDeletePreflight runs every BindProjectVaults refusal that is knowable
// BEFORE the departure record exists, plus the stricter existing-record guard,
// so a bind failure cannot strand the project deleted-but-unbound after the
// irreversible delete. The one precondition it cannot check is "the default
// vault holds the moved-to-vault record" — this delete is what writes it.
func migrateDeletePreflight(name string, m *migrateContext, errOut io.Writer) int {
	refuse := func(format string, a ...any) int {
		fmt.Fprintf(errOut, "%s: refusing before the irreversible delete: %s\n", name, fmt.Sprintf(format, a...))
		return cli.ExitUser
	}

	// Existing-record guard (do NOT re-derive the delete mode): a record already
	// present whose To names a different place means this project departed
	// elsewhere — refuse, do not skip and do not re-delete.
	if rec, found := departure.Read(m.source, m.slug); found {
		if rec.Malformed != "" {
			return refuse("the source already holds an unreadable departure record for %s (%s)", m.slug, rec.Malformed)
		}
		if rec.Kind != departure.MovedToVault {
			return refuse("the source already holds a %q departure record for %s, not a move to another vault", rec.Kind, m.slug)
		}
		recNorm, ok := storage.NormaliseRemoteURL(rec.To)
		wantNorm, ok2 := storage.NormaliseRemoteURL(m.destRemoteURL)
		if !ok || !ok2 || recNorm != wantNorm {
			return refuse("%s already departed the source to %q, which is not the selected destination %q", m.slug, rec.To, m.destRemoteURL)
		}
	}

	// The slug must not already be bound to a DIFFERENT vault in the global
	// [project_vaults]: checkBindSlug refuses a re-point, and it would do so only
	// AFTER the irreversible delete. Same target is fine (the bind is a no-op); a
	// different or unreadable entry is refused here. (The resolver guards the
	// forward path too — a valid foreign binding makes the source resolve
	// `binding:`, refused at stage-common — but this is the explicit, layer-local
	// check the design requires before ApplyDelete.)
	if boundRoot, bound, berr := storage.ProjectVaultBinding(m.slug); berr != nil {
		return refuse("%s already has a [project_vaults] entry for %s that cannot be used (%v); fix or remove it by hand", "the global config", m.slug, berr)
	} else if bound && !storage.SameVaultRoot(boundRoot, m.dest) {
		return refuse("%s is already bound to %s in [project_vaults], not the destination %s; re-pointing a binding is refused — edit the config by hand",
			m.slug, boundRoot, m.dest)
	}

	// dest is not this host's default vault.
	global, _, gerr := storage.ResolveGlobalVaultPath()
	if gerr != nil {
		return refuse("cannot resolve this host's default vault: %v", gerr)
	}
	if storage.SameVaultRoot(global, m.dest) {
		return refuse("%s is this host's default vault; a binding names a DIFFERENT vault", m.dest)
	}

	// The checkout marker exists and names this slug.
	cm, cerr := project.LocateMarker(m.checkout)
	if cerr != nil || cm.Path == "" {
		return refuse("the checkout %s has no readable .vibe-palace.toml (%v)", m.checkout, cerr)
	}
	if cm.Name != m.slug {
		return refuse("the checkout %s names project %q, not %q", m.checkout, cm.Name, m.slug)
	}
	// The checkout must NOT resolve via a tier-1 override: a .vibe-palace/local.toml
	// or untracked-marker vault_path wins over the binding and would fail config
	// bind's post-write verify AFTER the delete. A pre-bind checkout resolves
	// `global:` (becomes `binding:` once bound); a resume re-run resolves
	// `binding:` to this same dest — both are fine.
	cres, rerr := storage.ResolveVaultBinding(m.checkout)
	if rerr != nil {
		return refuse("the checkout %s does not resolve a vault: %v", m.checkout, rerr)
	}
	switch {
	case strings.HasPrefix(cres.Source, "cwd-local:"), strings.HasPrefix(cres.Source, "cwd:"):
		return refuse("the checkout %s resolves via %q (a tier-1 override); remove it so the new binding (tier 2) can win", m.checkout, cres.Source)
	case strings.HasPrefix(cres.Source, "binding:") && !storage.SameVaultRoot(cres.Path, m.dest):
		return refuse("the checkout %s is already bound to %s, not the destination %s", m.checkout, cres.Path, m.dest)
	}

	// dest holds no departure record for the slug (a vault that records it as
	// departed is not its primary).
	if rec, found := departure.Read(m.dest, m.slug); found {
		why := string(rec.Kind)
		if rec.Malformed != "" {
			why = "unreadable (" + rec.Malformed + ")"
		}
		return refuse("the destination %s holds a departure record for %s (%s); bind it where it lives now", m.dest, m.slug, why)
	}

	// dest holds the copied content.
	if !destHoldsProject(m.dest, m.slug) {
		return refuse("the destination %s holds neither Projects/%s nor palace/%s; run --copy first", m.dest, m.slug, m.slug)
	}

	// F2: the To string we will write must normalise to a LIVE destination
	// remote (the exact test config bind runs), not merely a remotes.toml entry.
	wantNorm, ok := storage.NormaliseRemoteURL(m.destRemoteURL)
	if !ok {
		return refuse("the destination remote %q is not a URL config bind can match", m.destRemoteURL)
	}
	urls, uerr := storage.VaultRemoteURLs(m.dest)
	if uerr != nil {
		return refuse("read the destination's live remotes: %v", uerr)
	}
	matched := false
	for _, u := range urls {
		if n, ok := storage.NormaliseRemoteURL(u); ok && n == wantNorm {
			matched = true
			break
		}
	}
	if !matched {
		return refuse("the departure 'To' %q matches no live remote of %s (remotes: %v); config bind would refuse after the delete",
			m.destRemoteURL, m.dest, urls)
	}
	fmt.Fprintf(errOut, "%s: preflight ok; the departure 'To' will be written as: %s\n", name, m.destRemoteURL)
	return cli.ExitOK
}

// --- helpers ---------------------------------------------------------------

func printBlastRadius(out io.Writer, m *migrateContext) {
	fmt.Fprintf(out, "copy pushes to EVERY configured remote of the destination (incl. any not in remotes.toml): %v\n", m.destOrder)
	fmt.Fprintf(out, "delete pushes to EVERY configured remote of the source (incl. any not in remotes.toml):      %v\n", m.srcOrder)
}

func printFleetFollowup(out io.Writer, m *migrateContext) {
	fmt.Fprintf(out, "\nOther hosts that work on %s must, IN THIS ORDER, after this is pushed:\n", m.slug)
	fmt.Fprintf(out, "  1. vp vault pull                 # REQUIRED FIRST: brings the departure record into that host's\n")
	fmt.Fprintf(out, "                                   #   default vault; clone --bind needs it present\n")
	fmt.Fprintf(out, "  2a. vp vault clone %s %s --bind %s   # if that host has no destination vault yet\n", m.destRemoteURL, m.dest, m.slug)
	fmt.Fprintf(out, "  2b. # or, if it already has the destination cloned:\n")
	fmt.Fprintf(out, "      vp config bind %s --vault %s --checkout <their checkout>\n", m.slug, m.dest)
	fmt.Fprintf(out, "  3. Reload the AI host (the MCP server resolved its vault at startup).\n")
}

func migrateNextLine(dest, stageFlag, expectFlag, digest string) string {
	line := "vp vault migrate-to " + shellQuoteArg(dest) + " " + stageFlag
	if digest != "" {
		line += " " + expectFlag + " " + digest
	}
	return line
}

// destHoldsProject reports whether the vault at root holds tracked trees for
// slug (Projects/<slug> or palace/<slug>), mirroring vaultHoldsProject.
func destHoldsProject(root, slug string) bool {
	for _, tree := range storage.ProjectTrees(slug) {
		// IsDir to match vaultHoldsProject: a stray FILE at Projects/<slug> must
		// not pass the preflight and then fail bind after the delete.
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(tree))); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// cleanAbsVaultArg expands ~ and makes the destination vault path absolute and
// clean (boundVaultRoot-parity: config bind requires an absolute path).
func cleanAbsVaultArg(p string) (string, error) { return cleanAbsPath(p) }

func cleanAbsPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// shellQuoteArg quotes a printed command argument when it contains shell-unsafe
// characters, so the next-command lines can be pasted verbatim.
func shellQuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:@+=,~", r)) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

// jsonEncode is the shared --json encoder used by each stage's --json branch.
func jsonEncode(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
