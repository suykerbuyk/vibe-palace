# Hugot Spike Verdict

## Date: 2026-04-07

## System

- OS: Linux 6.15.2-arch1-1 (Arch)
- CPU: AMD Ryzen 7 7745HX (16 threads)
- RAM: 60 GB
- Go version: 1.26.0 linux/amd64

## Results

| Metric | Measured | Go threshold | No-go threshold | Status |
|--------|----------|--------------|-----------------|--------|
| Single embed latency | **66ms** | <100ms | >500ms | **GO** |
| Batch 32 throughput | **290ms** | <3s | >15s | **GO** |
| Peak memory (1024 embeds) | **306 MB heap** | <500MB | >1GB | **GO** |
| Binary size (stripped) | **17 MB** | <30MB | >50MB | **GO** |
| CGO required | **No** | No | Yes | **GO** |

## Verdict: GO

All metrics pass comfortably within go thresholds. The pure-Go backend
(`hugot.NewGoSession()`) produces correct 384-dim L2-normalized embeddings
from `all-MiniLM-L6-v2` with no CGO dependency.

## Benchmark Details

```
cpu: AMD Ryzen 7 7745HX with Radeon Graphics
BenchmarkSingleEmbed-16    66ms/op   ~31 MB/op   ~10K allocs/op
BenchmarkBatch32-16       290ms/op   ~22 MB/op   ~58K allocs/op
```

- Single embed: **66ms** avg (3 rounds × 10 iterations)
- Batch 32: **290ms** avg → **9ms/item** amortized
- Cold rebuild 10K drawers (projected): 10,000 ÷ 32 = 313 batches × 290ms = **~91s** (~1.5 min)

## Test Summary

| Test | Result |
|------|--------|
| TestModelDownload | PASS — idempotent, model.onnx + tokenizer.json present |
| TestSingleEmbed | PASS — 384-dim, non-zero, L2 norm ≈ 1.0 |
| TestEmbedDeterminism | PASS — bitwise identical across calls |
| TestEmbedSimilarity | PASS — related 0.649, unrelated 0.044 |
| TestBatchEmbed | PASS — 32 × 384, cosine sim > 0.99 vs individual |
| TestMemoryStability | PASS — 107 MB growth over 1024 embeds (within 150 MB threshold) |

## Memory Notes

- Heap after 1024 embeds: ~306 MB. This includes the model weights (~90 MB
  ONNX file loaded into memory) plus GoMLX runtime state.
- Total allocations over 1024 embeds: ~2.6 GB, indicating significant GC
  pressure from intermediate tensors. For index-time workloads this is
  acceptable; for real-time serving, pooling/reuse would be needed.
- No unbounded growth detected — heap stabilizes after initial allocations.

## Implications for Vibe-Palace

- The "single binary, zero CGO" architecture is validated.
- 9ms/item amortized means a full 10K-drawer reindex takes ~90s — well within
  the 5-minute acceptance threshold.
- Binary size at 17 MB leaves ample headroom for HNSW, MCP server, and
  application logic within a reasonable binary.
- The `pipelines.WithNormalization()` option produces unit vectors, so cosine
  similarity reduces to dot product in the HNSW index.
