#!/usr/bin/env bash
# Apply veto gate: the saved plan must contain EXACTLY
#   3 destroys  +  4 creates  +  0 updates + 0 replacements-other
# (3 placeholder-TXT replacements expressed as destroy+create at the same
#  addresses, + the _amazonses.passo.build verification TXT).
# ANY other action on ANY other resource -> veto.
#
# Usage: ./plan-guard.sh tfplan.json
set -euo pipefail
F="${1:?usage: plan-guard.sh <tfplan.json>}"

# Only resources of this root may appear at all.
OTHER=$(jq -r '
  [.. | objects | select(has("resource_changes")) | .resource_changes[]? |
    select(.type != "cloudflare_dns_record") | .address] | unique | .[]' "$F")
if [ -n "$OTHER" ]; then
  echo "VETO: plan touches resources outside this root:" >&2; echo "$OTHER" >&2; exit 1
fi

DESTROY=$(jq '[.resource_changes[].change.actions[] | select(.=="delete")] | length' "$F")
CREATE=$(jq '[.resource_changes[].change.actions[] | select(.=="create")] | length' "$F")
UPDATE=$(jq '[.resource_changes[].change.actions[] | select(.=="update")] | length' "$F")
REPL=$(jq '[.resource_changes[] | select((.change.actions|index("delete")) and (.change.actions|index("create")))] | length' "$F")

echo "plan: destroy=${DESTROY} create=${CREATE} update=${UPDATE} (of which replacements=${REPL})"

[ "$DESTROY" = 3 ] || { echo "VETO: expected exactly 3 destroys (the 3 placeholder TXT substitutions)"; exit 1; }
[ "$CREATE"  = 4 ] || { echo "VETO: expected exactly 4 creates (3 DKIM CNAMEs + _amazonses TXT)"; exit 1; }
[ "$UPDATE"  = 0 ] || { echo "VETO: in-place updates are outside this root's authority"; exit 1; }

# Every destroy/create must be one of the authorized names.
jq -r '.resource_changes[].address' "$F" | sort -u | while read -r a; do
  case "$a" in
    cloudflare_dns_record.ses_dkim*|cloudflare_dns_record.ses_verification) ;;
    *) echo "VETO: unexpected resource $a"; exit 1 ;;
  esac
done
# (pipefail makes the subshell veto propagate)

echo "OK: plan is exactly the bounded substitution (3 destroys + 4 creates, verification TXT included)."
