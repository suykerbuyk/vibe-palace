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
	"github.com/suykerbuyk/vibe-palace/internal/detachlaunch"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/search"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/tools"
)

var searchFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project name (default: auto-detect from the current directory). Must name a project in the vault; an unknown one exits 1."},
	{Name: "--wing", Short: "-w", Arg: "WING", Help: "Filter by wing"},
	{Name: "--room", Short: "-r", Arg: "ROOM", Help: "Filter by room"},
	{Name: "--limit", Short: "-n", Arg: "N", Help: "Max results", Default: "10"},
	{Name: "--json", Help: "Output JSON"},
	{Name: "--raw", Help: "Also show raw source-text hits alongside summary hits (default: raw hits are hidden when a summary exists)."},
}

func cmdSearch() *cli.Command {
	return &cli.Command{
		Name:     "search",
		Synopsis: "vp search <query> [flags]",
		Description: "Semantic search across palace content. The project — named by --project or " +
			"detected from the current directory — must exist in the vault (a Projects/<slug>/ " +
			"directory or a palace store); an unknown or malformed project exits 1 before the " +
			"embedding model loads.",
		Flags: searchFlags,
		Examples: []cli.Example{
			{Cmd: "vp search \"database migrations\"", Comment: "Search current project"},
			{Cmd: "vp search \"auth\" -p myapp -n 5", Comment: "Search specific project"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(searchFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
				return cli.ExitUser
			}

			positional := fv.Args()
			if len(positional) == 0 {
				fmt.Fprintln(os.Stderr, "vp search: query argument required")
				return cli.ExitUser
			}
			query := positional[0]

			proj := fv.Get("--project")
			detected := proj == ""
			if detected {
				proj, _ = project.DetectProject(".")
			}
			if proj == "" {
				fmt.Fprintln(os.Stderr, "vp search: could not detect project (use --project)")
				return cli.ExitUser
			}

			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
				return cli.ExitUser
			}

			cfg, err := vault.LoadConfig("")
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
				return cli.ExitUser
			}

			// Validate the project before the model loads: a typo must not
			// cost a ~90 MB download to be answered "No results found.".
			if code := requireSearchProject(vault, proj, detected); code != cli.ExitOK {
				return code
			}

			// LAZY, not eager: construction is a ~90 MB download plus an
			// unbounded pipeline build on a cold cache, and NewONNXBackend takes
			// no ctx, so an eager construct here sits before the engine exists
			// and outside every search-path bound. Behind embedder.NewLazy the
			// construct runs on WarmEmbedder's detached goroutine and the
			// caller's wait is bounded by readyWait
			// (internal/search/engine.go:380, be6eca2), which refuses with
			// *IndexNotBuiltError -- the error runSearch below already maps to
			// ExitUser. NewLazy cannot fail, so there is no error branch here; a
			// real construction failure surfaces through that same bounded wait.
			emb := embedder.NewLazy(func() (embedder.Embedder, error) { return newVaultEmbedder(vault, cfg) })

			// WithRebuildLauncher: ADR-016 decision 4 is "a cold/stale search in
			// ANY process" — so a CLI `vp search` that degrades on a cold corpus
			// also records the durable rebuild-corpus want and spawns the detached
			// `vp index rebuild`, exactly as the MCP serving engine does. The seam
			// lives in the tools layer; this cold-search path never try-locks the
			// run lock. (A CLI engine is built WITHOUT WithBackgroundConverter, so
			// its runConvert never runs — the one engine path that takes the run
			// lock, decision 3, exists only on the MCP serving engine.)
			eng := search.NewEngine(emb, vault, cfg,
				// A cross-project search builds every project's index; each
				// resolves its own [index] transcripts.
				search.WithConfigFor(vault.LoadConfig),
				search.WithRebuildLauncher(tools.NewRebuildLauncher(vault, detachlaunch.Launch)))
			// NO `defer eng.Close()`, and no `defer emb.Close()` -- deliberate,
			// and guarded by the sourceaudit rule oneshotSearchNoEngineClose.
			//
			// Engine.Close joins warmWG (internal/search/engine.go:3243) before
			// closing the embedder (S-D1): correct for `vp mcp`, where Close
			// means shutdown of a process that keeps running, and wrong here,
			// where it would make a one-shot `vp search` sit through the whole
			// cold model load AFTER it has already printed its bounded refusal.
			// Closing only the embedder instead is not a substitute:
			// LazyEmbedder.Close does not join the warm goroutine, and it no-ops
			// while the construct is still in flight.
			//
			// Nothing durable is lost. Every durable write on this path -- the
			// vector commits and generation bumps in finalCommit
			// (internal/search/engine.go:2083, :2195) and the NoteRebuild want
			// recorded by the launcher ensureDetachedRebuild invokes
			// (internal/search/engine.go:656 ->
			// internal/tools/system_tools.go:1091) -- completes inside Search,
			// before this point. Engine.Close itself
			// (internal/search/engine.go:3225-3254) writes nothing durable:
			// bruteIndex.Close is a documented no-op
			// (internal/search/vector_index.go:338-339); hnswIndex.Close
			// (internal/search/hnsw_index.go:622) only cancels and joins an
			// in-memory tombstone rebuild, and that file touches no os/
			// indexstore writer at all; this engine is built WITHOUT
			// WithBackgroundConverter, so the one convertWG.Add
			// (internal/search/engine.go:2344) is unreachable past the
			// !convertEnabled return at :2294 and convertWG is empty;
			// LazyEmbedder.Close no-ops when unbuilt
			// (internal/embedder/lazy.go:84-90) and ONNXEmbedder.Close
			// (internal/embedder/onnx.go:525) only destroys an in-process
			// session; and cmd/vp/main.go:43 os.Exit's as soon as dispatch
			// returns.
			//
			// The model-cache flock is never released by Close on ANY path:
			// release is a local of NewONNX, and ONNXEmbedder.Close
			// (internal/embedder/onnx.go:525-537) only destroys the session and
			// pipeline. A NewONNX that returns ordinarily releases it itself
			// (internal/embedder/onnx.go:239 acquires, :255-260 defers the
			// release unless it was handed off). It outlives the process on one
			// path only: a download that hit modelDownloadTimeout hands release()
			// to a leaked goroutine (internal/embedder/onnx.go:296-308), and an
			// exit before that goroutine runs drops the lock with the fds.
			//
			// A download cut off that way can leave a snapshot that READS as
			// complete and is not: localSnapshotComplete
			// (internal/embedder/onnx.go:168-178) accepts a present
			// tokenizer.json plus any NONZERO *.onnx. NewONNX's self-heal
			// (internal/embedder/onnx.go:363-387) is keyed on LOAD FAILURE, so it
			// recovers such a snapshot only when it actually fails to load, and
			// only if its one retry download succeeds.
			//
			// 🔴 AND AFTER THIS CHANGE, AN INTERRUPTED DOWNLOAD IS THE ORDINARY
			// OUTCOME OF A COLD `vp search` — NOT AN INVARIABLE ONE, AND NOT AN
			// ACCIDENT. The condition is readyWait EXPIRING. A construct that
			// finishes the download and the pipeline build inside the 15 s lets
			// waitEmbedderReady return nil and the search proceed, and truncates
			// nothing. When it does not finish, the bounded refusal returns and
			// cmd/vp/main.go:43 exits with the first-ever ~90 MB fetch still in
			// flight — by design, and on a cold cache that is the likely case,
			// not a rare one. Before the lazy wrap it took an external kill,
			// which was exceptional. So do not read the paragraph above as "the
			// same window we already had": the window itself is unchanged; what
			// changed is how routinely it is entered.
			//
			// A cold host still gets there, because the remedy the refusal names
			// is not budgeted: waitEmbedderReady has ONE call site and it is
			// inside Search (internal/search/engine.go:978), while
			// `vp index rebuild` drives RebuildExplicit straight into
			// rebuildAndRemember with indexstore.NoTimeout and refuse=false
			// (internal/search/engine.go:1545-1546). Survivable, not benign.
			// Making the download resumable is its own task,
			// model-cache-download-is-not-resumable.
			//
			// Do not reintroduce warmWG.Wait here to "let the lock release
			// cleanly" — Close never released it — and do not "preserve a
			// process that can finish the download": that is the unbounded wait
			// this change exists to remove.

			limit := fv.Int("--limit")
			if limit == 0 {
				limit = 10
			}

			return runSearch(eng, proj, query, fv.Get("--wing"), fv.Get("--room"), limit, fv.Bool("--raw"), fv.Bool("--json"), os.Stdout)
		},
	}
}

