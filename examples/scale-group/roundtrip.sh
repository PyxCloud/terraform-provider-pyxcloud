#!/usr/bin/env bash
# Round-trip test harness for pd-TF-ASG (virtual-machine-scale-group, SPEC 6).
#
# For aws / gcp:
#   1. generate the concrete .tf from the canonical fixture
#      (asg-aws.json for aws -> Dublin/eu-west-1, min=1/max=1, smallest SKU to
#       minimise cost; asg.json for gcp -> Frankfurt):
#      network + security-group + scale-group, composed into generated.tf
#   2. terraform init + plan (always)
#   3. terraform apply + verify + destroy (only when test creds are present;
#      a missing-cred provider is SKIPPED EXPLICITLY, never silently)
#
# For digitalocean: there is NO native VM autoscaling primitive, so the renderer
#   is EXPECTED to fail with ErrAutoscaleUnsupported. The harness asserts that
#   clean error (a non-zero exit pointing to managed-kubernetes) instead of
#   generating .tf.
#
# Autoscaling resources cost money: the AWS apply path runs `destroy`
# IMMEDIATELY after verifying, and asserts the ASG + instances are gone.
#
# Creds:
#   aws -> AWS_PROFILE=pyxcloudtest (aws sts get-caller-identity)
#   gcp -> application-default creds + GOOGLE_PROJECT
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
gen() {
  local provider="$1" dir="$2" fixture="$3" with_sg="${4:-1}"
  : > "$dir/generated.tf"
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component network >> "$dir/generated.tf"
  echo "" >> "$dir/generated.tf"
  if [[ "$with_sg" == "1" ]]; then
    "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component security-group >> "$dir/generated.tf"
    echo "" >> "$dir/generated.tf"
  fi
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component scale-group >> "$dir/generated.tf"
  echo "generated $dir/generated.tf"
}

plan() { ( cd "$1" && terraform init -input=false >/dev/null && terraform plan -input=false -no-color ); }

# --- AWS (Dublin -> eu-west-1; 2vCPU/1GiB -> t3.micro, min=1/max=1) ---
gen aws aws asg-aws.json 1
plan aws
if AWS_PROFILE=pyxcloudtest aws sts get-caller-identity >/dev/null 2>&1; then
  echo ">>> AWS creds present: real apply + verify + immediate destroy"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform apply -auto-approve -no-color )
  echo ">>> describe ASG:"
  AWS_PROFILE=pyxcloudtest aws autoscaling describe-auto-scaling-groups \
    --region eu-west-1 \
    --query "AutoScalingGroups[?contains(Tags[?Key=='pyxcloud'].Value, 'true')].{Name:AutoScalingGroupName,Min:MinSize,Max:MaxSize,Desired:DesiredCapacity,Instances:length(Instances)}"
  echo ">>> describe instances launched by the ASG:"
  AWS_PROFILE=pyxcloudtest aws ec2 describe-instances \
    --filters Name=tag:pyxcloud,Values=true Name=instance-state-name,Values=running,pending \
    --region eu-west-1 \
    --query 'Reservations[].Instances[].{Id:InstanceId,Type:InstanceType,State:State.Name}'
  echo ">>> DESTROY (autoscaling resources cost money):"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve -no-color )
  echo ">>> verify teardown (ASG should be empty):"
  AWS_PROFILE=pyxcloudtest aws autoscaling describe-auto-scaling-groups \
    --region eu-west-1 \
    --query "AutoScalingGroups[?contains(Tags[?Key=='pyxcloud'].Value, 'true')].AutoScalingGroupName"
else
  echo ">>> SKIP AWS apply/destroy: no pyxcloudtest profile (plan only)"
fi

# --- GCP (Frankfurt -> europe-west3; 2vCPU/4GiB -> e2-medium) ---
gen gcp gcp asg.json 1
GOOGLE_PROJECT="${GOOGLE_PROJECT:-pyxcloud-plan-only}" plan gcp
if [[ "${GOOGLE_PROJECT:-}" != "" && "${GOOGLE_PROJECT}" != "pyxcloud-plan-only" ]] && gcloud auth application-default print-access-token >/dev/null 2>&1; then
  echo ">>> GCP creds present: real apply + destroy"
  ( cd gcp && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP GCP apply/destroy: no application-default creds / GOOGLE_PROJECT (plan only)"
fi

# --- DigitalOcean: EXPECTED clean error (no native VM ASG primitive) ---
echo ">>> DigitalOcean: asserting clean unsupported error (no native VM ASG)"
if "$RENDER_BIN" -fixture asg.json -provider digitalocean -component scale-group >/dev/null 2>do_err.txt; then
  echo "!!! FAIL: DO scale-group render unexpectedly succeeded (should be unsupported)"
  exit 1
else
  if grep -q "managed-kubernetes" do_err.txt; then
    echo ">>> OK: DO returns the expected unsupported error:"
    cat do_err.txt
    rm -f do_err.txt
  else
    echo "!!! FAIL: DO error did not mention the managed-kubernetes mapping:"
    cat do_err.txt
    exit 1
  fi
fi

echo "round-trip complete."
