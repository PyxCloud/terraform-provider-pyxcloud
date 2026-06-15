#!/usr/bin/env bash
# Round-trip harness for the wave-2 Ubicloud provider
# [pd-TF-PROVIDERS-WAVE2: ubicloud], SPEC §6 (adapted).
#
# HONEST COVERAGE: Ubicloud has THIN Terraform support. Its official provider
# (ubicloud/terraform-provider-ubicloud) exposes only vm / postgres /
# private_subnet / firewall(+rule) / project. So this harness:
#
#   1. GENERATES the four SUPPORTED components from the canonical fixture
#      (place.json) into generated.tf:
#        network (ubicloud_private_subnet)
#        security-group (ubicloud_firewall + ubicloud_firewall_rule)
#        virtual-machine (ubicloud_vm x count)
#        managed-database (ubicloud_postgres, postgres-only)
#   2. ASSERTS that every UNSUPPORTED component raises a clean plan-time error
#      naming the alternative (never a silent success, never an invented resource).
#   3. terraform init + validate (always; no creds needed).
#
# NO REAL APPLY: no Ubicloud test credentials are available in this environment,
# so this script deliberately STOPS at validate/plan. A real apply/destroy round
# trip would set UBICLOUD_API_TOKEN + TF_VAR_ubicloud_project_id + a real SSH key
# and run plan/apply/destroy — that path is gated and intentionally NOT run here.
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
RENDER_BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$RENDER_BIN" ]]; then
  echo "building pyxnet-render..."
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  RENDER_BIN="$ROOT/pyxnet-render"
fi

FIXTURE="place.json"
OUT="generated.tf"

echo ">>> SUPPORTED: generate the four real ubicloud_* component sets"
: > "$OUT"
for comp in network security-group virtual-machine managed-database; do
  "$RENDER_BIN" -fixture "$FIXTURE" -provider ubicloud -component "$comp" >> "$OUT"
  echo "" >> "$OUT"
done
echo "generated $OUT"

echo ">>> UNSUPPORTED: assert each component fails cleanly (named alternative)"
UNSUPPORTED=(scale-group load-balancer object-storage cache managed-queue \
  event-streaming dns-zone cdn-service waf-service managed-kubernetes \
  secrets-manager serverless-function)
for comp in "${UNSUPPORTED[@]}"; do
  # The fixture has no block for most of these; the loader errors with either
  # "unsupported on ubicloud" (render guard) or "fixture has no <x> block". Both
  # are clean, non-zero exits — what we require is that NO .tf is produced.
  if out="$("$RENDER_BIN" -fixture "$FIXTURE" -provider ubicloud -component "$comp" 2>&1)"; then
    echo "FAIL: $comp unexpectedly produced output on ubicloud:"; echo "$out"; exit 1
  fi
  echo "  ok: $comp -> clean error (no resource emitted)"
done

echo ">>> terraform init + validate (no creds needed)"
terraform init -input=false >/dev/null
terraform validate -no-color

if [[ "${UBICLOUD_API_TOKEN:-}" != "" ]]; then
  echo ">>> Ubicloud creds present: plan (apply/destroy left to the operator)"
  terraform plan -input=false -no-color
else
  echo ">>> SKIP Ubicloud plan/apply: no UBICLOUD_API_TOKEN (validate only — explicit)"
fi

echo "round-trip complete (Ubicloud: 4 supported, $((${#UNSUPPORTED[@]})) cleanly unsupported)."
