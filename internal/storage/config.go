// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/suykerbuyk/vibe-palace/internal/atomicfile"
	"github.com/suykerbuyk/vibe-palace/internal/slug"
	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// CurrentVersionMajor and CurrentVersionMinor are the schema version that
// this binary understands. A config file whose version_major exceeds
// CurrentVersionMajor is rejected. Minor bumps are additive-only and
// forward-compatible. See the [meta] block in defaults.toml.
const (
	CurrentVersionMajor = 1
	CurrentVersionMinor = 1
)

// MetaKind values identify which config-file schema a [meta] block belongs to.
const (
	MetaKindGlobal     = "global"
	MetaKindCwdProject = "cwd-project"
	// MetaKindHostProject is the host-local per-project file written by
	// WriteHostScoringConfig: outside every vault and never shared between
	// machines.
	MetaKindHostProject = "host-project"
)

// hostProjectKeyWarnOnce suppresses the "not a per-project key" warning to one
// emission per process per (file, key), so a long-lived `vp mcp` does not
// repeat itself on every LoadConfig.
var hostProjectKeyWarnOnce sync.Map // map[string]*sync.Once, keyed path+"\x00"+key

// missingMetaWarnOnce suppresses the "config has no [meta] block" warning
// to one emission per process, keyed by file path.
var missingMetaWarnOnce sync.Map // map[string]*sync.Once

//go:embed config/defaults.toml
var defaultsToml string

//go:embed config/template.toml
var templateToml string

// TemplateTomlContent returns the embedded template.toml content.
func TemplateTomlContent() string { return templateToml }

// DefaultsTomlContent returns the embedded defaults.toml content. Used
// by config-upgrade to derive canonical schema keys for the global
// config.
func DefaultsTomlContent() (string, error) { return defaultsToml, nil }

// Config holds resolved configuration values.
type Config struct {
	MetaVersionMajor       int                            `json:"meta_version_major"`
	MetaVersionMinor       int                            `json:"meta_version_minor"`
	MetaKind               string                         `json:"meta_kind"`
	VaultPath              string                         `json:"vault_path"`
	HTTPPort               int                            `json:"http_port"`
	LogLevel               string                         `json:"log_level"`
	EmbedderModel          string                         `json:"embedder_model"`
	EmbedderMaxSeqLen      int                            `json:"embedder_max_seq_len"`
	EmbedderBatchSize      int                            `json:"embedder_batch_size"`
	SearchDefaultLimit     int                            `json:"search_default_limit"`
	BoostWing              float64                        `json:"boost_wing"`
	BoostHall              float64                        `json:"boost_hall"`
	BoostRoom              float64                        `json:"boost_room"`
	ChunkMaxChars          int                            `json:"chunk_max_chars"`
	ChunkOverlap           int                            `json:"chunk_overlap"`
	PalaceRoomKeywords     map[string][]string            `json:"palace_room_keywords,omitempty"`
	PalaceScoringOverrides map[string]ScoringRoomOverride `json:"palace_scoring_overrides,omitempty"`
	PalaceMinScore         float64                        `json:"palace_min_score,omitempty"`
	PalaceLLM              LLMConfig                      `json:"palace_llm"`
	Archive                ArchiveConfig                  `json:"archive"`
	Enrichment             EnrichmentConfig               `json:"enrichment"`
	Summarization          SummarizationConfig            `json:"summarization"`
}

// EnrichmentConfig holds resolved settings for the session-enrichment LLM
// pass. APIKeyEnv is the name of the environment variable holding the API
// key, not the key itself. All fields are optional; Enabled is off by
// default so an absent [enrichment] block decodes to the zero value.
type EnrichmentConfig struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	APIKeyEnv      string `json:"api_key_env"`
	BaseURL        string `json:"base_url"`
	MaxTokens      int    `json:"max_tokens"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// SummarizationConfig holds resolved settings for the bulk LLM-summarization
// pass (search-oriented iteration/session-note summaries) — a distinct
// concern from EnrichmentConfig's human-facing session enrichment, so it has
// its own on/off toggle and its own model choice (a cheaper/faster model is
// plausibly sufficient here). Shared by every SummaryJobKind's Summarizer
// implementation, not per-kind. APIKeyEnv is the name of the environment
// variable holding the API key, not the key itself. All fields are optional;
// Enabled is off by default so an absent [summarization] block decodes to
// the zero value.
type SummarizationConfig struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	APIKeyEnv      string `json:"api_key_env"`
	BaseURL        string `json:"base_url"`
	MaxTokens      int    `json:"max_tokens"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// ArchiveConfig holds transcript-archive settings (ADR-001 Phase 6).
// All fields are optional; signing is off by default.
type ArchiveConfig struct {
	SignMode       string `json:"sign_mode,omitempty"`       // "", "ssh", "gpg"
	SignKey        string `json:"sign_key,omitempty"`        // ssh key path or gpg key id
	SignNamespace  string `json:"sign_namespace,omitempty"`  // ssh-sig namespace
	AllowedSigners string `json:"allowed_signers,omitempty"` // path for ssh verify
	SignerIdentity string `json:"signer_identity,omitempty"` // principal for ssh verify
}

// LLMConfig holds TOML-level LLM endpoint settings.
// APIKeyEnv is the name of the environment variable holding the API key,
// not the key itself. Compare with llm.Config which holds the resolved key.
type LLMConfig struct {
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model"`
	APIKeyEnv string `json:"api_key_env"`
	MaxTokens int    `json:"max_tokens"`
}

// ScoringRoomOverride holds weighted keyword overrides for a single room.
type ScoringRoomOverride struct {
	High   []string `json:"high,omitempty"`
	Medium []string `json:"medium,omitempty"`
	Low    []string `json:"low,omitempty"`
}

// tomlMeta captures the [meta] section that every managed config file carries.
type tomlMeta struct {
	VersionMajor int    `toml:"version_major"`
	VersionMinor int    `toml:"version_minor"`
	Kind         string `toml:"kind"`
}

