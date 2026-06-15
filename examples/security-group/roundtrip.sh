#!/usr/bin/env bash
# Round-trip test harness for pd-TF-SG (SPEC §6).
#
# For each of aws / gcp / digitalocean:
#   1. generate the concrete .tf from the canonical fixture
#      (sg.json for aws/gcp; sg-do.json for digitalocean — DO has no `all` proto)
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

# gen <provider> <dir> <fixture> [with_network]
# Renders the security-group, prepending the VPC network when with_network=1
# (AWS/GCP need the VPC the SG/firewall references; DO firewalls stand alone).
gen() {
  local provider="$1" dir="$2" fixture="$3" with_net="${4:-0}"
  : > "$dir/generated.tf"
  if [[ "$with_net" == "1" ]]; then
    "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component network >> "$dir/generated.tf"
    echo "" >> "$dir/generated.tf"
  fi
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component security-group >> "$dir/generated.tf"
  echo "generated $dir/generated.tf"
}

plan() { ( cd "$1" && terraform init -input=false >/dev/null && terraform plan -input=false -no-color ); }
apply_destroy() { ( cd "$1" && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color ); }

# --- AWS ---
gen aws aws sg.json 1
plan aws
if AWS_PROFILE=pyxcloudtest aws sts get-caller-identity >/dev/null 2>&1; then
  echo ">>> AWS creds present: real apply + verify + destroy"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform apply -auto-approve -no-color )
  AWS_PROFILE=pyxcloudtest aws ec2 describe-security-groups \
    --filters Name=group-name,Values=production-web --region eu-central-1 \
    --query 'SecurityGroups[].{Name:GroupName,Desc:Description,Ingress:length(IpPermissions),Egress:length(IpPermissionsEgress)}'
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP AWS apply/destroy: no pyxcloudtest profile (plan only)"
fi

# --- GCP ---
gen gcp gcp sg.json 1
plan gcp
if [[ -n "${GOOGLE_PROJECT:-}" ]] && gcloud auth application-default print-access-token >/dev/null 2>&1; then
  echo ">>> GCP creds present: real apply + destroy"
  apply_destroy gcp
else
  echo ">>> SKIP GCP apply/destroy: no application-default creds / GOOGLE_PROJECT (plan only)"
fi

# --- DigitalOcean ---
gen digitalocean digitalocean sg-do.json 0
plan digitalocean
if [[ -n "${DIGITALOCEAN_TOKEN:-}" ]]; then
  echo ">>> DO creds present: real apply + destroy"
  apply_destroy digitalocean
else
  echo ">>> SKIP DO apply/destroy: no DIGITALOCEAN_TOKEN (plan only)"
fi

echo "round-trip complete."
