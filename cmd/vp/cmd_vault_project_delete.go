// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

var vaultProjectDeleteFlags = []cli.FlagDef{
	{Name: "--moved-to", Arg: "URL", Help: "The destination vault's published remote; the delete refuses unless it holds a verified copy"},
	{Name: "--discard", Help: "Delete with no destination: the project is discarded"},
	vaultRootFlag,
	{Name: "--dry-run", Help: "Run every check and print the plan, the leftovers git revert cannot restore, and the digest; write nothing"},
	{Name: "--expect", Arg: "DIGEST", Help: "The digest the dry run printed; refuse on any mismatch"},
	{Name: "--json", Help: "Print the plan or result as JSON"},
}

// cmdVaultProject is `vp vault project <subcommand>`. The dispatcher resolves
// two words, so it routes its one subcommand, delete, itself. It sits under
// `project` because `vp vault delete` is already a file delete.
func cmdVaultProject() *cli.Command {
	var c *cli.Command
	c = &cli.Command{
		Name:     "vault project",
		Synopsis: "vp vault project delete <project>... (--moved-to <url> | --discard) [--vault <path>] [--dry-run] [--expect <digest>] [--json]",
		Description: "Delete projects from the vault in one published commit that carries their departure records. " +
			"Run it on the vault the projects leave. With --moved-to, the destination's published remote must hold a " +
			"verified copy of every project (the footprint equal at the copy commit, in its trailer, at this vault's " +
			"HEAD and at the destination tip); --discard deletes with no destination. The vault must equal every " +
			"remote's live tip. The commit is published to every remote exactly, never rebased; the ignored leftovers, " +
			"which git revert cannot restore, are removed only once every remote holds it. Run --dry-run first and " +
			"paste the command it prints. Re-running the same command finishes an unfinished run.",
		Flags: vaultProjectDeleteFlags,
		Examples: []cli.Example{
			{Cmd: "vp vault project delete qms --moved-to git@example.com:team/quantum-vault.git --dry-run", Comment: "Plan the delete of a project already copied into another vault"},
			{Cmd: "vp vault project delete scratch --discard --dry-run", Comment: "Plan discarding a project"},
		},
		Run: func(args []string) int {
			if len(args) == 0 || args[0] != "delete" {
				fmt.Fprintf(os.Stderr, "vp vault project: unknown subcommand; usage: %s\n", c.Synopsis)
				return cli.ExitUser
			}
			return runVaultProjectDelete(c, args[1:], os.Stdout, os.Stderr)
		},
	}
	return c
}

func runVaultProjectDelete(c *cli.Command, args []string, out, errOut io.Writer) int {
	const name = "vp vault project delete"
	fv, err := cli.ParseFlags(vaultProjectDeleteFlags, args)
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", name, err)
		return cli.ExitUser
	}
	if len(fv.Args()) == 0 {
		fmt.Fprintf(errOut, "%s: name at least one project\n", name)
		return cli.ExitUser
	}
	root, code := vaultRootFor(c, fv.Get("--vault"), "delete a project")
	if code != cli.ExitOK {
		return code
	}
	req := storage.DeleteRequest{Projects: fv.Args(), MovedTo: fv.Get("--moved-to"), Discard: fv.Bool("--discard"), Expect: fv.Get("--expect")}
	emit := func(v any) int {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			fmt.Fprintf(errOut, "%s: %v\n", name, err)
			return cli.ExitSystem
		}
		return cli.ExitOK
	}
	if fv.Bool("--dry-run") {
		plan, err := storage.PlanDelete(root, req)
		if err != nil {
			fmt.Fprintf(errOut, "%s: %v\n", name, err)
			return cli.ExitUser
		}
		if fv.Bool("--json") {
			return emit(plan)
		}
		fmt.Fprintln(out, "DRY RUN — nothing was written")
		printDeletePlan(out, plan)
		fmt.Fprintf(out, "\nTo run it:\n  %s\n", plan.Command)
		return cli.ExitOK
	}
	res, err := storage.ApplyDelete(root, req)
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", name, err)
		return cli.ExitUser
	}
	// The host-local index stores go after ApplyDelete has released the vault
	// lock, each under its own index commit lock (ADR-014; task
	// index-fingerprints-project-lifecycle-and-migration-marker).
	removal := indexstore.RemoveGoneProjects(context.Background(), storage.NewVault(root), req.Projects, indexstore.LifecycleRemovalTimeout)
	if fv.Bool("--json") {
		return emit(deleteOutput{DeleteResult: res, IndexRemoval: removal})
	}
	if res.Redo != "" && res.Redo != storage.RedoNone {
		fmt.Fprintf(out, "finished an unfinished run first: %s\n", res.Redo)
	}
	if res.Plan != nil {
		printDeletePlan(out, res.Plan)
	}
	if res.Commit != "" {
		fmt.Fprintf(out, "\ncommit %s, published to every remote\n", res.Commit)
	}
	fmt.Fprintf(out, "leftovers removed: %d file(s), %d dir(s)\n", res.FilesRemoved, res.DirsRemoved)
	for _, k := range res.Kept {
		fmt.Fprintf(out, "kept: %s (%s) %s\n", k.Path, k.Class, k.Reason)
	}
	for _, r := range removal {
		if r.Error != "" {
			fmt.Fprintf(out, "index store %s: kept (%s); the next index sweep retries it\n", r.Path, r.Error)
			continue
		}
		fmt.Fprintf(out, "index store %s: %s\n", r.Path, r.Outcome)
	}
	if len(res.Undo) > 0 {
		fmt.Fprintln(out, "\nUndo (restores everything but the leftovers):")
		for _, l := range res.Undo {
			fmt.Fprintf(out, "  %s\n", l)
		}
	}
	return cli.ExitOK
}

func printDeletePlan(out io.Writer, p *storage.DeletePlan) {
	fmt.Fprintf(out, "vault:   %s\nmode:    %s\nkind:    %s\n", p.Vault, p.Mode, p.Kind)
	if p.To != "" {
		fmt.Fprintf(out, "to:      %s\n", p.To)
	}
	for _, pp := range p.Projects {
		fmt.Fprintf(out, "project %s: footprint %s, %d tracked file(s)", pp.Project, pp.Footprint, len(pp.Tracked))
		if pp.CopyCommit != "" {
			fmt.Fprintf(out, ", copy %s", pp.CopyCommit)
		}
		if pp.Generation > 0 {
			fmt.Fprintf(out, ", generation %d", pp.Generation)
		}
		fmt.Fprintln(out)
		for _, f := range pp.Tracked {
			fmt.Fprintf(out, "  remove %s\n", f.Path)
		}
	}
	fmt.Fprintf(out, "not restorable by git revert (%d):\n", len(p.Leftovers))
	for _, l := range p.Leftovers {
		fmt.Fprintf(out, "  %s (%s, %d bytes)\n", l.Path, l.Class, l.Size)
	}
	for _, s := range p.IndexStores {
		fmt.Fprintf(out, "host-local index store, removed after the delete is published: %s\n", s)
	}
	fmt.Fprintf(out, "push targets: %v (branch %s)\n", p.Remotes, p.Branch)
	for _, w := range p.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}
	fmt.Fprintf(out, "digest: %s\n", p.Digest)
}

// deleteOutput is a delete's result plus the host-local index stores it removed
// after the vault lock was released (index_removal).
type deleteOutput struct {
	*storage.DeleteResult
	IndexRemoval []indexstore.IndexRemoval `json:"index_removal"`
}
