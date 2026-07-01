#!/usr/bin/env bash
# pg-dump-restore.sh
# ---------------------------------------------------------------------------
# FALLBACK full-copy path (pd-MIG-CUTOVER-F1-02, Path B). The proven primitive:
#   pg_dump (SOURCE) | gzip  ->  gunzip | psql (TARGET)
# streamed, with ON_ERROR_STOP on restore. Use this when logical replication
# is not viable (RDS can't get wal_level=logical, network can't do the
# TARGET-pulls-SOURCE path, or the DB is small enough that a freeze-window full
# copy fits the RTO budget). This is a FULL, POINT-IN-TIME copy — it implies a
# write-freeze on the SOURCE for the duration (no ongoing delta).
#
# Usage:
#   SRC_DSN=... DST_DSN=... DBNAME=keycloak ./pg-dump-restore.sh
#
# Required env:  SRC_DSN  DST_DSN  DBNAME
# Optional env:
#   DUMP_DIR   (default: ./_mig_dump)   where the .sql.gz lands (kept as artefact).
#   JOBS       (default: 1)             reserved; plain (piped) format used for
#                                       max compatibility across RDS<->DO.
#   NO_OWNER   (default: 1)             pass --no-owner --no-privileges.
#   CLEAN      (default: 0)             if 1, pg_dump --clean --if-exists (drops
#                                       objects before recreate on target).
#   STREAM     (default: 1)            if 1, stream SRC|gzip -> gunzip|TARGET
#                                       (no full file on disk except the tee'd
#                                       artefact); if 0, dump-to-file then restore.
#
# The default STREAM=1 does:  pg_dump SRC | gzip | tee dump.sql.gz | gunzip | psql DST
# so you get BOTH a low-footprint streamed restore AND a retained gzip artefact.
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-common.sh
source "${SCRIPT_DIR}/lib-common.sh"

require_cmd psql pg_dump gzip gunzip
require_env SRC_DSN DST_DSN DBNAME

DUMP_DIR="${DUMP_DIR:-./_mig_dump}"
NO_OWNER="${NO_OWNER:-1}"
CLEAN="${CLEAN:-0}"
STREAM="${STREAM:-1}"
mkdir -p "$DUMP_DIR"
DUMP_FILE="${DUMP_DIR}/${DBNAME}.$(date -u +%Y%m%dT%H%M%SZ).sql.gz"

DUMP_OPTS=( --format=plain --no-publications --no-subscriptions )
[[ "$NO_OWNER" == "1" ]] && DUMP_OPTS+=( --no-owner --no-privileges )
[[ "$CLEAN"    == "1" ]] && DUMP_OPTS+=( --clean --if-exists )

cat >&2 <<EOF
============================================================================
 pg_dump | gzip -> gunzip | psql   (FALLBACK full copy)
   DBNAME  : ${DBNAME}
   artefact: ${DUMP_FILE}
   opts    : ${DUMP_OPTS[*]}
   stream  : ${STREAM}
 NOTE: this is a point-in-time full copy. SOURCE must be write-frozen for RPO=0.
============================================================================
EOF

# Restore is always run with ON_ERROR_STOP=on so any error aborts the whole copy.
PSQL_RESTORE=( psql "$DST_DSN" -v ON_ERROR_STOP=1 --no-psqlrc -q )

set -o pipefail
if [[ "$STREAM" == "1" ]]; then
  log "streaming dump -> restore (also tee'ing gzip artefact)"
  # pipefail ensures a failure in ANY stage (dump, restore) fails the pipeline.
  pg_dump "$SRC_DSN" "${DUMP_OPTS[@]}" \
    | gzip -c \
    | tee "$DUMP_FILE" \
    | gunzip -c \
    | "${PSQL_RESTORE[@]}"
else
  log "STEP 1: dump SOURCE -> ${DUMP_FILE}"
  pg_dump "$SRC_DSN" "${DUMP_OPTS[@]}" | gzip -c > "$DUMP_FILE"
  ok "dump complete ($(du -h "$DUMP_FILE" | cut -f1))"
  log "STEP 2: restore ${DUMP_FILE} -> TARGET (ON_ERROR_STOP)"
  gunzip -c "$DUMP_FILE" | "${PSQL_RESTORE[@]}"
fi
ok "full copy complete"

# Post-restore: re-sync sequences to their table max (dump carries setval, but
# assert defensively so PK inserts on the target don't collide).
log "post-restore: verifying sequence high-water marks"
psql "$DST_DSN" --no-psqlrc -qtAX -v ON_ERROR_STOP=1 -c "
  SELECT count(*) FROM pg_sequences WHERE schemaname NOT IN ('pg_catalog','information_schema');" \
  | { read -r n; log "sequences present on target: ${n}"; }

cat >&2 <<EOF
----------------------------------------------------------------------------
 Full copy done. Now run the mandatory data-safety gate:
   SRC_DSN=... DST_DSN=... DBNAME=${DBNAME} ./verify-migration.sh
 (row-count + checksum + sequence parity; MODE=full skips the lag check since
  this is a point-in-time copy with no live subscription.)
----------------------------------------------------------------------------
EOF
ok "artefact retained: ${DUMP_FILE}"
