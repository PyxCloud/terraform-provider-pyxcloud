#!/usr/bin/env bash
# Round-trip test harness for pd-TF-REGION-VPC (SPEC §6).
#
# For each of aws / gcp / digitalocean:
#   1. generate the concrete .tf from the canonical fixture (place.json)
#   2. terraform init + plan (always)
#   3. terraform apply + verify + destroy (only when test creds are present;
#      a missing-cred provider is SKIPPED EXPLICITLY, never silently)
#
# Creds:
#   aws          -> AWS_PROFILE=pyxcloudtest (aws sts get-caller-identity)
#   gcp          -> application-default creds + GOOGLE_PROJECT
#   digitalocean -> DIGITALOCEAN_TOKEN
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
RENDER_BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$RENDER_BIN" ]]; then
  echo "building pyxnet-render..."
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  RENDER_BIN="$ROOT/pyxnet-render"
fi

gen() { # provider dir
  "$RENDER_BIN" -fixture place.json -provider "$1" > "$2/generated.tf"
  echo "generated $2/generated.tf"
}

plan() { ( cd "$1" && terraform init -input=false >/dev/null && terraform plan -input=false -no-color ); }
apply_destroy() { ( cd "$1" && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color ); }

# --- AWS ---
gen aws aws
plan aws
if AWS_PROFILE=pyxcloudtest aws sts get-caller-identity >/dev/null 2>&1; then
  echo ">>> AWS creds present: real apply + destroy"
  AWS_PROFILE=pyxcloudtest apply_destroy aws
else
  echo ">>> SKIP AWS apply/destroy: no pyxcloudtest profile (plan only)"
fi

# --- GCP ---
gen gcp gcp
plan gcp
if [[ -n "${GOOGLE_PROJECT:-}" ]] && gcloud auth application-default print-access-token >/dev/null 2>&1; then
  echo ">>> GCP creds present: real apply + destroy"
  apply_destroy gcp
else
  echo ">>> SKIP GCP apply/destroy: no application-default creds / GOOGLE_PROJECT (plan only)"
fi

# --- DigitalOcean ---
gen digitalocean digitalocean
plan digitalocean
if [[ -n "${DIGITALOCEAN_TOKEN:-}" ]]; then
  echo ">>> DO creds present: real apply + destroy"
  apply_destroy digitalocean
else
  echo ">>> SKIP DO apply/destroy: no DIGITALOCEAN_TOKEN (plan only)"
fi

# --- Azure (wave-2) ---
# Azure expects no test creds here; we generate + init + validate only (plan/apply
# are gated on ARM_SUBSCRIPTION_ID and SKIPPED EXPLICITLY when absent).
gen azure azure
( cd azure && terraform init -input=false >/dev/null && terraform validate -no-color )
if [[ -n "${ARM_SUBSCRIPTION_ID:-}" ]]; then
  echo ">>> Azure creds present: real plan + apply + destroy"
  ( cd azure && terraform plan -input=false -no-color )
  apply_destroy azure
else
  echo ">>> SKIP Azure plan/apply/destroy: no ARM_SUBSCRIPTION_ID (validate only)"
fi

echo "round-trip complete."
