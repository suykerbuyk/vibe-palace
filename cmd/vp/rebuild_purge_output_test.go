// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/cli"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/indexstore"
)

// khWarmStore builds an archive-owned store through production: ingest with
// the tier INCLUDED and nothing embedded, so a --drop-transcripts has chunks,
// KG and ledger rows to remove.
func khWarmStore(t *testing.T) string {
	t.Helper()
	vault := setupTestVaultEnv(t)
	stubVaultEmbedder(t, embedder.NewMock(384))
	setHostGlobalTranscripts(t, vault, true)
	drainArchiveOf(t, vault, "p", "SP", time.Date(2026, 5, 13, 2, 0, 0, 0, time.UTC))
	if code := dispatchVP(t, "index", "rebuild", "p", "--vault-root", vault, "--transcripts", "--no-embed"); code != cli.ExitOK {
		t.Fatalf("warm-up: exit %d", code)
	}
	if n := liveOwnedArchiveChunks(t, vault, "p"); n == 0 {
		t.Fatal("PRESENCE: the warm store holds no archive-owned chunk; the purge has nothing to remove")
	}
	setHostGlobalTranscripts(t, vault, false)
	return vault
}

func khRebuild(t *testing.T, vault string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runIndexRebuild(context.Background(), indexRebuildArgs{
		vaultRoot: vault, project: "p", dropTranscripts: true, noEmbed: true,
	}, &out, &errOut)
	return code, out.String(), errOut.String()
}

// `--drop-transcripts` reports what it removed, in counts, and the existing
// summary line is byte-identical in shape.
//
// Q1: the production runIndexRebuild. Q2: counts are absent from the default
// output. Q3: PRESENCE asserts archive-owned chunks existed before the purge.
//
// Mutation (M8): remove the purge line.
func TestDropTranscriptsPrintsWhatItRemoved(t *testing.T) {
	vault := khWarmStore(t)
	code, out, errOut := khRebuild(t, vault)
	if code != cli.ExitOK {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(out, "vp index rebuild: committed=") {
		t.Errorf("the existing summary line is gone:\n%s", out)
	}
	for _, want := range []string{"p transcript-tier purge: committed chunks=", "kg_entities=", "kg_triples=", "ledger_lines=", "vectors_reaped="} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "chunks=0 ") {
		t.Errorf("the purge removed archive chunks yet reports chunks=0:\n%s", out)
	}
}

// C2: the error exit still prints the committed counts, and the exit code is
// the one it has today (ExitSystem).
//
// The purge is made to fail at purge-kg through the exported commit-step seam,
// AFTER purge-ledger and purge-chunks committed.
//
// Mutation (M11): print the purge lines only on success.
func TestAFailedPurgeStillPrintsCountsAndKeepsTheExitCode(t *testing.T) {
	vault := khWarmStore(t)
	boom := errors.New("injected: disk full")
	defer indexstore.SetCommitStepHook(func(step string) error {
		if step == "purge-kg" {
			return boom
		}
		return nil
	})()
	code, out, errOut := khRebuild(t, vault)
	if code != cli.ExitSystem {
		t.Fatalf("exit = %d, want ExitSystem (%d): the exit code of a failed rebuild must not change", code, cli.ExitSystem)
	}
	if !strings.Contains(errOut, "disk full") {
		t.Errorf("the error is no longer printed: %q", errOut)
	}
	if !strings.Contains(out, "transcript-tier purge: committed chunks=") {
		t.Errorf("the committed counts of the steps that committed were not printed on the error exit:\n%s", out)
	}
	if !strings.Contains(out, "attempted (not committed): purge-kg") {
		t.Errorf("the failed step is not named as attempted:\n%s", out)
	}
}
