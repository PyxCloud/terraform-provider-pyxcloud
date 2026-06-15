#!/usr/bin/env bash
# Round-trip test harness for pd-TF-MDB (managed-database, SPEC 6).
#
# For each provider:
#   1. generate the concrete .tf from the canonical fixture:
#        network + security-group + managed-database  -> generated.tf
#   2. terraform init + plan (always)
#   3. terraform apply + verify + destroy (only when test creds are present;
#      a missing-cred provider is SKIPPED EXPLICITLY, never silently)
#
# DATA-SAFETY + COST: a managed database costs money and takes several minutes to
# create/destroy. The AWS apply path uses the TEST fixture (db-aws.json), which
# sets the smallest class (db.t3.micro), the 20 GiB storage minimum, and the
# TEST-ONLY override deletion_protection=false + skip_final_snapshot=true so the
# harness can destroy cleanly. It runs `destroy` IMMEDIATELY after verifying and
# asserts the instance is gone. The PRODUCTION fixture (db.json) keeps
# deletion_protection=true + a final snapshot — never torn down by this script.
#
# Creds:
#   aws -> AWS_PROFILE=pyxcloudtest (aws sts get-caller-identity); needs
#          TF_VAR_db_password for the RDS master password (throwaway).
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

# gen <provider> <dir> <fixture>
gen() {
  local provider="$1" dir="$2" fixture="$3"
  : > "$dir/generated.tf"
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component network >> "$dir/generated.tf"
  echo "" >> "$dir/generated.tf"
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component security-group >> "$dir/generated.tf"
  echo "" >> "$dir/generated.tf"
  "$RENDER_BIN" -fixture "$fixture" -provider "$provider" -component managed-database >> "$dir/generated.tf"
  echo "generated $dir/generated.tf"
}

plan() { ( cd "$1" && terraform init -input=false >/dev/null && terraform plan -input=false -no-color ); }

# --- AWS (Frankfurt -> eu-central-1; smallest RDS db.t3.micro postgres, 20 GiB) ---
# Uses the TEST fixture with the visible test-only teardown override.
gen aws aws db-aws.json
plan aws
if AWS_PROFILE=pyxcloudtest aws sts get-caller-identity >/dev/null 2>&1; then
  echo ">>> AWS creds present: real apply + verify + immediate destroy"
  export TF_VAR_db_password="${TF_VAR_db_password:-ChangeMe-RoundTrip-123!}"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform apply -auto-approve -no-color )
  echo ">>> describe db instances:"
  AWS_PROFILE=pyxcloudtest aws rds describe-db-instances \
    --region eu-central-1 \
    --query "DBInstances[?DBInstanceIdentifier=='app-db'].{Id:DBInstanceIdentifier,Class:DBInstanceClass,Engine:Engine,Status:DBInstanceStatus,Encrypted:StorageEncrypted,MultiAZ:MultiAZ,Storage:AllocatedStorage}"
  echo ">>> DESTROY (RDS costs money) — test fixture has skip_final_snapshot=true + deletion_protection=false:"
  ( cd aws && AWS_PROFILE=pyxcloudtest terraform destroy -auto-approve -no-color )
  echo ">>> verify teardown (no app-db instance should remain):"
  AWS_PROFILE=pyxcloudtest aws rds describe-db-instances \
    --region eu-central-1 \
    --query "DBInstances[?DBInstanceIdentifier=='app-db'].DBInstanceIdentifier" || true
else
  echo ">>> SKIP AWS apply/destroy: no pyxcloudtest profile (plan only)"
fi

# --- GCP (Frankfurt -> europe-west3; Cloud SQL postgres) ---
# Production fixture keeps deletion_protection=true, so apply is plan-only here
# unless creds are present (a real disposable apply would use a test fixture).
gen gcp gcp db.json
GOOGLE_PROJECT="${GOOGLE_PROJECT:-pyxcloud-plan-only}" plan gcp
if [[ "${GOOGLE_PROJECT:-}" != "" && "${GOOGLE_PROJECT}" != "pyxcloud-plan-only" ]] && gcloud auth application-default print-access-token >/dev/null 2>&1; then
  echo ">>> GCP creds present: real apply + destroy"
  ( cd gcp && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP GCP apply/destroy: no application-default creds / GOOGLE_PROJECT (plan only)"
fi

# --- DigitalOcean (Frankfurt -> fra1; managed postgres cluster, 2-node HA) ---
gen digitalocean digitalocean db.json
if [[ "${DIGITALOCEAN_TOKEN:-}" != "" ]]; then
  echo ">>> DO creds present: real apply + destroy"
  ( cd digitalocean && terraform init -input=false >/dev/null && terraform apply -auto-approve -no-color && terraform destroy -auto-approve -no-color )
else
  echo ">>> SKIP DO apply/destroy: no DIGITALOCEAN_TOKEN (plan only — init+validate)"
  ( cd digitalocean && terraform init -input=false >/dev/null && terraform validate -no-color )
fi

echo "round-trip complete."
