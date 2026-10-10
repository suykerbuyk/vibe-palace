#!/usr/bin/env bash
# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
#
# Resolve every `path:line` source citation in doc/ARCHITECTURE.md against the
# working tree and FAIL if any names a file that does not exist or a line past
# the end of that file. ARCHITECTURE is the only doc that carries code
# citations (the user guides carry none at this release), so it is the only
# file checked.
#
# Needs no network and changes nothing, so it is safe in `make test` and runs
# in CI via `make arch-cites`.
#
# Citation forms handled, in document order:
#   full-path cite   `internal/search/engine.go:1236`       (contains a slash)
#   full-path range  `internal/surface/version.go:807-812`  (end of range checked)
#   root hidden file `.goreleaser.yml:10`                    (leading dot, no slash)
#   bare-filename    `engine.go:1447`   resolved to its package dir (below)
#   line-only cont.  `:1247`            resolved against the last named file
#
# Bare-filename resolution (requirement a / c): a bare `foo.go:N` is resolved to
# the directory in which ARCHITECTURE itself names `.../foo.go` as a full path
# (its package dir). If the document anchors that basename to more than one
# directory, or it is not anchored in the document and is not unique in the
# repository, the check FAILS CLOSED rather than guess.
#
# Revision-pinned cites are EXCLUDED (requirement b): a citation qualified by an
# explicit revision is pinned to that revision, not to the working tree, so it
# is never resolved here. Both forms live inside one `code` span:
#   upstream, module-pinned:  `coder/hnsw@36cab60 encode.go:304`
#   in-repo, commit-pinned:   `a32a2d4 internal/capture/decisions.go:157`
#
# Exit status: 0 when every resolvable cite holds; 1 on a bad/unresolvable cite;
# 2 when the check itself could not run.
set -euo pipefail

setup_fail() {
	echo "check-architecture-citations: SETUP FAILED: $*" >&2
	exit 2
}

root=$(cd "$(dirname "$0")/.." && pwd) || setup_fail "cannot resolve the repository root"
doc="$root/doc/ARCHITECTURE.md"
[ -f "$doc" ] || setup_fail "no doc/ARCHITECTURE.md at $doc"
cd "$root" || setup_fail "cannot enter $root"

work=$(mktemp) || setup_fail "mktemp failed (TMPDIR=${TMPDIR:-unset})"
trap 'rm -f "$work"' EXIT

# Strip the revision-pinned spans first, so the cites they carry are never
# resolved against the tree: one pattern per span — a module pin (`…@<sha> …`)
# and a leading commit pin (`<sha> path:line`).
sed -E \
	-e 's/`[^`]*@[0-9a-f]{6,40}[^`]*`/ /g' \
	-e 's/`[0-9a-f]{7,40}[[:space:]]+[^`]*`/ /g' \
	"$doc" >"$work" || setup_fail "cannot preprocess $doc"

# The cleaned text is fed to awk twice: pass 1 (FNR==NR) learns, from every full
# slash-path the document writes, which directory each basename lives in; pass 2
# walks the cites in order and checks each.
awk -v root="$root" '
	function eof_of(path,   n, cmd) {
		if (path in eofcache) return eofcache[path]
		cmd = "wc -l < \"" root "/" path "\" 2>/dev/null"
		n = -1
		if ((cmd | getline n) <= 0) n = -1
		close(cmd)
		eofcache[path] = n + 0
		return eofcache[path]
	}
	function exists(path,   cmd, r) {
		cmd = "test -f \"" root "/" path "\" && echo y"
		r = ""; cmd | getline r; close(cmd)
		return (r == "y")
	}
	function find_unique(base,   cmd, line, hits, only) {
		cmd = "find . -type f -name \"" base "\" -not -path \"./.git/*\" 2>/dev/null"
		hits = 0; only = ""
		while ((cmd | getline line) > 0) { hits++; only = line }
		close(cmd)
		if (hits == 1) { sub(/^\.\//, "", only); return only }
		if (hits > 1) return "AMBIG"
		return ""
	}
	function fail(msg) { bad++; print "  FAIL: " msg > "/dev/stderr" }
	function checkfile(label, path, hi,   n) {
		checked++
		if (!exists(path)) { fail(label " -> missing file: " path); return }
		n = eof_of(path)
		if (n < 0)  { fail(label " -> cannot read: " path); return }
		if (hi > n) { fail(label " -> line " hi " past EOF (" path " has " n " lines)"); return }
	}

	# ---- pass 1: basename -> directory(ies) the document names it in ----
	FNR == NR {
		s = $0
		while (match(s, /[A-Za-z0-9_.\/-]+\/[A-Za-z0-9_.-]+\.(go|yml|yaml|json|toml|sh|md|html)/)) {
			p = substr(s, RSTART, RLENGTH); s = substr(s, RSTART + RLENGTH)
			b = p; sub(/.*\//, "", b)
			d = p; sub(/\/[^\/]+$/, "", d)
			if (!((b SUBSEP d) in seen)) { seen[b SUBSEP d] = 1; dircount[b]++; onedir[b] = d }
		}
		next
	}

	# ---- pass 2: resolve and check every cite, left to right ----
	{
		line = $0; off = 1
		while (1) {
			seg = substr(line, off)
			if (match(seg, /[A-Za-z0-9_.\/-]*:[0-9]+(-[0-9]+)?/) == 0) break
			tstart = off + RSTART - 1
			tok = substr(line, tstart, RLENGTH)
			prev = (tstart > 1) ? substr(line, tstart - 1, 1) : " "
			off = tstart + RLENGTH

			ci = index(tok, ":")
			path = substr(tok, 1, ci - 1)
			spec = substr(tok, ci + 1)
			split(spec, r, "-"); hi = (2 in r) ? r[2] + 0 : r[1] + 0

			if (path == "") {
				# line-only cite — only real if not glued to a preceding number/word
				if (prev ~ /[A-Za-z0-9_.\/-]/) continue
				if (curfile == "") { fail("line-only cite `:" spec "` has no preceding named file"); continue }
				checkfile(":" spec " (-> " curfile ")", curfile, hi)
			} else if (path ~ /\.[A-Za-z][A-Za-z0-9]*$/) {
				# NAMED cite (path ends with a letter-led extension)
				if (path ~ /\//) {
					resolved = path
				} else {
					base = path
					if (dircount[base] == 1) resolved = onedir[base] "/" base
					else if (dircount[base] > 1) { fail("bare cite `" tok "` is AMBIGUOUS — ARCHITECTURE anchors `" base "` to " dircount[base] " dirs; use a full path"); continue }
					else {
						u = find_unique(base)
						if (u == "AMBIG") { fail("bare cite `" tok "` is AMBIGUOUS in the repo; use a full path"); continue }
						if (u == "")      { fail("bare cite `" tok "` names no file in the repo"); continue }
						resolved = u
					}
				}
				curfile = resolved
				checkfile(tok, resolved, hi)
			}
			# else: path without a letter-led extension (a clock time, a version) — not a cite
		}
	}

	END {
		printf "check-architecture-citations: %d citation(s) checked, %d bad\n", checked, bad + 0 > "/dev/stderr"
		if (bad + 0 > 0) exit 1
	}
' "$work" "$work"
