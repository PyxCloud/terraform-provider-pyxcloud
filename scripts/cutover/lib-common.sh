#!/usr/bin/env bash
# lib-common.sh — shared helpers for the operator-driven DB migration tooling.
# Sourced by the pg-*/verify-migration/cutover scripts. Not executed directly.
#
# pd-MIG-CUTOVER-F1-02 (Path B — operator-driven).
# These scripts are the LOW-LEVEL primitive behind docs/cutover/RUNBOOK.md §3/§4.
# They NEVER touch prod on their own — the operator supplies DSNs explicitly.

set -euo pipefail

# ---- logging -----------------------------------------------------------------
_ts()   { date -u +%Y-%m-%dT%H:%M:%SZ; }
log()   { printf '[%s] %s\n'      "$(_ts)" "$*" >&2; }
ok()    { printf '[%s] OK   %s\n' "$(_ts)" "$*" >&2; }
warn()  { printf '[%s] WARN %s\n' "$(_ts)" "$*" >&2; }
die()   { printf '[%s] FAIL %s\n' "$(_ts)" "$*" >&2; exit 1; }

# ---- requirements ------------------------------------------------------------
require_cmd() {
  local c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || die "required command not found: $c"
  done
}

require_env() {
  local v
  for v in "$@"; do
    if [[ -z "${!v:-}" ]]; then
      die "required env var not set: $v"
    fi
  done
}

# ---- psql wrappers (ON_ERROR_STOP everywhere) --------------------------------
# psql_run <DSN> <SQL...>  — run SQL, fail hard on any error.
psql_run() {
  local dsn="$1"; shift
  psql "$dsn" -v ON_ERROR_STOP=1 --no-psqlrc -q -c "$*"
}

# psql_val <DSN> <SQL>  — return a single scalar value, trimmed.
psql_val() {
  local dsn="$1"; shift
  psql "$dsn" -v ON_ERROR_STOP=1 --no-psqlrc -qtAX -c "$*"
}

# psql_file <DSN> <FILE>  — run a SQL file, fail hard.
psql_file() {
  local dsn="$1" file="$2"
  psql "$dsn" -v ON_ERROR_STOP=1 --no-psqlrc -q -f "$file"
}

# list_user_tables <DSN> [DBNAME-ignored] — schema-qualified user tables, sorted.
# Excludes system schemas and (importantly) partitioned-table parents' rows are
# de-duplicated by relkind so counts are exact.
list_user_tables() {
  local dsn="$1"
  psql "$dsn" -v ON_ERROR_STOP=1 --no-psqlrc -qtAX -c "
    SELECT n.nspname || '.' || c.relname
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relkind IN ('r','p')            -- ordinary + partitioned tables
      AND n.nspname NOT IN ('pg_catalog','information_schema')
      AND n.nspname NOT LIKE 'pg_toast%'
      AND c.relispartition = false          -- count partition parents once
    ORDER BY 1;"
}

# primary_key_cols <DSN> <schema.table> — comma-sep PK columns, or empty.
primary_key_cols() {
  local dsn="$1" tbl="$2"
  psql "$dsn" -v ON_ERROR_STOP=1 --no-psqlrc -qtAX -c "
    SELECT string_agg(a.attname, ',' ORDER BY array_position(i.indkey, a.attnum))
    FROM pg_index i
    JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
    WHERE i.indrelid = '$tbl'::regclass AND i.indisprimary;"
}
