#!/usr/bin/env bash
# Round-trip test harness for pd-TF-EC2-VM (SPEC 6).
#
# For each of aws / gcp / digitalocean:
#   1. generate the concrete .tf from the canonical fixture
#      (vm-aws.json for aws -> Dublin/eu-west-1; vm.json for gcp/do -> Frankfurt):
#      network + security-group + virtual-machine, composed into generated.tf
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

# gen <provider> <dir> <fixture> [with_sg]
# Renders network + (optional) security-group + virtual-machine into generated.tf.
# AWS/GCP carry the SG (the instance references it / its VPC); DO firewalls
# attach to droplets and are omitted here to keep the round-trip minimal.
gen() {
  local provider="$1" dir="$2" fixture="$3" with_sg="${4:-1}"
  : > "$dir/generated.tf"
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component network >> "$dir/generated.tf"
  echo "" >> "$dir/generated.tf"
  if [[ "$with_sg" == "1" ]]; then
    "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component security-group >> "$dir/generated.tf"
    echo "" >> "$dir/generated.tf"
  fi
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component virtual-machine >> "$dir/generated.tf"
  echo "generated $dir/generated.tf"
}

plan() { ( cd "$1" && terraform init -input=false >/dev/null && terraform plan -input=false -no-color ); }
apply_destroy() { ( cd "$1" && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color ); }

# --- AWS (Dublin -> eu-west-1; 2vCPU/1GiB -> t3.micro, the smallest SKU) ---
gen aws aws vm-aws.json 1
plan aws
if AWS_PROFILE=pyxcloudtest aws sts get-caller-identity >/dev/null 2>&1; then
  echo ">>> AWS creds present: real apply + verify + destroy"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform apply -auto-approve -no-color )
  AWS_PROFILE=pyxcloudtest aws ec2 describe-instances \
    --filters Name=tag:pyxcloud,Values=true Name=instance-state-name,Values=running,pending \
    --region eu-west-1 \
    --query 'Reservations[].Instances[].{Id:InstanceId,Type:InstanceType,Image:ImageId,State:State.Name}'
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP AWS apply/destroy: no pyxcloudtest profile (plan only)"
fi

# --- GCP (Frankfurt -> europe-west3; 2vCPU/4GiB -> e2-medium) ---
# The google provider requires a project even for `plan`; use a placeholder for
# the plan-only path so schema/shape is validated without real creds.
gen gcp gcp vm.json 1
GOOGLE_PROJECT="${GOOGLE_PROJECT:-pyxcloud-plan-only}" plan gcp
if [[ "${GOOGLE_PROJECT:-}" != "" && "${GOOGLE_PROJECT}" != "pyxcloud-plan-only" ]] && gcloud auth application-default print-access-token >/dev/null 2>&1; then
  echo ">>> GCP creds present: real apply + destroy"
  apply_destroy gcp
else
  echo ">>> SKIP GCP apply/destroy: no application-default creds / GOOGLE_PROJECT (plan only)"
fi

# --- DigitalOcean (Frankfurt -> fra1; 2vCPU/4GiB -> s-2vcpu-4gb) ---
# The digitalocean provider requires a token even for `plan`; use a placeholder
# for the plan-only path.
gen digitalocean digitalocean vm.json 0
DIGITALOCEAN_TOKEN="${DIGITALOCEAN_TOKEN:-do-plan-only}" plan digitalocean
if [[ -n "${DIGITALOCEAN_TOKEN:-}" && "${DIGITALOCEAN_TOKEN}" != "do-plan-only" ]]; then
  echo ">>> DO creds present: real apply + destroy"
  apply_destroy digitalocean
else
  echo ">>> SKIP DO apply/destroy: no DIGITALOCEAN_TOKEN (plan only)"
fi

echo "round-trip complete."

# --- Oracle Cloud / OCI (wave-2; Frankfurt -> eu-frankfurt-1) ---
# No OCI creds in CI -> init + validate only (explicit), never a real apply.
: > oracle/generated.tf
"$RENDER_BIN" -fixture "../virtual-machine/vm.json" -provider oracle -component network >> oracle/generated.tf
echo "" >> oracle/generated.tf
"$RENDER_BIN" -fixture "../virtual-machine/vm.json" -provider oracle -component security-group >> oracle/generated.tf
echo "" >> oracle/generated.tf
"$RENDER_BIN" -fixture "../virtual-machine/vm.json" -provider oracle -component virtual-machine >> oracle/generated.tf
echo "" >> oracle/generated.tf
echo "generated oracle/generated.tf"
( cd oracle && terraform init -input=false >/dev/null && terraform validate -no-color )
if [[ -n "${OCI_CLI_TENANCY:-}" || -f "${OCI_CONFIG_FILE:-$HOME/.oci/config}" ]] && [[ -n "${PYX_OCI_APPLY:-}" ]]; then
  echo ">>> OCI creds present + PYX_OCI_APPLY set: real plan + apply + destroy"
  ( cd oracle && terraform plan -input=false -no-color && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP OCI apply/destroy: no OCI creds or PYX_OCI_APPLY unset (validate only)"
fi
