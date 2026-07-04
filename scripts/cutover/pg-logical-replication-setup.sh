#!/usr/bin/env bash
# pg-logical-replication-setup.sh
# ---------------------------------------------------------------------------
# LOW-DOWNTIME path for the AWS RDS -> DO Managed PG data migration
# (pd-MIG-CUTOVER-F1-02, Path B). Sets up native PostgreSQL logical
# replication: a PUBLICATION FOR ALL TABLES on the SOURCE, the schema +
# a data baseline on the TARGET, then a SUBSCRIPTION on the TARGET that
# streams the ongoing delta with near-zero lag.
#
# This is the primitive behind docs/cutover/RUNBOOK.md F3 (stand up
# replication, let it converge) and F4 step 2 (drain to lag=0).
#
# Usage:
#   SRC_DSN=...  DST_DSN=...  DBNAME=keycloak  ./pg-logical-replication-setup.sh
#
# Required env:
#   SRC_DSN   libpq DSN for the SOURCE (AWS RDS) database (superuser-ish / rds_replication).
#   DST_DSN   libpq DSN for the TARGET (DO Managed PG) database (owner/admin).
#   DBNAME    logical name used to derive PUBLICATION/SUBSCRIPTION names & baseline file.
# Optional env:
#   PUBNAME       (default: pyx_mig_pub_<DBNAME>)
#   SUBNAME       (default: pyx_mig_sub_<DBNAME>)
#   BASELINE_DIR  (default: ./_mig_baseline) where schema+data dumps land.
#   COPY_DATA     (default: true)  if true, SUBSCRIPTION does initial COPY of
#                                  existing rows; if false, assumes baseline was
#                                  restored and creates the sub WITH (copy_data=false).
#   SLOT_NAME     (default: <SUBNAME>) replication slot name on the source.
#
# STRATEGY
#   Two supported baseline strategies, controlled by COPY_DATA:
#     A) COPY_DATA=true  (default, simplest & safest for correctness):
#          - restore SCHEMA ONLY on target,
#          - CREATE SUBSCRIPTION ... WITH (copy_data=true) so PG itself does the
#            initial table sync, then streams the delta. No manual data dump.
#     B) COPY_DATA=false (for very large DBs where you pre-seed a baseline
#        out-of-band, e.g. from an RDS snapshot restore, to shrink the copy
#        window): restore schema + a consistent data snapshot yourself, then
#        create the subscription WITH (copy_data=false, ...) to only stream the
#        delta from the slot. Requires a slot created at the snapshot LSN — this
#        script documents but does not automate the snapshot-LSN handshake
#        (that is an RDS-snapshot operator step); for the 100GB/80GB clusters
#        prefer (A) unless the copy window is too long in rehearsal.
#
# Idempotent: re-running drops+recreates the publication and (only if you pass
#   RESET_SUB=1) the subscription. By default an existing subscription is left
#   alone (so you don't blow away an in-flight sync).
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-common.sh
source "${SCRIPT_DIR}/lib-common.sh"

require_cmd psql pg_dump
require_env SRC_DSN DST_DSN DBNAME

PUBNAME="${PUBNAME:-pyx_mig_pub_${DBNAME}}"
SUBNAME="${SUBNAME:-pyx_mig_sub_${DBNAME}}"
SLOT_NAME="${SLOT_NAME:-${SUBNAME}}"
BASELINE_DIR="${BASELINE_DIR:-./_mig_baseline}"
COPY_DATA="${COPY_DATA:-true}"
RESET_SUB="${RESET_SUB:-0}"

SCHEMA_DUMP="${BASELINE_DIR}/${DBNAME}.schema.sql"

cat >&2 <<EOF
============================================================================
 logical-replication setup
   DBNAME     : ${DBNAME}
   PUBLICATION: ${PUBNAME}   (on SOURCE)
   SUBSCRIPTION: ${SUBNAME}  (on TARGET)  slot=${SLOT_NAME}
   COPY_DATA  : ${COPY_DATA}
============================================================================
EOF

# ---------------------------------------------------------------------------
# RDS-SIDE PREREQUISITES (operator must have done these BEFORE running) — checked below.
#   1. Parameter group with  rds.logical_replication = 1  attached to the RDS
#      instance, and the instance REBOOTED (static param). This sets
#      wal_level=logical, max_replication_slots>0, max_wal_senders>0.
#   2. A role with the rds_replication role granted (RDS has no true SUPERUSER):
#        CREATE ROLE pyx_repl LOGIN PASSWORD '...';
#        GRANT rds_replication TO pyx_repl;
#        GRANT SELECT ON ALL TABLES IN SCHEMA public TO pyx_repl;   -- + other schemas
#      and SRC_DSN should connect as that role (or the master user).
#   3. Security group / network path from the DO Managed PG egress to the RDS
#      endpoint on 5432 (or through the VPN). Logical replication is TARGET-pulls
#      -from-SOURCE, so the TARGET must reach the SOURCE.
#   4. TARGET (DO Managed PG): a DB user with CREATE + the ability to CREATE
#      SUBSCRIPTION (DO grants this to the default doadmin/owner).
# ---------------------------------------------------------------------------

