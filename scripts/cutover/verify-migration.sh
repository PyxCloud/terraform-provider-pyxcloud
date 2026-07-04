#!/usr/bin/env bash
# verify-migration.sh
# ---------------------------------------------------------------------------
# THE DATA-SAFETY GATE (pd-MIG-CUTOVER-F1-02, Path B).
# Implements docs/cutover/RUNBOOK.md §4 as an executable, exit-code'd check.
# This is the MANDATORY pre-cutover gate. It exits NON-ZERO on ANY mismatch —
# a non-zero exit MUST abort the cutover (RUNBOOK F4 step 3).
#
# For each user table it verifies SOURCE (AWS) vs TARGET (DO):
#   1. per-table row-count parity        (COUNT(*) identical, zero tolerance)
#   2. per-table content checksum parity md5(string_agg(row::text ORDER BY pk))
#      (falls back to ORDER BY the whole row when a table has no PK)
#   3. sequence high-water marks match   (avoid post-promote PK collisions)
#   4. replication lag (MODE=lag/auto)   subscription state 'r' + lag bytes = 0
#
# Usage:
#   SRC_DSN=... DST_DSN=... DBNAME=keycloak [MODE=auto] ./verify-migration.sh
#
# Required env: SRC_DSN  DST_DSN  DBNAME
# Optional env:
#   MODE   auto (default) | full | lag
#          auto: run counts+checksums+sequences, AND lag if a subscription for
#                DBNAME exists on the target.
#          full: counts+checksums+sequences only (for pg_dump point-in-time copy).
#          lag : only the replication-lag check (fast, for the freeze drain loop).
#   SUBNAME       (default: pyx_mig_sub_<DBNAME>) subscription to check lag on.
#   TABLES        (optional) space/comma list of schema.table to restrict to
#                 (e.g. the critical Keycloak tables). Default: all user tables.
#   SKIP_CHECKSUM (default: 0) if 1, skip checksums (counts+seq+lag only) — for a
#                 fast pre-check on huge tables; NEVER skip for the final gate.
#   MAX_LAG_BYTES (default: 0) allowed replication lag for the lag check to pass.
#
# EXIT: 0 = all parity checks GREEN (safe to proceed). Non-zero = MISMATCH/abort.
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-common.sh
source "${SCRIPT_DIR}/lib-common.sh"

require_cmd psql
require_env SRC_DSN DST_DSN DBNAME

MODE="${MODE:-auto}"
SUBNAME="${SUBNAME:-pyx_mig_sub_${DBNAME}}"
SKIP_CHECKSUM="${SKIP_CHECKSUM:-0}"
MAX_LAG_BYTES="${MAX_LAG_BYTES:-0}"
FAILURES=0

fail() { warn "MISMATCH: $*"; FAILURES=$((FAILURES+1)); }

# ---- table checksum -----------------------------------------------------------
# Deterministic, order-stable content hash of a whole table. We hash per-row
# (md5(row::text)) then combine order-independently by XOR-free string_agg over
# an ORDER BY that is stable: PK if present, else the full row text. Using an
# ordered string_agg of per-row md5s keeps memory bounded vs hashing the whole
# concatenation, and is identical across two servers with the same rows.
table_checksum() {
  local dsn="$1" tbl="$2" pk="$3" ordexpr
  if [[ -n "$pk" ]]; then
    # composite-safe: build a row-constructor text of the PK cols as the sort key.
    ordexpr="ROW(${pk})::text"
  else
    # no PK: sort by the row's own md5 for a deterministic, stable order.
    ordexpr="md5(t::text)"
  fi
  psql "$dsn" -v ON_ERROR_STOP=1 --no-psqlrc -qtAX -c "
    SELECT COALESCE(md5(string_agg(row_md5, ',' ORDER BY ord)), 'EMPTY')
    FROM (
      SELECT md5(t::text) AS row_md5, ${ordexpr} AS ord
      FROM ${tbl} t
    ) s;"
}

table_count() { psql_val "$1" "SELECT count(*) FROM $2;"; }

# ---- resolve table list -------------------------------------------------------
declare -a TBL_LIST
if [[ -n "${TABLES:-}" ]]; then
  IFS=', ' read -r -a TBL_LIST <<< "$TABLES"
else
  mapfile -t TBL_LIST < <(list_user_tables "$SRC_DSN")
fi

