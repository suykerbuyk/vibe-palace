// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package search

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/suykerbuyk/vibe-palace/internal/storage"
)

// The real-vector source for the HNSW measurement (task
// hnsw-parameters-from-real-vector-recall-and-production-wiring, Scope 1): a
// scratch COPY of a host's embed caches, read raw through an fs.FS. Never the
// live cache, and never through EmbedCache, whose first use sweeps the tree
// and can delete vectors. The harness writes nothing into the tree it reads.

// The run-time inputs. VP_HNSW_REAL_CACHE is the trigger: unset, the real-vector
// tests skip; set, every other input they need must be present, or they fail.
const (
	envRealCache       = "VP_HNSW_REAL_CACHE"
	envRealAllowlist   = "VP_HNSW_REAL_ALLOWLIST"
	envRealFingerprint = "VP_HNSW_REAL_FINGERPRINT"
	envGridOut         = "VP_HNSW_GRID_OUT"
)

// quantumExcluded are the projects the harness never reads (ADR-014: no quantum
// project is read until the quantum split lands). They are the only project
// names compiled in. The allow-list cannot override them: naming one refuses
// the run before anything is read.
var quantumExcluded = []string{"orchestrator", "qa-metabuild-system"}

// vecFileBytes is the exact size of one cached vector: 384 little-endian
// float32s, no header. Cache writes are not atomic, so a torn file can pass the
// cache's own len%4 check; the harness takes only this exact length.
const vecFileBytes = embeddingDims * 4

// realInputs are the validated run-time inputs of a real-vector run.
type realInputs struct {
	CacheDir    string
	Allow       []string
	Fingerprint string
	GridOut     string
}

// realCacheInputs reads the real-vector inputs through getenv. With
// VP_HNSW_REAL_CACHE unset it returns run == false and no error: the run was
// not asked for, and the test skips. Once it is set, the run was asked for, and
// any missing or empty input is an error, never a skip (a skip is visually
// indistinguishable from a pass). wantGridOut also requires VP_HNSW_GRID_OUT.
func realCacheInputs(getenv func(string) string, wantGridOut bool) (realInputs, bool, error) {
	cache := getenv(envRealCache)
	if cache == "" {
		return realInputs{}, false, nil
	}
	in := realInputs{CacheDir: cache}
	ents, err := os.ReadDir(cache)
	if err != nil {
		return in, true, fmt.Errorf("%s=%q: %w", envRealCache, cache, err)
	}
	if len(ents) == 0 {
		return in, true, fmt.Errorf("%s=%q is an empty directory: nothing to measure", envRealCache, cache)
	}
	allowPath := getenv(envRealAllowlist)
	if allowPath == "" {
		return in, true, fmt.Errorf("%s is set but %s is not: name the allow-list file", envRealCache, envRealAllowlist)
	}
	allow, err := readAllowList(allowPath)
	if err != nil {
		return in, true, fmt.Errorf("%s=%q: %w", envRealAllowlist, allowPath, err)
	}
	if err := checkQuantumExclusion(allow); err != nil {
		return in, true, err
	}
	in.Allow = allow
	in.Fingerprint = strings.TrimSpace(getenv(envRealFingerprint))
	if in.Fingerprint == "" {
		return in, true, fmt.Errorf("%s is set but %s is unset or empty: give the expected .fingerprint text", envRealCache, envRealFingerprint)
	}
	if wantGridOut {
		in.GridOut = getenv(envGridOut)
		if in.GridOut == "" {
			return in, true, fmt.Errorf("%s is set but %s is not: the grid has nowhere to write its results", envRealCache, envGridOut)
		}
	}
	return in, true, nil
}

