// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/vaultaudit"
)

var auditVaultFlags = []cli.FlagDef{
	{Name: "--write", Help: "Write the report to Audits/<date>-vault-audit.md (default: print to stdout)"},
	{Name: "--accept", Help: "Accept every current finding into the baseline. THE BASELINE MAY ONLY SHRINK: this is for the FIRST run on a vault, not for silencing new drift"},
	{Name: "--raise", Help: "With --accept: re-record the measurement of every artifact in THIS run's NEW set. Without it an accepted measurement may only shrink, so a grown artifact keeps reporting — which is the point"},
}

// auditVaultOpts carries the flags into runAuditVault.
//
// It is a struct rather than three positional bools because three bools at one call
// site is a swap waiting to happen, and the two that matter here are the ones that
// WRITE to the baseline.
type auditVaultOpts struct {
	write  bool
	accept bool
	raise  bool
}

// cmdAuditVault is the VAULT-GLOBAL audit.
//
// It takes NO --project flag, and that is deliberate. The thing being audited is the
// VAULT; scoping it to whichever project the caller happened to invoke from is exactly
// how a project nobody has opened in three months escapes scrutiny forever. (A required
// parameter that does nothing is theatre — that was defect #4 of vp_health, deleted at
// 201.) Its sibling `vp audit rooms` IS per-project and takes --project; that asymmetry
// is correct and deliberate — rooms audits a project's classification, vault audits the
// vault.
//
// It is ADVISORY. It reports; it never blocks. A blocking auditor gets disabled the
// first time it is wrong at a bad moment, and a disabled auditor audits nothing. So a
// FAIL still exits 0 — the finding is in the report, loudly, not in the exit code.
func cmdAuditVault() *cli.Command {
	return &cli.Command{
		Name:     "audit vault",
		Synopsis: "vp audit vault [--write] [--accept [--raise]]",
		Description: "Audit the WHOLE VAULT against design intent: transcript round-trips, project-tree " +
			"coherence, KG portability, resume discipline, iteration headings. Reports pass/fail/unknown " +
			"per dimension against the accepted-debt baseline. Advisory — it never blocks.",
		Flags: auditVaultFlags,
		Examples: []cli.Example{
			{Cmd: "vp audit vault", Comment: "Audit the vault and print the report"},
			{Cmd: "vp audit vault --write", Comment: "Write the report into Audits/ (committed by vault tidy)"},
			{Cmd: "vp audit vault --accept", Comment: "Accept current findings as known debt (first run only)"},
			{Cmd: "vp audit vault --accept --raise", Comment: "Also re-record the measurement of every NEW finding, so a grown artifact stops reporting"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(auditVaultFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp audit vault: %v\n", err)
				return cli.ExitUser
			}
			opts := auditVaultOpts{
				write:  fv.Bool("--write"),
				accept: fv.Bool("--accept"),
				raise:  fv.Bool("--raise"),
			}
			// Fail closed, and BEFORE the vault is opened: --raise alone reads as
			// "re-record the measurements" and would otherwise do nothing at all,
			// which is the worst outcome — the operator believes the baseline moved
			// and it did not. An argument error must not need a vault to be reported.
			if opts.raise && !opts.accept {
				fmt.Fprintln(os.Stderr, "vp audit vault: --raise only means anything with --accept")
				return cli.ExitUser
			}
			vault, err := openProjectVault()
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp audit vault: %v\n", err)
				return cli.ExitSystem
			}
			// The WRITER stamps the calendar day, through the same helper the MCP
			// handler uses. This used to be an inline time.Now().Format("2006-01-02")
			// — process-local, and a second authority that disagreed with the MCP
			// surface for six hours every evening. Two format sites with nothing
			// holding them together WAS the bug; there is one now.
			return runAuditVault(vault, opts, vault.CalendarDay(time.Now()), os.Stdout)
		},
	}
}

func runAuditVault(vault *storage.Vault, opts auditVaultOpts, date string, out io.Writer) int {
	report, err := vaultaudit.Run(vault)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vp audit vault: %v\n", err)
		return cli.ExitSystem
	}

	if opts.accept {
		basePath := filepath.Join(vault.Root, filepath.FromSlash(vaultaudit.BaselineRelPath))
		prior, err := vaultaudit.LoadBaseline(basePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vp audit vault: %v\n", err)
			return cli.ExitSystem
		}
		// Regenerate, never rebuild: a survivor keeps the reason a human wrote for it.
		// Stamping UNTRIAGED over every reason on each accept would erase the triage and
		// let the list rot back into an undifferentiated blob (the bug sourceaudit
		// shipped and fixed at 203).
		// The raise set is THIS run's NEW findings, never the whole accepted backlog:
		// --raise re-records what the operator is looking at, not what they are not.
		var raise []vaultaudit.Finding
		if opts.raise {
			raise = report.NewFindings()
		}
		next := prior.Regenerate(report.Findings(), raise)
		if err := next.Save(vault.Root, basePath); err != nil {
			fmt.Fprintf(os.Stderr, "vp audit vault: %v\n", err)
			return cli.ExitSystem
		}
		fmt.Fprintf(out, "accepted current findings into %s\n", vaultaudit.BaselineRelPath)
		fmt.Fprintln(out, "⚠ every UNTRIAGED dimension now needs a REAL reason — an unexplained entry is "+
			"indistinguishable from an oversight, and in six months nobody will know which it was.")
		return cli.ExitOK
	}

	body := report.Render(date, vault.Root)

	if opts.write {
		rel := vaultaudit.ReportRelPath(date)
		abs := filepath.Join(vault.Root, filepath.FromSlash(rel))
		// atomicfile.Write with the REAL vault root, so the surface stamp fires. Passing
		// anything else silently skips it — and a write that skips the stamp is how the
		// vault loses its version floor.
		if err := atomicfile.Write(vault.Root, abs, []byte(body)); err != nil {
			fmt.Fprintf(os.Stderr, "vp audit vault: %v\n", err)
			return cli.ExitSystem
		}
		fmt.Fprintf(out, "wrote %s\n", rel)
		fmt.Fprintln(out, "(vault tidy sweeps Audits/ — commit it so `git log -p Audits/` shows drift)")
		return cli.ExitOK
	}

	fmt.Fprint(out, body)
	// ADVISORY: a FAIL is loud in the report, never in the exit code. See the doc comment.
	return cli.ExitOK
}
