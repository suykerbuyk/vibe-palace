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

	path, _, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
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
	v := testVault(t)

	path, _, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
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
	v := testVault(t)

	path, _, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
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

	if _, _, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
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
	v := testVault(t)

	path, _, err := v.WriteHostScoringConfig("proj", nil, 0)
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

// 🔴 THE BLOCKER THIS RELEASE WAS SENT BACK FOR.
//
// Every layer of this config stack replaces a scoring room WHOLE. A tuning run
// proposes only the tier it changed, so writing that proposal verbatim into the
// host-local file published a partial room that erased every other tier the
// vault file held. Measured before the fix: a vault room of
// high=[kubernetes] medium=[deploy] low=[ops] resolved to
// high=[] medium=[terraform] low=[] after one apply.
func TestWriteHostScoringConfigCarriesTheWholeRoomFromTheVault(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")
	writeVaultProjectConfig(t, v, "proj",
		"[palace.scoring.rooms.devops]\nhigh = [\"kubernetes\"]\nmedium = [\"deploy\"]\nlow = [\"ops\"]\n")

	before, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatal(err)
	}
	b := before.PalaceScoringOverrides["devops"]

	// Exactly the shape proposalsToOverrides / discoveryProposalsToOverrides
	// build: one room, one tier, the others nil.
	_, carried, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"devops": {Medium: []string{"terraform"}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	after, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatal(err)
	}
	a := after.PalaceScoringOverrides["devops"]

	for _, tc := range []struct {
		tier         string
		was, now     []string
		wantKeywords []string
	}{
		{"high", b.High, a.High, []string{"kubernetes"}},
		{"medium", b.Medium, a.Medium, []string{"deploy", "terraform"}},
		{"low", b.Low, a.Low, []string{"ops"}},
	} {
		for _, kw := range tc.wantKeywords {
			if !containsKeyword(tc.now, kw) {
				t.Errorf("%s: %v -> %v, lost %q", tc.tier, tc.was, tc.now, kw)
			}
		}
	}

	// The copy is real data movement between files, so it must be reported.
	vaultPath, err := v.ProjectConfigFile("proj")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(carried, "\n")
	for _, want := range []string{"devops:", "high=[kubernetes]", "low=[ops]", vaultPath} {
		if !strings.Contains(joined, want) {
			t.Errorf("transcript does not name %q:\n%s", want, joined)
		}
	}
	// It carried "deploy" into medium too, but the proposal already named that
	// tier, so the line reports the tier's carried keyword, not the new one.
	if strings.Contains(joined, "terraform") {
		t.Errorf("transcript reports a keyword the run PROPOSED as carried:\n%s", joined)
	}
	_ = hostPath
}

// The same completion protects the host GLOBAL config's rooms, which layer 3
// could already erase before R2 existed.
func TestWriteHostScoringConfigCarriesTheWholeRoomFromTheGlobalConfig(t *testing.T) {
	v, _ := hostLocalEnv(t, "proj",
		"[palace.scoring.rooms.general]\nhigh = [\"g-high\"]\nlow = [\"g-low\"]\n")

	if _, _, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"general": {Medium: []string{"new"}},
	}, 0); err != nil {
		t.Fatal(err)
	}
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.PalaceScoringOverrides["general"]
	if !containsKeyword(r.High, "g-high") || !containsKeyword(r.Low, "g-low") || !containsKeyword(r.Medium, "new") {
		t.Errorf("high=%v medium=%v low=%v, want the global config's tiers carried plus the proposal",
			r.High, r.Medium, r.Low)
	}
}

