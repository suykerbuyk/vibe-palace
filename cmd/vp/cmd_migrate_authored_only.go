// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

// ONE-SHOT. `vp migrate authored-only-palace` migrates a populated vault to the
// authored-only layout in one revertible commit (ADR-014 decision 11). Delete
// this file with internal/storage/authored_only_migration.go, both tests, its
// registration, its knownCommands() entry and the committed rehearsal script.
//
// Plan-first: the bare command previews and mutates nothing; --yes applies. The
// apply makes ONE commit over the whole vault and pushes ONLY the tag — it
// never pushes the commit and never --force. It prints the exact
// `git push <remote> HEAD:<branch>` line for the operator.

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// migrationNow is the injected clock (UTC). Tests override it; production reads
// the wall clock. The tag's date and the marker's date are the same UTC day,
// taken once per run from here (ADR-014 decision 4, "UTC throughout").
var migrationNow = func() time.Time { return time.Now().UTC() }

var migrateAuthoredOnlyFlags = []cli.FlagDef{
	{Name: "--vault", Arg: "PATH", Help: "Vault root to migrate (default: the configured vault_path). For rehearsal on a remote-stripped copy."},
	{Name: "--yes", Help: "APPLY the migration: one commit over the whole vault, pushing only the tag. Without this the command only previews."},
	{Name: "--attest", Arg: "FILE", Help: "Operator attestation file. REQUIRED with --yes; its bytes go verbatim into the commit message."},
}

func cmdMigrateAuthoredOnly() *cli.Command {
	return &cli.Command{
		Name:     "migrate authored-only-palace",
		Synopsis: "vp migrate authored-only-palace [--vault PATH] [--yes] [--attest FILE]",
		Description: "The one-time, revertible migration of a populated vault to the authored-only layout: " +
			"authored knowledge-graph records stay tracked (stamped origin: authored), every derived drawer, " +
			"extracted triple and ingested-archives ledger leaves git, and the vault gains the authored_only " +
			"marker and the derived-path ignore lines.\n\n" +
			"PLAN-FIRST: the bare command previews the path (normal or empty-vault), the tag it will use, the " +
			"attestation's required lines, the per-project deletions, the largest tracked blob after the commit, " +
			"the projects that become Projects-only, and the exact push commands; it mutates nothing. Pass --yes " +
			"to apply.\n\n" +
			"--yes makes ONE commit over the whole vault and requires --attest. It tags the parent commit " +
			"pre-authored-only-<UTC-date> and pushes ONLY the tag; it never pushes the commit and never --force. " +
			"It refuses unless the vault is clean, pushed, on a named branch, free of any merge/rebase, with the " +
			"data format not behind the binary, the marker absent, a valid attestation, and the surface floor and " +
			"ancestry satisfied at every remote tip.",
		Flags: migrateAuthoredOnlyFlags,
		Examples: []cli.Example{
			{Cmd: "vp migrate authored-only-palace", Comment: "Preview the migration; mutates nothing"},
			{Cmd: "vp migrate authored-only-palace --yes --attest attest.txt", Comment: "Apply in one commit; push only the tag"},
		},
		Run: func(args []string) int {
			fv, err := cli.ParseFlags(migrateAuthoredOnlyFlags, args)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate authored-only-palace: %v\n", err)
				return cli.ExitUser
			}
			root, err := resolveMigrationVaultRoot(fv.Get("--vault"))
			if err != nil {
				fmt.Fprintf(os.Stderr, "vp migrate authored-only-palace: %v\n", err)
				return cli.ExitUser
			}
			if code := enforceSurfaceOnRoot(root); code != cli.ExitOK {
				return code
			}
			return runAuthoredOnlyMigration(root, fv.Bool("--yes"), fv.Get("--attest"), migrationNow().UTC(), os.Stdout, os.Stderr)
		},
	}
}

// attestationFloor is the minimum vp version every writer host must attest,
// re-derived from the binary's surface and data format (v<surface>.<format>.0),
// never hardcoded (surface-10 reconciliation correction 2).
func attestationFloor() version3 {
	return version3{surface.MCPSurfaceVersion, surface.RequiredDataFormat, 0}
}

// version3 is a parsed v<major>.<minor>.<patch>.
type version3 struct{ major, minor, patch int }

func (v version3) String() string { return fmt.Sprintf("v%d.%d.%d", v.major, v.minor, v.patch) }

func (v version3) atLeast(o version3) bool {
	if v.major != o.major {
		return v.major > o.major
	}
	if v.minor != o.minor {
		return v.minor > o.minor
	}
	return v.patch >= o.patch
}