// tomlConfig is the intermediate struct matching the TOML file structure.
type tomlConfig struct {
	Meta      tomlMeta `toml:"meta"`
	VaultPath string   `toml:"vault_path"`
	HTTPPort  int      `toml:"http_port"`
	LogLevel  string   `toml:"log_level"`
	Embedder  struct {
		Model             string `toml:"model"`
		MaxSequenceLength int    `toml:"max_sequence_length"`
		BatchSize         int    `toml:"batch_size"`
	} `toml:"embedder"`
	Search struct {
		DefaultLimit        int     `toml:"default_limit"`
		StructuralBoostWing float64 `toml:"structural_boost_wing"`
		StructuralBoostHall float64 `toml:"structural_boost_hall"`
		StructuralBoostRoom float64 `toml:"structural_boost_room"`
	} `toml:"search"`
	Chunker struct {
		MaxChars int `toml:"max_chars"`
		Overlap  int `toml:"overlap"`
	} `toml:"chunker"`
	Palace struct {
		Rooms   map[string]tomlRoomConfig `toml:"rooms"`
		Scoring tomlScoringConfig         `toml:"scoring"`
		LLM     tomlLLMConfig             `toml:"llm"`
	} `toml:"palace"`
	Archive       tomlArchiveConfig       `toml:"archive"`
	Enrichment    tomlEnrichmentConfig    `toml:"enrichment"`
	Summarization tomlSummarizationConfig `toml:"summarization"`
}

