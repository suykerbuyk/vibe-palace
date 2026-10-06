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
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

var vaultCopyFlags = []cli.FlagDef{
	{Name: "--from", Arg: "URL", Help: "The source vault's PUBLISHED git remote URL (never a host path)"},
	{Name: "--at", Arg: "SHA", Help: "The source commit the dry run planned against (the dry run prints it)"},
	vaultRootFlag,
	{Name: "--dry-run", Help: "Plan only: print the file list, every refusal, the push targets, the digest and the real-run command line"},
	{Name: "--expect", Arg: "DIGEST", Help: "Refuse unless the plan still digests to DIGEST (the dry run prints it); with --as it binds the copy half"},
	{Name: "--as", Arg: "NEWNAME", Help: "Copy the single named project under a new slug: copy, then rename it in this vault (U11). Requires exactly one project"},
	{Name: "--json", Help: "Print the plan or result as JSON"},
}

// cmdVaultCopy is the thin CLI over storage.PlanCopy / storage.ApplyCopy, the
// same functions the MCP tool vp_vault_copy calls. Every refusal lives there.
func cmdVaultCopy() *cli.Command {
	var c *cli.Command
	c = &cli.Command{
		Name:     "vault copy",
		Synopsis: "vp vault copy <project>... --from <remote-url> [--at <sha>] [--vault <path>] [--dry-run] [--expect <digest>] [--json]",
		Description: "Copy projects from another vault's PUBLISHED git remote into this vault (receiver-run): " +
			"the source is read only through a private blobless snapshot of its remote, never by host path. " +
			"The run copies each project's footprint, makes ONE commit with Vp-Copy-* trailers, checks the " +
			"committed footprint hash against the source's, and publishes exactly that commit to every remote " +
			"of this vault, or refuses and rolls back. Run --dry-run first and paste the command line it prints. " +
			"There is no --no-push. Undo: git revert the copy commit and push it.",
		Flags: vaultCopyFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault copy qa-metabuild-system orchestrator --from git@github.com:me/vibe-palace-vault.git --vault ~/quantum-vibe-palace-vault --dry-run", Comment: "Plan the copy, then run the line it prints"},
		},
		Run: func(args []string) int {
			return runVaultCopy(c, args, os.Stdout, os.Stderr)
		},
	}
	return c
}

func runVaultCopy(c *cli.Command, args []string, out, errOut io.Writer) int {
	fv, err := cli.ParseFlags(vaultCopyFlags, args)
	if err != nil {
		fmt.Fprintf(errOut, "vp vault copy: %v\n", err)
		return cli.ExitUser
	}
	if len(fv.Args()) == 0 {
		fmt.Fprintln(errOut, "vp vault copy: name at least one project")
		return cli.ExitUser
	}
	if fv.Get("--from") == "" {
		fmt.Fprintln(errOut, "vp vault copy: --from is required")
		return cli.ExitUser
	}
	root, code := vaultRootFor(c, fv.Get("--vault"), "copy")
	if code != cli.ExitOK {
		return code
	}
	req := storage.CopyRequest{
		Vault: root, Projects: fv.Args(), From: fv.Get("--from"),
		At: fv.Get("--at"), Expect: fv.Get("--expect"), DryRun: fv.Bool("--dry-run"),
	}
	if newName := fv.Get("--as"); newName != "" {
		return runVaultCopyAs(root, req, newName, fv.Bool("--json"), fv.Bool("--dry-run"), out, errOut)
	}
	res, err := storage.ApplyCopy(req)
	// After the exact publish, on this host only, and never failing the copy:
	// the incoming archives join this host's baseline set.
	var warnings []string
	if err == nil && !req.DryRun {
		warnings = tools.AddIncomingArchivesToBaseline(context.Background(), storage.NewVault(root), req.Projects)
	}
	if fv.Bool("--json") {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		payload := map[string]any{"result": res}
		if err != nil {
			payload["error"] = err.Error()
		}
		if len(warnings) > 0 {
			payload["baseline_warnings"] = warnings
		}
		_ = enc.Encode(payload)
	} else if res != nil {
		printCopy(out, res, req.DryRun)
	}
	for _, w := range warnings {
		fmt.Fprintf(errOut, "vp vault copy: warning: %s\n", w)
	}
	if err != nil {
		fmt.Fprintf(errOut, "vp vault copy: %v\n", err)
		var pending *storage.LifecyclePendingError
		if errors.Is(err, storage.ErrCopyRefused) || errors.As(err, &pending) {
			return cli.ExitUser
		}
		return cli.ExitSystem
	}
	return cli.ExitOK
}

