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

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var vaultRenameFlags = []cli.FlagDef{
	vaultRootFlag,
	{Name: "--dry-run", Help: "Plan only: print the move set, the rewrite counts, every refusal, the push targets, the digest and the real-run command line"},
	{Name: "--expect", Arg: "DIGEST", Help: "Refuse unless the plan still digests to DIGEST (the dry run prints it)"},
	{Name: "--undo", Help: "Undo the host-local index step of an earlier rename, after you have git-reverted the rename commit (old back, new gone). It moves the index store back and re-labels it; the vault revert is yours to make and push"},
	{Name: "--json", Help: "Print the plan or result as JSON"},
}

// cmdVaultRename is the thin CLI over storage.PlanRename / storage.ApplyRename,
// the same functions the MCP tool vp_vault_rename calls. Every refusal lives there.
func cmdVaultRename() *cli.Command {
	var c *cli.Command
	c = &cli.Command{
		Name:     "vault rename",
		Synopsis: "vp vault rename <old> <new> [--vault <path>] [--dry-run] [--expect <digest>] [--json]",
		Description: "Rename a project from one slug to another INSIDE this vault: move its footprint " +
			"(Projects/<old>/ -> Projects/<new>/ and palace/<old>/ -> palace/<new>/), rewrite every stored " +
			"identifier, write a renamed departure record for the old slug, make ONE commit with Vp-Rename-* " +
			"trailers, check that nothing still names the old slug, and publish exactly that commit to every " +
			"remote of this vault, or refuse and roll back. Run --dry-run first and paste the command line it " +
			"prints. There is no --no-push. Undo: git revert the rename commit and push it. The checkout's " +
			"[project].name and any host binding are not touched; the departed-slug alert names those fixes.",
		Flags: vaultRenameFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault rename old-name new-name --dry-run", Comment: "Plan the rename, then run the line it prints"},
		},
		Run: func(args []string) int {
			return runVaultRename(c, args, os.Stdout, os.Stderr)
		},
	}
	return c
}

func runVaultRename(c *cli.Command, args []string, out, errOut io.Writer) int {
	fv, err := cli.ParseFlags(vaultRenameFlags, args)
	if err != nil {
		fmt.Fprintf(errOut, "vp vault rename: %v\n", err)
		return cli.ExitUser
	}
	if len(fv.Args()) != 2 {
		fmt.Fprintln(errOut, "vp vault rename: name exactly the old and new slugs: vp vault rename <old> <new>")
		return cli.ExitUser
	}
	root, code := vaultRootFor(c, fv.Get("--vault"), "rename")
	if code != cli.ExitOK {
		return code
	}
	if fv.Bool("--undo") {
		// The operator has git-reverted the rename commit; move the host-local
		// index store back. The two slugs are <old> <new>, as the forward run.
		hl, err := indexstore.UndoRenamedProject(context.Background(), storage.NewVault(root), fv.Args()[0], fv.Args()[1])
		if err != nil {
			fmt.Fprintf(errOut, "vp vault rename --undo: %v\n", err)
			return cli.ExitSystem
		}
		if fv.Bool("--json") {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"host_local": hl})
		} else {
			fmt.Fprintf(out, "undo host-local index step: %s (index renamed back: %t, imports carried back: %t)\n",
				hl.Outcome, hl.IndexRenamed, hl.ImportsCarried)
		}
		return cli.ExitOK
	}
	req := storage.RenameRequest{
		Vault: root, From: fv.Args()[0], To: fv.Args()[1],
		Expect: fv.Get("--expect"), DryRun: fv.Bool("--dry-run"),
	}
	res, err := storage.ApplyRename(req)
	// After the tracked rename publishes, run the host-local index step (storage
	// cannot import indexstore, so the CLI drives it, as delete does).
	var hostLocal *indexstore.RenameHostLocalResult
	if err == nil && !req.DryRun && res != nil && res.Commit != "" {
		hl, herr := indexstore.AdoptRenamedProject(context.Background(), storage.NewVault(root), req.From, req.To)
		if herr != nil {
			err = fmt.Errorf("the rename committed and published, but the host-local index step failed (re-run the rename, or run `vp index rebuild %s`): %w", req.To, herr)
		} else {
			hostLocal = &hl
		}
	}
	if fv.Bool("--json") {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		payload := map[string]any{"result": res}
		if hostLocal != nil {
			payload["host_local"] = hostLocal
		}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = enc.Encode(payload)
	} else if res != nil {
		printRename(out, res, req.DryRun)
		if hostLocal != nil {
			fmt.Fprintf(out, "host-local index step: %s (index renamed: %t, imports carried: %t)\n",
				hostLocal.Outcome, hostLocal.IndexRenamed, hostLocal.ImportsCarried)
		}
	}
	if err != nil {
		fmt.Fprintf(errOut, "vp vault rename: %v\n", err)
		var pending *storage.LifecyclePendingError
		if errors.Is(err, storage.ErrRenameRefused) || errors.As(err, &pending) {
			return cli.ExitUser
		}
		return cli.ExitSystem
	}
	return cli.ExitOK
}

func printRename(out io.Writer, res *storage.RenameResult, dryRun bool) {
	if res.Redo == storage.RedoPublished {
		fmt.Fprintf(out, "finished an interrupted rename: published %s\n", res.Commit)
		printRenameUndo(out, res.Undo)
		return
	}
	if res.Redo == storage.RedoRolledBack {
		fmt.Fprintln(out, "rolled back an interrupted rename that had not committed; renamed afresh")
	}
	p := res.Plan
	if p == nil {
		return
	}
	if dryRun {
		fmt.Fprintln(out, "DRY RUN — nothing was written to the vault")
	}
	fmt.Fprintf(out, "vault: %s\nrename: %s -> %s\n", p.Vault, p.From, p.To)
	fmt.Fprintf(out, "files moved: %d\n", p.Files)
	if dryRun {
		total := 0
		for _, n := range p.Rewrites {
			total += n
		}
		fmt.Fprintf(out, "identifier rewrites: %d\n", total)
	}
	fmt.Fprintf(out, "push targets: %v\ndigest: %s\n", p.PushTargets, p.Digest)
	if len(p.Refusals) > 0 {
		fmt.Fprintf(out, "\nREFUSED (%d):\n", len(p.Refusals))
		for _, r := range p.Refusals {
			fmt.Fprintf(out, "  - %s\n", r)
		}
		return
	}
	if dryRun {
		fmt.Fprintf(out, "\nTo run it:\n  %s\n", p.Command)
		return
	}
	fmt.Fprintf(out, "renamed and published %s to %v\n", res.Commit, p.PushTargets)
	printRenameUndo(out, res.Undo)
}

func printRenameUndo(out io.Writer, undo []string) {
	if len(undo) == 0 {
		return
	}
	fmt.Fprintln(out, "undo:")
	for _, l := range undo {
		fmt.Fprintf(out, "  %s\n", l)
	}
}
