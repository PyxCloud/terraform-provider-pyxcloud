#!/usr/bin/env bash
# Repoint `staging.passo.build` (+ `staging-console.passo.build`) on the live
# one-LB SNI edge routers (pyx-edge-1 / pyx-edge-2) to the NEW dedicated
# staging-fe shim droplet — replacing the fragile, hand-managed shim
# co-located on the obs droplet (10.0.1.7). pd-STAGING-FE-SHIM.
#
# WHY A SCRIPT AND NOT TERRAFORM: same reason as add-api-prod-route.sh — the
# edge droplets are cattle adopted via `import` with `ignore_changes =
# [user_data]` (edge-ingress/main.tf), so their nginx config is not managed by
# a terraform apply and user_data only runs on droplet CREATE. Until the
# one-LB edge config moves off user_data (see README reconciliation), this
# gated, idempotent script is the apply path — exactly like the existing
# api.pyxcloud.io route.
#
# WHAT THIS DOES NOT DO: it does not touch the obs droplet, does not remove
# any existing route, and does not provision the staging-fe droplet itself
# (that is the IaC in do_baseline.go / DOBaselineServices — apply that FIRST).
# This script ONLY repoints the edge map once the shim is confirmed healthy.
#
# Preconditions (see the runbook, RUNBOOK-STAGING-FE-SHIM.md):
#   1. The staging-fe droplet-autoscale pool is up (tag pyx-staging-fe) and its
#      own health gate passed (the bootstrap's final curl loop).
#   2. You have verified it serves the Amplify branch correctly via a DIRECT
#      curl to its private IP over the VPN/bastion (see the runbook step 2)
#      BEFORE repointing the public hostname onto it.
#
# Idempotent: adds the map lines + upstream if absent, and rewrites the
# upstream's server line to the current staging-fe IP if it changed (e.g.
# after a droplet-autoscale self-heal replacement). `nginx -t` gates the
# reload; a bad config is never activated.
#
# Usage:
#   ./add-staging-fe-route.sh                          # resolve IPs via doctl, apply to both edge nodes
#   SSH_KEY=~/.ssh/id_ed25519 ./add-staging-fe-route.sh
#   STAGING_FE_IP="10.0.1.20" EDGE_IPS="164.90.219.181 167.99.240.9" ./add-staging-fe-route.sh
set -euo pipefail

SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_ed25519}"
SSH_OPTS="-i $SSH_KEY -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15"

# Resolve the staging-fe shim's PRIVATE IP (the upstream target — the edge
# reaches it over the shared VPC, not the public internet; the shim's firewall
# only accepts :443 from the pyx-edge tag) + the edge droplets' PUBLIC IPs
# (this script SSHes to the edge boxes themselves, over their public IP, same
# as add-api-prod-route.sh).
if [ -z "${STAGING_FE_IP:-}" ]; then
  STAGING_FE_ID="$(doctl compute droplet list --tag-name pyx-staging-fe --format ID --no-header | head -n1 | tr -d '[:space:]')"
  [ -n "$STAGING_FE_ID" ] || { echo "ERROR: no pyx-staging-fe droplet found (apply the staging-fe IaC first)"; exit 1; }
  STAGING_FE_IP="$(doctl compute droplet get "$STAGING_FE_ID" --format PrivateIPv4 --no-header | tr -d '[:space:]')"
fi
if [ -z "${EDGE_IPS:-}" ]; then
  EDGE_IPS="$(doctl compute droplet list --tag-name pyx-edge --format PublicIPv4 --no-header | tr '\n' ' ')"
fi
STAGING_FE_IP="$(echo "$STAGING_FE_IP" | xargs)"
EDGE_IPS="$(echo "$EDGE_IPS" | xargs)"

[ -n "$STAGING_FE_IP" ] || { echo "ERROR: could not resolve the staging-fe private IP"; exit 1; }
[ -n "$EDGE_IPS" ]      || { echo "ERROR: no edge droplet IPs resolved"; exit 1; }

echo "staging_fe upstream server: $STAGING_FE_IP:443"
echo "edge routers to update:     $EDGE_IPS"
echo
read -r -p "Repoint staging.passo.build -> $STAGING_FE_IP on the routers above? [type 'yes' to continue] " CONFIRM
[ "$CONFIRM" = "yes" ] || { echo "aborted (no changes made)"; exit 1; }

# Remote, idempotent nginx.conf rewriter (runs ON each edge droplet).
REMOTE=$(cat <<PYEOF
import os, re, sys
staging_fe_ip = os.environ["STAGING_FE_IP"]
p = "/etc/nginx/nginx.conf"
s = open(p).read()
orig = s

# 1) Ensure the map has both hostnames -> staging_fe (insert before 'default').
for host in ("staging.passo.build", "staging-console.passo.build"):
    pattern = re.escape(host) + r"\s+staging_fe;"
    if not re.search(pattern, s):
        s = re.sub(r"(\n(\s*)default\s+sink;)", r"\n\2%s       staging_fe;\1" % host, s, count=1)

# 2) Ensure a staging_fe upstream exists with the CURRENT ip (replace if present).
up = "upstream staging_fe { server %s:443; }" % staging_fe_ip
if re.search(r"upstream\s+staging_fe\s*\{[^}]*\}", s):
    s = re.sub(r"upstream\s+staging_fe\s*\{[^}]*\}", up, s, count=1)
else:
    m = list(re.finditer(r"upstream\s+\w+\s*\{[^}]*\}", s))
    if not m:
        sys.exit("no upstream block found; nginx.conf shape unexpected")
    idx = m[-1].end()
    s = s[:idx] + "\n  " + up + s[idx:]

if s == orig:
    print("already up to date")
else:
    open(p, "w").write(s)
    print("nginx.conf updated")
PYEOF
)

for edge in $EDGE_IPS; do
  echo "== $edge =="
  ssh $SSH_OPTS "root@$edge" "STAGING_FE_IP='$STAGING_FE_IP' python3 - <<'RUN'
$REMOTE
RUN
nginx -t && systemctl reload nginx && echo 'reloaded OK on $edge'"
done

echo
echo "Done. Verify from a VPN-connected host (staging.passo.build is VPN-only):"
echo "  curl -sk --resolve staging.passo.build:443:188.166.192.241 https://staging.passo.build/ -o /dev/null -w '%{http_code}\n'"
echo
echo "If this looks wrong, roll back with:"
echo "  EDGE_IPS=\"$EDGE_IPS\" STAGING_FE_IP=10.0.1.7 ./add-staging-fe-route.sh   # points back at the obs-box shim IP"
