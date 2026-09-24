// Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0

package storage

// EmbedCacheFingerprintFile is the per-project sidecar, beside the vectors in
// palace/.local/embed-cache/<project>/, naming the embedding regime
// (embedder.Fingerprint) the directory's vectors were written under. The embed
// cache treats a missing or different sidecar as a stale directory: it removes
// the *.vec files once and writes its own fingerprint.
//
// 🔴 THE CONTRACT FOR ANYTHING THAT MOVES VECTORS BETWEEN DIRECTORIES:
//
//   - renaming a whole cache directory carries the sidecar with it;
//   - moving vectors into an EXISTING directory requires equal sidecars, or
//     removes the destination's sidecar so the directory re-validates (the
//     legacy-layout merge in SweepEmbedCaches does the latter);
//   - removing a cache directory removes the sidecar too (the orphan reap
//     does, or the directory would never empty).
//
// A vector moved without its regime is a stale vector served as a fresh one.
const EmbedCacheFingerprintFile = ".fingerprint"