// runVaultCopyAs drives the U11 copy-then-rename composition (tools.CopyProjectAs)
// for `vp vault copy <project> --from <url> --as <newname>`.
func runVaultCopyAs(root string, req storage.CopyRequest, newName string, asJSON, dryRun bool, out, errOut io.Writer) int {
	if len(req.Projects) != 1 {
		fmt.Fprintln(errOut, "vp vault copy --as: name exactly one project (you cannot copy a batch under one new name)")
		return cli.ExitUser
	}
	car, err := tools.CopyProjectAs(context.Background(), storage.NewVault(root), req, newName)
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		payload := map[string]any{"copy_as": car}
		if err != nil {
			payload["error"] = err.Error()
		}
		_ = enc.Encode(payload)
	} else if car != nil {
		if dryRun {
			fmt.Fprintln(out, "DRY RUN — nothing was written to the vault")
		}
		fmt.Fprintf(out, "copy --as: %s -> %s: %s\n", car.Project, car.NewName, car.Outcome)
		if car.Copy != nil && car.Copy.Plan != nil && dryRun {
			fmt.Fprintf(out, "copy digest: %s\n(the rename to %s runs after the copy and cannot be previewed)\n", car.Copy.Plan.Digest, car.NewName)
		}
		if car.Copy != nil && car.Copy.Commit != "" {
			fmt.Fprintf(out, "copied commit:  %s\n", car.Copy.Commit)
		}
		if car.Rename != nil && car.Rename.Commit != "" {
			fmt.Fprintf(out, "renamed commit: %s\n", car.Rename.Commit)
		}
		for _, w := range car.BaselineWarnings {
			fmt.Fprintf(errOut, "vp vault copy --as: warning: %s\n", w)
		}
	}
	if err != nil {
		fmt.Fprintf(errOut, "vp vault copy --as: %v\n", err)
		var pending *storage.LifecyclePendingError
		if errors.Is(err, storage.ErrCopyRefused) || errors.Is(err, storage.ErrRenameRefused) || errors.As(err, &pending) {
			return cli.ExitUser
		}
		return cli.ExitSystem
	}
	return cli.ExitOK
}

func printCopy(out io.Writer, res *storage.CopyResult, dryRun bool) {
	if res.Redo == storage.RedoPublished {
		fmt.Fprintf(out, "finished an interrupted copy: published %s\n", res.Commit)
		printCopyUndo(out, res.Undo)
		return
	}
	if res.Redo == storage.RedoRolledBack {
		fmt.Fprintln(out, "rolled back an interrupted copy that had not committed; copied afresh")
	}
	p := res.Plan
	if p == nil {
		return
	}
	if dryRun {
		fmt.Fprintln(out, "DRY RUN — nothing was written to the vault")
	}
	fmt.Fprintf(out, "vault:  %s\nsource: %s (%s @ %s)\nat:     %s\n", p.Vault, p.Source, p.SourceBranch, p.SourceTip, p.At)
	for _, pr := range p.Projects {
		note := ""
		if pr.ProjectsOnly {
			note = " (Projects/ only)"
		}
		fmt.Fprintf(out, "project %s: %d files, %d bytes, footprint %s%s\n", pr.Slug, pr.Files, pr.Bytes, pr.Footprint, note)
	}
	if dryRun {
		fmt.Fprintf(out, "files (%d, %d bytes):\n", len(p.Files), p.Bytes)
		for _, f := range p.Files {
			fmt.Fprintf(out, "  %s\n", f.Path)
		}
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
	fmt.Fprintf(out, "copied and published %s to %v\n", res.Commit, p.PushTargets)
	printCopyUndo(out, res.Undo)
}

func printCopyUndo(out io.Writer, undo []string) {
	if len(undo) == 0 {
		return
	}
	fmt.Fprintln(out, "undo:")
	for _, l := range undo {
		fmt.Fprintf(out, "  %s\n", l)
	}
}