func parseVersion3(s string) (version3, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return version3{}, fmt.Errorf("want v<major>.<minor>.<patch>, got %q", s)
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return version3{}, fmt.Errorf("want v<major>.<minor>.<patch>, got %q", s)
		}
		out[i] = n
	}
	return version3{out[0], out[1], out[2]}, nil
}

// attestation is the parsed, validated operator attestation.
type attestation struct {
	Bytes           []byte // the file's bytes, verbatim, for the commit message
	WriterHosts     string // the writer-hosts: line value
	QuantumNGClosed string // the quantum-ng-rows-closed: line value
}

// requiredAttestationLines are the two lines the plan prints and the apply
// requires.
func requiredAttestationLines() []string {
	return []string{
		"writer-hosts: <host>=<vp version>@<YYYY-MM-DD>[, <host>=<vp version>@<YYYY-MM-DD> ...] (every version at least " + attestationFloor().String() + ")",
		"quantum-ng-rows-closed: <YYYY-MM-DD>",
	}
}

// parseAttestation reads and validates an attestation file. It refuses when the
// file is unreadable, when either required line is absent or empty, or when any
// writer-hosts version is below the floor (naming the host).
func parseAttestation(path string) (*attestation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read attestation %s: %w", path, err)
	}
	a := &attestation{Bytes: data}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if v, ok := strings.CutPrefix(line, "writer-hosts:"); ok {
			a.WriterHosts = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "quantum-ng-rows-closed:"); ok {
			a.QuantumNGClosed = strings.TrimSpace(v)
		}
	}
	if a.WriterHosts == "" {
		return nil, fmt.Errorf("attestation %s lacks a non-empty `writer-hosts:` line", path)
	}
	if a.QuantumNGClosed == "" {
		return nil, fmt.Errorf("attestation %s lacks a non-empty `quantum-ng-rows-closed:` line", path)
	}
	floor := attestationFloor()
	for _, entry := range strings.Split(a.WriterHosts, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		host, rest, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("attestation writer-hosts entry %q is not <host>=<version>@<date>", entry)
		}
		verStr, _, _ := strings.Cut(rest, "@")
		ver, err := parseVersion3(verStr)
		if err != nil {
			return nil, fmt.Errorf("attestation writer-hosts host %q: %v", strings.TrimSpace(host), err)
		}
		if !ver.atLeast(floor) {
			return nil, fmt.Errorf("attestation writer-hosts host %q runs %s, below the required %s",
				strings.TrimSpace(host), ver.String(), floor.String())
		}
	}
	return a, nil
}

// runAuthoredOnlyMigration plans, prints, and with apply executes. It is the one
// body both the dry run and the apply use, so an override committed between a dry
// run and the apply is re-planned, never replayed.
func runAuthoredOnlyMigration(root string, apply bool, attestPath string, now time.Time, stdout, stderr io.Writer) int {
	const name = "vp migrate authored-only-palace"
	date := now.Format("2006-01-02")

	vault := storage.NewVault(root)

	// The marker-absent precondition is the cheapest, and it is the one a second
	// run trips.
	migrated, err := storage.VaultMigrated(root)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}
	if migrated {
		fmt.Fprintf(stderr, "%s: refusing: the vault already carries the authored_only marker (already migrated)\n", name)
		return cli.ExitUser
	}

	st, err := storage.ReadAuthoredOnlyGitState(root)
	if err != nil {
		fmt.Fprintf(stderr, "%s: refusing: %v\n", name, err)
		return cli.ExitUser
	}
	empty, notEmptyReason, err := storage.VaultEmptyForAuthoredOnly(root, st)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}
	plan, err := storage.PlanAuthoredOnlyMigration(vault)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}
	tag, err := storage.ChooseAuthoredOnlyTag(root, st, date)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}

	// Attestation: optional on the plan (print the two lines, and whether a given
	// file passes), required on the apply.
	var att *attestation
	if attestPath != "" {
		att, err = parseAttestation(attestPath)
		if err != nil && apply {
			fmt.Fprintf(stderr, "%s: refusing: %v\n", name, err)
			return cli.ExitUser
		}
	}

	printAuthoredOnlyPlan(stdout, root, st, plan, tag, empty, notEmptyReason, attestPath, att, err)

	if !apply {
		fmt.Fprintln(stdout, "dry run: nothing was changed; --yes applies the migration (--attest is required)")
		return cli.ExitOK
	}

	if attestPath == "" {
		fmt.Fprintf(stderr, "%s: refusing: --yes requires --attest FILE; the required lines are:\n", name)
		for _, l := range requiredAttestationLines() {
			fmt.Fprintf(stderr, "  %s\n", l)
		}
		return cli.ExitUser
	}
	if att == nil { // unreadable/invalid; parseAttestation already refused on apply
		return cli.ExitUser
	}

	// The remaining preconditions (both paths share clean-and-pushed, data
	// format, ancestry; the normal path adds the surface floor).
	if err := storage.CheckAuthoredOnlyGitPosture(root, st); err != nil {
		fmt.Fprintf(stderr, "%s: refusing: %v\n", name, err)
		return cli.ExitUser
	}
	if err := surface.EnforceFormatFailStop(root, false); err != nil {
		fmt.Fprintf(stderr, "%s: refusing: %v\n", name, err)
		return cli.ExitUser
	}
	if empty {
		if err := storage.RequireRemoteTipsAreAncestors(root, st); err != nil {
			fmt.Fprintf(stderr, "%s: refusing: %v\n", name, err)
			return cli.ExitUser
		}
	} else {
		if err := storage.RequireSurfaceFloorAtRemoteTips(root, surface.MCPSurfaceVersion, st); err != nil {
			fmt.Fprintf(stderr, "%s: refusing: %v\n", name, err)
			return cli.ExitUser
		}
	}

	// Tag the parent and push ONLY the tag, before writing anything.
	if err := storage.CreateAndPushAuthoredOnlyTag(root, st, tag); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}

	msg := authoredOnlyCommitMessage(date, empty, att)
	res, err := vault.ApplyAuthoredOnlyMigration(plan, storage.AuthoredOnlyApplyOptions{
		Date:       date,
		EmptyVault: empty,
		Surface:    surface.MCPSurfaceVersion,
		Message:    msg,
	})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return cli.ExitSystem
	}

	fmt.Fprintf(stdout, "migrated in commit %s (tag %s). %d path(s) removed", res.CommitSHA, tag.Name, len(res.Removed))
	if len(res.ResidueSlugs) > 0 {
		fmt.Fprintf(stdout, "; residue removed for %s", strings.Join(res.ResidueSlugs, ", "))
	}
	fmt.Fprintln(stdout, ".")
	printAuthoredOnlyPushLines(stdout, st, empty)
	return cli.ExitOK
}

