// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// treeFiles lists every file under root, relative and slash-separated.
func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// 🔴 THE POINT OF R2: the writer puts the file, its lock sidecar and its stamp
// OUTSIDE the vault. A vault that gains even one file here is the defect this
// release exists to remove.
func TestWriteHostScoringConfigWritesNothingIntoTheVault(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	v := testVault(t)

	// The vault as it stands before the write, and a project file to prove the
	// writer does not touch it.
	writeVaultProjectConfig(t, v, "proj", "[palace.scoring.rooms.general]\nhigh = [\"vault-high\"]\n")
	vaultProjPath, err := v.ProjectConfigFile("proj")
	if err != nil {
		t.Fatal(err)
	}
	beforeTree := treeFiles(t, v.Root)
	beforeHash := sha256Of(t, vaultProjPath)

	path, err := WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"general": {High: []string{"host-high"}},
	}, 0)
	if err != nil {
		t.Fatalf("WriteHostScoringConfig: %v", err)
	}

	if want := filepath.Join(xdg, "vibe-palace", "projects", "proj.toml"); path != want {
		t.Errorf("wrote %q, want %q", path, want)
	}
	if got := treeFiles(t, v.Root); strings.Join(got, ",") != strings.Join(beforeTree, ",") {
		t.Errorf("the vault tree changed:\n before %v\n after  %v", beforeTree, got)
	}
	if sha256Of(t, vaultProjPath) != beforeHash {
		t.Error("the vault's own project config was modified by the host-local writer")
	}
	// The lock sidecar belongs beside the host config, never in the vault.
	if _, err := os.Stat(filepath.Join(xdg, "vibe-palace", ".vp-locks")); err != nil {
		t.Errorf("no lock sidecar directory beside the host config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(v.Root, ".vp-locks")); !os.IsNotExist(err) {
		t.Errorf("a lock sidecar was created inside the vault (stat err=%v)", err)
	}
	// No surface stamp: the destination is not a vault.
	if _, err := os.Stat(filepath.Join(xdg, "vibe-palace", ".surface")); !os.IsNotExist(err) {
		t.Errorf("the host-local write stamped a surface version (stat err=%v)", err)
	}
}

// A file the writer creates carries a [meta] block mirroring the global
// config's version fields, so a later release has a schema version to gate on.
func TestWriteHostScoringConfigSeedsMeta(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	path, err := WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"general": {High: []string{"host-high"}},
	}, 0)
	if err != nil {
		t.Fatalf("WriteHostScoringConfig: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"[meta]",
		"version_major = 1",
		"version_minor = 1",
		`kind = "host-project"`,
		"[palace.scoring.rooms.general]",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("created file does not carry %q:\n%s", want, text)
		}
	}
	// It must parse, and its [meta] must be the one LoadConfig decodes rather
	// than reporting as an ignored key.
	var hl hostProjectLayer
	if _, derr := toml.DecodeFile(path, &hl); derr != nil {
		t.Fatalf("the file the writer created does not decode: %v", derr)
	}
	if hl.Meta.VersionMajor != CurrentVersionMajor || hl.Meta.Kind != MetaKindHostProject {
		t.Errorf("meta = %+v, want version_major %d and kind %q", hl.Meta, CurrentVersionMajor, MetaKindHostProject)
	}
}

// A second write preserves everything outside the scoring sections: the seeded
// header, and text the operator pasted above the scoring block.
//
// 🔴 ONE KNOWN, PRE-EXISTING LIMIT, AND IT IS NOT R2's. spliceScoringSections
// replaces the scoring section RANGE, and a comment line sitting between the
// end of that range and the next section header belongs to the range it
// follows — so such a comment is dropped, while the section after it survives.
// Verified on main 9769df4 with the vault writer, before this release existed:
// `comment kept=false llm kept=true`. R2 changes neither the splice nor that
// behaviour; the test pastes where the guarantee holds and pins the surviving
// section for the other case.
func TestWriteHostScoringConfigPreservesTextAcrossWrites(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	path, err := WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"general": {High: []string{"first"}},
	}, 0)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Paste ABOVE the scoring block, which is where the splice guarantees
	// preservation, plus a section AFTER it to pin that the splice stops at the
	// next header.
	const pastedAbove = "# operator note: keep this line\n[palace.llm]\nmodel = \"hand-edited\"\n\n"
	cut := strings.Index(string(first), "[palace.scoring")
	if cut < 0 {
		t.Fatalf("no scoring section in the created file:\n%s", first)
	}
	edited := string(first[:cut]) + pastedAbove + string(first[cut:])
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"general": {High: []string{"second"}},
	}, 0); err != nil {
		t.Fatalf("second write: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(after)
	for _, want := range []string{
		"# operator note: keep this line",
		`model = "hand-edited"`,
		"[meta]",
		`kind = "host-project"`,
		"first", // the merge adds to a tier; it does not replace the file
		"second",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the second write lost %q:\n%s", want, text)
		}
	}
}

// A no-op call creates nothing: an empty proposal set must not leave a config
// file behind that was not there before.
func TestWriteHostScoringConfigNoOpCreatesNothing(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	path, err := WriteHostScoringConfig("proj", nil, 0)
	if err != nil {
		t.Fatalf("WriteHostScoringConfig: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a no-op write created %s (stat err=%v)", path, err)
	}
	if files := treeFiles(t, xdg); len(files) != 0 {
		t.Errorf("a no-op write created %v", files)
	}
}