type tomlEnrichmentConfig struct {
	Enabled        bool   `toml:"enabled"`
	Provider       string `toml:"provider"`
	Model          string `toml:"model"`
	APIKeyEnv      string `toml:"api_key_env"`
	BaseURL        string `toml:"base_url"`
	MaxTokens      int    `toml:"max_tokens"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

type tomlSummarizationConfig struct {
	Enabled        bool   `toml:"enabled"`
	Provider       string `toml:"provider"`
	Model          string `toml:"model"`
	APIKeyEnv      string `toml:"api_key_env"`
	BaseURL        string `toml:"base_url"`
	MaxTokens      int    `toml:"max_tokens"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

type tomlArchiveConfig struct {
	SignMode       string `toml:"sign_mode"`
	SignKey        string `toml:"sign_key"`
	SignNamespace  string `toml:"sign_namespace"`
	AllowedSigners string `toml:"allowed_signers"`
	SignerIdentity string `toml:"signer_identity"`
}

type tomlRoomConfig struct {
	Keywords []string `toml:"keywords"`
}

type tomlScoringConfig struct {
	MinScore float64                    `toml:"min_score"`
	Rooms    map[string]tomlRoomScoring `toml:"rooms"`
}

// hostProjectLayer is what the HOST-LOCAL per-project file may set, and it is
// deliberately NOT tomlConfig.
//
// 🔴 THE TYPE IS THE ALLOW-LIST. A per-project file must not be able to move
// vault_path, http_port, log_level, the embedder or anything else that is
// vault-wide or machine-wide; decoding into tomlConfig would let every one of
// them through silently. Everything this struct does not name lands in
// MetaData.Undecoded() and is reported once per key as ignored, which is a
// refusal the operator can see rather than a value that quietly wins.
//
// Meta is decoded and NOT acted on: the file carries a [meta] block so a later
// release has a schema version to gate on, and decoding it here is what keeps
// the version keys out of the ignored-key warning. checkConfigVersion is not
// run on this layer (see LoadConfig's host-local layer).
type hostProjectLayer struct {
	Meta   tomlMeta `toml:"meta"`
	Palace struct {
		Scoring struct {
			// MinScore is a POINTER so an absent key is distinguishable from an
			// explicit 0. A plain float64 would let a file that never mentions
			// min_score overwrite a lower layer's value with zero.
			MinScore *float64                   `toml:"min_score"`
			Rooms    map[string]tomlRoomScoring `toml:"rooms"`
		} `toml:"scoring"`
	} `toml:"palace"`
}

type tomlRoomScoring struct {
	High   []string `toml:"high"`
	Medium []string `toml:"medium"`
	Low    []string `toml:"low"`
}

type tomlLLMConfig struct {
	Endpoint  string `toml:"endpoint"`
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"`
	MaxTokens int    `toml:"max_tokens"`
}

// flatten converts a tomlConfig into a Config.
func (tc *tomlConfig) flatten() Config {
	return Config{
		MetaVersionMajor:       tc.Meta.VersionMajor,
		MetaVersionMinor:       tc.Meta.VersionMinor,
		MetaKind:               tc.Meta.Kind,
		VaultPath:              tc.VaultPath,
		HTTPPort:               tc.HTTPPort,
		LogLevel:               tc.LogLevel,
		EmbedderModel:          tc.Embedder.Model,
		EmbedderMaxSeqLen:      tc.Embedder.MaxSequenceLength,
		EmbedderBatchSize:      tc.Embedder.BatchSize,
		SearchDefaultLimit:     tc.Search.DefaultLimit,
		BoostWing:              tc.Search.StructuralBoostWing,
		BoostHall:              tc.Search.StructuralBoostHall,
		BoostRoom:              tc.Search.StructuralBoostRoom,
		ChunkMaxChars:          tc.Chunker.MaxChars,
		ChunkOverlap:           tc.Chunker.Overlap,
		PalaceRoomKeywords:     flattenPalaceRooms(tc.Palace.Rooms),
		PalaceScoringOverrides: flattenScoringRooms(tc.Palace.Scoring.Rooms),
		PalaceMinScore:         tc.Palace.Scoring.MinScore,
		PalaceLLM: LLMConfig{
			Endpoint:  tc.Palace.LLM.Endpoint,
			Model:     tc.Palace.LLM.Model,
			APIKeyEnv: tc.Palace.LLM.APIKeyEnv,
			MaxTokens: tc.Palace.LLM.MaxTokens,
		},
		Archive: ArchiveConfig{
			SignMode:       tc.Archive.SignMode,
			SignKey:        tc.Archive.SignKey,
			SignNamespace:  tc.Archive.SignNamespace,
			AllowedSigners: tc.Archive.AllowedSigners,
			SignerIdentity: tc.Archive.SignerIdentity,
		},
		Enrichment: EnrichmentConfig{
			Enabled:        tc.Enrichment.Enabled,
			Provider:       tc.Enrichment.Provider,
			Model:          tc.Enrichment.Model,
			APIKeyEnv:      tc.Enrichment.APIKeyEnv,
			BaseURL:        tc.Enrichment.BaseURL,
			MaxTokens:      tc.Enrichment.MaxTokens,
			TimeoutSeconds: tc.Enrichment.TimeoutSeconds,
		},
		Summarization: SummarizationConfig{
			Enabled:        tc.Summarization.Enabled,
			Provider:       tc.Summarization.Provider,
			Model:          tc.Summarization.Model,
			APIKeyEnv:      tc.Summarization.APIKeyEnv,
			BaseURL:        tc.Summarization.BaseURL,
			MaxTokens:      tc.Summarization.MaxTokens,
			TimeoutSeconds: tc.Summarization.TimeoutSeconds,
		},
	}
}

// flattenPalaceRooms converts the TOML palace rooms config to a plain map.
// Returns nil if no rooms are configured.
func flattenPalaceRooms(rooms map[string]tomlRoomConfig) map[string][]string {
	if len(rooms) == 0 {
		return nil
	}
	m := make(map[string][]string, len(rooms))
	for room, cfg := range rooms {
		m[room] = cfg.Keywords
	}
	return m
}

// flattenScoringRooms converts TOML scoring room overrides to the Config format.
func flattenScoringRooms(rooms map[string]tomlRoomScoring) map[string]ScoringRoomOverride {
	if len(rooms) == 0 {
		return nil
	}
	m := make(map[string]ScoringRoomOverride, len(rooms))
	for room, cfg := range rooms {
		m[room] = ScoringRoomOverride{
			High:   cfg.High,
			Medium: cfg.Medium,
			Low:    cfg.Low,
		}
	}
	return m
}

// LoadConfig loads configuration with precedence: embedded defaults < host
// global config < host-local per-project file. Missing config files at any
// level are silently skipped.
//
// The per-project config that used to sit between the last two, the vault's
// Projects/<slug>/config.toml, is no longer read (task
// move-per-project-config-out-of-the-shared-vault). The one per-project tier
// that remains is host-local, and it carries palace.scoring only.
//
// After the host global config is decoded, its [meta] block is validated:
//   - version_major > CurrentVersionMajor is a hard error (binary is too old).
//   - version_major == 0 (missing [meta]) triggers one slog.Warn per path,
//     recommending `vp config upgrade`.
//
// Meta is zeroed before the host global decode so its version check sees that
// file's values, not the embedded defaults.
func (v *Vault) LoadConfig(project string) (Config, error) {
	var tc tomlConfig

	// Layer 1: embedded defaults (never missing, never version-mismatched
	// by construction — the build pins its own defaults).
	if _, err := toml.Decode(defaultsToml, &tc); err != nil {
		return Config{}, fmt.Errorf("decode embedded defaults: %w", err)
	}

	// Layer 2: the host global config (~/.config/vibe-palace/config.toml).
	// Meta is file-local, not inherited: zero it before each on-disk decode
	// so Config.Meta* reflects the actual on-disk file (or zero if absent).
	//
	// 🔴 THE BARE os.Stat GATE HERE IS TWO DIFFERENT THINGS AT ONCE, AND
	// NEITHER MAY BE COPIED INTO ANYTHING THAT WRITES.
	//
	// Its ENOENT half is deliberate: an absent config file is an answer, and
	// skipping the layer is the right one.
	//
	// Everything else it swallows is a KNOWN DEFECT, not a design choice, and
	// there are two of them. This gate reads `err == nil`, so EACCES and EIO —
	// a file that IS there and cannot be read — become "skip this layer". And
	// os.Stat's own ENOENT is not proof of absence either: it FOLLOWS symlinks,
	// so a dangling link reports as no file at all. Only an Lstat ENOENT means
	// "no file". Nobody chose either of those. configFilePresent below names
	// this exact shape as the hazard and is the fix; it is not applied here
	// only because doing so is a whole-binary behaviour change, for the reason
	// at the end of this comment.
	//
	// What a reader survives, a writer does not. A skipped layer costs a read
	// one wrong answer with every file still on disk. A writer cannot survive
	// the same shape, because what it silently drops it then overwrites. Use
	// configFilePresent (Lstat, refuses on anything but ENOENT) there;
	// scoringRoomsBelowHost's doc comment has the long form.
	//
	// This warning is here, next to the gate, because the shape has already
	// propagated once: the host-local scoring resolution was written beside
	// this code and inherited the bare os.Stat, which produced a partial room
	// on a write and cost a review round. The next person adding a layer will
	// be reading THIS function, not the one that got it wrong.
	//
	// Note the other two readers of this same file already refuse: VaultRoot
	// (vault.go) decodes with no stat gate at all, and HostGitEnabled goes
	// through configFilePresent. LoadConfig is the odd one out, and making it
	// uniform is a whole-binary behaviour change that wants its own unit --
	// every caller would start hard-erroring where it now gets defaults.
	vaultConfigPath, err := VaultConfigFilePath()
	if err == nil {
		if _, err := os.Stat(vaultConfigPath); err == nil {
			tc.Meta = tomlMeta{}
			if _, err := toml.DecodeFile(vaultConfigPath, &tc); err != nil {
				return Config{}, fmt.Errorf("decode vault config %s: %w", vaultConfigPath, err)
			}
			if err := checkConfigVersion(vaultConfigPath, tc.Meta); err != nil {
				return Config{}, err
			}
		}
	}

	// Top layer: the HOST-LOCAL per-project file.
	//
	// 🔴 IT IS HOST-LOCAL, WHICH IS THE WHOLE POINT. The vault is shared across
	// machines, so a tuning run on one host must not become every host's
	// classifier (task move-per-project-config-out-of-the-shared-vault). The
	// vault's per-project file that once sat below this one is no longer read;
	// its overrides are reported by the one-shot migration that deletes it.
	//
	// checkConfigVersion is NOT run here. The file carries a [meta] block so a
	// later release has a version to gate on, but nothing gates on it yet: a
	// gate added now would be a refusal with no schema change behind it.
	if project != "" {
		hostPath, err := HostProjectConfigPath(project)
		if err != nil {
			return Config{}, err
		}
		switch present, serr := hostProjectConfigPresent(hostPath); {
		case serr != nil:
			return Config{}, serr
		case present:
			var hl hostProjectLayer
			md, derr := toml.DecodeFile(hostPath, &hl)
			if derr != nil {
				return Config{}, fmt.Errorf("decode host-local project config %s: %w", hostPath, derr)
			}
			warnIgnoredHostProjectKeys(hostPath, md.Undecoded())
			applyHostProjectLayer(&tc, hl)
		}
	}

	return tc.flatten(), nil
}

// applyHostProjectLayer merges the host-local layer over everything below it.
//
// Rooms merge by WHOLE-ROOM REPLACEMENT: a room the host-local file names
// replaces that room's three tiers entirely, so a host-local room carrying only
// `high` resolves with Medium and Low empty. That is how a decode over the
// embedded defaults already composes — BurntSushi replaces the map entry for a
// key it decodes — and it is what makes a host-local room a statement about the
// whole room rather than a patch whose result depends on what a lower layer
// happened to say.
func applyHostProjectLayer(tc *tomlConfig, hl hostProjectLayer) {
	if v := hl.Palace.Scoring.MinScore; v != nil {
		tc.Palace.Scoring.MinScore = *v
	}
	if len(hl.Palace.Scoring.Rooms) == 0 {
		return
	}
	if tc.Palace.Scoring.Rooms == nil {
		tc.Palace.Scoring.Rooms = make(map[string]tomlRoomScoring, len(hl.Palace.Scoring.Rooms))
	}
	for room, rs := range hl.Palace.Scoring.Rooms {
		tc.Palace.Scoring.Rooms[room] = rs
	}
}

// warnIgnoredHostProjectKeys reports every key the host-local file set that the
// allow-list does not carry. It warns rather than failing: the key is already
// ignored, and a hard error would turn a stale hand-edit into a broken vp.
func warnIgnoredHostProjectKeys(path string, undecoded []toml.Key) {
	for _, k := range undecoded {
		key := k.String()
		oncePtr, _ := hostProjectKeyWarnOnce.LoadOrStore(path+"\x00"+key, &sync.Once{})
		oncePtr.(*sync.Once).Do(func() {
			slog.Warn("not a per-project key; ignored", "key", key, "path", path)
		})
	}
}

// checkConfigVersion rejects configs from a future major version and warns
// once per path when [meta] is missing (version_major == 0).
func checkConfigVersion(path string, m tomlMeta) error {
	if m.VersionMajor > CurrentVersionMajor {
		return fmt.Errorf(
			"config at %s is version %d.%d, this vp supports up to %d.%d — upgrade vp or downgrade the config",
			path, m.VersionMajor, m.VersionMinor, CurrentVersionMajor, CurrentVersionMinor,
		)
	}
	if m.VersionMajor == 0 {
		oncePtr, _ := missingMetaWarnOnce.LoadOrStore(path, &sync.Once{})
		oncePtr.(*sync.Once).Do(func() {
			slog.Warn("config has no [meta] block; run 'vp config upgrade' to add schema version markers", "path", path)
		})
	}
	return nil
}

// splitDot splits a string on '.' characters.
func splitDot(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

// VaultConfigFilePath returns the path to the vault-level config file.
func VaultConfigFilePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	// filepath.Join, NOT concatenation: os.UserConfigDir returns a
	// backslash-separated path on Windows (%AppData%), so gluing a "/..."
	// literal onto it yields the mixed-separator
	// `C:\Users\x\AppData\Roaming/vibe-palace/config.toml` that showed up in the
	// windows-lock CI failure of 2026-07-26. Join normalizes per-OS and is
	// byte-identical to the old result on Unix.
	return filepath.Join(configDir, "vibe-palace", "config.toml"), nil
}

// ErrGitDisabled is the refusal every vault git entry point returns when the
// host config says git_enabled = false. The text is the one the CLI printed
// before the refusal moved into storage. Map it with errors.Is; never
// construct or wrap it outside this package (the gitEnabledOwner source-audit
// rule enforces that).
var ErrGitDisabled = errors.New("git is disabled (git_enabled = false in config)")

// ErrGitConfigUnreadable is returned when git_enabled cannot be read, so it is
// unknown whether the operator disabled git. It never wraps ErrGitDisabled: an
// unreadable config is a broken file, not an operator decision, and it is never
// presented as "disabled". Vault git refuses on it all the same (fail closed).
var ErrGitConfigUnreadable = errors.New("host config unreadable")

// HostGitEnabled reports whether the HOST config (VaultConfigFilePath, not a
// vault file: two hosts sharing one vault can differ) permits vp to run git
// against the vault. It is the one reader of git_enabled. Inside this package
// only RefuseIfGitDisabled calls it; outside, only the reporting readers the
// gitEnabledOwner rule allow-lists do.
//
// It is deliberately NOT LoadConfig: LoadConfig fails open (a config it cannot
// stat reads as the embedded git_enabled = true), returns a Config whose
// GitEnabled is false on error, and lets an unrelated wrong-typed key block
// everything. The semantics, exactly:
//
//   - VaultConfigFilePath errors (no HOME, no XDG_CONFIG_HOME) → unreadable.
//   - os.Lstat, never os.Stat, decides "missing": only an Lstat ENOENT means
//     no config, which means enabled. A dangling symlink is unreadable.
//   - The top level is decoded into a map, never a struct, because BurntSushi
//     matches struct fields case-insensitively: a lone GIT_ENABLED would read as
//     the setting, and GIT_ENABLED beside git_enabled decodes to either value
//     depending on map iteration order. Any key other than the exact spelling
//     that equals git_enabled under strings.EqualFold is therefore unreadable.
//   - A decode error anywhere in the file, or a config from a newer vp
//     (checkConfigVersion, the same check LoadConfig applies), is unreadable and
//     is returned before any value is inspected.
//   - The exact key holding a bool → that value; holding anything else →
//     unreadable; absent (including nested under a table) → enabled.
func HostGitEnabled() (bool, error) {
	cfgPath, err := VaultConfigFilePath()
	if err != nil {
		return false, fmt.Errorf("cannot read git_enabled from the host config (path unresolved): %w: %w",
			ErrGitConfigUnreadable, err)
	}
	unreadable := func(cause error) (bool, error) {
		return false, fmt.Errorf("cannot read git_enabled from %s: %w: %w", cfgPath, ErrGitConfigUnreadable, cause)
	}
	if _, err := os.Lstat(cfgPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		return unreadable(err)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return unreadable(err)
	}
	var top map[string]any
	if _, err := toml.Decode(string(data), &top); err != nil {
		return unreadable(err)
	}
	var meta struct {
		Meta tomlMeta `toml:"meta"`
	}
	if _, err := toml.Decode(string(data), &meta); err != nil {
		return unreadable(err)
	}
	if err := checkConfigVersion(cfgPath, meta.Meta); err != nil {
		return unreadable(err)
	}
	for key := range top {
		if key != "git_enabled" && strings.EqualFold(key, "git_enabled") {
			return unreadable(fmt.Errorf("key %q is not git_enabled but differs from it only in case", key))
		}
	}
	value, ok := top["git_enabled"]
	if !ok {
		return true, nil
	}
	enabled, ok := value.(bool)
	if !ok {
		return unreadable(fmt.Errorf("git_enabled = %v is a %T, not a bool", value, value))
	}
	return enabled, nil
}

// configFilePresent is the ONE presence test for a config file this package
// decides to read or skip, so no caller grows its own and drifts. what names
// the file in the error, e.g. "host-local project config".
//
// 🔴 os.Lstat, NEVER os.Stat, AND AN UNREADABLE FILE IS NOT AN ABSENT ONE.
// Only an Lstat ENOENT means "no file". A dangling symlink, a parent directory
// with no search permission, an I/O error — each is a file that is there and
// cannot be read, and each must surface as an error rather than as absence.
// A bare `os.Stat(p) == nil` test silently converts all three into "skip this
// layer", which is how a reader prints "none" for a config that in fact breaks
// every command, and how a WRITER produces a partial room (see
// scoringRoomsBelowHost). It is the shape HostGitEnabled already refuses for
// git_enabled, for the same reason.
//
// It reports presence, not readability: the decode that follows is what says
// whether the bytes are good.
func configFilePresent(what, path string) (bool, error) {
	switch _, err := os.Lstat(path); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("stat %s %s: %w", what, path, err)
	}
}