// requireSearchProject refuses a search scoped to a project the vault does not
// hold, printing why and returning the exit code (cli.ExitOK when proj
// exists). A search of a project that does not exist is an error whichever way
// the project was named; answering it with empty results is a silent skip
// dressed as success.
//
// It is a thin wrapper over search.ProjectExists — the "exists" predicate,
// including exactly which trees and which oddities (a notes-only project, a
// .local-only palace husk, a symlinked Projects/<slug> or palace/<slug>) count,
// lives on that function's doc comment now, because it is the layer both this
// CLI check and (*search.Engine).Search's own inline check share. This wrapper
// owns only the CLI-specific parts: syntax-checking a flagged slug, and naming
// how the project was resolved (flag vs. detected) in the refusal.
//
// A flagged slug is syntax-checked first. A detected one already was, by
// DetectProject, so only membership applies to it — and its refusal names the
// detection, because the operator never typed the slug that failed.
func requireSearchProject(vault *storage.Vault, proj string, detected bool) int {
	if !detected {
		if err := slug.Validate(proj); err != nil {
			fmt.Fprintf(os.Stderr, "vp search: --project: %v\n", err)
			return cli.ExitUser
		}
	}
	exists, err := search.ProjectExists(vault, proj)
	if err != nil {
		// "I could not look" is not "absent".
		fmt.Fprintf(os.Stderr, "vp search: list projects: %v\n", err)
		return cli.ExitSystem
	}
	if exists {
		return cli.ExitOK
	}
	if detected {
		fmt.Fprintf(os.Stderr, "vp search: no project %q in vault %s (detected from the current directory); run 'vp init' here or pass --project\n", proj, vault.Root)
	} else {
		fmt.Fprintf(os.Stderr, "vp search: --project %q: no such project in vault %s\n", proj, vault.Root)
	}
	return cli.ExitUser
}

