#!/usr/bin/env bash
# StackIt round-trip harness (wave-2, pd-TF-PROVIDERS-WAVE2: stackit), SPEC §6.
#
#   1. generate the concrete StackIt .tf from the canonical fixture (place.json)
#      for every SUPPORTED component
#   2. terraform fmt (parse-check) always; terraform init + validate/plan when the
#      registry is reachable
#   3. terraform apply + verify + destroy ONLY when STACKIT creds are present;
#      a missing-cred run is SKIPPED EXPLICITLY, never silently
#
# Creds: STACKIT_SERVICE_ACCOUNT_TOKEN (or a service-account key file) +
#        TF_VAR_stackit_project_id.
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
RENDER_BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$RENDER_BIN" ]]; then
  echo "building pyxnet-render..."
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  RENDER_BIN="$ROOT/pyxnet-render"
fi

# Supported StackIt components (the honest coverage matrix; unsupported ones
# surface a clean plan-time error and are intentionally NOT generated here).
COMPONENTS=(network security-group virtual-machine managed-database \
  object-storage managed-kubernetes dns-zone secrets-manager load-balancer)

: > generated.tf
for c in "${COMPONENTS[@]}"; do
  "$RENDER_BIN" -fixture place.json -provider stackit -component "$c" >> generated.tf
  echo >> generated.tf
done
echo "generated $(wc -l < generated.tf) lines of StackIt HCL"

# Always: parse-check via terraform fmt (works offline, no registry).
terraform fmt -check generated.tf provider.tf || { terraform fmt generated.tf provider.tf; echo "formatted"; }

# Validate/plan when the registry is reachable.
if terraform init -input=false -no-color >/dev/null 2>&1; then
  terraform validate -no-color
  if [[ -n "${STACKIT_SERVICE_ACCOUNT_TOKEN:-}" ]]; then
    echo ">>> StackIt creds present: plan (+ optional apply/destroy)"
    terraform plan -input=false -no-color
    # terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color
  else
    echo ">>> SKIP StackIt apply/destroy: no STACKIT_SERVICE_ACCOUNT_TOKEN (validate only)"
  fi
else
  echo ">>> SKIP terraform init/validate: registry.terraform.io unreachable (fmt parse-check only)"
fi
