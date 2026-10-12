#!/usr/bin/env bash
# STATE-OWNERSHIP / IMPORT GUARD (read-only on Cloudflare; writes ONLY to the
# local terraform state via `terraform import`).
#
# For each of the 3 live SES DKIM token labels:
#   - TXT placeholder  -> `terraform import cloudflare_dns_record.ses_dkim[t] ID`
#     so the plan becomes a bounded same-address replacement (destroy TXT +
#     create CNAME) instead of a colliding duplicate create.
#   - no record        -> skip; the address is a plain create.
#   - CNAME already    -> import it too (idempotent re-run after a partial apply).
#   - ANY OTHER TYPE   -> hard veto: this root is not authorized to touch it.
#
# The mapping (id/type/name/content) is printed and stored in
# snapshots/import-mapping-<stamp>.json so every import is auditable and the
# original values/IDs survive for rollback.
set -euo pipefail

: "${CLOUDFLARE_API_TOKEN:?fail-closed: no token}"
ZONE="${CLOUDFLARE_ZONE:?fail-closed: no zone}"
cd "$(dirname "$0")"

TOKENS=$(aws sesv2 get-email-identity --region eu-west-1 --email-identity passo.build \
  --query 'DkimAttributes.Tokens' --output text) || exit 1
[ "$(echo $TOKENS | wc -w)" = 3 ] || { echo "VETO: expected 3 DKIM tokens"; exit 1; }

MAP_FILE="snapshots/import-mapping-$(date -u +%Y%m%dT%H%M%SZ).json"
mkdir -p snapshots
echo '{ "tokens": [' > "$MAP_FILE"
first=1
for t in $TOKENS; do
  NAME="${t}._domainkey.passo.build"
  REC=$(curl -fsS -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
    "https://api.cloudflare.com/client/v4/zones/${ZONE}/dns_records?name=${NAME}")
  ID=$(printf '%s' "$REC" | jq -r '.result[0].id // empty')
  TYPE=$(printf '%s' "$REC" | jq -r '.result[0].type // empty')
  CONTENT=$(printf '%s' "$REC" | jq -r '.result[0].content // empty')
  echo "  ${NAME}: id=${ID:-<absent>} type=${TYPE:-<absent>}"
  [ "$first" = 0 ] && echo ',' >> "$MAP_FILE"
  first=0
  printf '  {"name":"%s","id":"%s","type":"%s","content":"%s"}' "$NAME" "$ID" "$TYPE" "$CONTENT" >> "$MAP_FILE"
  if [ -z "$ID" ]; then
    echo "    -> no live record; ses_dkim[$t] will be a plain CREATE"
    continue
  fi
  case "$TYPE" in
    TXT|CNAME)
      terraform import -input=false "cloudflare_dns_record.ses_dkim[${t}]" "$ID"
      ;;
    *)
      echo "VETO: ${NAME} is a ${TYPE} — outside this root's authority. STOP, root review." >&2
      exit 1
      ;;
  esac
done
echo ' ]}' >> "$MAP_FILE"
echo "import mapping saved: $MAP_FILE"