// hostProjectConfigPresent is configFilePresent for the host-local per-project
// file, shared by LoadConfig's host-local layer and by ProjectConfigSources so
// the reader and the reporter cannot drift apart.
func hostProjectConfigPresent(path string) (bool, error) {
	return configFilePresent("host-local project config", path)
}

// ProjectConfigSources lists the per-project config files that EXIST for a
// project. Since the vault's per-project file stopped being read, there is at
// most one: the host-local file.
//
// It exists so a caller can report which files a LoadConfig actually read
// without doing any path arithmetic of its own — the "resolve, don't recall"
// rule applied to config sources. An empty result means there is no host-local
// file and the project inherits the host global config alone. It uses the
// same presence predicate as the layer that reads the file, so it reports
// exactly what LoadConfig decodes: a dangling symlink is present-and-broken.
func (v *Vault) ProjectConfigSources(project string) ([]string, error) {
	hostPath, err := HostProjectConfigPath(project)
	if err != nil {
		return nil, err
	}
	present, err := hostProjectConfigPresent(hostPath)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	return []string{hostPath}, nil
}

// scoringRoomsOnly is the narrow view used to attribute a scoring room to the
// file that set it. It decodes nothing else.
type scoringRoomsOnly struct {
	Palace struct {
		Scoring struct {
			Rooms map[string]tomlRoomScoring `toml:"rooms"`
		} `toml:"scoring"`
	} `toml:"palace"`
}