// requireRealInputs is realCacheInputs for a test: it skips when the run was not
// asked for and fails on any input error.
func requireRealInputs(t *testing.T, wantGridOut bool) realInputs {
	t.Helper()
	in, run, err := realCacheInputs(os.Getenv, wantGridOut)
	if !run {
		t.Skipf("real-vector run: set %s (and %s, %s) to run it", envRealCache, envRealAllowlist, envRealFingerprint)
	}
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// readAllowList reads one project slug per line. Blank lines and # comments
// are ignored. An empty list is an error.
func readAllowList(p string) ([]string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("the allow-list names no project")
	}
	return out, nil
}

// checkQuantumExclusion refuses an allow-list that names an excluded project.
func checkQuantumExclusion(allow []string) error {
	for _, p := range allow {
		if slices.Contains(quantumExcluded, p) {
			return fmt.Errorf("the allow-list names %q, a quantum project the harness never reads; no setting overrides the exclusion", p)
		}
	}
	return nil
}

// Skip reasons for one file of an allow-listed project.
const (
	skipNotVec   = "not .vec"
	skipShort    = "short"
	skipLong     = "long"
	skipUnusable = "unusable"
)

// projectLoad is what loading one allow-listed project found.
type projectLoad struct {
	Loaded int
	Skips  map[string]int
	// Missing: the project is allow-listed but has no directory in the cache.
	Missing bool
	// SidecarMismatch: the project's .fingerprint is absent or differs from
	// the expected text, so the whole project was skipped. Sidecar holds what
	// it said ("" when absent).
	SidecarMismatch bool
	Sidecar         string
}

// realCacheReport is the loader's account of the tree, kept beside the
// measurement. Directories outside the allow-list appear only by name.
type realCacheReport struct {
	Projects map[string]*projectLoad
	// Excluded and NotListed name the root entries skipped without being
	// opened: quantum projects, and anything not allow-listed.
	Excluded  []string
	NotListed []string
	// CrossProjectStems counts the file stems that load under more than one
	// project. Bare stems would collide; the namespaced ids do not.
	CrossProjectStems int
}

// realCorpus is the loaded union: namespaced ids and their vectors, in a
// deterministic order (projects, then files, both sorted).
type realCorpus struct {
	IDs    []string
	Vecs   [][]float32
	Report realCacheReport
}

// loadRealCache reads every allow-listed project under fsys's root. It refuses,
// before touching fsys, an allow-list that names a quantum project. The walk
// reads only the root's directory entries, and descends only into directories
// that are allow-listed and not excluded: nothing inside a skipped directory is
// ever opened, listed or statted. A project whose .fingerprint is not the
// expected text is skipped whole. A file loads only if it is a .vec of exactly
// vecFileBytes whose vector is usable; anything else is counted by reason. Ids
// are "<project>/<stem>", opaque strings that are never parsed or sized.
func loadRealCache(fsys fs.FS, allow []string, fingerprint string) (realCorpus, error) {
	if err := checkQuantumExclusion(allow); err != nil {
		return realCorpus{}, err
	}
	allowed := make(map[string]bool, len(allow))
	rep := realCacheReport{Projects: make(map[string]*projectLoad, len(allow))}
	for _, p := range allow {
		allowed[p] = true
		rep.Projects[p] = &projectLoad{Skips: map[string]int{}, Missing: true}
	}
	root, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return realCorpus{}, fmt.Errorf("read the cache root: %w", err)
	}
	var out realCorpus
	stemProjects := map[string]map[string]bool{}
	for _, e := range root {
		name := e.Name()
		switch {
		case slices.Contains(quantumExcluded, name):
			rep.Excluded = append(rep.Excluded, name)
			continue
		case !allowed[name] || !e.IsDir():
			rep.NotListed = append(rep.NotListed, name)
			continue
		}
		pl := rep.Projects[name]
		pl.Missing = false
		side, err := fs.ReadFile(fsys, path.Join(name, storage.EmbedCacheFingerprintFile))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return realCorpus{}, fmt.Errorf("read %s's sidecar: %w", name, err)
		}
		if got := strings.TrimSpace(string(side)); err != nil || got != fingerprint {
			pl.SidecarMismatch, pl.Sidecar = true, got
			continue
		}
		files, err := fs.ReadDir(fsys, name)
		if err != nil {
			return realCorpus{}, fmt.Errorf("list %s: %w", name, err)
		}
		for _, f := range files {
			fname := f.Name()
			if fname == storage.EmbedCacheFingerprintFile {
				continue
			}
			if f.IsDir() || !strings.HasSuffix(fname, ".vec") {
				pl.Skips[skipNotVec]++
				continue
			}
			data, err := fs.ReadFile(fsys, path.Join(name, fname))
			if err != nil {
				return realCorpus{}, fmt.Errorf("read %s/%s: %w", name, fname, err)
			}
			switch {
			case len(data) < vecFileBytes:
				pl.Skips[skipShort]++
				continue
			case len(data) > vecFileBytes:
				pl.Skips[skipLong]++
				continue
			}
			vec := decodeVec(data)
			if !usableVector(vec) {
				pl.Skips[skipUnusable]++
				continue
			}
			stem := strings.TrimSuffix(fname, ".vec")
			if stemProjects[stem] == nil {
				stemProjects[stem] = map[string]bool{}
			}
			stemProjects[stem][name] = true
			out.IDs = append(out.IDs, name+"/"+stem)
			out.Vecs = append(out.Vecs, vec)
			pl.Loaded++
		}
	}
	for _, ps := range stemProjects {
		if len(ps) > 1 {
			rep.CrossProjectStems++
		}
	}
	out.Report = rep
	return out, nil
}

