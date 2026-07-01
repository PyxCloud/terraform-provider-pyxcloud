#!/usr/bin/env bash
# cutover-db.sh
# ---------------------------------------------------------------------------
# F4 write-cutover ORCHESTRATOR (pd-MIG-CUTOVER-F1-02, Path B).
# Wraps docs/cutover/RUNBOOK.md §3 steps 2-4 for ONE database with hard,
# operator-confirmed abort points. It does NOT freeze AWS writes for you
# (RUNBOOK step 1 is an app/role-level action outside this script) and it does
# NOT flip DNS or repoint services (RUNBOOK steps 5-7). It owns the DB-level
# point-of-no-return: drain lag -> verify -> (operator confirm) -> promote target.
#
#   Sequence (per RUNBOOK F4):
#     A. wait for replication lag = 0            (step 2)
#     B. run verify-migration.sh  (the GATE)     (step 3, last reversible point)
#     C. OPERATOR CONFIRMATION                    (approval gate F4-02)
#     D. stop/disable subscription + advance seqs -> promote target to primary (step 4)
#
# Usage:
#   SRC_DSN=... DST_DSN=... DBNAME=keycloak ./cutover-db.sh
#
# Required env: SRC_DSN  DST_DSN  DBNAME
# Optional env:
#   SUBNAME       (default pyx_mig_sub_<DBNAME>)
#   LAG_TIMEOUT   (default 600) seconds to wait for lag=0 before aborting.
#   ASSUME_YES    (default 0) if 1, skip the interactive confirm (for rehearsal
#                 automation ONLY — never for the real prod flip).
#   DROP_SLOT     (default 1) drop the source replication slot after promote so
#                 the frozen source stops retaining WAL.
#
# SAFETY: every failure aborts BEFORE the irreversible promote. The only
# irreversible action is step D (disable subscription + promote). Up to and
# including the gate you can un-freeze AWS with zero data loss (RUNBOOK §5).
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib-common.sh
source "${SCRIPT_DIR}/lib-common.sh"

require_cmd psql
require_env SRC_DSN DST_DSN DBNAME

SUBNAME="${SUBNAME:-pyx_mig_sub_${DBNAME}}"
LAG_TIMEOUT="${LAG_TIMEOUT:-600}"
ASSUME_YES="${ASSUME_YES:-0}"
DROP_SLOT="${DROP_SLOT:-1}"

cat >&2 <<EOF
############################################################################
#  F4 DB WRITE-CUTOVER  —  DBNAME=${DBNAME}  SUB=${SUBNAME}
#  Precondition: AWS writes ALREADY FROZEN (RUNBOOK F4 step 1). If not, ABORT NOW.
############################################################################
EOF

# ---------------------------------------------------------------------------
# A. drain replication to lag = 0
# ---------------------------------------------------------------------------
log "A. draining replication to lag=0 (timeout ${LAG_TIMEOUT}s)"
deadline=$(( $(date +%s) + LAG_TIMEOUT ))
while :; do
  if MODE=lag SUBNAME="$SUBNAME" MAX_LAG_BYTES=0 \
       SRC_DSN="$SRC_DSN" DST_DSN="$DST_DSN" DBNAME="$DBNAME" \
       "${SCRIPT_DIR}/verify-migration.sh" >/dev/null 2>&1; then
    ok "A. lag = 0, all tables ready"
    break
  fi
  if [[ $(date +%s) -ge $deadline ]]; then
    # show the detail on failure
    MODE=lag SUBNAME="$SUBNAME" MAX_LAG_BYTES=0 \
      SRC_DSN="$SRC_DSN" DST_DSN="$DST_DSN" DBNAME="$DBNAME" \
      "${SCRIPT_DIR}/verify-migration.sh" || true
    die "A. lag did not reach 0 within ${LAG_TIMEOUT}s — ABORT, un-freeze AWS (RUNBOOK §5, zero data loss)."
  fi
  log "   lag not yet 0; waiting..."
  sleep 5
done

# ---------------------------------------------------------------------------
# A2. sync sequences SOURCE -> TARGET (inside the freeze, source is frozen).
#     Logical replication does NOT replicate sequence values, so the target
#     sequences are still at their restore-time value. We copy the source's
#     current last_value to the target here — safe because the source is
#     write-frozen (step A precondition) so its sequences no longer advance.
#     This makes the gate's sequence high-water check pass (RUNBOOK §4) and is
#     the authoritative fix for post-promote PK-collision risk.
# ---------------------------------------------------------------------------
log "A2. syncing sequence high-water marks SOURCE -> TARGET (source is frozen)"
while IFS='|' read -r seq lastval; do
  [[ -z "$seq" ]] && continue
  psql_run "$DST_DSN" "SELECT setval('${seq}', ${lastval});"