// scoringRoomsBelowHost resolves the scoring rooms of every layer BELOW the
// host-local file, and reports which file set each room.
//
// It walks the same two sources as LoadConfig's layers below the host-local
// file — the embedded defaults and the host global config — in the same order,
// applying the same whole-room replacement. It is a separate walk because
// attribution costs a second decode per layer and LoadConfig is hot;
// TestScoringRoomsBelowHostMatchesLoadConfig pins the two together and reds if
// they drift.
//
// 🔴 IT DIVERGES FROM LoadConfig IN ONE WAY, DELIBERATELY: AN UNREADABLE LAYER
// FAILS. LoadConfig tests the host global config with a bare `os.Stat(p) ==
// nil` and so fails OPEN — an unreadable file is skipped and the caller gets an
// answer missing that layer. That is survivable for a read: one wrong answer,
// and the file is still on disk. It is not survivable here. What this
// resolution feeds is a WRITE of the host-local file, which replaces the room
// WHOLE, so a silently skipped layer means the room is written without the
// tiers that layer held — the exact data loss the widening exists to prevent,
// reached through an unreadable file instead of an absent tier. Losing a layer
// on a read is recoverable; losing it on this write is not. So every layer here
// goes through configFilePresent, and anything but ENOENT refuses the write.
//
// The parity the test pins is the RESOLUTION on layers that can be read, not
// the disposition when one cannot.
//
// It walked a third source, the vault's Projects/<slug>/config.toml, until
// that file stopped being read; the carry from the host global config is why
// this and completeRoomsFromBelow outlived it.
func scoringRoomsBelowHost() (map[string]tomlRoomScoring, map[string]string, error) {
	rooms := map[string]tomlRoomScoring{}
	source := map[string]string{}

	apply := func(path string, rs map[string]tomlRoomScoring) {
		for name, r := range rs {
			rooms[name] = r
			source[name] = path
		}
	}

	// Layer 1: embedded defaults. It ships every scoring room commented out, so
	// this contributes nothing today — but it is walked rather than assumed,
	// because the day someone uncomments one is the day an assumption rots.
	var l1 scoringRoomsOnly
	if _, err := toml.Decode(defaultsToml, &l1); err != nil {
		return nil, nil, fmt.Errorf("decode embedded defaults: %w", err)
	}
	apply("the embedded defaults", l1.Palace.Scoring.Rooms)

	// Layer 2: the host global config. Its path error is RETURNED, not
	// swallowed as LoadConfig swallows it: a writer that cannot resolve the
	// layer cannot know what it is about to drop. (Unreachable through the one
	// caller today — WriteHostScoringConfig resolves the same path first, via
	// HostProjectConfigPath — but the swallow is the defect, not its current
	// reachability.)
	globalPath, err := VaultConfigFilePath()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve host config path: %w", err)
	}
	present, err := configFilePresent("host config", globalPath)
	if err != nil {
		return nil, nil, err
	}
	if present {
		var l2 scoringRoomsOnly
		if _, derr := toml.DecodeFile(globalPath, &l2); derr != nil {
			return nil, nil, fmt.Errorf("decode vault config %s: %w", globalPath, derr)
		}
		apply(globalPath, l2.Palace.Scoring.Rooms)
	}

	return rooms, source, nil
}

// hostRoomsAlreadyWritten returns the scoring rooms the host-local file already
// carries, so the transcript reports only what THIS write carries in for the
// first time rather than re-announcing a copy made on an earlier run.
//
// It is advisory. The authoritative merge happens under the lock inside
// writeScoringConfigAt, whose mergeTierInto dedups; a file that changed between
// this read and that merge costs at most a stale transcript line, never a wrong
// write.
func hostRoomsAlreadyWritten(hostPath string) (map[string]tomlRoomScoring, error) {
	present, err := hostProjectConfigPresent(hostPath)
	if err != nil || !present {
		return nil, err
	}
	var hl hostProjectLayer
	if _, derr := toml.DecodeFile(hostPath, &hl); derr != nil {
		return nil, fmt.Errorf("decode host-local project config %s: %w", hostPath, derr)
	}
	return hl.Palace.Scoring.Rooms, nil
}