// authoredOnlyCommitMessage builds the commit message, with the attestation
// bytes verbatim under a Migration-Attestation: heading after the summary.
func authoredOnlyCommitMessage(date string, empty bool, att *attestation) string {
	path := "normal"
	if empty {
		path = "empty-vault"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Migrate the vault to the authored-only layout (%s path)\n\n", path)
	fmt.Fprintf(&b, "authored_only = %q. Derived drawers, extracted triples and the ingest ledger\n", date)
	b.WriteString("leave git; authored knowledge-graph records stay tracked. Dropped bytes are\n")
	b.WriteString("recoverable at the pre-authored-only tag.\n\n")
	b.WriteString("Migration-Attestation:\n")
	b.Write(att.Bytes)
	if !strings.HasSuffix(string(att.Bytes), "\n") {
		b.WriteByte('\n')
	}
	return b.String()
}

// printAuthoredOnlyPushLines prints the exact fast-forward push command for each
// remote, what to do if git rejects it, and the rollback guidance (Scope 5): the
// revert command and its consequences, which differ by path.
func printAuthoredOnlyPushLines(w io.Writer, st *storage.AuthoredOnlyGitState, empty bool) {
	if len(st.Tips) == 0 {
		fmt.Fprintln(w, "No remote: nothing to push.")
	} else {
		fmt.Fprintln(w, "\nThe commit is NOT pushed. Publish it yourself with a fast-forward push:")
		for _, t := range st.Tips {
			fmt.Fprintf(w, "  git push %s HEAD:%s\n", t.Remote, st.Branch)
		}
		fmt.Fprintln(w, "Do NOT pull or sync onto the migration commit. If git rejects the push, the remote")
		fmt.Fprintln(w, "moved: run `git reset --hard <tag>`, then `vp vault sync`, then re-run this command.")
	}
	printAuthoredOnlyRevertLines(w, empty)
}

// printAuthoredOnlyRevertLines prints the rollback command and its consequences
// (Scope 5; ADR-014 lines 1185-1205). Rollback restores the data only: the tag
// stays and the binaries stay at v9. On the empty-vault path the revert removes
// the vault's ONLY surface stamp, so the operator must re-stamp in the SAME push.
func printAuthoredOnlyRevertLines(w io.Writer, empty bool) {
	fmt.Fprintln(w, "\nTo roll back (restores the data only; the tag stays, binaries stay at v9):")
	fmt.Fprintln(w, "  git revert HEAD    # then push the revert with the same fast-forward line above")
	fmt.Fprintln(w, "Consequences of the revert:")
	fmt.Fprintln(w, "  - tidy and pull fall back to their pre-migration rules;")
	fmt.Fprintln(w, "  - search reads the re-tracked drawers again;")
	fmt.Fprintln(w, "  - capture still writes nothing derived.")
	if empty {
		fmt.Fprintln(w, "  - WARNING (empty-vault path): the revert removes Audits/.surface, the vault's ONLY")
		fmt.Fprintln(w, "    surface stamp. A surface-9-or-below binary then becomes UNGATED on this vault.")
		fmt.Fprintln(w, "    You MUST re-stamp Audits/.surface at the current surface in the SAME push as the")
		fmt.Fprintln(w, "    revert, so the vault is never left ungated on a remote.")
	} else {
		fmt.Fprintln(w, "  - surface 9 survives: the stamps at 9 committed at every remote tip remain, and the")
		fmt.Fprintln(w, "    gate takes the maximum, so no re-stamp is needed on this path.")
	}
}

// printAuthoredOnlyPlan renders the plan preview.
func printAuthoredOnlyPlan(w io.Writer, root string, st *storage.AuthoredOnlyGitState, plan *storage.AuthoredOnlyPlan, tag storage.AuthoredOnlyTag, empty bool, notEmptyReason, attestPath string, att *attestation, attErr error) {
	printVaultRoot(w, root)
	if empty {
		fmt.Fprintln(w, "path: empty-vault (no tracked palace/ content and no .surface stamp at HEAD or any tip)")
	} else {
		fmt.Fprintln(w, "path: normal")
		if notEmptyReason != "" {
			fmt.Fprintf(w, "  (not empty: %s)\n", notEmptyReason)
		}
	}
	if tag.Reused {
		fmt.Fprintf(w, "tag: %s (reused — already on HEAD)\n", tag.Name)
	} else {
		fmt.Fprintf(w, "tag: %s\n", tag.Name)
	}

	fmt.Fprintln(w, "\nattestation — the apply requires a --attest file with these two lines:")
	for _, l := range requiredAttestationLines() {
		fmt.Fprintf(w, "  %s\n", l)
	}
	switch {
	case attestPath == "":
		fmt.Fprintln(w, "  (no --attest given)")
	case attErr != nil:
		fmt.Fprintf(w, "  --attest %s: INVALID — %v\n", attestPath, attErr)
	case att != nil:
		fmt.Fprintf(w, "  --attest %s: valid\n", attestPath)
	}

	var totalDrop, projectsOnly int
	for _, pr := range plan.Projects {
		drop := len(pr.DropTriples) + len(pr.DropDerived)
		if pr.EntitiesRel != "" && pr.EntitiesAuthored == 0 && pr.EntitiesExtracted > 0 {
			drop++
		}
		totalDrop += drop
		authoredKept := pr.AuthoredTriplesKept + pr.EntitiesAuthored
		fmt.Fprintf(w, "\nproject %s:\n", pr.Project)
		fmt.Fprintf(w, "  delete %d tracked file(s) (%d extracted triple(s), %d derived path(s))\n",
			drop, len(pr.DropTriples), len(pr.DropDerived))
		fmt.Fprintf(w, "  keep %d authored record(s)\n", authoredKept)
		// A project left with no authored KG content becomes Projects-only.
		if authoredKept == 0 {
			projectsOnly++
		}
	}
	fmt.Fprintf(w, "\ntotal tracked files to delete: %d\n", totalDrop)
	fmt.Fprintf(w, "projects becoming Projects-only (new vp_list_projects drift rows): %d\n", projectsOnly)
	if len(plan.ResidueSlugs) > 0 {
		fmt.Fprintf(w, "legacy residue to remove for departed slug(s): %s\n", strings.Join(plan.ResidueSlugs, ", "))
	}

	if max, relp, err := largestTrackedBlobAfter(root, plan); err == nil {
		fmt.Fprintf(w, "largest tracked blob after the commit: %d bytes (%s)\n", max, relp)
	}
	fmt.Fprintln(w, "\nrecommended first (optional): vp index rebuild --all --no-embed, with --skip <slug> for every")
	fmt.Fprintln(w, "project about to leave this vault (the migration does not need it).")
	printAuthoredOnlyPushLines(w, st, empty)
}

// largestTrackedBlobAfter predicts the largest tracked blob after the commit
// from the CURRENT tracked blob list minus the paths the apply drops (12-S5). It
// never reports the pre-commit maximum of a dropped drawer.
func largestTrackedBlobAfter(root string, plan *storage.AuthoredOnlyPlan) (int64, string, error) {
	drop := map[string]bool{}
	for _, p := range plan.DropPaths() {
		drop[p] = true
	}
	return storage.LargestTrackedBlobExcluding(root, drop)
}
