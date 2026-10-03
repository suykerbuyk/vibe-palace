#!/usr/bin/env bash
# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
#
# Verify that third_party/coder-hnsw/ is upstream github.com/coder/hnsw, at the
# version go.mod requires, plus third_party/coder-hnsw/vp.patch and NOTHING
# else: the same files, the same bytes, and the same executable bits. The
# vendored copy exists only because upstream's SavedGraph does not build for
# windows (task vector-index-interface-and-coder-hnsw-wrapper); delete the
# copy, the patch, the go.mod replace and this script when upstream merges the
# fix.
#
# Needs the network (go mod download), so it runs in the `hnsw` CI job, never in
# `make test`.
#
# Exit status: 0 when the copy matches, 1 on any drift, 2 when the check itself
# could not run (no go.mod requirement, a failed download, an unusable TMPDIR, a
# patch file missing). A setup failure is never reported as a match or as drift.
set -euo pipefail

setup_fail() {
	echo "check-hnsw-vendor: SETUP FAILED: $*" >&2
	exit 2
}

root=$(cd "$(dirname "$0")/.." && pwd) || setup_fail "cannot resolve the repository root"
vendored="$root/third_party/coder-hnsw"
patchfile="$vendored/vp.patch"
[ -d "$vendored" ] || setup_fail "no vendored copy at $vendored"
[ -f "$patchfile" ] || setup_fail "no patch at $patchfile"
cd "$root" || setup_fail "cannot enter $root"

# The REQUIRED version, not the replacement: a replace leaves .Version as the
# version named in the require block.
version=$(go list -m -f '{{.Version}}' github.com/coder/hnsw) ||
	setup_fail "go list cannot resolve github.com/coder/hnsw"
[ -n "$version" ] || setup_fail "go.mod requires no github.com/coder/hnsw"

json=$(go mod download -json "github.com/coder/hnsw@$version") ||
	setup_fail "go mod download github.com/coder/hnsw@$version failed"
upstream=$(printf '%s\n' "$json" | sed -n 's/^[[:space:]]*"Dir": "\(.*\)",$/\1/p')
[ -n "$upstream" ] && [ -d "$upstream" ] ||
	setup_fail "go mod download gave no directory for coder/hnsw@$version"

# mktemp honours TMPDIR; an unusable one is a setup failure, never drift.
work=$(mktemp -d) || setup_fail "mktemp -d failed (TMPDIR=${TMPDIR:-unset})"
trap 'rm -rf "$work"' EXIT
cp -r "$upstream" "$work/patched" || setup_fail "cannot copy upstream into $work"
chmod -R u+w "$work/patched" || setup_fail "cannot make $work/patched writable"
if ! patch -s -p1 -d "$work/patched" <"$patchfile"; then
	echo "check-hnsw-vendor: vp.patch does not apply to coder/hnsw@$version" >&2
	exit 1
fi
cp "$patchfile" "$work/patched/vp.patch" || setup_fail "cannot copy vp.patch into $work"

drift=0
if ! diff -r "$work/patched" "$vendored"; then
	drift=1
fi

# diff -r compares bytes, never modes. A module zip carries no file modes, so
# upstream as downloaded has no executable file: the vendored copy must have
# none either (git tracks only the executable bit).
executables() { (cd "$1" && find . -type f -perm -u=x | LC_ALL=C sort); }
if ! diff <(executables "$work/patched") <(executables "$vendored") >"$work/modes.diff"; then
	echo "check-hnsw-vendor: executable-bit drift (< upstream+patch, > vendored):" >&2
	cat "$work/modes.diff" >&2
	drift=1
fi

if [ "$drift" -ne 0 ]; then
	echo "check-hnsw-vendor: third_party/coder-hnsw differs from coder/hnsw@$version + vp.patch" >&2
	exit 1
fi
echo "check-hnsw-vendor: third_party/coder-hnsw == coder/hnsw@$version + vp.patch"
