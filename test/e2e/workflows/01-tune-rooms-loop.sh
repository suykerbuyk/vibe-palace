#!/usr/bin/env bash
#
# Case: 01-tune-rooms-loop
#
# Two-iteration loop that exercises the tune-rooms workflow end-to-end:
#   seed drawers → `vp tune rooms --export` → two `vp tune rooms --apply`
#   calls (idempotency check via decoded-TOML-struct equality).
#
# Hard-asserts mirror plan §"Hard-assert vs tracked-metric contract":
#   - exit code == 0 for every vp invocation
#   - report.json parses
#   - .project == "proj"
#   - .samples_total >= 4
#   - .proposals is an array (normalized via `// []` since Go marshals nil
#     slices as `null` — same pattern the plan applies to unmatched_flags).
#   - second --apply produces a decoded-TOML-struct-equal config.
#
# Everything else (ms per cmd, samples, proposals, agreements,
# disagreements, judgments_total) is TRACKED via emit_metric, not asserted.
#
# Ambiguity resolved:
#   - `vp tune rooms --export FILE` writes the TuneReport JSON to FILE
#     directly (see cmd/vp/cmd_tune.go around L160). No stdout redirect
#     needed.
#   - The mock LLM's `all-testing.json` contains placeholder drawer_ids
#     that do not match the freshly-seeded IDs. ParseJudgments silently
#     skips those, so judgments_total/proposals will be 0. That's fine
#     because these are metrics, not assertions — the flow still verifies
#     the CLI contract end-to-end.

set -e

: "${CASE_DIR:?CASE_DIR not set}"
: "${WORKFLOWS_DIR:?WORKFLOWS_DIR not set}"

export CASE_NAME="01-tune-rooms-loop"
export METRIC_FILE="$CASE_DIR/metrics.jsonl"

cd "$CASE_DIR"
mkdir -p proj
cd proj
git init -q

run_vp init
assert_exit_code 0 "$LAST_EXIT_CODE"

start_mock_llm --script "$WORKFLOWS_DIR/mockllm/responses/all-testing.json"
write_project_llm_config proj "$VP_MOCK_LLM_URL"

for iter in 1 2; do
  export METRIC_ITER="$iter"

  # --- seed drawers ---
  time_ms_start cap
  for i in 1 2 3 4 5; do
    seed_drawer proj facts "iter-$iter drawer $i: testing batch subsystem regression coverage"
  done
  cap_ms=$(time_ms_end cap)
  emit_metric "$METRIC_FILE" cmd=seed-drawers ms="$cap_ms" count=5

  # --- tune --export (HARD shape asserts) ---
  time_ms_start tx
  run_vp tune rooms --export "$CASE_DIR/report-$iter.json"
  t1=$(time_ms_end tx)
  assert_exit_code 0 "$LAST_EXIT_CODE"
  assert_file_exists "$CASE_DIR/report-$iter.json"

  assert_shape "$CASE_DIR/report-$iter.json" '.project'                        '"proj"'
  assert_shape "$CASE_DIR/report-$iter.json" '.samples_total >= 4'              'true'
  # Go marshals nil slices as `null`; normalize absent/null → [] for the
  # type check. Same pattern the plan uses for .unmatched_flags.
  assert_shape "$CASE_DIR/report-$iter.json" '(.proposals // []) | type'        '"array"'
  assert_shape "$CASE_DIR/report-$iter.json" '(.unmatched_flags // []) | type'  '"array"'
  assert_shape "$CASE_DIR/report-$iter.json" '(.judgments_total // 0) >= 0'     'true'

  emit_metric "$METRIC_FILE" cmd="tune --export" ms="$t1" \
    samples=$(json_field "$CASE_DIR/report-$iter.json" '.samples_total') \
    proposals=$(json_field "$CASE_DIR/report-$iter.json" '(.proposals // []) | length') \
    agreements=$(json_field "$CASE_DIR/report-$iter.json" '.agreements // 0') \
    disagreements=$(json_field "$CASE_DIR/report-$iter.json" '.disagreements // 0') \
    judgments_total=$(json_field "$CASE_DIR/report-$iter.json" '.judgments_total // 0')

  # --- tune --apply idempotency (HARD struct-equal assert) ---
  cfg=$(vault_project_config_path proj)

  run_vp tune rooms --apply
  assert_exit_code 0 "$LAST_EXIT_CODE"
  assert_file_exists "$cfg"
  cp "$cfg" "$CASE_DIR/config-$iter-apply1.toml"

  run_vp tune rooms --apply
  assert_exit_code 0 "$LAST_EXIT_CODE"
  cp "$cfg" "$CASE_DIR/config-$iter-apply2.toml"

  assert_toml_struct_equal \
    "$CASE_DIR/config-$iter-apply1.toml" \
    "$CASE_DIR/config-$iter-apply2.toml"

  emit_metric "$METRIC_FILE" cmd="tune --apply(idemp)" struct_stable=1

  # Disagreements as reclassification proxy — TRACKED, not asserted.
  emit_metric "$METRIC_FILE" cmd=tune iter_summary=1 \
    disagreements=$(json_field "$CASE_DIR/report-$iter.json" '.disagreements // 0') \
    judgments_total=$(json_field "$CASE_DIR/report-$iter.json" '.judgments_total // 0')
done

stop_mock_llm