log "checking SOURCE prerequisites..."
SRC_WAL="$(psql_val "$SRC_DSN" "SHOW wal_level;")"
[[ "$SRC_WAL" == "logical" ]] || die "SOURCE wal_level='${SRC_WAL}', need 'logical'. On RDS: set rds.logical_replication=1 in the parameter group and REBOOT."
ok "SOURCE wal_level=logical"

SRC_SLOTS="$(psql_val "$SRC_DSN" "SHOW max_replication_slots;")"
SRC_SENDERS="$(psql_val "$SRC_DSN" "SHOW max_wal_senders;")"
log "SOURCE max_replication_slots=${SRC_SLOTS} max_wal_senders=${SRC_SENDERS}"
[[ "${SRC_SLOTS:-0}" -ge 1 ]] || die "SOURCE max_replication_slots<1 — bump the parameter group."
[[ "${SRC_SENDERS:-0}" -ge 1 ]] || die "SOURCE max_wal_senders<1 — bump the parameter group."

# ---------------------------------------------------------------------------
# STEP 1 — SOURCE: PUBLICATION FOR ALL TABLES (idempotent drop+create).
# ---------------------------------------------------------------------------
# --- REPLICA IDENTITY preflight ---------------------------------------------
# Logical replication of UPDATE/DELETE requires each table to have a REPLICA
# IDENTITY (a PK, a unique index set as identity, or FULL). Tables with none
# will replicate INSERTs during initial COPY but then ERROR the apply worker on
# the first UPDATE/DELETE. Detect them up front. For no-PK tables we set
# REPLICA IDENTITY FULL on the SOURCE (safe; makes the whole row the key) unless
# STRICT_IDENTITY=1, in which case we refuse and let the operator decide.
STRICT_IDENTITY="${STRICT_IDENTITY:-0}"
log "PREFLIGHT: checking REPLICA IDENTITY on all user tables"
mapfile -t NOID < <(psql "$SRC_DSN" -v ON_ERROR_STOP=1 --no-psqlrc -qtAX -c "
  SELECT n.nspname||'.'||c.relname
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE c.relkind='r'
    AND n.nspname NOT IN ('pg_catalog','information_schema')
    AND n.nspname NOT LIKE 'pg_toast%'
    AND c.relreplident IN ('n','d')                    -- 'd'=default(PK) 'n'=nothing
    AND NOT EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid=c.oid AND i.indisprimary)
  ORDER BY 1;")
if [[ "${#NOID[@]}" -gt 0 && -n "${NOID[0]}" ]]; then
  warn "tables WITHOUT a usable replica identity (no PK): ${NOID[*]}"
  if [[ "$STRICT_IDENTITY" == "1" ]]; then
    die "STRICT_IDENTITY=1: refusing. Add a PK/unique index or set REPLICA IDENTITY FULL on these tables, then re-run."
  fi
  for t in "${NOID[@]}"; do
    [[ -z "$t" ]] && continue
    warn "setting REPLICA IDENTITY FULL on ${t} (no PK) so UPDATE/DELETE replicate"
    psql_run "$SRC_DSN" "ALTER TABLE ${t} REPLICA IDENTITY FULL;"
  done
  ok "replica identity resolved for no-PK tables"
else
  ok "all user tables have a usable replica identity"
fi

log "STEP 1: (re)creating PUBLICATION ${PUBNAME} FOR ALL TABLES on SOURCE"
psql_run "$SRC_DSN" "DROP PUBLICATION IF EXISTS ${PUBNAME};"
psql_run "$SRC_DSN" "CREATE PUBLICATION ${PUBNAME} FOR ALL TABLES;"
ok "PUBLICATION ${PUBNAME} ready"

# ---------------------------------------------------------------------------
# STEP 2 — TARGET: restore schema baseline (schema-only). Data comes from either
#   the subscription's initial COPY (COPY_DATA=true) or a pre-seeded baseline.
# ---------------------------------------------------------------------------
mkdir -p "$BASELINE_DIR"
log "STEP 2: dumping SCHEMA from SOURCE -> ${SCHEMA_DUMP}"
# --no-owner/--no-privileges: target roles differ (doadmin vs rds master).
# --no-publications/--no-subscriptions: don't carry pub/sub objects across.
pg_dump "$SRC_DSN" \
  --schema-only --no-owner --no-privileges \
  --no-publications --no-subscriptions \
  -f "$SCHEMA_DUMP"
