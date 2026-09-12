// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package testinfra

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/capture"
	"github.com/suykerbuyk/vibe-palace/internal/embedder"
	"github.com/suykerbuyk/vibe-palace/internal/memorytestutil"
	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// DrawerSpec is the caller-facing shape of one drawer to seed via
// WithDrawers/WithDrawer/WithDrawerOut. It mirrors storage.Drawer minus ID
// (generated deterministically by storage.DrawerID at flush time, exactly as
// storage.AppendDrawers itself generates it) and AddedBy (optional, left
// empty — no current seeding caller needs it).
type DrawerSpec struct {
	Content    string
	Hall       string
	SourceType string
	FiledAt    string
}

// MemoryFileSpec is one native-memory-shaped file to write via WithMemory.
type MemoryFileSpec struct {
	Name    string
	Content string
}

// drawerGroupKey batches every WithDrawers/WithDrawer/WithDrawerOut entry
// targeting the same (project, wing, room) so the flush at the end of
// New/Seed can write it with exactly one storage.AppendDrawers call, instead
// of one call per option. AppendDrawers already pays the room's dedup scan
// once per call; batching by group is what keeps N seed options against one
// room at O(1) scans rather than O(N).
type drawerGroupKey struct {
	project, wing, room string
}

// drawerEntry pairs one seeded DrawerSpec with the caller's optional
// out-parameter (set only by WithDrawerOut; nil for WithDrawers/WithDrawer).
type drawerEntry struct {
	spec DrawerSpec
	out  *storage.Drawer
}

// seedState is the bookkeeping New and Seed thread through the SeedOptions
// applied to one call. It carries the in-progress *TestHarness (every option
// closes over it through st.h rather than taking it as a separate function
// parameter, which is what keeps SeedOption's signature down to
// (*seedState, *testing.T)) plus the pending drawer batches, keyed by
// (project, wing, room) and flushed once — via flushDrawers — after every
// option in the call has run.
type seedState struct {
	h            *TestHarness
	drawerGroups map[drawerGroupKey][]drawerEntry
	// order preserves first-seen group order so a flush failure names a
	// deterministic, reproducible group rather than a map-iteration-order one.
	order []drawerGroupKey
}

func newSeedState(h *TestHarness) *seedState {
	return &seedState{h: h, drawerGroups: make(map[drawerGroupKey][]drawerEntry)}
}

func (st *seedState) enqueueDrawer(key drawerGroupKey, spec DrawerSpec, out *storage.Drawer) {
	if _, ok := st.drawerGroups[key]; !ok {
		st.order = append(st.order, key)
	}
	st.drawerGroups[key] = append(st.drawerGroups[key], drawerEntry{spec: spec, out: out})
}

// SeedOption configures one piece of ephemeral-vault fixture state, applied
// in order by New (which also constructs the harness) or Seed (which seeds an
// already-built one). A batching option (WithDrawers/WithDrawer/WithDrawerOut)
// enqueues into st rather than writing immediately; the drawer batches are
// flushed once, after every option in the call has run.
type SeedOption func(*seedState, *testing.T)

// New builds a fresh TestHarness — via NewHarnessWithEmbedder + a mock
// embedder, matching the harness's existing default — registers every MCP
// tool on it (so a WithCapturedSession option, or any later h.CallTool, has a
// registry to dispatch through), and applies opts to it in order. This
// collapses a test's whole fixture into one call:
//
//	h := testinfra.New(t, testinfra.WithProject("demo"), testinfra.WithDrawers(...))
//
// RegisterAllTools always runs; InitMCP does not run eagerly here — it runs
// lazily, exactly as CallToolRaw already does, so it only fires at all when a
// seed option (WithCapturedSession) or the test itself actually calls a tool.
func New(t *testing.T, opts ...SeedOption) *TestHarness {
	t.Helper()
	h := NewHarnessWithEmbedder(t, embedder.NewMock(384))
	h.RegisterAllTools(t)
	h.Seed(t, opts...)
	return h
}

// Seed applies opts to an already-built TestHarness. This is the common case
// for tests that construct h earlier for unrelated reasons (a custom
// embedder, config overrides, a git repo layered on top of the vault root)
// and only need the seeding step itself to be declarative.
func (h *TestHarness) Seed(t *testing.T, opts ...SeedOption) {
	t.Helper()
	st := newSeedState(h)
	for _, opt := range opts {
		opt(st, t)
	}
	flushDrawers(st, t)
}

