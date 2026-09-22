#!/usr/bin/env bash
# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
#
# ONE-SHOT verification census for `vp migrate project-slug` (task
# migrate-quantum-ng-vault-history-to-qa-metabuild-system, section
# "Verification census"). Deleted with the rest of scripts/oneshot-project-slug/.
#
# Modes (exactly the invocations the plan's runbook uses):
#   census.sh --cache-warm-check --vault V [--snapshot FILE]
#       CACHE WARM when every drawer of both slugs has a vector AND a search of
#       each slug embeds nothing new; else CACHE COLD <n>. --snapshot writes
#       "slug<TAB>name<TAB>size<TAB>sha256" per vector (only when WARM).
#   census.sh --profile live|rehearsal --phase B --vault V --out FILE
#       Measure census B (read-only; runs one `vp mcp` at a time).
#   census.sh --profile live|rehearsal --phase A --vault V --b B.json --counts counts.json --out FILE
#       Measure census A, then verify it against B: CENSUS PASS or CENSUS FAIL <check>.
#   census.sh --compare FIRST.json SECOND.json
#       SAME, or DIFF <key> per differing top-level key.
#   census.sh --cache-report APPLY.LOG --pre PRE.list --stray STRAY.list --vault V
#       CACHE-REPORT PASS when the cache step's report equals the pre snapshot.
#
# The vault named by --vault must be the one vp resolves (XDG_CONFIG_HOME);
# the script refuses when they differ. VP names the binary (default: vp).
# Exit 0 on PASS/WARM/SAME, 1 on a failed check, 2 on a usage or run error.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
helper="$here/slugcheck.py"
export VP="${VP:-vp}"

usage() { sed -n '6,24p' "${BASH_SOURCE[0]}" >&2; exit 2; }

mode="" vault="" out="" profile="" phase="" b="" counts="" snapshot="" pre="" stray="" log="" first="" second=""
while [ $# -gt 0 ]; do
  case "$1" in
    --cache-warm-check) mode=warm ;;
    --compare) mode=compare; first=${2:-}; second=${3:-}; shift 2 ;;
    --cache-report) mode=report; log=${2:-}; shift ;;
    --profile) profile=${2:-}; shift ;;
    --phase) phase=${2:-}; shift ;;
    --vault) vault=${2:-}; shift ;;
    --out) out=${2:-}; shift ;;
    --b) b=${2:-}; shift ;;
    --counts) counts=${2:-}; shift ;;
    --snapshot) snapshot=${2:-}; shift ;;
    --pre) pre=${2:-}; shift ;;
    --stray) stray=${2:-}; shift ;;
    -h|--help) usage ;;
    *) echo "census.sh: unknown argument $1" >&2; usage ;;
  esac
  shift
done
[ -n "$mode" ] || { [ -n "$profile" ] && mode=measure; } || usage

# The vault vp resolves must be the one being measured, or MCP facts and
# searches would describe a different vault than the file counts.
same_vault() {
  local resolved
  resolved=$(python3 - "$vault" <<'EOF'
import os, sys, tomllib
cfg = os.path.join(os.environ.get("XDG_CONFIG_HOME") or os.path.expanduser("~/.config"), "vibe-palace", "config.toml")
with open(cfg, "rb") as f:
    vp = os.path.expanduser(tomllib.load(f).get("vault_path", ""))
print("same" if vp and os.path.samefile(vp, sys.argv[1]) else "differs: " + vp)
EOF
)
  [ "$resolved" = same ] || { echo "census.sh: --vault $vault is not the vault vp resolves ($resolved); set XDG_CONFIG_HOME" >&2; exit 2; }
}

case "$mode" in
  warm)
    [ -n "$vault" ] || usage
    same_vault
    args=(cache-warm --vault "$vault")
    [ -z "$snapshot" ] || args+=(--snapshot "$snapshot")
    exec python3 "$helper" "${args[@]}"
    ;;
  compare)
    [ -n "$first" ] && [ -n "$second" ] || usage
    exec python3 "$helper" compare "$first" "$second"
    ;;
  report)
    [ -n "$log" ] && [ -n "$pre" ] && [ -n "$stray" ] && [ -n "$vault" ] || usage
    exec python3 "$helper" cache-report --vault "$vault" --log "$log" --pre "$pre" --stray "$stray"
    ;;
  measure)
    [ -n "$vault" ] && [ -n "$out" ] && [ -n "$phase" ] || usage
    same_vault
    python3 "$helper" measure --vault "$vault" --profile "$profile" --phase "$phase" --out "$out"
    if [ "$phase" = A ]; then
      [ -n "$b" ] && [ -n "$counts" ] || { echo "census.sh: --phase A needs --b and --counts" >&2; exit 2; }
      exec python3 "$helper" verify --b "$b" --a "$out" --counts "$counts"
    fi
    echo "CENSUS B written: $out"
    ;;
  *) usage ;;
esac
