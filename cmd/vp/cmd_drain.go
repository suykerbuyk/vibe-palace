// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/itersummary"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/summarize"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

const (
	// drainSummariesDefaultMax is the --max default, applied IN CODE
	// (fv.Int returns 0 when the flag is unset or non-positive), mirroring
	// internal/palace/discover.go's discoverDefaultMaxSamples pattern.
	// cli.FlagDef.Default is display-only prose for help text and is never
	// read by cli.ParseFlags.
	drainSummariesDefaultMax = 50
)

// drainLockRoot is the "vaultRoot" vaultlock.TryAcquire is given: NOT the
// vibe-palace vault (this queue is architecturally distinct from it, per the
// task's own Scoping — see internal/summarize's doc comment), but
// project_path/.vibe-palace, so the lock's sidecar (.vp-locks/<hash>.lock)
// lands inside the directory already covered by the project's blanket
// /.vibe-palace/ gitignore rule, exactly like every other host-local
// artifact this feature writes. vaultlock's own "vaultRoot" parameter is
// purely a directory namespace for its sidecar files — it does not require
// or validate that the given root is an actual vibe-palace vault (confirmed
// by reading internal/vaultlock/vaultlock.go's openLockFile: it only checks
// the root is a non-empty absolute path) — so reusing it here for a
// project-local, non-vault lock is a legitimate, general use of the
// package, not a layering violation.
func drainLockRoot(projectPath string) string {
	return filepath.Join(projectPath, ".vibe-palace")
}

// drainLockTarget is the path vaultlock.TryAcquire hashes into a lock
// filename. It MUST be an absolute, existing path — vaultlock's own
// canonicalKey resolves it via filepath.EvalSymlinks, and when that fails
// (as it always does for a relative, non-existent string) it falls back to
// EvalSymlinks on the target's own *parent directory*, which for a bare
// relative string like "summarization-drain" resolves relative to the
// CALLING PROCESS's cwd — not to the project at all. That was a real bug in
// an earlier version of this function: a detached drain (whatever cwd it
// inherited) and a manually-run `vp drain summaries` (the user's shell cwd)
// would hash to two DIFFERENT lock filenames under the same project's
// .vp-locks/ directory, so they would never actually contend — silently
// defeating the single-flight guarantee this whole redesign exists to
// provide. Using the queue directory itself (already created via MkdirAll
// before this is called) as the target fixes this: it is absolute,
// guaranteed to exist, and deterministically derived from projectPath alone,
// so canonicalKey's first branch (a real EvalSymlinks of the target itself)
// always succeeds and always resolves the same way regardless of caller cwd.
func drainLockTarget(projectPath string) string {
	return summarize.QueueDir(projectPath)
}

func cmdDrain() *cli.Command {
	return &cli.Command{
		Name:        "drain",
		Synopsis:    "vp drain <command> [flags]",
		Description: "One-shot draining of host-local background job queues.",
	}
}

var drainSummariesFlags = []cli.FlagDef{
	{Name: "--project-path", Arg: "PATH", Help: "Absolute path to the project repo root (required)"},
	{Name: "--max", Arg: "N", Help: "Maximum jobs to drain this call (default: 50)"},
}