// decodeVec decodes raw little-endian float32s.
func decodeVec(data []byte) []float32 {
	v := make([]float32, len(data)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return v
}

// encodeVec is decodeVec's inverse, for fixtures.
func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

// logRealCacheReport writes the report to the test log: per project, the
// vectors loaded and the skips by reason; then the excluded and unlisted root
// entries by name only.
func logRealCacheReport(t *testing.T, rep realCacheReport) {
	t.Helper()
	names := make([]string, 0, len(rep.Projects))
	for p := range rep.Projects {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		pl := rep.Projects[p]
		switch {
		case pl.Missing:
			t.Logf("project %s: allow-listed, no directory in the cache", p)
		case pl.SidecarMismatch:
			t.Logf("project %s: skipped whole, sidecar %q is not the expected fingerprint", p, pl.Sidecar)
		default:
			t.Logf("project %s: %d vectors loaded, skipped %v", p, pl.Loaded, pl.Skips)
		}
	}
	t.Logf("excluded (never opened): %d %v; not allow-listed (never opened): %d %v; stems under more than one project: %d",
		len(rep.Excluded), rep.Excluded, len(rep.NotListed), rep.NotListed, rep.CrossProjectStems)
}

// decoyRoot is the committed cache-shaped decoy tree, under synthetic names
// (plus orchestrator/, which must never be opened).
const decoyRoot = "testdata/hnsw-realcache-decoys"

// decoyFingerprint is the sidecar text of the decoy projects that must load.
const decoyFingerprint = "test-regime-1"

// fixedEnv returns a getenv over a fixed map.
func fixedEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// writeAllowList writes an allow-list file into dir and returns its path.
func writeAllowList(t *testing.T, dir string, lines ...string) string {
	t.Helper()
	p := dir + "/allow.txt"
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRealCacheSetButEmptyFails (row 6): with VP_HNSW_REAL_CACHE naming an empty
// directory, or one that does not exist, and every other input valid, the
// shared input check reports that the run was asked for and returns an error
// naming the cache; with VP_HNSW_REAL_CACHE unset it reports no run and no
// error, so the test skips.
func TestRealCacheSetButEmptyFails(t *testing.T) {
	scratch := t.TempDir()
	empty := t.TempDir()
	allow := writeAllowList(t, scratch, "proj-a")
	env := map[string]string{
		envRealCache:       empty,
		envRealAllowlist:   allow,
		envRealFingerprint: decoyFingerprint,
	}
	_, run, err := realCacheInputs(fixedEnv(env), false)
	if !run || err == nil || !strings.Contains(err.Error(), "empty directory") {
		t.Errorf("empty cache: run=%v err=%v, want run=true and an error naming the empty cache", run, err)
	}

	env[envRealCache] = scratch + "/no-such-cache"
	_, run, err = realCacheInputs(fixedEnv(env), false)
	if !run || err == nil || !strings.Contains(err.Error(), "no-such-cache") {
		t.Errorf("nonexistent cache: run=%v err=%v, want run=true and an error naming the path", run, err)
	}

	delete(env, envRealCache)
	_, run, err = realCacheInputs(fixedEnv(env), false)
	if run || err != nil {
		t.Errorf("unset cache: run=%v err=%v, want run=false and no error (the test skips)", run, err)
	}
}

// TestRealCacheInputsRequired (row 11): once VP_HNSW_REAL_CACHE names a usable
// directory, each missing or empty input is an error, never a skip.
func TestRealCacheInputsRequired(t *testing.T) {
	scratch := t.TempDir()
	cache := t.TempDir()
	if err := os.Mkdir(cache+"/proj-a", 0o755); err != nil {
		t.Fatal(err)
	}
	good := writeAllowList(t, scratch, "proj-a")
	emptyList := scratch + "/empty.txt"
	if err := os.WriteFile(emptyList, []byte("# only a comment\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := func() map[string]string {
		return map[string]string{
			envRealCache:       cache,
			envRealAllowlist:   good,
			envRealFingerprint: decoyFingerprint,
			envGridOut:         scratch + "/grid.jsonl",
		}
	}
	if _, run, err := realCacheInputs(fixedEnv(base()), true); !run || err != nil {
		t.Fatalf("every input valid: run=%v err=%v, want run=true and no error", run, err)
	}
	cases := []struct {
		name    string
		edit    func(map[string]string)
		grid    bool
		mention string
	}{
		{"cache directory missing", func(m map[string]string) { m[envRealCache] = scratch + "/no-such-cache" }, false, envRealCache},
		{"allow-list unset", func(m map[string]string) { delete(m, envRealAllowlist) }, false, envRealAllowlist},
		{"allow-list missing", func(m map[string]string) { m[envRealAllowlist] = scratch + "/nope.txt" }, false, envRealAllowlist},
		{"allow-list empty", func(m map[string]string) { m[envRealAllowlist] = emptyList }, false, "names no project"},
		{"fingerprint unset", func(m map[string]string) { delete(m, envRealFingerprint) }, false, envRealFingerprint},
		{"fingerprint blank", func(m map[string]string) { m[envRealFingerprint] = "  \n" }, false, envRealFingerprint},
		{"grid output unset", func(m map[string]string) { delete(m, envGridOut) }, true, envGridOut},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := base()
			c.edit(env)
			_, run, err := realCacheInputs(fixedEnv(env), c.grid)
			if !run {
				t.Fatalf("run=false: once %s is set a missing input must fail, never skip", envRealCache)
			}
			if err == nil || !strings.Contains(err.Error(), c.mention) {
				t.Fatalf("err = %v, want an error mentioning %q", err, c.mention)
			}
		})
	}
}

// countingFS records every path opened through it. Implementing only Open
// means fs.ReadDir, fs.ReadFile and fs.Stat all fall back to it, so every
// access the loader makes is recorded.
type countingFS struct {
	fsys    fs.FS
	touched []string
}

func (c *countingFS) Open(name string) (fs.File, error) {
	c.touched = append(c.touched, name)
	return c.fsys.Open(name)
}

// TestRealCacheExclusionRefuses (row 7): an allow-list naming either quantum
// project refuses before the loader touches the file system, and the shared
// input check refuses it too. Nothing overrides the exclusion.
func TestRealCacheExclusionRefuses(t *testing.T) {
	for _, q := range []string{"qa-metabuild-system", "orchestrator"} {
		t.Run(q, func(t *testing.T) {
			cfs := &countingFS{fsys: os.DirFS(decoyRoot)}
			_, err := loadRealCache(cfs, []string{"proj-a", q}, decoyFingerprint)
			if err == nil || !strings.Contains(err.Error(), q) {
				t.Errorf("loader: err = %v, want a refusal naming %q", err, q)
			}
			if len(cfs.touched) != 0 {
				t.Errorf("loader touched %v before refusing; it must read nothing", cfs.touched)
			}
			scratch := t.TempDir()
			env := map[string]string{
				envRealCache:       decoyRoot,
				envRealAllowlist:   writeAllowList(t, scratch, "proj-a", q),
				envRealFingerprint: decoyFingerprint,
			}
			if _, _, err := realCacheInputs(fixedEnv(env), false); err == nil || !strings.Contains(err.Error(), q) {
				t.Errorf("input check: err = %v, want a refusal naming %q", err, q)
			}
		})
	}
}

// TestRealCacheSkippedDirsNeverOpened (row 8): of a root holding proj-a
// (allow-listed) and orchestrator/, qa-metabuild-system/ and zz-unlisted/ (each
// with valid vectors), only proj-a is read. Nothing under the three skipped
// directories is opened, listed or statted, and each is reported by name.
func TestRealCacheSkippedDirsNeverOpened(t *testing.T) {
	root := t.TempDir()
	rng := newRandomGen(8, embeddingDims)
	for _, p := range []string{"proj-a", "orchestrator", "qa-metabuild-system", "zz-unlisted"} {
		writeCacheProject(t, root, p, decoyFingerprint, map[string][]float32{"0a0b0c0d": rng.next(), "1a1b1c1d": rng.next()})
	}
	cfs := &countingFS{fsys: os.DirFS(root)}
	corpus, err := loadRealCache(cfs, []string{"proj-a"}, decoyFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfs.touched {
		for _, skipped := range []string{"orchestrator", "qa-metabuild-system", "zz-unlisted"} {
			if p == skipped || strings.HasPrefix(p, skipped+"/") {
				t.Errorf("the loader touched %q inside skipped directory %s", p, skipped)
			}
		}
	}
	if !slices.Contains(cfs.touched, "proj-a/0a0b0c0d.vec") {
		t.Errorf("touched = %v: the counting FS saw no read of proj-a, so the check above is vacuous", cfs.touched)
	}
	if want := []string{"proj-a/0a0b0c0d", "proj-a/1a1b1c1d"}; !slices.Equal(corpus.IDs, want) {
		t.Errorf("loaded ids %v, want %v", corpus.IDs, want)
	}
	if !slices.Equal(corpus.Report.Excluded, []string{"orchestrator", "qa-metabuild-system"}) ||
		!slices.Equal(corpus.Report.NotListed, []string{"zz-unlisted"}) {
		t.Errorf("excluded %v, not listed %v; want [orchestrator qa-metabuild-system] and [zz-unlisted]",
			corpus.Report.Excluded, corpus.Report.NotListed)
	}
}

// writeCacheProject writes one cache-shaped project directory: a sidecar and
// one .vec per stem.
func writeCacheProject(t *testing.T, root, project, fingerprint string, vecs map[string][]float32) {
	t.Helper()
	dir := root + "/" + project
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/"+storage.EmbedCacheFingerprintFile, []byte(fingerprint+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for stem, v := range vecs {
		if err := os.WriteFile(dir+"/"+stem+".vec", encodeVec(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The committed decoys (decoyRoot), by project:
//   - proj-a: one valid vector 0a1b2c3d, a non-.vec notes.txt, a 1532-byte
//     short0001.vec, a 1540-byte long00001.vec, and two unusable vectors of
//     the right length, zero0000.vec (all zeros) and nan00000.vec (a NaN);
//   - proj-b: one valid vector under the same stem 0a1b2c3d;
//   - proj-x: one valid vector under a 32-hex stem (a host-local chunk id);
//   - proj-w: one valid vector, under a sidecar that names another regime;
//   - proj-n: one valid vector, and no .fingerprint sidecar at all;
//   - orchestrator: one valid vector that must never be opened.
const decoyWideStem = "00112233445566778899aabbccddeeff"

// TestRealCacheDecoyTree (row 9): on the committed decoys, every decoy is
// skipped and counted by reason (unusable vectors included), the wrong-regime
// project and the project with no sidecar are each skipped whole with what
// their sidecar said recorded, and the 32-hex stem loads as an opaque
// namespaced id.
func TestRealCacheDecoyTree(t *testing.T) {
	cfs := &countingFS{fsys: os.DirFS(decoyRoot)}
	corpus, err := loadRealCache(cfs, []string{"proj-a", "proj-b", "proj-n", "proj-w", "proj-x"}, decoyFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	rep := corpus.Report
	a := rep.Projects["proj-a"]
	if a.Loaded != 1 || a.Skips[skipNotVec] != 1 || a.Skips[skipShort] != 1 || a.Skips[skipLong] != 1 || a.Skips[skipUnusable] != 2 {
		t.Errorf("proj-a: loaded %d, skips %v; want 1 loaded, one each of %q, %q and %q, and 2 %q",
			a.Loaded, a.Skips, skipNotVec, skipShort, skipLong, skipUnusable)
	}
	if w := rep.Projects["proj-w"]; !w.SidecarMismatch || w.Sidecar != "test-regime-other" || w.Loaded != 0 {
		t.Errorf("proj-w: %+v; want skipped whole with sidecar %q recorded", *w, "test-regime-other")
	}
	if n := rep.Projects["proj-n"]; !n.SidecarMismatch || n.Sidecar != "" || n.Loaded != 0 {
		t.Errorf("proj-n: %+v; want skipped whole, with no sidecar recorded: a missing sidecar is not the expected regime", *n)
	}
	if !slices.Contains(corpus.IDs, "proj-x/"+decoyWideStem) {
		t.Errorf("ids %v: the 32-hex stem did not load as proj-x/%s", corpus.IDs, decoyWideStem)
	}
	if want := []string{"proj-a/0a1b2c3d", "proj-b/0a1b2c3d", "proj-x/" + decoyWideStem}; !slices.Equal(corpus.IDs, want) {
		t.Errorf("ids %v, want exactly %v", corpus.IDs, want)
	}
	for _, p := range cfs.touched {
		if strings.HasPrefix(p, "orchestrator") {
			t.Errorf("the loader touched %q", p)
		}
	}
}

// TestRealCacheNamespacesIdsByProject (row 10): one stem in two projects loads
// as two ids, is counted once as a cross-project stem, and survives Build: the
// index's Len is the loaded count. A bare-stem id would collapse in Build.
func TestRealCacheNamespacesIdsByProject(t *testing.T) {
	corpus, err := loadRealCache(os.DirFS(decoyRoot), []string{"proj-a", "proj-b"}, decoyFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(corpus.IDs, []string{"proj-a/0a1b2c3d", "proj-b/0a1b2c3d"}) {
		t.Fatalf("ids %v, want proj-a/0a1b2c3d and proj-b/0a1b2c3d", corpus.IDs)
	}
	if corpus.Report.CrossProjectStems != 1 {
		t.Errorf("cross-project stems = %d, want 1", corpus.Report.CrossProjectStems)
	}
	idx := newBruteIndex(embeddingDims)
	if err := idx.Build(corpus.Vecs, corpus.IDs); err != nil {
		t.Fatal(err)
	}
	if idx.Len() != len(corpus.IDs) {
		t.Errorf("Len = %d after Build, want the loaded count %d", idx.Len(), len(corpus.IDs))
	}
}