// completeRoomsFromBelow widens each proposed room so the host-local file
// carries the WHOLE room, and returns a transcript of what it carried in.
//
// 🔴 THIS IS WHAT KEEPS A TUNING RUN FROM ERASING A LOWER LAYER'S TIERS.
// Every layer in this stack replaces a scoring room WHOLE — the host-local
// file nils out a tier the host global config set (measured; it is the idiom,
// not an accident). A proposal names only the tiers it changed, so writing it
// verbatim into the host-local file would publish a partial room that then
// erases every tier the host global config held for that room. Completing the
// room from the layers below restores the invariant the stack depends on: no
// layer ever holds a partial room for a room a lower layer also has.
//
// The copy is real data movement between files, so it is not silent: each room
// whose values are carried in for the first time gets a transcript line naming
// the tiers, the keywords and the file they came from.
//
// It outlived the vault's per-project layer, whose rooms it also carried: the
// host global config below the host-local file replaces rooms whole too.
func completeRoomsFromBelow(
	proposed map[string]ScoringRoomOverride,
	below map[string]tomlRoomScoring,
	source map[string]string,
	alreadyHost map[string]tomlRoomScoring,
) (map[string]ScoringRoomOverride, []string) {
	out := make(map[string]ScoringRoomOverride, len(proposed))
	names := make([]string, 0, len(proposed))
	for name := range proposed {
		names = append(names, name)
	}
	sort.Strings(names)

	var transcript []string
	for _, name := range names {
		prop := proposed[name]
		b, ok := below[name]
		if !ok {
			out[name] = prop
			continue
		}
		have := alreadyHost[name]

		var carried []string
		tiers := []struct {
			label   string
			fromBel []string
			inProp  []string
			inHost  []string
			into    *[]string
		}{
			{"high", b.High, prop.High, have.High, &prop.High},
			{"medium", b.Medium, prop.Medium, have.Medium, &prop.Medium},
			{"low", b.Low, prop.Low, have.Low, &prop.Low},
		}
		for _, t := range tiers {
			if len(t.fromBel) == 0 {
				continue
			}
			// Report only what is not already accounted for, so a re-run that
			// changes nothing prints nothing.
			var newly []string
			for _, kw := range t.fromBel {
				if !containsKeyword(t.inProp, kw) && !containsKeyword(t.inHost, kw) {
					newly = append(newly, kw)
				}
			}
			if len(newly) > 0 {
				carried = append(carried, fmt.Sprintf("%s=%v", t.label, newly))
			}
			// The below-layer keywords go FIRST so the tier reads oldest-first
			// and a re-run is byte-stable; mergeTierInto dedups on the way in.
			*t.into = append(append([]string{}, t.fromBel...), *t.into...)
		}
		out[name] = prop
		if len(carried) > 0 {
			transcript = append(transcript,
				fmt.Sprintf("%s: carried %s from %s", name, strings.Join(carried, " "), source[name]))
		}
	}
	return out, transcript
}

// keywordKey is THE definition of when two keywords are the same keyword.
//
// 🔴 ONE DEFINITION, NOT TWO THAT AGREE TODAY. mergeKeywordTier dedups by this
// key and containsKeyword asks by this key, so the merge and the transcript
// cannot come to disagree about what counts as "already there". They did not
// agree by construction before: the transcript's test was written with
// strings.EqualFold while the merge deduped with `seen[kw]`, and the whole
// storage suite passed with the mismatch in place. A transcript that is wrong
// in that direction stays SILENT about a keyword the merge does add — the file
// gains a value and nothing says so, which is the defect class the transcript
// exists to close.
//
// Today it is the identity: dedup is case-SENSITIVE, so "Kubernetes" and
// "kubernetes" are two keywords and a merge that adds the second must report
// it. Change this one function to change both sides together.
func keywordKey(kw string) string { return kw }

// containsKeyword reports whether list already holds s, under keywordKey.
func containsKeyword(list []string, s string) bool {
	want := keywordKey(s)
	return slices.ContainsFunc(list, func(v string) bool { return keywordKey(v) == want })
}

// WriteHostScoringConfig merges scoring overrides into the HOST-LOCAL
// per-project file. It returns the path it wrote, so a caller can print the
// real destination rather than a path it assumed, and a transcript of any
// overrides it carried forward from a lower layer.
//
// It writes nothing into any vault: neither the file, nor its lock sidecar, nor
// a surface stamp.
//
// 🔴 IT WRITES THE WHOLE ROOM, NOT THE PROPOSAL. Every layer of this config
// stack replaces a scoring room whole, so a host-local room that names only the
// tiers a tuning run changed would erase every other tier the host global
// config held for that room. Each room is therefore completed from the layers
// below before it is written, and what that copies is reported rather than done
// silently. See completeRoomsFromBelow.
//
// It reads nothing from the vault. It stays a method so its callers, which
// already hold one, keep a single spelling.
func (v *Vault) WriteHostScoringConfig(project string, rooms map[string]ScoringRoomOverride, minScore float64) (string, []string, error) {
	cfgPath, err := HostProjectConfigPath(project)
	if err != nil {
		return "", nil, err
	}

	var transcript []string
	if len(rooms) > 0 {
		below, source, berr := scoringRoomsBelowHost()
		if berr != nil {
			return "", nil, berr
		}
		alreadyHost, herr := hostRoomsAlreadyWritten(cfgPath)
		if herr != nil {
			return "", nil, herr
		}
		rooms, transcript = completeRoomsFromBelow(rooms, below, source, alreadyHost)
	}

	// <XDG>/vibe-palace/projects/<slug>.toml -> <XDG>/vibe-palace: the lock
	// sidecar lands beside the host config, never in a vault.
	lockRoot := filepath.Dir(filepath.Dir(cfgPath))
	if err := writeScoringConfigAt(cfgPath, lockRoot, "", MetaKindHostProject, rooms, minScore); err != nil {
		return "", nil, err
	}
	return cfgPath, transcript, nil
}

// renderConfigMetaHeader is the [meta] block this writer seeds a file it
// creates with. It mirrors the global config's own version fields so the two
// tiers share one schema axis, and names the kind so a reader can tell which
// schema the file follows.
func renderConfigMetaHeader(kind string) string {
	return fmt.Sprintf(`# Host-local per-project config for vibe-palace.
#
# It is NOT in the vault and is NOT shared between machines: it holds what this
# host has learned about this project, and it outranks the host global config.
#
# Only palace.scoring keys are read from this file. Anything else is reported
# once as "not a per-project key; ignored" and has no effect.
#
# Machine-written by `+"`vp discover rooms --apply`"+` and `+"`vp tune rooms --apply`"+`;
# hand edits outside the scoring sections are preserved.

[meta]
version_major = %d
version_minor = %d
kind = %q
`, CurrentVersionMajor, CurrentVersionMinor, kind)
}

// HostProjectConfigPath returns the host-local per-project config file for a
// project: <UserConfigDir>/vibe-palace/projects/<slug>.toml.
//
// 🔴 IT IS DERIVED FROM VaultConfigFilePath, NOT FROM A SECOND os.UserConfigDir
// CALL. One resolution means one directory: a test (or a host) that redirects
// the global config through XDG_CONFIG_HOME redirects this file with it, and
// the two tiers cannot come to disagree about where "the host's config" lives.
func HostProjectConfigPath(project string) (string, error) {
	if err := slug.Validate(project); err != nil {
		return "", err
	}
	p, err := VaultConfigFilePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "projects", project+".toml"), nil
}