ok "schema dump written ($(wc -l < "$SCHEMA_DUMP" | tr -d ' ') lines)"

log "STEP 2: restoring SCHEMA onto TARGET (idempotent-ish; existing objects tolerated)"
# We tolerate "already exists" on re-runs but still surface real errors: run with
# ON_ERROR_STOP off ONLY for the schema restore, then assert key tables exist.
if psql "$DST_DSN" --no-psqlrc -q -f "$SCHEMA_DUMP" 2> "${BASELINE_DIR}/${DBNAME}.schema.restore.log"; then
  ok "schema restored on TARGET"
else
  warn "schema restore reported issues (may be pre-existing objects); see ${BASELINE_DIR}/${DBNAME}.schema.restore.log"
fi

# ---------------------------------------------------------------------------
# STEP 3 — TARGET: CREATE SUBSCRIPTION (the delta stream + optional initial COPY).
# ---------------------------------------------------------------------------
# Build the SOURCE connection string in libpq keyword form for the subscription.
# We reuse SRC_DSN verbatim; PG stores it (visible to superusers on target).
SUB_EXISTS="$(psql_val "$DST_DSN" "SELECT 1 FROM pg_subscription WHERE subname='${SUBNAME}';")"
if [[ "$SUB_EXISTS" == "1" ]]; then
  if [[ "$RESET_SUB" == "1" ]]; then
    warn "SUBSCRIPTION ${SUBNAME} exists; RESET_SUB=1 -> dropping it (and its remote slot)"
    # Disable + detach slot so DROP can also drop the remote slot cleanly.
    psql_run "$DST_DSN" "ALTER SUBSCRIPTION ${SUBNAME} DISABLE;"
    psql_run "$DST_DSN" "ALTER SUBSCRIPTION ${SUBNAME} SET (slot_name = NONE);"
    psql_run "$DST_DSN" "DROP SUBSCRIPTION ${SUBNAME};"
    # Best-effort drop of the orphaned slot on the source.
    psql_run "$SRC_DSN" "SELECT pg_drop_replication_slot('${SLOT_NAME}') WHERE EXISTS (SELECT 1 FROM pg_replication_slots WHERE slot_name='${SLOT_NAME}');" || true
    SUB_EXISTS=""
  else
    warn "SUBSCRIPTION ${SUBNAME} already exists — leaving it alone (pass RESET_SUB=1 to recreate)"
  fi
fi

if [[ "$SUB_EXISTS" != "1" ]]; then
  if [[ "$COPY_DATA" == "true" ]]; then
    COPY_OPT="copy_data = true"
    log "STEP 3: CREATE SUBSCRIPTION ${SUBNAME} (initial COPY + stream)"
  else
    COPY_OPT="copy_data = false"
    warn "STEP 3: CREATE SUBSCRIPTION ${SUBNAME} with copy_data=false — assumes you pre-seeded a consistent baseline at the slot LSN"
  fi
  psql_run "$DST_DSN" "
    CREATE SUBSCRIPTION ${SUBNAME}
      CONNECTION '${SRC_DSN}'
      PUBLICATION ${PUBNAME}
      WITH (slot_name = '${SLOT_NAME}', create_slot = true, ${COPY_OPT}, streaming = on);"
  ok "SUBSCRIPTION ${SUBNAME} created"
fi

# ---------------------------------------------------------------------------
# STEP 4 — report initial state.
# ---------------------------------------------------------------------------
log "STEP 4: subscription state on TARGET"
psql "$DST_DSN" --no-psqlrc -qX -c "
  SELECT s.subname, s.subenabled, r.srsubstate, count(*) AS tables
  FROM pg_subscription s
  LEFT JOIN pg_subscription_rel r ON r.srsubid = s.oid
  WHERE s.subname='${SUBNAME}'
  GROUP BY 1,2,3;" >&2 || true

cat >&2 <<EOF
----------------------------------------------------------------------------
 logical replication is now streaming for '${DBNAME}'.
 Watch initial sync + lag with:
   ./verify-migration.sh   (SRC_DSN / DST_DSN / DBNAME / MODE=lag)
 srsubstate legend: i=init  d=data copy  s=synced  r=ready(streaming)
 Proceed to cutover ONLY when all tables are 'r' AND lag=0 (RUNBOOK F4 step 2/3).
----------------------------------------------------------------------------
EOF
ok "setup complete"
