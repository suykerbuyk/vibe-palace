# The optional native ONNX-Runtime (ORT) embedder

The released `vp` binary is a single, static, **zero-CGO** Go binary: it uses the
pure-Go hugot backend and needs no system libraries. This document describes an
**optional, off-by-default** native ONNX-Runtime backend that re-embeds a corpus
far faster (Phase 0 measured ~11.7× the pure-Go rate on a contended host,
projecting ~10.7 min for 50k vectors vs. hours). It is a host-native
power/measurement path, **never** part of a release artifact or a gating CI job.
See [ADR-015](adr/015-optional-native-ort-embedder.md) for the decision and its
scope.

> 🔴 The default build stays zero-CGO. The ORT backend is reachable **only** in a
> binary built with `make build-ort` (`CGO_ENABLED=1 -tags ORT`). On a default
> binary, asking for `backend=ort` fails loud with instructions — it never
> silently falls back.

## Native dependencies

The ORT backend pins the libraries hugot v0.7.0 itself pins:

| Library | Pinned version | When it is needed |
|---|---|---|
| `libtokenizers.a` (daulet/tokenizers) | v1.26.0 | **Build time** — statically linked by the `ORT` tag. |
| `libonnxruntime.so` (microsoft/onnxruntime, CPU) | **1.24.4** | **Runtime** — `dlopen`'d by the process. |

`libonnxruntime-genai.so` is **not** required: feature extraction never loads it.

### Obtaining the libraries

Both come from upstream release artifacts:

- `libtokenizers.a` — from the hugot release workflow, or built from
  `daulet/tokenizers` at the pinned tag.
- `libonnxruntime.so` — from the
  [microsoft/onnxruntime 1.24.4 release](https://github.com/microsoft/onnxruntime/releases/tag/v1.24.4)
  (`onnxruntime-linux-x64-1.24.4.tgz`), whose `lib/libonnxruntime.so` is the file.

Place both in one directory, e.g. `~/vp-ort-spike/libs/`:

```
~/vp-ort-spike/libs/
  libtokenizers.a
  libonnxruntime.so        # (or a symlink to libonnxruntime.so.1.24.4)
```

## Building

```sh
make build-ort ORT_LIB_DIR=~/vp-ort-spike/libs
```

This runs `CGO_ENABLED=1 CGO_LDFLAGS="-L$ORT_LIB_DIR" go build -tags ORT` and
produces `./vp-ort`. `ORT_LIB_DIR` defaults to `~/vp-ort-spike/libs`. The target
is **not** wired into `make build`, `make test`, or any CI job; the default build
and the full goreleaser matrix remain `CGO_ENABLED=0`.

## Selecting the backend at runtime

The backend is a runtime value, independent of the build tag's casing:

- `[embedder].backend` in config — `"go"` (default) or `"ort"`.
- `VP_EMBEDDER_BACKEND=go|ort` — overrides the config value.
- `VP_ONNX_LIBRARY_PATH=<dir>` — the **directory** holding `libonnxruntime.so`
  (hugot joins the filename itself; it must be a directory, not a file). Unset,
  hugot falls back to `/usr/lib/libonnxruntime.so` on Linux.

```sh
VP_EMBEDDER_BACKEND=ort VP_ONNX_LIBRARY_PATH=~/vp-ort-spike/libs ./vp-ort search ...
```

## Isolation (do not mix regimes)

ORT-produced vectors and Go-produced vectors are kept in separate fingerprint
namespaces: the embed-cache fingerprint records `backend=ort` for the ORT regime
(the Go regime keeps today's string byte-identical). That is the **anti-mixing
safety net**, not an isolation mechanism — the embed cache is one directory per
project. When measuring with the ORT backend, point it at a **separate physical
index-store / cache path** (a throwaway project clone or a scratch `.local`
root), never a production per-project cache, or a `vp index rebuild` would
discard the production Go vectors from that directory.

## Running the ORT tests

The `-tags ORT` tests in `internal/embedder` (truncation on long inputs, Go/ORT
parity) run only with the native libs present:

```sh
CGO_ENABLED=1 CGO_LDFLAGS="-L$HOME/vp-ort-spike/libs" \
  VP_ONNX_LIBRARY_PATH="$HOME/vp-ort-spike/libs" \
  VP_ORT_TEST_MODEL_CACHE="$HOME/vp-ort-spike/models" \
  go test -tags ORT -run ORT ./internal/embedder/
```

`VP_ORT_TEST_MODEL_CACHE` is an optional warm model-cache directory; without it
the model is downloaded into the project cache like the Go tests. Tests skip
cleanly when `VP_ONNX_LIBRARY_PATH` is unset or the library is missing.
