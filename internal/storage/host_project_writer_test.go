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
//
// It pins the RESOLUTION, on layers that can be read. The two walks diverge on
// purpose when a layer cannot be read — LoadConfig skips it, the writer refuses
// — and that half is pinned by
// TestWriteHostScoringConfigRefusesAnUnreadableLowerLayer.
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

// 🔴 AN UNREADABLE LOWER LAYER MUST REFUSE THE WRITE, NOT PRODUCE A PARTIAL ROOM.
//
// The widening reads the layers below to complete each room. If that read
// silently skips a layer it cannot stat, completeRoomsFromBelow finds no room
// below, writes the proposal alone, and the host-local file — which outranks
// the vault and replaces the room WHOLE — drops every tier that layer held.
// That is the original data loss reached through an unreadable file instead of
// an absent tier, and it is silent in all three channels at once: no error, no
// transcript, no log.
//
// Both shapes here are the ones a bare `os.Stat(p) == nil` converts into
// "absent": a search-permission failure on the parent directory, and a dangling
// symlink.
func TestWriteHostScoringConfigRefusesAnUnreadableLowerLayer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny access")
	}

	// The control: with the layer readable, the room is widened and written.
	t.Run("control, readable", func(t *testing.T) {
		v, hostPath := hostLocalEnv(t, "proj", "")
		writeVaultProjectConfig(t, v, "proj",
			"[palace.scoring.rooms.general]\nhigh = [\"kubernetes\"]\n")
		_, carried, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
			"general": {High: []string{"docker"}},
		}, 0)
		if err != nil {
			t.Fatalf("control write failed: %v", err)
		}
		if len(carried) == 0 {
			t.Error("control carried nothing; this test no longer probes what it claims")
		}
		body, rerr := os.ReadFile(hostPath)
		if rerr != nil {
			t.Fatal(rerr)
		}
		if !strings.Contains(string(body), "kubernetes") {
			t.Errorf("control did not carry the vault tier:\n%s", body)
		}
	})

	t.Run("vault project config unreadable (parent mode 000)", func(t *testing.T) {
		v, hostPath := hostLocalEnv(t, "proj", "")
		writeVaultProjectConfig(t, v, "proj",
			"[palace.scoring.rooms.general]\nhigh = [\"kubernetes\"]\n")
		projPath, err := v.ProjectConfigFile("proj")
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(projPath)
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Skipf("cannot chmod %s: %v", dir, err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		// Confirm the shape really is unreadable, so a passing assertion below
		// cannot be an artefact of a permissive filesystem.
		if _, serr := os.Stat(projPath); serr == nil {
			t.Skip("the filesystem still permits stat through a mode-000 directory")
		}

		_, _, werr := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
			"general": {High: []string{"docker"}},
		}, 0)
		if werr == nil {
			body, _ := os.ReadFile(hostPath)
			t.Fatalf("the write SUCCEEDED with an unreadable vault layer; host file is now:\n%s", body)
		}
		if !strings.Contains(werr.Error(), projPath) {
			t.Errorf("error does not name the unreadable file %s: %v", projPath, werr)
		}
		// And it wrote nothing: a refusal that still left a partial room behind
		// would be the same defect with an error message attached.
		if _, serr := os.Stat(hostPath); !os.IsNotExist(serr) {
			body, _ := os.ReadFile(hostPath)
			t.Errorf("the refusal still wrote %s:\n%s", hostPath, body)
		}
	})

	t.Run("vault project config is a dangling symlink", func(t *testing.T) {
		v, hostPath := hostLocalEnv(t, "proj", "")
		projPath, err := v.ProjectConfigFile("proj")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(projPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "gone.toml"), projPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		_, _, werr := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
			"general": {High: []string{"docker"}},
		}, 0)
		if werr == nil {
			body, _ := os.ReadFile(hostPath)
			t.Fatalf("the write SUCCEEDED with a dangling vault layer; host file is now:\n%s", body)
		}
		if _, serr := os.Stat(hostPath); !os.IsNotExist(serr) {
			t.Errorf("the refusal still wrote %s", hostPath)
		}
	})

	t.Run("host global config unreadable (dangling symlink)", func(t *testing.T) {
		v, hostPath := hostLocalEnv(t, "proj", "")
		globalPath, err := VaultConfigFilePath()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(globalPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "gone.toml"), globalPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}

		_, _, werr := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
			"general": {High: []string{"docker"}},
		}, 0)
		if werr == nil {
			t.Fatalf("the write SUCCEEDED with an unreadable host global layer")
		}
		if _, serr := os.Stat(hostPath); !os.IsNotExist(serr) {
			t.Errorf("the refusal still wrote %s", hostPath)
		}
	})
}

