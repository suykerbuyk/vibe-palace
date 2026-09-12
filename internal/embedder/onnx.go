// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package embedder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/pipelines"

	"github.com/suykerbuyk/vibe-palace/internal/vaultlock"
)

// defaultMaxSeqLen is all-MiniLM-L6-v2's position-embedding table size. A
// maxSeqLen <= 0 falls back to this rather than to "unlimited": hugot derives
// its batch sequence length from the actual tokens produced, so an untruncated
// input can still exceed the model's position table and panic on the shape
// mismatch (measured 2026-08-24: a 515-token batch against a 512-position
// model, `pipeline.RunPipeline` broadcast panic).
const defaultMaxSeqLen = 512

// modelCacheLockTimeout bounds how long NewONNX waits to acquire the model
// cache lock before giving up with a clean, attributable error instead of
// blocking indefinitely behind a stuck holder (e.g., one that hit the known
// hugot/go-huggingface cold-download hang — see
// onnx-lock-can-cascade-a-cold-cache-hang). It is a var, not a const, so
// tests can shrink it and prove the timeout path is fast without waiting 8
// real minutes.
//
// 8 minutes sits under `make model-test`'s explicit -timeout 10m, `make
// integration`'s implicit go-test-default 10m, and CI's outer
// `timeout-minutes: 15` on the `model` job (.github/workflows/ci.yml) that
// wraps `make model-test` — the only CI job that reaches this lock today
// (`make integration` is not invoked by CI) — leaving slack at every layer
// for a waiter to receive this error and exit cleanly before a harsher,
// unattributed timeout/panic would otherwise fire first and obscure the real
// cause. It is also generous enough to absorb one legitimate single cold
// download of the model by whichever process gets there first: round 1
// (6c73a9a) measured a warm-path NewONNX at ~650-730ms, so every OTHER
// waiter's post-lock work is fast once the first cold fetch lands the model
// on disk. 8 minutes is spent only when a holder is genuinely stuck, not by
// ordinary serialization.
var modelCacheLockTimeout = 8 * time.Minute

// modelDownloadTimeout bounds how long NewONNX waits for hugot.DownloadModel
// to return once the model-cache lock is already held. hugot.DownloadModel's
// two internal network calls (repo.DownloadInfo, then repo.DownloadFiles) run
// on an uncancellable context.Background() with no http.Client timeout
// visible anywhere in either vendored package (hugot@v0.7.0,
// go-huggingface@v0.3.5) -- a stalled connection to huggingface.co blocks the
// calling goroutine indefinitely, with nothing in vibe-palace's own code to
// bound it, until this timeout fires.
//
// This bounds only the CALLER's wait per attempt; it does not make the
// underlying network call itself cancellable. If the network is durably
// down, the leaked goroutine keeps running hugot.DownloadModel (and keeps the
// model-cache lock, see handedOff in NewONNX) until that call eventually
// returns, however long that takes -- so a subsequent NewONNX call still has
// to wait behind it, bounded by modelCacheLockTimeout. A persistently broken
// network therefore produces a sequence of bounded waits (this timeout, then
// modelCacheLockTimeout for the next attempt) instead of one infinite hang,
// not zero waiting. It is a var, not a const, so tests can shrink it and
// prove the timeout path is fast without waiting 10 real minutes.
var modelDownloadTimeout = 10 * time.Minute

// downloadModel is a seam over hugot.DownloadModel so tests can substitute a
// deterministic, network-free stand-in for a stalled download -- proving the
// timeout and lock-transfer behavior below without patching the vendored
// dependency or requiring real (and inherently timing-dependent) network
// access.
var downloadModel = hugot.DownloadModel

// ONNXEmbedder implements Embedder using the hugot pure-Go ONNX backend.
type ONNXEmbedder struct {
	session   *hugot.Session
	pipeline  *pipelines.FeatureExtractionPipeline
	mu        sync.Mutex
	dims      int
	batchSz   int
	maxSeqLen int
}