// flushDrawers writes every batched (project, wing, room) group with exactly
// one storage.AppendDrawers call, then — for any entry seeded via
// WithDrawerOut — assigns the written storage.Drawer into the caller's out
// pointer. The ID is computed directly via storage.DrawerID(wing, content),
// the SAME exported function AppendDrawers itself calls internally to mint
// each drawer's ID, so the two are provably identical rather than merely
// similar; no ListDrawers re-scan is needed to learn it.
//
// AppendDrawers acquires the vaultlock itself (ADR-003): this function must
// never wrap the call in a lock of its own, or a caller already holding one
// (there are none today, but the invariant matters for whoever adds one)
// would deadlock re-entering it.
func flushDrawers(st *seedState, t *testing.T) {
	t.Helper()
	for _, key := range st.order {
		entries := st.drawerGroups[key]
		ds := make([]storage.Drawer, len(entries))
		for i, e := range entries {
			ds[i] = storage.Drawer{
				Content:    e.spec.Content,
				Hall:       e.spec.Hall,
				SourceType: e.spec.SourceType,
				FiledAt:    e.spec.FiledAt,
			}
		}
		if _, err := st.h.Vault.AppendDrawers(key.project, key.wing, key.room, ds); err != nil {
			t.Fatalf("seed drawers %s/%s/%s: %v", key.project, key.wing, key.room, err)
		}
		for _, e := range entries {
			if e.out == nil {
				continue
			}
			*e.out = storage.Drawer{
				ID:         storage.DrawerID(key.wing, e.spec.Content),
				Content:    e.spec.Content,
				Hall:       e.spec.Hall,
				SourceType: e.spec.SourceType,
				FiledAt:    e.spec.FiledAt,
			}
		}
	}
}

// WithProject materializes Projects/<slug>/ in the harness vault. Thin
// wrapper over TestHarness.SeedProject, replacing bare
// h.seedProject(t, ...)/h.SeedProject(t, ...) calls inside a New/Seed call.
func WithProject(slug string) SeedOption {
	return func(st *seedState, t *testing.T) {
		t.Helper()
		st.h.SeedProject(t, slug)
	}
}

// WithDrawers seeds every DrawerSpec in drawers into (project, wing, room) via
// storage.AppendDrawers (the batch entry point) — never AppendDrawer, the n=1
// wrapper, whose per-call room scan would make a large WithDrawers batch
// O(N²). Every WithDrawers/WithDrawer/WithDrawerOut option in the same
// New/Seed call that targets the same (project, wing, room) is batched
// together and flushed with one AppendDrawers call, regardless of how many
// separate option calls contributed to it.
func WithDrawers(project, wing, room string, drawers ...DrawerSpec) SeedOption {
	return func(st *seedState, t *testing.T) {
		key := drawerGroupKey{project: project, wing: wing, room: room}
		for _, d := range drawers {
			st.enqueueDrawer(key, d, nil)
		}
	}
}

// WithDrawer is the singular convenience over WithDrawers for the common
// one-drawer case, so a call site does not have to build a one-element
// []DrawerSpec. It still routes through the same batched AppendDrawers flush
// as WithDrawers — never AppendDrawer — so the O(N) property holds even for N
// mixed WithDrawer/WithDrawers/WithDrawerOut options in one New/Seed call.
// SourceType is hardcoded to "manual", matching TestHarness.AddDrawer's
// existing behavior for the same convenience shape.
func WithDrawer(project, wing, room, content, hall, filedAt string) SeedOption {
	return func(st *seedState, t *testing.T) {
		key := drawerGroupKey{project: project, wing: wing, room: room}
		st.enqueueDrawer(key, DrawerSpec{
			Content:    content,
			Hall:       hall,
			SourceType: "manual",
			FiledAt:    filedAt,
		}, nil)
	}
}

