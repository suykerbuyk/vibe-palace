// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/suykerbuyk/vibe-palace/internal/surface"
)

// runVaultMergeDriverExit is the testable core of the `vp vault merge-driver
// <ancestor> <ours> <theirs>` subcommand. It is invoked by git as the
// configured merge driver for `*.surface` files (see EnsureMergeDriverInstalled).
//
// Each input file is a TOML stamp; the resolution writes
// max(ours.Surface, theirs.Surface) back to <ours> with the auxiliary fields
// cleared (last_writer/last_write_at are re-stamped on the next vault write).
//
// Exit semantics (git's merge-driver contract: 0 = resolved, nonzero =
// conflict left for the operator):
//   - 0 on successful resolution.
//   - 1 if any of the three inputs cannot be parsed as a stamp (in which case
//     <ours> is overwritten with conventional `<<<<<<< / ======= / >>>>>>>`
//     text-conflict markers so the operator can hand-resolve).
//   - 1 if the argument count is wrong.
//
// The ancestor file is informational only — surface stamps are monotonic, so
// the resolution is independent of the merge base. A missing ancestor is
// tolerated; a malformed ancestor is treated as a parse failure.
func runVaultMergeDriverExit(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "vp vault merge-driver: usage: vp vault merge-driver <ancestor> <ours> <theirs>")
		return 1
	}
	return mergeDriver(args[0], args[1], args[2])
}

// mergeDriver does the actual work and returns the desired exit code (0 on
// success, 1 on malformed input). Split out so it is directly testable
// without spawning a subprocess.
func mergeDriver(ancestor, ours, theirs string) int {
	// Read the three files. Ancestor missing-on-disk is tolerated (treated
	// as the zero stamp); ours and theirs must exist.
	ancestorData, ancestorErr := readMergeInput(ancestor)
	oursData, oursErr := os.ReadFile(ours)
	theirsData, theirsErr := os.ReadFile(theirs)

	if oursErr != nil || theirsErr != nil {
		writeTextConflict(ours, oursData, theirsData)
		return 1
	}

	// Parse each input. Ancestor is informational only — if it fails to
	// parse we still bail to text conflict (spec: any of the three files
	// unparseable → exit 1).
	var oursStamp, theirsStamp surface.Stamp
	if err := toml.Unmarshal(oursData, &oursStamp); err != nil {
		writeTextConflict(ours, oursData, theirsData)
		return 1
	}
	if err := toml.Unmarshal(theirsData, &theirsStamp); err != nil {
		writeTextConflict(ours, oursData, theirsData)
		return 1
	}
	if ancestorErr == nil && len(ancestorData) > 0 {
		var ancestorStamp surface.Stamp
		if err := toml.Unmarshal(ancestorData, &ancestorStamp); err != nil {
			writeTextConflict(ours, oursData, theirsData)
			return 1
		}
		_ = ancestorStamp // informational only — monotonic stamps don't need it
	}

	// Pick max(ours, theirs).
	winner := oursStamp.Surface
	if theirsStamp.Surface > winner {
		winner = theirsStamp.Surface
	}

	resolved := surface.Stamp{
		Surface:     winner,
		LastWriter:  "",
		LastWriteAt: "",
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(resolved); err != nil {
		// Highly unlikely — encoding a fixed-shape struct.
		writeTextConflict(ours, oursData, theirsData)
		return 1
	}
	if err := os.WriteFile(ours, buf.Bytes(), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "vp vault merge-driver: write resolved %s: %v\n", ours, err)
		return 1
	}
	return 0
}

// readMergeInput reads a merge-driver input file. Missing files return
// (nil, nil) — distinct from a read error — so callers can tell "absent" from
// "unreadable". Any other error is returned verbatim.
func readMergeInput(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// writeTextConflict emits a conventional git text-conflict block to <ours> so
// that, paired with exit-1, git treats the file as unresolved and the
// operator can resolve by hand. Best-effort: any write error is logged but
// not retried (the merge is already failing).
func writeTextConflict(ours string, oursData, theirsData []byte) {
	var buf bytes.Buffer
	buf.WriteString("<<<<<<< ours\n")
	buf.Write(oursData)
	if len(oursData) > 0 && oursData[len(oursData)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("=======\n")
	buf.Write(theirsData)
	if len(theirsData) > 0 && theirsData[len(theirsData)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString(">>>>>>> theirs\n")
	if err := os.WriteFile(ours, buf.Bytes(), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "vp vault merge-driver: write text conflict to %s: %v\n", ours, err)
	}
}
