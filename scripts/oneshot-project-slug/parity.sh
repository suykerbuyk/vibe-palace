#!/usr/bin/env bash
# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
#
# ONE-SHOT search parity (C7) for `vp migrate project-slug` (task
# migrate-quantum-ng-vault-history-to-qa-metabuild-system, section "Drawer
# IDs, embed cache and search parity"). Deleted with the rest of
# scripts/oneshot-project-slug/.
#
# Modes:
#   parity.sh --baseline --vault V --counts counts.json --out b1.json
#       Pre-migration (rehearsal H6): B1 twice (-p quantum-ng -n 10), B2 once
#       (-p qa-metabuild-system -n 20). Records the stray rows' content hashes
#       and the exact stray re-embed the A run must create. Prints BASELINE
#       PASS only when the floors hold and the two B1 runs are identical.
#   parity.sh --vault V --b1 b1.json [--runs N] [--out a.json]
#       Post-migration (rehearsal H7: --runs 1; live RB10: --runs 2): run A
#       (-p qa-metabuild-system -n 20) N times, sequentially. Run 1 must
#       create exactly the recorded stray vectors, later runs none, all runs
#       must agree, and run 1 must match B1 by the plan's tie-group rule.
#       PARITY PASS, or PARITY FAIL <check>.
#
# Searches run one at a time; no `vp mcp` is started. The only writes are the
# vectors vp search itself adds under the gitignored palace/.local/.
# VP names the binary (default: vp). Exit 0 PASS, 1 FAIL, 2 usage or run error.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
helper="$here/slugcheck.py"
export VP="${VP:-vp}"

usage() { sed -n '8,24p' "${BASH_SOURCE[0]}" >&2; exit 2; }

baseline=0 vault="" counts="" out="" b1="" runs=1
while [ $# -gt 0 ]; do
  case "$1" in
    --baseline) baseline=1 ;;
    --vault) vault=${2:-}; shift ;;
    --counts) counts=${2:-}; shift ;;
    --out) out=${2:-}; shift ;;
    --b1) b1=${2:-}; shift ;;
    --runs) runs=${2:-}; shift ;;
    -h|--help) usage ;;
    *) echo "parity.sh: unknown argument $1" >&2; usage ;;
  esac
  shift
done
[ -n "$vault" ] || usage

# `vp search` has no --vault: it is pinned by XDG_CONFIG_HOME alone, so the
# vault vp resolves MUST be the one named here, or this would search (and
# embed into) another vault entirely — the live one, by default.
python3 "$helper" pin --vault "$vault" || exit 2

# Plan C7: no `vp mcp` alive during a run against the LIVE vault. A copy
# (no remote, not the configured vault) is exempt, as M3 exempts it: the
# other hosts' agents never point at a copy.
if python3 "$helper" is-live --vault "$vault" >/dev/null && pgrep -a -x vp | grep -q ' mcp'; then
  echo "parity.sh: a 'vp mcp' is running against a LIVE vault; parity runs with none alive (plan C7)" >&2
  exit 2
fi

if [ "$baseline" = 1 ]; then
  [ -n "$counts" ] && [ -n "$out" ] || usage
  exec python3 "$helper" parity-baseline --vault "$vault" --counts "$counts" --out "$out"
fi
[ -n "$b1" ] || usage
case "$runs" in ''|*[!0-9]*|0) echo "parity.sh: --runs must be a positive integer" >&2; exit 2 ;; esac
args=(parity-check --vault "$vault" --b1 "$b1" --runs "$runs")
[ -z "$out" ] || args+=(--out "$out")
exec python3 "$helper" "${args[@]}"