// writeScoringConfigAt merges scoring overrides into a config file. Its one
// production destination is the host-local per-project file
// (WriteHostScoringConfig); the vault per-project file it also wrote is no
// longer read or written.
//
// 🔴 THE THREE ROOTS ARE SEPARATE ARGUMENTS BECAUSE THEY ARE SEPARATE FACTS.
// cfgPath is the file. lockRoot is where vaultlock puts its sidecar
// (<lockRoot>/.vp-locks/), which for the host-local file is the config
// directory and MUST NOT be a vault root — a lock file inside the vault for a
// file outside it would be dirt a human then has to explain. stampRoot is the
// surface-stamp root, and it is "" for a non-vault destination: there is no
// vault whose version floor a host-local write should raise.
//
// Creates the file and parent directories if they don't exist. Uses atomic
// temp-file + os.Rename. Idempotent: skips keywords already present at the
// same weight tier.
//
// Decodes the existing file into a map[string]any rather than tomlConfig
// and only ever touches the palace.scoring subtree of that map. A Go map
// decoded from TOML only ever contains the keys that were actually present
// in the source text — there is no zero-value to distinguish from "absent"
// in the first place — so a key this function never mentions (vault_path,
// git_enabled, [palace.llm], ...) cannot be turned into an explicit zero
// value by this write, no matter how many other fields the schema grows.
// Everything outside this function (tomlConfig, flatten, LoadConfig) is
// unrelated to this guarantee and untouched by it.
func writeScoringConfigAt(cfgPath, lockRoot, stampRoot, metaKind string, rooms map[string]ScoringRoomOverride, minScore float64) error {
	// A true no-op: nothing to merge and no min_score override to set. Bail
	// out before any file I/O or table navigation so a call like this never
	// creates a config file (or empty [palace]/[palace.scoring]/
	// [palace.scoring.rooms] headers in an existing one) that wasn't there
	// before.
	if len(rooms) == 0 && minScore <= 0 {
		return nil
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	// This is a read→merge→write of the same file (RMW): hold the per-path
	// lock across the whole sequence so concurrent merges never lose updates.
	release, err := vaultlock.Acquire(lockRoot, cfgPath)
	if err != nil {
		return fmt.Errorf("lock config: %w", err)
	}
	defer release()

	// Load existing config if present. m only ever holds keys that were
	// actually present in the file — a fresh-create starts as an empty map,
	// not a zero-value schema.
	//
	// The BYTES are kept, not just the decode. They are the thing this function
	// writes back; the map exists only to compute the scoring subtree.
	m := map[string]any{}
	var existing []byte
	if raw, readErr := os.ReadFile(cfgPath); readErr == nil {
		existing = raw
		if _, err := toml.Decode(string(raw), &m); err != nil {
			return fmt.Errorf("decode existing config %s: %w", cfgPath, err)
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("read existing config %s: %w", cfgPath, readErr)
	}

	// A file this writer CREATES is seeded with a [meta] block, so it carries a
	// schema version a later release can gate on; one that already exists keeps
	// its own text, because the splice below owns the scoring subtree and
	// nothing else. An empty metaKind seeds no header.
	if len(existing) == 0 && metaKind != "" {
		existing = []byte(renderConfigMetaHeader(metaKind))
	}

	// Navigate/create the palace.scoring.rooms path with checked type
	// assertions: a config file that (incorrectly) has e.g.
	// `palace = "not a table"` must fail clearly here, not panic on the
	// next assertion.
	palaceTable, err := ensureTable(m, "palace")
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	scoringTable, err := ensureTable(palaceTable, "scoring")
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	roomsTable, err := ensureTable(scoringTable, "rooms")
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}

	// Merge scoring overrides.
	for room, ov := range rooms {
		roomTable, err := ensureTable(roomsTable, room)
		if err != nil {
			return fmt.Errorf("config %s: %w", cfgPath, err)
		}
		if err := mergeTierInto(roomTable, "high", ov.High); err != nil {
			return fmt.Errorf("config %s: room %q: %w", cfgPath, room, err)
		}
		if err := mergeTierInto(roomTable, "medium", ov.Medium); err != nil {
			return fmt.Errorf("config %s: room %q: %w", cfgPath, room, err)
		}
		if err := mergeTierInto(roomTable, "low", ov.Low); err != nil {
			return fmt.Errorf("config %s: room %q: %w", cfgPath, room, err)
		}
	}
	if minScore > 0 {
		scoringTable["min_score"] = minScore
	}

	// MERGE INTO THE EXISTING TEXT, never re-encode the parsed map. The map is
	// used to compute the merged scoring subtree and for nothing else; splicing
	// that subtree back into the original bytes is what keeps every comment,
	// every commented-out template block and every section this function does
	// not own. See spliceScoringSections for what re-encoding used to destroy.
	rendered, err := renderScoringSections(scoringTable)
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	merged := spliceScoringSections(string(existing), rendered)
	if !strings.HasSuffix(merged, "\n") {
		merged += "\n"
	}
	if merged == string(existing) {
		// Nothing changed: leave the file's mtime alone. A no-op write on a
		// tracked vault file is dirt a human then has to explain.
		return nil
	}
	if err := atomicfile.Write(stampRoot, cfgPath, []byte(merged)); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	slog.Info("wrote scoring config", "path", cfgPath, "rooms", len(rooms))
	return nil
}

// ensureTable returns the map[string]any stored at key in m, creating and
// inserting an empty one if key is absent. Returns an error if key is
// present but holds something other than a table, so a malformed config
// (e.g. `palace = "not a table"`) surfaces as a clear error rather than a
// panic on the next assertion.
func ensureTable(m map[string]any, key string) (map[string]any, error) {
	existing, present := m[key]
	if !present {
		t := map[string]any{}
		m[key] = t
		return t, nil
	}
	t, ok := existing.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("[%s] is not a table", key)
	}
	return t, nil
}

// mergeTierInto merges additions into room[key] (a keyword tier array),
// reusing mergeKeywordTier's dedup semantics. A nil/empty additions list is
// a no-op — an absent tier key stays absent, matching the pre-fix struct
// writer, which never emitted `high = []` for a tier no caller populated.
//
// If room[key] is already present but isn't something that can be read back
// as a keyword array (a scalar, a sub-table, or an array containing a
// non-string element), this returns an error rather than silently
// overwriting or truncating whatever was actually there — the map-based
// rewrite must not destroy data the old struct-based writer would have
// rejected cleanly at decode time.
func mergeTierInto(room map[string]any, key string, additions []string) error {
	if len(additions) == 0 {
		return nil
	}
	existing, err := tierToStrings(room[key])
	if err != nil {
		return fmt.Errorf("[%s] %w", key, err)
	}
	merged := mergeKeywordTier(existing, additions)
	room[key] = stringsToTier(merged)
	return nil
}

