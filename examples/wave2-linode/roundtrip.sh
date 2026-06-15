#!/usr/bin/env bash
# Wave-2 Linode (Akamai) round-trip harness (pd-TF-W2-LINODE, SPEC §6).
#
# Generates the concrete linode/* .tf for every SUPPORTED component from the
# canonical fixture (place.json), then runs `terraform init` + `terraform
# validate` OFFLINE (schema + reference validation — no creds needed).
#
# NO real apply/destroy is performed: Linode test credentials are not available
# in this environment. When a LINODE_TOKEN is present a `terraform plan` is run
# as well; otherwise plan is SKIPPED EXPLICITLY (never silently). Apply/destroy
# stays gated behind real creds, exactly as the wave-1 harness does.
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
RENDER_BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$RENDER_BIN" ]]; then
  echo "building pyxnet-render..."
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  RENDER_BIN="$ROOT/pyxnet-render"
fi

DIR=linode
OUT="$DIR/generated.tf"
: > "$OUT"

# SUPPORTED Linode components, in dependency order. scale-group / cache /
# managed-queue / event-streaming / cdn-service / waf-service / secrets-manager /
# serverless-function are UNSUPPORTED on Linode and intentionally omitted (they
# emit a clean plan-time error — see the PR coverage matrix).
for c in network security-group virtual-machine load-balancer \
         managed-database object-storage dns-zone managed-kubernetes; do
  echo "# ── $c ─────────────────────────────────────────────" >> "$OUT"
  "$RENDER_BIN" -fixture place.json -provider linode -component "$c" >> "$OUT"
  echo >> "$OUT"
done
echo "generated $OUT"

( cd "$DIR" && terraform init -input=false -backend=false >/dev/null && terraform validate -no-color )

if [[ -n "${LINODE_TOKEN:-}" ]]; then
  echo ">>> LINODE_TOKEN present: terraform plan (no apply/destroy in this harness)"
  ( cd "$DIR" && terraform plan -input=false -no-color )
else
  echo ">>> SKIP terraform plan/apply: no LINODE_TOKEN (validate only, stated explicitly)"
fi

echo "round-trip (validate) complete."
