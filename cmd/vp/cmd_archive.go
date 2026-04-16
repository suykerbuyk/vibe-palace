// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/suykerbuyk/vibe-palace/internal/archive"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/project"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// Root `vp archive` is a container that prints usage. All real work
// lives in child commands so each can carry its own flag set cleanly.
func cmdArchive() *cli.Command {
	return &cli.Command{
		Name:        "archive",
		Synopsis:    "vp archive <command> [flags]",
		Description: "Archive AI session transcripts with provenance manifests. See doc/adr/001-transcript-archive.md.",
		Subcommands: []string{"archive create", "archive list", "archive verify", "archive extract"},
		Run: func(args []string) int {
			fmt.Fprintln(os.Stderr, "Usage: vp archive <command> [flags]\n\nRun 'vp archive --help' for details.")
			return cli.ExitOK
		},
	}
}

// -------- archive create --------

var archiveCreateFlags = []cli.FlagDef{
	{Name: "--adapter", Arg: "NAME", Help: "Source adapter", Default: "claude-code"},
	{Name: "--session-id", Arg: "ID", Help: "Session identifier within the adapter"},
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project slug (default: auto-detect)"},
	{Name: "--source", Arg: "PATH", Help: "Override source transcript path"},
	{Name: "--cwd", Arg: "DIR", Help: "Working directory for path resolution (default: current)"},
	{Name: "--quiet", Short: "-q", Help: "Suppress non-error output"},
}

