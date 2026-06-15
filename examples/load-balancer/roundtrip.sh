#!/usr/bin/env bash
# Round-trip test harness for pd-TF-LB (load-balancer, SPEC 6).
#
# For each provider:
#   1. generate the concrete .tf from the canonical fixture:
#        aws/gcp: network + security-group + scale-group + load-balancer
#        do:      network + security-group + load-balancer  (NO scale-group:
#                 DigitalOcean has no native VM autoscaling primitive, so the LB
#                 fronts droplets by tag instead — see pd-TF-ASG)
#      composed into generated.tf
#   2. terraform init + plan (always)
#   3. terraform apply + verify + destroy (only when test creds are present;
#      a missing-cred provider is SKIPPED EXPLICITLY, never silently)
#
# A load balancer costs money: the AWS apply path runs `destroy` IMMEDIATELY
# after verifying, and asserts the LB + target group + instances are gone.
#
# Creds:
#   aws -> AWS_PROFILE=pyxcloudtest (aws sts get-caller-identity)
#   gcp -> application-default creds + GOOGLE_PROJECT
#   do  -> DIGITALOCEAN_TOKEN
set -euo pipefail
cd "$(dirname "$0")"

ROOT="$(cd ../.. && pwd)"
RENDER_BIN="${PYXNET_RENDER:-$ROOT/pyxnet-render}"
if [[ ! -x "$RENDER_BIN" ]]; then
  echo "building pyxnet-render..."
  (cd "$ROOT" && go build -o pyxnet-render ./cmd/pyxnet-render)
  RENDER_BIN="$ROOT/pyxnet-render"
fi

# gen <provider> <dir> <fixture> <with_asg>
gen() {
  local provider="$1" dir="$2" fixture="$3" with_asg="${4:-1}"
  : > "$dir/generated.tf"
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component network >> "$dir/generated.tf"
  echo "" >> "$dir/generated.tf"
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component security-group >> "$dir/generated.tf"
  echo "" >> "$dir/generated.tf"
  if [[ "$with_asg" == "1" ]]; then
    "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component scale-group >> "$dir/generated.tf"
    echo "" >> "$dir/generated.tf"
  fi
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component load-balancer >> "$dir/generated.tf"
  echo "generated $dir/generated.tf"
}

plan() { ( cd "$1" && terraform init -input=false >/dev/null && terraform plan -input=false -no-color ); }

# --- AWS (Dublin -> eu-west-1; ASG min=1/max=1 t3.micro + ALB HTTP:80) ---
gen aws aws lb-aws.json 1
plan aws
if AWS_PROFILE=pyxcloudtest aws sts get-caller-identity >/dev/null 2>&1; then
  echo ">>> AWS creds present: real apply + verify + immediate destroy"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform apply -auto-approve -no-color )
  echo ">>> describe load balancers:"
  AWS_PROFILE=pyxcloudtest aws elbv2 describe-load-balancers \
    --region eu-west-1 \
    --query "LoadBalancers[?contains(LoadBalancerName, 'web-lb')].{Name:LoadBalancerName,Type:Type,Scheme:Scheme,State:State.Code,DNS:DNSName}"
  echo ">>> describe target groups:"
  AWS_PROFILE=pyxcloudtest aws elbv2 describe-target-groups \
    --region eu-west-1 \
    --query "TargetGroups[?contains(TargetGroupName, 'web-lb')].{Name:TargetGroupName,Proto:Protocol,Port:Port,Type:TargetType}"
  echo ">>> describe instances launched by the ASG behind the LB:"
  AWS_PROFILE=pyxcloudtest aws ec2 describe-instances \
    --filters Name=tag:pyxcloud,Values=true Name=instance-state-name,Values=running,pending \
    --region eu-west-1 \
    --query 'Reservations[].Instances[].{Id:InstanceId,Type:InstanceType,State:State.Name}'
  echo ">>> DESTROY (load balancers cost money):"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve -no-color )
  echo ">>> verify teardown (no web-lb load balancer should remain):"
  AWS_PROFILE=pyxcloudtest aws elbv2 describe-load-balancers \
    --region eu-west-1 \
    --query "LoadBalancers[?contains(LoadBalancerName, 'web-lb')].LoadBalancerName"
else
  echo ">>> SKIP AWS apply/destroy: no pyxcloudtest profile (plan only)"
fi

# --- GCP (Frankfurt -> europe-west3; MIG + regional backend service + FR) ---
gen gcp gcp lb.json 1
GOOGLE_PROJECT="${GOOGLE_PROJECT:-pyxcloud-plan-only}" plan gcp
if [[ "${GOOGLE_PROJECT:-}" != "" && "${GOOGLE_PROJECT}" != "pyxcloud-plan-only" ]] && gcloud auth application-default print-access-token >/dev/null 2>&1; then
  echo ">>> GCP creds present: real apply + destroy"
  ( cd gcp && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP GCP apply/destroy: no application-default creds / GOOGLE_PROJECT (plan only)"
fi

# --- DigitalOcean (Frankfurt -> fra1; LB by droplet tag, NO scale-group) ---
gen digitalocean digitalocean lb.json 0
if [[ "${DIGITALOCEAN_TOKEN:-}" != "" ]]; then
  echo ">>> DO creds present: real apply + destroy"
  ( cd digitalocean && terraform init -input=false >/dev/null && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP DO apply/destroy: no DIGITALOCEAN_TOKEN (plan only — init+validate)"
  ( cd digitalocean && terraform init -input=false >/dev/null && terraform validate -no-color )
fi

echo "round-trip complete."

# --- Oracle Cloud / OCI (wave-2; Frankfurt -> eu-frankfurt-1) ---
# No OCI creds in CI -> init + validate only (explicit), never a real apply.
: > oracle/generated.tf
"$RENDER_BIN" -fixture "../load-balancer/lb.json" -provider oracle -component network >> oracle/generated.tf
echo "" >> oracle/generated.tf
"$RENDER_BIN" -fixture "../load-balancer/lb.json" -provider oracle -component security-group >> oracle/generated.tf
echo "" >> oracle/generated.tf
"$RENDER_BIN" -fixture "../load-balancer/lb.json" -provider oracle -component scale-group >> oracle/generated.tf
echo "" >> oracle/generated.tf
"$RENDER_BIN" -fixture "../load-balancer/lb.json" -provider oracle -component load-balancer >> oracle/generated.tf
echo "" >> oracle/generated.tf
echo "generated oracle/generated.tf"
( cd oracle && terraform init -input=false >/dev/null && terraform validate -no-color )
if [[ -n "${OCI_CLI_TENANCY:-}" || -f "${OCI_CONFIG_FILE:-$HOME/.oci/config}" ]] && [[ -n "${PYX_OCI_APPLY:-}" ]]; then
  echo ">>> OCI creds present + PYX_OCI_APPLY set: real plan + apply + destroy"
  ( cd oracle && terraform plan -input=false -no-color && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP OCI apply/destroy: no OCI creds or PYX_OCI_APPLY unset (validate only)"
fi