# =============================================================================
# LAG-ONLY mode (fast path for the freeze drain loop).
# =============================================================================
check_lag() {
  local sub_oid enabled unsynced lag_bytes
  sub_oid="$(psql_val "$DST_DSN" "SELECT oid FROM pg_subscription WHERE subname='${SUBNAME}';")"
  if [[ -z "$sub_oid" ]]; then
    warn "no subscription '${SUBNAME}' on target — lag check N/A (point-in-time copy?)."
    return 0
  fi
  enabled="$(psql_val "$DST_DSN" "SELECT subenabled FROM pg_subscription WHERE subname='${SUBNAME}';")"
  # tables not yet in 'r' (ready/streaming) state
  unsynced="$(psql_val "$DST_DSN" "
    SELECT count(*) FROM pg_subscription_rel r
    JOIN pg_subscription s ON s.oid=r.srsubid
    WHERE s.subname='${SUBNAME}' AND r.srsubstate <> 'r';")"
  log "subscription ${SUBNAME}: enabled=${enabled} not-ready-tables=${unsynced}"
  [[ "$unsynced" == "0" ]] || fail "subscription has ${unsynced} table(s) not in 'ready' state (initial sync incomplete)"

  # Lag from the SOURCE side: bytes between current WAL and the slot's confirmed
  # flush. This is the authoritative "un-applied WAL" measure.
  lag_bytes="$(psql_val "$SRC_DSN" "
    SELECT COALESCE(max(pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn)),0)
    FROM pg_replication_slots
    WHERE slot_name LIKE '%${DBNAME}%' OR slot_name='${SUBNAME}';")"
  lag_bytes="${lag_bytes:-0}"
  log "replication lag (source WAL vs slot confirmed_flush): ${lag_bytes} bytes"
  if [[ "${lag_bytes}" -gt "${MAX_LAG_BYTES}" ]]; then
    fail "replication lag ${lag_bytes}B > allowed ${MAX_LAG_BYTES}B"
  else
    ok "replication lag within tolerance (${lag_bytes}B <= ${MAX_LAG_BYTES}B)"
  fi
}

if [[ "$MODE" == "lag" ]]; then
  check_lag
  if [[ "$FAILURES" -gt 0 ]]; then die "LAG GATE RED (${FAILURES} issue(s)) — DO NOT CUT OVER"; fi
  ok "LAG GATE GREEN"; exit 0
fi

# =============================================================================
# FULL parity: counts + checksums + sequences (+ lag in auto mode).
# =============================================================================
log "verifying ${#TBL_LIST[@]} table(s) for DB '${DBNAME}' (MODE=${MODE})"
printf '%-45s %12s %12s  %-8s %s\n' "TABLE" "SRC_ROWS" "DST_ROWS" "COUNT" "CHECKSUM" >&2

for tbl in "${TBL_LIST[@]}"; do
  [[ -z "$tbl" ]] && continue
  sc="$(table_count "$SRC_DSN" "$tbl")"
  dc="$(table_count "$DST_DSN" "$tbl")"
  count_res="OK"; sum_res="-"
  [[ "$sc" == "$dc" ]] || { count_res="FAIL"; fail "row-count ${tbl}: SRC=${sc} DST=${dc}"; }

  if [[ "$SKIP_CHECKSUM" != "1" ]]; then
    pk="$(primary_key_cols "$SRC_DSN" "$tbl")"
    ssum="$(table_checksum "$SRC_DSN" "$tbl" "$pk")"
    dsum="$(table_checksum "$DST_DSN" "$tbl" "$pk")"
    if [[ "$ssum" == "$dsum" ]]; then sum_res="OK"; else sum_res="FAIL"; fail "checksum ${tbl}: SRC=${ssum} DST=${dsum}"; fi
  fi
  printf '%-45s %12s %12s  %-8s %s\n' "$tbl" "$sc" "$dc" "$count_res" "$sum_res" >&2
done

# ---- sequences ----------------------------------------------------------------
log "verifying sequence high-water marks"
mapfile -t SEQS < <(psql "$SRC_DSN" -v ON_ERROR_STOP=1 --no-psqlrc -qtAX -c "
  SELECT schemaname||'.'||sequencename FROM pg_sequences
  WHERE schemaname NOT IN ('pg_catalog','information_schema') ORDER BY 1;")
for seq in "${SEQS[@]}"; do
  [[ -z "$seq" ]] && continue
  sv="$(psql_val "$SRC_DSN" "SELECT last_value FROM ${seq};")"
  dv="$(psql_val "$DST_DSN" "SELECT last_value FROM ${seq};")"
  if [[ "$sv" == "$dv" ]]; then
    :
  else
    # For live replication the target sequence may legitimately be >= source
    # (advanced by applied inserts). Only a target < source is dangerous
    # (would reuse a value already used on source). Flag anything not equal
    # but distinguish the dangerous case.
    if [[ "${dv:-0}" -lt "${sv:-0}" ]]; then
      fail "sequence ${seq}: TARGET(${dv}) < SOURCE(${sv}) — PK-collision risk"
    else
      warn "sequence ${seq}: SRC=${sv} DST=${dv} (target ahead — acceptable for live replication)"
    fi
  fi
done

# ---- extensions / roles drift (advisory) --------------------------------------
log "checking extension parity (advisory)"
sext="$(psql_val "$SRC_DSN" "SELECT string_agg(extname,',' ORDER BY extname) FROM pg_extension;")"
dext="$(psql_val "$DST_DSN" "SELECT string_agg(extname,',' ORDER BY extname) FROM pg_extension;")"
[[ "$sext" == "$dext" ]] || warn "extension set differs: SRC=[${sext}] DST=[${dext}] (verify none are load-bearing)"

# ---- lag (auto) ---------------------------------------------------------------
if [[ "$MODE" == "auto" ]]; then
  check_lag
fi

# =============================================================================
echo >&2
if [[ "$FAILURES" -gt 0 ]]; then
  die "DATA-SAFETY GATE **RED**: ${FAILURES} mismatch(es). DO NOT PROMOTE / DO NOT CUT OVER (RUNBOOK §4)."
fi
ok "DATA-SAFETY GATE **GREEN**: all row-count + checksum + sequence checks passed."
exit 0