// tierToStrings converts a decoded TOML array value (a []any of string, as
// produced by decoding an array into map[string]any) to a []string.
//
// v == nil means the key was absent from the source file — that's not an
// error, just a fresh tier starting empty. Any other shape that isn't a
// clean array of strings (a scalar like `high = "oops"`, a sub-table like
// `[palace.scoring.rooms.ml.high]`, or an array with a non-string element
// like `high = ["x", 42]`) is a real data-integrity problem: returning nil
// for it would let the caller silently overwrite or truncate whatever was
// actually on disk, so this returns an error instead.
func tierToStrings(v any) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("is not an array of strings (got %T)", v)
	}
	out := make([]string, 0, len(arr))
	for i, e := range arr {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("element %d is not a string (got %T)", i, e)
		}
		out = append(out, s)
	}
	return out, nil
}

// stringsToTier converts a []string back to the []any shape the TOML
// encoder expects for an array value living under a map[string]any tree.
func stringsToTier(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// mergeKeywordTier adds new keywords to an existing tier, skipping duplicates.
//
// Duplicate means equal under keywordKey, which is also what the host-local
// writer's transcript asks by, so what this function silently drops and what
// the transcript declines to announce are the same set by construction.
func mergeKeywordTier(existing, additions []string) []string {
	if len(additions) == 0 {
		return existing
	}
	seen := make(map[string]bool, len(existing))
	for _, kw := range existing {
		seen[keywordKey(kw)] = true
	}
	for _, kw := range additions {
		if k := keywordKey(kw); !seen[k] {
			existing = append(existing, kw)
			seen[k] = true
		}
	}
	return existing
}

// renderScoringSections renders the palace.scoring subtree as fully-qualified
// TOML sections: [palace.scoring] for min_score, then one
// [palace.scoring.rooms.<room>] per room, rooms in sorted order.
//
// 🔴 THE SECTION HEADERS ARE WRITTEN HERE; THE VALUES ARE NOT. Every value goes
// through the real TOML encoder, so quoting, escaping and array formatting stay
// the encoder's job and this file never becomes a second TOML emitter. What is
// hand-written is one Sprintf per header, and it is hand-written for a reason
// the encoder cannot serve: encoding a nested map emits `[palace]` and then an
// INDENTED `[palace.scoring]` beneath it, which would duplicate a `[palace]`
// section the file may already have. Fully-qualified headers are legal TOML
// standing alone and compose with whatever else the file holds.
func renderScoringSections(scoring map[string]any) (string, error) {
	var b strings.Builder

	if ms, ok := scoring["min_score"]; ok {
		leaf := map[string]any{"min_score": ms}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(leaf); err != nil {
			return "", fmt.Errorf("encode min_score: %w", err)
		}
		b.WriteString("[palace.scoring]\n")
		b.WriteString(buf.String())
		b.WriteString("\n")
	}

	roomsAny, ok := scoring["rooms"]
	if !ok {
		return b.String(), nil
	}
	rooms, ok := roomsAny.(map[string]any)
	if !ok {
		return "", fmt.Errorf("palace.scoring.rooms is %T, want a table", roomsAny)
	}
	names := make([]string, 0, len(rooms))
	for name := range rooms {
		names = append(names, name)
	}
	// Sorted so a re-run over unchanged data produces byte-identical output.
	// Map iteration order would make every write a diff.
	sort.Strings(names)

	for _, name := range names {
		leaf, ok := rooms[name].(map[string]any)
		if !ok {
			return "", fmt.Errorf("palace.scoring.rooms.%s is %T, want a table", name, rooms[name])
		}
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(leaf); err != nil {
			return "", fmt.Errorf("encode room %q: %w", name, err)
		}
		fmt.Fprintf(&b, "[palace.scoring.rooms.%s]\n", name)
		b.WriteString(buf.String())
		b.WriteString("\n")
	}
	return b.String(), nil
}

// spliceScoringSections replaces every ACTIVE section named palace.scoring or
// nested beneath it with replacement, and returns the rest of the text byte for
// byte. When the file has no such section, replacement is appended.
//
// 🔴 THIS IS THE WHOLE FIX. The previous implementation decoded the file into a
// map[string]any, merged, and re-encoded the WHOLE map — and a map has nowhere
// to hold a comment, so every round trip deleted the file header, the
// "managed by vp" warning, the commented-out [palace.llm] template and the
// [search] defaults, to record a learned keyword. Measured across the live
// vault when this was written: every project config carrying a real
// [palace.scoring block had lost its comments, and every config without one
// still had them, with no exceptions in either direction. (That census was of
// the vault per-project files, since retired. Its recorded grep under-reported
// by one file — a template splice that kept the marker line — which is why any
// census of these files reads them with a TOML parser, never a line grep.)
//
// Only the scoring subtree is machine-owned, so only the scoring subtree is
// re-rendered. Everything else survives because it is never parsed.
//
// Commented section headers are NOT sections: FindSectionRanges considers only
// uncommented headers, so the template's `# [palace.scoring]` example is inert
// text belonging to whatever section encloses it, and it survives like any other
// comment. That is deliberate — it is the documentation a reader needs most once
// a machine has written the real block.
func spliceScoringSections(original, replacement string) string {
	const prefix = "palace.scoring"

	ranges := FindSectionRanges(original)
	lines := splitLines(original)

	first := -1
	drop := make(map[int]bool, len(lines))
	for _, r := range ranges {
		if r.Name != prefix && !strings.HasPrefix(r.Name, prefix+".") {
			continue
		}
		if first == -1 || r.StartLine < first {
			first = r.StartLine
		}
		for i := r.StartLine; i < r.EndLine && i < len(lines); i++ {
			drop[i] = true
		}
	}

	// BOTH BRANCHES TRIM THE REPLACEMENT THE SAME WAY, and that is not tidiness.
	// The append branch used to keep the rendered block's trailing newline while
	// the replace branch trimmed it, so the FIRST write left a trailing blank
	// line and the SECOND removed it — making an otherwise identical re-run a
	// one-byte diff, which on a tracked vault file is exactly the dirt this
	// change exists to stop producing.
	body := strings.TrimRight(replacement, "\n")

	if first == -1 {
		// No scoring section yet: append, separated by one blank line from
		// whatever the file already ends with.
		out := strings.TrimRight(original, "\n")
		if out == "" {
			return body
		}
		return out + "\n\n" + body
	}

	var kept []string
	for i, l := range lines {
		if i == first {
			kept = append(kept, body)
		}
		if drop[i] {
			continue
		}
		kept = append(kept, l)
	}
	return joinLines(kept)
}