func runSearch(eng *search.Engine, proj, query, wing, room string, limit int, includeRaw, asJSON bool, out io.Writer) int {
	ctx := context.Background()

	// A truly empty project has nothing any tier could index. Refuse it here,
	// before the search, so the CLI pays no model load and exits non-zero naming
	// `vp index rebuild` (ADR-014 decision 8). Engine.Search makes the same check,
	// so this is belt-and-suspenders for the CLI's own exit-code mapping.
	//
	// "pays no model load" is true only because cmdSearch builds the embedder
	// LAZILY; an eager construct there would have paid it before this check ran.
	if empty, err := eng.TrulyEmpty(proj); err != nil {
		fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
		return cli.ExitSystem
	} else if empty {
		fmt.Fprintf(os.Stderr, "vp search: %v\n", &search.NothingIndexableError{Project: proj})
		return cli.ExitUser
	}

	// No explicit pre-rebuild here: Engine.Search builds the project's index on
	// first use through the bounded, refuse-before-embed search path, so the CLI
	// inherits the same "never hang on a cold index" guarantee as the MCP surface
	// (a stray unbounded Rebuild here would bypass that gate). The unbounded full
	// rebuild stays reachable only through `vp index rebuild`.
	//
	// That inheritance depends on the LAZY embedder in cmdSearch as much as on
	// this line: an eager construct there would sit outside readyWait and the
	// guarantee would be the MCP surface's alone.
	results, err := eng.Search(ctx, query, search.SearchFilters{
		Project:    proj,
		Wing:       wing,
		Room:       room,
		Limit:      limit,
		IncludeRaw: includeRaw,
	})
	if err != nil {
		// An unknown project is a user error (ExitUser), never "I could not
		// search" (ExitSystem) — mirroring searchHandler's own
		// errors.As(*search.UnknownProjectError) special-case. Unreachable
		// from cmdSearch today, since requireSearchProject already refuses an
		// unknown project before runSearch is ever called; kept here as
		// defense-in-depth for any other caller of runSearch, and so a future
		// reordering can't silently regress the exit code.
		var unk *search.UnknownProjectError
		if errors.As(err, &unk) {
			fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
			return cli.ExitUser
		}
		// A truly empty project is a user-facing "nothing to search yet, run
		// `vp index rebuild`" condition (ExitUser), not "I could not search"
		// (ExitSystem). Normally unreachable here because runSearch refuses an
		// empty project above, before the rebuild; kept as defense-in-depth so
		// a reordering cannot silently regress the exit code, mirroring the
		// UnknownProjectError case.
		var none *search.NothingIndexableError
		if errors.As(err, &none) {
			fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
			return cli.ExitUser
		}
		// A cold/unbuilt index on this host is a user-facing "run `vp index
		// rebuild`" condition (ExitUser), not "I could not search" (ExitSystem):
		// the search refused to embed the corpus synchronously rather than hang.
		var notBuilt *search.IndexNotBuiltError
		if errors.As(err, &notBuilt) {
			fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
			return cli.ExitUser
		}
		fmt.Fprintf(os.Stderr, "vp search: %v\n", err)
		return cli.ExitSystem
	}

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(results)
		return cli.ExitOK
	}

	if len(results) == 0 {
		fmt.Fprintln(out, "No results found.")
		return cli.ExitOK
	}

	for i, r := range results {
		content := r.Content
		if len(content) > 80 {
			content = content[:77] + "..."
		}
		fmt.Fprintf(out, "%d. [%.3f] %s/%s  %s\n", i+1, r.Score, r.Wing, r.Room, content)
	}
	return cli.ExitOK
}