// A room no lower layer names is written exactly as proposed, and reports
// nothing: there is nothing to carry.
func TestWriteHostScoringConfigCarriesNothingForANewRoom(t *testing.T) {
	v, _ := hostLocalEnv(t, "proj", "")
	_, carried, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"brandnew": {Medium: []string{"kw"}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(carried) != 0 {
		t.Errorf("transcript = %v, want empty: no lower layer names this room", carried)
	}
}

// The transcript reports a copy ONCE. A second identical run has nothing left
// to carry, so it must say nothing rather than re-announce the same move.
func TestWriteHostScoringConfigTranscriptIsSilentOnARerun(t *testing.T) {
	v, _ := hostLocalEnv(t, "proj", "")
	writeVaultProjectConfig(t, v, "proj",
		"[palace.scoring.rooms.devops]\nhigh = [\"kubernetes\"]\nlow = [\"ops\"]\n")

	_, first, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"devops": {Medium: []string{"terraform"}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("the first write carried the vault's tiers but reported nothing")
	}
	_, second, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"devops": {Medium: []string{"terraform"}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Errorf("the second write re-announced a copy it did not make: %v", second)
	}
}

// 🔴 ANTI-DIVERGENCE PIN. scoringRoomsBelowHost walks layers 1-3 in its own
// code so LoadConfig does not pay for attribution on every call. This is what
// keeps the two walks from drifting: with no host-local file, the resolution
// they produce must be the same one.
func TestScoringRoomsBelowHostMatchesLoadConfig(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj",
		"[palace.scoring.rooms.general]\nhigh = [\"g-high\"]\nmedium = [\"g-medium\"]\n"+
			"\n[palace.scoring.rooms.onlyglobal]\nlow = [\"g-low\"]\n")
	writeVaultProjectConfig(t, v, "proj",
		"[palace.scoring.rooms.general]\nmedium = [\"v-medium\"]\n"+
			"\n[palace.scoring.rooms.onlyvault]\nhigh = [\"v-high\"]\n")
	if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
		t.Fatalf("this test requires no host-local file (stat err=%v)", err)
	}

	below, source, err := v.scoringRoomsBelowHost("proj")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := v.LoadConfig("proj")
	if err != nil {
		t.Fatal(err)
	}

	if len(below) != len(cfg.PalaceScoringOverrides) {
		t.Fatalf("below-host rooms %v, LoadConfig rooms %v", below, cfg.PalaceScoringOverrides)
	}
	for name, want := range cfg.PalaceScoringOverrides {
		got := below[name]
		if strings.Join(got.High, ",") != strings.Join(want.High, ",") ||
			strings.Join(got.Medium, ",") != strings.Join(want.Medium, ",") ||
			strings.Join(got.Low, ",") != strings.Join(want.Low, ",") {
			t.Errorf("room %q: below-host %+v, LoadConfig %+v", name, got, want)
		}
	}

	// Attribution names the file that actually set each room.
	globalPath, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	vaultPath, err := v.ProjectConfigFile("proj")
	if err != nil {
		t.Fatal(err)
	}
	if source["onlyglobal"] != globalPath {
		t.Errorf("onlyglobal attributed to %q, want %q", source["onlyglobal"], globalPath)
	}
	if source["general"] != vaultPath || source["onlyvault"] != vaultPath {
		t.Errorf("general=%q onlyvault=%q, want both %q", source["general"], source["onlyvault"], vaultPath)
	}
}

// 🔴 A REAL IDEMPOTENCY PIN, ON BYTES.
//
// TestWriteHostScoringConfigPreservesTextAcrossWrites is strings.Contains-only
// and stays green under a writer that appends a duplicate on every run. This
// compares the sha256 of the whole file, so a non-idempotent merge reds. It
// matters because R4 deletes the vault shim, which is the only other place the
// splice core's idempotency is pinned.
func TestWriteHostScoringConfigIsIdempotentOnBytes(t *testing.T) {
	v, _ := hostLocalEnv(t, "proj", "")
	writeVaultProjectConfig(t, v, "proj",
		"[palace.scoring.rooms.devops]\nhigh = [\"kubernetes\"]\nlow = [\"ops\"]\n")
	rooms := map[string]ScoringRoomOverride{
		"devops":  {Medium: []string{"terraform"}, High: []string{"helm"}},
		"testing": {Low: []string{"smoke"}},
	}

	path, _, err := v.WriteHostScoringConfig("proj", rooms, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := sha256Of(t, path)

	for i := 2; i <= 3; i++ {
		if _, _, err := v.WriteHostScoringConfig("proj", rooms, 0); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if got := sha256Of(t, path); got != first {
			body, _ := os.ReadFile(path)
			t.Fatalf("write %d changed the file; sha256 %s -> %s\n%s", i, first, got, body)
		}
	}
}