// WithDrawerOut is WithDrawer plus a captured return: it assigns the written
// storage.Drawer into out — populated no later than the end of the enclosing
// New/Seed call, once flushDrawers runs. It exists for the handful of call
// sites that need the generated ID (or, in one case, the content) back, the
// same way TestHarness.AddDrawer already returns it; unlike AddDrawer it does
// not re-scan the room via ListDrawers to learn the ID, since
// storage.DrawerID(wing, content) is deterministic and scan-independent.
//
// Reading out from inside another SeedOption in the SAME New/Seed call is
// unsupported: the flush that populates it is deferred to the end of the
// call, so out still holds its zero value until then.
//
// Note: if AppendDrawers silently skips this entry as an already-filed
// duplicate (same room, same wing+content as an earlier write with different
// Hall/FiledAt), out is still assigned from the DrawerSpec just requested,
// not from what is actually on disk. No current caller's fixture collides
// this way (each seeds fresh, unique content into a fresh ephemeral vault);
// a future caller relying on WithDrawerOut across a possible duplicate should
// not assume out reflects the pre-existing on-disk metadata.
func WithDrawerOut(project, wing, room, content, hall, filedAt string, out *storage.Drawer) SeedOption {
	return func(st *seedState, t *testing.T) {
		key := drawerGroupKey{project: project, wing: wing, room: room}
		st.enqueueDrawer(key, DrawerSpec{
			Content:    content,
			Hall:       hall,
			SourceType: "manual",
			FiledAt:    filedAt,
		}, out)
	}
}

// WithResume seeds Projects/<project>/resume.md via the storage-direct lane
// (Vault.WriteResume), bypassing vp_update_resume's MCP schema/CAS surface
// entirely. Because WriteResume's compare-and-set treats an empty
// expectedSha256 as a mismatch whenever the file already exists, this is only
// for seeding a resume that does not exist yet in the ephemeral vault — every
// current caller (a fresh harness's very first resume write) is exactly that.
func WithResume(project, content string) SeedOption {
	return func(st *seedState, t *testing.T) {
		t.Helper()
		if err := st.h.Vault.WriteResume(project, content, ""); err != nil {
			t.Fatalf("seed resume for %s: %v", project, err)
		}
	}
}

// WithMemory seeds Projects/<project>/memory/ — the vault's own memory store,
// read back by Vault.ListMemories/ReadMemory and by vp_memory_read — directly
// on disk, bypassing vp_memory_write's MCP surface. This is new code, not a
// call to memorytestutil.WriteNativeMemoryFixture: that helper is dir-only
// and writes a hardcoded 4-file set, so it cannot be parameterized by project
// or by content on its own. With no files given, WithMemory falls back to
// that exact 4-file fixture (still via WriteNativeMemoryFixture, preserving
// it as the canonical fixture shape) targeted at this project's memory dir;
// with files given, it writes each one directly, parameterized as asked.
func WithMemory(project string, files ...MemoryFileSpec) SeedOption {
	return func(st *seedState, t *testing.T) {
		t.Helper()
		dir, err := st.h.Vault.MemoryDir(project)
		if err != nil {
			t.Fatalf("seed memory dir for %s: %v", project, err)
		}
		if len(files) == 0 {
			if err := memorytestutil.WriteNativeMemoryFixture(dir); err != nil {
				t.Fatalf("seed memory fixture for %s: %v", project, err)
			}
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("seed memory dir for %s: %v", project, err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(dir, f.Name), []byte(f.Content), 0o644); err != nil {
				t.Fatalf("seed memory file %s for %s: %v", f.Name, project, err)
			}
		}
	}
}

// WithSession is the storage-direct lane for a captured session: it wraps
// capture.WriteSession directly, bypassing MCP schema validation and dispatch
// entirely. Fast and deterministic, at the cost of not exercising the real
// tool surface — WithCapturedSession is the named, explicit MCP-lane
// alternative for tests that need that surface exercised instead.
func WithSession(p capture.SessionParams) SeedOption {
	return func(st *seedState, t *testing.T) {
		t.Helper()
		if _, err := capture.WriteSession(context.Background(), st.h.Vault, st.h.Indexer, p); err != nil {
			t.Fatalf("seed session for %s: %v", p.Project, err)
		}
	}
}

// WithCapturedSession is the MCP lane for a captured session: it wraps
// h.CallTool(t, "vp_capture_session", args), so schema validation and
// dispatch run exactly as they do in production. This is deliberately NOT
// folded into or aliased with WithSession — the two exercise materially
// different code paths, and blurring them is exactly what the "declare the
// seeding lane" requirement this option satisfies exists to prevent.
//
// It returns its result via out (analogous to WithDrawerOut, for the same
// reason: a pure SeedOption has nowhere for a value to come back through),
// populated with the raw result text — matching h.CallTool's own return —
// no later than the point this option itself runs (immediately, unlike the
// batched drawer options: an MCP call is not something flushDrawers defers).
// out may be nil for a caller that only wants the side effect.
func WithCapturedSession(args map[string]any, out *string) SeedOption {
	return func(st *seedState, t *testing.T) {
		t.Helper()
		text := st.h.CallTool(t, "vp_capture_session", args)
		if out != nil {
			*out = text
		}
	}
}