// roomTiersOf reads the host-local file's tiers for one room, or zero if the
// file or the room is absent. It decodes the real bytes rather than trusting a
// return value: what the transcript is checked against below has to be what the
// file actually holds.
func roomTiersOf(t *testing.T, path, room string) tomlRoomScoring {
	t.Helper()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return tomlRoomScoring{}
	}
	var hl hostProjectLayer
	if _, err := toml.DecodeFile(path, &hl); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return hl.Palace.Scoring.Rooms[room]
}

// 🔴 THE TRANSCRIPT MUST NAME EVERY KEYWORD THE FILE GAINED THAT THE RUN DID
// NOT ASK FOR.
//
// This pins containsKeyword against mergeKeywordTier — the agreement that makes
// the transcript truthful — without calling containsKeyword. The oracle diffs
// the host file's real bytes before and after, so a loosened containsKeyword
// cannot blind it: the merge still adds the keyword, the oracle still sees it
// arrive, and the transcript's silence becomes a lie this test can see.
//
// Production now derives both sides from keywordKey, so no second copy exists
// to disagree. This is the belt for that pair of braces: it catches an edit
// that stops going through keywordKey at all.
//
// 🔴 IT JUDGES BY keywordKey, NOT BY BYTE EQUALITY, AND THAT IS THE POINT.
// keywordKey is the identity, and a keyword the run already asked for under
// that identity is not news. A byte-strict oracle would red on a legitimate
// future move to case-insensitive dedup — blocking the very change keywordKey
// exists to make easy — while still passing today. Measured: the first version
// of this test did exactly that.
//
// The input separates the two comparisons. "Kubernetes" differs from the
// proposed "kubernetes" only in case, so whether it must be reported depends
// entirely on the identity in force. "terraform" is unambiguously carried under
// any identity, so the test never goes vacuous when the first case is excused.
func TestTranscriptNamesEveryKeywordTheFileGainedFromBelow(t *testing.T) {
	v, hostPath := hostLocalEnv(t, "proj", "")
	writeVaultProjectConfig(t, v, "proj",
		"[palace.scoring.rooms.general]\nhigh = [\"Kubernetes\", \"terraform\"]\n")

	below := roomTiersOf(t, mustVaultProjectPath(t, v, "proj"), "general")
	before := roomTiersOf(t, hostPath, "general")

	proposed := []string{"kubernetes"} // differs from below's "Kubernetes" only in case
	_, carried, err := v.WriteHostScoringConfig("proj", map[string]ScoringRoomOverride{
		"general": {High: proposed},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	after := roomTiersOf(t, hostPath, "general")
	transcript := strings.Join(carried, "\n")

	// The oracle. It uses keywordKey — the identity — and never containsKeyword,
	// which is the function under test.
	key := func(kw string) string { return keywordKey(kw) }
	had := map[string]bool{}
	for _, kw := range before.High {
		had[key(kw)] = true
	}
	askedFor := map[string]bool{}
	for _, kw := range proposed {
		askedFor[key(kw)] = true
	}

	var mustReport []string
	gainedAny := false
	for _, kw := range after.High {
		if had[key(kw)] {
			continue
		}
		gainedAny = true
		// Only what came from BELOW and was not asked for is news.
		fromBelow := false
		for _, b := range below.High {
			if key(b) == key(kw) {
				fromBelow = true
				break
			}
		}
		if fromBelow && !askedFor[key(kw)] {
			mustReport = append(mustReport, kw)
		}
	}
	if !gainedAny {
		t.Fatalf("the file gained nothing, so this test no longer probes what it claims; high=%v", after.High)
	}
	if len(mustReport) == 0 {
		t.Fatalf("nothing was carried unasked-for, so this test is vacuous; below=%v proposed=%v after=%v",
			below.High, proposed, after.High)
	}
	for _, kw := range mustReport {
		if !strings.Contains(transcript, kw) {
			t.Errorf("the file gained %q from the layer below, unasked for, and the transcript did not say so.\n"+
				"  high before: %v\n  high after:  %v\n  transcript:  %q",
				kw, before.High, after.High, transcript)
		}
	}
}

func mustVaultProjectPath(t *testing.T, v *Vault, project string) string {
	t.Helper()
	p, err := v.ProjectConfigFile(project)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
