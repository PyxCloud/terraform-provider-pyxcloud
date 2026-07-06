#!/usr/bin/env bash
# Add the `api.pyxcloud.io -> backend_prod` route to the live one-LB SNI edge
# routers (pyx-edge-1 / pyx-edge-2). This is the ONLY missing piece for
# api.pyxcloud.io once the backend-prod fleet is serving:
#   - Cloudflare api.pyxcloud.io -> 188.166.192.241 (edge LB): already correct.
#   - backend-prod firewall :443 from 0.0.0.0/0: already open (prod_service_fw).
#   - edge nginx SNI map: MISSING api.pyxcloud.io -> backend_prod  <-- this script.
#
# WHY A SCRIPT AND NOT TERRAFORM: the edge droplets are cattle adopted via
# `import` with `ignore_changes = [user_data]` (edge-ingress/main.tf), so their
# nginx config is not managed by an apply and user_data only runs on create.
# Until the one-LB edge config is moved off user_data (see README reconciliation),
# this gated, idempotent script is the apply path. Run it AFTER the backend-prod
# ASG is up (its public IPs must exist), and RE-RUN it after any backend-prod roll
# (fresh droplets => fresh public IPs).
#
# Idempotent: adds the map line + upstream if absent, and rewrites the upstream
# servers to the current backend-prod IPs if they changed. `nginx -t` gates the
# reload; a bad config is never activated.
#
# Usage:
#   ./add-api-prod-route.sh                 # resolve IPs via doctl, apply to both edge nodes
#   SSH_KEY=~/.ssh/id_ed25519 ./add-api-prod-route.sh
#   BACKEND_IPS="1.2.3.4 5.6.7.8" EDGE_IPS="164.90.219.181 167.99.240.9" ./add-api-prod-route.sh
set -euo pipefail

SSH_KEY="${SSH_KEY:-$HOME/.ssh/id_ed25519}"
SSH_OPTS="-i $SSH_KEY -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=15"

# Resolve backend-prod origin IPs (the upstream targets) + edge droplet IPs.
if [ -z "${BACKEND_IPS:-}" ]; then
  BACKEND_IPS="$(doctl compute droplet list --tag-name pyx-backend-prod --format PublicIPv4 --no-header | tr '\n' ' ')"
fi
if [ -z "${EDGE_IPS:-}" ]; then
  EDGE_IPS="$(doctl compute droplet list --tag-name pyx-edge --format PublicIPv4 --no-header | tr '\n' ' ')"
fi
BACKEND_IPS="$(echo "$BACKEND_IPS" | xargs)"
EDGE_IPS="$(echo "$EDGE_IPS" | xargs)"

[ -n "$BACKEND_IPS" ] || { echo "ERROR: no backend-prod IPs (is the ASG up? apply infra PR #5 first)"; exit 1; }
[ -n "$EDGE_IPS" ]    || { echo "ERROR: no edge droplet IPs resolved"; exit 1; }

echo "backend_prod upstream servers: $BACKEND_IPS"
echo "edge routers to update:        $EDGE_IPS"

# Build the "server X:443;" lines for the upstream.
UPSTREAM_SERVERS=""
for ip in $BACKEND_IPS; do UPSTREAM_SERVERS="$UPSTREAM_SERVERS server $ip:443;"; done
UPSTREAM_SERVERS="$(echo "$UPSTREAM_SERVERS" | xargs)"

# Remote, idempotent nginx.conf rewriter (runs ON each edge droplet).
REMOTE=$(cat <<PYEOF
import os, re, sys
servers = os.environ["UPSTREAM_SERVERS"]
p = "/etc/nginx/nginx.conf"
s = open(p).read()
orig = s

# 1) Ensure the map has: api.pyxcloud.io  backend_prod;  (insert before 'default').
if not re.search(r"api\.pyxcloud\.io\s+backend_prod;", s):
    s = re.sub(r"(\n(\s*)default\s+sink;)", r"\n\2api.pyxcloud.io       backend_prod;\1", s, count=1)

# 2) Ensure a backend_prod upstream exists with the CURRENT servers (replace if present).
up = "upstream backend_prod { %s }" % servers
if re.search(r"upstream\s+backend_prod\s*\{[^}]*\}", s):
    s = re.sub(r"upstream\s+backend_prod\s*\{[^}]*\}", up, s, count=1)
else:
    # insert after the last existing 'upstream ... { ... }' line
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
  ssh $SSH_OPTS "root@$edge" "UPSTREAM_SERVERS='$UPSTREAM_SERVERS' python3 - <<'RUN'
$REMOTE
RUN
nginx -t && systemctl reload nginx && echo 'reloaded OK on $edge'"
done

echo "Done. Verify: curl -sk --resolve api.pyxcloud.io:443:188.166.192.241 https://api.pyxcloud.io/q/health"