func cmdDrainSummaries() *cli.Command {
	return &cli.Command{
		Name:     "drain summaries",
		Synopsis: "vp drain summaries --project-path PATH [--max N]",
		Description: "Drain queued summarization jobs (internal/summarize) for one " +
			"project. This is a ONE-SHOT command, not a daemon: it claims and " +
			"processes whatever is claimable right now, then exits. It is intended " +
			"to be launched as a detached background process (see " +
			"internal/detachlaunch) immediately after something enqueues " +
			"summarization work, so the enqueuing caller never blocks on draining. " +
			"--project-path is required and must be the project's root — it is " +
			"never inferred from this process's working directory, since a " +
			"detached child's inherited cwd is not reliably the project being " +
			"drained.",
		Flags: drainSummariesFlags,
		Examples: []cli.Example{
			{Cmd: "vp drain summaries --project-path /path/to/project", Comment: "Drain up to 50 queued summarization jobs"},
			{Cmd: "vp drain summaries --project-path /path/to/project --max 200", Comment: "Drain up to 200 jobs"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(drainSummariesFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp drain summaries: %v\n", err)
				return cli.ExitUser
			}

			projectPath := fv.Get("--project-path")
			if projectPath == "" {
				fmt.Fprintln(os.Stderr, "vp drain summaries: --project-path is required")
				return cli.ExitUser
			}
			if !filepath.IsAbs(projectPath) {
				// Fail fast with a clear message here, rather than letting a
				// relative path travel all the way to vaultlock.TryAcquire
				// (which hard-requires an absolute vaultRoot) and surface as
				// an opaque "acquire lock" system error.
				fmt.Fprintf(os.Stderr, "vp drain summaries: --project-path must be absolute, got %q\n", projectPath)
				return cli.ExitUser
			}

			max := fv.Int("--max")
			if max <= 0 {
				max = drainSummariesDefaultMax
			}

			// runDrainSummaries resolves the project's own [summarization]
			// config and constructs a real, config-driven Summarizer itself
			// (mirroring internal/hook/hook.go's own
			// capture.NewEnricherFromConfig call site) — there is nothing
			// left for this Run closure to inject.
			return runDrainSummaries(projectPath, max, os.Stdout)
		},
	}
}

// runDrainSummaries is the testable body of `vp drain summaries`. out
// receives the one-line result (and any error text): when this command is
// launched via internal/detachlaunch.Launch, the caller has already
// redirected this process's stdout to a log file, so writing here is all the
// logging this command needs to do.
func runDrainSummaries(projectPath string, max int, out io.Writer) int {
	// Resolve the vault and the project slug EXPLICITLY from the given
	// projectPath, never via os.Getwd(). This command is designed to be
	// launched as a detached child (internal/detachlaunch), whose inherited
	// cwd is not reliably the project being drained — the same pitfall
	// cmd/vp/bootstrap.go's openProjectVault() and
	// internal/tools/wrapstate_tools.go's vp_collect_wrap_state /
	// vp_preflight_wrap avoid by taking an explicit root/project_path
	// instead of Getwd().
	vault, err := OpenProjectVaultAt(projectPath)
	if err != nil {
		fmt.Fprintf(out, "vp drain summaries: open vault: %v\n", err)
		return cli.ExitSystem
	}
	slug, err := project.DetectProject(projectPath)
	if err != nil {
		fmt.Fprintf(out, "vp drain summaries: detect project: %v\n", err)
		return cli.ExitSystem
	}

	// Resolve the project's own [summarization] config and build the real
	// Summarizer this drain call uses — the same
	// load-config-then-build-client pattern internal/hook/hook.go's own
	// capture.NewEnricherFromConfig call site uses for enrichment. Disabled
	// or unresolvable summarization must never fail the drain itself (a
	// project that hasn't opted in still needs `vp drain summaries` to
	// succeed as a documented no-op) — only a hard error loading the config
	// file itself (malformed TOML, etc.) is a real ExitSystem failure here.
	cfg, err := vault.LoadConfig(slug)
	if err != nil {
		fmt.Fprintf(out, "vp drain summaries: load config: %v\n", err)
		return cli.ExitSystem
	}

	is, err := itersummary.NewIterationSummarizerFromConfig(cfg.Summarization, vault)
	if err != nil {
		// Enabled but unresolvable (e.g. missing API key env) must not fail
		// the whole drain — mirrors capture.NewEnricherFromConfig's own
		// contract: callers warn-log and proceed with a nil client, never a
		// hard failure. Here that means proceeding with a DispatchSummarizer
		// whose Iteration handler is nil, so any queued KindIteration job
		// is deferred back to the queue (ErrUnsupportedKind) rather than
		// dropped or crashing this call.
		slog.Warn("vp drain summaries: iteration summarizer disabled", "err", err)
	}
	// iterationSummarizer is deliberately typed as the summarize.Summarizer
	// INTERFACE, assigned only in the is != nil branch — never assigned
	// directly from `is` (a concrete *itersummary.IterationSummarizer). A
	// direct assignment would box a nil *IterationSummarizer into a
	// NON-nil Summarizer interface value (Go's classic typed-nil-interface
	// trap: the interface carries a type descriptor even when the pointer
	// inside it is nil), which would make DispatchSummarizer.Summarize's
	// own `d.Iteration != nil` check pass and dispatch into Summarize on a
	// nil receiver instead of correctly treating this as "no handler
	// registered".
	var iterationSummarizer summarize.Summarizer
	if is != nil {
		iterationSummarizer = is
	}
	dispatcher := &summarize.DispatchSummarizer{Iteration: iterationSummarizer}

	// Ensure the queue directory exists so callers globbing it (e.g.
	// vp_trigger_summarization_drain) always find a real, stat-able
	// directory, even for a project that has never enqueued anything.
	// summarize.DrainSummarizationQueue's own Claim (via internal/jobqueue.Claim)
	// already tolerates a missing directory as the "nothing claimable"
	// sentinel, so this is a courtesy to other callers, not a correctness
	// requirement of the drain itself.
	queueDir := summarize.QueueDir(projectPath)
	if err := os.MkdirAll(queueDir, 0o755); err != nil {
		fmt.Fprintf(out, "vp drain summaries: create queue dir: %v\n", err)
		return cli.ExitSystem
	}

	// Single-flight: an OS-level advisory lock (internal/vaultlock), not a
	// hand-rolled pidfile-plus-staleness-heuristic. This deliberately
	// replaces an earlier version of this function that used
	// os.OpenFile(O_EXCL) plus a fixed staleness window: that scheme had a
	// real fencing gap — a drain running longer than the staleness window
	// (plausible once a real, LLM-backed Summarizer lands) would have its
	// lock "reclaimed" by a second caller while still running, and the
	// first caller's own unconditional cleanup would then delete the SECOND
	// caller's lock out from under it. vaultlock.TryAcquire has no such
	// window: the lock is a real flock, released automatically by the OS on
	// process exit (including a crash), so there is no "is it stale yet?"
	// question to get wrong.
	release, ok, err := vaultlock.TryAcquire(drainLockRoot(projectPath), drainLockTarget(projectPath))
	if err != nil {
		fmt.Fprintf(out, "vp drain summaries: acquire lock: %v\n", err)
		return cli.ExitSystem
	}
	if !ok {
		// A concurrent drain already holds the lock. This is NOT a user
		// error — a concurrent drain finishing the work is a success, not a
		// failure — so it is reported at ExitOK rather than ExitUser.
		fmt.Fprintln(out, "vp drain summaries: already_running")
		return cli.ExitOK
	}
	defer release()

	drained, err := summarize.DrainSummarizationQueue(context.Background(), projectPath, dispatcher, max)
	if err != nil {
		fmt.Fprintf(out, "vp drain summaries: drain: %v\n", err)
		return cli.ExitSystem
	}

	// drained now genuinely reflects real work once [summarization] is
	// enabled and resolvable in this project's config: dispatcher above is a
	// real, config-driven DispatchSummarizer, not a documented no-op stand-in.
	// It stays 0 only when the queue is empty, or when summarization is
	// disabled/unresolvable (see the warn-log above) so every claimed
	// KindIteration job is deferred back to the queue via ErrUnsupportedKind.
	fmt.Fprintf(out, "vp drain summaries: project=%s drained=%d\n", slug, drained)
	return cli.ExitOK
}
