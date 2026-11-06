// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// Guards for [index] transcripts' three-layer resolution.

import (
	"os"
	"path/filepath"
	"testing"
)

// transcriptLayerEnv isolates HOME and the config dir so the host GLOBAL
// config and HostProjectConfigPath both resolve inside the test, and writes
// the host global with the given [index] transcripts value.
func transcriptLayerEnv(t *testing.T, global bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("APPDATA", filepath.Join(home, ".config"))

	path, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[meta]\nversion_major = 1\nversion_minor = 2\nkind = \"global\"\n\n[index]\ntranscripts = " +
		map[bool]string{true: "true", false: "false"}[global] + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// G12 — COMPONENT. TestAnAbsentIndexTranscriptsKeyDoesNotOverrideTheLayerBelow.
//
// hostProjectLayer.Index.Transcripts is a POINTER for the same reason
// Palace.Scoring.MinScore is one: an absent key must be distinguishable from
// an explicit false. A plain bool would let a host-local file that never
// mentions transcripts overwrite the layer below with the zero value.
//
// Q1 PATH: component — it calls LoadConfig directly. The wiring guard that a
// host-local FILE reaches the ingest decision is cmd/vp's
// TestTheHostLocalConfigFileReachesTheIngestDecision.
// Q2 VALUE: 🔴 the host GLOBAL is TRUE. With false, both layers agree on the
// zero value and the *bool -> bool change is invisible.
// Q3 PRESENCE: 🔴 the host-local file must EXIST and parse. An absent file
// makes LoadConfig skip the layer entirely, so applyHostProjectLayer never
// runs and the mutation acts on nothing; the file's existence is asserted.
//
// Mutation: change `Transcripts *bool` to `Transcripts bool` in
// hostProjectLayer (and drop the nil check in applyHostProjectLayer). The
// zero value then overwrites the global's true.
func TestAnAbsentIndexTranscriptsKeyDoesNotOverrideTheLayerBelow(t *testing.T) {
	const project = "p"
	transcriptLayerEnv(t, true)

	// A host-local file that EXISTS and sets something else, but never
	// mentions transcripts.
	path, err := HostProjectConfigPath(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[meta]\nversion_major = 1\nversion_minor = 0\nkind = \"project\"\n\n" +
		"[palace.scoring]\nmin_score = 0.5\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("PRESENCE: the host-local project config was not written: %v", err)
	}

	cfg, err := NewVault(t.TempDir()).LoadConfig(project)
	if err != nil {
		t.Fatal(err)
	}
	// The file WAS applied — min_score proves the layer ran at all, so a
	// resolved `true` below cannot be "the layer was skipped".
	if cfg.PalaceMinScore != 0.5 {
		t.Fatalf("PRESENCE: the host-local layer did not apply (min_score = %v, want 0.5), so this "+
			"guard would pass over a layer that never ran", cfg.PalaceMinScore)
	}
	if !cfg.IndexTranscripts {
		t.Fatal("a host-local file that never mentions [index] transcripts overwrote the host " +
			"global's true with the zero value. The field must be a POINTER so an absent key is " +
			"distinguishable from an explicit false — the same idiom palace.scoring.min_score uses")
	}
}

// TestAnExplicitHostLocalFalseStillOverrides is G12's other half: an explicit
// false MUST win over a global true, which is what makes the pointer a
// tri-state rather than a way to ignore the key.
func TestAnExplicitHostLocalFalseStillOverrides(t *testing.T) {
	const project = "p"
	transcriptLayerEnv(t, true)

	path, err := HostProjectConfigPath(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[index]\ntranscripts = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewVault(t.TempDir()).LoadConfig(project)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IndexTranscripts {
		t.Fatal("an explicit host-local `transcripts = false` did not override the host global's " +
			"true: the pointer is being read as 'absent' for an explicit zero value")
	}
}

// TestTheEmbeddedDefaultExcludesTranscripts pins the DEFAULT itself. It is the
// ruling, and it is the value every layering guard above is written against.
//
// Mutation: change `transcripts = false` to `true` in
// internal/storage/config/defaults.toml.
func TestTheEmbeddedDefaultExcludesTranscripts(t *testing.T) {
	transcriptLayerEnv(t, false)
	// No host global key at all: delete the file so only the embedded
	// defaults speak.
	path, err := VaultConfigFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewVault(t.TempDir()).LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IndexTranscripts {
		t.Fatal("the EMBEDDED DEFAULT indexes transcripts. That default is the ruling: archives " +
			"are captured exactly as before and simply not ingested, which is what keeps a rebuild " +
			"to hours rather than days")
	}
}

// TestResolveTranscriptsIsTheOnePrecedenceRule guards the rule where it now
// lives. It moved here from internal/ingest when the search engine became its
// SECOND caller: ingest resolves it for the admission decision, the engine for
// the walk's exclusion belt, the completeness record and the coverage state.
// A copy in either package would be a second place to forget it.
//
// Q1 PATH: component — it drives the rule with the four input rows. What
// proves a flag actually REACHES each caller is cmd/vp's
// TestTheTranscriptsFlagBeatsTheConfigFromArgv (ingest) and
// TestTheTranscriptsFlagReachesTheSearchEngine (the engine); neither of those
// may be replaced by this one.
// Q2 VALUE: each row's expected answer differs from the other input's value,
// so inverting the precedence changes two of the four.
// Q3 PRESENCE: the four rows ARE the input; there is no fixture to be absent.
//
// Mutation: invert the precedence (`if opt != nil` -> `if opt == nil`). Rows 2
// and 3 red.
func TestResolveTranscriptsIsTheOnePrecedenceRule(t *testing.T) {
	yes, no := true, false
	for _, row := range []struct {
		name string
		opt  *bool
		cfg  bool
		want bool
	}{
		{"no flag, config off", nil, false, false},
		{"no flag, config on", nil, true, true},
		{"--transcripts beats a config of off", &yes, false, true},
		{"--no-transcripts beats a config of on", &no, true, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			got := ResolveTranscripts(row.opt, Config{IndexTranscripts: row.cfg})
			if got != row.want {
				t.Fatalf("ResolveTranscripts(%v, cfg{IndexTranscripts:%v}) = %v, want %v",
					row.opt, row.cfg, got, row.want)
			}
		})
	}
}