// modelCacheLockPath derives the absolute path NewONNX locks against before
// touching modelCacheDir. It MUST mirror hugot's own modelPath derivation
// (hugot@v0.7.0 downloader.go:DownloadModel) byte-for-byte, colon-stripping
// included, or the lock silently guards a different path than the one hugot
// (via viant/afs's non-atomic remove+recreate+copy in file/upload.go:Upload)
// actually writes to — defeating the lock for any HF revision-pinned model
// name ("org/model:revision"). hugot strips everything from the first ':'
// onward before substituting '/' for '_'; replicate that exactly, not just
// the slash substitution.
func modelCacheLockPath(modelCacheDir, modelName string) string {
	modelP := modelName
	if strings.Contains(modelP, ":") {
		modelP = strings.Split(modelName, ":")[0]
	}
	return filepath.Join(modelCacheDir, strings.ReplaceAll(modelP, "/", "_"))
}

// localSnapshotComplete reports whether modelPath already holds everything
// hugot.DownloadModel would produce there: a tokenizer.json at the top level
// (the only file backends.LoadTokenizer requires — a missing one makes it a
// silent no-op tokenizer, so treat it as required) and at least one .onnx
// file. hugot's own copy step flattens all files with path.Base, so both are
// checked at the top level, not nested under "onnx/".
func localSnapshotComplete(modelPath string) bool {
	if _, err := os.Stat(filepath.Join(modelPath, "tokenizer.json")); err != nil {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(modelPath, "*.onnx"))
	return err == nil && len(matches) > 0
}

// NewONNX creates an ONNXEmbedder. modelCacheDir is where model files are
// downloaded and cached (e.g., {vault}/.local/models/).
//
// modelCacheDir is shared across every process that builds an embedder (every
// vp-owned test package, plus the CLI), and hugot.DownloadModel re-copies the
// model into it on every call with a non-atomic remove+recreate+copy (traced
// to viant/afs@v1.30.0's file/upload.go:Upload). A concurrent process can
// therefore observe a half-written model.onnx. NewONNX takes a blocking
// cross-process lock, keyed on the same path hugot itself writes to, and
// holds it across the download, pipeline build, and dimension probe so a
// second process never opens the file mid-write.
func NewONNX(modelName, modelCacheDir string, maxSeqLen, batchSize int) (*ONNXEmbedder, error) {
	if maxSeqLen <= 0 {
		maxSeqLen = defaultMaxSeqLen
	}

	lockTarget := modelCacheLockPath(modelCacheDir, modelName)
	release, err := vaultlock.AcquireWithTimeout(modelCacheDir, lockTarget, modelCacheLockTimeout)
	if err != nil {
		return nil, fmt.Errorf("lock model cache (waited up to %s): %w", modelCacheLockTimeout, err)
	}
	// handedOff decides, by construction, which of the two call sites below
	// releases the lock: this deferred release on every ordinary return, or
	// the leaked goroutine's release after a download timeout. Exactly one of
	// them ever runs release() -- never both, never a race between them --
	// because handedOff is written and read only in this goroutine,
	// sequentially, and Go guarantees a defer's read happens after every
	// earlier statement in the function, including the write in the timeout
	// branch below. (An earlier draft released via defer unconditionally and
	// separately tried to guard a double release with sync.Once; that stops
	// only a SECOND call to release(), not the deferred call racing ahead of
	// the leaked goroutine and firing FIRST, which is the actual hazard --
	// see the task's Review (2026-09-12b) for why that guard doesn't work.)
	handedOff := false
	defer func() {
		if !handedOff {
			release()
		}
	}()

	session, err := hugot.NewGoSession()
	if err != nil {
		return nil, fmt.Errorf("create go session: %w", err)
	}

	// hugot.DownloadModel forces a network round trip on every call, even on
	// a fully warm cache: go-huggingface's readCommitHashForRevision() always
	// refreshes the revision-info file on a Repo's first call in a process,
	// and deletes the cached copy before re-fetching it — so an offline
	// failure here also destroys the index a later offline run would need.
	// When the destination already holds a complete snapshot (a prior
	// successful download), skip hugot.DownloadModel entirely rather than
	// risk that delete-then-refetch offline.
	modelPath := lockTarget
	if !localSnapshotComplete(modelPath) {
		dlOpts := hugot.NewDownloadOptions()
		dlOpts.OnnxFilePath = "onnx/model.onnx"

		type dlResult struct {
			path string
			err  error
		}
		// Buffered so the goroutine can always deliver its result and exit,
		// even after NewONNX has already returned on the timeout branch below
		// and nothing is left listening synchronously.
		resultCh := make(chan dlResult, 1)
		go func() {
			p, derr := downloadModel(modelName, modelCacheDir, dlOpts)
			resultCh <- dlResult{p, derr}
		}()

		select {
		case res := <-resultCh:
			if res.err != nil {
				session.Destroy()
				return nil, fmt.Errorf("download model %s: %w", modelName, res.err)
			}
			modelPath = res.path
			// handedOff stays false: the outer defer releases the lock
			// normally, exactly as before this change.
		case <-time.After(modelDownloadTimeout):
			// The goroutine above may still be blocked inside downloadModel,
			// possibly still writing into modelCacheDir, so the lock must not
			// release yet even though NewONNX itself returns now. Transfer
			// release() to a second goroutine that waits for the real
			// download attempt to actually finish (success or error) before
			// calling it -- see the handedOff comment above the defer for why
			// this is race-free without sync.Once.
			handedOff = true
			go func() {
				<-resultCh
				release()
			}()
			session.Destroy()
			return nil, fmt.Errorf("download model %s: timed out after %s (network to huggingface.co may be unreachable or stalled)", modelName, modelDownloadTimeout)
		}
	}

	config := hugot.FeatureExtractionConfig{
		ModelPath:    modelPath,
		Name:         "vp-embedder",
		OnnxFilename: "onnx/model.onnx",
		Options: []hugot.FeatureExtractionOption{
			pipelines.WithNormalization(),
		},
	}

	pipeline, err := hugot.NewPipeline(session, config)
	if err != nil {
		session.Destroy()
		return nil, fmt.Errorf("create pipeline: %w", err)
	}

	// Probe dimensions with a test embedding.
	probe, err := pipeline.RunPipeline([]string{"probe"})
	if err != nil {
		session.Destroy()
		return nil, fmt.Errorf("probe embedding dimensions: %w", err)
	}
	if len(probe.Embeddings) == 0 || len(probe.Embeddings[0]) == 0 {
		session.Destroy()
		return nil, fmt.Errorf("probe returned empty embedding")
	}

	return &ONNXEmbedder{
		session:   session,
		pipeline:  pipeline,
		dims:      len(probe.Embeddings[0]),
		batchSz:   batchSize,
		maxSeqLen: maxSeqLen,
	}, nil
}