done < <(psql "$SRC_DSN" -v ON_ERROR_STOP=1 --no-psqlrc -qtAX -c "
  SELECT schemaname||'.'||sequencename||'|'||COALESCE(last_value,1)
  FROM pg_sequences
  WHERE schemaname NOT IN ('pg_catalog','information_schema');")
ok "A2. sequences synced"

# ---------------------------------------------------------------------------
# B. the DATA-SAFETY GATE (last reversible checkpoint)
# ---------------------------------------------------------------------------
log "B. running data-safety gate (row-count + checksum + sequences + lag)"
if ! MODE=auto SUBNAME="$SUBNAME" \
       SRC_DSN="$SRC_DSN" DST_DSN="$DST_DSN" DBNAME="$DBNAME" \
       "${SCRIPT_DIR}/verify-migration.sh"; then
  die "B. GATE RED — ABORT. Un-freeze AWS, drop maintenance banner (RUNBOOK §5). No data lost."
fi
ok "B. GATE GREEN — this is the LAST reversible point."

# ---------------------------------------------------------------------------
# C. operator confirmation (approval gate F4-02) — POINT OF NO RETURN AHEAD
# ---------------------------------------------------------------------------
if [[ "$ASSUME_YES" != "1" ]]; then
  cat >&2 <<EOF

  >>> POINT OF NO RETURN for '${DBNAME}'.
  >>> After promote, TARGET (DO) accepts writes and SOURCE (AWS) becomes stale.
  >>> Rolling back after this means losing writes taken on DO (RUNBOOK §3/§5).
  >>> Approval gate F4-02: migration owner + DBA must have signed off.

EOF
  read -r -p "  Type 'PROMOTE ${DBNAME}' to proceed, anything else to abort: " ans
  if [[ "$ans" != "PROMOTE ${DBNAME}" ]]; then
    die "C. operator aborted before promote — safe. Un-freeze AWS to resume service (RUNBOOK §5)."
  fi
else
  warn "C. ASSUME_YES=1 — skipping interactive confirm (rehearsal automation only)."
fi

# ---------------------------------------------------------------------------
# D. PROMOTE (irreversible): stop the subscription, detach slot, drop it.
#    This makes the target a standalone primary that accepts writes. The DO
#    Managed PG cluster is already a real primary; "promotion" here = severing
#    the inbound replication so nothing overwrites the now-authoritative target.
# ---------------------------------------------------------------------------
log "D. PROMOTE: disabling + dropping subscription ${SUBNAME} on target"
sub_exists="$(psql_val "$DST_DSN" "SELECT 1 FROM pg_subscription WHERE subname='${SUBNAME}';")"
if [[ "$sub_exists" == "1" ]]; then
  psql_run "$DST_DSN" "ALTER SUBSCRIPTION ${SUBNAME} DISABLE;"
  # Detach the remote slot from the sub so DROP SUBSCRIPTION won't try (and fail,
  # if the source is unreachable) to drop it; we drop the slot on the source
  # ourselves below.
  psql_run "$DST_DSN" "ALTER SUBSCRIPTION ${SUBNAME} SET (slot_name = NONE);"
  psql_run "$DST_DSN" "DROP SUBSCRIPTION ${SUBNAME};"
  ok "D. subscription dropped — target no longer receives from source"
else
  warn "D. no subscription ${SUBNAME} (point-in-time copy path?) — nothing to detach"
fi

# Final sequence safety bump: ensure every sequence on target is >= its table
# max so post-cutover inserts never collide (belt-and-suspenders over verify).
log "D. advancing target sequences to table maxima (collision guard)"
psql "$DST_DSN" -v ON_ERROR_STOP=1 --no-psqlrc -q <<'SQL'
DO $$
DECLARE r record; maxv bigint; seqname text;
BEGIN
  FOR r IN
    SELECT n.nspname AS sch, c.relname AS tbl, a.attname AS col,
           pg_get_serial_sequence(quote_ident(n.nspname)||'.'||quote_ident(c.relname), a.attname) AS seq
    FROM pg_attribute a
    JOIN pg_class c ON c.oid=a.attrelid
    JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE a.attnum>0 AND NOT a.attisdropped
      AND pg_get_serial_sequence(quote_ident(n.nspname)||'.'||quote_ident(c.relname), a.attname) IS NOT NULL
  LOOP
    EXECUTE format('SELECT COALESCE(max(%I),0) FROM %I.%I', r.col, r.sch, r.tbl) INTO maxv;
    IF maxv > 0 THEN
      PERFORM setval(r.seq, maxv);
    END IF;
  END LOOP;
END $$;
SQL
ok "D. sequences advanced"

# Drop the now-orphaned slot on the (frozen) source so it stops retaining WAL.
if [[ "$DROP_SLOT" == "1" ]]; then
  log "D. dropping orphaned replication slot(s) on source"
  psql "$SRC_DSN" -v ON_ERROR_STOP=1 --no-psqlrc -q -c "
    SELECT pg_drop_replication_slot(slot_name)
    FROM pg_replication_slots
    WHERE slot_name='${SUBNAME}' OR slot_name LIKE '%${DBNAME}%';" || warn "D. slot drop best-effort (source may be gone)"
fi

cat >&2 <<EOF
############################################################################
#  '${DBNAME}' PROMOTED. Target (DO) is now the authoritative primary.
#  NEXT (RUNBOOK §3 steps 5-7, outside this script):
#    5. repoint the 6 services at the DO PG endpoint + roll deployments
#    6. Cloudflare DNS canary 5% -> 25% -> 100%
#    7. E2E smoke (Keycloak canary login, API round-trip, MCP, SPAs, email, Spaces)
############################################################################
EOF
ok "cutover-db complete for ${DBNAME}"
