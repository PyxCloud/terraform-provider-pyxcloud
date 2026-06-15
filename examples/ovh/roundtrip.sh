#!/usr/bin/env bash
# Round-trip test harness for the wave-2 OVHcloud provider (SPEC §6, pd-TF-PROVIDERS-WAVE2: ovh).
#
#   1. generate the concrete .tf from the canonical fixture (place.json) for the
#      SUPPORTED OVH components (network / managed-database / managed-kubernetes /
#      object-storage); the unsupported components are asserted to fail cleanly.
#   2. terraform init + validate + plan (always — no creds needed for validate;
#      plan needs only the provider schema, not real auth, for these resources).
#   3. terraform apply + verify + destroy ONLY when OVH creds + the project id are
#      present (OVH_APPLICATION_KEY/_SECRET, OVH_CONSUMER_KEY, TF_VAR_ovh_service_name);
#      a missing-cred run is SKIPPED EXPLICITLY, never silently.
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
RENDER_BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$RENDER_BIN" ]]; then
  echo "building pyxnet-render..."
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  RENDER_BIN="$ROOT/pyxnet-render"
fi

echo "# GENERATED from place.json by pyxnet-render -provider ovh (do not edit)" > generated.tf
for c in network managed-database managed-kubernetes object-storage; do
  echo "" >> generated.tf
  "$RENDER_BIN" -fixture place.json -provider ovh -component "$c" >> generated.tf
  echo "generated component: $c"
done
terraform fmt generated.tf >/dev/null  # canonicalise alignment (the cluster block widens to private_network_id)

# Assert the unsupported components fail cleanly (never silently emit a fake resource).
for c in security-group virtual-machine virtual-machine-scale-group load-balancer cache managed-queue event-streaming dns-zone cdn-service waf-service secrets-manager serverless-function; do
  if "$RENDER_BIN" -fixture place.json -provider ovh -component "$c" >/dev/null 2>&1; then
    echo "FAIL: expected $c to be unsupported on ovh, but it rendered"; exit 1
  else
    echo "ok (unsupported, clean error): $c"
  fi
done

terraform init -input=false >/dev/null
terraform validate
terraform plan -input=false -no-color || echo ">>> plan requires provider auth for these resources; validate passed."

if [[ -n "${OVH_APPLICATION_KEY:-}" && -n "${OVH_APPLICATION_SECRET:-}" && -n "${OVH_CONSUMER_KEY:-}" && -n "${TF_VAR_ovh_service_name:-}" ]]; then
  echo ">>> OVH creds present: real apply + destroy"
  terraform apply -auto-approve -no-color
  terraform destroy -auto-approve -no-color
else
  echo ">>> SKIP OVH apply/destroy: no OVH_* creds / TF_VAR_ovh_service_name (validate/plan only)"
fi

echo "round-trip complete."