// truncateForModel bounds text to at most maxSeqLen-2 runes — a conservative
// stand-in for token count, since word-piece tokenization cannot produce more
// tokens than there are runes to consume. The -2 reserves room for the
// [CLS] and [SEP] special tokens hugot adds around the content tokens: at
// maxSeqLen itself (e.g. the 512-position default), a rune-for-rune worst
// case can still tokenize to maxSeqLen content tokens and, with CLS+SEP,
// overflow the position table by 2 — which is exactly the measured 515-vs-512
// panic. No tokenizer dependency is added; the floor of 1 keeps a tiny
// maxSeqLen (e.g. a test value of 2) from truncating to nothing.
func (e *ONNXEmbedder) truncateForModel(text string) string {
	limit := e.maxSeqLen - 2
	if limit < 1 {
		limit = 1
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// Embed returns a normalized embedding vector for a single text.
func (e *ONNXEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	result, err := e.pipeline.RunPipeline([]string{e.truncateForModel(text)})
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	if len(result.Embeddings) == 0 {
		return nil, fmt.Errorf("embed returned no results")
	}
	return result.Embeddings[0], nil
}

// EmbedBatch returns normalized embedding vectors for multiple texts.
// Inputs are processed in chunks of batchSize with context checks between chunks.
func (e *ONNXEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	result := make([][]float32, 0, len(texts))

	for start := 0; start < len(texts); start += e.batchSz {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		end := min(start+e.batchSz, len(texts))
		chunk := texts[start:end]
		truncated := make([]string, len(chunk))
		for i, t := range chunk {
			truncated[i] = e.truncateForModel(t)
		}

		e.mu.Lock()
		out, err := e.pipeline.RunPipeline(truncated)
		e.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("embed batch chunk [%d:%d]: %w", start, end, err)
		}
		result = append(result, out.Embeddings...)
	}

	return result, nil
}

// Dimensions returns the embedding dimensionality (384 for all-MiniLM-L6-v2).
// The model is already loaded, so this never fails.
func (e *ONNXEmbedder) Dimensions() (int, error) { return e.dims, nil }

// Close releases the hugot session and pipeline resources.
func (e *ONNXEmbedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.session != nil {
		e.session.Destroy()
		e.session = nil
	}
	return nil
}
