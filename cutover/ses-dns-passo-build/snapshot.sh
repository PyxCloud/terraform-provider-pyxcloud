#!/usr/bin/env bash
# Read-only before-snapshot of the EXACT record-set this root is authorized to
# touch: _amazonses.passo.build + the 3 live DKIM labels. Nothing else is
# listed; no write ever happens here. The token is read from the environment
# and never echoed.
#
# Usage: CLOUDFLARE_ZONE=<zone-id> ./snapshot.sh <out.json>
set -euo pipefail

: "${CLOUDFLARE_API_TOKEN:?fail-closed: no token}"
: "${CLOUDFLARE_ZONE:?fail-closed: no zone}"
OUT="${1:?usage: snapshot.sh <out.json|/dev/stdout>}"

ZONE="${CLOUDFLARE_ZONE}"
cf() { curl -fsS -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" "$@"; }

SELF="$PWD"
ZONE="$ZONE" OUT="$OUT" bash <<'SNAP'
set -euo pipefail
cf() { curl -fsS -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" "$@"; }
OUT="$OUT"
# 3 DKIM tokens + verification token are re-measured from SES so the snapshot
# covers exactly the labels this root manages.
TOKENS=$(aws sesv2 get-email-identity --region eu-west-1 --email-identity passo.build \
  --query 'DkimAttributes.Tokens' --output text)
NAMES=("_amazonses.passo.build")
for t in $TOKENS; do NAMES+=("${t}._domainkey.passo.build"); done
{
  echo '{ "captured_at_utc": "'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'",'
  echo '  "zone_id": "'"${CLOUDFLARE_ZONE}"'",'
  echo '  "records": ['
  first=1
  for n in "${NAMES[@]}"; do
    R=$(cf "https://api.cloudflare.com/client/v4/zones/${CLOUDFLARE_ZONE}/dns_records?per_page=100" \
        | jq -c --arg n "$n" '[.result[] | select(.name==$n) | {id,type,name,content,ttl,proxied}]')
    if [ "$first" = 0 ]; then echo ","; fi
    first=0
    printf '    {"name": "%s", "records": %s}' "$n" "$R"
  done
  echo
  echo '  ]}'
} > "$OUT"
echo "snapshot written: $OUT"
echo "note: this snapshot includes the SES DKIM token labels and verification token, which are PUBLIC DNS values by definition — safe to commit."
SNAP