func cmdArchiveCreate(info cli.BuildInfo) *cli.Command {
	return &cli.Command{
		Name:        "archive create",
		Synopsis:    "vp archive create --session-id ID [--adapter NAME] [--project P] [--source PATH]",
		Description: "Archive a raw AI session transcript under <vault>/Projects/<slug>/transcripts/ with a provenance manifest.",
		Flags:       archiveCreateFlags,
		Examples: []cli.Example{
			{Cmd: "vp archive create --session-id abc123", Comment: "Archive the current project's Claude Code session abc123"},
			{Cmd: "vp archive create --session-id abc123 -p myapp", Comment: "Archive into a specific project"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(archiveCreateFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp archive create: %v\n", err)
				return cli.ExitUser
			}
			sessionID := fv.Get("--session-id")
			if sessionID == "" {
				fmt.Fprintln(os.Stderr, "vp archive create: --session-id is required")
				return cli.ExitUser
			}
			proj, vaultRoot, code := resolveProjectAndVault(fv.Get("--project"), "archive create")
			if code != cli.ExitOK {
				return code
			}

			adapter := fv.Get("--adapter")
			if adapter == "" {
				adapter = archive.ClaudeCodeAdapterName
			}

			signOpts, signErr := archiveSignOptsFromVault(vaultRoot, proj)
			if signErr != nil {
				fmt.Fprintf(os.Stderr, "vp archive create: %v\n", signErr)
				return cli.ExitUser
			}

			res, err := archive.Create(archive.CreateOptions{
				Adapter:     adapter,
				SessionID:   sessionID,
				SourcePath:  fv.Get("--source"),
				SourceCWD:   fv.Get("--cwd"),
				VaultRoot:   vaultRoot,
				ProjectSlug: proj,
				VPVersion:   info.Version,
				Sign:        signOpts,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp archive create: %v\n", err)
				return cli.ExitSystem
			}

			if fv.Bool("--quiet") {
				return cli.ExitOK
			}
			if res.Skipped {
				fmt.Fprintf(os.Stdout, "already archived: %s\n", res.ManifestPath)
				return cli.ExitOK
			}
			fmt.Fprintf(os.Stdout, "archived: %s\n  manifest: %s\n  source_sha256: %s\n  turns: %d  bytes: %d -> %d\n",
				res.ArchivePath, res.ManifestPath,
				res.Manifest.SourceSHA256,
				res.Manifest.TurnCount,
				res.Manifest.SourceBytes, res.Manifest.CompressedBytes)
			return cli.ExitOK
		},
	}
}

// -------- archive list --------

var archiveListFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project slug (default: auto-detect)"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdArchiveList() *cli.Command {
	return &cli.Command{
		Name:        "archive list",
		Synopsis:    "vp archive list [--project P] [--json]",
		Description: "List archived transcripts for a project (oldest first).",
		Flags:       archiveListFlags,
		Examples: []cli.Example{
			{Cmd: "vp archive list", Comment: "List archives in the current project"},
			{Cmd: "vp archive list -p myapp --json", Comment: "Emit JSON for scripting"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(archiveListFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp archive list: %v\n", err)
				return cli.ExitUser
			}
			proj, vaultRoot, code := resolveProjectAndVault(fv.Get("--project"), "archive list")
			if code != cli.ExitOK {
				return code
			}

			entries, err := archive.ListEntries(vaultRoot, proj)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp archive list: %v\n", err)
				return cli.ExitSystem
			}

			if fv.Bool("--json") {
				ms := make([]*archive.Manifest, 0, len(entries))
				for _, e := range entries {
					ms = append(ms, e.Manifest)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(ms)
				return cli.ExitOK
			}

			if len(entries) == 0 {
				fmt.Fprintln(os.Stdout, "No archives found.")
				return cli.ExitOK
			}
			fmt.Fprintf(os.Stdout, "%-20s %-10s %-6s %-14s %s\n",
				"CAPTURED", "ADAPTER", "TURNS", "COMPRESSED", "SESSION")
			for _, e := range entries {
				m := e.Manifest
				fmt.Fprintf(os.Stdout, "%-20s %-10s %-6d %-14s %s\n",
					m.CapturedAt, m.Adapter, m.TurnCount,
					humanBytes(m.CompressedBytes), m.SessionID)
			}
			return cli.ExitOK
		},
	}
}

// -------- archive verify --------

var archiveVerifyFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project slug (default: auto-detect)"},
	{Name: "--all", Help: "Verify every archive in the project"},
	{Name: "--json", Help: "Output JSON"},
}

func cmdArchiveVerify() *cli.Command {
	return &cli.Command{
		Name:        "archive verify",
		Synopsis:    "vp archive verify [<path-or-session-id> | --all] [--project P]",
		Description: "Recompute source_sha256 from the compressed archive and compare against the manifest. Exits nonzero on any mismatch.",
		Flags:       archiveVerifyFlags,
		Examples: []cli.Example{
			{Cmd: "vp archive verify abc123", Comment: "Verify one archive by session id"},
			{Cmd: "vp archive verify --all", Comment: "Verify every archive in the current project"},
			{Cmd: "vp archive verify path/to/file.manifest.json", Comment: "Verify by manifest path"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(archiveVerifyFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp archive verify: %v\n", err)
				return cli.ExitUser
			}
			proj, vaultRoot, code := resolveProjectAndVault(fv.Get("--project"), "archive verify")
			if code != cli.ExitOK {
				return code
			}

			var toCheck []*archive.Entry
			if fv.Bool("--all") {
				entries, err := archive.ListEntries(vaultRoot, proj)
				if err != nil {
					fmt.Fprintf(os.Stderr, "vp archive verify: %v\n", err)
					return cli.ExitSystem
				}
				toCheck = entries
			} else {
				pos := fv.Args()
				if len(pos) == 0 {
					fmt.Fprintln(os.Stderr, "vp archive verify: provide <path-or-session-id> or use --all")
					return cli.ExitUser
				}
				e, err := archive.ResolveEntry(vaultRoot, proj, pos[0])
				if err != nil {
					fmt.Fprintf(os.Stderr, "vp archive verify: %v\n", err)
					return cli.ExitUser
				}
				toCheck = []*archive.Entry{e}
			}

			verifyOpts := archiveVerifyOptsFromVault(vaultRoot, proj)

			results := make([]*archive.VerifyResult, 0, len(toCheck))
			anyFail := false
			for _, e := range toCheck {
				r := archive.VerifyWithOptions(e, verifyOpts)
				results = append(results, r)
				if !r.OK {
					anyFail = true
				}
			}

			if fv.Bool("--json") {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(results)
			} else {
				for _, r := range results {
					status := "OK"
					if !r.OK {
						status = "FAIL"
					}
					sig := ""
					switch {
					case r.SignatureChecked && r.SignatureOK:
						sig = " sig:ok"
					case r.SignatureChecked && !r.SignatureOK:
						sig = " sig:FAIL"
					}
					fmt.Fprintf(os.Stdout, "[%s]%s %s  (session %s)\n",
						status, sig, r.Entry.ManifestPath, r.Entry.Manifest.SessionID)
					for _, p := range r.Problems {
						fmt.Fprintf(os.Stdout, "  - %s\n", p)
					}
				}
			}
			if anyFail {
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// -------- archive extract --------

var archiveExtractFlags = []cli.FlagDef{
	{Name: "--project", Short: "-p", Arg: "PROJECT", Help: "Project slug (default: auto-detect)"},
	{Name: "--to", Arg: "PATH", Help: "Output file path, or - for stdout", Default: "-"},
}

func cmdArchiveExtract() *cli.Command {
	return &cli.Command{
		Name:        "archive extract",
		Synopsis:    "vp archive extract <path-or-session-id> [--to PATH] [--project P]",
		Description: "Decompress an archived transcript to a file or stdout. Output matches the original pre-compression bytes.",
		Flags:       archiveExtractFlags,
		Examples: []cli.Example{
			{Cmd: "vp archive extract abc123", Comment: "Decompress to stdout by session id"},
			{Cmd: "vp archive extract abc123 --to session.jsonl", Comment: "Decompress to a file"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(archiveExtractFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp archive extract: %v\n", err)
				return cli.ExitUser
			}
			pos := fv.Args()
			if len(pos) == 0 {
				fmt.Fprintln(os.Stderr, "vp archive extract: provide <path-or-session-id>")
				return cli.ExitUser
			}
			proj, vaultRoot, code := resolveProjectAndVault(fv.Get("--project"), "archive extract")
			if code != cli.ExitOK {
				return code
			}
			e, err := archive.ResolveEntry(vaultRoot, proj, pos[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp archive extract: %v\n", err)
				return cli.ExitUser
			}

			dest := fv.Get("--to")
			if dest == "" {
				dest = "-"
			}
			var out io.Writer = os.Stdout
			if dest != "-" {
				f, err := os.Create(dest)
				if err != nil {
					fmt.Fprintf(os.Stderr, "vp archive extract: %v\n", err)
					return cli.ExitSystem
				}
				defer f.Close()
				out = f
			}
			if _, err := archive.Extract(e.ArchivePath, out); err != nil {
				fmt.Fprintf(os.Stderr, "vp archive extract: %v\n", err)
				return cli.ExitSystem
			}
			return cli.ExitOK
		},
	}
}

// resolveProjectAndVault is shared by every archive subcommand. Returns
// the project slug, vault root, and cli.ExitOK or an error exit code.
func resolveProjectAndVault(projArg, cmdName string) (string, string, int) {
	proj := projArg
	if proj == "" {
		proj, _ = project.DetectProject(".")
	}
	if proj == "" {
		fmt.Fprintf(os.Stderr, "vp %s: could not detect project (use --project)\n", cmdName)
		return "", "", cli.ExitUser
	}
	vault, err := openProjectVault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp %s: %v\n", cmdName, err)
		return "", "", cli.ExitUser
	}
	return proj, vault.Root, cli.ExitOK
}

// archiveSignOptsFromVault reads the `[archive]` section of the
// project's effective vibe-palace config and returns the signing
// options that Create should use. An unset sign_mode disables signing.
func archiveSignOptsFromVault(vaultRoot, proj string) (archive.SignOptions, error) {
	v := storage.NewVault(vaultRoot)
	cfg, err := v.LoadConfig(proj)
	if err != nil {
		return archive.SignOptions{}, fmt.Errorf("load archive config: %w", err)
	}
	return archive.SignOptions{
		Mode:      cfg.Archive.SignMode,
		Key:       cfg.Archive.SignKey,
		Namespace: cfg.Archive.SignNamespace,
	}, nil
}

// archiveVerifyOptsFromVault mirrors the above for the verify path.
func archiveVerifyOptsFromVault(vaultRoot, proj string) archive.VerifyOptions {
	v := storage.NewVault(vaultRoot)
	cfg, err := v.LoadConfig(proj)
	if err != nil {
		return archive.VerifyOptions{}
	}
	return archive.VerifyOptions{
		AllowedSigners: cfg.Archive.AllowedSigners,
		Identity:       cfg.Archive.SignerIdentity,
	}
}

// humanBytes renders sizes compactly for table display. Not a precise
// SI/IEC formatter; readability trumps strict standards here.
func humanBytes(n int64) string {
	const (
		_  = iota
		kb = 1 << (10 * iota)
		mb
		gb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.1fG", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.1fM", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1fK", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
