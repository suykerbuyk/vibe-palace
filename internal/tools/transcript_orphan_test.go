// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

const orphanRel = "Projects/test-proj/transcripts/orphan.jsonl.zst"

// writeOrphan leaves an aged lone archive (no manifest) in the vault.
func writeOrphan(t *testing.T, root string) {
	t.Helper()
	dirtyFile(t, root, orphanRel, "zst\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(orphanRel)), old, old); err != nil {
		t.Fatal(err)
	}
}

func orphanedField(t *testing.T, out any) []string {
	t.Helper()
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", out)
	}
	got, ok := m["orphaned"].([]string)
	if !ok {
		t.Fatalf("payload has no orphaned []string field: %#v", m)
	}
	return got
}

// T7c: the MCP tidy payloads (dry run and real) carry the orphan, in the field
// and in the summary text.
func TestOrphan_MCPTidyPayloadCarriesTheOrphan(t *testing.T) {
	root := initVaultRepo(t)
	writeOrphan(t, root)
	tool := VaultTidyTool(storage.NewVault(root))

	for _, params := range []string{`{"dry_run":true}`, `{"push":false}`} {
		res, err := tool.Handler(context.Background(), json.RawMessage(params))
		if err != nil {
			t.Fatalf("%s: %v", params, err)
		}
		if got := orphanedField(t, res); !slices.Contains(got, orphanRel) {
			t.Errorf("%s: orphaned = %v, want %s", params, got, orphanRel)
		}
		if summary, _ := res.(map[string]any)["summary"].(string); !strings.Contains(summary, orphanRel) {
			t.Errorf("%s: the summary does not name the orphan: %q", params, summary)
		}
	}
}

// T7c (sync): a bare MCP sync carries the orphan in the payload and the output
// text, and is not refused by it.
func TestOrphan_MCPSyncPayloadCarriesTheOrphan(t *testing.T) {
	root := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	gitT(t, root, "push", "origin", "main")
	writeOrphan(t, root)

	params, _ := json.Marshal(vaultSyncParams{Action: "sync"})
	res, err := VaultSyncTool(storage.NewVault(root)).Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("sync refused on an orphan: %v", err)
	}
	if got := orphanedField(t, res); !slices.Contains(got, orphanRel) {
		t.Errorf("orphaned = %v", got)
	}
	if out, _ := res.(map[string]any)["output"].(string); !strings.Contains(out, orphanRel) {
		t.Errorf("sync output does not name the orphan:\n%s", out)
	}
}

// An orphan must not block an MCP push (the original defect), and a real dirty
// file still does — with a refusal that lists only the real dirt.
func TestOrphan_MCPPushNotBlockedByOrphanButByRealDirt(t *testing.T) {
	root := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	gitT(t, root, "push", "origin", "main")
	dirtyFile(t, root, "committed.md", "c\n")
	gitT(t, root, "add", "committed.md")
	gitT(t, root, "commit", "-m", "unpushed")
	writeOrphan(t, root)

	tool := VaultSyncTool(storage.NewVault(root))
	params, _ := json.Marshal(vaultSyncParams{Action: "push"})
	if _, err := tool.Handler(context.Background(), params); err != nil {
		t.Fatalf("push blocked by an orphan: %v", err)
	}
	head := strings.TrimSpace(gitT(t, root, "rev-parse", "HEAD"))
	if got := strings.TrimSpace(gitT(t, bare, "rev-parse", "main")); got != head {
		t.Fatalf("remote main = %s, want %s: the push did not deliver", got, head)
	}

	dirtyFile(t, root, "stray -> name.txt", "x\n")
	_, err := tool.Handler(context.Background(), params)
	if err == nil {
		t.Fatal("push must refuse on real dirt")
	}
	if strings.Contains(err.Error(), "orphan.jsonl.zst") {
		t.Errorf("the refusal lists the exempt orphan: %v", err)
	}
	if !strings.Contains(err.Error(), "stray -> name.txt") {
		t.Errorf("the refusal does not name the real dirty file as ONE path: %v", err)
	}
}

// Delta nit: porcelainDirtyPaths reads a quoted path whole — a newline or
// " -> " in a filename cannot mis-render.
func TestPorcelainDirtyPaths_QuotedPathsAreLiteral(t *testing.T) {
	got := porcelainDirtyPaths("?? \"a -> b.txt\"\n?? \"two\\nlines.txt\"\n?? plain.txt\nR  old.txt -> new.txt\n")
	want := []string{"a -> b.txt", "two\nlines.txt", "plain.txt", "new.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("paths = %q, want %q", got, want)
	}
}

// T7d: the bootstrap carries an orphan alert from the same scan, in the
// directive, without setting vault_dirt; and is silent when there is none.
func TestOrphan_BootstrapAlert(t *testing.T) {
	vault := newGitBackedTestVault(t)
	assertVaultClean(t, vault.Root)
	if br := bootstrapOn(t, vault); strings.Contains(br.PostBootstrapInstructions, "ORPHANED TRANSCRIPTS") {
		t.Fatalf("orphan alert on a vault with no orphan:\n%s", br.PostBootstrapInstructions)
	}

	writeOrphan(t, vault.Root)
	br := bootstrapOn(t, vault)
	if !strings.Contains(br.PostBootstrapInstructions, vaultOrphanMessage(1)) {
		t.Errorf("no orphan alert in the directive:\n%s", br.PostBootstrapInstructions)
	}
	if br.VaultDirt != nil {
		t.Errorf("an orphan is not dirt: vault_dirt = %#v", br.VaultDirt)
	}
}

// F4: a path with edge whitespace survives the storage render → tools parse
// round trip as exactly itself (the line reader's TrimSpace must not eat it).
func TestPorcelainDirtyPaths_EdgeWhitespaceRoundTrip(t *testing.T) {
	root := initVaultRepo(t)
	bare := t.TempDir()
	gitT(t, bare, "init", "--bare", "-b", "main")
	gitT(t, root, "remote", "add", "origin", bare)
	gitT(t, root, "push", "origin", "main")
	names := []string{" lead.txt", "trail.txt ", "a -> b.txt"}
	for _, n := range names {
		dirtyFile(t, root, n, "x\n")
	}
	_, err := storage.PushPlain(root, []string{"origin"})
	var dte *storage.DirtyTreeError
	if !errors.As(err, &dte) {
		t.Fatalf("want *DirtyTreeError, got %v", err)
	}
	got := porcelainDirtyPaths(dte.Porcelain)
	slices.Sort(got)
	want := slices.Clone(names)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("round trip = %q, want %q\nporcelain:\n%s", got, want, dte.Porcelain)
	}
}
